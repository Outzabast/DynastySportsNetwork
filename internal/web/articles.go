package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// Articles are written on the machine that runs the site. Editing is open to
// loopback clients only (DSN_EDIT_ANYWHERE=1 lifts that), and every write
// must come from this site's own pages. Behind Cloudflare Access every request
// is already authenticated, so the hosted site lets anyone who got in edit.
func (s *Site) canEdit(r *http.Request) bool {
	if s.opt.BehindAccess || os.Getenv("DSN_EDIT_ANYWHERE") == "1" {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin rejects cross-site form posts (CSRF) against the local server.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	for _, h := range []string{"Origin", "Referer"} {
		if v := r.Header.Get(h); v != "" {
			u, err := url.Parse(v)
			return err == nil && u.Host == r.Host
		}
	}
	return false
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM, extension.Typographer))

// renderMarkdown turns a story body into HTML. Raw HTML in the source is
// dropped (goldmark's default), so a body can't inject scripts.
func renderMarkdown(src string) template.HTML {
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

func storyURL(a *store.Article) string {
	return fmt.Sprintf("/story/%d/%s", a.ID, a.Slug)
}

// timeAgo is ESPN's "10h" / "Oct 3, 2026" stamp.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	return t.Format("Jan 2, 2006")
}

// StoryCard is an article with what list pages need.
type StoryCard struct {
	*store.Article
	URL   string
	Teams []*store.Team
}

// NewsCard is the "<Name> News" box on team, player and coach pages.
type NewsCard struct {
	Title   string // "Texas State News"
	AllURL  string // the /news listing filtered to the subject
	Stories []StoryCard
}

func newsCard(title, all string, stories []StoryCard) *NewsCard {
	if len(stories) == 0 {
		return nil
	}
	return &NewsCard{Title: title, AllURL: all, Stories: stories}
}

// TagChip is one team, player or coach tag in the editor and on a story.
type TagChip struct {
	Kind  string `json:"kind"`  // "team", "person" or "coach" (the form field name)
	Value string `json:"value"` // team id, person UUID or coach key
	Name  string `json:"name"`
	Sub   string `json:"sub"` // "QB · Texas State"
	URL   string `json:"url"`
	pos   string
	team  int
}

// chipSub is the line under a tag's name: position (or role) and team.
func chipSub(pos string, t *store.Team) string {
	if t == nil {
		return pos
	}
	if pos == "" {
		return t.Name
	}
	return pos + " · " + t.Name
}

func playerChip(p store.PlayerRef, teams map[int]*store.Team) TagChip {
	return TagChip{Kind: "person", Value: p.ID.String(), Name: p.Name(), Sub: chipSub(p.Position, teams[p.TeamID]),
		URL: "/player/" + p.ID.String(), pos: p.Position, team: p.TeamID}
}

// coachRoleShort is a coach's role as a tag shows it: HC, OC, DC.
func coachRoleShort(p string) string {
	switch p {
	case "HeadCoach":
		return "HC"
	case "OffensiveCoordinator":
		return "OC"
	case "DefensiveCoordinator":
		return "DC"
	}
	return positionLabel(p)
}

func coachChip(k store.CoachRef, teams map[int]*store.Team) TagChip {
	pos := coachRoleShort(k.Position)
	return TagChip{Kind: "coach", Value: k.Key, Name: k.Name(), Sub: chipSub(pos, teams[k.TeamID]), URL: coachURL(k.Key), pos: pos, team: k.TeamID}
}

func teamChip(t *store.Team) TagChip {
	return TagChip{Kind: "team", Value: strconv.Itoa(t.ID), Name: t.Name, Sub: t.Conference, URL: fmt.Sprintf("/team/%d", t.ID)}
}

// tagChips loads the chips for an article's player and coach tags.
func (s *Site) tagChips(ctx context.Context, c *store.Ctx, teams map[int]*store.Team, a *store.Article) ([]TagChip, error) {
	people, coaches, err := s.resolveTags(ctx, c, a.PersonIDs, a.CoachKeys)
	if err != nil {
		return nil, err
	}
	var out []TagChip
	for _, p := range people {
		out = append(out, playerChip(p, teams))
	}
	for _, k := range coaches {
		out = append(out, coachChip(k, teams))
	}
	return out, nil
}

// resolveTags keeps the players and coaches that exist in this dynasty, in
// the order given, without repeats.
func (s *Site) resolveTags(ctx context.Context, c *store.Ctx, ids []uuid.UUID, keys []string) ([]store.PlayerRef, []store.CoachRef, error) {
	var people []store.PlayerRef
	var coaches []store.CoachRef
	err := parallel(
		func() (err error) { people, err = s.q.PeopleRefs(ctx, c, ids); return },
		func() (err error) { coaches, err = s.q.CoachRefs(ctx, c, keys); return },
	)
	return people, coaches, err
}

func (s *Site) storyCards(teams map[int]*store.Team, list []*store.Article) []StoryCard {
	out := make([]StoryCard, 0, len(list))
	for _, a := range list {
		sc := StoryCard{Article: a, URL: storyURL(a)}
		for _, id := range a.TeamIDs {
			if t := teams[id]; t != nil {
				sc.Teams = append(sc.Teams, t)
			}
		}
		out = append(out, sc)
	}
	return out
}

func (s *Site) news(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	qs := r.URL.Query()
	f := store.ArticleFilter{Drafts: s.canEdit(r), Team: atoi(qs.Get("team"), -1), Season: -1, Limit: 100}
	if id, err := uuid.Parse(qs.Get("player")); err == nil {
		f.Person = id
	}
	f.Coach = qs.Get("coach")
	list, err := s.q.Articles(ctx, c, f)
	if err != nil {
		return "", nil, err
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", nil, err
	}
	d := data{"Stories": s.storyCards(teams, list)}
	if f.Team >= 0 {
		d["Team"] = teams[f.Team]
	}
	// A player's or coach's news: name them in the heading.
	if f.Person != uuid.Nil || f.Coach != "" {
		var ids []uuid.UUID
		var keys []string
		if f.Person != uuid.Nil {
			ids = []uuid.UUID{f.Person}
		}
		if f.Coach != "" {
			keys = []string{f.Coach}
		}
		people, coaches, err := s.resolveTags(ctx, c, ids, keys)
		if err != nil {
			return "", nil, err
		}
		switch {
		case len(people) == 1:
			d["Subject"] = playerChip(people[0], teams)
		case len(coaches) == 1:
			d["Subject"] = coachChip(coaches[0], teams)
		default:
			return "", nil, errNotFound
		}
	}
	return "news", d, nil
}

func (s *Site) story(r *http.Request, c *store.Ctx) (string, data, error) {
	ctx := r.Context()
	a, err := s.q.Article(ctx, c, atoi(r.PathValue("id"), -1))
	if err != nil {
		return "", nil, err
	}
	if a == nil || (!a.Published && !s.canEdit(r)) {
		return "", nil, errNotFound
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", nil, err
	}
	chips, err := s.tagChips(ctx, c, teams, a)
	if err != nil {
		return "", nil, err
	}
	d := data{"A": a, "Body": renderMarkdown(a.Body), "Tagged": s.storyCards(teams, []*store.Article{a})[0].Teams, "TaggedPeople": chips}
	if a.HasGame() {
		if g, err := s.q.Game(ctx, c, *a.Season, *a.GameRow); err == nil && g != nil {
			cards, err := s.cards(ctx, c, *a.Season, []*store.Game{g}, false)
			if err == nil && len(cards) == 1 {
				d["Game"] = cards[0]
				d["Quarters"] = quarterCols(g)
			}
		}
	}
	more, err := s.q.Articles(ctx, c, store.ArticleFilter{Team: -1, Season: -1, Limit: 6})
	if err != nil {
		return "", nil, err
	}
	var others []*store.Article
	for _, m := range more {
		if m.ID != a.ID && len(others) < 5 {
			others = append(others, m)
		}
	}
	d["More"] = s.storyCards(teams, others)
	return "story", d, nil
}

// editor serves the write/edit form. ?game=2034/865 prefills a game story:
// a preview when the game is unplayed (or ?kind=preview), else a recap.
func (s *Site) editor(r *http.Request, c *store.Ctx) (string, data, error) {
	if !s.canEdit(r) {
		return "", nil, errNotFound
	}
	ctx := r.Context()
	a := &store.Article{}
	if id := r.PathValue("id"); id != "" {
		found, err := s.q.Article(ctx, c, atoi(id, -1))
		if err != nil {
			return "", nil, err
		}
		if found == nil {
			return "", nil, errNotFound
		}
		a = found
	} else if g := r.URL.Query().Get("game"); g != "" {
		if season, row, ok := parseGameRef(c, g); ok {
			a.Season, a.GameRow = &season, &row
			a.Kind = "recap"
			if game, err := s.q.Game(ctx, c, season, row); err == nil && game != nil {
				a.TeamIDs = []int{game.Away, game.Home}
				if !game.Played {
					a.Kind = "preview"
				}
			}
			if k := r.URL.Query().Get("kind"); k == "preview" || k == "recap" {
				a.Kind = k
			}
		}
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", nil, err
	}
	var list []*store.Team
	for _, t := range teams {
		if t.HasSeason {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	all := make([]TagChip, len(list))
	for i, t := range list {
		all[i] = teamChip(t)
	}
	gameRef := ""
	if a.HasGame() {
		gameRef = fmt.Sprintf("%d/%d", c.YearOf(*a.Season), *a.GameRow)
	}
	var chips []TagChip
	for _, id := range a.TeamIDs {
		if t := teams[id]; t != nil {
			chips = append(chips, teamChip(t))
		}
	}
	people, err := s.tagChips(ctx, c, teams, a)
	if err != nil {
		return "", nil, err
	}
	chips = append(chips, people...)
	return "editor", data{"A": a, "TeamChips": all, "Chips": chips, "GameRef": gameRef, "Error": r.URL.Query().Get("error")}, nil
}

// parseGameRef reads "2034/865" or a pasted box score URL ending in it.
func parseGameRef(c *store.Ctx, v string) (season, row int, ok bool) {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if i := strings.Index(v, "game/"); i >= 0 {
		v = v[i+len("game/"):]
	}
	parts := strings.Split(v, "/")
	if len(parts) < 2 {
		return 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	r, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || y < c.BaseYear {
		return 0, 0, false
	}
	return y - c.BaseYear, r, true
}

// action wraps a form post: editor-only, same-origin, then redirect.
func (s *Site) action(f func(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.canEdit(r) || !sameOrigin(r) {
			http.Error(w, "editing is only available on the machine running the site", http.StatusForbidden)
			return
		}
		c, err := s.context(r)
		if err != nil {
			s.fail(w, err)
			return
		}
		to, err := f(w, r, c)
		if err != nil {
			s.fail(w, err)
			return
		}
		if to != "" {
			http.Redirect(w, r, to, http.StatusSeeOther)
		}
	}
}

func (s *Site) saveStory(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error) {
	if err := r.ParseForm(); err != nil {
		return "", err
	}
	ctx := r.Context()
	a := &store.Article{}
	if id := r.PathValue("id"); id != "" {
		found, err := s.q.Article(ctx, c, atoi(id, -1))
		if err != nil {
			return "", err
		}
		if found == nil {
			return "", errNotFound
		}
		a = found
	}
	f := r.PostForm
	a.Headline = strings.TrimSpace(f.Get("headline"))
	a.Dek = strings.TrimSpace(f.Get("dek"))
	a.Byline = strings.TrimSpace(f.Get("byline"))
	a.Body = strings.ReplaceAll(f.Get("body"), "\r\n", "\n")
	a.Image = strings.TrimSpace(f.Get("image"))
	a.ImageCaption = strings.TrimSpace(f.Get("image_caption"))
	a.Published = f.Get("publish") == "1"
	a.Season, a.GameRow, a.Kind = nil, nil, ""
	if season, row, ok := parseGameRef(c, f.Get("game")); ok {
		a.Season, a.GameRow, a.Kind = &season, &row, "recap"
		if f.Get("kind") == "preview" {
			a.Kind = "preview"
		}
	}
	a.TeamIDs = nil
	for _, v := range f["team"] {
		if id, err := strconv.Atoi(v); err == nil {
			a.TeamIDs = append(a.TeamIDs, id)
		}
	}
	var ids []uuid.UUID
	for _, v := range f["person"] {
		if id, err := uuid.Parse(v); err == nil {
			ids = append(ids, id)
		}
	}
	people, coaches, err := s.resolveTags(ctx, c, ids, f["coach"])
	if err != nil {
		return "", err
	}
	a.PersonIDs, a.CoachKeys = nil, nil
	for _, p := range people {
		a.PersonIDs = append(a.PersonIDs, p.ID)
	}
	for _, k := range coaches {
		a.CoachKeys = append(a.CoachKeys, k.Key)
	}
	if a.Image != "" && !strings.HasPrefix(a.Image, "/media/") {
		a.Image = "" // only images served by this site
	}
	if a.Headline == "" {
		back := "/write"
		if a.ID != 0 {
			back = fmt.Sprintf("/story/%d/edit", a.ID)
		}
		return back + "?error=" + url.QueryEscape("A headline is required."), nil
	}
	if _, err := s.q.SaveArticle(ctx, c, a); err != nil {
		return "", err
	}
	return storyURL(a), nil
}

func (s *Site) deleteStory(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error) {
	if r.FormValue("confirm") != "delete" {
		return fmt.Sprintf("/story/%s/edit?error=%s", r.PathValue("id"), url.QueryEscape("Type delete to confirm.")), nil
	}
	if err := s.q.DeleteArticle(r.Context(), c, atoi(r.PathValue("id"), -1)); err != nil {
		return "", err
	}
	return "/news", nil
}

// tagPageSize is how many players or coaches the tag picker shows at once.
const tagPageSize = 20

// tagBrowse pages through players or coaches for the editor's tag picker:
// GET /write/people?kind=person|coach&q=baylor&page=2 ->
// {items: [{kind, value, name, sub, url}], total, page, pages}.
func (s *Site) tagBrowse(w http.ResponseWriter, r *http.Request) {
	if !s.canEdit(r) {
		http.NotFound(w, r)
		return
	}
	c, err := s.context(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()
	qs := r.URL.Query()
	filter := strings.TrimSpace(qs.Get("q"))
	page := max(atoi(qs.Get("page"), 1), 1)
	pg := store.Page{Offset: (page - 1) * tagPageSize, Limit: tagPageSize}
	var teams map[int]*store.Team
	items := []TagChip{}
	total := 0
	err = parallel(
		func() (err error) { teams, err = s.q.Teams(ctx, c, c.Season); return },
		func() error {
			if qs.Get("kind") == "coach" {
				list, n, err := s.q.BrowseCoaches(ctx, c, filter, pg)
				total = n
				for _, k := range list {
					items = append(items, coachChip(k, nil))
				}
				return err
			}
			list, n, err := s.q.BrowsePeople(ctx, c, filter, pg)
			total = n
			for _, p := range list {
				items = append(items, playerChip(p, nil))
			}
			return err
		},
	)
	if err != nil {
		s.fail(w, err)
		return
	}
	// Team names are filled in once both loads are done.
	for i := range items {
		items[i].Sub = chipSub(items[i].pos, teams[items[i].team])
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"items": items, "total": total, "page": page, "pages": max((total+tagPageSize-1)/tagPageSize, 1)})
}

// preview renders Markdown for the editor's preview pane.
func (s *Site) previewStory(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, string(renderMarkdown(r.FormValue("body"))))
	return "", nil
}

var imageTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

// upload stores an article image under media/articles (never in the DB).
func (s *Site) upload(w http.ResponseWriter, r *http.Request, c *store.Ctx) (string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer file.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	ext, ok := imageTypes[http.DetectContentType(head[:n])]
	if !ok {
		http.Error(w, "only JPEG, PNG, WebP or GIF images", http.StatusBadRequest)
		return "", nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	suffix := make([]byte, 3)
	rand.Read(suffix)
	base := store.Slugify(strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename)))
	name := fmt.Sprintf("%s-%s-%s%s", time.Now().Format("2006-01-02"), base, hex.EncodeToString(suffix), ext)
	if s.opt.MediaURL != "" {
		if err := putMedia(r.Context(), s.opt.MediaURL, "articles/"+name, http.DetectContentType(head[:n]), file, hdr.Size); err != nil {
			return "", err
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"url": "/media/articles/" + name})
		return "", nil
	}
	dir := filepath.Join(s.opt.MediaDir, "articles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": "/media/articles/" + name})
	return "", nil
}
