# Plan: multi-user Go backend

Status: proposal, 2026-11-15. Nothing here is built yet.

## Goal

Move `yt` from a one-person Python app with one SQLite file to a
small home service that several people share.

- **One central server** on the Linux machine: Go, Postgres, and Docker
  Compose. It holds the catalog and everyone's personal data.
- **Each person has an account.** Logging in shows that person's own
  channels, playlists, progress, pins, collections, marks and Toggl
  integration.
- **Each computer keeps its own downloads** on its own SSD. A small helper
  program on each computer, the **agent**, downloads and plays the files.
  The database stores lists and settings only, never video files.
- **Toggl Track is a per-user integration.** Each user pastes their own API
  key. Settings come in layers: channel first, then playlist.
- **Later, the cloud.** The same pieces run on a hosted server with HTTPS.
  Devices without an agent, such as a phone, stream through the server, as
  the current streaming mode already does.

What stays the same: the UI and how it behaves, the sealed page (strict
CSP, no direct calls from the page to YouTube), yt-dlp as the engine
(called as an external program), and every Toggl behaviour built so far.

## The pieces

```
  ┌─────────────── Linux machine (docker compose) ───────────────┐
  │  yt-server (Go)  ── Postgres        thumbs volume             │
  │   • login, users, sessions          (cached thumbnails)       │
  │   • catalog sync with yt-dlp (metadata only)                  │
  │   • per-user state API, Toggl calls, stream relay             │
  └──────────────────────────────▲────────────────────────────────┘
                                 │ HTTP on the home network
        ┌────────────────────────┴───────────────────────┐
  ┌─────┴──────────── each computer ─────────┐   (phone: browser only,
  │  yt-agent (Go, runs as the logged-in OS  │    streams via server)
  │  user, listens on 127.0.0.1:8421)        │
  │   • serves the UI to the browser         │
  │   • proxies /api/* to the server         │
  │   • downloads with yt-dlp + ffmpeg       │
  │   • plays local files /media/<id>        │
  │   • small local SQLite: what's on disk   │
  └──────────────────────────────────────────┘
```

**Why the browser talks to the agent, not the server directly.** The page
then has a single origin (`127.0.0.1:8421`). Local files and server data
come through the same door, so there are no CORS rules, no
mixed-content problems and no cookie trouble, and the CSP stays
`connect-src 'self'`. Without an agent, the browser opens the server
directly and gets the same UI with downloads turned off (stream only).

## Data model (Postgres)

There are three kinds of data, kept separate:

| Kind | Tables | Shared? |
|---|---|---|
| Catalog (facts about YouTube) | `channels`, `sources`, `videos`, `source_videos`, `video_chapters` | shared by everyone, so a video's metadata is fetched once |
| Per user | `users`, `sessions`, `user_sources` (tracked, shelved, pinned_at, topic, sort), `user_videos` (watched, progress, starred, hidden), `collections`, `collection_items`, `marks`, `view_prefs`, `toggl_accounts`, `toggl_rules` | private to each user |
| Per device | `devices`, `device_videos` (video_id, size, downloaded_at) | the server only sees **what** each device has. Paths stay on the device. |

Notes:
- Today `watched`, `progress`, `starred` and `hidden` are columns on
  `videos`. They move to `user_videos (user_id, video_id)`. "Hidden by sync
  because unavailable" stays on `videos` because it's a catalog fact. "Hidden
  by you" moves to `user_videos`.
- `pinned_at`, `shelved` and topics move to `user_sources`. The 5-pin cap
  applies per user.
- Downloads belong to the **device**, not the user. Two people on the same
  computer share its files.
- The Toggl API key is stored encrypted (AES-256-GCM). The key that unlocks
  it comes from a file named in the config, never stored in the database.
- IDs: YouTube ids stay as text keys. Users, devices and collections use
  `bigint generated always as identity`. Times are `timestamptz`.

## Toggl in layers

`toggl_rules (user_id, source_id, name, project, tags)`: one row per channel
or playlist that has settings. When a timer starts:

1. **Tags** = the channel's tags + the playlist's own tags (empty by
   default) + `Youtube` + `Youtube:<Channel>`. Duplicates are removed
   ignoring case, and each tag keeps the capitals it was typed with (as
   today).
2. **Entry name**, first one set wins: the playlist's own name → the
   channel's "Name in Toggl" → the playlist's title → the channel name.
   With no playlist name set, this matches today's behaviour exactly.
3. **Project**: the playlist's, otherwise the channel's.

UI: the channel page edits the channel layer and shows what its playlists
inherit. The playlist page shows the inherited tags greyed out, plus its own
tags and name. "Reset name" clears the layer it sits on.

## Go layout

```
cmd/yt/              cobra root: serve, agent, migrate, user, import, sync, get…
internal/config/     viper, reading YAML with YT_* environment overrides
internal/db/         pgx pool; queries in sqlc (typed Go from plain SQL)
internal/db/migrations/  goose SQL files, embedded with go:embed
internal/auth/       argon2id passwords, server-side sessions, CSRF check
internal/server/     net/http (Go 1.22 router), handlers, jobs, stream relay
internal/agent/      proxy, downloader, local media, local SQLite index
internal/ytdlp/      runs yt-dlp, parses its JSON (port of ytdlp.py)
internal/toggl/      Toggl v9 client + rule layering
web/                 the single-page UI (moved out of ui.py), go:embed
deploy/              docker-compose.yml, Dockerfile, example YAML configs
```

Libraries: cobra, viper, goose, pgx/v5, sqlc (codegen only),
`golang.org/x/crypto/argon2`, and `modernc.org/sqlite` for the agent (no
cgo).

## Configuration (YAML)

`server.yaml`:
```yaml
listen: ":8420"
public_url: "http://192.168.1.10:8420"
database:
  url: "postgres://yt:${YT_DB_PASSWORD}@db:5432/yt?sslmode=disable"
  migrate_on_start: true
storage:
  thumbs_dir: /data/thumbs
auth:
  session_ttl: 720h
  secret_key_file: /run/secrets/yt_key   # encrypts Toggl keys
  signup: invite        # invite | closed (no open sign-up)
ytdlp:
  path: yt-dlp
  extra_args: []
```

`agent.yaml` (one per computer, in `~/.config/yt/agent.yaml`):
```yaml
listen: "127.0.0.1:8421"
server_url: "http://192.168.1.10:8420"
device_name: "jair-desktop"
media_dir: "~/Videos/yt"
download:
  max_height: 1080
  sub_langs: "en,es"
ytdlp:
  path: yt-dlp
  extra_args: []
```

Viper reads the YAML. Any key can be overridden by an environment
variable (`YT_DATABASE_URL`, `YT_MEDIA_DIR`…), and secrets such as passwords
always come from the environment or files.

## Docker Compose

`deploy/docker-compose.yml`:
- `db`: postgres:17, `pgdata` volume, healthcheck.
- `server`: built from `deploy/Dockerfile` (a Go build stage, then a slim
  runtime stage with yt-dlp + ffmpeg). Mounts `server.yaml` and the `thumbs`
  volume, and starts once `db` is healthy. Runs `goose up` (embedded) on
  start, or `yt migrate up` by hand.
- Port 8420 is open on the home network only.

The agent is **not** in Compose. It must write to each computer's own disk
and run as that user, so it is a single binary installed with a systemd
user unit, like `ytlocal.service` today.

## Accounts and security

- `yt user add <name>` (CLI on the server) creates users. The first one is
  an admin. There is no open sign-up.
- Login: a form posts name + password, the server creates a `sessions` row
  and sends an HttpOnly, SameSite=Strict cookie. Sessions can be revoked.
- Every query is scoped by `user_id`. Tests check that one user can never
  read another user's rows.
- Pairing an agent: `yt agent pair` → the user logs in once → the server
  issues a **device token**, which the agent keeps in its config directory.
  The agent sends the device token for inventory updates and passes the
  user's own session cookie through for everything else.
- Home only for now: plain HTTP on the LAN. For the cloud: Caddy in front
  for HTTPS, `Secure` cookies, rate-limited login.

## Moving your existing data

`yt import sqlite ~/.local/share/ytlocal/catalog.db --user jair --device <id>`
- Copies sources, videos and memberships into the catalog tables, and
  watched/progress/stars/pins/marks/collections/topics/sorts/Toggl settings
  into your user rows.
- Your current Toggl token (from `config.json`) is stored encrypted on your
  account.
- Your downloaded files are recorded against this computer's device. The
  agent takes over `~/Videos/yt` as it is, so nothing is re-downloaded or
  moved.
- The thumbs folder is copied into the Compose volume.
- Safe to run again while testing: it upserts.

## Phases

Each phase ends with something runnable. The Python app keeps working
until the switch-over in phase 7.

1. **Skeleton.** Go module, cobra commands, viper YAML, Compose with
   Postgres, goose with the first migration, `/healthz`. *Done when:*
   `docker compose up` gives a healthy server.
2. **Schema + import.** All migrations, sqlc queries, `yt import sqlite`.
   *Done when:* your real data imports and its counts match SQLite.
   *Done 2026-11-15*, with these changes:
   - "Picks" are not playlists: they are a per-user list per channel
     (`user_picks`).
   - The Toggl playlist layer waits for phase 7; channel rules are imported.
   - sqlc moves to phase 4, where the app's queries are written. The
     import uses plain pgx.
   - A video's uploader is known for 5,800 of 6,450 videos; the rest were
     only seen in other people's playlists, and a later sync fills them in.
   - Not carried over: `left_source` (empty in the real data) and Toggl
     settings for names no channel matches (one: "Mikel Hyperpolyglot").
3. **Accounts.** Users, login page, sessions, the `yt user` commands.
4. **API parity.** Port every `/api/*` route from `server.py` with the same
   JSON shapes, per user: state, sources, browse, sync jobs, pins, shelve,
   forget, topics, sort, collections, marks, chapters, thumbs, stream
   relay. Python's API output is recorded first and used as golden tests,
   so the UI barely changes. This is the largest phase.
   *Step 4a done 2026-11-15:* reading the library and every per-person
   change (stars, watched, hidden, progress, marks, collections, pins,
   shelves, forget, sorts, topics, Toggl settings). Checked against the
   Python app on the same snapshot; what still differs is on purpose:
   - A channel lists videos in YouTube's upload order rather than by
     month-rounded dates.
   - Ties always break the same way.
   - Groups use real channel ids.
   Other changes:
   - sqlc is dropped. The main video list is built from optional filters,
     which sqlc handles badly, so SQL is written by hand with named columns.
   - The schema now uses the strict *Mere Mortals* prefixes
     (docs/db-conventions.md).
   - "On this computer" comes from an `X-Yt-Device` header, which the
     agent will set (phase 6).
   - Downloads imported from the old app count as part of your library.
   *Step 4b done 2026-11-15:* adding sources (Add box, Browse, job cards)
   and `yt sync`.
   - The catalog is shared: a channel two people track is listed once.
     Adding it puts it in your library only.
   - A source is skipped by `yt sync` only when everyone tracking it has
     shelved it.
   - Job cards are per person and live in memory, as before.
   - The server image carries a pinned, checksum-checked yt-dlp.
   - Still manual: nothing runs `yt sync` on a schedule yet.
5. **UI move.** Move the page out of `ui.py` into `web/`. Add login and
   logout, show the user's name, and mark which videos are on *this*
   computer vs. none.
6. **Agent.** Proxy, download queue (port of `jobs.py`), local media with
   byte ranges, buffering, inventory reports, pairing, systemd unit.
7. **Toggl layers + switch-over.** Per-user keys and settings screen,
   `toggl_rules` layering, playlist tag/name editing. Import your data
   for real, swap `ytlocal.service` for `yt-agent` plus Compose, and keep the
   Python code for a while before deleting it.
8. **Later: cloud.** Hosted server, Caddy/HTTPS, backups (`pg_dump` on a
   schedule), maybe a phone-friendly layout.

## Size, honestly

The Python app is about 6,700 lines: UI 2,300, CLI 1,200, DB 1,000,
server 800, yt-dlp 800. The UI is mostly reused. The rest is a rewrite
plus new parts (accounts, agent, layers). Expect roughly the effort of
building the current app again. Phases 4 and 6 are the big ones; phases
1–3 are quick.

## Open questions

- **CLI:** the CLI today has many commands (`add`, `list`, `get`, `watch`…).
  Proposed: keep the ones you use, talking to the server or agent, and drop
  the rest. Which do you use?
- **Shared or private tracking:** if two people track the same channel,
  they share the catalog but keep separate progress (proposed). Should one
  person's pins or collections ever be visible to the other?
- **Who can sync:** syncing a channel spends YouTube requests. Proposed: any
  user can, and jobs are queued on the server so two people don't fetch the
  same channel twice.
- **Agent on Windows/macOS:** Go builds for both easily, but each machine
  still needs yt-dlp + ffmpeg installed. Which OSes are in the house?
