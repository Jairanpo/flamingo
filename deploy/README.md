# Deploying the Go server

The Python app (`./yt`, `ytlocal/`) keeps running as before; this is the
new server growing next to it. See `docs/go-backend-plan.md`.

## With Docker Compose

```bash
cd deploy
cp .env.example .env          # set FLAMINGO_DB_PASSWORD
docker compose up -d --build
curl localhost:8420/healthz   # {"db":"ok","schema_version":1,"status":"ok"}
docker compose exec server flamingo migrate status -c /etc/flamingo/server.yaml
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

## Tests

`go test -p 1 ./...` runs everything. The database tests run only when
`FLAMINGO_TEST_DATABASE_URL` points at a disposable database (they wipe it):

```bash
docker compose -f deploy/docker-compose.yml exec db createdb -U flamingo flamingo_test   # once
FLAMINGO_TEST_DATABASE_URL="postgres://flamingo:<password>@127.0.0.1:5433/flamingo_test?sslmode=disable" go test -p 1 ./...
```
