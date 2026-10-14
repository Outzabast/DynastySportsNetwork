package store

import (
	"testing"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

func TestPositionOrdersTheCalendar(t *testing.T) {
	order := []extract.Season{
		{SeasonIndex: 3, Stage: "PreSeason", Week: 0},
		{SeasonIndex: 3, Stage: "NFLSeason", Week: 1},
		{SeasonIndex: 3, Stage: "NFLSeason", Week: 15},
		{SeasonIndex: 3, Stage: "NFLSeason", Week: 18},
		{SeasonIndex: 3, Stage: "OffSeason", OffseasonStage: 1, Week: 2},
		{SeasonIndex: 3, Stage: "OffSeason", OffseasonStage: 4, Week: 0},
		{SeasonIndex: 4, Stage: "PreSeason", Week: 0},
	}
	var prev int64 = -1
	for _, s := range order {
		p, _ := Position(s)
		if p <= prev {
			t.Fatalf("%+v not after previous (%d <= %d)", s, p, prev)
		}
		prev = p
	}
	if _, known := Position(extract.Season{Stage: "SomethingNew"}); known {
		t.Fatal("unknown stage should be reported for review")
	}
}

func TestContradicts(t *testing.T) {
	snap := &extract.Snapshot{Games: []extract.Game{
		{Row: 1, Played: true, HomeTeamID: 2, AwayTeamID: 3, HomeScore: 21, AwayScore: 14},
		{Row: 2, Played: false, HomeTeamID: 4, AwayTeamID: 5},
	}}
	same := map[int]storedGame{1: {Home: 2, Away: 3, HomeScore: 21, AwayScore: 14, Played: true}, 2: {Home: 4, Away: 5}}
	if bad, why := contradicts(snap, same); bad {
		t.Fatalf("consistent history flagged: %s", why)
	}
	changed := map[int]storedGame{1: {Home: 2, Away: 3, HomeScore: 28, AwayScore: 14, Played: true}}
	if bad, _ := contradicts(snap, changed); !bad {
		t.Fatal("changed score not detected")
	}
	unplayed := map[int]storedGame{2: {Home: 4, Away: 5, HomeScore: 10, AwayScore: 3, Played: true}}
	if bad, _ := contradicts(snap, unplayed); !bad {
		t.Fatal("played game now unplayed not detected")
	}
}

func TestSameOwner(t *testing.T) {
	a := facts{"Omar", "Aarons", "Denton", "Texas", "Right", "AaronsOmar_30391", 13371}
	if !sameOwner(a, a) {
		t.Fatal("identical facts must match")
	}
	b := a
	b.town = "Austin"
	if sameOwner(a, b) {
		t.Fatal("different hometown must split")
	}
	c := a
	c.birth = 0
	if !sameOwner(a, c) {
		t.Fatal("unknown birth date must not split")
	}
	d := a
	d.asset = "AaronsOmar_99999"
	if sameOwner(a, d) {
		t.Fatal("different asset name must split")
	}
}
