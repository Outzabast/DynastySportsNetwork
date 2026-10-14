package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// upsert bulk-loads rows through a temp table and merges them into table.
// Columns in keep are left untouched on conflict (e.g. first_snapshot).
func upsert(ctx context.Context, tx pgx.Tx, table string, cols, pk []string, rows [][]any, keep ...string) error {
	if len(rows) == 0 {
		return nil
	}
	tmp := "tmp_" + table
	if _, err := tx.Exec(ctx, fmt.Sprintf(`CREATE TEMP TABLE IF NOT EXISTS %s (LIKE %s INCLUDING DEFAULTS) ON COMMIT DROP`, tmp, table)); err != nil {
		return fmt.Errorf("%s temp: %w", table, err)
	}
	if _, err := tx.Exec(ctx, "TRUNCATE "+tmp); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{tmp}, cols, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("%s copy: %w", table, err)
	}
	isPK := map[string]bool{}
	for _, c := range pk {
		isPK[c] = true
	}
	skip := map[string]bool{}
	for _, c := range keep {
		skip[c] = true
	}
	var sets []string
	for _, c := range cols {
		if !isPK[c] && !skip[c] {
			sets = append(sets, fmt.Sprintf("%s = excluded.%s", c, c))
		}
	}
	colList := strings.Join(cols, ", ")
	q := fmt.Sprintf(`INSERT INTO %s (%s) SELECT DISTINCT ON (%s) %s FROM %s ON CONFLICT (%s) DO `, table, colList, strings.Join(pk, ", "), colList, tmp, strings.Join(pk, ", "))
	if len(sets) == 0 {
		q += "NOTHING"
	} else {
		q += "UPDATE SET " + strings.Join(sets, ", ")
	}
	if _, err := tx.Exec(ctx, q); err != nil {
		return fmt.Errorf("%s merge: %w", table, err)
	}
	return nil
}

// replace swaps a branch's current-state rows for a table.
func replace(ctx context.Context, tx pgx.Tx, table string, branch int, cols []string, rows [][]any) error {
	if _, err := tx.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE branch_id = $1", table), branch); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("%s copy: %w", table, err)
	}
	return nil
}

// tableColumns lists a table's columns in order.
func tableColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// copyBranchRows copies rows matching where (with $1 = source branch) into a
// new branch, rewriting branch_id.
func copyBranchRows(ctx context.Context, tx pgx.Tx, table string, from, to int, where string, args ...any) error {
	cols, err := tableColumns(ctx, tx, table)
	if err != nil {
		return err
	}
	// Serial ids are regenerated in the new branch.
	kept := cols[:0:0]
	sel := []string{}
	for _, c := range cols {
		switch c {
		case "id":
			continue
		case "branch_id":
			sel = append(sel, fmt.Sprintf("%d", to))
		default:
			sel = append(sel, c)
		}
		kept = append(kept, c)
	}
	cols = kept
	q := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s WHERE branch_id = $1", table, strings.Join(cols, ", "), strings.Join(sel, ", "), table)
	if where != "" {
		q += " AND (" + where + ")"
	}
	_, err = tx.Exec(ctx, q, append([]any{from}, args...)...)
	if err != nil {
		return fmt.Errorf("copy %s: %w", table, err)
	}
	return nil
}
