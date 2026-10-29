package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Read side. Every query is scoped to the active branch of the dynasty with
// the most recent save, so the site never shows superseded history.

// Stats is a stat line keyed by the game's raw column names.
type Stats map[string]int64

// Add sums another line into s.
func (s Stats) Add(o Stats) {
	for k, v := range o {
		switch k {
		case "SEAS_YEAR", "YEARBYYEARTEAMINDEX", "STAT_KEEP":
			s[k] = v
		default:
			if strings.HasSuffix(k, "LONGEST") || strings.HasSuffix(k, "LONGESTRETURN") {
				if v > s[k] {
					s[k] = v
				}
				continue
			}
			s[k] += v
		}
	}
}

// Group maps a raw stat category to the display group it belongs to. The
// game splits a player's line across a base row and a kick/punt-return row;
// they are disjoint and sum to the team totals.
func Group(category string) string {
	switch category {
	case "offense", "returns_off":
		return "offense"
	case "defense", "returns_def":
		return "defense"
	}
	return category
}

// Ctx is the site-wide context: which dynasty/branch/season is current.
type Ctx struct {
	Dynasty    int
	Name       string // dynasty name, e.g. "Texas State"
	LeagueID   int64
	Branch     int
	Season     int // season index
	Year       int // calendar year of Season
	BaseYear   int
	Week       int
	WeekType   string
	Stage      string
	SaveName   string
	LastUpdate time.Time
	Seasons    []int
	Snapshots  int
}

// YearOf converts a season index to a calendar year.
func (c *Ctx) YearOf(season int) int { return c.BaseYear + season }

// Queries serves read models with a small cache. Everything is scoped to one
// dynasty (and its active branch); dynasties never share rows.
type Queries struct {
	s     *Store
	mu    sync.Mutex
	ctx   map[int]*Ctx // by dynasty id (0 = most recently played)
	at    map[int]time.Time
	teams map[[2]int]map[int]*Team // (branch, season)
	memo_ memoCache
}

func NewQueries(s *Store) *Queries {
	return &Queries{s: s, ctx: map[int]*Ctx{}, at: map[int]time.Time{}, teams: map[[2]int]map[int]*Team{}}
}

// Invalidate drops caches after an ingest.
func (q *Queries) Invalidate() {
	q.mu.Lock()
	q.ctx = map[int]*Ctx{}
	q.at = map[int]time.Time{}
	q.teams = map[[2]int]map[int]*Team{}
	q.mu.Unlock()
	q.clearMemo()
}

// ErrNoData means nothing has been ingested yet.
var ErrNoData = errors.New("no dynasty data ingested yet")

// Dynasty is one dynasty (save lineage) for the switcher.
type Dynasty struct {
	ID         int
	LeagueID   int64
	Name       string
	UserTeamID int
	Coach      string
	SaveName   string
	LastSave   *time.Time
	Year       int
	Snapshots  int
}

// Dynasties lists every dynasty, most recently played first.
func (q *Queries) Dynasties(ctx context.Context) ([]*Dynasty, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT d.id, d.league_id, d.name, d.user_team_id, d.coach_name, d.save_name, d.last_save_at,
		COALESCE((SELECT s.year FROM snapshot s JOIN branch b ON b.id = s.branch_id AND b.status = 'active' WHERE s.dynasty_id = d.id ORDER BY s.pos DESC LIMIT 1), 0),
		(SELECT count(*) FROM snapshot s WHERE s.dynasty_id = d.id)
		FROM dynasty d ORDER BY d.last_save_at DESC NULLS LAST, d.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Dynasty
	for rows.Next() {
		d := &Dynasty{}
		if err := rows.Scan(&d.ID, &d.LeagueID, &d.Name, &d.UserTeamID, &d.Coach, &d.SaveName, &d.LastSave, &d.Year, &d.Snapshots); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Context returns the site context for a dynasty (0 = most recently played).
func (q *Queries) Context(ctx context.Context, dynasty int) (*Ctx, error) {
	q.mu.Lock()
	if c, ok := q.ctx[dynasty]; ok && time.Since(q.at[dynasty]) < 30*time.Second {
		q.mu.Unlock()
		return c, nil
	}
	q.mu.Unlock()
	c := &Ctx{}
	// One round trip: the dynasty's newest snapshot on the active branch, then
	// the latest position on that branch (a re-ingest of an old file must not
	// move the site backwards), the seasons with data, and the snapshot count.
	var seasons []int32
	err := q.s.DB.QueryRow(ctx, `WITH cur AS (
			SELECT d.id AS dynasty, d.league_id, d.base_year, d.name, b.id AS branch, s.season_index, s.year, s.week, s.week_type, s.stage, s.save_name, s.ingested_at
			FROM snapshot s JOIN branch b ON b.id = s.branch_id AND b.status = 'active' JOIN dynasty d ON d.id = s.dynasty_id
			WHERE ($1 = 0 OR d.id = $1)
			ORDER BY d.last_save_at DESC NULLS LAST, s.pos DESC, s.id DESC LIMIT 1)
		SELECT cur.dynasty, cur.league_id, cur.base_year, cur.name, cur.branch,
			COALESCE(lat.season_index, cur.season_index), COALESCE(lat.year, cur.year), COALESCE(lat.week, cur.week), COALESCE(lat.week_type, cur.week_type),
			COALESCE(lat.stage, cur.stage), COALESCE(lat.save_name, cur.save_name), cur.ingested_at,
			ARRAY(SELECT season_index FROM player_season_stat WHERE branch_id = cur.branch
				UNION SELECT season_index FROM game WHERE branch_id = cur.branch ORDER BY 1 DESC),
			(SELECT count(*) FROM snapshot WHERE dynasty_id = cur.dynasty)
		FROM cur LEFT JOIN LATERAL (SELECT season_index, year, week, week_type, stage, save_name FROM snapshot
			WHERE branch_id = cur.branch ORDER BY pos DESC, id DESC LIMIT 1) lat ON true`, dynasty).
		Scan(&c.Dynasty, &c.LeagueID, &c.BaseYear, &c.Name, &c.Branch, &c.Season, &c.Year, &c.Week, &c.WeekType, &c.Stage, &c.SaveName, &c.LastUpdate,
			&seasons, &c.Snapshots)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoData
	}
	if err != nil {
		return nil, err
	}
	if c.BaseYear == 0 {
		c.BaseYear = c.Year - c.Season
	}
	for _, s := range seasons {
		if s >= 0 {
			c.Seasons = append(c.Seasons, int(s))
		}
	}
	q.mu.Lock()
	q.ctx[dynasty], q.at[dynasty] = c, time.Now()
	q.mu.Unlock()
	return c, nil
}

// Team is a program with its record for one season.
type Team struct {
	ID           int
	Name         string // display name, e.g. "Alabama"
	Short        string // abbreviation, e.g. "ALA"
	Long         string
	Nick         string
	Asset        string
	Color        string
	AltColor     string
	Conference   string
	Division     string // "" unless the conference is split
	W, L         int
	CW, CL       int
	ConfStanding int
	Streak       int
	AP           int
	Coaches      int
	CFP          int
	OffRank      int
	DefRank      int
	RecruitRank  int
	HeadCoachRow int
	Stats        Stats
	HasSeason    bool
}

// Record formats W-L.
func (t *Team) Record() string { return itoa(t.W) + "-" + itoa(t.L) }

// ConfRecord formats conference W-L.
func (t *Team) ConfRecord() string { return itoa(t.CW) + "-" + itoa(t.CL) }

// Rank returns the CFP rank when published, else the AP rank (0 = unranked).
func (t *Team) Rank() int {
	if t.CFP > 0 && t.CFP <= 25 {
		return t.CFP
	}
	if t.AP > 0 && t.AP <= 25 {
		return t.AP
	}
	return 0
}

func itoa(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	b := []byte{}
	for {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// Teams returns every program with its record for a season.
func (q *Queries) Teams(ctx context.Context, c *Ctx, season int) (map[int]*Team, error) {
	q.mu.Lock()
	if m, ok := q.teams[[2]int{c.Branch, season}]; ok {
		q.mu.Unlock()
		return m, nil
	}
	q.mu.Unlock()
	rows, err := q.s.DB.Query(ctx, `SELECT t.team_id, t.display_name, t.short_name, t.long_name, t.nick_name, t.asset_name, t.primary_color, t.secondary_color,
		ts.conference, ts.wins, ts.losses, ts.conf_wins, ts.conf_losses, ts.conf_standing, ts.streak, ts.ap_rank, ts.coaches_rank, ts.cfp_rank,
		ts.off_rank, ts.def_rank, ts.recruiting_rank, ts.head_coach_row, ts.stats, ts.team_id IS NOT NULL, COALESCE(ts.division, '')
		FROM team t LEFT JOIN team_season ts ON ts.team_id = t.team_id AND ts.branch_id = $2 AND ts.season_index = $3
		WHERE t.dynasty_id = $1`, c.Dynasty, c.Branch, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[int]*Team{}
	for rows.Next() {
		t := &Team{}
		var conf *string
		var w, l, cw, cl, cs, st, ap, co, cfp, off, def, rr, hc *int
		var stats []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.Short, &t.Long, &t.Nick, &t.Asset, &t.Color, &t.AltColor, &conf, &w, &l, &cw, &cl, &cs, &st, &ap, &co, &cfp,
			&off, &def, &rr, &hc, &stats, &t.HasSeason, &t.Division); err != nil {
			return nil, err
		}
		if conf != nil {
			t.Conference = *conf
		}
		for _, p := range []struct {
			dst *int
			src *int
		}{{&t.W, w}, {&t.L, l}, {&t.CW, cw}, {&t.CL, cl}, {&t.ConfStanding, cs}, {&t.Streak, st}, {&t.AP, ap}, {&t.Coaches, co}, {&t.CFP, cfp},
			{&t.OffRank, off}, {&t.DefRank, def}, {&t.RecruitRank, rr}, {&t.HeadCoachRow, hc}} {
			if p.src != nil {
				*p.dst = *p.src
			}
		}
		if t.Color == "" {
			t.Color = "#444444"
		}
		json.Unmarshal(stats, &t.Stats)
		m[t.ID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	results, err := q.SeasonResults(ctx, c, season)
	if err != nil {
		return nil, err
	}
	recs := tallyRecords(results, 1<<30)
	for id, t := range m {
		if r, ok := recs[id]; ok {
			r.apply(&t.W, &t.L, &t.CW, &t.CL, &t.Streak)
		}
	}
	q.mu.Lock()
	q.teams[[2]int{c.Branch, season}] = m
	q.mu.Unlock()
	return m, nil
}

// Game is one game on the schedule.
type Game struct {
	Season       int
	Row          int
	Week         int
	WeekType     string
	Status       string
	Played       bool
	Home, Away   int
	HomeScore    int
	AwayScore    int
	HomeQ, AwayQ []int32
	HomeOT       int
	AwayOT       int
	Overtime     bool
	Month, Day   int
	DayOfWeek    string
	TimeOfDay    int
	Network      string
	Attendance   int
	Temperature  int
	Weather      string
	Wind         string
	WindSpeed    int
	Stadium      string
	BowlName     string
	GameOfWeek   bool
	GameType     string // see Kind
	HomeBox      Stats
	AwayBox      Stats
}

// Kind classifies the game: conference, nonconference, conf_championship,
// bowl, playoff or national_championship. Games stored before the game type
// was recorded fall back to what the bowl name says.
func (g *Game) Kind() string {
	if g.GameType != "" {
		return g.GameType
	}
	switch {
	case strings.Contains(g.BowlName, "National Championship"):
		return "national_championship"
	case strings.HasPrefix(g.BowlName, "CFP"):
		return "playoff"
	case g.BowlName != "":
		return "bowl"
	}
	return ""
}

// TypeLabel is the short tag shown on scoreboards and schedules.
func (g *Game) TypeLabel() string {
	switch g.Kind() {
	case "conference":
		return "Conference"
	case "nonconference":
		return "Non-Conference"
	case "conf_championship":
		return "Conference Championship"
	case "bowl", "playoff":
		return g.BowlName
	case "national_championship":
		return "National Championship"
	}
	return ""
}

// Postseason reports bowl and CFP games.
func (g *Game) Postseason() bool {
	switch g.Kind() {
	case "bowl", "playoff", "national_championship":
		return true
	}
	return false
}

const gameCols = `season_index, game_row, week, week_type, status, played, home_team, away_team, home_score, away_score, home_quarters, away_quarters,
	home_ot, away_ot, overtime, month, day, day_of_week, time_of_day, network, attendance, temperature, weather, wind, wind_speed, stadium, bowl_name,
	game_of_week, home_box, away_box, game_type`

func scanGames(rows pgx.Rows) ([]*Game, error) {
	defer rows.Close()
	var out []*Game
	for rows.Next() {
		g := &Game{}
		var hb, ab []byte
		if err := rows.Scan(&g.Season, &g.Row, &g.Week, &g.WeekType, &g.Status, &g.Played, &g.Home, &g.Away, &g.HomeScore, &g.AwayScore, &g.HomeQ, &g.AwayQ,
			&g.HomeOT, &g.AwayOT, &g.Overtime, &g.Month, &g.Day, &g.DayOfWeek, &g.TimeOfDay, &g.Network, &g.Attendance, &g.Temperature, &g.Weather, &g.Wind,
			&g.WindSpeed, &g.Stadium, &g.BowlName, &g.GameOfWeek, &hb, &ab, &g.GameType); err != nil {
			return nil, err
		}
		json.Unmarshal(hb, &g.HomeBox)
		json.Unmarshal(ab, &g.AwayBox)
		out = append(out, g)
	}
	return out, rows.Err()
}

// WeekKey identifies a scoreboard week.
type WeekKey struct {
	WeekType string
	Week     int
	Games    int
	Played   int
}

// ID drops the counts, for use as a map key.
func (w WeekKey) ID() WeekID { return WeekID{w.WeekType, w.Week} }

// WeekID names one week of a season.
type WeekID struct {
	WeekType string
	Week     int
}

// Weeks lists the weeks that have games in a season, in calendar order.
func (q *Queries) Weeks(ctx context.Context, c *Ctx, season int) ([]WeekKey, error) {
	v, err := q.memo(c, memoKey("weeks", season), func() (any, error) { return q.weeks(ctx, c, season) })
	if err != nil {
		return nil, err
	}
	return append([]WeekKey(nil), v.([]WeekKey)...), nil
}

func (q *Queries) weeks(ctx context.Context, c *Ctx, season int) ([]WeekKey, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT week_type, week, count(*), count(*) FILTER (WHERE played) FROM game WHERE branch_id = $1 AND season_index = $2
		GROUP BY 1,2 ORDER BY 2, 1`, c.Branch, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeekKey
	for rows.Next() {
		var w WeekKey
		rows.Scan(&w.WeekType, &w.Week, &w.Games, &w.Played)
		out = append(out, w)
	}
	return out, rows.Err()
}

// GamesForWeeks returns several scoreboard weeks in one query, each in
// GamesForWeek order.
func (q *Queries) GamesForWeeks(ctx context.Context, c *Ctx, season int, weeks []WeekID) (map[WeekID][]*Game, error) {
	// Cache per week so different week sets share entries.
	out := map[WeekID][]*Game{}
	var missing []WeekID
	for _, w := range weeks {
		if v, ok := q.peek(c, memoKey("week", season, w)); ok {
			out[w] = append([]*Game(nil), v.([]*Game)...)
		} else {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	got, err := q.gamesForWeeks(ctx, c, season, missing)
	if err != nil {
		return nil, err
	}
	for _, w := range missing {
		gs := got[w]
		q.memo(c, memoKey("week", season, w), func() (any, error) { return gs, nil })
		out[w] = append([]*Game(nil), gs...)
	}
	return out, nil
}

func (q *Queries) gamesForWeeks(ctx context.Context, c *Ctx, season int, weeks []WeekID) (map[WeekID][]*Game, error) {
	out := map[WeekID][]*Game{}
	if len(weeks) == 0 {
		return out, nil
	}
	types, nums := make([]string, len(weeks)), make([]int32, len(weeks))
	for i, w := range weeks {
		types[i], nums[i] = w.WeekType, int32(w.Week)
	}
	rows, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = $2
		AND (week_type, week) IN (SELECT * FROM unnest($3::text[], $4::int[]))
		ORDER BY game_of_week DESC, month, day, time_of_day, game_row`, c.Branch, season, types, nums)
	if err != nil {
		return nil, err
	}
	gs, err := scanGames(rows)
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		k := WeekID{g.WeekType, g.Week}
		out[k] = append(out[k], g)
	}
	return out, nil
}

// GamesForWeek returns a scoreboard week.
func (q *Queries) GamesForWeek(ctx context.Context, c *Ctx, season int, weekType string, week int) ([]*Game, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = $2 AND week_type = $3 AND week = $4
		ORDER BY game_of_week DESC, month, day, time_of_day, game_row`, c.Branch, season, weekType, week)
	if err != nil {
		return nil, err
	}
	return scanGames(rows)
}

// TeamGames returns a team's schedule for a season.
func (q *Queries) TeamGames(ctx context.Context, c *Ctx, season, team int) ([]*Game, error) {
	v, err := q.memo(c, memoKey("teamgames", season, team), func() (any, error) {
		rows, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = $2 AND (home_team = $3 OR away_team = $3)
			ORDER BY week, month, day`, c.Branch, season, team)
		if err != nil {
			return nil, err
		}
		return scanGames(rows)
	})
	if err != nil {
		return nil, err
	}
	return append([]*Game(nil), v.([]*Game)...), nil
}

// TeamPoints is a team's scoring from played games in one season.
type TeamPoints struct {
	Games, For, Against int
}

// SeasonPoints totals points scored and allowed per team over a season's
// played games (the TeamStats row carries no points).
func (q *Queries) SeasonPoints(ctx context.Context, c *Ctx, season int) (map[int]TeamPoints, error) {
	v, err := q.memo(c, memoKey("points", season), func() (any, error) { return q.seasonPoints(ctx, c, season) })
	if err != nil {
		return nil, err
	}
	out := map[int]TeamPoints{}
	for k, p := range v.(map[int]TeamPoints) {
		out[k] = p
	}
	return out, nil
}

func (q *Queries) seasonPoints(ctx context.Context, c *Ctx, season int) (map[int]TeamPoints, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT home_team, away_team, home_score, away_score FROM game
		WHERE branch_id = $1 AND season_index = $2 AND played`, c.Branch, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]TeamPoints{}
	for rows.Next() {
		var h, a, hs, as int
		if err := rows.Scan(&h, &a, &hs, &as); err != nil {
			return nil, err
		}
		for _, side := range [][3]int{{h, hs, as}, {a, as, hs}} {
			tp := out[side[0]]
			tp.Games++
			tp.For += side[1]
			tp.Against += side[2]
			out[side[0]] = tp
		}
	}
	return out, rows.Err()
}

// Result is the slim view of a game the predictor needs.
type Result struct {
	Week                 int
	Home, Away           int
	HomeScore, AwayScore int
	Played               bool
	Neutral              bool // conference title games, bowls and playoffs
	Conference           bool // counts toward the conference record
}

// SeasonResults lists every game of a season (played or not) without box
// scores, in week order.
func (q *Queries) SeasonResults(ctx context.Context, c *Ctx, season int) ([]Result, error) {
	v, err := q.memo(c, memoKey("results", season), func() (any, error) { return q.seasonResults(ctx, c, season) })
	if err != nil {
		return nil, err
	}
	return append([]Result(nil), v.([]Result)...), nil
}

func (q *Queries) seasonResults(ctx context.Context, c *Ctx, season int) ([]Result, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT week, home_team, away_team, home_score, away_score, played, game_type, bowl_name FROM game
		WHERE branch_id = $1 AND season_index = $2 ORDER BY week, game_row`, c.Branch, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Result
	for rows.Next() {
		var r Result
		var g Game
		if err := rows.Scan(&r.Week, &r.Home, &r.Away, &r.HomeScore, &r.AwayScore, &r.Played, &g.GameType, &g.BowlName); err != nil {
			return nil, err
		}
		switch g.Kind() {
		case "conf_championship", "bowl", "playoff", "national_championship":
			r.Neutral = true
		case "conference":
			r.Conference = true
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Game returns one game.
func (q *Queries) Game(ctx context.Context, c *Ctx, season, row int) (*Game, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = $2 AND game_row = $3`, c.Branch, season, row)
	if err != nil {
		return nil, err
	}
	gs, err := scanGames(rows)
	if err != nil || len(gs) == 0 {
		return nil, err
	}
	return gs[0], nil
}

// PlayerRef names a person as shown on a page.
type PlayerRef struct {
	ID        uuid.UUID
	First     string
	Last      string
	Position  string
	Jersey    int
	TeamID    int
	Portrait  string
	ClassYear string
}

// Name returns "First Last".
func (p PlayerRef) Name() string { return strings.TrimSpace(p.First + " " + p.Last) }

// Short returns "F. Last".
func (p PlayerRef) Short() string {
	if p.First == "" {
		return p.Last
	}
	return p.First[:1] + ". " + p.Last
}

// Line is a player's stat line in one display group.
type Line struct {
	Player PlayerRef
	TeamID int
	Group  string
	Stats  Stats
}

// GameLines returns every player line for a game, merged by display group.
func (q *Queries) GameLines(ctx context.Context, c *Ctx, season, row int) ([]*Line, error) {
	m, err := q.GamesLines(ctx, c, season, []int{row})
	return m[row], err
}

// GamesLines is GameLines for several games of a season in one query, keyed
// by game row.
func (q *Queries) GamesLines(ctx context.Context, c *Ctx, season int, games []int) (map[int][]*Line, error) {
	return q.gamesLines(ctx, c, season, games, "s.stats", "")
}

// leaderKeys are the only stats score cards read (passing, rushing and
// receiving leaders). They appear on both offense and returns_off lines
// (the game files some players' rushing under its KP/return stats table),
// which together make up the "offense" group.
var leaderKeys = []string{"PASSYARDS", "PASSCOMPLETED", "PASSATTEMPTS", "PASSTDS", "RUSHYARDS", "RUSHATTEMPTS", "RUSHTDS",
	"RECEIVEYARDS", "RECEIVECATCHES", "RECEIVETDS"}

// GamesLeaderLines is GamesLines trimmed to the offense group and
// leaderKeys, a fraction of the bytes, for schedule and scoreboard cards.
func (q *Queries) GamesLeaderLines(ctx context.Context, c *Ctx, season int, games []int) (map[int][]*Line, error) {
	pairs := make([]string, len(leaderKeys))
	for i, k := range leaderKeys {
		pairs[i] = "'" + k + "', s.stats->'" + k + "'"
	}
	return q.gamesLines(ctx, c, season, games, "jsonb_strip_nulls(jsonb_build_object("+strings.Join(pairs, ", ")+"))", "AND s.category IN ('offense', 'returns_off')")
}

func (q *Queries) gamesLines(ctx context.Context, c *Ctx, season int, games []int, statsExpr, where string) (map[int][]*Line, error) {
	out := map[int][]*Line{}
	if len(games) == 0 {
		return out, nil
	}
	list := make([]int32, len(games))
	for i, g := range games {
		list[i] = int32(g)
	}
	rows, err := q.s.DB.Query(ctx, `SELECT s.game_row, s.person_id, p.first_name, p.last_name, COALESCE(ps.position, ''), COALESCE(ps.jersey, 0), COALESCE(ps.portrait, ''),
		s.team_id, s.category, `+statsExpr+`
		FROM player_game_stat s JOIN person p ON p.id = s.person_id
		LEFT JOIN player_season ps ON ps.branch_id = s.branch_id AND ps.season_index = s.season_index AND ps.person_id = s.person_id
		WHERE s.branch_id = $1 AND s.season_index = $2 AND s.game_row = ANY($3::int[]) `+where, c.Branch, season, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct {
		game  int
		group string
		id    uuid.UUID
	}
	idx := map[key]*Line{}
	for rows.Next() {
		var pr PlayerRef
		var game, team int
		var cat string
		var raw []byte
		if err := rows.Scan(&game, &pr.ID, &pr.First, &pr.Last, &pr.Position, &pr.Jersey, &pr.Portrait, &team, &cat, &raw); err != nil {
			return nil, err
		}
		var st Stats
		json.Unmarshal(raw, &st)
		g := Group(cat)
		k := key{game, g, pr.ID}
		l, ok := idx[k]
		if !ok {
			l = &Line{Player: pr, TeamID: team, Group: g, Stats: Stats{}}
			idx[k] = l
			out[game] = append(out[game], l)
		}
		l.Stats.Add(st)
	}
	return out, rows.Err()
}

// ScoringPlay is a scoring summary entry with participants resolved.
type ScoringPlay struct {
	Seq          int
	Quarter      int
	ClockSec     int
	HomeScore    int
	AwayScore    int
	HomePrev     int
	AwayPrev     int
	Conversion   string
	ScoringTeam  string
	Participants []Participant
}

// Participant is a team leader's cumulative line at the time of a score.
type Participant struct {
	Player PlayerRef
	Stats  Stats
}

// ScoringPlays returns the stored scoring summary for a game.
func (q *Queries) ScoringPlays(ctx context.Context, c *Ctx, season, row int) ([]*ScoringPlay, error) {
	// The plays and every participant's bio go in one batch (one round trip).
	b := &pgx.Batch{}
	b.Queue(`SELECT seq, quarter, clock_sec, home_score, away_score, home_prev, away_prev, conversion, scoring_team, participants
		FROM scoring_play WHERE branch_id = $1 AND season_index = $2 AND game_row = $3 ORDER BY seq`, c.Branch, season, row)
	b.Queue(playerRefSelect+`p.id IN (SELECT (e->>'personId')::uuid FROM scoring_play sp,
		jsonb_array_elements(CASE WHEN jsonb_typeof(sp.participants) = 'array' THEN sp.participants ELSE '[]'::jsonb END) e
		WHERE sp.branch_id = $1 AND sp.season_index = $2 AND sp.game_row = $3 AND e->>'personId' IS NOT NULL)`, c.Branch, season, row)
	br := q.s.DB.SendBatch(ctx, b)
	defer br.Close()
	rows, err := br.Query()
	if err != nil {
		return nil, err
	}
	var out []*ScoringPlay
	type rawPart struct {
		PersonID *uuid.UUID `json:"personId"`
		Stats    Stats      `json:"stats"`
	}
	raws := [][]rawPart{}
	for rows.Next() {
		p := &ScoringPlay{}
		var raw []byte
		if err := rows.Scan(&p.Seq, &p.Quarter, &p.ClockSec, &p.HomeScore, &p.AwayScore, &p.HomePrev, &p.AwayPrev, &p.Conversion, &p.ScoringTeam, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var parts []rawPart
		json.Unmarshal(raw, &parts)
		raws = append(raws, parts)
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	refRows, err := br.Query()
	if err != nil {
		return nil, err
	}
	refs, err := scanPlayerRefs(refRows)
	if err != nil {
		return nil, err
	}
	for i, parts := range raws {
		for _, rp := range parts {
			pt := Participant{Stats: rp.Stats}
			if rp.PersonID != nil {
				pt.Player = refs[*rp.PersonID]
			}
			out[i].Participants = append(out[i].Participants, pt)
		}
	}
	return out, nil
}

// playerRefSelect selects PlayerRefs with their bio for season $2 (latest
// otherwise); append the WHERE condition.
const playerRefSelect = `SELECT p.id, p.first_name, p.last_name, COALESCE(ps.position, ''), COALESCE(ps.jersey, 0), COALESCE(ps.team_id, -1), COALESCE(ps.portrait, ''), COALESCE(ps.school_year, '')
	FROM person p LEFT JOIN LATERAL (SELECT * FROM player_season x WHERE x.branch_id = $1 AND x.person_id = p.id ORDER BY (x.season_index = $2) DESC, x.season_index DESC LIMIT 1) ps ON true
	WHERE `

func scanPlayerRefs(rows pgx.Rows) (map[uuid.UUID]PlayerRef, error) {
	defer rows.Close()
	out := map[uuid.UUID]PlayerRef{}
	for rows.Next() {
		var r PlayerRef
		if err := rows.Scan(&r.ID, &r.First, &r.Last, &r.Position, &r.Jersey, &r.TeamID, &r.Portrait, &r.ClassYear); err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	return out, rows.Err()
}

// PlayerRefs resolves people with their bio for a season (latest otherwise).
func (q *Queries) PlayerRefs(ctx context.Context, c *Ctx, season int, ids map[uuid.UUID]bool) (map[uuid.UUID]PlayerRef, error) {
	list := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	if len(list) == 0 {
		return map[uuid.UUID]PlayerRef{}, nil
	}
	rows, err := q.s.DB.Query(ctx, playerRefSelect+`p.id = ANY($3::uuid[])`, c.Branch, season, list)
	if err != nil {
		return nil, err
	}
	return scanPlayerRefs(rows)
}

// SeasonLine is a person's season totals in one display group for one team.
type SeasonLine struct {
	Player   PlayerRef
	Redshirt string // the player's redshirt status that season
	Season   int
	TeamID   int
	Group    string
	Stats    Stats
}

// SeasonLines returns season lines filtered by season (or -1 for all),
// person (uuid.Nil for all) and team (-1 for all).
func (q *Queries) SeasonLines(ctx context.Context, c *Ctx, season int, person uuid.UUID, team int) ([]*SeasonLine, error) {
	if person != uuid.Nil || team < 0 {
		return q.seasonLines(ctx, c, season, person, team, "s.stats")
	}
	// One team's season (team stats page, game previews): cached.
	v, err := q.memo(c, memoKey("teamlines", season, team), func() (any, error) {
		return q.seasonLines(ctx, c, season, person, team, "s.stats")
	})
	if err != nil {
		return nil, err
	}
	return append([]*SeasonLine(nil), v.([]*SeasonLine)...), nil
}

// LeagueSeasonLines is SeasonLines for every player in a season with
// zero-valued stats left out, which more than halves what crosses the
// network. Only for callers that read stats by key (a missing key reads as
// zero), such as leader boards; not for output that lists the keys.
func (q *Queries) LeagueSeasonLines(ctx context.Context, c *Ctx, season int) ([]*SeasonLine, error) {
	return q.seasonLines(ctx, c, season, uuid.Nil, -1,
		`COALESCE((SELECT jsonb_object_agg(k, v) FROM jsonb_each(s.stats) e(k, v) WHERE v <> '0'::jsonb), '{}'::jsonb)`)
}

func (q *Queries) seasonLines(ctx context.Context, c *Ctx, season int, person uuid.UUID, team int, statsExpr string) ([]*SeasonLine, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT s.season_index, s.person_id, p.first_name, p.last_name, COALESCE(ps.position, ''), COALESCE(ps.jersey, 0), COALESCE(ps.portrait, ''),
		COALESCE(ps.school_year, ''), COALESCE(ps.redshirt, ''), s.team_id, s.category, `+statsExpr+`
		FROM player_season_stat s JOIN person p ON p.id = s.person_id
		LEFT JOIN (SELECT DISTINCT ON (x.person_id) x.* FROM player_season x WHERE x.branch_id = $1 AND ($3::uuid IS NULL OR x.person_id = $3)
			ORDER BY x.person_id, (x.season_index = $2) DESC, x.season_index DESC) ps ON ps.person_id = s.person_id
		WHERE s.branch_id = $1 AND ($2 < 0 OR s.season_index = $2) AND ($3::uuid IS NULL OR s.person_id = $3) AND ($4 < 0 OR s.team_id = $4)`,
		c.Branch, season, nullUUID(person), team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]*SeasonLine{}
	var out []*SeasonLine
	for rows.Next() {
		var l SeasonLine
		var cat string
		var raw []byte
		if err := rows.Scan(&l.Season, &l.Player.ID, &l.Player.First, &l.Player.Last, &l.Player.Position, &l.Player.Jersey, &l.Player.Portrait, &l.Player.ClassYear,
			&l.Redshirt, &l.TeamID, &cat, &raw); err != nil {
			return nil, err
		}
		var st Stats
		json.Unmarshal(raw, &st)
		l.Group = Group(cat)
		l.Player.TeamID = l.TeamID
		k := l.Player.ID.String() + "|" + itoa(l.Season) + "|" + itoa(l.TeamID) + "|" + l.Group
		if e, ok := idx[k]; ok {
			e.Stats.Add(st)
			continue
		}
		l.Stats = Stats{}
		l.Stats.Add(st)
		ll := l
		idx[k] = &ll
		out = append(out, &ll)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Season < out[j].Season })
	return out, rows.Err()
}

func nullUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}

// GameLog is a player's line in one game.
type GameLog struct {
	Game  *Game
	Team  int // the team the player played for in this game
	Group string
	Stats Stats
}

// LogSeason is a season in which a person has game-by-game lines, and the
// team they played for (the one with the most games, should they have moved
// mid-season).
type LogSeason struct {
	Season int
	Team   int
}

// PlayerLogSeasons lists the seasons with game-by-game lines, newest first.
func (q *Queries) PlayerLogSeasons(ctx context.Context, c *Ctx, person uuid.UUID) ([]LogSeason, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT DISTINCT ON (season_index) season_index, team_id FROM player_game_stat
		WHERE branch_id = $1 AND person_id = $2
		GROUP BY season_index, team_id ORDER BY season_index DESC, count(DISTINCT game_row) DESC, team_id`, c.Branch, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogSeason
	for rows.Next() {
		var ls LogSeason
		if err := rows.Scan(&ls.Season, &ls.Team); err != nil {
			return nil, err
		}
		out = append(out, ls)
	}
	return out, rows.Err()
}

// PlayerGameLog returns a person's game lines for a season.
func (q *Queries) PlayerGameLog(ctx context.Context, c *Ctx, season int, person uuid.UUID) ([]*GameLog, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT s.game_row, s.team_id, s.category, s.stats FROM player_game_stat s WHERE s.branch_id = $1 AND s.season_index = $2 AND s.person_id = $3`,
		c.Branch, season, person)
	if err != nil {
		return nil, err
	}
	type key struct {
		row   int
		group string
	}
	acc := map[key]Stats{}
	team := map[int]int{}
	var keys []key
	for rows.Next() {
		var row, tm int
		var cat string
		var raw []byte
		if err := rows.Scan(&row, &tm, &cat, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var st Stats
		json.Unmarshal(raw, &st)
		team[row] = tm
		k := key{row, Group(cat)}
		if _, ok := acc[k]; !ok {
			acc[k] = Stats{}
			keys = append(keys, k)
		}
		acc[k].Add(st)
	}
	rows.Close()
	gameRows := map[int]*Game{}
	if len(keys) > 0 {
		list := []int32{}
		for _, k := range keys {
			list = append(list, int32(k.row))
		}
		gr, err := q.s.DB.Query(ctx, `SELECT `+gameCols+` FROM game WHERE branch_id = $1 AND season_index = $2 AND game_row = ANY($3::int[])`, c.Branch, season, list)
		if err != nil {
			return nil, err
		}
		gs, err := scanGames(gr)
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			gameRows[g.Row] = g
		}
	}
	var out []*GameLog
	for _, k := range keys {
		if g := gameRows[k.row]; g != nil {
			out = append(out, &GameLog{Game: g, Team: team[k.row], Group: k.group, Stats: acc[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Game.Week < out[j].Game.Week })
	return out, nil
}

// Bio is a person's profile.
type Bio struct {
	PlayerRef
	HomeTown    string
	HomeState   string
	Height      int
	Weight      int
	Stars       int
	Redshirt    string
	OnRoster    bool
	Captain     bool
	Injury      string
	BioSeason   int
	FirstSeason int
	Provisional bool
}

// Person returns a person's profile with their most recent season bio.
func (q *Queries) Person(ctx context.Context, c *Ctx, id uuid.UUID) (*Bio, error) {
	b := &Bio{}
	err := q.s.DB.QueryRow(ctx, `SELECT p.id, p.first_name, p.last_name, p.home_town, p.home_state, p.first_season, p.provisional,
		COALESCE(ps.position,''), COALESCE(ps.jersey,0), COALESCE(ps.team_id,-1), COALESCE(ps.portrait,''), COALESCE(ps.school_year,''),
		COALESCE(ps.height,0), COALESCE(ps.weight,0), COALESCE(ps.star_rating,0), COALESCE(ps.redshirt,''), COALESCE(ps.on_roster,false),
		COALESCE(ps.is_captain,false), COALESCE(ps.injury_status,''), COALESCE(ps.season_index,-1)
		FROM person p LEFT JOIN LATERAL (SELECT * FROM player_season x WHERE x.branch_id = $1 AND x.person_id = p.id ORDER BY x.season_index DESC LIMIT 1) ps ON true
		WHERE p.id = $2 AND p.dynasty_id = $3`, c.Branch, id, c.Dynasty).Scan(&b.ID, &b.First, &b.Last, &b.HomeTown, &b.HomeState, &b.FirstSeason, &b.Provisional,
		&b.Position, &b.Jersey, &b.TeamID, &b.Portrait, &b.ClassYear, &b.Height, &b.Weight, &b.Stars, &b.Redshirt, &b.OnRoster, &b.Captain, &b.Injury, &b.BioSeason)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return b, err
}

// RosterEntry is a player on a team's roster.
type RosterEntry struct {
	PlayerRef
	Height, Weight int
	HomeTown       string
	HomeState      string
	Stars          int
	Redshirt       string
	Captain        bool
	Injury         string
	// Departed is set when the player was on the roster during the season
	// but not on the team at its end: "NFL Draft, Rd N", "Transferred",
	// "Graduated" or "Left team".
	Departed string
}

// Roster returns everyone on a team's roster during a season. Seasons
// recorded in roster_spot list every player seen on the roster in an
// in-season save, departures included, with each player's bio as of his last
// in-season save. Older data falls back to the season's latest bios, which
// miss players who left before that save. Injuries show only for the
// current season.
func (q *Queries) Roster(ctx context.Context, c *Ctx, season, team int) ([]*RosterEntry, error) {
	rows, err := q.s.DB.Query(ctx, `WITH spots AS (
			SELECT * FROM roster_spot WHERE branch_id = $1 AND season_index = $2 AND team_id = $3),
		members AS (
			SELECT person_id, position, jersey, portrait, school_year, height, weight, star_rating, redshirt, is_captain FROM spots
			UNION ALL
			SELECT person_id, position, jersey, portrait, school_year, height, weight, star_rating, redshirt, is_captain FROM player_season
			WHERE branch_id = $1 AND season_index = $2 AND team_id = $3 AND on_roster AND NOT EXISTS (SELECT 1 FROM spots))
		SELECT p.id, p.first_name, p.last_name, m.position, m.jersey, $3::int, m.portrait,
			m.school_year, m.height, m.weight, p.home_town, p.home_state, m.star_rating,
			m.redshirt, m.is_captain, CASE WHEN $2 = $4 THEN COALESCE(ps.injury_status, '') ELSE '' END,
			CASE WHEN ps.team_id = $3 AND ps.on_roster THEN '' ELSE COALESCE(
				(SELECT 'NFL Draft, Rd ' || d.round FROM draft_pick d WHERE d.branch_id = $1 AND d.person_id = p.id AND d.season_index = $2),
				-- On another roster now, or next season.
				CASE WHEN ps.on_roster AND ps.team_id <> $3 THEN 'Transferred' END,
				(SELECT 'Transferred' FROM roster_spot r WHERE r.branch_id = $1 AND r.person_id = p.id AND r.season_index = $2 + 1 AND r.team_id <> $3 LIMIT 1),
				CASE WHEN ps.school_year = 'Senior' THEN 'Graduated' END, 'Left team') END
		FROM members m JOIN person p ON p.id = m.person_id
		LEFT JOIN player_season ps ON ps.branch_id = $1 AND ps.season_index = $2 AND ps.person_id = m.person_id
		ORDER BY m.jersey, p.last_name`, c.Branch, season, team, c.Season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RosterEntry
	for rows.Next() {
		r := &RosterEntry{}
		if err := rows.Scan(&r.ID, &r.First, &r.Last, &r.Position, &r.Jersey, &r.TeamID, &r.Portrait, &r.ClassYear, &r.Height, &r.Weight, &r.HomeTown, &r.HomeState,
			&r.Stars, &r.Redshirt, &r.Captain, &r.Injury, &r.Departed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Coach is a staff member for a season.
type Coach struct {
	Row                   int
	First, Last, Position string
	TeamID, Age           int
	Asset                 string
	W, L, SW, SL          int
	BowlW, BowlL          int
	Conf, Natl            int
	PlayoffW, PlayoffL    int
	IsUser                bool
	Key                   string // CoachKey; links to /coach/{key}
	YearsCoaching         int
	SeasonsWithTeam       int
	HomeState             string
}

// Name returns "First Last".
func (k *Coach) Name() string { return strings.TrimSpace(k.First + " " + k.Last) }

// Coaches returns a team's coaching staff for a season (team -1 = all).
func (q *Queries) Coaches(ctx context.Context, c *Ctx, season, team int) ([]*Coach, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT `+coachCols+` FROM coach_season
		WHERE branch_id = $1 AND season_index = $2 AND ($3 < 0 OR team_id = $3) AND team_id >= 0
		ORDER BY CASE position WHEN 'HeadCoach' THEN 0 WHEN 'OffensiveCoordinator' THEN 1 ELSE 2 END`, c.Branch, season, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Coach
	for rows.Next() {
		k := &Coach{}
		if err := scanCoach(rows, k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// PollEntry is one team in a poll week.
type PollEntry struct {
	TeamID     int
	Rank       int
	Points     int
	FirstPlace int
	PrevRank   int
	W, L       int
}

// PollWeek identifies a recorded poll.
type PollWeek struct {
	WeekType string
	Week     int
}

// PollWeeks lists recorded poll weeks for a season, newest first.
func (q *Queries) PollWeeks(ctx context.Context, c *Ctx, season int) ([]PollWeek, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT DISTINCT week_type, week FROM poll_week WHERE branch_id = $1 AND season_index = $2 ORDER BY week DESC`, c.Branch, season)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PollWeek
	for rows.Next() {
		var p PollWeek
		rows.Scan(&p.WeekType, &p.Week)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Poll returns one poll for a week.
func (q *Queries) Poll(ctx context.Context, c *Ctx, season int, w PollWeek, poll string) ([]*PollEntry, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT team_id, rank, points, first_place, prev_rank, wins, losses FROM poll_week
		WHERE branch_id = $1 AND season_index = $2 AND week_type = $3 AND week = $4 AND poll = $5 ORDER BY rank`, c.Branch, season, w.WeekType, w.Week, poll)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PollEntry
	for rows.Next() {
		p := &PollEntry{}
		rows.Scan(&p.TeamID, &p.Rank, &p.Points, &p.FirstPlace, &p.PrevRank, &p.W, &p.L)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The poll was taken before that week's games.
	results, err := q.SeasonResults(ctx, c, season)
	if err != nil {
		return nil, err
	}
	recs := tallyRecords(results, w.Week)
	for _, p := range out {
		if r, ok := recs[p.TeamID]; ok {
			var cw, cl, st int
			r.apply(&p.W, &p.L, &cw, &cl, &st)
		}
	}
	return out, nil
}

// Award is an honor won by a player.
type Award struct {
	PersonID    *uuid.UUID
	First, Last string
	TeamID      int
	Position    string
	Type        string
	Period      string
	PeriodIndex int
	Season      int // -1 when unknown
}

// Awards returns player awards, optionally for one person.
func (q *Queries) Awards(ctx context.Context, c *Ctx, person uuid.UUID) ([]*Award, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT person_id, first_name, last_name, team_id, position, award_type, period,
			-- The parser drops a zero week, which reads as -1: Week 0.
			CASE WHEN period = 'Game' THEN GREATEST(period_index, 0) ELSE period_index END, season_index FROM award
		WHERE branch_id = $1 AND ($2::uuid IS NULL OR person_id = $2)
			-- A weekly award the save no longer lists for this or last season
			-- is a stale row, not an award anyone won.
			AND NOT (period = 'Game' AND season_index < 0)
		ORDER BY season_index DESC, period_index DESC, award_type`, c.Branch, nullUUID(person))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Award
	for rows.Next() {
		a := &Award{}
		if err := rows.Scan(&a.PersonID, &a.First, &a.Last, &a.TeamID, &a.Position, &a.Type, &a.Period, &a.PeriodIndex, &a.Season); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LeagueAward is a historical national award winner.
type LeagueAward struct {
	Type, First, Last, Position, Team string
	Seq                               int
}

// LeagueAwards returns the league's award history.
func (q *Queries) LeagueAwards(ctx context.Context, c *Ctx) ([]*LeagueAward, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT award_type, first_name, last_name, position, team_name, seq FROM league_award WHERE branch_id = $1 ORDER BY seq DESC`, c.Branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LeagueAward
	for rows.Next() {
		a := &LeagueAward{}
		rows.Scan(&a.Type, &a.First, &a.Last, &a.Position, &a.Team, &a.Seq)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ConfChamp is a conference title game result.
type ConfChamp struct {
	Seq                       int
	Conference, Winner, Loser string
	WS, LS, WR, LR            int
	Coach                     string
}

// ConferenceChampions returns title game history, newest first.
func (q *Queries) ConferenceChampions(ctx context.Context, c *Ctx) ([]*ConfChamp, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT seq, conference, winner, loser, winner_score, loser_score, winner_rank, loser_rank, coach_name FROM conference_champion
		WHERE branch_id = $1 ORDER BY seq DESC`, c.Branch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ConfChamp
	for rows.Next() {
		x := &ConfChamp{}
		rows.Scan(&x.Seq, &x.Conference, &x.Winner, &x.Loser, &x.WS, &x.LS, &x.WR, &x.LR, &x.Coach)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Record is a record-book entry.
type Record struct {
	Scope, ScopeName, Period, Stat string
	Rank, Value                    int
	First, Last, Position, Team    string
	Year                           int
}

// RecordBook returns record-book entries for a scope/name.
func (q *Queries) RecordBook(ctx context.Context, c *Ctx, scope, name string) ([]*Record, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT scope, scope_name, period, stat_type, rank, value, first_name, last_name, position, team_name, year FROM record_book
		WHERE branch_id = $1 AND scope = $2 AND ($3 = '' OR scope_name = $3) ORDER BY period, stat_type, rank`, c.Branch, scope, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Record
	for rows.Next() {
		r := &Record{}
		rows.Scan(&r.Scope, &r.ScopeName, &r.Period, &r.Stat, &r.Rank, &r.Value, &r.First, &r.Last, &r.Position, &r.Team, &r.Year)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecruitEntry is a prospect in a recruiting class.
type RecruitEntry struct {
	PlayerRef
	HomeTown, HomeState string
	Height, Weight      int
	Class               string
	Stars               int
	National, PosRank   int
	StateRank           int
	Stage               string
	Committed           int
}

// Recruits returns a season's recruiting class, best ranked first.
func (q *Queries) Recruits(ctx context.Context, c *Ctx, season, team int) ([]*RecruitEntry, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT p.id, p.first_name, p.last_name, r.position, p.home_town, p.home_state, COALESCE(ps.height,0), COALESCE(ps.weight,0),
		COALESCE(ps.portrait,''), r.class, r.stars, r.national_rank, r.position_rank, r.state_rank, r.stage, r.committed_team
		FROM recruit r JOIN person p ON p.id = r.person_id
		LEFT JOIN player_season ps ON ps.branch_id = r.branch_id AND ps.season_index = r.season_index AND ps.person_id = r.person_id
		WHERE r.branch_id = $1 AND r.season_index = $2 AND ($3 < 0 OR r.committed_team = $3)
		ORDER BY (r.national_rank = 0), r.national_rank, r.stars DESC`, c.Branch, season, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RecruitEntry
	for rows.Next() {
		r := &RecruitEntry{}
		if err := rows.Scan(&r.ID, &r.First, &r.Last, &r.Position, &r.HomeTown, &r.HomeState, &r.Height, &r.Weight, &r.Portrait, &r.Class, &r.Stars, &r.National,
			&r.PosRank, &r.StateRank, &r.Stage, &r.Committed); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Injury is a current injury.
type Injury struct {
	Player             PlayerRef
	Type, Severity     string
	MinWeeks, MaxWeeks int
}

// Injuries returns a team's current injuries.
func (q *Queries) Injuries(ctx context.Context, c *Ctx, team int) ([]*Injury, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT p.id, p.first_name, p.last_name, i.type, i.severity, i.min_weeks, i.max_weeks FROM injury i JOIN person p ON p.id = i.person_id
		WHERE i.branch_id = $1 AND i.team_id = $2 ORDER BY i.max_weeks DESC`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Injury
	for rows.Next() {
		x := &Injury{}
		rows.Scan(&x.Player.ID, &x.Player.First, &x.Player.Last, &x.Type, &x.Severity, &x.MinWeeks, &x.MaxWeeks)
		out = append(out, x)
	}
	return out, rows.Err()
}

// DepthSlot is a depth chart entry.
type DepthSlot struct {
	Position string
	Slot     int
	Player   PlayerRef
}

// DepthChart returns a team's current depth chart.
func (q *Queries) DepthChart(ctx context.Context, c *Ctx, team int) ([]*DepthSlot, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT d.position, d.slot, p.id, p.first_name, p.last_name FROM depth_chart d JOIN person p ON p.id = d.person_id
		WHERE d.branch_id = $1 AND d.team_id = $2 ORDER BY d.position, d.slot`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DepthSlot
	for rows.Next() {
		x := &DepthSlot{}
		rows.Scan(&x.Position, &x.Slot, &x.Player.ID, &x.Player.First, &x.Player.Last)
		out = append(out, x)
	}
	return out, rows.Err()
}

// Event is a player event (leaving intent, position change).
type Event struct {
	Season int
	Kind   string
	Detail map[string]any
}

// Events returns a person's events.
func (q *Queries) Events(ctx context.Context, c *Ctx, person uuid.UUID) ([]*Event, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT season_index, kind, detail FROM player_event WHERE branch_id = $1 AND person_id = $2 ORDER BY season_index`, c.Branch, person)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e := &Event{}
		var raw []byte
		rows.Scan(&e.Season, &e.Kind, &raw)
		json.Unmarshal(raw, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Rivalry is a series record.
type Rivalry struct {
	Name                  string
	Team1, Team2          int
	W1, W2                int
	StreakLen, StreakTeam int
}

// Rivalries returns rivalries involving a team (-1 = all).
func (q *Queries) Rivalries(ctx context.Context, c *Ctx, team int) ([]*Rivalry, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT name, team1, team2, team1_wins, team2_wins, streak_len, streak_team FROM rivalry
		WHERE branch_id = $1 AND ($2 < 0 OR team1 = $2 OR team2 = $2) AND name <> '' ORDER BY name`, c.Branch, team)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Rivalry
	for rows.Next() {
		r := &Rivalry{}
		rows.Scan(&r.Name, &r.Team1, &r.Team2, &r.W1, &r.W2, &r.StreakLen, &r.StreakTeam)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SearchPeople finds people by name.
func (q *Queries) SearchPeople(ctx context.Context, c *Ctx, term string) ([]PlayerRef, error) {
	like := "%" + strings.ToLower(term) + "%"
	rows, err := q.s.DB.Query(ctx, `SELECT p.id, p.first_name, p.last_name, COALESCE(ps.position,''), COALESCE(ps.jersey,0), COALESCE(ps.team_id,-1), COALESCE(ps.portrait,''), COALESCE(ps.school_year,'')
		FROM person p JOIN LATERAL (SELECT * FROM player_season x WHERE x.branch_id = $1 AND x.person_id = p.id ORDER BY x.season_index DESC LIMIT 1) ps ON true
		WHERE p.dynasty_id = $2 AND lower(p.first_name || ' ' || p.last_name) LIKE $3 ORDER BY ps.on_roster DESC, p.last_name LIMIT 50`, c.Branch, c.Dynasty, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlayerRef
	for rows.Next() {
		var r PlayerRef
		rows.Scan(&r.ID, &r.First, &r.Last, &r.Position, &r.Jersey, &r.TeamID, &r.Portrait, &r.ClassYear)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminInfo summarizes ingest state.
type AdminInfo struct {
	Snapshots []SnapshotInfo
	Branches  []BranchInfo
	Flags     []FlagInfo
	Counts    map[string]int64
}

type SnapshotInfo struct {
	ID, Branch, Season, Year, Week int
	Stage, WeekType, Save, SHA     string
	Ingested                       time.Time
}

type BranchInfo struct {
	ID, Parent int
	Status     string
	Created    time.Time
}

type FlagInfo struct {
	Kind    string
	Detail  string
	Created time.Time
}

// Admin returns ingest diagnostics for the current dynasty.
func (q *Queries) Admin(ctx context.Context, c *Ctx) (*AdminInfo, error) {
	a := &AdminInfo{Counts: map[string]int64{}}
	rows, err := q.s.DB.Query(ctx, `SELECT id, branch_id, season_index, year, week, stage, week_type, save_name, sha256, ingested_at FROM snapshot WHERE dynasty_id = $1 ORDER BY id DESC LIMIT 50`, c.Dynasty)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s SnapshotInfo
		rows.Scan(&s.ID, &s.Branch, &s.Season, &s.Year, &s.Week, &s.Stage, &s.WeekType, &s.Save, &s.SHA, &s.Ingested)
		a.Snapshots = append(a.Snapshots, s)
	}
	rows.Close()
	rows, err = q.s.DB.Query(ctx, `SELECT id, COALESCE(parent_id, 0), status, created_at FROM branch WHERE dynasty_id = $1 ORDER BY id`, c.Dynasty)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b BranchInfo
		rows.Scan(&b.ID, &b.Parent, &b.Status, &b.Created)
		a.Branches = append(a.Branches, b)
	}
	rows.Close()
	rows, err = q.s.DB.Query(ctx, `SELECT kind, detail::text, created_at FROM review_flag WHERE dynasty_id = $1 AND NOT resolved ORDER BY id DESC LIMIT 100`, c.Dynasty)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f FlagInfo
		rows.Scan(&f.Kind, &f.Detail, &f.Created)
		a.Flags = append(a.Flags, f)
	}
	rows.Close()
	for _, t := range []string{"game", "player_game_stat", "player_season_stat", "scoring_play", "award"} {
		var n int64
		q.s.DB.QueryRow(ctx, "SELECT count(*) FROM "+t+" WHERE branch_id = $1", c.Branch).Scan(&n)
		a.Counts[t] = n
	}
	var n int64
	q.s.DB.QueryRow(ctx, "SELECT count(*) FROM person WHERE dynasty_id = $1", c.Dynasty).Scan(&n)
	a.Counts["person"] = n
	return a, nil
}

// record is a team's W-L tallied from played games.
type record struct {
	W, L, CW, CL, Streak int
}

// tallyRecords counts each team's record over played games before
// beforeWeek. The save's own W-L columns can lag a week behind the results
// it already holds, so the site counts the games itself.
func tallyRecords(results []Result, beforeWeek int) map[int]*record {
	out := map[int]*record{}
	for _, r := range results { // week order
		if !r.Played || r.Week >= beforeWeek || r.HomeScore == r.AwayScore {
			continue
		}
		for _, side := range []struct {
			team int
			won  bool
		}{{r.Home, r.HomeScore > r.AwayScore}, {r.Away, r.AwayScore > r.HomeScore}} {
			rec := out[side.team]
			if rec == nil {
				rec = &record{}
				out[side.team] = rec
			}
			if side.won {
				rec.W++
				if r.Conference {
					rec.CW++
				}
				if rec.Streak > 0 {
					rec.Streak++
				} else {
					rec.Streak = 1
				}
			} else {
				rec.L++
				if r.Conference {
					rec.CL++
				}
				if rec.Streak < 0 {
					rec.Streak--
				} else {
					rec.Streak = -1
				}
			}
		}
	}
	return out
}

// apply replaces the save's record when the games show more decisions.
// Seasons ingested before game types were stored can't count conference
// games, so the save's conference record stands unless ours is ahead too.
func (r *record) apply(w, l, cw, cl, streak *int) {
	if r.W+r.L > *w+*l {
		*w, *l, *streak = r.W, r.L, r.Streak
	}
	if r.CW+r.CL > *cw+*cl {
		*cw, *cl = r.CW, r.CL
	}
}

// DraftPick is a player taken in the NFL draft. Season is the college season
// he was drafted out of (the draft is held the following spring); Coach is
// his school's head coach that season.
type DraftPick struct {
	PlayerRef
	Season, Round int
	Coach         *CoachRef
}

// CoachRef names a coach and links his page.
type CoachRef struct {
	Key, First, Last string
}

// DraftFilter narrows DraftPicks; zero values match everything.
type DraftFilter struct {
	Team   int // -1 = any
	Person uuid.UUID
	Coach  string // coach_key
}

// DraftPicks returns NFL draft picks, newest draft first, then by round.
func (q *Queries) DraftPicks(ctx context.Context, c *Ctx, f DraftFilter) ([]*DraftPick, error) {
	rows, err := q.s.DB.Query(ctx, `SELECT d.person_id, p.first_name, p.last_name, d.position, d.team_id, d.season_index, d.round,
			COALESCE(hc.coach_key, ''), COALESCE(hc.first_name, ''), COALESCE(hc.last_name, '')
		FROM draft_pick d JOIN person p ON p.id = d.person_id
		LEFT JOIN LATERAL (SELECT coach_key, first_name, last_name FROM coach_season cs
			WHERE cs.branch_id = d.branch_id AND cs.season_index = d.season_index AND cs.team_id = d.team_id AND cs.position = 'HeadCoach'
			ORDER BY cs.snapshot_id DESC LIMIT 1) hc ON true
		WHERE d.branch_id = $1 AND ($2 < 0 OR d.team_id = $2) AND ($3::uuid IS NULL OR d.person_id = $3) AND ($4 = '' OR hc.coach_key = $4)
		ORDER BY d.season_index DESC, d.round, p.last_name, p.first_name`, c.Branch, f.Team, nullUUID(f.Person), f.Coach)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DraftPick
	for rows.Next() {
		d := &DraftPick{}
		var k CoachRef
		if err := rows.Scan(&d.ID, &d.First, &d.Last, &d.Position, &d.TeamID, &d.Season, &d.Round, &k.Key, &k.First, &k.Last); err != nil {
			return nil, err
		}
		if k.Key != "" {
			d.Coach = &k
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
