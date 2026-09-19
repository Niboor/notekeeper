# 4. Ingestion: from chat message to note

Owned entirely by Core (BOT-B1, §3 of the requirements): bots forward normalised events, and Core decides what they mean. Covers BOT-3..BOT-10, GRP-*, EDT-*, CORE-N18, CORE-A9, and the command handling of CORE-R11.

## 1. Event contract

Bots first upload any attachments (`PUT /bot/v1/uploads/{id}`), then post one event.

```json
POST /bot/v1/events
{
  "event_id":     "$abc…",                    // unique per platform event; dedupe key (BOT-7)
  "kind":         "message_created",          // | message_edited | message_deleted
  "sender":       "@robin:example.org",       // verified by the bot from platform metadata
  "conversation": "!room:example.org",
  "message_id":   "$abc…",                    // for edits/deletes: the ORIGINAL message id
  "timestamp":    "2026-09-19T20:30:00Z",     // platform event time
  "relates_to":   { "reply_to": "$prev…", "thread": null },
  "parts": [
    { "type": "text", "text": "concert friday" },
    { "type": "attachment", "upload_id": "0198…", "filename": "tickets.pdf", "media_type": "application/pdf" },
    { "type": "attachment_failed", "filename": "big.mov", "size": 90000000, "reason": "too_large" },
    { "type": "unsupported", "description": "location shared" }
  ]
}
```

- `message_edited`: `message_id` is the original message, `event_id` identifies the edit, `parts` is the new content.
- `message_deleted`: no `parts`.
- Every platform message may become several parts (a caption plus a file are two parts, `part_index` 0 and 1).
- Sizes and counts are capped (parts per event, text length) and violations rejected (SEC-API-3).

## 2. Processing

Everything for one event runs in **one transaction that first locks the user's row** (`FOR UPDATE`). Because every event and every app write for a user serialises on that lock, two concurrent deliveries of the same conversation cannot both decide "create a new note"; the replay of an identical event is caught by the dedupe insert, and the concurrent race between *different* events is caught by the lock (BOT-9, BOT-B4).

```
1  authenticate bot; resolve (bot instance, sender) → identity → user       reject: identity_unlinked / user disabled
2  lock user row; increment change_seq
3  insert into ingest_events (bot, event_id); on conflict → return stored result   (idempotent replay, BOT-7)
4  reject if timestamp < identity.linked_at                                  (MX-10)
5  clamp timestamp to now() if more than 5 minutes in the future             (CORE-N18)
6  dispatch on kind: created | edited | deleted
7  write changes rows, refresh search (trigger), NOTIFY; store result in ingest_events; commit
```

### 2.1 `message_created`

**Target selection**, first match wins:

1. **Explicit relation (GRP-1).** If `relates_to.reply_to` or `.thread` identifies a message whose part exists (`parts_source` index: bot, conversation, message id) → that part's note. If the note is `active`, append the parts with `attach_reason = 'reply'` or `'thread'`, with no time limit. If it is `deleted`, create a **new** note whose first part records `related_part_id` (GRP-7).
2. **Automatic grouping.** Find the *candidate*: the sender's most recent active note in this conversation (`parts_conv_recent`: identity + conversation, latest part by `created_at`, note `active`). It qualifies only if `event.timestamp − candidate.last_part.created_at` is between 0 and the **grouping window** (user setting, default 60 s; measured on platform timestamps, so a backlog groups as it would have live, GRP-5). Then:
   - **Media-only event** (only attachment parts): merge into the candidate (GRP-2a, GRP-3). This adds no text, so GRP-4 cannot be violated.
   - **Text-only event**: merge only if the candidate has **no text part** (a media-only note waiting for its caption, GRP-2b). Otherwise not (GRP-4: two text messages are never merged by timing).
   - **Mixed event** (text and attachment in one message): treated as a text event for the caption rule; merged only if the candidate has no text part.
3. **Otherwise** create a new note.

**Creating a note:** `created_at = event.timestamp`, `received_at = now()`, `category_id = null` (Inbox; ordering is by `created_at`, so a backlog appears chronologically, CORE-N18). Parts get `ordinal` in event order, `attach_reason = 'first'` (or the merge reason), and their source reference `(bot, conversation, message_id, part_index)`. The unique index on that reference makes creations permanently idempotent even after `ingest_events` rows expire.

**Failed attachments (CORE-A9).** A bot reports `attachment_failed` when the platform-side download failed or the size exceeded the limit before upload; Core itself records the same part when an upload was rejected for quota or size. A message consisting only of failed attachments still creates a note holding the failed part(s). Text is never dropped because an attachment failed. The feedback carries the reason so the bot can tell the user.

**Feedback:** `created` or `appended` → `react: "ok"`; a rejection → `reply_text`.

### 2.2 `message_edited`

1. Find the part(s) by `(bot, conversation, message_id, part_index)`; none → `ignored` (EDT-6). The sender must be the identity that created the part, otherwise `ignored` (SEC-BOT-1).
2. For each text part in the new content, compare `event.timestamp` with the part's `text_edited_at` (null counts as older than any edit):
   - **Newer** than the last edit: replace the text, set `text_edited_at`, insert a `note_part_versions` row with `origin = 'chat', applied = true`.
   - **Older** (a delayed chat edit that lost to an app edit): insert the version with `applied = false` only. **Latest edit wins by edit time, not arrival time** (EDT-5).
3. If the note changed, bump `notes.version` and `updated_at` and write a change. The note stays wherever it is, including in the Trash (EDT-7).
4. Attachment changes (EDT-8, Should): when the new content replaces an attachment, the part's `attachment_id` is replaced and the old attachment deleted if unreferenced.

### 2.3 `message_deleted`

Delete all parts with that source message (and their attachments when unreferenced). If the note has no parts left, **dismiss the note** (`state = 'deleted'`, never a permanent delete, EDT-4). Unknown message → `ignored`.

## 3. Commands (`POST /bot/v1/commands`)

The bot recognises a command only when the message body **starts** with the exact prefix `!` followed by a known command name; anything else is a note (MX-8, SEC-BOT-12). The bot forwards `{command, args, sender, conversation, reply_to}`; Core interprets:

| Command | Behaviour |
|---|---|
| `link <code>` | Redeem a pairing code ([03](03-auth.md) §5.2). Allowed for unlinked senders |
| `unlink` | Unlink this identity |
| `help` | Reply text listing commands. Allowed for unlinked senders |
| `remind <when>` as a reply | Set a reminder on the replied-to message's note (CORE-R11a) |
| `remind <when> <text>` | Create an Inbox note with `<text>` and a reminder (CORE-R11b). The note is created exactly like `message_created`, so its source reference lets later edits update it |
| `snooze <duration>` as a reply to a reminder message | Snooze that reminder (resolved through `outbox_messages`) |
| `done` as a reply to a reminder message | Mark it done |

A known command with invalid arguments returns an error `reply_text` with an example and creates nothing (MX-8). Commands from unlinked senders other than `link` and `help` behave like any unlinked message.

### 3.1 Time expressions (CORE-R11, CORE-R13)

A small, deterministic parser in `core/internal/timeparse`, English only in v1, table-tested, evaluated in the user's timezone and relative to the event timestamp:

```
when      = relative | absolute
relative  = "in" N unit          -- "in 2 hours", "in 3 days", "in 45 minutes"
absolute  = day [time] | time    -- "tomorrow 9am", "friday 18:30", "next monday", "17:00"
day       = "today" | "tomorrow" | weekday | "next" weekday | date     -- date: "12 dec", "dec 12", "2026-12-12"
time      = H[:MM] ["am"|"pm"] | HH:MM
```

Bare times resolve to the next future occurrence; a weekday alone to the next such day at 09:00 (configurable default time); results are converted through the IANA zone so DST is correct (CORE-R2). Anything else fails with an error and an example. Recurrence via chat is out of scope (the app sets `rrule`).

## 4. Walk-throughs

| Scenario | Outcome |
|---|---|
| F2: "buy milk" | No candidate → new Inbox note; feedback ✅ |
| F3: PDF then "concert friday" 20 s later | PDF creates a note; the text has no relation, candidate is the media-only note within the window with no text → merged as caption |
| F4: "call dentist", then "renew passport" 5 s later | Second is text-only and the candidate already has text → two notes (GRP-4) |
| Photo burst (4 images in 10 s) | First creates the note, the rest are media-only within the window → one note (GRP-3) |
| Text, file, another file, then text | text + file merge (caption before), second file merges (media-only), final text: candidate has text → new note |
| Reply to an earlier message a day later | Explicit relation (GRP-1): appended to that note regardless of the window |
| Reply to a message of a note that is in the Trash | New note recording the relation (GRP-7) |
| Edit "milk" → "milk, eggs" after the app also changed it | Compared by edit time; the later one wins, the other stays in history (EDT-5) |
| Bot down for a week, then catches up | Events carry platform timestamps: notes get their true created times and sort chronologically; the grouping window uses those timestamps (GRP-5, CORE-N18) |
| Event replayed by the bot | Dedupe returns the stored result; nothing changes (BOT-7) |

## 5. Grouping as a replaceable module (GRP-9)

The decision is one pure function: `decide(event, candidate, relatedPart, window) → {target: existing|new, reason}` in `core/internal/grouping`, with no database access. The transaction supplies the candidate and related part; the function returns the decision, which makes the policy table-testable (every row of §4 is a test case, NFR-Q1) and replaceable without touching the API or bots.
