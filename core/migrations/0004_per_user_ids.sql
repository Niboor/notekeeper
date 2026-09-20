-- +goose Up
-- Identifiers that clients may choose (pages, categories, notes, attachments) are unique per user,
-- not globally. With a global primary key, creating an object under an id that belongs to someone
-- else fails differently from creating one under an unused id, which would tell a caller that the
-- id exists (SEC-ISO-3). Every child table already references (user_id, id), so only the primary
-- keys and the two constraints that mention a bare note id change.

alter table pages      drop constraint pages_pkey;       alter table pages      add primary key (user_id, id);
alter table categories drop constraint categories_pkey;  alter table categories add primary key (user_id, id);
alter table notes      drop constraint notes_pkey;       alter table notes      add primary key (user_id, id);
alter table attachments drop constraint attachments_pkey; alter table attachments add primary key (user_id, id);

alter table note_search drop constraint note_search_pkey;
alter table note_search add primary key (user_id, note_id);

alter table note_parts drop constraint note_parts_note_id_ordinal_key;
alter table note_parts add constraint note_parts_note_ordinal_key unique (user_id, note_id, ordinal) deferrable initially deferred;

-- An upload in progress is found by the client's attachment id, while the blob gets its own id.
alter table blobs add column upload_id uuid;
create unique index blobs_upload on blobs (user_id, upload_id) where not complete;

-- +goose StatementBegin
create or replace function nk_note_search_refresh(p_note uuid) returns void language plpgsql as $$
declare
  v_user uuid := nk_current_user();
  v_body text;
begin
  if not exists (select 1 from notes where user_id = v_user and id = p_note) then
    return; -- the note is gone; its search row cascaded with it
  end if;
  select coalesce(string_agg(s.x, E'\n' order by s.ord, s.sub), '') into v_body from (
    select p.ordinal as ord, 0 as sub, p.text as x from note_parts p where p.user_id = v_user and p.note_id = p_note and p.text is not null
    union all
    select p.ordinal, 1, a.filename from note_parts p join attachments a on a.user_id = p.user_id and a.id = p.attachment_id
      where p.user_id = v_user and p.note_id = p_note
    union all
    select p.ordinal, 2, p.failed_filename from note_parts p where p.user_id = v_user and p.note_id = p_note and p.failed_filename is not null
  ) s;
  if v_body = '' then
    delete from note_search where user_id = v_user and note_id = p_note;
  else
    insert into note_search (note_id, user_id, body, doc)
    values (p_note, v_user, v_body, to_tsvector('english', nk_unaccent(v_body)))
    on conflict (user_id, note_id) do update set body = excluded.body, doc = excluded.doc;
  end if;
end $$;
-- +goose StatementEnd

-- +goose StatementBegin
create or replace function nk_attachments_search_trigger() returns trigger language plpgsql as $$
declare r record;
begin
  for r in select distinct note_id from note_parts where user_id = coalesce(new.user_id, old.user_id) and attachment_id = coalesce(new.id, old.id) loop
    perform nk_note_search_refresh(r.note_id);
  end loop;
  return null;
end $$;
-- +goose StatementEnd

-- +goose Down
drop index blobs_upload;
alter table blobs drop column upload_id;
