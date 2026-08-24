package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/chezgoulet/ragamuffin/internal/retrieval"
)

// maxRerankRequestDocs caps how many documents a single /v1/rerank call may
// carry, mirroring the internal listwise cap (retrieval.Rerank truncates the
// head at its own limit; this guards request size before that).
const maxRerankRequestDocs = 200

// rerankRequest is the body of POST /v1/rerank (issue #877): a query and the
// candidate documents to reorder.
type rerankRequest struct {
	Query string      `json:"query"`
	Docs  []rerankDoc `json:"docs"`
}

type rerankDoc struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// rerankResponse returns the document IDs in relevance order (best first),
// plus the effective provider for observability.
type rerankResponse struct {
	Order    []string `json:"order"`
	Provider string   `json:"provider"`
}

// handleRerank exposes the listwise LLM reranker as a standalone HTTP seam:
//
//	POST /v1/rerank {"query": "...", "docs": [{"id": "...", "text": "..."}]}
//
// It runs retrieval.Rerank against the configured LLM and returns the doc IDs
// in ranked order. Degrades to the input order when no LLM is configured or
// on any error — reranking never fails a caller. Gated by RAGAMUFFIN_RERANK,
// matching recall-side rerank=true semantics.
func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}
	if !s.cfg.RerankEnabled {
		writeError(w, http.StatusNotImplemented, "RERANK_DISABLED",
			"reranking not enabled — set RAGAMUFFIN_RERANK=true")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024) // 4 MB
	var req rerankRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "query is required")
		return
	}
	if len(req.Docs) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "docs must not be empty")
		return
	}
	if len(req.Docs) > maxRerankRequestDocs {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "too many docs")
		return
	}
	docs := make([]retrieval.RerankDoc, 0, len(req.Docs))
	inputIDs := make([]string, 0, len(req.Docs))
	for _, d := range req.Docs {
		if strings.TrimSpace(d.ID) == "" {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "every doc needs an id")
			return
		}
		docs = append(docs, retrieval.RerankDoc{ID: d.ID, Text: d.Text})
		inputIDs = append(inputIDs, d.ID)
	}

	order := inputIDs
	provider := "none"
	c := s.completerFor(r.Context())
	if c != nil {
		order = retrieval.Rerank(r.Context(), c, req.Query, docs)
		provider = "llm"
	}
	if order == nil {
		order = inputIDs
	}
	writeJSON(w, http.StatusOK, rerankResponse{Order: order, Provider: provider})
}
