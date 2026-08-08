// Package server is the HTTP layer: API (upload, auth, drafts), draft
// serving with the raw-HTML contract, and (in later PRs) the web dashboard.
package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/config"
	"github.com/10xdev4u-alt/princedotdev/internal/db"
	"github.com/10xdev4u-alt/princedotdev/internal/store"
	"github.com/10xdev4u-alt/princedotdev/internal/web"
)

// Server wires the store, database, and HTTP routes together.
type Server struct {
	cfg       config.Config
	db        *db.DB
	store     *store.Store
	rl        *rateLimiter
	dashboard *web.DashboardHandler
	mux       *http.ServeMux
}

// New opens the database + store and builds the route table.
func New(cfg config.Config) (*Server, error) {
	d, err := db.Open(cfg)
	if err != nil {
		return nil, err
	}
	st, err := store.New(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	web.Init(cfg.PublicBaseURL)
	s := &Server{
		cfg:   cfg,
		db:    d,
		store: st,
		rl:    newRateLimiter(cfg.UploadRateLimitWindowMs, cfg.UploadRateLimitMax),
		mux:   http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Close releases the database handle.
func (s *Server) Close() error { return s.db.Close() }

// Checkpoint flushes the WAL into the main database file (used on graceful
// shutdown so volume snapshots are complete).
func (s *Server) Checkpoint() error { return s.db.Checkpoint() }

// Handler returns the HTTP handler for the whole service.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	m := s.mux
	m.Handle("GET /", s.noStore(s.handleHome()))
	m.Handle("GET /healthz", http.HandlerFunc(s.handleHealthz))

	// Identity
	m.Handle("GET /api/me", s.requireAuth(s.handleMe))

	// Drafts: list, detail, comments, review status
	m.Handle("GET /api/drafts", s.requireAuth(s.handleListDrafts))
	m.Handle("GET /api/drafts/{draftId}", s.requireAuth(s.handleDraftDetail))
	m.Handle("GET /api/drafts/{draftId}/comments", s.noStore(http.HandlerFunc(s.handleListComments)))
	m.Handle("POST /api/drafts/{draftId}/comments", s.requireAuth(s.handleAddComment))
	m.Handle("POST /api/drafts/{draftId}/status", s.requireAuth(s.handleSetStatus))
	m.Handle("DELETE /api/drafts/{draftId}", s.requireAuth(s.handleDeleteDraft))

	// Teams
	m.Handle("GET /api/teams", s.requireAuth(s.handleListTeams))
	m.Handle("POST /api/teams", s.requireAuth(s.handleCreateTeam))
	m.Handle("GET /api/teams/{teamId}", s.requireAuth(s.handleTeamDetail))
	m.Handle("GET /api/teams/{teamId}/members", s.requireAuth(s.handleListTeamMembers))
	m.Handle("POST /api/teams/{teamId}/members", s.requireAuth(s.handleAddTeamMember))
	m.Handle("DELETE /api/teams/{teamId}/members/{accountId}", s.requireAuth(s.handleRemoveTeamMember))

	// Control panel
	m.Handle("GET /api/stats", s.requireAuth(s.handleStats))
	m.Handle("POST /api/api-keys", s.requireAuth(s.handleMintKey))
	m.Handle("DELETE /api/api-keys/{keyId}", s.requireAuth(s.handleRevokeKey))

	// Uploads (anonymous allowed; auth resolved before rate limiting so the
	// limiter can key on the API key when present).
	m.Handle("POST /api/uploads", s.optionalAuthMiddleware(s.httpRateLimit(s.handleUpload, func(r *http.Request) string {
		if k := authFrom(r); k != nil {
			return "upload:" + k.ID
		}
		return "upload:" + clientIP(r)
	})))

	// Draft serving: byte-for-byte raw HTML to every client.
	m.Handle("GET /d/{draftId}", s.noStore(s.handleServe()))
	m.Handle("GET /d/{draftId}/raw", s.noStore(s.handleServe()))
	m.Handle("GET /d/{draftId}/v/{versionNumber}", s.noStore(s.handleServe()))
	m.Handle("GET /d/{draftId}/v/{versionNumber}/raw", s.noStore(s.handleServe()))

	// Web dashboard (session-gated); register only when SESSION_SECRET is set.
	s.dashboard = web.NewDashboard(s.cfg.SessionSecret, s.db, s.cfg.StorageBudget)
	if s.dashboard.Enabled() {
		s.dashboard.Routes(m)
	}
}

// noStore sets security + caching headers on every response.
func (s *Server) noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHome() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(web.HomeHTML(s.cfg.PublicBaseURL)))
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if err := s.db.Ping(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	k := authFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"accountId":   k.AccountID,
		"accountName": k.AccountName,
		"apiKeyId":    k.ID,
		"apiKeyName":  k.Name,
	})
}

// clientIP is a best-effort client address (X-Forwarded-For when present).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	return r.RemoteAddr
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
