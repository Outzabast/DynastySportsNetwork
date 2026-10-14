package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
)

// CoachKey identifies a coach across seasons within a dynasty. The game reuses
// Coach rows, so the row is not an identity; name plus portrait asset is.
func CoachKey(first, last, asset string) string {
	return strings.ToLower(strings.TrimSpace(first) + "|" + strings.TrimSpace(last) + "|" + strings.TrimSpace(asset))
}

// HistoryYear is one team season: the game's own history row merged with
// what we captured (captured seasons win for the season in progress).
type HistoryYear struct {
	Year         int
	Season       int // season index
	TeamID       int
	CoachName    string // abbreviated by the game, e.g. "B. McWhorter"
	Conference   string
	Division     string
	W, L, T      int
	CW, CL       int
	FinalRank    int
	ConfStanding int
	ConfChamp    bool
	Postseason   []Postseason
	Captured     bool // we hold box scores and stats for this season
}

// Record formats W-L(-T).
func (h *HistoryYear) Record() string {
	if h.T > 0 {
		return itoa(h.W) + "-" + itoa(h.L) + "-" + itoa(h.T)
	}
	return itoa(h.W) + "-" + itoa(h.L)
}

// ConfRecord formats conference W-L.
func (h *HistoryYear) ConfRecord() string { return itoa(h.CW) + "-" + itoa(h.CL) }

// Postseason is one bowl or CFP game in a season's history.
type Postseason struct {
	Bowl    string `json:"bowl"`
	Playoff bool   `json:"playoff"`
	Round   string `json:"round"`
	Won     bool   `json:"won"`
}

// TeamHistory returns every season the game or we know of for a team,
// newest first.
func (q *Queries) TeamHistory(ctx context.Context, c *Ctx, team int) ([]*HistoryYear, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT year, coach_name, conference, division, wins, losses, ties, conf_wins, conf_losses, final_rank, conf_standing,
		conf_champ, postseason FROM team_history WHERE branch_id = $1 AND team_id = $2`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[int]*HistoryYear{}
	for rows.Next() {
		h := &HistoryYear{TeamID: team}
		var post []byte
		if err := rows.Scan(&h.Year, &h.CoachName, &h.Conference, &h.Division, &h.W, &h.L, &h.T, &h.CW, &h.CL, &h.FinalRank, &h.ConfStanding,
			&h.ConfChamp, &post); err != nil {
			return nil, err
		}
		json.Unmarshal(post, &h.Postseason)
		h.Season = h.Year - c.BaseYear
		by[h.Year] = h
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Captured seasons: current records for the season in progress, and the
	// flag that box scores and stats exist.
	cr, err := q.s.DB.Query(ctx, `SELECT season_index, conference, division, wins, losses, conf_wins, conf_losses, conf_standing, ap_rank
		FROM team_season WHERE branch_id = $1 AND team_id = $2`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	for cr.Next() {
		var season, w, l, cw, cl, cs, ap int
		var conf, div string
		if err := cr.Scan(&season, &conf, &div, &w, &l, &cw, &cl, &cs, &ap); err != nil {
			return nil, err
		}
		y := c.YearOf(season)
		h, ok := by[y]
		if !ok {
			h = &HistoryYear{Year: y, Season: season, TeamID: team, Conference: conf, FinalRank: ap}
			by[y] = h
		}
		h.Captured = true
		if season == c.Season || !ok {
			h.W, h.L, h.CW, h.CL, h.ConfStanding = w, l, cw, cl, cs
			if div != "" {
				h.Division = div
			}
		}
	}
	if err := cr.Err(); err != nil {
		return nil, err
	}
	out := make([]*HistoryYear, 0, len(by))
	for _, h := range by {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Year > out[j].Year })
	return out, nil
}

// Stint is one season a coach spent at a school in one role.
type Stint struct {
	Season   int
	Year     int
	TeamID   int
	Position string // HeadCoach, OffensiveCoordinator, DefensiveCoordinator
	Captured bool   // from our captures (else inferred from the game's team history)
	History  *HistoryYear
}

// CoachProfile is everything about one coach across the dynasty.
type CoachProfile struct {
	Latest  *Coach   // most recent captured season
	Stints  []*Stint // newest first
	Seasons []int    // captured season indexes, ascending
}

// abbrev is the game's history spelling of a coach: "B. McWhorter".
func abbrev(first, last string) string {
	first = strings.TrimSpace(first)
	if first == "" {
		return strings.TrimSpace(last)
	}
	return first[:1] + ". " + strings.TrimSpace(last)
}

const coachCols = `coach_row, first_name, last_name, position, team_id, age, asset_name, wins, losses, school_wins, school_losses,
	bowl_wins, bowl_losses, conf_titles, natl_titles, playoff_wins, playoff_losses, is_user, coach_key, years_coaching, seasons_with_team, home_state`

func scanCoach(row interface{ Scan(...any) error }, k *Coach, extra ...any) error {
	dst := append(extra, &k.Row, &k.First, &k.Last, &k.Position, &k.TeamID, &k.Age, &k.Asset, &k.W, &k.L, &k.SW, &k.SL, &k.BowlW, &k.BowlL,
		&k.Conf, &k.Natl, &k.PlayoffW, &k.PlayoffL, &k.IsUser, &k.Key, &k.YearsCoaching, &k.SeasonsWithTeam, &k.HomeState)
	return row.Scan(dst...)
}

// CoachProfile gathers a coach's seasons. Captured seasons come from
// coach_season. Earlier head-coaching seasons are inferred from the game's
// team history when the abbreviated name matches at a school the coach is
// known to have led; coordinators are not in that history.
func (q *Queries) CoachProfile(ctx context.Context, c *Ctx, key string) (*CoachProfile, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT season_index, `+coachCols+` FROM coach_season WHERE branch_id = $1 AND coach_key = $2 ORDER BY season_index`, c.Branch, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	p := &CoachProfile{}
	captured := map[[2]int]bool{} // season, team
	headTeams := map[int]bool{}
	for rows.Next() {
		k := &Coach{}
		var season int
		if err := scanCoach(rows, k, &season); err != nil {
			return nil, err
		}
		p.Latest = k
		p.Seasons = append(p.Seasons, season)
		if k.TeamID < 0 {
			continue // between jobs
		}
		captured[[2]int{season, k.TeamID}] = true
		if k.Position == "HeadCoach" {
			headTeams[k.TeamID] = true
		}
		p.Stints = append(p.Stints, &Stint{Season: season, Year: c.YearOf(season), TeamID: k.TeamID, Position: k.Position, Captured: true})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if p.Latest == nil {
		return nil, nil
	}
	teams := map[int]bool{}
	for _, s := range p.Stints {
		teams[s.TeamID] = true
	}
	name := abbrev(p.Latest.First, p.Latest.Last)
	for team := range teams {
		hist, err := q.TeamHistory(ctx, c, team)
		if err != nil {
			return nil, err
		}
		for _, h := range hist {
			for _, s := range p.Stints {
				if s.TeamID == team && s.Year == h.Year {
					s.History = h
				}
			}
			if headTeams[team] && !captured[[2]int{h.Season, team}] && strings.EqualFold(h.CoachName, name) {
				p.Stints = append(p.Stints, &Stint{Season: h.Season, Year: h.Year, TeamID: team, Position: "HeadCoach", History: h})
			}
		}
	}
	sort.Slice(p.Stints, func(i, j int) bool { return p.Stints[i].Year > p.Stints[j].Year })
	return p, nil
}

// StaffHistory returns the coaches captured at a team per season:
// season index -> position -> coach.
func (q *Queries) StaffHistory(ctx context.Context, c *Ctx, team int) (map[int]map[string]*Coach, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT season_index, first_name, last_name, position, coach_key FROM coach_season
		WHERE branch_id = $1 AND team_id = $2`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]map[string]*Coach{}
	for rows.Next() {
		k := &Coach{TeamID: team}
		var season int
		if err := rows.Scan(&season, &k.First, &k.Last, &k.Position, &k.Key); err != nil {
			return nil, err
		}
		if out[season] == nil {
			out[season] = map[string]*Coach{}
		}
		out[season][k.Position] = k
	}
	return out, rows.Err()
}

// GamesForTeamSeasons returns a team's games in the given seasons, newest first.
func (q *Queries) GamesForTeamSeasons(ctx context.Context, c *Ctx, team int, seasons []int) ([]*Game, error) {
	ss := make([]int32, len(seasons))
	for i, s := range seasons {
		ss[i] = int32(s)
	}
	rows, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = ANY($2::int[]) AND (home_team = $3 OR away_team = $3)
		AND (home_team >= 0 OR away_team >= 0) AND week_type <> 'PreSeason'
		ORDER BY season_index DESC, CASE week_type WHEN 'RegularSeason' THEN 0 ELSE 1 END DESC, week DESC`, c.Branch, ss, team)
	if err != nil {
		return nil, err
	}
	return scanGames(rows)
}
