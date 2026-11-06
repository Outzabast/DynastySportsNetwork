package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A failed connect must not echo the password or user from the URL.
func TestOpenErrorHidesCredentials(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Open(ctx, "postgres://keyid123:sk_supersecret@127.0.0.1:1/postgres?sslmode=disable&connect_timeout=2")
	if err == nil {
		t.Fatal("expected connect error")
	}
	msg := err.Error()
	for _, leak := range []string{"sk_supersecret", "keyid123"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error leaks %q: %s", leak, msg)
		}
	}
	if !strings.Contains(msg, "127.0.0.1:1/postgres") {
		t.Errorf("error should name the host: %s", msg)
	}
	t.Log(msg)
}
