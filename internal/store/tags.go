package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// Article tags name players by person UUID and coaches by CoachKey; these
// look up what the editor and story pages show for them.

// PeopleRefs returns the given players of this dynasty in the order asked;
// ids that aren't this dynasty's players are left out.
func (q *Queries) PeopleRefs(ctx context.Context, c *Ctx, ids []uuid.UUID) ([]PlayerRef, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.s.DB.Query(ctx, playerRefSelect+`p.id = ANY($3::uuid[]) AND p.dynasty_id = $4`, c.Branch, c.Season, ids, c.Dynasty)
	if err != nil {
		return nil, err
	}
	by, err := scanPlayerRefs(rows)
	if err != nil {
		return nil, err
	}
	var out []PlayerRef
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if r, ok := by[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// Name returns "First Last".
func (k CoachRef) Name() string { return strings.TrimSpace(k.First + " " + k.Last) }

const coachRefSelect = `SELECT DISTINCT ON (coach_key) coach_key, first_name, last_name, position, team_id, asset_name, season_index
	FROM coach_season WHERE branch_id = $1 AND coach_key <> ''`

func scanCoachRefs(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]CoachRef, error) {
	defer rows.Close()
	var out []CoachRef
	for rows.Next() {
		var k CoachRef
		if err := rows.Scan(&k.Key, &k.First, &k.Last, &k.Position, &k.TeamID, &k.Asset, &k.Season); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// CoachRefs returns the given coaches in the order asked; unknown keys are
// left out.
func (q *Queries) CoachRefs(ctx context.Context, c *Ctx, keys []string) ([]CoachRef, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := q.s.DB.Query(ctx, coachRefSelect+` AND coach_key = ANY($2::text[]) ORDER BY coach_key, season_index DESC`, c.Branch, keys)
	if err != nil {
		return nil, err
	}
	list, err := scanCoachRefs(rows)
	if err != nil {
		return nil, err
	}
	by := map[string]CoachRef{}
	for _, k := range list {
		by[k.Key] = k
	}
	var out []CoachRef
	seen := map[string]bool{}
	for _, key := range keys {
		if k, ok := by[key]; ok && !seen[key] {
			seen[key] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// SearchCoaches finds coaches by name, current staffs first.
func (q *Queries) SearchCoaches(ctx context.Context, c *Ctx, term string, limit int) ([]CoachRef, error) {
	like := "%" + strings.ToLower(term) + "%"
	rows, err := q.s.DB.Query(ctx, `SELECT * FROM (`+coachRefSelect+` AND lower(first_name || ' ' || last_name) LIKE $2
		ORDER BY coach_key, season_index DESC) k ORDER BY season_index DESC, last_name, first_name LIMIT $3`, c.Branch, like, limit)
	if err != nil {
		return nil, err
	}
	return scanCoachRefs(rows)
}
