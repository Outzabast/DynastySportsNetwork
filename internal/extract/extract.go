package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leaguelines/cfb-dynasty/dynasty"
)

// ParserModule is the pinned parser version (keep in sync with go.mod).
const ParserModule = "github.com/leaguelines/cfb-dynasty@v0.3.1-0.20260731033729-9905effe3665"

// fcsTeamIndex is the TeamIndex placeholder for FCS opponents.
const fcsTeamIndex = 255

// statTables maps raw stat table names to (scope, category).
var statTables = map[string][2]string{
	"GameOffensiveStats":           {"game", "offense"},
	"GameDefensiveStats":           {"game", "defense"},
	"GameKickingStats":             {"game", "kicking"},
	"GameOLineStats":               {"game", "oline"},
	"GameOffensiveKPReturnStats":   {"game", "returns_off"},
	"GameDefensiveKPReturnStats":   {"game", "returns_def"},
	"SeasonOffensiveStats":         {"season", "offense"},
	"SeasonDefensiveStats":         {"season", "defense"},
	"SeasonKickingStats":           {"season", "kicking"},
	"SeasonOLineStats":             {"season", "oline"},
	"SeasonOffensiveKPReturnStats": {"season", "returns_off"},
	"SeasonDefensiveKPReturnStats": {"season", "returns_def"},
	"CareerOffensiveStats":         {"career", "offense"},
	"CareerDefensiveStats":         {"career", "defense"},
	"CareerKickingStats":           {"career", "kicking"},
	"CareerOLineStats":             {"career", "oline"},
	"CareerOffensiveKPReturnStats": {"career", "returns_off"},
	"CareerDefensiveKPReturnStats": {"career", "returns_def"},
}

// SHA256File hashes a file on disk.
func SHA256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Extract parses the save copy at path. The caller must pass a copy, never the
// live save (AGENTS.md rule 2).
func Extract(path, schemaDir string) (*Snapshot, error) {
	settings := dynasty.DefaultSettings()
	settings.SchemaDir = schemaDir
	settings.AutoParse = true
	f, err := dynasty.Open(path, &settings)
	if err != nil {
		return nil, fmt.Errorf("open save: %w", err)
	}
	exp, err := f.ExportWithOptions(dynasty.ExportOptions{
		OmitGameStats: true,
		Sections: dynasty.ExportSections{
			Season: true, Teams: true, Games: true, Recruits: true, Recruiting: true,
			Coaches: true, LeavingPlayers: true, Injuries: true, DepthCharts: true,
			History: true, Rivalries: true, PositionChanges: true, Bowls: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}

	x := &extractor{f: f, exp: &exp, tables: map[string]*dynasty.Table{}}
	snap := &Snapshot{
		SourcePath:    path,
		SaveName:      filepath.Base(path),
		SHA256:        exp.Source.SHA256,
		Size:          exp.Source.Size,
		ParserVersion: ParserModule,
	}
	if sv := exp.Source.SchemaVersion; sv != nil {
		snap.SchemaVersion = fmt.Sprintf("%d.%d", sv.Major, sv.Minor)
	}
	if err := x.run(snap); err != nil {
		return nil, err
	}
	return snap, nil
}

type extractor struct {
	f      *dynasty.File
	exp    *dynasty.Export
	tables map[string]*dynasty.Table

	teamIDByRow map[int]int
	seasonGame  uint32
	statByID    map[uint32]statTable
	players     *dynasty.Table
	wanted      map[int]bool // player rows referenced by anything we keep
}

type statTable struct {
	scope, category string
	t               *dynasty.Table
}

func (x *extractor) table(name string) *dynasty.Table {
	if t, ok := x.tables[name]; ok {
		return t
	}
	t, ok := x.f.PrimaryTableByName(name)
	if !ok || t.ReadRecords() != nil {
		t = nil
	}
	x.tables[name] = t
	return t
}

func (x *extractor) row(t *dynasty.Table, i int) (dynasty.Record, bool) {
	if t == nil || i < 0 || i >= len(t.Records) {
		return dynasty.Record{}, false
	}
	return t.Records[i], true
}

// arrayRefs resolves an array-store reference to its member references. Some
// array tables (schema type record[]) store packed 32-bit refs as plain ints:
// 15-bit table ID + 17-bit row.
func (x *extractor) arrayRefs(ref *dynasty.RecordReference) []dynasty.RecordReference {
	if ref == nil || ref.TableID == 0 {
		return nil
	}
	t, ok := x.f.GetTableByID(ref.TableID)
	if !ok || t == nil || t.ReadRecords() != nil {
		return nil
	}
	r, ok := x.row(t, int(ref.RowNumber))
	if !ok {
		return nil
	}
	keys := make([]int, 0, len(r.Fields))
	for k := range r.Fields {
		if n, err := strconv.Atoi(k); err == nil {
			keys = append(keys, n)
		}
	}
	sort.Ints(keys)
	var out []dynasty.RecordReference
	for _, k := range keys {
		v := r.Fields[strconv.Itoa(k)]
		switch {
		case v.Reference != nil:
			if v.Reference.TableID != 0 || v.Reference.RowNumber != 0 {
				out = append(out, *v.Reference)
			}
		case v.Int != 0:
			out = append(out, dynasty.RecordReference{TableID: uint32(v.Int >> 17), RowNumber: uint32(v.Int & 0x1FFFF)})
		}
	}
	return out
}

func intF(r dynasty.Record, k string) int {
	v, ok := r.Fields[k]
	if !ok {
		return 0
	}
	if v.Int == 0 && v.Bool {
		return 1
	}
	return int(v.Int)
}

func strF(r dynasty.Record, k string) string {
	v, ok := r.Fields[k]
	if !ok {
		return ""
	}
	return strings.TrimSpace(v.String)
}

func boolF(r dynasty.Record, k string) bool {
	v, ok := r.Fields[k]
	return ok && (v.Bool || v.Int != 0)
}

func refF(r dynasty.Record, k string) *dynasty.RecordReference {
	v, ok := r.Fields[k]
	if !ok || v.Reference == nil || (v.Reference.TableID == 0 && v.Reference.RowNumber == 0) {
		return nil
	}
	return v.Reference
}

// statsOf copies every numeric/bool column of a stat row.
func statsOf(r dynasty.Record) Stats {
	s := Stats{}
	for k, v := range r.Fields {
		if v.Reference != nil {
			continue
		}
		switch {
		case v.String != "":
			// enum/string columns (TeamPrefixName) are not stats
		case v.Bool:
			s[k] = 1
		default:
			s[k] = v.Int
		}
	}
	return s
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func (x *extractor) run(snap *Snapshot) error {
	x.players = x.table("Player")
	if x.players == nil {
		return fmt.Errorf("save has no Player table")
	}
	if sg := x.table("SeasonGame"); sg != nil {
		x.seasonGame = sg.Header.TableID
	}
	x.statByID = map[uint32]statTable{}
	for name, sc := range statTables {
		if t := x.table(name); t != nil {
			x.statByID[t.Header.TableID] = statTable{sc[0], sc[1], t}
		}
	}
	x.wanted = map[int]bool{}

	if fr := x.table("Franchise"); fr != nil && len(fr.Records) > 0 {
		snap.LeagueID = int64(intF(fr.Records[0], "LeagueID"))
	}
	x.season(snap)
	x.teams(snap)
	x.games(snap)
	x.playerStats(snap)
	x.entities(snap)
	x.playerRows(snap)
	return nil
}

func (x *extractor) season(snap *Snapshot) {
	si := x.table("SeasonInfo")
	if si == nil || len(si.Records) == 0 {
		return
	}
	r := si.Records[0]
	snap.Season = Season{
		BaseCalendarYear: intF(r, "BaseCalendarYear"),
		SeasonIndex:      intF(r, "CurrentYear"),
		Year:             intF(r, "CurrentSeasonYear"),
		Stage:            strF(r, "CurrentStage"),
		Week:             intF(r, "CurrentWeek"),
		WeekType:         strF(r, "CurrentWeekType"),
		OffseasonStage:   intF(r, "CurrentOffseasonStage"),
		Periods:          map[string]bool{},
	}
	for k, v := range r.Fields {
		if strings.HasPrefix(k, "Is") && strings.HasSuffix(k, "Active") {
			snap.Season.Periods[k] = v.Bool || v.Int != 0
		}
	}
	if snap.Season.Year == 0 {
		snap.Season.Year = snap.Season.BaseCalendarYear + snap.Season.SeasonIndex
	}
}

func colorHex(r dynasty.Record, suffix string) string {
	rr, g, b := intF(r, "TEAM_BACKGROUNDCOLORR"+suffix), intF(r, "TEAM_BACKGROUNDCOLORG"+suffix), intF(r, "TEAM_BACKGROUNDCOLORB"+suffix)
	if rr == 0 && g == 0 && b == 0 {
		return ""
	}
	return fmt.Sprintf("#%02x%02x%02x", rr&0xff, g&0xff, b&0xff)
}

func (x *extractor) teams(snap *Snapshot) {
	x.teamIDByRow = map[int]int{}
	tt := x.table("Team")
	if tt == nil {
		return
	}
	expByID := map[int]dynasty.TeamExport{}
	for _, t := range x.exp.Teams {
		expByID[t.ID] = t
	}
	for _, r := range tt.Records {
		id, ok := r.Fields["TeamIndex"]
		if !ok || id.Int == fcsTeamIndex {
			continue
		}
		e, ok := expByID[int(id.Int)]
		if !ok {
			continue // not an official FBS program
		}
		x.teamIDByRow[r.Index] = int(id.Int)
		t := Team{
			ID: int(id.Int), Row: r.Index,
			ShortName: e.ShortName, LongName: e.LongName, DisplayName: e.DisplayName,
			NickName: strF(r, "NickName"), Abbrev: strF(r, "TEAM_PREFIX_NAME"), AssetName: strF(r, "AssetName"),
			Conference:     e.Conference,
			PrimaryColor:   colorHex(r, ""),
			SecondaryColor: colorHex(r, "2"),
			Wins:           deref(e.OverallWins), Losses: deref(e.OverallLosses),
			ConfWins: deref(e.ConferenceWins), ConfLosses: deref(e.ConferenceLosses),
			ConfStanding: deref(e.ConferenceStanding), DivStanding: deref(e.DivisionStanding),
			Streak:   deref(e.SeasonWinLossStreak),
			APRank:   intF(r, "MediaPoll_CurrentRank"), APPoints: intF(r, "MediaPoll_CurrentPoints"),
			APFirstPlace: intF(r, "MediaPoll_FirstPlaceVotes"), APLastRank: intF(r, "MediaPoll_LastWeeksRank"),
			CoachesRank: intF(r, "CoachesPoll_CurrentRank"), CoachesPoints: intF(r, "CoachesPoll_CurrentPoints"),
			CoachesFirst: intF(r, "CoachesPoll_FirstPlaceVotes"), CoachesLastRank: intF(r, "CoachesPoll_LastWeeksRank"),
			CFPRank: intF(r, "CFPPoll_CurrentRank"), CFPLastRank: intF(r, "CFPPoll_LastWeeksRank"),
			OffensiveRank: deref(e.OffensiveRank), DefensiveRank: deref(e.DefensiveRank),
			PlayoffStatus: e.PlayoffStatus, PlayoffRound: e.PlayoffRoundReached,
			HeadCoachRow: -1, OCRow: -1, DCRow: -1,
		}
		if e.RecruitingClass != nil {
			t.RecruitingRank = deref(e.RecruitingClass.NationalRank)
		}
		if e.StaffIDs != nil {
			t.HeadCoachRow = derefOr(e.StaffIDs.HeadCoachID, -1)
			t.OCRow = derefOr(e.StaffIDs.OffensiveCoordinatorID, -1)
			t.DCRow = derefOr(e.StaffIDs.DefensiveCoordinatorID, -1)
		}
		for _, m := range x.arrayRefs(refF(r, "Roster")) {
			t.RosterRows = append(t.RosterRows, int(m.RowNumber))
			x.wanted[int(m.RowNumber)] = true
		}
		snap.Teams = append(snap.Teams, t)

		// Season team totals: every TeamStats column.
		if ref := refF(r, "TeamSeasonStats"); ref != nil {
			if row, ok := x.f.RecordByReference("TeamStats", ref); ok {
				snap.TeamSeasonStats = append(snap.TeamSeasonStats, TeamSeasonStat{TeamID: t.ID, Stats: statsOf(row)})
			}
		}
	}
	sort.Slice(snap.Teams, func(i, j int) bool { return snap.Teams[i].ID < snap.Teams[j].ID })
}

func derefOr(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

func (x *extractor) teamIDFromRef(ref *dynasty.RecordReference) int {
	if ref == nil {
		return -1
	}
	if id, ok := x.teamIDByRow[int(ref.RowNumber)]; ok {
		return id
	}
	return -1
}

func (x *extractor) games(snap *Snapshot) {
	sg := x.table("SeasonGame")
	if sg == nil {
		return
	}
	expByRow := map[int]dynasty.GameExport{}
	for _, g := range x.exp.Games {
		expByRow[int(g.ID)] = g
	}
	snapT := x.table("OffensivePlayerStatSnapShot")
	for _, r := range sg.Records {
		home, away := x.teamIDFromRef(refF(r, "HomeTeam")), x.teamIDFromRef(refF(r, "AwayTeam"))
		if refF(r, "HomeTeam") == nil && refF(r, "AwayTeam") == nil {
			continue
		}
		status := strF(r, "GameStatus")
		e := expByRow[r.Index]
		g := Game{
			Row: r.Index, SeasonIndex: intF(r, "SeasonYear"), Week: intF(r, "SeasonWeek"), WeekType: strF(r, "SeasonWeekType"),
			Status: status, Played: status == "HomeWon" || status == "AwayWon" || status == "Tie",
			HomeTeamID: home, AwayTeamID: away,
			HomeScore: intF(r, "HomeScore"), AwayScore: intF(r, "AwayScore"),
			HomeOT: intF(r, "HomeScoreOT"), AwayOT: intF(r, "AwayScoreOT"), Overtime: boolF(r, "IsOvertimeGame"),
			Simmed: boolF(r, "IsSimmed"), Month: intF(r, "GameDateMonth"), Day: intF(r, "GameDateDay"),
			DayOfWeek: strF(r, "DayOfWeek"), TimeOfDay: intF(r, "TimeOfDay"), Network: strF(r, "BroadcastNetwork"),
			Attendance: intF(r, "Attendance"), Temperature: intF(r, "Temperature"), Weather: strF(r, "Weather"),
			Wind: strF(r, "Wind"), WindSpeed: intF(r, "WindSpeed"), Stadium: e.StadiumName, BowlName: e.BowlGameName,
			GameOfWeek: boolF(r, "IsGameOfTheWeek"), Rematch: boolF(r, "IsRematch"),
		}
		if refF(r, "BowlGame") == nil && g.WeekType == "RegularSeason" {
			g.BowlName = ""
		}
		for q := 1; q <= 4; q++ {
			g.HomeQuarters = append(g.HomeQuarters, intF(r, fmt.Sprintf("HomeScoreQuarter%d", q)))
			g.AwayQuarters = append(g.AwayQuarters, intF(r, fmt.Sprintf("AwayScoreQuarter%d", q)))
		}
		if g.Played {
			if ref := refF(r, "HomeTeamStatCache"); ref != nil {
				if row, ok := x.f.RecordByReference("TeamStats", ref); ok {
					g.HomeBox = statsOf(row)
				}
			}
			if ref := refF(r, "AwayTeamStatCache"); ref != nil {
				if row, ok := x.f.RecordByReference("TeamStats", ref); ok {
					g.AwayBox = statsOf(row)
				}
			}
		}
		// Scoring summary: only present while the game's week is current.
		for i, pref := range x.arrayRefs(refF(r, "ScoringSummaries")) {
			p, ok := x.f.RecordByReference("ScoringSummary", &pref)
			if !ok {
				continue
			}
			sp := ScoringPlay{
				Seq: i, Quarter: intF(p, "Quarter"), ClockSec: intF(p, "TimeStampInSec"),
				HomeScore: intF(p, "HomeCurrentScore"), AwayScore: intF(p, "AwayCurrentScore"),
				HomePrev: intF(p, "HomePreviousScore"), AwayPrev: intF(p, "AwayPreviousScore"),
				Conversion: strF(p, "Conversion"),
			}
			field := "HomePlayerSnapshots"
			sp.ScoringTeam = "home"
			if sp.AwayScore > sp.AwayPrev {
				field, sp.ScoringTeam = "AwayPlayerSnapshots", "away"
			}
			for _, sref := range x.arrayRefs(refF(p, field)) {
				sr, ok := x.row(snapT, int(sref.RowNumber))
				if !ok {
					continue
				}
				pr := refF(sr, "PlayerRef")
				if pr == nil {
					continue
				}
				x.wanted[int(pr.RowNumber)] = true
				st := statsOf(sr)
				sp.Participants = append(sp.Participants, Scorer{PlayerRow: int(pr.RowNumber), Stats: st})
			}
			g.ScoringPlays = append(g.ScoringPlays, sp)
		}
		snap.Games = append(snap.Games, g)
	}
}

// playerStats walks each Player's stat references (direct and via array
// stores) and attributes every stat row it owns. A row is owned once.
func (x *extractor) playerStats(snap *Snapshot) {
	gameTeams := map[int][2]int{}
	for _, g := range snap.Games {
		gameTeams[g.Row] = [2]int{g.HomeTeamID, g.AwayTeamID}
	}
	type key struct {
		table uint32
		row   uint32
	}
	owned := map[key]bool{}
	// recordAt attributes a stat row to a player. forceGame >= 0 pins game
	// lines to that SeasonGame row (unpublished cache rows).
	var recordAt func(pr dynasty.Record, ref dynasty.RecordReference, forceGame, forceTeam int)
	record := func(pr dynasty.Record, ref dynasty.RecordReference) { recordAt(pr, ref, -1, -1) }
	recordAt = func(pr dynasty.Record, ref dynasty.RecordReference, forceGame, forceTeam int) {
		st, ok := x.statByID[ref.TableID]
		if !ok {
			return
		}
		k := key{ref.TableID, ref.RowNumber}
		if owned[k] {
			return
		}
		row, ok := x.row(st.t, int(ref.RowNumber))
		if !ok {
			return
		}
		owned[k] = true
		stats := statsOf(row)
		switch st.scope {
		case "game":
			gameRow := forceGame
			if gameRow < 0 {
				gref := refF(row, "SeasonGame")
				if gref == nil || gref.TableID != x.seasonGame {
					return // stale row from another season
				}
				gameRow = int(gref.RowNumber)
			}
			teams, ok := gameTeams[gameRow]
			if !ok {
				return
			}
			opp := x.teamIDFromRef(refF(row, "OpposingTeam"))
			team := teams[0]
			if opp == teams[0] {
				team = teams[1]
			}
			if forceTeam >= 0 {
				team = forceTeam
			}
			snap.PlayerGameStats = append(snap.PlayerGameStats, PlayerGameStat{
				GameRow: gameRow, PlayerRow: pr.Index, TeamID: team, Category: st.category, SourceRow: row.Index, Stats: stats,
			})
		case "season":
			year := intF(row, "SEAS_YEAR")
			if year < 0 {
				return
			}
			snap.PlayerSeasonStats = append(snap.PlayerSeasonStats, PlayerSeasonStat{
				PlayerRow: pr.Index, SeasonIndex: year, TeamID: intF(row, "YEARBYYEARTEAMINDEX"),
				TeamAbbrev: strF(row, "TeamPrefixName"), Category: st.category, SourceRow: row.Index, Stats: stats,
			})
		case "career":
			snap.PlayerCareerStats = append(snap.PlayerCareerStats, PlayerCareerStat{
				PlayerRow: pr.Index, Category: st.category, SourceRow: row.Index, Stats: stats,
			})
		}
		x.wanted[pr.Index] = true
	}
	// Games played this week keep their player lines in a per-game cache
	// (SeasonGame.Home/AwayPlayerStatCache -> UnpublishedGameStats) until the
	// game publishes them to each player's GameStats[] array at the next
	// advance. Capture them now: like the scoring summary, they exist only
	// during the game's week.
	if sg, unpub := x.table("SeasonGame"), x.table("UnpublishedGameStats"); sg != nil && unpub != nil {
		for _, g := range sg.Records {
			if boolF(g, "HasBeenPublished") {
				continue // lines already live in players' GameStats[]; the cache may be stale
			}
			for i, side := range []string{"HomePlayerStatCache", "AwayPlayerStatCache"} {
				team := gameTeams[g.Index][i]
				for _, ref := range x.arrayRefs(refF(g, side)) {
					u, ok := x.row(unpub, int(ref.RowNumber))
					if !ok {
						continue
					}
					sref, pref := refF(u, "GameStats"), refF(u, "Player")
					if sref == nil || pref == nil {
						continue
					}
					if pr, ok := x.row(x.players, int(pref.RowNumber)); ok {
						recordAt(pr, *sref, g.Index, team)
					}
				}
			}
		}
	}

	careerOff := x.table("CareerOffensiveStats")
	for _, pr := range x.players.Records {
		if strF(pr, "FirstName") == "" && strF(pr, "LastName") == "" {
			continue
		}
		for name, v := range pr.Fields {
			ref := v.Reference
			if ref == nil || (ref.TableID == 0 && ref.RowNumber == 0) {
				continue
			}
			if name == "CareerStats" && ref.TableID == 0 && careerOff != nil {
				// Local ref: row index into CareerOffensiveStats (parser convention).
				record(pr, dynasty.RecordReference{TableID: careerOff.Header.TableID, RowNumber: ref.RowNumber})
				continue
			}
			if _, ok := x.statByID[ref.TableID]; ok {
				record(pr, *ref)
				continue
			}
			if name == "GameStats" || name == "SeasonStats" || name == "CareerStats" {
				for _, m := range x.arrayRefs(ref) {
					record(pr, m)
				}
			}
		}
	}
}

func starsFromEnum(s string) int {
	switch strings.ToUpper(s) {
	case "ONE_STAR":
		return 1
	case "TWO_STAR":
		return 2
	case "THREE_STAR":
		return 3
	case "FOUR_STAR":
		return 4
	case "FIVE_STAR":
		return 5
	}
	return 0
}

func (x *extractor) entities(snap *Snapshot) {
	e := x.exp
	teamByName := map[string]int{}
	for _, t := range snap.Teams {
		teamByName[t.LongName] = t.ID
		teamByName[t.DisplayName] = t.ID
	}
	// Coaches: real-world facts only.
	for _, c := range e.Coaches {
		co := Coach{Row: c.ID, FirstName: c.FirstName, LastName: c.LastName, Position: c.Position,
			TeamID: derefOr(c.TeamID, -1), Age: deref(c.Age), IsUser: c.IsUserControlled}
		if c.Career != nil {
			co.Wins, co.Losses = deref(c.Career.Wins), deref(c.Career.Losses)
			co.SchoolWins, co.SchoolLosses = deref(c.Career.WinsAtCurrentSchool), deref(c.Career.LossesAtCurrentSchool)
			co.BowlWins, co.BowlLosses = deref(c.Career.BowlWins), deref(c.Career.BowlLosses)
			co.ConfTitles, co.NatlTitles = deref(c.Career.ConfChampWins), deref(c.Career.NCWins)
			co.PlayoffWins, co.PlayoffLoss = deref(c.Career.PlayoffWins), deref(c.Career.PlayoffLosses)
		}
		snap.Coaches = append(snap.Coaches, co)
	}
	if ct := x.table("Coach"); ct != nil {
		for i := range snap.Coaches {
			if r, ok := x.row(ct, snap.Coaches[i].Row); ok {
				snap.Coaches[i].AssetName = strF(r, "AssetName")
			}
		}
	}

	// Recruits: committed school comes from the pursuit state's top school.
	topSchool := map[int]int{}
	for _, r := range e.Recruiting {
		if r.TopSchool != nil {
			topSchool[r.RecruitID] = r.TopSchool.TeamID
		}
	}
	for _, r := range e.Recruits {
		if r.Player == nil {
			continue
		}
		p := r.Player
		rc := Recruit{PlayerRow: p.ID, FirstName: p.FirstName, LastName: p.LastName, Position: p.Position,
			HomeTown: p.HomeTown, HomeState: p.HomeState, Class: r.Class, Stars: starsFromEnum(p.StarRating),
			NationalRank: deref(r.NationalRank), PositionRank: deref(r.PositionRank), StateRank: deref(r.StateRank),
			Stage: r.RecruitStage, CommittedTeam: -1, Height: deref(p.Height), Weight: deref(p.Weight)}
		if r.RecruitStage == "Committed" || r.RecruitStage == "Signed" {
			if t, ok := topSchool[r.ID]; ok {
				rc.CommittedTeam = t
			}
		}
		snap.Recruits = append(snap.Recruits, rc)
		x.wanted[p.ID] = true
	}

	for _, a := range e.PlayerAwards {
		snap.PlayerAwards = append(snap.PlayerAwards, PlayerAward{PlayerRow: a.PlayerID, FirstName: a.FirstName, LastName: a.LastName,
			TeamID: derefOr(a.TeamID, -1), Position: a.Position, AwardType: a.AwardType, Period: a.Period, PeriodIndex: derefOr(a.PeriodIndex, -1)})
		x.wanted[a.PlayerID] = true
	}
	for _, a := range e.LeagueAwards {
		snap.LeagueAwards = append(snap.LeagueAwards, LeagueAward{Row: a.ID, FirstName: a.FirstName, LastName: a.LastName,
			Position: a.Position, AwardType: a.AwardType, TeamName: a.TeamDisplayName})
	}
	for _, c := range e.ConferenceChampions {
		snap.ConferenceChampions = append(snap.ConferenceChampions, ConferenceChampion{Row: c.ID, Conference: c.ConferenceName,
			Winner: c.WinningTeamName, Loser: c.LosingTeamName, WinnerScore: deref(c.WinningScore), LoserScore: deref(c.LosingScore),
			WinnerRank: deref(c.WinningTeamRank), LoserRank: deref(c.LosingTeamRank),
			CoachName: strings.TrimSpace(c.WinningCoachFirst + " " + c.WinningCoachLast)})
	}
	for _, r := range e.RecordBook {
		snap.RecordBook = append(snap.RecordBook, RecordBookEntry{Scope: r.Scope, ScopeName: r.ScopeName, Period: r.Period,
			StatType: r.StatType, Rank: r.Rank, Value: r.StatValue, FirstName: r.FirstName, LastName: r.LastName,
			Position: r.Position, TeamName: r.TeamName, Year: deref(r.CalendarYear)})
	}
	for _, r := range e.Rivalries {
		snap.Rivalries = append(snap.Rivalries, Rivalry{Row: r.ID, Name: r.Name, Team1: derefOr(r.Team1ID, -1), Team2: derefOr(r.Team2ID, -1),
			Team1Wins: deref(r.Team1Wins), Team2Wins: deref(r.Team2Wins), StreakLen: deref(r.StreakLength), StreakTeam: derefOr(r.StreakTeamID, -1)})
	}
	for _, i := range e.Injuries {
		snap.Injuries = append(snap.Injuries, Injury{PlayerRow: i.PlayerID, TeamID: derefOr(i.TeamID, -1), Type: i.Type,
			Severity: i.Severity, MinWeeks: deref(i.MinWeeks), MaxWeeks: deref(i.MaxWeeks)})
		x.wanted[i.PlayerID] = true
	}
	for _, l := range e.LeavingPlayers {
		snap.LeavingPlayers = append(snap.LeavingPlayers, LeavingPlayer{PlayerRow: l.PlayerID, TeamID: derefOr(l.TeamID, -1),
			LeaveType: l.LeaveType, Round: deref(l.ProjectRound)})
		x.wanted[l.PlayerID] = true
	}
	for _, p := range e.PositionChanges {
		if p.PlayerID == nil {
			continue
		}
		snap.PositionChanges = append(snap.PositionChanges, PositionChange{PlayerRow: *p.PlayerID, OldPosition: p.OldPosition,
			NewPosition: p.NewPosition, SeasonIndex: deref(p.SeasonYear), Week: deref(p.SeasonWeek), Stage: p.SeasonStage,
			OldTeam: derefOr(p.OldTeamID, -1), NewTeam: derefOr(p.NewTeamID, -1)})
	}
	for _, d := range e.DepthCharts {
		for i, s := range d.Slots {
			order := s.Depth
			if order == 0 {
				order = i
			}
			snap.DepthCharts = append(snap.DepthCharts, DepthChartSlot{TeamID: d.TeamID, Position: s.Position, Order: order, PlayerRow: s.PlayerID})
		}
	}
	for _, b := range e.BowlGames {
		snap.Bowls = append(snap.Bowls, Bowl{Row: b.ID, Name: b.Name, IsPlayoff: b.IsPlayoffBowl, BracketSlot: derefOr(b.PlayoffBracketSlot, -1)})
	}
}

func (x *extractor) playerRows(snap *Snapshot) {
	teamOf := map[int]int{}
	for _, t := range snap.Teams {
		for _, r := range t.RosterRows {
			teamOf[r] = t.ID
		}
	}
	rows := make([]int, 0, len(x.wanted))
	for r := range x.wanted {
		rows = append(rows, r)
	}
	sort.Ints(rows)
	for _, i := range rows {
		r, ok := x.row(x.players, i)
		if !ok {
			continue
		}
		first, last := strF(r, "FirstName"), strF(r, "LastName")
		if first == "" && last == "" {
			continue
		}
		team, on := teamOf[i]
		if !on {
			team = -1
		}
		snap.Players = append(snap.Players, Player{
			Row: i, FirstName: first, LastName: last, Position: strF(r, "Position"), TeamID: team,
			PrevTeamID: intF(r, "PrevTeamIndex"), SchoolYear: strF(r, "SchoolYear"), Redshirt: strF(r, "RedshirtStatus"),
			Jersey: intF(r, "JerseyNum"), Height: intF(r, "Height"), Weight: playerWeight(r),
			HomeTown: strF(r, "PLYR_HOME_TOWN"), HomeState: strF(r, "PLYR_HOME_STATE"), Handedness: strF(r, "PLYR_HANDEDNESS"),
			AssetName: strF(r, "PLYR_ASSETNAME"), BirthDate: int64(intF(r, "PLYR_BIRTHDATE")),
			PortraitAsset: strF(r, "GenericHeadAssetName"), StarRating: starsFromEnum(strF(r, "ProspectStarRating")),
			IsCaptain: boolF(r, "PLYR_ISCAPTAIN"), InjuryStatus: strF(r, "InjuryStatus"), OnRoster: on,
		})
	}
}

// JSON is a helper for debugging/archiving a snapshot.
func (s *Snapshot) JSON() ([]byte, error) { return json.Marshal(s) }

// playerWeight decodes Player.Weight, stored as pounds minus 160 (same offset
// the parser applies).
func playerWeight(r dynasty.Record) int {
	if w := intF(r, "Weight"); w > 0 {
		return w + 160
	}
	return 0
}
