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
func recordDraft(ctx context.Context, tx pgx.Tx, branch, snapID int, pos int64, snap *extract.Snapshot,
	person func(row int) (uuid.UUID, bool), flag func(kind string, detail any) error) (int, error) {
	cur := draftStateOf(snap)
	if _, err := tx.Exec(ctx, `UPDATE snapshot SET draft_state = $2 WHERE id = $1`, snapID, js(cur)); err != nil {
		return 0, err
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT draft_state FROM snapshot
		WHERE branch_id = $1 AND draft_state IS NOT NULL AND (pos, id) < ($2, $3)
		ORDER BY pos DESC, id DESC LIMIT 1`, branch, pos, snapID).Scan(&raw)
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
	var picks []extract.LeavingPlayer
	for _, l := range snap.LeavingPlayers {
		if isDraftRow(l) && prev.Rows[strconv.Itoa(l.Row)] != draftSig(l) {
			picks = append(picks, l)
		}
	}
	if len(picks) != drafted {
		return 0, flag("draft_mismatch", map[string]any{"changedRows": len(picks), "coachPicks": drafted})
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
func (s *Store) BackfillDraft(ctx context.Context, snapshotID int, snap *extract.Snapshot) (int, error) {
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
	n, err := recordDraft(ctx, tx, branch, snapshotID, pos, snap, person, flag)
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
