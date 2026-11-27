#!/usr/bin/env bash
# Can the server's container reach YouTube? Run:  sudo scripts/netcheck.sh
# Compares name lookups and an HTTPS request inside the container with a
# throwaway container told to use a public DNS server.
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../deploy"

t() { local s=$(date +%s.%N); "$@"; echo "  -> exit $?, $(echo "$(date +%s.%N) - $s" | bc | cut -c1-5)s"; }

echo "== resolv.conf inside the server"
docker compose exec -T server cat /etc/resolv.conf
echo "== name lookup inside the server"
t docker compose exec -T server timeout 30 getent hosts www.youtube.com
for v in 4 6; do
  echo "== HTTPS to YouTube inside the server over IPv$v"
  t docker compose exec -T server timeout 15 curl -$v -s -o /dev/null -w "  http %{http_code}\n" https://www.youtube.com
done
echo "== yt-dlp version inside the server"
docker compose exec -T server yt-dlp --version
echo "== same lookup from a container using 1.1.1.1 for DNS"
t docker run --rm --dns 1.1.1.1 debian:bookworm-slim timeout 30 getent hosts www.youtube.com
