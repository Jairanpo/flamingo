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

Each person connects their own account; the token is sealed with
`flamingo_key` before it is stored. Lose the key and everyone sets their
token again.

```sh
docker compose exec -it server flamingo user toggl jair -c /etc/flamingo/server.yaml
# --workspace ID to file entries outside the default workspace, --clear to disconnect
```

## Downloads: the agent on each computer

The server never touches a computer's disk. Each computer that should keep
downloads runs the **agent**: it shows the page at `http://127.0.0.1:8421`,
passes calls on to the server, and downloads (video or sound only), keeps
and plays the files. It needs `yt-dlp` and `ffmpeg` installed.

Where files go is set in the app (⬇ folders): one default per system
(Linux, macOS, Windows), and optionally a folder of this computer's own.
Files are found by the `[video id]` in their names, anywhere in the folder.

```sh
go build -o ~/.local/bin/flamingo ./cmd/flamingo
~/.local/bin/flamingo agent pair --server http://127.0.0.1:8430   # once
scripts/install-agent.sh   # starts it now and at every login (no sudo)
```

On another computer, use the server's LAN address in `--server`. Pairing
the same name again replaces its key. The agent's settings live in
`~/.config/flamingo/agent.yaml`; logs: `journalctl --user -u flamingo-agent`.

## Tests

`go test -p 1 ./...` runs everything. The database tests run only when
`FLAMINGO_TEST_DATABASE_URL` points at a disposable database (they wipe it):

```bash
docker compose -f deploy/docker-compose.yml exec db createdb -U flamingo flamingo_test   # once
FLAMINGO_TEST_DATABASE_URL="postgres://flamingo:<password>@127.0.0.1:5433/flamingo_test?sslmode=disable" go test -p 1 ./...
```
