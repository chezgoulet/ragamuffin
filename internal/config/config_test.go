package config

import (
	"os"
	"testing"
)

func TestLoad_RequiredEnv(t *testing.T) {
	os.Setenv("RAGAMUFFIN_VAULT_PATH", "/test/vault")
	os.Setenv("RAGAMUFFIN_QDRANT_URL", "http://localhost:6334")
	defer os.Unsetenv("RAGAMUFFIN_VAULT_PATH")
	defer os.Unsetenv("RAGAMUFFIN_QDRANT_URL")

	cfg := Load()

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

	cfg := Load()

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

	cfg := Load()

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

	cfg := Load()

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
