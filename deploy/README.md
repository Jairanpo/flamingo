# Deploying the Go server

The Python app (`./yt`, `ytlocal/`) keeps running as before; this is the
new server growing next to it. See `docs/go-backend-plan.md`.

## With Docker Compose

```bash
cd deploy
cp .env.example .env          # set FLAMINGO_DB_PASSWORD
openssl rand -base64 32 > flamingo_key   # seals Toggl tokens; back it up
chmod 644 flamingo_key        # the container's user (uid 10001) must read it
docker compose up -d --build
curl localhost:8420/healthz   # {"db":"ok","schema_version":1,"status":"ok"}
docker compose exec server flamingo migrate status -c /etc/flamingo/server.yaml
```

After changing the code, rebuild and restart the server in one step (it
waits for `/healthz`, and prints the logs if the server does not come up):

```bash
sudo scripts/redeploy.sh
```

## Without Docker

```bash
go build -o bin/flamingo ./cmd/flamingo
FLAMINGO_DATABASE_URL=postgres://flamingo:pw@localhost:5432/flamingo ./bin/flamingo serve
./bin/flamingo migrate status       # also: up, down
```

Config is YAML (`~/.config/flamingo/server.yaml` by default, or `-c file`). Any
key can be overridden with `FLAMINGO_<SECTION>_<KEY>`, and `${VAR}` inside values
is expanded from the environment.

## Importing the Python app's library

Postgres listens on `127.0.0.1:5433` for this machine only.

```bash
export FLAMINGO_DATABASE_URL="postgres://flamingo:$(grep FLAMINGO_DB_PASSWORD deploy/.env | cut -d= -f2)@127.0.0.1:5433/flamingo?sslmode=disable"
./bin/flamingo import sqlite --user jair --display-name Jair --device "$(hostname)"
```

It prints row counts per table plus notes on anything it couldn't carry
over. Running it again replaces that user's library rather than doubling it.

## Syncing the catalog

`flamingo sync` lists every tracked channel and playlist again and stores what
changed (new uploads, removals, videos gone private):

```sh
docker compose exec server flamingo sync -c /etc/flamingo/server.yaml            # everything not shelved
docker compose exec server flamingo sync -c /etc/flamingo/server.yaml --limit 30 # quick: newest 30 each
docker compose exec server flamingo sync -c /etc/flamingo/server.yaml --source UC…
```

The image pins yt-dlp (`YTDLP_VERSION` in the Dockerfile). When YouTube
changes and listings start failing, bump it and rebuild.

## Toggl Track

Each person connects their own account from the page: **your name (top right) → Toggl
Track**, paste the API token (Toggl → Profile settings), optionally a
workspace id. The server checks it with Toggl, seals it with
`flamingo_key`, and never shows it again — it can only be replaced or
removed. Lose the key and everyone sets their token again.

An admin can do the same from the terminal:

```sh
sudo scripts/set-toggl.sh jair        # --workspace ID, or --clear to disconnect
```

## Downloads: the agent on each computer

The server never touches a computer's disk. Each computer that should keep
downloads runs the **agent**, a small background helper at
`127.0.0.1:8421` with no page of its own. You always open Flamingo at the
server's address; the page finds the agent on its computer, gets a
short-lived ticket for it from the server, and asks it directly to download
(video or sound only), delete and play files. A computer without an agent,
like a phone, streams.

**Setting a computer up:** in the app, ⬇ set up downloads. Pick the system
(Linux, macOS, Windows), download the helper, install `yt-dlp` and
`ffmpeg` once, then get a one-time pairing code and run the one command the
page shows. That is `flamingo agent install --server <address> --code
<code>`: it keeps the program (`~/.local/bin`, Application Support, or
`%LOCALAPPDATA%\Flamingo`), pairs the computer and starts the agent at
every login. The page turns downloads on by itself once it answers.

On this machine, from the repository: `scripts/install-agent.sh` builds and
reinstalls it (no sudo; already paired, so no code).

Where files go is set in the app (⬇ folders): one default per system, and
optionally a folder of this computer's own. Files are found by the
`[video id]` in their names, anywhere in the folder.

The agent's settings live in `~/.config/flamingo/agent.yaml`. If the page
is opened at an address other than `server_url` (a cloud server behind its
own name), list it under `page_origins`. Logs: `journalctl --user -u
flamingo-agent`.

## Tests

`go test -p 1 ./...` runs everything. The database tests run only when
`FLAMINGO_TEST_DATABASE_URL` points at a disposable database (they wipe it):

```bash
docker compose -f deploy/docker-compose.yml exec db createdb -U flamingo flamingo_test   # once
FLAMINGO_TEST_DATABASE_URL="postgres://flamingo:<password>@127.0.0.1:5433/flamingo_test?sslmode=disable" go test -p 1 ./...
```
