package store

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// slowQueryTracer logs queries that take longer than its threshold, so a
// slow page can be traced to the query (or the connection) behind it.
type slowQueryTracer struct{ threshold time.Duration }

type traceKey struct{}

type traceStart struct {
	at  time.Time
	sql string
}

func (t slowQueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, traceKey{}, traceStart{time.Now(), d.SQL})
}

func (t slowQueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
	st, ok := ctx.Value(traceKey{}).(traceStart)
	if !ok {
		return
	}
	if took := time.Since(st.at); took > t.threshold {
		sql := strings.Join(strings.Fields(st.sql), " ")
		if len(sql) > 160 {
			sql = sql[:160] + "…"
		}
		slog.Warn("slow query", "took", took.Round(time.Millisecond), "rows", d.CommandTag.RowsAffected(), "err", d.Err, "sql", sql)
	}
}
