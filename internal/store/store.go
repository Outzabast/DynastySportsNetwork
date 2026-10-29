// Package store owns the Postgres schema, ingest/reconciliation, and the
// read queries the site uses.
package store

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps the connection pool.
type Store struct {
	DB *pgxpool.Pool
}

// Open connects and applies the schema (idempotent).
func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.Tracer = slowQueryTracer{threshold: 300 * time.Millisecond}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect %s: %w", url, err)
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{DB: pool}, nil
}

// LatestSnapshotID returns the newest snapshot's id (0 when none). A reader
// on another machine polls it to learn that the collector ingested a save.
func (s *Store) LatestSnapshotID(ctx context.Context) (int, error) {
	var id int
	err := s.DB.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM snapshot`).Scan(&id)
	return id, err
}

// Close releases the pool.
func (s *Store) Close() { s.DB.Close() }
