package config

import (
	"os"
	"strconv"
)

// Config is the env-driven server configuration. Every value has a sane
// default so `docker run` and `go run` both just work.
type Config struct {
	Port          string
	DataDir       string
	PublicBaseURL string
	SessionSecret string
	MaxHTMLBytes  int64
	// StorageBudget is the max total bytes of stored draft HTML (all versions
	// of live drafts). Default 5 GiB — sized for "worth of HTML files".
	StorageBudget int64
	// UploadRateLimitMax / UploadRateLimitWindowMs bound uploads per key/ip.
	UploadRateLimitMax      int
	UploadRateLimitWindowMs int64
	ShookBaseURL            string
}

const (
	defaultPort               = "8080"
	defaultDataDir            = "./data"
	defaultPublicBaseURL      = "http://localhost:8080"
	defaultMaxHTMLBytes       = 512 * 1024
	defaultStorageBudget      = 5 * 1024 * 1024 * 1024 // 5 GiB
	defaultUploadRateLimitMax = 30
	defaultUploadRateLimitWin = 60_000
	defaultShookBaseURL       = "https://shoo.dev"
)

// Load reads configuration from the environment.
func Load() Config {
	return Config{
		Port:                    getenv("PORT", defaultPort),
		DataDir:                 getenv("DATA_DIR", defaultDataDir),
		PublicBaseURL:           getenv("PUBLIC_BASE_URL", defaultPublicBaseURL),
		SessionSecret:           os.Getenv("SESSION_SECRET"),
		MaxHTMLBytes:            getenvInt64("MAX_HTML_BYTES", defaultMaxHTMLBytes),
		StorageBudget:           getenvInt64("STORAGE_BUDGET_BYTES", defaultStorageBudget),
		UploadRateLimitMax:      int(getenvInt64("UPLOAD_RATE_LIMIT_MAX", int64(defaultUploadRateLimitMax))),
		UploadRateLimitWindowMs: getenvInt64("UPLOAD_RATE_LIMIT_WINDOW_MS", defaultUploadRateLimitWin),
		ShookBaseURL:            getenv("SHOO_BASE_URL", defaultShookBaseURL),
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
