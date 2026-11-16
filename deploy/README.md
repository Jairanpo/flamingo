# Deploying the Go server

The Python app (`./yt`, `ytlocal/`) keeps running as before; this is the
new server growing next to it. See `docs/go-backend-plan.md`.

## With Docker Compose

```bash
cd deploy
cp .env.example .env          # set YT_DB_PASSWORD
docker compose up -d --build
curl localhost:8420/healthz   # {"db":"ok","schema_version":1,"status":"ok"}
docker compose exec server yt migrate status -c /etc/yt/server.yaml
```

## Without Docker

```bash
go build -o bin/yt ./cmd/yt
YT_DATABASE_URL=postgres://yt:pw@localhost:5432/yt ./bin/yt serve
./bin/yt migrate status       # also: up, down
```

Config is YAML (`~/.config/yt/server.yaml` by default, or `-c file`). Any
key can be overridden with `YT_<SECTION>_<KEY>`, and `${VAR}` inside values
is expanded from the environment.

## Importing the Python app's library

Postgres listens on `127.0.0.1:5433` for this machine only.

```bash
export YT_DATABASE_URL="postgres://yt:$(grep YT_DB_PASSWORD deploy/.env | cut -d= -f2)@127.0.0.1:5433/yt?sslmode=disable"
./bin/yt import sqlite --user jair --display-name Jair --device "$(hostname)"
```

It prints row counts per table plus notes on anything it couldn't carry
over. Running it again replaces that user's library rather than doubling it.

## Tests

`go test -p 1 ./...` runs everything. The database tests run only when
`YT_TEST_DATABASE_URL` points at a disposable database (they wipe it):

```bash
docker compose -f deploy/docker-compose.yml exec db createdb -U yt yt_test   # once
YT_TEST_DATABASE_URL="postgres://yt:<password>@127.0.0.1:5433/yt_test?sslmode=disable" go test -p 1 ./...
```
