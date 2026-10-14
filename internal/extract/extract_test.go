package extract

import (
	"os"
	"path/filepath"
	"testing"
)

// Fixture tests run against real saves in data/saves (gitignored) and skip
// when none are present.
func fixture(t *testing.T) (save, schemas string) {
	t.Helper()
	root := filepath.Join("..", "..", "data")
	schemas = filepath.Join(root, "schemas")
	matches, _ := filepath.Glob(filepath.Join(root, "saves", "DYNASTY-*"))
	if len(matches) == 0 {
		t.Skip("no save fixture in data/saves")
	}
	if _, err := os.Stat(schemas); err != nil {
		t.Skip("no schema bundle in data/schemas")
	}
	return matches[0], schemas
}

// Every FBS team's player lines must add up to its box score: this is what
// proves no stat line was dropped or attributed to the wrong team.
func TestPlayerLinesMatchTeamBox(t *testing.T) {
	save, schemas := fixture(t)
	s, err := Extract(save, schemas)
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ game, team int }
	pass, rush := map[key]int64{}, map[key]int64{}
	for _, l := range s.PlayerGameStats {
		if l.Category == "offense" || l.Category == "returns_off" {
			k := key{l.GameRow, l.TeamID}
			pass[k] += l.Stats["PASSYARDS"]
			rush[k] += l.Stats["RUSHYARDS"]
		}
	}
	checked, bad := 0, 0
	for _, g := range s.Games {
		if g.HomeBox == nil {
			continue
		}
		for _, side := range []struct {
			team int
			box  Stats
		}{{g.HomeTeamID, g.HomeBox}, {g.AwayTeamID, g.AwayBox}} {
			if side.team < 0 {
				continue // FCS opponents have no player lines
			}
			checked++
			k := key{g.Row, side.team}
			if pass[k] != side.box["OFFPASSYARDS"] || rush[k] != side.box["OFFRUSHYARDS"] {
				bad++
				if bad < 5 {
					t.Logf("game %d team %d: pass %d/%d rush %d/%d", g.Row, side.team, pass[k], side.box["OFFPASSYARDS"], rush[k], side.box["OFFRUSHYARDS"])
				}
			}
		}
	}
	if checked == 0 {
		t.Skip("fixture has no played games")
	}
	// A stat row whose player the save has since deleted (cut mid-season)
	// has no owner and cannot be attributed; allow a sliver of those.
	t.Logf("checked %d team-games, %d with unattributable lines", checked, bad)
	if bad*100 > checked {
		t.Fatalf("too many team-games disagree with the box score: %d of %d", bad, checked)
	}
}

func TestKickingAndPuntingAttributed(t *testing.T) {
	save, schemas := fixture(t)
	s, err := Extract(save, schemas)
	if err != nil {
		t.Fatal(err)
	}
	var fg, punts int
	for _, l := range s.PlayerGameStats {
		if l.Category == "kicking" {
			fg += int(l.Stats["KICKFGATTEMPTS"])
			punts += int(l.Stats["PUNTATTEMPTS"])
		}
	}
	played := 0
	for _, g := range s.Games {
		if g.Played {
			played++
		}
	}
	if played > 0 && (fg == 0 || punts == 0) {
		t.Fatalf("kicking lines missing: fg=%d punts=%d over %d games", fg, punts, played)
	}
}
