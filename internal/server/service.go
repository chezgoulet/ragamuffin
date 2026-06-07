package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/qdrant/go-client/qdrant"

	"github.com/chezgoulet/ragamuffin/internal/auth"
	"github.com/chezgoulet/ragamuffin/internal/logstore"
	qutil "github.com/chezgoulet/ragamuffin/internal/qdrantutil"
)

// ── Recall ─────────────────────────────────────────────────────────────────────

// doRecall is the shared business logic for semantic search.
// Both REST handleRecall and MCP mcpRecall call this method,
// ensuring temporal filtering, detail-level filtering, and result
// mapping are identical across both interfaces.
func (s *Server) doRecall(ctx context.Context, req recallRequest) ([]recallResult, float32, error) {
	// Embed query
	vector, err := s.embeddingFor(ctx).EmbedSingle(ctx, req.Query)
	if err != nil {
		return nil, 0, fmt.Errorf("embedding failed: %w", err)
	}

	// Build filter
	filter := recallFilter(req)

	// Apply time filter (fixes the MCP drift vs REST)
	if isTemporalRecall(req.TimeFilter) {
		dateTo := temporalRecallDate(req.TimeFilter)
		if dateTo != "" {
			conditions := []*pb.Condition{
				{
					ConditionOneOf: &pb.Condition_Field{
						Field: &pb.FieldCondition{
							Key: "file_last_updated",
							Range: &pb.Range{
								Lte: float64Ptr(parseRFC3339Unix(dateTo)),
							},
						},
					},
				},
			}
			if filter != nil {
				filter.Must = append(filter.Must, conditions...)
			} else {
				filter = &pb.Filter{Must: conditions}
			}
		}
	}

	// Search
	results, err := s.qdrantFor(ctx).Search(ctx, vector, uint64(req.TopK), float32(req.ScoreThreshold), req.SourceFilter, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("search failed: %w", err)
	}

	// Map results with detail-level filtering
	out := make([]recallResult, 0, len(results))
	var topScore float32
	for _, r := range results {
		payload := r.Payload
		res := recallResult{
			Score:   r.Score,
			ChunkID: r.Id.GetUuid(),
		}
		if r.Score > topScore {
			topScore = r.Score
		}
		if v, ok := payload["text"]; ok {
			res.Text = v.GetStringValue()
		}
		if v, ok := payload["first_paragraph"]; ok {
			res.FirstParagraph = v.GetStringValue()
		}
		if v, ok := payload["source_file"]; ok {
			res.SourceFile = v.GetStringValue()
		}
		if v, ok := payload["header"]; ok {
			res.Header = v.GetStringValue()
		}
		if v, ok := payload["chunk_index"]; ok {
			res.ChunkIndex = int(v.GetIntegerValue())
		}
		if v, ok := payload["file_last_updated"]; ok {
			res.FileLastUpdated = v.GetStringValue()
		}

		// Detail-level field filtering
		switch req.Detail {
		case "l0":
			res.Text = ""
			res.FirstParagraph = ""
		case "l1":
			res.Text = ""
		}
		// l2: include everything

		out = append(out, res)
	}

	return out, topScore, nil
}

// ── Ask / Synthesis ────────────────────────────────────────────────────────────

// doAsk handles the full ask pipeline: retrieval + LLM synthesis.
// Both REST handleAsk and MCP mcpAsk call this method.
func (s *Server) doAsk(ctx context.Context, query, mode string, topK int) (string, []string, string, error) {
	if !s.cfg.HasLLM() {
		return "", nil, "", fmt.Errorf("LLM not configured")
	}

	contextText, sources, modeUsed, err := s.queryContext(ctx, query, mode, topK)
	if err != nil {
		return "", nil, "", fmt.Errorf("retrieval failed: %w", err)
	}

	answer, err := s.llmFor(ctx).Synthesize(ctx, query, contextText)
	if err != nil {
		return "", nil, "", fmt.Errorf("LLM call failed: %w", err)
	}

	return answer, sources, modeUsed, nil
}

// ── Chunk Get ──────────────────────────────────────────────────────────────────

// doGetChunk retrieves a single chunk by UUID. Shared by REST and MCP handlers.
func (s *Server) doGetChunk(ctx context.Context, chunkID string) (map[string]interface{}, error) {
	uid, err := uuid.Parse(chunkID)
	if err != nil {
		return nil, fmt.Errorf("chunk_id must be a valid UUID")
	}
	pointID := pb.NewIDUUID(uid.String())

	qc := s.qdrantFor(ctx)
	pts, err := qc.GetPoints(ctx, qc.Collection(), []*pb.PointId{pointID})
	if err != nil {
		return nil, fmt.Errorf("chunk retrieval failed: %w", err)
	}
	if len(pts) == 0 {
		return nil, fmt.Errorf("chunk with ID %s not found", chunkID)
	}

	pt := pts[0]
	payload := pt.GetPayload()

	resp := map[string]interface{}{
		"chunk_id":          chunkID,
		"source_file":       "",
		"header":            "",
		"text":              "",
		"first_paragraph":   "",
		"chunk_index":       0,
		"file_last_updated": "",
	}

	if v, ok := payload["source_file"]; ok {
		resp["source_file"] = v.GetStringValue()
	}
	if v, ok := payload["header"]; ok {
		resp["header"] = v.GetStringValue()
	}
	if v, ok := payload["text"]; ok {
		resp["text"] = v.GetStringValue()
	}
	if v, ok := payload["first_paragraph"]; ok {
		resp["first_paragraph"] = v.GetStringValue()
	}
	if v, ok := payload["chunk_index"]; ok {
		resp["chunk_index"] = int(v.GetIntegerValue())
	}
	if v, ok := payload["file_last_updated"]; ok {
		resp["file_last_updated"] = v.GetStringValue()
	}

	return resp, nil
}

// ── Draft / File Write ─────────────────────────────────────────────────────────

// draftResult is the shared return type for draft operations.
type draftResult struct {
	Mode    string `json:"mode"`
	Path    string `json:"path"`
	Written bool   `json:"written"`
	PRURL   string `json:"pr_url,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// doDraft handles file write/delete/PR operations. Shared by REST and MCP.
func (s *Server) doDraft(ctx context.Context, req draftRequest) (*draftResult, error) {
	// Enforce write access
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}
	if req.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	if req.TargetPath == "" {
		return nil, fmt.Errorf("target_path is required")
	}
	if req.Mode == "" {
		req.Mode = "direct"
	}

	cleanPath := filepath.Clean(req.TargetPath)
	vaultPath := s.vaultPathFromContext(ctx)
	fullPath, err := safeVaultPath(vaultPath, cleanPath)
	if err != nil {
		return nil, err
	}

	if req.Mode == "pr" {
		if !s.cfg.HasGit() {
			return nil, fmt.Errorf("git provider not configured")
		}
		prURL, branch, err := s.createPR(req.Title, req.Content, cleanPath, req.Description)
		if err != nil {
			return nil, fmt.Errorf("PR creation failed: %w", err)
		}
		return &draftResult{
			Mode:   "pr",
			Path:   cleanPath,
			PRURL:  prURL,
			Branch: branch,
		}, nil
	}

	if req.Delete {
		if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("delete failed: %w", err)
		}
	} else if req.Content == "" {
		return nil, fmt.Errorf("content required unless delete=true")
	} else {
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir failed: %w", err)
		}
		if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
			return nil, fmt.Errorf("write failed: %w", err)
		}
	}

	return &draftResult{
		Mode:    "direct",
		Path:    cleanPath,
		Written: true,
	}, nil
}

// ── Store / Ingest ─────────────────────────────────────────────────────────────

// storeResult is the shared return type for ingest operations.
type storeResult struct {
	Status     string `json:"status"`
	Vault      string `json:"vault"`
	Source     string `json:"source"`
	ChunkCount int    `json:"chunk_count"`
}

// doStore ingests content into a vault. Shared by REST documents handler and MCP.
func (s *Server) doStore(ctx context.Context, content, source, vaultName string, tags []string) (*storeResult, error) {
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	if source == "" {
		return nil, fmt.Errorf("source is required")
	}

	if vaultName == "" {
		vaultName = "default"
	}

	idx := s.indexers.Get(vaultName)
	if idx == nil {
		idx = s.provisionVault(ctx, vaultName)
		if idx == nil {
			return nil, fmt.Errorf("vault %q not found and could not be provisioned", vaultName)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := idx.Ingest(ctx, content, source, tags); err != nil {
		return nil, fmt.Errorf("ingest failed: %w", err)
	}

	_, chunkCount, _, _, _, _ := idx.Stats()

	return &storeResult{
		Status:     "ok",
		Vault:      vaultName,
		Source:     source,
		ChunkCount: chunkCount,
	}, nil
}

// ── Stats ──────────────────────────────────────────────────────────────────────

// statsResult is the shared return type for vault statistics.
type statsResult struct {
	Vault          string `json:"vault"`
	IndexedFiles   int    `json:"indexed_files"`
	TotalChunks    int    `json:"total_chunks"`
	TotalFacts     int    `json:"total_facts"`
	LastIndexed    string `json:"last_indexed"`
	VaultAgeDays   int    `json:"vault_age_days"`
}

// doStats collects vault operational metrics. Shared by REST and MCP.
func (s *Server) doStats(ctx context.Context) (*statsResult, error) {
	vaultName := vaultFromContext(ctx)
	if vaultName == "" {
		vaultName = "default"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var fileCount, chunkCount int
	var lastIndexed time.Time

	idx := s.indexers.Get(vaultName)
	if idx != nil {
		fileCount, chunkCount, lastIndexed, _, _, _ = idx.Stats()
	} else if !s.cfg.IsMultiTenant() {
		idx2 := s.indexerFor(ctx)
		if idx2 != nil {
			fileCount, chunkCount, lastIndexed, _, _, _ = idx2.Stats()
		}
	}

	factsCtx, factsCancel := context.WithTimeout(ctx, 5*time.Second)
	defer factsCancel()
	totalFacts, err := s.facts.Count(factsCtx)
	if err != nil {
		s.log(ctx).Warn("stats: facts count failed", "error", err)
		totalFacts = 0
	}

	vaultAgeDays := 0
	if !lastIndexed.IsZero() {
		vaultAgeDays = int(time.Since(lastIndexed).Hours() / 24)
	}

	return &statsResult{
		Vault:        vaultName,
		IndexedFiles: fileCount,
		TotalChunks:  chunkCount,
		TotalFacts:   int(totalFacts),
		LastIndexed:  lastIndexed.Format(time.RFC3339),
		VaultAgeDays: vaultAgeDays,
	}, nil
}

// ── Audit ──────────────────────────────────────────────────────────────────────

// doAudit runs vault health checks. Shared by REST and MCP.
func (s *Server) doAudit(ctx context.Context, req auditRequest) (map[string]interface{}, error) {
	staleDays := req.StaleDays
	if staleDays <= 0 {
		staleDays = 90
	}

	checks := req.Checks
	if len(checks) == 0 {
		checks = []string{"stale", "semantic_conflict", "gap", "duplicate"}
	}

	sampleSize := req.SampleSize
	if sampleSize <= 0 {
		sampleSize = 50
	}

	vaultPath := s.vaultPathFromContext(ctx)
	var qc = s.qdrantFor(ctx)

	resp := map[string]interface{}{
		"checks_run": checks,
	}

	checkSet := make(map[string]bool)
	for _, c := range checks {
		checkSet[c] = true
	}

	if checkSet["stale"] {
		staleFiles, err := s.checkStaleness(vaultPath, staleDays)
		if err != nil {
			s.log(ctx).Error("audit: staleness check failed", "error", err)
		}
		resp["stale_files"] = staleFiles
	}

	if checkSet["gap"] {
		gaps := s.checkGaps(vaultPath)
		resp["gaps"] = gaps
	}

	if checkSet["duplicate"] {
		dupes := s.checkDuplicates(vaultPath)
		resp["duplicates"] = dupes
	}

	if checkSet["semantic_conflict"] {
		if !s.cfg.HasLLM() {
			resp["semantic_conflicts"] = []interface{}{}
		} else {
			auditCtx, auditCancel := context.WithTimeout(ctx, 5*time.Minute)
			defer auditCancel()
			conflicts, llmCalls := s.checkSemanticConflicts(auditCtx, qc, sampleSize)
			resp["semantic_conflicts"] = conflicts
			resp["semantic_conflict_llm_calls"] = llmCalls
		}
	}

	return resp, nil
}

// ── Session helpers ────────────────────────────────────────────────────────────

// doCreateSession creates a new conversation session. Shared by REST and MCP.
func (s *Server) doCreateSession(ctx context.Context, agentID, content, vault, source string, autoExtract *bool) (map[string]interface{}, error) {
	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	sessionID := uuid.New().String()

	if vault == "" {
		vault = fmt.Sprintf("agent::%s", agentID)
	}

	sess, err := s.logStore.CreateSession(ctx, sessionID, vault, agentID, source)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	if autoExtract != nil && s.extractor != nil {
		s.extractor.SetSessionAutoExtract(sessionID, *autoExtract)
	}

	turnCount := sess.TurnCount
	if content != "" {
		turn, err := s.logStore.AppendTurn(ctx, sessionID, content, "user")
		if err != nil {
			s.log(ctx).Warn("session create: initial turn append failed", "error", err)
		} else {
			turnCount = 1
			// Async index initial turn into vault's Qdrant collection (#523)
			go s.indexSessionTurn(s.shutdownCtx, sessionID, content, "user", turn.ID)
		}
	}

	ae := false
	if autoExtract != nil {
		ae = *autoExtract
	}

	return map[string]interface{}{
		"session_id":   sessionID,
		"id":           sess.ID,
		"vault":        sess.Vault,
		"agent_id":     sess.AgentID,
		"source":       sess.Source,
		"turn_count":   turnCount,
		"created_at":   sess.CreatedAt,
		"updated_at":   sess.UpdatedAt,
		"auto_extract": ae,
	}, nil
}

// doGetSession retrieves a session with its turns. Shared by REST and MCP.
func (s *Server) doGetSession(ctx context.Context, sessionID string, turnLimit int) (map[string]interface{}, error) {
	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	sess, turns, err := s.logStore.GetSession(ctx, sessionID, turnLimit)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("session %q not found", sessionID)
		}
		return nil, fmt.Errorf("get session: %w", err)
	}

	turnData := make([]map[string]interface{}, len(turns))
	for i, t := range turns {
		turnData[i] = map[string]interface{}{
			"id":         t.ID,
			"content":    t.Content,
			"role":       t.Role,
			"created_at": t.CreatedAt,
		}
	}

	return map[string]interface{}{
		"session_id": sess.ID,
		"id":         sess.ID,
		"vault":      sess.Vault,
		"agent_id":   sess.AgentID,
		"source":     sess.Source,
		"turn_count": sess.TurnCount,
		"created_at": sess.CreatedAt,
		"updated_at": sess.UpdatedAt,
		"turns":      turnData,
	}, nil
}

// doListSessions lists sessions, optionally filtered. Shared by REST and MCP.
func (s *Server) doListSessions(ctx context.Context, agentID, vault string, limit, offset int) ([]logstore.Session, error) {
	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	if agentID != "" && vault == "" {
		vault = fmt.Sprintf("agent::%s", agentID)
	}

	if limit <= 0 {
		limit = 100
	}

	sessions, err := s.logStore.ListSessions(ctx, vault, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	return sessions, nil
}

// doAppendTurn appends a turn to an existing session. Shared by REST and MCP.
func (s *Server) doAppendTurn(ctx context.Context, sessionID, content, role string, autoExtract *bool) (map[string]interface{}, error) {
	if s.logStore == nil {
		return nil, fmt.Errorf("session store not available")
	}
	if sessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	if role == "" {
		role = "user"
	}

	turn, err := s.logStore.AppendTurn(ctx, sessionID, content, role)
	if err != nil {
		return nil, fmt.Errorf("append turn: %w", err)
	}

	// Trigger extraction if auto_extract is set
	extract := false
	if autoExtract != nil {
		extract = *autoExtract
	} else if s.extractor != nil {
		extract = s.extractor.SessionAutoExtract(sessionID)
	}
	if extract && s.extractor != nil && s.extractor.Enabled() {
		go s.extractor.Extract(s.shutdownCtx, sessionID, content, role)
	}

	// Async index turn into vault's Qdrant collection (#523)
	go s.indexSessionTurn(s.shutdownCtx, sessionID, content, role, turn.ID)

	return map[string]interface{}{
		"turn_id":    turn.ID,
		"session_id": turn.SessionID,
		"role":       turn.Role,
		"created_at": turn.CreatedAt,
	}, nil
}

// indexSessionTurn asynchronously indexes a turn's content into the vault's
// Qdrant collection so session conversations become searchable via /ask (#523).
func (s *Server) indexSessionTurn(ctx context.Context, sessionID, content, role string, turnID int64) {
	if s.logStore == nil || s.indexers == nil {
		return
	}

	// Resolve the vault from the session
	sess, _, err := s.logStore.GetSession(ctx, sessionID, 1)
	if err != nil {
		s.log(ctx).Debug("session index: session not found", "session_id", sessionID, "error", err)
		return
	}

	vault := sess.Vault
	if vault == "" {
		vault = "default"
	}

	idx := s.indexers.Get(vault)
	if idx == nil {
		// Vault might not exist yet — attempt provision in multi-tenant mode
		if !s.cfg.AutoProvisionVaults {
			s.log(ctx).Debug("session index: vault not found", "vault", vault)
			return
		}
		idx = s.provisionVault(ctx, vault)
		if idx == nil {
			s.log(ctx).Debug("session index: could not provision vault", "vault", vault)
			return
		}
	}

	source := fmt.Sprintf("session:%s/turn:%d", sessionID, turnID)
	if err := idx.Ingest(ctx, fmt.Sprintf("%s: %s", role, content), source, []string{"session"}); err != nil {
		s.log(ctx).Warn("session index: ingest failed", "session_id", sessionID, "turn", turnID, "error", err)
	} else {
		s.log(ctx).Debug("session index: turn indexed", "session_id", sessionID, "turn", turnID, "vault", vault)
	}
}

// ── Facts helpers ──────────────────────────────────────────────────────────────

// factToMap converts a *factResponse to a plain map for MCP JSON serialization.
func factToMap(fr *factResponse) map[string]interface{} {
	m := map[string]interface{}{
		"key":                fr.Key,
		"value":              fr.Value,
		"confidence":         fr.Confidence,
		"status":             fr.Status,
		"supersedes":         fr.Supersedes,
		"conflict_resolved":  fr.ConflictResolved,
		"confirmation_count": fr.ConfirmationCount,
		"created_at":         fr.CreatedAt,
		"updated_at":         fr.UpdatedAt,
	}
	if len(fr.Tags) > 0 {
		tags := make([]string, len(fr.Tags))
		copy(tags, fr.Tags)
		m["tags"] = tags
	}
	if fr.Source != "" {
		m["source"] = fr.Source
	}
	if fr.SourceType != "" {
		m["source_type"] = fr.SourceType
	}
	if len(fr.Contradicts) > 0 {
		m["contradicts"] = fr.Contradicts
	}
	if fr.LastConfirmedAt != "" {
		m["last_confirmed_at"] = fr.LastConfirmedAt
	}
	return m
}

// doFactsList retrieves facts by key, prefix, tag, or status. Shared by REST and MCP.
func (s *Server) doFactsList(ctx context.Context, key, prefix, tag, status string, limit int) (interface{}, error) {
	factsCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if key != "" {
		points, err := s.facts.ScrollFiltered(factsCtx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
		if err != nil {
			return nil, fmt.Errorf("facts query failed: %w", err)
		}
		if len(points) == 0 {
			return nil, fmt.Errorf("fact not found: %s", key)
		}
		fr := pointToFact(points[0])
		if fr == nil {
			return nil, fmt.Errorf("corrupt fact data for key: %s", key)
		}
		return map[string]interface{}{"facts": []interface{}{factToMap(fr)}}, nil
	}

	var conditions []*pb.Condition
	if prefix != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "fact_key",
					Match: &pb.Match{
						MatchValue: &pb.Match_Text{Text: prefix},
					},
				},
			},
		})
	}
	if tag != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "fact_tags",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: tag},
					},
				},
			},
		})
	}
	if status != "" {
		conditions = append(conditions, &pb.Condition{
			ConditionOneOf: &pb.Condition_Field{
				Field: &pb.FieldCondition{
					Key: "status",
					Match: &pb.Match{
						MatchValue: &pb.Match_Keyword{Keyword: status},
					},
				},
			},
		})
	}

	var filter *pb.Filter
	if len(conditions) > 0 {
		filter = &pb.Filter{Must: conditions}
	}

	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	points, err := s.facts.ScrollFiltered(factsCtx, s.factsCollectionFor(ctx), filter, uint32(limit), "")
	if err != nil {
		return nil, fmt.Errorf("facts query failed: %w", err)
	}

	facts := make([]interface{}, 0, len(points))
	for _, p := range points {
		if fr := pointToFact(p); fr != nil {
			facts = append(facts, factToMap(fr))
		}
	}

	return map[string]interface{}{"facts": facts, "count": len(facts)}, nil
}

// doFactsUpsert creates or updates a fact. Shared by REST and MCP.
func (s *Server) doFactsUpsert(ctx context.Context, key, value, source, sourceType string, tags []string, confidence float64, ttlDays int) (map[string]interface{}, error) {
	// Enforce write access — same as handleFactsPost in facts.go
	if claims := auth.ClaimsFromContext(ctx); claims != nil && !claims.HasAccess("write") {
		return nil, fmt.Errorf("write access required")
	}

	if key == "" {
		return nil, fmt.Errorf("key is required for upsert")
	}
	if value == "" {
		return nil, fmt.Errorf("value is required for upsert")
	}

	created := false

	// Check if fact exists
	exists, err := s.factExists(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to check fact existence: %w", err)
	}
	if !exists {
		created = true
	}

	// Build the fact payload (mirrors handleFactsPost's logic)
	now := time.Now().UTC().Format(time.RFC3339)
	var createdAt string
	var confirmationCount int64 = 1
	var lastConfirmedAt string

	if exists {
		points, err := s.facts.ScrollFiltered(ctx, s.factsCollectionFor(ctx), factKeyFilter(key), 1, "")
		if err == nil && len(points) > 0 {
			createdAt, _ = qutil.GetPayloadString(points[0].GetPayload(), "created_at")
			if cc, ok := points[0].GetPayload()["confirmation_count"]; ok {
				confirmationCount = cc.GetIntegerValue() + 1
			}
			lastConfirmedAt = now
		}
	}
	if createdAt == "" {
		createdAt = now
	}

	if confidence < 0 || confidence > 1.0 {
		confidence = 1.0
	}

	if ttlDays < 0 {
		ttlDays = 0
	}

	expiresAt := computeExpiresAt(ttlDays)
	var expiresAtUnix float64
	if ttlDays > 0 {
		expiresAtUnix = float64(time.Now().UTC().AddDate(0, 0, ttlDays).Unix())
	}

	payload := pb.NewValueMap(map[string]interface{}{
		"fact_key":           key,
		"fact_value":         value,
		"source":             source,
		"source_type":        sourceType,
		"confidence":         confidence,
		"status":             "active",
		"supersedes":         "",
		"conflict_resolved":  true,
		"confirmation_count": confirmationCount,
		"last_confirmed_at":  lastConfirmedAt,
		"created_at":         createdAt,
		"updated_at":         now,
		"ttl_days":           int64(ttlDays),
		"expires_at":         expiresAt,
		"expires_at_unix":    expiresAtUnix,
	})
	payload["contradicts"] = &pb.Value{
		Kind: &pb.Value_ListValue{
			ListValue: &pb.ListValue{Values: []*pb.Value{}},
		},
	}

	if len(tags) > 0 {
		tagVals := make([]*pb.Value, len(tags))
		for i, t := range tags {
			tagVals[i] = qutil.Nv(t)
		}
		payload["fact_tags"] = &pb.Value{
			Kind: &pb.Value_ListValue{
				ListValue: &pb.ListValue{Values: tagVals},
			},
		}
	}

	pointID := factKeyHash(key)
	point := &pb.PointStruct{
		Id: &pb.PointId{
			PointIdOptions: &pb.PointId_Uuid{Uuid: pointID},
		},
		Payload: payload,
		Vectors: &pb.Vectors{
			VectorsOptions: &pb.Vectors_Vector{
				Vector: &pb.Vector{Data: []float32{0, 0, 0, 0}},
			},
		},
	}

	if err := s.facts.Upsert(ctx, []*pb.PointStruct{point}); err != nil {
		return nil, fmt.Errorf("failed to store fact: %w", err)
	}

	return map[string]interface{}{
		"key":     key,
		"value":   value,
		"status":  "active",
		"created": created,
	}, nil
}

// ── Graph ──────────────────────────────────────────────────────────────────────

// doGraphFull returns the full entity graph for a vault by scrolling all chunks.
func (s *Server) doGraphFull(ctx context.Context, vaultName string, limit int) (map[string]interface{}, error) {
	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		return map[string]interface{}{"nodes": []interface{}{}, "edges": []interface{}{}}, nil
	}

	nodes := make(map[string]graphNode)
	edges := make(map[string]graphEdge)
	edgeKey := func(s, t, rel string) string { return s + "|" + t + "|" + rel }

	var offset *pb.PointId
	totalNodes := 0

	for {
		scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
		points, nextOffset, err := qc.Scroll(scrollCtx, 100, offset)
		scrollCancel()
		if err != nil {
			s.log(ctx).Warn("graph: scroll failed", "error", err)
			break
		}

		for _, point := range points {
			if totalNodes >= limit {
				break
			}

			sourceFile := point.GetPayload()["source_file"].GetStringValue()
			if sourceFile == "" {
				continue
			}

			fileID := "file:" + sourceFile
			if _, exists := nodes[fileID]; !exists {
				nodes[fileID] = graphNode{
					ID:    fileID,
					Type:  "file",
					Label: displayName(sourceFile),
				}
				totalNodes++
			}

			if linksVal := point.GetPayload()["links_to"]; linksVal != nil {
				for _, linkVal := range linksVal.GetListValue().GetValues() {
					targetFile := linkVal.GetStringValue()
					if targetFile == "" {
						continue
					}
					targetID := "file:" + targetFile
					k := edgeKey(fileID, targetID, "links_to")
					if _, exists := edges[k]; !exists {
						edges[k] = graphEdge{
							Source:       fileID,
							Target:       targetID,
							Relationship: "links_to",
						}
					}
				}
			}
		}

		if nextOffset == nil || totalNodes >= limit {
			break
		}
		offset = nextOffset
	}

	nodeList := make([]map[string]interface{}, 0, len(nodes))
	edgeList := make([]map[string]interface{}, 0, len(edges))
	for _, n := range nodes {
		nodeList = append(nodeList, map[string]interface{}{
			"id": n.ID, "type": n.Type, "label": n.Label,
		})
		if len(nodeList) >= limit {
			break
		}
	}
	for _, e := range edges {
		edgeList = append(edgeList, map[string]interface{}{
			"source": e.Source, "target": e.Target, "relationship": e.Relationship,
		})
		if len(edgeList) >= limit {
			break
		}
	}

	return map[string]interface{}{"nodes": nodeList, "edges": edgeList}, nil
}

// doGraphEntity returns an entity-focused graph using BFS traversal.
func (s *Server) doGraphEntity(ctx context.Context, vaultName, entity string, depth, limit int) (map[string]interface{}, error) {
	qc := s.indexers.GetClient(vaultName)
	if qc == nil {
		if depth == 0 {
			eb := newEntityBFS(entity, depth, limit)
			return map[string]interface{}{"nodes": eb.Nodes(), "edges": eb.Edges()}, nil
		}
		return map[string]interface{}{"nodes": []interface{}{}, "edges": []interface{}{}}, nil
	}

	eb := newEntityBFS(entity, depth, limit)

	{
		var scrollOffset *pb.PointId
		for {
			scrollCtx, scrollCancel := context.WithTimeout(ctx, 10*time.Second)
			points, nextOffset, err := qc.Scroll(scrollCtx, 500, scrollOffset)
			scrollCancel()
			if err != nil {
				break
			}
			for _, p := range points {
				if p.GetPayload() == nil {
					continue
				}

				src := p.GetPayload()["source_file"].GetStringValue()
				if src != "" {
					if text := p.GetPayload()["text"].GetStringValue(); text != "" && strings.Contains(text, entity) {
						eb.AddMatch(src)
					}
					if linksVal := p.GetPayload()["links_to"]; linksVal != nil {
						for _, linkVal := range linksVal.GetListValue().GetValues() {
							eb.AddLink(src, linkVal.GetStringValue())
						}
					}
				}
			}
			if nextOffset == nil || len(eb.Nodes()) >= limit {
				break
			}
			scrollOffset = nextOffset
		}
	}

	eb.Run()

	nodeList := make([]map[string]interface{}, 0, len(eb.Nodes()))
	edgeList := make([]map[string]interface{}, 0, len(eb.Edges()))
	for _, n := range eb.Nodes() {
		nodeList = append(nodeList, map[string]interface{}{
			"id": n.ID, "type": n.Type, "label": n.Label,
		})
	}
	for _, e := range eb.Edges() {
		edgeList = append(edgeList, map[string]interface{}{
			"source": e.Source, "target": e.Target, "relationship": e.Relationship,
		})
	}

	return map[string]interface{}{"nodes": nodeList, "edges": edgeList}, nil
}
