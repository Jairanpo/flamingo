# Database conventions

These are the rules for every migration in `internal/db/migrations/`. They
follow the tamashii schema (`env-setup/schemas/maindb.sql` and its goose
migrations), which applies *Database Design for Mere Mortals* (Hernandez) and
*SQL Antipatterns, Vol. 1* (Karwin). Where this page cites a book, it gives
the idea in its own words.

## Naming

| Thing | Rule | Example |
|---|---|---|
| Table | plural, snake_case, one subject per table | `videos`, `playlists` |
| Linking table | the two subjects it joins, singular + plural | `playlist_videos`, `user_playlists` |
| Column | `<prefix>_<what>` on **every** column that isn't a foreign key. The prefix is 3–4 letters taken from the table's name, unique in the database, and never changes (*Mere Mortals*, ch. 7: "use the first three or four letters of the table name", applied consistently) | `vid_title`, `play_title`, `plse_rank` |
| Validation table | one `<prefix>_code` column holding the value | `sort_orders.sort_code = 'NEWEST'` |
| Primary key | `<prefix>_id`, never a bare `id` | `vid_id`, `user_id` |
| Foreign key | the **same name** as the key it points to | `playlist_videos.vid_id → videos.vid_id` |
| Timestamps | `<prefix>_created_at`, `<prefix>_updated_at`, `<prefix>_deleted_at` (soft delete), all `TIMESTAMPTZ` | `vid_created_at` |
| Validation value | UPPER_SNAKE, checked with a regex | `'COURSE'`, `'LIVE'` |
| Constraint | named after the rule it enforces | `vid_duration_not_negative` |
| Index | `idx_<table>_<columns>` (or `CREATE INDEX ON` when the name doesn't matter) | `idx_playlist_videos_position` |

### Prefixes

| Table | Prefix | | Table | Prefix |
|---|---|---|---|---|
| `users` | `user_` | | `user_channels` | `usch_` |
| `user_roles` | `usro_` | | `user_playlists` | `uspl_` |
| `sessions` | `sess_` | | `user_picks` | `uspi_` |
| `channels` | `chan_` | | `user_pick_videos` | `upvi_` |
| `playlists` | `play_` | | `user_videos` | `usvi_` |
| `playlist_sections` | `plse_` | | `collections` | `coll_` |
| `playlist_videos` | `plvi_` | | `collection_videos` | `covi_` |
| `videos` | `vid_` | | `marks` | `mark_` |
| `video_chapters` | `vich_` | | `topics` | `topi_` |
| `sort_orders` | `sort_` | | `channel_topics` | `chto_` |
| `tags` | `tag_` | | `toggl_channel_rules` | `tcru_` |
| `devices` | `devi_` | | `toggl_channel_rule_tags` | `tcrt_` |
| `device_videos` | `dvvi_` | | | |

A new table picks its prefix the same way and adds it here first.

Column names are unique across the whole database, except for foreign keys,
which repeat their key's name on purpose. A column name alone then tells you
which table it belongs to, and `JOIN … USING (vid_id)` reads naturally
(*Mere Mortals*: unique field names; *Antipatterns*: "ID Required").

## Every table has a type

Like tamashii, each table gets a `COMMENT ON TABLE` whose first line is its
type in *Mere Mortals* terms, followed by a sentence or two on what it holds:

- **Data**: the subjects themselves (`users`, `videos`, `channels`).
- **Linking**: resolves a many-to-many relationship; usually a composite
  primary key made of the two foreign keys (`playlist_videos`).
- **Subset**: extra fields that apply to only some rows of a data table, with
  a 1:1 key (`easypost_address_details` in tamashii).
- **Validation**: the allowed values for a field (`playlist_sections`).

## Antipatterns we avoid

- **Jaywalking** (lists in one column): today's `toggl_map.tags` is
  "Learning,AI" in a single string. It becomes a `tags` table plus a linking
  table. The same goes for chapters, which are JSON text today.
- **31 Flavors** (allowed values frozen in a CHECK or ENUM): sets of values
  that may grow, such as playlist sections or video visibility, live in
  validation tables and are referenced by foreign key. Adding a value is an
  `INSERT`, not a schema change.
- **Polymorphic Associations** (one column pointing to "a channel *or* a
  playlist"): today's `view_prefs.scope = 'source:<id>'` hides a foreign key
  in a string. Each target gets its own real foreign key instead.
- **Keyless Entry**: every relationship is a declared `REFERENCES`, with an
  explicit `ON DELETE` choice.
- **Fear of the Unknown**: `NULL` means "not known / not applicable". No
  placeholders such as `'N/A'`, `0` or `''` standing in for missing values
  (tamashii had to clean up `'N/A'` defaults in a later migration).
- **Magic numbers**: today's `hidden` column uses 0/1/2 codes. Values that mean
  different things become separate, named columns or validation rows.
- **Entity-Attribute-Value**: settings are real columns with real types, not
  key/value rows.
- **ID Required**: natural keys are used when they are stable and short.
  YouTube's own video, channel and playlist ids are the keys of those tables,
  and generated identity keys are used only where nothing natural exists
  (users, collections).
- **Readable Passwords**: passwords are stored only as argon2id hashes, and
  Toggl API keys are encrypted.
- **Implicit Columns**: queries name their columns; no `SELECT *` in
  application code (sqlc enforces this).
- **Index Shotgun**: an index exists because a query needs it, and the
  migration says which one.

## Migrations

- goose SQL files named `YYYYMMDDHHMMSS_<what_it_does>.sql`, as in tamashii
  (e.g. `20261116000001_create_videos.sql`).
- Each has `-- +goose Up` and `-- +goose Down`, with bodies wrapped in
  `StatementBegin` / `StatementEnd`, and the Down must really undo the Up.
- A migration that has run anywhere is never edited; changes go in a new
  migration.
- Seed rows for validation tables live in the migration that creates the
  table.
