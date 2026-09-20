# Notekeeper — Security requirements

Status: draft v0.1. Companion to [requirements.md](requirements.md); read that first for terminology (Core, bot instance, link, share link, ...).

## 1. How to read this document

- The main requirements say what the system **does**. This document says what must **not be possible**, whoever tries and however they try.
- Every item in §4 is written as a negative ("X must not be possible") so that it translates directly into an **abuse-case test**: perform the attack, expect it to fail. A security requirement is only `fully tested` when such a test exists (§8).
- This document is **normative**, like `requirements.md`, and uses the same priorities (Must / Should / Could) and the same **State** column (`unimplemented`, `implemented`, `fully tested`).
- Where a requirement already exists in `requirements.md`, the *Related* column points to it rather than restating it. Where an item here adds something new, it is the authoritative statement.
- IDs are stable; gaps in numbering are intentional.

## 2. Assets, actors and trust boundaries

### 2.1 Assets (what we protect)

| Asset | Why it matters |
|---|---|
| Note content and attachments | Personal: tickets, reminders, private todos, photos. The main asset. |
| Account credentials and sessions | Gateway to everything else. |
| Bot credentials and Matrix device keys | A stolen bot credential can inject notes; stolen Matrix keys can read encrypted chats. |
| Share-link tokens | Bearer secrets that grant access without an account. |
| Availability and integrity of the service | Notes must not be silently lost or corrupted. |
| The operator's cluster and database | Compromise here is compromise of everything. |

### 2.2 Actors (who might attack)

| Actor | Capabilities assumed |
|---|---|
| **Anonymous** | Anyone on the internet who can reach the public endpoints. |
| **Share-link holder** | Anonymous plus a valid share link; may forward it. |
| **Authenticated user** | Has an account and may be curious or malicious towards other users. |
| **Compromised user session** | An attacker holding a stolen session or refresh token. |
| **Compromised chat account** | An attacker controlling a linked Matrix account (can send messages as the user). |
| **Bot instance** | Holds bot credentials; may be buggy or compromised. |
| **Third party in a chat room** | Someone who joins a DM room or forwards messages to the bot. |
| **Admin** | The single admin, who is also the operator: trusted, with full access at the database level (R1). |
| **Network attacker** | Can observe or tamper with traffic between components and clients. |
| **Malicious content** | A crafted note, file, filename or chat message from any of the above. |

### 2.3 Trust boundaries

1. Internet ⇄ Core (user API, public share endpoints).
2. Bot ⇄ Core (bot API), and chat platform ⇄ bot.
3. Core/bots ⇄ PostgreSQL.
4. Clients (browser, future Android) ⇄ Core.

## 3. Access matrix

The intended permissions **at the application level**, as the reference for authorisation tests (SEC-ISO-1). They do not bind the operator, who can read everything at the database level (R1). "own" means resources owned by that actor's user. Anything not listed as allowed is denied.

| Resource / action | Anonymous | Share-link holder | User (own) | User (other's) | Admin | Bot instance |
|---|---|---|---|---|---|---|
| Read/write own notes, pages, attachments | ✗ | ✗ | ✓ | ✗ | own only | ✗ |
| Read another user's notes or attachments | ✗ | ✗ | ✗ | ✗ | ✗ | ✗ |
| Read a shared note via its valid link | ✗ | ✓ (that note only) | ✓ | ✓ (with link) | ✓ (with link) | ✗ |
| Create/revoke share links | ✗ | ✗ | own notes | ✗ | own notes | ✗ |
| Ingest a message for a user | ✗ | ✗ | ✗ | ✗ | ✗ | only for identities linked to that bot instance |
| Claim deliveries / lifecycle notices | ✗ | ✗ | ✗ | ✗ | ✗ | only those addressed to that bot instance |
| Download an attachment | ✗ | only from the shared note | own | ✗ | own | only those referenced by its own deliveries |
| Create users, issue activation links, set quotas | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ |
| Register bot instances, rotate bot credentials | ✗ | ✗ | ✗ | ✗ | ✓ | ✗ |
| See per-user metadata (username, status, storage use) | ✗ | ✗ | own | ✗ | ✓ | ✗ |
| See any user's content (admin functions) | ✗ | ✗ | own | ✗ | ✗ | ✗ |

## 4. Must not be possible

Format: **ID | Priority | State | What must not be possible | Related**.

### 4.1 Authentication and sessions (SEC-AUTH)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-AUTH-1 | Must | fully tested | Authenticating with a missing, empty or incorrect password, or as a disabled or not-yet-activated account. | AUTH-C1, AUTH-U8 |
| SEC-AUTH-2 | Must | fully tested | Guessing passwords at scale: attempts are rate-limited per account and per source IP with growing delays. The mechanism must not let an outsider lock the real user (or the admin) out for long; prefer delays over permanent lockout. | AUTH-C6 |
| SEC-AUTH-3 | Must | fully tested | Learning whether a username exists, from login responses, activation responses or response timing. | — |
| SEC-AUTH-4 | Must | fully tested | Reusing an activation link after use, after expiry, or after a newer link was issued for the same user; guessing one (at least 128 bits, stored only as a hash, compared in constant time). | AUTH-U8 |
| SEC-AUTH-5 | Must | fully tested | Setting a password that is shorter than 10 characters or appears in a common/breached-password list (checked offline, no third-party calls). | AUTH-C1 |
| SEC-AUTH-6 | Must | fully tested | Keeping access after sign-out, admin-issued password reset or account disable: all sessions and refresh tokens are revoked immediately, and access tokens live at most 15 minutes. A password change revokes all *other* sessions but keeps the current one. | AUTH-U4, AUTH-U8 |
| SEC-AUTH-7 | Must | fully tested | Using a refresh token that was already rotated: reuse beyond a short grace window (default 60 s) revokes the whole token family (theft detection). Within the grace window a repeated refresh (second tab, retried request after a network failure) returns the same successor, so a legitimate user is never logged out by a race. | AUTH-C3, AUTH-C10 |
| SEC-AUTH-8 | Must | fully tested | Triggering a state-changing request cross-site (CSRF), or from a cross-origin page reading authenticated responses. State-changing operations are never reachable by GET. | AUTH-C4 |
| SEC-AUTH-9 | Must | implemented | Session fixation: a fresh session identifier is issued on every login. | AUTH-C4 |
| SEC-AUTH-10 | Must | unimplemented | Intercepting or redirecting the native-client authorisation code: PKCE is mandatory, redirect URIs are matched exactly, no open redirects anywhere in login flows. | AUTH-C3 |
| SEC-AUTH-11 | Must | fully tested | Resetting a password without the admin: no self-service reset path exists, so there is no reset-poisoning or account-recovery attack surface. | AUTH-U8 |
| SEC-AUTH-12 | Must | implemented | Session or access tokens appearing in URLs (query strings, paths), logs or referrers. | NFR-S1 |
| SEC-AUTH-13 | Must | fully tested | Creating a second admin, or re-running the admin bootstrap once an admin exists (for instance by changing a config value); the bootstrap secret must not have a default value. | AUTH-U7 |
| SEC-AUTH-14 | Should | unimplemented | Taking over the admin account with only a password: the admin has a second factor once one is available. | AUTH-C7 |
| SEC-AUTH-15 | Must | implemented | A user being logged out unexpectedly: a session survives access-token expiry, server restarts, deployments, several tabs and concurrent or retried refreshes. Only sign-out, revocation, account disable, admin password reset, or reaching the configured session lifetime ends it. | AUTH-C9, AUTH-C10 |
| SEC-AUTH-16 | Should | fully tested | A user who suspects a compromise being unable to cut off all access in one step: sign out everywhere and revoke all share links. | AUTH-U10, CORE-SH14 |

### 4.2 Authorisation and tenant isolation (SEC-ISO)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-ISO-1 | Must | fully tested | Any actor performing an action outside the access matrix (§3). Authorisation tests are generated from the OpenAPI description: every endpoint × every actor, asserting the expected result. New endpoints without a declared actor set fail CI. | AUTH-U1, NFR-S3 |
| SEC-ISO-2 | Must | fully tested | Reading, modifying, listing, counting or enumerating another user's notes, pages, categories, attachments, history, reminders, share links, sessions or links, through **any** path: REST, realtime channel, change feed, search (results, snippets, counts), export, error messages. | AUTH-U1, NFR-S3 |
| SEC-ISO-3 | Must | fully tested | Telling whether an ID belongs to another user: foreign IDs and non-existent IDs yield identical responses (same status, body and timing class). | — |
| SEC-ISO-4 | Must | fully tested | Referencing another user's objects in a request of one's own (moving a note into a foreign category, attaching a foreign attachment, pointing a reminder at a foreign note). Every relation is validated against the same owner. | — |
| SEC-ISO-5 | Must | fully tested | Subscribing to another user's realtime stream or change-feed cursor, or receiving events after revocation: streams are authenticated at connect and re-validated when the token expires or is revoked. | CORE-S1, CORE-S3 |
| SEC-ISO-6 | Must | implemented | Setting server-controlled fields from a client request (owner, admin flag, state, version, timestamps, source reference, storage backend, quota): mass assignment. | — |
| SEC-ISO-7 | Must | implemented | Obtaining admin capability by any means other than being the bootstrapped admin: the role is never derived from data a user can influence. | AUTH-U7 |
| SEC-ISO-8 | Must | unimplemented | Using an idempotency key to replay or read another user's cached response; keys are scoped per user (and per bot instance). | CORE-S5 |
| SEC-ISO-9 | Must | fully tested | Proving that another user holds a given file: content-hash deduplication is strictly per user, and a client can never obtain a blob by claiming its hash without providing the content. | CORE-A5 |
| SEC-ISO-10 | Should | fully tested | Bypassing tenant filters through an application bug: PostgreSQL row-level security (or equivalent) acts as a second, independent barrier. | NFR-S3 |

### 4.3 Admin boundary (SEC-ADM)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-ADM-1 | Must | unimplemented | The admin reading other users' notes, attachments, history or share-link content through any application feature or API. Admin endpoints return metadata only (username, status, quota use). | AUTH-U6 |
| SEC-ADM-2 | Must | implemented | The admin logging in as a user or choosing a user's password: there is no "log in as" feature, and passwords are only ever set by the user through the activation flow. Issuing an activation link revokes the user's sessions and is audit-logged, and the user gets a security notice (AUTH-U11). | AUTH-U8 |
| SEC-ADM-3 | Must | implemented | An admin action leaving no trace: every admin action is written to the audit log. | AUTH-B8, NFR-S6 |
| SEC-ADM-4 | Must | fully tested | Admin functions being reachable by a non-admin, by a bot credential, or through a share link. | SEC-ISO-1 |

### 4.4 Bots and identity linking (SEC-BOT)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-BOT-1 | Must | implemented | A bot instance acting for a user who has not linked that bot instance: ingest, edit, delete, delivery claim and attachment download are all scoped to (bot instance, linked identity). | AUTH-B2 |
| SEC-BOT-2 | Must | fully tested | Using bot credentials on the user API, user tokens on the bot API, or a bot credential from one instance on another instance's items. | AUTH-B2 |
| SEC-BOT-3 | Must | unimplemented | A bot reading anything beyond what it was handed: notes, attachments not referenced by its own deliveries, users, or the links of other bot instances. The identity lookup reveals only "linked or not", never which user, and is rate-limited and audited. | BOT-15, BOT-2 |
| SEC-BOT-4 | Must | fully tested | Redeeming a pairing code twice, after expiry, or by guessing (attempts rate-limited; codes are single-use, short-lived, consumed atomically and bound to the user who created them). | AUTH-B3 |
| SEC-BOT-5 | Must | fully tested | Linking an identity that did not itself send the code: the sender identity comes from the platform's verified event metadata, never from message text; and a bot instance can only assert identities of its own platform and configured homeserver namespace. | AUTH-B3 |
| SEC-BOT-6 | Must | fully tested | An external identity being linked to two users, or a link silently moving to another user: re-linking requires an explicit unlink first. | AUTH-B4 |
| SEC-BOT-7 | Must | fully tested | An unlinked or revoked identity creating, editing or deleting notes, or its messages being stored or their content logged. | AUTH-B5, AUTH-B6 |
| SEC-BOT-13 | Must | fully tested | A **disabled** user's identities ingesting notes, edits or deletes, or receiving deliveries: ingest is rejected, queued deliveries are suspended (not deleted) and resume if the account is re-enabled, and sessions are revoked. | AUTH-U6 |
| SEC-BOT-8 | Must | fully tested | A leaked or misbehaving bot credential staying valid: the admin can disable a bot instance instantly, and rotation needs no downtime. Secrets are stored hashed and never logged. | AUTH-B1 |
| SEC-BOT-9 | Must | fully tested | Replayed or forged ingest events changing state twice, or carrying implausible timestamps that reorder a user's Inbox (clamped per CORE-N18). | BOT-7, CORE-N18 |
| SEC-BOT-10 | Must | unimplemented | A bot flooding Core or a user: per-bot-instance and per-user rate and size limits apply to ingest and deliveries, on top of user quotas. | NFR-S4, CORE-A3 |
| SEC-BOT-11 | Must | unimplemented | A delivery for user A ending up in user B's conversation. The delivery target is fixed by Core from the identity's own conversation, which only changes on events sent by that identity. | BOT-12 |
| SEC-BOT-12 | Must | fully tested | Chat text being interpreted as a command anywhere other than at the very start of a message with the exact command prefix. Commands do only what their definition says; arguments are parsed as data, never evaluated. | MX-8, BOT-14 |

### 4.5 Share links and public endpoints (SEC-SHR)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-SHR-1 | Must | unimplemented | Opening a link after expiry, revocation, note dismissal, note deletion, or owner disable/deletion. | CORE-SH2..SH5 |
| SEC-SHR-2 | Must | unimplemented | Creating a link without an expiry, or with an expiry beyond the configured maximum, by any means including a hand-crafted API request: enforced server-side, not just in the UI. | CORE-SH2 |
| SEC-SHR-3 | Must | unimplemented | Guessing or enumerating tokens (at least 128 bits, hashed at rest, constant-time comparison), or distinguishing "unknown" from "expired" from "revoked" by response or timing. | CORE-SH6 |
| SEC-SHR-4 | Must | unimplemented | Reaching anything but the shared note and its attachments with a link: other notes, attachments of other notes (also by guessing IDs or path tricks), any user-API endpoint, the note's page/category, source, reminders, history or owner identity. | CORE-SH4, CORE-SH7 |
| SEC-SHR-5 | Must | unimplemented | Shared content running script in, or reading storage or cookies of, the authenticated app: separate origin or equivalent isolation, no cookies set or accepted on public endpoints. | CORE-SH7, CORE-SH8 |
| SEC-SHR-6 | Must | unimplemented | A link leaking through referrers, search-engine indexing or shared caches: `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`, non-cacheable responses. | CORE-SH8 |
| SEC-SHR-7 | Must | unimplemented | Full share tokens appearing in access logs, metrics labels, traces, error reports or referrers: the token lives in the URL fragment and is sent only in a request header to the public API, which is never logged. | CORE-SH10, NFR-S1 |
| SEC-SHR-8 | Must | unimplemented | A single link (or many links) exhausting bandwidth or CPU: per-link and per-IP limits apply. | CORE-SH9 |
| SEC-SHR-9 | Must | unimplemented | Creating links for a note the caller does not own. | SEC-ISO-4 |
| SEC-SHR-10 | Should | unimplemented | The public page being used to pass content off as coming from the operator: it shows a clear notice that the content was shared by a Notekeeper user and is not verified. | — |
| SEC-SHR-11 | Must | unimplemented | Minting share links in bulk (for example from a stolen session): creation is rate-limited and each user has a cap on simultaneously active links. | CORE-SH1 |

### 4.6 Content and attachments (SEC-CNT)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-CNT-1 | Must | implemented | Stored XSS: note text, filenames, page/category/user names, HTML from chat (`formatted_body`), link text and error messages must never execute script in the web app or the public share page. Markdown is rendered with raw HTML disabled, and other HTML is sanitised through an allowlist. | WEB-N5 |
| SEC-CNT-2 | Must | fully tested | Dangerous URL schemes in notes: links with `javascript:`, `data:` or `vbscript:` are never rendered as active links (allowlist: `http`, `https`, `mailto`, `tel`). | WEB-N5 |
| SEC-CNT-3 | Must | fully tested | An uploaded file executing in the browser: HTML, SVG, JS and unknown types are served as downloads (`Content-Disposition: attachment`, `application/octet-stream`); `X-Content-Type-Options: nosniff` and a restrictive `Content-Security-Policy` (`sandbox`) on every attachment response; images render only via `<img>`, never inline SVG. | WEB-N5 |
| SEC-CNT-4 | Must | fully tested | Filename tricks: header injection (CR/LF), path traversal, or use of a filename as a storage path. Filenames are metadata only, sanitised whenever placed in a header. | CORE-A4 |
| SEC-CNT-5 | Must | implemented | Untrusted media being parsed, decoded or transcoded by Notekeeper's own components (thumbnailers, image or PDF libraries): attachments are stored and served byte for byte, so no media parser is reachable from user uploads. Any future feature that processes media must be designed as its own sandboxed component first. | CORE-A4 |
| SEC-CNT-6 | Must | fully tested | Exceeding size limits or quota through concurrent or split uploads: quota checks are atomic with the write. | CORE-A3 |
| SEC-CNT-7 | Must | unimplemented | Reminder or delivery text pinging people or triggering bot behaviour: note text is sent as inert content (no `@room`/mentions, formatted text escaped, no leading command prefix acted on). | CORE-R6 |
| SEC-CNT-8 | Must | unimplemented | Core or a bot fetching arbitrary URLs supplied by users or chat content (SSRF). Bots fetch media only from the configured homeserver via `mxc://` URIs. Any future link-preview feature must use a separate, network-restricted fetcher. | MX-3 |
| SEC-CNT-9 | Should | unimplemented | Malware in attachments going unnoticed: optional antivirus scanning hook for uploads and share-link downloads. | — |

### 4.7 API and abuse resistance (SEC-API)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-API-1 | Must | fully tested | SQL injection: all queries are parameterised; full-text search input is treated as data and cannot express expensive or unsafe query constructs. | CORE-N13 |
| SEC-API-2 | Must | fully tested | Reaching a non-public endpoint without authentication: default-deny, with every route declaring its actors explicitly. | SEC-ISO-1 |
| SEC-API-3 | Must | implemented | Unbounded input: request size, JSON depth, note length, parts per note, pages/categories/notes per user, page sizes and search complexity are all capped. | NFR-S4 |
| SEC-API-4 | Must | unimplemented | One user or client degrading the service for others: per-user and per-IP rate limits, capped concurrent realtime connections and uploads, database statement timeouts, slow-client timeouts. | NFR-S4 |
| SEC-API-5 | Must | implemented | Internal details in responses: stack traces, SQL, hostnames, library versions. Errors are generic externally and detailed only in logs. | NFR-API3 |
| SEC-API-6 | Must | unimplemented | Cross-origin access from foreign sites: CORS allows only the web app's own origin, never a wildcard together with credentials; framing of the app is denied (`frame-ancestors 'none'`). | — |
| SEC-API-7 | Must | fully tested | Race conditions breaking invariants: single-use pairing/activation codes, the one-admin rule, storage quota and note version checks are enforced atomically in the database. | AUTH-U7, CORE-A3 |
| SEC-API-8 | Must | unimplemented | Web pages missing baseline security headers: HSTS, `X-Content-Type-Options`, a strict Content-Security-Policy without `unsafe-inline` scripts, `Referrer-Policy`. | WEB-N5 |

### 4.8 Data protection and privacy (SEC-DATA)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-DATA-1 | Must | unimplemented | Note content, filenames, tokens, passwords, credentials or Matrix keys appearing in logs, metrics labels, traces, crash reports or URL query strings. | NFR-S7, NFR-O1 |
| SEC-DATA-2 | Must | implemented | Secrets stored recoverably where hashing suffices: passwords (argon2id), activation, pairing, refresh and share tokens, and bot secrets are stored only as hashes. Secrets that must be recoverable (Matrix device keys, bot account credentials) are encrypted at rest with a key from a Kubernetes Secret. | AUTH-C1, MX-N1 |
| SEC-DATA-3 | Must | unimplemented | Client-to-Core traffic over plain HTTP. | NFR-S1 |
| SEC-DATA-4 | Should | unimplemented | Component-to-component and database traffic inside the cluster over unencrypted connections, where the cluster's network cannot be assumed trusted (TLS to PostgreSQL at least). | NFR-S2 |
| SEC-DATA-5 | Must | implemented | Data surviving deletion: after deleting a note, attachment or user, blobs, search-index entries, history, caches and queued deliveries are gone (verified by tests). | AUTH-U9, CORE-N10 |
| SEC-DATA-6 | Must | fully tested | Authenticated responses being stored by shared caches or proxies (`Cache-Control: private, no-store` on API responses; `private, no-cache` with an `ETag` on attachments, so clients can revalidate but shared caches never store them; the ingress example does not cache). | — |
| SEC-DATA-7 | Should | unimplemented | Backups being readable by unintended parties: backups are encrypted and access-controlled, with documented retention (which bounds how long deleted data persists). | NFR-R4, AUTH-U9 |
| SEC-DATA-8 | Must | unimplemented | A data export containing anything but the requesting user's own data. | AUTH-U5 |

### 4.9 Matrix bot (SEC-MX)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-MX-1 | Must | fully tested | The bot reading or ingesting messages from group rooms, or from a DM after a third member joined. | MX-1, MX-2 |
| SEC-MX-2 | Must | unimplemented | Reminder or delivery content being posted into a room that no longer consists solely of the bot and the linked user. The bot re-checks room membership immediately before sending. | MX-12, BOT-12 |
| SEC-MX-3 | Must | implemented | Decrypted message content or device keys leaking through logs, crash dumps or unencrypted storage. | MX-N1, SEC-DATA-1 |
| SEC-MX-4 | Must | fully tested | Two bot instances sharing one Matrix device identity (split-brain, key theft surface). | MX-N2 |
| SEC-MX-5 | Must | implemented | The bot's Matrix credentials being stored in the database in plaintext or being visible to Core. | SEC-DATA-2 |
| SEC-MX-6 | Should | unimplemented | An impostor device silently being trusted for the linked user without any trace: new devices for a linked identity are logged, and the trust model (trust on first use) is documented for users. | MX-3 |

### 4.10 Deployment and operations (SEC-OPS)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-OPS-1 | Must | implemented | Secrets in container images, git or ConfigMaps. The example manifests use Secret references only, and startup fails if a placeholder secret is unchanged. | NFR-S1, NFR-D6 |
| SEC-OPS-2 | Must | unimplemented | Containers running as root, privileged, with writable root filesystems, extra capabilities or privilege escalation: the example manifests set `runAsNonRoot`, `readOnlyRootFilesystem`, drop all capabilities and use the runtime default seccomp profile. | NFR-D6 |
| SEC-OPS-3 | Must | unimplemented | Unnecessary network paths: only Core and the bots reach PostgreSQL; bots reach only their chat platform and Core. Example NetworkPolicies demonstrate this. | NFR-S2 |
| SEC-OPS-4 | Must | unimplemented | Metrics, health, debug or profiling endpoints being reachable from the internet: they are served on a separate internal port and not routed by the ingress. | NFR-O2, NFR-O4 |
| SEC-OPS-5 | Must | fully tested | The application's runtime database role altering the schema or reading other components' private tables (bot crypto state): migrations use a separate, more privileged role; each component has its own least-privilege role. | NFR-D4, NFR-S2 |
| SEC-OPS-6 | Must | unimplemented | Known-vulnerable dependencies or images shipping unnoticed: CI scans dependencies and images, versions are pinned, base images are minimal. | NFR-S6 |
| SEC-OPS-7 | Must | unimplemented | Shipping any default credential (default admin password, sample bot secret, example token). | SEC-AUTH-13 |
| SEC-OPS-8 | Should | unimplemented | TLS being weak or absent at the ingress: the example ingress enforces TLS with modern settings and HSTS. | SEC-DATA-3 |

### 4.11 Auditing and detection (SEC-AUD)

| ID | Pri | State | Must not be possible | Related |
|---|---|---|---|---|
| SEC-AUD-1 | Must | implemented | Security-relevant events going unrecorded: failed and successful logins, activation, password changes, session revocation, link/unlink, bot credential creation/rotation/disabling, admin actions, share-link creation/revocation, and rejected bot requests. | AUTH-B8, NFR-S6 |
| SEC-AUD-2 | Must | fully tested | The audit log containing note content or secrets, or being modifiable through the application: it is append-only from the application's point of view. | SEC-DATA-1 |
| SEC-AUD-3 | Should | unimplemented | An attack pattern going unnoticed: metrics and example alerts for spikes in failed logins, rejected bot requests, share-link 404s and rate-limit hits. | NFR-O2 |
| SEC-AUD-4 | Should | unimplemented | Sensitive account events happening without the user being told: new sign-ins, password changes or activation links, chat link changes and share-link creation produce a security notice in chat and in the app. | AUTH-U11 |

## 5. Accepted risks (explicitly not prevented)

These are known limits. They are stated so that nobody assumes otherwise.

| # | Risk | Why it is accepted / mitigation |
|---|---|---|
| R1 | The **admin is the operator**: one trusted person with full database and cluster access, who can therefore read all notes and attachments and take over any account. | Acceptable for a small friends-and-family deployment. Notes are stored readable by the server (NFR-S5) and users should be told. The application itself doesn't expose other users' content to the admin (SEC-ADM-1), and account takeover is audited and announced (SEC-ADM-2). Encrypted backups (SEC-DATA-7) and least privilege still apply. |
| R2 | The **Matrix homeserver** is trusted for who a message is from. A malicious homeserver could forge messages as a user and thereby create notes. | Inherent to Matrix. The bot instance is bound to one configured homeserver, and in encrypted rooms message authenticity is also protected by the Megolm session. |
| R3 | A **compromised chat account** can create notes for that user and receive their reminders (including attachments). It cannot read existing notes, because the bot API offers no query commands. | Revoke the link (AUTH-B5). Adding chat-side query commands later would widen this risk and requires a fresh review. |
| R4 | A **share link** can be forwarded by whoever holds it, and the content can be copied. | Links are read-only, expire (CORE-SH2) and can be revoked (CORE-SH3). |
| R5 | **Malware** in attachments is not scanned by default. | Files are never executed by Notekeeper and are served as downloads (SEC-CNT-3). Optional scanning is a Should (SEC-CNT-9). |
| R6 | **Availability** depends on a single PostgreSQL instance and the operator. | Consistent with the sizing in NFR-P3; chat platforms retain messages while the system is down (NFR-R1). |

## 6. Cryptographic and configuration baselines

| ID | Pri | State | Requirement |
|---|---|---|---|
| SEC-BASE-1 | Must | implemented | Password hashing uses argon2id with parameters documented and tunable, and re-hashing on login when parameters are raised. |
| SEC-BASE-2 | Must | implemented | All random secrets (tokens, codes, session identifiers) come from a cryptographically secure random source; pairing codes are at least 40 bits of entropy, all other tokens at least 128. |
| SEC-BASE-3 | Must | unimplemented | TLS 1.2 or higher only, for all client-facing traffic. |
| SEC-BASE-4 | Should | unimplemented | Every secret (bot credentials, database passwords, encryption key for Matrix state) can be rotated without data loss; the procedure is documented. |
| SEC-BASE-5 | Must | implemented | Security-relevant limits are configuration, not code: token and session lifetimes (idle and absolute), rate limits, share-link maximum lifetime, size limits. Defaults are the safe ones. |

## 7. Relationship to other requirements

- `NFR-S1..S7`, `AUTH-*`, `CORE-SH*`, `WEB-N5` and the Matrix NFRs stay where they are; this document restates none of them, except to make the "must not" explicit.
- Where a new item here needs behaviour that the functional requirements don't yet describe (for example refresh-token family revocation, or the membership re-check before reminders), this document is the authoritative source.

## 8. Verification

| Approach | Covers |
|---|---|
| **Authorisation matrix tests**, generated from the OpenAPI description: every endpoint × every actor in §3 | SEC-ISO-1..4, SEC-ADM-4, SEC-BOT-1..3, SEC-API-2 |
| **Negative abuse-case tests**, one or more per item above, named after the ID (e.g. `SEC-AUTH-7_refresh_reuse_revokes_family`) | All Must items |
| **Renderer/sanitiser tests** with a corpus of XSS payloads and hostile filenames, run against both the app and the public share page | SEC-CNT-1..4 |
| **Response-header tests** asserting the required headers on each response class (app, API, attachments, public pages) | SEC-API-6, SEC-API-8, SEC-SHR-5, SEC-SHR-6, SEC-CNT-3 |
| **Deletion tests** checking that no rows, blobs or index entries remain for a deleted user, note or attachment | SEC-DATA-5 |
| **Log scanning tests** running the integration suite and asserting that no secret or note content marker appears in output | SEC-DATA-1, SEC-SHR-7, SEC-MX-3 |
| **Manifest and image scanning** (policy checks on the example manifests, dependency and image vulnerability scans) in CI | SEC-OPS-1..3, SEC-OPS-6 |
| **Session tests**: access-token expiry, server restart, several tabs, concurrent and retried refresh, and lifetime expiry | SEC-AUTH-6, SEC-AUTH-7, SEC-AUTH-15 |
| **Concurrency tests** for single-use codes, the one-admin rule and quota | SEC-API-7, SEC-BOT-4, SEC-CNT-6 |
| **Periodic manual review** of this document against the current design, and a pre-release check that every `Must` here is at least `implemented` | Everything |
