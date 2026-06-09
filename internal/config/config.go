package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// VaultConfig holds per-vault configuration, including overrides.
type VaultConfig struct {
	Path                 string
	ChunkStrategy        string
	ChunkMaxTokens       int
	ChunkFixedSize       int
	ChunkFixedOverlap    int
	EmbeddingModel       string
	EmbeddingDims        int
	AuditEntityExtraction bool
	AuditEntityLLM       string

	// Per-vault LLM overrides. Empty fields fall through to server defaults.
	LLMProvider       string
	LLMEndpoint       string
	LLMApiKey         string
	LLMModel          string
	LLMTimeout        time.Duration

	// Per-vault embedding overrides. Empty = use server default.
	EmbeddingEndpoint string
	EmbeddingApiKey   string
	EmbeddingTimeout  time.Duration
}

// HasLLM returns true if per-vault LLM config is provided.
func (vc *VaultConfig) HasLLM() bool {
	return vc.LLMEndpoint != "" && vc.LLMApiKey != ""
}

// HasEmbedding returns true if per-vault embedding config is provided.
func (vc *VaultConfig) HasEmbedding() bool {
	return vc.EmbeddingEndpoint != "" && vc.EmbeddingApiKey != ""
}

// Config holds all Ragamuffin configuration, parsed from environment variables.
type Config struct {
	// Required (single-tenant mode)
	VaultPath string

	// Multi-tenancy — mutually exclusive with VaultPath
	Vaults map[string]*VaultConfig // vault name → config (v0.4+)
	// VaultsRoot restricts runtime vault creation paths (handleCreateVault).
	// Only used in multi-tenant mode (#413).
	VaultsRoot string
	// MultiTenantMode is true when the server should operate in multi-tenant
	// mode even without explicit RAGAMUFFIN_VAULTS entries. Triggered by
	// setting RAGAMUFFIN_VAULTS_ROOT + RAGAMUFFIN_AUTO_PROVISION_VAULTS=true.
	MultiTenantMode bool

	// Required
	QdrantURL       string
	EmbeddingAPIKey string

	// Optional — Qdrant
	QdrantCollection string
	FactsCollection  string
	FactsVectorSize  uint64

	// Optional — Watcher
	WatchInterval string
	WatcherMode   string

	// Optional — Embedding
	EmbeddingProvider string
	EmbeddingModel    string
	EmbeddingBaseURL  string
	EmbeddingDims     int
	EmbeddingTimeout  time.Duration
	ChunkVectorSize   uint64 // vector dimension for chunk/doc collections (defaults to EmbeddingDims)

	// Optional — Chunking
	ChunkStrategy  string
	ChunkMaxTokens int

	// Optional — Server
	Port string
	Host string

	// Optional — Rate Limiting
	RateLimitEnabled  bool
	RateLimitRecall   int
	RateLimitAsk      int
	RateLimitDraft    int
	RateLimitAudit    int
	RateLimitFacts    int
	RateLimitLogs     int
	RateLimitSnapshot int
	RateLimitReindex   int
	RateLimitIngest    int
	RateLimitReview    int

	// Pruner configuration
	PrunerEnabled                bool
	PrunerStaleInterval          time.Duration
	PrunerConflictInterval       time.Duration
	PrunerSupersedeInterval      time.Duration
	PrunerSourceStaleInterval    time.Duration
	PrunerExpiredInterval        time.Duration
	PrunerStaleDays              int
	PrunerConflictSampleSize     int
	PrunerLowConfidenceThreshold float64
	PrunerConflictThreshold     float64
	PrunerImportanceThreshold   float64

	// Automatic extraction from conversation turns
	ExtractEnabled            bool
	ExtractWindow             int
	ExtractMaxConfidence      float64
	ExtractDedupThreshold     float64
	ExtractConcurrency        int
	ExtractPerSessionCooldown int

	RestoreMismatchThreshold float64 // 0.0-1.0, default 0.1
	LogStorePath        string          // explicit path for log.db; empty = heuristic
	LogstoreMaxRows     int             // 0 = unlimited

	// Optional — LLM
	LLMProvider string
	LLMBaseURL  string
	LLMModel    string
	LLMAPIKey   string
	LLMTimeout      time.Duration
	EventWebhookURL    string
	EventWebhookEvents []string

	// Optional — Git
	GitProviderEnabled bool
	GitProvider        string
	GitToken           string
	GitBaseBranch      string
	GitBaseURL         string
	GitRepos           string

	// Optional — Webhook ingestion
	WebhookVaultMap map[string]string // repo → vault mapping from RAGAMUFFIN_WEBHOOK_VAULT_MAP

	// Optional — Audit / Tuning
	AuditSampleSize int
	AutoThreshold   float64

	// Optional — Auth
	AuthMode           string
	AuthReadKey        string
	AuthWriteKey       string
	AuthJWTIssuer      string
	AuthJWTAudience    string
	AuthJWKSURL        string
	AuthOIDCIssuer     string
	AuthOIDCClientID   string
	AutoProvisionVaults bool

	// Optional — Logging
	LogLevel string
}

// IsMultiTenant returns true when multi-tenancy is active.
// Triggered by RAGAMUFFIN_VAULTS or by RAGAMUFFIN_VAULTS_ROOT +
// RAGAMUFFIN_AUTO_PROVISION_VAULTS=true (#524).
func (c *Config) IsMultiTenant() bool {
	return len(c.Vaults) > 0 ||
		(c.VaultsRoot != "" && c.AutoProvisionVaults)
}

// HasLLM returns true if an LLM is configured.
func (c *Config) HasLLM() bool {
	return c.LLMProvider != "" && c.LLMAPIKey != ""
}

// HasGit returns true if git provider is enabled and configured.
func (c *Config) HasGit() bool {
	return c.GitProviderEnabled && c.GitToken != ""
}

func ValidVaultName(name string) bool {
	if len(name) == 0 || len(name) > 32 {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' && i > 0 && i < len(name)-1 {
			continue // hyphens allowed, but not leading or trailing
		}
		return false
	}
	return true
}

// FactsCollectionFor returns the vault-specific facts collection name.
// Falls back to c.FactsCollection when vault name is empty.
func (c *Config) FactsCollectionFor(vaultName string) string {
	if vaultName != "" {
		return fmt.Sprintf("ragamuffin_%s_facts", vaultName)
	}
	return c.FactsCollection
}

// Validate checks configuration and returns a list of fatal errors.
// Returns nil if the configuration is valid.
func (c *Config) Validate() []string {
	var errs []string

	// Single-tenant vs multi-tenant: must pick one
	if c.VaultPath != "" && c.IsMultiTenant() {
		errs = append(errs, "RAGAMUFFIN_VAULT_PATH and RAGAMUFFIN_VAULTS are mutually exclusive — set one or the other")
	} else if c.IsMultiTenant() {
		// Multi-tenant: validate all vault paths exist
		for name, vc := range c.Vaults {
			if !ValidVaultName(name) {
				errs = append(errs, fmt.Sprintf("invalid vault name %q: must be lowercase alphanumeric with hyphens, max 32 chars", name))
				continue
			}
			if info, err := os.Stat(vc.Path); err != nil {
				errs = append(errs, fmt.Sprintf("vault %q path %q does not exist or is not readable", name, vc.Path))
			} else if !info.IsDir() {
				errs = append(errs, fmt.Sprintf("vault %q path %q is not a directory", name, vc.Path))
			}
		}
	} else if c.VaultPath != "" {
		// Single-tenant: vault path must exist
		if info, err := os.Stat(c.VaultPath); err != nil {
			errs = append(errs, fmt.Sprintf("RAGAMUFFIN_VAULT_PATH %q does not exist or is not readable", c.VaultPath))
		} else if !info.IsDir() {
			errs = append(errs, fmt.Sprintf("RAGAMUFFIN_VAULT_PATH %q is not a directory", c.VaultPath))
		}
	} else {
		errs = append(errs, "must set either RAGAMUFFIN_VAULT_PATH (single-tenant) or RAGAMUFFIN_VAULTS (multi-tenant)")
	}

	// Qdrant URL must be parseable
	if _, err := parseURL(c.QdrantURL); err != nil {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_QDRANT_URL %q is not a valid URL: %v", c.QdrantURL, err))
	}

	// Embedding dims must be positive (only valid for single-tenant or instance-level)
	if !c.IsMultiTenant() && c.EmbeddingDims <= 0 {
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
	if c.FactsVectorSize == 0 {
		errs = append(errs, "RAGAMUFFIN_FACTS_VECTOR_SIZE must be positive, got 0")
	}
	if c.RateLimitFacts < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_FACTS must be non-negative, got %d", c.RateLimitFacts))
	}
	if c.RateLimitLogs < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_LOGS must be non-negative, got %d", c.RateLimitLogs))
	}
	if c.RateLimitSnapshot < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_SNAPSHOT must be non-negative, got %d", c.RateLimitSnapshot))
	}
	if c.RateLimitReindex < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_REINDEX must be non-negative, got %d", c.RateLimitReindex))
	}
	if c.RateLimitIngest < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_INGEST must be non-negative, got %d", c.RateLimitIngest))
	}
	if c.RateLimitReview < 0 {
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_RATE_LIMIT_REVIEW must be non-negative, got %d", c.RateLimitReview))
	}

	// Pruner config: StaleDays must be positive if pruner is enabled
	if c.PrunerEnabled && c.PrunerStaleDays <= 0 {
		errs = append(errs, "RAGAMUFFIN_PRUNER_STALE_DAYS must be positive when pruner is enabled")
	}

	// Auth mode must be valid
	switch strings.ToLower(c.AuthMode) {
	case "none", "api_key", "jwt", "oidc":
		// valid
	default:
		errs = append(errs, fmt.Sprintf("RAGAMUFFIN_AUTH_MODE must be 'none', 'api_key', 'jwt', or 'oidc', got %q", c.AuthMode))
	}

	// OIDC mode requires issuer and client ID
	if strings.ToLower(c.AuthMode) == "oidc" && c.AuthOIDCIssuer == "" {
		errs = append(errs, "RAGAMUFFIN_AUTH_OIDC_ISSUER is required for OIDC mode")
	}

	return errs
}

func parseURL(raw string) (interface{}, error) {
	// Bare host:port or host — valid gRPC address, skip URL validation
	if !strings.Contains(raw, "://") && strings.Contains(raw, ":") {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("malformed URL: %w", err)
	}
	if u.Scheme == "" {
		return nil, fmt.Errorf("missing scheme: %q", raw)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host: %q", raw)
	}
	return u, nil
}

// Load reads configuration from environment variables with defaults.
// RAGAMUFFIN_VAULT_PATH (single-tenant) and RAGAMUFFIN_VAULTS (multi-tenant)
// are mutually exclusive. One must be set.
func Load() (*Config, error) {
	qdrantURL, err := requireEnv("RAGAMUFFIN_QDRANT_URL")
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		QdrantURL:       qdrantURL,
		EmbeddingAPIKey: os.Getenv("RAGAMUFFIN_EMBEDDING_API_KEY"),

		QdrantCollection: envOrDefault("RAGAMUFFIN_QDRANT_COLLECTION", "ragamuffin"),
		FactsCollection:  envOrDefault("RAGAMUFFIN_FACTS_COLLECTION", "ragamuffin_facts"),
		FactsVectorSize:  uint64(envInt("RAGAMUFFIN_FACTS_VECTOR_SIZE", envInt("RAGAMUFFIN_EMBEDDING_DIMS", 1536))),
		WatchInterval:    envOrDefault("RAGAMUFFIN_WATCH_INTERVAL", "60s"),
		WatcherMode:      envOrDefault("RAGAMUFFIN_WATCHER_MODE", "poll"),

		EmbeddingProvider: envOrDefault("RAGAMUFFIN_EMBEDDING_PROVIDER", "openai"),
		EmbeddingModel:    envOrDefault("RAGAMUFFIN_EMBEDDING_MODEL", "text-embedding-3-small"),
		EmbeddingBaseURL:  envOrDefault("RAGAMUFFIN_EMBEDDING_BASE_URL", "https://api.openai.com/v1"),
		EmbeddingDims:     envInt("RAGAMUFFIN_EMBEDDING_DIMS", 1536),
		EmbeddingTimeout:  envDuration("RAGAMUFFIN_EMBEDDING_TIMEOUT", 30*time.Second),
		ChunkVectorSize:   uint64(envInt("RAGAMUFFIN_CHUNK_VECTOR_SIZE", 0)),

		ChunkMaxTokens: envInt("RAGAMUFFIN_CHUNK_MAX_TOKENS", 2000),

		Port: envOrDefault("RAGAMUFFIN_PORT", "8000"),
		Host: envOrDefault("RAGAMUFFIN_HOST", "0.0.0.0"),

		RateLimitEnabled:  envBool("RAGAMUFFIN_RATE_LIMIT_ENABLED"),
		RateLimitRecall:   envInt("RAGAMUFFIN_RATE_LIMIT_RECALL", 60),
		RateLimitAsk:      envInt("RAGAMUFFIN_RATE_LIMIT_ASK", 10),
		RateLimitDraft:    envInt("RAGAMUFFIN_RATE_LIMIT_DRAFT", 30),
		RateLimitAudit:    envInt("RAGAMUFFIN_RATE_LIMIT_AUDIT", 5),
		RateLimitFacts:    envInt("RAGAMUFFIN_RATE_LIMIT_FACTS", 30),
		RateLimitLogs:     envInt("RAGAMUFFIN_RATE_LIMIT_LOGS", 60),
		RateLimitSnapshot: envInt("RAGAMUFFIN_RATE_LIMIT_SNAPSHOT", 5),
		RateLimitReindex:   envInt("RAGAMUFFIN_RATE_LIMIT_REINDEX", 30),
		RateLimitIngest:    envInt("RAGAMUFFIN_RATE_LIMIT_INGEST", 30),
		RateLimitReview:    envInt("RAGAMUFFIN_RATE_LIMIT_REVIEW", 30),

		PrunerEnabled:                envBool("RAGAMUFFIN_PRUNER_ENABLED"),
		PrunerStaleInterval:          envDuration("RAGAMUFFIN_PRUNER_STALE_INTERVAL", 24*time.Hour),
		PrunerConflictInterval:       envDuration("RAGAMUFFIN_PRUNER_CONFLICT_INTERVAL", 72*time.Hour),
		PrunerSupersedeInterval:      envDuration("RAGAMUFFIN_PRUNER_SUPERSEDE_INTERVAL", 24*time.Hour),
		PrunerStaleDays:              envInt("RAGAMUFFIN_PRUNER_STALE_DAYS", 90),
		PrunerConflictThreshold:      envFloat("RAGAMUFFIN_PRUNER_CONFLICT_THRESHOLD", 0.85),
		PrunerSourceStaleInterval:    envDuration("RAGAMUFFIN_PRUNER_SOURCE_STALE_INTERVAL", 0),
		PrunerExpiredInterval:        envDuration("RAGAMUFFIN_PRUNER_EXPIRED_INTERVAL", 24*time.Hour),
		PrunerConflictSampleSize:     envInt("RAGAMUFFIN_PRUNER_CONFLICT_SAMPLE_SIZE", 50),
		PrunerLowConfidenceThreshold: envFloat("RAGAMUFFIN_PRUNER_LOW_CONFIDENCE_THRESHOLD", 0.5),
		PrunerImportanceThreshold:   envFloat("RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD", 0.0),
		ExtractEnabled:            envBool("RAGAMUFFIN_EXTRACT_ENABLED"),
		ExtractWindow:             envInt("RAGAMUFFIN_EXTRACT_WINDOW", 10),
		ExtractMaxConfidence:      math.Min(envFloat("RAGAMUFFIN_EXTRACT_MAX_CONFIDENCE", 0.85), 0.85),
		ExtractDedupThreshold:     envFloat("RAGAMUFFIN_EXTRACT_DEDUP_THRESHOLD", 0.85),
		ExtractConcurrency:        envInt("RAGAMUFFIN_EXTRACT_CONCURRENCY", 2),
		ExtractPerSessionCooldown: envInt("RAGAMUFFIN_EXTRACT_PER_SESSION_COOLDOWN", 30),
		RestoreMismatchThreshold:     envFloat("RAGAMUFFIN_RESTORE_MISMATCH_THRESHOLD", 0.1),
		LogStorePath:        os.Getenv("RAGAMUFFIN_LOGSTORE_PATH"),
		LogstoreMaxRows:       envInt("RAGAMUFFIN_LOGSTORE_MAX_ROWS", 100000),

		LLMProvider: os.Getenv("RAGAMUFFIN_LLM_PROVIDER"),
		LLMBaseURL:  envOrDefault("RAGAMUFFIN_LLM_BASE_URL", "https://api.deepseek.com"), // NOTE: code appends "/v1/chat/completions", so omit "/v1" here
		LLMModel:    os.Getenv("RAGAMUFFIN_LLM_MODEL"),
		LLMAPIKey:   os.Getenv("RAGAMUFFIN_LLM_API_KEY"),
		LLMTimeout:         envDuration("RAGAMUFFIN_LLM_TIMEOUT", 120*time.Second),
		EventWebhookURL:    os.Getenv("RAGAMUFFIN_EVENT_WEBHOOK_URL"),
		EventWebhookEvents: envCSV("RAGAMUFFIN_EVENT_WEBHOOK_EVENTS"),

		GitProviderEnabled: envBool("RAGAMUFFIN_GIT_PROVIDER_ENABLED"),
		GitProvider:        envOrDefault("RAGAMUFFIN_GIT_PROVIDER", "github"),
		GitToken:           os.Getenv("RAGAMUFFIN_GIT_TOKEN"),
		GitBaseBranch:      envOrDefault("RAGAMUFFIN_GIT_BASE_BRANCH", "main"),
		GitBaseURL:         os.Getenv("RAGAMUFFIN_GIT_BASE_URL"),
		GitRepos:           os.Getenv("RAGAMUFFIN_GIT_REPOS"),

		WebhookVaultMap: parseJSONMap("RAGAMUFFIN_WEBHOOK_VAULT_MAP"),

		AuthMode:         envOrDefault("RAGAMUFFIN_AUTH_MODE", "none"),
		AuthReadKey:      os.Getenv("RAGAMUFFIN_AUTH_READ_KEY"),
		AuthWriteKey:     os.Getenv("RAGAMUFFIN_AUTH_WRITE_KEY"),
		AuthJWTIssuer:    os.Getenv("RAGAMUFFIN_AUTH_JWT_ISSUER"),
		AuthJWTAudience:  os.Getenv("RAGAMUFFIN_AUTH_JWT_AUDIENCE"),
		AuthJWKSURL:      os.Getenv("RAGAMUFFIN_AUTH_JWT_JWKS_URL"),
		AuthOIDCIssuer:   os.Getenv("RAGAMUFFIN_AUTH_OIDC_ISSUER"),
		AutoProvisionVaults: envBool("RAGAMUFFIN_AUTO_PROVISION_VAULTS"),
	AuthOIDCClientID:    os.Getenv("RAGAMUFFIN_AUTH_OIDC_CLIENT_ID"),

		AuditSampleSize: envInt("RAGAMUFFIN_AUDIT_SAMPLE_SIZE", 50),
		AutoThreshold:   envFloat("RAGAMUFFIN_AUTO_THRESHOLD", 0.75),
		LogLevel:        envOrDefault("RAGAMUFFIN_LOG_LEVEL", "info"),
	}

	// Parse vaults root for multi-tenant path validation
	cfg.VaultsRoot = os.Getenv("RAGAMUFFIN_VAULTS_ROOT")

	// If VaultsRoot + AutoProvision are set, enter multi-tenant mode
	// even without explicit RAGAMUFFIN_VAULTS entries (#524).
	if cfg.VaultsRoot != "" && cfg.AutoProvisionVaults {
		cfg.MultiTenantMode = true
	}

	// Parse multi-tenancy if vaults are explicitly set or auto-provision mode is active
	vaultsRaw := os.Getenv("RAGAMUFFIN_VAULTS")
	if vaultsRaw != "" || cfg.MultiTenantMode {
		vaults := make(map[string]*VaultConfig)
		for _, entry := range strings.Split(vaultsRaw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			parts := strings.SplitN(entry, ":", 2)
			name := strings.TrimSpace(parts[0])
			if name == "" {
				return nil, fmt.Errorf("invalid vault entry %q in RAGAMUFFIN_VAULTS: empty name", entry)
			}
			var path string
			if len(parts) == 2 && parts[1] != "" {
				path = strings.TrimSpace(parts[1])
			} else {
				// Auto-derive path from VaultsRoot or default to /opt/vault/<name> (#522)
				root := cfg.VaultsRoot
				if root == "" {
					root = "/opt/vault"
				}
				path = filepath.Join(root, name)
			}
			if _, exists := vaults[name]; exists {
				return nil, fmt.Errorf("duplicate vault name %q in RAGAMUFFIN_VAULTS", name)
			}
			vc := &VaultConfig{Path: path}

			// Parse per-vault overrides: RAGAMUFFIN_VAULT_{NAME}_{SETTING}
			prefix := fmt.Sprintf("RAGAMUFFIN_VAULT_%s_", strings.ToUpper(name))
			for _, e := range os.Environ() {
				if !strings.HasPrefix(e, prefix) {
					continue
				}
				kv := strings.SplitN(e, "=", 2)
				if len(kv) != 2 {
					continue
				}
				key := strings.TrimPrefix(kv[0], prefix)
				val := kv[1]
				switch key {
				case "CHUNK_STRATEGY":
					vc.ChunkStrategy = val
				case "CHUNK_MAX_TOKENS":
					if n, err := strconv.Atoi(val); err == nil {
						vc.ChunkMaxTokens = n
					}
				case "CHUNK_FIXED_SIZE":
					if n, err := strconv.Atoi(val); err == nil {
						vc.ChunkFixedSize = n
					}
				case "CHUNK_FIXED_OVERLAP":
					if n, err := strconv.Atoi(val); err == nil {
						vc.ChunkFixedOverlap = n
					}
				case "EMBEDDING_MODEL":
					vc.EmbeddingModel = val
				case "EMBEDDING_DIMS":
					if n, err := strconv.Atoi(val); err == nil {
						vc.EmbeddingDims = n
					}
				case "LLM_PROVIDER":
					vc.LLMProvider = val
				case "LLM_ENDPOINT":
					vc.LLMEndpoint = val
				case "LLM_API_KEY":
					vc.LLMApiKey = val
				case "LLM_MODEL":
					vc.LLMModel = val
				case "LLM_TIMEOUT":
					if d, err := time.ParseDuration(val); err == nil {
						vc.LLMTimeout = d
					}
				case "EMBEDDING_ENDPOINT":
					vc.EmbeddingEndpoint = val
				case "EMBEDDING_API_KEY":
					vc.EmbeddingApiKey = val
				case "EMBEDDING_TIMEOUT":
					if d, err := time.ParseDuration(val); err == nil {
						vc.EmbeddingTimeout = d
					}
				case "AUDIT_ENTITY_EXTRACTION":
					vc.AuditEntityExtraction = val == "true" || val == "1"
				case "AUDIT_ENTITY_LLM":
					vc.AuditEntityLLM = val
				}
			}

			vaults[name] = vc
		}
		cfg.Vaults = vaults
	} else {
		// Single-tenant mode
		vaultPath, err := requireEnv("RAGAMUFFIN_VAULT_PATH")
		if err != nil {
			return nil, err
		}
		cfg.VaultPath = vaultPath
	}

	return cfg, nil
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

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
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

func envCSV(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

func parseJSONMap(key string) map[string]string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}
