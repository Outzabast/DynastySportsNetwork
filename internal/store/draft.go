package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

// draftState is what a snapshot records so the next one can tell whether the
// NFL draft ran in between. The save keeps no per-player draft result, but
// the draft rewrites the LeavingPlayer list with one EarlyNFL_<round> row per
// pick, and every pick adds one to its head coach's career draft count. The
// list also holds stale rows (projections, earlier years), which stay as
// they were; so the picks are the EarlyNFL rows that changed, and there must
// be exactly as many as the head coaches' counts grew. Coaches are compared
// one by one (hires and firings change the head-coach total too). Verified on
// the 2035 draft: 224 changed rows, 224 more picks, matching for all 139
// teams and 32 per round.
type draftState struct {
	HC   map[string]int    `json:"hc"`   // head coach (row and name) -> career picks
	Rows map[string]string `json:"rows"` // LeavingPlayer row -> player row, type, round
}

func isDraftRow(l extract.LeavingPlayer) bool { return strings.HasPrefix(l.LeaveType, "EarlyNFL") }

func draftSig(l extract.LeavingPlayer) string {
	return fmt.Sprintf("%d:%s:%d", l.PlayerRow, l.LeaveType, l.Round)
}

func draftStateOf(snap *extract.Snapshot) draftState {
	st := draftState{HC: map[string]int{}, Rows: map[string]string{}}
	for _, c := range snap.Coaches {
		if c.Position == "HeadCoach" {
			st.HC[fmt.Sprintf("%d:%s %s", c.Row, c.FirstName, c.LastName)] = c.DraftPicks
		}
	}
	for _, l := range snap.LeavingPlayers {
		if isDraftRow(l) {
			st.Rows[strconv.Itoa(l.Row)] = draftSig(l)
		}
	}
	return st
}

// newPicks is how many players the head coaches in both states had drafted
// in between.
func newPicks(prev, cur draftState) int {
	n := 0
	for k, v := range cur.HC {
		if p, ok := prev.HC[k]; ok && v > p {
			n += v - p
		}
	}
	return n
}

// maxCoachSurplus is how many more picks the head coaches' counts may grow
// by than the draft rows show. The 2036 draft (2037 offseason) credited one
// coach a pick no player on his team accounts for, with every round full.
const maxCoachSurplus = 2

// draftPicks finds the draft's picks: the EarlyNFL rows that changed since
// prev. A pick whose projection came true in the same list row doesn't
// change, so when the changed rows fall short of the coaches' count, a
// round with fewer picks than the others is filled from that round's
// unchanged rows of teams whose head coach gained more picks than the
// team's changed rows, but only when exactly the missing number of such
// rows exists. The result must give every round 1-7 the same number of
// picks, no team more picks than its coach gained, and at most
// maxCoachSurplus fewer picks than the coaches gained. ok is false when it
// can't; detail says what was reconciled (nil when nothing needed it).
func draftPicks(prev draftState, snap *extract.Snapshot, drafted int) (picks []extract.LeavingPlayer, ok bool, detail map[string]any) {
	var unchanged []extract.LeavingPlayer
	for _, l := range snap.LeavingPlayers {
		if !isDraftRow(l) {
			continue
		}
		if prev.Rows[strconv.Itoa(l.Row)] != draftSig(l) {
			picks = append(picks, l)
		} else {
			unchanged = append(unchanged, l)
		}
	}
	if len(picks) == drafted {
		return picks, true, nil
	}
	detail = map[string]any{"changedRows": len(picks), "coachPicks": drafted}

	growth := map[int]int{} // team -> head coach's new picks
	for _, c := range snap.Coaches {
		k := fmt.Sprintf("%d:%s %s", c.Row, c.FirstName, c.LastName)
		if p, had := prev.HC[k]; had && c.Position == "HeadCoach" && c.DraftPicks > p {
			growth[c.TeamID] += c.DraftPicks - p
		}
	}
	teamPicks, roundPicks := map[int]int{}, map[int]int{}
	for _, l := range picks {
		teamPicks[l.TeamID]++
		roundPicks[draftRound(l)]++
	}
	full := 0
	for r := 1; r <= 7; r++ {
		full = max(full, roundPicks[r])
	}
	var added []string
	for r := 1; r <= 7; r++ {
		need := full - roundPicks[r]
		if need == 0 {
			continue
		}
		var cands []extract.LeavingPlayer
		for _, l := range unchanged {
			if draftRound(l) == r && growth[l.TeamID] > teamPicks[l.TeamID] {
				cands = append(cands, l)
			}
		}
		if len(cands) != need {
			detail["round"], detail["candidates"] = r, len(cands)
			return nil, false, detail
		}
		for _, l := range cands {
			picks = append(picks, l)
			teamPicks[l.TeamID]++
			roundPicks[r]++
			added = append(added, fmt.Sprintf("player row %d, team %d, round %d", l.PlayerRow, l.TeamID, r))
		}
	}
	for team, n := range teamPicks {
		if n > growth[team] {
			detail["overTeam"] = team
			return nil, false, detail
		}
	}
	if full == 0 || drafted-len(picks) < 0 || drafted-len(picks) > maxCoachSurplus {
		return nil, false, detail
	}
	detail["added"], detail["picks"] = added, len(picks)
	return picks, true, detail
}

// draftRound is the round of an EarlyNFL row: its ProjectRound, or the
// round in the type name.
func draftRound(l extract.LeavingPlayer) int {
	if l.Round >= 1 && l.Round <= 7 {
		return l.Round
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(l.LeaveType, "EarlyNFL_"))
	return n
}

// recordDraft stores the snapshot's draft state and, when the draft ran
// since the branch's previous snapshot, its picks. Players map to people
// through person; a pick whose player has no person is skipped and flagged.
// A changed-row count that disagrees with the coaches' count is flagged and
// nothing is written: guessing would put draft results on the wrong people.
//
// prevID, when not 0, names the snapshot to compare with instead of the
// branch's previous one (for redoing a draft whose post-draft saves were
// pruned from the archive).
func recordDraft(ctx context.Context, tx pgx.Tx, branch, snapID int, pos int64, snap *extract.Snapshot,
	person func(row int) (uuid.UUID, bool), flag func(kind string, detail any) error, prevID int) (int, error) {
	cur := draftStateOf(snap)
	if _, err := tx.Exec(ctx, `UPDATE snapshot SET draft_state = $2 WHERE id = $1`, snapID, js(cur)); err != nil {
		return 0, err
	}
	var raw []byte
	var err error
	if prevID != 0 {
		err = tx.QueryRow(ctx, `SELECT draft_state FROM snapshot WHERE id = $1 AND branch_id = $2
			AND draft_state IS NOT NULL AND (pos, id) < ($3, $4)`, prevID, branch, pos, snapID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("snapshot %d: no earlier draft state on branch %d to compare with", prevID, branch)
		}
	} else {
		err = tx.QueryRow(ctx, `SELECT draft_state FROM snapshot
			WHERE branch_id = $1 AND draft_state IS NOT NULL AND (pos, id) < ($2, $3)
			ORDER BY pos DESC, id DESC LIMIT 1`, branch, pos, snapID).Scan(&raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var prev draftState
	if err := json.Unmarshal(raw, &prev); err != nil {
		return 0, err
	}
	drafted := newPicks(prev, cur)
	if drafted == 0 {
		return 0, nil
	}
	picks, ok, detail := draftPicks(prev, snap, drafted)
	if !ok {
		return 0, flag("draft_mismatch", detail)
	}
	if detail != nil {
		if err := flag("draft_reconciled", detail); err != nil {
			return 0, err
		}
	}

	// The draft follows the season it ends; a save already in the next
	// season (a skipped offseason save) still drafts from the last one.
	season := snap.Season.SeasonIndex
	if snap.Season.Stage != "OffSeason" {
		season--
	}
	posByRow := map[int]string{}
	for _, p := range snap.Players {
		posByRow[p.Row] = p.Position
	}
	var rows [][]any
	var unmapped []string
	for _, l := range picks {
		id, ok := person(l.PlayerRow)
		if !ok {
			unmapped = append(unmapped, fmt.Sprintf("player row %d", l.PlayerRow))
			continue
		}
		rows = append(rows, []any{branch, id, season, l.TeamID, draftRound(l), posByRow[l.PlayerRow], snapID})
	}
	if err := upsert(ctx, tx, "draft_pick", []string{"branch_id", "person_id", "season_index", "team_id", "round", "position", "snapshot_id"},
		[]string{"branch_id", "person_id"}, rows); err != nil {
		return 0, err
	}
	if len(unmapped) > 0 {
		if err := flag("draft_unmapped", map[string]any{"picks": unmapped}); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// personsAt maps player rows to the people whose identity aliases cover a
// snapshot. Rows claimed by two people map to no one.
func personsAt(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, branch, snapshotID int) (func(row int) (uuid.UUID, bool), error) {
	rows, err := q.Query(ctx, `SELECT player_row, person_id FROM person_alias
		WHERE branch_id = $1 AND first_snapshot <= $2 AND last_snapshot >= $2`, branch, snapshotID)
	if err != nil {
		return nil, err
	}
	byRow := map[int]uuid.UUID{}
	ambiguous := map[int]bool{}
	for rows.Next() {
		var row int
		var id uuid.UUID
		if err := rows.Scan(&row, &id); err != nil {
			rows.Close()
			return nil, err
		}
		if prev, dup := byRow[row]; dup && prev != id {
			ambiguous[row] = true
		}
		byRow[row] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(row int) (uuid.UUID, bool) {
		id, ok := byRow[row]
		return id, ok && !ambiguous[row]
	}, nil
}

// BackfillDraft records an already-ingested snapshot's draft state, and its
// draft picks if the draft ran since the branch's previous recorded state.
// Snapshots must be fed in timeline order. It returns the picks written.
// prevID, when not 0, is the snapshot to compare with (see recordDraft).
func (s *Store) BackfillDraft(ctx context.Context, snapshotID int, snap *extract.Snapshot, prevID int) (int, error) {
	var branch, dyn int
	var pos int64
	if err := s.DB.QueryRow(ctx, `SELECT branch_id, dynasty_id, pos FROM snapshot WHERE id = $1`, snapshotID).Scan(&branch, &dyn, &pos); err != nil {
		return 0, fmt.Errorf("snapshot %d: %w", snapshotID, err)
	}
	person, err := personsAt(ctx, s.DB, branch, snapshotID)
	if err != nil {
		return 0, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	flag := func(kind string, detail any) error {
		b, _ := json.Marshal(detail)
		_, err := tx.Exec(ctx, `INSERT INTO review_flag (dynasty_id, branch_id, snapshot_id, kind, detail) VALUES ($1,$2,$3,$4,$5)`,
			dyn, branch, snapshotID, kind, b)
		return err
	}
	n, err := recordDraft(ctx, tx, branch, snapshotID, pos, snap, person, flag, prevID)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// BackfillAwardSeasons dates the stored awards a snapshot's save can date:
// rows still at season -1 that match one of its awards take that award's
// season, or are dropped when a dated copy is already stored. It returns
// the number of rows dated.
func (s *Store) BackfillAwardSeasons(ctx context.Context, snapshotID int, snap *extract.Snapshot) (int, error) {
	var branch int
	if err := s.DB.QueryRow(ctx, `SELECT branch_id FROM snapshot WHERE id = $1`, snapshotID).Scan(&branch); err != nil {
		return 0, fmt.Errorf("snapshot %d: %w", snapshotID, err)
	}
	var rows [][]any
	for _, a := range snap.PlayerAwards {
		if a.SeasonIndex >= 0 {
			rows = append(rows, []any{a.SeasonIndex, a.AwardType, a.Period, a.PeriodIndex, a.FirstName, a.LastName, a.TeamID})
		}
	}
	if len(rows) == 0 {
		return 0, nil
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE tmp_award_season (season_index int, award_type text, period text, period_index int,
		first_name text, last_name text, team_id int) ON COMMIT DROP`); err != nil {
		return 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tmp_award_season"}, []string{"season_index", "award_type", "period", "period_index",
		"first_name", "last_name", "team_id"}, pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	const match = `a.branch_id = $1 AND a.season_index = -1 AND a.award_type = t.award_type AND a.period = t.period
		AND a.period_index = t.period_index AND a.first_name = t.first_name AND a.last_name = t.last_name AND a.team_id = t.team_id`
	if _, err := tx.Exec(ctx, `DELETE FROM award a USING tmp_award_season t WHERE `+match+`
		AND EXISTS (SELECT 1 FROM award d WHERE d.branch_id = a.branch_id AND d.season_index = t.season_index AND d.award_type = a.award_type
			AND d.period = a.period AND d.period_index = a.period_index AND d.first_name = a.first_name AND d.last_name = a.last_name AND d.team_id = a.team_id)`, branch); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE award a SET season_index = t.season_index FROM tmp_award_season t WHERE `+match, branch)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}

// DateSeasonAwards gives undated season-long awards their season, which
// they carry as their period index. An undated row whose dated copy exists
// is dropped.
func (s *Store) DateSeasonAwards(ctx context.Context) (int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM award a WHERE a.season_index = -1 AND a.period = 'Season' AND EXISTS (
		SELECT 1 FROM award d WHERE d.branch_id = a.branch_id AND d.season_index = a.period_index AND d.award_type = a.award_type
			AND d.period = a.period AND d.period_index = a.period_index AND d.first_name = a.first_name AND d.last_name = a.last_name AND d.team_id = a.team_id)`); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE award SET season_index = period_index WHERE season_index = -1 AND period = 'Season' AND period_index >= 0`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), tx.Commit(ctx)
}
