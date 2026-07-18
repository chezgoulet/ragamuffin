package server

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// citation is a single chunk-level attribution surfaced in an /ask response
// when citation mode is requested (#A4). Each entry corresponds to a chunk ID
// the model cited inline via a [cite: <chunk_id>] marker.
type citation struct {
	ChunkID    string  `json:"chunk_id"`
	SourceFile string  `json:"source_file"`
	ChunkIndex int     `json:"chunk_index"`
	Score      float32 `json:"score"`
	Text       string  `json:"text,omitempty"`
}

// citeMarker matches inline citation markers of the form [cite: <chunk_id>].
// The chunk_id capture is permissive (UUIDs, "none") and trimmed by the caller.
var citeMarker = regexp.MustCompile(`\[cite:\s*([^\]]+)\]`)

// citedChunk holds the retrieval metadata for a chunk that may be cited.
type citedChunk struct {
	chunkID    string
	sourceFile string
	chunkIndex int
	score      float32
	text       string
}

// buildCitedContext renders retrieval results into a prompt context block whose
// passages are labelled with chunk IDs so the model can attribute sentences.
// It returns the context text plus a lookup keyed by chunk ID for resolving the
// markers the model emits.
func buildCitedContext(chunks []citedChunk) (string, map[string]citedChunk) {
	lookup := make(map[string]citedChunk, len(chunks))
	var b strings.Builder
	for _, c := range chunks {
		if c.chunkID == "" {
			continue
		}
		lookup[c.chunkID] = c
		b.WriteString(fmt.Sprintf("[chunk_id: %s | Source: %s | Chunk %d | Score: %.3f]\n",
			c.chunkID, c.sourceFile, c.chunkIndex, c.score))
		b.WriteString(c.text)
		b.WriteString("\n\n")
	}
	return b.String(), lookup
}

// parseCitations extracts the ordered, de-duplicated set of chunk IDs the model
// cited in its answer and resolves them against the retrieval lookup. IDs that
// were not part of the supplied context (hallucinated) and the sentinel "none"
// are dropped. Order follows first appearance in the answer.
func parseCitations(answer string, lookup map[string]citedChunk) []citation {
	matches := citeMarker.FindAllStringSubmatch(answer, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []citation
	for _, m := range matches {
		for _, raw := range strings.Split(m[1], ",") {
			id := strings.TrimSpace(raw)
			if id == "" || strings.EqualFold(id, "none") || seen[id] {
				continue
			}
			c, ok := lookup[id]
			if !ok {
				continue
			}
			seen[id] = true
			out = append(out, citation{
				ChunkID:    c.chunkID,
				SourceFile: c.sourceFile,
				ChunkIndex: c.chunkIndex,
				Score:      c.score,
				Text:       c.text,
			})
		}
	}
	return out
}

// doAskCited answers a query with sentence-level chunk attributions. It embeds
// the query, retrieves chunks, builds a chunk-ID-labelled context, asks the LLM
// to cite inline, then parses the markers into a structured citation list.
func (s *Server) doAskCited(ctx context.Context, query string, topK int) (string, []string, []citation, error) {
	if !s.cfg.HasLLM() {
		return "", nil, nil, fmt.Errorf("LLM not configured")
	}

	vector, err := s.embeddingFor(ctx).EmbedSingle(ctx, query)
	if err != nil {
		return "", nil, nil, fmt.Errorf("embedding failed: %w", err)
	}
	results, err := s.qdrantFor(ctx).Search(ctx, vector, uint64(topK), 0.0, "", nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("search failed: %w", err)
	}

	chunks := make([]citedChunk, 0, len(results))
	sources := make([]string, 0, len(results))
	seenSrc := make(map[string]bool)
	for _, r := range results {
		payload := r.GetPayload()
		c := citedChunk{chunkID: r.GetId().GetUuid(), score: r.GetScore()}
		if payload != nil {
			c.sourceFile, _ = qutil.GetPayloadString(payload, "source_file")
			if v, ok := payload["chunk_index"]; ok {
				c.chunkIndex = int(v.GetIntegerValue())
			}
			c.text, _ = qutil.GetPayloadString(payload, "text")
		}
		if c.sourceFile != "" && !seenSrc[c.sourceFile] {
			seenSrc[c.sourceFile] = true
			sources = append(sources, c.sourceFile)
		}
		chunks = append(chunks, c)
	}

	contextText, lookup := buildCitedContext(chunks)

	answer, err := s.llmFor(ctx).SynthesizeCited(ctx, query, contextText)
	if err != nil {
		return "", nil, nil, fmt.Errorf("LLM call failed: %w", err)
	}

	return answer, sources, parseCitations(answer, lookup), nil
}
