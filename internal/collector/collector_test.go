package collector

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeIngest struct {
	mu   sync.Mutex
	shas []string
}

func (f *fakeIngest) IngestFile(ctx context.Context, copyPath, archivePath, sha string, mod time.Time) error {
	if _, err := os.Stat(archivePath); err != nil {
		return err
	}
	f.mu.Lock()
	f.shas = append(f.shas, sha)
	f.mu.Unlock()
	return nil
}

func (f *fakeIngest) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.shas)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestCollectorIngestsNewAndChangedSaves(t *testing.T) {
	saves, data := t.TempDir(), t.TempDir()
	fi := &fakeIngest{}
	c := &Collector{SavesDir: saves, DataDir: data, Interval: 30 * time.Millisecond, Settle: 60 * time.Millisecond,
		Ingest: fi, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	save := filepath.Join(saves, "DYNASTY-TEST-AUTOSAVE")
	os.WriteFile(save, []byte("week 1"), 0o644)
	os.WriteFile(filepath.Join(saves, "ROSTER-Official"), []byte("not a dynasty"), 0o644)
	waitFor(t, func() bool { return fi.count() == 1 })

	// Unchanged file: no re-ingest.
	time.Sleep(300 * time.Millisecond)
	if n := fi.count(); n != 1 {
		t.Fatalf("unchanged save re-ingested: %d", n)
	}

	// The game autosaves at the next week advance.
	later := time.Now().Add(time.Second)
	os.WriteFile(save, []byte("week 2"), 0o644)
	os.Chtimes(save, later, later)
	waitFor(t, func() bool { return fi.count() == 2 })
	if fi.shas[0] == fi.shas[1] {
		t.Fatal("expected a different content hash after the change")
	}
	if _, err := os.Stat(filepath.Join(data, "archive", fi.shas[1]+".gz")); err != nil {
		t.Fatalf("archive missing: %v", err)
	}
	// The live save must never be modified.
	if b, _ := os.ReadFile(save); string(b) != "week 2" {
		t.Fatal("live save was modified")
	}
}

func TestIsDynastySave(t *testing.T) {
	for name, want := range map[string]bool{
		"DYNASTY-SEP19-10h09m37-AUTOSAVE":        true,
		"DYNASTY-SEP19-10h09m37-AUTOSAVE - Copy": true,
		"DYNASTY-PURDUE.sav":                     true,
		"ROSTER-Official":                        false,
		"PROFILE-COLLEGE":                        false,
		"DYNASTY-X.bak":                          false,
	} {
		if got := IsDynastySave(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}
