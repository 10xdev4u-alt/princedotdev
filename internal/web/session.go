package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// SessionCookie is the cookie name for the dashboard session.
const SessionCookie = "draftdeck_session"

const sessionTTL = 30 * 24 * time.Hour

// Session is the verified payload of a session token.
type Session struct {
	AccountID   string
	AccountName string
	Email       string
}

// signToken returns base64url(JSON payload).HMAC-SHA256 — stateless like the
// Node version; a restart invalidates nothing.
func signToken(secret string, payload map[string]any, ttl time.Duration) (string, error) {
	payload["exp"] = time.Now().Add(ttl).Unix()
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(body)
	return encoded + "." + hmacDigest(secret, encoded), nil
}

func verifyToken(secret, token string) (map[string]any, bool) {
	dot := strings.Index(token, ".")
	if dot <= 0 {
		return nil, false
	}
	body, sig := token[:dot], token[dot+1:]
	expected := hmacDigest(secret, body)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	exp, ok := payload["exp"].(float64)
	if !ok || int64(exp) < time.Now().Unix() {
		return nil, false
	}
	return payload, true
}

// CreateSessionCookie builds the Set-Cookie header for a new session.
func CreateSessionCookie(secret string, s Session) (string, error) {
	token, err := signToken(secret, map[string]any{
		"accountId":   s.AccountID,
		"accountName": s.AccountName,
		"email":       orEmpty(s.Email),
	}, sessionTTL)
	if err != nil {
		return "", err
	}
	return serializeCookie(SessionCookie, token, int64(sessionTTL.Seconds()), secureCookie(secret != "")), nil
}

// ClearSessionCookie builds the Set-Cookie header that expires the session.
func ClearSessionCookie() string {
	return serializeCookie(SessionCookie, "", 0, false)
}

// ReadSession validates the session cookie, or nil.
func ReadSession(r *http.Request, secret string) *Session {
	if secret == "" {
		return nil
	}
	token := readCookie(r, SessionCookie)
	if token == "" {
		return nil
	}
	payload, ok := verifyToken(secret, token)
	if !ok {
		return nil
	}
	id, _ := payload["accountId"].(string)
	if id == "" {
		return nil
	}
	name, _ := payload["accountName"].(string)
	email, _ := payload["email"].(string)
	return &Session{AccountID: id, AccountName: name, Email: email}
}

func hmacDigest(secret, value string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func serializeCookie(name, value string, maxAge int64, secure bool) string {
	attrs := []string{
		name + "=" + value, // token is already URL-safe (base64url + ".")
		"Path=/",
		"HttpOnly",
		"SameSite=Lax",
		"Max-Age=" + itoa(maxAge),
	}
	if secure {
		attrs = append(attrs, "Secure")
	}
	return strings.Join(attrs, "; ")
}

func readCookie(r *http.Request, name string) string {
	for _, part := range strings.Split(r.Header.Get("Cookie"), ";") {
		eq := strings.Index(part, "=")
		if eq == -1 {
			continue
		}
		if strings.TrimSpace(part[:eq]) == name {
			return strings.TrimSpace(part[eq+1:])
		}
	}
	return ""
}

func secureCookie(hasSecret bool) bool {
	// Secure cookies only when we're not obviously running locally.
	return hasSecret && !strings.HasPrefix(publicBaseURL, "http://localhost")
}

// publicBaseURL is set by Init; used for the Secure-cookie heuristic.
var publicBaseURL string

// Init records the public base URL for cookie/redirect decisions.
func Init(baseURL string) { publicBaseURL = baseURL }

func orEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func itoa(n int64) string {
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
