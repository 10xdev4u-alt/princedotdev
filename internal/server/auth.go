package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/10xdev4u-alt/princedotdev/internal/db"
)

// bearerToken extracts the token from an Authorization: Bearer header, or "".
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// optionalAuth resolves the API key from the request, or nil when absent.
func (s *Server) optionalAuth(r *http.Request) (*db.APIKey, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, nil
	}
	return s.db.FindAPIKeyByToken(token)
}

// requireAuth rejects the request with 401 unless a valid key is present.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, err := s.optionalAuth(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Internal server error."})
			return
		}
		if key == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "Missing or invalid API key."})
			return
		}
		r = withAuth(r, key)
		next(w, r)
	}
}

type authCtxKey struct{}

// withAuth attaches the resolved key to the request context.
func withAuth(r *http.Request, key *db.APIKey) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authCtxKey{}, key))
}

// authFrom returns the attached key, or nil.
func authFrom(r *http.Request) *db.APIKey {
	if v, ok := r.Context().Value(authCtxKey{}).(*db.APIKey); ok {
		return v
	}
	return nil
}
