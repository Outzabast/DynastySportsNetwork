// Package web serves the ESPN-style site and its JSON API from the store's
// active branch. Only real-world fields are exposed (AGENTS.md rule 1).
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/outzabast/dynastysportsnetwork/internal/collector"
	"github.com/outzabast/dynastysportsnetwork/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Options configures the site.
type Options struct {
	Store    *store.Store
	MediaDir string
	SavesDir string
	Log      *slog.Logger
	Status   func() collector.Status
}

// Site is the HTTP application.
type Site struct {
	opt   Options
	q     *store.Queries
	tmpl  map[string]*template.Template
	media *mediaIndex

	mu      sync.Mutex
	clients map[chan string]struct{}
}

// New builds the site.
func New(opt Options) (*Site, error) {
	s := &Site{opt: opt, q: store.NewQueries(opt.Store), clients: map[chan string]struct{}{}}
	s.media = newMediaIndex(opt.MediaDir)
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// Notify pushes a live-update event to connected browsers.
func (s *Site) Notify(res *store.IngestResult) {
	s.q.Invalidate()
	msg := fmt.Sprintf(`{"snapshot":%d,"action":%q}`, res.SnapshotID, res.Action)
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.clients {
		select {
		case c <- msg:
		default:
		}
	}
}

func (s *Site) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := make(chan string, 4)
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case m := <-c:
			fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", m)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// ListenAndServe runs until ctx is cancelled.
func (s *Site) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sh)
	}()
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Site) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(http.FileServer(http.FS(static)), time.Hour)))
	mux.Handle("GET /media/", http.StripPrefix("/media/", cacheFor(http.FileServer(http.Dir(s.opt.MediaDir)), 24*time.Hour)))
	mux.HandleFunc("GET /events", s.events)

	mux.HandleFunc("GET /{$}", s.page(s.home))
	mux.HandleFunc("GET /scoreboard", s.page(s.scoreboard))
	mux.HandleFunc("GET /game/{season}/{row}", s.page(s.game))
	mux.HandleFunc("GET /standings", s.page(s.standings))
	mux.HandleFunc("GET /rankings", s.page(s.rankings))
	mux.HandleFunc("GET /teams", s.page(s.teams))
	mux.HandleFunc("GET /team/{id}", s.page(s.team))
	mux.HandleFunc("GET /team/{id}/roster", s.page(s.teamRoster))
	mux.HandleFunc("GET /team/{id}/schedule", s.page(s.teamSchedule))
	mux.HandleFunc("GET /team/{id}/stats", s.page(s.teamStats))
	mux.HandleFunc("GET /team/{id}/history", s.page(s.teamHistory))
	mux.HandleFunc("GET /coach/{key}", s.page(s.coach))
	mux.HandleFunc("GET /coaches", s.page(s.coaches))
	mux.HandleFunc("GET /news", s.page(s.news))
	mux.HandleFunc("GET /story/{id}", s.page(s.story))
	mux.HandleFunc("GET /story/{id}/{slug}", s.page(s.story))
	mux.HandleFunc("GET /write", s.page(s.editor))
	mux.HandleFunc("POST /write", s.action(s.saveStory))
	mux.HandleFunc("GET /story/{id}/edit", s.page(s.editor))
	mux.HandleFunc("POST /story/{id}/edit", s.action(s.saveStory))
	mux.HandleFunc("POST /story/{id}/delete", s.action(s.deleteStory))
	mux.HandleFunc("POST /write/preview", s.action(s.previewStory))
	mux.HandleFunc("POST /write/upload", s.action(s.upload))
	mux.HandleFunc("GET /player/{id}", s.page(s.player))
	mux.HandleFunc("GET /stats", s.page(s.leaders))
	mux.HandleFunc("GET /awards", s.page(s.awards))
	mux.HandleFunc("GET /records", s.page(s.records))
	mux.HandleFunc("GET /recruiting", s.page(s.recruiting))
	mux.HandleFunc("GET /history", s.page(s.history))
	mux.HandleFunc("GET /search", s.page(s.search))
	mux.HandleFunc("GET /admin", s.page(s.admin))
	mux.HandleFunc("GET /dynasties", s.page(s.dynasties))
	mux.HandleFunc("GET /dynasty/{id}", s.switchDynasty)

	mux.HandleFunc("GET /api/scoreboard", s.api(s.apiScoreboard))
	mux.HandleFunc("GET /api/game/{season}/{row}", s.api(s.apiGame))
	mux.HandleFunc("GET /api/team/{id}", s.api(s.apiTeam))
	mux.HandleFunc("GET /api/player/{id}", s.api(s.apiPlayer))
	mux.HandleFunc("GET /api/standings", s.api(s.apiStandings))
	return logRequests(s.opt.Log, mux)
}

func cacheFor(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		h.ServeHTTP(w, r)
	})
}

func logRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h.ServeHTTP(w, r)
		if d := time.Since(start); d > 500*time.Millisecond && r.URL.Path != "/events" {
			log.Warn("slow request", "path", r.URL.Path, "took", d.Round(time.Millisecond))
		}
	})
}
