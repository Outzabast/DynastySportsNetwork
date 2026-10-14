package web

import (
	"context"
	"sync"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Every query is a network round trip to the database, which is slow when
// the database is hosted. Pages run independent queries at the same time and
// share the current season's scoreboard data with the ticker.

// parallel runs fns concurrently and returns the first error in argument
// order (what running them one after another would have returned).
func parallel(fns ...func() error) error {
	errs := make([]error, len(fns))
	var wg sync.WaitGroup
	for i, f := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f()
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// weekCache holds one request's current-season week list and scoreboard
// weeks, so the ticker and the page fetch them once.
type weekCache struct {
	c *store.Ctx

	weeksOnce sync.Once
	weeks     []store.WeekKey
	weeksErr  error

	mu    sync.Mutex
	games map[store.WeekID]*weekGames
}

type weekGames struct {
	done  chan struct{}
	games []*store.Game
	err   error
}

type weekCacheKey struct{}

func withWeekCache(ctx context.Context, c *store.Ctx) context.Context {
	return context.WithValue(ctx, weekCacheKey{}, &weekCache{c: c, games: map[store.WeekID]*weekGames{}})
}

func weekCacheFor(ctx context.Context, c *store.Ctx, season int) *weekCache {
	wc, _ := ctx.Value(weekCacheKey{}).(*weekCache)
	if wc == nil || wc.c != c || season != c.Season {
		return nil
	}
	return wc
}

// seasonWeeks is Queries.Weeks, shared within a request for the current season.
func (s *Site) seasonWeeks(ctx context.Context, c *store.Ctx, season int) ([]store.WeekKey, error) {
	wc := weekCacheFor(ctx, c, season)
	if wc == nil {
		return s.q.Weeks(ctx, c, season)
	}
	wc.weeksOnce.Do(func() { wc.weeks, wc.weeksErr = s.q.Weeks(ctx, c, season) })
	return wc.weeks, wc.weeksErr
}

// weekGames fetches several scoreboard weeks in one query. Within a request,
// current-season weeks already fetched (or being fetched) are reused.
func (s *Site) weekGames(ctx context.Context, c *store.Ctx, season int, ids ...store.WeekID) (map[store.WeekID][]*store.Game, error) {
	wc := weekCacheFor(ctx, c, season)
	if wc == nil {
		return s.q.GamesForWeeks(ctx, c, season, ids)
	}
	wc.mu.Lock()
	var missing []store.WeekID
	var mine []*weekGames
	for _, id := range ids {
		if _, ok := wc.games[id]; !ok {
			wg := &weekGames{done: make(chan struct{})}
			wc.games[id] = wg
			missing = append(missing, id)
			mine = append(mine, wg)
		}
	}
	wanted := make([]*weekGames, len(ids))
	for i, id := range ids {
		wanted[i] = wc.games[id]
	}
	wc.mu.Unlock()
	if len(missing) > 0 {
		got, err := s.q.GamesForWeeks(ctx, c, season, missing)
		for i, id := range missing {
			mine[i].games, mine[i].err = got[id], err
			close(mine[i].done)
		}
	}
	out := map[store.WeekID][]*store.Game{}
	for i, id := range ids {
		<-wanted[i].done
		if wanted[i].err != nil {
			return nil, wanted[i].err
		}
		out[id] = wanted[i].games
	}
	return out, nil
}

// addLeaders fills in box score leaders for the played games among cards,
// with one query for all of them.
func (s *Site) addLeaders(ctx context.Context, c *store.Ctx, season int, cards ...*GameCard) error {
	var rows []int
	for _, gc := range cards {
		if gc.G.Played {
			rows = append(rows, gc.G.Row)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	teams, err := s.q.Teams(ctx, c, season)
	if err != nil {
		return err
	}
	lines, err := s.q.GamesLines(ctx, c, season, rows)
	if err != nil {
		return err
	}
	for _, gc := range cards {
		if gc.G.Played {
			gc.Leaders = gameLeaders(lines[gc.G.Row], teams)
		}
	}
	return nil
}
