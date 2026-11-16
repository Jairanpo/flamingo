# Deploying the Go server (phase 1)

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

## Tests

`go test ./...` runs everything. The database test runs only when
`YT_TEST_DATABASE_URL` points at a disposable Postgres; it migrates up and
back down.
