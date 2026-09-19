-- +goose Up
-- The data model of docs/design/01-data-model.md. Written as one migration because no release
-- exists yet; from the first release on, migrations are additive and backwards compatible (NFR-D4).

-- ---- helpers -----------------------------------------------------------------------------------

-- The user a transaction acts for. Set with `set local app.user_id` by the store layer; null when
-- unset, so a forgotten context matches no rows instead of raising an error (SEC-ISO-10).
-- +goose StatementBegin
create function nk_current_user() returns uuid
  language sql stable parallel safe
  as $$ select nullif(current_setting('app.user_id', true), '')::uuid $$;
-- +goose StatementEnd

-- ---- users, sessions, tokens (design 01 section 2) ---------------------------------------------

create table users (
  id            uuid primary key,
  username      text not null check (length(username) between 1 and 64),
  display_name  text not null check (length(display_name) between 1 and 100),
  email         text,
  status        text not null default 'pending' check (status in ('pending','active','disabled','deleting')),
  is_admin      boolean not null default false,
  password_hash text,
  timezone      text not null default 'UTC',
  settings      jsonb not null default '{}',
  change_seq    bigint not null default 0,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now(),
  version       integer not null default 1
);
create unique index users_username_key on users (lower(username));
create unique index users_single_admin on users (is_admin) where is_admin;

create table sessions (
  id                   uuid primary key,
  user_id              uuid not null references users(id) on delete cascade,
  client_kind          text not null check (client_kind in ('web','native')),
  label                text not null,
  created_at           timestamptz not null default now(),
  last_refreshed_at    timestamptz not null default now(),
  expires_at           timestamptz not null,
  absolute_expires_at  timestamptz not null,
  refresh_generation   integer not null default 0,
  previous_valid_until timestamptz,
  revoked_at           timestamptz,
  revoked_reason       text
);
create index sessions_user on sessions (user_id) where revoked_at is null;

create table user_tokens (
  id         uuid primary key,
  user_id    uuid not null references users(id) on delete cascade,
  kind       text not null check (kind in ('activation')),
  token_hash bytea not null unique,
  expires_at timestamptz not null,
  used_at    timestamptz,
  created_by uuid references users(id) on delete set null
);
create index user_tokens_user on user_tokens (user_id);

create table auth_throttle (
  key           text primary key,
  failures      integer not null,
  blocked_until timestamptz,
  updated_at    timestamptz not null
);

-- ---- bots and linked identities (design 01 section 3) ------------------------------------------

create table bot_instances (
  id              uuid primary key,
  type            text not null,
  name            text not null unique,
  identity_domain text not null,
  status          text not null default 'active' check (status in ('active','disabled')),
  last_seen_at    timestamptz,
  created_at      timestamptz not null default now()
);

create table bot_credentials (
  id              uuid primary key,
  bot_instance_id uuid not null references bot_instances(id) on delete cascade,
  client_id       text not null unique,
  secret_hash     bytea not null,
  scopes          text[] not null default '{ingest,deliver}',
  created_at      timestamptz not null default now(),
  disabled_at     timestamptz
);

create table external_identities (
  id               uuid primary key,
  user_id          uuid not null references users(id) on delete cascade,
  bot_instance_id  uuid not null references bot_instances(id),
  bot_type         text not null,
  external_user_id text not null,
  conversation_id  text,
  reminder_target  boolean not null default false,
  linked_at        timestamptz not null default now()
);
create unique index identities_unique on external_identities (bot_type, external_user_id);
create unique index identities_user_id on external_identities (user_id, id);

create table pairing_codes (
  id              uuid primary key,
  user_id         uuid not null references users(id) on delete cascade,
  code_hash       bytea not null unique,
  bot_instance_id uuid references bot_instances(id) on delete cascade,
  bot_type        text not null,
  expires_at      timestamptz not null,
  used_at         timestamptz
);
create index pairing_codes_user on pairing_codes (user_id);

-- ---- content (design 01 section 4) -------------------------------------------------------------

create table pages (
  id          uuid primary key,
  user_id     uuid not null references users(id) on delete cascade,
  name        text not null check (length(name) between 1 and 100),
  position    text not null collate "C",
  archived_at timestamptz,
  created_at  timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  version     integer not null default 1,
  unique (user_id, id)
);
create index pages_order on pages (user_id, position);

create table categories (
  id         uuid primary key,
  user_id    uuid not null,
  page_id    uuid not null,
  name       text not null check (length(name) between 1 and 100),
  position   text not null collate "C",
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  version    integer not null default 1,
  unique (user_id, id),
  foreign key (user_id, page_id) references pages (user_id, id) on delete cascade
);
create index categories_order on categories (user_id, page_id, position);

create table notes (
  id          uuid primary key,
  user_id     uuid not null references users(id) on delete cascade,
  category_id uuid,
  position    text collate "C",
  state       text not null default 'active' check (state in ('active','deleted')),
  deleted_at  timestamptz,
  created_at  timestamptz not null,
  received_at timestamptz not null default now(),
  updated_at  timestamptz not null default now(),
  version     integer not null default 1,
  unique (user_id, id),
  -- Only category_id is nulled (PostgreSQL 15+), never user_id, which is part of the key.
  foreign key (user_id, category_id) references categories (user_id, id) on delete set null (category_id),
  check ((category_id is null) = (position is null))
);
create index notes_board on notes (user_id, category_id, position) where state = 'active' and category_id is not null;
create index notes_inbox on notes (user_id, created_at desc, id) where state = 'active' and category_id is null;
create index notes_trash on notes (user_id, deleted_at desc) where state = 'deleted';

-- A note whose category disappears falls back to the Inbox, which has no position (CORE-P4).
-- +goose StatementBegin
create function nk_notes_clear_position() returns trigger language plpgsql as $$
begin
  if new.category_id is null then new.position := null; end if;
  return new;
end $$;
-- +goose StatementEnd
create trigger notes_clear_position before update of category_id on notes
  for each row execute function nk_notes_clear_position();

-- ---- attachments and blobs (design 01 section 6) -----------------------------------------------

create table blobs (
  id             uuid primary key,
  user_id        uuid not null references users(id) on delete cascade,
  backend        text not null default 'pg',
  size_bytes     bigint not null,
  chunk_size     integer not null default 262144,
  sha256         bytea,
  complete       boolean not null default false,
  reserved_bytes bigint not null default 0,
  created_at     timestamptz not null default now(),
  unique (user_id, id)
);
create index blobs_incomplete on blobs (created_at) where not complete;
create unique index blobs_dedupe on blobs (user_id, sha256, size_bytes) where complete and sha256 is not null;

create table blob_chunks (
  user_id uuid not null,
  blob_id uuid not null,
  idx     integer not null,
  data    bytea not null,
  primary key (blob_id, idx),
  foreign key (user_id, blob_id) references blobs (user_id, id) on delete cascade
);
alter table blob_chunks alter column data set storage external;

create table attachments (
  id         uuid primary key,
  user_id    uuid not null,
  blob_id    uuid not null,
  filename   text not null,
  media_type text not null,
  created_at timestamptz not null default now(),
  unique (user_id, id),
  foreign key (user_id, blob_id) references blobs (user_id, id)
);
create index attachments_blob on attachments (blob_id);

create table user_storage (
  user_id        uuid primary key references users(id) on delete cascade,
  used_bytes     bigint not null default 0,
  reserved_bytes bigint not null default 0,
  quota_bytes    bigint
);

-- ---- note parts and history (design 01 section 4.2) --------------------------------------------

create table note_parts (
  id                     uuid primary key,
  user_id                uuid not null,
  note_id                uuid not null,
  ordinal                integer not null,
  kind                   text not null check (kind in ('text','attachment','failed_attachment','unsupported')),
  text                   text,
  attachment_id          uuid,
  failed_filename        text,
  failed_size            bigint,
  failed_reason          text,
  attach_reason          text not null check (attach_reason in ('first','reply','thread','media-adjacency','app')),
  related_part_id        uuid,
  created_at             timestamptz not null,
  text_edited_at         timestamptz,
  source_bot_instance_id uuid,
  source_identity_id     uuid,
  source_conversation_id text,
  source_message_id      text,
  source_part_index      smallint,
  unique (user_id, id),
  unique (note_id, ordinal) deferrable initially deferred,
  foreign key (user_id, note_id) references notes (user_id, id) on delete cascade,
  foreign key (user_id, attachment_id) references attachments (user_id, id)
);
create unique index parts_source on note_parts
  (source_bot_instance_id, source_conversation_id, source_message_id, source_part_index)
  where source_message_id is not null;
create index parts_conv_recent on note_parts (source_identity_id, source_conversation_id, created_at desc)
  where source_identity_id is not null;
create index parts_note on note_parts (note_id, ordinal);

create table note_part_versions (
  id              uuid primary key,
  user_id         uuid not null,
  part_id         uuid not null,
  text            text not null,
  origin          text not null check (origin in ('chat','app')),
  edited_at       timestamptz not null,
  recorded_at     timestamptz not null default now(),
  applied         boolean not null,
  source_event_id text,
  session_id      uuid,
  foreign key (user_id, part_id) references note_parts (user_id, id) on delete cascade
);
create index versions_part on note_part_versions (part_id, edited_at desc);

-- ---- change log (design 01 section 5) ----------------------------------------------------------

create table changes (
  user_id     uuid not null references users(id) on delete cascade,
  seq         bigint not null,
  entity_type text not null,
  entity_id   uuid not null,
  op          text not null check (op in ('upsert','delete')),
  version     integer,
  created_at  timestamptz not null default now(),
  primary key (user_id, seq)
);

-- ---- search (design 01 section 8; the maintaining trigger arrives with milestone M2) -----------

create table note_search (
  note_id uuid primary key,
  user_id uuid not null,
  body    text not null,
  doc     tsvector not null,
  foreign key (user_id, note_id) references notes (user_id, id) on delete cascade
);
create index note_search_doc on note_search using gin (doc);
create index note_search_trgm on note_search using gin (nk_unaccent(lower(body)) gin_trgm_ops);

-- ---- reminders, outbox, notifications (design 01 section 9) ------------------------------------

create table reminders (
  id            uuid primary key,
  user_id       uuid not null,
  note_id       uuid not null,
  due_at        timestamptz not null,
  rrule         text,
  tz            text not null,
  state         text not null default 'pending' check (state in ('pending','fired','suspended','done','cancelled')),
  last_fired_at timestamptz,
  claimed_until timestamptz,
  created_at    timestamptz not null default now(),
  version       integer not null default 1,
  foreign key (user_id, note_id) references notes (user_id, id) on delete cascade
);
create index reminders_due on reminders (due_at) where state = 'pending';
create index reminders_note on reminders (note_id);

create table bot_outbox (
  id               uuid primary key,
  bot_instance_id  uuid not null references bot_instances(id),
  kind             text not null check (kind in ('reminder','notice','lifecycle')),
  user_id          uuid references users(id) on delete cascade,
  external_user_id text not null,
  conversation_id  text not null,
  payload          jsonb not null,
  state            text not null default 'queued' check (state in ('queued','claimed','delivered','failed','expired')),
  attempts         integer not null default 0,
  next_attempt_at  timestamptz not null default now(),
  lease_expires_at timestamptz,
  due_at           timestamptz,
  reminder_id      uuid,
  created_at       timestamptz not null default now(),
  finished_at      timestamptz,
  failure_reason   text
);
create index outbox_claim on bot_outbox (bot_instance_id, next_attempt_at) where state in ('queued','claimed');
create index outbox_user on bot_outbox (user_id) where user_id is not null;

create table outbox_messages (
  bot_instance_id uuid not null,
  conversation_id text not null,
  message_id      text not null,
  outbox_id       uuid not null references bot_outbox(id) on delete cascade,
  primary key (bot_instance_id, conversation_id, message_id)
);

create table notifications (
  id         uuid primary key,
  user_id    uuid not null references users(id) on delete cascade,
  kind       text not null,
  payload    jsonb not null,
  created_at timestamptz not null default now(),
  read_at    timestamptz
);
create index notifications_user on notifications (user_id, created_at desc);

-- ---- sharing (design 01 section 10) ------------------------------------------------------------

create table share_links (
  id               uuid primary key,
  user_id          uuid not null,
  note_id          uuid not null,
  token_hash       bytea not null unique,
  created_at       timestamptz not null default now(),
  expires_at       timestamptz not null,
  revoked_at       timestamptz,
  last_accessed_at timestamptz,
  view_count       integer not null default 0,
  foreign key (user_id, note_id) references notes (user_id, id) on delete cascade
);
create index share_links_user on share_links (user_id);

-- ---- operational tables (design 01 section 11) -------------------------------------------------

create table ingest_events (
  bot_instance_id uuid not null,
  event_id        text not null,
  user_id         uuid not null references users(id) on delete cascade,
  received_at     timestamptz not null default now(),
  result          jsonb not null,
  primary key (bot_instance_id, event_id)
);
create index ingest_events_received on ingest_events (received_at);

create table unlinked_senders (
  bot_instance_id  uuid not null,
  external_user_id text not null,
  last_notice_at   timestamptz not null,
  primary key (bot_instance_id, external_user_id)
);

create table idempotency_keys (
  scope        text not null,
  key          text not null,
  request_hash bytea not null,
  response     jsonb not null,
  created_at   timestamptz not null default now(),
  primary key (scope, key)
);

create table audit_log (
  id          bigserial primary key,
  at          timestamptz not null default now(),
  actor_kind  text not null,
  actor_id    uuid,
  action      text not null,
  target_kind text,
  target_id   uuid,
  detail      jsonb not null default '{}'
);
create index audit_log_at on audit_log (at desc);

-- ---- row-level security on content tables (design README section 3, SEC-ISO-10) ---------------
-- FORCE applies the policies to the table owner too, so they also protect deployments that use
-- a single database role. Superusers and BYPASSRLS roles are the operator's responsibility.

-- +goose StatementBegin
do $$
declare t text;
begin
  foreach t in array array['pages','categories','notes','note_parts','note_part_versions','note_search',
                           'attachments','blobs','blob_chunks','reminders','notifications','changes']
  loop
    execute format('alter table %I enable row level security', t);
    execute format('alter table %I force row level security', t);
    execute format('create policy own_rows on %I using (user_id = nk_current_user()) with check (user_id = nk_current_user())', t);
  end loop;
end $$;
-- +goose StatementEnd

-- ---- privileges for the runtime role (design 01 section 12) ------------------------------------
-- Roles are created by the operator (deploy/sql/roles.sql). When nk_app exists it gets DML on
-- everything, and the audit log is append-only for it (SEC-AUD-2).

-- +goose StatementBegin
do $$
begin
  if exists (select 1 from pg_roles where rolname = 'nk_app') then
    grant usage on schema public to nk_app;
    grant select, insert, update, delete on all tables in schema public to nk_app;
    grant usage, select on all sequences in schema public to nk_app;
    grant execute on all functions in schema public to nk_app;
    revoke update, delete, truncate on audit_log from nk_app;
    alter default privileges in schema public grant select, insert, update, delete on tables to nk_app;
    alter default privileges in schema public grant usage, select on sequences to nk_app;
  end if;
end $$;
-- +goose StatementEnd

-- +goose Down
drop table audit_log, idempotency_keys, unlinked_senders, ingest_events, share_links, notifications,
  outbox_messages, bot_outbox, reminders, note_search, changes, note_part_versions, note_parts,
  user_storage, attachments, blob_chunks, blobs, notes, categories, pages, pairing_codes,
  external_identities, bot_credentials, bot_instances, auth_throttle, user_tokens, sessions, users;
drop function nk_notes_clear_position();
drop function nk_current_user();
