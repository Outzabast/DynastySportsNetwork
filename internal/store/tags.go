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

// Page is one page of a browse listing.
type Page struct {
	Offset, Limit int
}

// BrowsePeople lists players a page at a time for the tag picker, players on
// a current roster first. The filter matches a name, the team's name, or a
// position exactly ("qb"); "" lists everyone. Each player shows as of his
// latest season on this branch. Each word of the filter must match ("baylor
// qb"). Returns the page and the total match count.
func (q *Queries) BrowsePeople(ctx context.Context, c *Ctx, filter string, p Page) ([]PlayerRef, int, error) {
	words := filterWords(filter)
	rows, err := q.s.DB.Query(ctx, `WITH latest AS (
			SELECT DISTINCT ON (person_id) person_id, team_id, position, jersey, portrait, school_year, on_roster
			FROM player_season WHERE branch_id = $1 ORDER BY person_id, season_index DESC)
		SELECT p.id, p.first_name, p.last_name, l.position, l.jersey, l.team_id, l.portrait, l.school_year, count(*) OVER ()
		FROM latest l JOIN person p ON p.id = l.person_id AND p.dynasty_id = $2
		LEFT JOIN team t ON t.dynasty_id = $2 AND t.team_id = l.team_id
		WHERE NOT EXISTS (SELECT 1 FROM unnest($3::text[]) w WHERE NOT (
			lower(p.first_name || ' ' || p.last_name) LIKE '%' || w || '%'
			OR lower(COALESCE(t.display_name || ' ' || t.long_name || ' ' || t.nick_name, '')) LIKE '%' || w || '%'
			OR lower(l.position) = w))
		ORDER BY l.on_roster DESC, p.last_name, p.first_name, p.id
		OFFSET $4 LIMIT $5`, c.Branch, c.Dynasty, words, p.Offset, p.Limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []PlayerRef
	total := 0
	for rows.Next() {
		var r PlayerRef
		if err := rows.Scan(&r.ID, &r.First, &r.Last, &r.Position, &r.Jersey, &r.TeamID, &r.Portrait, &r.ClassYear, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 && p.Offset > 0 {
		// Past the end: count without the page.
		_, total, err = q.BrowsePeople(ctx, c, filter, Page{Limit: 1})
	}
	return out, total, err
}

// BrowseCoaches lists coaches a page at a time for the tag picker, the most
// recently seen first. Each word of the filter must match the name or the
// team's name.
func (q *Queries) BrowseCoaches(ctx context.Context, c *Ctx, filter string, p Page) ([]CoachRef, int, error) {
	words := filterWords(filter)
	rows, err := q.s.DB.Query(ctx, `WITH latest AS (`+coachRefSelect+` ORDER BY coach_key, season_index DESC)
		SELECT k.*, count(*) OVER () FROM latest k
		LEFT JOIN team t ON t.dynasty_id = $2 AND t.team_id = k.team_id
		WHERE NOT EXISTS (SELECT 1 FROM unnest($3::text[]) w WHERE NOT (
			lower(k.first_name || ' ' || k.last_name) LIKE '%' || w || '%'
			OR lower(COALESCE(t.display_name || ' ' || t.long_name || ' ' || t.nick_name, '')) LIKE '%' || w || '%'))
		ORDER BY k.season_index DESC, k.last_name, k.first_name, k.coach_key
		OFFSET $4 LIMIT $5`, c.Branch, c.Dynasty, words, p.Offset, p.Limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []CoachRef
	total := 0
	for rows.Next() {
		var k CoachRef
		if err := rows.Scan(&k.Key, &k.First, &k.Last, &k.Position, &k.TeamID, &k.Asset, &k.Season, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 && p.Offset > 0 {
		_, total, err = q.BrowseCoaches(ctx, c, filter, Page{Limit: 1})
	}
	return out, total, err
}

// filterWords splits a browse filter into lowercase words.
func filterWords(filter string) []string {
	words := strings.Fields(strings.ToLower(filter))
	if words == nil {
		words = []string{}
	}
	return words
}
