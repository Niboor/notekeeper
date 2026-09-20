// Package notify tells a user something outside a request they are making: in the app (a
// notification) and in every chat they linked (a notice on the bot outbox). It carries security
// notices (AUTH-U11) and is the one place that renders that wording (BOT-8). Everything runs
// inside the caller's transaction, so a notice exists exactly when the change it reports does.
package notify

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Notification kinds.
const (
	KindSecurity       = "security"
	KindReminder       = "reminder"
	KindDeliveryFailed = "delivery_failed"
)

// Notices a user may mute (AUTH-U11). The others cannot be muted.
const (
	MuteSession = "session"
	MuteShare   = "share"
)

// muted reports whether the user muted a kind of notice. Notices without a mute key cannot be muted.
func muted(settings []byte, key string) bool {
	if key == "" {
		return false
	}
	var s struct {
		Muted []string `json:"muted_notices"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		return false
	}
	return slices.Contains(s.Muted, key)
}

// Security records a security notice for the user: a notification in the app and a notice in each
// linked chat. muteKey names the setting that silences it, or is empty for notices that cannot be
// muted (password changes, chats linked or unlinked, activation links).
func Security(ctx context.Context, tx *store.UserTx, now time.Time, muteKey, text string) error {
	u, err := tx.Q.GetUser(ctx, tx.UserID)
	if err != nil {
		return err
	}
	if muted(u.Settings, muteKey) {
		return nil
	}
	payload, err := json.Marshal(map[string]any{"text": text})
	if err != nil {
		return err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if err := tx.Q.InsertNotification(ctx, dbq.InsertNotificationParams{ID: id, UserID: tx.UserID, Kind: KindSecurity, Payload: payload}); err != nil {
		return err
	}
	if err := tx.Change(ctx, "notification", id, "upsert", nil); err != nil {
		return err
	}
	targets, err := tx.Q.NoticeTargets(ctx, tx.UserID)
	if err != nil {
		return err
	}
	instances := map[uuid.UUID]bool{}
	for _, t := range targets {
		oid, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if err := tx.Q.InsertOutbox(ctx, dbq.InsertOutboxParams{ID: oid, BotInstanceID: t.BotInstanceID, Kind: "notice", UserID: uuid.NullUUID{UUID: tx.UserID, Valid: true},
			ExternalUserID: t.ExternalUserID, ConversationID: *t.ConversationID, Payload: payload, NextAttemptAt: now}); err != nil {
			return err
		}
		instances[t.BotInstanceID] = true
	}
	for inst := range instances {
		if err := tx.Notify(ctx, store.ChannelOutbox, inst.String()); err != nil {
			return err
		}
	}
	return nil
}
