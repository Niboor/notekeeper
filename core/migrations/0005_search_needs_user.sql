-- +goose Up
-- The search trigger must never index nothing silently. Migration 0004 rewrote it to look the note
-- up under the current user, which made "no user context" indistinguishable from "the note is
-- gone". Restore the rule of migration 0003: without app.user_id the trigger fails loudly. (Row-level
-- security already refuses such writes for the runtime role; this catches superuser and
-- operator writes, which bypass it.)

-- +goose StatementBegin
create or replace function nk_note_search_refresh(p_note uuid) returns void language plpgsql as $$
declare
  v_user uuid := nk_current_user();
  v_body text;
begin
  if v_user is null then
    raise exception 'nk_note_search_refresh: app.user_id is not set (set it with set_config before changing note parts)';
  end if;
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

-- +goose Down
select 1;
