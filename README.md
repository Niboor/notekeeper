# Notekeeper

Notes from your chats, organised. A chat bot (Matrix first) forwards messages to a web app
where they land in an Inbox and can be sorted into categories on pages, dismissed with undo,
searched, reminded, and shared through expiring read-only links.

Everything is documented under [docs/](docs/):

| Document | Contents |
|---|---|
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
[docs/design/08-deployment-and-testing.md](docs/design/08-deployment-and-testing.md)).

Layout: `api/` OpenAPI specs, `core/` backend, `bots/` chat bots and their shared client,
`web/` web app, `deploy/` example deployment files, `docs/` documentation.
