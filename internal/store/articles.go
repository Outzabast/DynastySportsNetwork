package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Article is a story written by hand for one dynasty.
type Article struct {
	ID           int
	Dynasty      int
	Slug         string
	Headline     string
	Dek          string // the one-line summary under the headline
	Byline       string
	Body         string // Markdown
	Image        string // site path, e.g. /media/articles/2034-week15.jpg
	ImageCaption string
	Season       *int // linked game, when the story is a recap
	GameRow      *int
	TeamIDs      []int
	PersonIDs    []uuid.UUID
	Published    bool
	PublishedAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// When is the time a reader sees: publication, else last edit.
func (a *Article) When() time.Time {
	if a.PublishedAt != nil {
		return *a.PublishedAt
	}
	return a.UpdatedAt
}

// HasGame reports whether the story is tied to a game.
func (a *Article) HasGame() bool { return a.Season != nil && a.GameRow != nil }

var slugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify makes a URL slug from a headline.
func Slugify(s string) string {
	s = strings.Trim(slugJunk.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 80 {
		s = strings.TrimRight(s[:80], "-")
	}
	if s == "" {
		s = "story"
	}
	return s
}

const articleCols = `id, dynasty_id, slug, headline, dek, byline, body, image, image_caption, season_index, game_row, team_ids, person_ids,
	published, published_at, created_at, updated_at`

func scanArticle(row interface{ Scan(...any) error }) (*Article, error) {
	a := &Article{}
	var teams []int32
	err := row.Scan(&a.ID, &a.Dynasty, &a.Slug, &a.Headline, &a.Dek, &a.Byline, &a.Body, &a.Image, &a.ImageCaption, &a.Season, &a.GameRow,
		&teams, &a.PersonIDs, &a.Published, &a.PublishedAt, &a.CreatedAt, &a.UpdatedAt)
	for _, t := range teams {
		a.TeamIDs = append(a.TeamIDs, int(t))
	}
	return a, err
}

// ArticleFilter narrows an article listing.
type ArticleFilter struct {
	Drafts bool // include unpublished stories
	Team   int  // -1 = any
	Season int  // with GameRow: stories about one game; -1 = any
	Game   int
	Limit  int
}

// Articles lists a dynasty's stories, newest first.
func (q *Queries) Articles(ctx context.Context, c *Ctx, f ArticleFilter) ([]*Article, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := q.s.DB.Query(ctx, `SELECT `+articleCols+` FROM article WHERE dynasty_id = $1
		AND ($2 OR published) AND ($3 < 0 OR $3 = ANY(team_ids)) AND ($4 < 0 OR (season_index = $4 AND game_row = $5))
		ORDER BY COALESCE(published_at, updated_at) DESC, id DESC LIMIT $6`, c.Dynasty, f.Drafts, f.Team, f.Season, f.Game, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Article
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Article loads one story of this dynasty (nil when missing).
func (q *Queries) Article(ctx context.Context, c *Ctx, id int) (*Article, error) {
	a, err := scanArticle(q.s.DB.QueryRow(ctx, `SELECT `+articleCols+` FROM article WHERE dynasty_id = $1 AND id = $2`, c.Dynasty, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// SaveArticle inserts (ID 0) or updates a story and returns its id. The
// first publication stamps published_at; unpublishing keeps the stamp.
func (q *Queries) SaveArticle(ctx context.Context, c *Ctx, a *Article) (int, error) {
	a.Slug = Slugify(a.Headline)
	teams := make([]int32, len(a.TeamIDs))
	for i, t := range a.TeamIDs {
		teams[i] = int32(t)
	}
	persons := a.PersonIDs
	if persons == nil {
		persons = []uuid.UUID{}
	}
	if a.ID == 0 {
		err := q.s.DB.QueryRow(ctx, `INSERT INTO article (dynasty_id, slug, headline, dek, byline, body, image, image_caption, season_index, game_row,
			team_ids, person_ids, published, published_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::int[], $12::uuid[], $13, CASE WHEN $13 THEN now() END) RETURNING id`,
			c.Dynasty, a.Slug, a.Headline, a.Dek, a.Byline, a.Body, a.Image, a.ImageCaption, a.Season, a.GameRow, teams, persons, a.Published).Scan(&a.ID)
		return a.ID, err
	}
	tag, err := q.s.DB.Exec(ctx, `UPDATE article SET slug = $3, headline = $4, dek = $5, byline = $6, body = $7, image = $8, image_caption = $9,
		season_index = $10, game_row = $11, team_ids = $12::int[], person_ids = $13::uuid[], published = $14,
		published_at = CASE WHEN $14 AND published_at IS NULL THEN now() ELSE published_at END, updated_at = now()
		WHERE dynasty_id = $1 AND id = $2`,
		c.Dynasty, a.ID, a.Slug, a.Headline, a.Dek, a.Byline, a.Body, a.Image, a.ImageCaption, a.Season, a.GameRow, teams, persons, a.Published)
	if err == nil && tag.RowsAffected() == 0 {
		err = errors.New("article not found")
	}
	return a.ID, err
}

// DeleteArticle removes a story. Articles are the one thing the site writes
// itself, so deleting one is the author's call.
func (q *Queries) DeleteArticle(ctx context.Context, c *Ctx, id int) error {
	_, err := q.s.DB.Exec(ctx, `DELETE FROM article WHERE dynasty_id = $1 AND id = $2`, c.Dynasty, id)
	return err
}
