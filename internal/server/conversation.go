package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	qdrant "github.com/qdrant/go-client/qdrant"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// ── Request/Response types ────────────────────────────────────────────────────

type conversationMessage struct {
	Role    string `json:"role"`    // "user" or "assistant"
	Content string `json:"content"` // message text
}

type conversationRequest struct {
	Vault    string                 `json:"vault"`    // optional in single-tenant mode
	Messages []conversationMessage  `json:"messages"` // conversation transcript
	Context  map[string]interface{} `json:"context,omitempty"` // optional metadata
}

type conversationResponse struct {
	Status         string   `json:"status"`
	ConversationID string   `json:"conversation_id"`
	FactCount      int      `json:"fact_count"`
	Facts          []string `json:"facts"`
}

// extractedFact represents a single factual claim extracted from conversation
type extractedFact struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Confidence  int    `json:"confidence"` // 1-10
	Category    string `json:"category"`   // e.g., preference, knowledge, relationship
	TTLDays     int    `json:"ttl_days"`   // 0 = no expiry
}

// ── POST /v1/ingest/conversation ──────────────────────────────────────────────

func (s *Server) handleIngestConversation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024) // 2 MB limit

	var req conversationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("invalid JSON body: %s", err))
		return
	}

	// Validate messages
	if len(req.Messages) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "at least one message is required")
		return
	}
	for i, msg := range req.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("message %d: role must be 'user' or 'assistant'", i))
			return
		}
		if strings.TrimSpace(msg.Content) == "" {
			writeError(w, 400, "INVALID_REQUEST", fmt.Sprintf("message %d: content is required", i))
			return
		}
	}

	// Resolve vault
	vaultName := req.Vault
	if vaultName == "" {
		if s.cfg.IsMultiTenant() {
			writeError(w, 400, "INVALID_REQUEST", "vault is required in multi-tenant mode")
			return
		}
		vaultName = "default"
	}

	// Ensure fact store is available
	if s.facts == nil {
		writeError(w, 502, "NO_FACT_STORE", "facts collection not configured")
		return
	}

	// Get the facts client (respects per-vault facts)
	fc := s.factsQdrantFor(r.Context())
	if fc == nil {
		writeError(w, 502, "NO_FACT_CLIENT", "no facts client available")
		return
	}

	// Get the LLM synthesizer
	lm := s.llmFor(r.Context())
	if lm == nil {
		writeError(w, 502, "NO_LLM", "LLM not configured")
		return
	}

	// Build conversation transcript text
	transcript := formatConversation(req.Messages)

	// Build LLM extraction prompt
	prompt := buildExtractionPrompt(req.Messages, req.Context)

	// Call LLM
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := lm.Synthesize(ctx, prompt, transcript)
	if err != nil {
		s.log(r.Context()).Error("conversation extraction LLM call failed", "error", err)
		writeError(w, 502, "EXTRACTION_FAILED", fmt.Sprintf("LLM extraction failed: %s", err))
		return
	}

	// Parse extracted facts from LLM response
	facts, err := parseExtractedFacts(result)
	if err != nil {
		s.log(r.Context()).Error("failed to parse extracted facts", "error", err, "raw", result)
		// Fallback: try to create a single fact from the raw response
		facts = []extractedFact{{
			Key:    fmt.Sprintf("conversation-summary-%s", uuid.New().String()[:8]),
			Value:  result,
			Confidence: 5,
		}}
	}

	if len(facts) == 0 {
		writeJSON(w, 200, conversationResponse{
			Status:         "ok",
			ConversationID: "",
			FactCount:      0,
			Facts:          []string{},
		})
		return
	}

	// Generate conversation ID
	conversationID := uuid.New().String()

	// Create facts
	var factKeys []string
	for _, f := range facts {
		key := f.Key
		if key == "" {
			key = fmt.Sprintf("conv-%s-%d", conversationID[:8], len(factKeys))
		}
		factKeys = append(factKeys, key)

		now := time.Now().UTC()

		payload := map[string]*qdrant.Value{
			"fact_key":           qutil.Nv(key),
			"fact_value":         qutil.Nv(f.Value),
			"status":             qutil.Nv("needs_review"),
			"source_type":        qutil.Nv("conversation_extraction"),
			"source":             qutil.Nv("conversation"),
			"conversation_id":    qutil.Nv(conversationID),
			"confidence":         qutil.Nv(float64(f.Confidence)),
			"category":           qutil.Nv(f.Category),
			"created_at":         qutil.Nv(now.Format(time.RFC3339)),
			"updated_at":         qutil.Nv(now.Format(time.RFC3339)),

			// Edge fields (empty)
			"supersedes":         qutil.Nv(""),
			"refines":            qutil.Nv(""),
			"contradicts":        qutil.NvList([]string{}),
			"supports":           qutil.NvList([]string{}),
			"confirmation_count": qutil.Nv(float64(0)),
			"conflict_resolved":  qutil.Nv(true),
			"superseded_by":      qutil.Nv(float64(0)),
		}

		if f.TTLDays > 0 {
			payload["ttl_days"] = qutil.Nv(float64(f.TTLDays))
			payload["expires_at_unix"] = qutil.Nv(float64(now.AddDate(0, 0, f.TTLDays).Unix()))
		}

		// Embed the fact value for semantic search — was previously zero-filled
		vec, err := s.embedder.EmbedSingle(r.Context(), f.Value)
		if err != nil {
			s.log(r.Context()).Error("failed to embed conversation fact", "key", key, "error", err)
			// Fall back to zero vector so the fact is at least stored with metadata
			vec = make([]float32, s.cfg.FactsVectorSize)
		}

		point := &qdrant.PointStruct{
			Id: &qdrant.PointId{
				PointIdOptions: &qdrant.PointId_Uuid{
					Uuid: uuid.New().String(),
				},
			},
			Payload: payload,
			Vectors: &qdrant.Vectors{
				VectorsOptions: &qdrant.Vectors_Vector{
					Vector: &qdrant.Vector{
						Data: vec,
					},
				},
			},
		}

		if err := fc.Upsert(r.Context(), []*qdrant.PointStruct{point}); err != nil {
			s.log(r.Context()).Error("failed to upsert conversation fact", "key", key, "error", err)
			continue
		}
	}

	writeJSON(w, 200, conversationResponse{
		Status:         "ok",
		ConversationID: conversationID,
		FactCount:      len(factKeys),
		Facts:          factKeys,
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// formatConversation formats messages into a readable transcript
func formatConversation(messages []conversationMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		role := strings.ToUpper(msg.Role[:1]) + msg.Role[1:] // "User" / "Assistant"
		b.WriteString(fmt.Sprintf("%s: %s\n", role, strings.TrimSpace(msg.Content)))
	}
	return b.String()
}

// buildExtractionPrompt creates the LLM prompt for fact extraction
func buildExtractionPrompt(messages []conversationMessage, ctx map[string]interface{}) string {
	var b strings.Builder
	b.WriteString(`Extract factual claims from the following conversation transcript.

For each factual claim, return a JSON array of objects. Each object must have these fields:
- "key": A short, unique identifier for this fact (snake_case, descriptive)
- "value": The factual statement itself
- "confidence": Integer 1-10 estimating how certain the claim is
- "category": One of "preference", "knowledge", "relationship", "event", "goal", "identity", "opinion"
- "ttl_days": Number of days until this fact expires (0 = no expiry). Default 90 for transient info like preferences, 365 for knowledge.

Only return the JSON array. No markdown, no explanation.
Example response format:
[{"key":"user_likes_pizza","value":"The user prefers pizza over pasta","confidence":8,"category":"preference","ttl_days":90}]

`)

	// Add context metadata if provided
	if ctx != nil {
		b.WriteString("Context about the conversation:\n")
		for k, v := range ctx {
			b.WriteString(fmt.Sprintf("- %s: %v\n", k, v))
		}
		b.WriteString("\n")
	}

	b.WriteString("Conversation transcript:\n")
	b.WriteString(formatConversation(messages))

	return b.String()
}

// parseExtractedFacts attempts to parse the LLM response as JSON array of extractedFact
func parseExtractedFacts(raw string) ([]extractedFact, error) {
	// Clean response: strip any markdown code fences
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.TrimPrefix(cleaned, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)

	var facts []extractedFact
	if err := json.Unmarshal([]byte(cleaned), &facts); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return facts, nil
}
