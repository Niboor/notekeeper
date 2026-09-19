-- name: InsertNote :one
insert into notes (id, user_id, created_at, received_at) values ($1, $2, $3, $4) returning *;

-- name: InsertNotePart :one
insert into note_parts (id, user_id, note_id, ordinal, kind, text, attachment_id, failed_filename, failed_size, failed_reason,
  attach_reason, related_part_id, created_at, source_bot_instance_id, source_identity_id, source_conversation_id,
  source_message_id, source_part_index)
values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) returning *;

-- name: GetNote :one
select * from notes where id = $1 and user_id = $2;

-- name: ListNoteParts :many
select sqlc.embed(p), b.type as source_bot_type
from note_parts p left join bot_instances b on b.id = p.source_bot_instance_id
where p.note_id = any($1::uuid[]) order by p.note_id, p.ordinal;

-- name: ListInbox :many
select * from notes
where user_id = $1 and state = 'active' and category_id is null
  and (sqlc.narg('after_created')::timestamptz is null or (created_at, id) < (sqlc.narg('after_created')::timestamptz, sqlc.narg('after_id')::uuid))
order by created_at desc, id desc
limit $2;

-- name: CountInbox :one
select count(*) from notes where user_id = $1 and state = 'active' and category_id is null;

-- name: InsertIngestEvent :execrows
insert into ingest_events (bot_instance_id, event_id, user_id, result) values ($1, $2, $3, $4)
on conflict do nothing;

-- name: GetIngestEvent :one
select result from ingest_events where bot_instance_id = $1 and event_id = $2;

-- name: UpdateIngestResult :exec
update ingest_events set result = $3 where bot_instance_id = $1 and event_id = $2;
