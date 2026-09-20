-- +goose Up
-- The address a user writes to in their chat app (for Matrix: @notekeeper:example.org). The bot reports it
-- itself with its heartbeat, so the pairing instructions can name the account to message.
alter table bot_instances add column address text;

-- +goose Down
alter table bot_instances drop column address;
