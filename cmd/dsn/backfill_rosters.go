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

// runBackfillRosters fills roster_spot from the archived in-season saves, so
// past seasons' rosters include players who left before the season's last
// save (graduates, draftees, transfers). New saves record spots as they are
// ingested; this covers saves ingested before roster_spot existed. It only
// adds spots, and only for players whose identity alias covers the save.
func runBackfillRosters(args []string) error {
	var c config
	fs := flag.NewFlagSet("backfill-rosters", flag.ExitOnError)
	c.bind(fs)
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.DB.Query(ctx, `SELECT id, archive_path, save_name FROM snapshot
		WHERE stage <> 'OffSeason' AND archive_pruned_at IS NULL ORDER BY id`)
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
	fmt.Printf("%d in-season snapshot(s) with an archived save\n", len(items))

	total := 0
	for _, it := range items {
		tmp := filepath.Join(c.DataDir, "inbox", it.name+".copy")
		os.MkdirAll(filepath.Dir(tmp), 0o755)
		archive := filepath.FromSlash(strings.ReplaceAll(it.archive, `\`, "/"))
		if _, err := os.Stat(archive); err != nil {
			fmt.Printf("snapshot %d: archive missing, skipped\n", it.id)
			continue
		}
		if err := collector.Unarchive(archive, tmp); err != nil {
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		snap, err := extract.Extract(tmp, c.SchemaDir)
		os.Remove(tmp)
		if err != nil {
			return fmt.Errorf("snapshot %d: extract: %w", it.id, err)
		}
		n, skipped, err := st.BackfillRosterSpots(ctx, it.id, snap)
		if err != nil {
			return fmt.Errorf("snapshot %d: %w", it.id, err)
		}
		fmt.Printf("snapshot %d (%s, %d %s wk %d): %d spots, %d rows without an identity\n", it.id, it.name, snap.Season.Year, snap.Season.Stage, snap.Season.Week, n, skipped)
		total += n
	}
	fmt.Printf("recorded %d roster spots; restart `dsn serve` to clear its caches\n", total)
	return nil
}
