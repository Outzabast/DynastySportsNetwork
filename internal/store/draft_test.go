package store

import (
	"strconv"
	"testing"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

func TestNewPicksComparesEachHeadCoach(t *testing.T) {
	hc := func(row, picks int, last string) extract.Coach {
		return extract.Coach{Row: row, FirstName: "A", LastName: last, Position: "HeadCoach", DraftPicks: picks}
	}
	before := draftStateOf(&extract.Snapshot{Coaches: []extract.Coach{hc(1, 2, "McWhorter"), hc(2, 10, "Old"),
		{Row: 3, FirstName: "B", LastName: "Coord", Position: "OffensiveCoordinator", DraftPicks: 1}}})
	// McWhorter's school had 5 drafted; coach 2 was replaced by a new hire
	// with his own career count, which is not a draft; coordinators don't count.
	after := draftStateOf(&extract.Snapshot{Coaches: []extract.Coach{hc(1, 7, "McWhorter"), hc(2, 40, "New"),
		{Row: 3, FirstName: "B", LastName: "Coord", Position: "OffensiveCoordinator", DraftPicks: 6}}})
	if n := newPicks(before, after); n != 5 {
		t.Fatalf("newPicks = %d, want 5", n)
	}
	if n := newPicks(after, after); n != 0 {
		t.Fatalf("no draft between identical saves, got %d", n)
	}
}

func TestDraftRowsChangeOnlyForPicks(t *testing.T) {
	stale := extract.LeavingPlayer{Row: 4, PlayerRow: 100, LeaveType: "EarlyNFL_3", Round: 3}
	projected := extract.LeavingPlayer{Row: 9, PlayerRow: 200, LeaveType: "EarlyNFL_6", Round: 6}
	before := draftStateOf(&extract.Snapshot{LeavingPlayers: []extract.LeavingPlayer{stale, projected,
		{Row: 12, PlayerRow: 300, LeaveType: "Transfer_NIL"}}})
	drafted := projected
	drafted.LeaveType, drafted.Round = "EarlyNFL_5", 5 // projected 6th, went in the 5th
	after := []extract.LeavingPlayer{stale, drafted}
	var picks []extract.LeavingPlayer
	for _, l := range after {
		if isDraftRow(l) && before.Rows[strconv.Itoa(l.Row)] != draftSig(l) {
			picks = append(picks, l)
		}
	}
	if len(picks) != 1 || picks[0].PlayerRow != 200 || draftRound(picks[0]) != 5 {
		t.Fatalf("picks = %+v, want player row 200 in round 5", picks)
	}
	if len(before.Rows) != 2 {
		t.Fatalf("only EarlyNFL rows belong in the state, got %v", before.Rows)
	}
}
