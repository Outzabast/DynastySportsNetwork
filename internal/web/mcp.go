package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// MCP: a Model Context Protocol server (Streamable HTTP transport, JSON
// responses, no sessions) at /mcp, so AI agents on this machine can browse
// the site the way the user does. It is read-only: every tool renders the
// site's own GET pages, so agents see exactly what the pages show and
// nothing more (AGENTS.md rule 1). Only loopback clients are served.
//
//	claude mcp add --transport http dsn http://localhost:8080/mcp

const mcpPath = "/mcp"

var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

const mcpInstructions = `Dynasty Sports Network: an ESPN-style site for one EA Sports College Football 27 dynasty. Every team, player, coach and game comes from the user's dynasty save, not real life. Start with site_overview (current week, seasons, team ids, page guide), use search to find a player's or team's page, then read_page any path. Pages link to each other with site paths (player pages are /player/<uuid>), and each page lists the query parameters its filters accept (season is a calendar year). Exports (/player/<id>/export, /game/<season>/<row>/export, /team/<id>/export/season, /team/<id>/export/stats, /stats/teams/export) give the full stat tables of those pages.`

const pageGuide = `Pages (read_page paths; season=<year> works on most):
- /  front page: headlines, top games, standings snapshot
- /scoreboard?season=&type=&week=&conf=  scores for one week
- /game/<season>/<row>  box score, scoring summary, team and player stats (preview before kickoff)
- /standings?season=  conference standings
- /rankings?season=&week=  AP / Coaches / CFP polls
- /teams  all teams by conference
- /team/<id>?season=  team home: record, schedule, leaders, coaching staff
- /team/<id>/roster?season=  full roster, incl. players who left that season and why (NFL draft round, transfer, graduated)
- /team/<id>/schedule?season=  results and schedule
- /team/<id>/stats?season=  team and player season stats
- /team/<id>/history  season-by-season records, bowls, coaches, NFL draft picks
- /coaches, /coach/<key>  coaching records, stints, players drafted
- /player/<uuid>  bio, career stats by season, game logs, awards, draft, transactions
- /stats?season=&conf=&cat=  individual leaders;  /stats/teams?season=&conf=&cat=&mode=  team comparison
- /awards?season=  season awards and players of the week
- /records?scope=&name=  record book (league / conference / team)
- /recruiting?season=  recruiting classes and commits
- /history  champions and season summaries
- /news, /story/<id>  articles
- /search?q=  player and team search
- /dynasties  all recorded dynasties (pass dynasty=<id> to any tool to read another one)
- /admin  data status: saves recorded, review flags
Markdown exports (full tables): /player/<uuid>/export, /game/<season>/<row>/export, /team/<id>/export/season?season=, /team/<id>/export/stats?season=, /stats/teams/export?season=
JSON: /api/scoreboard, /api/game/<season>/<row>, /api/team/<id>, /api/player/<uuid>, /api/standings`

// mcpBlocked are GET paths an agent can't read: the live event stream never
// ends, editor pages are forms, and switching dynasty sets a browser cookie
// (tools take a dynasty argument instead).
func mcpBlocked(p string) bool {
	return p == mcpPath || p == "/events" || p == "/write" || strings.HasPrefix(p, "/dynasty/") ||
		strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/media/") ||
		(strings.HasPrefix(p, "/story/") && strings.HasSuffix(p, "/edit"))
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// localRequest reports whether r comes from this machine and, when a
// browser sent it, from a page this machine serves (DNS rebinding guard).
func localRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil {
			return false
		}
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
		default:
			return false
		}
	}
	return true
}

// agentRequest gates the MCP endpoint: loopback only, unless Cloudflare Access
// authenticates every request (agents use an Access service token). A browser
// request must still come from one of the site's own pages.
func (s *Site) agentRequest(r *http.Request) bool {
	if !s.opt.BehindAccess {
		return localRequest(r)
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		return err == nil && u.Host == r.Host
	}
	return true
}

func (s *Site) mcp(w http.ResponseWriter, r *http.Request) {
	if !s.agentRequest(r) {
		http.Error(w, "the MCP endpoint only serves this machine", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		// No server-initiated stream: nothing is ever pushed to agents.
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body = bytes.TrimSpace(body)
	var reqs []rpcRequest
	batch := len(body) > 0 && body[0] == '['
	if batch {
		err = json.Unmarshal(body, &reqs)
	} else {
		var one rpcRequest
		err = json.Unmarshal(body, &one)
		reqs = []rpcRequest{one}
	}
	if err != nil {
		writeJSON(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error: " + err.Error()}})
		return
	}
	var out []rpcResponse
	for _, req := range reqs {
		if len(req.ID) == 0 || req.Method == "" {
			continue // a notification, or a response to us: nothing to answer
		}
		res, rerr := s.mcpCall(r, req)
		out = append(out, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr})
	}
	switch {
	case len(out) == 0:
		w.WriteHeader(http.StatusAccepted)
	case batch:
		writeJSON(w, out)
	default:
		writeJSON(w, out[0])
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Site) mcpCall(r *http.Request, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := mcpVersions[0]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "dsn", "title": "Dynasty Sports Network", "version": "1"},
			"instructions":    mcpInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params: " + err.Error()}
		}
		var args mcpArgs
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &args); err != nil {
				return nil, &rpcError{-32602, "invalid arguments: " + err.Error()}
			}
		}
		var text string
		var err error
		switch p.Name {
		case "site_overview":
			text, err = s.mcpOverview(r, args)
		case "search":
			text, err = s.mcpSearch(r, args)
		case "read_page":
			text, err = s.mcpReadPage(r, args)
		default:
			return nil, &rpcError{-32602, "unknown tool " + p.Name}
		}
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(text, false), nil
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
}

type mcpArgs struct {
	Path     string `json:"path"`
	Query    string `json:"query"`
	Dynasty  int    `json:"dynasty"`
	Find     string `json:"find"`
	Offset   int    `json:"offset"`
	MaxChars int    `json:"max_chars"`
}

var dynastyArg = map[string]any{"type": "integer", "description": "Dynasty id (see /dynasties). Defaults to the most recently played dynasty."}

var mcpTools = []map[string]any{
	{
		"name":        "site_overview",
		"title":       "Site overview",
		"description": "Start here. The dynasty being shown, the current season, stage and week, the seasons on record, every team's id and conference, the recorded dynasties, and a guide to every page read_page can open.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"dynasty": dynastyArg}},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	},
	{
		"name":        "search",
		"title":       "Search players and teams",
		"description": "Find players (current and former, all seasons) and teams by name. Returns each match's page path for read_page.",
		"inputSchema": map[string]any{
			"type":       "object",
			"properties": map[string]any{"query": map[string]any{"type": "string", "description": "Part of a name, at least 2 characters"}, "dynasty": dynastyArg},
			"required":   []string{"query"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	},
	{
		"name":  "read_page",
		"title": "Read a site page",
		"description": "Read any page of the site as Markdown, exactly what the user sees in the browser: tables, links to other pages (follow them with this tool), and the query parameters each page's filters accept. Also returns the Markdown exports and the /api JSON endpoints. Long pages come in parts; pass offset to continue, or find to keep only matching rows.\n\n" +
			pageGuide,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "Site path with any query string, e.g. /team/124/roster?season=2035 or /player/<uuid>"},
				"dynasty":   dynastyArg,
				"find":      map[string]any{"type": "string", "description": "Keep only lines and table rows containing this text (case-insensitive), plus headings and table headers. Use it on long pages, e.g. find=\"Texas State\" on /recruiting"},
				"offset":    map[string]any{"type": "integer", "description": "Character offset to continue a long page from"},
				"max_chars": map[string]any{"type": "integer", "description": "Most characters to return (default 60000)"},
			},
			"required": []string{"path"},
		},
		"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
	},
}

// mcpContext is the store context for a tool call's dynasty.
func (s *Site) mcpContext(r *http.Request, dynasty int) (*store.Ctx, error) {
	c, err := s.q.Context(r.Context(), dynasty)
	if errors.Is(err, store.ErrNoData) {
		if dynasty != 0 {
			return nil, fmt.Errorf("no dynasty %d; see /dynasties", dynasty)
		}
		return nil, errors.New("no saves have been recorded yet")
	}
	return c, err
}

func (s *Site) mcpOverview(r *http.Request, a mcpArgs) (string, error) {
	ctx := r.Context()
	c, err := s.mcpContext(r, a.Dynasty)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s dynasty (id %d)\n\nNow: %d season, %s, %s week %d. Last save %s (%d saves recorded).\n",
		c.Name, c.Dynasty, c.Year, c.Stage, c.WeekType, c.Week, c.LastUpdate.Format("2006-01-02 15:04"), c.Snapshots)
	var years []string
	for _, sn := range c.Seasons {
		years = append(years, itoa(c.YearOf(sn)))
	}
	fmt.Fprintf(&b, "Seasons on record: %s\n", strings.Join(years, ", "))
	if ds, err := s.q.Dynasties(ctx); err == nil && len(ds) > 1 {
		b.WriteString("\n## Dynasties\n\n")
		for _, d := range ds {
			fmt.Fprintf(&b, "- %d: %s (%s, %d)\n", d.ID, d.Name, d.Coach, d.Year)
		}
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", err
	}
	byConf := map[string][]*store.Team{}
	var confs []string
	for _, t := range teams {
		if _, ok := byConf[t.Conference]; !ok {
			confs = append(confs, t.Conference)
		}
		byConf[t.Conference] = append(byConf[t.Conference], t)
	}
	sort.Strings(confs)
	fmt.Fprintf(&b, "\n## Teams (%d season): id name (abbr), record\n", c.Year)
	for _, conf := range confs {
		ts := byConf[conf]
		sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
		name := conf
		if name == "" {
			name = "Independents"
		}
		fmt.Fprintf(&b, "\n%s: ", name)
		for i, t := range ts {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%d %s (%s) %d-%d", t.ID, t.Name, t.Short, t.W, t.L)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n" + pageGuide + "\n")
	return b.String(), nil
}

func (s *Site) mcpSearch(r *http.Request, a mcpArgs) (string, error) {
	term := strings.TrimSpace(a.Query)
	if len(term) < 2 {
		return "", errors.New("query needs at least 2 characters")
	}
	ctx := r.Context()
	c, err := s.mcpContext(r, a.Dynasty)
	if err != nil {
		return "", err
	}
	teams, err := s.q.Teams(ctx, c, c.Season)
	if err != nil {
		return "", err
	}
	byID := map[int]*store.Team{}
	var b strings.Builder
	lt := strings.ToLower(term)
	var hits []*store.Team
	for _, t := range teams {
		byID[t.ID] = t
		if strings.Contains(strings.ToLower(t.Long+" "+t.Name+" "+t.Nick+" "+t.Short), lt) {
			hits = append(hits, t)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Name < hits[j].Name })
	if len(hits) > 0 {
		b.WriteString("## Teams\n\n")
		for _, t := range hits {
			fmt.Fprintf(&b, "- %s %s (%s, %s): /team/%d\n", t.Name, t.Nick, t.Short, t.Conference, t.ID)
		}
	}
	people, err := s.q.SearchPeople(ctx, c, term)
	if err != nil {
		return "", err
	}
	if len(people) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("## Players (latest season on record; up to 50)\n\n")
		for _, p := range people {
			team := "not on a current roster"
			if t := byID[p.TeamID]; t != nil {
				team = t.Name
			}
			fmt.Fprintf(&b, "- %s, %s %s, %s: /player/%s\n", p.Name(), p.Position, p.ClassYear, team, p.ID)
		}
	}
	if b.Len() == 0 {
		return "No players or teams match " + term + ".", nil
	}
	return b.String(), nil
}

const mcpPageChars = 60000

func (s *Site) mcpReadPage(r *http.Request, a mcpArgs) (string, error) {
	if s.pages == nil {
		return "", errors.New("site routes are not ready")
	}
	u, err := url.Parse(strings.TrimSpace(a.Path))
	if err != nil || !strings.HasPrefix(u.Path, "/") || u.Host != "" {
		return "", fmt.Errorf("path must be a site path like /team/124, got %q", a.Path)
	}
	if mcpBlocked(u.Path) {
		return "", fmt.Errorf("%s can't be read through MCP", u.Path)
	}
	q := u.Query()
	if a.Dynasty > 0 {
		q.Set("dynasty", itoa(a.Dynasty))
	}
	u.RawQuery = q.Encode()
	req := httptest.NewRequestWithContext(r.Context(), http.MethodGet, u.RequestURI(), nil)
	req.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	s.pages.ServeHTTP(rec, req)
	res := rec.Result()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%s: page not found", u.Path)
	case res.StatusCode >= 300 && res.StatusCode < 400:
		return "", fmt.Errorf("%s redirects to %s", u.Path, res.Header.Get("Location"))
	case res.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%s: %s: %s", u.Path, res.Status, strings.TrimSpace(rec.Body.String()))
	}
	var text string
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		title, body, err := pageMarkdown(rec.Body)
		if err != nil {
			return "", err
		}
		text = "# " + title + "\n\n" + body
	} else {
		text = rec.Body.String()
	}
	if a.Find != "" {
		text = findLines(text, a.Find)
	}
	return pageSlice(text, a.Offset, a.MaxChars), nil
}

// findLines keeps the lines of a page that contain term, with the headings
// and table headers that give them context. Headings and tables left with no
// matches are dropped.
func findLines(text, term string) string {
	lt := strings.ToLower(term)
	lines := strings.Split(text, "\n")
	var out, pending []string // pending: context not yet followed by a match
	matches := 0
	section := 0 // level of a matching heading whose section is kept whole
	level := func(line string) int { return len(line) - len(strings.TrimLeft(line, "#")) }
	for i, line := range lines {
		heading := strings.HasPrefix(line, "#")
		if section > 0 && (!heading || level(line) > section) {
			out = append(out, line)
			continue
		}
		section = 0
		if heading {
			// A heading replaces any table header and any deeper or equal
			// heading still pending.
			for n := len(pending); n > 0 && (!strings.HasPrefix(pending[n-1], "#") || level(pending[n-1]) >= level(line)); n = len(pending) {
				pending = pending[:n-1]
			}
		}
		switch {
		case heading && strings.Contains(strings.ToLower(line), lt):
			// A matching heading keeps its whole section.
			out = append(out, "")
			out = append(out, pending...)
			out = append(out, line)
			pending = nil
			section = level(line)
			matches++
		case heading:
			pending = append(pending, line)
		case i+1 < len(lines) && strings.HasPrefix(lines[i+1], "| --- "):
			pending = append(pending, line) // table header
		case strings.HasPrefix(line, "| --- "):
			if n := len(pending); n > 0 && !strings.HasPrefix(pending[n-1], "#") {
				pending = append(pending, line)
			}
		case strings.Contains(strings.ToLower(line), lt):
			if len(pending) > 0 {
				out = append(out, "")
				out = append(out, pending...)
				pending = nil
			}
			out = append(out, line)
			matches++
		case strings.HasPrefix(line, "|"):
			// An unmatched row: its table's header stays pending.
		default:
			// The table ended: drop its pending header.
			for n := len(pending); n > 0 && !strings.HasPrefix(pending[n-1], "#"); n = len(pending) {
				pending = pending[:n-1]
			}
		}
	}
	if matches == 0 {
		return "No lines on this page contain \"" + term + "\"."
	}
	return fmt.Sprintf("[%d matching lines for %q]\n", matches, term) + strings.Join(out, "\n")
}

// pageSlice returns part of a long page, saying where the next part starts.
func pageSlice(text string, offset, limit int) string {
	if limit <= 0 {
		limit = mcpPageChars
	}
	offset = min(max(offset, 0), len(text))
	end := min(offset+limit, len(text))
	// Cut at a line break so table rows stay whole.
	if end < len(text) {
		if i := strings.LastIndexByte(text[offset:end], '\n'); i > 0 {
			end = offset + i + 1
		}
	}
	out := text[offset:end]
	if offset > 0 {
		out = fmt.Sprintf("[Continuing from character %d of %d]\n\n", offset, len(text)) + out
	}
	if end < len(text) {
		out += fmt.Sprintf("\n\n[Page continues: %d more characters. Call read_page again with offset=%d.]", len(text)-end, end)
	}
	return out
}
