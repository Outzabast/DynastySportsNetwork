package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testPage = `<!doctype html><html><head><title>Roster - DSN</title></head><body>
<header><nav><a href="/">Home</a></nav></header>
<main>
<h1>Texas State <span>Bobcats</span></h1>
<form method="get"><select name="season"><option value="2034">2034</option><option value="2035" selected>2035</option></select>
<input type="hidden" name="week" value="3"><select id="rf-pos"><option>QB</option></select></form>
<dl><div><dt>Class</dt><dd>RS SR</dd></div></dl>
<table><thead><tr><th>Name</th><th>Pos</th></tr></thead>
<tbody><tr><td><a href="/player/abc">Deejay Dallas</a><span>#13</span></td><td>QB</td></tr>
<tr><td colspan="2">A | B</td></tr></tbody></table>
<p id="x" hidden>secret</p><script>var x = 1;</script>
<div class="t">Selected in round 7</div><div class="r">2036</div>
</main><footer>footer</footer></body></html>`

func TestPageMarkdown(t *testing.T) {
	title, md, err := pageMarkdown(strings.NewReader(testPage))
	if err != nil {
		t.Fatal(err)
	}
	if title != "Roster - DSN" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{
		"# Texas State Bobcats",
		"- season=2035 — options: 2034, 2035",
		"- week=3",
		"Class: RS SR",
		"| Name | Pos |\n| --- | --- |\n| [Deejay Dallas](/player/abc) #13 | QB |\n| A \\| B |  |",
		"Selected in round 7\n\n2036",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	for _, bad := range []string{"Home", "footer", "secret", "var x", "rf-pos", "QB</option>"} {
		if strings.Contains(md, bad) {
			t.Errorf("%q should not be in:\n%s", bad, md)
		}
	}
}

func TestFindLines(t *testing.T) {
	page := "# Recruiting\n\n## Top Prospects\n\n| Name | Status |\n| --- | --- |\n| A | Texas State |\n| B | Rice |\n| C | Texas State |\n\n## Classes\n\n| Team | Pts |\n| --- | --- |\n| Rice | 9 |"
	got := findLines(page, "texas state")
	want := "[2 matching lines for \"texas state\"]\n\n# Recruiting\n## Top Prospects\n| Name | Status |\n| --- | --- |\n| A | Texas State |\n| C | Texas State |"
	if got != want {
		t.Errorf("findLines =\n%s\nwant\n%s", got, want)
	}
	// A matching heading keeps its whole section.
	got = findLines(page, "classes")
	want = "[1 matching lines for \"classes\"]\n\n# Recruiting\n## Classes\n\n| Team | Pts |\n| --- | --- |\n| Rice | 9 |"
	if got != want {
		t.Errorf("findLines section =\n%s\nwant\n%s", got, want)
	}
}

func TestPageSlice(t *testing.T) {
	text := strings.Repeat("row\n", 10) // 40 characters
	first := pageSlice(text, 0, 10)
	if !strings.HasPrefix(first, "row\nrow\n") || !strings.Contains(first, "offset=8") {
		t.Errorf("first part = %q", first)
	}
	if rest := pageSlice(text, 8, 100); strings.Contains(rest, "Page continues") || !strings.Contains(rest, "character 8 of 40") {
		t.Errorf("rest = %q", rest)
	}
}

func mcpPost(t *testing.T, s *Site, body string, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5000"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	s.mcp(rec, req)
	return rec
}

func TestMCPProtocol(t *testing.T) {
	pages := http.NewServeMux()
	pages.HandleFunc("GET /team/124/roster", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(testPage))
	})
	s := &Site{pages: pages}

	rec := mcpPost(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`, "")
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	json.Unmarshal(rec.Body.Bytes(), &init)
	if init.Result.ProtocolVersion != "2025-03-26" {
		t.Errorf("initialize: %s", rec.Body)
	}
	if rec := mcpPost(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, ""); rec.Code != http.StatusAccepted {
		t.Errorf("notification: %d", rec.Code)
	}
	if rec := mcpPost(t, s, `{}`, "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", rec.Code)
	}

	call := func(args string) (string, bool) {
		rec := mcpPost(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_page","arguments":`+args+`}}`, "http://localhost:8080")
		var res struct {
			Result struct {
				Content []struct{ Text string } `json:"content"`
				IsError bool                    `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.Result.Content) != 1 {
			t.Fatalf("tools/call: %s", rec.Body)
		}
		return res.Result.Content[0].Text, res.Result.IsError
	}
	if text, isErr := call(`{"path":"/team/124/roster?season=2035"}`); isErr || !strings.HasPrefix(text, "# Roster - DSN\n\n# Texas State Bobcats") {
		t.Errorf("read_page: %v %q", isErr, text)
	}
	if text, isErr := call(`{"path":"/events"}`); !isErr || !strings.Contains(text, "can't be read") {
		t.Errorf("blocked path: %v %q", isErr, text)
	}
	if text, isErr := call(`{"path":"/team/1/nope"}`); !isErr || !strings.Contains(text, "not found") {
		t.Errorf("missing page: %v %q", isErr, text)
	}
}

func TestMCPLoopbackOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, mcpPath, strings.NewReader(`{}`))
	req.RemoteAddr = "192.168.1.20:5000"
	rec := httptest.NewRecorder()
	(&Site{}).mcp(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("LAN client got %d, want 403", rec.Code)
	}
}
