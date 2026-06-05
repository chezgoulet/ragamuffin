package indexer

import (
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
)

// testLLM creates a non-nil LLM client for testing.
func testLLM() *llm.Client {
	return llm.New("test-provider", "http://localhost", "sk-test", "gpt-4o", 30*time.Second)
}

// testEmbedder creates a non-nil embedding client for testing.
func testEmbedder() *embedding.Client {
	return embedding.New("http://localhost", "sk-test", "text-embedding-3-small", 0)
}

// ── SetLLM / GetLLM ──────────────────────────────────────────────────────────

func TestManager_SetLLM_Stores(t *testing.T) {
	m := NewManager()
	lm := testLLM()

	m.SetLLM("docs", lm)
	got := m.GetLLM("docs")
	if got != lm {
		t.Error("GetLLM returned different client")
	}
}

func TestManager_SetLLM_Replace(t *testing.T) {
	m := NewManager()
	lm1 := testLLM()
	lm2 := testLLM()

	m.SetLLM("docs", lm1)
	m.SetLLM("docs", lm2)

	got := m.GetLLM("docs")
	if got != lm2 {
		t.Error("SetLLM did not replace existing client")
	}
}

func TestManager_SetLLM_NilClears(t *testing.T) {
	m := NewManager()
	m.SetLLM("docs", testLLM())

	m.SetLLM("docs", nil)
	got := m.GetLLM("docs")
	if got != nil {
		t.Error("expected nil after clearing with SetLLM(docs, nil)")
	}
}

func TestManager_GetLLM_Missing(t *testing.T) {
	m := NewManager()
	if got := m.GetLLM("nonexistent"); got != nil {
		t.Error("expected nil for missing vault")
	}
}

func TestManager_GetLLM_DefaultNotSet(t *testing.T) {
	m := NewManager()
	if got := m.GetLLM("default"); got != nil {
		t.Error("expected nil for default vault before SetLLM")
	}
}

func TestManager_SetLLM_MultipleVaults(t *testing.T) {
	m := NewManager()
	docsLm := testLLM()
	codeLm := testLLM()

	m.SetLLM("docs", docsLm)
	m.SetLLM("code", codeLm)

	if got := m.GetLLM("docs"); got != docsLm {
		t.Error("docs vault returned wrong client")
	}
	if got := m.GetLLM("code"); got != codeLm {
		t.Error("code vault returned wrong client")
	}
}

// ── SetEmbedder / GetEmbedder ─────────────────────────────────────────────────

func TestManager_SetEmbedder_Stores(t *testing.T) {
	m := NewManager()
	ec := testEmbedder()

	m.SetEmbedder("docs", ec)
	got := m.GetEmbedder("docs")
	if got != ec {
		t.Error("GetEmbedder returned different client")
	}
}

func TestManager_SetEmbedder_Replace(t *testing.T) {
	m := NewManager()
	ec1 := testEmbedder()
	ec2 := testEmbedder()

	m.SetEmbedder("docs", ec1)
	m.SetEmbedder("docs", ec2)

	got := m.GetEmbedder("docs")
	if got != ec2 {
		t.Error("SetEmbedder did not replace existing client")
	}
}

func TestManager_SetEmbedder_NilClears(t *testing.T) {
	m := NewManager()
	m.SetEmbedder("docs", testEmbedder())

	m.SetEmbedder("docs", nil)
	got := m.GetEmbedder("docs")
	if got != nil {
		t.Error("expected nil after clearing with SetEmbedder(docs, nil)")
	}
}

func TestManager_GetEmbedder_Missing(t *testing.T) {
	m := NewManager()
	if got := m.GetEmbedder("nonexistent"); got != nil {
		t.Error("expected nil for missing vault")
	}
}

func TestManager_SetEmbedder_MultipleVaults(t *testing.T) {
	m := NewManager()
	docsEc := testEmbedder()
	codeEc := testEmbedder()

	m.SetEmbedder("docs", docsEc)
	m.SetEmbedder("code", codeEc)

	if got := m.GetEmbedder("docs"); got != docsEc {
		t.Error("docs vault returned wrong embedder")
	}
	if got := m.GetEmbedder("code"); got != codeEc {
		t.Error("code vault returned wrong embedder")
	}
}

// ── Concurrent safety (basic smoke test) ─────────────────────────────────────

func TestManager_ConcurrentAccess(t *testing.T) {
	m := NewManager()
	done := make(chan bool, 4)

	// Concurrent writers
	for i := 0; i < 2; i++ {
		go func() {
			m.SetLLM("docs", testLLM())
			m.SetEmbedder("docs", testEmbedder())
			done <- true
		}()
	}

	// Concurrent readers
	for i := 0; i < 2; i++ {
		go func() {
			m.GetLLM("docs")
			m.GetEmbedder("docs")
			done <- true
		}()
	}

	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for concurrent access")
		}
	}
}

// ── Isolate from existing tests ──────────────────────────────────────────────

func TestManager_AddStillWorks(t *testing.T) {
	m := NewManager()

	if err := m.Add("test", nil, nil); err != nil {
		t.Fatalf("Add() failed: %v", err)
	}

	if cnt := m.VaultCount(); cnt != 1 {
		t.Errorf("VaultCount = %d, want 1", cnt)
	}

	// LLM still works alongside indexers
	m.SetLLM("test", testLLM())
	if m.GetLLM("test") == nil {
		t.Error("expected LLM client after SetLLM")
	}
}
