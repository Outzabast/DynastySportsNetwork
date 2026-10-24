package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/extract"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// runBackfillTeamStats fills team_season.stats rows that were ingested empty
// (extractor bug: TeamSeasonStats was read as a TeamStats row instead of a
// TeamStats[] array). Each row is refilled from the archived save that wrote
// it, so nothing else in the live database changes. Rows that already hold
// stats are never touched.
func runBackfillTeamStats(args []string) error {
	var c config
	fs := flag.NewFlagSet("backfill-team-stats", flag.ExitOnError)
	c.bind(fs)
	dry := fs.Bool("dry-run", false, "report what would change without writing")
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.DB.Query(ctx, `SELECT DISTINCT s.id, s.archive_path, s.save_name FROM team_season ts
		JOIN snapshot s ON s.id = ts.snapshot_id
		WHERE (ts.stats IS NULL OR ts.stats = '{}'::jsonb) AND s.archive_pruned_at IS NULL ORDER BY s.id`)
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
	fmt.Printf("%d snapshot(s) own empty team stats\n", len(items))

	total := 0
	for _, it := range items {
		tmp := filepath.Join(c.DataDir, "inbox", it.name+".copy")
		os.MkdirAll(filepath.Dir(tmp), 0o755)
		// Archive paths are stored as written by the collector's OS
		// (backslashes on Windows); normalize so either machine can read them.
		archive := filepath.FromSlash(strings.ReplaceAll(it.archive, `\`, "/"))
		if err := collector.Unarchive(archive, tmp); err != nil {
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		snap, err := extract.Extract(tmp, c.SchemaDir)
		os.Remove(tmp)
		if err != nil {
			return fmt.Errorf("snapshot %d: extract: %w", it.id, err)
		}
		tx, err := st.DB.Begin(ctx)
		if err != nil {
			return err
		}
		n := 0
		for _, ts := range snap.TeamSeasonStats {
			if len(ts.Stats) == 0 {
				continue
			}
			b, _ := json.Marshal(ts.Stats)
			tag, err := tx.Exec(ctx, `UPDATE team_season SET stats = $1 WHERE snapshot_id = $2 AND team_id = $3
				AND (stats IS NULL OR stats = '{}'::jsonb)`, b, it.id, ts.TeamID)
			if err != nil {
				tx.Rollback(ctx)
				return err
			}
			n += int(tag.RowsAffected())
		}
		if *dry {
			tx.Rollback(ctx)
		} else if err := tx.Commit(ctx); err != nil {
			return err
		}
		fmt.Printf("snapshot %d (%s, %d %s wk %d): %d team rows\n", it.id, it.name, snap.Season.Year, snap.Season.Stage, snap.Season.Week, n)
		total += n
	}
	verb := "updated"
	if *dry {
		verb = "would update"
	}
	fmt.Printf("%s %d team_season rows; restart `dsn serve` to clear its team cache\n", verb, total)
	return nil
}
