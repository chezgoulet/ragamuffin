package server

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/chezgoulet/ragamuffin/internal/config"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
	pb "github.com/qdrant/go-client/qdrant"
)

func reconServer(store *factsMockStore, enabled bool, window time.Duration) *Server {
	return &Server{
		cfg: &config.Config{
			FactsCollection:        "test_facts",
			FactsVectorSize:        4,
			ReconsolidationEnabled: enabled,
			ReconsolidationWindow:  window,
		},
		facts:       store,
		shutdownCtx: context.Background(),
		logger:      slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

func TestIncrementFactAccessStampsLastRecalledWhenEnabled(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")

	s := reconServer(store, true, 30*time.Minute)
	s.incrementFactAccess(context.Background(), "user/pref")

	if store.setPayloadCalls == 0 {
		t.Fatal("expected SetPayload to be called")
	}
	if _, ok := store.lastSetPayload["last_recalled_at"]; !ok {
		t.Fatal("last_recalled_at not stamped when reconsolidation enabled")
	}
}

func TestIncrementFactAccessNoLastRecalledWhenDisabled(t *testing.T) {
	store := newFactsMockStore()
	store.addPoint("user/pref", "dark mode", "active")

	s := reconServer(store, false, 30*time.Minute)
	s.incrementFactAccess(context.Background(), "user/pref")

	if _, ok := store.lastSetPayload["last_recalled_at"]; ok {
		t.Fatal("last_recalled_at must not be stamped when reconsolidation disabled")
	}
}

func TestApplyReconsolidationWithinWindow(t *testing.T) {
	now := time.Now().UTC()
	current := map[string]*pb.Value{
		"last_recalled_at": qutil.Nv(now.Add(-5 * time.Minute).Format(time.RFC3339)),
	}
	s := reconServer(newFactsMockStore(), true, 30*time.Minute)

	fields := map[string]any{}
	if !s.applyReconsolidation(current, fields, now) {
		t.Fatal("expected reconsolidation within window")
	}
	if _, ok := fields["reconsolidated_at"]; !ok {
		t.Fatal("reconsolidated_at not set")
	}
	chain, ok := fields["reconsolidation_chain"].([]string)
	if !ok || len(chain) != 1 {
		t.Fatalf("expected chain of length 1, got %v", fields["reconsolidation_chain"])
	}
}

func TestApplyReconsolidationOutsideWindow(t *testing.T) {
	now := time.Now().UTC()
	current := map[string]*pb.Value{
		"last_recalled_at": qutil.Nv(now.Add(-2 * time.Hour).Format(time.RFC3339)),
	}
	s := reconServer(newFactsMockStore(), true, 30*time.Minute)

	if s.applyReconsolidation(current, map[string]any{}, now) {
		t.Fatal("must not reconsolidate outside the window")
	}
}

func TestApplyReconsolidationNeverRecalled(t *testing.T) {
	s := reconServer(newFactsMockStore(), true, 30*time.Minute)
	if s.applyReconsolidation(map[string]*pb.Value{}, map[string]any{}, time.Now().UTC()) {
		t.Fatal("must not reconsolidate a fact that was never recalled")
	}
}

func TestApplyReconsolidationDisabled(t *testing.T) {
	now := time.Now().UTC()
	current := map[string]*pb.Value{
		"last_recalled_at": qutil.Nv(now.Format(time.RFC3339)),
	}
	s := reconServer(newFactsMockStore(), false, 30*time.Minute)
	if s.applyReconsolidation(current, map[string]any{}, now) {
		t.Fatal("must not reconsolidate when disabled")
	}
}

func TestApplyReconsolidationVAppendsChain(t *testing.T) {
	now := time.Now().UTC()
	prior := now.Add(-20 * time.Minute).Format(time.RFC3339)
	payload := map[string]*pb.Value{
		"last_recalled_at":      qutil.Nv(now.Add(-3 * time.Minute).Format(time.RFC3339)),
		"reconsolidation_chain": qutil.NvList([]string{prior}),
	}
	s := reconServer(newFactsMockStore(), true, 30*time.Minute)

	if !s.applyReconsolidationV(payload, now) {
		t.Fatal("expected reconsolidation")
	}
	chain := qutil.GetPayloadStringList(payload, "reconsolidation_chain")
	if len(chain) != 2 || chain[0] != prior {
		t.Fatalf("chain not appended immutably: %v", chain)
	}
	if _, ok := qutil.GetPayloadString(payload, "reconsolidated_at"); !ok {
		t.Fatal("reconsolidated_at not stamped")
	}
}
