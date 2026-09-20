-- +goose Up
-- Search index maintenance (docs/design/01-data-model.md section 8). The searchable text lives in
-- note_parts (text) and attachments (filenames), so a generated column cannot hold it; instead a
-- constraint trigger, deferred to commit, rebuilds a note's row from its parts. Search is thereby
-- updated in the same transaction as the change and no code path can forget it (CORE-N13).
--
-- The trigger runs as the invoking role under row-level security, so it needs app.user_id to be
-- set, as every request transaction does. An unset user makes the insert fail loudly instead of
-- silently indexing nothing.

-- +goose StatementBegin
create function nk_note_search_refresh(p_note uuid) returns void language plpgsql as $$
declare
  v_user uuid;
  v_body text;
begin
  select user_id into v_user from notes where id = p_note;
  if v_user is null then
    return; -- the note is gone; its search row cascaded with it
  end if;
  select coalesce(string_agg(s.x, E'\n' order by s.ord, s.sub), '') into v_body from (
    select p.ordinal as ord, 0 as sub, p.text as x from note_parts p where p.note_id = p_note and p.text is not null
    union all
    select p.ordinal, 1, a.filename from note_parts p join attachments a on a.user_id = p.user_id and a.id = p.attachment_id where p.note_id = p_note
    union all
    select p.ordinal, 2, p.failed_filename from note_parts p where p.note_id = p_note and p.failed_filename is not null
  ) s;
  if v_body = '' then
    delete from note_search where note_id = p_note;
  else
    insert into note_search (note_id, user_id, body, doc)
    values (p_note, v_user, v_body, to_tsvector('english', nk_unaccent(v_body)))
    on conflict (note_id) do update set body = excluded.body, doc = excluded.doc;
  end if;
end $$;
-- +goose StatementEnd

-- +goose StatementBegin
create function nk_note_parts_search_trigger() returns trigger language plpgsql as $$
begin
  if tg_op = 'DELETE' then
    perform nk_note_search_refresh(old.note_id);
  else
    perform nk_note_search_refresh(new.note_id);
    if tg_op = 'UPDATE' and old.note_id <> new.note_id then
      perform nk_note_search_refresh(old.note_id);
    end if;
  end if;
  return null;
end $$;
-- +goose StatementEnd

create constraint trigger note_parts_search after insert or update or delete on note_parts
  deferrable initially deferred for each row execute function nk_note_parts_search_trigger();

-- Attachment names are indexed too: renaming or removing an attachment refreshes the notes using it.
-- +goose StatementBegin
create function nk_attachments_search_trigger() returns trigger language plpgsql as $$
declare r record;
begin
  for r in select distinct note_id from note_parts where attachment_id = coalesce(new.id, old.id) loop
    perform nk_note_search_refresh(r.note_id);
  end loop;
  return null;
end $$;
-- +goose StatementEnd

create constraint trigger attachments_search after update on attachments
  deferrable initially deferred for each row execute function nk_attachments_search_trigger();

-- Index what already exists. Content tables are under FORCE row-level security and the migration
-- role is not a superuser, so the backfill sets app.user_id per user (design 01 section 12).
-- +goose StatementBegin
do $$
declare u record; n record;
begin
  for u in select id from users loop
    perform set_config('app.user_id', u.id::text, true);
    for n in select id from notes where user_id = u.id loop
      perform nk_note_search_refresh(n.id);
    end loop;
  end loop;
  perform set_config('app.user_id', '', true);
end $$;
-- +goose StatementEnd

-- +goose Down
drop trigger attachments_search on attachments;
drop trigger note_parts_search on note_parts;
drop function nk_attachments_search_trigger();
drop function nk_note_parts_search_trigger();
drop function nk_note_search_refresh(uuid);
