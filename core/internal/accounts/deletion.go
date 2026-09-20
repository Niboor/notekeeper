package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// ErrProtected is returned when the account may not be deleted (the admin's).
var ErrProtected = errors.New("the admin account cannot be deleted")

const (
	chunkBatch = 200  // attachment chunks per transaction
	noteBatch  = 200  // notes per transaction (each takes its parts, history and reminders)
	rowBatch   = 5000 // change-feed and ingest rows per transaction
)

// RequestDeletion starts the complete deletion of an account (AUTH-U4, AUTH-U9). From this moment the
// account cannot sign in, ingest, fire reminders or serve share links, because all of those check the
// status live; sessions and tokens are revoked, and every bot instance that served the user is told
// to forget their chats (BOT-16). The data itself is removed by ProcessDeletions, in steps that can
// be repeated after a crash.
func (s *Service) RequestDeletion(ctx context.Context, actor Actor, userID uuid.UUID) error {
	var instances map[uuid.UUID]bool
	err := s.St.InTx(ctx, func(q *dbq.Queries) error {
		u, err := q.GetUser(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if u.IsAdmin {
			return ErrProtected
		}
		if u.Status == "deleting" {
			return nil // already on its way
		}
		if err := q.SetUserStatus(ctx, dbq.SetUserStatusParams{ID: userID, Status: "deleting", UpdatedAt: s.Now()}); err != nil {
			return err
		}
		if err := s.revokeAll(ctx, q, userID, nil, "user_deleted"); err != nil {
			return err
		}
		idents, err := q.IdentitiesForDeletion(ctx, userID)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]string{"reason": "user_deleted"})
		if err != nil {
			return err
		}
		instances = map[uuid.UUID]bool{}
		for _, i := range idents {
			id, err := uuid.NewV7()
			if err != nil {
				return err
			}
			// A lifecycle item names no user: it must outlive the account (AUTH-U9).
			if err := q.InsertOutbox(ctx, dbq.InsertOutboxParams{ID: id, BotInstanceID: i.BotInstanceID, Kind: "lifecycle",
				ExternalUserID: i.ExternalUserID, ConversationID: *i.ConversationID, Payload: payload, NextAttemptAt: s.Now()}); err != nil {
				return err
			}
			instances[i.BotInstanceID] = true
		}
		return store.Audit(ctx, q, store.AuditEntry{ActorKind: actor.Kind, ActorID: actor.ID, Action: "user.deletion_requested", TargetKind: "user", TargetID: &userID})
	})
	if err != nil {
		return err
	}
	for inst := range instances {
		_ = s.St.Notify(ctx, store.ChannelOutbox, inst.String()) // the bot's poll would find it anyway
	}
	return nil
}

// ProcessDeletions removes the data of accounts that are being deleted and returns how many accounts
// it finished. Attachment chunks go first, in small transactions, so a large account never holds one
// long transaction; then the account row goes, and every table that references it (notes, parts,
// history, search entries, reminders, notifications, share links, sessions, tokens, pairing codes,
// identity links, ingest records and undelivered outbox items) goes with it through the foreign keys.
// Afterwards only a content-free audit entry remains (AUTH-U9, SEC-DATA-5).
func (s *Service) ProcessDeletions(ctx context.Context) (int, error) {
	ids, err := s.St.Q().ListDeletingUsers(ctx)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		for {
			var n int64
			err := s.St.InUserScoped(ctx, id, func(q *dbq.Queries) error {
				var err error
				n, err = q.DeleteBlobChunksBatch(ctx, dbq.DeleteBlobChunksBatchParams{UserID: id, Limit: chunkBatch})
				return err
			})
			if err != nil {
				return done, fmt.Errorf("delete attachment data: %w", err)
			}
			if n == 0 {
				break
			}
		}
		// Notes (with their parts, history, reminders and search rows), then the change feed and the
		// ingest records, each in bounded batches; only then the files and the account row, which takes
		// what is left (sessions, tokens, identities, share links, notifications, outbox items) with it.
		// The user's context is set even now: deleting parts fires the search index trigger, which
		// insists on knowing whose parts they are (migration 0005).
		for _, step := range []struct {
			name string
			run  func(q *dbq.Queries) (int64, error)
		}{
			{"notes", func(q *dbq.Queries) (int64, error) {
				return q.DeleteUserNotesBatch(ctx, dbq.DeleteUserNotesBatchParams{UserID: id, MaxRows: noteBatch})
			}},
			{"change feed", func(q *dbq.Queries) (int64, error) {
				return q.DeleteUserChangesBatch(ctx, dbq.DeleteUserChangesBatchParams{UserID: id, MaxRows: rowBatch})
			}},
			{"ingest records", func(q *dbq.Queries) (int64, error) {
				return q.DeleteUserIngestEventsBatch(ctx, dbq.DeleteUserIngestEventsBatchParams{UserID: id, MaxRows: rowBatch})
			}},
		} {
			for {
				var n int64
				if err := s.St.InUserScoped(ctx, id, func(q *dbq.Queries) (err error) { n, err = step.run(q); return err }); err != nil {
					return done, fmt.Errorf("delete %s: %w", step.name, err)
				}
				if n == 0 {
					break
				}
			}
		}
		err := s.St.InUserScoped(ctx, id, func(q *dbq.Queries) error {
			if _, err := q.DeleteUserNoteParts(ctx, id); err != nil { // none left, unless a note came in meanwhile
				return err
			}
			if _, err := q.DeleteUserAttachments(ctx, id); err != nil {
				return err
			}
			n, err := q.DeleteUserRow(ctx, id)
			if err != nil || n == 0 {
				return err
			}
			return store.Audit(ctx, q, store.AuditEntry{ActorKind: "system", Action: "user.deleted", TargetKind: "user", TargetID: &id})
		})
		if err != nil {
			return done, fmt.Errorf("delete account: %w", err)
		}
		done++
	}
	return done, nil
}
