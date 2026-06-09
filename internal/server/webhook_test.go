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
		name       string
		header     string
		value      string
		wantProv   string
		wantEvent  string
	}{
		{"github push", "X-GitHub-Event", "push", "github", "push"},
		{"github ping", "X-GitHub-Event", "ping", "github", "ping"},
		{"gitlab push hook", "X-GitLab-Event", "Push Hook", "gitlab", "Push Hook"},
		{"forgejo push", "X-Forgejo-Event", "push", "gitea", "push"},
		{"forgejo takes priority over github", "X-Forgejo-Event", "push", "gitea", "push"},
		{"no header", "", "", "", ""},
		{"unknown header", "X-Foobar-Event", "push", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhook/git", nil)
			if tt.header != "" {
				r.Header.Set(tt.header, tt.value)
			}
			// Forgejo priority test: set both headers
			if tt.name == "forgejo takes priority over github" {
				r.Header.Set("X-GitHub-Event", "push")
			}
			prov, eventType := detectProvider(r)
			if prov != tt.wantProv {
				t.Errorf("detectProvider provider = %q, want %q", prov, tt.wantProv)
			}
			if eventType != tt.wantEvent {
				t.Errorf("detectProvider eventType = %q, want %q", eventType, tt.wantEvent)
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
		if !strings.Contains(event.Files[i].RawURL, e.suffix) {
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
	if !strings.Contains(event.Files[0].RawURL, "/-/raw/") {
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

	// Gitea raw URLs use SHA (not branch name) to avoid race conditions
	if !strings.Contains(event.Files[0].RawURL, "/raw/def456/") {
		t.Errorf("gitea rawURL should contain /raw/def456/, got %q", event.Files[0].RawURL)
	}
}

func TestCollectChangedFiles_Deduplicates(t *testing.T) {
	type commitShape struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	}

	commits := []commitShape{
		{Added: []string{"a.md"}, Modified: []string{"b.md"}},
		{Added: []string{"a.md"}, Modified: []string{"c.md"}}, // a.md appears in both commits
	}

	files := collectChangedFiles(commits)
	if len(files) != 3 {
		t.Errorf("expected 3 unique files, got %d: %v", len(files), files)
	}
}

func TestCollectChangedFiles_Empty(t *testing.T) {
	type commitShape struct {
		Added    []string `json:"added"`
		Modified []string `json:"modified"`
		Removed  []string `json:"removed"`
	}
	files := collectChangedFiles([]commitShape{})
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
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
	vault := s.resolveVault("https://github.com/chezgoulet/ragamuffin")
	if vault != "ragamuffin" {
		t.Errorf("suffix match: got %q, want %q", vault, "ragamuffin")
	}
}

func TestResolveVault_NoPartialSuffixMatch(t *testing.T) {
	// Short pattern must not match a longer repo path.
	// "muffin" is not a valid repo suffix so "ragamuffin" should NOT match.
	s := &Server{cfg: &config.Config{WebhookVaultMap: map[string]string{
		"muffin": "bad-vault",
	}}}
	vault := s.resolveVault("https://github.com/chezgoulet/ragamuffin")
	if vault != "" {
		t.Errorf("expected empty (no boundary before pattern), got %q", vault)
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

func TestURLPathEscape(t *testing.T) {
	// Verify that file paths with special chars are URL-encoded
	// (the RawURL is constructed with url.PathEscape)
	payload := `{
		"ref": "refs/heads/main",
		"before": "a",
		"after": "b",
		"commits": [{"added": ["path with spaces.md"], "modified": [], "removed": []}],
		"repository": {"full_name": "test/repo", "clone_url": "https://github.com/test/repo.git"}
	}`

	event, err := parseGitHubPush([]byte(payload))
	if err != nil {
		t.Fatalf("parseGitHubPush failed: %v", err)
	}
	if len(event.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(event.Files))
	}
	if !strings.Contains(event.Files[0].RawURL, "path%20with%20spaces.md") {
		t.Errorf("rawURL should URL-encode spaces, got %q", event.Files[0].RawURL)
	}
}
