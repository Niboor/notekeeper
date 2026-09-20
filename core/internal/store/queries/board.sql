-- ---- pages ----

-- name: InsertPage :one
insert into pages (id, user_id, name, position) values ($1, $2, $3, $4) returning *;

-- name: GetPage :one
select * from pages where id = $1 and user_id = $2;

-- name: ListPages :many
select * from pages where user_id = $1 order by position, id;

-- name: UpdatePage :one
update pages set name = coalesce(sqlc.narg('name'), name), position = coalesce(sqlc.narg('position'), position),
  archived_at = case when sqlc.narg('archived')::bool is null then archived_at
                     when sqlc.narg('archived')::bool then coalesce(archived_at, @now::timestamptz) else null end,
  updated_at = @now, version = version + 1
where id = @id and user_id = @user_id returning *;

-- name: DeletePage :execrows
delete from pages where id = $1 and user_id = $2;

-- name: PageKeyAfter :one
select position from pages where user_id = $1 and id <> $2 and position > $3 order by position, id limit 1;

-- name: PageKeyBefore :one
select position from pages where user_id = $1 and id <> $2 and position < $3 order by position desc, id desc limit 1;

-- name: PageKeyFirst :one
select position from pages where user_id = $1 and id <> $2 order by position, id limit 1;

-- name: PageKeyLast :one
select position from pages where user_id = $1 and id <> $2 order by position desc, id desc limit 1;

-- ---- categories ----

-- name: InsertCategory :one
insert into categories (id, user_id, page_id, name, position) values ($1, $2, $3, $4, $5) returning *;

-- name: GetCategory :one
select * from categories where id = $1 and user_id = $2;

-- name: ListCategoriesOfPage :many
select * from categories where user_id = $1 and page_id = $2 order by position, id;

-- name: UpdateCategory :one
update categories set name = coalesce(sqlc.narg('name'), name), position = coalesce(sqlc.narg('position'), position),
  page_id = coalesce(sqlc.narg('page_id'), page_id), updated_at = @now, version = version + 1
where id = @id and user_id = @user_id returning *;

-- name: DeleteCategory :execrows
delete from categories where id = $1 and user_id = $2;

-- name: CategoryKeyAfter :one
select position from categories where user_id = $1 and page_id = $2 and id <> $3 and position > $4 order by position, id limit 1;

-- name: CategoryKeyBefore :one
select position from categories where user_id = $1 and page_id = $2 and id <> $3 and position < $4 order by position desc, id desc limit 1;

-- name: CategoryKeyFirst :one
select position from categories where user_id = $1 and page_id = $2 and id <> $3 order by position, id limit 1;

-- name: CategoryKeyLast :one
select position from categories where user_id = $1 and page_id = $2 and id <> $3 order by position desc, id desc limit 1;

-- name: CategoryIDsOfPage :many
select id from categories where user_id = $1 and page_id = $2;

-- ---- notes in categories ----

-- name: ListCategoryNotes :many
select * from notes
where user_id = $1 and category_id = $2 and state = 'active'
  and (sqlc.narg('after_position')::text is null or (position, id) > (sqlc.narg('after_position')::text collate "C", sqlc.narg('after_id')::uuid))
order by position, id
limit $3;

-- name: CountCategoryNotes :one
select count(*) from notes where user_id = $1 and category_id = $2 and state = 'active';

-- name: NoteIDsInCategories :many
select id from notes where user_id = $1 and category_id = any($2::uuid[]);

-- name: NoteKeyAfter :one
select position from notes where user_id = $1 and category_id = $2 and state = 'active' and id <> $3 and position > $4 order by position, id limit 1;

-- name: NoteKeyBefore :one
select position from notes where user_id = $1 and category_id = $2 and state = 'active' and id <> $3 and position < $4 order by position desc, id desc limit 1;

-- name: NoteKeyFirst :one
select position from notes where user_id = $1 and category_id = $2 and state = 'active' and id <> $3 order by position, id limit 1;

-- name: NoteKeyLast :one
select position from notes where user_id = $1 and category_id = $2 and state = 'active' and id <> $3 order by position desc, id desc limit 1;

-- name: NotePosition :one
select position from notes where user_id = $1 and id = $2 and category_id = $3 and state = 'active';

-- name: CategoryNotePositions :many
select id from notes where user_id = $1 and category_id = $2 and state = 'active' and id <> $3 order by position, id;

-- name: RekeyNote :one
update notes set position = $3, updated_at = $4, version = version + 1 where id = $1 and user_id = $2 returning version;

-- name: PageIDsOrdered :many
select id from pages where user_id = $1 and id <> $2 order by position, id;

-- name: RekeyPage :one
update pages set position = $3, updated_at = $4, version = version + 1 where id = $1 and user_id = $2 returning version;

-- name: CategoryIDsOrdered :many
select id from categories where user_id = $1 and page_id = $2 and id <> $3 order by position, id;

-- name: RekeyCategory :one
update categories set position = $3, updated_at = $4, version = version + 1 where id = $1 and user_id = $2 returning version;
