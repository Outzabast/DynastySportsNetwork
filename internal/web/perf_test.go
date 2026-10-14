package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// TestPagePerf counts database round trips and times the main pages. It only
// reads. Opt in with DSN_PERF_DB (a Postgres URL); DSN_PERF_RTT (e.g. 40ms)
// routes the connection through a local proxy that adds that round-trip
// latency, and DSN_PERF_OUT writes each page's HTML there for diffing.
//
//	DSN_PERF_DB=postgres://dsn:dsn@localhost:5433/dsn_v12?sslmode=disable DSN_PERF_RTT=40ms go test ./internal/web -run PagePerf -v
func TestPagePerf(t *testing.T) {
	dsn := os.Getenv("DSN_PERF_DB")
	if dsn == "" {
		t.Skip("DSN_PERF_DB not set")
	}
	ctx := context.Background()
	if rtt, _ := time.ParseDuration(os.Getenv("DSN_PERF_RTT")); rtt > 0 {
		dsn = viaLatencyProxy(t, dsn, rtt)
	}
	tr := &countTracer{trace: os.Getenv("DSN_PERF_TRACE") != ""}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Tracer = tr
	// Pool built directly, not with store.Open: Open applies the schema, and
	// this test must never write.
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := &store.Store{DB: pool}

	paths := perfPaths(t, ctx, pool)
	out := os.Getenv("DSN_PERF_OUT")
	if out != "" {
		os.MkdirAll(out, 0o755)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Warm the pool's connections so the first page doesn't pay for TLS.
	for i := 0; i < 4; i++ {
		pool.Exec(ctx, "SELECT 1")
	}
	for _, p := range paths {
		// Cold: a fresh site has empty context/teams caches, like the first
		// request after a restart or a new save. Warm: the same site again.
		site, err := New(Options{Store: st, MediaDir: filepath.Join("..", "..", "media"), Log: log})
		if err != nil {
			t.Fatal(err)
		}
		h := site.routes()
		for _, phase := range []string{"cold", "warm"} {
			tr.reset()
			start := time.Now()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", p, nil)
			req.RemoteAddr = "203.0.113.1:1234" // not loopback: no editor links
			h.ServeHTTP(rec, req)
			took := time.Since(start)
			if rec.Code != 200 {
				t.Errorf("%s: status %d: %s", p, rec.Code, rec.Body.String())
			}
			t.Logf("%-28s %s  queries=%3d batches=%2d connects=%d  %v", p, phase, tr.queries.Load(), tr.batches.Load(), tr.connects.Load(), took.Round(time.Millisecond))
			for _, l := range tr.log {
				t.Log(l)
			}
			if out != "" && phase == "warm" {
				name := strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(p, "_"), "_")
				if name == "" {
					name = "home"
				}
				os.WriteFile(filepath.Join(out, name+".html"), normalizeHTML(rec.Body.Bytes()), 0o644)
			}
		}
	}
}

// normalizeHTML drops relative timestamps ("5m", "2h") and the asset version
// (the binary's build time), which change between runs.
func normalizeHTML(b []byte) []byte {
	b = regexp.MustCompile(`\?v=\d+`).ReplaceAll(b, nil)
	return regexp.MustCompile(`>(just now|\d+[mhd])<`).ReplaceAll(b, []byte("><"))
}

func perfPaths(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	var dyn, branch, season, base, team int
	err := pool.QueryRow(ctx, `SELECT d.id, b.id, s.season_index, d.base_year, d.user_team_id
		FROM snapshot s JOIN branch b ON b.id = s.branch_id AND b.status = 'active' JOIN dynasty d ON d.id = s.dynasty_id
		ORDER BY d.last_save_at DESC NULLS LAST, s.pos DESC, s.id DESC LIMIT 1`).Scan(&dyn, &branch, &season, &base, &team)
	if err != nil {
		t.Fatal(err)
	}
	if base == 0 {
		var year int
		pool.QueryRow(ctx, `SELECT year FROM snapshot WHERE branch_id = $1 ORDER BY pos DESC LIMIT 1`, branch).Scan(&year)
		base = year - season
	}
	paths := []string{"/", "/standings", "/team/" + fmt.Sprint(team), "/team/" + fmt.Sprint(team) + "/history", "/coaches", "/scoreboard"}
	var row int
	if pool.QueryRow(ctx, `SELECT game_row FROM game WHERE branch_id = $1 AND season_index = $2 AND played AND (home_team = $3 OR away_team = $3)
		ORDER BY week DESC LIMIT 1`, branch, season, team).Scan(&row) == nil {
		paths = append(paths, fmt.Sprintf("/game/%d/%d", base+season, row))
	}
	var key string
	if pool.QueryRow(ctx, `SELECT coach_key FROM coach_season WHERE branch_id = $1 AND team_id = $2 AND position = 'HeadCoach'
		ORDER BY season_index DESC LIMIT 1`, branch, team).Scan(&key) == nil {
		paths = append(paths, "/coach/"+url.PathEscape(key))
	}
	return paths
}

// countTracer counts statements and new connections; with DSN_PERF_TRACE
// set it also records when each statement started and how long it took.
type countTracer struct {
	queries, batches, connects atomic.Int64
	trace                      bool
	mu                         sync.Mutex
	log                        []string
	t0                         time.Time
}

type traceStart struct{}
type traceSQL struct{}

func (c *countTracer) reset() {
	c.queries.Store(0)
	c.batches.Store(0)
	c.connects.Store(0)
	c.mu.Lock()
	c.log, c.t0 = nil, time.Now()
	c.mu.Unlock()
}

func (c *countTracer) record(ctx context.Context, what string) {
	if !c.trace {
		return
	}
	start, _ := ctx.Value(traceStart{}).(time.Time)
	what = strings.Join(strings.Fields(what), " ")
	if len(what) > 90 {
		what = what[:90]
	}
	c.mu.Lock()
	c.log = append(c.log, fmt.Sprintf("  +%5dms %5dms  %s", start.Sub(c.t0).Milliseconds(), time.Since(start).Milliseconds(), what))
	c.mu.Unlock()
}

func (c *countTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.queries.Add(1)
	return context.WithValue(context.WithValue(ctx, traceStart{}, time.Now()), traceSQL{}, d.SQL)
}
func (c *countTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	sql, _ := ctx.Value(traceSQL{}).(string)
	c.record(ctx, sql)
}
func (c *countTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	c.batches.Add(1)
	return context.WithValue(ctx, traceStart{}, time.Now())
}
func (c *countTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
	c.queries.Add(1)
}
func (c *countTracer) TraceBatchEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchEndData) {
	c.record(ctx, "(batch)")
}
func (c *countTracer) TraceConnectStart(ctx context.Context, _ pgx.TraceConnectStartData) context.Context {
	c.connects.Add(1)
	return context.WithValue(ctx, traceStart{}, time.Now())
}
func (c *countTracer) TraceConnectEnd(ctx context.Context, _ pgx.TraceConnectEndData) {
	c.record(ctx, "(connect)")
}

// viaLatencyProxy returns dsn rewritten to a local TCP proxy that delays
// every chunk by rtt/2 in each direction.
func viaLatencyProxy(t *testing.T, dsn string, rtt time.Duration) string {
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	target := u.Host
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				s, err := net.Dial("tcp", target)
				if err != nil {
					c.Close()
					return
				}
				go delayCopy(s, c, rtt/2)
				delayCopy(c, s, rtt/2)
			}()
		}
	}()
	u.Host = ln.Addr().String()
	return u.String()
}

func delayCopy(dst, src net.Conn, d time.Duration) {
	type chunk struct {
		b  []byte
		at time.Time
	}
	ch := make(chan chunk, 1024)
	go func() {
		defer dst.Close()
		for c := range ch {
			time.Sleep(time.Until(c.at))
			if _, err := dst.Write(c.b); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 64<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			ch <- chunk{append([]byte(nil), buf[:n]...), time.Now().Add(d)}
		}
		if err != nil {
			close(ch)
			return
		}
	}
}
