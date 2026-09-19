-- name: CreateUser :one
insert into users (id, username, display_name, email, timezone, is_admin)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: GetUser :one
select * from users where id = $1;

-- name: GetUserByUsername :one
select * from users where lower(username) = lower($1);

-- name: LockUser :one
select id, status, is_admin from users where id = $1 for update;

-- name: NextChangeSeq :one
update users set change_seq = change_seq + 1 where id = $1 returning change_seq;

-- name: CurrentChangeSeq :one
select change_seq from users where id = $1;

-- name: InsertChange :exec
insert into changes (user_id, seq, entity_type, entity_id, op, version)
values ($1, $2, $3, $4, $5, $6);

-- name: ListChanges :many
select seq, entity_type, entity_id, op, version from changes
where user_id = $1 and seq > $2 order by seq limit $3;

-- name: OldestChangeSeq :one
select coalesce(min(seq), 0)::bigint from changes where user_id = $1;

-- name: ActivateUser :execrows
update users set password_hash = $2, status = 'active', updated_at = $3, version = version + 1
where id = $1 and status = 'pending';

-- name: ClearUserPassword :exec
update users set password_hash = null, status = 'pending', updated_at = $2, version = version + 1
where id = $1;

-- name: SetPasswordHash :exec
update users set password_hash = $2, updated_at = $3, version = version + 1 where id = $1;

-- name: SetUserStatus :exec
update users set status = $2, updated_at = $3, version = version + 1 where id = $1;

-- name: UpdateProfile :one
update users set display_name = coalesce($2, display_name), timezone = coalesce($3, timezone),
  settings = coalesce($4, settings), updated_at = $5, version = version + 1
where id = $1 returning *;

-- name: ListUsers :many
select u.id, u.username, u.display_name, u.status, u.is_admin, u.created_at,
       coalesce(s.used_bytes, 0)::bigint as used_bytes, s.quota_bytes
from users u left join user_storage s on s.user_id = u.id
order by lower(u.username);

-- name: CountUsers :one
select count(*) from users;

-- name: EnsureUserStorage :exec
insert into user_storage (user_id) values ($1) on conflict do nothing;

-- name: SetQuota :exec
update user_storage set quota_bytes = $2 where user_id = $1;

-- name: GetAdmin :one
select * from users where is_admin;

-- name: SetAdmin :exec
update users set is_admin = $2, updated_at = $3, version = version + 1 where id = $1;
