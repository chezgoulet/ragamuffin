package indexer

import (
	"fmt"
	"sync"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/qdrant"
)

// Manager holds per-vault indexers and their Qdrant clients.
// Single-tenant mode uses a single entry with name "default".
type Manager struct {
	mu       sync.RWMutex
	indexers map[string]*Indexer
	clients  map[string]*qdrant.Client
}

// NewManager creates an empty indexer manager.
func NewManager() *Manager {
	return &Manager{
		indexers: make(map[string]*Indexer),
		clients:  make(map[string]*qdrant.Client),
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

// VaultStats returns aggregated stats for vault-name-based queries.
// Returns zeros if vault not found.
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
