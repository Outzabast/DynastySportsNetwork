package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// testStore creates a scratch database on the local Postgres (DSN_TEST_PG,
// default the docker-compose server) and drops it when the test ends. The
// test skips when no server is reachable.
func testStore(t *testing.T) *Store {
	t.Helper()
	admin := os.Getenv("DSN_TEST_PG")
	if admin == "" {
		admin = "postgres://dsn:dsn@localhost:5433/postgres?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Skipf("no local Postgres (%v)", err)
	}
	name := fmt.Sprintf("dsn_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		conn.Close(ctx)
		t.Skipf("cannot create a test database: %v", err)
	}
	u, _ := url.Parse(admin)
	u.Path = "/" + name
	st, err := Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		conn.Close(context.Background())
	})
	return st
}

// spoilerFixture is one season in week 5: the user's team (1) has not played
// its game (row 10) yet, while the CPU game 3-4 (row 11) is already final.
// Week 4 is complete. team3SaveWins = 2 makes the save's team 3 record and
// season totals already count the week 5 game.
func spoilerFixture(t *testing.T, st *Store, team3SaveWins int) (passer uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := st.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO dynasty (id, league_id, base_year, name, user_team_id, last_save_at) VALUES (1, 42, 2036, 'Test', 1, now())`)
	exec(`INSERT INTO branch (id, dynasty_id) VALUES (1, 1)`)
	addSnapshot(t, st, 1)
	for id := 1; id <= 4; id++ {
		exec(`INSERT INTO team (dynasty_id, team_id, short_name, long_name, display_name, nick_name, abbrev, asset_name, primary_color, secondary_color)
			VALUES (1, $1, $2, $2, $2, $2, $2, '', '#000000', '#ffffff')`, id, fmt.Sprintf("T%d", id))
	}
	// The save's own records: team 3's may already count its week 5 win.
	for _, r := range [][3]int{{1, 1, 0}, {2, 0, 1}, {3, team3SaveWins, 0}, {4, 0, 1}} {
		exec(`INSERT INTO team_season (branch_id, season_index, team_id, conference, wins, losses, conf_wins, conf_losses, conf_standing, streak,
			ap_rank, coaches_rank, cfp_rank, off_rank, def_rank, recruiting_rank, playoff_status, playoff_round, head_coach_row, stats, snapshot_id)
			VALUES (1, 0, $1, 'Test', $2, $3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', 0, $4, 1)`,
			r[0], r[1], r[2], fmt.Sprintf(`{"OFFYARDS": %d}`, 300+100*(r[1]-1)))
	}
	game := func(row, week, home, away, hs, as int, played bool) {
		exec(`INSERT INTO game (branch_id, season_index, game_row, week, week_type, status, played, home_team, away_team, home_score, away_score,
			home_quarters, away_quarters, home_ot, away_ot, overtime, simmed, month, day, day_of_week, time_of_day, network, attendance, temperature,
			weather, wind, wind_speed, stadium, bowl_name, game_of_week, home_box, away_box, first_snapshot, last_snapshot, game_type)
			VALUES (1, 0, $1, $2, 'RegularSeason', $3, $4, $5, $6, $7, $8, '{7,7,7,7}', '{0,3,0,0}', 0, 0, false, false, 9, $2, 'Sat', 0, '', 50000, 70,
			'', '', 0, '', '', false, '{"OFFYARDS": 100}', '{"OFFYARDS": 90}', 1, 1, 'conference')`,
			row, week, map[bool]string{true: "HomeWon", false: "Unplayed"}[played], played, home, away, hs, as)
	}
	game(1, 4, 1, 2, 28, 3, true)
	game(2, 4, 3, 4, 28, 3, true)
	game(10, 5, 1, 2, 0, 0, false) // the user's game
	game(11, 5, 3, 4, 31, 7, true) // simulated already: a spoiler
	passer = uuid.New()
	exec(`INSERT INTO person (id, dynasty_id, first_name, last_name, home_town, home_state, handedness, asset_name, birth_date,
		first_branch, first_snapshot, first_season, first_team) VALUES ($1, 1, 'Pat', 'Passer', '', '', '', '', 0, 1, 1, 0, 3)`, passer)
	for _, l := range [][2]int{{2, 100}, {11, 200}} {
		exec(`INSERT INTO player_game_stat (branch_id, season_index, game_row, person_id, team_id, category, stats, snapshot_id, source_row)
			VALUES (1, 0, $1, $2, 3, 'offense', $3, 1, 0)`, l[0], passer, fmt.Sprintf(`{"PASSYARDS": %d, "GAMESPLAYED": 1}`, l[1]))
	}
	// Season totals in the save count week 5 only when its record does.
	yards, games := 100, 1
	if team3SaveWins == 2 {
		yards, games = 300, 2
	}
	exec(`INSERT INTO player_season_stat (branch_id, season_index, person_id, team_id, category, stats, snapshot_id, source_row)
		VALUES (1, 0, $1, 3, 'offense', $2, 1, 0)`, passer, fmt.Sprintf(`{"PASSYARDS": %d, "GAMESPLAYED": %d}`, yards, games))
	return passer
}

func addSnapshot(t *testing.T, st *Store, id int) {
	t.Helper()
	if _, err := st.DB.Exec(context.Background(), `INSERT INTO snapshot (id, dynasty_id, branch_id, sha256, save_name, size, archive_path, parser_version,
		schema_version, season_index, year, stage, week, week_type, offseason_stage, pos, fingerprint)
		VALUES ($1, 1, 1, $2, 'save', 0, '', '', '', 0, 2036, 'NFLSeason', 5, 'RegularSeason', 0, $3, '')`, id, fmt.Sprint(id), int64(id)); err != nil {
		t.Fatal(err)
	}
}

func TestSpoilersHiddenUntilUserGameFinal(t *testing.T) {
	for _, saveCounts := range []bool{false, true} {
		t.Run(fmt.Sprintf("saveCountsWeek=%v", saveCounts), func(t *testing.T) {
			st := testStore(t)
			wins := 1
			if saveCounts {
				wins = 2
			}
			passer := spoilerFixture(t, st, wins)
			ctx := context.Background()
			q := NewQueries(st)
			c, err := q.Context(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}

			// The user's game is not final: the CPU game looks unplayed everywhere.
			wk, err := q.GamesForWeek(ctx, c, 0, "RegularSeason", 5)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range wk {
				if g.Played || g.HomeScore != 0 || g.AwayScore != 0 || g.Status != "Unplayed" || (g.Row == 11 && (g.HomeBox != nil || len(g.HomeQ) != 0)) {
					t.Fatalf("week 5 game %d shows a result: %+v", g.Row, g)
				}
			}
			if g, _ := q.Game(ctx, c, 0, 11); g == nil || g.Played {
				t.Fatalf("box score of game 11 should be a preview: %+v", g)
			}
			if g, _ := q.Game(ctx, c, 0, 2); g == nil || !g.Played {
				t.Fatal("week 4 should stay final")
			}
			weeks, _ := q.Weeks(ctx, c, 0)
			for _, w := range weeks {
				if w.Week == 5 && w.Played != 0 {
					t.Fatalf("week 5 counts %d played games", w.Played)
				}
			}
			byWeek, _ := q.GamesForWeeks(ctx, c, 0, []WeekID{{"RegularSeason", 5}})
			for _, g := range byWeek[WeekID{"RegularSeason", 5}] {
				if g.Played {
					t.Fatal("ticker shows a week 5 result")
				}
			}
			sched, _ := q.TeamGames(ctx, c, 0, 3)
			for _, g := range sched {
				if g.Row == 11 && g.Played {
					t.Fatal("team schedule shows the hidden result")
				}
			}
			if lines, _ := q.GameLines(ctx, c, 0, 11); len(lines) != 0 {
				t.Fatal("hidden game has leaders/box lines")
			}
			if log, _ := q.PlayerGameLog(ctx, c, 0, passer); len(log) != 1 || log[0].Game.Row != 2 {
				t.Fatalf("game log should hold week 4 only: %d entries", len(log))
			}
			teams, err := q.Teams(ctx, c, 0)
			if err != nil {
				t.Fatal(err)
			}
			if r := teams[3].Record(); r != "1-0" {
				t.Fatalf("team 3 record %s, want 1-0", r)
			}
			if r := teams[4].Record(); r != "0-1" {
				t.Fatalf("team 4 record %s, want 0-1", r)
			}
			if y := teams[3].Stats["OFFYARDS"]; y != 300 {
				t.Fatalf("team 3 season yards %d, want 300 (week 4 only)", y)
			}
			if p, _ := q.SeasonPoints(ctx, c, 0); p[3].Games != 1 || p[3].For != 28 {
				t.Fatalf("team 3 points %+v", p[3])
			}
			results, _ := q.SeasonResults(ctx, c, 0)
			for _, r := range results {
				if r.Week == 5 && r.Played {
					t.Fatal("season results show week 5")
				}
			}
			for _, lines := range [][]*SeasonLine{
				must(q.SeasonLines(ctx, c, 0, passer, -1)),
				must(q.SeasonLines(ctx, c, 0, uuid.Nil, 3)),
				must(q.LeagueSeasonLines(ctx, c, 0)),
			} {
				if len(lines) != 1 || lines[0].Stats["PASSYARDS"] != 100 || lines[0].Stats["GAMESPLAYED"] != 1 {
					t.Fatalf("season line should count week 4 only: %+v", lines[0].Stats)
				}
			}

			// A later save: the user's game is final, so everything shows.
			if _, err := st.DB.Exec(ctx, `UPDATE game SET (played, status, home_score, away_score) = (true, 'HomeWon', 24, 10) WHERE game_row = 10`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.Exec(ctx, `UPDATE team_season SET wins = 2 WHERE team_id = 3`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.Exec(ctx, `UPDATE player_season_stat SET stats = '{"PASSYARDS": 300, "GAMESPLAYED": 2}'`); err != nil {
				t.Fatal(err)
			}
			addSnapshot(t, st, 2)
			q.Invalidate()
			c, err = q.Context(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			if g, _ := q.Game(ctx, c, 0, 11); g == nil || !g.Played || g.HomeScore != 31 {
				t.Fatalf("game 11 should be final now: %+v", g)
			}
			if lines, _ := q.GameLines(ctx, c, 0, 11); len(lines) != 1 {
				t.Fatal("game 11 box lines should show")
			}
			teams, _ = q.Teams(ctx, c, 0)
			if r := teams[3].Record(); r != "2-0" {
				t.Fatalf("team 3 record %s, want 2-0", r)
			}
			if r := teams[4].Record(); r != "0-2" {
				t.Fatalf("team 4 record %s, want 0-2", r)
			}
			if lines := must(q.SeasonLines(ctx, c, 0, passer, -1)); lines[0].Stats["PASSYARDS"] != 300 {
				t.Fatalf("season line %+v, want 300 yards", lines[0].Stats)
			}
			if log, _ := q.PlayerGameLog(ctx, c, 0, passer); len(log) != 2 {
				t.Fatalf("game log should hold both games: %d", len(log))
			}
		})
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
