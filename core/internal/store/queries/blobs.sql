-- name: EnsureStorageRow :exec
insert into user_storage (user_id) values ($1) on conflict do nothing;

-- name: ReserveQuota :execrows
update user_storage set reserved_bytes = reserved_bytes + @n::bigint
where user_id = @user_id and used_bytes + reserved_bytes + @n::bigint <= coalesce(quota_bytes, @default_quota::bigint);

-- name: ReleaseReservation :exec
update user_storage set reserved_bytes = greatest(reserved_bytes - @n::bigint, 0) where user_id = @user_id;

-- name: CommitReservation :exec
update user_storage set reserved_bytes = greatest(reserved_bytes - @n::bigint, 0), used_bytes = used_bytes + @stored::bigint
where user_id = @user_id;

-- name: ReleaseUsed :exec
update user_storage set used_bytes = greatest(used_bytes - @n::bigint, 0) where user_id = @user_id;

-- name: InsertBlob :exec
insert into blobs (id, user_id, size_bytes, chunk_size, reserved_bytes) values ($1, $2, $3, $4, $3);

-- name: InsertChunk :exec
insert into blob_chunks (user_id, blob_id, idx, data) values ($1, $2, $3, $4);

-- name: GetChunk :one
select data from blob_chunks where blob_id = $1 and idx = $2;

-- name: GetBlob :one
select * from blobs where id = $1 and user_id = $2;

-- name: CompleteBlob :exec
update blobs set complete = true, size_bytes = $3, sha256 = $4, reserved_bytes = 0 where id = $1 and user_id = $2;

-- name: DeleteBlob :execrows
delete from blobs where id = $1 and user_id = $2;

-- name: FindDuplicateBlob :one
select id from blobs where user_id = $1 and sha256 = $2 and size_bytes = $3 and complete and id <> $4 limit 1;

-- name: InsertAttachment :one
insert into attachments (id, user_id, blob_id, filename, media_type) values ($1, $2, $3, $4, $5) returning *;

-- name: GetAttachment :one
select * from attachments where id = $1 and user_id = $2;

-- name: GetAttachmentWithBlob :one
select a.id, a.filename, a.media_type, a.created_at, b.id as blob_id, b.size_bytes, b.sha256, b.chunk_size
from attachments a join blobs b on b.user_id = a.user_id and b.id = a.blob_id
where a.id = $1 and a.user_id = $2 and b.complete;

-- name: DeleteAttachment :exec
delete from attachments where id = $1 and user_id = $2;

-- name: BlobIsShared :one
select exists(select 1 from attachments where blob_id = $1 and user_id = $2);

-- name: AttachmentInUse :one
select exists(select 1 from note_parts where attachment_id = $1 and user_id = $2);

-- name: AttachmentsOfNote :many
select a.id, a.blob_id, b.size_bytes from note_parts p
join attachments a on a.user_id = p.user_id and a.id = p.attachment_id
join blobs b on b.user_id = a.user_id and b.id = a.blob_id
where p.note_id = $1;

-- name: StaleIncompleteBlobs :many
select id, reserved_bytes from blobs where user_id = $1 and not complete and created_at < $2;

-- name: UnlinkedAttachments :many
select a.id from attachments a
where a.user_id = $1 and a.created_at < $2
  and not exists (select 1 from note_parts p where p.attachment_id = a.id);

-- name: StorageUsage :one
select used_bytes, reserved_bytes, quota_bytes from user_storage where user_id = $1;

-- name: PartAttachmentInfo :many
select a.id, a.filename, a.media_type, b.size_bytes from attachments a join blobs b on b.user_id = a.user_id and b.id = a.blob_id
where a.id = any($1::uuid[]) and a.user_id = $2;
