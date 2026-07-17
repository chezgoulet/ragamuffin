package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chezgoulet/ragamuffin/internal/llm"
)

// extraction is the LLM's structured output: entities plus the relations that
// connect them. The extractor resolves entity names to node ids, then writes
// bi-temporal edges.
type extraction struct {
	Entities  []extractedEntity   `json:"entities"`
	Relations []extractedRelation `json:"relations"`
}

type extractedEntity struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // person|org|project|concept
}

type extractedRelation struct {
	Source string `json:"source"` // entity name
	Target string `json:"target"` // entity name
	Type   string `json:"type"`   // relation label, e.g. "works_on", "reports_to"
	Fact   string `json:"fact"`   // natural-language statement of the relation
}

// Extractor turns text into graph entities and bi-temporal edges via an LLM.
type Extractor struct {
	store  *Store
	lm     llm.Synthesizer
	logger *slog.Logger
}

// NewExtractor creates a graph extractor.
func NewExtractor(store *Store, lm llm.Synthesizer, logger *slog.Logger) *Extractor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Extractor{store: store, lm: lm, logger: logger.With("component", "graph.extractor")}
}

// IngestText extracts entities/relations from a block of text and writes them to
// the graph for the given vault. Newer relations of the same (source,target,type)
// invalidate prior ones (bi-temporal supersession). Returns counts.
func (ex *Extractor) IngestText(ctx context.Context, vault, text string) (entities, edges int, err error) {
	if ex.lm == nil {
		return 0, 0, fmt.Errorf("graph: extractor requires an LLM")
	}
	if strings.TrimSpace(text) == "" {
		return 0, 0, nil
	}

	prompt := buildGraphPrompt(text)
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	raw, err := ex.lm.Synthesize(cctx, prompt, "")
	if err != nil {
		return 0, 0, fmt.Errorf("graph: LLM extraction: %w", err)
	}
	parsed, err := parseExtraction(raw)
	if err != nil {
		return 0, 0, fmt.Errorf("graph: parse extraction: %w", err)
	}

	// Resolve entity names → ids (upsert dedupes by vault+name+kind).
	idByName := make(map[string]string, len(parsed.Entities))
	for _, e := range parsed.Entities {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		kind := normalizeKind(e.Kind)
		id, uerr := ex.store.UpsertEntity(ctx, Entity{
			ID:    uuid.NewString(),
			Vault: vault,
			Name:  name,
			Kind:  kind,
		})
		if uerr != nil {
			ex.logger.Warn("graph: upsert entity failed", "name", name, "error", uerr)
			continue
		}
		idByName[strings.ToLower(name)] = id
		entities++
	}

	for _, r := range parsed.Relations {
		srcID, ok1 := idByName[strings.ToLower(strings.TrimSpace(r.Source))]
		tgtID, ok2 := idByName[strings.ToLower(strings.TrimSpace(r.Target))]
		relType := normalizeRelType(r.Type)
		if !ok1 || !ok2 || relType == "" {
			continue
		}
		_, aerr := ex.store.AddEdge(ctx, Edge{
			ID:       uuid.NewString(),
			Vault:    vault,
			SourceID: srcID,
			TargetID: tgtID,
			Type:     relType,
			Fact:     strings.TrimSpace(r.Fact),
		}, true) // invalidate prior edge of same shape
		if aerr != nil {
			ex.logger.Warn("graph: add edge failed", "type", relType, "error", aerr)
			continue
		}
		edges++
	}
	return entities, edges, nil
}

func buildGraphPrompt(text string) string {
	var b strings.Builder
	b.WriteString(`Extract a knowledge graph from the text below.

Return ONLY valid JSON (no markdown, no explanation) with this shape:
{
  "entities": [{"name": "<canonical name>", "kind": "person|org|project|concept"}],
  "relations": [{"source": "<entity name>", "target": "<entity name>", "type": "<snake_case relation>", "fact": "<one sentence stating the relation>"}]
}

Rules:
- Use canonical, deduplicated entity names (e.g. "Alice", not "she" or "Alice Smith / Alice").
- Every relation's source and target MUST appear in entities.
- Relation types are short snake_case verbs, e.g. works_on, reports_to, founded, uses, depends_on.
- Omit anything not clearly stated. Prefer precision over recall.

Text:
`)
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}

// parseExtraction parses the LLM JSON response, tolerating markdown fences.
func parseExtraction(raw string) (extraction, error) {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var out extraction
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return extraction{}, fmt.Errorf("invalid JSON: %w", err)
	}
	return out, nil
}

func normalizeKind(k string) EntityKind {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "person", "people", "individual":
		return KindPerson
	case "org", "organization", "organisation", "company", "team":
		return KindOrg
	case "project", "product", "repo", "repository":
		return KindProject
	default:
		return KindConcept
	}
}

func normalizeRelType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	t = strings.ReplaceAll(t, " ", "_")
	t = strings.ReplaceAll(t, "-", "_")
	// strip anything that isn't [a-z0-9_]
	var b strings.Builder
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_")
}
