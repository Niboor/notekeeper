-- +goose Up
-- The reminder scheduler claims due reminders across users before it knows whose they are, so it
-- needs to see the reminders table without a user context. The policy is narrow: it opens that one
-- table, for the duration of one transaction that sets app.scheduler, and only for the claim (SEC-ISO-10).
-- +goose StatementBegin
create function nk_scheduler() returns boolean language sql stable as
$$ select coalesce(nullif(current_setting('app.scheduler', true), ''), 'off') = 'on' $$;
-- +goose StatementEnd

create policy scheduler_claim on reminders using (nk_scheduler()) with check (nk_scheduler());

-- +goose Down
drop policy scheduler_claim on reminders;
drop function nk_scheduler();
