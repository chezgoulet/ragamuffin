package retrieval

import (
	"strings"
	"testing"
)

func TestClassifyQueryTemporal(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"when was the last deployment", QueryTemporal},
		{"what happened yesterday in the project", QueryTemporal},
		{"recent changes to the config file", QueryTemporal},
		{"timeline for the release schedule", QueryTemporal},
		{"deadline for the Q3 report", QueryTemporal},
		{"show me what changed last week", QueryTemporal},
		{"events from january 2025", QueryTemporal},
		{"when did we migrate to postgres", QueryTemporal},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ClassifyQuery(tt.query); got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestClassifyQueryCausal(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"why did the build fail", QueryCausal},
		{"what caused the outage", QueryCausal},
		{"reason for the rollback", QueryCausal},
		{"impact of the database migration", QueryCausal},
		{"how did the performance degrade", QuerySemantic},
		{"what is the effect of increasing timeout", QueryCausal},
		{"led to the incident", QueryCausal},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ClassifyQuery(tt.query); got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestClassifyQueryEntity(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"who is Alice", QueryEntity},
		{"tell me about Project Aurora", QueryEntity},
		{"who was the architect of the system", QueryEntity},
		{"what does John think about kubernetes", QueryEntity},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ClassifyQuery(tt.query); got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}

	// Proper noun mid-query (capitalized word not at start)
	if got := ClassifyQuery("deployment strategy for ProjectX"); got != QueryEntity {
		t.Errorf("ClassifyQuery with proper noun = %q, want entity", got)
	}
}

func TestClassifyQueryLookup(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"what is the database connection string", QueryLookup},
		{"list all active configurations", QueryLookup},
		{"find the deployment script", QueryLookup},
		{"show me the latest metrics", QueryLookup},
		{"tell me the current status", QueryLookup},
		{"give me the access keys", QueryLookup},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ClassifyQuery(tt.query); got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestClassifyQuerySemantic(t *testing.T) {
	tests := []struct {
		query string
		want  QueryType
	}{
		{"how does the authentication flow work", QuerySemantic},
		{"what is the best practice for error handling", QuerySemantic},
		{"what is the difference between postgres and mysql", QuerySemantic},
		{"what is the purpose of the config file", QuerySemantic},
		{"what is the recommended approach for error handling", QuerySemantic},
		{"how can we improve the pipeline", QuerySemantic},
		{"explain the architecture", QuerySemantic},
		{"", QuerySemantic},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			if got := ClassifyQuery(tt.query); got != tt.want {
				t.Errorf("ClassifyQuery(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestClassifyQueryAllUppercase(t *testing.T) {
	if got := ClassifyQuery("WHEN WAS THE DEPLOYMENT"); got != QueryTemporal {
		t.Errorf("uppercase temporal: got %q, want %q", got, QueryTemporal)
	}
}

func TestClassifyQueryLongQuery(t *testing.T) {
	// Build a long query that still classifies based on its first keyword.
	base := "when did we migrate"
	padding := strings.Repeat(" x ", 10000)
	got := ClassifyQuery(base + padding)
	if got != QueryTemporal {
		t.Errorf("long query with temporal signal: got %q, want %q", got, QueryTemporal)
	}
}

func TestClassifyQueryPriority(t *testing.T) {
	// Temporal beats causal (temporal is checked first)
	if got := ClassifyQuery("why did the deployment fail yesterday"); got != QueryTemporal {
		t.Errorf("temporal should beat causal, got %q", got)
	}

	// Causal beats entity
	if got := ClassifyQuery("why did Project Aurora fail"); got != QueryCausal {
		t.Errorf("causal should beat entity, got %q", got)
	}

	// Entity beats lookup
	if got := ClassifyQuery("who is the Project Aurora lead"); got != QueryEntity {
		t.Errorf("entity should beat lookup, got %q", got)
	}
}
