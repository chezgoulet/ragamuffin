package procedural

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// ── Extract ────────────────────────────────────────────────────────────────────

// Extract scans session turns for action sequences of minSteps+ consecutive steps
// that led to a positive outcome, and returns extracted Procedure structs.
//
// Algorithm:
//  1. Collect all assistant turns (agent actions)
//  2. Filter to action-like turns (commands, structured steps)
//  3. Group consecutive action turns into sequences of minSteps+
//  4. Check the following user turn for positive outcome signals
//  5. If positive outcome confirmed, emit a Procedure
func Extract(turns []Turn, minSteps int) []Procedure {
	if len(turns) < minSteps {
		return nil
	}

	// Phase 1: tag each assistant turn as action-like or not
	actionIndices := make([]int, 0, len(turns))
	for i, t := range turns {
		if t.Role == "assistant" && isActionContent(t.Content) {
			actionIndices = append(actionIndices, i)
		}
	}

	if len(actionIndices) < minSteps {
		return nil
	}

	// Phase 2: group consecutive action turns into sequences
	sequences := groupConsecutive(actionIndices)

	// Phase 3: filter sequences that led to a positive outcome
	var procedures []Procedure
	for _, seq := range sequences {
		if len(seq) < minSteps {
			continue
		}

		// Check the turn immediately after the last action for positive outcome
		lastIdx := seq[len(seq)-1]
		if !hasPositiveOutcome(turns, lastIdx) {
			continue
		}

		proc := buildProcedure(turns, seq)
		if proc != nil {
			procedures = append(procedures, *proc)
		}
	}

	return procedures
}

// ── Action Detection ───────────────────────────────────────────────────────────

// isActionContent returns true if the content contains action-like language.
func isActionContent(content string) bool {
	lower := strings.ToLower(content)

	// Check for explicit numbered steps
	if containsStepIndicators(lower) {
		return true
	}

	// Check for action verbs
	for _, kw := range ActionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	return false
}

// containsStepIndicators checks for "step N:", "1.", "2." etc.
func containsStepIndicators(s string) bool {
	indicators := []string{
		"step 1", "step 2", "step 3",
		"1. ", "2. ", "3. ",
		"first,", "second,", "third,",
		"first:", "second:", "third:",
	}
	for _, ind := range indicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

// ── Outcome Detection ──────────────────────────────────────────────────────────

// positiveSignals are substrings that indicate a successful outcome.
var positiveSignals = []string{
	"ok", "works", "resolved", "fixed", "pass", "success",
	"succeeded", "completed", "verified", "confirmed",
	"error resolved", "issue closed", "test passes",
}

// negativeSignals are substrings that indicate failure/no resolution.
var negativeSignals = []string{
	"failed", "error", "broken", "still broken", "not working",
	"not resolved", "still failing", "404", "500",
	"timeout", "unreachable", "unavailable",
}

// hasPositiveOutcome checks up to 2 turns after the last action for a positive outcome.
// We look ahead up to 2 positions because a system tool output may appear between
// the last action and the user's confirmation (e.g., user: "ok that fixed it").
func hasPositiveOutcome(turns []Turn, lastActionIdx int) bool {
	// Check up to 2 turns ahead: the immediate next turn (often system output)
	// and the one after that (often user confirmation).
	maxCheck := lastActionIdx + 2
	if maxCheck >= len(turns) {
		maxCheck = len(turns) - 1
	}

	startIdx := lastActionIdx + 1
	if startIdx >= len(turns) {
		return false
	}

	// Step 1: scan for negative signals in all checked turns
	for i := startIdx; i <= maxCheck; i++ {
		if turns[i].Role != "user" && turns[i].Role != "system" {
			continue
		}
		content := strings.ToLower(turns[i].Content)
		for _, sig := range negativeSignals {
			if strings.Contains(content, sig) {
				return false
			}
		}
	}

	// Step 2: scan for positive signals in all checked turns
	for i := startIdx; i <= maxCheck; i++ {
		if turns[i].Role != "user" && turns[i].Role != "system" {
			continue
		}
		content := strings.ToLower(turns[i].Content)
		for _, sig := range positiveSignals {
			if strings.Contains(content, sig) {
				return true
			}
		}
	}

	return false
}

// ── Sequence Extraction ────────────────────────────────────────────────────────

// groupConsecutive groups consecutive indices into sequences.
// Indices are considered consecutive if there's no gap of more than 1
// non-action turns between them.
func groupConsecutive(indices []int) [][]int {
	if len(indices) == 0 {
		return nil
	}

	var sequences [][]int
	current := []int{indices[0]}

	for i := 1; i < len(indices); i++ {
		// Allow gap of up to 2 (to skip brief non-action turns in between)
		if indices[i]-indices[i-1] <= 3 {
			current = append(current, indices[i])
		} else {
			sequences = append(sequences, current)
			current = []int{indices[i]}
		}
	}
	sequences = append(sequences, current)

	return sequences
}

// ── Procedure Building ─────────────────────────────────────────────────────────

// buildProcedure constructs a Procedure from a sequence of turns.
func buildProcedure(turns []Turn, seq []int) *Procedure {
	if len(seq) == 0 {
		return nil
	}

	// Extract steps from the action turns
	steps := make([]string, 0, len(seq))
	for _, idx := range seq {
		step := extractStep(turns[idx].Content)
		if step != "" {
			steps = append(steps, step)
		}
	}

	if len(steps) < DefaultMinSteps {
		return nil
	}

	// Generate name from the first few steps
	name := generateName(steps)

	// Generate trigger from context (the user turn before the first action)
	var trigger string
	firstIdx := seq[0]
	if firstIdx > 0 {
		trigger = summarizeTrigger(turns[firstIdx-1].Content)
	}
	if trigger == "" {
		trigger = "Agent initiated action sequence"
	}

	return &Procedure{
		Name:    name,
		Trigger: trigger,
		Steps:   steps,
		SuccessCount: 1,
	}
}

// extractStep extracts a single step description from a turn's content.
func extractStep(content string) string {
	// Try to find a code block or command
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		// Prefer lines with action verbs or commands
		if len(trimmed) > 5 && len(trimmed) < 200 {
			return trimmed
		}
	}
	// Fall back to first meaningful line
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 5 {
			return trimmed
		}
	}
	// Last resort: shorten the content
	if len(content) > 200 {
		return content[:200] + "..."
	}
	return content
}

// generateName creates a human-readable procedure name from steps.
func generateName(steps []string) string {
	if len(steps) == 0 {
		return "Unknown procedure"
	}

	// Try to extract the action from the first step
	first := steps[0]
	name := summarizeStep(first)

	// If we have enough context, include the goal
	if len(steps) >= 2 {
		second := summarizeStep(steps[1])
		if second != name {
			name = name + " and " + strings.ToLower(second[:min(len(second), 30)])
		}
	}

	return name
}

// summarizeStep extracts a short action description from a step string.
func summarizeStep(step string) string {
	// Remove backtick code formatting
	step = strings.Trim(step, "`")

	// Take first action verb + first noun
	words := strings.Fields(step)
	if len(words) == 0 {
		return step
	}

	// Find first action verb
	for i, w := range words {
		wLower := strings.ToLower(w)
		for _, kw := range ActionKeywords {
			if strings.Contains(wLower, kw) && i < len(words)-1 {
				// Return verb + next word(s) for context
				end := i + 4
				if end > len(words) {
					end = len(words)
				}
				result := strings.Join(words[i:end], " ")
				if len(result) > 60 {
					return result[:60] + "..."
				}
				return result
			}
		}
	}

	// Fallback: first 5 words
	end := 5
	if end > len(words) {
		end = len(words)
	}
	result := strings.Join(words[:end], " ")
	if len(result) > 60 {
		return result[:60] + "..."
	}
	return result
}

// summarizeTrigger creates a short description of the trigger context.
func summarizeTrigger(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 10 && len(trimmed) < 200 {
			return trimmed
		}
	}
	if len(content) > 200 {
		return content[:200] + "..."
	}
	return content
}

// ── Key Derivation ─────────────────────────────────────────────────────────────

// DeriveKey generates a deterministic key for a procedure fact.
// Format: procedure-<sha256(name+trigger)[:12]>
func DeriveKey(name, trigger string) string {
	input := name + "\x00" + trigger
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%s%x", KeyPrefix, h[:6]) // 12 hex chars
}
