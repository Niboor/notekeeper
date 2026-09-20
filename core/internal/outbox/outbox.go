// Package outbox is the Core-to-bot channel (docs/design/05-realtime-and-jobs.md section 4): items
// addressed to one bot instance are claimed with a lease, sent by the bot, and acknowledged. It
// carries lifecycle notices (forget this chat), security notices and reminders. Delivery is
// at-least-once; the item id is the bot's idempotency key for the platform send (BOT-13).
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Lease is how long a claimed item is reserved for the bot before it is offered again.
const Lease = 60 * time.Second

// ExpireAfter is how long an item may wait for delivery before it is given up (docs/design/05 §4.4).
const ExpireAfter = 7 * 24 * time.Hour

// Errors.
var (
	ErrNotFound = store.ErrNotFound
	ErrInvalid  = errors.New("invalid outbox result")
)

// Service manages the outbox.
type Service struct {
	St  *store.Store
	Now func() time.Time
}

// New creates the service.
func New(st *store.Store) *Service { return &Service{St: st, Now: time.Now} }

// Item is an outbox item as handed to a bot.
type Item struct {
	ID             uuid.UUID
	Kind           string
	ExternalUserID string
	ConversationID string
	Payload        map[string]any
	Attempts       int32
	DueAt          *time.Time
}

// Claim reserves up to limit items for a bot instance. An item whose lease expired (the bot died
// before acknowledging) is offered again and does not count as an attempt, so a bot outage never
// exhausts the retries (CORE-R4). Items of disabled users are skipped (SEC-BOT-13).
func (s *Service) Claim(ctx context.Context, instance uuid.UUID, limit int) ([]Item, error) {
	now := s.Now()
	rows, err := s.St.Q().ClaimOutbox(ctx, dbq.ClaimOutboxParams{Lease: now.Add(Lease), Instance: instance, Now: now, MaxItems: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Item, len(rows))
	for i, r := range rows {
		payload := map[string]any{}
		_ = json.Unmarshal(r.Payload, &payload)
		out[i] = Item{ID: r.ID, Kind: r.Kind, ExternalUserID: r.ExternalUserID, ConversationID: r.ConversationID, Payload: payload, Attempts: r.Attempts, DueAt: r.DueAt}
	}
	return out, nil
}

// Result is what a bot reports about a claimed item.
type Result struct {
	State      string // delivered | failed_transient | failed_permanent
	Reason     string
	MessageIDs []string
}

var backoff = []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

func retryDelay(attempts int32) time.Duration {
	if int(attempts) < len(backoff) {
		return backoff[attempts]
	}
	return time.Hour
}

// Report records the outcome of a claimed item. Only the instance the item is addressed to, and
// only while the item is claimed, can report on it (SEC-BOT-1).
func (s *Service) Report(ctx context.Context, instance, id uuid.UUID, r Result) error {
	now := s.Now()
	item, err := s.St.Q().GetClaimedOutbox(ctx, dbq.GetClaimedOutboxParams{ID: id, BotInstanceID: instance})
	if err != nil {
		return ErrNotFound
	}
	reason := r.Reason
	switch r.State {
	case "delivered":
		if n, err := s.St.Q().MarkOutboxDelivered(ctx, dbq.MarkOutboxDeliveredParams{ID: id, BotInstanceID: instance, FinishedAt: &now}); err != nil || n == 0 {
			return orNotFound(err)
		}
		for _, m := range r.MessageIDs {
			// Remember which chat messages were ours, so "!snooze" and "!done" as replies can find their reminder (BOT-13).
			if err := s.St.Q().InsertOutboxMessage(ctx, dbq.InsertOutboxMessageParams{BotInstanceID: instance, ConversationID: item.ConversationID, MessageID: m, OutboxID: id}); err != nil {
				return err
			}
		}
		return nil
	case "failed_transient":
		next := now.Add(retryDelay(item.Attempts))
		n, err := s.St.Q().RequeueOutbox(ctx, dbq.RequeueOutboxParams{ID: id, BotInstanceID: instance, NextAttemptAt: next, FailureReason: &reason})
		if err != nil || n == 0 {
			return orNotFound(err)
		}
		return nil
	case "failed_permanent":
		n, err := s.St.Q().FailOutbox(ctx, dbq.FailOutboxParams{ID: id, BotInstanceID: instance, FinishedAt: &now, FailureReason: &reason})
		if err != nil || n == 0 {
			return orNotFound(err)
		}
		return s.notifyFailure(ctx, item, "delivery_failed")
	}
	return ErrInvalid
}

func orNotFound(err error) error {
	if err != nil {
		return err
	}
	return ErrNotFound
}

// notifyFailure tells the user in the app that a message meant for their chat could not be
// delivered, so a reminder that never reaches the chat still reaches them (CORE-R9, docs/design/05 §4.4).
func (s *Service) notifyFailure(ctx context.Context, item dbq.BotOutbox, kind string) error {
	if !item.UserID.Valid || item.Kind == "lifecycle" {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"outbox_kind": item.Kind, "outbox_id": item.ID})
	return s.St.InUserTx(ctx, item.UserID.UUID, func(tx *store.UserTx) error {
		id, _ := uuid.NewV7()
		if err := tx.Q.InsertNotification(ctx, dbq.InsertNotificationParams{ID: id, UserID: tx.UserID, Kind: kind, Payload: payload}); err != nil {
			return err
		}
		return tx.Change(ctx, "notification", id, "upsert", nil)
	})
}

// Expire gives up on items that waited longer than ExpireAfter and tells their users (docs/design/05 §4.4).
func (s *Service) Expire(ctx context.Context) error {
	now := s.Now()
	rows, err := s.St.Q().StaleOutbox(ctx, now.Add(-ExpireAfter))
	if err != nil {
		return err
	}
	for _, r := range rows {
		n, err := s.St.Q().ExpireOutboxItem(ctx, dbq.ExpireOutboxItemParams{ID: r.ID, FinishedAt: &now})
		if err != nil {
			return err
		}
		if n == 1 && r.UserID.Valid && r.Kind != "lifecycle" {
			if err := s.notifyFailure(ctx, dbq.BotOutbox{ID: r.ID, UserID: r.UserID, Kind: r.Kind}, "delivery_failed"); err != nil {
				return err
			}
		}
	}
	return nil
}

// AttachmentOwner says whose file a bot may fetch for a claimed item, or ErrNotFound. The item must
// be claimed by this bot instance and must list the file in its payload (BOT-15, SEC-BOT-3).
func (s *Service) AttachmentOwner(ctx context.Context, instance, item, attachment uuid.UUID) (uuid.UUID, error) {
	row, err := s.St.Q().GetClaimedOutbox(ctx, dbq.GetClaimedOutboxParams{ID: item, BotInstanceID: instance})
	if err != nil || !row.UserID.Valid {
		return uuid.Nil, ErrNotFound
	}
	var p struct {
		Attachments []struct {
			ID uuid.UUID `json:"id"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(row.Payload, &p); err != nil {
		return uuid.Nil, ErrNotFound
	}
	for _, a := range p.Attachments {
		if a.ID == attachment {
			return row.UserID.UUID, nil
		}
	}
	return uuid.Nil, ErrNotFound
}
