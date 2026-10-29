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

// runBackfillDraftAwards re-reads the archived saves in timeline order to
// record what saves ingested before these features could not: NFL draft
// picks (found by comparing each save with the one before it) and the season
// of every weekly award. Saves ingested from now on record both themselves.
func runBackfillDraftAwards(args []string) error {
	var c config
	fs := flag.NewFlagSet("backfill-draft-awards", flag.ExitOnError)
	c.bind(fs)
	fs.Parse(args)
	ctx := context.Background()
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	rows, err := st.DB.Query(ctx, `SELECT id, archive_path, save_name FROM snapshot
		WHERE archive_pruned_at IS NULL ORDER BY branch_id, pos, id`)
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
	fmt.Printf("%d snapshot(s) with an archived save\n", len(items))

	picks, dated := 0, 0
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
		p, err := st.BackfillDraft(ctx, it.id, snap)
		if err != nil {
			return fmt.Errorf("snapshot %d: draft: %w", it.id, err)
		}
		d, err := st.BackfillAwardSeasons(ctx, it.id, snap)
		if err != nil {
			return fmt.Errorf("snapshot %d: awards: %w", it.id, err)
		}
		fmt.Printf("snapshot %d (%s, %d %s wk %d): %d draft picks, %d awards dated\n", it.id, it.name, snap.Season.Year, snap.Season.Stage, snap.Season.Week, p, d)
		picks, dated = picks+p, dated+d
	}
	// Season awards carry their season; date any the saves above no longer list.
	n, err := st.DateSeasonAwards(ctx)
	if err != nil {
		return err
	}
	var left int
	st.DB.QueryRow(ctx, `SELECT count(*) FROM award WHERE season_index = -1`).Scan(&left)
	fmt.Printf("recorded %d draft picks, dated %d awards (+%d season awards); %d awards still undated; restart `dsn serve` to clear its caches\n", picks, dated, n, left)
	return nil
}
