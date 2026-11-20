package web

import (
	"encoding/json"
	"strings"
	"testing"
)

// A bio renders like an article: Markdown in, raw HTML dropped.
func TestBioHTML(t *testing.T) {
	if got := bioHTML(" \n "); got != "" {
		t.Errorf("blank bio rendered %q", got)
	}
	got := string(bioHTML("**Captain**\n\n<script>alert(1)</script>"))
	if !strings.Contains(got, "<strong>Captain</strong>") || strings.Contains(got, "<script") {
		t.Errorf("bio HTML = %q", got)
	}
}

// The bio tools need exactly one of player or coach.
func TestMCPBioNeedsOneSubject(t *testing.T) {
	s := &Site{}
	for _, args := range []string{`{"body":"x"}`, `{"player":"p","coach":"c","body":"x"}`} {
		for _, tool := range []string{"get_bio", "save_bio"} {
			rec := mcpPost(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+args+`}}`, "")
			var res struct {
				Result struct {
					Content []struct{ Text string } `json:"content"`
					IsError bool                    `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || len(res.Result.Content) != 1 {
				t.Fatalf("%s %s: %s", tool, args, rec.Body)
			}
			if !res.Result.IsError || !strings.Contains(res.Result.Content[0].Text, "player or coach") {
				t.Errorf("%s %s: %+v", tool, args, res.Result)
			}
		}
	}
}
