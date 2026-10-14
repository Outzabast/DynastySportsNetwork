// Package collector watches the game's saves directory and feeds every new
// distinct save into ingest. It never opens a live save for writing: files are
// copied (read-only, shared) once they stop changing, and only the copy is
// parsed (AGENTS.md rule 2).
package collector

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/extract"
)

// Ingester receives each new save copy.
type Ingester interface {
	IngestFile(ctx context.Context, copyPath, archivePath, sha string, modTime time.Time) error
}

// Collector polls the saves directory. Polling (every Interval) is cheap,
// dependency-free, and never holds handles on the game's files.
type Collector struct {
	SavesDir string
	DataDir  string
	Interval time.Duration
	Settle   time.Duration // file must be unchanged this long before copying
	Ingest   Ingester
	Log      *slog.Logger

	mu       sync.Mutex
	seen     map[string]fileState // path -> last observed state
	done     map[string]string    // path -> sha of the last version ingested
	Status   Status
}

// Status is surfaced on the site's admin page.
type Status struct {
	LastScan   time.Time
	LastIngest time.Time
	LastFile   string
	LastError  string
	Ingested   int
	Skipped    int
}

type fileState struct {
	size  int64
	mod   time.Time
	since time.Time // when this (size, mod) was first observed
}

// IsDynastySave reports whether a file in the saves dir looks like a dynasty
// save. Roster/profile files and the game's own backup folders are skipped.
func IsDynastySave(name string) bool {
	up := strings.ToUpper(name)
	if !strings.HasPrefix(up, "DYNASTY") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == "" || ext == ".sav"
}

// Snapshot returns a copy of the status.
func (c *Collector) Snapshot() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Status
}

// Run scans until ctx is cancelled. On start every existing save is
// considered, oldest first, so a fresh server reconciles what it can.
func (c *Collector) Run(ctx context.Context) {
	if c.Interval == 0 {
		c.Interval = 3 * time.Second
	}
	if c.Settle == 0 {
		c.Settle = 4 * time.Second
	}
	c.seen = map[string]fileState{}
	c.done = map[string]string{}
	t := time.NewTicker(c.Interval)
	defer t.Stop()
	for {
		c.scan(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) scan(ctx context.Context) {
	entries, err := os.ReadDir(c.SavesDir)
	if err != nil {
		c.setErr("read saves dir: " + err.Error())
		return
	}
	type cand struct {
		path string
		info os.FileInfo
	}
	var ready []cand
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || !IsDynastySave(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(c.SavesDir, e.Name())
		st, ok := c.seen[p]
		if !ok || st.size != info.Size() || !st.mod.Equal(info.ModTime()) {
			c.seen[p] = fileState{size: info.Size(), mod: info.ModTime(), since: now}
			continue
		}
		if now.Sub(st.since) < c.Settle && now.Sub(info.ModTime()) < c.Settle {
			continue // still being written
		}
		if c.done[p] == stamp(info) {
			continue
		}
		ready = append(ready, cand{p, info})
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].info.ModTime().Before(ready[j].info.ModTime()) })
	c.mu.Lock()
	c.Status.LastScan = now
	c.mu.Unlock()
	for _, r := range ready {
		if ctx.Err() != nil {
			return
		}
		if err := c.collect(ctx, r.path, r.info); err != nil {
			c.Log.Error("collect failed", "file", r.path, "err", err)
			c.setErr(filepath.Base(r.path) + ": " + err.Error())
			continue
		}
		c.done[r.path] = stamp(r.info)
	}
}

func stamp(info os.FileInfo) string {
	return info.ModTime().UTC().Format(time.RFC3339Nano) + "/" + itoa(info.Size())
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (c *Collector) setErr(msg string) {
	c.mu.Lock()
	c.Status.LastError = msg
	c.mu.Unlock()
}

// collect copies the save, verifies it did not change during the copy,
// archives it by content hash, and hands the copy to ingest.
func (c *Collector) collect(ctx context.Context, path string, info os.FileInfo) error {
	inbox := filepath.Join(c.DataDir, "inbox")
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(inbox, filepath.Base(path)+".copy")
	if err := CopyFile(path, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	after, err := os.Stat(path)
	if err != nil {
		return err
	}
	if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		c.seen[path] = fileState{size: after.Size(), mod: after.ModTime(), since: time.Now()}
		return nil // changed mid-copy; retry next scan
	}
	sha, _, err := extract.SHA256File(tmp)
	if err != nil {
		return err
	}
	archive, err := Archive(c.DataDir, tmp, sha)
	if err != nil {
		return err
	}
	c.Log.Info("save changed", "file", filepath.Base(path), "sha", sha[:12])
	if err := c.Ingest.IngestFile(ctx, tmp, archive, sha, info.ModTime()); err != nil {
		return err
	}
	c.mu.Lock()
	c.Status.LastIngest = time.Now()
	c.Status.LastFile = filepath.Base(path)
	c.Status.LastError = ""
	c.Status.Ingested++
	c.mu.Unlock()
	return nil
}

// CopyFile copies src to dst opening src read-only.
func CopyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Archive stores a gzip of the save under data/archive/<sha>.gz (idempotent)
// and returns its path. This is the raw layer used for full re-ingest.
func Archive(dataDir, src, sha string) (string, error) {
	dir := filepath.Join(dataDir, "archive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, sha+".gz")
	if _, err := os.Stat(dst); err == nil {
		return dst, nil
	}
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		out.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	return dst, os.Rename(tmp, dst)
}

// Unarchive expands an archived save to dst.
func Unarchive(archive, dst string) error {
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
