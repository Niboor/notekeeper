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

-- ---- note operations (M2) ----

-- name: SetNoteLocation :one
update notes set category_id = $3, position = $4, updated_at = $5, version = version + 1
where id = $1 and user_id = $2 returning *;

-- name: DismissNote :one
update notes set state = 'deleted', deleted_at = $3, updated_at = $3, version = version + 1
where id = $1 and user_id = $2 and state = 'active' returning *;

-- name: RestoreNote :one
update notes set state = 'active', deleted_at = null, updated_at = $3, version = version + 1
where id = $1 and user_id = $2 and state = 'deleted' returning *;

-- name: DeleteNote :execrows
delete from notes where id = $1 and user_id = $2 and state = 'deleted';

-- name: TouchNote :one
update notes set updated_at = $3, version = version + 1 where id = $1 and user_id = $2 returning *;

-- name: ListTrash :many
select * from notes
where user_id = $1 and state = 'deleted'
  and (sqlc.narg('after_deleted')::timestamptz is null or (deleted_at, id) < (sqlc.narg('after_deleted')::timestamptz, sqlc.narg('after_id')::uuid))
order by deleted_at desc, id desc
limit $2;

-- name: CountTrash :one
select count(*) from notes where user_id = $1 and state = 'deleted';

-- name: NoteLocations :many
select n.id as note_id, c.id as category_id, c.name as category_name, p.id as page_id, p.name as page_name
from notes n join categories c on c.user_id = n.user_id and c.id = n.category_id
             join pages p on p.user_id = c.user_id and p.id = c.page_id
where n.user_id = $1 and n.id = any($2::uuid[]);

-- name: NextPartOrdinal :one
select coalesce(max(ordinal) + 1, 0)::int from note_parts where note_id = $1;

-- name: GetNotePart :one
select * from note_parts where id = $1 and note_id = $2 and user_id = $3;

-- name: CountNoteParts :one
select count(*) from note_parts where note_id = $1;

-- name: UpdatePartText :exec
update note_parts set text = $2, text_edited_at = $3 where id = $1;

-- name: DeleteNotePart :exec
delete from note_parts where id = $1;

-- name: InsertPartVersion :exec
insert into note_part_versions (id, user_id, part_id, text, origin, edited_at, applied, source_event_id, session_id)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: LatestAppVersion :one
select id, session_id, recorded_at from note_part_versions
where part_id = $1 and origin = 'app' and applied order by edited_at desc, recorded_at desc limit 1;

-- name: OverwriteVersionText :exec
update note_part_versions set text = $2, edited_at = $3, recorded_at = $3 where id = $1;

-- name: ListNoteVersions :many
select v.* from note_part_versions v join note_parts p on p.id = v.part_id
where p.note_id = $1 order by v.part_id, v.edited_at desc, v.recorded_at desc;

-- name: NoteByClientID :one
select * from notes where id = $1;

-- name: InsertNoteAt :one
insert into notes (id, user_id, category_id, position, created_at, received_at) values ($1, $2, $3, $4, $5, $5) returning *;

-- name: NotesByIDs :many
select * from notes where user_id = $1 and id = any($2::uuid[]);
