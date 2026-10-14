package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	body := "# comment\n\nDSN_TEST_A=postgres://u:p@h:5432/db?sslmode=require\nDSN_TEST_B=\"quoted\"\nDSN_TEST_C=from-file\nnot a pair\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSN_TEST_C", "from-env")
	os.Unsetenv("DSN_TEST_A")
	os.Unsetenv("DSN_TEST_B")
	t.Cleanup(func() { os.Unsetenv("DSN_TEST_A"); os.Unsetenv("DSN_TEST_B") })

	loadDotEnv(path)

	if got := os.Getenv("DSN_TEST_A"); got != "postgres://u:p@h:5432/db?sslmode=require" {
		t.Errorf("A = %q", got)
	}
	if got := os.Getenv("DSN_TEST_B"); got != "quoted" {
		t.Errorf("B = %q", got)
	}
	if got := os.Getenv("DSN_TEST_C"); got != "from-env" {
		t.Errorf("real env should win, C = %q", got)
	}
}

func TestDBHostHidesCredentials(t *testing.T) {
	got := dbHost("postgres://user:secret@db.example.com:5432/dsn?sslmode=require")
	if got != "db.example.com:5432/dsn" {
		t.Errorf("dbHost = %q", got)
	}
}
