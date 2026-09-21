# Running Notekeeper

For the person who installs and looks after a deployment. What every setting means is in
[configuration.md](configuration.md); how the parts fit together is in
[design/08](design/08-deployment-and-testing.md). The Kubernetes files in `deploy/k8s` are
**examples**: they show the shape and the security posture, and you adapt them to your cluster.

## 1. What runs

| Component | Image | Scale | Notes |
|---|---|---|---|
| Core | `notekeeper/core` | 2+ replicas | Stateless. Four listeners: user API 8080, bot API 8081 (cluster only), public share API 8082, health and metrics 9090 (cluster only). Also runs the background jobs (reminders, cleanup, deletions) on every replica; work is claimed so each item is done once |
| Web | `notekeeper/web` | 2+ replicas | nginx serving the built app on the application host and, on the share host, only the share page. Sends the security headers |
| Matrix bot | `notekeeper/matrix-bot` | exactly 1 | One instance per bot account: two would fight over one Matrix device. `Recreate` strategy plus a database lock |
| PostgreSQL 16 or later | yours | 1 | The only stateful part: notes, attachments, sessions, search index, job queue, the bot's encryption state |

## 2. First installation

1. **Database.** Create a database. As a superuser, run `deploy/sql/roles.sql` inside it, then give each role its own password (`alter role nk_app password '…'`, and the same for `nk_migrate` and `nk_bot_matrix`); the script's passwords are published and Core and the bot refuse to start with them: it creates the roles `nk_migrate` (owns the schema, used only by the
   migration), `nk_app` (Core at runtime; cannot alter the schema and is bound by row-level security) and
   `nk_bot_matrix` (the bot, with a schema of its own that cannot read Core's tables).
2. **Secrets.** Generate the token keys (`head -c 32 /dev/urandom | base64`, as `k1:<key>`), and choose the
   database URLs, the bot's Matrix password and a pickle key of 16 or more random characters. Put them in
   Kubernetes Secrets. The example manifests carry `change-me` placeholders that make Core and the bot
   refuse to start, so a forgotten secret cannot go live. **The schema owner's URL
   (`NK_MIGRATE_DATABASE_URL`) goes in a Secret of its own, `notekeeper-migrate`, that only the migration
   Job reads**; Core's Secret holds the runtime URL and the token keys. A compromised Core pod must not be
   able to read the credential that can switch row-level security off. Core logs a warning when it starts
   with the migration URL set.
3. **Migrate.** Run the migration Job (`core migrate` under `nk_migrate`). It is safe to run again.
4. **Start** Core and the web image. Check `/readyz` on port 9090 (database reachable, migrations current).
5. **Create the admin.** `core admin bootstrap <username>` prints an activation link, once. Open it on the
   application host and choose a password. There is exactly one admin and no way to become one otherwise.
6. **Register the Matrix bot** in *Administration → Chat bots* (or `core admin bot-create matrix <name>
   <homeserver domain>` and `core admin bot-credential <name>`). The bot key is shown once: put it in the
   bot's Secret as `NK_BOT_KEY`. The domain is the part after the colon in your users' Matrix IDs; the
   bot refuses to link identities from any other domain.
7. **The Matrix account.** (One bot serves one homeserver: its own. Users of other servers, even federated ones, are declined; set `MX_ALLOWED_DOMAINS` only if you want that, and disable federation on the homeserver if you do not need it.) Create an account for the bot on your homeserver (its name is `MX_USER`), then
   start the bot. It logs in once and keeps its device in PostgreSQL, encrypted with `MX_PICKLE_KEY`.
8. **Create accounts.** In *Administration*, create a user and give them the activation link. They open it,
   choose a password, and link a chat under *Settings → Chats*. Nothing can be self-registered.

## 3. Upgrading

Releases are images on ghcr (`ghcr.io/<owner>/notekeeper-core`, `-web`, `-matrix-bot`), published when a version tag such as `v1.2.3` is pushed and its tests have passed. The GitHub release lists the images and their digests. Tags: `1.2.3` (exact), `1.2` (latest patch of that minor version) and `latest` (latest stable release); a pre-release (`v1.0.0-rc.1`) has its exact tag only. Pin the exact tag, or the digest, in your overlay.

Run the new migration Job first, then roll out the new images. Migrations are forward-only and are written to
work with the previous release still running (add before remove), so a rolling update needs no downtime.
Core drains requests on shutdown and tells open browsers to reconnect. The bot is restarted, not rolled: it
comes back from its stored position and replays anything not yet acknowledged, which Core recognises.
Roll back an image, not a migration: restore from backup if a migration itself must be undone.

## 4. Backup and restore (NFR-R4)

Everything is in PostgreSQL, **attachments included**, so a database backup is a complete backup. There is
no separate file store to keep in step.

* **Backups.** Use your normal PostgreSQL tooling: `pg_dump -Fc` for a logical copy, or base backups plus WAL
  archiving for point-in-time recovery. Attachments make the database larger; size the storage for the
  quotas you set (`NK_DEFAULT_QUOTA_BYTES` times the number of users is the ceiling).
* **Encrypt and restrict backups.** They contain everything in the clear, including note text and files,
  and the bot's encrypted device state. Encrypt them and limit who can read them (SEC-DATA-7).
* **Restore.** Restore into an empty database, run `deploy/sql/roles.sql` if the roles do not exist, run the
  migration Job (a no-op when the dump is current), then start Core and the bot. Sessions in the dump stay
  valid. If the dump is older than the bot's last sync, the bot replays chat messages from its stored
  position; Core ignores the ones it already has.
* **Test it.** Restore a copy at least once a quarter into a scratch database and open it.
  `make test-e2e-stack` shows what a fresh installation looks like.
* **Deleted accounts and backups.** Deleting an account removes its data from the live database. Copies of the
  database taken before that still contain it until they expire. Keep backup retention short and write it
  down for your users (AUTH-U9).

## 5. Rotating secrets (SEC-BASE-4)

| Secret | How |
|---|---|
| Token keys | Prepend a new `kid:key` to `NK_TOKEN_KEYS` and roll out. Old tokens still verify with the old key. Remove the old key after the longest session lifetime, or at once to sign everyone out |
| Database passwords | Change the role's password, update the Secret, restart. `nk_app` and `nk_migrate` are independent |
| Bot key | Create a second credential for the instance (*Administration → New secret*), deploy it, then disable the old one |
| Matrix password | Change it on the homeserver and in the Secret; the existing device keeps working |
| `MX_PICKLE_KEY` | It encrypts the device keys in the database, so changing it makes them unreadable: plan a new device (log the bot out on the homeserver, empty the bot's schema, start it fresh) and tell users that encrypted history from before is not decryptable by the bot |

## 6. Watching it

Prometheus scrapes port 9090 of Core and port 9091 of the bot. `deploy/k8s/base/prometheusrule.yaml` has
example alerts (the "is it down" alerts find Core and the bot by the name of their scrape job: adjust the two patterns in the file
to yours). The ones to care about first:

* the **bot is down** or **Core is down**: chat messages are not arriving (they wait in the chat and are
  replayed afterwards; nothing is lost);
* **outbox backlog** grows: reminders and notices are waiting for a bot to send them;
* **reminder lag** above a minute: the background jobs are not keeping up;
* **failed sign-ins**, **rejected bot requests**, **share link probing** and **rate-limit hits** spike: someone
  is guessing, or a credential leaked.

Also worth an alert: the **reminder job stopped** (`nk_job_last_success_timestamp_seconds`; a stopped job counts no
failures), a reminder or notice **waiting more than five minutes** for a bot, **server errors** above 5% of requests,
requests **waiting for a database connection**, sessions ended because an **old refresh token was replayed**, and
users **refused a live-update stream** because they hold too many.

**Dashboards.** `deploy/grafana/` has four Grafana dashboards, importable as they are (*Dashboards → New → Import*,
one file each; nothing in them is tied to one data source or job name):

| File | What it answers |
|---|---|
| `nk-service.json` | Is Core up, fast and free of errors? Requests, status classes, latency by route, realtime streams, database pool, memory and CPU |
| `nk-pipeline.json` | Do chat messages arrive and do reminders leave on time? Ingest and grouping, reminder lag, outbox depth and age, and when each background task last worked |
| `nk-bot.json` | Is the bot syncing, how old are messages when it handles them, what it sent and what failed, and how it talks to Core |
| `nk-security.json` | Sign-in failures, replayed tokens, rejected bot requests, share-link probing, rate limits |

Each has a *Data source* choice, and *Core* and *Bot* choices for the scrape job names (they are found from the metrics,
so you rarely need to touch them). To load them in Kubernetes with the Grafana sidecar, put them in a
ConfigMap with the label your Grafana watches (`grafana_dashboard: "1"` in kube-prometheus-stack):

```bash
kubectl -n monitoring create configmap notekeeper-dashboards \
  --from-file=deploy/grafana/nk-service.json --from-file=deploy/grafana/nk-pipeline.json \
  --from-file=deploy/grafana/nk-bot.json --from-file=deploy/grafana/nk-security.json
kubectl -n monitoring label configmap notekeeper-dashboards grafana_dashboard=1
```

The JSON is generated by `deploy/grafana/build.py` (run by `make generate`): change that file and regenerate, not the JSON.

Metrics carry no names or content. Logs are JSON with a request id that a chat message keeps from the bot to
Core, so one message can be followed; they never contain note text, file names or secrets.

## 7. Everyday administration

* **Lost password.** *Administration → Activation link* for the user: it clears the password, signs them out
  everywhere and gives a link that sets a new one. The user is told in the app and in their chats.
* **Disable an account** (a departed user, a stolen session): sessions end at once, ingest stops, reminders and
  share links stop working; enabling it restores them.
* **Delete an account:** removes everything about the person and tells the bot to forget their chat. The data
  goes in the background within a minute or so; the account shows as *Being deleted* until then.
* **Storage.** The list shows each account's use. Set a quota per user, or empty it for the default.
* **A user asks for their data.** They can download it themselves under *Settings → Export my data*.
* **The admin never sees notes.** The admin section shows accounts, storage and bots only.
* **Sharing off:** `NK_SHARE_ENABLED=false` stops every link; the app hides the action.

## 8. Security checklist for an installation

* TLS at the ingress (TLS 1.2 or later, HSTS), a different registrable domain for the share host if possible.
* Only ports 8080 and 8082 of Core, and the web image, are reachable from outside. **8081 (bot API) and 9090
  (metrics, health) are cluster-internal**; the example NetworkPolicies allow only what is needed.
* PostgreSQL reachable only from Core, the migration Job and the bot; use `sslmode=verify-full`, which also checks the server certificate.
* Secrets in Secrets, never in ConfigMaps, images or git; nothing shipped with a default password.
* Containers as non-root with read-only root filesystems (the examples do this).
* Backups encrypted, and their retention known.
* Someone reads the alerts.

## 9. When something is wrong

| Symptom | Look at |
|---|---|
| Messages from chat do not appear | Is the bot up (`/readyz` on 9091)? Is the sender linked (*Settings → Chats*)? Did the bot join the room (only two-person direct messages are used)? `nk_bot_requests_rejected_total`, the bot's logs |
| The bot cannot read a message | The bot's device is new to the sender: they must send again; an old Element session may need to verify the bot |
| Reminders late or missing | `nk_reminder_lag_seconds`, `nk_outbox_items`, `nk_outbox_oldest_wait_seconds`, `nk_job_last_success_timestamp_seconds{task="fire_due_reminders"}`, `nk_job_failures_total`; is the target chat still a direct message with one person? |
| "Not ready" | The database is unreachable or a migration is pending: run the migration Job |
| Users see 429 | The rate limits; raise `NK_RATE_*` if legitimate traffic hits them |
| A share link says "not available" | Expired, revoked, note in the Trash, or sharing disabled; the answer is deliberately the same for all of them |
