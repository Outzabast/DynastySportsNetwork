package extract

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGameType(t *testing.T) {
	conf := map[int]string{1: "Big 12", 2: "Big 12", 3: "SEC", 4: "Independent", 5: "Independent"}
	cases := []struct {
		name             string
		weekType         string
		week, home, away int
		bowl             string
		hasBowl, playoff bool
		want             string
	}{
		{"conference", "RegularSeason", 5, 1, 2, "", false, false, GameConference},
		{"nonconference", "RegularSeason", 5, 1, 3, "", false, false, GameNonConference},
		{"two independents", "RegularSeason", 5, 4, 5, "", false, false, GameNonConference},
		{"fcs opponent", "RegularSeason", 2, 1, -1, "", false, false, GameNonConference},
		{"title game", "RegularSeason", 15, 1, 2, "", false, false, GameConfChampionship},
		{"bowl", "BowlSeason1", 17, 1, 3, "Texas Bowl", true, false, GameBowl},
		{"cfp", "BowlSeason2", 18, 1, 3, "CFP Quarterfinal", true, true, GamePlayoff},
		{"title", "NationalChampionship", 20, 1, 3, "National Championship", true, true, GameNationalChamp},
		{"preseason", "PreSeason", 0, 1, 3, "", false, false, ""},
	}
	for _, c := range cases {
		if got := gameType(c.weekType, c.week, 15, c.home, c.away, conf, c.bowl, c.hasBowl, c.playoff); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// Every save fixture: title games are intra-conference, divisions cover the
// split conferences, CFP games are named CFP rounds, and the game's own
// season history resolves bowls by name.
func TestLeagueStructure(t *testing.T) {
	_, schemas := fixture(t)
	saves, _ := filepath.Glob(filepath.Join("..", "..", "data", "saves", "DYNASTY-*"))
	for _, save := range saves {
		s, err := Extract(save, schemas)
		if err != nil {
			t.Fatal(err)
		}
		conf := map[int]string{}
		div := map[int]string{}
		for _, tm := range s.Teams {
			conf[tm.ID], div[tm.ID] = tm.Conference, tm.Division
		}
		// Exactly one head coach per FBS team, and it is the team's staff HC.
		hcs := map[int]int{}
		hcRow := map[int]int{}
		for _, c := range s.Coaches {
			if c.Age == 0 {
				t.Errorf("%s: filler coach row %d (%s %s) extracted", filepath.Base(save), c.Row, c.FirstName, c.LastName)
			}
			if c.Position == "HeadCoach" && c.TeamID >= 0 {
				hcs[c.TeamID]++
				hcRow[c.TeamID] = c.Row
			}
		}
		for _, tm := range s.Teams {
			if tm.HeadCoachRow >= 0 && (hcs[tm.ID] != 1 || hcRow[tm.ID] != tm.HeadCoachRow) {
				t.Errorf("%s: team %d has %d head coaches; staff HC row %d", filepath.Base(save), tm.ID, hcs[tm.ID], tm.HeadCoachRow)
			}
		}
		titleConfs := 0
		for _, c := range s.Conferences {
			if c.ChampGame != "" {
				titleConfs++
			}
			for _, d := range c.Divisions {
				for _, id := range d.TeamIDs {
					if len(c.Divisions) > 1 && div[id] != d.Name {
						t.Errorf("%s: team %d in %s/%s but Division=%q", filepath.Base(save), id, c.Name, d.Name, div[id])
					}
				}
			}
		}
		counts := map[string]int{}
		for _, g := range s.Games {
			counts[g.GameType]++
			switch g.GameType {
			case GameConfChampionship, GameConference:
				if conf[g.HomeTeamID] != conf[g.AwayTeamID] {
					t.Errorf("%s: game %d typed %s across conferences", filepath.Base(save), g.Row, g.GameType)
				}
			case GamePlayoff:
				if !strings.HasPrefix(g.BowlName, "CFP") {
					t.Errorf("%s: playoff game %d named %q", filepath.Base(save), g.Row, g.BowlName)
				}
			}
		}
		if counts[GameConfChampionship] > titleConfs {
			t.Errorf("%s: %d title games for %d title conferences", filepath.Base(save), counts[GameConfChampionship], titleConfs)
		}
		if counts[GameConference] == 0 || counts[GameNonConference] == 0 {
			t.Errorf("%s: game type counts look wrong: %v", filepath.Base(save), counts)
		}
		if len(s.TeamHistory) == 0 {
			t.Errorf("%s: no team history", filepath.Base(save))
		}
		for _, h := range s.TeamHistory {
			for _, p := range h.Postseason {
				if p.Bowl == "" || p.Bowl == "Bowl game" {
					t.Errorf("%s: team %d %d has an unnamed postseason game", filepath.Base(save), h.TeamID, h.Year)
				}
			}
		}
		t.Logf("%s: types %v, %d conferences, %d history rows", filepath.Base(save), counts, len(s.Conferences), len(s.TeamHistory))
	}
}

// An offseason save after realignment: Troy (122) and Vanderbilt (104) have
// moved CUSA -> SEC for next season, but the save is still labeled 2035, the
// season they played in CUSA.
func TestSeasonConferencesUsesHistoryAfterRealignment(t *testing.T) {
	snap := &Snapshot{
		Season: Season{Year: 2035},
		Teams: []Team{
			{ID: 122, Conference: "SEC", Division: "East"},
			{ID: 104, Conference: "SEC", Division: "West"},
			{ID: 2, Conference: "SEC", Division: "West"},
			{ID: 83, Conference: "CUSA"},
		},
		Conferences: []Conference{
			{Name: "SEC", ChampGame: "SEC Championship", Divisions: []Division{{Name: "East", TeamIDs: []int{122}}, {Name: "West", TeamIDs: []int{104, 2}}}},
			{Name: "CUSA", ChampGame: "CUSA Championship", Divisions: []Division{{Name: "", TeamIDs: []int{83}}}},
		},
		TeamHistory: []TeamHistoryYear{
			{TeamID: 122, Year: 2035, Conference: "CUSA"}, {TeamID: 122, Year: 2036, Conference: "SEC", Division: "East"},
			{TeamID: 104, Year: 2035, Conference: "CUSA"},
			{TeamID: 2, Year: 2035, Conference: "SEC", Division: "West"},
			{TeamID: 83, Year: 2035, Conference: "CUSA"},
		},
	}
	(&extractor{}).seasonConferences(snap)
	conf := map[int]string{}
	for _, tm := range snap.Teams {
		conf[tm.ID] = tm.Conference
		if tm.ID != 2 && tm.Division != "" {
			t.Errorf("team %d: division %q, want none", tm.ID, tm.Division)
		}
	}
	if conf[122] != "CUSA" || conf[104] != "CUSA" || conf[2] != "SEC" || conf[83] != "CUSA" {
		t.Fatalf("conferences = %v", conf)
	}
	if got := gameType("RegularSeason", 15, 15, 104, 122, conf, "", false, false); got != GameConfChampionship {
		t.Errorf("Troy @ Vanderbilt title game = %q", got)
	}
	if got := gameType("RegularSeason", 6, 15, 122, 83, conf, "", false, false); got != GameConference {
		t.Errorf("Troy vs Southern Miss = %q, want conference", got)
	}
	for _, c := range snap.Conferences {
		var members []int
		for _, d := range c.Divisions {
			members = append(members, d.TeamIDs...)
		}
		want := map[string]int{"SEC": 1, "CUSA": 3}[c.Name]
		if len(members) != want {
			t.Errorf("%s has %d teams %v, want %d", c.Name, len(members), members, want)
		}
	}
}

// In-season saves already agree with history and are left alone.
func TestSeasonConferencesLeavesInSeasonSaves(t *testing.T) {
	snap := &Snapshot{
		Season:      Season{Year: 2036},
		Teams:       []Team{{ID: 122, Conference: "SEC", Division: "East"}},
		Conferences: []Conference{{Name: "SEC", Divisions: []Division{{Name: "East", TeamIDs: []int{122}}, {Name: "West"}}}},
		TeamHistory: []TeamHistoryYear{{TeamID: 122, Year: 2036, Conference: "SEC", Division: "Mystery"}},
	}
	(&extractor{}).seasonConferences(snap)
	if snap.Teams[0].Division != "East" || len(snap.Conferences[0].Divisions) != 2 {
		t.Fatalf("in-season save changed: %+v %+v", snap.Teams[0], snap.Conferences[0])
	}
}
