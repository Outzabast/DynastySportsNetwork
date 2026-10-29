package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/extract"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// runRepairSeasonConferences fixes seasons whose last save was taken after
// an offseason realignment: those saves recorded next season's conferences
// against the finished season (a CUSA title game shown as the SEC
// Championship, conference games shown as non-conference). For each season
// on each branch, the save that last wrote it is re-read with the fixed
// extractor and its conference facts rewritten.
func runRepairSeasonConferences(args []string) error {
	var c config
	fs := flag.NewFlagSet("repair-season-conferences", flag.ExitOnError)
	c.bind(fs)
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.DB.Query(ctx, `SELECT DISTINCT ON (branch_id, season_index) id, archive_path, save_name, archive_pruned_at IS NOT NULL
		FROM snapshot ORDER BY branch_id, season_index, id DESC`)
	if err != nil {
		return err
	}
	type item struct {
		id            int
		archive, name string
		pruned        bool
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.archive, &it.name, &it.pruned); err != nil {
			return err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var teams, games int
	for _, it := range items {
		archive := filepath.FromSlash(strings.ReplaceAll(it.archive, `\`, "/"))
		if it.pruned {
			fmt.Printf("snapshot %d: archive pruned, skipped\n", it.id)
			continue
		}
		tmp := filepath.Join(c.DataDir, "inbox", it.name+".copy")
		os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := collector.Unarchive(archive, tmp); err != nil {
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		snap, err := extract.Extract(tmp, c.SchemaDir)
		os.Remove(tmp)
		if err != nil {
			return fmt.Errorf("snapshot %d: extract: %w", it.id, err)
		}
		t, g, err := st.RepairSeasonConferences(ctx, it.id, snap)
		if err != nil {
			return err
		}
		fmt.Printf("snapshot %d (%s, %d %s wk %d): %d team seasons, %d games fixed\n", it.id, it.name, snap.Season.Year, snap.Season.Stage, snap.Season.Week, t, g)
		teams, games = teams+t, games+g
	}
	fmt.Printf("fixed %d team seasons and %d games; restart `dsn serve` to clear its caches\n", teams, games)
	return nil
}
