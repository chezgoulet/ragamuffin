// Package tokenutil provides shared token estimation for the Ragamuffin codebase.
package tokenutil

import "strings"

// EstTokens returns an approximate token count (words × 1.3).
// This is a rough heuristic used for context window budgeting, not a tokenizer.
func EstTokens(text string) int {
	return int(float64(len(strings.Fields(text))) * 1.3)
}
