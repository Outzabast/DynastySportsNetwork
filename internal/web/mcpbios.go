package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Bio tools read and write the Markdown bio shown on a player's or coach's
// page. Bios are written by people (or agents on their behalf), never filled
// in from game data.

var bioSubjectArgs = map[string]any{
	"player":  map[string]any{"type": "string", "description": "The player, as the uuid in their /player/<uuid> path (search finds it)"},
	"coach":   map[string]any{"type": "string", "description": "The coach, as the key in their /coach/<key> path (search finds it)"},
	"dynasty": dynastyArg,
}

var bioTools = []map[string]any{
	{
		"name":        "get_bio",
		"title":       "Get a player's or coach's bio",
		"description": "The Markdown bio shown on a player's or coach's page, exactly as stored, so it can be revised with save_bio. Give player or coach, not both.",
		"inputSchema": map[string]any{"type": "object", "properties": bioSubjectArgs},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	},
	{
		"name":  "save_bio",
		"title": "Write a player's or coach's bio",
		"description": "Replace the Markdown bio on a player's or coach's page; an empty body removes it. Give player or coach, not both. " +
			"Write only when the user asked. Bios are about the person as a fan would know them: real-world facts only, never ratings, development traits or other game internals, and never invented stats. " +
			"Coaches are matched by name and portrait, which the game can reuse, so two coaches who share both share one bio.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": func() map[string]any {
				p := map[string]any{"body": map[string]any{"type": "string", "description": "Markdown: paragraphs, ## headings, lists, bold, links to site paths, ![](/media/...) images"}}
				for k, v := range bioSubjectArgs {
					p[k] = v
				}
				return p
			}(),
			"required": []string{"body"},
		},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
	},
}

func init() { mcpTools = append(mcpTools, bioTools...) }

type bioArgs struct {
	Player  string  `json:"player"`
	Coach   string  `json:"coach"`
	Body    *string `json:"body"`
	Dynasty int     `json:"dynasty"`
}

// mcpBioSubject resolves the player or coach a bio tool names.
func (s *Site) mcpBioSubject(r *http.Request, raw json.RawMessage) (*store.Ctx, *bioSubject, bioArgs, error) {
	var a bioArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, nil, a, err
		}
	}
	player := strings.TrimPrefix(strings.TrimSpace(a.Player), "/player/")
	coach := strings.TrimPrefix(strings.TrimSpace(a.Coach), "/coach/")
	if u, err := url.PathUnescape(coach); err == nil {
		coach = u
	}
	if (player == "") == (coach == "") {
		return nil, nil, a, errors.New("give player or coach, not both")
	}
	c, err := s.mcpContext(r, a.Dynasty)
	if err != nil {
		return nil, nil, a, err
	}
	ctx := r.Context()
	if coach != "" {
		refs, err := s.q.CoachRefs(ctx, c, []string{coach})
		if err != nil {
			return nil, nil, a, err
		}
		if len(refs) == 0 {
			return nil, nil, a, fmt.Errorf("no coach %q; search finds coaches", coach)
		}
		return c, &bioSubject{key: coach, Name: refs[0].Name(), URL: coachURL(coach), Coach: true}, a, nil
	}
	id, err := uuid.Parse(player)
	if err != nil {
		return nil, nil, a, fmt.Errorf("player %q isn't a /player/<uuid> id; search finds players", a.Player)
	}
	refs, err := s.q.PeopleRefs(ctx, c, []uuid.UUID{id})
	if err != nil {
		return nil, nil, a, err
	}
	if len(refs) == 0 {
		return nil, nil, a, fmt.Errorf("no player %s; search finds players", id)
	}
	return c, &bioSubject{person: id, Name: refs[0].Name(), URL: "/player/" + id.String()}, a, nil
}

func (s *Site) mcpGetBio(r *http.Request, raw json.RawMessage) (string, error) {
	c, b, _, err := s.mcpBioSubject(r, raw)
	if err != nil {
		return "", err
	}
	body, err := s.loadBio(r, c, b)
	if err != nil {
		return "", err
	}
	if body == "" {
		return fmt.Sprintf("%s (%s) has no bio yet. Write one with save_bio.", b.Name, b.URL), nil
	}
	return fmt.Sprintf("# %s bio\n\n- page: %s\n\n## Body (Markdown)\n\n%s\n", b.Name, b.URL, body), nil
}

func (s *Site) mcpSaveBio(r *http.Request, raw json.RawMessage) (string, error) {
	c, b, a, err := s.mcpBioSubject(r, raw)
	if err != nil {
		return "", err
	}
	if a.Body == nil {
		return "", errors.New("body is required; an empty string removes the bio")
	}
	body := strings.TrimSpace(strings.ReplaceAll(*a.Body, "\r\n", "\n"))
	if b.Coach {
		err = s.q.SaveCoachBio(r.Context(), c, b.key, body)
	} else {
		err = s.q.SavePlayerBio(r.Context(), c, b.person, body)
	}
	if err != nil {
		return "", err
	}
	if body == "" {
		return fmt.Sprintf("Removed %s's bio (%s).", b.Name, b.URL), nil
	}
	return fmt.Sprintf("Saved %s's bio. Read it back with read_page %s.", b.Name, b.URL), nil
}
