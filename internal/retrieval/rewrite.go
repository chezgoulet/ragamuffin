package retrieval

import (
	"context"
	"fmt"
	"strings"
)

// Completer is the minimal LLM surface the retrieval package needs for query
// rewriting and reranking. It is declared here (rather than imported from
// internal/llm) so this package stays dependency-free and trivially mockable;
// *llm.Client satisfies it structurally.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// RewriteMode selects a query-rewriting strategy applied before embedding.
type RewriteMode string

const (
	// RewriteOff performs no rewriting; the raw query is embedded as-is.
	RewriteOff RewriteMode = "off"
	// RewriteHyDE generates a hypothetical answer document and embeds that
	// instead of the question (arXiv:2212.10496). Dense retrieval matches
	// answer-shaped text better than question-shaped text.
	RewriteHyDE RewriteMode = "hyde"
	// RewriteStepBack derives a more general "step-back" question that
	// surfaces the background principles needed to answer (arXiv:2310.06117).
	RewriteStepBack RewriteMode = "stepback"
	// RewriteMultiQuery expands the query into several paraphrases; the caller
	// embeds each and fuses results with RRF (arXiv:2303.01424).
	RewriteMultiQuery RewriteMode = "multiquery"
)

// ParseRewriteMode maps a config string to a RewriteMode, defaulting to
// RewriteOff for empty or unrecognized values. ok reports whether the value
// was recognized so callers can warn on typos.
func ParseRewriteMode(s string) (mode RewriteMode, ok bool) {
	switch RewriteMode(strings.ToLower(strings.TrimSpace(s))) {
	case RewriteHyDE:
		return RewriteHyDE, true
	case RewriteStepBack:
		return RewriteStepBack, true
	case RewriteMultiQuery:
		return RewriteMultiQuery, true
	case RewriteOff, "":
		return RewriteOff, true
	default:
		return RewriteOff, false
	}
}

// multiQueryCount is how many paraphrases RewriteMultiQuery requests.
const multiQueryCount = 3

// Rewrite transforms a query per the given mode using the LLM, returning the
// list of query strings to embed. On RewriteOff, a nil completer, or any LLM
// error, it degrades gracefully to the original query so retrieval never fails
// because rewriting failed. The returned slice always contains at least the
// original query for single-query modes; multi-query returns 1..N strings.
func Rewrite(ctx context.Context, c Completer, mode RewriteMode, query string) []string {
	orig := strings.TrimSpace(query)
	if c == nil || mode == RewriteOff || orig == "" {
		return []string{orig}
	}

	switch mode {
	case RewriteHyDE:
		out, err := c.Complete(ctx, hydePrompt(orig))
		if err != nil {
			return []string{orig}
		}
		if hyp := strings.TrimSpace(out); hyp != "" {
			return []string{hyp}
		}
		return []string{orig}

	case RewriteStepBack:
		out, err := c.Complete(ctx, stepBackPrompt(orig))
		if err != nil {
			return []string{orig}
		}
		// Query both the step-back (general) and original (specific) query.
		if sb := firstLine(out); sb != "" && sb != orig {
			return []string{sb, orig}
		}
		return []string{orig}

	case RewriteMultiQuery:
		out, err := c.Complete(ctx, multiQueryPrompt(orig, multiQueryCount))
		if err != nil {
			return []string{orig}
		}
		queries := parseLines(out)
		if !containsFold(queries, orig) {
			queries = append(queries, orig)
		}
		if len(queries) == 0 {
			return []string{orig}
		}
		return queries

	default:
		return []string{orig}
	}
}

func hydePrompt(q string) string {
	return fmt.Sprintf(
		"Write a short, factual passage that directly answers the following "+
			"question, as if it were an excerpt from an authoritative document. "+
			"Do not add preamble, disclaimers, or say you are unsure. Output only "+
			"the passage.\n\nQuestion: %s", q)
}

func stepBackPrompt(q string) string {
	return fmt.Sprintf(
		"Given a specific question, produce a single more general 'step-back' "+
			"question whose answer provides the background principles needed to "+
			"answer the specific one. Output only the step-back question on one "+
			"line, with no numbering or preamble.\n\nSpecific question: %s", q)
}

func multiQueryPrompt(q string, n int) string {
	return fmt.Sprintf(
		"Generate %d alternative phrasings of the following search query that "+
			"capture different ways it might be expressed in a knowledge base. "+
			"Vary the vocabulary and specificity. Output one query per line, with "+
			"no numbering, bullets, or preamble.\n\nQuery: %s", n, q)
}

// firstLine returns the first non-empty, cleaned line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if cleaned := cleanLine(line); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

// parseLines splits an LLM list response into cleaned, de-duplicated lines,
// stripping common bullet/number prefixes.
func parseLines(s string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, line := range strings.Split(s, "\n") {
		cleaned := cleanLine(line)
		if cleaned == "" {
			continue
		}
		key := strings.ToLower(cleaned)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

// cleanLine strips leading list markers ("1.", "-", "*", "•") and surrounding
// quotes/whitespace from an LLM line.
func cleanLine(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	// Strip "N." / "N)" numeric prefixes.
	if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 {
		if allDigits(line[:i]) {
			line = strings.TrimSpace(line[i+1:])
		}
	}
	line = strings.TrimLeft(line, "-*• \t")
	line = strings.Trim(line, `"'`)
	return strings.TrimSpace(line)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsFold(list []string, s string) bool {
	for _, e := range list {
		if strings.EqualFold(e, s) {
			return true
		}
	}
	return false
}
