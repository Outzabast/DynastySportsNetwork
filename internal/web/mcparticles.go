package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Article tools let agents write for the site the same way the /write editor
// does. Articles are the one thing the site writes itself (never game data),
// and a save is a draft unless the agent says publish.

var articleTools = []map[string]any{
	{
		"name":        "get_article",
		"title":       "Get an article",
		"description": "An article's fields and its Markdown body exactly as stored, drafts included, so it can be revised with save_article. /news lists every article with its id.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":      map[string]any{"type": "integer", "description": "Article id, the number in /story/<id>/..."},
				"dynasty": dynastyArg,
			},
			"required": []string{"id"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	},
	{
		"name":  "save_article",
		"title": "Write or edit an article",
		"description": "Create a news article (no id) or update one (id). The body is Markdown: ## headings, lists, bold, and links to site paths such as [Box score](/game/2036/410). " +
			"Saves a draft unless publish is true; drafts are visible to editors only. Publish only when the user asked for it. " +
			"On update, fields left out keep their stored values. Use the same real-world facts the pages show; never invent stats.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":            map[string]any{"type": "integer", "description": "Article to update; omit to create one"},
				"headline":      map[string]any{"type": "string", "description": "Required when creating"},
				"dek":           map[string]any{"type": "string", "description": "One-line summary shown under the headline"},
				"byline":        map[string]any{"type": "string", "description": "Author shown on the story, e.g. AP"},
				"body":          map[string]any{"type": "string", "description": "Markdown body; required when creating"},
				"teams":         map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Team ids the story is about (site_overview lists them); the story appears on those team pages"},
				"game":          map[string]any{"type": "string", "description": "Link the story to one game, as <season>/<row> from its /game path, e.g. 2036/410. Empty string removes the link"},
				"kind":          map[string]any{"type": "string", "enum": []string{"recap", "preview"}, "description": "With game: recap (default) or preview"},
				"image":         map[string]any{"type": "string", "description": "Lead image, a /media/... path on this site"},
				"image_caption": map[string]any{"type": "string"},
				"publish":       map[string]any{"type": "boolean", "description": "true publishes, false makes it a draft. Default for a new article: false; on update, left out keeps the current state"},
				"dynasty":       dynastyArg,
			},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false},
	},
}

func init() { mcpTools = append(mcpTools, articleTools...) }

type articleArgs struct {
	ID           int     `json:"id"`
	Dynasty      int     `json:"dynasty"`
	Headline     *string `json:"headline"`
	Dek          *string `json:"dek"`
	Byline       *string `json:"byline"`
	Body         *string `json:"body"`
	Teams        *[]int  `json:"teams"`
	Game         *string `json:"game"`
	Kind         *string `json:"kind"`
	Image        *string `json:"image"`
	ImageCaption *string `json:"image_caption"`
	Publish      *bool   `json:"publish"`
}

func parseArticleArgs(raw json.RawMessage) (articleArgs, error) {
	var a articleArgs
	if len(raw) == 0 {
		return a, nil
	}
	err := json.Unmarshal(raw, &a)
	return a, err
}

func (s *Site) mcpGetArticle(r *http.Request, raw json.RawMessage) (string, error) {
	a, err := parseArticleArgs(raw)
	if err != nil {
		return "", err
	}
	c, err := s.mcpContext(r, a.Dynasty)
	if err != nil {
		return "", err
	}
	art, err := s.q.Article(r.Context(), c, a.ID)
	if err != nil {
		return "", err
	}
	if art == nil {
		return "", fmt.Errorf("no article %d; see /news", a.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", art.Headline)
	fmt.Fprintf(&b, "- id: %d\n- url: %s\n- state: %s\n", art.ID, storyURL(art), articleState(art))
	fmt.Fprintf(&b, "- dek: %s\n- byline: %s\n", art.Dek, art.Byline)
	if art.HasGame() {
		fmt.Fprintf(&b, "- game: %d/%d (%s)\n", c.YearOf(*art.Season), *art.GameRow, art.Kind)
	}
	if len(art.TeamIDs) > 0 {
		ids := make([]string, len(art.TeamIDs))
		for i, t := range art.TeamIDs {
			ids[i] = itoa(t)
		}
		fmt.Fprintf(&b, "- teams: %s\n", strings.Join(ids, ", "))
	}
	if art.Image != "" {
		fmt.Fprintf(&b, "- image: %s (%s)\n", art.Image, art.ImageCaption)
	}
	b.WriteString("\n## Body (Markdown)\n\n" + art.Body + "\n")
	return b.String(), nil
}

func (s *Site) mcpSaveArticle(r *http.Request, raw json.RawMessage) (string, error) {
	a, err := parseArticleArgs(raw)
	if err != nil {
		return "", err
	}
	ctx := r.Context()
	c, err := s.mcpContext(r, a.Dynasty)
	if err != nil {
		return "", err
	}
	art := &store.Article{}
	if a.ID != 0 {
		if art, err = s.q.Article(ctx, c, a.ID); err != nil {
			return "", err
		}
		if art == nil {
			return "", fmt.Errorf("no article %d; omit id to create one", a.ID)
		}
	} else if a.Headline == nil || a.Body == nil {
		return "", errors.New("a new article needs headline and body")
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&art.Headline, a.Headline)
	set(&art.Dek, a.Dek)
	set(&art.Byline, a.Byline)
	set(&art.ImageCaption, a.ImageCaption)
	if a.Body != nil {
		art.Body = strings.ReplaceAll(*a.Body, "\r\n", "\n")
	}
	if art.Headline == "" {
		return "", errors.New("headline can't be empty")
	}
	if a.Image != nil {
		img := strings.TrimSpace(*a.Image)
		if img != "" && !strings.HasPrefix(img, "/media/") {
			return "", errors.New("image must be a /media/... path on this site")
		}
		art.Image = img
	}
	if a.Teams != nil {
		teams, err := s.q.Teams(ctx, c, c.Season)
		if err != nil {
			return "", err
		}
		known := map[int]bool{}
		for _, t := range teams {
			known[t.ID] = true
		}
		art.TeamIDs = nil
		for _, id := range *a.Teams {
			if !known[id] {
				return "", fmt.Errorf("no team %d; site_overview lists team ids", id)
			}
			art.TeamIDs = append(art.TeamIDs, id)
		}
	}
	if a.Game != nil {
		art.Season, art.GameRow, art.Kind = nil, nil, ""
		if g := strings.TrimSpace(*a.Game); g != "" {
			season, row, ok := parseGameRef(c, g)
			if !ok {
				return "", fmt.Errorf("game %q isn't <season>/<row>, e.g. 2036/410", g)
			}
			art.Season, art.GameRow, art.Kind = &season, &row, "recap"
		}
	}
	if a.Kind != nil && art.HasGame() {
		switch *a.Kind {
		case "recap", "preview":
			art.Kind = *a.Kind
		default:
			return "", errors.New("kind is recap or preview")
		}
	}
	if a.Publish != nil {
		art.Published = *a.Publish
	}
	if _, err := s.q.SaveArticle(ctx, c, art); err != nil {
		return "", err
	}
	verb := "Updated"
	if a.ID == 0 {
		verb = "Created"
	}
	return fmt.Sprintf("%s article %d (%s): %s\nRead it back with read_page %s.", verb, art.ID, articleState(art), storyURL(art), storyURL(art)), nil
}

func articleState(a *store.Article) string {
	if a.Published {
		return "published"
	}
	return "draft"
}
