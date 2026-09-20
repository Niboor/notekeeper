#!/usr/bin/env bash
# Runs the browser end-to-end tests against the built images: PostgreSQL, the migration, Core and the
# web image with nginx (docs/design/08). Unlike run.sh it goes through nginx, so the proxy routes, the
# Content-Security-Policy and the separate share host are part of what is tested. Called by
# `make test-e2e-stack`; needs Docker.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
COMPOSE="docker compose -f $ROOT/deploy/docker-compose.stack.yml"
cleanup() { $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker build -q -f deploy/docker/Dockerfile.core -t notekeeper/core:dev . >/dev/null
docker build -q -f deploy/docker/Dockerfile.web -t notekeeper/web:dev . >/dev/null
$COMPOSE up -d --wait db core web

# Operator setup, as in a real installation: the admin (who is also the test user) and a bot instance.
link=$($COMPOSE exec -T core /core admin bootstrap alice | grep -o 'activate#[^ ]*' | head -1)
export E2E_ACTIVATION_TOKEN="${link#activate#}"
$COMPOSE exec -T core /core admin bot-create matrix e2e-bot localhost >/dev/null
export E2E_BOT_KEY="$($COMPOSE exec -T core /core admin bot-credential e2e-bot | grep -o 'nkb\.[^ ]*')"
export E2E_BOT_URL="http://localhost:18081"
export E2E_BASE_URL="http://localhost:8088"
export E2E_STACK=1

cd e2e
if [ -n "${E2E_HOLD:-}" ]; then echo "stack is up on $E2E_BASE_URL"; sleep "${E2E_HOLD}"; exit 0; fi
npx playwright test "$@"
