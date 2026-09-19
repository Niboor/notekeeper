-- Example role setup (docs/design/01-data-model.md section 12). Run once, as a superuser, before
-- `core migrate`. Passwords here are for development only; production passwords come from Secrets.
--
--   nk_app         Core runtime: DML under row-level security, no DDL, no BYPASSRLS.
--   nk_bot_matrix  Matrix bot runtime: owns only its own schema, no access to Core's tables.
--
-- The role that runs `core migrate` (in development the superuser) owns the tables.

create role nk_app login password 'nk_app_dev' nobypassrls nocreatedb nocreaterole;
create role nk_bot_matrix login password 'nk_bot_dev' nobypassrls nocreatedb nocreaterole;
create schema matrix_bot authorization nk_bot_matrix;
alter role nk_bot_matrix set search_path = matrix_bot;
