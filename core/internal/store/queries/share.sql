-- name: InsertShareLink :one
insert into share_links (id, user_id, note_id, token_hash, created_at, expires_at)
values ($1, $2, $3, $4, $5, $6) returning *;

-- name: ListShareLinks :many
select l.id, l.note_id, l.created_at, l.expires_at, l.last_accessed_at, l.view_count,
       n.state as note_state,
       coalesce((select left(p.text, 140) from note_parts p where p.user_id = l.user_id and p.note_id = l.note_id and p.kind = 'text'
                 order by p.ordinal limit 1), '')::text as excerpt
from share_links l
join notes n on n.user_id = l.user_id and n.id = l.note_id
where l.user_id = @user_id and l.revoked_at is null and l.expires_at > @now::timestamptz
  and (sqlc.narg('note_id')::uuid is null or l.note_id = sqlc.narg('note_id')::uuid)
order by l.created_at desc, l.id;

-- name: CountActiveShareLinks :one
select count(*) from share_links where user_id = $1 and revoked_at is null and expires_at > $2;

-- name: CountShareLinksCreatedSince :one
select count(*) from share_links where user_id = $1 and created_at > $2;

-- name: RevokeShareLink :one
update share_links set revoked_at = $3 where id = $1 and user_id = $2 and revoked_at is null and expires_at > $3 returning id, note_id;

-- name: RevokeAllShareLinks :many
update share_links set revoked_at = $2 where user_id = $1 and revoked_at is null and expires_at > $2 returning id;

-- name: LookupShare :one
-- A link is valid only as a live condition: not revoked, not expired, and the owner active. The
-- note's own state is checked afterwards, under the owner's row-level security (CORE-SH5).
select l.id, l.user_id, l.note_id, l.expires_at
from share_links l join users u on u.id = l.user_id
where l.token_hash = $1 and l.revoked_at is null and l.expires_at > $2 and u.status = 'active';

-- name: TouchShareLink :exec
update share_links set view_count = view_count + 1, last_accessed_at = $2 where id = $1;

-- name: PurgeShareLinks :execrows
delete from share_links where expires_at < $1 or revoked_at < $1;

-- name: NoteHasAttachment :one
select exists(select 1 from note_parts where user_id = $1 and note_id = $2 and attachment_id = $3);
