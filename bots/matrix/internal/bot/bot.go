// Package bot is the Matrix bot: it joins direct messages, hands what people write to Core, and
// shows Core's feedback in the chat. It contains platform logic only (docs/design/06-matrix-bot.md).
package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/Niboor/notekeeper/bots/matrix/internal/config"
	"github.com/Niboor/notekeeper/bots/matrix/internal/normalise"
	"github.com/Niboor/notekeeper/bots/matrix/internal/syncack"
	"github.com/Niboor/notekeeper/bots/sdk"
	"github.com/Niboor/notekeeper/bots/sdk/botclient"
)

const (
	maxInvitesPerMinute = 10
	ignoredNotice       = "This room has more than two members, so I am ignoring it. Notekeeper only works in direct messages between you and me."
)

// Bot is one running Matrix bot.
type Bot struct {
	cfg  config.Config
	log  *slog.Logger
	core *sdk.Client
	met  *metrics

	client *mautrix.Client
	helper *cryptohelper.CryptoHelper
	db     *dbutil.Database
	rooms  roomMemory

	lastSync atomic.Int64 // unix nanoseconds of the last fully processed sync response

	// history and cursor, if set, replace the homeserver and Core in gap filling (tests).
	history historyClient
	cursor  cursorSource

	// LookupBackoff, if set, replaces the wait between attempts to list a room's members (tests).
	LookupBackoff func(attempt int) time.Duration

	mu            sync.Mutex
	dmCache       map[id.RoomID]bool
	invites       []time.Time
	undecryptable map[id.RoomID]time.Time
}

// New creates a bot. Call Start, then Sync.
func New(cfg config.Config, log *slog.Logger, core *sdk.Client) *Bot {
	b := &Bot{cfg: cfg, log: log, core: core, met: newMetrics(), dmCache: map[id.RoomID]bool{}, undecryptable: map[id.RoomID]time.Time{}}
	// Lets an alert tell "the bot is up but stuck" (a message it cannot get through, a homeserver that
	// stopped answering) from "the bot is running": a standby replica reports 0.
	b.met.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "nk_bot_last_sync_timestamp_seconds", Help: "Unix time of the last fully processed sync response, 0 before the first.",
	}, func() float64 {
		if t := b.LastSync(); !t.IsZero() {
			return float64(t.Unix())
		}
		return 0
	}))
	return b
}

// LastSync returns when the last sync response was processed, or the zero time.
func (b *Bot) LastSync() time.Time {
	n := b.lastSync.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Start logs in, opens the crypto store in PostgreSQL and registers handlers. The device is
// created on first start and reused afterwards: the crypto helper stores its device id and logs
// in as that same device (verified in the M0 spike).
func (b *Bot) Start(ctx context.Context) error {
	client, err := mautrix.NewClient(b.cfg.Homeserver, "", "")
	if err != nil {
		return err
	}
	client.Log = zerolog.Nop() // our own structured logging; mautrix logs may contain event content

	ackStore := syncack.NewStore(nil)
	syncer := syncack.NewSyncer(ackStore, func() id.UserID { return client.UserID })
	syncer.OnProcessed = func() { b.lastSync.Store(time.Now().UnixNano()) }
	client.Syncer = syncer

	db, err := dbutil.NewWithDialect(b.cfg.DatabaseURL, "pgx")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	b.db = db
	rooms := &roomStore{db: db}
	b.rooms = rooms
	if err := rooms.init(ctx); err != nil {
		return fmt.Errorf("prepare room table: %w", err)
	}

	helper, err := cryptohelper.NewCryptoHelper(client, b.cfg.PickleKey, db)
	if err != nil {
		return fmt.Errorf("crypto helper: %w", err)
	}
	user := b.cfg.User
	helper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: user},
		Password:   b.cfg.Password,
	}
	helper.DecryptErrorCallback = func(evt *event.Event, err error) {
		// Never log the event body; identifiers are enough to investigate (SEC-DATA-1).
		b.met.decryptFailures.Inc()
		b.log.Warn("could not decrypt an event", "room", evt.RoomID, "event", evt.ID, "error", err)
		b.tellUndecryptable(evt)
	}
	if err := helper.Init(ctx); err != nil {
		return fmt.Errorf("initialise encryption: %w", err)
	}
	client.Crypto = helper
	// The crypto helper installed its PostgreSQL-backed store; defer its sync token saves.
	ackStore.SyncStore = client.Store
	client.Store = ackStore
	b.client, b.helper, b.db = client, helper, db

	syncer.OnSync(b.backfillGaps)
	syncer.OnEventType(event.EventMessage, b.onMessage)
	syncer.OnEventType(event.EventRedaction, b.onRedaction)
	syncer.OnEventType(event.StateMember, b.onMember)
	b.log.Info("matrix bot started", "user", client.UserID, "device", client.DeviceID)
	return nil
}

// Sync runs the sync loop until ctx ends or a handler failed. The loop is synchronous with Core:
// an event is handled only after the previous one, and the sync token advances only after the
// whole batch was handled (package syncack), so a Core outage stalls the bot rather than losing
// messages (NFR-R1).
func (b *Bot) Sync(ctx context.Context) error {
	err := b.client.SyncWithContext(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// Close stops using the crypto database.
func (b *Bot) Close() error {
	if b.helper != nil {
		return b.helper.Close()
	}
	return nil
}

// Heartbeat reports liveness to Core until ctx ends (WEB-12).
func (b *Bot) Heartbeat(ctx context.Context) {
	t := time.NewTicker(b.cfg.HeartbeatEvery)
	defer t.Stop()
	for {
		if err := b.core.Heartbeat(ctx); err != nil && ctx.Err() == nil {
			b.log.Warn("heartbeat failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// requestID derives a short id from a Matrix event id so one message can be followed through the
// logs of the bot and Core (NFR-O1). Core only accepts letters, digits, '-' and '_'.
func requestID(evt id.EventID) string {
	sum := sha256.Sum256([]byte(evt))
	return "mx-" + hex.EncodeToString(sum[:8])
}

func (b *Bot) onMessage(ctx context.Context, evt *event.Event) {
	if evt.Sender == b.client.UserID {
		return
	}
	if !b.allowedOrStall(ctx, evt.RoomID) {
		return
	}
	b.handle(ctx, evt, normalise.Message(evt))
}

func (b *Bot) onRedaction(ctx context.Context, evt *event.Event) {
	if evt.Sender == b.client.UserID || !b.allowedOrStall(ctx, evt.RoomID) {
		return
	}
	b.handle(ctx, evt, normalise.Redaction(evt))
}

// handle sends a normalised event or command to Core and shows the feedback. It returns only
// when Core has answered, the request was refused for good, or the context ended.
func (b *Bot) handle(ctx context.Context, evt *event.Event, res normalise.Result) {
	ctx = sdk.WithRequestID(ctx, requestID(evt.ID))
	switch res.Kind {
	case normalise.KindEvent:
		b.fetchMedia(ctx, evt, &res)
		out, err := b.core.PostEvent(ctx, *res.Event)
		if b.failed(ctx, evt, "event", err) {
			return
		}
		b.met.events.WithLabelValues(string(out.Result)).Inc()
		b.feedback(ctx, evt, out.Feedback)
	case normalise.KindCommand:
		out, err := b.core.PostCommand(ctx, *res.Command)
		if b.failed(ctx, evt, "command", err) {
			return
		}
		b.met.events.WithLabelValues("command").Inc()
		b.feedback(ctx, evt, out.Feedback)
	}
}

// failed reports whether err ended the handling. A refusal that cannot be fixed by retrying is
// counted and skipped, so one bad event never blocks the bot forever; a cancelled context means
// we are shutting down and the batch will be replayed from the last committed token.
func (b *Bot) failed(ctx context.Context, evt *event.Event, what string, err error) bool {
	if err == nil {
		return false
	}
	var perm *sdk.PermanentError
	switch {
	case errors.As(err, &perm):
		b.met.events.WithLabelValues("refused").Inc()
		b.log.Error("core refused a "+what, "room", evt.RoomID, "event", evt.ID, "status", perm.Status, "code", perm.Code)
		// Panic-free but visible: the user gets a generic reply so a silent failure is impossible.
		b.sendNotice(ctx, evt.RoomID, "Something went wrong saving that message. It was not saved.")
	case ctx.Err() != nil:
		// shutting down
	default:
		// Not reachable: PostEvent only returns on success, permanent failure or a dead context.
		b.log.Error("core call failed", "error", err)
	}
	return true
}

// feedback shows what Core asked for: a reaction on the source message and/or a reply (BOT-8).
// Feedback problems are logged, never retried: the message is already safe in Core.
func (b *Bot) feedback(ctx context.Context, evt *event.Event, fb botclient.Feedback) {
	if fb.React != nil && *fb.React != "" {
		emoji := "✅"
		if *fb.React != "ok" {
			emoji = *fb.React
		}
		if _, err := b.client.SendReaction(ctx, evt.RoomID, evt.ID, emoji); err != nil {
			b.log.Warn("could not react", "room", evt.RoomID, "event", evt.ID, "error", err)
		}
	}
	if fb.ReplyText != nil && *fb.ReplyText != "" {
		b.sendNotice(ctx, evt.RoomID, *fb.ReplyText)
	}
}

func (b *Bot) sendNotice(ctx context.Context, room id.RoomID, text string) {
	if _, err := b.client.SendNotice(ctx, room, text); err != nil {
		b.log.Warn("could not send a reply", "room", room, "error", err)
	}
}

const (
	undecryptableNotice = "I could not read your last message: the encryption keys did not arrive, so it was not saved. Please send it again."
	undecryptableEvery  = 10 * time.Minute
)

// tellUndecryptable answers a message that could not be decrypted, once every few minutes per room,
// so the user knows to send it again instead of wondering why the note never appeared (CR-004). The
// crypto helper has already waited for the keys and asked the sender's devices for them.
func (b *Bot) tellUndecryptable(evt *event.Event) {
	if evt.Sender == b.client.UserID {
		return
	}
	b.mu.Lock()
	last := b.undecryptable[evt.RoomID]
	due := time.Since(last) >= undecryptableEvery
	if due {
		b.undecryptable[evt.RoomID] = time.Now()
	}
	b.mu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if b.roomAllowed(ctx, evt.RoomID) {
		b.sendNotice(ctx, evt.RoomID, undecryptableNotice)
	}
}

// UserID is the bot's Matrix user id (known after Start).
func (b *Bot) UserID() id.UserID { return b.client.UserID }
