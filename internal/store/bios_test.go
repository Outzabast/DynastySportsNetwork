package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// Bios save, update and clear for players and coaches of this dynasty, and
// refuse anyone who isn't in it.
func TestBios(t *testing.T) {
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

	if b, err := q.PlayerBio(ctx, c, passer); err != nil || b != "" {
		t.Fatalf("no bio yet: %q, %v", b, err)
	}
	for _, body := range []string{"Three-year **starter**.", "Team captain."} {
		if err := q.SavePlayerBio(ctx, c, passer, body); err != nil {
			t.Fatal(err)
		}
		if b, err := q.PlayerBio(ctx, c, passer); err != nil || b != body {
			t.Fatalf("player bio = %q, %v; want %q", b, err, body)
		}
	}
	if err := q.SavePlayerBio(ctx, c, passer, "  \n"); err != nil {
		t.Fatal(err)
	}
	if b, _ := q.PlayerBio(ctx, c, passer); b != "" {
		t.Fatalf("blank save should clear the bio, got %q", b)
	}
	if err := q.SavePlayerBio(ctx, c, uuid.New(), "Who?"); !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("unknown player: %v", err)
	}

	if err := q.SaveCoachBio(ctx, c, key, "Former QB."); err != nil {
		t.Fatal(err)
	}
	if b, err := q.CoachBio(ctx, c, key); err != nil || b != "Former QB." {
		t.Fatalf("coach bio = %q, %v", b, err)
	}
	if err := q.SaveCoachBio(ctx, c, "nobody|here|x", "Who?"); !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("unknown coach: %v", err)
	}
	if err := q.SaveCoachBio(ctx, c, key, ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := q.CoachBio(ctx, c, key); b != "" {
		t.Fatalf("blank save should clear the coach bio, got %q", b)
	}
}
