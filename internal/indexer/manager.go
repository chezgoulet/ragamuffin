package indexer

import (
	"fmt"
	"sync"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
)

// Manager holds per-vault indexers, Qdrant clients, LLM clients, and
// embedding clients. Single-tenant mode uses name "default".
type Manager struct {
	mu           sync.RWMutex
	indexers     map[string]*Indexer
	clients      map[string]*qdrant.Client
	llmClients   map[string]*llm.Client
	embedClients map[string]*embedding.Client
}

// NewManager creates an empty indexer manager.
func NewManager() *Manager {
	return &Manager{
		indexers:     make(map[string]*Indexer),
		clients:      make(map[string]*qdrant.Client),
		llmClients:   make(map[string]*llm.Client),
		embedClients: make(map[string]*embedding.Client),
	}
}

// Add registers a new indexer for a vault. The vault name must be unique.
func (m *Manager) Add(name string, idx *Indexer, qc *qdrant.Client) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.indexers[name]; exists {
		return fmt.Errorf("indexer for vault %q already registered", name)
	}
	m.indexers[name] = idx
	m.clients[name] = qc
	return nil
}

// Get returns the indexer for a vault, or nil if not found.
func (m *Manager) Get(name string) *Indexer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.indexers[name]
}

// GetClient returns the Qdrant client for a vault, or nil if not found.
func (m *Manager) GetClient(name string) *qdrant.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

// SetLLM stores a per-vault LLM client. Pass nil to clear.
func (m *Manager) SetLLM(name string, lm *llm.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lm != nil {
		m.llmClients[name] = lm
	} else {
		delete(m.llmClients, name)
	}
}

// GetLLM returns the per-vault LLM client, or nil if not set.
func (m *Manager) GetLLM(name string) *llm.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmClients[name]
}

// SetEmbedder stores a per-vault embedding client. Pass nil to clear.
func (m *Manager) SetEmbedder(name string, ec *embedding.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ec != nil {
		m.embedClients[name] = ec
	} else {
		delete(m.embedClients, name)
	}
}

// GetEmbedder returns the per-vault embedding client, or nil if not set.
func (m *Manager) GetEmbedder(name string) *embedding.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.embedClients[name]
}

// VaultCount returns the number of registered indexers.
func (m *Manager) VaultCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.indexers)
}

// VaultNames returns all registered vault names.
func (m *Manager) VaultNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.indexers))
	for name := range m.indexers {
		names = append(names, name)
	}
	return names
}

// ForEach calls fn for each registered vault indexer.
func (m *Manager) ForEach(fn func(name string, idx *Indexer)) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, idx := range m.indexers {
		fn(name, idx)
	}
}

// VaultStats holds aggregated stats for a vault.
type VaultStats struct {
	FileCount   int
	ChunkCount  int
	LastIndexed time.Time
	Indexing    bool
}

// Stats returns stats for a specific vault.
func (m *Manager) Stats(name string) VaultStats {
	idx := m.Get(name)
	if idx == nil {
		return VaultStats{}
	}
	fc, cc, li, ing, _, _ := idx.Stats()
	return VaultStats{
		FileCount:   fc,
		ChunkCount:  cc,
		LastIndexed: li,
		Indexing:    ing,
	}
}

// Reindex triggers a full re-index for a vault. Returns false if already queued
// or the vault does not exist.
func (m *Manager) Reindex(name string) bool {
	idx := m.Get(name)
	if idx == nil {
		return false
	}
	return idx.Reindex()
}
