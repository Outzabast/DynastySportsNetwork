package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// Articles tagged with a player or coach list on that player's or coach's
// page; drafts and other people's stories do not.
func TestArticlePlayerAndCoachTags(t *testing.T) {
	st := testStore(t)
	passer := spoilerFixture(t, st, 1)
	ctx := context.Background()
	key := CoachKey("Dana", "Doe", "coach_07")
	if _, err := st.DB.Exec(ctx, `INSERT INTO coach_season (branch_id, season_index, coach_row, first_name, last_name, position, team_id, age, asset_name,
		wins, losses, school_wins, school_losses, bowl_wins, bowl_losses, conf_titles, natl_titles, playoff_wins, playoff_losses, is_user, snapshot_id, coach_key)
		VALUES (1, 0, 5, 'Dana', 'Doe', 'HeadCoach', 3, 50, 'coach_07', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, false, 1, $1)`, key); err != nil {
		t.Fatal(err)
	}
	q := NewQueries(st)
	c, err := q.Context(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Refs keep known ids in order and drop unknown ones.
	stranger := uuid.New()
	people, err := q.PeopleRefs(ctx, c, []uuid.UUID{stranger, passer, passer})
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0].ID != passer || people[0].Name() != "Pat Passer" {
		t.Fatalf("PeopleRefs = %+v", people)
	}
	coaches, err := q.CoachRefs(ctx, c, []string{"nobody", key})
	if err != nil {
		t.Fatal(err)
	}
	if len(coaches) != 1 || coaches[0].Key != key || coaches[0].TeamID != 3 || coaches[0].Position != "HeadCoach" {
		t.Fatalf("CoachRefs = %+v", coaches)
	}
	if hits, err := q.SearchCoaches(ctx, c, "dana d", 5); err != nil || len(hits) != 1 || hits[0].Key != key {
		t.Fatalf("SearchCoaches = %+v, %v", hits, err)
	}

	save := func(a *Article) int {
		t.Helper()
		id, err := q.SaveArticle(ctx, c, a)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	both := save(&Article{Headline: "Passer and Doe", Published: true, PersonIDs: []uuid.UUID{passer}, CoachKeys: []string{key}})
	coachOnly := save(&Article{Headline: "Doe speaks", Published: true, CoachKeys: []string{key}})
	save(&Article{Headline: "Draft about Passer", PersonIDs: []uuid.UUID{passer}})
	save(&Article{Headline: "Someone else", Published: true, TeamIDs: []int{3}})

	ids := func(f ArticleFilter) []int {
		t.Helper()
		f.Team, f.Season = -1, -1
		list, err := q.Articles(ctx, c, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, a := range list {
			out = append(out, a.ID)
		}
		return out
	}
	if got := ids(ArticleFilter{Person: passer}); len(got) != 1 || got[0] != both {
		t.Fatalf("player news = %v, want [%d]", got, both)
	}
	if got := ids(ArticleFilter{Person: passer, Drafts: true}); len(got) != 2 {
		t.Fatalf("player news with drafts = %v, want 2", got)
	}
	if got := ids(ArticleFilter{Coach: key}); len(got) != 2 || got[0] != coachOnly || got[1] != both {
		t.Fatalf("coach news = %v, want [%d %d]", got, coachOnly, both)
	}
	if got := ids(ArticleFilter{}); len(got) != 3 {
		t.Fatalf("all published = %v, want 3", got)
	}

	// Tags round-trip, and an update can clear them.
	a, err := q.Article(ctx, c, both)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.PersonIDs) != 1 || a.PersonIDs[0] != passer || len(a.CoachKeys) != 1 || a.CoachKeys[0] != key {
		t.Fatalf("stored tags = %v %v", a.PersonIDs, a.CoachKeys)
	}
	a.PersonIDs, a.CoachKeys = nil, nil
	save(a)
	if got := ids(ArticleFilter{Person: passer}); len(got) != 0 {
		t.Fatalf("player news after untagging = %v", got)
	}
}
