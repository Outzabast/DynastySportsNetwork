# V1 status and the road to "complete"

Verified on 2026-10-14 against the real saves in this machine's Saves folder
(4 dynasties: Texas State 2034, Texas Tech 2031, Purdue 2030, Rice 2027).

## What v1 does

**Snapshot + reconcile (requirement 1)**
- Watches the saves folder (polls every 3s). Waits until a file has stopped
  changing, copies it read-only, verifies it didn't change mid-copy, hashes it
  (SHA-256), archives a gzip by hash in `data/archive/`, and skips duplicates.
- Extracts everything the parser exposes, plus stat columns the parser's
  high-level export drops, read through its public table API:
  - **Every raw stat column**, stored as JSONB keyed by the game's column
    name, for player game / season / career lines in all categories: offense,
    defense, kicking + punting, O-line, kick/punt returns. Nothing is subset.
  - Full team box scores (third/fourth downs, red zone, penalties,
    possession, 2-pt, etc.) and season team totals.
  - **Scoring summaries** (only kept by the game during the game's week).
    Scorers are reconstructed from the team-leader snapshots the save attaches
    to each score, e.g. "Alonzo Kahn Pass From Oscar Driver (Kick)".
  - **This week's player lines**, which live in an unpublished per-game cache
    until the next advance (also week-of-game only).
- Check: player lines sum to the team box score for 1,674/1,674 FBS
  team-games in the Texas State save. In the Texas Tech save one team-game is
  10 rushing yards short because the player was deleted from the save.
- Reconciliation: timeline position + results fingerprint per snapshot.
  Forward saves extend the active branch. A revert (earlier week, or a stored
  result that changed or became unplayed) forks a new branch that carries
  forward everything before the divergence. The old branch is kept (never
  deleted), and restoring a save from it reactivates it. An *older* file found
  late (a "- Copy" or backup) is archived on a side branch and does **not**
  move the live timeline.
- Player identity: random UUID per person. Player rows are aliases with
  validity ranges. Name, hometown, handedness, asset id and birth date are
  checked on every snapshot. A reused row becomes a new person; ambiguous
  matches become provisional people and are flagged. Anything uncertain goes
  to `review_flag` (visible on `/admin`).
- Fresh load backfills what the save still holds: past seasons' player
  stats (back to season 1 for current players), award history, conference
  champions, record book, rivalries.

**Site (requirement 2)**: home, scoreboard (by week, conference and Top 25 filters,
game leaders), gamecast/box score, standings, rankings (AP / Coaches / CFP with
movement), teams, team home/schedule/roster + depth chart/stats, player pages
(season + career, game log, *every recorded stat*, awards, transactions),
leaders and full sortable stat tables, awards, record book, recruiting,
history, search, data status, dynasty switcher, JSON API, live update toast.

**Media (requirement 3)**: logos, helmets, headshots, coach photos, trophies,
bowl and conference logos are served from `media/`. The DB stores only text
and numbers.

**Change detection (requirement 4)**: automatic, no user action. Open pages get
a "new save detected" prompt over server-sent events.

## Known gaps (to reach "complete")

Ordered by impact.

1. **Rollover fixture (AGENTS.md "first task").** Play a throwaway dynasty
   through a full offseason, keep every weekly autosave, and turn the series into
   a regression suite. Still open:
   - The offseason phase order. Positions currently assume PreSeason <
     NFLSeason < OffSeason, and unknown stages are flagged.
   - How player rows are reused at rollover, signing, and the draft.
   - Career-row behavior. Purdue showed 15 players whose career games
     "decreased" across a mid-season to offseason jump with identical facts;
     these are kept as the same person and flagged `career_decreased_kept`.
2. **Historical games before the first capture.** The save only holds the
   current season's games, so earlier seasons have player and team season
   totals but no game-by-game results or box scores. Scoring summaries exist
   only for games captured during their week. Both fill in going forward.
3. **Scoring play detail.** The save stores no play length or kicker on the
   scoring entry. TD scorers come from leader snapshots and FG kickers from
   the team's kicking line. Defensive and special-teams TDs show as
   "Touchdown". A play-by-play source would fix this.
4. **Polls history** builds up week by week from now on; past weeks of past
   seasons aren't in the save.
5. **Upstream the extractor gaps** to `leaguelines/cfb-dynasty` (AGENTS.md
   rule 6):
   - per-player kicking/punting and O-line game rows
   - unpublished current-week player lines
   - scoring-summary snapshot arrays (packed `record[]` refs)
   - full TeamStats columns
   - `TeamIndex = 0` rows leaking into Air Force's roster export
   - kicking/O-line rows bucketed into game 0
6. **Architecture split.** The collector writes straight to Postgres in-process.
   AGENTS.md describes a collector that uploads to a backend API. Split it when
   the site is hosted somewhere other than the gaming PC.
7. **Records "as of game".** Scoreboard cards show teams' current records
   rather than their records on game day. Store per-week team records to fix.
8. **Admin tools.** Review flags are read-only. Add merge/split/resolve
   actions and a way to choose the active branch by hand.
9. **More ESPN surfaces:** conference pages, bowl/CFP bracket page, national
   champions list, coaching carousel/transfer portal tracker, draft results,
   team history (year-by-year records), head-to-head, player comparison.
10. **Fonts and polish.** Licensed ESPN fonts are not bundled. Responsive
    passes, dark mode, and per-page images are still to do.

## Verification

- `go test ./...`: collector (watch, settle, dedupe, archive, never writes
  the live save), timeline ordering, contradiction detection, identity rules,
  and fixture tests against real saves in `data/saves` (box-score
  reconciliation, kicking attribution). Fixture tests skip when no save is
  present.
- Manual browser pass of every route against the 4 local dynasties.
