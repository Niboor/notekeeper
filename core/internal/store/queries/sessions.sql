-- name: InsertSession :exec
insert into sessions (id, user_id, client_kind, label, created_at, last_refreshed_at, expires_at, absolute_expires_at)
values ($1, $2, $3, $4, $5, $5, $6, $7);

-- name: GetSessionPrincipal :one
select s.id, s.user_id, s.client_kind, s.expires_at, s.absolute_expires_at, s.revoked_at,
       u.status, u.is_admin, u.username, u.display_name, u.timezone
from sessions s join users u on u.id = s.user_id
where s.id = $1;

-- name: GetSessionForUpdate :one
select * from sessions where id = $1 for update;

-- name: RotateSession :execrows
update sessions set refresh_generation = refresh_generation + 1, previous_valid_until = $3,
  last_refreshed_at = $4, expires_at = $5
where id = $1 and refresh_generation = $2 and revoked_at is null;

-- name: RevokeSession :execrows
update sessions set revoked_at = $2, revoked_reason = $3 where id = $1 and revoked_at is null;

-- name: RevokeUserSessions :many
update sessions set revoked_at = $2, revoked_reason = $3
where user_id = $1 and revoked_at is null and (sqlc.narg('except')::uuid is null or id <> sqlc.narg('except')::uuid)
returning id;

-- name: ListUserSessions :many
select id, client_kind, label, created_at, last_refreshed_at, expires_at
from sessions where user_id = $1 and revoked_at is null and expires_at > $2
order by last_refreshed_at desc;

-- name: GetUserSession :one
select id, user_id from sessions where id = $1 and user_id = $2;

-- name: DeleteOldSessions :execrows
delete from sessions where coalesce(revoked_at, expires_at) < $1;

-- name: NotifySessions :exec
select pg_notify('nk_sessions', $1::text);
