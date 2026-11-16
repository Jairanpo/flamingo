#!/usr/bin/env bash
# Rebuilds and restarts the Flamingo server container with the current code,
# then checks it answers. Docker needs root here:  sudo scripts/redeploy.sh
# Migrations run on start (database.migrate_on_start).
set -euo pipefail
deploy="$(cd "$(dirname "${BASH_SOURCE[0]}")/../deploy" && pwd)"
cd "$deploy"
port=$(grep -s '^FLAMINGO_PORT=' .env | cut -d= -f2- || true)
port=${port:-8420}

docker compose up -d --build server
for _ in $(seq 1 20); do
  if curl -fsS "localhost:$port/healthz"; then echo; exit 0; fi
  sleep 1
done
echo "server did not answer on port $port; recent logs:" >&2
docker compose logs --tail 40 server >&2
exit 1
