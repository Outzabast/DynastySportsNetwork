# AGENTS.md — Dynasty ESPN

Guidance for coding agents working in this repo. Read this before making changes.

## What we're building

An ESPN-style website for a single **EA Sports College Football 27** dynasty. It should feel like browsing espn.com: scoreboard, standings, rankings, team pages, player pages, box scores, leaderboards, schedules, awards, record book, recruiting, draft. The difference is that every team, player, and game comes from the user's dynasty save.

The site keeps the **complete history** of the dynasty. The game does not keep full history (players are deleted when they leave, season data resets), so this project's backend is the permanent record.

## Hard rules

1. **Real-world data only on the site.** Show only what a real fan could see on ESPN. Never expose video-game internals: overall rating, attribute ratings, development traits, archetypes, abilities, skill groups, XP, NIL dollar values, recruiting pitches/dealbreakers, coach levels, prestige scores, job security, etc. These may be stored in the raw archive but must not enter the site's read models or API.
2. **Never write to the game's save.** Read-only. Always copy the save first and parse the copy. Never open the live save file for writing or hold it locked.
3. **Player identity is sacred.** Never attribute one player's stats, awards, or records to another. When identity is ambiguous, keep players separate and flag for review. Splitting is cheap to fix later; a bad merge is not. See [Player identity](#player-identity).
4. **Never delete history on revert.** Reverted data is marked superseded on an abandoned branch, not deleted.
5. **Don't redistribute EA assets.** Schema bundles (`C27_*.gz`) and `dynasty-tuning-binary.FTC` are derived from the game install. Never commit them. Never commit real dynasty saves either. Keep them in gitignored `data/`.
6. **Use the parser, don't rewrite it.** Extraction is the parser's job. If it's missing something or wrong, fix it upstream (PR to `leaguelines/cfb-dynasty`) rather than forking or reimplementing, unless upstream won't take the change.

## Source data

### Parser
- **Repo:** https://github.com/leaguelines/cfb-dynasty (Go, MIT). Library at `github.com/leaguelines/cfb-dynasty/dynasty`, CLI at `cmd/cfb-dynasty`.
- README describes it as feature-complete for read-only export of CFB 27 PC saves: season/teams/rosters, schedule and per-game stats (team box scores + attributed player lines), season stats, career stats, recruiting, coaches, injuries, depth charts, awards, conference champions, full record book, rivalries, position changes, draft picks, bowls.
- Pin a specific version in `go.mod`. Don't track `main` blindly.
- Package docs on pkg.go.dev may lag the repo; trust the repo source/README.

### Schema bundle (required, local only)
- Decoding requires a gzip JSON schema bundle such as `C27_468_2.gz`, built from the user's own game install:
  1. Extract `cfb27-db-data/<patch>/` with MMC Frosty Modding Tools → Legacy Explorer.
  2. `cfb-dynasty schema-build -o ./data/schemas ./data/cfb27-db-data`
  3. Optional: put `dynasty-tuning-binary.FTC` under `data/schemas/cfb27-db-data/<patch>/`.
- Rebuild after EA title updates that change table layouts.
- The save header version (e.g. `809.1`) uses different numbering from the bundle (`468.2`). This is expected.

### Saves
- PC only. Location: `%USERPROFILE%\Documents\EA SPORTS CFB27\saves\`. Files are typically extensionless.
- Example autosave name: `DYNASTYJUL0605h10m56AUTOSAVE`.
- The game **autosaves at every week advance**.
- Container: `FBCHUNKS` magic, zlib payload, FranTk-style tables (same engine family as Madden; `madden-franchise` JS lib also parses it).

### Facts about the save format (verified from parser source/docs)
- Cross-table references are 32 bits: 15-bit table ID + 17-bit row number.
- **Teams** have a stable ID: `Team.TeamIndex` (Air Force = 0, Akron = 1, Alabama = 2, …). Never use Team table row order. `TeamIndex` 255 = FCS placeholder, skip.
- **Players have no known stable ID.** Every `playerId` the parser exports (rosters, depth charts, awards, injuries, leaving players, position changes, season and game stats) is the **row index in the `Player` table**, resolved via `playerRecordByReference` / `playerRecordFromField` in `dynasty/export_player_resolve.go`. Rows are reused after players leave. **Never use `playerId` as a person key.**
- Stat rows do not reference players. `Player` rows point to their stats via `GameStats[]` / `SeasonStats[]` arrays; the parser inverts them.
- Per-game stats are bucketed into games by direct `SeasonGame` row index; stale references to other seasons are dropped.
- `Franchise.LeagueID` is a stable per-dynasty code. Use it as the dynasty key.
- Season context: year, week, week type, phase, plus `SeasonPeriodsExport` flags (recruiting, signing, transfer portal, draft, carousel, award periods, etc.).
- Team export includes W–L, conference W–L, Coaches/Media/CFP poll ranks, points, first-place votes, last-week ranks, conference standing.
- Record book: scopes league / conference / team × periods career / season / game. League boards are top-N; conference and team boards hold one holder each.

## Architecture

```
Save dir ─► Collector ─► Backend API ─► Postgres ─► Site
            (local)       reconcile      history      live updates
```

### Collector (local, Windows, background)
- Watches the saves directory. Waits until a file stops changing, copies it, hashes it (SHA-256), skips duplicates.
- Parses the copy with `cfb-dynasty`, normalizes, uploads to the backend. Never blocks or slows the game.
- Written in Go, importing the parser as a library.
- Fully automatic. No user action per update.

### Storage, three layers
1. **Raw archive:** every distinct save, compressed, content-addressed by hash, plus the parser version and schema version used. Lets us re-ingest all history when rules or the parser improve.
2. **Canonical history:** normalized, append-oriented tables, roughly: dynasty, branch, snapshot, season, team, team_season, person, person_alias, player_season, game, team_game_stats, player_game_stats, player_season_stats, award, poll_week, recruiting_class, transfer, coach, coach_stint, record_book.
3. **Read models:** views/tables shaped for site pages (scoreboard, standings, rankings, team page, player page, box score, leaders). Only real-world fields.

### Reconciliation
Each snapshot gets a timeline position `(season, stage/phase, week)` and a lineage fingerprint (hash of game results to date).
- **Forward progress:** insert new games, stat lines, awards; update in-progress totals.
- **Revert detected:** position earlier than our latest, or snapshot contradicts stored results (different score, previously played game now unplayed). Find the divergence point, mark later data on the old branch superseded, start a new branch. The site reads only the active branch.
- Abandoned branches are kept. Restoring that save again should reactivate them.

## Offseason and rollover

Don't hardcode an offseason calendar. Community sources disagree and EA hasn't published the full one. Record every phase/week transition the collector observes and learn the real sequence from data.

Rough order from community guides (unverified): end of season recap → players leaving (transfer / draft intent, persuasion) → NFL draft → transfer portal window(s) → recruiting/signing → class rollover / new season.

Risk points to handle:
- Players leaving: intent is not departure. Record as an event.
- Draft and graduation: player rows freed and later reused.
- Transfer portal: same person, new team.
- Signing: recruits become rostered players (the recruit record links to its `Player` row).
- New season: class years advance, season stats/standings reset.

## Player identity

There is no stable player ID in the save. We mint our own.

- **The person ID is a random UUID minted once.** It is never derived from or hashed from attributes (name, position, etc.). Attribute-derived IDs collide for same-name players and change when attributes change.
- **Game row indexes are aliases with validity ranges** (`person_alias`: person UUID, Player row, first snapshot, last snapshot, branch).
- **Continuity carries identity.** Saves arrive every week. Same row in consecutive snapshots with consistent attributes means same person.
- **Attributes validate continuity:**

| Field | Rule |
|---|---|
| First/last name, hometown + state, handedness | Must match. Mismatch = row reused, new person. |
| Recruiting class year, star rating, origin season | Fixed history, must match. |
| Class year (FR/SO/JR/SR, RS) | May advance by exactly one at rollover. |
| Position | May change; ideally confirmed by a `PositionChange` row. |
| Team | May change at the portal; ideally agrees with `PrevTeamIndex`. |
| Height, weight, jersey | May drift; never decide identity alone. |
| Career stat totals | Monotonic non-decreasing. A decrease = different person. |

- **New row with no prior owner:** mint a UUID, record origin facts (name, hometown/state, recruiting class, star rating, first team, first season seen).
- **Row reused** (fixed facts contradict the previous owner): close out the old person (graduated / drafted / departed) and mint a new UUID.
- **Ambiguous or gap in snapshots:** do not auto-merge. Create a provisional person and flag for review.
- **Invariants checked every snapshot:** no person on two rosters; every stat line's owner is on that team that week; our summed history agrees with the save's career totals and record-book holders.
- **Branch scope:** people first seen on an abandoned branch retire with that branch. Regenerated recruits after a revert are new people even if names repeat.
- Stat rows store their source snapshot and source row so identity corrections can re-attribute them.

## Open questions (verify against real CFB 27 saves before relying on assumptions)

- Does the `Player` table have any ID-like field at all? Dump the schema's Player field list to check.
- Are Player rows reused at rollover, or do new players take unused rows?
- How many past seasons of game/season stat rows does the save retain?
- Are full player stat lines kept for simulated (non-user) games?
- Are `PrevTeamIndex` and `PositionChange` reliable for validating transfers and position moves?
- Do career totals carry across a transfer?
- Exact offseason phase order and where each kind of data resets.

**First task:** build a rollover fixture. Advance a throwaway dynasty through a full offseason, keep every weekly autosave, export each with the parser, and diff consecutive snapshots. Use it to answer the questions above and as the regression test suite for reconciliation and identity.

## Conventions

- Prefer verified facts from the parser source or real saves over community guides. Community guides and other games' material (CFB 25/26, Madden) are leads, not facts.
- Write tests against real save fixtures stored locally in `data/` (gitignored). Tests skip when fixtures are absent.
- Keep the raw-to-canonical mapping in one place so re-ingestion is a single code path.