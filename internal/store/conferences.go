package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

// RepairSeasonConferences rewrites a season's conference facts from a
// re-extraction of the snapshot that last wrote them: each team's conference
// and division, each game's type, and the conference structure. Offseason
// saves used to record next season's realignment against the finished
// season; the extractor now reads the season's own conferences. Only rows
// that differ change. It returns the number of team seasons and games fixed.
func (s *Store) RepairSeasonConferences(ctx context.Context, snapshotID int, snap *extract.Snapshot) (teams, games int, err error) {
	var branch, season int
	if err := s.DB.QueryRow(ctx, `SELECT branch_id, season_index FROM snapshot WHERE id = $1`, snapshotID).Scan(&branch, &season); err != nil {
		return 0, 0, fmt.Errorf("snapshot %d: %w", snapshotID, err)
	}
	if season != snap.Season.SeasonIndex {
		return 0, 0, fmt.Errorf("snapshot %d is season %d but its save reads as %d", snapshotID, season, snap.Season.SeasonIndex)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx)

	b := &pgx.Batch{}
	for _, t := range snap.Teams {
		b.Queue(`UPDATE team_season SET conference = $4, division = $5
			WHERE branch_id = $1 AND season_index = $2 AND team_id = $3 AND (conference <> $4 OR division <> $5)`,
			branch, season, t.ID, t.Conference, t.Division)
	}
	for _, g := range snap.Games {
		if g.SeasonIndex != season {
			continue
		}
		b.Queue(`UPDATE game SET game_type = $4 WHERE branch_id = $1 AND season_index = $2 AND game_row = $3 AND game_type <> $4`,
			branch, season, g.Row, g.GameType)
	}
	res := tx.SendBatch(ctx, b)
	for i := range len(snap.Teams) {
		tag, err := res.Exec()
		if err != nil {
			res.Close()
			return 0, 0, fmt.Errorf("team %d: %w", snap.Teams[i].ID, err)
		}
		teams += int(tag.RowsAffected())
	}
	for _, g := range snap.Games {
		if g.SeasonIndex != season {
			continue
		}
		tag, err := res.Exec()
		if err != nil {
			res.Close()
			return 0, 0, fmt.Errorf("game %d: %w", g.Row, err)
		}
		games += int(tag.RowsAffected())
	}
	if err := res.Close(); err != nil {
		return 0, 0, err
	}

	var confs [][]any
	for _, c := range snap.Conferences {
		confs = append(confs, []any{branch, season, c.Name, c.ChampGame, js(c.Divisions), snapshotID})
	}
	if len(confs) == 0 {
		return teams, games, tx.Commit(ctx) // no conference table in this save: keep what's stored
	}
	if err := replaceWhere(ctx, tx, "conference_season", "branch_id = $1 AND season_index = $2", []any{branch, season},
		[]string{"branch_id", "season_index", "name", "champ_game", "divisions", "snapshot_id"}, confs); err != nil {
		return 0, 0, err
	}
	return teams, games, tx.Commit(ctx)
}
