-- name: ExportPages :many
select * from pages where user_id = $1 order by position, id;

-- name: ExportCategories :many
select * from categories where user_id = $1 order by page_id, position, id;

-- name: ExportNotes :many
select * from notes where user_id = $1 and id > $2 order by id limit $3;

-- name: ExportParts :many
select * from note_parts where user_id = $1 and note_id = any(@ids::uuid[]) order by note_id, ordinal;

-- name: ExportVersions :many
select v.* from note_part_versions v join note_parts p on p.user_id = v.user_id and p.id = v.part_id
where v.user_id = $1 and p.note_id = any(@ids::uuid[]) order by v.part_id, v.edited_at, v.id;

-- name: ExportReminders :many
select * from reminders where user_id = $1 order by note_id, due_at, id;

-- name: ExportShareLinks :many
select id, note_id, created_at, expires_at, revoked_at, last_accessed_at, view_count from share_links where user_id = $1 order by created_at, id;

-- name: ExportAttachmentInfo :many
select a.id, a.filename, a.media_type, b.size_bytes from attachments a join blobs b on b.user_id = a.user_id and b.id = a.blob_id
where a.user_id = $1 and a.id = any(@ids::uuid[]);

-- name: CountRecentExports :one
select count(*) from audit_log where action = 'user.exported' and target_id = $1 and at > $2;
