#!/usr/bin/env bash
# Runs the browser end-to-end tests against a real stack: PostgreSQL (docker compose), the Core
# binary, and the production build of the web app. Called by `make test-e2e`.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
export PATH="$ROOT/.bin:$PATH"

DB=nk_e2e
COMPOSE="docker compose -f deploy/docker-compose.yml"
CORE_USER=18080 CORE_BOT=18081 CORE_PUBLIC=18082 CORE_OPS=19090 WEB=4174
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

$COMPOSE up -d --wait db
# A fresh database every run, with the roles from the operator script (idempotent).
$COMPOSE exec -T db psql -U nk -d postgres -v ON_ERROR_STOP=1 -q -c "drop database if exists $DB with (force)" -c "create database $DB"
$COMPOSE exec -T db psql -U nk -d "$DB" -v ON_ERROR_STOP=1 -q < deploy/sql/roles.sql

(cd core && go build -o ../.bin/core ./cmd/core)
(cd web && npm run build --silent)

export NK_DATABASE_URL="postgres://nk_app:nk_app_dev@localhost:5432/$DB?sslmode=disable"
export NK_MIGRATE_DATABASE_URL="postgres://nk_migrate:nk_migrate_dev@localhost:5432/$DB?sslmode=disable"
export NK_TOKEN_KEYS="e2e:$(head -c 32 /dev/urandom | base64)"
export NK_APP_URL="http://localhost:$WEB"
export NK_USER_ADDR=":$CORE_USER" NK_BOT_ADDR=":$CORE_BOT" NK_PUBLIC_ADDR=":$CORE_PUBLIC" NK_OPS_ADDR=":$CORE_OPS"
export NK_ARGON2_MEMORY_KIB=8192 NK_LOG_LEVEL=warn

.bin/core migrate
.bin/core serve >"$ROOT/e2e/core.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 60); do curl -fs "http://localhost:$CORE_OPS/readyz" >/dev/null && break; sleep 0.5; done
curl -fs "http://localhost:$CORE_OPS/readyz" >/dev/null || { echo "Core did not become ready:"; cat e2e/core.log; exit 1; }

# Operator setup: the admin (who is also the test user), a Matrix bot instance and its key.
link=$(.bin/core admin bootstrap alice | grep -o 'activate#[^ ]*' | head -1)
export E2E_ACTIVATION_TOKEN="${link#activate#}"
.bin/core admin bot-create matrix e2e-bot localhost >/dev/null
export E2E_BOT_KEY="$(.bin/core admin bot-credential e2e-bot | grep -o 'nkb\.[^ ]*')"
export E2E_BOT_URL="http://localhost:$CORE_BOT"
export E2E_BASE_URL="http://localhost:$WEB"

(cd web && NK_API_PROXY="http://localhost:$CORE_USER" npx vite preview --port "$WEB" --strictPort >"$ROOT/e2e/web.log" 2>&1) &
pids+=($!)
for _ in $(seq 1 60); do curl -fs "http://localhost:$WEB/" >/dev/null && break; sleep 0.5; done

cd e2e
npx playwright test "$@"
