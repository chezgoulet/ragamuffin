package indexer

import (
	"fmt"
	"sync"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/embedding"
	"github.com/chezgoulet/ragamuffin/internal/llm"
	"github.com/chezgoulet/ragamuffin/internal/qdrant"
)

// Manager holds per-vault indexers, Qdrant clients (chunk + fact), LLM clients, and
// embedding clients. Single-tenant mode uses name "default".
type Manager struct {
	mu           sync.RWMutex
	indexers     map[string]*Indexer
	clients      map[string]qdrant.FactStore // chunk/doc Qdrant clients
	factClients  map[string]qdrant.FactStore // per-vault fact Qdrant clients
	llmClients   map[string]llm.Synthesizer
	embedClients map[string]embedding.Embedder
}

// NewManager creates an empty indexer manager.
func NewManager() *Manager {
	return &Manager{
		indexers:     make(map[string]*Indexer),
		clients:      make(map[string]qdrant.FactStore),
		factClients:  make(map[string]qdrant.FactStore),
		llmClients:   make(map[string]llm.Synthesizer),
		embedClients: make(map[string]embedding.Embedder),
	}
}

// Add registers a new indexer for a vault. The vault name must be unique.
func (m *Manager) Add(name string, idx *Indexer, qc qdrant.FactStore) error {
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
func (m *Manager) GetClient(name string) qdrant.FactStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clients[name]
}

// AddFactClient registers a per-vault facts Qdrant client.
func (m *Manager) AddFactClient(name string, fc qdrant.FactStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factClients[name] = fc
}

// GetFactClient returns the per-vault facts Qdrant client, or nil.
func (m *Manager) GetFactClient(name string) qdrant.FactStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.factClients[name]
}

// SetLLM stores a per-vault LLM client. Pass nil to clear.
func (m *Manager) SetLLM(name string, lm llm.Synthesizer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lm != nil {
		m.llmClients[name] = lm
	} else {
		delete(m.llmClients, name)
	}
}

// GetLLM returns the per-vault LLM client, or nil if not set.
func (m *Manager) GetLLM(name string) llm.Synthesizer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.llmClients[name]
}

// SetEmbedder stores a per-vault embedding client. Pass nil to clear.
func (m *Manager) SetEmbedder(name string, ec embedding.Embedder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ec != nil {
		m.embedClients[name] = ec
	} else {
		delete(m.embedClients, name)
	}
}

// GetEmbedder returns the per-vault embedding client, or nil if not set.
func (m *Manager) GetEmbedder(name string) embedding.Embedder {
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

// Remove deletes a vault's indexer and clients by name. No-op if not found.
func (m *Manager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.indexers, name)
	delete(m.clients, name)
	delete(m.factClients, name)
	delete(m.llmClients, name)
	delete(m.embedClients, name)
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
