-- Example role setup (docs/design/01-data-model.md section 12). Run once, as a superuser, connected to
-- the application database (the bot's schema is created in it), before `core migrate`. The passwords here are
-- for development only and published in the repository: Core and the bot refuse to start with them unless
-- NK_ALLOW_DEV_CREDENTIALS=true. For a real installation change them (alter role ... password '...') right
-- after running this script, and put the new ones in the Secrets.
--
--   nk_migrate     Owns the schema and runs `core migrate` (and River's migrations); DDL only in practice.
--   nk_app         Core runtime: DML under row-level security, no DDL, no BYPASSRLS.
--   nk_bot_matrix  Matrix bot runtime: owns only its own schema, no access to Core's tables.
--
-- The migration role is deliberately NOT a superuser: row-level security is forced on the content
-- tables, so a data migration that touches them must set app.user_id per user (docs/design/01 section 12).
-- Running migrations as a superuser would hide that in development.

do $$ begin
  if not exists (select 1 from pg_roles where rolname = 'nk_migrate') then
    create role nk_migrate login password 'nk_migrate_dev' nobypassrls;
  end if;
end $$;
-- The database name is not known here, so the grant uses current_database().
do $$ begin execute format('grant create, connect on database %I to nk_migrate', current_database()); end $$;
alter schema public owner to nk_migrate;

do $$ begin
  if not exists (select 1 from pg_roles where rolname = 'nk_app') then
    create role nk_app login password 'nk_app_dev' nobypassrls nocreatedb nocreaterole;
  end if;
  if not exists (select 1 from pg_roles where rolname = 'nk_bot_matrix') then
    create role nk_bot_matrix login password 'nk_bot_dev' nobypassrls nocreatedb nocreaterole;
  end if;
end $$;
create schema if not exists matrix_bot authorization nk_bot_matrix;
alter role nk_bot_matrix set search_path = matrix_bot;
