# 5. Realtime, background jobs, outbox and reminders

Covers CORE-S1..S5, CORE-R*, BOT-11..BOT-16, NFR-D2, NFR-D1.

## 1. Change feed and notification

Every write transaction appends `changes` rows (see [01](01-data-model.md) §5) with the user's next `seq`, and calls `pg_notify('nk_changes', '<user_id>:<seq>')` inside the same transaction. PostgreSQL delivers the notification only on commit and caps payloads at 8000 bytes, so the payload is **identifiers only, never content**; consumers read the change rows.

`nk_sessions` payloads are `s:<session id>` (one session) or `u:<user id>` (every session of a user).

Each replica runs one **listener goroutine** on a dedicated `pgx.Conn`, outside the pool (`LISTEN nk_changes; LISTEN nk_sessions; LISTEN nk_outbox`). It never shares a pooled connection, which is what keeps the design correct if a pooler is introduced later (tech-stack §3.3). On connection loss it reconnects, re-issues `LISTEN`, and triggers a catch-up for every connected stream, because notifications sent while disconnected are lost. As a safety net every stream also polls its user's `change_seq` every 30 seconds.

## 2. SSE stream (`GET /api/v1/events`)

- Authenticated like any user API request. The stream is registered in an in-process map `user_id → streams`.
- **First event:** a fresh connection (no `Last-Event-ID`) starts with `event: hello` and `data: {"seq": <current>}`; the client refetches its views on it, which closes the gap between its initial fetch and the subscription (decision 45).
- **Events:** `event: change`, `id: <seq>`, `data: {"entity_type","entity_id","op","version"}`. The client applies or refetches by entity (see [07](07-web-app.md) §4).
- **Catch-up:** a client connects with `Last-Event-ID` (the browser sends it on automatic reconnect). The server replays `changes` after that seq. If the id is older than retention it sends `event: resync` and the client refetches its views.
- **Heartbeat:** a comment line every 25 seconds keeps proxies from closing the stream.
- **Cap:** at most 10 streams per user **per Core replica**; with N replicas a user can hold up to 10N.
- **Resync:** a client whose `Last-Event-ID` (or `cursor`) cannot be served from the change feed gets `event: resync` (HTTP 410 on the JSON endpoint): the oldest kept change is beyond it, nothing is kept although changes were made since, or it is ahead of the feed (a restored database) (CR-040).
- **Expiry:** the stream is closed when its access token expires (15 minutes); `EventSource` reconnects automatically with the refreshed cookie, so this is invisible (SEC-ISO-5). It is closed immediately when `nk_sessions` announces the session's revocation, and on server shutdown after sending `event: reconnect`.
- **Limits:** at most 10 concurrent streams per user (SEC-API-4).
- Because the stream only carries change *notifications* for one user's own rows, subscribing to another user's data is structurally impossible (SEC-ISO-5).

`GET /changes?cursor=` is the same data as paginated JSON for clients that do not hold a stream (Android later, AND-3).

## 3. Background jobs (River)

River (a Postgres-backed queue built on `SKIP LOCKED`) runs inside Core. Jobs are enqueued transactionally with the change that causes them (so a reminder cannot be created without its schedule being visible), and any replica may run them. Periodic jobs are leader-elected by River, so two replicas do not double-run them.

| Job | Trigger | Purpose |
|---|---|---|
| `FireDueReminders` | every 10 s | Fire due reminders (§5) |
| `ExpireOutbox` | every 5 min | Expire undelivered items older than 7 days; make an in-app notification (§4.4) |
| `ReleaseStaleUploads` | every 10 min | Release quota reservations and delete incomplete blobs and unlinked attachments older than 1 hour |
| `PurgeShareLinks` | hourly | Delete links expired more than 7 days ago |
| `PurgeSessions` | daily | Delete sessions expired or revoked more than 30 days ago; old `auth_throttle`, `idempotency_keys`, `ingest_events`, `changes`, `unlinked_senders`, read notifications (retention table in [01](01-data-model.md) §13) |
| `DeleteUser` | on request | Complete user deletion ([03](03-auth.md) §4.3) |

Each job is idempotent and bounded (batches), and exports Prometheus metrics for duration, failures and lag (NFR-O2).

## 4. The bot outbox (Core → bot; BOT-11..BOT-16)

The outbox is part of the **v1 core contract**, not of the reminder feature: it carries the lifecycle notices that make deletion and unlinking complete (BOT-16, AUTH-U9) and the security notices (AUTH-U11), and it is also the transport for reminders. Reminders are a Should; the outbox is a Must. Nothing in this section depends on §5.

### 4.1 Items

`bot_outbox` rows have a `kind`:

| Kind | Addressed to | Content |
|---|---|---|
| `lifecycle` | a bot instance, an external identity, a conversation. **No user reference** | `{reason: unlinked \| user_deleted}`: the bot purges everything for that identity (BOT-B7) |
| `notice` | a user's linked identity | Rendered English text (security notices, delivery failures) |
| `reminder` | a user's reminder-target identity | Rendered text, note link, attachment references (§5.3) |

Core renders all user-visible wording (BOT-8); bots send it as given.

### 4.2 Claim, deliver, acknowledge

`GET /bot/v1/outbox?wait=25&limit=10`:

```sql
update bot_outbox set state = 'claimed', lease_expires_at = now() + interval '60 seconds'
where id in (
  select o.id from bot_outbox o
  left join users u on u.id = o.user_id
  where o.bot_instance_id = $1
    and ((o.state = 'queued' and o.next_attempt_at <= now())
         or (o.state = 'claimed' and o.lease_expires_at < now()))
    and (o.user_id is null or u.status = 'active')          -- suspended for disabled users (SEC-BOT-13)
  order by o.created_at
  limit $2
  for update of o skip locked)
returning *;
```

If nothing is available the request waits up to `wait` seconds, woken by `NOTIFY nk_outbox` for that instance (with a 5-second poll as fallback), then returns an empty list. The bot processes each item and calls `POST /outbox/{id}/result`:

- `delivered` (with the platform message ids, stored in `outbox_messages` so replies can be mapped, BOT-13) → `state = 'delivered'`.
- `failed_transient` → `attempts + 1`, `state = 'queued'`, `next_attempt_at = now + backoff` (1, 2, 5, 15 minutes, then hourly).
- `failed_permanent` (room gone, user left) → `state = 'failed'`, and an in-app `delivery_failed` notification for the user.
- No result before the lease expires (bot died) → offered again; a lease expiry does **not** count as an attempt, so a bot outage never exhausts retries (CORE-R4).

### 4.3 Guarantees

At-least-once. The item id is the idempotency key the bot uses for the platform send (for Matrix, the transaction id, BOT-13), so a crash between sending and acknowledging does not produce a visible duplicate.

### 4.4 Expiry

`ExpireOutbox` marks items older than 7 days as `expired` and creates a `delivery_failed` notification, so a reminder that could never reach a chat still reaches the user in the app.

## 5. Reminders (Should)

### 5.1 Firing

`FireDueReminders` runs every 10 seconds (well inside the 60-second p95 target, CORE-R5). It works in two steps so that the claim is **durable** (a bare `FOR UPDATE SKIP LOCKED` only holds until its transaction ends) and so that the lock order stays *user row first* ([README](README.md) §3).

**Step 1: claim** with a lease, the same pattern as the outbox:

```sql
update reminders set claimed_until = now() + interval '60 seconds'
where id in (
  select r.id from reminders r
  where r.state = 'pending' and r.due_at <= now()
    and (r.claimed_until is null or r.claimed_until < now())
    and exists (select 1 from users u where u.id = r.user_id and u.status = 'active')
  order by r.due_at limit 100
  for update skip locked)
returning id, user_id;
```

Two replicas can never claim the same reminder (CORE-R5). This transaction commits immediately. It runs through `Store.InSchedulerTx`, which sets `app.scheduler`; migration 0007 adds a narrow row-level-security policy that lets exactly that setting see the `reminders` table across users (every other table stays invisible without a user context, decision 55). The note's state is therefore not part of the claim; it is checked in step 2 under the owner's context.

A failing reminder does not hold the others back: `FireDue` carries on with the rest of the claimed batch, sets the failing one aside (`claimed_until` pushed ten minutes ahead, so it is not claimed first in every cycle) and returns the collected errors, which count as a failed job run for the alerts (CR-020).

**Step 2: fire each claimed reminder in its own transaction**, which first locks the owner's user row (`FOR UPDATE`, taking the next `change_seq`), then **re-reads the reminder** (`where id = $1 and state = 'pending' and due_at <= now()`); if the user edited, snoozed, dismissed or deleted it in the meantime, the row no longer qualifies and nothing is sent. Then:

1. Build the delivery content once: note text excerpt (first 300 characters), note link, attachment list, `late = now() − due_at > 5 minutes`.
2. For each **reminder-target identity** (CORE-R3) insert a `reminder` outbox item; insert an in-app notification; if there is no target identity, only the notification.
3. One-off: set `state = 'fired'` and `last_fired_at = now()` (it stays visible on the note as "reminded …", CORE-R8; the user may snooze or mark it done). Recurring: compute the next occurrence **after now** from `rrule` in the user's profile time zone (the reminder's `tz` records the zone it was created in and is informational; a recurring reminder follows the profile, decision 63) (missed occurrences collapse into this one late delivery, CORE-R10), set `due_at` to it, keep `pending`, set `last_fired_at`.

### 5.2 Dismissal and restore (CORE-R7)

Dismissing a note sets its `pending` reminders to `suspended`. Restoring re-arms those whose `due_at` is in the future (recurring: next occurrence after now); reminders that came due while dismissed are marked `cancelled` without sending. There is no join on the note in the claim; step 2 of firing checks the note's state under the owner's context, which is the guard.

Reminders that are waiting in the outbox are dropped with what they belong to: deleting a reminder, marking it done, deleting its note for good, unlinking the chat or switching reminders off for that chat cancels the queued items of that reminder or chat (`failure_reason = 'cancelled'`), so text and file names of something the user removed are not sent afterwards (CR-012).

### 5.3 Reminder content and attachments

The outbox `payload` lists the note's attachments as `{id, filename, media_type, size}`. The bot downloads each through `GET /bot/v1/outbox/{id}/attachments/{attachmentId}`, which only serves attachments referenced by an item claimed by that instance (BOT-15, SEC-BOT-3), and re-sends them into the chat; if that fails the text falls back to filename plus note link (CORE-R6). Note text is sent as inert content: no mentions, formatting escaped, no leading command prefix acted upon (SEC-CNT-7).

### 5.4 Snooze, done, replies

**Notices.** Security notices (AUTH-U11) are written by package `notify` inside the transaction of the event they report (identity linked or unlinked, share link created) or right after it commits (sign-in, password change, activation link): an in-app `security` notification plus one `notice` outbox item per linked chat. A brand-new account's first activation and the chat being unlinked are not announced to themselves. Users mute `session` and `share` through `settings.muted_notices`; other keys are ignored.

App: `POST /reminders/{id}/snooze` sets `due_at` and `pending`; `:done` sets `done`. `snooze` takes an absolute time chosen by the client (the quick options are computed in the browser's zone); `done` on a repeating reminder skips this occurrence and stays armed, and clearing it ends the repetition (CORE-R10). Chat: `!snooze` and `!done` as a reply to a reminder message resolve through `outbox_messages(bot, conversation, message_id) → outbox → reminder` and do the same (CORE-R11c).

## 6. Failure scenarios

| Situation | Behaviour |
|---|---|
| Two replicas run `FireDueReminders` at once | `SKIP LOCKED` gives each reminder to exactly one (CORE-R5) |
| Core crashes mid-fire | The firing transaction rolls back; the 60-second claim lease expires and the reminder fires on a later tick (so a crash can delay a reminder by up to a minute) |
| Bot is down at the due time | The item waits; when the bot returns it is claimed and sent, flagged `late` (CORE-R4) |
| Bot sent the message but crashed before acknowledging | Lease expires, item re-offered; the transaction id makes the Matrix send idempotent |
| Note is dismissed after the item was queued | The item is still delivered: it was already due when queued; dismissal only affects reminders not yet fired |
| User is disabled | Outbox items for the user are not claimed until re-enabled (SEC-BOT-13) |
