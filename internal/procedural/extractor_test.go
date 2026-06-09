package procedural

import (
	"testing"
)

// ── Fixtures ───────────────────────────────────────────────────────────────────

// sampleTurns is a realistic session trace with a successful action sequence.
var sampleTurns = []Turn{
	{Content: "The nginx service won't start after a config change", Role: "user"},
	{Content: "Let me check the nginx config syntax.\n\n```bash\nnginx -t\n```", Role: "assistant"},
	{Content: "nginx: [emerg] unknown directive 'timeout' in /etc/nginx/nginx.conf:42", Role: "system"},
	{Content: "I see a syntax error. Let me read the config file to understand the issue.\n\n```bash\ncat -n /etc/nginx/nginx.conf | head -50\n```", Role: "assistant"},
	{Content: "There's a typo on line 42 — `timeout` should be `send_timeout`. Let me fix it.\n\n```bash\nsed -i 's/timeout/send_timeout/' /etc/nginx/nginx.conf\n```", Role: "assistant"},
	{Content: "Now let me verify the syntax and reload nginx.\n\n```bash\nnginx -t && systemctl reload nginx\n```", Role: "assistant"},
	{Content: "Syntax is ok. nginx reloaded successfully.", Role: "system"},
	{Content: "Let me verify it's serving correctly.\n\n```bash\ncurl -sI http://localhost | head -1\n```", Role: "assistant"},
	{Content: "HTTP/1.1 200 OK", Role: "system"},
	{Content: "ok, the fix works. nginx is back up.", Role: "user"},
}

// noOutcomeTurns is a session with action sequences but no positive resolution.
var noOutcomeTurns = []Turn{
	{Content: "nginx is broken", Role: "user"},
	{Content: "Let me check the config.\n\n```bash\nnginx -t\n```", Role: "assistant"},
	{Content: "nginx: [emerg] unknown directive", Role: "system"},
	{Content: "Let me fix it.\n\n```bash\nsed -i 's/bad/good/' /etc/nginx/nginx.conf\n```", Role: "assistant"},
	{Content: "nginx -t && systemctl reload nginx", Role: "assistant"},
	{Content: "still broken. it's still failing.", Role: "user"},
}

// shortSession is a session with fewer than minSteps action turns.
var shortSession = []Turn{
	{Content: "What's the weather?", Role: "user"},
	{Content: "Let me check.\n\ncurl -s wttr.in", Role: "assistant"},
	{Content: "It's 72°F and sunny.", Role: "user"},
}

// emptySession has no turns.
var emptySession []Turn

// mixedSession has actions interspersed with non-action assistant turns.
var mixedSession = []Turn{
	{Content: "The database is slow", Role: "user"},
	{Content: "Let me check the query performance.\n\n```sql\nEXPLAIN ANALYZE SELECT * FROM users WHERE email = 'test@test.com';\n```", Role: "assistant"},
	{Content: "Seq Scan on users...", Role: "system"},
	{Content: "I think an index would help. Let me check existing indexes.\n\n```sql\nSELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'users';\n```", Role: "assistant"},
	{Content: "No index on email column", Role: "system"},
	{Content: "Let me create one.\n\n```sql\nCREATE INDEX idx_users_email ON users(email);\n```", Role: "assistant"},
	{Content: "CREATE INDEX", Role: "system"},
	{Content: "Check query again: SELECT * FROM users WHERE email = 'test@test.com'", Role: "assistant"},
	{Content: "Result returned quickly.", Role: "system"},
	{Content: "ok, that fixed it. Much faster now.", Role: "user"},
}

// ── Tests ──────────────────────────────────────────────────────────────────────

func TestExtractEmptySession(t *testing.T) {
	procs := Extract(emptySession, DefaultMinSteps)
	if len(procs) != 0 {
		t.Errorf("expected 0 procedures from empty session, got %d", len(procs))
	}
}

func TestExtractShortSession(t *testing.T) {
	procs := Extract(shortSession, DefaultMinSteps)
	if len(procs) != 0 {
		t.Errorf("expected 0 procedures from short session, got %d", len(procs))
	}
}

func TestExtractNoOutcome(t *testing.T) {
	procs := Extract(noOutcomeTurns, DefaultMinSteps)
	if len(procs) != 0 {
		t.Errorf("expected 0 procedures from unresolved session, got %d", len(procs))
	}
}

func TestExtractSuccessfulSequence(t *testing.T) {
	procs := Extract(sampleTurns, DefaultMinSteps)
	if len(procs) == 0 {
		t.Fatal("expected at least 1 procedure from successful session, got 0")
	}

	proc := procs[0]
	if proc.Name == "" {
		t.Error("procedure name should not be empty")
	}
	if len(proc.Steps) < DefaultMinSteps {
		t.Errorf("expected at least %d steps, got %d", DefaultMinSteps, len(proc.Steps))
	}
	if proc.SuccessCount != 1 {
		t.Errorf("expected SuccessCount=1 for new procedure, got %d", proc.SuccessCount)
	}
	if proc.Trigger == "" {
		t.Error("expected non-empty trigger")
	}
}

func TestExtractMixedSession(t *testing.T) {
	procs := Extract(mixedSession, DefaultMinSteps)
	if len(procs) == 0 {
		t.Fatal("expected at least 1 procedure from mixed session, got 0")
	}

	proc := procs[0]
	if len(proc.Steps) < DefaultMinSteps {
		t.Errorf("expected at least %d steps, got %d", DefaultMinSteps, len(proc.Steps))
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	k1 := DeriveKey("Fix nginx config", "nginx won't start")
	k2 := DeriveKey("Fix nginx config", "nginx won't start")
	if k1 != k2 {
		t.Errorf("expected deterministic keys, got %q and %q", k1, k2)
	}
	if len(k1) == 0 {
		t.Error("expected non-empty key")
	}
}

func TestDeriveKeyDifferentInputs(t *testing.T) {
	k1 := DeriveKey("Fix nginx", "nginx broken")
	k2 := DeriveKey("Fix database", "db slow")
	if k1 == k2 {
		t.Errorf("expected different keys for different inputs, got %q", k1)
	}
}

func TestIsActionContent(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"run this command", true},
		{"check the status", true},
		{"I think the issue is...", false},
		{"Let me think about this", false},
		{"Step 1: check config", true},
		{"1. Install the package", true},
		{"The answer is 42", false},
		{"curl -s http://localhost", true},
		{"grep for the error", true},
		{"read the file", true},
	}
	for _, tt := range tests {
		got := isActionContent(tt.content)
		if got != tt.want {
			t.Errorf("isActionContent(%q) = %v, want %v", tt.content, got, tt.want)
		}
	}
}

func TestHasPositiveOutcome(t *testing.T) {
	// Session with positive outcome at the end
	positiveEnd := append(sampleTurns, Turn{Content: "ok, that works great.", Role: "user"})
	lastIdx := len(positiveEnd) - 2
	got := hasPositiveOutcome(positiveEnd, lastIdx)
	if !got {
		t.Error("expected positive outcome")
	}

	// Session with negative outcome
	negativeEnd := []Turn{
		{Content: "run fix", Role: "assistant"},
		{Content: "still failing", Role: "user"},
	}
	got = hasPositiveOutcome(negativeEnd, 0)
	if got {
		t.Error("expected no positive outcome for failure")
	}

	// No turn after last action — no outcome
	noNext := []Turn{
		{Content: "run fix", Role: "assistant"},
	}
	got = hasPositiveOutcome(noNext, 0)
	if got {
		t.Error("expected no positive outcome when no next turn exists")
	}
}

func TestGroupConsecutive(t *testing.T) {
	// Consecutive indices
	seqs := groupConsecutive([]int{0, 1, 2})
	if len(seqs) != 1 {
		t.Fatalf("expected 1 sequence, got %d", len(seqs))
	}
	if len(seqs[0]) != 3 {
		t.Errorf("expected 3 indices, got %d", len(seqs[0]))
	}

	// Gapped indices → two sequences
	seqs = groupConsecutive([]int{0, 1, 5, 6, 7})
	if len(seqs) != 2 {
		t.Fatalf("expected 2 sequences, got %d", len(seqs))
	}

	// Empty
	seqs = groupConsecutive([]int{})
	if seqs != nil {
		t.Errorf("expected nil for empty input, got %v", seqs)
	}
}

func BenchmarkExtract(b *testing.B) {
	// Build a 500-turn session
	bigSession := make([]Turn, 0, 500)
	for i := 0; i < 50; i++ {
		bigSession = append(bigSession,
			Turn{Content: "The service is down", Role: "user"},
			Turn{Content: "Let me check.\n\n```bash\nsystemctl status nginx\n```", Role: "assistant"},
			Turn{Content: "nginx is not running", Role: "system"},
			Turn{Content: "Let me start it.\n\n```bash\nsystemctl start nginx\n```", Role: "assistant"},
			Turn{Content: "nginx started successfully", Role: "system"},
			Turn{Content: "Check it's serving.\n\n```bash\ncurl -sI localhost | head -1\n```", Role: "assistant"},
			Turn{Content: "HTTP/1.1 200 OK", Role: "system"},
			Turn{Content: "ok, fixed.", Role: "user"},
		)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Extract(bigSession, DefaultMinSteps)
	}
}
