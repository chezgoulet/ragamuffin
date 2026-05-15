package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all Ragamuffin configuration, parsed from environment variables.
type Config struct {
	// Required
	VaultPath       string
	QdrantURL       string
	EmbeddingAPIKey string

	// Optional — Qdrant
	QdrantCollection string

	// Optional — Watcher
	WatchInterval string
	WatcherMode    string

	// Optional — Embedding
	EmbeddingProvider string
	EmbeddingModel    string
	EmbeddingBaseURL  string
	EmbeddingDims     int

	// Optional — Chunking
	ChunkMaxTokens int

	// Optional — Server
	Port string
	Host string

	// Optional — Rate Limiting
	RateLimitEnabled bool
	RateLimitRecall  int
	RateLimitAsk     int
	RateLimitDraft   int
	RateLimitAudit   int

	// Optional — LLM
	LLMProvider string
	LLMBaseURL  string
	LLMModel    string
	LLMAPIKey   string

	// Optional — Git
	GitProviderEnabled bool
	GitProvider        string
	GitToken           string
	GitBaseBranch      string
	GitBaseURL         string
	GitRepos           string

	// Optional — Audit / Tuning
	AuditSampleSize int
	AutoThreshold  float64

	// Optional — Logging
	LogLevel string
}

// HasLLM returns true if an LLM is configured.
func (c *Config) HasLLM() bool {
	return c.LLMProvider != "" && c.LLMAPIKey != ""
}

// HasGit returns true if git provider is enabled and configured.
func (c *Config) HasGit() bool {
	return c.GitProviderEnabled && c.GitToken != ""
}

// Validate checks configuration and returns a list of fatal errors.
// Returns nil if the configuration is valid.
func (c *Config) Validate() []string {
	var errs []string

	// Vault path must exist
	if info, err := os.Stat(c.VaultPath); err != nil {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_VAULT_PATH %q does not exist or is not readable", c.VaultPath))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_VAULT_PATH %q is not a directory", c.VaultPath))
	}

	// Qdrant URL must be parseable
	if _, err := parseURL(c.QdrantURL); err != nil {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_QDRANT_URL %q is not a valid URL: %v", c.QdrantURL, err))
	}

	// Embedding dims must be positive
	if c.EmbeddingDims <= 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_EMBEDDING_DIMS must be positive, got %d", c.EmbeddingDims))
	}

	// Watch interval must be a valid duration
	if _, err := time.ParseDuration(c.WatchInterval); err != nil {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_WATCH_INTERVAL %q is not a valid duration", c.WatchInterval))
	}

	// Watcher mode must be poll or inotify
	if c.WatcherMode != "poll" && c.WatcherMode != "inotify" {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_WATCHER_MODE must be 'poll' or 'inotify', got %q", c.WatcherMode))
	}

	// Chunk max tokens must be non-negative
	if c.ChunkMaxTokens < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_CHUNK_MAX_TOKENS must be non-negative, got %d", c.ChunkMaxTokens))
	}

	// Rate limit values must be non-negative
	if c.RateLimitRecall < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_RECALL must be non-negative, got %d", c.RateLimitRecall))
	}
	if c.RateLimitAsk < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_ASK must be non-negative, got %d", c.RateLimitAsk))
	}
	if c.RateLimitDraft < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_DRAFT must be non-negative, got %d", c.RateLimitDraft))
	}
	if c.RateLimitAudit < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_AUDIT must be non-negative, got %d", c.RateLimitAudit))
	}

	return errs
}

func parseURL(raw string) (interface{}, error) {
	// gRPC addresses are bare host:port — skip URL validation
	if strings.Contains(raw, ":") && !strings.Contains(raw, "://") {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		return nil, fmt.Errorf("missing scheme")
	}
	return nil, nil
}

// Load reads configuration from environment variables with defaults.
// Returns an error if any required variable is unset.
func Load() (*Config, error) {
	vaultPath, err := requireEnv("RAGAMUFFIN_VAULT_PATH")
	if err != nil {
		return nil, err
	}
	qdrantURL, err := requireEnv("RAGAMUFFIN_QDRANT_URL")
	if err != nil {
		return nil, err
	}

	return &Config{
		VaultPath:       vaultPath,
		QdrantURL:       qdrantURL,
		EmbeddingAPIKey: os.Getenv("RAGAMUFFIN_EMBEDDING_API_KEY"),

		QdrantCollection: envOrDefault("RAGAMUFFIN_QDRANT_COLLECTION", "ragamuffin"),
		WatchInterval:    envOrDefault("RAGAMUFFIN_WATCH_INTERVAL", "60s"),
		WatcherMode:      envOrDefault("RAGAMUFFIN_WATCHER_MODE", "poll"),

		EmbeddingProvider: envOrDefault("RAGAMUFFIN_EMBEDDING_PROVIDER", "openai"),
		EmbeddingModel:    envOrDefault("RAGAMUFFIN_EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingBaseURL:  envOrDefault("RAGAMUFFIN_EMBEDDING_BASE_URL", "https://api.openai.com/v1"),
		EmbeddingDims:     envInt("RAGAMUFFIN_EMBEDDING_DIMS", 1536),

		ChunkMaxTokens: envInt("RAGAMUFFIN_CHUNK_MAX_TOKENS", 2000),

		Port: envOrDefault("RAGAMUFFIN_PORT", "8000"),
		Host: envOrDefault("RAGAMUFFIN_HOST", "0.0.0.0"),

		RateLimitEnabled: envBool("RAGAMUFFIN_RATE_LIMIT_ENABLED"),
		RateLimitRecall:  envInt("RAGAMUFFIN_RATE_LIMIT_RECALL", 60),
		RateLimitAsk:     envInt("RAGAMUFFIN_RATE_LIMIT_ASK", 10),
		RateLimitDraft:   envInt("RAGAMUFFIN_RATE_LIMIT_DRAFT", 30),
		RateLimitAudit:   envInt("RAGAMUFFIN_RATE_LIMIT_AUDIT", 5),

		LLMProvider: os.Getenv("RAGAMUFFIN_LLM_PROVIDER"),
		LLMBaseURL:  os.Getenv("RAGAMUFFIN_LLM_BASE_URL"),
		LLMModel:    os.Getenv("RAGAMUFFIN_LLM_MODEL"),
		LLMAPIKey:   os.Getenv("RAGAMUFFIN_LLM_API_KEY"),

		GitProviderEnabled: envBool("RAGAMUFFIN_GIT_PROVIDER_ENABLED"),
		GitProvider:        envOrDefault("RAGAMUFFIN_GIT_PROVIDER", "github"),
		GitToken:           os.Getenv("RAGAMUFFIN_GIT_TOKEN"),
		GitBaseBranch:      envOrDefault("RAGAMUFFIN_GIT_BASE_BRANCH", "main"),
		GitBaseURL:         os.Getenv("RAGAMUFFIN_GIT_BASE_URL"),
		GitRepos:           os.Getenv("RAGAMUFFIN_GIT_REPOS"),

		AuditSampleSize: envInt("RAGAMUFFIN_AUDIT_SAMPLE_SIZE", 50),
		AutoThreshold:   envFloat("RAGAMUFFIN_AUTO_THRESHOLD", 0.75),
		LogLevel:        envOrDefault("RAGAMUFFIN_LOG_LEVEL", "info"),
	}, nil
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required environment variable not set: %s", key)
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	v := os.Getenv(key)
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

func envFloat(key string, def float64) float64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}
