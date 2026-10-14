package extract

import (
	"strings"

	"github.com/leaguelines/cfb-dynasty/dynasty"
)

// Game types, as stored on Game.GameType.
const (
	GameConference       = "conference"
	GameNonConference    = "nonconference"
	GameConfChampionship = "conf_championship"
	GameBowl             = "bowl"
	GamePlayoff          = "playoff"
	GameNationalChamp    = "national_championship"
)

// rows resolves an array-store reference to its member records.
func (x *extractor) rows(ref *dynasty.RecordReference) []dynasty.Record {
	var out []dynasty.Record
	for _, m := range x.arrayRefs(ref) {
		t, ok := x.f.GetTableByID(m.TableID)
		if !ok || t == nil || t.ReadRecords() != nil {
			continue
		}
		if r, ok := x.row(t, int(m.RowNumber)); ok {
			out = append(out, r)
		}
	}
	return out
}

func isIndependent(conf string) bool {
	return conf == "" || strings.Contains(strings.ToLower(conf), "independ")
}

// conferences reads each conference's divisions and title game. Teams get a
// division only when their conference is actually split (two or more).
func (x *extractor) conferences(snap *Snapshot) {
	ct := x.table("Conference")
	if ct == nil {
		return
	}
	teamIdx := map[int]int{}
	for i, t := range snap.Teams {
		teamIdx[t.ID] = i
	}
	for _, r := range ct.Records {
		name := strF(r, "Name")
		if name == "" {
			continue // unused conference slot
		}
		c := Conference{Name: name}
		if strF(r, "ChampionshipGameType") != "NONE" {
			c.ChampGame = strF(r, "ConfChampGameName")
		}
		for _, d := range x.rows(refF(r, "Divisions")) {
			div := Division{Name: strF(d, "Name")}
			for _, m := range x.arrayRefs(refF(d, "Teams")) {
				if id, ok := x.teamIDByRow[int(m.RowNumber)]; ok {
					div.TeamIDs = append(div.TeamIDs, id)
				}
			}
			if len(div.TeamIDs) > 0 {
				c.Divisions = append(c.Divisions, div)
			}
		}
		if len(c.Divisions) > 1 {
			for _, d := range c.Divisions {
				for _, id := range d.TeamIDs {
					if i, ok := teamIdx[id]; ok {
						snap.Teams[i].Division = d.Name
					}
				}
			}
		}
		snap.Conferences = append(snap.Conferences, c)
	}
}

// bowlInfo names the bowl a game references and whether it is a CFP game.
func (x *extractor) bowlInfo(ref *dynasty.RecordReference) (name string, playoff, ok bool) {
	if ref == nil {
		return "", false, false
	}
	t, found := x.f.GetTableByID(ref.TableID)
	if !found || t == nil || t.ReadRecords() != nil {
		return "", false, false
	}
	r, found := x.row(t, int(ref.RowNumber))
	if !found {
		return "", false, false
	}
	return strF(r, "Name"), boolF(r, "IsPlayoffBowl"), true
}

// gameType classifies one game. confOf maps team id to its conference.
func gameType(weekType string, week, confChampWeek int, home, away int, confOf map[int]string, bowl string, hasBowl, playoff bool) string {
	switch {
	case hasBowl && playoff && (weekType == "NationalChampionship" || strings.Contains(bowl, "National Championship")):
		return GameNationalChamp
	case hasBowl && playoff:
		return GamePlayoff
	case hasBowl:
		return GameBowl
	case weekType != "RegularSeason":
		return ""
	}
	hc, ac := confOf[home], confOf[away]
	if hc == "" || hc != ac || isIndependent(hc) {
		return GameNonConference
	}
	if confChampWeek > 0 && week >= confChampWeek {
		return GameConfChampionship
	}
	return GameConference
}

// teamHistory reads each team's season-by-season record from the save.
// Postseason entries resolve through the bowl list: the game stores ordinary
// bowls in the same slots as CFP rounds, so the bowl itself decides.
func (x *extractor) teamHistory(snap *Snapshot) {
	tt := x.table("Team")
	if tt == nil {
		return
	}
	type bowl struct {
		name    string
		playoff bool
	}
	bowls := map[int]bowl{}
	if bt := x.table("BowlGame"); bt != nil {
		for _, r := range bt.Records {
			if n := strF(r, "Name"); n != "" {
				bowls[intF(r, "PresentationId")] = bowl{n, boolF(r, "IsPlayoffBowl")}
			}
		}
	}
	// New Year's Six bowls host CFP quarterfinals and semifinals in some
	// seasons; they live in their own table.
	if pt := x.table("PlayoffBowlsInfo"); pt != nil {
		for _, r := range pt.Records {
			if n := strF(r, "Name"); n != "" {
				bowls[intF(r, "PresentationId")] = bowl{n, true}
			}
		}
	}
	// The four slots are postseason weeks; ordinary bowls share them with
	// CFP rounds, so the bowl decides whether it was a playoff game.
	slots := [][3]string{
		{"FirstRoundCFPBowlGameResult", "FirstRoundCFPBowlPresentationId", "CFP First Round"},
		{"QuarterFinalsBowlGameResult", "QuarterFinalsBowlPresentationId", "CFP Quarterfinal"},
		{"SemiFinalsBowlGameResult", "SemiFinalsBowlPresentationId", "CFP Semifinal"},
		{"NationalBowlGameResult", "NationalBowlPresentationId", "National Championship"},
	}
	for _, r := range tt.Records {
		id, ok := x.teamIDByRow[r.Index]
		if !ok {
			continue
		}
		for _, h := range x.rows(refF(r, "TeamSeriesHistory")) {
			y := TeamHistoryYear{
				TeamID: id, Year: intF(h, "Year"), CoachName: strF(h, "CoachName"),
				Conference: strF(h, "ConferenceName"), Division: strF(h, "DivisionName"),
				Wins: intF(h, "Wins"), Losses: intF(h, "Losses"), Ties: intF(h, "Ties"),
				ConfWins: intF(h, "ConferenceWins"), ConfLosses: intF(h, "ConferenceLosses"),
				FinalRank: intF(h, "FinalMediaRank"), ConfStanding: intF(h, "FinalConferenceStanding"),
			}
			if e := strF(h, "ConfChampionshipConferenceEnum"); e != "" && e != "None" && e != "Invalid_" {
				y.ConfChamp = true
			}
			if y.Year == 0 || (y.Wins+y.Losses+y.Ties == 0 && y.CoachName == "") {
				continue
			}
			for _, s := range slots {
				res := strF(h, s[0])
				if res != "Win" && res != "Loss" {
					continue
				}
				b := bowls[intF(h, s[1])]
				p := PostseasonResult{Bowl: b.name, Playoff: b.playoff, Won: res == "Win"}
				if p.Playoff {
					p.Round = s[2]
				}
				if p.Bowl == "" {
					p.Bowl = "Bowl game"
				}
				y.Postseason = append(y.Postseason, p)
			}
			snap.TeamHistory = append(snap.TeamHistory, y)
		}
	}
}
