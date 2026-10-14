package main

import (
	"flag"
	"os"
	"path/filepath"
)

type config struct {
	DB        string
	SavesDir  string
	DataDir   string
	SchemaDir string
	MediaDir  string
	Addr      string
	NoWatch   bool
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// defaultSavesDir finds the CFB 27 saves folder; EA has used both spellings.
func defaultSavesDir() string {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, "Documents", "EA SPORTS College Football 27", "Saves"),
		filepath.Join(home, "Documents", "EA SPORTS CFB27", "saves"),
	} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			return p
		}
	}
	return filepath.Join(home, "Documents", "EA SPORTS College Football 27", "Saves")
}

func (c *config) bind(fs *flag.FlagSet) {
	fs.StringVar(&c.DB, "db", env("DSN_DB", "postgres://dsn:dsn@localhost:5433/dsn?sslmode=disable"), "Postgres URL (DSN_DB)")
	fs.StringVar(&c.SavesDir, "saves", env("DSN_SAVES", defaultSavesDir()), "game saves directory to watch (DSN_SAVES)")
	fs.StringVar(&c.DataDir, "data", env("DSN_DATA", "data"), "local data dir for the raw archive (DSN_DATA)")
	fs.StringVar(&c.SchemaDir, "schema-dir", env("DSN_SCHEMAS", "data/schemas"), "parser schema bundle dir (DSN_SCHEMAS)")
	fs.StringVar(&c.MediaDir, "media", env("DSN_MEDIA", "media"), "game media folder served at /media (DSN_MEDIA)")
	fs.StringVar(&c.Addr, "addr", env("DSN_ADDR", ":8080"), "HTTP listen address (DSN_ADDR)")
	fs.BoolVar(&c.NoWatch, "no-watch", false, "serve only; do not watch the saves directory")
}
