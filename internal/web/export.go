package web

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Exports: the tables the site shows, as Markdown (laid out for pasting into
// an LLM) or an Excel workbook (one sheet per section). Builders produce a
// format-neutral xdoc and the two writers render it, so both formats always
// carry the same data. Only what the pages show goes in (AGENTS.md rule 1).

type xkv struct{ Key, Val string }

type xdoc struct {
	Title  string
	File   string // download name without extension
	Meta   []xkv
	Sheets []*xsheet
}

// xsheet is one workbook sheet and one "##" section in Markdown.
type xsheet struct {
	Name    string // sheet tab (Excel limits it to 31 characters)
	Heading string // full section title; Name when empty
	Meta    []xkv
	Body    string // free text (a story's Markdown), written after Meta
	Tables  []*xtable
}

type xtable struct {
	Title   string
	Headers []string
	Rows    [][]string
	Total   []string
	Legend  string // column key, written once per document in Markdown
}

func (sh *xsheet) heading() string {
	if sh.Heading != "" {
		return sh.Heading
	}
	return sh.Name
}

const exportSource = "Dynasty Sports Network, an EA Sports College Football 27 dynasty. Every team, player and game comes from the dynasty save, not real life."

type exportFunc func(r *http.Request, c *store.Ctx) (*xdoc, error)

// export serves a builder as ?format=xlsx or Markdown (the default).
func (s *Site) export(f exportFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.context(r)
		if err == nil {
			r = r.WithContext(withWeekCache(r.Context(), c))
		}
		var doc *xdoc
		if err == nil {
			doc, err = f(r, c)
		}
		if errors.Is(err, store.ErrNoData) || errors.Is(err, errNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		var buf bytes.Buffer
		ext, ctype := "md", "text/markdown; charset=utf-8"
		if r.URL.Query().Get("format") == "xlsx" {
			ext, ctype = "xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
			err = writeXLSX(&buf, doc)
		} else {
			writeMarkdown(&buf, doc)
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, doc.File, ext))
		w.Write(buf.Bytes())
	}
}

// ---- Builders ----

func (s *Site) exportTeamStats(r *http.Request, c *store.Ctx) (*xdoc, error) {
	ctx := r.Context()
	season, id := seasonParam(r, c), atoi(r.PathValue("id"), -1)
	var (
		teams  map[int]*store.Team
		lines  []*store.SeasonLine
		points map[int]store.TeamPoints
	)
	err := parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { lines, err = s.q.SeasonLines(ctx, c, season, uuid.Nil, id); return },
		func() (err error) { points, err = s.q.SeasonPoints(ctx, c, season); return },
	)
	if err != nil {
		return nil, err
	}
	t, ok := teams[id]
	if !ok {
		return nil, errNotFound
	}
	year := c.YearOf(season)
	return &xdoc{
		Title: fmt.Sprintf("%d %s %s Team and Player Stats", year, t.Long, t.Nick),
		File:  fmt.Sprintf("%d-%s-stats", year, slug(t.Name)),
		Meta:  teamMeta(c, t, season),
		Sheets: []*xsheet{
			{Name: "Team Stats", Tables: []*xtable{teamTotalsTable(t, points[id])}},
			playerStatsSheet(lines),
		},
	}, nil
}

func (s *Site) exportSeason(r *http.Request, c *store.Ctx) (*xdoc, error) {
	ctx := r.Context()
	season, id := seasonParam(r, c), atoi(r.PathValue("id"), -1)
	var (
		teams  map[int]*store.Team
		games  []*store.Game
		lines  []*store.SeasonLine
		points map[int]store.TeamPoints
	)
	err := parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { games, err = s.q.TeamGames(ctx, c, season, id); return },
		func() (err error) { lines, err = s.q.SeasonLines(ctx, c, season, uuid.Nil, id); return },
		func() (err error) { points, err = s.q.SeasonPoints(ctx, c, season); return },
	)
	if err != nil {
		return nil, err
	}
	t, ok := teams[id]
	if !ok {
		return nil, errNotFound
	}
	var played []*store.Game
	var rows []int
	for _, g := range games {
		if g.Played {
			played = append(played, g)
			rows = append(rows, g.Row)
		}
	}
	gameLines, err := s.q.GamesLines(ctx, c, season, rows)
	if err != nil {
		return nil, err
	}
	plays := make([][]*store.ScoringPlay, len(played))
	fetch := make([]func() error, len(played))
	for i, g := range played {
		fetch[i] = func() (err error) { plays[i], err = s.q.ScoringPlays(ctx, c, season, g.Row); return }
	}
	if err := parallel(fetch...); err != nil {
		return nil, err
	}

	year := c.YearOf(season)
	d := &xdoc{
		Title: fmt.Sprintf("%d %s %s Season: Box Scores and Stats", year, t.Long, t.Nick),
		File:  fmt.Sprintf("%d-%s-season", year, slug(t.Name)),
		Meta:  teamMeta(c, t, season),
		Sheets: []*xsheet{
			{Name: "Schedule", Tables: []*xtable{scheduleTable(games, teams, t.ID, year)}},
			{Name: "Team Stats", Tables: []*xtable{teamTotalsTable(t, points[id])}},
			playerStatsSheet(lines),
		},
	}
	for i, g := range played {
		d.Sheets = append(d.Sheets, boxSheet(g, teams, gameLines[g.Row], plays[i], t.ID, year))
	}
	return d, nil
}

func (s *Site) exportTeamCompare(r *http.Request, c *store.Ctx) (*xdoc, error) {
	ctx := r.Context()
	q := r.URL.Query()
	season, conf, perGame := seasonParam(r, c), q.Get("conf"), q.Get("mode") != "total"
	var (
		teams  map[int]*store.Team
		points map[int]store.TeamPoints
	)
	err := parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { points, err = s.q.SeasonPoints(ctx, c, season); return },
	)
	if err != nil {
		return nil, err
	}
	list := teamStatLines(teams, points, conf)
	scope, mode, modeFile := "FBS", "Per game", "per-game"
	if conf != "" {
		scope = conf
	}
	if !perGame {
		mode, modeFile = "Season totals", "totals"
	}
	year := c.YearOf(season)
	d := &xdoc{
		Title: fmt.Sprintf("%d %s Team Stats Comparison", year, scope),
		File:  fmt.Sprintf("%d-%s-team-stats-%s", year, slug(scope), modeFile),
		Meta: []xkv{{"Source", exportSource}, {"Dynasty", c.Name}, {"Season", fmt.Sprint(year)}, {"Teams", scope},
			{"Values", mode + " (percentages and averages are the same in both modes)"}, {"Data through", dataThrough(c, season)}},
	}
	for i := range teamTables {
		tb := &teamTables[i]
		cols, rows, sortCol := rankTeams(tb, list, perGame, tb.SortBy)
		sh := &xsheet{Name: tb.Name}
		if len(cols) > 0 {
			sh.Heading = fmt.Sprintf("%s (ranked by %s)", tb.Name, cols[sortCol].Title)
		}
		xt := &xtable{Headers: []string{"RK", "Team", "Conference"}}
		var legend []string
		for _, col := range cols {
			xt.Headers = append(xt.Headers, col.Key)
			legend = append(legend, col.Key+" = "+col.Title)
		}
		xt.Legend = strings.Join(legend, ", ")
		for _, row := range rows {
			xt.Rows = append(xt.Rows, append([]string{fmt.Sprint(row.Rank), row.Team.Name, row.Team.Conference}, row.Cells...))
		}
		sh.Tables = []*xtable{xt}
		d.Sheets = append(d.Sheets, sh)
	}
	return d, nil
}

func (s *Site) exportPlayer(r *http.Request, c *store.Ctx) (*xdoc, error) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, errNotFound
	}
	var (
		bio    *store.Bio
		teams  map[int]*store.Team
		lines  []*store.SeasonLine
		awards []*store.Award
		events []*store.Event
	)
	err = parallel(
		func() (err error) { bio, err = s.q.Person(ctx, c, id); return },
		func() (err error) { teams, err = s.q.Teams(ctx, c, c.Season); return },
		func() (err error) { lines, err = s.q.SeasonLines(ctx, c, -1, id, -1); return },
		func() (err error) { awards, err = s.q.Awards(ctx, c, id); return },
		func() (err error) { events, err = s.q.Events(ctx, c, id); return },
	)
	if err != nil {
		return nil, err
	}
	if bio == nil {
		return nil, errNotFound
	}

	// Game logs for every season the player has stats in. The team they
	// played for decides each game's opponent.
	playedFor := map[int]map[int]bool{}
	for _, l := range lines {
		if playedFor[l.Season] == nil {
			playedFor[l.Season] = map[int]bool{}
		}
		playedFor[l.Season][l.TeamID] = true
	}
	var seasons []int
	for sn := range playedFor {
		seasons = append(seasons, sn)
	}
	sort.Ints(seasons)
	logs := make([][]*store.GameLog, len(seasons))
	logTeams := make([]map[int]*store.Team, len(seasons))
	var fetch []func() error
	for i, sn := range seasons {
		fetch = append(fetch,
			func() (err error) { logs[i], err = s.q.PlayerGameLog(ctx, c, sn, id); return },
			func() (err error) { logTeams[i], err = s.q.Teams(ctx, c, sn); return })
	}
	if err := parallel(fetch...); err != nil {
		return nil, err
	}

	team := teamOr(teams, bio.TeamID)
	d := &xdoc{
		Title: bio.Name() + " Career Stats and Game Log",
		File:  slug(bio.Name()) + "-career",
		Meta:  []xkv{{"Source", exportSource}, {"Dynasty", c.Name}, {"Data through", dataThrough(c, c.Season)}},
	}

	profile := &xsheet{Name: "Profile", Heading: "Profile: " + bio.Name(), Meta: playerMeta(bio, team)}
	if len(awards) > 0 {
		xt := &xtable{Title: "Awards and Honors", Headers: []string{"When", "Award", "Team"}}
		for _, a := range awards {
			when := fmt.Sprint(c.YearOf(a.PeriodIndex))
			if a.Period != "Season" {
				when = fmt.Sprintf("Week %d", a.PeriodIndex)
			}
			xt.Rows = append(xt.Rows, []string{when, AwardName(a.Type), teamOr(teams, a.TeamID).Name})
		}
		profile.Tables = append(profile.Tables, xt)
	}
	if len(events) > 0 {
		xt := &xtable{Title: "Transactions", Headers: []string{"Season", "Event"}}
		for _, e := range events {
			xt.Rows = append(xt.Rows, []string{fmt.Sprint(c.YearOf(e.Season)), eventText(e)})
		}
		profile.Tables = append(profile.Tables, xt)
	}
	d.Sheets = append(d.Sheets, profile)

	career := &xsheet{Name: "Career Stats", Heading: "Career Stats by Season"}
	for _, rt := range careerTables(lines, teams, c) {
		career.Tables = append(career.Tables, fromRendered(rt, rt.Name, "Season", "Team"))
	}
	d.Sheets = append(d.Sheets, career)

	glog := &xsheet{Name: "Game Log", Heading: "Game Log (every game played)"}
	var noLog []string
	for i, sn := range seasons {
		if len(logs[i]) == 0 {
			noLog = append(noLog, fmt.Sprint(c.YearOf(sn)))
		}
	}
	if len(noLog) > 0 {
		glog.Meta = []xkv{{"Note", "No game-by-game stats were recorded for " + strings.Join(noLog, ", ") +
			"; totals here cover only the games listed. See Career Stats for full season totals."}}
	}
	for _, tb := range boxTables {
		xt := &xtable{Title: tb.Name, Headers: []string{"Season", "Date", "Week", "Opponent", "Result"}, Legend: colLegend(tb.Cols)}
		for _, col := range tb.Cols {
			xt.Headers = append(xt.Headers, col.Key)
		}
		total := store.Stats{}
		for i, sn := range seasons {
			year := c.YearOf(sn)
			byGame := map[int]store.Stats{}
			var order []*store.Game
			for _, l := range logs[i] {
				if !tb.groupOK(l.Group) {
					continue
				}
				if _, ok := byGame[l.Game.Row]; !ok {
					byGame[l.Game.Row] = store.Stats{}
					order = append(order, l.Game)
				}
				byGame[l.Game.Row].Add(l.Stats)
			}
			for _, g := range order {
				st := byGame[g.Row]
				if !tb.Has(st) {
					continue
				}
				total.Add(st)
				us := bio.TeamID
				switch {
				case playedFor[sn][g.Home]:
					us = g.Home
				case playedFor[sn][g.Away]:
					us = g.Away
				}
				row := []string{fmt.Sprint(year), dateLabel(g, year), weekLabel(g.WeekType, g.Week), opponent(g, logTeams[i], us), gameResult(g, us)}
				for _, col := range tb.Cols {
					row = append(row, col.Val(st))
				}
				xt.Rows = append(xt.Rows, row)
			}
		}
		if len(xt.Rows) == 0 {
			continue
		}
		xt.Total = []string{"Total", "", "", "", ""}
		for _, col := range tb.Cols {
			xt.Total = append(xt.Total, col.Val(total))
		}
		glog.Tables = append(glog.Tables, xt)
	}
	d.Sheets = append(d.Sheets, glog)
	return d, nil
}

// exportGame is everything on the game page: the full box score for a played
// game, the preview (predictor, odds, leaders, matchup, standings) for one not
// yet played, and the text of every preview and recap story on the game.
func (s *Site) exportGame(r *http.Request, c *store.Ctx) (*xdoc, error) {
	d, err := s.gameData(r, c)
	if err != nil {
		return nil, err
	}
	g, away, home := d["G"].(*store.Game), d["Away"].(*store.Team), d["Home"].(*store.Team)
	teams, year := d["Teams"].(map[int]*store.Team), d["Year"].(int)
	line, _ := d["Line"].(*Line)

	kind, kindFile := "Box Score", "box-score"
	if !g.Played {
		kind, kindFile = "Game Preview", "preview"
	}
	doc := &xdoc{
		Title: fmt.Sprintf("%d %s vs. %s: %s", year, fullName(away), fullName(home), kind),
		File:  fmt.Sprintf("%d-%s-vs-%s-%s", year, slug(away.Name), slug(home.Name), kindFile),
		Meta:  gameMeta(c, g, away, home, line, teams, year),
	}

	if g.Played {
		box := boxSheet(g, teams, d["Lines"].([]*store.Line), d["Plays"].([]*store.ScoringPlay), g.Home, year)
		box.Name, box.Meta = "Box Score", nil // the game details are in the document meta
		if len(d["Plays"].([]*store.ScoringPlay)) == 0 {
			box.Meta = []xkv{{"Scoring plays", "Not available. The game keeps a scoring summary only during the week a game is played, and this game was played before its week was captured."}}
		}
		doc.Sheets = append(doc.Sheets, box)
	} else {
		pred, _ := d["Pred"].(*Prediction)
		leaders, _ := d["Leaders"].([]PreviewLeader)
		matchup, _ := d["Matchup"].([]PreviewStat)
		stand, _ := d["Standings"].([]Standing)
		doc.Sheets = append(doc.Sheets, previewSheet(g, away, home, pred, line, leaders, matchup, stand, year))
	}

	stories, _ := d["Stories"].([]StoryCard)
	for _, st := range stories {
		doc.Sheets = append(doc.Sheets, storySheet(st.Article))
	}
	return doc, nil
}

// gameMeta is the game header and info bar: teams, score, date, venue,
// weather, coverage and the pregame line.
func gameMeta(c *store.Ctx, g *store.Game, away, home *store.Team, line *Line, teams map[int]*store.Team, year int) []xkv {
	side := func(t *store.Team) string {
		name := fullName(t)
		if t.ID < 0 {
			return name // FCS placeholder: no record kept
		}
		if rk := t.Rank(); rk > 0 {
			name = fmt.Sprintf("#%d %s", rk, name)
		}
		rec := t.Record()
		if t.Conference != "" {
			rec += ", " + t.ConfRecord() + " " + t.Conference
		}
		return fmt.Sprintf("%s (%s)", name, rec)
	}
	m := []xkv{{"Source", exportSource}, {"Dynasty", c.Name}, {"Season", fmt.Sprint(year)}, {"Week", weekLabel(g.WeekType, g.Week)}}
	if l := g.TypeLabel(); l != "" {
		m = append(m, xkv{"Game", l})
	}
	if g.BowlName != "" {
		m = append(m, xkv{"Bowl", g.BowlName})
	}
	m = append(m, xkv{"Away", side(away)}, xkv{"Home", side(home)})
	if g.Played {
		final := "Final"
		if g.Overtime {
			final = "Final (OT)"
		}
		m = append(m, xkv{"Result", fmt.Sprintf("%s: %s %d, %s %d", final, away.Name, g.AwayScore, home.Name, g.HomeScore)})
	}
	date := dateLabel(g, year)
	if !g.Played {
		date += ", " + gameStatus(g)
	}
	m = append(m, xkv{"Date", date})
	if g.Stadium != "" {
		m = append(m, xkv{"Stadium", g.Stadium})
	}
	if g.Attendance > 0 {
		m = append(m, xkv{"Attendance", fmt.Sprint(g.Attendance)})
	}
	weather := fmt.Sprintf("%d°F, %s", g.Temperature, prettyEnum(g.Weather))
	if g.Wind != "" {
		weather += fmt.Sprintf(", wind %s (%d mph)", prettyEnum(g.Wind), g.WindSpeed)
	}
	m = append(m, xkv{"Weather", weather})
	if g.Network != "" {
		m = append(m, xkv{"Coverage", g.Network})
	}
	if line != nil {
		label := "Line"
		if g.Played {
			label = "Pregame line"
		}
		fav := "Pick'em"
		if line.Favorite != -2 {
			fav = teamOr(teams, line.Favorite).Short + " " + line.FavSpread
		}
		m = append(m, xkv{label, fmt.Sprintf("%s, O/U %s (DSN line, set from this season's scores)", fav, line.Total)})
	}
	return append(m, xkv{"Data through", dataThrough(c, g.Season)})
}

// fullName is "Texas State Bobcats" (FCS placeholders have no nickname).
func fullName(t *store.Team) string {
	return strings.TrimSpace(t.Long + " " + t.Nick)
}

// previewSheet is the pregame page for a game not yet played.
func previewSheet(g *store.Game, away, home *store.Team, pred *Prediction, line *Line, leaders []PreviewLeader, matchup []PreviewStat, stand []Standing, year int) *xsheet {
	sh := &xsheet{Name: "Matchup", Heading: fmt.Sprintf("Matchup: %s at %s", away.Name, home.Name)}
	if pred != nil {
		basis := "Based on last season's results"
		if pred.Games > 0 {
			basis = fmt.Sprintf("Based on %d games played this season", pred.Games)
		}
		if g.Week > 0 {
			basis += fmt.Sprintf(", before week %d", g.Week)
		}
		sh.Meta = append(sh.Meta, xkv{"Matchup predictor", basis + "."})
		sh.Tables = append(sh.Tables, &xtable{Title: "Matchup Predictor", Headers: []string{"Team", "Win %", "Projected Points"}, Rows: [][]string{
			{away.Name, fmt.Sprintf("%.1f", pred.AwayPct), fmt.Sprintf("%.0f", pred.AwayPts)},
			{home.Name, fmt.Sprintf("%.1f", pred.HomePct), fmt.Sprintf("%.0f", pred.HomePts)},
		}})
	}
	if line != nil {
		sh.Tables = append(sh.Tables, &xtable{Title: "Game Odds", Headers: []string{"Team", "Record", "Spread", "Total", "Moneyline"}, Rows: [][]string{
			{away.Name, away.Record(), line.AwaySpread + " (-110)", "o" + line.Total + " (-110)", line.AwayML},
			{home.Name, home.Record(), line.HomeSpread + " (-110)", "u" + line.Total + " (-110)", line.HomeML},
		}})
	}
	if len(leaders) > 0 {
		xt := &xtable{Title: "Season Leaders", Headers: []string{"Category", away.Short + " Leader", away.Short, home.Short + " Leader", home.Short}}
		who := func(l *LeaderSide) (string, string) {
			if l == nil {
				return "-", "-"
			}
			name := fmt.Sprintf("%s (%s)", l.Player.Name(), l.Player.Position)
			if l.Detail != "" {
				name += ": " + l.Detail
			}
			return name, l.Value
		}
		for _, pl := range leaders {
			an, av := who(pl.Away)
			hn, hv := who(pl.Home)
			xt.Rows = append(xt.Rows, []string{pl.Name, an, av, hn, hv})
		}
		sh.Tables = append(sh.Tables, xt)
	}
	if len(matchup) > 0 {
		xt := &xtable{Title: "Team Stats (per game; ranks among all FBS teams)", Headers: []string{"Stat", away.Short, away.Short + " Rank", home.Short, home.Short + " Rank"}}
		for _, ps := range matchup {
			xt.Rows = append(xt.Rows, []string{ps.Label, ps.Away, fmt.Sprint(ps.AwayRank), ps.Home, fmt.Sprint(ps.HomeRank)})
		}
		sh.Tables = append(sh.Tables, xt)
	}
	for _, st := range stand {
		for _, dv := range st.Divisions {
			title := fmt.Sprintf("%d %s Standings", year, st.Conference)
			if dv.Name != "" {
				title += ": " + dv.Name
			}
			xt := &xtable{Title: title, Headers: []string{"Team", "Conf", "Overall"}}
			for _, t := range dv.Teams {
				xt.Rows = append(xt.Rows, []string{t.Name, t.ConfRecord(), t.Record()})
			}
			sh.Tables = append(sh.Tables, xt)
		}
	}
	return sh
}

// storySheet is one preview or recap story, in full.
func storySheet(a *store.Article) *xsheet {
	label := a.Label()
	if label == "" {
		label = "Story"
	}
	sh := &xsheet{Name: label, Heading: label + ": " + a.Headline, Body: a.Body}
	if a.Dek != "" {
		sh.Meta = append(sh.Meta, xkv{"Summary", a.Dek})
	}
	if a.Byline != "" {
		sh.Meta = append(sh.Meta, xkv{"By", a.Byline})
	}
	if a.Published {
		sh.Meta = append(sh.Meta, xkv{"Published", a.When().Format("Jan 2, 2006")})
	} else {
		sh.Meta = append(sh.Meta, xkv{"Status", "Draft (not published)"})
	}
	if a.ImageCaption != "" {
		sh.Meta = append(sh.Meta, xkv{"Photo caption", a.ImageCaption})
	}
	return sh
}

// ---- Shared pieces ----

func dataThrough(c *store.Ctx, season int) string {
	if season != c.Season {
		return "Season complete"
	}
	if c.WeekType == "RegularSeason" || strings.HasPrefix(c.WeekType, "BowlSeason") {
		return fmt.Sprintf("%d %s", c.Year, weekLabel(c.WeekType, c.Week))
	}
	return fmt.Sprintf("%d %s", c.Year, prettyEnum(c.WeekType))
}

func teamMeta(c *store.Ctx, t *store.Team, season int) []xkv {
	m := []xkv{{"Source", exportSource}, {"Dynasty", c.Name}, {"Season", fmt.Sprint(c.YearOf(season))},
		{"Team", t.Long + " " + t.Nick}, {"Record", t.Record()}}
	if t.Conference != "" {
		m = append(m, xkv{"Conference", fmt.Sprintf("%s (%s)", t.Conference, t.ConfRecord())})
	}
	if rk := t.Rank(); rk > 0 {
		m = append(m, xkv{"Ranking", fmt.Sprintf("#%d", rk)})
	}
	return append(m, xkv{"Data through", dataThrough(c, season)})
}

func playerMeta(b *store.Bio, team *store.Team) []xkv {
	m := []xkv{{"Name", b.Name()}, {"Position", positionName(b.Position)}}
	if b.Jersey > 0 {
		m = append(m, xkv{"Jersey", fmt.Sprintf("#%d", b.Jersey)})
	}
	if team.ID >= 0 {
		label := "Team"
		if !b.OnRoster {
			label = "Last team"
		}
		m = append(m, xkv{label, team.Long + " " + team.Nick})
	}
	if b.ClassYear != "" {
		m = append(m, xkv{"Class", classYear(b.ClassYear, b.Redshirt)})
	}
	if b.Height > 0 {
		m = append(m, xkv{"Height", height(b.Height)})
	}
	if b.Weight > 0 {
		m = append(m, xkv{"Weight", fmt.Sprintf("%d lbs", b.Weight)})
	}
	if b.HomeTown != "" {
		m = append(m, xkv{"Hometown", b.HomeTown + ", " + b.HomeState})
	}
	if b.Stars > 0 {
		m = append(m, xkv{"Recruiting", fmt.Sprintf("%d-star recruit", b.Stars)})
	}
	if b.Captain {
		m = append(m, xkv{"Role", "Team captain"})
	}
	if !b.OnRoster {
		m = append(m, xkv{"Status", "Not on a current roster"})
	}
	return m
}

func eventText(e *store.Event) string {
	switch e.Kind {
	case "position_change":
		return fmt.Sprintf("Moved from %v to %v", e.Detail["from"], e.Detail["to"])
	case "leaving":
		return "Leaving program: " + prettyEnum(strings.TrimPrefix(fmt.Sprint(e.Detail["type"]), "Transfer_"))
	}
	return prettyEnum(e.Kind)
}

// teamTotalsTable is the team stats summary, with points added on top.
func teamTotalsTable(t *store.Team, p store.TeamPoints) *xtable {
	xt := &xtable{Title: "Team Totals", Headers: []string{"Stat", "Total", "Per Game"}}
	perGame := func(v int) string {
		if p.Games == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f", float64(v)/float64(p.Games))
	}
	if p.Games > 0 {
		xt.Rows = append(xt.Rows, []string{"Points", fmt.Sprint(p.For), perGame(p.For)}, []string{"Points Allowed", fmt.Sprint(p.Against), perGame(p.Against)})
	}
	for _, r := range teamSeasonRows(t.Stats, statGames(t)) {
		xt.Rows = append(xt.Rows, []string{r.Label, r.Away, r.Home})
	}
	return xt
}

func playerStatsSheet(lines []*store.SeasonLine) *xsheet {
	sh := &xsheet{Name: "Player Stats"}
	for _, rt := range teamPlayerTables(lines) {
		sh.Tables = append(sh.Tables, fromRendered(rt, rt.Name, "Player", "POS", "Class"))
	}
	return sh
}

func scheduleTable(games []*store.Game, teams map[int]*store.Team, team, year int) *xtable {
	xt := &xtable{Title: "Schedule and Results", Headers: []string{"Week", "Date", "Opponent", "Game", "Result"}}
	for _, g := range games {
		res := gameResult(g, team)
		if res == "" {
			res = "Not played"
		}
		xt.Rows = append(xt.Rows, []string{weekLabel(g.WeekType, g.Week), dateLabel(g, year), opponent(g, teams, team), g.TypeLabel(), res})
	}
	return xt
}

// opponent is "vs Alabama" or "@ Alabama" from one team's side.
func opponent(g *store.Game, teams map[int]*store.Team, team int) string {
	if g.Home == team {
		return "vs " + teamOr(teams, g.Away).Name
	}
	return "@ " + teamOr(teams, g.Home).Name
}

// gameTag is the short game label for a sheet tab, e.g. "Wk 3" or "CFP".
func gameTag(g *store.Game) string {
	switch g.Kind() {
	case "conf_championship":
		return "CCG"
	case "bowl":
		return "Bowl"
	case "playoff":
		return "CFP"
	case "national_championship":
		return "NCG"
	}
	if g.WeekType == "RegularSeason" {
		return fmt.Sprintf("Wk %d", g.Week)
	}
	return weekShort(g.WeekType, g.Week)
}

// boxSheet is one game's full box score, seen from team's schedule.
func boxSheet(g *store.Game, teams map[int]*store.Team, lines []*store.Line, plays []*store.ScoringPlay, team, year int) *xsheet {
	away, home := teamOr(teams, g.Away), teamOr(teams, g.Home)
	opp, at := home, "@"
	if g.Home == team {
		opp, at = away, "vs"
	}
	final := ""
	if g.Overtime {
		final = " (OT)"
	}
	sh := &xsheet{
		Name:    fmt.Sprintf("%s %s %s", gameTag(g), at, opp.Short),
		Heading: fmt.Sprintf("%s: %s %d, %s %d%s", weekLabel(g.WeekType, g.Week), away.Name, g.AwayScore, home.Name, g.HomeScore, final),
		Meta:    []xkv{{"Date", dateLabel(g, year)}},
	}
	if l := g.TypeLabel(); l != "" {
		sh.Meta = append(sh.Meta, xkv{"Game", l})
	}
	if g.Stadium != "" {
		sh.Meta = append(sh.Meta, xkv{"Stadium", g.Stadium})
	}
	if g.Attendance > 0 {
		sh.Meta = append(sh.Meta, xkv{"Attendance", fmt.Sprint(g.Attendance)})
	}

	qs := quarterCols(g)
	line := &xtable{Title: "Scoring by Quarter", Headers: append(append([]string{"Team"}, qs...), "T")}
	for _, side := range []struct {
		t     *store.Team
		q     []int32
		ot    int
		score int
	}{{away, g.AwayQ, g.AwayOT, g.AwayScore}, {home, g.HomeQ, g.HomeOT, g.HomeScore}} {
		row := []string{side.t.Name}
		for i := range 4 {
			v := "-"
			if i < len(side.q) {
				v = fmt.Sprint(side.q[i])
			}
			row = append(row, v)
		}
		if len(qs) > 4 {
			row = append(row, fmt.Sprint(side.ot))
		}
		line.Rows = append(line.Rows, append(row, fmt.Sprint(side.score)))
	}
	sh.Tables = append(sh.Tables, line)

	if sl := scoringLines(g, plays, lines); len(sl) > 0 {
		xt := &xtable{Title: "Scoring Plays", Headers: []string{"Q", "Time", "Team", "Type", "Play", away.Short, home.Short}}
		for _, p := range sl {
			q := fmt.Sprint(p.Quarter)
			if p.Quarter > 4 {
				q = "OT"
			}
			xt.Rows = append(xt.Rows, []string{q, p.Clock, teamOr(teams, p.TeamID).Short, p.Type, p.Description, fmt.Sprint(p.Away), fmt.Sprint(p.Home)})
		}
		sh.Tables = append(sh.Tables, xt)
	}

	if rows := teamBoxRows(g.AwayBox, g.HomeBox); len(rows) > 0 {
		xt := &xtable{Title: "Team Stats", Headers: []string{"Stat", away.Short, home.Short}}
		parent := ""
		for _, r := range rows {
			label := r.Label
			if r.Indent {
				label = parent + ": " + label
			} else {
				parent = r.Label
			}
			xt.Rows = append(xt.Rows, []string{label, r.Away, r.Home})
		}
		sh.Tables = append(sh.Tables, xt)
	}

	for _, bc := range boxCategories(g, lines) {
		for _, side := range []struct {
			t  *store.Team
			rt *RenderedTable
		}{{away, bc.Away}, {home, bc.Home}} {
			if len(side.rt.Rows) == 0 {
				continue
			}
			sh.Tables = append(sh.Tables, fromRendered(*side.rt, bc.Name+": "+side.t.Name, "Player", "No."))
		}
	}
	return sh
}

// fromRendered converts a site table. lead names the label column and, when
// given, the sub-label column (position, team, jersey) and the class column.
func fromRendered(rt RenderedTable, title string, lead ...string) *xtable {
	xt := &xtable{Title: title, Headers: append([]string(nil), lead...), Legend: colLegend(rt.Headers)}
	for _, col := range rt.Headers {
		xt.Headers = append(xt.Headers, col.Key)
	}
	cells := func(r Row) []string {
		out := []string{r.Label}
		if len(lead) > 1 {
			out = append(out, r.Sub)
		}
		if len(lead) > 2 {
			out = append(out, r.Class)
		}
		return append(out, r.Cells...)
	}
	for _, r := range rt.Rows {
		xt.Rows = append(xt.Rows, cells(r))
	}
	if rt.Total != nil {
		xt.Total = cells(*rt.Total)
	}
	return xt
}

func colLegend(cols []Col) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.Key + " = " + c.Title
	}
	return strings.Join(parts, ", ")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

// ---- Markdown ----

func writeMarkdown(w *bytes.Buffer, d *xdoc) {
	fmt.Fprintf(w, "# %s\n\n", d.Title)
	mdMeta(w, d.Meta)
	seen := map[string]bool{}
	for _, sh := range d.Sheets {
		fmt.Fprintf(w, "## %s\n\n", sh.heading())
		mdMeta(w, sh.Meta)
		if sh.Body != "" {
			w.WriteString(demoteHeadings(strings.TrimSpace(sh.Body)) + "\n\n")
		}
		if len(sh.Tables) == 0 && len(sh.Meta) == 0 && sh.Body == "" {
			w.WriteString("_No entries._\n\n")
		}
		for _, t := range sh.Tables {
			if t.Title != "" {
				fmt.Fprintf(w, "### %s\n\n", t.Title)
			}
			if t.Legend != "" && !seen[t.Legend] {
				seen[t.Legend] = true
				fmt.Fprintf(w, "Columns: %s\n\n", t.Legend)
			}
			if len(t.Rows) == 0 {
				w.WriteString("_No entries._\n\n")
				continue
			}
			mdRow(w, t.Headers)
			w.WriteString("|" + strings.Repeat(" --- |", len(t.Headers)) + "\n")
			for _, r := range t.Rows {
				mdRow(w, r)
			}
			if t.Total != nil {
				tot := append([]string(nil), t.Total...)
				tot[0] = "**" + tot[0] + "**"
				mdRow(w, tot)
			}
			w.WriteString("\n")
		}
	}
}

func mdMeta(w *bytes.Buffer, m []xkv) {
	for _, kv := range m {
		fmt.Fprintf(w, "- **%s:** %s\n", kv.Key, kv.Val)
	}
	if len(m) > 0 {
		w.WriteString("\n")
	}
}

var mdHeading = regexp.MustCompile(`(?m)^(#{1,6})(\s)`)

// demoteHeadings moves a story's own headings two levels down, so they nest
// under its "##" section instead of competing with the document's.
func demoteHeadings(s string) string {
	return mdHeading.ReplaceAllStringFunc(s, func(h string) string {
		n := min(strings.Count(h, "#")+2, 6)
		return strings.Repeat("#", n) + h[len(h)-1:]
	})
}

var (
	mdMarks    = regexp.MustCompile(`^#{1,6}\s+|\*\*|__|` + "`")
	mdListItem = regexp.MustCompile(`^([-*+]|\d+[.)])\s`)
	mdParaGap  = regexp.MustCompile(`\n\s*\n`)
)

// plainParagraphs splits story Markdown into paragraphs for spreadsheet
// cells, dropping heading and emphasis markers. Wrapped lines of a paragraph
// join; each list item stays its own paragraph.
func plainParagraphs(s string) []string {
	var out []string
	for _, para := range mdParaGap.Split(strings.TrimSpace(s), -1) {
		var cur []string
		flush := func() {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, " "))
				cur = nil
			}
		}
		for _, l := range strings.Split(para, "\n") {
			l = strings.TrimSpace(l)
			if mdListItem.MatchString(l) {
				flush()
			}
			if l = strings.TrimSpace(mdMarks.ReplaceAllString(l, "")); l != "" {
				cur = append(cur, l)
			}
		}
		flush()
	}
	return out
}

var mdEscape = strings.NewReplacer("|", `\|`, "\n", " ")

func mdRow(w *bytes.Buffer, cells []string) {
	w.WriteString("|")
	for _, c := range cells {
		w.WriteString(" " + mdEscape.Replace(c) + " |")
	}
	w.WriteString("\n")
}

// ---- Excel ----

func writeXLSX(w io.Writer, d *xdoc) error {
	f := excelize.NewFile()
	defer f.Close()
	title, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	head, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"2B2B2B"}}})
	totalStyle := func(numFmt string) *excelize.Style {
		return &excelize.Style{Font: &excelize.Font{Bold: true}, CustomNumFmt: &numFmt,
			Border: []excelize.Border{{Type: "top", Color: "000000", Style: 1}}}
	}
	total, _ := f.NewStyle(totalStyle("General"))
	wrap, _ := f.NewStyle(&excelize.Style{Alignment: &excelize.Alignment{WrapText: true, Vertical: "top"}})
	// Decimal styles by digit count, plain and for total rows.
	decimals := map[[2]int]int{}
	decimal := func(digits int, isTotal bool) int {
		key := [2]int{digits, 0}
		if isTotal {
			key[1] = 1
		}
		if id, ok := decimals[key]; ok {
			return id
		}
		numFmt := "0." + strings.Repeat("0", digits)
		st := &excelize.Style{CustomNumFmt: &numFmt}
		if isTotal {
			st = totalStyle(numFmt)
		}
		id, _ := f.NewStyle(st)
		decimals[key] = id
		return id
	}

	used := map[string]bool{}
	for i, sh := range d.Sheets {
		name := sheetName(sh.Name, used)
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return err
			}
		} else if _, err := f.NewSheet(name); err != nil {
			return err
		}
		row := 1
		widths := map[int]int{}
		put := func(vals []string, style int, track bool) error {
			cell, _ := excelize.CoordinatesToCellName(1, row)
			vs := make([]any, len(vals))
			for j, v := range vals {
				vs[j] = cellValue(v)
				if track && len(v) > widths[j] {
					widths[j] = len(v)
				}
			}
			if err := f.SetSheetRow(name, cell, &vs); err != nil {
				return err
			}
			if style != 0 && len(vals) > 0 {
				end, _ := excelize.CoordinatesToCellName(len(vals), row)
				if err := f.SetCellStyle(name, cell, end, style); err != nil {
					return err
				}
			}
			if track {
				for j, v := range vals {
					dot := strings.IndexByte(v, '.')
					if _, isNum := vs[j].(float64); !isNum || dot < 0 {
						continue
					}
					c, _ := excelize.CoordinatesToCellName(j+1, row)
					if err := f.SetCellStyle(name, c, c, decimal(len(v)-dot-1, style == total)); err != nil {
						return err
					}
				}
			}
			row++
			return nil
		}
		if err := put([]string{sh.heading()}, title, false); err != nil {
			return err
		}
		meta := sh.Meta
		if i == 0 {
			meta = append(append([]xkv(nil), d.Meta...), meta...)
		}
		for _, kv := range meta {
			if err := put([]string{kv.Key, kv.Val}, 0, false); err != nil {
				return err
			}
			cell, _ := excelize.CoordinatesToCellName(1, row-1)
			f.SetCellStyle(name, cell, cell, bold)
		}
		row++
		// Story text: one wrapped paragraph per row in a wide first column.
		if sh.Body != "" {
			for _, p := range plainParagraphs(sh.Body) {
				cell, _ := excelize.CoordinatesToCellName(1, row)
				if err := f.SetCellStr(name, cell, p); err != nil {
					return err
				}
				f.SetCellStyle(name, cell, cell, wrap)
				row++
			}
			row++
		}
		for _, t := range sh.Tables {
			if t.Title != "" {
				if err := put([]string{t.Title}, bold, false); err != nil {
					return err
				}
			}
			if err := put(t.Headers, head, true); err != nil {
				return err
			}
			for _, r := range t.Rows {
				if err := put(r, 0, true); err != nil {
					return err
				}
			}
			if t.Total != nil {
				if err := put(t.Total, total, true); err != nil {
					return err
				}
			}
			row++
		}
		for j, n := range widths {
			col, _ := excelize.ColumnNumberToName(j + 1)
			width := float64(min(max(n+2, 7), 60))
			if j == 0 {
				width = max(width, 16)
			}
			f.SetColWidth(name, col, col, width)
		}
		if sh.Body != "" && len(widths) == 0 {
			f.SetColWidth(name, "A", "A", 100)
		}
	}
	f.SetActiveSheet(0)
	return f.Write(w)
}

var plainNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// cellValue stores plain numbers as numbers so Excel can sort and sum them;
// everything else (3/5, 12:30, #7) stays text.
func cellValue(v string) any {
	if !plainNumber.MatchString(v) {
		return v
	}
	var f float64
	if _, err := fmt.Sscan(v, &f); err != nil {
		return v
	}
	return f
}

var badSheetChars = strings.NewReplacer("[", "(", "]", ")", ":", "-", "*", "", "?", "", "/", "-", `\`, "-")

// sheetName makes a valid, unique Excel sheet name (31 characters at most,
// unique ignoring case).
func sheetName(s string, used map[string]bool) string {
	s = strings.Trim(badSheetChars.Replace(s), "' ")
	if s == "" {
		s = "Sheet"
	}
	cut := func(s string, n int) string {
		r := []rune(s)
		if len(r) > n {
			return strings.TrimSpace(string(r[:n]))
		}
		return s
	}
	name := cut(s, 31)
	for i := 2; used[strings.ToLower(name)]; i++ {
		suffix := fmt.Sprintf(" (%d)", i)
		name = cut(s, 31-len(suffix)) + suffix
	}
	used[strings.ToLower(name)] = true
	return name
}
