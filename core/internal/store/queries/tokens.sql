-- name: DeleteUserTokens :exec
delete from user_tokens where user_id = $1 and kind = $2;

-- name: InsertUserToken :exec
insert into user_tokens (id, user_id, kind, token_hash, expires_at, created_by)
values ($1, $2, $3, $4, $5, $6);

-- name: ConsumeUserToken :one
update user_tokens set used_at = $3
where token_hash = $1 and kind = $2 and used_at is null and expires_at > $3
returning user_id;

-- name: GetThrottle :one
select failures, blocked_until, updated_at from auth_throttle where key = $1;

-- name: RecordThrottleFailure :one
insert into auth_throttle (key, failures, blocked_until, updated_at)
values ($1, 1, null, $2)
on conflict (key) do update set
  failures = case when auth_throttle.updated_at < $3 then 1 else auth_throttle.failures + 1 end,
  updated_at = $2
returning failures;

-- name: ResetThrottle :exec
delete from auth_throttle where key = $1;

-- name: SetThrottleBlock :exec
update auth_throttle set blocked_until = $2 where key = $1;

-- name: InsertAudit :exec
insert into audit_log (actor_kind, actor_id, action, target_kind, target_id, detail)
values ($1, $2, $3, $4, $5, $6);
