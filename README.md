# Dynasty Sports Network

An ESPN-style website for EA Sports College Football 27 dynasties. A local
server watches your saves folder, reads every new autosave (read-only, from a
copy), reconciles it into a permanent history in Postgres, and serves
scoreboards, box scores, standings, rankings, team and player pages, stat
leaders, awards, the record book, and recruiting.

See [AGENTS.md](AGENTS.md) for the project rules and
[docs/V1-STATUS.md](docs/V1-STATUS.md) for what v1 covers and what's next.

## Quick start (Windows)

Prerequisites: Go 1.25+, Docker Desktop.

1. **Schema bundle.** Put a CFB 27 schema bundle (`C27_*.gz`) in `data/schemas/`.
   It is built from your own game install (see AGENTS.md). Never commit it.
2. **Database:**
   ```bash
   docker compose up -d
   ```
3. **Run the site and watcher:**
   ```bash
   go run ./cmd/dsn serve
   ```
   Open http://localhost:8080. On first start every dynasty save in the
   saves folder is ingested (oldest first). After that, each autosave the game
   writes is picked up within a few seconds and the open pages offer a refresh.

## Commands

| Command | Purpose |
|---|---|
| `dsn serve` | Website + save watcher (default). `-no-watch` to serve only. |
| `dsn ingest <save>...` | Copy, archive, and ingest specific save files. |
| `dsn reingest -into <db-url>` | Rebuild history from the raw archive into an empty database (after a parser upgrade). |
| `dsn extract [-o out.json] <save-copy>` | Print what a save contains (debug). |

Settings (flag, env var, or a `.env` file at the repo root; see `.env.example`): `-db`/`DSN_DB` (default `postgres://dsn:dsn@localhost:5433/dsn`),
`-saves`/`DSN_SAVES` (auto-detects `Documents\EA SPORTS College Football 27\Saves`),
`-data`/`DSN_DATA`, `-schema-dir`/`DSN_SCHEMAS`, `-media`/`DSN_MEDIA`, `-addr`/`DSN_ADDR`.

Dev tools: `go run ./cmd/tabledump <save>` lists raw tables; `go run ./cmd/schemadump -tables X,Y <save>` prints field types.

## Database: local Docker or hosted

By default the server uses the Docker Postgres from `docker-compose.yml`, so
the data lives on whichever machine runs it. To keep one history across all
your machines, point every machine at the same hosted Postgres (Neon,
Supabase, Prisma Postgres, Railway, Render, or any plain Postgres 14+):

1. Create a database and copy its **direct** connection string. Pooled
   URLs also work if you append `&default_query_exec_mode=exec`.
2. On each machine, copy `.env.example` to `.env` (gitignored) and set
   ```
   DSN_DB=postgres://USER:PASSWORD@HOST:5432/DBNAME?sslmode=require
   ```
3. Run `go run ./cmd/dsn serve` as usual. The startup log prints
   `database host=...` so you can confirm which one it's using. The schema
   is created automatically on first connect.

A real `DSN_DB` environment variable or `-db` flag overrides `.env`, so
`-db postgres://dsn:dsn@localhost:5433/dsn?sslmode=disable` gets you back to
local Docker for a one-off.

**Moving your existing local data up** (run once, from the machine that has it):
```bash
docker compose exec -T db pg_dump -U dsn -Fc dsn > dsn.dump
docker run --rm -i postgres:17 pg_restore --no-owner --no-privileges -d "<hosted connection string>" < dsn.dump
```

Notes:
- Two machines can run at the same time. Ingests of one dynasty are
  serialized by a database lock, and only the newest save moves the live
  timeline.
- The raw save archive (`data/archive`, used by `dsn reingest`) stays on each
  machine. To keep it in one place too, set `DSN_DATA` to a synced folder such
  as OneDrive.
- The database is about 0.5 GB with four dynasties, so check your provider's
  storage limit.

## Layout

```
cmd/dsn            server, watcher, ingest commands
internal/extract   save copy -> Snapshot (the single raw-to-canonical mapping)
internal/store     Postgres schema, reconciliation/identity, read queries
internal/collector save-folder watcher, copy + SHA-256 + gzip archive
internal/web       ESPN-style pages, JSON API, live-update events
media/             game art served at /media (never stored in the DB)
data/              gitignored: schemas, archive, inbox copies, fixtures
```

## Multiple dynasties

Each dynasty is keyed by the save's `Franchise.LeagueID`, so every save of the
same dynasty (autosaves, copies, backups) lands in one history, and different
dynasties never share rows. The header shows the dynasty you're viewing; click
it (or open `/dynasties`) to switch.

## Fonts

The site uses espn.com's own typefaces from `media/fonts` (personal use):
Benton Sans for body text, Benton Sans Cond for table headers and labels,
Playoff Pro Cond for scores, ESPN Ignite Display for page titles, and Publico
for feature headlines. They are registered in `internal/web/static/site.css`
and mapped to the `--font-*` variables there.
