package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	pb "github.com/qdrant/go-client/qdrant"
	"log/slog"
)

// newFreshnessServer builds a minimal Server wired to a factsMockStore.
// factsMockStore is not a *qdrant.Client, so the handler exercises its
// ScrollFiltered fallback path — sufficient to validate the response shape
// and staleness logic.
func newFreshnessServer(store *factsMockStore, threshold time.Duration) *Server {
	return &Server{
		cfg:         &config.Config{FactsCollection: "test_facts", FactsVectorSize: 4, FactsFreshnessThreshold: threshold, AutoProvisionVaults: false},
		facts:       store,
		shutdownCtx: context.Background(),
		logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// addFreshnessPoint seeds a fact with an explicit updated_at_unix timestamp.
func (s *factsMockStore) addFreshnessPoint(key, updatedAt string, updatedAtUnix float64) {
	payload := map[string]*pb.Value{
		"fact_key":        {Kind: &pb.Value_StringValue{StringValue: key}},
		"updated_at":      {Kind: &pb.Value_StringValue{StringValue: updatedAt}},
		"updated_at_unix": {Kind: &pb.Value_DoubleValue{DoubleValue: updatedAtUnix}},
		"status":          {Kind: &pb.Value_StringValue{StringValue: "active"}},
	}
	s.points[key] = &pb.RetrievedPoint{
		Id:      &pb.PointId{PointIdOptions: &pb.PointId_Uuid{Uuid: factKeyHash(key)}},
		Payload: payload,
	}
}

func TestFactsFreshness_Healthy(t *testing.T) {
	store := newFactsMockStore()
	// Write 5 minutes ago.
	recent := time.Now().UTC().Add(-5 * time.Minute).Unix()
	store.addFreshnessPoint("librarian/last", time.Unix(recent, 0).Format(time.RFC3339), float64(recent))
	s := newFreshnessServer(store, 24*time.Hour)

	w := httptest.NewRecorder()
	s.handleFactsFreshness(w, httptest.NewRequest(http.MethodGet, "/v1/facts/freshness", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp factFreshnessResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Stale {
		t.Errorf("expected healthy (not stale), got stale=true: %+v", resp)
	}
	if resp.LastWriteUnix != recent {
		t.Errorf("LastWriteUnix = %d, want %d", resp.LastWriteUnix, recent)
	}
	if resp.AgeSeconds <= 0 {
		t.Errorf("AgeSeconds = %d, want > 0", resp.AgeSeconds)
	}
	if resp.ThresholdSeconds != 86400 {
		t.Errorf("ThresholdSeconds = %d, want 86400", resp.ThresholdSeconds)
	}
}

func TestFactsFreshness_Stale(t *testing.T) {
	store := newFactsMockStore()
	// Write 48 hours ago — beyond the 24h threshold.
	old := time.Now().UTC().Add(-48 * time.Hour).Unix()
	store.addFreshnessPoint("librarian/last", time.Unix(old, 0).Format(time.RFC3339), float64(old))
	s := newFreshnessServer(store, 24*time.Hour)

	w := httptest.NewRecorder()
	s.handleFactsFreshness(w, httptest.NewRequest(http.MethodGet, "/v1/facts/freshness", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp factFreshnessResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Stale {
		t.Errorf("expected stale=true, got stale=false: %+v", resp)
	}
	if resp.AgeSeconds <= 86400 {
		t.Errorf("AgeSeconds = %d, want > 86400", resp.AgeSeconds)
	}
}

func TestFactsFreshness_NeverWritten(t *testing.T) {
	store := newFactsMockStore()
	s := newFreshnessServer(store, 24*time.Hour)

	w := httptest.NewRecorder()
	s.handleFactsFreshness(w, httptest.NewRequest(http.MethodGet, "/v1/facts/freshness", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp factFreshnessResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Stale {
		t.Errorf("expected stale=true when no facts exist, got stale=false")
	}
	if resp.LastWriteUnix != 0 {
		t.Errorf("LastWriteUnix = %d, want 0", resp.LastWriteUnix)
	}
	if resp.AgeSeconds != -1 {
		t.Errorf("AgeSeconds = %d, want sentinel -1", resp.AgeSeconds)
	}
}

func TestFactsFreshness_MethodNotAllowed(t *testing.T) {
	store := newFactsMockStore()
	s := newFreshnessServer(store, 24*time.Hour)
	w := httptest.NewRecorder()
	s.handleFactsFreshness(w, httptest.NewRequest(http.MethodPost, "/v1/facts/freshness", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
