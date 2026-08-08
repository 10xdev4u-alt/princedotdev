package server

import (
	"net/http"
	"sync"
	"time"
)

// bucket is one key's fixed-window counter (same semantics as the Node port).
type bucket struct {
	count   int
	resetAt time.Time
}

// rateLimiter is a tiny in-memory fixed-window limiter keyed by identity.
type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	max     int
	buckets map[string]*bucket
}

func newRateLimiter(windowMs int64, max int) *rateLimiter {
	if windowMs <= 0 {
		windowMs = 60_000
	}
	if max <= 0 {
		max = 30
	}
	return &rateLimiter{window: time.Duration(windowMs) * time.Millisecond, max: max, buckets: map[string]*bucket{}}
}

// allow reports whether key may proceed. When false it returns the number of
// seconds to wait before retrying.
func (rl *rateLimiter) allow(key string) (ok bool, retryAfter int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	b := rl.buckets[key]
	if b == nil || !b.resetAt.After(now) {
		rl.buckets[key] = &bucket{count: 1, resetAt: now.Add(rl.window)}
		return true, 0
	}
	b.count++
	if b.count > rl.max {
		secs := int(b.resetAt.Sub(now).Seconds()) + 1
		return false, secs
	}
	return true, 0
}

// httpRateLimit wraps a handler with the limiter, keyed by the provided func.
func (s *Server) httpRateLimit(next http.HandlerFunc, key func(r *http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, retry := s.rl.allow(key(r))
		if !ok {
			w.Header().Set("Retry-After", itoa(retry))
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "Rate limit exceeded."})
			return
		}
		next(w, r)
	}
}
