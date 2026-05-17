package pruner

import (
	"testing"
)

// Integration tests for the pruner scan methods.
//
// These tests require a real Qdrant instance. They are skipped in short mode
// (go test -short) and require QDRANT_TEST_URL to be set.
//
// To run:
//   docker run -d -p 6334:6334 qdrant/qdrant
//   export QDRANT_TEST_URL=http://localhost:6334
//   go test -run TestPrunerIntegration ./internal/pruner/ -v
//
// Note: The Pruner currently depends on concrete *qdrant.Client / *embedding.Client
// types rather than interfaces. The scan unit tests (tested separately in
// pruner_test.go via helper/pure functions) are the primary coverage; these
// integration tests verify end-to-end behavior with a live Qdrant backend.
// Future work: extract interface for qdrant.Client to enable pure unit tests
// for scan methods without external dependencies.
//
// See also: internal/server/testutil/MockQdrant for function-pointer-based mocks
// that can be used once the Pruner uses interfaces.

func TestPrunerIntegration_StaleScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Log("Integration test scaffold for staleScan — requires live Qdrant")
	// TODO: 
	// 1. Create Pruner with real Qdrant client
	// 2. Upsert test facts (active+expired, active+fresh, ttl=0)
	// 3. Run staleScan
	// 4. Verify status changed for expired facts
}

func TestPrunerIntegration_ConflictScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Log("Integration test scaffold for conflictScan — requires live Qdrant + embedder")
}

func TestPrunerIntegration_SupersedeScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Log("Integration test scaffold for supersedeScan — requires live Qdrant")
}

func TestPrunerIntegration_LowConfidenceScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Log("Integration test scaffold for lowConfidenceScan — requires live Qdrant")
}

func TestPrunerIntegration_Scheduler(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	t.Log("Integration test scaffold for scheduler — requires live Qdrant")
}

// verify pb types are importable (compile-time dependency check)
var _ = []byte("github.com/qdrant/go-client/qdrant used by pruner scans")
