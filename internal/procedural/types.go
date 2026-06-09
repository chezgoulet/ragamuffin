// Package procedural extracts repeatable action sequences from session traces
// and persists them as procedure facts in Qdrant.
package procedural

import "time"

// ── Constants ──────────────────────────────────────────────────────────────────

const (
	// FactTypeProcedure is stored in the Qdrant payload for filtered recall.
	FactTypeProcedure = "procedure"

	// DefaultMinSteps is the minimum number of action steps to form a procedure.
	DefaultMinSteps = 3

	// DefaultDedupThreshold is the default cosine similarity threshold for dedup.
	DefaultDedupThreshold = 0.85

	// KeyPrefix is the prefix for procedure fact keys.
	KeyPrefix = "procedure-"
)

// ── Types ──────────────────────────────────────────────────────────────────────

// Procedure is a repeatable action sequence extracted from a session trace.
type Procedure struct {
	Name          string   `json:"name"`
	Trigger       string   `json:"trigger"`
	Steps         []string `json:"steps"`
	SourceSession string   `json:"source_session"`
	SuccessCount  int      `json:"success_count"`
	LastUsed      string   `json:"last_used"`
}

// Turn represents a single turn in a session trace.
type Turn struct {
	Content string `json:"content"`
	Role    string `json:"role"`
}

// ActionKeywords are verbs/phrases that indicate an agent action step.
// Used by the extractor to identify action turns.
var ActionKeywords = []string{
	"run", "check", "read", "write", "grep", "restart", "verify",
	"curl", "exec", "edit", "apply", "build", "test", "deploy",
	"install", "configure", "create", "update", "delete", "copy",
	"move", "start", "stop", "reload", "rebuild", "compile",
	"inspect", "examine", "search", "query", "fetch", "list",
	"step 1", "step 2", "step 3",
}

// ── Helpers ────────────────────────────────────────────────────────────────────

// DedupResult describes the outcome of dedup against existing procedures.
type DedupResult struct {
	ExistingKey   string // Non-empty if a match was found
	ShouldUpdate  bool   // True if the existing procedure should be updated
	ShouldCreate  bool   // True if no match found — write new
	Similarity    float64
}

// NewProcedure creates a Procedure with sensible defaults.
func NewProcedure(name, trigger string, steps []string, sourceSession string) Procedure {
	return Procedure{
		Name:          name,
		Trigger:       trigger,
		Steps:         steps,
		SourceSession: sourceSession,
		SuccessCount:  1,
		LastUsed:      time.Now().UTC().Format(time.RFC3339),
	}
}
