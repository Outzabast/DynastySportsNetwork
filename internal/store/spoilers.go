package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// Spoilers. The game simulates every CPU game of a week before the user plays
// their own, and autosaves in between. Until the user's game that week is
// final, the site treats the week's other games as not yet played: no score,
// no box score or leaders, and they count toward no record or season total.
// History is stored complete; only the read side hides, and a later save with
// the user's game final shows everything.

// hiddenWeek is the set of games currently hidden, all in one week of one
// season. The zero value hides nothing.
type hiddenWeek struct {
	season   int
	weekType string
	week     int
	games    map[int]*hiddenGame // by game row
	// included marks teams whose season totals in the save already count
	// their hidden game, so the read side must take it back out. The save
	// usually publishes a week's results only when the week advances.
	included map[int]bool
}

type hiddenGame struct {
	home, away       int
	homeBox, awayBox Stats
}

func (h *hiddenWeek) has(season, row int) bool {
	return h != nil && season == h.season && h.games[row] != nil
}

// any reports whether a season (or -1, every season) has hidden games.
func (h *hiddenWeek) any(season int) bool {
	return h != nil && len(h.games) > 0 && (season < 0 || season == h.season)
}

// playedBy reports whether a team played in a hidden game of the season.
func (h *hiddenWeek) playedBy(season, team int) bool {
	if !h.any(season) {
		return false
	}
	for _, g := range h.games {
		if g.home == team || g.away == team {
			return true
		}
	}
	return false
}

// hidden returns the games hidden for the current save.
func (q *Queries) hidden(ctx context.Context, c *Ctx) (*hiddenWeek, error) {
	v, err := q.memo(c, "hidden", func() (any, error) { return q.loadHidden(ctx, c) })
	if err != nil {
		return nil, err
	}
	return v.(*hiddenWeek), nil
}

func (q *Queries) loadHidden(ctx context.Context, c *Ctx) (*hiddenWeek, error) {
	h := &hiddenWeek{season: c.Season, weekType: c.WeekType, week: c.Week, games: map[int]*hiddenGame{}, included: map[int]bool{}}
	if c.WeekType == "" || c.WeekType == "OffSeason" || c.WeekType == "PreSeason" {
		return h, nil
	}
	// The user's team -1 would match FCS opponents, so it must be a real team.
	rows, err := q.s.DB.Query(ctx, `SELECT g.game_row, g.home_team, g.away_team, g.home_box, g.away_box
		FROM game g JOIN dynasty d ON d.id = $1
		WHERE g.branch_id = $2 AND g.season_index = $3 AND g.week_type = $4 AND g.week = $5 AND g.played
			AND d.user_team_id >= 0 AND g.home_team <> d.user_team_id AND g.away_team <> d.user_team_id
			AND EXISTS (SELECT 1 FROM game u WHERE u.branch_id = g.branch_id AND u.season_index = g.season_index
				AND u.week_type = g.week_type AND u.week = g.week AND NOT u.played
				AND (u.home_team = d.user_team_id OR u.away_team = d.user_team_id))`,
		c.Dynasty, c.Branch, c.Season, c.WeekType, c.Week)
	if err != nil {
		return nil, err
	}
	var teams []int32
	for rows.Next() {
		var row int
		g := &hiddenGame{}
		var hb, ab []byte
		if err := rows.Scan(&row, &g.home, &g.away, &hb, &ab); err != nil {
			rows.Close()
			return nil, err
		}
		json.Unmarshal(hb, &g.homeBox)
		json.Unmarshal(ab, &g.awayBox)
		h.games[row] = g
		teams = append(teams, int32(g.home), int32(g.away))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(h.games) == 0 {
		return h, err
	}
	// A team's save record counts the hidden game when it holds as many
	// decisions as the stored results do, hidden ones included.
	rows, err = q.s.DB.Query(ctx, `SELECT ts.team_id FROM team_season ts
		WHERE ts.branch_id = $1 AND ts.season_index = $2 AND ts.team_id = ANY($3::int[])
			AND ts.wins + ts.losses >= (SELECT count(*) FROM game g WHERE g.branch_id = ts.branch_id AND g.season_index = ts.season_index
				AND g.played AND g.home_score <> g.away_score AND (g.home_team = ts.team_id OR g.away_team = ts.team_id))`,
		c.Branch, c.Season, teams)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t int
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		h.included[t] = true
	}
	return h, rows.Err()
}

// unplayed returns a copy of g as it looked before kickoff.
func unplayed(g *Game) *Game {
	u := *g
	u.Status, u.Played = "Unplayed", false
	u.HomeScore, u.AwayScore, u.HomeOT, u.AwayOT, u.Overtime = 0, 0, 0, 0, false
	u.HomeQ, u.AwayQ = nil, nil
	u.HomeBox, u.AwayBox = nil, nil
	u.Attendance = 0
	return &u
}

// mask replaces hidden games with their unplayed copies, in place.
func (h *hiddenWeek) mask(gs []*Game) []*Game {
	if h == nil || len(h.games) == 0 {
		return gs
	}
	for i, g := range gs {
		if h.has(g.Season, g.Row) {
			gs[i] = unplayed(g)
		}
	}
	return gs
}

// masked wraps scanGames: q.masked(ctx, c)(scanGames(rows)) returns the
// freshly scanned list with hidden games masked.
func (q *Queries) masked(ctx context.Context, c *Ctx) func([]*Game, error) ([]*Game, error) {
	return func(gs []*Game, err error) ([]*Game, error) {
		if err != nil {
			return nil, err
		}
		h, err := q.hidden(ctx, c)
		if err != nil {
			return nil, err
		}
		return h.mask(gs), nil
	}
}

// visibleRows drops hidden games from a list of a season's game rows.
func (h *hiddenWeek) visibleRows(season int, games []int) []int {
	if !h.any(season) {
		return games
	}
	out := make([]int, 0, len(games))
	for _, g := range games {
		if !h.has(season, g) {
			out = append(out, g)
		}
	}
	return out
}

// sub takes a line back out of s: the inverse of Add for summed stats.
// Longest plays and identity columns can't be undone and are left alone.
func (s Stats) sub(o Stats) {
	for k, v := range o {
		switch k {
		case "SEAS_YEAR", "YEARBYYEARTEAMINDEX", "STAT_KEEP":
			continue
		}
		if strings.HasSuffix(k, "LONGEST") || strings.HasSuffix(k, "LONGESTRETURN") {
			continue
		}
		if _, ok := s[k]; ok {
			s[k] -= v
		}
	}
}

// adjustTeams brings a season's teams in line with the hidden games: records
// come from the visible results alone, and totals that already count a
// hidden game lose it.
func (h *hiddenWeek) adjustTeams(season int, m map[int]*Team, recs map[int]*record) {
	if !h.any(season) {
		return
	}
	for _, g := range h.games {
		for _, side := range []struct {
			team int
			box  Stats
		}{{g.home, g.homeBox}, {g.away, g.awayBox}} {
			t := m[side.team]
			if t == nil {
				continue
			}
			r := recs[side.team]
			if r == nil {
				r = &record{}
			}
			t.W, t.L, t.CW, t.CL, t.Streak = r.W, r.L, r.CW, r.CL, r.Streak
			if h.included[side.team] && t.Stats != nil {
				t.Stats = copyStats(t.Stats)
				t.Stats.sub(side.box)
			}
		}
	}
}

func copyStats(s Stats) Stats {
	out := make(Stats, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// hiddenLines loads the hidden games' player lines that a save's season
// totals already count, for subtracting from season lines.
func (q *Queries) hiddenLines(ctx context.Context, c *Ctx, h *hiddenWeek, person uuid.UUID, team int) ([]*SeasonLine, error) {
	var rows, teams []int32
	for r := range h.games {
		rows = append(rows, int32(r))
	}
	for t := range h.included {
		if team < 0 || t == team {
			teams = append(teams, int32(t))
		}
	}
	if len(teams) == 0 {
		return nil, nil
	}
	rs, err := q.s.DB.Query(ctx, `SELECT person_id, team_id, category, stats FROM player_game_stat
		WHERE branch_id = $1 AND season_index = $2 AND game_row = ANY($3::int[]) AND team_id = ANY($4::int[])
			AND ($5::uuid IS NULL OR person_id = $5)`, c.Branch, h.season, rows, teams, nullUUID(person))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []*SeasonLine
	for rs.Next() {
		l := &SeasonLine{Season: h.season}
		var cat string
		var raw []byte
		if err := rs.Scan(&l.Player.ID, &l.TeamID, &cat, &raw); err != nil {
			return nil, err
		}
		json.Unmarshal(raw, &l.Stats)
		l.Group = Group(cat)
		out = append(out, l)
	}
	return out, rs.Err()
}
