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
4. Open the mautrix `CryptoHelper` with the SQL crypto store on PostgreSQL (via `dbutil` and the `pgx` driver) and the pickle key, with `LoginAs` set: the helper looks up the device id it stored, logs in again as **that same device**, and restarts therefore keep the device identity (verified in the M0 spike). Olm is **libolm via cgo**, as in the mautrix bridges (tech-stack §4).
5. Start the sync loop, the outbox loop and the heartbeat (`POST /heartbeat` every 30 s).

## 3. Sync, ordering and never losing a message

The sync handler is **synchronous with Core**: `DefaultSyncer` dispatches every event to its handlers one after another in the sync goroutine (verified in the M0 spike), and each handler calls Core and only returns once Core has answered or the event is classified as not retryable.

**mautrix-go saves its sync token before it processes the response** (`SyncWithContext`: "Save the token now before processing it"). Used as is, a crash mid-batch, or a handler that gives up while Core is down, would skip those events for good and break NFR-R1 and BOT-B2. The bot therefore uses `internal/syncack`: a `Syncer` wrapper and a `Store` wrapper that swallow the early `SaveNextBatch` and **commit the token only after `ProcessResponse` returned without error**. The token itself lives in the crypto store, so it is in PostgreSQL.

- Core down or slow: the handler retries with exponential backoff, the sync loop stalls, the token is not committed, nothing is lost; Matrix retains the messages. Key sharing for encrypted rooms resumes as soon as Core is back.
- Handler gives up (or panics) or the process dies mid-batch: `ProcessResponse` reports an error, the sync loop ends, the pod restarts, and the batch is replayed from the last committed token; Core deduplicates by event id (BOT-7).
- **Not retryable** results (Core answered `rejected`, or `4xx` for a malformed event): the bot reports to the user if `reply_text` is set, records a counter and moves on. It never blocks forever on one bad event.

Events are handled in arrival order, which for one room is the platform order Core expects (BOT-9); with one sync goroutine there is no reordering.

*Guarded by tests:* `TestSyncTokenCommit` in `bots/matrix/internal/e2eetest` runs the same "handler fails mid-batch, pod restarts" scenario twice against Synapse: with mautrix's default behaviour the message is **lost** (this documents the library behaviour and will fail loudly if a future mautrix version changes it), and with `syncack` it is **redelivered**. Unit tests in `internal/syncack` cover the wrapper.

## 4. Rooms, invites and DMs (MX-1, MX-2, SEC-MX-1)

- **Invites:** accept only invites whose `is_direct` flag is set, rate limited; decline others. After joining, the bot checks the room has exactly two joined members (the bot and the inviter).
- **Third member joins:** the bot sets `rooms.ignored = true`, stops processing that room (no ingest, no deliveries), and sends a single `m.notice` explaining why (MX-2).
- **Unlinked senders** are handled by Core's response (`identity_unlinked` and its `reply_text`, rate limited by Core).
- **Own messages** and `m.notice` events are ignored (MX-4).
- The bot reports the DM's room id in every event (`conversation`), so Core keeps the reminder-delivery conversation current (BOT-12).

## 5. Normalisation (MX-4, MX-5, MX-6)

| Matrix | Bot API |
|---|---|
| `m.text`, `m.emote` | text part taken from `body`, which carries the Markdown the user typed (decision 42); `formatted_body` is not converted. For replies the quoted fallback block that older clients prepend is stripped |
| `m.image`, `m.file`, `m.audio`, `m.video` | attachment part; a caption is present when `filename` differs from `body` (then `body` becomes the text part, `part_index` 0, the file part 1) |
| `m.location` | text part with a `geo:` link |
| anything else | `unsupported` part with a description; never dropped |
| `m.replace` relation | `message_edited` with the original message id |
| redaction | `message_deleted` |
| `m.in_reply_to`, `m.thread` | `relates_to.reply_to` and `.thread` |

Encrypted media (`file` with `key`, `iv`, `hashes`) is downloaded from the configured homeserver via its `mxc://` URI only (SEC-CNT-8) and decrypted. The size is checked against `info.size` and Core's limit **before** downloading: too large → an `attachment_failed` part with reason `too_large`, no download. The sender's `info.size` is only used to refuse early and is never trusted: the request length is the `Content-Length` of the homeserver's answer (a download without one is refused, reason `size_unknown`), which for the AES-CTR encryption used by Matrix equals the plaintext length, and it is checked against the limit again. The decrypted stream is written to Core with `PUT /uploads/{id}` using mautrix's `DecryptStream`, so bot memory does not depend on file size beyond the homeserver download itself. The upload id is derived from the Matrix event id and part index, so a retry or replay reuses it. A file is tried three times (`sdk.UploadAttempts`); a chat is never stalled by one file, unlike a message, which is retried until Core answers (NFR-R1). Whatever fails becomes an `attachment_failed` part with a reason (`too_large`, `download_failed`, `size_unknown`, `corrupt`, `unsupported_encryption`, `quota_exceeded`, `upload_failed`), and the message's text still lands (CORE-A9). The maximum is `NK_MAX_ATTACHMENT_BYTES` (default 25 MiB, as in Core). Two library details verified in the M0 spike: call `PrepareForDecryption()` **before** `DecryptStream` (the cipher is built eagerly and panics on an unprepared file), and the file's SHA-256 is only verified when the stream is **closed**, so the bot must check the error from `Close()` and abort the Core upload (a short or cancelled request leaves an incomplete blob that the janitor removes) when it fails.

## 6. Commands and feedback

A command is recognised only when the message body **begins with** `!` and a known command name (MX-8); the bot forwards `POST /commands`. Core answers with `reply_text` and/or `react`:

- **Success feedback:** an `m.reaction` (default ✅) on the source event (MX-7, BOT-B3), unless the user muted it.
- **Errors and command replies:** an `m.notice` with Core's `reply_text`. Notices are ignored by the bot's own handler, so it never reacts to itself.

## 7. Outbox loop (reminders, notices, lifecycle)

A goroutine long-polls `GET /outbox?wait=20` (shorter than the HTTP client timeout) and processes items sequentially per conversation:

| Kind | Bot action |
|---|---|
| `reminder`, `notice` | **Re-check the room** immediately before sending: exactly two joined members, the bot and the linked user, and not flagged ignored; otherwise report `failed_permanent` (SEC-MX-2). Send the text as `m.notice`/`m.text` with a Matrix transaction id derived from the outbox item id (idempotent, BOT-13). For reminders, then download each attachment through the outbox attachment endpoint and send it as Matrix media (encrypted upload in encrypted rooms); if that fails, the text already names the file and links to the note (MX-12, CORE-R6). Report `delivered` with the event ids |
For a reminder that lists files the bot then fetches each one through `GET /outbox/{id}/attachments/{attachmentId}` (only while the claim lasts, only files named in the payload, BOT-15) and sends it as Matrix media with the transaction id `<item id>-f<index>`, encrypting the upload when the room is encrypted; a file that cannot be fetched or sent becomes a line naming it with the note link, and never changes the delivered result (MX-12, CORE-R6).

| `lifecycle` | For an unlink, first a short goodbye notice; then leave and forget the room (MX-13), delete the room's rows and the identity's mapping from the bot's tables, drop the room's Megolm sessions where the store API allows, then report `delivered` (BOT-B7) |

Send failures that look transient (network, homeserver 5xx, rate limit `M_LIMIT_EXCEEDED` with its retry hint) report `failed_transient`; a missing room, a departed user or a forbidden send report `failed_permanent`.

## 8. Encryption operations

- **Device identity** lives in the Postgres crypto store, encrypted with the pickle key; losing the pod loses nothing (MX-N1). Losing the database *does* lose the device: the bot would log in as a new device, and only messages sent afterwards are decryptable. This is documented as the accepted failure mode, together with backing up the `matrix_bot` schema with the rest of PostgreSQL.
- **Trust model:** trust on first use for the linked user's devices; new devices seen for a linked identity are logged (structured, without content) so unexpected ones can be noticed (SEC-MX-6). Users are told in the documentation that the bot's device may show as unverified in Element.
- **Secrets:** homeserver credentials and the pickle key come only from Kubernetes Secrets, never from the database or Core, and are never logged (SEC-MX-3, SEC-MX-5).

## 9. Operations

`/healthz` (process up), `/readyz` (leader lock held, last sync succeeded within 2 minutes, Core reachable), `/metrics` (sync lag, events by result, outbox items by outcome, decryption failures; BOT-B6, NFR-O2). Structured logs contain room and event identifiers only, never message text or filenames (SEC-DATA-1).

## 10. Testing

Integration and end-to-end tests run against Synapse in a container ([08](08-deployment-and-testing.md) §5): a second mautrix client plays the Element user and creates an **encrypted** DM. Cases: text note, file with caption, photo burst, reply-append, edit, redaction, unlinked sender, third member joins, invite to a non-DM, reminder with attachment, lifecycle purge, restart mid-batch (no loss or duplication), Core outage during sync, and two bot instances competing for the lock (only one becomes ready).
