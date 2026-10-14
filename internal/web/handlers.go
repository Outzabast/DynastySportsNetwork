package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

type data map[string]any

type pageFunc func(r *http.Request, c *store.Ctx) (string, data, error)

var errNotFound = errors.New("not found")

const dynastyCookie = "dsn_dynasty"

// dynastyFor returns the dynasty the viewer selected (?dynasty= or cookie);
// 0 means "the most recently played dynasty".
func dynastyFor(r *http.Request) int {
	if v := atoi(r.URL.Query().Get("dynasty"), 0); v > 0 {
		return v
	}
	if ck, err := r.Cookie(dynastyCookie); err == nil {
		return atoi(ck.Value, 0)
	}
	return 0
}

// context loads the selected dynasty, falling back to the most recent one if
// the selection no longer exists.
func (s *Site) context(r *http.Request) (*store.Ctx, error) {
	d := dynastyFor(r)
	c, err := s.q.Context(r.Context(), d)
	if errors.Is(err, store.ErrNoData) && d != 0 {
		return s.q.Context(r.Context(), 0)
	}
	return c, err
}

// switchDynasty selects a dynasty for this browser and goes to its front page.
func (s *Site) switchDynasty(w http.ResponseWriter, r *http.Request) {
	id := atoi(r.PathValue("id"), 0)
	http.SetCookie(w, &http.Cookie{Name: dynastyCookie, Value: itoa(id), Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Site) dynasties(r *http.Request, c *store.Ctx) (string, data, error) {
	ds, err := s.q.Dynasties(r.Context())
	if err != nil {
		return "", nil, err
	}
	return "dynasties", data{"Dynasties": ds}, nil
}

func (s *Site) page(f pageFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.context(r)
		if errors.Is(err, store.ErrNoData) {
			s.render(w, http.StatusOK, "empty", data{"SavesDir": s.opt.SavesDir, "Status": s.status()})
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		// The ticker loads alongside the page and shares its scoreboard data.
		r = r.WithContext(withWeekCache(r.Context(), c))
		var ticker []*GameCard
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker = s.ticker(r.Context(), c)
		}()
		name, d, err := f(r, c)
		<-done
		if errors.Is(err, errNotFound) {
			s.render(w, http.StatusNotFound, "notfound", data{"C": c, "Ticker": ticker})
			return
		}
		if err != nil {
			s.fail(w, err)
			return
		}
		d["C"] = c
		d["Ticker"] = ticker
		d["Path"] = r.URL.Path
		d["CanEdit"] = canEdit(r)
		s.render(w, http.StatusOK, name, d)
	}
}

func (s *Site) fail(w http.ResponseWriter, err error) {
	s.opt.Log.Error("page", "err", err)
	http.Error(w, "Internal error: "+err.Error(), http.StatusInternalServerError)
}

func (s *Site) render(w http.ResponseWriter, code int, name string, d data) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "missing template "+name, 500)
		return
	}
	if _, ok := d["Path"]; !ok {
		d["Path"] = ""
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := t.ExecuteTemplate(w, "layout", d); err != nil {
		s.opt.Log.Error("render", "template", name, "err", err)
	}
}

func (s *Site) status() any {
	if s.opt.Status == nil {
		return nil
	}
	return s.opt.Status()
}

func atoi(v string, d int) int {
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return d
}

// seasonParam reads ?season=YEAR (calendar) and returns the season index.
func seasonParam(r *http.Request, c *store.Ctx) int {
	if y := atoi(r.URL.Query().Get("season"), 0); y > 0 {
		return y - c.BaseYear
	}
	return c.Season
}

// ---- Shared view models ----

// GameCard is a scoreboard tile.
type GameCard struct {
	G       *store.Game
	Away    *store.Team
	Home    *store.Team
	Leaders []string
	Year    int
}

// Tag names the kind of game for scoreboards and schedules.
func (gc *GameCard) Tag() string {
	switch gc.G.Kind() {
	case "conference":
		return "Conference"
	case "nonconference":
		return "Non-Conference"
	case "conf_championship":
		if gc.Home != nil && gc.Home.Conference != "" {
			return gc.Home.Conference + " Championship"
		}
		return "Conference Championship"
	case "bowl":
		return "Bowl Game"
	case "playoff":
		return "College Football Playoff"
	case "national_championship":
		return "CFP National Championship"
	}
	return ""
}

// Featured marks title games, bowls and playoff games for a banner.
func (gc *GameCard) Featured() bool {
	k := gc.G.Kind()
	return k == "conf_championship" || gc.G.Postseason()
}

func (s *Site) cards(ctx context.Context, c *store.Ctx, season int, games []*store.Game, leaders bool) ([]*GameCard, error) {
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return nil, err
	}
	out := make([]*GameCard, 0, len(games))
	for _, g := range games {
		out = append(out, &GameCard{G: g, Away: teamOr(teams, g.Away), Home: teamOr(teams, g.Home), Year: c.YearOf(season)})
	}
	if leaders {
		if err := s.addLeaders(ctx, c, season, out...); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// teamOr returns a placeholder for FCS opponents (TeamIndex 255 / unknown).
func teamOr(m map[int]*store.Team, id int) *store.Team {
	if t, ok := m[id]; ok {
		return t
	}
	return &store.Team{ID: -1, Name: "FCS Opponent", Short: "FCS", Long: "FCS Opponent", Color: "#6c6d6f"}
}

func gameLeaders(lines []*store.Line, teams map[int]*store.Team) []string {
	best := func(group, key string, f func(store.Stats) string) string {
		var top *store.Line
		for _, l := range lines {
			if l.Group == group && (top == nil || l.Stats[key] > top.Stats[key]) {
				top = l
			}
		}
		if top == nil || top.Stats[key] == 0 {
			return ""
		}
		abbr := ""
		if t, ok := teams[top.TeamID]; ok {
			abbr = t.Short
		}
		return fmt.Sprintf("%s (%s) %s", top.Player.Short(), abbr, f(top.Stats))
	}
	var out []string
	if v := best("offense", "PASSYARDS", func(s store.Stats) string {
		return fmt.Sprintf("%d/%d, %d YDS, %d TD", s["PASSCOMPLETED"], s["PASSATTEMPTS"], s["PASSYARDS"], s["PASSTDS"])
	}); v != "" {
		out = append(out, "PASS: "+v)
	}
	if v := best("offense", "RUSHYARDS", func(s store.Stats) string {
		return fmt.Sprintf("%d CAR, %d YDS, %d TD", s["RUSHATTEMPTS"], s["RUSHYARDS"], s["RUSHTDS"])
	}); v != "" {
		out = append(out, "RUSH: "+v)
	}
	if v := best("offense", "RECEIVEYARDS", func(s store.Stats) string {
		return fmt.Sprintf("%d REC, %d YDS, %d TD", s["RECEIVECATCHES"], s["RECEIVEYARDS"], s["RECEIVETDS"])
	}); v != "" {
		out = append(out, "REC: "+v)
	}
	return out
}

// ticker is the scores strip at the top of every page (current week).
func (s *Site) ticker(ctx context.Context, c *store.Ctx) []*GameCard {
	wt, wk := s.currentWeek(ctx, c)
	byWeek, err := s.weekGames(ctx, c, c.Season, store.WeekID{WeekType: wt, Week: wk})
	if err != nil {
		return nil
	}
	cards, _ := s.cards(ctx, c, c.Season, byWeek[store.WeekID{WeekType: wt, Week: wk}], false)
	// Ranked matchups first, like ESPN's top events.
	sort.SliceStable(cards, func(i, j int) bool { return cardWeight(cards[i]) > cardWeight(cards[j]) })
	if len(cards) > 30 {
		cards = cards[:30]
	}
	return cards
}

func cardWeight(g *GameCard) int {
	w := 0
	for _, t := range []*store.Team{g.Away, g.Home} {
		if r := t.Rank(); r > 0 {
			w += 30 - r
		}
	}
	if g.G.GameOfWeek {
		w += 100
	}
	return w
}

// currentWeek picks the scoreboard week to show: the latest week with a
// played game, or the current save week.
func (s *Site) currentWeek(ctx context.Context, c *store.Ctx) (string, int) {
	weeks, err := s.seasonWeeks(ctx, c, c.Season)
	return pickWeek(c, weeks, err)
}

func pickWeek(c *store.Ctx, weeks []store.WeekKey, err error) (string, int) {
	if err != nil || len(weeks) == 0 {
		return c.WeekType, c.Week
	}
	// The week the save is on (games about to be played), else the last week.
	for _, w := range weeks {
		if w.Week == c.Week && w.WeekType == c.WeekType {
			return w.WeekType, w.Week
		}
	}
	for _, w := range weeks {
		if w.Week >= c.Week {
			return w.WeekType, w.Week
		}
	}
	last := weeks[len(weeks)-1]
	return last.WeekType, last.Week
}

// ---- Pages ----

func (s *Site) home(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	var (
		teams             map[int]*store.Team
		lines             []*store.SeasonLine
		stories           []*store.Article
		wt                string
		wk                int
		featured          *GameCard
		upcoming, results []*GameCard
	)
	// The scoreboard (weeks, then games, then box score leaders) runs
	// alongside the league stat lines, the slowest single query.
	err := parallel(
		func() error {
			var games, prev []*store.Game
			err := parallel(
				func() (err error) { teams, err = s.q.Teams(ctx, c, c.Season); return },
				func() error {
					weeks, werr := s.seasonWeeks(ctx, c, c.Season)
					wt, wk = pickWeek(c, weeks, werr)
					cur := store.WeekID{WeekType: wt, Week: wk}
					ids := []store.WeekID{cur}
					// Last completed week's results among ranked teams.
					var last *store.WeekID
					for i := len(weeks) - 1; i >= 0; i-- {
						if weeks[i].Played > 0 && weeks[i].Played == weeks[i].Games {
							id := weeks[i].ID()
							last = &id
							if id != cur {
								ids = append(ids, id)
							}
							break
						}
					}
					byWeek, err := s.weekGames(ctx, c, c.Season, ids...)
					if err != nil {
						return err
					}
					games = byWeek[cur]
					if last != nil {
						prev = byWeek[*last]
					}
					return nil
				},
			)
			if err != nil {
				return err
			}
			if upcoming, err = s.cards(ctx, c, c.Season, games, false); err != nil {
				return err
			}
			sort.SliceStable(upcoming, func(i, j int) bool { return cardWeight(upcoming[i]) > cardWeight(upcoming[j]) })
			if results, err = s.cards(ctx, c, c.Season, prev, false); err != nil {
				return err
			}
			sort.SliceStable(results, func(i, j int) bool { return cardWeight(results[i]) > cardWeight(results[j]) })
			if len(upcoming) > 0 {
				featured = upcoming[0]
			}
			if len(results) > 8 {
				results = results[:8]
			}
			if len(upcoming) > 10 {
				upcoming = upcoming[1:11]
			}
			// Box score leaders only for the cards shown, in one query.
			shown := append(append([]*GameCard{}, upcoming...), results...)
			if featured != nil {
				shown = append(shown, featured)
			}
			return s.addLeaders(ctx, c, c.Season, shown...)
		},
		func() (err error) { lines, err = s.q.LeagueSeasonLines(ctx, c, c.Season); return },
		func() (err error) {
			stories, err = s.q.Articles(ctx, c, store.ArticleFilter{Team: -1, Season: -1, Limit: 8})
			return
		},
	)
	if err != nil {
		return "", nil, err
	}
	top25 := rankedTeams(teams, "ap")
	leaders := []LeaderBoard{}
	for _, id := range []string{"passyds", "rushyds", "recyds", "sacks", "ints", "tackles"} {
		leaders = append(leaders, s.leaderBoard(id, lines, teams, 5))
	}
	return "home", data{"Featured": featured, "Upcoming": upcoming, "Results": results, "Top25": top25, "Teams": teams,
		"Leaders": leaders, "WeekLabel": weekLabel(wt, wk), "Stories": s.storyCards(teams, stories)}, nil
}

func rankedTeams(teams map[int]*store.Team, poll string) []*store.Team {
	var out []*store.Team
	for _, t := range teams {
		r := t.AP
		switch poll {
		case "coaches":
			r = t.Coaches
		case "cfp":
			r = t.CFP
		}
		if r > 0 && r <= 25 {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].AP, out[j].AP
		if poll == "coaches" {
			ri, rj = out[i].Coaches, out[j].Coaches
		} else if poll == "cfp" {
			ri, rj = out[i].CFP, out[j].CFP
		}
		return ri < rj
	})
	return out
}

func (s *Site) scoreboard(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season := seasonParam(r, c)
	weeks, err := s.seasonWeeks(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	wt, wk := r.URL.Query().Get("type"), atoi(r.URL.Query().Get("week"), -1)
	if wk < 0 {
		if season == c.Season {
			wt, wk = s.currentWeek(ctx, c)
		} else if len(weeks) > 0 {
			wt, wk = weeks[len(weeks)-1].WeekType, weeks[len(weeks)-1].Week
		}
	}
	if wt == "" {
		wt = "RegularSeason"
	}
	byWeek, err := s.weekGames(ctx, c, season, store.WeekID{WeekType: wt, Week: wk})
	if err != nil {
		return "", nil, err
	}
	games := byWeek[store.WeekID{WeekType: wt, Week: wk}]
	conf := r.URL.Query().Get("conf")
	cards, err := s.cards(ctx, c, season, games, true)
	if err != nil {
		return "", nil, err
	}
	if conf != "" {
		f := cards[:0]
		for _, g := range cards {
			if g.Away.Conference == conf || g.Home.Conference == conf {
				f = append(f, g)
			}
		}
		cards = f
	}
	if r.URL.Query().Get("top25") == "1" {
		f := cards[:0]
		for _, g := range cards {
			if g.Away.Rank() > 0 || g.Home.Rank() > 0 {
				f = append(f, g)
			}
		}
		cards = f
	}
	// Group by date like ESPN.
	type day struct {
		Label string
		Games []*GameCard
	}
	var days []*day
	byLabel := map[string]*day{}
	for _, g := range cards {
		l := dateLabel(g.G, c.YearOf(season))
		d, ok := byLabel[l]
		if !ok {
			d = &day{Label: l}
			byLabel[l] = d
			days = append(days, d)
		}
		d.Games = append(d.Games, g)
	}
	teams, _ := s.q.Teams(ctx, c, season)
	return "scoreboard", data{"Weeks": weeks, "WeekType": wt, "Week": wk, "Days": days, "Season": season, "Conf": conf,
		"Confs": conferences(teams), "Top25": r.URL.Query().Get("top25") == "1"}, nil
}

func conferences(teams map[int]*store.Team) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range teams {
		if t.Conference != "" && !seen[t.Conference] {
			seen[t.Conference] = true
			out = append(out, t.Conference)
		}
	}
	sort.Strings(out)
	return out
}

// ScoreLine is a rendered scoring-summary entry.
type ScoreLine struct {
	Quarter     int
	Clock       string
	TeamID      int
	Type        string
	Description string
	Away, Home  int
}

func (s *Site) game(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season, row := atoi(r.PathValue("season"), -1)-c.BaseYear, atoi(r.PathValue("row"), -1)
	var (
		g      *store.Game
		teams  map[int]*store.Team
		lines  []*store.Line
		plays  []*store.ScoringPlay
		recaps []*store.Article
	)
	err := parallel(
		func() (err error) { g, err = s.q.Game(ctx, c, season, row); return },
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { lines, err = s.q.GameLines(ctx, c, season, row); return },
		func() (err error) { plays, err = s.q.ScoringPlays(ctx, c, season, row); return },
		func() (err error) {
			recaps, err = s.q.Articles(ctx, c, store.ArticleFilter{Team: -1, Season: season, Game: row, Drafts: canEdit(r), Limit: 5})
			return
		},
	)
	if err != nil {
		return "", nil, err
	}
	if g == nil {
		return "", nil, errNotFound
	}
	away, home := teamOr(teams, g.Away), teamOr(teams, g.Home)
	box := func(t Table, team int) *RenderedTable {
		rt := &RenderedTable{ID: t.ID, Name: t.Name, Headers: t.Cols}
		total := store.Stats{}
		for _, l := range lines {
			if l.TeamID != team || !t.groupOK(l.Group) || !t.Has(l.Stats) {
				continue
			}
			total.Add(l.Stats)
			row := Row{Label: l.Player.Name(), Href: "/player/" + l.Player.ID.String(), sortV: t.Cols[t.SortBy].Num(l.Stats)}
			if l.Player.Jersey > 0 {
				row.Sub = fmt.Sprintf("#%d", l.Player.Jersey)
			}
			for _, col := range t.Cols {
				row.Cells = append(row.Cells, col.Val(l.Stats))
			}
			rt.Rows = append(rt.Rows, row)
		}
		sortRows(rt.Rows)
		if len(rt.Rows) > 0 {
			tr := Row{Label: "TEAM"}
			for _, col := range t.Cols {
				tr.Cells = append(tr.Cells, col.Val(total))
			}
			rt.Total = &tr
		}
		return rt
	}
	// ESPN layout: one row per category, away and home side by side. A
	// category is shown when either team has an entry.
	var boxCats []BoxCategory
	for _, t := range boxTables {
		a, h := box(t, g.Away), box(t, g.Home)
		if len(a.Rows)+len(h.Rows) == 0 {
			continue
		}
		boxCats = append(boxCats, BoxCategory{Name: t.Name, Away: a, Home: h})
	}
	return "game", data{"G": g, "Away": away, "Home": home, "Box": boxCats, "Recaps": s.storyCards(teams, recaps),
		"TeamStats": teamBoxRows(g.AwayBox, g.HomeBox), "Scoring": scoringLines(g, plays, lines), "HasScoring": len(plays) > 0,
		"Season": season, "Year": c.YearOf(season), "Quarters": quarterCols(g)}, nil
}

func quarterCols(g *store.Game) []string {
	cols := []string{"1", "2", "3", "4"}
	if g.Overtime || g.HomeOT+g.AwayOT > 0 {
		cols = append(cols, "OT")
	}
	return cols
}

// scoringLines turns stored scoring entries into ESPN-style lines. Each entry
// carries the scoring team's passing/rushing/receiving leaders at that moment;
// the leader whose touchdown count went up scored (or threw) the TD.
func scoringLines(g *store.Game, plays []*store.ScoringPlay, lines []*store.Line) []ScoreLine {
	var out []ScoreLine
	prevTD := map[string]map[string]int64{"home": {}, "away": {}}
	kickers := map[string]string{}
	for _, l := range lines {
		if l.Group == "kicking" && l.Stats["KICKFGMADE"] > 0 {
			side := "away"
			if l.TeamID == g.Home {
				side = "home"
			}
			if _, dup := kickers[side]; dup {
				kickers[side] = "" // more than one kicker made a FG: don't guess
			} else {
				kickers[side] = l.Player.Name()
			}
		}
	}
	for i, p := range plays {
		side := p.ScoringTeam
		pts := p.HomeScore - p.HomePrev
		if side == "away" {
			pts = p.AwayScore - p.AwayPrev
		}
		// Conversion points show up as the next entry's starting score.
		away, home := p.AwayScore, p.HomeScore
		if i+1 < len(plays) {
			away, home = max(away, plays[i+1].AwayPrev), max(home, plays[i+1].HomePrev)
		} else {
			away, home = max(away, g.AwayScore), max(home, g.HomeScore)
		}
		conv := (home - p.HomeScore) + (away - p.AwayScore)
		roles := []string{"pass", "rush", "rec"}
		scorer := map[string]string{}
		for j, part := range p.Participants {
			if j >= len(roles) || part.Player.ID == uuid.Nil {
				continue
			}
			id := part.Player.ID.String()
			if part.Stats["Touchdowns"] > prevTD[side][id] {
				scorer[roles[j]] = part.Player.Name()
			}
			prevTD[side][id] = part.Stats["Touchdowns"]
		}
		if pts <= 0 {
			continue
		}
		team := g.Home
		if side == "away" {
			team = g.Away
		}
		sl := ScoreLine{Quarter: p.Quarter, Clock: clock(int64(p.ClockSec)), TeamID: team, Away: away, Home: home}
		switch {
		case pts >= 6:
			sl.Type = "TD"
			switch {
			case scorer["pass"] != "" && scorer["rec"] != "":
				sl.Description = scorer["rec"] + " Pass From " + scorer["pass"]
			case scorer["rush"] != "":
				sl.Description = scorer["rush"] + " Run"
			case scorer["pass"] != "":
				sl.Description = scorer["pass"] + " Touchdown Pass"
			case scorer["rec"] != "":
				sl.Description = scorer["rec"] + " Touchdown Reception"
			default:
				sl.Description = "Touchdown"
			}
			switch {
			case conv == 1:
				sl.Description += " (Kick)"
			case conv == 2:
				sl.Description += " (Two-Point Conversion)"
			case p.Conversion == "FieldGoal":
				sl.Description += " (Kick Failed)"
			}
		case pts == 3:
			sl.Type = "FG"
			if k := kickers[side]; k != "" {
				sl.Description = k + " Field Goal"
			} else {
				sl.Description = "Field Goal"
			}
		case pts == 2:
			sl.Type = "SF"
			sl.Description = "Safety"
		default:
			sl.Type = "PTS"
			sl.Description = fmt.Sprintf("%d points", pts)
		}
		out = append(out, sl)
	}
	return out
}

// Standing is a conference standings table.
// Standing is one conference table. Split conferences have one Division
// per division; the rest have a single unnamed Division.
type Standing struct {
	Conference string
	Divisions  []DivisionStanding
	Teams      []*store.Team // every team, conference order (API)
}

// DivisionStanding is one division's rows ("" name = no split).
type DivisionStanding struct {
	Name  string
	Teams []*store.Team
}

func (s *Site) standingsData(ctx context.Context, c *store.Ctx, season int) ([]Standing, error) {
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return nil, err
	}
	by := map[string][]*store.Team{}
	for _, t := range teams {
		if t.HasSeason {
			by[t.Conference] = append(by[t.Conference], t)
		}
	}
	var out []Standing
	for conf, ts := range by {
		sort.Slice(ts, func(i, j int) bool {
			a, b := ts[i], ts[j]
			if a.ConfStanding != b.ConfStanding && a.ConfStanding > 0 && b.ConfStanding > 0 {
				return a.ConfStanding < b.ConfStanding
			}
			if a.CW != b.CW {
				return a.CW > b.CW
			}
			return a.W > b.W
		})
		st := Standing{Conference: conf, Teams: ts}
		idx := map[string]int{}
		for _, t := range ts {
			i, ok := idx[t.Division]
			if !ok {
				i = len(st.Divisions)
				idx[t.Division] = i
				st.Divisions = append(st.Divisions, DivisionStanding{Name: t.Division})
			}
			st.Divisions[i].Teams = append(st.Divisions[i].Teams, t)
		}
		sort.SliceStable(st.Divisions, func(i, j int) bool { return st.Divisions[i].Name < st.Divisions[j].Name })
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Conference < out[j].Conference })
	return out, nil
}

func (s *Site) standings(r *http.Request, c *store.Ctx) (string, data, error) {
	season := seasonParam(r, c)
	st, err := s.standingsData(r.Context(), c, season)
	if err != nil {
		return "", nil, err
	}
	return "standings", data{"Standings": st, "Season": season}, nil
}

func (s *Site) rankings(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season := seasonParam(r, c)
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	weeks, err := s.q.PollWeeks(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	var wk store.PollWeek
	if len(weeks) > 0 {
		wk = weeks[0]
		if w := atoi(r.URL.Query().Get("week"), -1); w >= 0 {
			for _, x := range weeks {
				if x.Week == w {
					wk = x
				}
			}
		}
	}
	polls := []struct {
		ID, Name string
		Entries  []*store.PollEntry
		Others   []*store.PollEntry
	}{{ID: "ap", Name: "AP Top 25"}, {ID: "coaches", Name: "Coaches Poll"}, {ID: "cfp", Name: "College Football Playoff Rankings"}}
	for i := range polls {
		es, err := s.q.Poll(ctx, c, season, wk, polls[i].ID)
		if err != nil {
			return "", nil, err
		}
		for _, e := range es {
			if e.Rank <= 25 {
				polls[i].Entries = append(polls[i].Entries, e)
			} else if e.Points > 0 {
				polls[i].Others = append(polls[i].Others, e)
			}
		}
	}
	return "rankings", data{"Polls": polls, "Teams": teams, "Weeks": weeks, "Week": wk, "Season": season}, nil
}

func (s *Site) teams(r *http.Request, c *store.Ctx) (string, data, error) {
	st, err := s.standingsData(r.Context(), c, c.Season)
	if err != nil {
		return "", nil, err
	}
	for _, x := range st {
		sort.Slice(x.Teams, func(i, j int) bool { return x.Teams[i].Name < x.Teams[j].Name })
	}
	return "teams", data{"Standings": st}, nil
}

func (s *Site) teamHeader(ctx context.Context, r *http.Request, c *store.Ctx) (*store.Team, int, error) {
	season := seasonParam(r, c)
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return nil, 0, err
	}
	t, ok := teams[atoi(r.PathValue("id"), -1)]
	if !ok {
		return nil, 0, errNotFound
	}
	return t, season, nil
}

// LeaderBoard is a ranked list for one leader category.
type LeaderBoard struct {
	ID, Name, Short string
	Entries         []LeaderEntry
}

// LeaderEntry is one leaderboard row.
type LeaderEntry struct {
	Rank   int
	Player store.PlayerRef
	Team   *store.Team
	Value  string
}

func (s *Site) leaderBoard(id string, lines []*store.SeasonLine, teams map[int]*store.Team, limit int) LeaderBoard {
	var ls *leaderStat
	for i := range leaderStats {
		if leaderStats[i].ID == id {
			ls = &leaderStats[i]
		}
	}
	if ls == nil {
		return LeaderBoard{}
	}
	t := tableFor(seasonTables, ls.Table)
	col := t.Cols[ls.Col]
	// A person can have rows for two teams in a season (transfers); rank per line.
	type cand struct {
		l *store.SeasonLine
		s store.Stats
		v float64
	}
	merged := map[string]*cand{}
	var order []string
	for _, l := range lines {
		if !t.groupOK(l.Group) {
			continue
		}
		k := l.Player.ID.String() + itoa(l.TeamID)
		m, ok := merged[k]
		if !ok {
			m = &cand{l: l, s: store.Stats{}}
			merged[k] = m
			order = append(order, k)
		}
		m.s.Add(l.Stats)
	}
	var cs []*cand
	for _, k := range order {
		m := merged[k]
		if !t.Has(m.s) || (ls.Min != nil && !ls.Min(m.s)) {
			continue
		}
		m.v = col.Num(m.s)
		if m.v <= 0 {
			continue
		}
		cs = append(cs, m)
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].v > cs[j].v })
	lb := LeaderBoard{ID: ls.ID, Name: ls.Name, Short: ls.Short}
	for i, m := range cs {
		if i >= limit {
			break
		}
		lb.Entries = append(lb.Entries, LeaderEntry{Rank: i + 1, Player: m.l.Player, Team: teamOr(teams, m.l.TeamID), Value: col.Val(m.s)})
	}
	return lb
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *Site) team(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season, id := seasonParam(r, c), atoi(r.PathValue("id"), -1)
	var (
		teams    map[int]*store.Team
		games    []*store.Game
		lines    []*store.SeasonLine
		coaches  []*store.Coach
		inj      []*store.Injury
		riv      []*store.Rivalry
		recruits []*store.RecruitEntry
		news     []*store.Article
	)
	// Everything keys on the team id from the URL, so it all loads at once;
	// an unknown team is caught afterwards.
	err := parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, season); return },
		func() (err error) { games, err = s.q.TeamGames(ctx, c, season, id); return },
		func() (err error) { lines, err = s.q.SeasonLines(ctx, c, season, uuid.Nil, id); return },
		func() (err error) { coaches, err = s.q.Coaches(ctx, c, season, id); return },
		func() (err error) { inj, err = s.q.Injuries(ctx, c, id); return },
		func() (err error) { riv, err = s.q.Rivalries(ctx, c, id); return },
		func() (err error) { recruits, err = s.q.Recruits(ctx, c, season, id); return },
		func() (err error) {
			news, err = s.q.Articles(ctx, c, store.ArticleFilter{Team: id, Season: -1, Limit: 5})
			return
		},
	)
	if err != nil {
		return "", nil, err
	}
	t, ok := teams[id]
	if !ok {
		return "", nil, errNotFound
	}
	sched, err := s.cards(ctx, c, season, games, false)
	if err != nil {
		return "", nil, err
	}
	var leaders []LeaderBoard
	for _, id := range []string{"passyds", "rushyds", "recyds", "tackles", "sacks", "ints"} {
		lb := s.leaderBoard(id, lines, teams, 1)
		if len(lb.Entries) > 0 {
			leaders = append(leaders, lb)
		}
	}
	st, _ := s.standingsData(ctx, c, season)
	var confTable *Standing
	for i := range st {
		if st[i].Conference == t.Conference {
			confTable = &st[i]
		}
	}
	return "team", data{"T": t, "Season": season, "Schedule": sched, "Leaders": leaders, "Coaches": coaches, "Injuries": inj,
		"Rivalries": riv, "Teams": teams, "Recruits": recruits, "Conf": confTable, "Tab": "home", "Stories": s.storyCards(teams, news)}, nil
}

func (s *Site) teamRoster(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	t, season, err := s.teamHeader(ctx, r, c)
	if err != nil {
		return "", nil, err
	}
	roster, err := s.q.Roster(ctx, c, season, t.ID)
	if err != nil {
		return "", nil, err
	}
	groups := []struct {
		Name      string
		Positions map[string]bool
		Players   []*store.RosterEntry
	}{
		{"Offense", set("QB", "HB", "FB", "WR", "TE", "LT", "LG", "C", "RG", "RT"), nil},
		{"Defense", set("LE", "RE", "DT", "LOLB", "MLB", "ROLB", "CB", "FS", "SS"), nil},
		{"Special Teams", set("K", "P", "LS"), nil},
	}
	for _, p := range roster {
		placed := false
		for i := range groups {
			if groups[i].Positions[p.Position] {
				groups[i].Players = append(groups[i].Players, p)
				placed = true
				break
			}
		}
		if !placed {
			groups[0].Players = append(groups[0].Players, p)
		}
	}
	depth, err := s.q.DepthChart(ctx, c, t.ID)
	if err != nil {
		return "", nil, err
	}
	return "roster", data{"T": t, "Season": season, "Groups": groups, "Count": len(roster), "Depth": depthByPos(depth), "Tab": "roster"}, nil
}

func set(xs ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// DepthPos is one position's depth chart column.
type DepthPos struct {
	Position string
	Players  []store.PlayerRef
}

var depthOrder = []string{"QB", "HB", "FB", "WR", "TE", "LT", "LG", "C", "RG", "RT", "LE", "RE", "DT", "LOLB", "MLB", "ROLB", "CB", "FS", "SS", "K", "P", "KR", "PR", "LS"}

func depthByPos(slots []*store.DepthSlot) []DepthPos {
	by := map[string][]store.PlayerRef{}
	for _, d := range slots {
		by[d.Position] = append(by[d.Position], d.Player)
	}
	var out []DepthPos
	for _, p := range depthOrder {
		if ps, ok := by[p]; ok {
			if len(ps) > 3 {
				ps = ps[:3]
			}
			out = append(out, DepthPos{p, ps})
			delete(by, p)
		}
	}
	return out
}

func (s *Site) teamSchedule(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	t, season, err := s.teamHeader(ctx, r, c)
	if err != nil {
		return "", nil, err
	}
	games, err := s.q.TeamGames(ctx, c, season, t.ID)
	if err != nil {
		return "", nil, err
	}
	sched, err := s.cards(ctx, c, season, games, true)
	if err != nil {
		return "", nil, err
	}
	return "schedule", data{"T": t, "Season": season, "Schedule": sched, "Tab": "schedule"}, nil
}

func (s *Site) teamStats(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	t, season, err := s.teamHeader(ctx, r, c)
	if err != nil {
		return "", nil, err
	}
	lines, err := s.q.SeasonLines(ctx, c, season, uuid.Nil, t.ID)
	if err != nil {
		return "", nil, err
	}
	var tables []RenderedTable
	for _, tb := range seasonTables {
		rt := renderSeasonTable(tb, lines, func(l *store.SeasonLine) Row {
			return Row{Label: l.Player.Name(), Href: "/player/" + l.Player.ID.String(), Sub: l.Player.Position}
		}, true)
		if len(rt.Rows) > 0 {
			tables = append(tables, rt)
		}
	}
	games := t.W + t.L
	return "teamstats", data{"T": t, "Season": season, "Tables": tables, "Summary": teamSeasonRows(t.Stats, games), "Tab": "stats"}, nil
}

// renderSeasonTable builds one table, merging a person's rows for the same
// season/team (base + return rows).
func renderSeasonTable(tb Table, lines []*store.SeasonLine, label func(*store.SeasonLine) Row, withTotal bool) RenderedTable {
	rt := RenderedTable{ID: tb.ID, Name: tb.Name, Headers: tb.Cols}
	type acc struct {
		l *store.SeasonLine
		s store.Stats
	}
	merged := map[string]*acc{}
	var order []string
	for _, l := range lines {
		if !tb.groupOK(l.Group) {
			continue
		}
		k := l.Player.ID.String() + "|" + itoa(l.Season) + "|" + itoa(l.TeamID)
		a, ok := merged[k]
		if !ok {
			a = &acc{l: l, s: store.Stats{}}
			merged[k] = a
			order = append(order, k)
		}
		a.s.Add(l.Stats)
	}
	total := store.Stats{}
	for _, k := range order {
		a := merged[k]
		if !tb.Has(a.s) {
			continue
		}
		row := label(a.l)
		row.sortV = tb.Cols[tb.SortBy].Num(a.s)
		row.Season, row.Team = a.l.Season, a.l.TeamID
		for _, col := range tb.Cols {
			row.Cells = append(row.Cells, col.Val(a.s))
		}
		rt.Rows = append(rt.Rows, row)
		total.Add(a.s)
	}
	if withTotal && len(rt.Rows) > 1 {
		tr := Row{Label: "Total"}
		for _, col := range tb.Cols {
			if col.Key == "GP" {
				tr.Cells = append(tr.Cells, "")
				continue
			}
			tr.Cells = append(tr.Cells, col.Val(total))
		}
		rt.Total = &tr
	}
	return rt
}

func (s *Site) player(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return "", nil, errNotFound
	}
	bio, err := s.q.Person(ctx, c, id)
	if err != nil {
		return "", nil, err
	}
	if bio == nil {
		return "", nil, errNotFound
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", nil, err
	}
	lines, err := s.q.SeasonLines(ctx, c, -1, id, -1)
	if err != nil {
		return "", nil, err
	}
	var tables []RenderedTable
	for _, tb := range seasonTables {
		rt := renderSeasonTable(tb, lines, func(l *store.SeasonLine) Row {
			t := teamOr(teams, l.TeamID)
			return Row{Label: fmt.Sprint(c.YearOf(l.Season)), Sub: t.Short, Href: fmt.Sprintf("/team/%d?season=%d", l.TeamID, c.YearOf(l.Season))}
		}, false)
		if len(rt.Rows) == 0 {
			continue
		}
		sort.SliceStable(rt.Rows, func(i, j int) bool { return rt.Rows[i].Season < rt.Rows[j].Season })
		// Career row.
		car := store.Stats{}
		for _, l := range lines {
			if tb.groupOK(l.Group) {
				car.Add(l.Stats)
			}
		}
		tr := Row{Label: "Career"}
		for _, col := range tb.Cols {
			tr.Cells = append(tr.Cells, col.Val(car))
		}
		rt.Total = &tr
		tables = append(tables, rt)
	}
	// Game log for the latest season with games.
	logSeason := c.Season
	if y := atoi(r.URL.Query().Get("season"), 0); y > 0 {
		logSeason = y - c.BaseYear
	}
	glog, err := s.q.PlayerGameLog(ctx, c, logSeason, id)
	if err != nil {
		return "", nil, err
	}
	logTeams, _ := s.q.Teams(ctx, c, logSeason)
	logTables := gameLogTables(glog, logTeams, c.YearOf(logSeason), bio.TeamID)
	awards, err := s.q.Awards(ctx, c, id)
	if err != nil {
		return "", nil, err
	}
	events, err := s.q.Events(ctx, c, id)
	if err != nil {
		return "", nil, err
	}
	raw := rawStatTable(lines, c)
	return "player", data{"P": bio, "Team": teamOr(teams, bio.TeamID), "Tables": tables, "LogTables": logTables, "LogYear": c.YearOf(logSeason),
		"Awards": awards, "Events": events, "Raw": raw, "Teams": teams}, nil
}

// gameLogTables renders a player's game log, one table per stat table with data.
func gameLogTables(logs []*store.GameLog, teams map[int]*store.Team, year, team int) []RenderedTable {
	var out []RenderedTable
	for _, tb := range boxTables {
		rt := RenderedTable{ID: tb.ID, Name: tb.Name, Headers: tb.Cols}
		byGame := map[int]store.Stats{}
		var order []*store.Game
		for _, l := range logs {
			if !tb.groupOK(l.Group) {
				continue
			}
			if _, ok := byGame[l.Game.Row]; !ok {
				byGame[l.Game.Row] = store.Stats{}
				order = append(order, l.Game)
			}
			byGame[l.Game.Row].Add(l.Stats)
		}
		total := store.Stats{}
		for _, g := range order {
			st := byGame[g.Row]
			if !tb.Has(st) {
				continue
			}
			total.Add(st)
			opp, at := teamOr(teams, g.Home), "@ "
			if g.Home == team {
				opp, at = teamOr(teams, g.Away), "vs "
			}
			res := "W"
			if (g.Home == team && g.HomeScore < g.AwayScore) || (g.Away == team && g.AwayScore < g.HomeScore) {
				res = "L"
			}
			row := Row{Label: fmt.Sprintf("%d/%d", g.Month, g.Day), Href: fmt.Sprintf("/game/%d/%d", year, g.Row),
				Sub: fmt.Sprintf("%s%s %s %d-%d", at, opp.Short, res, max(g.HomeScore, g.AwayScore), min(g.HomeScore, g.AwayScore))}
			for _, col := range tb.Cols {
				row.Cells = append(row.Cells, col.Val(st))
			}
			rt.Rows = append(rt.Rows, row)
		}
		if len(rt.Rows) == 0 {
			continue
		}
		tr := Row{Label: "Totals"}
		for _, col := range tb.Cols {
			tr.Cells = append(tr.Cells, col.Val(total))
		}
		rt.Total = &tr
		out = append(out, rt)
	}
	return out
}

// RawTable lists every recorded stat column per season (nothing is dropped).
type RawTable struct {
	Years []int
	Rows  []RawRow
}

// RawRow is one raw stat across seasons.
type RawRow struct {
	Group, Label string
	Values       []int64
}

func rawStatTable(lines []*store.SeasonLine, c *store.Ctx) RawTable {
	seasons := map[int]bool{}
	type key struct{ group, stat string }
	vals := map[key]map[int]int64{}
	for _, l := range lines {
		seasons[l.Season] = true
		for k, v := range l.Stats {
			if hiddenStat[k] {
				continue
			}
			kk := key{l.Group, k}
			if vals[kk] == nil {
				vals[kk] = map[int]int64{}
			}
			if strings.HasSuffix(k, "LONGEST") || strings.HasSuffix(k, "LONGESTRETURN") {
				if v > vals[kk][l.Season] {
					vals[kk][l.Season] = v
				}
			} else {
				vals[kk][l.Season] += v
			}
		}
	}
	var ss []int
	for s := range seasons {
		ss = append(ss, s)
	}
	sort.Ints(ss)
	rt := RawTable{}
	for _, s := range ss {
		rt.Years = append(rt.Years, c.YearOf(s))
	}
	var keys []key
	for k, m := range vals {
		any := false
		for _, v := range m {
			if v != 0 {
				any = true
			}
		}
		if any {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].group != keys[j].group {
			return keys[i].group < keys[j].group
		}
		return rawLabel(keys[i].stat) < rawLabel(keys[j].stat)
	})
	for _, k := range keys {
		row := RawRow{Group: groupName(k.group), Label: rawLabel(k.stat)}
		for _, s := range ss {
			row.Values = append(row.Values, vals[k][s])
		}
		rt.Rows = append(rt.Rows, row)
	}
	return rt
}

func groupName(g string) string {
	switch g {
	case "offense":
		return "Offense"
	case "defense":
		return "Defense"
	case "kicking":
		return "Kicking & Punting"
	case "oline":
		return "Blocking"
	}
	return g
}

func (s *Site) leaders(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season := seasonParam(r, c)
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	lines, err := s.q.LeagueSeasonLines(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	conf := r.URL.Query().Get("conf")
	if conf != "" {
		f := lines[:0]
		for _, l := range lines {
			if t, ok := teams[l.TeamID]; ok && t.Conference == conf {
				f = append(f, l)
			}
		}
		lines = f
	}
	cat := r.URL.Query().Get("cat")
	if cat != "" {
		// Full sortable table for one stat table, like ESPN's stats pages.
		tb := tableFor(seasonTables, cat)
		if tb == nil {
			return "", nil, errNotFound
		}
		sortCol := atoi(r.URL.Query().Get("sort"), tb.SortBy)
		if sortCol < 0 || sortCol >= len(tb.Cols) {
			sortCol = tb.SortBy
		}
		t2 := *tb
		t2.SortBy = sortCol
		rt := renderSeasonTable(t2, lines, func(l *store.SeasonLine) Row {
			return Row{Label: l.Player.Name(), Href: "/player/" + l.Player.ID.String(), Sub: teamOr(teams, l.TeamID).Short}
		}, false)
		sortRows(rt.Rows)
		if len(rt.Rows) > 100 {
			rt.Rows = rt.Rows[:100]
		}
		return "statstable", data{"Table": rt, "Cat": cat, "Sort": sortCol, "Season": season, "Conf": conf, "Confs": conferences(teams), "Tables": seasonTables}, nil
	}
	var boards []LeaderBoard
	for _, ls := range leaderStats {
		lb := s.leaderBoard(ls.ID, lines, teams, 5)
		lb.Short = ls.Table
		boards = append(boards, lb)
	}
	return "leaders", data{"Boards": boards, "Season": season, "Conf": conf, "Confs": conferences(teams), "Tables": seasonTables}, nil
}

func (s *Site) awards(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	all, err := s.q.Awards(ctx, c, uuid.Nil)
	if err != nil {
		return "", nil, err
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", nil, err
	}
	// Major national awards by season, All-America teams for the selected season.
	season := seasonParam(r, c)
	type seasonAwards struct {
		Year  int
		Major []*store.Award
	}
	bySeason := map[int]*seasonAwards{}
	var allAm []*store.Award
	var weekly []*store.Award
	for _, a := range all {
		switch {
		case a.Period == "Season" && !strings.HasPrefix(a.Type, "ALL_AM"):
			sa, ok := bySeason[a.PeriodIndex]
			if !ok {
				sa = &seasonAwards{Year: c.YearOf(a.PeriodIndex)}
				bySeason[a.PeriodIndex] = sa
			}
			sa.Major = append(sa.Major, a)
		case a.Period == "Season" && a.PeriodIndex == season && (a.Type == "ALL_AM_1ST" || a.Type == "ALL_AM_2ND" || a.Type == "ALL_AM_FR"):
			allAm = append(allAm, a)
		case a.Period == "Game" && (a.Type == "Offensive_Player_of_Week" || a.Type == "Defensive_Player_of_Week"):
			weekly = append(weekly, a)
		}
	}
	var seasons []*seasonAwards
	for _, v := range bySeason {
		sort.Slice(v.Major, func(i, j int) bool { return awardOrder(v.Major[i].Type) < awardOrder(v.Major[j].Type) })
		seasons = append(seasons, v)
	}
	sort.Slice(seasons, func(i, j int) bool { return seasons[i].Year > seasons[j].Year })
	sort.SliceStable(weekly, func(i, j int) bool { return weekly[i].PeriodIndex > weekly[j].PeriodIndex })
	history, err := s.q.LeagueAwards(ctx, c)
	if err != nil {
		return "", nil, err
	}
	return "awards", data{"Seasons": seasons, "AllAm": allAm, "Weekly": weekly, "History": history, "Teams": teams, "Season": season}, nil
}

var majorAwardOrder = []string{"HEISMAN", "BEST_PLAYER", "BEST_POTY", "BEST_QB", "BEST_SR_QB", "BEST_RB", "BEST_REC", "BEST_TE", "BEST_C", "BEST_IL", "BEST_DL", "BEST_DE",
	"BEST_DEF_1", "BEST_DEF_2", "BEST_LB", "BEST_DB", "BEST_KICK", "BEST_PUNT", "MOST_VERSATILE", "BEST_FRESHMAN_POTY", "BEST_ACADEMIC", "BEST_SR", "BEST_HC", "BEST_AC"}

func awardOrder(t string) int {
	for i, x := range majorAwardOrder {
		if x == t {
			return i
		}
	}
	return 100
}

func (s *Site) records(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "league"
	}
	name := r.URL.Query().Get("name")
	recs, err := s.q.RecordBook(ctx, c, scope, name)
	if err != nil {
		return "", nil, err
	}
	type board struct {
		Period, Stat string
		Entries      []*store.Record
	}
	var boards []*board
	idx := map[string]*board{}
	for _, x := range recs {
		k := x.Period + x.Stat
		if scope != "league" && name == "" {
			k += x.ScopeName
		}
		b, ok := idx[k]
		if !ok {
			b = &board{Period: x.Period, Stat: x.Stat}
			idx[k] = b
			boards = append(boards, b)
		}
		b.Entries = append(b.Entries, x)
	}
	teams, _ := s.q.Teams(ctx, c, c.Season)
	var names []string
	if scope == "team" {
		for _, t := range teams {
			names = append(names, t.Long)
		}
	} else if scope == "conference" {
		names = conferences(teams)
	}
	sort.Strings(names)
	return "records", data{"Boards": boards, "Scope": scope, "Name": name, "Names": names}, nil
}

func (s *Site) recruiting(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	season := seasonParam(r, c)
	rec, err := s.q.Recruits(ctx, c, season, atoi(r.URL.Query().Get("team"), -1))
	if err != nil {
		return "", nil, err
	}
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return "", nil, err
	}
	// Team class rankings.
	var classes []*store.Team
	for _, t := range teams {
		if t.RecruitRank > 0 {
			classes = append(classes, t)
		}
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i].RecruitRank < classes[j].RecruitRank })
	if len(classes) > 25 {
		classes = classes[:25]
	}
	return "recruiting", data{"Recruits": rec, "Teams": teams, "Classes": classes, "Season": season}, nil
}

func (s *Site) history(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	champs, err := s.q.ConferenceChampions(ctx, c)
	if err != nil {
		return "", nil, err
	}
	riv, err := s.q.Rivalries(ctx, c, -1)
	if err != nil {
		return "", nil, err
	}
	teams, _ := s.q.Teams(ctx, c, c.Season)
	return "history", data{"Champs": champs, "Rivalries": riv, "Teams": teams}, nil
}

func (s *Site) search(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	term := strings.TrimSpace(r.URL.Query().Get("q"))
	teams, _ := s.q.Teams(ctx, c, c.Season)
	var people []store.PlayerRef
	var ts []*store.Team
	if len(term) >= 2 {
		var err error
		people, err = s.q.SearchPeople(ctx, c, term)
		if err != nil {
			return "", nil, err
		}
		lt := strings.ToLower(term)
		for _, t := range teams {
			if strings.Contains(strings.ToLower(t.Long+" "+t.Name+" "+t.Nick+" "+t.Short), lt) {
				ts = append(ts, t)
			}
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	}
	return "search", data{"Q": term, "People": people, "TeamHits": ts, "Teams": teams}, nil
}

func (s *Site) admin(r *http.Request, c *store.Ctx) (string, data, error) {
	a, err := s.q.Admin(r.Context(), c)
	if err != nil {
		return "", nil, err
	}
	teams, _ := s.q.Teams(r.Context(), c, c.Season)
	var noLogo []string
	for _, t := range teams {
		if s.media.TeamLogo(t.Name, t.Long) == "" {
			noLogo = append(noLogo, t.Name)
		}
	}
	sort.Strings(noLogo)
	return "admin", data{"A": a, "Status": s.status(), "SavesDir": s.opt.SavesDir, "NoLogo": noLogo}, nil
}

// ---- JSON API (real-world fields only) ----

func (s *Site) api(f func(r *http.Request, c *store.Ctx) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.context(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		v, err := f(r, c)
		if errors.Is(err, errNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(v)
	}
}

func (s *Site) apiScoreboard(r *http.Request, c *store.Ctx) (any, error) {
	_, d, err := s.scoreboard(r, c)
	if err != nil {
		return nil, err
	}
	return data{"season": c.YearOf(d["Season"].(int)), "weekType": d["WeekType"], "week": d["Week"], "days": d["Days"]}, nil
}

func (s *Site) apiGame(r *http.Request, c *store.Ctx) (any, error) {
	_, d, err := s.game(r, c)
	if err != nil {
		return nil, err
	}
	return data{"game": d["G"], "away": d["Away"], "home": d["Home"], "scoring": d["Scoring"], "awayBox": d["AwayBox"], "homeBox": d["HomeBox"]}, nil
}

func (s *Site) apiTeam(r *http.Request, c *store.Ctx) (any, error) {
	_, d, err := s.team(r, c)
	if err != nil {
		return nil, err
	}
	return data{"team": d["T"], "schedule": d["Schedule"], "coaches": d["Coaches"]}, nil
}

func (s *Site) apiPlayer(r *http.Request, c *store.Ctx) (any, error) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, errNotFound
	}
	bio, err := s.q.Person(r.Context(), c, id)
	if err != nil || bio == nil {
		return nil, errNotFound
	}
	lines, err := s.q.SeasonLines(r.Context(), c, -1, id, -1)
	if err != nil {
		return nil, err
	}
	type season struct {
		Year   int         `json:"year"`
		TeamID int         `json:"teamId"`
		Group  string      `json:"group"`
		Stats  store.Stats `json:"stats"`
	}
	var ss []season
	for _, l := range lines {
		st := store.Stats{}
		for k, v := range l.Stats {
			if !hiddenStat[k] {
				st[k] = v
			}
		}
		ss = append(ss, season{c.YearOf(l.Season), l.TeamID, l.Group, st})
	}
	return data{"player": bio, "seasons": ss}, nil
}

func (s *Site) apiStandings(r *http.Request, c *store.Ctx) (any, error) {
	return s.standingsData(r.Context(), c, seasonParam(r, c))
}
