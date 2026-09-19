# 6. Matrix bot

Covers MX-*, BOT-B1..B7, SEC-MX-*. The bot is a Go program using `mautrix-go`. It contains platform logic only: protocol, encryption, message formats. Grouping, edits, commands and wording live in Core (BOT-B1).

## 1. Structure

```
bots/matrix/
  cmd/matrix-bot/        main: config, startup, health/metrics server
  internal/leader/       Postgres advisory-lock leader election
  internal/matrix/       mautrix client, CryptoHelper, sync handling, DM checks
  internal/normalise/    Matrix event → bot API event (parts, relations, captions, HTML→Markdown)
  internal/core/         generated bot API client (bots/sdk) + upload/download helpers
  internal/outbox/       outbox long-poll loop, delivery to Matrix
  internal/store/        the bot's own tables (schema matrix_bot)
```

State stored by the bot (own schema, own database role, SEC-OPS-5): mautrix's crypto tables (Olm accounts and sessions, Megolm sessions, device tracking), the sync token, and a small `rooms` table (room flags such as "ignored: more than two members"). Nothing else is durable: no message content, no user data beyond what Matrix state already holds (BOT-B5, SEC-MX-3).

## 2. Startup and single-active-instance

1. Read configuration and Secrets: homeserver URL, bot account credentials, the **pickle key** for encrypting device keys at rest (MX-N1), Core URL and bot key, database URL.
2. Connect to PostgreSQL and take a **session-level advisory lock** `pg_try_advisory_lock(hash('matrix-bot:' || instance name))` on a **dedicated connection** that stays open. If the lock is not obtained the process stays passive (not ready) and retries every 5 seconds. If the lock connection ever drops the process **exits immediately** and Kubernetes restarts it: a process that might have lost the lock must not keep syncing (MX-N2, SEC-MX-4).
3. Deployment strategy is `Recreate` with one replica, so a rolling update never runs two instances; the advisory lock is the safety net if someone scales it anyway.
4. Log in (access token from the Secret if present, else password login stored device), open the mautrix `CryptoHelper` with the SQL crypto store on PostgreSQL and the pickle key. Olm is **libolm via cgo**, as in the mautrix bridges (tech-stack §4).
5. Start the sync loop, the outbox loop and the heartbeat (`POST /heartbeat` every 30 s).

## 3. Sync, ordering and never losing a message

The sync handler is **synchronous with Core**: for every event in a sync response the bot calls Core and only returns once Core has answered (or the event is classified as not retryable). mautrix persists the next batch token only after a sync response has been fully processed, so:

- Core down or slow → the handler retries with exponential backoff and the sync loop stalls, the token does not advance, and nothing is lost; Matrix retains the messages (NFR-R1, BOT-B2). Key sharing for encrypted rooms resumes as soon as Core is back.
- Bot crash mid-batch → the batch is replayed from the stored token on restart; Core deduplicates by event id (BOT-7).
- **Not retryable** results (Core answered `rejected`, or `4xx` for a malformed event): the bot reports to the user if `reply_text` is set, records a counter and moves on. It never blocks forever on one bad event.

Events are handled in arrival order, which for one room is the platform order Core expects (BOT-9); with one sync goroutine there is no reordering.

*Verify at implementation time:* that mautrix's syncer runs handlers to completion before saving the token in the version pinned by `go.mod`; a contract test in the bot's integration suite kills the process mid-batch and asserts no loss and no duplication.

## 4. Rooms, invites and DMs (MX-1, MX-2, SEC-MX-1)

- **Invites:** accept only invites whose `is_direct` flag is set, rate limited; decline others. After joining, the bot checks the room has exactly two joined members (the bot and the inviter).
- **Third member joins:** the bot sets `rooms.ignored = true`, stops processing that room (no ingest, no deliveries), and sends a single `m.notice` explaining why (MX-2).
- **Unlinked senders** are handled by Core's response (`identity_unlinked` and its `reply_text`, rate limited by Core).
- **Own messages** and `m.notice` events are ignored (MX-4).
- The bot reports the DM's room id in every event (`conversation`), so Core keeps the reminder-delivery conversation current (BOT-12).

## 5. Normalisation (MX-4, MX-5, MX-6)

| Matrix | Bot API |
|---|---|
| `m.text`, `m.emote` | text part; `formatted_body` HTML converted to Markdown through an allowlist (paragraphs, emphasis, code, lists including task lists, links, quotes); everything else stripped; falls back to `body` |
| `m.image`, `m.file`, `m.audio`, `m.video` | attachment part; a caption is present when `filename` differs from `body` (then `body` becomes the text part, `part_index` 0, the file part 1) |
| `m.location` | text part with a `geo:` link |
| anything else | `unsupported` part with a description; never dropped |
| `m.replace` relation | `message_edited` with the original message id |
| redaction | `message_deleted` |
| `m.in_reply_to`, `m.thread` | `relates_to.reply_to` and `.thread` |

Encrypted media (`file` with `key`, `iv`, `hashes`) is downloaded from the configured homeserver via its `mxc://` URI only (SEC-CNT-8) and decrypted. The size is checked against `info.size` and Core's limit **before** downloading: too large → an `attachment_failed` part with reason `too_large`, no download. The decrypted stream is written to Core with `PUT /uploads/{id}`; where the library decrypts in place, memory is bounded by the maximum attachment size (25 MiB by default) times a small concurrency limit (4).

## 6. Commands and feedback

A command is recognised only when the message body **begins with** `!` and a known command name (MX-8); the bot forwards `POST /commands`. Core answers with `reply_text` and/or `react`:

- **Success feedback:** an `m.reaction` (default ✅) on the source event (MX-7, BOT-B3), unless the user muted it.
- **Errors and command replies:** an `m.notice` with Core's `reply_text`. Notices are ignored by the bot's own handler, so it never reacts to itself.

## 7. Outbox loop (reminders, notices, lifecycle)

A goroutine long-polls `GET /outbox?wait=25` and processes items sequentially per conversation:

| Kind | Bot action |
|---|---|
| `reminder`, `notice` | **Re-check the room** immediately before sending: exactly two joined members, the bot and the linked user, and not flagged ignored; otherwise report `failed_permanent` (SEC-MX-2). Send the text as `m.notice`/`m.text` with a Matrix transaction id derived from the outbox item id (idempotent, BOT-13). For reminders, then download each attachment through the outbox attachment endpoint and send it as Matrix media (encrypted upload in encrypted rooms); if that fails, the text already names the file and links to the note (MX-12, CORE-R6). Report `delivered` with the event ids |
| `lifecycle` | Leave and forget the room (MX-13), delete the room's rows and the identity's mapping from the bot's tables, drop the room's Megolm sessions where the store API allows, then report `delivered` (BOT-B7) |

Send failures that look transient (network, homeserver 5xx, rate limit `M_LIMIT_EXCEEDED` with its retry hint) report `failed_transient`; a missing room, a departed user or a forbidden send report `failed_permanent`.

## 8. Encryption operations

- **Device identity** lives in the Postgres crypto store, encrypted with the pickle key; losing the pod loses nothing (MX-N1). Losing the database *does* lose the device: the bot would log in as a new device, and only messages sent afterwards are decryptable. This is documented as the accepted failure mode, together with backing up the `matrix_bot` schema with the rest of PostgreSQL.
- **Trust model:** trust on first use for the linked user's devices; new devices seen for a linked identity are logged (structured, without content) so unexpected ones can be noticed (SEC-MX-6). Users are told in the documentation that the bot's device may show as unverified in Element.
- **Secrets:** homeserver credentials and the pickle key come only from Kubernetes Secrets, never from the database or Core, and are never logged (SEC-MX-3, SEC-MX-5).

## 9. Operations

`/healthz` (process up), `/readyz` (leader lock held, last sync succeeded within 2 minutes, Core reachable), `/metrics` (sync lag, events by result, outbox items by outcome, decryption failures; BOT-B6, NFR-O2). Structured logs contain room and event identifiers only, never message text or filenames (SEC-DATA-1).

## 10. Testing

Integration and end-to-end tests run against Synapse in a container ([08](08-deployment-and-testing.md) §5): a second mautrix client plays the Element user and creates an **encrypted** DM. Cases: text note, file with caption, photo burst, reply-append, edit, redaction, unlinked sender, third member joins, invite to a non-DM, reminder with attachment, lifecycle purge, restart mid-batch (no loss or duplication), Core outage during sync, and two bot instances competing for the lock (only one becomes ready).
