package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chezgoulet/ragamuffin/internal/config"
)

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   string
	}{
		{"github", "X-GitHub-Event", "push", "github"},
		{"gitlab", "X-GitLab-Event", "Push Hook", "gitlab"},
		{"forgejo", "X-Forgejo-Event", "push", "gitea"},
		{"no header", "", "", ""},
		{"unknown header", "X-Foobar-Event", "push", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhook/git", nil)
			if tt.header != "" {
				r.Header.Set(tt.header, tt.value)
			}
			got := detectProvider(r)
			if got != tt.want {
				t.Errorf("detectProvider = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseGitHubPush(t *testing.T) {
	payload := `{
		"ref": "refs/heads/main",
		"before": "abc123",
		"after": "def456",
		"commits": [
			{
				"added": ["newfile.md"],
				"removed": [],
				"modified": ["README.md", "src/main.go"]
			}
		],
		"repository": {
			"full_name": "chezgoulet/ragamuffin",
			"clone_url": "https://github.com/chezgoulet/ragamuffin.git"
		}
	}`

	event, err := parseGitHubPush([]byte(payload))
	if err != nil {
		t.Fatalf("parseGitHubPush failed: %v", err)
	}

	if event.Provider != "github" {
		t.Errorf("provider = %q, want %q", event.Provider, "github")
	}
	if event.Ref != "refs/heads/main" {
		t.Errorf("ref = %q, want %q", event.Ref, "refs/heads/main")
	}
	if event.RepoURL != "https://github.com/chezgoulet/ragamuffin" {
		t.Errorf("repoURL = %q, want %q", event.RepoURL, "https://github.com/chezgoulet/ragamuffin")
	}

	if len(event.Files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(event.Files))
	}

	expected := []struct {
		path   string
		suffix string
	}{
		{"newfile.md", "def456/newfile.md"},
		{"README.md", "def456/README.md"},
		{"src/main.go", "def456/src/main.go"},
	}
	for i, e := range expected {
		if event.Files[i].Path != e.path {
			t.Errorf("file %d path = %q, want %q", i, event.Files[i].Path, e.path)
		}
		if !contains(event.Files[i].RawURL, e.suffix) {
			t.Errorf("file %d rawURL = %q, want suffix %q", i, event.Files[i].RawURL, e.suffix)
		}
	}
}

func TestParseGitHubPush_NoCommits(t *testing.T) {
	payload := `{
		"ref": "refs/heads/main",
		"before": "abc",
		"after": "def",
		"commits": [],
		"repository": {
			"full_name": "chezgoulet/ragamuffin",
			"clone_url": "https://github.com/chezgoulet/ragamuffin.git"
		}
	}`

	event, err := parseGitHubPush([]byte(payload))
	if err != nil {
		t.Fatalf("parseGitHubPush failed: %v", err)
	}
	if len(event.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(event.Files))
	}
}

func TestParseGitLabPush(t *testing.T) {
	payload := `{
		"ref": "refs/heads/main",
		"before": "abc123",
		"after": "def456",
		"commits": [
			{
				"added": ["docs/guide.md"],
				"modified": ["README.md"],
				"removed": []
			}
		],
		"project": {
			"git_http_url": "https://gitlab.com/chezgoulet/ragamuffin.git",
			"path_with_namespace": "chezgoulet/ragamuffin"
		}
	}`

	event, err := parseGitLabPush([]byte(payload))
	if err != nil {
		t.Fatalf("parseGitLabPush failed: %v", err)
	}

	if event.Provider != "gitlab" {
		t.Errorf("provider = %q, want %q", event.Provider, "gitlab")
	}
	if event.RepoURL != "https://gitlab.com/chezgoulet/ragamuffin" {
		t.Errorf("repoURL = %q, want %q", event.RepoURL, "https://gitlab.com/chezgoulet/ragamuffin")
	}
	if len(event.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(event.Files))
	}

	if event.Files[0].Path != "docs/guide.md" {
		t.Errorf("file 0 path = %q, want %q", event.Files[0].Path, "docs/guide.md")
	}
	if !contains(event.Files[0].RawURL, "/-/raw/") {
		t.Errorf("rawURL should contain /-/raw/, got %q", event.Files[0].RawURL)
	}
}

func TestParseGiteaPush(t *testing.T) {
	payload := `{
		"ref": "refs/heads/main",
		"before": "abc123",
		"after": "def456",
		"commits": [
			{
				"added": ["config.yml"],
				"modified": ["src/app.go"],
				"removed": []
			}
		],
		"repository": {
			"full_name": "chezgoulet/ragamuffin",
			"clone_url": "https://git.chezgoulet.dev/chezgoulet/ragamuffin.git",
			"html_url": "https://git.chezgoulet.dev/chezgoulet/ragamuffin"
		}
	}`

	event, err := parseGiteaPush([]byte(payload))
	if err != nil {
		t.Fatalf("parseGiteaPush failed: %v", err)
	}

	if event.Provider != "gitea" {
		t.Errorf("provider = %q, want %q", event.Provider, "gitea")
	}
	if len(event.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(event.Files))
	}

	// Gitea raw URLs use branch from ref, not SHA
	if !contains(event.Files[0].RawURL, "/raw/main/") {
		t.Errorf("gitea rawURL should contain /raw/main/, got %q", event.Files[0].RawURL)
	}
}

func TestCollectFiles_Deduplicates(t *testing.T) {
	type commitShape struct {
		Added    []string
		Modified []string
		Removed  []string
	}

	commits := []commitShape{
		{Added: []string{"a.md"}, Modified: []string{"b.md"}},
		{Added: []string{"a.md"}, Modified: []string{"c.md"}}, // a.md appears in both commits
	}

	files := collectFiles(commits)
	if len(files) != 3 {
		t.Errorf("expected 3 unique files, got %d: %v", len(files), files)
	}
}

func TestResolveVault_ExactMatch(t *testing.T) {
	s := &Server{cfg: &config.Config{WebhookVaultMap: map[string]string{
		"https://github.com/chezgoulet/ragamuffin": "ragamuffin",
		"https://github.com/chezgoulet/library":    "library",
	}}}

	vault := s.resolveVault("https://github.com/chezgoulet/ragamuffin")
	if vault != "ragamuffin" {
		t.Errorf("exact match: got %q, want %q", vault, "ragamuffin")
	}
}

func TestResolveVault_SuffixMatch(t *testing.T) {
	s := &Server{cfg: &config.Config{WebhookVaultMap: map[string]string{
		"chezgoulet/ragamuffin": "ragamuffin",
		"chezgoulet/library":    "library",
	}}}
	// repoURL ends with the key suffix
	vault := s.resolveVault("https://github.com/chezgoulet/ragamuffin")
	if vault != "ragamuffin" {
		t.Errorf("suffix match: got %q, want %q", vault, "ragamuffin")
	}
}

func TestResolveVault_NoMatch(t *testing.T) {
	s := &Server{cfg: &config.Config{WebhookVaultMap: map[string]string{
		"chezgoulet/library": "library",
	}}}
	vault := s.resolveVault("https://github.com/chezgoulet/other")
	if vault != "" {
		t.Errorf("expected empty, got %q", vault)
	}
}

func TestParsePushEvent_Routing(t *testing.T) {
	githubBody := json.RawMessage(`{"ref":"refs/heads/main","before":"a","after":"b","commits":[],"repository":{"full_name":"test/repo","clone_url":"https://github.com/test/repo.git"}}`)
	gitlabBody := json.RawMessage(`{"ref":"refs/heads/main","before":"a","after":"b","commits":[],"project":{"git_http_url":"https://gitlab.com/test/repo.git","path_with_namespace":"test/repo"}}`)
	giteaBody := json.RawMessage(`{"ref":"refs/heads/main","before":"a","after":"b","commits":[],"repository":{"full_name":"test/repo","clone_url":"https://git.example.com/test/repo.git","html_url":"https://git.example.com/test/repo"}}`)

	tests := []struct {
		name     string
		provider string
		body     []byte
		wantRepo string
	}{
		{"github", "github", githubBody, "https://github.com/test/repo"},
		{"gitlab", "gitlab", gitlabBody, "https://gitlab.com/test/repo"},
		{"gitea", "gitea", giteaBody, "https://git.example.com/test/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := parsePushEvent(tt.provider, tt.body)
			if err != nil {
				t.Fatalf("parsePushEvent failed: %v", err)
			}
			if event.RepoURL != tt.wantRepo {
				t.Errorf("RepoURL = %q, want %q", event.RepoURL, tt.wantRepo)
			}
			if event.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", event.Provider, tt.provider)
			}
		})
	}
}

func TestParsePushEvent_UnknownProvider(t *testing.T) {
	_, err := parsePushEvent("unknown", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// ── Helpers ────────────────────────────────────────────────────────────────────

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
