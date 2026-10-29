package main

import (
	"context"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
	"github.com/outzabast/dynastysportsnetwork/internal/web"
)

// runHosted serves the read-only site in the Cloudflare container. The local
// `dsn serve` keeps watching saves and writing to the same database; this side
// never ingests. Media comes from R2 through the Worker (DSN_MEDIA_URL), and
// new snapshots are found by polling, since the ingest happens elsewhere.
func runHosted(ctx context.Context, p *pipeline, c config) error {
	site, err := web.New(web.Options{Store: p.store, MediaURL: env("DSN_MEDIA_URL", "http://media.r2"), BehindAccess: true, Log: p.log})
	if err != nil {
		return err
	}
	go pollSnapshots(ctx, p, site.Notify)
	p.log.Info("serving hosted", "addr", c.Addr)
	return site.ListenAndServe(ctx, c.Addr)
}

// pollSnapshots announces snapshots the collector ingested on another machine.
func pollSnapshots(ctx context.Context, p *pipeline, notify func(*store.IngestResult)) {
	last, err := p.store.LatestSnapshotID(ctx)
	if err != nil {
		p.log.Warn("poll snapshots", "err", err)
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		id, err := p.store.LatestSnapshotID(ctx)
		if err != nil {
			p.log.Warn("poll snapshots", "err", err)
			continue
		}
		if id > last {
			last = id
			notify(&store.IngestResult{SnapshotID: id, Action: "remote"})
		}
	}
}
