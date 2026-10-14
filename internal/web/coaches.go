package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

func coachURL(key string) string {
	if key == "" {
		return ""
	}
	return "/coach/" + url.PathEscape(key)
}

func positionLabel(p string) string {
	switch p {
	case "HeadCoach":
		return "Head Coach"
	case "OffensiveCoordinator":
		return "Offensive Coordinator"
	case "DefensiveCoordinator":
		return "Defensive Coordinator"
	}
	return prettyEnum(p)
}

// StintRow is one row of a coach's year-by-year table.
type StintRow struct {
	*store.Stint
	Team *store.Team
}

// CoachedGame is one game on a coach's log.
type CoachedGame struct {
	Season   int
	Position string
	Card     *GameCard
	Team     int // the coach's team
	Won      bool
	Decided  bool
}

// CoachSeasonGames groups a coach's games by season.
type CoachSeasonGames struct {
	Year     int
	Team     *store.Team
	Position string
	W, L     int
	Games    []CoachedGame
}

func (s *Site) coach(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	key := r.PathValue("key")
	var p *store.CoachProfile
	var teams map[int]*store.Team
	err := parallel(
		func() (err error) { p, err = s.q.CoachProfile(ctx, c, key); return },
		func() (err error) { teams, err = s.q.Teams(ctx, c, c.Season); return },
	)
	if err != nil {
		return "", nil, err
	}
	if p == nil {
		return "", nil, errNotFound
	}
	var rows []StintRow
	var hcW, hcL, bowlW, bowlL, cfpW, cfpL, confTitles int
	schools := map[int]bool{}
	for _, st := range p.Stints {
		rows = append(rows, StintRow{st, teams[st.TeamID]})
		schools[st.TeamID] = true
		if st.Position != "HeadCoach" || st.History == nil {
			continue
		}
		hcW += st.History.W
		hcL += st.History.L
		if st.History.ConfChamp {
			confTitles++
		}
		for _, ps := range st.History.Postseason {
			switch {
			case ps.Playoff && ps.Won:
				cfpW++
			case ps.Playoff:
				cfpL++
			case ps.Won:
				bowlW++
			default:
				bowlL++
			}
		}
	}

	// Games coached: every captured season, the team's games in that role.
	// One query for all of them, with each season's teams loaded alongside.
	var list []store.TeamSeason
	seen := map[store.TeamSeason]bool{}
	var loads []func() error
	for _, st := range p.Stints {
		ts := store.TeamSeason{Season: st.Season, Team: st.TeamID}
		if !st.Captured || seen[ts] {
			continue
		}
		seen[ts] = true
		list = append(list, ts)
		loads = append(loads, func() error { _, err := s.q.Teams(ctx, c, st.Season); return err })
	}
	var byStint map[store.TeamSeason][]*store.Game
	loads = append(loads, func() (err error) { byStint, err = s.q.GamesForTeamSeasonList(ctx, c, list); return })
	if err := parallel(loads...); err != nil {
		return "", nil, err
	}
	var logs []CoachSeasonGames
	for _, st := range p.Stints {
		if !st.Captured {
			continue
		}
		games := byStint[store.TeamSeason{Season: st.Season, Team: st.TeamID}]
		cards, err := s.cards(ctx, c, st.Season, games, false)
		if err != nil {
			return "", nil, err
		}
		sg := CoachSeasonGames{Year: st.Year, Team: teams[st.TeamID], Position: st.Position}
		for i, g := range games {
			cg := CoachedGame{Season: st.Season, Position: st.Position, Card: cards[i], Team: st.TeamID}
			if g.Played {
				cg.Decided = true
				cg.Won = (g.Home == st.TeamID && g.HomeScore > g.AwayScore) || (g.Away == st.TeamID && g.AwayScore > g.HomeScore)
				if cg.Won {
					sg.W++
				} else {
					sg.L++
				}
			}
			sg.Games = append(sg.Games, cg)
		}
		if len(sg.Games) > 0 {
			logs = append(logs, sg)
		}
	}

	k := p.Latest
	var current *store.Team
	if k.TeamID >= 0 && len(p.Seasons) > 0 && p.Seasons[len(p.Seasons)-1] == c.Season {
		current = teams[k.TeamID]
	}
	return "coach", data{"K": k, "Current": current, "Stints": rows, "Logs": logs, "Teams": teams,
		"HCW": hcW, "HCL": hcL, "BowlW": bowlW, "BowlL": bowlL, "CFPW": cfpW, "CFPL": cfpL, "ConfTitles": confTitles,
		"Schools": len(schools), "Seasons": len(p.Stints)}, nil
}

// HistoryRow is one season on a team's history page.
type HistoryRow struct {
	*store.HistoryYear
	HeadCoach, OC, DC *store.Coach
}

func (s *Site) teamHistory(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	var t *store.Team
	var season int
	var hist []*store.HistoryYear
	var staff map[int]map[string]*store.Coach
	id := atoi(r.PathValue("id"), -1)
	err := parallel(
		func() (err error) { t, season, err = s.teamHeader(ctx, r, c); return },
		func() (err error) { hist, err = s.q.TeamHistory(ctx, c, id); return },
		func() (err error) { staff, err = s.q.StaffHistory(ctx, c, id); return },
	)
	if err != nil {
		return "", nil, err
	}
	// Seasons before capture name only the head coach, abbreviated; link it
	// when a captured head coach of this school has that spelling.
	byAbbrev := map[string]*store.Coach{}
	for _, pos := range staff {
		if hc := pos["HeadCoach"]; hc != nil && hc.First != "" {
			byAbbrev[strings.ToLower(hc.First[:1]+". "+hc.Last)] = hc
		}
	}
	var rows []HistoryRow
	var w, l, bowlW, bowlL, cfpW, cfpL, titles, ranked int
	for _, h := range hist {
		row := HistoryRow{HistoryYear: h}
		if pos := staff[h.Season]; pos != nil {
			row.HeadCoach, row.OC, row.DC = pos["HeadCoach"], pos["OffensiveCoordinator"], pos["DefensiveCoordinator"]
		}
		if row.HeadCoach == nil {
			row.HeadCoach = byAbbrev[strings.ToLower(h.CoachName)]
		}
		rows = append(rows, row)
		w += h.W
		l += h.L
		if h.ConfChamp {
			titles++
		}
		if h.FinalRank > 0 && h.FinalRank <= 25 && h.Season < c.Season {
			ranked++
		}
		for _, ps := range h.Postseason {
			switch {
			case ps.Playoff && ps.Won:
				cfpW++
			case ps.Playoff:
				cfpL++
			case ps.Won:
				bowlW++
			default:
				bowlL++
			}
		}
	}
	return "teamhistory", data{"T": t, "Season": season, "Tab": "history", "Rows": rows, "W": w, "L": l,
		"BowlW": bowlW, "BowlL": bowlL, "CFPW": cfpW, "CFPL": cfpL, "Titles": titles, "Ranked": ranked}, nil
}

// StaffRow is one school's staff on the coaches index.
type StaffRow struct {
	Team              *store.Team
	HeadCoach, OC, DC *store.Coach
}

func (s *Site) coaches(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season := seasonParam(r, c)
	var teams map[int]*store.Team
	var all []*store.Coach
	err := parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { all, err = s.q.Coaches(ctx, c, season, -1); return },
	)
	if err != nil {
		return "", nil, err
	}
	by := map[int]*StaffRow{}
	for _, k := range all {
		t := teams[k.TeamID]
		if t == nil {
			continue
		}
		row := by[k.TeamID]
		if row == nil {
			row = &StaffRow{Team: t}
			by[k.TeamID] = row
		}
		switch k.Position {
		case "HeadCoach":
			row.HeadCoach = k
		case "OffensiveCoordinator":
			row.OC = k
		case "DefensiveCoordinator":
			row.DC = k
		}
	}
	rows := make([]*StaffRow, 0, len(by))
	for _, row := range by {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Team.Name < rows[j].Team.Name })
	return "coaches", data{"Rows": rows, "Season": season}, nil
}
