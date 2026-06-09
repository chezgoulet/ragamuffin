package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Webhook handler ────────────────────────────────────────────────────────────

func (s *Server) handleWebhookGit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "METHOD_NOT_ALLOWED", "only POST is accepted")
		return
	}

	// Identify provider from request headers (only accept push events)
	provider, eventType := detectProvider(r)
	if provider == "" {
		writeError(w, 400, "UNKNOWN_PROVIDER",
			"set X-GitHub-Event, X-GitLab-Event, or X-Forgejo-Event header")
		return
	}
	if eventType != "push" && eventType != "Push Hook" {
		s.logger.Debug("webhook: ignoring non-push event", "provider", provider, "event", eventType)
		writeJSON(w, 200, map[string]any{
			"status": "ignored",
			"reason": fmt.Sprintf("non-push event: %s", eventType),
		})
		return
	}

	// Read body — http.MaxBytesReader rejects oversized bodies early
	// by closing the connection; the handler must not read more if the
	// limit was exceeded.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20)) // 5 MB max
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", "failed to read request body")
		return
	}

	// Parse push event
	event, err := parsePushEvent(provider, body)
	if err != nil {
		s.logger.Warn("webhook: invalid push event", "provider", provider, "error", err)
		writeError(w, 400, "INVALID_PAYLOAD", fmt.Sprintf("invalid push event: %s", err))
		return
	}

	if len(event.Files) == 0 {
		writeJSON(w, 200, map[string]any{
			"status":   "ok",
			"ingested": 0,
			"errors":   0,
			"reason":   "no changed files",
		})
		return
	}

	// Resolve vault from repo mapping
	vaultName := s.resolveVault(event.RepoURL)
	if vaultName == "" {
		s.logger.Warn("webhook: no vault mapping for repo",
			"repo", event.RepoURL, "provider", provider)
		writeError(w, 400, "NO_VAULT_MAPPING",
			fmt.Sprintf("no vault mapping for repo %q — set RAGAMUFFIN_WEBHOOK_VAULT_MAP", event.RepoURL))
		return
	}

	// Get or provision the indexer
	idx := s.indexers.Get(vaultName)
	if idx == nil {
		if !s.cfg.AutoProvisionVaults {
			writeError(w, 400, "VAULT_NOT_FOUND",
				fmt.Sprintf("vault %q not found and auto-provisioning is disabled", vaultName))
			return
		}
		idx = s.provisionVault(r.Context(), vaultName)
		if idx == nil {
			writeError(w, 400, "VAULT_PROVISION_FAILED",
				fmt.Sprintf("vault %q could not be provisioned", vaultName))
			return
		}
	}

	// Ingest each file
	ingested := 0
	errCount := 0

	for _, f := range event.Files {
		content, err := downloadFile(r.Context(), f.RawURL, 30*time.Second)
		if err != nil {
			s.logger.Warn("webhook: download failed", "file", f.Path, "error", err)
			errCount++
			continue
		}

		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		// Ingest signature: Ingest(ctx, content, source string, tags []string, meta map[string]string)
		if err := idx.Ingest(ctx, content, f.Path, nil, map[string]string{
			"source": "webhook",
			"repo":   event.RepoURL,
			"sha":    event.AfterSHA,
		}); err != nil {
			cancel()
			s.logger.Warn("webhook: ingest failed", "file", f.Path, "error", err)
			errCount++
			continue
		}
		cancel()
		ingested++
	}

	s.logger.Info("webhook: ingestion complete",
		"provider", provider, "repo", event.RepoURL,
		"ingested", ingested, "errors", errCount)

	writeJSON(w, 202, map[string]any{
		"status":   "accepted",
		"ingested": ingested,
		"errors":   errCount,
	})
}

// ── Provider detection ─────────────────────────────────────────────────────────

// detectProvider reads provider-specific webhook headers and returns
// (provider, eventType). Forgejo detection takes priority over GitHub
// since Forgejo sets both X-Forgejo-Event and X-GitHub-Event headers.
func detectProvider(r *http.Request) (string, string) {
	if v := r.Header.Get("X-Forgejo-Event"); v != "" {
		return "gitea", v // Forgejo shares the same payload format
	}
	if v := r.Header.Get("X-GitHub-Event"); v != "" {
		return "github", v
	}
	if v := r.Header.Get("X-GitLab-Event"); v != "" {
		return "gitlab", v
	}
	return "", ""
}

// ── Parsed event types ─────────────────────────────────────────────────────────

// gitCommit is a named type for commit changes across all providers.
// Using a named type ensures Go struct identity works across parsers
// and the file-collection helper (field order matches JSON tag order).
type gitCommit struct {
	Added    []string `json:"added"`
	Removed  []string `json:"removed"`
	Modified []string `json:"modified"`
}

type webhookFile struct {
	Path   string // file path within repo
	RawURL string // URL to download raw content
}

type pushEvent struct {
	Provider  string
	RepoURL   string
	Ref       string
	BeforeSHA string
	AfterSHA  string
	Files     []webhookFile
}

// ── GitHub push event ──────────────────────────────────────────────────────────

type githubPushPayload struct {
	Ref    string `json:"ref"`
	Before string `json:"before"`
	After  string `json:"after"`
	Commits []gitCommit `json:"commits"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
}

func parseGitHubPush(body []byte) (*pushEvent, error) {
	var p githubPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse github push: %w", err)
	}

	files := collectChangedFiles(p.Commits)
	rawURLs := make([]webhookFile, 0, len(files))
	for _, f := range files {
		rawURLs = append(rawURLs, webhookFile{
			Path:   f,
			RawURL: fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
				p.Repository.FullName, p.After, url.PathEscape(f)),
		})
	}

	return &pushEvent{
		Provider:  "github",
		RepoURL:   strings.TrimSuffix(p.Repository.CloneURL, ".git"),
		Ref:       p.Ref,
		BeforeSHA: p.Before,
		AfterSHA:  p.After,
		Files:     rawURLs,
	}, nil
}

// ── GitLab push event ──────────────────────────────────────────────────────────

type gitlabPushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Commits []gitCommit `json:"commits"`
	Project struct {
		HTTPURL           string `json:"git_http_url"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
}

func parseGitLabPush(body []byte) (*pushEvent, error) {
	var p gitlabPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse gitlab push: %w", err)
	}

	files := collectChangedFiles(p.Commits)
	repoBase := strings.TrimSuffix(p.Project.HTTPURL, ".git")
	rawURLs := make([]webhookFile, 0, len(files))
	for _, f := range files {
		rawURLs = append(rawURLs, webhookFile{
			Path:   f,
			RawURL: fmt.Sprintf("%s/-/raw/%s/%s", repoBase, p.After, url.PathEscape(f)),
		})
	}

	return &pushEvent{
		Provider:  "gitlab",
		RepoURL:   repoBase,
		Ref:       p.Ref,
		BeforeSHA: p.Before,
		AfterSHA:  p.After,
		Files:     rawURLs,
	}, nil
}

// ── Gitea / Forgejo push event ─────────────────────────────────────────────────

type giteaPushPayload struct {
	Ref     string `json:"ref"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Commits []gitCommit `json:"commits"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

func parseGiteaPush(body []byte) (*pushEvent, error) {
	var p giteaPushPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("parse gitea push: %w", err)
	}

	files := collectChangedFiles(p.Commits)
	// Gitea raw URL: {html_url}/raw/{sha}/{path}
	// Note: Uses SHA not branch name to avoid race condition (branch
	// could move between push and download).
	rawURLs := make([]webhookFile, 0, len(files))
	for _, f := range files {
		rawURLs = append(rawURLs, webhookFile{
			Path:   f,
			RawURL: fmt.Sprintf("%s/raw/%s/%s", p.Repository.HTMLURL, p.After, url.PathEscape(f)),
		})
	}

	return &pushEvent{
		Provider:  "gitea",
		RepoURL:   strings.TrimSuffix(p.Repository.CloneURL, ".git"),
		Ref:       p.Ref,
		BeforeSHA: p.Before,
		AfterSHA:  p.After,
		Files:     rawURLs,
	}, nil
}

// ── Routing ─────────────────────────────────────────────────────────────────────

func parsePushEvent(provider string, body []byte) (*pushEvent, error) {
	switch provider {
	case "github":
		return parseGitHubPush(body)
	case "gitlab":
		return parseGitLabPush(body)
	case "gitea":
		return parseGiteaPush(body)
	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// ── File collection ────────────────────────────────────────────────────────────

// collectChangedFiles extracts unique Added + Modified paths from commits.
func collectChangedFiles(commits []gitCommit) []string {
	seen := make(map[string]bool)
	var files []string
	for _, c := range commits {
		for _, f := range c.Added {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
		for _, f := range c.Modified {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

// ── Vault resolution ───────────────────────────────────────────────────────────

func (s *Server) resolveVault(repoURL string) string {
	// Check exact match first
	if v, ok := s.cfg.WebhookVaultMap[repoURL]; ok {
		return v
	}
	// Check suffix match: "github.com/chezgoulet/ragamuffin" matches
	// entries keyed by "chezgoulet/ragamuffin" (pattern must be a suffix
	// of repoURL preceded by '/' or ':' — avoids false positives like
	// "muffin" matching "ragamuffin").
	for pattern, vault := range s.cfg.WebhookVaultMap {
		if strings.HasSuffix(repoURL, pattern) {
			// Require that the pattern is either the entire URL or
			// preceded by a separator character ('/' or '.').
			if len(pattern) == len(repoURL) {
				return vault
			}
			sep := repoURL[len(repoURL)-len(pattern)-1]
			if sep == '/' || sep == '.' || sep == ':' {
				return vault
			}
		}
	}
	return ""
}

// ── File downloading ───────────────────────────────────────────────────────────

func downloadFile(ctx context.Context, url string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	// OpenClaw / Ragamuffin user-agent
	req.Header.Set("User-Agent", "Ragamuffin/0.9")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10 MB max
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	// Reject binary content by sniffing the first 512 bytes
	contentType := http.DetectContentType(body)
	if !isIngestableContentType(contentType) {
		return "", fmt.Errorf("skipped binary content: %s", contentType)
	}

	return string(body), nil
}

// isIngestableContentType returns true if the MIME type can be ingested
// as text. Binary files (images, archives, etc.) are skipped.
func isIngestableContentType(contentType string) bool {
	lower := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(lower, "text/"):
		return true
	case strings.HasPrefix(lower, "application/json"):
		return true
	case strings.HasPrefix(lower, "application/xml"):
		return true
	case strings.HasPrefix(lower, "application/x-yaml"):
		return true
	default:
		return false
	}
}
