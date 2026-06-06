# Dev — Phase 3 Recovery

You fell over mid-Phase 3. Here's where we are.

## Merged to main while you were out
- **#374** — provider health in /health — ✅ merged
- **#377** — embedding timeout (full fix with per-vault config) — ❌ has merge conflicts, needs rebase
- **#379** — /inbox endpoint — ✅ merged
- **#373** — partial embedding timeout fix — superseded by #377, closed

## First: pull main and rebase your open PRs

```bash
cd /opt/data/work/ragamuffin
git fetch origin main
git checkout dev/352-embed-timeout   # #377
git rebase origin/main
# resolve conflicts, git rebase --continue, force push

git checkout dev/recall-filters      # #378
git rebase origin/main
git push --force
```

Same for any other open branches you have.

## Remaining issues to work (in priority order)

### Must complete — new Phase 3 features (added while you were out)

1. **#370** — Knowledge graph edges between facts
   - Normalize supersedes/contradicts into unified edge model (type, source_id, target_id, created_at)
   - Add refines and supports relationship types
   - GET /v1/facts/{key}/graph?depth=N — bidirectional graph traversal
   - Update review handler to set edges on supersede/confirm/reclassify
   - Update pruner to follow edges
   - This is the most important feature in the sprint. Takes priority.

2. **#371** — Conversation-to-fact extraction endpoint
   - POST /v1/ingest/conversation — accepts {messages: [...], vault: "..."}
   - LLM extracts factual claims from transcript
   - Each claim becomes a fact in needs_review status with source type conversation_extraction
   - Facts reference source via conversation_id payload field

3. **#372** — Importance and recency scoring for smart fact pruning
   - Add access_count and last_accessed_at to fact payload
   - Update /recall, /ask, /v1/facts/{key} read paths to increment access_count
   - Compute importance from: access_count, recency, confirmation_count, confidence
   - RAGAMUFFIN_PRUNER_IMPORTANCE_THRESHOLD env var (0.0-1.0, default 0.0 = disabled)
   - Pruner skips facts above threshold even if they exceed age/staleness

### Complete what you already started

4. **#375** — deduplicate isIndexable into shared internal/indexutil — you already PR'd this, just needs rebase + merge
5. **#378** — /recall filters — you already PR'd this, just needs rebase + merge
6. **#273** — deduplicate getPayloadString helpers — no PR yet, still to do

### Standing rules
- After every merge to main, rebase your remaining branches before pushing
- One PR per issue, reference the issue number
- Tests expected with every PR
- If a PR has merge conflicts against main, rebase and fix — don't open a new one
