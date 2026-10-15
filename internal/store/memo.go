package store

import (
	"fmt"
	"sync"
)

// Read results only change when a save is ingested, so the queries behind
// every page view (ticker weeks, schedules, predictor results) are kept
// across requests. Entries are keyed by branch and snapshot count: Context
// refreshes every 30s, so an ingest from another machine (the collector)
// clears them without a restart. Invalidate clears them after a local ingest.

type memoEntry struct {
	done chan struct{} // closed once val/err are set
	val  any
	err  error
}

type memoCache struct {
	mu      sync.Mutex
	version [2]int // branch, snapshots
	entries map[string]*memoEntry
}

func (m *memoCache) entry(c *Ctx, key string) (e *memoEntry, owner bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v := [2]int{c.Branch, c.Snapshots}; v != m.version || m.entries == nil {
		m.version, m.entries = v, map[string]*memoEntry{}
	}
	if e, ok := m.entries[key]; ok {
		return e, false
	}
	e = &memoEntry{done: make(chan struct{})}
	m.entries[key] = e
	return e, true
}

func (m *memoCache) drop(key string, e *memoEntry) {
	m.mu.Lock()
	if m.entries[key] == e {
		delete(m.entries, key)
	}
	m.mu.Unlock()
}

// memo returns fn's result for key, computing it once per data version;
// concurrent callers wait for the first. Callers must treat the value as
// shared and hand out copies of slices and maps.
func (q *Queries) memo(c *Ctx, key string, fn func() (any, error)) (any, error) {
	e, owner := q.memo_.entry(c, key)
	if owner {
		e.val, e.err = fn()
		if e.err != nil {
			// Don't keep failures: a timeout must not stick until the next save.
			q.memo_.drop(key, e)
		}
		close(e.done)
	}
	<-e.done
	return e.val, e.err
}

// peek returns a finished cached value without computing it.
func (q *Queries) peek(c *Ctx, key string) (any, bool) {
	m := &q.memo_
	m.mu.Lock()
	var e *memoEntry
	if m.version == [2]int{c.Branch, c.Snapshots} && m.entries != nil {
		e = m.entries[key]
	}
	m.mu.Unlock()
	if e == nil {
		return nil, false
	}
	select {
	case <-e.done:
		return e.val, e.err == nil
	default:
		return nil, false
	}
}

func (q *Queries) clearMemo() {
	q.memo_.mu.Lock()
	q.memo_.entries = nil
	q.memo_.mu.Unlock()
}

func memoKey(name string, args ...any) string { return name + fmt.Sprint(args...) }
