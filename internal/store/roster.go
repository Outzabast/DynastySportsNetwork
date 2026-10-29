package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

var (
	rosterSpotKey  = []string{"branch_id", "season_index", "team_id", "person_id"}
	rosterSpotBio  = []string{"position", "jersey", "school_year", "redshirt", "height", "weight", "star_rating", "portrait", "is_captain"}
	rosterSpotCols = append(append(append([]string(nil), rosterSpotKey...), "first_snapshot", "last_snapshot"), rosterSpotBio...)
)

// rosterSpots is the roster_spot rows for a snapshot: every rostered player
// on his team, with his bio as of this save. Offseason saves give none: by
// then the season's departures are gone, next season's arrivals are on and
// class years have advanced, and none of that belongs to the season the save
// is still labeled with. Rows are unique by key.
func rosterSpots(snap *extract.Snapshot, branch, snapshot int, person func(row int) (uuid.UUID, bool)) [][]any {
	if snap.Season.Stage == "OffSeason" {
		return nil
	}
	type key struct {
		team int
		id   uuid.UUID
	}
	seen := map[key]bool{}
	var rows [][]any
	for _, p := range snap.Players {
		if !p.OnRoster || p.TeamID < 0 {
			continue
		}
		id, ok := person(p.Row)
		if !ok || seen[key{p.TeamID, id}] {
			continue
		}
		seen[key{p.TeamID, id}] = true
		rows = append(rows, []any{branch, snap.Season.SeasonIndex, p.TeamID, id, snapshot, snapshot,
			p.Position, p.Jersey, p.SchoolYear, p.Redshirt, p.Height, p.Weight, p.StarRating, p.PortraitAsset, p.IsCaptain})
	}
	return rows
}

// BackfillRosterSpots records the roster spots of an already-ingested
// snapshot from its archived save. Players are mapped to people through the
// identity aliases that were valid at that snapshot, so nothing is guessed:
// a row with no alias covering the snapshot (or claimed by two people) is
// skipped. Existing spots widen to include the snapshot, and take its bio
// only if it is the latest save seen for them. It returns the number of
// spots written and of rostered rows skipped.
func (s *Store) BackfillRosterSpots(ctx context.Context, snapshotID int, snap *extract.Snapshot) (written, skipped int, err error) {
	var branch int
	if err := s.DB.QueryRow(ctx, `SELECT branch_id FROM snapshot WHERE id = $1`, snapshotID).Scan(&branch); err != nil {
		return 0, 0, fmt.Errorf("snapshot %d: %w", snapshotID, err)
	}
	rows, err := s.DB.Query(ctx, `SELECT player_row, person_id FROM person_alias
		WHERE branch_id = $1 AND first_snapshot <= $2 AND last_snapshot >= $2`, branch, snapshotID)
	if err != nil {
		return 0, 0, err
	}
	byRow := map[int]uuid.UUID{}
	ambiguous := map[int]bool{}
	for rows.Next() {
		var row int
		var id uuid.UUID
		if err := rows.Scan(&row, &id); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if prev, dup := byRow[row]; dup && prev != id {
			ambiguous[row] = true
		}
		byRow[row] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	person := func(row int) (uuid.UUID, bool) {
		id, ok := byRow[row]
		if !ok || ambiguous[row] {
			skipped++
			return uuid.Nil, false
		}
		return id, true
	}
	spots := rosterSpots(snap, branch, snapshotID, person)
	if len(spots) == 0 {
		return 0, skipped, nil
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE tmp_spot (LIKE roster_spot) ON COMMIT DROP`); err != nil {
		return 0, 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"tmp_spot"}, rosterSpotCols, pgx.CopyFromRows(spots)); err != nil {
		return 0, 0, err
	}
	sets := []string{
		"first_snapshot = LEAST(roster_spot.first_snapshot, excluded.first_snapshot)",
		"last_snapshot = GREATEST(roster_spot.last_snapshot, excluded.last_snapshot)",
	}
	for _, c := range rosterSpotBio {
		sets = append(sets, fmt.Sprintf("%s = CASE WHEN excluded.last_snapshot >= roster_spot.last_snapshot THEN excluded.%s ELSE roster_spot.%s END", c, c, c))
	}
	cols := strings.Join(rosterSpotCols, ", ")
	if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO roster_spot (%s) SELECT %s FROM tmp_spot
		ON CONFLICT (%s) DO UPDATE SET %s`, cols, cols, strings.Join(rosterSpotKey, ", "), strings.Join(sets, ", "))); err != nil {
		return 0, 0, err
	}
	return len(spots), skipped, tx.Commit(ctx)
}
