package retrieval

import (
	"strings"
	"unicode"
)

// QueryType classifies a natural-language query by its primary intent.
// The classifier is lightweight, keyword-driven, deterministic, and completes
// in well under 1 ms. Used for recall-strategy hints and result metadata.
type QueryType string

const (
	QuerySemantic QueryType = "semantic"
	QueryTemporal QueryType = "temporal"
	QueryCausal   QueryType = "causal"
	QueryEntity   QueryType = "entity"
	QueryLookup   QueryType = "lookup"
)

// ClassifyQuery examines query text and returns the most specific intent type.
// Priority order: temporal > causal > entity > lookup > semantic.
// Multiple signals for the same type do not increase specificity.
func ClassifyQuery(query string) QueryType {
	q := strings.TrimSpace(query)
	if q == "" {
		return QuerySemantic
	}

	lq := strings.ToLower(q)

	if hasTemporalSignal(lq) {
		return QueryTemporal
	}
	if hasCausalSignal(lq) {
		return QueryCausal
	}
	if hasEntitySignal(q, lq) {
		return QueryEntity
	}
	if hasLookupSignal(lq) {
		return QueryLookup
	}
	return QuerySemantic
}

// temporalSignals detect date/time references and recency queries.
func hasTemporalSignal(lq string) bool {
	keywords := []string{
		"when", "yesterday", "today", "tomorrow",
		"last week", "last month", "last year",
		"next week", "next month", "next year",
		"this week", "this month", "this year",
		"recent", "recently", "past", "upcoming",
		"schedule", "timeline", "deadline",
		"ago", "since", "before", "after",
	}
	for _, kw := range keywords {
		if strings.Contains(lq, kw) {
			return true
		}
	}

	// Day-of-week names
	days := []string{
		"monday", "tuesday", "wednesday", "thursday",
		"friday", "saturday", "sunday",
	}
	for _, d := range days {
		if strings.Contains(lq, d) {
			return true
		}
	}

	// Date patterns: YYYY, YYYY-MM-DD, month names
	months := []string{
		"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december",
		"jan", "feb", "mar", "apr", "jun", "jul", "aug", "sep", "oct", "nov", "dec",
	}
	for _, m := range months {
		if strings.Contains(lq, m) {
			return true
		}
	}

	// "planned" and "quarter" are common in business temporal queries
	more := []string{"planned", "quarter"}
	for _, w := range more {
		if strings.Contains(lq, w) {
			return true
		}
	}

	return false
}

// causalSignals detect cause-effect and explanatory queries.
func hasCausalSignal(lq string) bool {
	keywords := []string{
		"why", "because", "cause", "causes", "caused",
		"reason", "reasons", "result", "results", "resulted",
		"led to", "lead to", "leads to",
		"impact", "effect", "affect", "consequence",
		"due to", "therefore", "consequently", "hence",
		"as a result",
	}
	for _, kw := range keywords {
		if strings.Contains(lq, kw) {
			return true
		}
	}
	return false
}

// entitySignals detect queries about specific named entities.
func hasEntitySignal(q, lq string) bool {
	// "who is", "who was", "tell me about X", "what is X" where X is capitalized
	entityPhrases := []string{
		"who is", "who was", "tell me about",
	}
	for _, phrase := range entityPhrases {
		if strings.HasPrefix(lq, phrase) {
			return true
		}
	}

	// Proper noun heuristic: words starting with uppercase mid-query
	words := strings.Fields(q)
	firstWord := true
	for _, w := range words {
		if firstWord {
			firstWord = false
			continue
		}
		if len(w) >= 2 && unicode.IsUpper(rune(w[0])) {
			// Skip common words that aren't entities
			lower := strings.ToLower(w)
			skipWords := map[string]bool{
				"the": true, "a": true, "an": true,
				"this": true, "that": true, "these": true, "those": true,
				"is": true, "are": true, "was": true, "were": true,
				"do": true, "does": true, "did": true,
				"can": true, "could": true, "will": true, "would": true,
				"should": true, "may": true, "might": true, "must": true,
				"my": true, "our": true, "your": true, "its": true, "their": true,
				"me": true, "we": true, "he": true, "she": true, "it": true,
				"they": true, "us": true, "him": true, "her": true, "them": true,
			}
			if skipWords[lower] {
				continue
			}
			return true
		}
	}

	return false
}

// lookupSignals detect fact-retrieval and listing queries.
func hasLookupSignal(lq string) bool {
	// "what is [article]" + abstract adjective → explanatory, skip lookup.
	// "what is the connection string" → lookup.
	if strings.HasPrefix(lq, "what is ") || strings.HasPrefix(lq, "what are ") {
		after := strings.TrimPrefix(lq, "what is ")
		after = strings.TrimPrefix(after, "what are ")
		after = strings.TrimSpace(after)
		// Check for article + abstract adjective markers
		abstractPrefixes := []string{
			"a ", "an ",
			"the best", "the proper", "the correct", "the right", "the most",
			"the difference", "the purpose", "the meaning", "the relationship",
			"the recommended", "the ideal", "the standard", "the fastest",
			"the safest", "the key", "the common", "the preferred", "the optimal",
			"the main", "the primary", "the typical", "the usual", "the expected",
		}
		for _, ap := range abstractPrefixes {
			if strings.HasPrefix(after, ap) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(lq, "what was ") {
		return true
	}

	prefixes := []string{
		"find ", "list ", "show me", "search ",
		"tell me the", "give me", "i need",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lq, p) || strings.Contains(lq, " "+p) {
			return true
		}
	}
	return false
}
