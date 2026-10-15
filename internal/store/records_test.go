package store

import "testing"

func TestTallyRecordsAheadOfSave(t *testing.T) {
	results := []Result{
		{Week: 1, Home: 1, Away: 2, HomeScore: 30, AwayScore: 10, Played: true, Conference: true},
		{Week: 2, Home: 3, Away: 1, HomeScore: 21, AwayScore: 14, Played: true},
		{Week: 3, Home: 1, Away: 4, HomeScore: 28, AwayScore: 3, Played: true, Conference: true},
		{Week: 4, Home: 1, Away: 5, HomeScore: 35, AwayScore: 7, Played: true},
		{Week: 5, Home: 1, Away: 2, Played: false},
	}
	recs := tallyRecords(results, 1<<30)
	r := recs[1]
	if r.W != 3 || r.L != 1 || r.CW != 2 || r.CL != 0 || r.Streak != 2 {
		t.Fatalf("team 1 record: %+v", r)
	}
	// The save still says 2-1 (a week behind): the games win.
	w, l, cw, cl, st := 2, 1, 2, 0, 1
	r.apply(&w, &l, &cw, &cl, &st)
	if w != 3 || l != 1 || cw != 2 || cl != 0 || st != 2 {
		t.Fatalf("apply: %d-%d %d-%d streak %d", w, l, cw, cl, st)
	}
	// A poll taken before week 3 only counts weeks 1-2.
	if p := tallyRecords(results, 3)[1]; p.W != 1 || p.L != 1 || p.Streak != -1 {
		t.Fatalf("before week 3: %+v", p)
	}
	// Missing games (an older season) never lower the save's record.
	w, l, cw, cl, st = 9, 3, 6, 2, 4
	r.apply(&w, &l, &cw, &cl, &st)
	if w != 9 || l != 3 || cw != 6 || cl != 2 || st != 4 {
		t.Fatalf("save record should stand: %d-%d %d-%d", w, l, cw, cl)
	}
}
