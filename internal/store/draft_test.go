package store

import (
	"encoding/json"
	"os"
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

func TestDraftPicksFillsAProjectionThatCameTrue(t *testing.T) {
	hc := func(row, team, picks int) extract.Coach {
		return extract.Coach{Row: row, FirstName: "C", LastName: strconv.Itoa(row), Position: "HeadCoach", TeamID: team, DraftPicks: picks}
	}
	row := 0
	lp := func(player, team, round int) extract.LeavingPlayer {
		row++
		return extract.LeavingPlayer{Row: row, PlayerRow: player, TeamID: team, LeaveType: "EarlyNFL_" + strconv.Itoa(round), Round: round}
	}
	// Two picks a round from teams 1 and 2. Team 1's round-4 pick was
	// projected 4th in the same row, so its row doesn't change; a stale
	// round-4 row of team 3 (whose coach gained nothing) stays put too.
	exact := lp(400, 1, 4)
	stale := lp(900, 3, 4)
	var before, after []extract.LeavingPlayer
	before = append(before, exact, stale)
	after = append(after, exact, stale)
	for r := 1; r <= 7; r++ {
		if r != 4 {
			after = append(after, lp(100+r, 1, r))
		}
		after = append(after, lp(200+r, 2, r))
	}
	prev := draftStateOf(&extract.Snapshot{LeavingPlayers: before, Coaches: []extract.Coach{hc(1, 1, 0), hc(2, 2, 0), hc(3, 3, 5), hc(4, 4, 0)}})
	// Team 4's coach is credited a pick no player accounts for.
	snap := &extract.Snapshot{LeavingPlayers: after, Coaches: []extract.Coach{hc(1, 1, 7), hc(2, 2, 7), hc(3, 3, 5), hc(4, 4, 1)}}
	drafted := newPicks(prev, draftStateOf(snap))
	picks, ok, detail := draftPicks(prev, snap, drafted)
	if !ok || len(picks) != 14 {
		t.Fatalf("ok=%v picks=%d detail=%v, want 14 picks", ok, len(picks), detail)
	}
	for _, l := range picks {
		if l.PlayerRow == 900 {
			t.Fatal("stale row of a team whose coach gained no picks was taken")
		}
	}

	// Two equally good candidates for one missing pick: refuse.
	snap.LeavingPlayers = append(snap.LeavingPlayers, lp(401, 1, 4))
	prev.Rows[strconv.Itoa(row)] = draftSig(snap.LeavingPlayers[len(snap.LeavingPlayers)-1])
	if _, ok, _ := draftPicks(prev, snap, drafted); ok {
		t.Fatal("ambiguous fill should be refused")
	}
}

// TestDraftPicksOnRealSaves checks the 2036 draft between two extracted
// saves (dsn extract -o) named by DSN_DRAFT_PRE and DSN_DRAFT_POST.
func TestDraftPicksOnRealSaves(t *testing.T) {
	pre, post := os.Getenv("DSN_DRAFT_PRE"), os.Getenv("DSN_DRAFT_POST")
	if pre == "" || post == "" {
		t.Skip("DSN_DRAFT_PRE / DSN_DRAFT_POST not set")
	}
	load := func(p string) *extract.Snapshot {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var s extract.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		return &s
	}
	a, b := load(pre), load(post)
	prev, cur := draftStateOf(a), draftStateOf(b)
	drafted := newPicks(prev, cur)
	picks, ok, detail := draftPicks(prev, b, drafted)
	t.Logf("coach picks %d, picks %d, detail %v", drafted, len(picks), detail)
	if !ok {
		t.Fatalf("draft not reconciled: %v", detail)
	}
	rounds := map[int]int{}
	for _, l := range picks {
		rounds[draftRound(l)]++
	}
	for r := 1; r <= 7; r++ {
		if rounds[r] != 32 {
			t.Fatalf("round %d has %d picks", r, rounds[r])
		}
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
