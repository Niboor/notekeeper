# 2. APIs

Three OpenAPI documents (NFR-API1), written first and stored in `api/`: `user.yaml`, `bot.yaml`, `public.yaml`. Go server code, the TypeScript client and the shared Go bot client are generated from them (see [tech-stack.md](../tech-stack.md) §3.2 for the `oapi-codegen` caveats this design is shaped by). All three follow the conventions in [README](README.md) §3: UUID ids, RFC 3339 times, cursor pagination, `problem+json` errors, `404` for foreign ids.

**Action endpoints** are sub-paths (`/notes/{id}/move`), not `:verb` suffixes (decision 43).

**Authorisation** of every operation is declared in the document's `security` and enforced by the router (decision 44): `security: []` is anonymous, `sessionAuth`/`bearerAuth` any signed-in user, and the scope `admin` additionally requires the admin. An operation without a declaration is an error at start-up.

Versioning: major version in the path (`/api/v1`, `/bot/v1`, `/api/public/v1`); compatible changes only within a version, with a deprecation period (NFR-API2, AND-7).

## 1. User API (`/api/v1`)

Authentication: session cookies for the web app; bearer access tokens for native clients (reserved for the Android client, same endpoints). Unsafe methods from the web additionally require the CSRF protections in [03-auth.md](03-auth.md) §2.4.

### 1.1 Session and account

| Method and path | Purpose | Reqs |
|---|---|---|
| `POST /auth/login` | Username and password; sets cookies. `remember: false` gives a browser-session-only session | AUTH-C1, AUTH-C9, AUTH-C11 |
| `POST /auth/refresh` | Renew tokens (called silently by the client) | AUTH-C10, SEC-AUTH-7 |
| `POST /auth/logout` | End the current session | AUTH-U4 |
| `POST /auth/activate` | `{token, password}`: set a password from an activation link, sign in | AUTH-U8 |
| `GET /me`, `PATCH /me` | Profile, timezone, grouping window, muted notices | WEB-12 |
| `POST /me/password` | Change password (keeps this session, revokes others) | AUTH-U4, SEC-AUTH-6 |
| `GET /me/sessions`, `DELETE /me/sessions/{id}`, `POST /me/sessions/revoke-all` | List, revoke, sign out everywhere | AUTH-U4, AUTH-U10 |
| `DELETE /me` | Delete account (requires the password) | AUTH-U4, AUTH-U9 |
| `GET /me/export` | Stream all of the user's data as an archive | AUTH-U5 |
| `GET /me/identities`, `PATCH /me/identities/{id}`, `DELETE /me/identities/{id}` | Linked chat identities; mark reminder targets; unlink | AUTH-B5, CORE-R3 |
| `POST /me/pairing-codes` | Create a pairing code `{bot_instance_id}` (at most 5 active) | AUTH-B3 |
| `GET /bot-instances` | Instances the user can link to, with online status | WEB-12 |
| `GET /notifications`, `POST /notifications/{id}/read` | In-app notices | CORE-R9, AUTH-U11 |

### 1.2 Pages, categories, notes

| Method and path | Purpose | Reqs |
|---|---|---|
| `GET /pages`, `POST /pages`, `PATCH /pages/{id}`, `DELETE /pages/{id}` | Page management; deleting moves notes to the Inbox | CORE-P1, CORE-P4 |
| `POST /categories`, `PATCH /categories/{id}`, `DELETE /categories/{id}` | Category management; `PATCH` can rename, reorder (`before_id`/`after_id`) and move to another page | CORE-P2, CORE-P3 |
| `GET /pages/{id}/board` | Categories of a page with the first N notes of each and counts, in one call | WEB-1, WEB-N3 |
| `GET /categories/{id}/notes`, `GET /inbox/notes` | Paginated notes of a category, or of the Inbox (newest first) | CORE-N4, WEB-2 |
| `POST /notes` | Create a note in the app: `{id, category_id \| null, before_id?, after_id?, parts}`. `category_id` null puts it in the Inbox (ordered by created time); for a category the default position is the top of the column, or between the given neighbours. Parts are text (Markdown, including task lists) and previously uploaded attachments; `attach_reason` is `app` | WEB-9 |
| `GET /notes/{id}` | A note with parts, reminders and share-link summary | WEB-8 |
| `PATCH /notes/{id}/parts/{partId}` | Edit a text part: `{text, base_version}`; latest wins, `stale` flag in the response | CORE-S4, EDT-5 |
| `POST /notes/{id}/parts`, `DELETE /notes/{id}/parts/{partId}` | Add a text or attachment part; remove a part | WEB-9 |
| `POST /notes/{id}/move` | `{category_id \| null, before_id?, after_id?}` | CORE-N3, WEB-3, WEB-4 |
| `POST /notes/{id}/dismiss`, `POST /notes/{id}/restore` | Soft delete and undo | CORE-N6..N8 |
| `DELETE /notes/{id}` | Permanent delete; only for a note in the Trash | CORE-N10 |
| `GET /trash/notes` | Deleted notes, newest deleted first | CORE-N9 |
| `POST /notes/{id}/merge`, `POST /notes/{id}/parts/{partId}/split` | Correct grouping | CORE-N14 |
| `GET /notes/{id}/history` | Text versions of every part | EDT-3, WEB-13 |
| `GET /search` | `q`, `scope=active\|trash\|all`, `page_id`, `category_id`, `has_attachment`, `has_reminder`, `cursor` | CORE-N13, WEB-14 |

### 1.3 Attachments

| Method and path | Purpose | Reqs |
|---|---|---|
| `PUT /attachments/{id}` | **Raw streamed upload.** Body is the file with `Content-Type: application/octet-stream` (the generated server only hands a raw body through for that type); `Content-Length` required (`411` otherwise); `X-Filename` carries the (percent-encoded) name and `X-Media-Type` its media type. Idempotent on `id`. The attachment stays unlinked until a part references it | CORE-A1..A3, CORE-A6 |
| `GET /attachments/{id}` | Download with `Range`, `If-None-Match`, `ETag` (blob sha256 or id+size) | CORE-A6, NFR-API4 |

There is no multipart upload anywhere (see tech-stack §3.2). Downloads always carry `Content-Disposition`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`, and `Cache-Control: private, no-cache` (design decision D5); types outside a small inline allowlist (common raster images, PDF, audio, video) are served as `attachment` with `application/octet-stream` (SEC-CNT-3).

### 1.4 Reminders, sharing

| Method and path | Purpose | Reqs |
|---|---|---|
| `POST /notes/{id}/reminders`, `PATCH /reminders/{id}`, `DELETE /reminders/{id}` | Set, change (including `rrule`), clear | CORE-R1, CORE-R10 |
| `POST /reminders/{id}/snooze`, `POST /reminders/{id}/done` | Snooze or complete | CORE-R8 |
| `GET /reminders?state=upcoming` | Upcoming reminders | CORE-R9 |
| `POST /notes/{id}/share-links` | `{expires_in}` from presets; returns the link **once** (only its hash is stored) | CORE-SH1, CORE-SH2 |
| `GET /share-links`, `DELETE /share-links/{id}`, `POST /share-links/revoke-all` | List with usage, revoke | CORE-SH3, CORE-SH14 |

### 1.5 Realtime and sync

| Method and path | Purpose | Reqs |
|---|---|---|
| `GET /events` | Server-Sent Events stream (see [05](05-realtime-and-jobs.md) §2). Hand-written handler; a stub in the spec | CORE-S1, WEB-11 |
| `GET /changes?cursor=&limit=` | The change feed: changes after a cursor, including deletions | CORE-S3, AND-3 |

### 1.6 Administration (admin only)

| Method and path | Purpose | Reqs |
|---|---|---|
| `GET /admin/users`, `POST /admin/users` | List (metadata only: username, status, storage use); create a pending user | AUTH-U6, WEB-19 |
| `POST /admin/users/{id}/activation-link` | Issue an activation link (clears the password, revokes sessions, notifies the user) | AUTH-U8, SEC-ADM-2 |
| `PATCH /admin/users/{id}` | Disable or enable, set quota | AUTH-U6 |
| `DELETE /admin/users/{id}` | Delete user and all data | AUTH-U9 |
| `GET /admin/bot-instances`, `POST /admin/bot-instances`, `PATCH /admin/bot-instances/{id}` | Register, disable | AUTH-B1 |
| `POST /admin/bot-instances/{id}/credentials`, `DELETE /admin/bot-instances/{id}/credentials/{cid}` | Create a credential (secret shown once), disable one | AUTH-B1, SEC-BOT-8 |

`is_admin` is read from the database on every request; it is never carried in a token (SEC-ISO-7). No admin endpoint returns note content (SEC-ADM-1).

## 2. Bot API (`/bot/v1`)

Cluster-internal. Authentication: `Authorization: Bearer nkb.<client_id>.<secret>`; the secret is looked up by `client_id` and compared in constant time against the stored hash. The credential's bot instance and scopes bound everything (SEC-BOT-1, SEC-BOT-2).

| Method and path | Purpose | Scope | Reqs |
|---|---|---|---|
| `PUT /uploads/{id}` | Stream an attachment for a linked identity (`X-External-User` header, `X-Filename`, `Content-Length`); same storage path and quota as the user upload | ingest | BOT-6, CORE-A3 |
| `POST /events` | A normalised chat event (see [04](04-ingestion.md) §1) | ingest | BOT-3..BOT-9 |
| `POST /commands` | A chat command (`link`, `unlink`, `help`, `remind`, `snooze`, `done`) with sender, conversation and replied-to message | ingest | BOT-14, AUTH-B3 |
| `GET /identities/{external_user_id}` | Is this identity linked; its `linked_at` and conversation. Nothing about the user | ingest | BOT-2, SEC-BOT-3 |
| `GET /outbox?wait=25&limit=` | **Long-poll the outbox**: claim queued items with a lease | deliver | BOT-11 |
| `POST /outbox/{id}/result` | `{state: delivered\|failed_transient\|failed_permanent, reason, message_ids[]}` | deliver | BOT-11, BOT-13 |
| `GET /outbox/{id}/attachments/{attachmentId}` | Download an attachment referenced by an item claimed by this instance | deliver | BOT-15 |
| `GET /conversations/{id}/cursor` | Last platform timestamp delivered for a conversation | ingest | BOT-10 |
| `POST /heartbeat` | Liveness for the admin's bot status | ingest | WEB-12 |

`POST /events` and `POST /commands` return a **feedback** object so bots contain no wording of their own (BOT-8):

```json
{ "result": "created", "note_id": "…", "feedback": { "react": "ok", "reply_text": null } }
```

`result` is one of `created`, `appended`, `updated`, `removed`, `ignored`, `rejected`; `rejected` carries a `code` (`identity_unlinked`, `too_large`, `quota_exceeded`, …) and `reply_text` when the user should be told something.

## 3. Public share API (`/api/public/v1`)

Served only on the share hostname. No cookies are set or read. The share token is sent in the `X-Share-Token` request header; it never appears in a URL (design decision D2, SEC-SHR-7).

| Method and path | Purpose | Reqs |
|---|---|---|
| `GET /share` | The shared note's current content: created date and parts (text; attachments as `{id, filename, media_type, size}`). Nothing else | CORE-SH4 |
| `GET /share/attachments/{id}` | Bytes of one attachment **of that note**; `Range` is supported but the page does not use it | CORE-SH4, CORE-SH7 |

Every request re-evaluates link validity as a live join (see [01](01-data-model.md) §10). Unknown, expired, revoked and inactive-note links all return the same `404` body with the same timing class (SEC-SHR-3). Rate limits per IP and per link apply, with a per-link request and bandwidth ceiling (CORE-SH9). The page fetches attachments with the header and displays them through blob URLs; at the 25 MiB cap this is acceptable, at the price of no seeking in large audio or video.

## 4. Streaming implementation notes

- **Uploads:** the generated strict-server handler receives `io.Reader`; the handler wraps it with a limit reader (`Content-Length`, maximum size), reserves quota first (see [01](01-data-model.md) §6.2) and writes 256 KiB chunks. Request-body validation is disabled on these routes.
- **Downloads:** a custom response type implements the generated visitor and calls `http.ServeContent` with the chunk-backed `ReadSeeker`.
- **SSE and the outbox long-poll** are hand-written handlers registered next to the generated routes; the CI route-vs-spec test covers them (tech-stack §3.2, item 5).
- **Timeouts:** header read timeout on all listeners; no global write timeout on streaming routes, but per-request context deadlines (uploads 10 minutes, long-polls 30 seconds).

## 5. Data export (AUTH-U5, SEC-DATA-8)

`GET /me/export` streams a ZIP archive built on the fly (`archive/zip` writing straight to the response, one attachment at a time, so memory stays bounded): `export.json` with pages, categories, notes with parts, text history, reminders and share-link metadata (no tokens), plus `attachments/<attachment id>-<filename>` for every file, referenced from the JSON. The export runs under the requesting user's row-level-security context, so it can contain nothing but that user's data. It is rate limited to a few requests per hour per user.
