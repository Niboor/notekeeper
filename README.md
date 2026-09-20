# Notekeeper

Notes from your chats, organised. A chat bot (Matrix first) forwards messages to a web app
where they land in an Inbox and can be sorted into categories on pages, dismissed with undo,
searched, reminded, and shared through expiring read-only links.

Everything is documented under [docs/](docs/):

| Document | Contents |
|---|---|
| [user-guide.md](docs/user-guide.md) | For people who use it, including **who can read your notes** |
| [operations.md](docs/operations.md) | Installing, upgrading, backup and restore, rotating secrets, monitoring |
| [configuration.md](docs/configuration.md) | Every setting |
| [bot-guide.md](docs/bot-guide.md) | Writing a bot for another chat platform |
| [requirements.md](docs/requirements.md) | Functional and non-functional requirements, with implementation state |
| [security-requirements.md](docs/security-requirements.md) | What must not be possible; access matrix; accepted risks |
| [tech-stack.md](docs/tech-stack.md) | Technology choices |
| [design/](docs/design/README.md) | Technical design, and a requirement-to-design-to-test traceability table |

## Working on it

Requirements: Go, Node.js, Docker (for integration tests), Python 3, `make`, and for the Matrix bot a C compiler plus the libolm library and headers (Arch: `sudo pacman -S libolm`; Debian/Ubuntu: `libolm-dev`).

```bash
make help        # list targets
make tools       # install pinned developer tools into .bin/
make web-install # install web dependencies
make up          # start PostgreSQL for development
make test        # unit tests of every component (no Docker, no network)
make check       # everything CI runs
```

### Try it locally

```bash
make up                                       # PostgreSQL (development roles are created on first start)
make dev-core                                 # migrate and run Core on :8080 (user), :8081 (bot), :9090 (ops)
make dev-admin ARGS="bootstrap you"           # create the admin; prints the activation link
make dev-web                                  # the web app on http://localhost:5173, proxying /api to Core
make dev-admin ARGS="bot-create matrix matrix-dev your.homeserver.name"
make dev-admin ARGS="bot-credential matrix-dev"   # prints the bot key once
```

Open the activation link, choose a password, then link a chat under *Settings → Chats*. To run
the bot itself see `bots/matrix` (`matrix-bot run`, configured through environment variables, listed in
[docs/configuration.md](docs/configuration.md)).

### Run the built images

```bash
make images            # notekeeper/core, notekeeper/web, notekeeper/matrix-bot (tag :dev)
make test-e2e-stack    # the whole app behind nginx in Docker, browser tests included
```

`deploy/docker-compose.stack.yml` is that stack; `deploy/k8s` has example Kubernetes manifests
(`make lint` renders and checks them). For a real installation read [docs/operations.md](docs/operations.md).

Layout: `api/` OpenAPI specs, `core/` backend, `bots/` chat bots and their shared client,
`web/` web app, `e2e/` browser tests, `deploy/` images, nginx and example manifests, `docs/` documentation.

Test tiers: `make test` (unit), `make test-integration` (Docker: PostgreSQL 16 and latest, Synapse), `make test-e2e`
(browser tests against a local stack), `make test-e2e-stack` (browser tests against the built images), `make test-perf`
(latency at 50 000 notes).
