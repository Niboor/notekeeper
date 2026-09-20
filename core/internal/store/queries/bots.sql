-- name: CreateBotInstance :one
insert into bot_instances (id, type, name, identity_domain) values ($1, $2, $3, $4) returning *;

-- name: GetBotInstance :one
select * from bot_instances where id = $1;

-- name: ListBotInstances :many
select * from bot_instances order by name;

-- name: SetBotInstanceStatus :execrows
update bot_instances set status = $2 where id = $1;

-- name: TouchBotInstance :exec
-- The address is optional: a heartbeat without one keeps what is stored.
update bot_instances set last_seen_at = $2, address = coalesce(sqlc.narg('address'), address) where id = $1;

-- name: CreateBotCredential :one
insert into bot_credentials (id, bot_instance_id, client_id, secret_hash, scopes)
values ($1, $2, $3, $4, $5) returning *;

-- name: ListBotCredentials :many
select id, client_id, scopes, created_at, disabled_at from bot_credentials
where bot_instance_id = $1 order by created_at;

-- name: DisableBotCredential :execrows
update bot_credentials set disabled_at = $3 where id = $1 and bot_instance_id = $2 and disabled_at is null;

-- name: GetBotCredentialByClientID :one
select c.id as credential_id, c.secret_hash, c.scopes, c.disabled_at,
       i.id as instance_id, i.type, i.name, i.identity_domain, i.status
from bot_credentials c join bot_instances i on i.id = c.bot_instance_id
where c.client_id = $1;

-- name: GetIdentityByExternal :one
select * from external_identities where bot_type = $1 and external_user_id = $2;

-- name: GetIdentity :one
select * from external_identities where id = $1;

-- name: ListUserIdentities :many
select e.id, e.bot_instance_id, i.name as instance_name, e.bot_type, e.external_user_id,
       e.conversation_id, e.reminder_target, e.linked_at
from external_identities e join bot_instances i on i.id = e.bot_instance_id
where e.user_id = $1 order by e.linked_at;

-- name: CountUserIdentities :one
select count(*) from external_identities where user_id = $1;

-- name: InsertIdentity :one
insert into external_identities (id, user_id, bot_instance_id, bot_type, external_user_id, conversation_id, reminder_target, linked_at)
values ($1, $2, $3, $4, $5, $6, $7, $8) returning *;

-- name: DeleteIdentity :execrows
delete from external_identities where id = $1 and user_id = $2;

-- name: UpdateIdentityConversation :exec
update external_identities set conversation_id = $2 where id = $1 and conversation_id is distinct from $2;

-- name: SetReminderTarget :execrows
update external_identities set reminder_target = $3 where id = $1 and user_id = $2;

-- name: InsertPairingCode :exec
insert into pairing_codes (id, user_id, code_hash, bot_instance_id, bot_type, expires_at)
values ($1, $2, $3, $4, $5, $6);

-- name: CountActivePairingCodes :one
select count(*) from pairing_codes where user_id = $1 and used_at is null and expires_at > $2;

-- name: LookupPairingCode :one
select id, user_id, bot_instance_id, bot_type from pairing_codes
where code_hash = $1 and used_at is null and expires_at > $2;

-- name: ConsumePairingCode :one
update pairing_codes set used_at = $2
where id = $1 and used_at is null and expires_at > $2
returning user_id;

-- name: NoticeDue :one
insert into unlinked_senders (bot_instance_id, external_user_id, last_notice_at)
values ($1, $2, $3)
on conflict (bot_instance_id, external_user_id) do update set last_notice_at = $3
  where unlinked_senders.last_notice_at < $4
returning true;

-- name: InsertOutbox :exec
insert into bot_outbox (id, bot_instance_id, kind, user_id, external_user_id, conversation_id, payload, next_attempt_at)
values ($1, $2, $3, $4, $5, $6, $7, $8);
