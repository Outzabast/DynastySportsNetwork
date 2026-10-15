package web

import (
	"fmt"
	"math"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Matchup predictor and betting line, built only from final scores (what a
// fan or a sportsbook can see), never from game ratings.
//
// Each team gets an offense and a defense rating in points, fitted by ridge
// least squares over the season's played games:
//
//	points(A vs B) = mu ± hfa/2 + off[A] - def[B]
//
// so the expected margin is (off-def gap) + home field. Ratings are pulled
// toward last season's (regressed) values, which carries the early weeks;
// by midseason the current results dominate. FCS opponents share one rating.
// A small poll-ranking edge is added on top (see rankEdge).

const (
	predPriorWeight = 3.0  // the prior counts as this many games
	predCarryOver   = 0.6  // share of last season's rating kept as the prior
	predHFA         = 2.5  // home field prior, points
	predSigma       = 15.0 // spread error, points (college football ~14-16)
	predVig         = 1.0225
	predRankMax     = 4.0 // ranking edge of #1 over an unranked team, points
	fcsTeam         = -1
)

type ratings struct {
	mu, hfa  float64
	off, def map[int]float64
	games    int
}

// fitRatings fits ratings on played games before cutoffWeek.
func fitRatings(results []store.Result, cutoffWeek int, prior *ratings) *ratings {
	idx := map[int]int{}
	var ids []int
	add := func(t int) {
		if _, ok := idx[t]; !ok {
			idx[t] = len(ids)
			ids = append(ids, t)
		}
	}
	for _, r := range results {
		add(r.Home)
		add(r.Away)
	}
	if prior != nil {
		for t := range prior.off {
			add(t)
		}
	}
	// Parameters: 0 = mu, 1 = hfa, then off/def per team.
	n := 2 + 2*len(ids)
	oi := func(t int) int { return 2 + 2*idx[t] }
	di := func(t int) int { return 3 + 2*idx[t] }
	A := make([][]float64, n)
	for i := range A {
		A[i] = make([]float64, n)
	}
	b := make([]float64, n)
	obs := func(w, y float64, coef map[int]float64) {
		for i, ci := range coef {
			b[i] += w * ci * y
			for j, cj := range coef {
				A[i][j] += w * ci * cj
			}
		}
	}
	games := 0
	for _, r := range results {
		if !r.Played || r.Week >= cutoffWeek {
			continue
		}
		games++
		h := 0.5
		if r.Neutral {
			h = 0
		}
		obs(1, float64(r.HomeScore), map[int]float64{0: 1, 1: h, oi(r.Home): 1, di(r.Away): -1})
		obs(1, float64(r.AwayScore), map[int]float64{0: 1, 1: -h, oi(r.Away): 1, di(r.Home): -1})
	}
	// Priors keep the system solvable and steady with few games.
	mu0 := 28.0
	if prior != nil {
		mu0 = prior.mu
	}
	obs(1, mu0, map[int]float64{0: 1})
	obs(20, predHFA, map[int]float64{1: 1})
	for _, t := range ids {
		po, pd := 0.0, 0.0
		if prior != nil {
			po, pd = predCarryOver*prior.off[t], predCarryOver*prior.def[t]
		} else if t == fcsTeam {
			po, pd = -10, -10
		}
		obs(predPriorWeight, po, map[int]float64{oi(t): 1})
		obs(predPriorWeight, pd, map[int]float64{di(t): 1})
	}
	x := solve(A, b)
	out := &ratings{mu: x[0], hfa: x[1], off: map[int]float64{}, def: map[int]float64{}, games: games}
	for _, t := range ids {
		out.off[t], out.def[t] = x[oi(t)], x[di(t)]
	}
	return out
}

// solve returns x with A x = b for symmetric positive definite A (Cholesky).
func solve(A [][]float64, b []float64) []float64 {
	n := len(b)
	L := make([][]float64, n)
	for i := range L {
		L[i] = make([]float64, i+1)
		for j := 0; j <= i; j++ {
			s := A[i][j]
			for k := 0; k < j; k++ {
				s -= L[i][k] * L[j][k]
			}
			if i == j {
				L[i][i] = math.Sqrt(math.Max(s, 1e-9))
			} else {
				L[i][j] = s / L[j][j]
			}
		}
	}
	y := make([]float64, n)
	for i := 0; i < n; i++ {
		s := b[i]
		for k := 0; k < i; k++ {
			s -= L[i][k] * y[k]
		}
		y[i] = s / L[i][i]
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := y[i]
		for k := i + 1; k < n; k++ {
			s -= L[k][i] * x[k]
		}
		x[i] = s / L[i][i]
	}
	return x
}

func (r *ratings) points(team, opp int, home, neutral bool) float64 {
	h := 0.0
	if !neutral {
		h = r.hfa / 2
		if !home {
			h = -h
		}
	}
	return r.mu + h + r.off[team] - r.def[opp]
}

// Prediction is the model's view of one game.
type Prediction struct {
	AwayPct, HomePct float64 // win probability, percent
	AwayPts, HomePts float64
	Spread           float64 // home margin, points (positive = home favored)
	Total            float64
	Games            int // results the model learned from
}

// Line is the book: spread, total and moneylines with standard juice.
type Line struct {
	AwaySpread, HomeSpread string // "-3.5", "+3.5", "PK"
	Total                  string // "50.5"
	AwayML, HomeML         string
	Favorite               int // team id, or -2 for a pick'em
	FavSpread              string
}

// rankEdge is the bonus, in points, a poll ranking adds to a team's margin.
// Books and fans lean toward the ranked team even when results say the sides
// are close: #1 is worth predRankMax over an unranked team, #25 almost nothing.
func rankEdge(rank int) float64 {
	if rank < 1 || rank > 25 {
		return 0
	}
	return predRankMax * float64(26-rank) / 25
}

// predict projects a game; homeRank and awayRank are poll ranks (0 = unranked).
func predict(r *ratings, home, away int, neutral bool, homeRank, awayRank int) *Prediction {
	hp, ap := r.points(home, away, true, neutral), r.points(away, home, false, neutral)
	// Split the ranking edge across both scores so the total is unchanged.
	edge := rankEdge(homeRank) - rankEdge(awayRank)
	hp, ap = hp+edge/2, ap-edge/2
	spread := hp - ap
	ph := 0.5 * (1 + math.Erf(spread/(predSigma*math.Sqrt2)))
	return &Prediction{AwayPct: 100 * (1 - ph), HomePct: 100 * ph, AwayPts: ap, HomePts: hp, Spread: spread, Total: hp + ap, Games: r.games}
}

func half(v float64) float64 { return math.Round(v*2) / 2 }

func signed(v float64) string {
	if v == 0 {
		return "PK"
	}
	if v > 0 {
		return fmt.Sprintf("+%g", v)
	}
	return fmt.Sprintf("%g", v)
}

// moneyline converts a win probability to American odds with the book's vig.
func moneyline(p float64) string {
	q := math.Min(p*predVig, 0.995)
	var ml float64
	if q >= 0.5 {
		ml = -100 * q / (1 - q)
	} else {
		ml = 100 * (1 - q) / q
	}
	step := 5.0
	if math.Abs(ml) >= 1000 {
		step = 50
	}
	ml = math.Round(ml/step) * step
	ml = math.Max(math.Min(ml, 5000), -10000)
	if ml > 0 {
		return fmt.Sprintf("+%g", ml)
	}
	if ml == -100 {
		return "EVEN"
	}
	return fmt.Sprintf("%g", ml)
}

func (p *Prediction) line(home, away int) *Line {
	s := half(p.Spread)
	l := &Line{HomeSpread: signed(-s), AwaySpread: signed(s), Total: fmt.Sprintf("%g", half(p.Total)),
		HomeML: moneyline(p.HomePct / 100), AwayML: moneyline(p.AwayPct / 100), Favorite: -2, FavSpread: "PK"}
	switch {
	case s > 0:
		l.Favorite, l.FavSpread = home, l.HomeSpread
	case s < 0:
		l.Favorite, l.FavSpread = away, l.AwaySpread
	}
	return l
}
