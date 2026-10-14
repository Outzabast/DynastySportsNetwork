package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/extract"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
	"github.com/outzabast/dynastysportsnetwork/internal/web"
)

// pipeline turns a save copy into canonical history. Ingests are serialized.
type pipeline struct {
	cfg    config
	store  *store.Store
	log    *slog.Logger
	mu     sync.Mutex
	notify func(*store.IngestResult)
}

func (p *pipeline) IngestFile(ctx context.Context, copyPath, archivePath, sha string, modTime time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	start := time.Now()
	snap, err := extract.Extract(copyPath, p.cfg.SchemaDir)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	snap.SaveName = filepath.Base(filepath.Clean(trimCopySuffix(copyPath)))
	snap.SHA256 = sha
	res, err := p.store.Ingest(ctx, snap, archivePath, modTime)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	p.log.Info("ingested", "save", snap.SaveName, "season", snap.Season.Year, "stage", snap.Season.Stage, "week", snap.Season.Week,
		"action", res.Action, "branch", res.BranchID, "snapshot", res.SnapshotID,
		"kept", res.Persons.Kept, "minted", res.Persons.Minted, "relinked", res.Persons.Relinked, "closed", res.Persons.Closed,
		"took", time.Since(start).Round(time.Millisecond))
	for _, n := range res.Notes {
		p.log.Warn("reconcile", "note", n)
	}
	if p.notify != nil && res.Action != "duplicate" {
		p.notify(res)
	}
	return nil
}

func trimCopySuffix(p string) string {
	if filepath.Ext(p) == ".copy" {
		return p[:len(p)-len(".copy")]
	}
	return p
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

func openPipeline(ctx context.Context, c config) (*pipeline, error) {
	st, err := store.Open(ctx, c.DB)
	if err != nil {
		return nil, err
	}
	return &pipeline{cfg: c, store: st, log: newLogger()}, nil
}

// runIngest copies and ingests explicit save files (oldest first by mtime).
func runIngest(args []string) error {
	var c config
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	c.bind(fs)
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("ingest needs at least one save path")
	}
	ctx := context.Background()
	p, err := openPipeline(ctx, c)
	if err != nil {
		return err
	}
	defer p.store.Close()
	for _, path := range fs.Args() {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(c.DataDir, "inbox"), 0o755); err != nil {
			return err
		}
		tmp := filepath.Join(c.DataDir, "inbox", filepath.Base(path)+".copy")
		if err := collector.CopyFile(path, tmp); err != nil {
			return err
		}
		sha, _, err := extract.SHA256File(tmp)
		if err != nil {
			return err
		}
		archive, err := collector.Archive(c.DataDir, tmp, sha)
		if err != nil {
			return err
		}
		err = p.IngestFile(ctx, tmp, archive, sha, info.ModTime())
		os.Remove(tmp)
		if err != nil {
			return err
		}
	}
	return nil
}

// runReingest rebuilds history from the raw archive (e.g. after a parser
// upgrade): it re-ingests every archived snapshot into a fresh database in
// the order the snapshots were first ingested.
func runReingest(args []string) error {
	var c config
	fs := flag.NewFlagSet("reingest", flag.ExitOnError)
	c.bind(fs)
	target := fs.String("into", "", "Postgres URL of an EMPTY database to rebuild into (required)")
	fs.Parse(args)
	if *target == "" {
		return fmt.Errorf("-into is required; reingest never rewrites the live database in place")
	}
	ctx := context.Background()
	src, err := store.Open(ctx, c.DB)
	if err != nil {
		return err
	}
	defer src.Close()
	rows, err := src.DB.Query(ctx, `SELECT DISTINCT ON (sha256) sha256, archive_path, save_name, mod_time FROM snapshot ORDER BY sha256, id`)
	if err != nil {
		return err
	}
	type item struct {
		sha, archive, name string
		mod                time.Time
	}
	var items []item
	for rows.Next() {
		var it item
		var mod *time.Time
		if err := rows.Scan(&it.sha, &it.archive, &it.name, &mod); err != nil {
			return err
		}
		if mod != nil {
			it.mod = *mod
		}
		items = append(items, it)
	}
	rows.Close()
	order := map[string]int{}
	r2, err := src.DB.Query(ctx, `SELECT sha256, MIN(id) FROM snapshot GROUP BY sha256`)
	if err != nil {
		return err
	}
	for r2.Next() {
		var sha string
		var id int
		r2.Scan(&sha, &id)
		order[sha] = id
	}
	r2.Close()
	sortItems := func(a, b item) bool { return order[a.sha] < order[b.sha] }
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && sortItems(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	dc := c
	dc.DB = *target
	p, err := openPipeline(ctx, dc)
	if err != nil {
		return err
	}
	defer p.store.Close()
	for _, it := range items {
		tmp := filepath.Join(c.DataDir, "inbox", it.name+".copy")
		os.MkdirAll(filepath.Dir(tmp), 0o755)
		if err := collector.Unarchive(it.archive, tmp); err != nil {
			return err
		}
		err := p.IngestFile(ctx, tmp, it.archive, it.sha, it.mod)
		os.Remove(tmp)
		if err != nil {
			return err
		}
	}
	return nil
}

func runServe(args []string) error {
	var c config
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	c.bind(fs)
	fs.Parse(args)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	p, err := openPipeline(ctx, c)
	if err != nil {
		return err
	}
	defer p.store.Close()

	col := &collector.Collector{SavesDir: c.SavesDir, DataDir: c.DataDir, Ingest: p, Log: p.log}
	site, err := web.New(web.Options{Store: p.store, MediaDir: c.MediaDir, Log: p.log, Status: col.Snapshot, SavesDir: c.SavesDir})
	if err != nil {
		return err
	}
	p.notify = site.Notify
	if !c.NoWatch {
		p.log.Info("watching saves", "dir", c.SavesDir)
		go col.Run(ctx)
	}
	p.log.Info("serving", "addr", "http://localhost"+c.Addr)
	return site.ListenAndServe(ctx, c.Addr)
}
