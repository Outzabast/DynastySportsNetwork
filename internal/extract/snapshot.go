// Package extract turns a copied dynasty save into a normalized Snapshot.
//
// This is the single raw-to-canonical mapping (AGENTS.md: "Keep the
// raw-to-canonical mapping in one place"). It uses the pinned cfb-dynasty
// parser for decoding: its high-level Export for entity sections, and its
// public table API for stat columns the high-level export does not carry yet
// (kicking/punting and O-line lines per player, full team box scores, scoring
// play scorers). Those gaps are candidates to upstream.
package extract

import "time"

// Stats holds every numeric column of one raw stat row, keyed by the game's
// own column name (e.g. "PASSYARDS", "KICKFGMADE50ORMORE"). Nothing is dropped.
type Stats map[string]int64

// Snapshot is everything we read from one save, in our own shape.
type Snapshot struct {
	SourcePath    string    `json:"sourcePath"`
	SaveName      string    `json:"saveName"`
	SHA256        string    `json:"sha256"`
	Size          int64     `json:"size"`
	ModTime       time.Time `json:"modTime"`
	ParserVersion string    `json:"parserVersion"`
	SchemaVersion string    `json:"schemaVersion"`
	LeagueID      int64     `json:"leagueId"`

	Season Season `json:"season"`

	Teams    []Team    `json:"teams"`
	Players  []Player  `json:"players"`
	Coaches  []Coach   `json:"coaches"`
	Games    []Game    `json:"games"`
	Recruits []Recruit `json:"recruits"`

	PlayerGameStats   []PlayerGameStat   `json:"playerGameStats"`
	PlayerSeasonStats []PlayerSeasonStat `json:"playerSeasonStats"`
	PlayerCareerStats []PlayerCareerStat `json:"playerCareerStats"`
	TeamSeasonStats   []TeamSeasonStat   `json:"teamSeasonStats"`

	PlayerAwards        []PlayerAward        `json:"playerAwards"`
	LeagueAwards        []LeagueAward        `json:"leagueAwards"`
	ConferenceChampions []ConferenceChampion `json:"conferenceChampions"`
	RecordBook          []RecordBookEntry    `json:"recordBook"`
	Rivalries           []Rivalry            `json:"rivalries"`
	Injuries            []Injury             `json:"injuries"`
	LeavingPlayers      []LeavingPlayer      `json:"leavingPlayers"`
	PositionChanges     []PositionChange     `json:"positionChanges"`
	DepthCharts         []DepthChartSlot     `json:"depthCharts"`
	Bowls               []Bowl               `json:"bowls"`
	Conferences         []Conference         `json:"conferences"`
	TeamHistory         []TeamHistoryYear    `json:"teamHistory"`
}

// Conference is one conference's structure this season. A conference with a
// single division (or none) has no division split in its standings.
type Conference struct {
	Name      string     `json:"name"`
	ChampGame string     `json:"champGame"` // "" when the conference plays no title game
	Divisions []Division `json:"divisions"`
}

// Division is one division of a conference, in the game's listed order.
type Division struct {
	Name    string `json:"name"`
	TeamIDs []int  `json:"teamIds"`
}

// TeamHistoryYear is the game's own season-by-season record for a team
// (Team.TeamSeriesHistory). It reaches back before our first capture.
type TeamHistoryYear struct {
	TeamID       int                `json:"teamId"`
	Year         int                `json:"year"` // calendar year
	CoachName    string             `json:"coachName"`
	Conference   string             `json:"conference"`
	Division     string             `json:"division"`
	Wins         int                `json:"wins"`
	Losses       int                `json:"losses"`
	Ties         int                `json:"ties"`
	ConfWins     int                `json:"confWins"`
	ConfLosses   int                `json:"confLosses"`
	FinalRank    int                `json:"finalRank"`
	ConfStanding int                `json:"confStanding"`
	ConfChamp    bool               `json:"confChamp"`
	Postseason   []PostseasonResult `json:"postseason"`
}

// PostseasonResult is one bowl or CFP game in a team's season history.
type PostseasonResult struct {
	Bowl    string `json:"bowl"`
	Playoff bool   `json:"playoff"`
	Round   string `json:"round"` // CFP round when Playoff
	Won     bool   `json:"won"`
}

// Season is the save's position on the dynasty calendar.
type Season struct {
	BaseCalendarYear int             `json:"baseCalendarYear"`
	SeasonIndex      int             `json:"seasonIndex"` // SeasonInfo.CurrentYear (0 = first season)
	Year             int             `json:"year"`        // calendar year
	Stage            string          `json:"stage"`
	Week             int             `json:"week"`
	WeekType         string          `json:"weekType"`
	OffseasonStage   int             `json:"offseasonStage"`
	ConfChampWeek    int             `json:"confChampWeek"`
	Periods          map[string]bool `json:"periods"`
}

// Team is one FBS program, keyed by the stable Team.TeamIndex.
type Team struct {
	ID              int    `json:"id"`
	Row             int    `json:"row"`
	ShortName       string `json:"shortName"`
	LongName        string `json:"longName"`
	DisplayName     string `json:"displayName"`
	NickName        string `json:"nickName"`
	Abbrev          string `json:"abbrev"`
	AssetName       string `json:"assetName"`
	Conference      string `json:"conference"`
	Division        string `json:"division"` // "" when the conference has no division split
	PrimaryColor    string `json:"primaryColor"`
	SecondaryColor  string `json:"secondaryColor"`
	Wins            int    `json:"wins"`
	Losses          int    `json:"losses"`
	ConfWins        int    `json:"confWins"`
	ConfLosses      int    `json:"confLosses"`
	ConfStanding    int    `json:"confStanding"`
	DivStanding     int    `json:"divStanding"`
	Streak          int    `json:"streak"`
	APRank          int    `json:"apRank"` // media poll
	APPoints        int    `json:"apPoints"`
	APFirstPlace    int    `json:"apFirstPlace"`
	APLastRank      int    `json:"apLastRank"`
	CoachesRank     int    `json:"coachesRank"`
	CoachesPoints   int    `json:"coachesPoints"`
	CoachesFirst    int    `json:"coachesFirst"`
	CoachesLastRank int    `json:"coachesLastRank"`
	CFPRank         int    `json:"cfpRank"`
	CFPLastRank     int    `json:"cfpLastRank"`
	OffensiveRank   int    `json:"offensiveRank"`
	DefensiveRank   int    `json:"defensiveRank"`
	RecruitingRank  int    `json:"recruitingRank"`
	PlayoffStatus   string `json:"playoffStatus"`
	PlayoffRound    string `json:"playoffRound"`
	HeadCoachRow    int    `json:"headCoachRow"`
	OCRow           int    `json:"ocRow"`
	DCRow           int    `json:"dcRow"`
	RosterRows      []int  `json:"rosterRows"`
}

// Player is one Player-table row. Row is NOT an identity; see store/identity.
type Player struct {
	Row           int    `json:"row"`
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Position      string `json:"position"`
	TeamID        int    `json:"teamId"` // -1 when not on an FBS roster
	PrevTeamID    int    `json:"prevTeamId"`
	SchoolYear    string `json:"schoolYear"`
	Redshirt      string `json:"redshirt"`
	Jersey        int    `json:"jersey"`
	Height        int    `json:"height"`
	Weight        int    `json:"weight"`
	HomeTown      string `json:"homeTown"`
	HomeState     string `json:"homeState"`
	Handedness    string `json:"handedness"`
	AssetName     string `json:"assetName"` // PLYR_ASSETNAME, e.g. "AaronsOmar_30391"
	BirthDate     int64  `json:"birthDate"` // PLYR_BIRTHDATE raw value
	PortraitAsset string `json:"portraitAsset"`
	StarRating    int    `json:"starRating"`
	IsCaptain     bool   `json:"isCaptain"`
	InjuryStatus  string `json:"injuryStatus"`
	OnRoster      bool   `json:"onRoster"`
}

// Coach holds only real-world coach facts (no prestige, levels, security).
type Coach struct {
	Row          int    `json:"row"`
	FirstName    string `json:"firstName"`
	LastName     string `json:"lastName"`
	Position     string `json:"position"`
	TeamID       int    `json:"teamId"`
	Age          int    `json:"age"`
	AssetName    string `json:"assetName"`
	Wins         int    `json:"wins"`
	Losses       int    `json:"losses"`
	BowlWins     int    `json:"bowlWins"`
	BowlLosses   int    `json:"bowlLosses"`
	ConfTitles   int    `json:"confTitles"`
	NatlTitles   int    `json:"natlTitles"`
	PlayoffWins  int    `json:"playoffWins"`
	PlayoffLoss  int    `json:"playoffLosses"`
	SchoolWins   int    `json:"schoolWins"`
	SchoolLosses int    `json:"schoolLosses"`
	IsUser       bool   `json:"isUser"`

	SeasonWins      int    `json:"seasonWins"`
	SeasonLosses    int    `json:"seasonLosses"`
	SeasonsWithTeam int    `json:"seasonsWithTeam"`
	YearsCoaching   int    `json:"yearsCoaching"`
	HomeState       string `json:"homeState"`
}

// Game is one SeasonGame row in the current season.
type Game struct {
	Row          int    `json:"row"`
	SeasonIndex  int    `json:"seasonIndex"`
	Week         int    `json:"week"`
	WeekType     string `json:"weekType"`
	Status       string `json:"status"`
	Played       bool   `json:"played"`
	HomeTeamID   int    `json:"homeTeamId"`
	AwayTeamID   int    `json:"awayTeamId"`
	HomeScore    int    `json:"homeScore"`
	AwayScore    int    `json:"awayScore"`
	HomeQuarters []int  `json:"homeQuarters"`
	AwayQuarters []int  `json:"awayQuarters"`
	HomeOT       int    `json:"homeOT"`
	AwayOT       int    `json:"awayOT"`
	Overtime     bool   `json:"overtime"`
	Simmed       bool   `json:"simmed"`
	Month        int    `json:"month"`
	Day          int    `json:"day"`
	DayOfWeek    string `json:"dayOfWeek"`
	TimeOfDay    int    `json:"timeOfDay"` // minutes after midnight
	Network      string `json:"network"`
	Attendance   int    `json:"attendance"`
	Temperature  int    `json:"temperature"`
	Weather      string `json:"weather"`
	Wind         string `json:"wind"`
	WindSpeed    int    `json:"windSpeed"`
	Stadium      string `json:"stadium"`
	BowlName     string `json:"bowlName"`
	// GameType: conference, nonconference, conf_championship, bowl, playoff,
	// national_championship (preseason/unscheduled games stay "").
	GameType     string        `json:"gameType"`
	GameOfWeek   bool          `json:"gameOfWeek"`
	Rematch      bool          `json:"rematch"`
	HomeBox      Stats         `json:"homeBox"`
	AwayBox      Stats         `json:"awayBox"`
	ScoringPlays []ScoringPlay `json:"scoringPlays"`
}

// ScoringPlay is one entry of a game's scoring summary. The game only keeps
// these during the week the game is played, so they must be captured then.
type ScoringPlay struct {
	Seq          int      `json:"seq"`
	Quarter      int      `json:"quarter"`
	ClockSec     int      `json:"clockSec"`
	HomeScore    int      `json:"homeScore"`
	AwayScore    int      `json:"awayScore"`
	HomePrev     int      `json:"homePrev"`
	AwayPrev     int      `json:"awayPrev"`
	Conversion   string   `json:"conversion"`
	ScoringTeam  string   `json:"scoringTeam"` // "home" / "away"
	Participants []Scorer `json:"participants"`
}

// Scorer is a player snapshot attached to a scoring play.
type Scorer struct {
	PlayerRow int   `json:"playerRow"`
	Stats     Stats `json:"stats"`
}

// PlayerGameStat is one category line for one player in one game.
type PlayerGameStat struct {
	GameRow   int    `json:"gameRow"`
	PlayerRow int    `json:"playerRow"`
	TeamID    int    `json:"teamId"`
	Category  string `json:"category"`
	SourceRow int    `json:"sourceRow"`
	Stats     Stats  `json:"stats"`
}

// PlayerSeasonStat is one category line for one player in one season.
type PlayerSeasonStat struct {
	PlayerRow   int    `json:"playerRow"`
	SeasonIndex int    `json:"seasonIndex"`
	TeamID      int    `json:"teamId"`
	TeamAbbrev  string `json:"teamAbbrev"`
	Category    string `json:"category"`
	SourceRow   int    `json:"sourceRow"`
	Stats       Stats  `json:"stats"`
}

// PlayerCareerStat is one category line of the save's career totals.
type PlayerCareerStat struct {
	PlayerRow int    `json:"playerRow"`
	Category  string `json:"category"`
	SourceRow int    `json:"sourceRow"`
	Stats     Stats  `json:"stats"`
}

// TeamSeasonStat is the save's current-season team totals.
type TeamSeasonStat struct {
	TeamID int   `json:"teamId"`
	Stats  Stats `json:"stats"`
}

type Recruit struct {
	PlayerRow     int    `json:"playerRow"`
	FirstName     string `json:"firstName"`
	LastName      string `json:"lastName"`
	Position      string `json:"position"`
	HomeTown      string `json:"homeTown"`
	HomeState     string `json:"homeState"`
	Class         string `json:"class"`
	Stars         int    `json:"stars"`
	NationalRank  int    `json:"nationalRank"`
	PositionRank  int    `json:"positionRank"`
	StateRank     int    `json:"stateRank"`
	Stage         string `json:"stage"`
	CommittedTeam int    `json:"committedTeam"`
	Height        int    `json:"height"`
	Weight        int    `json:"weight"`
}

type PlayerAward struct {
	PlayerRow   int    `json:"playerRow"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	TeamID      int    `json:"teamId"`
	Position    string `json:"position"`
	AwardType   string `json:"awardType"`
	Period      string `json:"period"`
	PeriodIndex int    `json:"periodIndex"`
}

type LeagueAward struct {
	Row       int    `json:"row"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Position  string `json:"position"`
	AwardType string `json:"awardType"`
	TeamName  string `json:"teamName"`
}

type ConferenceChampion struct {
	Row         int    `json:"row"`
	Conference  string `json:"conference"`
	Winner      string `json:"winner"`
	Loser       string `json:"loser"`
	WinnerScore int    `json:"winnerScore"`
	LoserScore  int    `json:"loserScore"`
	WinnerRank  int    `json:"winnerRank"`
	LoserRank   int    `json:"loserRank"`
	CoachName   string `json:"coachName"`
}

type RecordBookEntry struct {
	Scope     string `json:"scope"`
	ScopeName string `json:"scopeName"`
	Period    string `json:"period"`
	StatType  string `json:"statType"`
	Rank      int    `json:"rank"`
	Value     int    `json:"value"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Position  string `json:"position"`
	TeamName  string `json:"teamName"`
	Year      int    `json:"year"`
}

type Rivalry struct {
	Row        int    `json:"row"`
	Name       string `json:"name"`
	Team1      int    `json:"team1"`
	Team2      int    `json:"team2"`
	Team1Wins  int    `json:"team1Wins"`
	Team2Wins  int    `json:"team2Wins"`
	StreakLen  int    `json:"streakLen"`
	StreakTeam int    `json:"streakTeam"`
}

type Injury struct {
	PlayerRow int    `json:"playerRow"`
	TeamID    int    `json:"teamId"`
	Type      string `json:"type"`
	Severity  string `json:"severity"`
	MinWeeks  int    `json:"minWeeks"`
	MaxWeeks  int    `json:"maxWeeks"`
}

type LeavingPlayer struct {
	PlayerRow int    `json:"playerRow"`
	TeamID    int    `json:"teamId"`
	LeaveType string `json:"leaveType"`
	Round     int    `json:"projectedRound"`
}

type PositionChange struct {
	PlayerRow   int    `json:"playerRow"`
	OldPosition string `json:"oldPosition"`
	NewPosition string `json:"newPosition"`
	SeasonIndex int    `json:"seasonIndex"`
	Week        int    `json:"week"`
	Stage       string `json:"stage"`
	OldTeam     int    `json:"oldTeam"`
	NewTeam     int    `json:"newTeam"`
}

type DepthChartSlot struct {
	TeamID    int    `json:"teamId"`
	Position  string `json:"position"`
	Order     int    `json:"order"`
	PlayerRow int    `json:"playerRow"`
}

type Bowl struct {
	Row         int    `json:"row"`
	Name        string `json:"name"`
	IsPlayoff   bool   `json:"isPlayoff"`
	BracketSlot int    `json:"bracketSlot"`
}
