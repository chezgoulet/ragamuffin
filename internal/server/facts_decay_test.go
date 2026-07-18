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

func TestIncrementFactAccessStampsDecayWhenEnabled(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")
	// Age the fact so accessibility is below 1.0.
	old := time.Now().UTC().Add(-60 * 24 * time.Hour).Format(time.RFC3339)
	store.points["user/pref"].Payload["created_at"] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: old}}

	s := decayServer(store, true)
	s.incrementFactAccess(context.Background(), "user/pref")

	if store.setPayloadCalls == 0 {
		t.Fatal("expected SetPayload to be called")
	}
	pl := store.lastSetPayload
	if _, ok := pl["accessibility"]; !ok {
		t.Fatal("accessibility not stamped")
	}
	if _, ok := pl["effective_confidence"]; !ok {
		t.Fatal("effective_confidence not stamped")
	}
	acc := pl["accessibility"].GetDoubleValue()
	if acc <= 0 || acc > 1 {
		t.Fatalf("accessibility out of range: %v", acc)
	}
	// confidence is 1.0 in the fixture, so effective == accessibility.
	if eff := pl["effective_confidence"].GetDoubleValue(); eff != acc {
		t.Fatalf("effective_confidence %v != accessibility %v for confidence=1.0", eff, acc)
	}
}

func TestIncrementFactAccessNoDecayFieldsWhenDisabled(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")

	s := decayServer(store, false)
	s.incrementFactAccess(context.Background(), "user/pref")

	if store.setPayloadCalls == 0 {
		t.Fatal("expected SetPayload to be called")
	}
	if _, ok := store.lastSetPayload["accessibility"]; ok {
		t.Fatal("accessibility must not be stamped when decay disabled")
	}
	if _, ok := store.lastSetPayload["effective_confidence"]; ok {
		t.Fatal("effective_confidence must not be stamped when decay disabled")
	}
}
