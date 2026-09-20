package bot

import (
	"context"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

// roomStore keeps the little the bot must remember about rooms: which ones it ignores because
// they are not two-person direct messages (MX-2). It lives in the bot's own schema (SEC-OPS-5).
// No message content and no user data is stored here (BOT-B5, SEC-MX-3).
type roomStore struct{ db *dbutil.Database }

// roomMemory is what the bot needs from the room table (a fake in tests).
type roomMemory interface {
	ignore(ctx context.Context, room id.RoomID) (alreadyNotified bool, err error)
	markNotified(ctx context.Context, room id.RoomID) error
	recover(ctx context.Context, room id.RoomID) error
	forget(ctx context.Context, room id.RoomID) error
}

func (s *roomStore) init(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `create table if not exists nk_rooms (
		room_id  text primary key,
		ignored  boolean not null default false,
		notified boolean not null default false
	)`)
	return err
}

// recover records that a room that was ignored is a two-person chat again, so the user is told
// once more if it happens again (CR-005). A room the store never heard of is left alone.
func (s *roomStore) recover(ctx context.Context, room id.RoomID) error {
	_, err := s.db.Exec(ctx, `update nk_rooms set ignored = false, notified = false where room_id = $1 and ignored`, room)
	return err
}

// ignore marks a room ignored and reports whether the explanation has been sent before.
func (s *roomStore) ignore(ctx context.Context, room id.RoomID) (alreadyNotified bool, err error) {
	err = s.db.QueryRow(ctx, `insert into nk_rooms (room_id, ignored) values ($1, true)
		on conflict (room_id) do update set ignored = true returning notified`, room).Scan(&alreadyNotified)
	return alreadyNotified, err
}

func (s *roomStore) markNotified(ctx context.Context, room id.RoomID) error {
	_, err := s.db.Exec(ctx, `update nk_rooms set notified = true where room_id = $1`, room)
	return err
}

// forget drops everything remembered about a room (a chat that was unlinked, MX-12).
func (s *roomStore) forget(ctx context.Context, room id.RoomID) error {
	_, err := s.db.Exec(ctx, `delete from nk_rooms where room_id = $1`, room)
	return err
}
