-- name: ClaimOutbox :many
update bot_outbox set state = 'claimed', lease_expires_at = @lease::timestamptz
where id in (
  select o.id from bot_outbox o
  left join users u on u.id = o.user_id
  where o.bot_instance_id = @instance
    and ((o.state = 'queued' and o.next_attempt_at <= @now::timestamptz)
         or (o.state = 'claimed' and o.lease_expires_at < @now::timestamptz))
    and (o.user_id is null or u.status = 'active')
  order by o.created_at
  limit @max_items
  for update of o skip locked)
returning *;

-- name: GetClaimedOutbox :one
select * from bot_outbox where id = $1 and bot_instance_id = $2 and state = 'claimed';

-- name: MarkOutboxDelivered :execrows
update bot_outbox set state = 'delivered', finished_at = $3, lease_expires_at = null
where id = $1 and bot_instance_id = $2 and state = 'claimed';

-- name: RequeueOutbox :execrows
update bot_outbox set state = 'queued', attempts = attempts + 1, next_attempt_at = $3, lease_expires_at = null, failure_reason = $4
where id = $1 and bot_instance_id = $2 and state = 'claimed';

-- name: FailOutbox :execrows
update bot_outbox set state = 'failed', finished_at = $3, lease_expires_at = null, failure_reason = $4
where id = $1 and bot_instance_id = $2 and state = 'claimed';

-- name: InsertOutboxMessage :exec
insert into outbox_messages (bot_instance_id, conversation_id, message_id, outbox_id) values ($1, $2, $3, $4)
on conflict do nothing;

-- name: StaleOutbox :many
select id, user_id, kind from bot_outbox
where state in ('queued', 'claimed') and created_at < $1
order by created_at limit 200;

-- name: ExpireOutboxItem :execrows
update bot_outbox set state = 'expired', finished_at = $2, lease_expires_at = null, failure_reason = 'expired'
where id = $1 and state in ('queued', 'claimed');

-- name: InsertNotification :exec
insert into notifications (id, user_id, kind, payload) values ($1, $2, $3, $4);

-- name: OutboxDepth :many
select bot_instance_id, state, count(*)::bigint as n from bot_outbox group by bot_instance_id, state;
