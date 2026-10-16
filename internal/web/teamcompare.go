package web

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Team stat comparison: every FBS team (or one conference) ranked on its
// season TeamStats totals, shown per game or as season totals.

type teamColKind int

const (
	tcCount teamColKind = iota // counting stat: divided by games in per-game mode
	tcRate                     // ratio of totals: same in both modes
	tcTime                     // seconds, shown as m:ss
	tcPlain                    // never divided (games played)
)

// teamCol is one column of a team comparison table. Values are read from the
// team's TeamStats row plus the synthetic PTS / PTSA / PGP keys (points for,
// against, and games those points came from).
type teamCol struct {
	Key, Title string
	kind       teamColKind
	val        func(S) float64
	low        bool // lower is better (defense allowed, giveaways, penalties)
	digits     int  // rate columns
	pct        bool // rate shown as a percentage
	div        string
}

type teamTable struct {
	ID, Name string
	Cols     []teamCol
	SortBy   int
}

func k(key string) func(S) float64 { return func(s S) float64 { return float64(s[key]) } }

func sum(keys ...string) func(S) float64 {
	return func(s S) float64 {
		var t int64
		for _, key := range keys {
			t += s[key]
		}
		return float64(t)
	}
}

func per(num, den func(S) float64) func(S) float64 {
	return func(s S) float64 {
		if d := den(s); d != 0 {
			return num(s) / d
		}
		return 0
	}
}

func cnt(key, title string, v func(S) float64) teamCol {
	return teamCol{Key: key, Title: title, kind: tcCount, val: v}
}

func rate(key, title string, v func(S) float64, digits int) teamCol {
	return teamCol{Key: key, Title: title, kind: tcRate, val: v, digits: digits}
}

func pctOf(key, title, made, att string) teamCol {
	return teamCol{Key: key, Title: title, kind: tcRate, val: per(k(made), k(att)), digits: 1, pct: true}
}

func (c teamCol) lower() teamCol { c.low = true; return c }

func (c teamCol) pointsGames() teamCol { c.div = "PGP"; return c }

var teamTables = []teamTable{
	{ID: "offense", Name: "Offense", SortBy: 2, Cols: []teamCol{
		{Key: "GP", Title: "Games Played", kind: tcPlain, val: sum("WINS", "LOSSES", "TIES")},
		cnt("PTS", "Points", k("PTS")).pointsGames(),
		cnt("YDS", "Total Yards", k("OFFYARDS")),
		rate("Y/P", "Yards Per Play", per(k("OFFYARDS"), sum("PASSATTEMPTS", "RUSHATTEMPTS")), 1),
		cnt("PASS", "Passing Yards", k("OFFPASSYARDS")),
		cnt("RUSH", "Rushing Yards", k("OFFRUSHYARDS")),
		cnt("1ST", "First Downs", k("FIRSTDOWNS")),
		pctOf("3RD%", "Third Down Conversion %", "THIRDDOWNCONV", "THIRDDOWNS"),
		pctOf("4TH%", "Fourth Down Conversion %", "FOURTHDOWNCONV", "FOURTHDOWNS"),
		pctOf("RZ%", "Red Zone TD %", "OFFREDZONETDS", "OFFREDZONES"),
		{Key: "TOP", Title: "Time of Possession", kind: tcTime, val: k("POSSESSIONTIME")},
	}},
	{ID: "passing", Name: "Passing", SortBy: 3, Cols: []teamCol{
		cnt("CMP", "Completions", k("PASSCOMPLETIONS")),
		cnt("ATT", "Pass Attempts", k("PASSATTEMPTS")),
		pctOf("CMP%", "Completion %", "PASSCOMPLETIONS", "PASSATTEMPTS"),
		cnt("YDS", "Passing Yards", k("OFFPASSYARDS")),
		rate("AVG", "Yards Per Attempt", per(k("OFFPASSYARDS"), k("PASSATTEMPTS")), 1),
		cnt("TD", "Passing Touchdowns", k("PASSTDS")),
		cnt("INT", "Interceptions Thrown", k("PASSINTS")).lower(),
		cnt("SACK", "Sacks Allowed", k("SACKSALLOWED")).lower(),
	}},
	{ID: "rushing", Name: "Rushing", SortBy: 1, Cols: []teamCol{
		cnt("ATT", "Rushing Attempts", k("RUSHATTEMPTS")),
		cnt("YDS", "Rushing Yards", k("OFFRUSHYARDS")),
		rate("AVG", "Yards Per Carry", per(k("OFFRUSHYARDS"), k("RUSHATTEMPTS")), 1),
		cnt("TD", "Rushing Touchdowns", k("RUSHTDS")),
	}},
	{ID: "defense", Name: "Defense", SortBy: 1, Cols: []teamCol{
		cnt("PTS", "Points Allowed", k("PTSA")).pointsGames().lower(),
		cnt("YDS", "Total Yards Allowed", sum("DEFPASSYARDS", "DEFRUSHYARDS")).lower(),
		cnt("PASS", "Passing Yards Allowed", k("DEFPASSYARDS")).lower(),
		cnt("RUSH", "Rushing Yards Allowed", k("DEFRUSHYARDS")).lower(),
		cnt("SACK", "Sacks", k("SACKS")),
		cnt("INT", "Interceptions", k("DEFINTS")),
		cnt("FR", "Fumbles Recovered", k("FUMBLEREC")),
		cnt("PD", "Passes Defended", k("PASSDEFLECTIONS")),
		pctOf("RZ%", "Opponent Red Zone TD %", "DEFREDZONETDS", "DEFREDZONES").lower(),
	}},
	{ID: "turnovers", Name: "Turnovers", SortBy: 2, Cols: []teamCol{
		cnt("TAKE", "Takeaways", k("TAKEAWAYS")),
		cnt("GIVE", "Giveaways", k("GIVEAWAYS")).lower(),
		cnt("DIFF", "Turnover Margin", func(s S) float64 { return float64(s["TAKEAWAYS"] - s["GIVEAWAYS"]) }),
		cnt("INT", "Interceptions", k("DEFINTS")),
		cnt("FR", "Fumbles Recovered", k("FUMBLEREC")),
		cnt("INT THR", "Interceptions Thrown", k("PASSINTS")).lower(),
		cnt("FUM LOST", "Fumbles Lost", k("FUMBLESLOST")).lower(),
	}},
	{ID: "special", Name: "Special Teams", SortBy: 0, Cols: []teamCol{
		cnt("KR YDS", "Kick Return Yards", k("KICKRETURNYARDS")),
		cnt("PR YDS", "Punt Return Yards", k("PUNTRETURNYARDS")),
		cnt("PUNTS", "Punts", k("PUNTS")).lower(),
		cnt("PUNT YDS", "Punt Yards", k("PUNTYARDS")),
		rate("AVG", "Yards Per Punt", per(k("PUNTYARDS"), k("PUNTS")), 1),
	}},
	{ID: "penalties", Name: "Penalties", SortBy: 0, Cols: []teamCol{
		cnt("PEN", "Penalties", k("PENALTIES")).lower(),
		cnt("YDS", "Penalty Yards", k("PENALTYYARDS")).lower(),
		rate("AVG", "Yards Per Penalty", per(k("PENALTYYARDS"), k("PENALTIES")), 1).lower(),
	}},
}

// num is the column's value in the given mode; it is what rows are ranked on.
func (c teamCol) num(s S, perGame bool) float64 {
	v := c.val(s)
	if !perGame || c.kind == tcRate || c.kind == tcPlain {
		return v
	}
	div := "GP"
	if c.div != "" {
		div = c.div
	}
	if g := s[div]; g > 0 {
		return v / float64(g)
	}
	return 0
}

func (c teamCol) format(v float64, perGame bool) string {
	switch {
	case c.kind == tcTime:
		return clock(int64(v + 0.5))
	case c.kind == tcRate && c.pct:
		return fmt.Sprintf("%.*f", c.digits, v*100)
	case c.kind == tcRate:
		return fmt.Sprintf("%.*f", c.digits, v)
	case perGame && c.kind == tcCount:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%d", int64(v))
	}
}

// teamLine is one FBS team's season TeamStats plus the synthetic keys GP
// (games in the stats row), PTS / PTSA (points for / against) and PGP (games
// those points came from).
type teamLine struct {
	t *store.Team
	s S
}

// teamStatLines lists FBS teams with recorded totals, optionally one conference.
func teamStatLines(teams map[int]*store.Team, points map[int]store.TeamPoints, conf string) []teamLine {
	var list []teamLine
	for _, t := range teams {
		if !t.HasSeason || t.Conference == "" || len(t.Stats) == 0 || (conf != "" && t.Conference != conf) {
			continue
		}
		st := S{}
		for key, v := range t.Stats {
			st[key] = v
		}
		st["GP"] = st["WINS"] + st["LOSSES"] + st["TIES"]
		p := points[t.ID]
		st["PTS"], st["PTSA"], st["PGP"] = int64(p.For), int64(p.Against), int64(p.Games)
		list = append(list, teamLine{t, st})
	}
	return list
}

// teamColumn finds a comparison column by table and key.
func teamColumn(table, key string) teamCol {
	for _, tb := range teamTables {
		if tb.ID != table {
			continue
		}
		for _, c := range tb.Cols {
			if c.Key == key {
				return c
			}
		}
	}
	panic("unknown team column " + table + "/" + key)
}

// rankIn is a team's per-game rank on col among list (ties share a rank).
func rankIn(list []teamLine, col teamCol, team int) int {
	var mine float64
	found := false
	for _, e := range list {
		if e.t.ID == team {
			mine, found = col.num(e.s, true), true
		}
	}
	if !found {
		return 0
	}
	rank := 1
	for _, e := range list {
		v := col.num(e.s, true)
		if (col.low && v < mine) || (!col.low && v > mine) {
			rank++
		}
	}
	return rank
}

// TeamCompareRow is one ranked team.
type TeamCompareRow struct {
	Rank  int
	Team  *store.Team
	Cells []string
	sortV float64
}

func (s *Site) teamCompare(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	q := r.URL.Query()
	season := seasonParam(r, c)
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	points, err := s.q.SeasonPoints(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	tb := &teamTables[0]
	for i := range teamTables {
		if teamTables[i].ID == q.Get("cat") {
			tb = &teamTables[i]
		}
	}
	conf := q.Get("conf")
	perGame := q.Get("mode") != "total"
	sortCol := atoi(q.Get("sort"), tb.SortBy)
	if sortCol < 0 || sortCol >= len(tb.Cols) {
		sortCol = tb.SortBy
	}

	cols, rows, sortCol := rankTeams(tb, teamStatLines(teams, points, conf), perGame, sortCol)

	mode := "pg"
	if !perGame {
		mode = "total"
	}
	title := "FBS"
	if conf != "" {
		title = conf
	}
	return "teamcompare", data{
		"Table": tb, "Cols": cols, "Rows": rows, "Sort": sortCol, "Cat": tb.ID, "Tables": teamTables,
		"Season": season, "Conf": conf, "Confs": conferences(teams), "Mode": mode, "PerGame": perGame,
		"Scope": title,
	}, nil
}

// rankTeams ranks teams on one comparison table's sortCol, returning the
// columns actually shown and the index of the sort column among them.
func rankTeams(tb *teamTable, list []teamLine, perGame bool, sortCol int) ([]teamCol, []TeamCompareRow, int) {
	// Drop columns the save never fills (all zero), so a dead game field
	// doesn't show up as a column of zeros.
	cols := make([]teamCol, 0, len(tb.Cols))
	sortKey := tb.Cols[sortCol].Key
	for _, col := range tb.Cols {
		for _, e := range list {
			if col.val(e.s) != 0 {
				cols = append(cols, col)
				break
			}
		}
	}
	sortCol = 0
	for i, col := range cols {
		if col.Key == sortKey {
			sortCol = i
		}
	}

	rows := make([]TeamCompareRow, 0, len(list))
	for _, e := range list {
		row := TeamCompareRow{Team: e.t}
		for i, col := range cols {
			v := col.num(e.s, perGame)
			row.Cells = append(row.Cells, col.format(v, perGame))
			if i == sortCol {
				row.sortV = v
			}
		}
		rows = append(rows, row)
	}
	var sc teamCol
	if len(cols) > 0 {
		sc = cols[sortCol]
	}
	better := func(a, b float64) bool {
		if sc.low {
			return a < b
		}
		return a > b
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].sortV != rows[j].sortV {
			return better(rows[i].sortV, rows[j].sortV)
		}
		return rows[i].Team.Name < rows[j].Team.Name
	})
	// Ties share a rank (1, 2, 2, 4) and are compared on the shown value.
	for i := range rows {
		rows[i].Rank = i + 1
		if i > 0 && len(cols) > 0 && rows[i].Cells[sortCol] == rows[i-1].Cells[sortCol] {
			rows[i].Rank = rows[i-1].Rank
		}
	}
	return cols, rows, sortCol
}
