# Configuration

Everything is configured through environment variables; nothing needs a rebuild (NFR-O5). Secrets come
from Kubernetes Secrets or your secret manager and never from files in an image (SEC-OPS-1). Time
values are Go durations (`15m`, `2160h`); sizes are bytes. Defaults are safe for a small deployment.
A test (`core/internal/config/docs_test.go`, part of `make test`) fails when a variable read by the code is
missing here, or documented here but read by nothing.

## Core (`core serve`, `core migrate`, `core admin ...`)

### Required

| Variable | Purpose |
|---|---|
| `NK_DATABASE_URL` | PostgreSQL connection for the runtime role `nk_app` (16 or later). Prefer `sslmode=require` or better. Refused if it still contains the placeholder `change-me` |
| `NK_TOKEN_KEYS` | Keys that sign access and refresh tokens, `kid:base64key` pairs separated by commas; the first signs, all verify, so keys can be rotated by prepending a new one. At least 32 random bytes each. Refused if it contains a placeholder |
| `NK_APP_URL` | Public address of the web app, `https://notes.example.org`. The host is the only one the user API answers to, and it is the base of links in reminders |
| `NK_SHARE_URL` | Public address of the share host, `https://share.example.net`, on a different registrable domain from the app if you can (CORE-SH8). Share links are built from it |

### Migration

| Variable | Default | Purpose |
|---|---|---|
| `NK_MIGRATE_DATABASE_URL` | | Connection for the schema-owning role `nk_migrate`, used only by `core migrate` (the migration Job), which needs no other credential. The serving processes never hold it (they log a warning if it is set); keep it in a Secret that only the Job reads. Refused if it contains `change-me` |

### Network

| Variable | Default | Purpose |
|---|---|---|
| `NK_USER_ADDR` | `:8080` | Listener of the user API |
| `NK_BOT_ADDR` | `:8081` | Listener of the bot API. Cluster-internal: never route it from an ingress (SEC-OPS-4) |
| `NK_PUBLIC_ADDR` | `:8082` | Listener of the public share API |
| `NK_OPS_ADDR` | `:9090` | Health (`/healthz`, `/readyz`) and metrics (`/metrics`). Never route it from an ingress |
| `NK_APP_HOSTS` | host of `NK_APP_URL` | Comma-separated host names the user API answers to |
| `NK_SHARE_HOSTS` | host of `NK_SHARE_URL` | Comma-separated host names the share API answers to |
| `NK_TRUSTED_PROXIES` | none | Comma-separated CIDRs of the ingress or proxies whose `X-Forwarded-For` is believed, so that per-address limits see the real client. **Set it when Core runs behind an ingress**: left empty (or wrong), every visitor shares the ingress address, and the per-address rate limit and login throttle act on everyone together. Core logs a warning when it sees `X-Forwarded-For` from a peer that is not trusted. Use the addresses of the ingress pods, not a whole cluster range, or any pod could pretend to be any client (SEC-AUTH-2) |
| `NK_SHUTDOWN_TIMEOUT` | `25s` | How long a stopping replica drains requests and event streams |
| `NK_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. Logs are JSON on stdout and never contain note text, file names or secrets |

### Sessions and passwords

| Variable | Default | Purpose |
|---|---|---|
| `NK_ACCESS_TOKEN_TTL` | `15m` | Lifetime of the short access token; the web app renews it silently |
| `NK_SESSION_IDLE_LIFETIME` | `2160h` (90 days) | A session ends after this long without use |
| `NK_SESSION_ABSOLUTE_LIFETIME` | `8760h` (365 days) | A session ends this long after sign-in whatever its use; `0` means unlimited |
| `NK_REFRESH_GRACE` | `60s` | How long the previous refresh token is still accepted, so a lost response does not sign anyone out |
| `NK_ACTIVATION_TTL` | `168h` | How long an activation link works |
| `NK_ARGON2_MEMORY_KIB` | `65536` | argon2id memory cost (SEC-BASE-1); passwords are re-hashed at sign-in when the cost rises |
| `NK_ARGON2_ITERATIONS` | `3` | argon2id time cost |
| `NK_ARGON2_PARALLELISM` | `2` | argon2id threads |
| `NK_ARGON2_CONCURRENCY` | `4` | Passwords hashed at once, so a burst of sign-ins cannot exhaust memory (SEC-API-4) |

### Attachments, sharing and limits

| Variable | Default | Purpose |
|---|---|---|
| `NK_MAX_ATTACHMENT_BYTES` | `26214400` (25 MiB) | Largest single file (CORE-A3) |
| `NK_DEFAULT_QUOTA_BYTES` | `2147483648` (2 GiB) | Storage each user may use unless the admin sets another quota |
| `NK_SHARE_ENABLED` | `true` | `false` switches sharing off; existing links stop working (CORE-SH11) |
| `NK_SHARE_MAX_LIFETIME` | `720h` (30 days) | The longest a share link may live (CORE-SH2) |
| `NK_SHARE_MAX_ACTIVE` | `200` | Links one user may hold at once (SEC-SHR-11) |
| `NK_SHARE_CREATED_PER_HOUR` | `30` | Links one user may create per hour (SEC-SHR-11) |
| `NK_MAX_NOTES_PER_USER` | `100000` | Notes one account may hold (Inbox, pages and Trash). Past it, creating a note is refused with `413 notes_limit` and a chat message is answered with a reply; deleting notes for good makes room (SEC-API-3) |
| `NK_MAX_TEXT_BYTES_PER_USER` | `268435456` | Text one account may hold, counting all note text and its history (256 MiB). Past it, growth is refused with `413 text_limit`; editing downwards still works (SEC-CNT-6). Files have their own quota, `NK_DEFAULT_QUOTA_BYTES` |
| `NK_RATE_USER_PER_MIN` | `1800` | Requests per minute per signed-in user on the user API |
| `NK_RATE_IP_PER_MIN` | `3000` | Requests per minute per client address on the user API |
| `NK_RATE_BOT_PER_MIN` | `6000` | Requests per minute per bot instance |
| `NK_RATE_IDENTITY_PER_MIN` | `600` | Events per minute one linked person may push through a bot, and lookups per bot instance (SEC-BOT-10, SEC-BOT-3) |
| `NK_MAX_CONCURRENT_UPLOADS` | `4` | Uploads one user (or one chat identity) may run at once (SEC-API-4) |

Rate limits are in memory per replica: with two replicas the effective ceiling is twice the value.
Limits that must hold across replicas (sign-in and pairing attempts, share-link creation, exports) are
kept in PostgreSQL and are not configurable here. Refusals are counted in `nk_rate_limited_total`.

## Matrix bot (`matrix-bot run`)

| Variable | Default | Purpose |
|---|---|---|
| `MX_HOMESERVER` | required | `https://matrix.example.org`, the homeserver the bot's account lives on |
| `MX_USER` | required | The bot account: a localpart or a full Matrix ID |
| `MX_ALLOWED_DOMAINS` | the bot's own homeserver | Comma-separated Matrix server names whose users the bot serves. Empty means only the server of the bot's own account: invites from users of any other server are declined and their messages ignored, even when the servers are federated. Core enforces the same from its side, because a bot instance can only link identities of its configured `identity_domain` (SEC-BOT-5) |
| `MX_PASSWORD` | required | The account password, used to log in once; the device is then kept in the crypto store |
| `MX_PICKLE_KEY` | required | At least 16 characters; encrypts the device keys at rest (MX-N1). Keep it stable: losing it loses the device |
| `NK_BOT_DATABASE_URL` | required | The bot's own database role and schema (`nk_bot_matrix`). It cannot read Core's tables (SEC-OPS-5) |
| `NK_CORE_URL` | required | Base address of Core's bot API, cluster-internal (`http://core-bot:8081`) |
| `NK_BOT_KEY` | required | The bot key `nkb.<client id>.<secret>`, created by `core admin bot-credential` or in the admin section |
| `NK_BOT_INSTANCE` | `matrix` | Name of the bot instance registered in Core; also names the single-instance lock |
| `NK_BOT_LISTEN` | `:9091` | Health (`/healthz`, `/readyz`) and metrics of the bot |
| `NK_MAX_ATTACHMENT_BYTES` | `26214400` | Largest file the bot downloads from chat; bigger ones are recorded as failed attachments without being fetched. Keep it equal to Core's |
| `NK_LOG_LEVEL` | `info` | As for Core |

Secrets in the bot's environment (`MX_PASSWORD`, `MX_PICKLE_KEY`, `NK_BOT_KEY`) that still contain
`change-me` make the bot refuse to start (SEC-OPS-7).

## Web image (nginx)

The image renders two server blocks at start from these variables (`deploy/nginx`):

| Variable | Default | Purpose |
|---|---|---|
| `APP_HOST` | `localhost` | Host name of the application; must match `NK_APP_URL` |
| `SHARE_HOST` | `share.localhost` | Host name of the share page; must match `NK_SHARE_URL` |
| `CORE_USER_UPSTREAM` | `core:8080` | Where `/api/v1/` goes on the application host |
| `CORE_PUBLIC_UPSTREAM` | `core:8082` | Where `/api/public/v1/` goes on the share host |

The image needs writable `/tmp`, `/var/cache/nginx` and `/etc/nginx/conf.d` (memory `emptyDir`s in the
example manifests) and runs as user 101 with a read-only root filesystem.

## Test and tooling variables

These are read by tests and scripts, not by the running services: `NK_TEST_LOG`, `NK_TEST_VERBOSE`,
`NK_TEST_PG_IMAGE`, `NK_TEST_PG_IMAGES`, `NK_TEST_SYNAPSE_IMAGE` (see [design 08](design/08-deployment-and-testing.md)
section 6) and `NK_PERF_NOTES` (size of `make test-perf`, default 50 000).
