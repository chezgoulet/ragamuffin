package config

import (
	"os"
	"strconv"
)

// Config holds all Ragamuffin configuration, parsed from environment variables.
type Config struct {
	// Required
	VaultPath      string
	QdrantURL      string
	EmbeddingAPIKey string

	// Optional — Qdrant
	QdrantCollection string

	// Optional — Watcher
	WatchInterval string

	// Optional — Embedding
	EmbeddingProvider string
	EmbeddingModel    string
	EmbeddingBaseURL  string

	// Optional — Server
	Port string
	Host string

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
	GitRepos           string

	// Optional — Audit
	AuditSampleSize int

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

// Load reads configuration from environment variables with defaults.
func Load() *Config {
	return &Config{
		VaultPath:      requireEnv("RAGAMUFFIN_VAULT_PATH"),
		QdrantURL:      requireEnv("RAGAMUFFIN_QDRANT_URL"),
		EmbeddingAPIKey: requireEnv("RAGAMUFFIN_EMBEDDING_API_KEY"),

		QdrantCollection: envOrDefault("RAGAMUFFIN_QDRANT_COLLECTION", "ragamuffin"),
		WatchInterval:     envOrDefault("RAGAMUFFIN_WATCH_INTERVAL", "60s"),

		EmbeddingProvider: envOrDefault("RAGAMUFFIN_EMBEDDING_PROVIDER", "openai"),
		EmbeddingModel:    envOrDefault("RAGAMUFFIN_EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingBaseURL:  envOrDefault("RAGAMUFFIN_EMBEDDING_BASE_URL", "https://api.openai.com/v1"),

		Port: envOrDefault("RAGAMUFFIN_PORT", "8000"),
		Host: envOrDefault("RAGAMUFFIN_HOST", "0.0.0.0"),

		LLMProvider: os.Getenv("RAGAMUFFIN_LLM_PROVIDER"),
		LLMBaseURL:  os.Getenv("RAGAMUFFIN_LLM_BASE_URL"),
		LLMModel:    os.Getenv("RAGAMUFFIN_LLM_MODEL"),
		LLMAPIKey:   os.Getenv("RAGAMUFFIN_LLM_API_KEY"),

		GitProviderEnabled: envBool("RAGAMUFFIN_GIT_PROVIDER_ENABLED"),
		GitProvider:        envOrDefault("RAGAMUFFIN_GIT_PROVIDER", "github"),
		GitToken:           os.Getenv("RAGAMUFFIN_GIT_TOKEN"),
		GitBaseBranch:      envOrDefault("RAGAMUFFIN_GIT_BASE_BRANCH", "main"),
		GitRepos:           os.Getenv("RAGAMUFFIN_GIT_REPOS"),

		AuditSampleSize: envInt("RAGAMUFFIN_AUDIT_SAMPLE_SIZE", 50),
		LogLevel:        envOrDefault("RAGAMUFFIN_LOG_LEVEL", "info"),
	}
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("required environment variable not set: " + key)
	}
	return v
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
