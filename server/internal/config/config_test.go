package config

import (
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	validDatabaseURL = "postgres://h/db"
	validSecret      = "0123456789abcdef0123456789abcdef"
	// validKey is 32 zero bytes in base64.
	validKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

// required returns the settings without defaults plus overrides.
func required(overrides map[string]string) map[string]string {
	values := map[string]string{
		"DATABASE_URL": validDatabaseURL, "MOBQA_TOKEN_SECRET": validSecret, "MOBQA_SECRET_KEY": validKey,
	}
	for key, v := range overrides {
		values[key] = v
	}
	return values
}

func lookupFrom(values map[string]string) Lookup {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadAPIDefaults(t *testing.T) {
	cfg, err := LoadAPI(lookupFrom(required(nil)))
	if err != nil {
		t.Fatalf("LoadAPI: %v", err)
	}

	if cfg.HTTPAddress != ":18080" {
		t.Errorf("HTTPAddress = %q, want :18080", cfg.HTTPAddress)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
	if len(cfg.CORSOrigins) != 0 {
		t.Errorf("CORSOrigins = %v, want none", cfg.CORSOrigins)
	}
	if cfg.BlobDir != "data/blobs" || cfg.MaxBuildSize != 2<<30 {
		t.Errorf("BlobDir = %q, MaxBuildSize = %d", cfg.BlobDir, cfg.MaxBuildSize)
	}
	if len(cfg.SecretKey) != 32 {
		t.Errorf("SecretKey has %d bytes, want 32", len(cfg.SecretKey))
	}
	if cfg.AccessTTL != 15*time.Minute || cfg.RefreshTTL != 720*time.Hour {
		t.Errorf("AccessTTL = %v, RefreshTTL = %v, want 15m and 720h", cfg.AccessTTL, cfg.RefreshTTL)
	}
	if cfg.ID == "" || cfg.RunWorkers != 16 || cfg.Lease != 2*time.Minute || cfg.PollInterval != 30*time.Second {
		t.Errorf("ID = %q, RunWorkers = %d, Lease = %v, PollInterval = %v", cfg.ID, cfg.RunWorkers, cfg.Lease,
			cfg.PollInterval)
	}
	if cfg.AuthoringWorkers != 2 || cfg.AuthoringSettle != time.Second {
		t.Errorf("AuthoringWorkers = %d, AuthoringSettle = %v, want 2 and 1s", cfg.AuthoringWorkers, cfg.AuthoringSettle)
	}
	if cfg.LLM.APIKey != "" || cfg.LLM.EmbeddingModel != "openai/text-embedding-3-small" {
		t.Errorf("LLM = %+v, want no key and the default embedding model", cfg.LLM)
	}
	if !slices.Equal(cfg.ICEServers, []string{"stun:stun.l.google.com:19302"}) {
		t.Errorf("ICEServers = %v, want the public STUN server", cfg.ICEServers)
	}
}

func TestLoadAPIOverrides(t *testing.T) {
	cfg, err := LoadAPI(lookupFrom(required(map[string]string{
		"DATABASE_URL":           "postgresql://u:p@db:5432/mobqa",
		"MOBQA_HTTP_ADDRESS":     "127.0.0.1:9000",
		"MOBQA_LOG_LEVEL":        "debug",
		"MOBQA_CORS_ORIGINS":     " http://localhost:5173 , https://app.example.com ",
		"MOBQA_SHUTDOWN_TIMEOUT": "3s",
		"MOBQA_ACCESS_TTL":       "5m",
		"MOBQA_REFRESH_TTL":      "24h",
	})))
	if err != nil {
		t.Fatalf("LoadAPI: %v", err)
	}

	if cfg.HTTPAddress != "127.0.0.1:9000" || cfg.LogLevel != slog.LevelDebug || cfg.ShutdownTimeout != 3*time.Second ||
		cfg.AccessTTL != 5*time.Minute || cfg.RefreshTTL != 24*time.Hour {
		t.Fatalf("cfg = %+v", cfg)
	}
	wantOrigins := []string{"http://localhost:5173", "https://app.example.com"}
	if !slices.Equal(cfg.CORSOrigins, wantOrigins) {
		t.Fatalf("CORSOrigins = %v, want %v", cfg.CORSOrigins, wantOrigins)
	}
}

func TestLoadAPIRejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing database url", values: map[string]string{"MOBQA_TOKEN_SECRET": validSecret}},
		{name: "non postgres database url", values: required(map[string]string{"DATABASE_URL": "mysql://localhost/db"})},
		{name: "bad log level", values: required(map[string]string{"MOBQA_LOG_LEVEL": "loud"})},
		{name: "bad duration", values: required(map[string]string{"MOBQA_SHUTDOWN_TIMEOUT": "soon"})},
		{name: "zero duration", values: required(map[string]string{"MOBQA_SHUTDOWN_TIMEOUT": "0s"})},
		{name: "origin with path", values: required(map[string]string{"MOBQA_CORS_ORIGINS": "http://a.com/x"})},
		{name: "missing token secret", values: map[string]string{"DATABASE_URL": validDatabaseURL}},
		{name: "short token secret", values: required(map[string]string{"MOBQA_TOKEN_SECRET": "short"})},
		{name: "missing secret key", values: required(map[string]string{"MOBQA_SECRET_KEY": ""})},
		{name: "secret key not base64", values: required(map[string]string{"MOBQA_SECRET_KEY": "not base64!"})},
		{name: "short secret key", values: required(map[string]string{"MOBQA_SECRET_KEY": "c2hvcnQ="})},
		{name: "bad build size", values: required(map[string]string{"MOBQA_MAX_BUILD_SIZE": "2GB"})},
		{name: "zero build size", values: required(map[string]string{"MOBQA_MAX_BUILD_SIZE": "0"})},
		{name: "refresh shorter than access", values: required(map[string]string{"MOBQA_REFRESH_TTL": "1m"})},
		{name: "no run workers", values: required(map[string]string{"MOBQA_RUN_WORKERS": "0"})},
		{name: "bad llm url", values: required(map[string]string{"MOBQA_LLM_BASE_URL": "openrouter.ai"})},
		{name: "ice server not stun", values: required(map[string]string{"MOBQA_ICE_SERVERS": "turn.example.com"})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadAPI(lookupFrom(tc.values))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoadWorker(t *testing.T) {
	cfg, err := LoadWorker(lookupFrom(map[string]string{
		"DATABASE_URL": validDatabaseURL, "MOBQA_WORKER_ID": "worker-a", "MOBQA_ANALYSIS_WORKERS": "4",
		"MOBQA_LLM_API_KEY": "sk-test", "MOBQA_EMBEDDING_MODEL": "vendor/embedder",
	}))
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}

	if cfg.ID != "worker-a" || cfg.AnalysisWorkers != 4 || cfg.Lease != 2*time.Minute ||
		cfg.PollInterval != 30*time.Second || cfg.DrainTimeout != 15*time.Second || cfg.BlobDir != "data/blobs" {
		t.Fatalf("cfg = %+v", cfg)
	}
	want := LLM{
		BaseURL: "https://openrouter.ai/api/v1", APIKey: "sk-test", Model: "anthropic/claude-sonnet-5",
		EmbeddingModel: "vendor/embedder",
	}
	if cfg.GenerationWorkers != 2 || cfg.IndexWorkers != 2 || cfg.ReviewWorkers != 1 || cfg.LLM != want {
		t.Fatalf("generation workers %d, index workers %d, review workers %d, llm %+v", cfg.GenerationWorkers,
			cfg.IndexWorkers, cfg.ReviewWorkers, cfg.LLM)
	}
}

func TestLoadWorkerDefaultsIDToHostAndPID(t *testing.T) {
	cfg, err := LoadWorker(lookupFrom(map[string]string{"DATABASE_URL": validDatabaseURL}))
	if err != nil || cfg.ID == "" {
		t.Fatalf("LoadWorker = %+v, %v", cfg, err)
	}
}

func TestLoadWorkerRejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing database url", values: map[string]string{}},
		{
			name:   "no analysis workers",
			values: map[string]string{"DATABASE_URL": validDatabaseURL, "MOBQA_ANALYSIS_WORKERS": "0"},
		},
		{name: "bad lease", values: map[string]string{"DATABASE_URL": validDatabaseURL, "MOBQA_WORKER_LEASE": "-1s"}},
		{
			name:   "no generation workers",
			values: map[string]string{"DATABASE_URL": validDatabaseURL, "MOBQA_GENERATION_WORKERS": "0"},
		},
		{
			name:   "no index workers",
			values: map[string]string{"DATABASE_URL": validDatabaseURL, "MOBQA_INDEX_WORKERS": "0"},
		},
		{
			name:   "llm url without scheme",
			values: map[string]string{"DATABASE_URL": validDatabaseURL, "MOBQA_LLM_BASE_URL": "openrouter.ai"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadWorker(lookupFrom(tc.values)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestLoadHub(t *testing.T) {
	cfg, err := LoadHub(lookupFrom(map[string]string{
		"MOBQA_API_URL": "https://api.example.com/", "MOBQA_HUB_TOKEN": "mqh_secret",
	}))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}

	if cfg.APIURL != "https://api.example.com" || cfg.PollInterval != 5*time.Second || cfg.ADBPath != "adb" ||
		cfg.FFmpegPath != "ffmpeg" || cfg.ElementWait != 10*time.Second || cfg.Settle != 500*time.Millisecond ||
		!strings.HasSuffix(cfg.CacheDir, filepath.Join("mobqa", "builds")) ||
		!slices.Equal(cfg.ICEServers, []string{"stun:stun.l.google.com:19302"}) {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadHubRejectsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing url", values: map[string]string{"MOBQA_HUB_TOKEN": "mqh_secret"}},
		{
			name:   "unsupported scheme",
			values: map[string]string{"MOBQA_API_URL": "ftp://api.example.com", "MOBQA_HUB_TOKEN": "mqh_secret"},
		},
		{name: "user token", values: map[string]string{"MOBQA_API_URL": "https://api.example.com", "MOBQA_HUB_TOKEN": "eyJ"}},
		{
			name: "bad poll interval",
			values: map[string]string{
				"MOBQA_API_URL": "https://api.example.com", "MOBQA_HUB_TOKEN": "mqh_secret", "MOBQA_HUB_POLL_INTERVAL": "0s",
			},
		},
		{
			name: "no settle",
			values: map[string]string{
				"MOBQA_API_URL": "https://api.example.com", "MOBQA_HUB_TOKEN": "mqh_secret", "MOBQA_HUB_SETTLE": "0s",
			},
		},
		{
			name: "ice server that is not stun or turn",
			values: map[string]string{
				"MOBQA_API_URL": "https://api.example.com", "MOBQA_HUB_TOKEN": "mqh_secret",
				"MOBQA_HUB_ICE_SERVERS": "stun:stun.example.com:3478, https://turn.example.com",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadHub(lookupFrom(tc.values)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
