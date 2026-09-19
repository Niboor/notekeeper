//go:build integration

package e2eetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/attachment"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"notekeeper/bots/matrix/internal/syncack"
)

// actor is a Matrix user with its own Olm/Megolm state in its own PostgreSQL database. In the
// tests "alice" stands in for a person using Element and "bot" is the Notekeeper bot.
type actor struct {
	t          *testing.T
	name       string
	homeserver string
	dbURL      string
	pickleKey  []byte

	// ack makes the sync token commit only after a batch was processed (package syncack).
	ack bool
	// panicOn simulates Core being unreachable: the first message with this body panics the handler.
	panicOn string

	client *mautrix.Client
	helper *cryptohelper.CryptoHelper
	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	panicked bool
	syncErr  error
	messages chan *event.Event // decrypted room messages received
	joined   chan id.RoomID
}

func (a *actor) start(register bool) {
	a.t.Helper()
	ctx := context.Background()
	client, err := mautrix.NewClient(a.homeserver, "", "")
	if err != nil {
		a.t.Fatal(err)
	}
	client.Log = zerolog.Nop()
	if register {
		if _, err := client.RegisterDummy(ctx, &mautrix.ReqRegister[any]{Username: a.name, Password: "pw-" + a.name}); err != nil {
			a.t.Fatalf("register %s: %v", a.name, err)
		}
	}

	var ackStore *syncack.Store
	if a.ack {
		ackStore = syncack.NewStore(nil)
		client.Syncer = syncack.NewSyncer(ackStore, func() id.UserID { return client.UserID })
	}
	syncer := client.Syncer.(mautrix.ExtensibleSyncer)
	a.messages = make(chan *event.Event, 32)
	a.joined = make(chan id.RoomID, 8)
	syncer.OnEventType(event.EventMessage, func(_ context.Context, evt *event.Event) {
		if evt.Sender == client.UserID {
			return
		}
		a.mu.Lock()
		if a.panicOn != "" && !a.panicked && evt.Content.AsMessage().Body == a.panicOn {
			a.panicked = true
			a.mu.Unlock()
			panic("simulated: Core unreachable while handling a message")
		}
		a.mu.Unlock()
		a.messages <- evt
	})
	syncer.OnEventType(event.StateMember, func(ctx context.Context, evt *event.Event) {
		if evt.GetStateKey() != client.UserID.String() {
			return
		}
		if evt.Content.AsMember().Membership == event.MembershipInvite {
			// The real bot only accepts direct-message invites (MX-2).
			if _, err := client.JoinRoomByID(ctx, evt.RoomID); err == nil {
				a.joined <- evt.RoomID
			}
		}
	})

	db, err := dbutil.NewWithDialect(a.dbURL, "pgx")
	if err != nil {
		a.t.Fatal(err)
	}
	helper, err := cryptohelper.NewCryptoHelper(client, a.pickleKey, db)
	if err != nil {
		a.t.Fatal(err)
	}
	helper.LoginAs = &mautrix.ReqLogin{
		Type:       mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{Type: mautrix.IdentifierTypeUser, User: a.name},
		Password:   "pw-" + a.name,
	}
	if err := helper.Init(ctx); err != nil {
		a.t.Fatalf("init crypto for %s: %v", a.name, err)
	}
	client.Crypto = helper
	if ackStore != nil {
		// The crypto helper installed its PostgreSQL-backed store during Init; defer its token saves.
		ackStore.SyncStore = client.Store
		client.Store = ackStore
	}
	a.client, a.helper = client, helper

	syncCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.done = make(chan struct{})
	go func() {
		defer close(a.done)
		err := client.SyncWithContext(syncCtx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.mu.Lock()
			a.syncErr = err
			a.mu.Unlock()
		}
	}()
}

// stop ends syncing and closes the crypto database, as a pod being killed would.
func (a *actor) stop() {
	a.cancel()
	<-a.done
	if err := a.helper.Close(); err != nil {
		a.t.Fatalf("close crypto db: %v", err)
	}
}

func (a *actor) next(timeout time.Duration) *event.Event {
	a.t.Helper()
	select {
	case evt := <-a.messages:
		return evt
	case <-time.After(timeout):
		a.t.Fatalf("%s: timed out waiting for a message", a.name)
		return nil
	}
}

// expectNone asserts that no message arrives within timeout.
func (a *actor) expectNone(timeout time.Duration) {
	a.t.Helper()
	select {
	case evt := <-a.messages:
		a.t.Fatalf("%s: unexpected message %q", a.name, evt.Content.AsMessage().Body)
	case <-time.After(timeout):
	}
}

// dm creates the two actors and an encrypted direct message between them, as Element does.
func dm(t *testing.T, ctx context.Context, hs string, pg *postgresDB, suffix string, ack bool, panicOn string) (alice, bot *actor, room id.RoomID) {
	t.Helper()
	alice = &actor{t: t, name: "alice_" + suffix, homeserver: hs, dbURL: pg.newDatabase(t, "alice_"+suffix), pickleKey: []byte("alice-pickle-key-0123456789abcdef")}
	bot = &actor{t: t, name: "bot_" + suffix, homeserver: hs, dbURL: pg.newDatabase(t, "bot_"+suffix), pickleKey: []byte("bot-pickle-key-0123456789abcdef!"), ack: ack, panicOn: panicOn}
	alice.start(true)
	bot.start(true)
	resp, err := alice.client.CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Invite:   []id.UserID{bot.client.UserID},
		IsDirect: true,
		Preset:   "trusted_private_chat",
		InitialState: []*event.Event{{
			Type:     event.StateEncryption,
			StateKey: new(string),
			Content:  event.Content{Parsed: &event.EncryptionEventContent{Algorithm: id.AlgorithmMegolmV1}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-bot.joined:
	case <-time.After(30 * time.Second):
		t.Fatal("bot did not join the invited room")
	}
	time.Sleep(2 * time.Second) // Alice's client must learn that the bot joined before it can encrypt
	return alice, bot, resp.RoomID
}

func TestEncryptedDirectMessageRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)

	alice, bot, room := dm(t, ctx, hs, pg, "rt", false, "")
	defer alice.stop()
	botStopped := false
	defer func() {
		if !botStopped {
			bot.stop()
		}
	}()

	// 1. Encrypted text.
	if _, err := alice.client.SendText(ctx, room, "buy milk, eggs"); err != nil {
		t.Fatalf("alice send: %v", err)
	}
	got := bot.next(45 * time.Second)
	if got.Content.AsMessage().Body != "buy milk, eggs" || !got.Mautrix.WasEncrypted {
		t.Fatalf("bot got %q (encrypted=%v)", got.Content.AsMessage().Body, got.Mautrix.WasEncrypted)
	}

	// 2. Encrypted attachment (a PDF ticket), downloaded and decrypted by the bot with the
	// streaming decrypter, so bot memory does not depend on file size (design 06 section 5).
	plaintext := make([]byte, 3*1024*1024+123)
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatal(err)
	}
	file := attachment.NewEncryptedFile()
	ciphertext := file.Encrypt(plaintext)
	up, err := alice.client.UploadBytesWithName(ctx, ciphertext, "application/octet-stream", "tickets.pdf")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	_, err = alice.client.SendMessageEvent(ctx, room, event.EventMessage, &event.MessageEventContent{
		MsgType:  event.MsgFile,
		Body:     "tickets.pdf",
		FileName: "tickets.pdf",
		Info:     &event.FileInfo{MimeType: "application/pdf", Size: len(plaintext)},
		File:     &event.EncryptedFileInfo{EncryptedFile: *file, URL: up.ContentURI.CUString()},
	})
	if err != nil {
		t.Fatalf("send file: %v", err)
	}
	content := bot.next(45 * time.Second).Content.AsMessage()
	if content.MsgType != event.MsgFile || content.File == nil {
		t.Fatalf("bot did not get an encrypted file event: %+v", content)
	}
	mxc, err := content.File.URL.Parse()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := bot.client.DownloadBytes(ctx, mxc)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	// PrepareForDecryption must be called first: DecryptStream builds its cipher eagerly and
	// panics on an unprepared file. The hash is only verified when the stream is closed, so the
	// bot must check Close's error before it accepts the upload (design 06 section 5).
	if err := content.File.PrepareForDecryption(); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	dec := content.File.DecryptStream(bytes.NewReader(blob))
	roundTripped, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("decrypt stream: %v", err)
	}
	if err := dec.Close(); err != nil {
		t.Fatalf("attachment hash check failed: %v", err)
	}
	if !bytes.Equal(roundTripped, plaintext) {
		t.Fatal("decrypted attachment differs from the original")
	}

	// 3. The crypto state is in PostgreSQL, not on local disk (MX-N1, BOT-B5).
	tables := tablesOf(t, bot.dbURL)
	for _, want := range []string{"crypto_account", "crypto_megolm_inbound_session", "crypto_device"} {
		if !tables[want] {
			t.Errorf("expected crypto table %s in the bot database; have %v", want, tables)
		}
	}
	deviceBefore := bot.client.DeviceID

	// 4. Restart the bot: same database, same pickle key. It must come back as the same device
	// and still decrypt what Alice sends, using the session it stored in PostgreSQL.
	bot.stop()
	botStopped = true
	bot.start(false)
	botStopped = false
	if bot.client.DeviceID != deviceBefore {
		t.Fatalf("device changed across restart: %s -> %s", deviceBefore, bot.client.DeviceID)
	}
	time.Sleep(2 * time.Second)
	if _, err := alice.client.SendText(ctx, room, "after restart"); err != nil {
		t.Fatal(err)
	}
	if got := bot.next(45 * time.Second); got.Content.AsMessage().Body != "after restart" || !got.Mautrix.WasEncrypted {
		t.Fatalf("after restart bot got %q (encrypted=%v)", got.Content.AsMessage().Body, got.Mautrix.WasEncrypted)
	}
}

// TestSyncTokenCommit documents and guards the mautrix-go behaviour that shaped design 06
// section 3: the library saves its sync token BEFORE dispatching a response's events. A handler
// that fails (here: a panic standing in for "Core unreachable") therefore loses the message
// unless the token commit is deferred (package syncack).
func TestSyncTokenCommit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	hs, pg := startSynapse(t), startPostgres(t)

	for _, tc := range []struct {
		name      string
		ack       bool
		redeliver bool
	}{
		{"mautrix default loses the batch", false, false},
		{"deferred commit replays the batch", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			suffix := "default"
			if tc.ack {
				suffix = "ack"
			}
			alice, bot, room := dm(t, ctx, hs, pg, suffix, tc.ack, "poison")
			defer alice.stop()

			// A message whose handling fails: the sync loop ends with an error.
			if _, err := alice.client.SendText(ctx, room, "poison"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-bot.done:
			case <-time.After(45 * time.Second):
				t.Fatal("sync loop did not stop after the handler failed")
			}
			bot.mu.Lock()
			failed := bot.syncErr != nil
			bot.mu.Unlock()
			if !failed {
				t.Fatal("expected the sync loop to end with an error")
			}
			if err := bot.helper.Close(); err != nil {
				t.Fatal(err)
			}

			// Restart as the pod would: same database, same device.
			bot.start(false)
			defer bot.stop()
			if tc.redeliver {
				if got := bot.next(45 * time.Second); got.Content.AsMessage().Body != "poison" {
					t.Fatalf("expected the failed message to be redelivered, got %q", got.Content.AsMessage().Body)
				}
			} else {
				bot.expectNone(10 * time.Second) // lost: this is why syncack exists
			}
		})
	}
}

func tablesOf(t *testing.T, url string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `select table_name from information_schema.tables where table_schema = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}
