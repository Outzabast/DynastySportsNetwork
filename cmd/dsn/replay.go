package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/extract"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

// archivedSave is one file in data/archive. Without the old database, the
// file's modified time stands in for the save's: the collector writes each
// archive once, when it first ingests that save, so file times follow the
// original ingest order.
type archivedSave struct {
	path string
	sha  string
	mod  time.Time
}

// listArchive returns every archived save, oldest first.
func listArchive(dataDir string) ([]archivedSave, error) {
	files, err := filepath.Glob(filepath.Join(dataDir, "archive", "*.gz"))
	if err != nil {
		return nil, err
	}
	var out []archivedSave
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		out = append(out, archivedSave{path: f, sha: strings.TrimSuffix(filepath.Base(f), ".gz"), mod: info.ModTime()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].mod.Before(out[j].mod) })
	return out, nil
}

// planRow is the dry-run verdict for one archived save.
type planRow struct {
	save     archivedSave
	snap     *extract.Snapshot
	pos      int64
	verdict  string // forward | same-week | BACKWARD | CONTRADICTS | new-dynasty
	detail   string
	extracts time.Duration
}

// leagueLine tracks the newest save seen per dynasty while planning.
type leagueLine struct {
	pos    int64
	season int
	games  map[int]gameResult
}

type gameResult struct {
	home, away, hs, as int
	played            bool
}

// planReplay extracts every archived save (nothing is written to any
// database) and reports how ingest would treat it in file-time order.
func planReplay(ctx context.Context, c config, saves []archivedSave, w io.Writer) ([]planRow, error) {
	tmpDir, err := os.MkdirTemp("", "dsn-plan-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	lines := map[int64]*leagueLine{}
	var rows []planRow
	for i, s := range saves {
		if err := ctx.Err(); err != nil {
			return rows, err
		}
		tmp := filepath.Join(tmpDir, s.sha)
		if err := collector.Unarchive(s.path, tmp); err != nil {
			return rows, fmt.Errorf("%s: %w", s.sha[:12], err)
		}
		start := time.Now()
		snap, err := extract.Extract(tmp, c.SchemaDir)
		os.Remove(tmp)
		if err != nil {
			return rows, fmt.Errorf("extract %s: %w", s.sha[:12], err)
		}
		pos, _ := store.Position(snap.Season)
		r := planRow{save: s, snap: snap, pos: pos, extracts: time.Since(start)}
		games := map[int]gameResult{}
		for _, g := range snap.Games {
			games[g.Row] = gameResult{g.HomeTeamID, g.AwayTeamID, g.HomeScore, g.AwayScore, g.Played}
		}
		ln := lines[int64(snap.LeagueID)]
		switch {
		case ln == nil:
			r.verdict = "new-dynasty"
		default:
			var bad []string
			if ln.season == snap.Season.SeasonIndex {
				for row, prev := range ln.games {
					cur, ok := games[row]
					if !ok || !prev.played {
						continue
					}
					if !cur.played {
						bad = append(bad, fmt.Sprintf("game %d unplayed (was %d-%d)", row, prev.as, prev.hs))
					} else if cur != prev {
						bad = append(bad, fmt.Sprintf("game %d %d-%d -> %d-%d", row, prev.as, prev.hs, cur.as, cur.hs))
					}
				}
			}
			switch {
			case len(bad) > 0:
				sort.Strings(bad)
				r.verdict = "CONTRADICTS"
				r.detail = fmt.Sprintf("%d game(s): %s", len(bad), strings.Join(bad[:min(3, len(bad))], "; "))
			case pos < ln.pos:
				r.verdict = "BACKWARD"
				r.detail = fmt.Sprintf("pos %d < previous %d", pos, ln.pos)
			case pos == ln.pos:
				r.verdict = "same-week"
			default:
				r.verdict = "forward"
			}
		}
		if ln == nil || pos >= ln.pos {
			lines[int64(snap.LeagueID)] = &leagueLine{pos: pos, season: snap.Season.SeasonIndex, games: games}
		}
		rows = append(rows, r)
		fmt.Fprintf(os.Stderr, "planned %d/%d %s\n", i+1, len(saves), s.sha[:12])
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tarchived\tsha\tleague\tyear\tstage\twk\tpos\tplayed\tverdict\tdetail")
	for i, r := range rows {
		played := 0
		for _, g := range r.snap.Games {
			if g.Played {
				played++
			}
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%d\t%s\t%d\t%d\t%d\t%s\t%s\n", i+1, r.save.mod.Format("2006-01-02 15:04:05"), r.save.sha[:12],
			r.snap.LeagueID, r.snap.Season.Year, r.snap.Season.Stage, r.snap.Season.Week, r.pos, played, r.verdict, r.detail)
	}
	return rows, tw.Flush()
}

// replayArchive ingests every archived save, oldest first, into the pipeline's
// (empty) database through the normal ingest path.
func replayArchive(ctx context.Context, p *pipeline, c config, saves []archivedSave) error {
	for i, s := range saves {
		tmp := filepath.Join(c.DataDir, "inbox", "archive-"+s.sha[:12]+".copy")
		if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
			return err
		}
		if err := collector.Unarchive(s.path, tmp); err != nil {
			return err
		}
		p.log.Info("replay", "n", i+1, "of", len(saves), "sha", s.sha[:12], "archived", s.mod.Format(time.DateTime))
		err := p.IngestFile(ctx, tmp, s.path, s.sha, s.mod)
		os.Remove(tmp)
		if err != nil {
			return fmt.Errorf("replay %s: %w", s.sha[:12], err)
		}
	}
	return nil
}
