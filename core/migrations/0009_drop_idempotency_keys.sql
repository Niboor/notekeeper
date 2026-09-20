-- +goose Up
-- The Idempotency-Key header was designed but never implemented: creations take a client-chosen id, which
-- makes a retry safe (decision 62). The table was only ever purged.
drop table if exists idempotency_keys;

-- +goose Down
create table idempotency_keys (
  scope        text not null,
  key          text not null,
  request_hash bytea not null,
  response     jsonb not null,
  created_at   timestamptz not null default now(),
  primary key (scope, key)
);
