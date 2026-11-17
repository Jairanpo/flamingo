#!/usr/bin/env bash
# Builds the flamingo binary, installs it to ~/.local/bin, and starts this
# computer's agent at every login (systemd user unit). No sudo needed.
# Pair first, once:  ~/.local/bin/flamingo agent pair --server <url>
set -euo pipefail
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin="$HOME/.local/bin"
units="$HOME/.config/systemd/user"

mkdir -p "$bin" "$units"
(cd "$repo" && go build -trimpath -o "$bin/flamingo" ./cmd/flamingo)
echo "installed $bin/flamingo"

if [ ! -s "$HOME/.config/flamingo/agent.token" ]; then
  echo "not paired yet. Run this, then this script again:" >&2
  echo "  $bin/flamingo agent pair --server http://127.0.0.1:8430" >&2
  exit 1
fi

cp "$repo/deploy/flamingo-agent.service" "$units/"
systemctl --user daemon-reload
systemctl --user enable flamingo-agent.service
systemctl --user restart flamingo-agent.service
sleep 2
if curl -fsS -o /dev/null 127.0.0.1:8421/login; then
  echo "agent up: open http://127.0.0.1:8421"
else
  echo "agent did not answer; recent logs:" >&2
  journalctl --user -u flamingo-agent -n 30 --no-pager >&2
  exit 1
fi
