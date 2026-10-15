package store

import (
	"errors"
	"testing"
)

func TestMemo(t *testing.T) {
	q := NewQueries(nil)
	c := &Ctx{Branch: 1, Snapshots: 5}
	calls := 0
	fn := func() (any, error) { calls++; return calls, nil }
	q.memo(c, "k", fn)
	if v, _ := q.memo(c, "k", fn); v != 1 || calls != 1 {
		t.Fatalf("second call should hit the cache: v=%v calls=%d", v, calls)
	}
	// A new save (snapshot count changes) starts over.
	c2 := &Ctx{Branch: 1, Snapshots: 6}
	if v, _ := q.memo(c2, "k", fn); v != 2 {
		t.Fatalf("new data version should recompute: v=%v", v)
	}
	// Failures are not kept.
	boom := errors.New("timeout")
	if _, err := q.memo(c2, "e", func() (any, error) { return nil, boom }); err != boom {
		t.Fatalf("want error, got %v", err)
	}
	if v, err := q.memo(c2, "e", fn); err != nil || v != 3 {
		t.Fatalf("failed entry should be retried: v=%v err=%v", v, err)
	}
	if _, ok := q.peek(c2, "e"); !ok {
		t.Fatal("peek should see the finished entry")
	}
	q.Invalidate()
	if _, ok := q.peek(c2, "e"); ok {
		t.Fatal("Invalidate should clear the cache")
	}
}
