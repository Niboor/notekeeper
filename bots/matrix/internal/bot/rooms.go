package bot

import (
	"context"
	"database/sql"
	"errors"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/id"
)

// roomStore keeps the little the bot must remember about rooms: which ones it ignores because
// they are not two-person direct messages (MX-2). It lives in the bot's own schema (SEC-OPS-5).
// No message content and no user data is stored here (BOT-B5, SEC-MX-3).
type roomStore struct{ db *dbutil.Database }

func (s *roomStore) init(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `create table if not exists nk_rooms (
		room_id  text primary key,
		ignored  boolean not null default false,
		notified boolean not null default false
	)`)
	return err
}

func (s *roomStore) isIgnored(ctx context.Context, room id.RoomID) (bool, error) {
	var ignored bool
	err := s.db.QueryRow(ctx, `select ignored from nk_rooms where room_id = $1`, room).Scan(&ignored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return ignored, err
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
