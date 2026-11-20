package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Bios are Markdown written on the site about a player or coach, edited at
// /player/{id}/bio and /coach/{key}/bio by anyone who can edit stories.

// bioSubject is the player or coach a bio route names.
type bioSubject struct {
	person uuid.UUID // set for a player
	key    string    // set for a coach
	Name   string
	URL    string // their page
	Coach  bool
}

func (b *bioSubject) EditURL() string { return b.URL + "/bio" }

// subject resolves the {id} or {key} in a bio route (errNotFound when unknown).
func (s *Site) bioSubject(r *http.Request, c *store.Ctx) (*bioSubject, error) {
	ctx := r.Context()
	if key := r.PathValue("key"); key != "" {
		refs, err := s.q.CoachRefs(ctx, c, []string{key})
		if err != nil {
			return nil, err
		}
		if len(refs) == 0 {
			return nil, errNotFound
		}
		return &bioSubject{key: key, Name: refs[0].Name(), URL: coachURL(key), Coach: true}, nil
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		return nil, errNotFound
	}
	refs, err := s.q.PeopleRefs(ctx, c, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, errNotFound
	}
	return &bioSubject{person: id, Name: refs[0].Name(), URL: "/player/" + id.String()}, nil
}

func (s *Site) loadBio(r *http.Request, c *store.Ctx, b *bioSubject) (string, error) {
	if b.Coach {
		return s.q.CoachBio(r.Context(), c, b.key)
	}
	return s.q.PlayerBio(r.Context(), c, b.person)
}

// bioHTML renders a stored bio for a page ("" when there is none).
func bioHTML(body string) template.HTML {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return renderMarkdown(body)
}

func (s *Site) bioEditor(r *http.Request, c *store.Ctx) (string, data, error) {
	if !s.canEdit(r) {
		return "", nil, errNotFound
	}
	b, err := s.bioSubject(r, c)
	if err != nil {
		return "", nil, err
	}
	body, err := s.loadBio(r, c, b)
	if err != nil {
		return "", nil, err
	}
	return "bioeditor", data{"S": b, "Body": body, "Error": r.URL.Query().Get("error")}, nil
}

func (s *Site) saveBio(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error) {
	b, err := s.bioSubject(r, c)
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(strings.ReplaceAll(r.FormValue("body"), "\r\n", "\n"))
	if b.Coach {
		err = s.q.SaveCoachBio(r.Context(), c, b.key, body)
	} else {
		err = s.q.SavePlayerBio(r.Context(), c, b.person, body)
	}
	if errors.Is(err, store.ErrUnknownSubject) {
		return b.EditURL() + "?error=" + url.QueryEscape("That player or coach isn't in this dynasty."), nil
	}
	if err != nil {
		return "", err
	}
	return b.URL, nil
}
