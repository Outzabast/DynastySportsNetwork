package store

import "context"

// SupersededArchive is a raw save none of whose snapshots is the newest save
// at its timeline position on its branch. The game autosaves several times a
// week and the week's last save carries everything the earlier ones did, so
// only that one is kept in the archive.
type SupersededArchive struct {
	SHA       string
	Snapshots []int
}

// SupersededArchives lists archived saves that can be deleted. scope limits it
// to the timeline position of one snapshot (0 = every position). A save shared
// by several snapshots goes only when every one of them is superseded.
func (s *Store) SupersededArchives(ctx context.Context, scope int) ([]SupersededArchive, error) {
	rows, err := s.DB.Query(ctx, `WITH k AS (
			SELECT id, sha256, branch_id, pos, archive_pruned_at,
				row_number() OVER (PARTITION BY branch_id, pos ORDER BY mod_time DESC NULLS LAST, id DESC) = 1 AS newest
			FROM snapshot),
		sc AS (SELECT branch_id, pos FROM snapshot WHERE id = $1)
		SELECT k.sha256, array_agg(k.id ORDER BY k.id) FROM k LEFT JOIN sc ON true
		GROUP BY k.sha256
		HAVING bool_and(NOT k.newest) AND bool_or(k.archive_pruned_at IS NULL)
			AND ($1 = 0 OR bool_or(k.branch_id = sc.branch_id AND k.pos = sc.pos))
		ORDER BY min(k.id)`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SupersededArchive
	for rows.Next() {
		var a SupersededArchive
		var ids []int32
		if err := rows.Scan(&a.SHA, &ids); err != nil {
			return nil, err
		}
		for _, id := range ids {
			a.Snapshots = append(a.Snapshots, int(id))
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkArchivePruned records that a save's archive file is being deleted.
func (s *Store) MarkArchivePruned(ctx context.Context, sha string) error {
	_, err := s.DB.Exec(ctx, `UPDATE snapshot SET archive_pruned_at = now() WHERE sha256 = $1 AND archive_pruned_at IS NULL`, sha)
	return err
}
