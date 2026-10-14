package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

func (s *Site) funcs() template.FuncMap {
	m := s.media
	return template.FuncMap{
		"inc": func(i int) int { return i + 1 },
		// dict builds a map for passing several values to a sub-template.
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i].(string)] = kv[i+1]
			}
			return m
		},
		"logo": func(t *store.Team) string {
			if t == nil || t.ID < 0 {
				return ""
			}
			return m.TeamLogo(t.Name, t.Long)
		},
		"helmet": func(t *store.Team) string {
			if t == nil || t.ID < 0 {
				return ""
			}
			return m.TeamHelmet(t.Name, t.Long)
		},
		"teamURL": func(t *store.Team) string {
			if t == nil || t.ID < 0 {
				return ""
			}
			return fmt.Sprintf("/team/%d", t.ID)
		},
		"team": func(teams map[int]*store.Team, id int) *store.Team { return teamOr(teams, id) },
		"portrait":      m.Portrait,
		"coachPortrait": m.CoachPortrait,
		"coachURL":      coachURL,
		"storyURL":      storyURL,
		"timeAgo":       timeAgo,
		"posLabel":      positionLabel,
		"confLogo":      m.ConfLogo,
		"bowlLogo":      m.BowlLogo,
		"trophy":        m.Trophy,
		"awardName":     AwardName,
		"weekLabel":     weekLabel,
		"weekShort":     weekShort,
		"status":        gameStatus,
		"dateLabel":     dateLabel,
		"height":        height,
		"classYr":       classYear,
		"stars":         stars,
		"streak":        streak,
		"pretty":        prettyEnum,
		"position":      positionName,
		"add":           func(a, b int) int { return a + b },
		"sub":           func(a, b int) int { return a - b },
		"year":          func(c *store.Ctx, season int) int { return c.YearOf(season) },
		"pct":           func(a, b int) string { return pct(int64(a), int64(b)) },
		"color":         safeColor,
		"css":           func(s string) template.CSS { return template.CSS(s) },
		"winner": func(g *store.Game, home bool) bool {
			if !g.Played {
				return false
			}
			if home {
				return g.HomeScore > g.AwayScore
			}
			return g.AwayScore > g.HomeScore
		},
		"q": func(qs []int32, i int) string {
			if i < len(qs) {
				return fmt.Sprint(qs[i])
			}
			return "-"
		},
		"result": func(g *store.Game, team int) string {
			if !g.Played {
				return ""
			}
			us, them := g.HomeScore, g.AwayScore
			if g.Away == team {
				us, them = them, us
			}
			wl := "W"
			if us < them {
				wl = "L"
			} else if us == them {
				wl = "T"
			}
			ot := ""
			if g.Overtime {
				ot = " OT"
			}
			return fmt.Sprintf("%s %d-%d%s", wl, us, them, ot)
		},
		"isWin": func(g *store.Game, team int) bool {
			if g.Home == team {
				return g.HomeScore > g.AwayScore
			}
			return g.AwayScore > g.HomeScore
		},
		"rankOf": func(t *store.Team) int {
			if t == nil {
				return 0
			}
			return t.Rank()
		},
		"leave": func(s string) string { return prettyEnum(strings.TrimPrefix(s, "Transfer_")) },
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"slice": func(xs []int, i int) int { return xs[i] },
		"mod":   func(a, b int) int { return a % b },
		"ordinal":      ordinal,
		"assetVersion": func() string { return assetVersion },
		"until": func(s string, n int) string {
			if len(s) > n {
				return s[:n]
			}
			return s
		},
	}
}

func (s *Site) loadTemplates() error {
	s.tmpl = map[string]*template.Template{}
	base, err := fs.ReadFile(assets, "templates/layout.html")
	if err != nil {
		return err
	}
	parts, err := fs.ReadFile(assets, "templates/partials.html")
	if err != nil {
		return err
	}
	files, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return err
	}
	for _, f := range files {
		name := strings.TrimSuffix(path.Base(f), ".html")
		if name == "layout" || name == "partials" {
			continue
		}
		body, err := fs.ReadFile(assets, f)
		if err != nil {
			return err
		}
		t := template.New(name).Funcs(s.funcs())
		for _, src := range [][]byte{base, parts, body} {
			if _, err := t.Parse(string(src)); err != nil {
				return fmt.Errorf("template %s: %w", f, err)
			}
		}
		s.tmpl[name] = t
	}
	return nil
}

// assetVersion busts browser caches for static files on each server start.
var assetVersion = fmt.Sprint(time.Now().Unix())

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func safeColor(c string) template.CSS {
	if colorRe.MatchString(c) {
		return template.CSS(c)
	}
	return template.CSS("#444444")
}

func weekLabel(wt string, w int) string {
	switch wt {
	case "RegularSeason":
		if w == 0 {
			return "Week 0"
		}
		return fmt.Sprintf("Week %d", w)
	case "BowlSeason1":
		return "Bowls & Playoff Round 1"
	case "BowlSeason2":
		return "Postseason Week 2"
	case "BowlSeason3":
		return "Postseason Week 3"
	case "BowlSeason4":
		return "Postseason Week 4"
	case "PreSeason":
		return "Preseason"
	}
	return fmt.Sprintf("%s %d", prettyEnum(wt), w)
}

func weekShort(wt string, w int) string {
	if wt == "RegularSeason" {
		return fmt.Sprintf("Week %d", w)
	}
	if strings.HasPrefix(wt, "BowlSeason") {
		return "Bowls " + strings.TrimPrefix(wt, "BowlSeason")
	}
	return prettyEnum(wt)
}

var months = []string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

func monthName(m int) string {
	if m >= 1 && m <= 12 {
		return months[m]
	}
	return ""
}

func dateLabel(g *store.Game, year int) string {
	y := year
	if g.Month >= 1 && g.Month <= 6 {
		y++
	}
	return fmt.Sprintf("%s, %s %d, %d", g.DayOfWeek, monthName(g.Month), g.Day, y)
}

func kickoff(min int) string {
	if min <= 0 {
		return "TBD"
	}
	h, m := min/60, min%60
	ap := "AM"
	if h >= 12 {
		ap = "PM"
	}
	if h > 12 {
		h -= 12
	}
	if h == 0 {
		h = 12
	}
	return fmt.Sprintf("%d:%02d %s", h, m, ap)
}

func gameStatus(g *store.Game) string {
	if g.Played {
		if g.Overtime {
			return "FINAL/OT"
		}
		return "FINAL"
	}
	return kickoff(g.TimeOfDay)
}

func height(in int) string {
	if in <= 0 {
		return "--"
	}
	return fmt.Sprintf("%d' %d\"", in/12, in%12)
}

func classYear(year, redshirt string) string {
	abbr := map[string]string{"Freshman": "FR", "Sophomore": "SO", "Junior": "JR", "Senior": "SR", "Graduate": "GR"}[year]
	if abbr == "" {
		abbr = year
	}
	if strings.Contains(strings.ToLower(redshirt), "redshirted") || strings.EqualFold(redshirt, "Redshirt") {
		abbr = "RS " + abbr
	}
	return abbr
}

func stars(n int) string {
	if n <= 0 {
		return ""
	}
	if n > 5 {
		n = 5
	}
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

func streak(n int) string {
	switch {
	case n > 0:
		return fmt.Sprintf("W%d", n)
	case n < 0:
		return fmt.Sprintf("L%d", -n)
	}
	return "-"
}

var camel = regexp.MustCompile(`([a-z])([A-Z])`)

func prettyEnum(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	return camel.ReplaceAllString(s, "$1 $2")
}

var positionNames = map[string]string{
	"QB": "Quarterback", "HB": "Running Back", "FB": "Fullback", "WR": "Wide Receiver", "TE": "Tight End", "LT": "Left Tackle", "LG": "Left Guard",
	"C": "Center", "RG": "Right Guard", "RT": "Right Tackle", "LE": "Defensive End", "RE": "Defensive End", "DT": "Defensive Tackle",
	"LOLB": "Linebacker", "MLB": "Linebacker", "ROLB": "Linebacker", "CB": "Cornerback", "FS": "Safety", "SS": "Safety", "K": "Place Kicker", "P": "Punter",
}

func positionName(p string) string {
	if n, ok := positionNames[p]; ok {
		return n
	}
	return p
}

func ordinal(n int) string {
	suf := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suf = "st"
		case 2:
			suf = "nd"
		case 3:
			suf = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suf)
}
