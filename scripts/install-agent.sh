#!/usr/bin/env bash
# Builds the flamingo binary from this repository and (re)installs this
# computer's agent with it: kept in ~/.local/bin, started at every login
# (systemd user unit). No sudo needed.
# First time on this computer? Get a code in the app (⬇ set up downloads):
#   scripts/install-agent.sh --server http://localhost:8430 --code XXXX-XXXX
set -euo pipefail
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(cd "$repo" && go build -trimpath -o "$tmp/flamingo" ./cmd/flamingo)
"$tmp/flamingo" agent install "$@"
