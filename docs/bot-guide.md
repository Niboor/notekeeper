# Writing a bot for another chat platform

Notekeeper's core knows nothing about any chat platform. A **bot** is a small program that connects one
platform to Core through the bot API: it forwards what people write, and it shows what Core wants said.
The Matrix bot in `bots/matrix` is the reference; Adding a platform needs a new bot and nothing in Core beyond
registering it (NFR-X1, NFR-X2). This guide is the contract; `api/bot.yaml` is the source of truth and the
generated Go client is in `bots/sdk`.

## 1. What a bot is, and is not

A bot **is** a translator and a courier. It **is not** allowed to decide anything about notes: not grouping
messages, not what an edit means, not what to say. Core decides and answers with the wording. Keep the bot
thin (BOT-B1), stateless where the platform allows, and keep whatever state it needs (a sync position, its
device keys) in its own database schema, never in Core's tables.

## 2. Registration and authentication

* The admin registers a **bot instance**: a platform type, a name and the **identity domain** its users live in.
  Any number of instances per platform is fine.
* Each instance has one or more **credentials**: `nkb.<client id>.<secret>`, sent as `Authorization: Bearer ...`.
  Scopes are `ingest` (events, commands, uploads, identity lookups) and `deliver` (the outbox). Secrets are
  stored hashed and shown once; they can be rotated by adding a new one and disabling the old.
* A bot key works **only** on the bot API. It can never call the user API, and a user token never works here.
* The bot API is cluster-internal. Do not expose it; the bot needs no inbound network access at all.

## 3. The events a bot sends

`POST /bot/v1/events` with a normalised event:

```json
{
  "event_id": "$abc:example.org",          // unique per bot instance; the idempotency key
  "kind": "message_created",               // message_created | message_edited | message_deleted
  "sender": "@robin:example.org",          // the platform identity that wrote it
  "conversation": "!room:example.org",     // the chat; where Core sends replies and reminders
  "message_id": "$abc:example.org",        // the platform's id of the message, for later edits and replies
  "timestamp": "2026-03-01T10:00:00.123Z", // the PLATFORM time, not the time you noticed it
  "relates_to": {"reply_to": "$earlier", "thread": "$root"},
  "parts": [ {"type": "text", "text": "buy milk"} ]
}
```

Parts are `text`, `attachment` (with an `upload_id`, see below), `attachment_failed` (a file you could not
fetch, with a `reason`) and `unsupported` (something you cannot represent; give a short description). Never
drop a message silently. Send Markdown as the person typed it; Core stores the text as given.

Core answers `created`, `appended`, `updated`, `removed`, `ignored` or `rejected`, plus `feedback`:
`react` (show a reaction on the source message) and `reply_text` (say this in the chat). **Show exactly what
Core says** and add no wording of your own. A `rejected` event with `identity_unlinked` carries the linking
instructions once an hour; you need not track that.

### Rules

* **Deliver in platform order, at least once.** Retry until Core has answered. A 5xx or a network error means
  try again with the same `event_id`; a 4xx other than 429 means Core refused it for good, so record it and move on
  (the SDK does this: `sdk.Client.PostEvent`). Replays are harmless: the same `event_id` gets the same answer.
* **Advance your own position only after Core answered.** For Matrix that means the sync token is saved after
  the batch was handled (`bots/matrix/internal/syncack`). A Core outage then stalls the bot instead of losing messages.
* **Send the platform timestamp**, so a backlog after downtime groups and sorts exactly as it would have live.
* **Only messages from after linking are used** (Core ignores older ones); do not import history.
* Ignore your own messages, and messages that are the platform's own notices.
* **Edits and deletes** are events of the same shape. For an edit send the new text; for a file with a caption
  send only the caption. For a delete send `message_deleted` with the `message_id`.
* **Commands.** Messages that start with `!` and a known word (`link`, `unlink`, `help`, `remind`, `snooze`,
  `done`) go to `POST /bot/v1/commands` with `{command, args, sender, conversation, message_id, reply_to,
  timestamp}` instead of as notes. Anything else that starts with `!` is an ordinary note. Show Core's
  `reply_text`.

## 4. Attachments

`PUT /bot/v1/uploads/{id}` streams the file body with headers `X-External-User` (the sender), `X-Filename`
(percent-encoded) and `X-Media-Type`, and an exact `Content-Length`. Choose the id yourself and derive it from
the event and part so a retry reuses it. Upload **before** sending the event and put the id in the part as
`upload_id`. If the upload fails permanently, send the part as `attachment_failed` with a reason: the message text
still becomes a note and the person is told which file was not kept. Check the size against your configured
maximum **before** downloading, stream without buffering, and decrypt on the fly if the platform encrypts media
(and verify the checksum at the end).

## 5. What Core asks a bot to say: the outbox

`GET /bot/v1/outbox?wait=20` long-polls for items addressed to your instance and claims them with a 60 second
lease. Each has an `id`, a `kind` and a target (`conversation_id`):

* `notice`: send `payload.text` (security notices, delivery failures).
* `reminder`: send `payload.text`; if `payload.attachments` lists files, fetch each with
  `GET /bot/v1/outbox/{id}/attachments/{attachmentId}` and send it as media; if that fails, send a line naming the
  file with `payload.note_url`. Send the text as **plain, inert content**: no formatting, no mentions.
* `lifecycle`: the identity was unlinked or deleted (`payload.reason`): leave the chat and delete everything you
  keep about it (cursors, room mappings).

Before sending, **check that the chat is still a direct message between the bot and that one person**;
otherwise report `failed_permanent`. Use the item `id` as the platform's idempotency key for the send, so a
crash between sending and reporting never posts twice. Then report `POST /bot/v1/outbox/{id}/result` with
`delivered` (and the platform message ids: Core uses them to find the reminder when someone replies `!snooze` or
`!done`), `failed_transient` (Core retries with backoff) or `failed_permanent` (Core tells the user in the app).
A lease that expires without a report is offered again and does not count as an attempt.

## 6. Other calls

`GET /bot/v1/identities/{external_user_id}` says whether an identity is linked (and nothing about who; it is
limited and audited), `GET /bot/v1/conversations/{id}/cursor` says how far Core has message times for a
conversation, `POST /bot/v1/heartbeat` shows the bot as online in the app, `GET /bot/v1/version` is for readiness.

## 7. Operating a bot

Expose `/healthz`, `/readyz` (platform reachable, Core reachable, recently synced) and `/metrics`; log in JSON
without message text and pass a request id to Core (`X-Request-ID`) so one message can be followed. If the
platform account can only run once (like a Matrix device), take a database advisory lock and run one replica.

## 8. Testing

Test the translation as pure functions (platform event in, bot API event out; `bots/matrix/internal/normalise`),
the media path against a fake homeserver and a fake Core (`bots/matrix/internal/bot/media_test.go`), and the whole
thing against the real platform once (`bots/matrix/internal/e2eetest` uses Synapse in a container). The shared SDK
(`bots/sdk`) has the retry policy; use it or copy its behaviour.
