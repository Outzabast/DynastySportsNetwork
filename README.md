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

Settings (flag or env): `-db`/`DSN_DB` (default `postgres://dsn:dsn@localhost:5433/dsn`),
`-saves`/`DSN_SAVES` (auto-detects `Documents\EA SPORTS College Football 27\Saves`),
`-data`/`DSN_DATA`, `-schema-dir`/`DSN_SCHEMAS`, `-media`/`DSN_MEDIA`, `-addr`/`DSN_ADDR`.

Dev tools: `go run ./cmd/tabledump <save>` lists raw tables; `go run ./cmd/schemadump -tables X,Y <save>` prints field types.

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

ESPN's typefaces are licensed and are not bundled. The stylesheet uses the same
Helvetica Neue/Arial fallback stack espn.com uses. To use licensed fonts, add them
under `internal/web/static/fonts` and set `--font-head` / `--font-body` in
`site.css`.
