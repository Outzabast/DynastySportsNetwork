package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

// IngestResult reports what reconciliation decided for one snapshot.
type IngestResult struct {
	SnapshotID int
	BranchID   int
	Action     string // duplicate | first | forward | fork | reactivate
	Notes      []string
	Persons    struct{ Kept, Minted, Relinked, Closed int }
	Duration   time.Duration
}

// Ingest reconciles one snapshot into the dynasty's history inside a single
// transaction. archivePath is where the raw save copy was archived.
func (s *Store) Ingest(ctx context.Context, snap *extract.Snapshot, archivePath string, modTime time.Time) (*IngestResult, error) {
	start := time.Now()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	res := &IngestResult{}
	in := &ingest{tx: tx, snap: snap, res: res}
	if err := in.run(ctx, archivePath, modTime); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	return res, nil
}

type ingest struct {
	tx      pgx.Tx
	snap    *extract.Snapshot
	res     *IngestResult
	dynasty int
	branch  int
	snapID  int
	pos     int64
	persons map[int]uuid.UUID // player row -> person
	// live is true when this save is newer (wall clock) than anything seen for
	// the dynasty. Only live saves may move the active timeline: an older copy
	// discovered late (a backup, a "- Copy") is archived on its own branch.
	live bool
}

func (in *ingest) flag(ctx context.Context, kind string, detail any) error {
	b, _ := json.Marshal(detail)
	_, err := in.tx.Exec(ctx, `INSERT INTO review_flag (dynasty_id, branch_id, snapshot_id, kind, detail) VALUES ($1,$2,NULLIF($3,0),$4,$5)`,
		in.dynasty, in.branch, in.snapID, kind, b)
	return err
}

func (in *ingest) run(ctx context.Context, archivePath string, modTime time.Time) error {
	snap := in.snap
	tx := in.tx
	if err := tx.QueryRow(ctx, `INSERT INTO dynasty (league_id, base_year) VALUES ($1,$2)
		ON CONFLICT (league_id) DO UPDATE SET base_year = excluded.base_year RETURNING id`,
		snap.LeagueID, snap.Season.BaseCalendarYear).Scan(&in.dynasty); err != nil {
		return fmt.Errorf("dynasty: %w", err)
	}
	// Serialize ingests per dynasty.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(in.dynasty)); err != nil {
		return err
	}
	// Name the dynasty after the user's program so several dynasties can be
	// told apart (e.g. "Texas State").
	userTeam, coach, name := -1, "", ""
	for _, c := range snap.Coaches {
		if c.IsUser && c.TeamID >= 0 {
			userTeam, coach = c.TeamID, strings.TrimSpace(c.FirstName+" "+c.LastName)
			break
		}
	}
	for _, t := range snap.Teams {
		if t.ID == userTeam {
			name = t.DisplayName
		}
	}
	if name == "" {
		name = fmt.Sprintf("Dynasty %d", snap.LeagueID)
	}
	var prev *time.Time
	if err := tx.QueryRow(ctx, `SELECT last_save_at FROM dynasty WHERE id = $1`, in.dynasty).Scan(&prev); err != nil {
		return err
	}
	in.live = prev == nil || modTime.After(*prev)
	if in.live {
		if _, err := tx.Exec(ctx, `UPDATE dynasty SET name = $2, user_team_id = $3, coach_name = $4, save_name = $5, last_save_at = $6 WHERE id = $1`,
			in.dynasty, name, userTeam, coach, snap.SaveName, modTime); err != nil {
			return err
		}
	}

	pos, knownStage := Position(snap.Season)
	in.pos = pos
	fp := Fingerprint(snap)

	// Already seen this exact save content?
	var seenBranch int
	var seenStatus string
	err := tx.QueryRow(ctx, `SELECT s.branch_id, b.status FROM snapshot s JOIN branch b ON b.id = s.branch_id
		WHERE s.dynasty_id = $1 AND s.sha256 = $2 ORDER BY s.id DESC LIMIT 1`, in.dynasty, snap.SHA256).Scan(&seenBranch, &seenStatus)
	if err == nil {
		if seenStatus == "active" || !in.live {
			in.res.Action, in.res.BranchID = "duplicate", seenBranch
			return nil
		}
		// A save from an abandoned branch was restored: reactivate that branch.
		if err := in.activate(ctx, seenBranch); err != nil {
			return err
		}
		in.res.Action, in.res.BranchID = "reactivate", seenBranch
		in.res.Notes = append(in.res.Notes, "restored a save from an abandoned branch; reactivated it")
		in.branch = seenBranch
		return in.flag(ctx, "branch_reactivated", map[string]any{"branch": seenBranch, "sha256": snap.SHA256})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}

	// Active branch (create on first ingest).
	err = tx.QueryRow(ctx, `SELECT id FROM branch WHERE dynasty_id = $1 AND status = 'active' ORDER BY id DESC LIMIT 1`, in.dynasty).Scan(&in.branch)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `INSERT INTO branch (dynasty_id) VALUES ($1) RETURNING id`, in.dynasty).Scan(&in.branch); err != nil {
			return err
		}
		in.res.Action = "first"
	} else if err != nil {
		return err
	} else {
		if err := in.reconcileBranch(ctx, pos); err != nil {
			return err
		}
	}
	in.res.BranchID = in.branch

	periods, _ := json.Marshal(snap.Season.Periods)
	if err := tx.QueryRow(ctx, `INSERT INTO snapshot (dynasty_id, branch_id, sha256, save_name, size, mod_time, archive_path,
		parser_version, schema_version, season_index, year, stage, week, week_type, offseason_stage, pos, fingerprint, periods)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`,
		in.dynasty, in.branch, snap.SHA256, snap.SaveName, snap.Size, modTime, archivePath, snap.ParserVersion, snap.SchemaVersion,
		snap.Season.SeasonIndex, snap.Season.Year, snap.Season.Stage, snap.Season.Week, snap.Season.WeekType, snap.Season.OffseasonStage,
		pos, fp, periods).Scan(&in.snapID); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	in.res.SnapshotID = in.snapID
	if !knownStage {
		if err := in.flag(ctx, "unknown_stage", map[string]any{"stage": snap.Season.Stage, "week": snap.Season.Week, "weekType": snap.Season.WeekType}); err != nil {
			return err
		}
	}

	if err := in.resolveIdentities(ctx); err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	return in.write(ctx)
}

// reconcileBranch decides whether the snapshot continues the active branch,
// returns to an abandoned branch, or forks a new one.
func (in *ingest) reconcileBranch(ctx context.Context, pos int64) error {
	tx := in.tx
	var latest int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(pos), -1) FROM snapshot WHERE branch_id = $1`, in.branch).Scan(&latest); err != nil {
		return err
	}
	stored, err := in.storedGames(ctx, in.branch)
	if err != nil {
		return err
	}
	bad, why := contradicts(in.snap, stored)
	if pos >= latest && !bad {
		in.res.Action = "forward"
		return nil
	}
	if !bad {
		why = fmt.Sprintf("timeline moved backwards (pos %d < latest %d)", pos, latest)
	}

	// Revert. Prefer an abandoned branch this snapshot is consistent with and
	// not behind (the user went back to a line they had played before).
	rows, err := tx.Query(ctx, `SELECT b.id, COALESCE(MAX(s.pos), -1) FROM branch b JOIN snapshot s ON s.branch_id = b.id
		WHERE b.dynasty_id = $1 AND b.status = 'abandoned' GROUP BY b.id ORDER BY b.id DESC`, in.dynasty)
	if err != nil {
		return err
	}
	type cand struct {
		id     int
		latest int64
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.latest); err != nil {
			rows.Close()
			return err
		}
		cands = append(cands, c)
	}
	rows.Close()
	for _, c := range cands {
		if c.latest > pos {
			continue
		}
		sg, err := in.storedGames(ctx, c.id)
		if err != nil {
			return err
		}
		if len(sg) == 0 {
			continue
		}
		if ok, _ := contradicts(in.snap, sg); !ok {
			old := in.branch
			in.branch = c.id
			if !in.live {
				in.res.Action = "archived"
				in.res.Notes = append(in.res.Notes, fmt.Sprintf("older save (%s); recorded on side branch %d, active timeline unchanged", why, c.id))
				return nil
			}
			if err := in.activate(ctx, c.id); err != nil {
				return err
			}
			in.res.Action = "reactivate"
			in.res.Notes = append(in.res.Notes, fmt.Sprintf("revert detected (%s); continued abandoned branch %d", why, c.id))
			return in.flag(ctx, "branch_reactivated", map[string]any{"from": old, "to": c.id, "reason": why})
		}
	}

	// Fork: new branch carries forward everything before the divergence.
	old := in.branch
	var nb int
	status := "active"
	if !in.live {
		status = "abandoned"
	}
	if err := tx.QueryRow(ctx, `INSERT INTO branch (dynasty_id, parent_id, fork_pos, status) VALUES ($1,$2,$3,$4) RETURNING id`, in.dynasty, old, pos, status).Scan(&nb); err != nil {
		return err
	}
	if err := in.copyForward(ctx, old, nb, stored); err != nil {
		return err
	}
	if !in.live {
		in.branch = nb
		in.res.Action = "archived"
		in.res.Notes = append(in.res.Notes, fmt.Sprintf("older save (%s); recorded on side branch %d, active timeline unchanged", why, nb))
		return in.flag(ctx, "older_save_archived", map[string]any{"branch": nb, "parent": old, "reason": why, "save": in.snap.SaveName})
	}
	if _, err := tx.Exec(ctx, `UPDATE branch SET status = 'abandoned', abandoned_at = now() WHERE id = $1`, old); err != nil {
		return err
	}
	in.branch = nb
	in.res.Action = "fork"
	in.res.Notes = append(in.res.Notes, fmt.Sprintf("revert detected (%s); forked branch %d from %d", why, nb, old))
	return in.flag(ctx, "branch_forked", map[string]any{"from": old, "to": nb, "reason": why})
}

func (in *ingest) activate(ctx context.Context, id int) error {
	if _, err := in.tx.Exec(ctx, `UPDATE branch SET status = 'abandoned', abandoned_at = now() WHERE dynasty_id = $1 AND status = 'active' AND id <> $2`, in.dynasty, id); err != nil {
		return err
	}
	_, err := in.tx.Exec(ctx, `UPDATE branch SET status = 'active', abandoned_at = NULL WHERE id = $1`, id)
	return err
}

func (in *ingest) storedGames(ctx context.Context, branch int) (map[int]storedGame, error) {
	rows, err := in.tx.Query(ctx, `SELECT game_row, home_team, away_team, home_score, away_score, played FROM game
		WHERE branch_id = $1 AND season_index = $2`, branch, in.snap.Season.SeasonIndex)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]storedGame{}
	for rows.Next() {
		var row int
		var g storedGame
		if err := rows.Scan(&row, &g.Home, &g.Away, &g.HomeScore, &g.AwayScore, &g.Played); err != nil {
			return nil, err
		}
		out[row] = g
	}
	return out, rows.Err()
}

// copyForward seeds a forked branch: all prior seasons, plus current-season
// games (and their lines/scoring) whose results the snapshot agrees with.
func (in *ingest) copyForward(ctx context.Context, from, to int, stored map[int]storedGame) error {
	season := in.snap.Season.SeasonIndex
	agree := []int32{}
	byRow := map[int]extract.Game{}
	for _, g := range in.snap.Games {
		byRow[g.Row] = g
	}
	for row, s := range stored {
		g, ok := byRow[row]
		if s.Played && ok && g.Played && g.HomeScore == s.HomeScore && g.AwayScore == s.AwayScore && g.HomeTeamID == s.Home && g.AwayTeamID == s.Away {
			agree = append(agree, int32(row))
		}
	}
	prior := "season_index < $2"
	for _, t := range []string{"team_season", "poll_week", "player_season", "player_season_stat", "coach_season", "recruit", "player_event"} {
		if err := copyBranchRows(ctx, in.tx, t, from, to, prior, season); err != nil {
			return err
		}
	}
	gameScoped := "season_index < $2 OR (season_index = $2 AND game_row = ANY($3))"
	for _, t := range []string{"game", "player_game_stat", "scoring_play"} {
		if err := copyBranchRows(ctx, in.tx, t, from, to, gameScoped, season, agree); err != nil {
			return err
		}
	}
	for _, t := range []string{"award", "league_award", "conference_champion", "record_book", "rivalry"} {
		if err := copyBranchRows(ctx, in.tx, t, from, to, ""); err != nil {
			return err
		}
	}
	// Identity carries over: the same rows map to the same people up to the fork.
	return copyBranchRows(ctx, in.tx, "person_alias", from, to, "open")
}

type facts struct {
	first, last, town, state, hand, asset string
	birth                                 int64
}

func factsOf(p extract.Player) facts {
	return facts{p.FirstName, p.LastName, p.HomeTown, p.HomeState, p.Handedness, p.AssetName, p.BirthDate}
}

// sameOwner applies the fixed-fact rules: name, hometown, handedness must
// match; asset name and birth date must match when both are known.
func sameOwner(a, b facts) bool {
	if a.first != b.first || a.last != b.last || a.town != b.town || a.state != b.state || a.hand != b.hand {
		return false
	}
	if a.asset != "" && b.asset != "" && a.asset != b.asset {
		return false
	}
	if a.birth != 0 && b.birth != 0 && a.birth != b.birth {
		return false
	}
	return true
}

// resolveIdentities maps every Player row in the snapshot to a person.
func (in *ingest) resolveIdentities(ctx context.Context) error {
	tx := in.tx
	in.persons = map[int]uuid.UUID{}

	type aliasRow struct {
		id     int
		person uuid.UUID
		f      facts
	}
	open := map[int]aliasRow{}
	rows, err := tx.Query(ctx, `SELECT a.id, a.player_row, p.id, p.first_name, p.last_name, p.home_town, p.home_state, p.handedness, p.asset_name, p.birth_date
		FROM person_alias a JOIN person p ON p.id = a.person_id WHERE a.branch_id = $1 AND a.open`, in.branch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a aliasRow
		var row int
		if err := rows.Scan(&a.id, &row, &a.person, &a.f.first, &a.f.last, &a.f.town, &a.f.state, &a.f.hand, &a.f.asset, &a.f.birth); err != nil {
			rows.Close()
			return err
		}
		open[row] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Unaliased people on this branch who could be re-linked by strong facts
	// (e.g. a player who moved to a different row). Keyed by asset name.
	aliased := map[uuid.UUID]bool{}
	for _, a := range open {
		aliased[a.person] = true
	}
	type cand struct {
		id uuid.UUID
		f  facts
	}
	byAsset := map[string][]cand{}
	rows, err = tx.Query(ctx, `SELECT p.id, p.first_name, p.last_name, p.home_town, p.home_state, p.handedness, p.asset_name, p.birth_date
		FROM person p WHERE p.dynasty_id = $1 AND p.asset_name <> '' AND EXISTS (
			SELECT 1 FROM person_alias a WHERE a.person_id = p.id AND a.branch_id = $2)`, in.dynasty, in.branch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.f.first, &c.f.last, &c.f.town, &c.f.state, &c.f.hand, &c.f.asset, &c.f.birth); err != nil {
			rows.Close()
			return err
		}
		if !aliased[c.id] {
			byAsset[c.f.asset] = append(byAsset[c.f.asset], c)
		}
	}
	rows.Close()

	// Career totals must never go down for the same person.
	careerGP := map[uuid.UUID]int64{}
	rows, err = tx.Query(ctx, `SELECT person_id, MAX(COALESCE((stats->>'GAMESPLAYED')::bigint, 0)) FROM player_career_stat WHERE branch_id = $1 GROUP BY person_id`, in.branch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		var gp int64
		if err := rows.Scan(&id, &gp); err != nil {
			rows.Close()
			return err
		}
		careerGP[id] = gp
	}
	rows.Close()
	snapGP := map[int]int64{}
	for _, c := range in.snap.PlayerCareerStats {
		if v := c.Stats["GAMESPLAYED"]; v > snapGP[c.PlayerRow] {
			snapGP[c.PlayerRow] = v
		}
	}

	seen := map[int]bool{}
	var keepIDs []int32
	var newAliases [][]any
	var newPersons [][]any
	closeAlias := func(id int, reason string) error {
		in.res.Persons.Closed++
		_, err := tx.Exec(ctx, `UPDATE person_alias SET open = false, close_reason = $2 WHERE id = $1`, id, reason)
		return err
	}
	for _, p := range in.snap.Players {
		seen[p.Row] = true
		f := factsOf(p)
		if a, ok := open[p.Row]; ok {
			if sameOwner(a.f, f) {
				// Same row with matching name, hometown, handedness, asset name (when
				// present) and birth date is effectively unique, so a career
				// decrease then points at the save re-pointing career rows (seen
				// across rollover), not a new person. Without a birth date we split.
				strong := f.birth != 0 && a.f.birth == f.birth
				if prev, ok := careerGP[a.person]; ok && snapGP[p.Row] < prev && strong {
					if err := in.flag(ctx, "career_decreased_kept", map[string]any{"row": p.Row, "person": a.person, "had": prev, "now": snapGP[p.Row]}); err != nil {
						return err
					}
				}
				if prev, ok := careerGP[a.person]; ok && snapGP[p.Row] < prev && !strong {
					if err := closeAlias(a.id, "career_decreased"); err != nil {
						return err
					}
					if err := in.flag(ctx, "career_decreased", map[string]any{"row": p.Row, "person": a.person, "had": prev, "now": snapGP[p.Row]}); err != nil {
						return err
					}
				} else {
					in.persons[p.Row] = a.person
					keepIDs = append(keepIDs, int32(a.id))
					in.res.Persons.Kept++
					continue
				}
			} else if err := closeAlias(a.id, "row_reused"); err != nil {
				return err
			}
		}
		var person uuid.UUID
		provisional := false
		cs := []cand{}
		if f.asset != "" {
			for _, c := range byAsset[f.asset] {
				if sameOwner(c.f, f) && c.f.birth == f.birth {
					cs = append(cs, c)
				}
			}
		}
		switch len(cs) {
		case 1:
			person = cs[0].id
			byAsset[f.asset] = nil
			in.res.Persons.Relinked++
		default:
			person = uuid.New()
			if len(cs) > 1 {
				provisional = true
				if err := in.flag(ctx, "ambiguous_identity", map[string]any{"row": p.Row, "name": p.FirstName + " " + p.LastName, "candidates": len(cs)}); err != nil {
					return err
				}
			}
			newPersons = append(newPersons, []any{person, in.dynasty, p.FirstName, p.LastName, p.HomeTown, p.HomeState, p.Handedness, p.AssetName,
				p.BirthDate, in.branch, in.snapID, in.snap.Season.SeasonIndex, p.TeamID, provisional})
			in.res.Persons.Minted++
		}
		in.persons[p.Row] = person
		newAliases = append(newAliases, []any{person, in.branch, p.Row, in.snapID, in.snapID, true})
	}
	// Rows no longer present: the player left the save (or the row was freed).
	for row, a := range open {
		if !seen[row] {
			if err := closeAlias(a.id, "not_seen"); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE person_alias SET last_snapshot = $1 WHERE id = ANY($2)`, in.snapID, keepIDs); err != nil {
		return err
	}
	if len(newPersons) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"person"}, []string{"id", "dynasty_id", "first_name", "last_name", "home_town", "home_state",
			"handedness", "asset_name", "birth_date", "first_branch", "first_snapshot", "first_season", "first_team", "provisional"}, pgx.CopyFromRows(newPersons)); err != nil {
			return err
		}
	}
	if len(newAliases) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"person_alias"}, []string{"person_id", "branch_id", "player_row", "first_snapshot", "last_snapshot", "open"}, pgx.CopyFromRows(newAliases)); err != nil {
			return err
		}
	}
	return nil
}

func js(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func i32s(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

// write stores everything in the snapshot on the current branch.
func (in *ingest) write(ctx context.Context) error {
	snap, tx, b, sid := in.snap, in.tx, in.branch, in.snapID
	season := snap.Season.SeasonIndex
	unmapped := 0
	person := func(row int) (uuid.UUID, bool) {
		p, ok := in.persons[row]
		if !ok {
			unmapped++
		}
		return p, ok
	}

	// Teams (dynasty-level identity) and team seasons.
	var teams, tseason, polls [][]any
	tstats := map[int]extract.Stats{}
	for _, t := range snap.TeamSeasonStats {
		tstats[t.TeamID] = t.Stats
	}
	for _, t := range snap.Teams {
		teams = append(teams, []any{in.dynasty, t.ID, t.ShortName, t.LongName, t.DisplayName, t.NickName, t.Abbrev, t.AssetName, t.PrimaryColor, t.SecondaryColor})
		tseason = append(tseason, []any{b, season, t.ID, t.Conference, t.Wins, t.Losses, t.ConfWins, t.ConfLosses, t.ConfStanding, t.Streak,
			t.APRank, t.CoachesRank, t.CFPRank, t.OffensiveRank, t.DefensiveRank, t.RecruitingRank, t.PlayoffStatus, t.PlayoffRound, t.HeadCoachRow, js(tstats[t.ID]), sid})
		for _, p := range []struct {
			name                   string
			rank, pts, first, prev int
		}{{"ap", t.APRank, t.APPoints, t.APFirstPlace, t.APLastRank}, {"coaches", t.CoachesRank, t.CoachesPoints, t.CoachesFirst, t.CoachesLastRank}, {"cfp", t.CFPRank, 0, 0, t.CFPLastRank}} {
			if p.rank > 0 && (p.rank <= 25 || (p.name != "cfp" && p.pts > 0)) {
				polls = append(polls, []any{b, season, snap.Season.Week, snap.Season.WeekType, p.name, t.ID, p.rank, p.pts, p.first, p.prev, t.Wins, t.Losses, sid})
			}
		}
	}
	if err := upsert(ctx, tx, "team", []string{"dynasty_id", "team_id", "short_name", "long_name", "display_name", "nick_name", "abbrev", "asset_name", "primary_color", "secondary_color"},
		[]string{"dynasty_id", "team_id"}, teams); err != nil {
		return err
	}
	if err := upsert(ctx, tx, "team_season", []string{"branch_id", "season_index", "team_id", "conference", "wins", "losses", "conf_wins", "conf_losses", "conf_standing", "streak",
		"ap_rank", "coaches_rank", "cfp_rank", "off_rank", "def_rank", "recruiting_rank", "playoff_status", "playoff_round", "head_coach_row", "stats", "snapshot_id"},
		[]string{"branch_id", "season_index", "team_id"}, tseason); err != nil {
		return err
	}
	if err := upsert(ctx, tx, "poll_week", []string{"branch_id", "season_index", "week", "week_type", "poll", "team_id", "rank", "points", "first_place", "prev_rank", "wins", "losses", "snapshot_id"},
		[]string{"branch_id", "season_index", "week_type", "week", "poll", "team_id"}, polls); err != nil {
		return err
	}

	// Player bios for the current season.
	var pseason [][]any
	for _, p := range snap.Players {
		id, ok := person(p.Row)
		if !ok {
			continue
		}
		pseason = append(pseason, []any{b, season, id, p.TeamID, p.OnRoster, p.Position, p.SchoolYear, p.Redshirt, p.Jersey, p.Height, p.Weight,
			p.StarRating, p.PortraitAsset, p.IsCaptain, p.InjuryStatus, sid})
	}
	if err := upsert(ctx, tx, "player_season", []string{"branch_id", "season_index", "person_id", "team_id", "on_roster", "position", "school_year", "redshirt",
		"jersey", "height", "weight", "star_rating", "portrait", "is_captain", "injury_status", "snapshot_id"},
		[]string{"branch_id", "season_index", "person_id"}, pseason); err != nil {
		return err
	}

	// Games, box scores and scoring summaries.
	var games, plays [][]any
	for _, g := range snap.Games {
		var hb, ab []byte
		if g.HomeBox != nil {
			hb, ab = js(g.HomeBox), js(g.AwayBox)
		}
		games = append(games, []any{b, g.SeasonIndex, g.Row, g.Week, g.WeekType, g.Status, g.Played, g.HomeTeamID, g.AwayTeamID, g.HomeScore, g.AwayScore,
			i32s(g.HomeQuarters), i32s(g.AwayQuarters), g.HomeOT, g.AwayOT, g.Overtime, g.Simmed, g.Month, g.Day, g.DayOfWeek, g.TimeOfDay, g.Network,
			g.Attendance, g.Temperature, g.Weather, g.Wind, g.WindSpeed, g.Stadium, g.BowlName, g.GameOfWeek, hb, ab, sid, sid})
		for _, p := range g.ScoringPlays {
			parts := []map[string]any{}
			for _, sc := range p.Participants {
				m := map[string]any{"stats": sc.Stats}
				if id, ok := in.persons[sc.PlayerRow]; ok {
					m["personId"] = id
				}
				parts = append(parts, m)
			}
			plays = append(plays, []any{b, g.SeasonIndex, g.Row, p.Seq, p.Quarter, p.ClockSec, p.HomeScore, p.AwayScore, p.HomePrev, p.AwayPrev,
				p.Conversion, p.ScoringTeam, js(parts), sid})
		}
	}
	if err := upsert(ctx, tx, "game", []string{"branch_id", "season_index", "game_row", "week", "week_type", "status", "played", "home_team", "away_team",
		"home_score", "away_score", "home_quarters", "away_quarters", "home_ot", "away_ot", "overtime", "simmed", "month", "day", "day_of_week", "time_of_day",
		"network", "attendance", "temperature", "weather", "wind", "wind_speed", "stadium", "bowl_name", "game_of_week", "home_box", "away_box",
		"first_snapshot", "last_snapshot"}, []string{"branch_id", "season_index", "game_row"}, games, "first_snapshot"); err != nil {
		return err
	}
	if err := upsert(ctx, tx, "scoring_play", []string{"branch_id", "season_index", "game_row", "seq", "quarter", "clock_sec", "home_score", "away_score",
		"home_prev", "away_prev", "conversion", "scoring_team", "participants", "snapshot_id"}, []string{"branch_id", "season_index", "game_row", "seq"}, plays); err != nil {
		return err
	}

	// Stat lines: every column the game records, per category.
	var pgs [][]any
	for _, l := range snap.PlayerGameStats {
		id, ok := person(l.PlayerRow)
		if !ok {
			continue
		}
		pgs = append(pgs, []any{b, season, l.GameRow, id, l.TeamID, l.Category, js(l.Stats), sid, l.SourceRow})
	}
	if err := upsert(ctx, tx, "player_game_stat", []string{"branch_id", "season_index", "game_row", "person_id", "team_id", "category", "stats", "snapshot_id", "source_row"},
		[]string{"branch_id", "season_index", "game_row", "person_id", "category"}, pgs); err != nil {
		return err
	}
	var pss [][]any
	for _, l := range snap.PlayerSeasonStats {
		id, ok := person(l.PlayerRow)
		if !ok {
			continue
		}
		pss = append(pss, []any{b, l.SeasonIndex, id, l.TeamID, l.Category, js(l.Stats), sid, l.SourceRow})
	}
	if err := upsert(ctx, tx, "player_season_stat", []string{"branch_id", "season_index", "person_id", "team_id", "category", "stats", "snapshot_id", "source_row"},
		[]string{"branch_id", "season_index", "person_id", "team_id", "category"}, pss); err != nil {
		return err
	}
	var pcs [][]any
	for _, l := range snap.PlayerCareerStats {
		id, ok := person(l.PlayerRow)
		if !ok {
			continue
		}
		pcs = append(pcs, []any{b, id, l.Category, js(l.Stats), sid})
	}
	if err := upsert(ctx, tx, "player_career_stat", []string{"branch_id", "person_id", "category", "stats", "snapshot_id"},
		[]string{"branch_id", "person_id", "category"}, pcs); err != nil {
		return err
	}

	// Coaches.
	var coaches [][]any
	for _, c := range snap.Coaches {
		coaches = append(coaches, []any{b, season, c.Row, c.FirstName, c.LastName, c.Position, c.TeamID, c.Age, c.AssetName, c.Wins, c.Losses,
			c.SchoolWins, c.SchoolLosses, c.BowlWins, c.BowlLosses, c.ConfTitles, c.NatlTitles, c.PlayoffWins, c.PlayoffLoss, c.IsUser, sid})
	}
	if err := upsert(ctx, tx, "coach_season", []string{"branch_id", "season_index", "coach_row", "first_name", "last_name", "position", "team_id", "age", "asset_name",
		"wins", "losses", "school_wins", "school_losses", "bowl_wins", "bowl_losses", "conf_titles", "natl_titles", "playoff_wins", "playoff_losses", "is_user", "snapshot_id"},
		[]string{"branch_id", "season_index", "coach_row"}, coaches); err != nil {
		return err
	}

	// History the game keeps.
	var awards [][]any
	for _, a := range snap.PlayerAwards {
		var pid any
		if id, ok := in.persons[a.PlayerRow]; ok {
			pid = id
		}
		awards = append(awards, []any{b, pid, a.FirstName, a.LastName, a.TeamID, a.Position, a.AwardType, a.Period, a.PeriodIndex, sid})
	}
	if err := upsert(ctx, tx, "award", []string{"branch_id", "person_id", "first_name", "last_name", "team_id", "position", "award_type", "period", "period_index", "snapshot_id"},
		[]string{"branch_id", "award_type", "period", "period_index", "first_name", "last_name", "team_id"}, awards, "person_id"); err != nil {
		return err
	}
	var la [][]any
	for _, a := range snap.LeagueAwards {
		la = append(la, []any{b, a.AwardType, a.FirstName, a.LastName, a.Position, a.TeamName, a.Row, sid})
	}
	if err := upsert(ctx, tx, "league_award", []string{"branch_id", "award_type", "first_name", "last_name", "position", "team_name", "seq", "snapshot_id"},
		[]string{"branch_id", "award_type", "seq"}, la); err != nil {
		return err
	}
	var cc [][]any
	for _, c := range snap.ConferenceChampions {
		cc = append(cc, []any{b, c.Row, c.Conference, c.Winner, c.Loser, c.WinnerScore, c.LoserScore, c.WinnerRank, c.LoserRank, c.CoachName, sid})
	}
	if err := upsert(ctx, tx, "conference_champion", []string{"branch_id", "seq", "conference", "winner", "loser", "winner_score", "loser_score", "winner_rank", "loser_rank", "coach_name", "snapshot_id"},
		[]string{"branch_id", "seq"}, cc); err != nil {
		return err
	}
	var rb [][]any
	for _, r := range snap.RecordBook {
		rb = append(rb, []any{b, r.Scope, r.ScopeName, r.Period, r.StatType, r.Rank, r.Value, r.FirstName, r.LastName, r.Position, r.TeamName, r.Year, sid})
	}
	if err := upsert(ctx, tx, "record_book", []string{"branch_id", "scope", "scope_name", "period", "stat_type", "rank", "value", "first_name", "last_name", "position", "team_name", "year", "snapshot_id"},
		[]string{"branch_id", "scope", "scope_name", "period", "stat_type", "rank"}, rb); err != nil {
		return err
	}
	var rv [][]any
	for _, r := range snap.Rivalries {
		rv = append(rv, []any{b, r.Row, r.Name, r.Team1, r.Team2, r.Team1Wins, r.Team2Wins, r.StreakLen, r.StreakTeam, sid})
	}
	if err := upsert(ctx, tx, "rivalry", []string{"branch_id", "row_id", "name", "team1", "team2", "team1_wins", "team2_wins", "streak_len", "streak_team", "snapshot_id"},
		[]string{"branch_id", "row_id"}, rv); err != nil {
		return err
	}

	// Current state: injuries and depth charts.
	var inj [][]any
	seenInj := map[uuid.UUID]bool{}
	for _, i := range snap.Injuries {
		id, ok := person(i.PlayerRow)
		if !ok || seenInj[id] {
			continue
		}
		seenInj[id] = true
		inj = append(inj, []any{b, id, i.TeamID, i.Type, i.Severity, i.MinWeeks, i.MaxWeeks, sid})
	}
	if err := replace(ctx, tx, "injury", b, []string{"branch_id", "person_id", "team_id", "type", "severity", "min_weeks", "max_weeks", "snapshot_id"}, inj); err != nil {
		return err
	}
	var dc [][]any
	seenDC := map[string]bool{}
	for _, d := range snap.DepthCharts {
		id, ok := in.persons[d.PlayerRow]
		k := fmt.Sprint(d.TeamID, d.Position, d.Order)
		if !ok || seenDC[k] {
			continue
		}
		seenDC[k] = true
		dc = append(dc, []any{b, d.TeamID, d.Position, d.Order, id, sid})
	}
	if err := replace(ctx, tx, "depth_chart", b, []string{"branch_id", "team_id", "position", "slot", "person_id", "snapshot_id"}, dc); err != nil {
		return err
	}

	// Recruiting class for this season.
	var rc [][]any
	for _, r := range snap.Recruits {
		id, ok := person(r.PlayerRow)
		if !ok {
			continue
		}
		rc = append(rc, []any{b, season, id, r.Position, r.Class, r.Stars, r.NationalRank, r.PositionRank, r.StateRank, r.Stage, r.CommittedTeam, sid})
	}
	if err := upsert(ctx, tx, "recruit", []string{"branch_id", "season_index", "person_id", "position", "class", "stars", "national_rank", "position_rank",
		"state_rank", "stage", "committed_team", "snapshot_id"}, []string{"branch_id", "season_index", "person_id"}, rc); err != nil {
		return err
	}

	// Events.
	var ev [][]any
	for _, l := range snap.LeavingPlayers {
		id, ok := in.persons[l.PlayerRow]
		if !ok {
			continue
		}
		ev = append(ev, []any{b, id, season, "leaving", js(map[string]any{"type": l.LeaveType, "team": l.TeamID, "projectedRound": l.Round}), sid})
	}
	for _, p := range snap.PositionChanges {
		id, ok := in.persons[p.PlayerRow]
		if !ok {
			continue
		}
		ev = append(ev, []any{b, id, p.SeasonIndex, "position_change", js(map[string]any{"from": p.OldPosition, "to": p.NewPosition, "week": p.Week,
			"stage": p.Stage, "oldTeam": p.OldTeam, "newTeam": p.NewTeam}), sid})
	}
	if err := upsert(ctx, tx, "player_event", []string{"branch_id", "person_id", "season_index", "kind", "detail", "snapshot_id"},
		[]string{"branch_id", "person_id", "season_index", "kind", "detail"}, ev, "snapshot_id"); err != nil {
		return err
	}

	if unmapped > 0 {
		in.res.Notes = append(in.res.Notes, fmt.Sprintf("%d stat/bio lines referenced players with no identity (skipped)", unmapped))
		return in.flag(ctx, "unmapped_rows", map[string]any{"count": unmapped})
	}
	return nil
}
