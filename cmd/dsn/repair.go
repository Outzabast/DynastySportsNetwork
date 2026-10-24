package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/extract"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// runRepairFCSLines fixes the team on player game lines in games against FCS
// opponents. Lines captured from a game's week-of cache were put on the FBS
// team when they belonged to the FCS side (extractor bug: the FCS side's team
// id is -1, which was read as "no side given"). Each line is re-derived from
// the archived save that wrote it, matched by game, category and source stat
// row, and only its team_id changes. Lines in FBS-vs-FBS games are never
// touched.
func runRepairFCSLines(args []string) error {
	var c config
	fs := flag.NewFlagSet("repair-fcs-lines", flag.ExitOnError)
	c.bind(fs)
	dry := fs.Bool("dry-run", false, "report what would change without writing")
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.DB.Query(ctx, `SELECT DISTINCT s.id, s.archive_path, s.save_name FROM player_game_stat l
		JOIN game g ON g.branch_id = l.branch_id AND g.season_index = l.season_index AND g.game_row = l.game_row
		JOIN snapshot s ON s.id = l.snapshot_id
		WHERE -1 IN (g.home_team, g.away_team) AND s.archive_pruned_at IS NULL ORDER BY s.id`)
	if err != nil {
		return err
	}
	type item struct {
		id            int
		archive, name string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.archive, &it.name); err != nil {
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Printf("%d snapshot(s) own lines in games against FCS opponents\n", len(items))

	total := 0
	for _, it := range items {
		tmp := filepath.Join(c.DataDir, "inbox", it.name+".copy")
		os.MkdirAll(filepath.Dir(tmp), 0o755)
		archive := filepath.FromSlash(strings.ReplaceAll(it.archive, `\`, "/"))
		if err := collector.Unarchive(archive, tmp); err != nil {
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		snap, err := extract.Extract(tmp, c.SchemaDir)
		os.Remove(tmp)
		if err != nil {
			return fmt.Errorf("snapshot %d: extract: %w", it.id, err)
		}
		fcs := map[int]bool{}
		for _, g := range snap.Games {
			if g.HomeTeamID == -1 || g.AwayTeamID == -1 {
				fcs[g.Row] = true
			}
		}
		var fix [][]any
		for _, l := range snap.PlayerGameStats {
			if fcs[l.GameRow] {
				fix = append(fix, []any{l.GameRow, l.Category, l.SourceRow, l.TeamID})
			}
		}

		tx, err := st.DB.Begin(ctx)
		if err != nil {
			return err
		}
		n, err := applyTeamFix(ctx, tx, it.id, snap.Season.SeasonIndex, fix)
		if err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		if *dry {
			tx.Rollback(ctx)
		} else if err := tx.Commit(ctx); err != nil {
			return err
		}
		fmt.Printf("snapshot %d (%s, %d %s wk %d): %d lines\n", it.id, it.name, snap.Season.Year, snap.Season.Stage, snap.Season.Week, n)
		total += n
	}
	verb := "moved"
	if *dry {
		verb = "would move"
	}
	fmt.Printf("%s %d lines to their correct team; restart `dsn serve` to clear its caches\n", verb, total)
	return nil
}

// applyTeamFix sets team_id on the lines a snapshot wrote, keyed by game,
// category and source stat row. It returns the number of lines changed.
func applyTeamFix(ctx context.Context, tx pgx.Tx, snapshot, season int, fix [][]any) (int, error) {
	if len(fix) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE fcs_fix (game_row int, category text, source_row int, team_id int) ON COMMIT DROP`); err != nil {
		return 0, err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"fcs_fix"}, []string{"game_row", "category", "source_row", "team_id"}, pgx.CopyFromRows(fix)); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE player_game_stat l SET team_id = f.team_id FROM fcs_fix f
		WHERE l.snapshot_id = $1 AND l.season_index = $2 AND l.game_row = f.game_row AND l.category = f.category
			AND l.source_row = f.source_row AND l.team_id <> f.team_id`, snapshot, season)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
