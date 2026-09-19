-- +goose Up
-- Extensions used by search (docs/design/01-data-model.md section 8). Both are trusted
-- extensions in PostgreSQL 13+, so a database owner can create them.
create extension if not exists unaccent;
create extension if not exists pg_trgm;

-- unaccent() is not IMMUTABLE, which indexes and generated data require; this wrapper pins
-- the dictionary so it is safe to use in them.
-- +goose StatementBegin
create function nk_unaccent(text) returns text
  language sql immutable parallel safe
  as $$ select public.unaccent('public.unaccent', $1) $$;
-- +goose StatementEnd

-- +goose Down
drop function nk_unaccent(text);
