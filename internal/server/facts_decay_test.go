package server

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	pb "github.com/qdrant/go-client/qdrant"
)

func decayServer(store *factsMockStore, decay bool) *Server {
	return &Server{
		cfg: &config.Config{
			FactsCollection:   "test_facts",
			FactsVectorSize:   4,
			DecayEnabled:      decay,
			DecayHalfLifeDays: 30.0,
		},
		facts:       store,
		shutdownCtx: context.Background(),
		logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// TestIncrementFactAccessDoesNotStampDecay verifies decay fields are no longer
// written on access. Stamping them at access time (last_accessed_at == now)
// always yielded ~1.0; they are now computed live on read instead.
func TestIncrementFactAccessDoesNotStampDecay(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")

	s := decayServer(store, true)
	s.incrementFactAccess(context.Background(), "user/pref")

	if store.setPayloadCalls == 0 {
		t.Fatal("expected SetPayload to be called")
	}
	if _, ok := store.lastSetPayload["accessibility"]; ok {
		t.Error("accessibility must not be stamped at access time")
	}
	if _, ok := store.lastSetPayload["effective_confidence"]; ok {
		t.Error("effective_confidence must not be stamped at access time")
	}
}

// TestFactResponseComputesDecayLive verifies the response reports a true
// decayed accessibility computed from the stored last_accessed_at, not ~1.0.
func TestFactResponseComputesDecayLive(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")
	// Last accessed 60 days ago. Neutralize the stability multiplier
	// (no confirmations/confidence/accesses) so the baseline 30-day half-life
	// applies → accessibility ≈ 0.25 at 60 days.
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	payload := store.points["user/pref"].Payload
	payload["last_accessed_at"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: old}}
	payload["confirmation_count"] = &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 0}}
	payload["confidence"] = &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 0}}
	payload["access_count"] = &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: 0}}

	fr := pointToFactResponse(payload, "user/pref", true, 30.0)
	if fr.Accessibility == nil {
		t.Fatal("accessibility should be computed when decay enabled")
	}
	if *fr.Accessibility >= 0.9 {
		t.Fatalf("aged fact should have decayed accessibility, got %v", *fr.Accessibility)
	}
	if *fr.Accessibility < 0.1 || *fr.Accessibility > 0.5 {
		t.Fatalf("accessibility ≈0.25 expected for 60d/30d half-life, got %v", *fr.Accessibility)
	}
}

// TestFactResponseNoDecayWhenDisabled verifies the fields are omitted when
// decay is disabled.
func TestFactResponseNoDecayWhenDisabled(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")
	payload := store.points["user/pref"].Payload

	fr := pointToFactResponse(payload, "user/pref", false, 30.0)
	if fr.Accessibility != nil {
		t.Error("accessibility must be nil when decay disabled")
	}
	if fr.EffectiveConfidence != nil {
		t.Error("effective_confidence must be nil when decay disabled")
	}
}
