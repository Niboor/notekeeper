# Notekeeper — Technology stack

Status: draft v0.1. This is a design document: it records the technology choices that the technical design builds on. Requirement IDs (`CORE-…`, `SEC-…`, `NFR-…`) refer to [requirements.md](requirements.md) and [security-requirements.md](security-requirements.md). There is no State column here; a choice changes only by editing this file and adding a decisions-log entry in `requirements.md`.

## 1. Summary

| Component | Choice | Main reason |
|---|---|---|
| Core API and workers | **Go**, one binary | Streaming attachments with bounded memory (CORE-A6, CORE-A8), single static image, same language as the bot |
| API contract | **OpenAPI 3, spec-first**, `oapi-codegen` (with the caveats in §3.2) | OpenAPI is the source of truth (NFR-API1); generated authorisation tests (SEC-ISO-1) |
| Database access | **pgx v5 + sqlc**, migrations with **goose** | Plain SQL, typed, no ORM; a dedicated listener connection for `LISTEN/NOTIFY` (CORE-S2) |
| Background jobs | **River** (Postgres queue) | Reminders, deliveries, cleanup without Redis (CORE-R5, NFR-D2) |
| Realtime | **SSE** fed by Postgres `LISTEN/NOTIFY` | Works through ingress and from a future Android client (CORE-S1, AND-4) |
| Matrix bot | **Go + mautrix-go**, libolm | Only SDK with a built-in Postgres crypto store (MX-N1, BOT-B5) |
| Web app | **React 19 + TypeScript + Vite** static SPA, including the share page | Accessible drag and drop and primitives (WEB-3, WEB-5, WEB-N4); one UI codebase |
| Database | **PostgreSQL 16+** (`unaccent`, `pg_trgm`) | NFR-D8, NFR-D2, CORE-N13 |
| Tests | Go `testing` + testcontainers, Vitest, Playwright, Synapse | NFR-Q2, NFR-Q5 |
| Packaging | Multi-stage Dockerfiles, docker-compose, Kustomize examples | NFR-D6 |

Languages: **Go** (backend and bots) and **TypeScript** (web). Kotlin joins later for Android.

## 2. Repository layout

A monorepo, so that the API contract, generated clients and all components change together.

```
api/            OpenAPI specs (user, bot, public) and generator configuration
core/           Core API, workers, migrations, SQL queries
bots/matrix/    Matrix bot
bots/sdk/       Go client for the bot API, shared by all future bots (generated + thin helpers)
web/            React SPA (application and share page)
deploy/         docker-compose, example Kubernetes manifests (Kustomize)
docs/           requirements, security requirements, this document, design
Makefile        entry point for build, test, lint (see §9)
```

## 3. Core

### 3.1 Runtime and libraries

- **Go**, current stable release, `CGO_ENABLED=0` for Core.
- **HTTP:** standard library `net/http` with `chi` for routing and middleware. Structured logging with `log/slog` (JSON, request IDs propagated from bots, NFR-O1); Prometheus metrics with `prometheus/client_golang` (NFR-O2). No tracing (dropped).
- **Database:** `pgx` v5 with `pgxpool`; queries written in SQL and compiled with `sqlc`; migrations with `goose` (embedded SQL, run as a Kubernetes Job or init container, NFR-D4).
- **Passwords:** argon2id from `golang.org/x/crypto` (SEC-BASE-1); common-password list embedded in the binary (SEC-AUTH-5).
- **Tokens and sessions:** opaque MAC-derived tokens (not JWTs, and not stored: they are recomputed from the session id and a server key, so a database leak reveals no token), always validated against the session row so revocation is immediate (SEC-AUTH-6) and any replica can serve any request (AUTH-C5). Refresh-token rotation with a grace window (SEC-AUTH-7). Sliding session lifetime (AUTH-C9). See design/03-auth.md §2.
- **OAuth 2.0 authorisation code + PKCE (AUTH-C3):** a small hand-written endpoint for our own first-party clients. Runner-up: `zitadel/oidc`, if third-party clients ever appear.
- **Modes:** the same binary starts as `core serve` (HTTP API) and runs River workers in-process. Splitting workers into their own Deployment later needs no code change.

### 3.2 API contract and `oapi-codegen`

The OpenAPI documents in `api/` are written first. There are three: **user API**, **bot API** and **public API** (share pages). `oapi-codegen` generates Go types, chi routing and strict server interfaces, and the TypeScript client is generated with `openapi-typescript` and `openapi-fetch`. A Kotlin client can be generated later.

Known caveats and how we handle them:

1. **No multipart uploads.** The strict server hands you a raw `multipart.Reader` (or fails outright) and buffering is easy to trigger. Attachments are uploaded as a plain `PUT` of the raw body (`application/octet-stream`, filename and media type in headers), which the strict server exposes as an `io.Reader`.
2. **Request validation middleware reads bodies.** The strict server does not validate requests itself, so a validator middleware (`kin-openapi`) is used. It is configured to **skip body validation on the upload routes**; parameters and headers are still validated there.
3. **Downloads use custom response types.** The generated response visitor is replaced by one that calls `http.ServeContent` (`Range`, `If-None-Match`, `206`/`304`; NFR-API4). It needs an `io.ReadSeeker` over the chunked attachment rows in Postgres.
4. **SSE is not generated.** OpenAPI 3.1 cannot describe `text/event-stream` properly, so the events endpoint is a hand-written handler registered beside the generated routes, and appears in the spec as a stub that is excluded from generation.
5. **Everything must still be in the spec** (SEC-ISO-1). A CI test walks the router and fails if any route has no operation in the spec, so hand-written routes cannot slip in unnoticed.
6. **Generated code is committed** and CI fails if regenerating changes it (`make generate` then `git diff --exit-code`). The generator version is pinned.
7. **Guardrail for streaming:** a test uploads and downloads a large file with a small `GOMEMLIMIT` and asserts that memory stays flat (CORE-A8). If this test cannot be made to pass with the generated code, we revisit the tool. **Huma** is the fallback (code-first, built-in SSE and streaming responses), with the spec generated, committed and diffed in CI.
8. **Spike result (M0, September 2026, oapi-codegen v2.8.0, `GOMEMLIMIT=64MiB`):** the strict server hands the handler `r.Body` directly, and a chunked upload into PostgreSQL held peak RSS at **26 MiB** for one 100 MB upload, four concurrent 100 MB uploads, a full download and a `Range` request (206, bytes verified). With the validator middleware's `ExcludeRequestBody` it was 28 MiB; **with full body validation it reached 917 MiB**, confirming caveat 2. `Range` support needs the `*http.Request` inside the response visitor, which the generated visitor signature does not pass: a strict middleware stores the request in the context and the handler returns a custom response object that calls `http.ServeContent`. The tool stays.

### 3.3 Realtime, jobs and PostgreSQL usage

- **Realtime (CORE-S1, CORE-S2):** each replica holds one dedicated long-lived `pgx.Conn` (outside the pool) that runs `LISTEN`; incoming notifications fan out to connected SSE clients. Clients that reconnect catch up through the change feed (CORE-S3), so a missed notification is harmless.
- **Connection pooling:** no PgBouncer in v1. If one is added later, the listener connection bypasses it or uses session mode, and transaction mode is used for everything else (CORE-A8).
- **Jobs (River):** reminder scheduling, delivery retries and expiry, share-link expiry purge, session cleanup, attachment blob cleanup. Claimed with `SKIP LOCKED`, enqueued transactionally with the change that causes them (CORE-R4, CORE-R5).
- **Search (CORE-N13):** a trigger-maintained `note_search` table (the indexed text spans note parts and attachment filenames, so a generated column cannot work) holding a `tsvector` built with the `english` configuration, `unaccent` wrapped in an immutable SQL function, and a GIN index. `pg_trgm` for prefix matching and optional typo tolerance. See design/01-data-model.md §8.
- **Identifiers:** UUIDv7 generated in the application, since the database-side generator is only in PostgreSQL 18 (NFR-D8).
- **Attachments (CORE-A2, CORE-A8):** chunked `bytea` rows in an ordinary table with cascading delete, behind a storage-backend interface; not Large Objects.
- **No server-side media processing** (CORE-A4, SEC-CNT-5).

### 3.4 Markdown

Note text is rendered **in the browser**, by one shared React component used in the app and on the share page (SEC-CNT-1, SEC-CNT-2). Markdown is parsed with raw HTML disabled and a URL-scheme allowlist, and task-list items are rendered as checkboxes whose toggles write back to the source text (CORE-N17). Core stores and serves plain Markdown and never renders HTML. Formatted HTML from Matrix (`formatted_body`) is converted to Markdown by the bot (MX-5), through a maintained HTML-to-Markdown library and an allowlist.

## 4. Matrix bot

- **Go with `mautrix-go`**, using `CryptoHelper` and its SQL crypto store on PostgreSQL, in its own schema and with its own database role (SEC-OPS-5). The pickle key that encrypts device keys at rest comes from a Kubernetes Secret (MX-N1).
- **Olm implementation: libolm via cgo**, exactly as the mautrix bridges do. It needs a C compiler and libolm headers at build time and the libolm shared library at run time, so the bot image is a slim Alpine or Debian image rather than distroless. libolm is deprecated upstream. `-tags goolm` (pure Go, experimental) is the fallback if that becomes a problem.
- **Single active instance (MX-N2):** a PostgreSQL advisory lock held on a dedicated connection, plus a `Recreate` update strategy.
- **Bot API client:** the shared Go client in `bots/sdk`, generated from the bot API spec.
- **Attachments:** the bot fetches and decrypts media from the configured homeserver only, via `mxc://` URIs (SEC-CNT-8).
- **Verification:** before the bot is built out, review how mautrix-whatsapp wires `CryptoHelper` and its Postgres store, and keep an encrypted-DM round trip (text and attachment) with Synapse as the bot's first integration test.

## 5. Web app

- **React 19 + TypeScript + Vite**, built to static files. Both the application and the share page (CORE-SH10) are routes of the same build.
- **Data:** TanStack Query for server state, optimistic updates (WEB-3) and applying SSE events (WEB-11); the native `EventSource` for SSE (cookie authentication works there).
- **API client:** generated from the OpenAPI specs.
- **Drag and drop (WEB-3, WEB-5, WEB-N4):** `dnd-kit` with its keyboard sensor and screen-reader announcements. `@dnd-kit/react` is still pre-1.0, so the design starts from the stable packages. Runner-up: Atlassian's Pragmatic drag and drop.
- **UI:** Tailwind CSS with Radix primitives for accessible dialogs, menus and popovers (WEB-N4).
- **PWA (WEB-N8):** `vite-plugin-pwa` (Workbox) for the app shell.
- **Localisation:** English only in v1, strings externalised through a message catalogue (NFR-Q3).
- **Serving:** static files from `nginx-unprivileged`. The ingress routes `/api` on the application hostname to Core, so cookies are same-origin and no CORS is needed (SEC-API-6).
- **Share hostname (CORE-SH8, CORE-SH10):** a second hostname, preferably on a separate registrable domain, that serves the same static build but sets no cookies, has its own strict Content-Security-Policy, and routes only the public API to Core. The share token is read from the URL fragment and sent to the public API in a header.
- **Runner-up:** Svelte 5 with `svelte-dnd-action`, if React is later unwanted.

## 6. PostgreSQL

- **Supported: 16 and later** (NFR-D8). CI runs the newest stable release and 16.
- **Extensions:** `unaccent` and `pg_trgm`, both standard contrib modules, available on managed PostgreSQL.
- **Roles (SEC-OPS-5):** a migration role; a Core runtime role without DDL rights; a separate role and schema for the bot's state.
- **Operation:** provisioning, HA and backups of PostgreSQL itself are the operator's responsibility; a backup and restore procedure is documented (NFR-R4). docker-compose includes a single instance for development.

## 7. Packaging and deployment

| Image | Base | Notes |
|---|---|---|
| `core` | `distroless/static:nonroot` | Static Go binary, no cgo |
| `matrix-bot` | slim Alpine or Debian | cgo, libolm |
| `web` | `nginx-unprivileged` | Static build; app and share hostnames |

- **docker-compose** (development and end-to-end tests): PostgreSQL, Synapse, Core, web, Matrix bot.
- **Kubernetes:** example manifests only (NFR-D6), plain YAML with Kustomize: Deployments, Services, Ingress with two hostnames, ConfigMap and Secret templates, a migration Job, probes, security contexts, NetworkPolicies (SEC-OPS-1..3). Core runs two replicas; the bot one replica with `Recreate`.
- **Config:** environment variables and Secrets; token and session lifetimes, rate limits, size limits and the share-link maximum lifetime are configuration (SEC-BASE-5).

## 8. Android (later)

Kotlin with Jetpack Compose, a client generated from the same OpenAPI specs, AppAuth for the PKCE flow, Room and WorkManager when offline support is added. Nothing is needed now beyond the API constraints already in the requirements (§9 of `requirements.md`).

## 9. Tooling and tests

Entry point is the `Makefile` (NFR-Q5); `make help` lists every target.

| Target | What it runs | Needs |
|---|---|---|
| `make test` | Unit tests of every component | Nothing (no Docker, no network) |
| `make test-core`, `make test-bot`, `make test-web` | Unit tests of one component | Nothing |
| `make test-integration` | Per-component integration tests against real PostgreSQL 16 and the newest release, including the streaming memory guardrail (§3.2) and the OpenAPI-generated authorisation matrix (SEC-ISO-1) | Docker |
| `make test-e2e` | Whole stack in docker-compose, with Playwright driving the web app, and a second mautrix client acting as the Element user against Synapse | Docker |
| `make lint` | `golangci-lint`, ESLint, `tsc`, `kube-linter` on the examples | — |
| `make generate` | Regenerate code from the OpenAPI specs and SQL | — |
| `make docs-check` | Regenerate the requirement → design → test traceability table and fail on drift (`docs/design/tools/traceability.py`) | Python 3 |
| `make check` | Everything CI runs, including `govulncheck`, `osv-scanner`, `trivy` on images, `gitleaks` and `docs-check` | Docker |

- **Go tests:** the standard `testing` package with `testcontainers-go`; unit tests carry no container dependency.
- **Web tests:** Vitest and Testing Library for unit tests, Playwright for end-to-end.
- **Matrix:** Synapse in a container as the reference homeserver for the bot's integration and end-to-end tests (MX-N3 requires only that other spec-compliant servers work).

## 10. Risks and open technical points

| Risk / open point | Handling |
|---|---|
| libolm is deprecated | Bridges use it in production; `goolm` as fallback (§4) |
| `oapi-codegen` and streaming | Caveats and guardrail test in §3.2; Huma as fallback |
| `@dnd-kit/react` is pre-1.0 | Start from stable packages; keep Pragmatic DnD as fallback |
| `LISTEN/NOTIFY` and poolers | Dedicated listener connection, no PgBouncer in v1 (§3.3) |
| Attachments in PostgreSQL grow the database and WAL | Quotas (CORE-A3), storage-backend interface for a later object store (CORE-A2, CORE-A7) |
| Public attachment access from `<img>` tags cannot carry a header | Resolved in the design: the share page fetches attachments with the token header into blob URLs, so no token or signature is ever in a URL; no `Range` on the share page (design/README.md D2) |

Points deliberately left to the technical design document: database schema, position/ordering strategy for notes, exact token and cookie format, the public attachment URL scheme, the grouping module structure, and the API resource model.

## 11. Version pins and notes from M0

- **Go** 1.26.4 (`go.work` and `go.mod` files); one workspace with three modules: `core`, `bots/sdk`, `bots/matrix`, so Core stays free of the bot's cgo dependencies.
- **Developer tools** are pinned in the `Makefile` and installed into `.bin/` by `make tools` (sqlc, goose, oapi-codegen, golangci-lint, govulncheck, gitleaks, kubeconform, kube-linter, osv-scanner). `trivy` is run through its container image when image scanning is added.
- **libolm** (system package, with headers) and a C compiler for the Matrix bot; `goolm` was considered and not chosen (decisions log entry 37).
- **Node** 22 or newer, npm; no pnpm. **TypeScript is pinned to 5.9**: `openapi-typescript` requires `^5` and `typescript-eslint` `<6.1`, so TypeScript 7 cannot be used yet.
- Module path prefix is `notekeeper/…` (workspace-local); rename when a repository host is chosen.
