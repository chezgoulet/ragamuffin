package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestLoad_RequiredEnv(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/test/vault")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
	defer os.Unsetenv("RAGAMUFFIN_QDRANT_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.VaultPath != "/test/vault" {
		t.Errorf("VaultPath = %q, want /test/vault", cfg.VaultPath)
	}
	if cfg.QdrantURL != "http://localhost:6334" {
		t.Errorf("QdrantURL = %q", cfg.QdrantURL)
	}
}

func TestLoad_Defaults(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/vault")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
	defer os.Unsetenv("RAGAMUFFIN_QDRANT_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.QdrantCollection != "ragamuffin" {
		t.Errorf("QdrantCollection = %q, want ragamuffin", cfg.QdrantCollection)
	}
	if cfg.WatchInterval != "60s" {
		t.Errorf("WatchInterval = %q, want 60s", cfg.WatchInterval)
	}
	if cfg.EmbeddingProvider != "openai" {
		t.Errorf("EmbeddingProvider = %q, want openai", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel = %q", cfg.EmbeddingModel)
	}
	if cfg.Port != "8000" {
		t.Errorf("Port = %q, want 8000", cfg.Port)
	}
	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host = %q, want 0.0.0.0", cfg.Host)
	}
	if cfg.GitProvider != "github" {
		t.Errorf("GitProvider = %q, want github", cfg.GitProvider)
	}
	if cfg.GitBaseBranch != "main" {
		t.Errorf("GitBaseBranch = %q, want main", cfg.GitBaseBranch)
	}
	if cfg.AuditSampleSize != 50 {
		t.Errorf("AuditSampleSize = %d, want 50", cfg.AuditSampleSize)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoad_CustomEnv(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/custom/vault")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://custom:6334")
	os.Setenv("RAGAMUFFIN_EMBEDDING_API_KEY", "sk-test")
	os.Setenv("RAGAMUFFIN_QDRANT_COLLECTION", "custom_coll")
	os.Setenv("RAGAMUFFIN_WATCH_INTERVAL", "30s")
	os.Setenv("RAGAMUFFIN_PORT", "9000")
	os.Setenv("RAGAMUFFIN_LOG_LEVEL", "debug")
	defer func() {
		for _, k := range []string{
			"RAGAMUFFIN_VAULT_PATH", "RAGAMUFFIN_QDRANT_URL",
			"RAGAMUFFIN_EMBEDDING_API_KEY", "RAGAMUFFIN_QDRANT_COLLECTION",
			"RAGAMUFFIN_WATCH_INTERVAL", "RAGAMUFFIN_PORT", "RAGAMUFFIN_LOG_LEVEL",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.EmbeddingAPIKey != "sk-test" {
		t.Errorf("EmbeddingAPIKey = %q", cfg.EmbeddingAPIKey)
	}
	if cfg.QdrantCollection != "custom_coll" {
		t.Errorf("QdrantCollection = %q", cfg.QdrantCollection)
	}
	if cfg.WatchInterval != "30s" {
		t.Errorf("WatchInterval = %q", cfg.WatchInterval)
	}
	if cfg.Port != "9000" {
		t.Errorf("Port = %q", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
}

func TestHasLLM(t *testing.T) {
	cfg := &Config{}
	if cfg.HasLLM() {
		t.Error("HasLLM() = true with no config")
	}

	cfg.LLMProvider = "openai_compatible"
	if cfg.HasLLM() {
		t.Error("HasLLM() = true with provider but no key")
	}

	cfg.LLMAPIKey = "sk-test"
	if !cfg.HasLLM() {
		t.Error("HasLLM() = false with provider and key")
	}
}

func TestHasGit(t *testing.T) {
	cfg := &Config{}
	if cfg.HasGit() {
		t.Error("HasGit() = true with no config")
	}

	cfg.GitProviderEnabled = true
	if cfg.HasGit() {
		t.Error("HasGit() = true with enabled but no token")
	}

	cfg.GitToken = "ghp_test"
	if !cfg.HasGit() {
		t.Error("HasGit() = false with enabled and token")
	}
}

func TestLoad_GitConfig(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/vault")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_GIT_PROVIDER_ENABLED", "true")
	os.Setenv("RAGAMUFFIN_GIT_TOKEN", "ghp_test")
	os.Setenv("RAGAMUFFIN_GIT_REPOS", "chezgoulet/vault")
	defer func() {
		for _, k := range []string{
			"RAGAMUFFIN_VAULT_PATH", "RAGAMUFFIN_QDRANT_URL",
			"RAGAMUFFIN_GIT_PROVIDER_ENABLED", "RAGAMUFFIN_GIT_TOKEN",
			"RAGAMUFFIN_GIT_REPOS",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if !cfg.GitProviderEnabled {
		t.Error("GitProviderEnabled = false")
	}
	if cfg.GitToken != "ghp_test" {
		t.Errorf("GitToken = %q", cfg.GitToken)
	}
	if cfg.GitRepos != "chezgoulet/vault" {
		t.Errorf("GitRepos = %q", cfg.GitRepos)
	}
	if !cfg.HasGit() {
		t.Error("HasGit() = false")
	}
}

func TestEnvBool(t *testing.T) {
	tests := []struct {
		val      string
		expected bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"False", false},
		{"0", false},
		{"", false},
		{"garbage", false},
	}

	for _, tt := range tests {
		os.Setenv("TEST_BOOL", tt.val)
		result := envBool("TEST_BOOL")
		if result != tt.expected {
			t.Errorf("envBool(%q) = %v, want %v", tt.val, result, tt.expected)
		}
	}
	os.Unsetenv("TEST_BOOL")
}

func TestEnvInt(t *testing.T) {
	os.Setenv("TEST_INT", "42")
	if v := envInt("TEST_INT", 10); v != 42 {
		t.Errorf("envInt = %d, want 42", v)
	}
	os.Unsetenv("TEST_INT")

	if v := envInt("MISSING", 10); v != 10 {
		t.Errorf("envInt default = %d, want 10", v)
	}

	os.Setenv("TEST_INT", "notanumber")
	if v := envInt("TEST_INT", 10); v != 10 {
		t.Errorf("envInt invalid = %d, want 10", v)
	}
	os.Unsetenv("TEST_INT")
}

func TestValidate_ValidConfig(t *testing.T) {
	dir := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidate_VaultPathMissing(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/nonexistent/path/12345")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for missing vault path")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "VAULT_PATH") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected VAULT_PATH error, got: %v", errs)
	}
}

func TestValidate_InvalidQdrantURL(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "not-a-url")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for invalid Qdrant URL")
	}
}

func TestValidate_NegativeEmbeddingDims(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_EMBEDDING_DIMS", "-1")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_EMBEDDING_DIMS")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for negative embedding dims")
	}
}

func TestValidate_InvalidWatchInterval(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_WATCH_INTERVAL", "not-a-duration")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_WATCH_INTERVAL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for invalid watch interval")
	}
}

func TestValidate_InvalidWatcherMode(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_WATCHER_MODE", "magic")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_WATCHER_MODE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for invalid watcher mode")
	}
}

func TestValidate_NegativeRateLimit(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_RATE_LIMIT_RECALL", "-5")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_RATE_LIMIT_RECALL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	errs := cfg.Validate()
	if len(errs) == 0 {
		t.Error("expected error for negative rate limit")
	}
}

func TestValidate_ValidWatcherModes(t *testing.T) {
	dir := t.TempDir()

	for _, mode := range []string{"poll", "inotify"} {
		os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
		os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
		os.Setenv("RAGAMUFFIN_WATCHER_MODE", mode)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() failed: %v", err)
		}
		errs := cfg.Validate()
		if len(errs) != 0 {
			t.Errorf("mode %q should be valid, got errors: %v", mode, errs)
		}

		os.Unsetenv("RAGAMUFFIN_WATCHER_MODE")
	}
	os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
	os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
}

func TestValidate_ZeroChunkMaxTokens(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("RAGAMUFFIN_VAULT_PATH", dir)
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_CHUNK_MAX_TOKENS", "0")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_CHUNK_MAX_TOKENS")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.ChunkMaxTokens != 0 {
		t.Errorf("ChunkMaxTokens = %d, want 0", cfg.ChunkMaxTokens)
	}
	errs := cfg.Validate()
	if len(errs) != 0 {
		t.Errorf("zero chunk max tokens should be valid, got: %v", errs)
	}
}

// ── Multi-tenancy tests ───────────────────────────────────────────────────────

func TestIsMultiTenant(t *testing.T) {
	cfg := &Config{}
	if cfg.IsMultiTenant() {
		t.Error("IsMultiTenant() = true with nil Vaults")
	}

	cfg.Vaults = map[string]*VaultConfig{}
	if cfg.IsMultiTenant() {
		t.Error("IsMultiTenant() = true with empty Vaults")
	}

	cfg.Vaults["docs"] = &VaultConfig{Path: "/opt/docs"}
	if !cfg.IsMultiTenant() {
		t.Error("IsMultiTenant() = false with one vault")
	}
}

func TestLoad_MultiTenant(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULTS", fmt.Sprintf("docs:%s,code:%s", dir1, dir2))
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_CHUNK_STRATEGY", "heading")
	os.Setenv("RAGAMUFFIN_VAULT_CODE_CHUNK_FIXED_SIZE", "500")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULTS")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_VAULT_DOCS_CHUNK_STRATEGY")
		os.Unsetenv("RAGAMUFFIN_VAULT_CODE_CHUNK_FIXED_SIZE")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if !cfg.IsMultiTenant() {
		t.Fatal("expected multi-tenant mode")
	}

	docs, ok := cfg.Vaults["docs"]
	if !ok {
		t.Fatal("expected vault 'docs'")
	}
	if docs.Path != dir1 {
		t.Errorf("docs path = %q, want %q", docs.Path, dir1)
	}
	if docs.ChunkStrategy != "heading" {
		t.Errorf("docs ChunkStrategy = %q, want heading", docs.ChunkStrategy)
	}

	code, ok := cfg.Vaults["code"]
	if !ok {
		t.Fatal("expected vault 'code'")
	}
	if code.Path != dir2 {
		t.Errorf("code path = %q, want %q", code.Path, dir2)
	}
	if code.ChunkFixedSize != 500 {
		t.Errorf("code ChunkFixedSize = %d, want 500", code.ChunkFixedSize)
	}
}

func TestLoad_InvalidVaultEntry(t *testing.T) {
	tests := []string{
		"badentry",                        // no colon
		":/path",                           // empty name
		"docs:",                            // empty path
		"docs:/opt/docs,finance:",           // second entry empty path
	}

	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer os.Unsetenv("RAGAMUFFIN_QDRANT_URL")

	for _, entry := range tests {
		os.Setenv("RAGAMUFFIN_VAULTS", entry)
		_, err := Load()
		if err == nil {
			t.Errorf("expected error for vault entry %q", entry)
		}
		os.Unsetenv("RAGAMUFFIN_VAULTS")
	}
}

func TestLoad_DuplicateVaultName(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULTS", "docs:/opt/a,test:/opt/b,test:/opt/c")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULTS")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
	}()

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "duplicate vault name") {
		t.Errorf("expected duplicate vault name error, got: %v", err)
	}
}

func TestValidate_MutuallyExclusive(t *testing.T) {
	cfg := &Config{
		VaultPath: "/opt/vault",
		Vaults:    map[string]*VaultConfig{"docs": {Path: "/opt/docs"}},
		QdrantURL: "http://localhost:6334",
	}
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "mutually exclusive") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mutually exclusive error, got: %v", errs)
	}
}

func TestValidate_NeitherPathNorVaults(t *testing.T) {
	cfg := &Config{QdrantURL: "http://localhost:6334"}
	errs := cfg.Validate()
	found := false
	for _, e := range errs {
		if strings.Contains(e, "must set either") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'must set either' error, got: %v", errs)
	}
}

func TestVaultConfig_HasLLM(t *testing.T) {
	vc := &VaultConfig{}
	if vc.HasLLM() {
		t.Error("HasLLM() = true with empty VaultConfig")
	}

	vc.LLMEndpoint = "http://localhost:8080"
	if vc.HasLLM() {
		t.Error("HasLLM() = true with endpoint but no key")
	}

	vc.LLMApiKey = "sk-test"
	if !vc.HasLLM() {
		t.Error("HasLLM() = false with endpoint and key")
	}
}

func TestVaultConfig_HasEmbedding(t *testing.T) {
	vc := &VaultConfig{}
	if vc.HasEmbedding() {
		t.Error("HasEmbedding() = true with empty VaultConfig")
	}

	vc.EmbeddingEndpoint = "http://localhost:8080"
	if vc.HasEmbedding() {
		t.Error("HasEmbedding() = true with endpoint but no key")
	}

	vc.EmbeddingApiKey = "sk-test"
	if !vc.HasEmbedding() {
		t.Error("HasEmbedding() = false with endpoint and key")
	}
}

func TestLoad_MultiTenantWithLLMOverride(t *testing.T) {
	dir1 := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULTS", fmt.Sprintf("docs:%s", dir1))
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_PROVIDER", "openai")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT", "https://custom-llm.example.com")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_API_KEY", "sk-custom")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_MODEL", "gpt-4o")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_TIMEOUT", "30s")
	defer func() {
		for _, k := range []string{
			"RAGAMUFFIN_VAULTS", "RAGAMUFFIN_QDRANT_URL",
			"RAGAMUFFIN_VAULT_DOCS_LLM_PROVIDER",
			"RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT",
			"RAGAMUFFIN_VAULT_DOCS_LLM_API_KEY",
			"RAGAMUFFIN_VAULT_DOCS_LLM_MODEL",
			"RAGAMUFFIN_VAULT_DOCS_LLM_TIMEOUT",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	docs, ok := cfg.Vaults["docs"]
	if !ok {
		t.Fatal("expected vault 'docs'")
	}

	if docs.LLMProvider != "openai" {
		t.Errorf("LLMProvider = %q, want openai", docs.LLMProvider)
	}
	if docs.LLMEndpoint != "https://custom-llm.example.com" {
		t.Errorf("LLMEndpoint = %q", docs.LLMEndpoint)
	}
	if docs.LLMApiKey != "sk-custom" {
		t.Errorf("LLMApiKey = %q", docs.LLMApiKey)
	}
	if docs.LLMModel != "gpt-4o" {
		t.Errorf("LLMModel = %q", docs.LLMModel)
	}
	if docs.LLMTimeout != 30*time.Second {
		t.Errorf("LLMTimeout = %v", docs.LLMTimeout)
	}

	if !docs.HasLLM() {
		t.Error("HasLLM() = false, expected true")
	}
}

func TestLoad_MultiTenantWithEmbeddingOverride(t *testing.T) {
	dir1 := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULTS", fmt.Sprintf("docs:%s", dir1))
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_EMBEDDING_ENDPOINT", "https://custom-embed.example.com")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_EMBEDDING_API_KEY", "sk-embed-custom")
	defer func() {
		for _, k := range []string{
			"RAGAMUFFIN_VAULTS", "RAGAMUFFIN_QDRANT_URL",
			"RAGAMUFFIN_VAULT_DOCS_EMBEDDING_ENDPOINT",
			"RAGAMUFFIN_VAULT_DOCS_EMBEDDING_API_KEY",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	docs, ok := cfg.Vaults["docs"]
	if !ok {
		t.Fatal("expected vault 'docs'")
	}

	if docs.EmbeddingEndpoint != "https://custom-embed.example.com" {
		t.Errorf("EmbeddingEndpoint = %q", docs.EmbeddingEndpoint)
	}
	if docs.EmbeddingApiKey != "sk-embed-custom" {
		t.Errorf("EmbeddingApiKey = %q", docs.EmbeddingApiKey)
	}

	if !docs.HasEmbedding() {
		t.Error("HasEmbedding() = false, expected true")
	}
}

func TestLoad_MultiTenantPartialLLMOverride(t *testing.T) {
	// Setting LLM_ENDPOINT without LLM_API_KEY should not enable HasLLM()
	dir1 := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULTS", fmt.Sprintf("docs:%s", dir1))
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT", "https://custom-llm.example.com")
	defer func() {
		os.Unsetenv("RAGAMUFFIN_VAULTS")
		os.Unsetenv("RAGAMUFFIN_QDRANT_URL")
		os.Unsetenv("RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	docs, ok := cfg.Vaults["docs"]
	if !ok {
		t.Fatal("expected vault 'docs'")
	}

	if docs.LLMEndpoint != "https://custom-llm.example.com" {
		t.Errorf("LLMEndpoint = %q", docs.LLMEndpoint)
	}
	if docs.HasLLM() {
		t.Error("HasLLM() = true with endpoint but no key")
	}
}

func TestVaultConfig_HasLLM_InvalidTimeout(t *testing.T) {
	dir1 := t.TempDir()

	os.Setenv("RAGAMUFFIN_VAULTS", fmt.Sprintf("docs:%s", dir1))
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_TIMEOUT", "not-a-duration")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT", "http://localhost:8080")
	os.Setenv("RAGAMUFFIN_VAULT_DOCS_LLM_API_KEY", "sk-test")
	defer func() {
		for _, k := range []string{
			"RAGAMUFFIN_VAULTS", "RAGAMUFFIN_QDRANT_URL",
			"RAGAMUFFIN_VAULT_DOCS_LLM_TIMEOUT",
			"RAGAMUFFIN_VAULT_DOCS_LLM_ENDPOINT",
			"RAGAMUFFIN_VAULT_DOCS_LLM_API_KEY",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	docs := cfg.Vaults["docs"]
	if docs.LLMTimeout != 0 {
		t.Errorf("expected zero timeout for invalid duration, got %v", docs.LLMTimeout)
	}
	if !docs.HasLLM() {
		t.Error("HasLLM() = false with endpoint and key set")
	}
}

func TestValidVaultName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"docs", true},
		{"my-vault", true},
		{"vault123", true},
		{"a", true},
		{"a-b", true},
		{"Docs", false},           // uppercase
		{"my_vault", false},       // underscore
		{"-docs", false},          // leading hyphen
		{"docs-", false},          // trailing hyphen
		{"", false},               // empty
		{"a", true},               // single char
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false}, // 33 chars
		{"a-b-c-d-e-f-g", true},  // 13 chars, multiple hyphens
		{"no spaces", false},      // space
		{"docs!", false},          // special char
	}

	for _, tt := range tests {
		got := ValidVaultName(tt.name)
		if got != tt.valid {
			t.Errorf("ValidVaultName(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

func TestValidate_MultiTenantVaultPaths(t *testing.T) {
	existingDir := t.TempDir()

	cfg := &Config{
		QdrantURL: "http://localhost:6334",
		Vaults: map[string]*VaultConfig{
			"good":    {Path: existingDir},
			"missing": {Path: "/nonexistent/path/xyz789"},
		},
	}
	errs := cfg.Validate()
	foundMissing := false
	foundGood := true
	for _, e := range errs {
		if strings.Contains(e, "missing") && strings.Contains(e, "does not exist") {
			foundMissing = true
		}
		if strings.Contains(e, "good") {
			foundGood = false
		}
	}
	if !foundMissing {
		t.Errorf("expected error for missing vault path, got: %v", errs)
	}
	if !foundGood {
		t.Errorf("unexpected error for valid vault path: %v", errs)
	}
}
