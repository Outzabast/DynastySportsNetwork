package store

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bios are Markdown written on the site about a player or coach. Saving an
// empty bio removes it.

// ErrUnknownSubject means the player or coach isn't in this dynasty.
var ErrUnknownSubject = errors.New("no such player or coach in this dynasty")

// PlayerBio returns a player's bio Markdown ("" when none).
func (q *Queries) PlayerBio(ctx context.Context, c *Ctx, person uuid.UUID) (string, error) {
	var body string
	err := q.s.DB.QueryRow(ctx, `SELECT body FROM person_bio WHERE dynasty_id = $1 AND person_id = $2`, c.Dynasty, person).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// SavePlayerBio stores (or, when blank, removes) a player's bio.
func (q *Queries) SavePlayerBio(ctx context.Context, c *Ctx, person uuid.UUID, body string) error {
	if strings.TrimSpace(body) == "" {
		_, err := q.s.DB.Exec(ctx, `DELETE FROM person_bio WHERE dynasty_id = $1 AND person_id = $2`, c.Dynasty, person)
		return err
	}
	tag, err := q.s.DB.Exec(ctx, `INSERT INTO person_bio (dynasty_id, person_id, body)
		SELECT $1, id, $3 FROM person WHERE dynasty_id = $1 AND id = $2
		ON CONFLICT (dynasty_id, person_id) DO UPDATE SET body = EXCLUDED.body, updated_at = now()`, c.Dynasty, person, body)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrUnknownSubject
	}
	return err
}

// CoachBio returns a coach's bio Markdown ("" when none).
func (q *Queries) CoachBio(ctx context.Context, c *Ctx, key string) (string, error) {
	var body string
	err := q.s.DB.QueryRow(ctx, `SELECT body FROM coach_bio WHERE dynasty_id = $1 AND coach_key = $2`, c.Dynasty, key).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return body, err
}

// SaveCoachBio stores (or, when blank, removes) a coach's bio. The coach must
// have been captured on some branch of this dynasty.
func (q *Queries) SaveCoachBio(ctx context.Context, c *Ctx, key string, body string) error {
	if strings.TrimSpace(body) == "" {
		_, err := q.s.DB.Exec(ctx, `DELETE FROM coach_bio WHERE dynasty_id = $1 AND coach_key = $2`, c.Dynasty, key)
		return err
	}
	tag, err := q.s.DB.Exec(ctx, `INSERT INTO coach_bio (dynasty_id, coach_key, body)
		SELECT $1, $2, $3 WHERE $2 <> '' AND EXISTS (SELECT 1 FROM coach_season cs JOIN branch b ON b.id = cs.branch_id
			WHERE b.dynasty_id = $1 AND cs.coach_key = $2)
		ON CONFLICT (dynasty_id, coach_key) DO UPDATE SET body = EXCLUDED.body, updated_at = now()`, c.Dynasty, key, body)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrUnknownSubject
	}
	return err
}
