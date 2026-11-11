// Package config describes process startup settings. It is never decoded from an API request.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid config")

// Lookup reads one setting; os.LookupEnv satisfies it.
type Lookup func(key string) (string, bool)

const (
	defaultHTTPAddress     = ":18080"
	defaultShutdownTimeout = 15 * time.Second
	// defaultAccessTTL bounds how long a stolen access token stays useful; the browser refreshes silently.
	defaultAccessTTL  = 15 * time.Minute
	defaultRefreshTTL = 30 * 24 * time.Hour
	// minTokenSecretLength is the HS256 key size.
	minTokenSecretLength = 32
	// secretKeyLength is the AES-256 key that seals environment secrets.
	secretKeyLength = 32
	defaultBlobDir  = "data/blobs"
	// defaultMaxBuildSize fits the largest real-world APKs and IPAs with room to spare.
	defaultMaxBuildSize = 2 << 30

	defaultWorkerLease        = 2 * time.Minute
	defaultWorkerPollInterval = 30 * time.Second
	defaultAnalysisWorkers    = 2
	// defaultGenerationWorkers bounds concurrent model calls, which are slow and billed.
	defaultGenerationWorkers = 2
	// defaultRunWorkers bounds the devices one API process drives at once; each mostly waits on its hub.
	defaultRunWorkers = 16
	// defaultAuthoringWorkers bounds the agents one API process runs at once; each calls the model every turn.
	defaultAuthoringWorkers = 2
	// defaultAuthoringSettle gives the app time to react to an action before the agent looks again.
	defaultAuthoringSettle = time.Second
	// defaultIndexWorkers keeps note indexing from crowding out generation at the provider.
	defaultIndexWorkers = 2
	// defaultReviewWorkers is enough for reviews, which come one per authoring and are not waited on.
	defaultReviewWorkers = 1
	defaultLLMBaseURL    = "https://openrouter.ai/api/v1"
	defaultLLMModel      = "anthropic/claude-sonnet-5"
	// defaultEmbeddingModel produces the 1536 dimensions the knowledge store keeps.
	defaultEmbeddingModel = "openai/text-embedding-3-small"

	defaultHubPollInterval = 5 * time.Second
	// defaultElementWait covers a screen that loads over the network before a step gives up.
	defaultElementWait = 10 * time.Second
	defaultHubSettle   = 500 * time.Millisecond
	hubTokenPrefix     = "mqh_"
	// defaultICEServers lets a browser outside the hub's network find a path to it; set TURN servers too
	// when either side sits behind a symmetric NAT.
	defaultICEServers = "stun:stun.l.google.com:19302"
)

type API struct {
	HTTPAddress     string
	DatabaseURL     string
	LogLevel        slog.Level
	CORSOrigins     []string
	ShutdownTimeout time.Duration
	TokenSecret     string
	AccessTTL       time.Duration
	RefreshTTL      time.Duration
	// SecretKey seals stored secrets; changing it makes every stored secret unreadable.
	SecretKey    []byte
	BlobDir      string
	MaxBuildSize int64
	// ID names this process in the leases of the run jobs it takes; it must differ between replicas.
	ID               string
	RunWorkers       int
	AuthoringWorkers int
	AuthoringSettle  time.Duration
	Lease            time.Duration
	PollInterval     time.Duration
	LLM              LLM
	// ICEServers are the STUN and TURN URLs browsers use to reach the hubs that stream device screens.
	ICEServers []string
}

func LoadAPI(lookup Lookup) (API, error) {
	level, err := parseLevel(value(lookup, "MOBQA_LOG_LEVEL", "info"))
	if err != nil {
		return API{}, err
	}
	cfg := API{
		HTTPAddress: value(lookup, "MOBQA_HTTP_ADDRESS", defaultHTTPAddress),
		DatabaseURL: value(lookup, "DATABASE_URL", ""),
		LogLevel:    level,
		CORSOrigins: splitList(value(lookup, "MOBQA_CORS_ORIGINS", "")),
		TokenSecret: value(lookup, "MOBQA_TOKEN_SECRET", ""),
		BlobDir:     value(lookup, "MOBQA_BLOB_DIR", defaultBlobDir),
		ID:          value(lookup, "MOBQA_API_ID", processID()),
		LLM:         loadLLM(lookup),
		ICEServers:  splitList(value(lookup, "MOBQA_ICE_SERVERS", defaultICEServers)),
	}
	if cfg.MaxBuildSize, err = parseSize(value(lookup, "MOBQA_MAX_BUILD_SIZE", ""), defaultMaxBuildSize); err != nil {
		return API{}, err
	}
	runWorkers, err := parseSize(value(lookup, "MOBQA_RUN_WORKERS", ""), defaultRunWorkers)
	if err != nil {
		return API{}, err
	}
	cfg.RunWorkers = int(runWorkers)
	authoringWorkers, err := parseSize(value(lookup, "MOBQA_AUTHORING_WORKERS", ""), defaultAuthoringWorkers)
	if err != nil {
		return API{}, err
	}
	cfg.AuthoringWorkers = int(authoringWorkers)
	if cfg.SecretKey, err = parseKey(value(lookup, "MOBQA_SECRET_KEY", "")); err != nil {
		return API{}, err
	}
	durations := []struct {
		target   *time.Duration
		key      string
		fallback time.Duration
	}{
		{target: &cfg.ShutdownTimeout, key: "MOBQA_SHUTDOWN_TIMEOUT", fallback: defaultShutdownTimeout},
		{target: &cfg.AccessTTL, key: "MOBQA_ACCESS_TTL", fallback: defaultAccessTTL},
		{target: &cfg.RefreshTTL, key: "MOBQA_REFRESH_TTL", fallback: defaultRefreshTTL},
		{target: &cfg.Lease, key: "MOBQA_WORKER_LEASE", fallback: defaultWorkerLease},
		{target: &cfg.PollInterval, key: "MOBQA_WORKER_POLL_INTERVAL", fallback: defaultWorkerPollInterval},
		{target: &cfg.AuthoringSettle, key: "MOBQA_AUTHORING_SETTLE", fallback: defaultAuthoringSettle},
	}
	for _, d := range durations {
		if *d.target, err = parseDuration(value(lookup, d.key, ""), d.fallback); err != nil {
			return API{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return API{}, err
	}
	return cfg, nil
}

func (c API) Validate() error {
	if strings.TrimSpace(c.HTTPAddress) == "" {
		return fmt.Errorf("%w: MOBQA_HTTP_ADDRESS is empty", ErrInvalid)
	}
	if err := validateDatabaseURL(c.DatabaseURL); err != nil {
		return err
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("%w: MOBQA_SHUTDOWN_TIMEOUT must be positive", ErrInvalid)
	}
	if len(c.TokenSecret) < minTokenSecretLength {
		return fmt.Errorf("%w: MOBQA_TOKEN_SECRET must be at least %d bytes", ErrInvalid, minTokenSecretLength)
	}
	if len(c.SecretKey) != secretKeyLength {
		return fmt.Errorf("%w: MOBQA_SECRET_KEY must be %d bytes", ErrInvalid, secretKeyLength)
	}
	if c.AccessTTL <= 0 || c.RefreshTTL <= c.AccessTTL {
		return fmt.Errorf("%w: MOBQA_REFRESH_TTL must exceed a positive MOBQA_ACCESS_TTL", ErrInvalid)
	}
	if strings.TrimSpace(c.BlobDir) == "" || c.MaxBuildSize <= 0 {
		return fmt.Errorf("%w: MOBQA_BLOB_DIR and a positive MOBQA_MAX_BUILD_SIZE are required", ErrInvalid)
	}
	if strings.TrimSpace(c.ID) == "" || c.RunWorkers < 1 || c.Lease <= 0 || c.PollInterval <= 0 {
		return fmt.Errorf("%w: MOBQA_API_ID, MOBQA_RUN_WORKERS, the worker lease and poll interval must be set",
			ErrInvalid)
	}
	if c.AuthoringWorkers < 1 || c.AuthoringSettle < 0 {
		return fmt.Errorf("%w: MOBQA_AUTHORING_WORKERS must be positive and MOBQA_AUTHORING_SETTLE not negative",
			ErrInvalid)
	}
	for _, origin := range c.CORSOrigins {
		if err := validateOrigin(origin); err != nil {
			return err
		}
	}
	if err := validateICEServers("MOBQA_ICE_SERVERS", c.ICEServers); err != nil {
		return err
	}
	return c.LLM.validate()
}

// processID tells apart processes on one host in job leases.
func processID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func validateDatabaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: DATABASE_URL is required", ErrInvalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return fmt.Errorf("%w: DATABASE_URL must be a postgres:// URL", ErrInvalid)
	}
	return nil
}

func validateICEServers(key string, servers []string) error {
	for _, server := range servers {
		if !slices.ContainsFunc([]string{"stun:", "stuns:", "turn:", "turns:"}, func(scheme string) bool {
			return strings.HasPrefix(server, scheme)
		}) {
			return fmt.Errorf("%w: %s must list stun: or turn: URLs", ErrInvalid, key)
		}
	}
	return nil
}

func validateOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" {
		return fmt.Errorf("%w: CORS origin %q must be scheme://host[:port]", ErrInvalid, origin)
	}
	return nil
}

func value(lookup Lookup, key, fallback string) string {
	raw, ok := lookup(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	return strings.TrimSpace(raw)
}

func parseLevel(raw string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%w: MOBQA_LOG_LEVEL %q", ErrInvalid, raw)
	}
	return level, nil
}

func parseDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: duration %q", ErrInvalid, raw)
	}
	return parsed, nil
}

func parseSize(raw string, fallback int64) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: size %q must be a number of bytes", ErrInvalid, raw)
	}
	return parsed, nil
}

func parseKey(raw string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: MOBQA_SECRET_KEY must be base64", ErrInvalid)
	}
	return key, nil
}

func splitList(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

// Worker configures the background job process.
type Worker struct {
	DatabaseURL string
	LogLevel    slog.Level
	BlobDir     string
	// ID names this process in job leases; it must differ between workers running at the same time.
	ID string
	// Lease is how long a job stays claimed without a heartbeat before another worker may take it.
	Lease        time.Duration
	PollInterval time.Duration
	// DrainTimeout is how long running jobs may finish after shutdown begins.
	DrainTimeout      time.Duration
	AnalysisWorkers   int
	GenerationWorkers int
	IndexWorkers      int
	ReviewWorkers     int
	LLM               LLM
}

// LLM points at an OpenAI-compatible provider. Without an API key, work that needs a model fails with a
// clear reason instead of the process refusing to start.
type LLM struct {
	BaseURL        string
	APIKey         string
	Model          string
	EmbeddingModel string
}

func loadLLM(lookup Lookup) LLM {
	return LLM{
		BaseURL:        value(lookup, "MOBQA_LLM_BASE_URL", defaultLLMBaseURL),
		APIKey:         value(lookup, "MOBQA_LLM_API_KEY", ""),
		Model:          value(lookup, "MOBQA_LLM_MODEL", defaultLLMModel),
		EmbeddingModel: value(lookup, "MOBQA_EMBEDDING_MODEL", defaultEmbeddingModel),
	}
}

func (c LLM) validate() error {
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: MOBQA_LLM_BASE_URL must be an http(s) URL", ErrInvalid)
	}
	if c.Model == "" || c.EmbeddingModel == "" {
		return fmt.Errorf("%w: MOBQA_LLM_MODEL and MOBQA_EMBEDDING_MODEL must not be empty", ErrInvalid)
	}
	return nil
}

func LoadWorker(lookup Lookup) (Worker, error) {
	level, err := parseLevel(value(lookup, "MOBQA_LOG_LEVEL", "info"))
	if err != nil {
		return Worker{}, err
	}
	cfg := Worker{
		DatabaseURL: value(lookup, "DATABASE_URL", ""),
		LogLevel:    level,
		BlobDir:     value(lookup, "MOBQA_BLOB_DIR", defaultBlobDir),
		ID:          value(lookup, "MOBQA_WORKER_ID", processID()),
		LLM:         loadLLM(lookup),
	}
	durations := []struct {
		target   *time.Duration
		key      string
		fallback time.Duration
	}{
		{target: &cfg.Lease, key: "MOBQA_WORKER_LEASE", fallback: defaultWorkerLease},
		{target: &cfg.PollInterval, key: "MOBQA_WORKER_POLL_INTERVAL", fallback: defaultWorkerPollInterval},
		{target: &cfg.DrainTimeout, key: "MOBQA_SHUTDOWN_TIMEOUT", fallback: defaultShutdownTimeout},
	}
	for _, d := range durations {
		if *d.target, err = parseDuration(value(lookup, d.key, ""), d.fallback); err != nil {
			return Worker{}, err
		}
	}
	counts := []struct {
		target   *int
		key      string
		fallback int64
	}{
		{target: &cfg.AnalysisWorkers, key: "MOBQA_ANALYSIS_WORKERS", fallback: defaultAnalysisWorkers},
		{target: &cfg.GenerationWorkers, key: "MOBQA_GENERATION_WORKERS", fallback: defaultGenerationWorkers},
		{target: &cfg.IndexWorkers, key: "MOBQA_INDEX_WORKERS", fallback: defaultIndexWorkers},
		{target: &cfg.ReviewWorkers, key: "MOBQA_REVIEW_WORKERS", fallback: defaultReviewWorkers},
	}
	for _, c := range counts {
		count, err := parseSize(value(lookup, c.key, ""), c.fallback)
		if err != nil {
			return Worker{}, err
		}
		*c.target = int(count)
	}
	if err := cfg.Validate(); err != nil {
		return Worker{}, err
	}
	return cfg, nil
}

func (c Worker) Validate() error {
	if err := validateDatabaseURL(c.DatabaseURL); err != nil {
		return err
	}
	if strings.TrimSpace(c.BlobDir) == "" || strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: MOBQA_BLOB_DIR and MOBQA_WORKER_ID must not be empty", ErrInvalid)
	}
	if c.Lease <= 0 || c.PollInterval <= 0 || c.DrainTimeout <= 0 {
		return fmt.Errorf("%w: worker lease, poll interval and shutdown timeout must be positive", ErrInvalid)
	}
	if c.AnalysisWorkers < 1 || c.GenerationWorkers < 1 || c.IndexWorkers < 1 || c.ReviewWorkers < 1 {
		return fmt.Errorf("%w: MOBQA_ANALYSIS_WORKERS, MOBQA_GENERATION_WORKERS, MOBQA_INDEX_WORKERS and "+
			"MOBQA_REVIEW_WORKERS must be at least 1", ErrInvalid)
	}
	return c.LLM.validate()
}

// Hub configures the device agent that runs on a machine with phones attached.
type Hub struct {
	// APIURL is the API's base URL; the hub dials its WebSocket endpoint.
	APIURL   string
	Token    string
	LogLevel slog.Level
	// PollInterval is how often the hub looks for attached and removed devices.
	PollInterval time.Duration
	// ADBPath is the adb binary; Android discovery is skipped when it cannot be found.
	ADBPath string
	// ElementWait is how long a replayed op looks for its element before its step fails.
	ElementWait time.Duration
	// Settle is the pause after each replayed action and between looks at the screen.
	Settle time.Duration
	// CacheDir keeps the builds the hub downloaded for runs.
	CacheDir string
	// ICEServers are the STUN and TURN URLs the hub offers to the peers of its live screens.
	ICEServers []string
	// FFmpegPath is the ffmpeg binary; without it the hub does not film the attempts it runs.
	FFmpegPath string
}

func LoadHub(lookup Lookup) (Hub, error) {
	level, err := parseLevel(value(lookup, "MOBQA_LOG_LEVEL", "info"))
	if err != nil {
		return Hub{}, err
	}
	cfg := Hub{
		APIURL:     strings.TrimSuffix(value(lookup, "MOBQA_API_URL", ""), "/"),
		Token:      value(lookup, "MOBQA_HUB_TOKEN", ""),
		LogLevel:   level,
		ADBPath:    value(lookup, "MOBQA_ADB_PATH", "adb"),
		CacheDir:   value(lookup, "MOBQA_HUB_CACHE_DIR", ""),
		ICEServers: splitList(value(lookup, "MOBQA_HUB_ICE_SERVERS", defaultICEServers)),
		FFmpegPath: value(lookup, "MOBQA_FFMPEG_PATH", "ffmpeg"),
	}
	if cfg.CacheDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return Hub{}, fmt.Errorf("%w: set MOBQA_HUB_CACHE_DIR: %w", ErrInvalid, err)
		}
		cfg.CacheDir = filepath.Join(dir, "mobqa", "builds")
	}
	durations := []struct {
		target   *time.Duration
		key      string
		fallback time.Duration
	}{
		{target: &cfg.PollInterval, key: "MOBQA_HUB_POLL_INTERVAL", fallback: defaultHubPollInterval},
		{target: &cfg.ElementWait, key: "MOBQA_HUB_ELEMENT_WAIT", fallback: defaultElementWait},
		{target: &cfg.Settle, key: "MOBQA_HUB_SETTLE", fallback: defaultHubSettle},
	}
	for _, d := range durations {
		if *d.target, err = parseDuration(value(lookup, d.key, ""), d.fallback); err != nil {
			return Hub{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Hub{}, err
	}
	return cfg, nil
}

func (c Hub) Validate() error {
	parsed, err := url.Parse(c.APIURL)
	if err != nil || parsed.Host == "" || !slices.Contains([]string{"http", "https", "ws", "wss"}, parsed.Scheme) {
		return fmt.Errorf("%w: MOBQA_API_URL must be an http(s) or ws(s) URL", ErrInvalid)
	}
	if !strings.HasPrefix(c.Token, hubTokenPrefix) {
		return fmt.Errorf("%w: MOBQA_HUB_TOKEN must be a hub token (mqh_...)", ErrInvalid)
	}
	if c.PollInterval <= 0 || c.ElementWait <= 0 || c.Settle <= 0 {
		return fmt.Errorf("%w: MOBQA_HUB_POLL_INTERVAL, MOBQA_HUB_ELEMENT_WAIT and MOBQA_HUB_SETTLE must be positive",
			ErrInvalid)
	}
	if err := validateICEServers("MOBQA_HUB_ICE_SERVERS", c.ICEServers); err != nil {
		return err
	}
	return nil
}
