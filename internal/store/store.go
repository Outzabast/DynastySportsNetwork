// Package store owns the Postgres schema, ingest/reconciliation, and the
// read queries the site uses.
package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps the connection pool.
type Store struct {
	DB *pgxpool.Pool
}

// Open connects and applies the schema (idempotent).
func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
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
		return nil, fmt.Errorf("connect %s: %w", safeHost(dsn), connectCause(err))
	}
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{DB: pool}, nil
}

// safeHost names the database server without credentials or query options,
// so connection errors can be logged without leaking the URL's secrets.
func safeHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(unparsed DB url)"
	}
	return u.Host + u.Path
}

// connectCause drops pgx's ConnectError wrapper, whose message names the
// database user (on Prisma the user is the API key id).
func connectCause(err error) error {
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		if inner := ce.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// SnapshotCount returns how many snapshots the database holds.
func (s *Store) SnapshotCount(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM snapshot`).Scan(&n)
	return n, err
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
