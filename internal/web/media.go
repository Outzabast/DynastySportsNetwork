package web

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// mediaIndex resolves game media (logos, portraits, trophies) to URLs under
// /media. Files are served straight from the media folder; nothing is stored
// in the database.
type mediaIndex struct {
	dir       string
	logos     map[string]string // normalized team key -> file (flat logos)
	logos3d   map[string]string
	helmets   map[string]string
	portraits map[string]bool
	coaches   map[string]bool
	bowls     map[string]string
	confs     map[string]string // folder key -> latest primary logo path
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]`)

func norm(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

// teamAliases covers programs whose media file names differ from the game's
// display name.
var teamAliases = map[string][]string{
	"california":      {"cal"},
	"eastcarolina":    {"ecu"},
	"middletennessee": {"midtennstate"},
	"mtsu":            {"midtennstate"},
	"uconn":           {"connecticut"},
	"massachusetts":   {"umass"},
	"samhouston":      {"samhoustonstate"},
	"miamioh":         {"miamiuniversity"},
	"smu":             {"mustangs"},
}

func listDir(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func indexBy(dir, prefix, suffix string) map[string]string {
	m := map[string]string{}
	for _, f := range listDir(dir) {
		if !strings.HasPrefix(f, prefix) || !strings.HasSuffix(f, suffix) {
			continue
		}
		m[norm(strings.TrimSuffix(strings.TrimPrefix(f, prefix), suffix))] = f
	}
	return m
}

func newMediaIndex(dir string) *mediaIndex {
	m := &mediaIndex{dir: dir, portraits: map[string]bool{}, coaches: map[string]bool{}, confs: map[string]string{}}
	m.logos = indexBy(filepath.Join(dir, "logos-flat-dark"), "tlow_", ".webp")
	m.logos3d = indexBy(filepath.Join(dir, "3d_logos", "png_OL"), "", "_OL.webp")
	m.helmets = indexBy(filepath.Join(dir, "helmet", "left"), "thel_lthelmets_", "_result.webp")
	m.bowls = indexBy(filepath.Join(dir, "bowlgames"), "bowl_", ".webp")
	for _, f := range listDir(filepath.Join(dir, "playerportrait")) {
		m.portraits[strings.TrimSuffix(strings.TrimPrefix(f, "nilpp_"), ".webp")] = true
	}
	for _, f := range listDir(filepath.Join(dir, "coaches")) {
		m.coaches[strings.TrimSuffix(strings.TrimPrefix(f, "nilcp_"), ".webp")] = true
	}
	confDir := filepath.Join(dir, "All Logos", "Conferences")
	for _, sub := range listDir(confDir) {
		files := listDir(filepath.Join(confDir, sub))
		sort.Strings(files)
		if len(files) > 0 {
			m.confs[norm(sub)] = "All Logos/Conferences/" + sub + "/" + files[len(files)-1]
		}
	}
	return m
}

func (m *mediaIndex) url(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(p, " ", "%20")
	}
	return "/media/" + strings.Join(parts, "/")
}

func (m *mediaIndex) teamKeys(names ...string) []string {
	var keys []string
	for _, n := range names {
		k := norm(n)
		keys = append(keys, teamAliases[k]...)
		keys = append(keys, k)
	}
	return keys
}

func (m *mediaIndex) find(idx map[string]string, keys []string) string {
	for _, k := range keys {
		if f, ok := idx[k]; ok {
			return f
		}
	}
	return ""
}

// TeamLogo returns the flat logo URL for a team, or "" if none.
func (m *mediaIndex) TeamLogo(names ...string) string {
	if f := m.find(m.logos, m.teamKeys(names...)); f != "" {
		return m.url("logos-flat-dark/" + f)
	}
	if f := m.find(m.logos3d, m.teamKeys(names...)); f != "" {
		return m.url("3d_logos/png_OL/" + f)
	}
	return ""
}

// TeamHelmet returns the helmet image URL.
func (m *mediaIndex) TeamHelmet(names ...string) string {
	if f := m.find(m.helmets, m.teamKeys(names...)); f != "" {
		return m.url("helmet/left/" + f)
	}
	return ""
}

// Portrait returns a player headshot URL (generic blank when unknown).
func (m *mediaIndex) Portrait(asset string) string {
	if asset != "" && m.portraits[asset] {
		return m.url("playerportrait/nilpp_" + asset + ".webp")
	}
	return m.url("playerportrait/nilpp_Blank.webp")
}

// CoachPortrait returns a coach headshot URL.
func (m *mediaIndex) CoachPortrait(asset string) string {
	if asset != "" && m.coaches[asset] {
		return m.url("coaches/nilcp_" + asset + ".webp")
	}
	return m.url("playerportrait/nilpp_Blank.webp")
}

// BowlLogo returns bowl art for a bowl name.
func (m *mediaIndex) BowlLogo(name string) string {
	if name == "" {
		return ""
	}
	if f, ok := m.bowls[norm(name)]; ok {
		return m.url("bowlgames/" + f)
	}
	return ""
}

var confFolders = map[string]string{
	"acc":      "atlanticcoastconference",
	"american": "americanathleticconference",
	"big12":    "big12conference",
	"bigten":   "bigtenconference",
	"cusa":     "conferenceusa",
	"mac":      "midamericanconference",
	"mw":       "mountainwestconference",
	"pac12":    "pac12conference",
	"sec":      "southeasternconference",
	"sunbelt":  "sunbeltconference",
}

// ConfLogo returns a conference logo URL.
func (m *mediaIndex) ConfLogo(conf string) string {
	k := norm(conf)
	if f, ok := m.confs[confFolders[k]]; ok {
		return m.url(f)
	}
	for key, f := range m.confs {
		if strings.HasPrefix(key, k) {
			return m.url(f)
		}
	}
	return ""
}

// awardInfo names the game's award enums the way fans know them.
var awardInfo = map[string][2]string{
	"HEISMAN":            {"Heisman Trophy", "trophies_Heisman.webp"},
	"BEST_PLAYER":        {"Maxwell Award", "trophies_MaxwellAward.webp"},
	"BEST_POTY":          {"Walter Camp Award", "trophies_WalterCampAward.webp"},
	"BEST_QB":            {"Davey O'Brien Award", "trophies_DaveyOBrienAward.webp"},
	"BEST_SR_QB":         {"Johnny Unitas Golden Arm", "trophies_JohnnyUnitas.webp"},
	"BEST_RB":            {"Doak Walker Award", "trophies_DoakWalkerRunningBackAward.webp"},
	"BEST_REC":           {"Biletnikoff Award", "trophies_BiletnikoffAward.webp"},
	"BEST_TE":            {"John Mackey Award", "trophies_JohnMackeyAward.webp"},
	"BEST_C":             {"Rimington Trophy", "trophies_RimingtonTrophy.webp"},
	"BEST_IL":            {"Outland Trophy", "trophies_OutlandTrophy.webp"},
	"BEST_DL":            {"Lombardi Award", "trophies_Lombardi.webp"},
	"BEST_DE":            {"Ted Hendricks Award", "trophies_BestDefensiveEnd.webp"},
	"BEST_DEF_1":         {"Bronko Nagurski Trophy", "trophies_BronkoNagurskiTrophy.webp"},
	"BEST_DEF_2":         {"Chuck Bednarik Award", "trophies_ChuckBednarikAward.webp"},
	"BEST_LB":            {"Butkus Award", "trophies_ButkusAward.webp"},
	"BEST_DB":            {"Jim Thorpe Award", "trophies_JimThorpeAward.webp"},
	"BEST_KICK":          {"Lou Groza Award", "trophies_LouGroza.webp"},
	"BEST_PUNT":          {"Ray Guy Award", "trophies_RayGuyAward.webp"},
	"MOST_VERSATILE":     {"Paul Hornung Award", "trophies_HornungAward.webp"},
	"BEST_HC":            {"Coach of the Year", "trophies_BearBryantHeadCoach.webp"},
	"BEST_AC":            {"Broyles Award", "trophies_Broyles.webp"},
	"BEST_ACADEMIC":      {"Campbell Trophy", "trophies_CampbellAward.webp"},
	"BEST_FRESHMAN_POTY": {"Shaun Alexander Freshman of the Year", "trophies_ShaunAlexanderAward.webp"},
	"BEST_SR":            {"Senior CLASS Award", "trophies_CFB_Generic_Trophy_PlayeroftheYear.webp"},
	"BEST_RET":           {"Jet Award", "trophies_TheJetAward.webp"},

	"ALL_AM_1ST":          {"All-America First Team", ""},
	"ALL_AM_2ND":          {"All-America Second Team", ""},
	"ALL_AM_FR":           {"Freshman All-America", ""},
	"ALL_AM_1ST_CONF":     {"All-Conference First Team", ""},
	"ALL_AM_2ND_CONF":     {"All-Conference Second Team", ""},
	"ALL_AM_FR_CONF":      {"All-Conference Freshman Team", ""},
	"ALL_AM_1ST_PRE":      {"Preseason All-America First Team", ""},
	"ALL_AM_2ND_PRE":      {"Preseason All-America Second Team", ""},
	"ALL_AM_1ST_PRE_CONF": {"Preseason All-Conference First Team", ""},
	"ALL_AM_2ND_PRE_CONF": {"Preseason All-Conference Second Team", ""},

	"Offensive_Player_of_Week":      {"National Offensive Player of the Week", ""},
	"Defensive_Player_of_Week":      {"National Defensive Player of the Week", ""},
	"Offensive_Player_of_Week_Conf": {"Conference Offensive Player of the Week", ""},
	"Defensive_Player_of_Week_Conf": {"Conference Defensive Player of the Week", ""},
}

// AwardName returns the display name for an award enum.
func AwardName(t string) string {
	if a, ok := awardInfo[t]; ok {
		return a[0]
	}
	return strings.ReplaceAll(strings.Title(strings.ToLower(strings.ReplaceAll(t, "_", " "))), "Of", "of")
}

// Trophy returns trophy art for a major award.
func (m *mediaIndex) Trophy(t string) string {
	if a, ok := awardInfo[t]; ok && a[1] != "" {
		return m.url("awards/" + a[1])
	}
	return ""
}
