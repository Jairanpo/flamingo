#!/usr/bin/env bash
# Connects (or replaces) a user's Toggl Track API token. It asks for the
# token without echoing it, then seals it with flamingo_key before storing.
#   sudo scripts/set-toggl.sh jair               # set or replace the token
#   sudo scripts/set-toggl.sh jair --clear       # disconnect
#   sudo scripts/set-toggl.sh jair --workspace ID
set -euo pipefail
user="${1:?usage: sudo scripts/set-toggl.sh USER [--clear|--workspace ID]}"
shift
cd "$(dirname "${BASH_SOURCE[0]}")/../deploy"
docker compose exec -it server \
  flamingo user toggl "$user" -c /etc/flamingo/server.yaml "$@"
