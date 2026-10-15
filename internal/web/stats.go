package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

type S = store.Stats

// Col is one column of an ESPN-style stat table.
type Col struct {
	Key   string // short header, e.g. "YDS"
	Title string // tooltip
	Val   func(S) string
	Num   func(S) float64 // for sorting / leaders
}

// Table is a stat table definition over one display group.
type Table struct {
	ID     string
	Name   string
	Group  string
	Has    func(S) bool
	Cols   []Col
	SortBy int // column index used to order rows
}

func n(s S, k string) int64 { return s[k] }

func ratio(a, b int64, digits int) string {
	if b == 0 {
		return "0." + strings.Repeat("0", digits)
	}
	return fmt.Sprintf("%.*f", digits, float64(a)/float64(b))
}

func pct(a, b int64) string {
	if b == 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", 100*float64(a)/float64(b))
}

func intCol(key, title, field string) Col {
	return Col{key, title, func(s S) string { return fmt.Sprint(n(s, field)) }, func(s S) float64 { return float64(n(s, field)) }}
}

func sumCol(key, title string, fields ...string) Col {
	f := func(s S) int64 {
		var t int64
		for _, k := range fields {
			t += s[k]
		}
		return t
	}
	return Col{key, title, func(s S) string { return fmt.Sprint(f(s)) }, func(s S) float64 { return float64(f(s)) }}
}

func avgCol(key, title, num, den string) Col {
	return Col{key, title, func(s S) string { return ratio(n(s, num), n(s, den), 1) }, func(s S) float64 {
		if n(s, den) == 0 {
			return 0
		}
		return float64(n(s, num)) / float64(n(s, den))
	}}
}

func fracCol(key, title, made, att string) Col {
	return Col{key, title, func(s S) string { return fmt.Sprintf("%d/%d", n(s, made), n(s, att)) }, func(s S) float64 { return float64(n(s, made)) }}
}

func pctCol(key, title, made, att string) Col {
	return Col{key, title, func(s S) string { return pct(n(s, made), n(s, att)) }, func(s S) float64 {
		if n(s, att) == 0 {
			return 0
		}
		return float64(n(s, made)) / float64(n(s, att))
	}}
}

// passerRating is the NCAA passing efficiency rating.
func passerRating(s S) float64 {
	att := float64(n(s, "PASSATTEMPTS"))
	if att == 0 {
		return 0
	}
	return (8.4*float64(n(s, "PASSYARDS")) + 330*float64(n(s, "PASSTDS")) + 100*float64(n(s, "PASSCOMPLETED")) - 200*float64(n(s, "PASSINTS"))) / att
}

var rtgCol = Col{"RTG", "Passing Efficiency Rating", func(s S) string { return fmt.Sprintf("%.1f", passerRating(s)) }, passerRating}

func sacks(s S) float64 { return float64(n(s, "DLINESACKS")) + 0.5*float64(n(s, "DLINEHALFSACK")) }

var sackCol = Col{"SACK", "Sacks", func(s S) string {
	v := sacks(s)
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}, sacks}

func fgPoints(s S) int64 { return 3*n(s, "KICKFGMADE") + n(s, "KICKEPMADE") }

// Box score tables (one game).
var boxTables = []Table{
	{ID: "passing", Name: "Passing", Group: "offense", Has: func(s S) bool { return n(s, "PASSATTEMPTS") > 0 }, Cols: []Col{
		fracCol("C/ATT", "Completions/Attempts", "PASSCOMPLETED", "PASSATTEMPTS"), intCol("YDS", "Passing Yards", "PASSYARDS"),
		avgCol("AVG", "Yards per Pass Attempt", "PASSYARDS", "PASSATTEMPTS"), intCol("TD", "Passing Touchdowns", "PASSTDS"),
		intCol("INT", "Interceptions", "PASSINTS"), intCol("SACKS", "Times Sacked", "PASSSACKED"), rtgCol}, SortBy: 1},
	{ID: "rushing", Name: "Rushing", Group: "offense", Has: func(s S) bool { return n(s, "RUSHATTEMPTS") > 0 }, Cols: []Col{
		intCol("CAR", "Rushing Attempts", "RUSHATTEMPTS"), intCol("YDS", "Rushing Yards", "RUSHYARDS"), avgCol("AVG", "Yards per Rush", "RUSHYARDS", "RUSHATTEMPTS"),
		intCol("TD", "Rushing Touchdowns", "RUSHTDS"), intCol("LONG", "Long Rush", "RUSHLONGEST")}, SortBy: 1},
	{ID: "receiving", Name: "Receiving", Group: "offense", Has: func(s S) bool { return n(s, "RECEIVECATCHES") > 0 }, Cols: []Col{
		intCol("REC", "Receptions", "RECEIVECATCHES"), intCol("YDS", "Receiving Yards", "RECEIVEYARDS"), avgCol("AVG", "Yards per Reception", "RECEIVEYARDS", "RECEIVECATCHES"),
		intCol("TD", "Receiving Touchdowns", "RECEIVETDS"), intCol("LONG", "Long Reception", "RECEIVELONGEST")}, SortBy: 1},
	{ID: "fumbles", Name: "Fumbles", Group: "offense", Has: func(s S) bool { return n(s, "RUSHFUMBLES") > 0 }, Cols: []Col{
		intCol("FUM", "Fumbles", "RUSHFUMBLES")}, SortBy: 0},
	{ID: "defensive", Name: "Defensive", Group: "defense", Has: func(s S) bool {
		return n(s, "DEFTACKLES")+n(s, "ASSDEFTACKLES")+n(s, "DLINESACKS")+n(s, "DLINEHALFSACK")+n(s, "DEFPASSDEFLECTIONS")+n(s, "DSECINTS")+n(s, "DLINEFORCEDFUMBLES") > 0
	}, Cols: []Col{
		sumCol("TOT", "Total Tackles", "DEFTACKLES", "ASSDEFTACKLES"), intCol("SOLO", "Solo Tackles", "DEFTACKLES"), sackCol,
		intCol("TFL", "Tackles for Loss", "DEFTACKLESFORLOSS"), intCol("PD", "Passes Defended", "DEFPASSDEFLECTIONS"),
		intCol("FF", "Forced Fumbles", "DLINEFORCEDFUMBLES"), sumCol("TD", "Defensive Touchdowns", "DSECINTTDS", "DLINEFUMBLETDS")}, SortBy: 0},
	{ID: "interceptions", Name: "Interceptions", Group: "defense", Has: func(s S) bool { return n(s, "DSECINTS") > 0 }, Cols: []Col{
		intCol("INT", "Interceptions", "DSECINTS"), intCol("YDS", "Interception Return Yards", "DSECINTRETURNYARDS"), intCol("TD", "Interception Return TDs", "DSECINTTDS")}, SortBy: 0},
	{ID: "kickreturns", Name: "Kick Returns", Group: "", Has: func(s S) bool { return n(s, "KRETATTEMPTS") > 0 }, Cols: []Col{
		intCol("NO", "Kick Returns", "KRETATTEMPTS"), intCol("YDS", "Kick Return Yards", "KRETYARDS"), avgCol("AVG", "Yards per Kick Return", "KRETYARDS", "KRETATTEMPTS"),
		intCol("LONG", "Long Kick Return", "KRETLONGEST"), intCol("TD", "Kick Return Touchdowns", "KRETTDS")}, SortBy: 1},
	{ID: "puntreturns", Name: "Punt Returns", Group: "", Has: func(s S) bool { return n(s, "PRETATTEMPTS") > 0 }, Cols: []Col{
		intCol("NO", "Punt Returns", "PRETATTEMPTS"), intCol("YDS", "Punt Return Yards", "PRETYARDS"), avgCol("AVG", "Yards per Punt Return", "PRETYARDS", "PRETATTEMPTS"),
		intCol("LONG", "Long Punt Return", "PRETLONGEST"), intCol("TD", "Punt Return Touchdowns", "PRETTDS")}, SortBy: 1},
	{ID: "kicking", Name: "Kicking", Group: "kicking", Has: func(s S) bool { return n(s, "KICKFGATTEMPTS")+n(s, "KICKEPATTEMPTS") > 0 }, Cols: []Col{
		fracCol("FG", "Field Goals Made/Attempted", "KICKFGMADE", "KICKFGATTEMPTS"), pctCol("PCT", "Field Goal Percentage", "KICKFGMADE", "KICKFGATTEMPTS"),
		intCol("LONG", "Long Field Goal", "KICKFGLONGEST"), fracCol("XP", "Extra Points Made/Attempted", "KICKEPMADE", "KICKEPATTEMPTS"),
		{"PTS", "Kicking Points", func(s S) string { return fmt.Sprint(fgPoints(s)) }, func(s S) float64 { return float64(fgPoints(s)) }}}, SortBy: 4},
	{ID: "punting", Name: "Punting", Group: "kicking", Has: func(s S) bool { return n(s, "PUNTATTEMPTS") > 0 }, Cols: []Col{
		intCol("NO", "Punts", "PUNTATTEMPTS"), intCol("YDS", "Punt Yards", "PUNTYARDS"), avgCol("AVG", "Yards per Punt", "PUNTYARDS", "PUNTATTEMPTS"),
		intCol("TB", "Touchbacks", "PUNTTOUCHBACKS"), intCol("In 20", "Punts Inside 20", "PUNTIN20"), intCol("LONG", "Long Punt", "PUNTLONGEST")}, SortBy: 0},
	{ID: "blocking", Name: "Offensive Line", Group: "oline", Has: func(s S) bool { return n(s, "OLINEPANCAKES")+n(s, "OLINESACKSALLOWED") > 0 }, Cols: []Col{
		intCol("PAN", "Pancake Blocks", "OLINEPANCAKES"), intCol("SA", "Sacks Allowed", "OLINESACKSALLOWED")}, SortBy: 0},
}

var gpCol = intCol("GP", "Games Played", "GAMESPLAYED")

// Season tables (player pages, team stats, leaders) show more columns.
var seasonTables = []Table{
	{ID: "passing", Name: "Passing", Group: "offense", Has: func(s S) bool { return n(s, "PASSATTEMPTS") > 0 }, Cols: []Col{
		gpCol, intCol("CMP", "Completions", "PASSCOMPLETED"), intCol("ATT", "Attempts", "PASSATTEMPTS"), pctCol("CMP%", "Completion Percentage", "PASSCOMPLETED", "PASSATTEMPTS"),
		intCol("YDS", "Passing Yards", "PASSYARDS"), avgCol("AVG", "Yards per Attempt", "PASSYARDS", "PASSATTEMPTS"), intCol("TD", "Passing TDs", "PASSTDS"),
		intCol("INT", "Interceptions", "PASSINTS"), intCol("LNG", "Long Pass", "PASSLONGEST"), intCol("SACK", "Times Sacked", "PASSSACKED"), rtgCol}, SortBy: 4},
	{ID: "rushing", Name: "Rushing", Group: "offense", Has: func(s S) bool { return n(s, "RUSHATTEMPTS") > 0 }, Cols: []Col{
		gpCol, intCol("CAR", "Rushing Attempts", "RUSHATTEMPTS"), intCol("YDS", "Rushing Yards", "RUSHYARDS"), avgCol("AVG", "Yards per Carry", "RUSHYARDS", "RUSHATTEMPTS"),
		intCol("TD", "Rushing TDs", "RUSHTDS"), intCol("LNG", "Long Rush", "RUSHLONGEST"), intCol("20+", "Runs of 20+ Yards", "RUSH20YARDRUNS"),
		intCol("BTK", "Broken Tackles", "RUSHBROKENTACKLES"), intCol("YACON", "Yards After Contact", "RUSHYARDSAFTER1STHIT"), intCol("FUM", "Fumbles", "RUSHFUMBLES")}, SortBy: 2},
	{ID: "receiving", Name: "Receiving", Group: "offense", Has: func(s S) bool { return n(s, "RECEIVECATCHES") > 0 }, Cols: []Col{
		gpCol, intCol("REC", "Receptions", "RECEIVECATCHES"), intCol("YDS", "Receiving Yards", "RECEIVEYARDS"), avgCol("AVG", "Yards per Reception", "RECEIVEYARDS", "RECEIVECATCHES"),
		intCol("TD", "Receiving TDs", "RECEIVETDS"), intCol("LNG", "Long Reception", "RECEIVELONGEST"), intCol("YAC", "Yards After Catch", "RECEIVEYARDSAFTER"),
		intCol("DRP", "Drops", "RECEIVEDROPS")}, SortBy: 2},
	{ID: "defense", Name: "Defense", Group: "defense", Has: func(s S) bool {
		return n(s, "DEFTACKLES")+n(s, "ASSDEFTACKLES")+n(s, "DLINESACKS")+n(s, "DLINEHALFSACK")+n(s, "DEFPASSDEFLECTIONS")+n(s, "DSECINTS") > 0
	}, Cols: []Col{
		gpCol, sumCol("TOT", "Total Tackles", "DEFTACKLES", "ASSDEFTACKLES"), intCol("SOLO", "Solo Tackles", "DEFTACKLES"), intCol("AST", "Assisted Tackles", "ASSDEFTACKLES"),
		sackCol, intCol("TFL", "Tackles for Loss", "DEFTACKLESFORLOSS"), intCol("PD", "Passes Defended", "DEFPASSDEFLECTIONS"), intCol("INT", "Interceptions", "DSECINTS"),
		intCol("YDS", "Interception Return Yards", "DSECINTRETURNYARDS"), intCol("LNG", "Long Interception Return", "DSECINTLONGESTRETURN"), intCol("TD", "Interception TDs", "DSECINTTDS"),
		intCol("FF", "Forced Fumbles", "DLINEFORCEDFUMBLES"), intCol("FR", "Fumble Recoveries", "DLINEFUMBLERECOVERIES"), intCol("FTD", "Fumble Return TDs", "DLINEFUMBLETDS"),
		intCol("BLK", "Kicks Blocked", "DLINEBLOCKS"), intCol("SFTY", "Safeties", "DLINESAFETIES"), intCol("CA", "Catches Allowed", "CTHALLOWED"), intCol("HITS", "Big Hits", "BIGHITS")}, SortBy: 1},
	{ID: "kicking", Name: "Kicking", Group: "kicking", Has: func(s S) bool { return n(s, "KICKFGATTEMPTS")+n(s, "KICKEPATTEMPTS")+n(s, "KICKNUMKICKOFFS") > 0 }, Cols: []Col{
		gpCol, intCol("FGM", "Field Goals Made", "KICKFGMADE"), intCol("FGA", "Field Goals Attempted", "KICKFGATTEMPTS"), pctCol("PCT", "Field Goal Percentage", "KICKFGMADE", "KICKFGATTEMPTS"),
		intCol("LNG", "Long Field Goal", "KICKFGLONGEST"), fracCol("1-29", "Made/Att 1-29 Yards", "KICKFGMADE29ORLESS", "KICKFGATTEMPTS29ORLESS"),
		fracCol("30-39", "Made/Att 30-39 Yards", "KICKFGMADE30TO39", "KICKFGATTEMPTS30TO39"), fracCol("40-49", "Made/Att 40-49 Yards", "KICKFGMADE40TO49", "KICKFGATTEMPTS40TO49"),
		fracCol("50+", "Made/Att 50+ Yards", "KICKFGMADE50ORMORE", "KICKFGATTEMPTS50ORMORE"), intCol("BLK", "Field Goals Blocked", "KICKFGBLOCKED"),
		intCol("XPM", "Extra Points Made", "KICKEPMADE"), intCol("XPA", "Extra Points Attempted", "KICKEPATTEMPTS"),
		intCol("KO", "Kickoffs", "KICKNUMKICKOFFS"), intCol("TB", "Kickoff Touchbacks", "KICKTOUCHBACKS"),
		{"PTS", "Kicking Points", func(s S) string { return fmt.Sprint(fgPoints(s)) }, func(s S) float64 { return float64(fgPoints(s)) }}}, SortBy: 1},
	{ID: "punting", Name: "Punting", Group: "kicking", Has: func(s S) bool { return n(s, "PUNTATTEMPTS") > 0 }, Cols: []Col{
		gpCol, intCol("PUNTS", "Punts", "PUNTATTEMPTS"), intCol("YDS", "Punt Yards", "PUNTYARDS"), avgCol("AVG", "Gross Average", "PUNTYARDS", "PUNTATTEMPTS"),
		avgCol("NET", "Net Average", "PUNTNETYARDS", "PUNTATTEMPTS"), intCol("LNG", "Long Punt", "PUNTLONGEST"), intCol("IN20", "Inside 20", "PUNTIN20"),
		intCol("TB", "Touchbacks", "PUNTTOUCHBACKS"), intCol("BLK", "Blocked", "PUNTBLOCKED")}, SortBy: 2},
	{ID: "returns", Name: "Returns", Group: "", Has: func(s S) bool { return n(s, "KRETATTEMPTS")+n(s, "PRETATTEMPTS") > 0 }, Cols: []Col{
		intCol("KR", "Kick Returns", "KRETATTEMPTS"), intCol("KYDS", "Kick Return Yards", "KRETYARDS"), avgCol("KAVG", "Yards per Kick Return", "KRETYARDS", "KRETATTEMPTS"),
		intCol("KLNG", "Long Kick Return", "KRETLONGEST"), intCol("KTD", "Kick Return TDs", "KRETTDS"),
		intCol("PR", "Punt Returns", "PRETATTEMPTS"), intCol("PYDS", "Punt Return Yards", "PRETYARDS"), avgCol("PAVG", "Yards per Punt Return", "PRETYARDS", "PRETATTEMPTS"),
		intCol("PLNG", "Long Punt Return", "PRETLONGEST"), intCol("PTD", "Punt Return TDs", "PRETTDS")}, SortBy: 1},
	{ID: "blocking", Name: "Offensive Line", Group: "oline", Has: func(s S) bool { return n(s, "GAMESPLAYED") > 0 || n(s, "OLINEPANCAKES") > 0 }, Cols: []Col{
		gpCol, intCol("GS", "Games Started", "GAMESSTARTED"), intCol("PAN", "Pancake Blocks", "OLINEPANCAKES"), intCol("SA", "Sacks Allowed", "OLINESACKSALLOWED"),
		intCol("SNAPS", "Downs Played", "DOWNSPLAYED")}, SortBy: 2},
}

// tableFor finds a table definition by id.
func tableFor(tables []Table, id string) *Table {
	for i := range tables {
		if tables[i].ID == id {
			return &tables[i]
		}
	}
	return nil
}

// groupOK reports whether a line's group feeds a table (returns tables accept
// both offense and defense return rows).
func (t *Table) groupOK(g string) bool { return t.Group == "" || t.Group == g }

// Row is one rendered row of a stat table.
type Row struct {
	Label  string
	Href   string
	Sub    string
	Cells  []string
	sortV  float64
	Team   int
	Season int
}

// RenderedTable is a table ready for the template.
type RenderedTable struct {
	ID, Name string
	Headers  []Col
	Rows     []Row
	Total    *Row
}

// BoxCategory is one box score category with both teams' tables.
type BoxCategory struct {
	Name       string
	Away, Home *RenderedTable
}

// leaderStat is a leaderboard category.
type leaderStat struct {
	ID, Name, Short string
	Table           string
	Col             int
	Min             func(S) bool // qualifier
	Lower           bool         // lower is better
}

var leaderStats = []leaderStat{
	{"passyds", "Passing Yards", "YDS", "passing", 4, nil, false},
	{"passtd", "Passing Touchdowns", "TD", "passing", 6, nil, false},
	{"rating", "Passing Efficiency", "RTG", "passing", 10, func(s S) bool { return n(s, "PASSATTEMPTS") >= 15*n(s, "GAMESPLAYED") && n(s, "PASSATTEMPTS") > 0 }, false},
	{"rushyds", "Rushing Yards", "YDS", "rushing", 2, nil, false},
	{"rushtd", "Rushing Touchdowns", "TD", "rushing", 4, nil, false},
	{"ypc", "Yards Per Carry", "AVG", "rushing", 3, func(s S) bool { return n(s, "RUSHATTEMPTS") >= 6*n(s, "GAMESPLAYED") && n(s, "RUSHATTEMPTS") > 0 }, false},
	{"recyds", "Receiving Yards", "YDS", "receiving", 2, nil, false},
	{"rec", "Receptions", "REC", "receiving", 1, nil, false},
	{"rectd", "Receiving Touchdowns", "TD", "receiving", 4, nil, false},
	{"tackles", "Total Tackles", "TOT", "defense", 1, nil, false},
	{"sacks", "Sacks", "SACK", "defense", 4, nil, false},
	{"tfl", "Tackles For Loss", "TFL", "defense", 5, nil, false},
	{"ints", "Interceptions", "INT", "defense", 7, nil, false},
	{"pd", "Passes Defended", "PD", "defense", 6, nil, false},
	{"ff", "Forced Fumbles", "FF", "defense", 11, nil, false},
	{"fgm", "Field Goals Made", "FGM", "kicking", 1, nil, false},
	{"kpts", "Kicking Points", "PTS", "kicking", 14, nil, false},
	{"puntavg", "Punting Average", "AVG", "punting", 3, func(s S) bool { return n(s, "PUNTATTEMPTS") >= 3*n(s, "GAMESPLAYED") && n(s, "PUNTATTEMPTS") > 0 }, false},
	{"kryds", "Kick Return Yards", "KYDS", "returns", 1, nil, false},
	{"pryds", "Punt Return Yards", "PYDS", "returns", 6, nil, false},
	{"pancakes", "Pancake Blocks", "PAN", "blocking", 2, nil, false},
}

func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].sortV > rows[j].sortV })
}

// hiddenStat lists raw columns that are bookkeeping or game-internal grades,
// not stats a fan would see.
var hiddenStat = map[string]bool{"SEAS_YEAR": true, "YEARBYYEARTEAMINDEX": true, "STAT_KEEP": true, "GAMERATING": true}

// rawLabels gives readable names to every raw column we know; anything new
// still shows (title-cased) so no recorded stat is ever dropped.
var rawLabels = map[string]string{
	"4QCOMEBACKS": "4th Quarter Comebacks", "DOWNSPLAYED": "Downs Played", "FIRSTDOWNS": "First Downs", "GAMESPLAYED": "Games Played", "GAMESSTARTED": "Games Started",
	"PASSATTEMPTS": "Pass Attempts", "PASSCOMPLETED": "Completions", "PASSINTS": "Interceptions Thrown", "PASSLONGEST": "Long Pass", "PASSSACKED": "Times Sacked",
	"PASSTDS": "Passing TDs", "PASSYARDS": "Passing Yards", "RECEIVECATCHES": "Receptions", "RECEIVEDROPS": "Drops", "RECEIVELONGEST": "Long Reception",
	"RECEIVETDS": "Receiving TDs", "RECEIVEYARDS": "Receiving Yards", "RECEIVEYARDSAFTER": "Yards After Catch", "RUSH20YARDRUNS": "20+ Yard Runs",
	"RUSHATTEMPTS": "Rushing Attempts", "RUSHBROKENTACKLES": "Broken Tackles", "RUSHFUMBLES": "Fumbles", "RUSHLONGEST": "Long Rush", "RUSHTDS": "Rushing TDs",
	"RUSHYARDS": "Rushing Yards", "RUSHYARDSAFTER1STHIT": "Yards After Contact", "ASSDEFTACKLES": "Assisted Tackles", "BIGHITS": "Big Hits",
	"CTHALLOWED": "Catches Allowed", "DEFPASSDEFLECTIONS": "Passes Defended", "DEFTACKLES": "Solo Tackles", "DEFTACKLESFORLOSS": "Tackles For Loss",
	"DLINEBLOCKS": "Kicks Blocked", "DLINEFORCEDFUMBLES": "Forced Fumbles", "DLINEFUMBLERECOVERIES": "Fumble Recoveries", "DLINEFUMBLERECOVERYYARDS": "Fumble Return Yards",
	"DLINEFUMBLETDS": "Fumble Return TDs", "DLINEHALFSACK": "Half Sacks", "DLINESACKS": "Sacks", "DLINESAFETIES": "Safeties", "DSECINTLONGESTRETURN": "Long INT Return",
	"DSECINTRETURNYARDS": "INT Return Yards", "DSECINTS": "Interceptions", "DSECINTTDS": "INT Return TDs", "KICKEPATTEMPTS": "XP Attempts", "KICKEPBLOCKED": "XP Blocked",
	"KICKEPMADE": "XP Made", "KICKFGATTEMPTS": "FG Attempts", "KICKFGATTEMPTS29ORLESS": "FG Att 1-29", "KICKFGATTEMPTS30TO39": "FG Att 30-39",
	"KICKFGATTEMPTS40TO49": "FG Att 40-49", "KICKFGATTEMPTS50ORMORE": "FG Att 50+", "KICKFGBLOCKED": "FG Blocked", "KICKFGLONGEST": "Long FG", "KICKFGMADE": "FG Made",
	"KICKFGMADE29ORLESS": "FG Made 1-29", "KICKFGMADE30TO39": "FG Made 30-39", "KICKFGMADE40TO49": "FG Made 40-49", "KICKFGMADE50ORMORE": "FG Made 50+",
	"KICKNUMKICKOFFS": "Kickoffs", "KICKTOUCHBACKS": "Kickoff Touchbacks", "PUNTATTEMPTS": "Punts", "PUNTBLOCKED": "Punts Blocked", "PUNTIN20": "Punts Inside 20",
	"PUNTLONGEST": "Long Punt", "PUNTNETYARDS": "Net Punt Yards", "PUNTTOUCHBACKS": "Punt Touchbacks", "PUNTYARDS": "Punt Yards", "GAMEWINFGATTEMPTS": "Game-Winning FG Att",
	"GAMEWINFGSMADE": "Game-Winning FG Made", "OLINEPANCAKES": "Pancakes", "OLINESACKSALLOWED": "Sacks Allowed", "KRETATTEMPTS": "Kick Returns", "KRETLONGEST": "Long Kick Return",
	"KRETTDS": "Kick Return TDs", "KRETYARDS": "Kick Return Yards", "PRETATTEMPTS": "Punt Returns", "PRETLONGEST": "Long Punt Return", "PRETTDS": "Punt Return TDs",
	"PRETYARDS": "Punt Return Yards",
}

func rawLabel(k string) string {
	if l, ok := rawLabels[k]; ok {
		return l
	}
	return strings.Title(strings.ToLower(strings.ReplaceAll(k, "_", " ")))
}

// teamBox is the ESPN "Team Stats" comparison for a game.
type boxRow struct {
	Label       string
	Away, Home  string
	Indent      bool
}

func clock(sec int64) string { return fmt.Sprintf("%d:%02d", sec/60, sec%60) }

func teamBoxRows(away, home S) []boxRow {
	if away == nil || home == nil {
		return nil
	}
	r := func(label string, f func(S) string, indent bool) boxRow { return boxRow{label, f(away), f(home), indent} }
	i := func(k string) func(S) string { return func(s S) string { return fmt.Sprint(s[k]) } }
	return []boxRow{
		r("1st Downs", i("FIRSTDOWNS"), false),
		r("3rd down efficiency", func(s S) string { return fmt.Sprintf("%d-%d", s["THIRDDOWNCONV"], s["THIRDDOWNS"]) }, false),
		r("4th down efficiency", func(s S) string { return fmt.Sprintf("%d-%d", s["FOURTHDOWNCONV"], s["FOURTHDOWNS"]) }, false),
		// TOTALYARDS also counts kick and punt return yards; ESPN total yards is
		// passing + rushing, which the game keeps as OFFYARDS.
		r("Total Yards", i("OFFYARDS"), false),
		r("Passing", i("OFFPASSYARDS"), false),
		r("Comp-Att", func(s S) string { return fmt.Sprintf("%d-%d", s["PASSCOMPLETIONS"], s["PASSATTEMPTS"]) }, true),
		r("Yards per pass", func(s S) string { return ratio(s["OFFPASSYARDS"], s["PASSATTEMPTS"], 1) }, true),
		r("Interceptions thrown", i("PASSINTS"), true),
		r("Rushing", i("OFFRUSHYARDS"), false),
		r("Rushing Attempts", i("RUSHATTEMPTS"), true),
		r("Yards per rush", func(s S) string { return ratio(s["OFFRUSHYARDS"], s["RUSHATTEMPTS"], 1) }, true),
		r("Red Zone (Made-Att)", func(s S) string { return fmt.Sprintf("%d-%d", s["OFFREDZONETDS"], s["OFFREDZONES"]) }, false),
		r("Penalties", func(s S) string { return fmt.Sprintf("%d-%d", s["PENALTIES"], s["PENALTYYARDS"]) }, false),
		r("Turnovers", i("GIVEAWAYS"), false),
		r("Fumbles lost", i("FUMBLESLOST"), true),
		r("Interceptions thrown", i("PASSINTS"), true),
		r("Sacks", i("SACKS"), false),
		r("Punts", i("PUNTS"), false),
		r("Kick Return Yards", i("KICKRETURNYARDS"), false),
		r("2-Pt Conversions", func(s S) string { return fmt.Sprintf("%d-%d", s["TWOPOINTCONVMADE"], s["TWOPOINTCONVATTEMPTS"]) }, false),
		r("Possession", func(s S) string { return clock(s["POSSESSIONTIME"]) }, false),
	}
}

// teamSeasonRows is the team stats summary (season totals).
func teamSeasonRows(s S, games int) []boxRow {
	if s == nil {
		return nil
	}
	pg := func(k string) string {
		if games == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f", float64(s[k])/float64(games))
	}
	row := func(label string, total string, per string) boxRow { return boxRow{label, total, per, false} }
	return []boxRow{
		row("Total Yards", fmt.Sprint(s["OFFYARDS"]), pg("OFFYARDS")),
		row("Passing Yards", fmt.Sprint(s["OFFPASSYARDS"]), pg("OFFPASSYARDS")),
		row("Rushing Yards", fmt.Sprint(s["OFFRUSHYARDS"]), pg("OFFRUSHYARDS")),
		row("Passing TD", fmt.Sprint(s["PASSTDS"]), pg("PASSTDS")),
		row("Rushing TD", fmt.Sprint(s["RUSHTDS"]), pg("RUSHTDS")),
		row("First Downs", fmt.Sprint(s["FIRSTDOWNS"]), pg("FIRSTDOWNS")),
		row("3rd Down Conv", fmt.Sprintf("%d-%d", s["THIRDDOWNCONV"], s["THIRDDOWNS"]), pct(s["THIRDDOWNCONV"], s["THIRDDOWNS"])+"%"),
		row("4th Down Conv", fmt.Sprintf("%d-%d", s["FOURTHDOWNCONV"], s["FOURTHDOWNS"]), pct(s["FOURTHDOWNCONV"], s["FOURTHDOWNS"])+"%"),
		row("Red Zone TD", fmt.Sprintf("%d-%d", s["OFFREDZONETDS"], s["OFFREDZONES"]), pct(s["OFFREDZONETDS"], s["OFFREDZONES"])+"%"),
		row("Penalties-Yards", fmt.Sprintf("%d-%d", s["PENALTIES"], s["PENALTYYARDS"]), pg("PENALTYYARDS")),
		row("Turnovers (Giveaways)", fmt.Sprint(s["GIVEAWAYS"]), pg("GIVEAWAYS")),
		row("Takeaways", fmt.Sprint(s["TAKEAWAYS"]), pg("TAKEAWAYS")),
		row("Sacks", fmt.Sprint(s["SACKS"]), pg("SACKS")),
		row("Total Yards Allowed", fmt.Sprint(s["DEFPASSYARDS"]+s["DEFRUSHYARDS"]), func() string {
			if games == 0 {
				return "-"
			}
			return fmt.Sprintf("%.1f", float64(s["DEFPASSYARDS"]+s["DEFRUSHYARDS"])/float64(games))
		}()),
		row("Passing Yards Allowed", fmt.Sprint(s["DEFPASSYARDS"]), pg("DEFPASSYARDS")),
		row("Rushing Yards Allowed", fmt.Sprint(s["DEFRUSHYARDS"]), pg("DEFRUSHYARDS")),
		row("Defensive Interceptions", fmt.Sprint(s["DEFINTS"]), pg("DEFINTS")),
		row("Punts", fmt.Sprint(s["PUNTS"]), pg("PUNTS")),
		row("Kick Return Yards", fmt.Sprint(s["KICKRETURNYARDS"]), pg("KICKRETURNYARDS")),
		row("Time of Possession", clock(s["POSSESSIONTIME"]), func() string {
			if games == 0 {
				return "-"
			}
			return clock(s["POSSESSIONTIME"] / int64(games))
		}()),
	}
}
