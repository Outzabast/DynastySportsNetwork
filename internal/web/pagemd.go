package web

import (
	"io"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// pageMarkdown turns a rendered site page into Markdown for AI agents: the
// page title and its <main> content (no nav, ticker or footer), with tables
// as Markdown tables, links kept so an agent can follow them, and each
// filter form listed as the query parameters it submits.
func pageMarkdown(r io.Reader) (title, body string, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	var main *html.Node
	walk(doc, func(n *html.Node) bool {
		switch n.DataAtom {
		case atom.Title:
			title = strings.TrimSpace(textOf(n))
		case atom.Main:
			main = n
			return false
		}
		return true
	})
	if main == nil {
		main = doc
	}
	m := &mdw{}
	m.children(main)
	return title, tidyMarkdown(m.b.String()), nil
}

// walk visits n and its descendants in document order; f returns false to
// skip a node's children.
func walk(n *html.Node, f func(*html.Node) bool) {
	if !f(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, f)
	}
}

func textOf(n *html.Node) string {
	var b strings.Builder
	walk(n, func(n *html.Node) bool {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		return true
	})
	return b.String()
}

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

var spaces = regexp.MustCompile(`\s+`)

type mdw struct {
	b    strings.Builder
	list int // list nesting depth
}

func (m *mdw) block() { m.b.WriteString("\n\n") }

func (m *mdw) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		m.node(c)
	}
}

// inline renders n's content on one line.
func (m *mdw) inline(n *html.Node) string {
	sub := &mdw{list: m.list}
	sub.children(n)
	return strings.TrimSpace(spaces.ReplaceAllString(sub.b.String(), " "))
}

func (m *mdw) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		m.b.WriteString(spaces.ReplaceAllString(n.Data, " "))
		return
	case html.ElementNode:
	default:
		return
	}
	if _, hidden := attr(n, "hidden"); hidden {
		return
	}
	// Pages space side-by-side elements with CSS; keep their words apart.
	if n.PrevSibling != nil {
		if s := m.b.String(); s != "" && !strings.ContainsAny(s[len(s)-1:], " \n([") {
			m.b.WriteString(" ")
		}
	}
	switch n.DataAtom {
	case atom.Script, atom.Style, atom.Svg, atom.Noscript, atom.Template, atom.Img, atom.Button, atom.Canvas, atom.Input, atom.Select, atom.Textarea:
		return
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if t := m.inline(n); t != "" {
			m.block()
			m.b.WriteString(strings.Repeat("#", int(n.Data[1]-'0')) + " " + t)
			m.block()
		}
	case atom.Br:
		m.b.WriteString("\n")
	case atom.A:
		t := m.inline(n)
		href, _ := attr(n, "href")
		switch {
		case t == "":
		case strings.HasPrefix(href, "/") && !strings.Contains(href, "format=xlsx"):
			m.b.WriteString("[" + t + "](" + href + ")")
		default:
			m.b.WriteString(t)
		}
	case atom.Li:
		m.b.WriteString("\n" + strings.Repeat("  ", max(m.list-1, 0)) + "- " + m.inline(n))
	case atom.Ul, atom.Ol:
		m.list++
		m.block()
		m.children(n)
		m.block()
		m.list--
	case atom.Table:
		m.table(n)
	case atom.Form:
		m.form(n)
	case atom.Summary:
		if t := m.inline(n); t != "" {
			m.block()
			m.b.WriteString("**" + t + "**")
			m.block()
		}
	case atom.Dt:
		m.b.WriteString("\n" + m.inline(n) + ":")
	case atom.Dd:
		m.b.WriteString(m.inline(n) + "\n")
	case atom.Td, atom.Th:
		// A cell outside a table row: keep its text apart from its neighbors.
		m.b.WriteString(" " + m.inline(n) + " ")
	case atom.Span, atom.B, atom.Strong, atom.Em, atom.I, atom.Small, atom.Abbr, atom.Code, atom.Time, atom.Label, atom.Sup, atom.Sub, atom.Mark:
		m.children(n)
	default: // div, p, section, header, details, dl, ...: separate lines
		m.b.WriteString("\n")
		m.children(n)
		m.b.WriteString("\n")
	}
}

func (m *mdw) table(n *html.Node) {
	var caption string
	var rows [][]string
	header := false
	walk(n, func(c *html.Node) bool {
		if c == n {
			return true
		}
		switch c.DataAtom {
		case atom.Caption:
			caption = m.inline(c)
			return false
		case atom.Table: // nested tables flatten into their cell
			return false
		case atom.Tr:
			var row []string
			allTh := true
			for td := c.FirstChild; td != nil; td = td.NextSibling {
				if td.DataAtom != atom.Td && td.DataAtom != atom.Th {
					continue
				}
				allTh = allTh && td.DataAtom == atom.Th
				row = append(row, strings.ReplaceAll(m.inline(td), "|", `\|`))
				for span := atoi(attrOr(td, "colspan"), 1); span > 1; span-- {
					row = append(row, "")
				}
			}
			if len(row) > 0 {
				if len(rows) == 0 {
					header = allTh
				}
				rows = append(rows, row)
			}
			return false
		}
		return true
	})
	if len(rows) == 0 {
		return
	}
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	if !header {
		rows = append([][]string{make([]string, width)}, rows...)
	}
	m.block()
	if caption != "" {
		m.b.WriteString(caption + "\n\n")
	}
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		m.b.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			m.b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	m.block()
}

func attrOr(n *html.Node, key string) string {
	v, _ := attr(n, key)
	return v
}

// form lists the query parameters a GET filter form submits, with each
// select's choices, so an agent can request other seasons, weeks, etc.
// Fields without a name are browser-side filters and are left out.
func (m *mdw) form(n *html.Node) {
	var fields []string
	walk(n, func(c *html.Node) bool {
		name, named := attr(c, "name")
		switch c.DataAtom {
		case atom.Select:
			if !named {
				return false
			}
			var opts []string
			cur := ""
			walk(c, func(o *html.Node) bool {
				if o.DataAtom != atom.Option {
					return true
				}
				label := strings.TrimSpace(textOf(o))
				val, hasVal := attr(o, "value")
				if !hasVal {
					val = label
				}
				opt := val
				if val != label {
					opt = val + " (" + label + ")"
				}
				if val == "" {
					opt = `"" (` + label + ")"
				}
				if _, sel := attr(o, "selected"); sel {
					cur = val
				}
				opts = append(opts, opt)
				return false
			})
			fields = append(fields, name+"="+cur+" — options: "+strings.Join(opts, ", "))
			return false
		case atom.Input:
			if named {
				fields = append(fields, name+"="+attrOr(c, "value"))
			}
		}
		return true
	})
	if len(fields) > 0 {
		action := attrOr(n, "action")
		if action == "" {
			action = "this page"
		}
		m.block()
		m.b.WriteString("Query parameters (" + action + "):")
		for _, f := range fields {
			m.b.WriteString("\n- " + f)
		}
		m.block()
	}
	// Anything else inside the form (labels, links) still shows.
	walk(n, func(c *html.Node) bool {
		if c == n {
			return true
		}
		if c.DataAtom == atom.Select || c.DataAtom == atom.Input || c.Type == html.TextNode {
			return false
		}
		if c.DataAtom == atom.A || c.DataAtom == atom.Table {
			m.node(c)
			return false
		}
		return true
	})
}

// tidyMarkdown trims lines and collapses runs of blank lines.
func tidyMarkdown(s string) string {
	var out []string
	blank := true
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		if !strings.HasPrefix(strings.TrimLeft(line, " "), "- ") {
			line = strings.TrimLeft(line, " \t")
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
