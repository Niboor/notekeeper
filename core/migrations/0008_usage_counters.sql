-- +goose Up
-- The counters are plain sums, never clamped at zero: a row trigger (the history cap below) can delete
-- rows before the statement trigger of the insert that caused it has added them, so a counter may dip
-- below zero for a moment inside a statement and is exact when the statement ends.
-- Per-user counters for the notes-and-text budget (SEC-API-3, SEC-CNT-6). They are kept by statement-level
-- triggers so every path that writes notes, parts or history is counted, cascades included, and one
-- statement that touches many rows makes one update.

alter table user_storage
  add column note_count bigint not null default 0,
  add column text_bytes bigint not null default 0;

-- Existing data.
insert into user_storage (user_id, note_count, text_bytes)
select u.id,
       (select count(*) from notes n where n.user_id = u.id),
       coalesce((select sum(octet_length(p.text)) from note_parts p where p.user_id = u.id), 0)
     + coalesce((select sum(octet_length(v.text)) from note_part_versions v where v.user_id = u.id), 0)
from users u
on conflict (user_id) do update set note_count = excluded.note_count, text_bytes = excluded.text_bytes;

-- +goose StatementBegin
create function nk_count_notes() returns trigger language plpgsql as $$
begin
  if tg_op = 'INSERT' then
    insert into user_storage (user_id, note_count)
    select user_id, count(*) from new_rows group by user_id
    on conflict (user_id) do update set note_count = user_storage.note_count + excluded.note_count;
  else
    update user_storage s set note_count = s.note_count - d.n
    from (select user_id, count(*) n from old_rows group by user_id) d where s.user_id = d.user_id;
  end if;
  return null;
end $$;

create function nk_count_text() returns trigger language plpgsql as $$
begin
  if tg_op = 'INSERT' then
    insert into user_storage (user_id, text_bytes)
    select user_id, sum(octet_length(text)) from new_rows where text is not null group by user_id
    on conflict (user_id) do update set text_bytes = user_storage.text_bytes + excluded.text_bytes;
  elsif tg_op = 'DELETE' then
    update user_storage s set text_bytes = s.text_bytes - d.n
    from (select user_id, sum(octet_length(text)) n from old_rows where text is not null group by user_id) d where s.user_id = d.user_id;
  else
    update user_storage s set text_bytes = s.text_bytes + d.n
    from (select user_id, sum(coalesce(octet_length(nr.text), 0)) - sum(coalesce(octet_length(o.text), 0)) n
          from new_rows nr join old_rows o using (user_id, id) group by user_id) d where s.user_id = d.user_id;
  end if;
  return null;
end $$;

-- History of one text part is capped, oldest first: a part that is edited over and over (a ticked
-- checklist sends the whole text each time) cannot grow without limit (SR-010).
create function nk_prune_versions() returns trigger language plpgsql as $$
begin
  delete from note_part_versions where id in (
    select v.id from note_part_versions v where v.user_id = new.user_id and v.part_id = new.part_id
    order by v.edited_at desc, v.id desc offset 200);
  return null;
end $$;
-- +goose StatementEnd

create trigger versions_prune after insert on note_part_versions for each row execute function nk_prune_versions();
create trigger notes_count_insert after insert on notes referencing new table as new_rows for each statement execute function nk_count_notes();
create trigger notes_count_delete after delete on notes referencing old table as old_rows for each statement execute function nk_count_notes();
create trigger parts_text_insert after insert on note_parts referencing new table as new_rows for each statement execute function nk_count_text();
create trigger parts_text_delete after delete on note_parts referencing old table as old_rows for each statement execute function nk_count_text();
create trigger parts_text_update after update on note_parts referencing old table as old_rows new table as new_rows for each statement execute function nk_count_text();
create trigger versions_text_insert after insert on note_part_versions referencing new table as new_rows for each statement execute function nk_count_text();
create trigger versions_text_delete after delete on note_part_versions referencing old table as old_rows for each statement execute function nk_count_text();

-- +goose Down
drop trigger versions_prune on note_part_versions;
drop function nk_prune_versions();
drop trigger versions_text_delete on note_part_versions;
drop trigger versions_text_insert on note_part_versions;
drop trigger parts_text_update on note_parts;
drop trigger parts_text_delete on note_parts;
drop trigger parts_text_insert on note_parts;
drop trigger notes_count_delete on notes;
drop trigger notes_count_insert on notes;
drop function nk_count_text();
drop function nk_count_notes();
alter table user_storage drop column text_bytes, drop column note_count;
