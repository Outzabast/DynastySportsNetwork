package web

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// seasonResults loads a season's results and the previous season's (the
// predictor's prior) in parallel.
func (s *Site) seasonResults(ctx context.Context, c *store.Ctx, season int) (cur, last []store.Result, err error) {
	err = parallel(
		func() (err error) { cur, err = s.q.SeasonResults(ctx, c, season); return },
		func() (err error) {
			if season > 0 {
				last, err = s.q.SeasonResults(ctx, c, season-1)
			}
			return
		},
	)
	return
}

// prediction fits the predictor on every result before the game's week, so a
// played game shows the line it carried into kickoff.
func prediction(cur, last []store.Result, g *store.Game, home, away *store.Team) (*Prediction, *Line) {
	var prior *ratings
	if len(last) > 0 {
		prior = fitRatings(last, 1<<30, nil)
	}
	neutral := false
	for _, r := range cur {
		if r.Home == g.Home && r.Away == g.Away && r.Week == g.Week {
			neutral = r.Neutral
		}
	}
	p := predict(fitRatings(cur, g.Week, prior), g.Home, g.Away, neutral, home.Rank(), away.Rank())
	return p, p.line(g.Home, g.Away)
}

// LeaderSide is one team's leader in a preview category.
type LeaderSide struct {
	Player store.PlayerRef
	Value  string
	Detail string
}

// PreviewLeader pairs both teams' season leaders in one category.
type PreviewLeader struct {
	Name       string
	Away, Home *LeaderSide
}

var previewLeaderCats = []struct {
	name, table string
	col         int
	detail      func(S) string
}{
	{"Passing Yards", "passing", 4, func(s S) string {
		return fmt.Sprintf("%d/%d, %d TD, %d INT", s["PASSCOMPLETED"], s["PASSATTEMPTS"], s["PASSTDS"], s["PASSINTS"])
	}},
	{"Rushing Yards", "rushing", 2, func(s S) string { return fmt.Sprintf("%d CAR, %d TD", s["RUSHATTEMPTS"], s["RUSHTDS"]) }},
	{"Receiving Yards", "receiving", 2, func(s S) string { return fmt.Sprintf("%d REC, %d TD", s["RECEIVECATCHES"], s["RECEIVETDS"]) }},
	{"Sacks", "defense", 4, nil},
	{"Tackles", "defense", 1, nil},
}

// teamLeader is the team's top player on one season-table column.
func teamLeader(lines []*store.SeasonLine, table string, col int, detail func(S) string) *LeaderSide {
	t := tableFor(seasonTables, table)
	merged := map[uuid.UUID]S{}
	refs := map[uuid.UUID]store.PlayerRef{}
	for _, l := range lines {
		if !t.groupOK(l.Group) {
			continue
		}
		if merged[l.Player.ID] == nil {
			merged[l.Player.ID], refs[l.Player.ID] = S{}, l.Player
		}
		merged[l.Player.ID].Add(l.Stats)
	}
	var best *LeaderSide
	bestV := 0.0
	for id, st := range merged {
		if !t.Has(st) {
			continue
		}
		v := t.Cols[col].Num(st)
		if v > bestV || (v == bestV && best != nil && refs[id].Name() < best.Player.Name()) {
			best, bestV = &LeaderSide{Player: refs[id], Value: t.Cols[col].Val(st)}, v
			if detail != nil {
				best.Detail = detail(st)
			}
		}
	}
	return best
}

func matchupLeaders(away, home []*store.SeasonLine) []PreviewLeader {
	var out []PreviewLeader
	for _, c := range previewLeaderCats {
		pl := PreviewLeader{Name: c.name, Away: teamLeader(away, c.table, c.col, c.detail), Home: teamLeader(home, c.table, c.col, c.detail)}
		if pl.Away != nil || pl.Home != nil {
			out = append(out, pl)
		}
	}
	return out
}

// PreviewStat is one per-game team stat with each side's FBS rank.
type PreviewStat struct {
	Label              string
	Away, Home         string
	AwayRank, HomeRank int
}

var previewStatCols = []struct {
	label, table, key string
	season            bool // show the season total (ranks stay per game)
}{
	{"Points Per Game", "offense", "PTS", false},
	{"Total Yards", "offense", "YDS", false},
	{"Passing Yards", "offense", "PASS", false},
	{"Rushing Yards", "offense", "RUSH", false},
	{"Points Allowed", "defense", "PTS", false},
	{"Yards Allowed", "defense", "YDS", false},
	{"3rd Down %", "offense", "3RD%", false},
	{"Turnover Margin", "turnovers", "DIFF", true},
}

func matchupStats(list []teamLine, away, home int) []PreviewStat {
	var a, h S
	for _, e := range list {
		switch e.t.ID {
		case away:
			a = e.s
		case home:
			h = e.s
		}
	}
	if a == nil || h == nil {
		return nil
	}
	var out []PreviewStat
	for _, c := range previewStatCols {
		col := teamColumn(c.table, c.key)
		val := func(st S) string {
			if c.season {
				v := col.num(st, false)
				if v > 0 {
					return "+" + col.format(v, false)
				}
				return col.format(v, false)
			}
			return col.format(col.num(st, true), true)
		}
		label := c.label
		if c.season {
			label += " (Season)"
		}
		out = append(out, PreviewStat{Label: label, Away: val(a), Home: val(h),
			AwayRank: rankIn(list, col, away), HomeRank: rankIn(list, col, home)})
	}
	return out
}

// predictorColors picks the two donut colors, falling back to the home
// team's second color when both primaries look alike (red vs red).
func predictorColors(away, home *store.Team) (string, string) {
	a, h := away.Color, home.Color
	if colorDist(a, h) < 90 && home.AltColor != "" && colorDist(a, home.AltColor) > colorDist(a, h) {
		h = home.AltColor
	}
	return a, h
}

func colorDist(x, y string) float64 {
	rgb := func(s string) (r, g, b float64, ok bool) {
		s = strings.TrimPrefix(s, "#")
		if len(s) != 6 {
			return 0, 0, 0, false
		}
		v, err := strconv.ParseUint(s, 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), true
	}
	r1, g1, b1, ok1 := rgb(x)
	r2, g2, b2, ok2 := rgb(y)
	if !ok1 || !ok2 {
		return 255
	}
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return math.Sqrt(dr*dr + dg*dg + db*db)
}

// previewStandings returns the conference tables of the two teams.
func previewStandings(all []Standing, away, home *store.Team) []Standing {
	var out []Standing
	for _, st := range all {
		if st.Conference != "" && (st.Conference == away.Conference || st.Conference == home.Conference) {
			out = append(out, st)
		}
	}
	return out
}

// preview gathers the pregame page: predictor, line, leaders, team stats and
// standings.
func (s *Site) preview(ctx context.Context, c *store.Ctx, g *store.Game, teams map[int]*store.Team, away, home *store.Team) (data, error) {
	var (
		awayLines []*store.SeasonLine
		homeLines []*store.SeasonLine
		points    map[int]store.TeamPoints
		stand     []Standing
	)
	err := parallel(
		func() (err error) { awayLines, err = s.q.SeasonLines(ctx, c, g.Season, uuid.Nil, g.Away); return },
		func() (err error) { homeLines, err = s.q.SeasonLines(ctx, c, g.Season, uuid.Nil, g.Home); return },
		func() (err error) { points, err = s.q.SeasonPoints(ctx, c, g.Season); return },
		func() (err error) { stand, err = s.standingsData(ctx, c, g.Season); return },
	)
	if err != nil {
		return nil, err
	}
	ac, hc := predictorColors(away, home)
	return data{
		"AwayColor": ac, "HomeColor": hc,
		"Leaders":   matchupLeaders(awayLines, homeLines),
		"Matchup":   matchupStats(teamStatLines(teams, points, ""), g.Away, g.Home),
		"Standings": previewStandings(stand, away, home),
	}, nil
}
