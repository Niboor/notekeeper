-- name: CreateUser :one
insert into users (id, username, display_name, email, timezone, is_admin)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: GetUser :one
select * from users where id = $1;

-- name: GetUserByUsername :one
select * from users where lower(username) = lower($1);

-- name: LockUser :one
select u.id, u.status, u.is_admin, coalesce(s.note_count, 0)::bigint as note_count, coalesce(s.text_bytes, 0)::bigint as text_bytes
from users u left join user_storage s on s.user_id = u.id
where u.id = $1 for update of u;

-- name: UserUsage :one
select coalesce(s.note_count, 0)::bigint as note_count, coalesce(s.text_bytes, 0)::bigint as text_bytes
from user_storage s where s.user_id = $1;

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
update users set display_name = coalesce(sqlc.narg('display_name'), display_name),
  timezone = coalesce(sqlc.narg('timezone'), timezone), settings = coalesce(sqlc.narg('settings')::jsonb, settings),
  updated_at = @updated_at, version = version + 1
where id = @id returning *;

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

-- name: ListDeletingUsers :many
select id from users where status = 'deleting' order by updated_at limit 50;

-- name: DeleteBlobChunksBatch :execrows
delete from blob_chunks where (blob_id, idx) in (select c.blob_id, c.idx from blob_chunks c where c.user_id = $1 limit $2) and user_id = $1;

-- name: DeleteUserRow :execrows
delete from users where id = $1 and status = 'deleting';

-- name: IdentitiesForDeletion :many
select bot_instance_id, external_user_id, conversation_id from external_identities where user_id = $1 and conversation_id is not null;

-- The batch deletes below let a large account go in many short transactions instead of one long
-- one (CR-031). Deleting a note takes its parts, history, reminders, search row and share links.
-- name: DeleteUserNotesBatch :execrows
delete from notes where ctid in (select n.ctid from notes n where n.user_id = @user_id limit @max_rows);

-- name: DeleteUserChangesBatch :execrows
delete from changes where ctid in (select c.ctid from changes c where c.user_id = @user_id limit @max_rows);

-- name: DeleteUserIngestEventsBatch :execrows
delete from ingest_events where ctid in (select e.ctid from ingest_events e where e.user_id = @user_id limit @max_rows);

-- name: DeleteUserNoteParts :execrows
delete from note_parts where user_id = $1;

-- name: DeleteUserAttachments :execrows
delete from attachments where user_id = $1;

-- name: UserWasActivated :one
-- The audit log outlives password resets, so it says whether an account has ever been set up.
select exists(select 1 from audit_log where action = 'user.activated' and target_id = $1);
