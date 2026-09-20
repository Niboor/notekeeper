# 3. Accounts, authentication and tokens

Covers AUTH-*, and the SEC-AUTH, SEC-ADM, SEC-BOT and SEC-SHR items that describe mechanisms. Key material and lifetimes are configuration (SEC-BASE-5); defaults are the values quoted here.

## 1. Principles

- One credential type per audience: **user sessions** for people, **bot keys** for bot instances, **share tokens** for anonymous viewers. None is accepted on another audience's listener (SEC-BOT-2, SEC-SHR-4).
- **Nothing secret is stored recoverably.** Passwords are argon2id hashes; activation tokens, pairing codes, bot secrets and share tokens are stored as SHA-256 hashes (they are high-entropy random values, so a fast hash is sufficient); session tokens are not stored at all (§2).
- Authorisation state (`is_admin`, `status`, session revocation) is read from the database on every request, never trusted from a token (SEC-ISO-7, SEC-AUTH-6).

## 2. Sessions and tokens

### 2.1 Token construction

A session row records who is signed in and until when. The **access token** and **refresh token** are MAC-derived from it with a server key `K` (from a Kubernetes Secret, referenced by key id so keys can be rotated: old keys verify, new ones sign):

```
access  = "nka." + b64url( kid | session_id | expires_at | HMAC-SHA256(K[kid], "a" | session_id | expires_at) )
refresh = "nkr." + b64url( kid | session_id | generation | HMAC-SHA256(K[kid], "r" | session_id | generation) )
```

Validation recomputes the MAC, checks the expiry (access tokens live **15 minutes**), then loads the session row and checks `revoked_at is null`, `expires_at > now()` and that the user is `active`. One primary-key lookup per request; nothing is cached, so revocation and disabling are immediate (SEC-AUTH-6). Because tokens are derived, a database leak alone reveals no usable token (SEC-DATA-2), and no token table churns.

**Why derived, not random-and-stored?** Rotating refresh tokens need a *grace window* (§2.3) in which a repeated request must receive the same successor. With stored hashes the successor's plaintext is unrecoverable; with derived tokens it is recomputed. The cost is that a leak of `K` (plus a known session id) would allow forging tokens, so `K` is treated as the most sensitive secret after the database credentials.

### 2.2 Login, cookies, lifetime

`POST /auth/login` verifies the credentials (§3), creates a session with `expires_at = now + idle lifetime` (default **90 days**, sliding) and `absolute_expires_at = now + absolute maximum` (default **1 year**, `0` = unlimited), and sets cookies:

| Cookie | Content | Attributes |
|---|---|---|
| `__Host-nka` | access token | `Secure; HttpOnly; SameSite=Strict; Path=/` |
| `__Secure-nkr` | refresh token | `Secure; HttpOnly; SameSite=Strict; Path=/api/v1/auth` |

Both are persistent cookies with `Max-Age` equal to the session lifetime, except `remember: false` (shared computer, AUTH-C11) which sets session cookies and an absolute lifetime of 12 hours. A fresh session id is created at every login (SEC-AUTH-9). The refresh cookie is only sent to the auth endpoints; the access cookie only matters for API calls, which are same-origin (`SameSite=Strict` costs nothing because the static page itself needs no cookie).

Native clients (Android, later) use the same endpoints with tokens in JSON bodies and `Authorization: Bearer`; the authorisation-code-with-PKCE endpoints (AUTH-C3) are added when that client is built.

### 2.3 Silent renewal and the grace window (AUTH-C10, SEC-AUTH-7, SEC-AUTH-15)

`POST /auth/refresh` presents a refresh token of generation *g* for a session whose current generation is *c*:

| Case | Action |
|---|---|
| *g = c* | Atomically `update sessions set refresh_generation = c+1, previous_valid_until = now()+60s, last_refreshed_at = now(), expires_at = least(now()+idle, absolute) where id = $1 and refresh_generation = c`; return a new access token and `refresh(c+1)`. |
| *g = c−1* and `now() ≤ previous_valid_until` | A second tab or a retried request: return a new access token and the **same** `refresh(c)` (recomputed). No state change, nobody is logged out by a race. |
| *g < c−1*, or *g = c−1* after the grace window | **Reuse of a rotated token**: revoke the session (`revoked_reason = 'refresh_reuse'`), write an audit entry, send a security notice (§7). |
| Session revoked or expired, or reuse detected | `401` with code `session_expired` (never `unauthenticated`, which means "no credentials"); the client shows the login page. |

The client refreshes proactively (at about 80% of the access lifetime) and on any `401`, single-flight within a tab and serialised across tabs with the Web Locks API ([07](07-web-app.md) §3). A user is therefore signed in until the idle lifetime lapses without use, the absolute maximum is reached, or the session is revoked (SEC-AUTH-15).

### 2.4 CSRF and cross-origin

The API has no CORS configuration: browsers cannot read its responses cross-origin (SEC-API-6). For unsafe methods (`POST`, `PUT`, `PATCH`, `DELETE`) the user listener requires all of: `Sec-Fetch-Site: same-origin` (or, when absent, an `Origin` header matching the application origin), and the custom header `X-Notekeeper-Client: web`, which a cross-site form or simple request cannot set (SEC-AUTH-8). No state-changing route accepts `GET`. The SSE endpoint is `GET` and read-only. Responses carry `frame-ancestors 'none'`.

### 2.5 Revocation

Revoking sets `revoked_at` on the session (one row), and sends `NOTIFY nk_sessions, '<session_id>'` so that any replica holding an SSE stream for that session closes it at once (SEC-ISO-5). Events that revoke sessions: logout; password change (other sessions only); admin-issued activation link; disabling the user; user deletion; "sign out everywhere"; refresh reuse.

## 3. Login and throttling

1. Normalise the username (`lower`, Unicode NFC). A name longer than 64 bytes is hashed before it is used in a key, so a caller cannot make keys of any size.
2. **Count the attempt before checking anything.** One attempt is charged to three `auth_throttle` keys, each in its own short transaction that locks the key's row (`EnsureThrottle`, `LockThrottle`, `SetThrottle`), so the check and the count cannot be separated by another request; requests that arrive at the same moment cannot all pass (SR-001). The keys are used **whether or not the user exists**, so throttling reveals nothing (SEC-AUTH-3):

   | Key | Policy | Meant to stop |
   |---|---|---|
   | `acct:<name>\|<address>` (the pair) | no free attempts, delay `2^n` seconds up to 15 minutes | guessing at one account from one address, and a person mistyping is slowed at once |
   | `acct:<name>` | 50 free attempts, delay `2^(n-50)` seconds **up to 30 seconds** | guessing spread over many addresses |
   | `ip:<address>` | 20 free attempts, delay up to 15 minutes | one address trying many accounts |

   A key that is already blocked refuses the attempt without counting it, so retrying while blocked does not lengthen the wait. Because the strict key includes the address, an outsider who keeps guessing a known username slows down only their own address; the person it belongs to signs in from theirs at once. The account-wide key never delays for longer than half a minute, so a botnet cannot keep the real user out either (SEC-AUTH-2, SR-002).
3. Look up the user; if none, or not `active`, or no password, verify the password against a fixed dummy hash (equal work, equal timing) and fail with the same generic `401`.
4. Verify with argon2id. A wrong password needs nothing more (it is already counted), and the attempt is written to the audit log with the address. A right one forgets the pair key and takes one failure back from the other two keys (so someone signing in successfully cannot wipe what a guesser on the same address or account has piled up).
5. If the stored hash uses old parameters, re-hash and store (SEC-BASE-1).

Other password checks have their own keys so they never lock the sign-in: confirming the current password (change password, delete account) uses `pw:<user id>` with the strict policy (SR-006), pairing codes use `pair:<instance>:<identity>`, and activation links are counted per address (`act:<address>`, ten free attempts). **Activation** looks at the token (without using it) before it hashes the new password, so a request with a bad token costs one lookup and cannot queue in front of real sign-ins for the hashing slots (SR-003).

Argon2id defaults: 64 MiB, 3 iterations, parallelism 2, tunable. A semaphore bounds concurrent hash operations per replica so a burst of logins cannot exhaust memory (SEC-API-4). The client address comes from `X-Forwarded-For`, trusted only from configured ingress addresses (`NK_TRUSTED_PROXIES`). Because the strict login key includes the address, a wrong setting matters: with the ingress address for every visitor the pair key becomes account-wide, so Core logs a warning when it sees `X-Forwarded-For` from an untrusted peer (SR-004).

## 4. Accounts

### 4.1 Admin bootstrap (AUTH-U7, SEC-AUTH-13; design decision D4)

`core admin bootstrap --username <name>` (run through `kubectl exec` or `docker compose run`) creates the admin in `pending` state and prints an activation link once. It refuses if an admin exists, and the partial unique index `users_single_admin` enforces it in the database. There is no bootstrap environment variable or default secret. `core admin link --username <name>` and `core admin transfer` are the operator's recovery tools (lost admin password, changing the admin).

### 4.2 Creating users and activation (AUTH-U6, AUTH-U8; design decision D3)

The admin creates a user (`status = 'pending'`, no password) and issues an **activation link**: a 192-bit random token, stored as a SHA-256 hash with `expires_at` (default 7 days) and single use (`update … set used_at = now() where token_hash = $1 and used_at is null and expires_at > now() returning user_id`, atomic, SEC-AUTH-4). The link is `https://<app host>/activate#<token>` and the token sits in the URL fragment, so it never reaches a server log (SEC-AUTH-12). Issuing a new link deletes older ones for that user, **clears the password**, sets the user `pending` and revokes all sessions, writes an audit entry (SEC-ADM-2, SEC-ADM-3) and sends the security notice (§7). The admin never sees or chooses a password. `POST /auth/activate` validates the password policy, stores the argon2id hash, sets the user `active`, and signs the user in.

Password policy (SEC-AUTH-5): at least 10 characters and not in the embedded list of the 100,000 most common passwords (checked offline, case-insensitively). There is no self-service reset (SEC-AUTH-11).

### 4.3 Disabling and deleting

**Disable** sets `status = 'disabled'`, revokes every session, and causes ingest to be rejected, outbox claiming to skip the user's items and share links to stop (live joins) (SEC-BOT-13). Re-enabling restores all of it; suspended deliveries resume.

**Delete** (by the user with their password, or by the admin; AUTH-U9) does in the request: set `status = 'deleting'`, revoke sessions, enqueue `DeleteUser`. The job (`ProcessDeletions`, polled every 15 seconds and safe to run again after a crash) works in **bounded batches, each its own short transaction** with the user's context set, in this order: attachment chunks (200 per transaction); notes (200 per transaction; each note takes its parts, versions, search row, reminders and share links with it); the change feed and the ingest records (5000 rows per transaction); the remaining notes' files (attachments); and finally the user row, whose cascade removes what is left: outbox items of the user, notifications, pages and categories, identities, sessions, tokens, pairing codes, and the storage row. The identities' `lifecycle` outbox items were queued in the request, naming only the identity and conversation. Nothing runs in one large transaction, so the 30-second statement timeout cannot stop a big account from ever disappearing (CR-031). An audit entry with the user id and no content remains. Backups are outside this guarantee (documented retention, SEC-DATA-7). The deletion job is covered by a test that asserts no row referencing the user remains (SEC-DATA-5).

## 5. Bots

### 5.1 Bot keys (AUTH-B1, AUTH-B2, SEC-BOT-8)

The admin creates a bot instance (type, name, `identity_domain`) and a credential. The secret (256 random bits) is shown once; only its SHA-256 is stored. The bot sends `Authorization: Bearer nkb.<client_id>.<secret>`. Multiple credentials may be active during rotation; disabling one takes effect on the next request. Scopes are `ingest` and `deliver`; a credential without `deliver` cannot claim outbox items. Bot instances are addressed by their credential: an item is visible only to the instance it is addressed to (SEC-BOT-1).

### 5.2 Linking chat identities (AUTH-B3..B5)

1. The user calls `POST /me/pairing-codes`. Core creates an 8-character Crockford-base32 code (40 bits) `XXXX-XXXX`, stores its hash with `expires_at = now + 10 minutes`, optionally bound to one bot instance. At most five active codes per user.
2. The user sends `!link XXXX-XXXX` to the bot in a DM. The bot forwards a `link` **command** with the sender identity taken from the platform's verified event metadata, never from text (SEC-BOT-5).
3. Core checks the identity belongs to the instance's `identity_domain` (for Matrix, the homeserver domain), throttles attempts per `(instance, identity)` in `auth_throttle` (SEC-BOT-4), then consumes the code atomically (`update pairing_codes set used_at = now() where code_hash = $1 and used_at is null and expires_at > now() returning user_id`).
4. Core inserts the `external_identities` row (the user's first identity is marked `reminder_target`, CORE-R3). The unique index on `(bot_type, external_user_id)` means an identity already linked to any user is refused with "unlink first" (SEC-BOT-6), never silently moved.
5. Core returns feedback `Linked to your account.` and sends a security notice to the user's other identities and the app (§7).

**Unlink** (`!unlink` from chat, or the app): delete the identity, enqueue a `lifecycle` item so the bot forgets the conversation (BOT-16), send a notice. From that moment ingest for that identity is rejected (SEC-BOT-7).

### 5.3 Identity lookups and unlinked senders

`GET /bot/v1/identities/{id}` answers only "linked or not, since when, which conversation", and is rate limited and audited (SEC-BOT-3). An event from an unlinked sender is rejected with `identity_unlinked`; Core returns the linking instructions as `reply_text` only if `unlinked_senders.last_notice_at` is older than an hour (AUTH-B6), and stores nothing from the message.

## 6. Share tokens (CORE-SH1..SH9, SEC-SHR-*)

- Creating a link generates a 192-bit random token, returns it once inside the URL `https://<share host>/s#<token>`, and stores only its SHA-256, `expires_at` (chosen preset, capped by `NK_SHARE_MAX_LIFETIME`, default 30 days; server-enforced, SEC-SHR-2), and the owner and note.
- A per-user cap of active links and a creation rate limit apply (SEC-SHR-11).
- The public API looks the link up by `sha256(X-Share-Token)`; the token is never logged because it is never in a URL, and request headers are not logged (SEC-SHR-7). The live-validity join and uniform `404` are described in [02](02-api.md) §3.
- The share listener sets no cookies and sends `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex, nofollow`, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff` (SEC-SHR-6).

## 7. Security notices (AUTH-U11, SEC-AUD-4)

A single `notify.Security(user, kind, meta)` function writes an in-app `notifications` row and one `notice` outbox item per linked identity of the user (skipping muted kinds). Events: new sign-in (browser label, time) *[mutable]*; password changed; activation link issued; identity linked or unlinked (sent to the user's other identities); share link created *[mutable]*; refresh reuse detected. If the user has no linked identity the in-app notice is the only channel and is shown at next login.

## 8. Audit log

Written for: login success and failure, activation, password change, session revocation and refresh reuse, identity link and unlink, bot credential create, rotate and disable, bot request rejections, admin actions (create, link, disable, delete), share link create and revoke (SEC-AUD-1). Entries hold ids, kind and small structured detail, never content, tokens or filenames (SEC-AUD-2). Prometheus counters mirror the security-relevant events for alerting (SEC-AUD-3).
