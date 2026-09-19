package bots

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/auth"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/throttle"
)

const (
	pairingTTL        = 10 * time.Minute
	maxActiveCodes    = 5
	unlinkedNoticeGap = time.Hour
)

// pairingPolicy: four wrong guesses are free, then delays grow (SEC-BOT-4). With 40 bits of
// code entropy and a 10 minute lifetime, guessing is hopeless either way.
var pairingPolicy = throttle.Policy{Free: 4, Max: 15 * time.Minute}

// PairingCode is a freshly created code, shown once.
type PairingCode struct {
	Code    string // XXXX-XXXX
	Expires time.Time
}

// CreatePairingCode makes a short-lived one-time code the user sends to a bot as `!link CODE`
// (AUTH-B3). At most five are active per user. If instance is non-nil the code only works for that instance.
func (s *Service) CreatePairingCode(ctx context.Context, user uuid.UUID, botType string, instance *uuid.UUID) (PairingCode, error) {
	now := s.Now()
	code := auth.NewPairingCode()
	id, _ := uuid.NewV7()
	expires := now.Add(pairingTTL)
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		if botType == "" && instance != nil {
			inst, err := q.GetBotInstance(ctx, *instance)
			if err != nil {
				return fmt.Errorf("%w: unknown bot", ErrInvalidInput)
			}
			botType = inst.Type
		}
		if botType == "" {
			return fmt.Errorf("%w: bot type", ErrInvalidInput)
		}
		n, err := q.CountActivePairingCodes(ctx, dbq.CountActivePairingCodesParams{UserID: user, ExpiresAt: now})
		if err != nil {
			return err
		}
		if n >= maxActiveCodes {
			return ErrConflict
		}
		var inst uuid.NullUUID
		if instance != nil {
			inst = uuid.NullUUID{UUID: *instance, Valid: true}
		}
		return q.InsertPairingCode(ctx, dbq.InsertPairingCodeParams{ID: id, UserID: user, CodeHash: auth.HashToken(code),
			BotInstanceID: inst, BotType: botType, ExpiresAt: expires})
	})
	return PairingCode{Code: code, Expires: expires}, err
}

// LinkOutcome says what happened to a link attempt; Core turns it into wording for the chat.
type LinkOutcome string

// Link outcomes.
const (
	Linked          LinkOutcome = "linked"
	LinkInvalidCode LinkOutcome = "invalid_code"
	LinkAlready     LinkOutcome = "already_linked"
	LinkWrongDomain LinkOutcome = "wrong_domain"
	LinkThrottled   LinkOutcome = "throttled"
)

// Link redeems a pairing code for a chat identity taken from the platform's verified event
// metadata (SEC-BOT-5). The identity must belong to the instance's domain, attempts are
// throttled per (instance, identity), and the code is consumed atomically. An identity that is
// already linked, to anyone, is refused rather than moved (AUTH-B4, SEC-BOT-6).
func (s *Service) Link(ctx context.Context, bot *Principal, externalUser, conversation, rawCode string) (LinkOutcome, uuid.UUID, error) {
	key := "pair:" + bot.InstanceID.String() + ":" + strings.ToLower(externalUser)
	if wait, err := s.throttle.Blocked(ctx, key); err != nil {
		return "", uuid.Nil, err
	} else if wait > 0 {
		return LinkThrottled, uuid.Nil, nil
	}
	if !IdentityInDomain(bot.Type, externalUser, bot.IdentityDomain) {
		return LinkWrongDomain, uuid.Nil, nil
	}
	fail := func(o LinkOutcome) (LinkOutcome, uuid.UUID, error) {
		_ = s.throttle.Fail(ctx, key, pairingPolicy)
		return o, uuid.Nil, nil
	}
	code := auth.NormalisePairingCode(rawCode)
	if code == "" {
		return fail(LinkInvalidCode)
	}
	now := s.Now()
	pc, err := s.St.Q().LookupPairingCode(ctx, dbq.LookupPairingCodeParams{CodeHash: auth.HashToken(code), ExpiresAt: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(LinkInvalidCode)
	}
	if err != nil {
		return "", uuid.Nil, err
	}
	if pc.BotType != bot.Type || (pc.BotInstanceID.Valid && pc.BotInstanceID.UUID != bot.InstanceID) {
		return fail(LinkInvalidCode)
	}
	if _, err := s.St.Q().GetIdentityByExternal(ctx, dbq.GetIdentityByExternalParams{BotType: bot.Type, ExternalUserID: externalUser}); err == nil {
		return LinkAlready, uuid.Nil, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", uuid.Nil, err
	}

	var identity uuid.UUID
	outcome := Linked
	err = s.St.InUserTx(ctx, pc.UserID, func(tx *store.UserTx) error {
		if tx.Status != "active" {
			outcome = LinkInvalidCode
			return nil
		}
		if _, err := tx.Q.ConsumePairingCode(ctx, dbq.ConsumePairingCodeParams{ID: pc.ID, UsedAt: &now}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				outcome = LinkInvalidCode // lost a race for the same code
				return nil
			}
			return err
		}
		existing, err := tx.Q.CountUserIdentities(ctx, tx.UserID)
		if err != nil {
			return err
		}
		identity, _ = uuid.NewV7()
		conv := &conversation
		if conversation == "" {
			conv = nil
		}
		_, err = tx.Q.InsertIdentity(ctx, dbq.InsertIdentityParams{ID: identity, UserID: tx.UserID, BotInstanceID: bot.InstanceID,
			BotType: bot.Type, ExternalUserID: externalUser, ConversationID: conv, ReminderTarget: existing == 0, LinkedAt: now})
		if store.IsUniqueViolation(err, "identities_unique") {
			outcome = LinkAlready
			return errAbort
		}
		if err != nil {
			return err
		}
		if err := tx.Change(ctx, "identity", identity, "upsert", nil); err != nil {
			return err
		}
		return store.Audit(ctx, tx.Q, store.AuditEntry{ActorKind: "bot", ActorID: &bot.InstanceID, Action: "identity.linked",
			TargetKind: "user", TargetID: &tx.UserID})
	})
	if errors.Is(err, errAbort) {
		return outcome, uuid.Nil, nil
	}
	if err != nil {
		return "", uuid.Nil, err
	}
	if outcome != Linked {
		return fail(outcome)
	}
	_ = s.throttle.Reset(ctx, key)
	return Linked, identity, nil
}

var errAbort = errors.New("abort")

// IdentityInDomain reports whether an external identity belongs to the bot instance's domain
// (SEC-BOT-5). For Matrix the domain is the homeserver part of "@user:server".
func IdentityInDomain(botType, external, domain string) bool {
	if domain == "" {
		return true
	}
	if botType == "matrix" {
		_, server, ok := strings.Cut(external, ":")
		return ok && strings.EqualFold(server, domain)
	}
	return true
}

// Identity is a linked chat identity as the bot API and the app see it.
type Identity struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	BotInstanceID  uuid.UUID
	ExternalUserID string
	ConversationID *string
	ReminderTarget bool
	LinkedAt       time.Time
}

// ResolveIdentity finds the linked identity for a sender, or reports that there is none.
func (s *Service) ResolveIdentity(ctx context.Context, bot *Principal, externalUser string) (Identity, bool, error) {
	row, err := s.St.Q().GetIdentityByExternal(ctx, dbq.GetIdentityByExternalParams{BotType: bot.Type, ExternalUserID: externalUser})
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	return Identity{ID: row.ID, UserID: row.UserID, BotInstanceID: row.BotInstanceID, ExternalUserID: row.ExternalUserID,
		ConversationID: row.ConversationID, ReminderTarget: row.ReminderTarget, LinkedAt: row.LinkedAt}, true, nil
}

// UnlinkNoticeDue records that an unlinked sender was just told how to link, and reports whether
// that reply should be sent (at most once per hour per sender, AUTH-B6).
func (s *Service) UnlinkNoticeDue(ctx context.Context, bot *Principal, externalUser string) (bool, error) {
	now := s.Now()
	_, err := s.St.Q().NoticeDue(ctx, dbq.NoticeDueParams{BotInstanceID: bot.InstanceID, ExternalUserID: externalUser,
		LastNoticeAt: now, LastNoticeAt_2: now.Add(-unlinkedNoticeGap)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Unlink removes a linked identity of a user and queues a lifecycle item so the bot forgets the
// conversation (BOT-16, AUTH-B5). Ingest for the identity is rejected from this moment on (SEC-BOT-7).
func (s *Service) Unlink(ctx context.Context, actor Actor, user, identity uuid.UUID) error {
	return s.St.InUserTx(ctx, user, func(tx *store.UserTx) error {
		row, err := tx.Q.GetIdentity(ctx, identity)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.UserID != user) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Q.DeleteIdentity(ctx, dbq.DeleteIdentityParams{ID: identity, UserID: user}); err != nil {
			return err
		}
		if row.ConversationID != nil {
			payload, _ := json.Marshal(map[string]string{"reason": "unlinked"})
			oid, _ := uuid.NewV7()
			// A lifecycle item names no user: it must outlive the account (AUTH-U9).
			if err := tx.Q.InsertOutbox(ctx, dbq.InsertOutboxParams{ID: oid, BotInstanceID: row.BotInstanceID, Kind: "lifecycle",
				ExternalUserID: row.ExternalUserID, ConversationID: *row.ConversationID, Payload: payload, NextAttemptAt: s.Now()}); err != nil {
				return err
			}
			if err := tx.Notify(ctx, store.ChannelOutbox, row.BotInstanceID.String()); err != nil {
				return err
			}
		}
		if err := tx.Change(ctx, "identity", identity, "delete", nil); err != nil {
			return err
		}
		return store.Audit(ctx, tx.Q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "identity.unlinked",
			TargetKind: "user", TargetID: &user})
	})
}

// ListIdentities lists the user's linked identities.
func (s *Service) ListIdentities(ctx context.Context, user uuid.UUID) ([]dbq.ListUserIdentitiesRow, error) {
	return s.St.Q().ListUserIdentities(ctx, user)
}
