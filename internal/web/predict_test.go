package web

import (
	"math"
	"testing"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

func TestPredictFavorsStrongerTeam(t *testing.T) {
	// Round robin, home and away, where each team scores 28 plus half the
	// strength gap: team 1 is 14 points better than average, team 2 14 worse.
	strength := map[int]float64{1: 14, 2: -14, 3: 0, 4: 0}
	var rs []store.Result
	w := 0
	for _, h := range []int{1, 2, 3, 4} {
		for _, a := range []int{1, 2, 3, 4} {
			if h == a {
				continue
			}
			w++
			gap := (strength[h] - strength[a] + predHFA) / 2
			rs = append(rs, store.Result{Week: w, Home: h, Away: a, HomeScore: int(28 + gap), AwayScore: int(28 - gap), Played: true})
		}
	}
	r := fitRatings(rs, 99, nil)
	p := predict(r, 2, 1, false) // team 1 visits team 2
	if p.AwayPct < 85 || p.Spread > -15 {
		t.Fatalf("strong road team should be a big favorite: %+v", p)
	}
	if math.Abs(p.AwayPct+p.HomePct-100) > 1e-9 {
		t.Fatalf("probabilities must sum to 100: %+v", p)
	}
	l := p.line(2, 1)
	if l.Favorite != 1 || l.AwaySpread[0] != '-' || l.HomeSpread[0] != '+' || l.AwayML[0] != '-' {
		t.Fatalf("line should favor the road team: %+v", l)
	}
	// Cutoff hides later games: with none played the line is near a pick'em
	// plus home field.
	early := predict(fitRatings(rs, 1, nil), 2, 1, false)
	if early.Games != 0 || math.Abs(early.Spread-predHFA) > 0.5 {
		t.Fatalf("no games should mean home field only: %+v", early)
	}
}

func TestMoneyline(t *testing.T) {
	for _, c := range []struct {
		p    float64
		want string
	}{{0.5, "-105"}, {0.75, "-330"}, {0.25, "+290"}} {
		if got := moneyline(c.p); got != c.want {
			t.Errorf("moneyline(%v) = %s, want %s", c.p, got, c.want)
		}
	}
}
