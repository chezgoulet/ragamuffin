package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── Request / Response types ───────────────────────────────────────────────────

type inboxCreateRequest struct {
	Content string   `json:"content"`
	Source  string   `json:"source"`
	Tags    []string `json:"tags"`
	Vault   string   `json:"vault,omitempty"`
}

type inboxEntry struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Content   string   `json:"content,omitempty"`
	Source    string   `json:"source,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	Processed bool     `json:"processed"`
}

const inboxDirName = "_inbox"

// ── Helpers ────────────────────────────────────────────────────────────────────

var nonSlugChars = regexp.MustCompile(`[^a-z0-9-]+`)
var multiDash = regexp.MustCompile(`-{2,}`)

// slugify converts a string into a URL-safe slug.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = nonSlugChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	s = multiDash.ReplaceAllString(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	return strings.TrimRight(s, "-")
}

// inboxDir returns the _inbox directory path for a vault.
func (s *Server) inboxDir(vaultPath string) (string, error) {
	dir := filepath.Join(vaultPath, inboxDirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create inbox dir: %w", err)
	}
	return dir, nil
}

// validInboxID checks that the ID contains only safe characters and no path traversal sequences.
// Inbox IDs are generated server-side as "20060102-150405-slug", so only alphanumeric,
// hyphens, and underscores are permitted.
var inboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

func validInboxID(id string) bool {
	return id != "" && len(id) <= 128 && inboxIDPattern.MatchString(id) && !strings.Contains(id, "..")
}

// parseInboxID validates the id and returns the filename (id + ".md")
// if valid, or empty string if the id is rejected for path traversal.
func parseInboxFile(id string) string {
	if !validInboxID(id) {
		return ""
	}
	// IDs are stored as "20060102-150405-slug.md"
	return id + ".md"
}

// readInboxEntry reads a markdown file and parses its frontmatter and content.
func readInboxEntry(path string) (*inboxEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	content := string(data)
	entry := &inboxEntry{
		ID:        strings.TrimSuffix(filepath.Base(path), ".md"),
		Content:   content,
		Processed: false,
	}

	// Parse frontmatter
	if strings.HasPrefix(content, "---\n") {
		parts := strings.SplitN(content[4:], "\n---\n", 2)
		if len(parts) == 2 {
			fm := parts[0]
			entry.Content = parts[1]
			for _, line := range strings.Split(fm, "\n") {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "title:"):
					entry.Title = strings.TrimSpace(strings.TrimPrefix(line, "title:"))
					entry.Title = strings.Trim(entry.Title, "\"")
				case strings.HasPrefix(line, "source:"):
					entry.Source = strings.TrimSpace(strings.TrimPrefix(line, "source:"))
					entry.Source = strings.Trim(entry.Source, "\"")
				case strings.HasPrefix(line, "tags:"):
					tagStr := strings.TrimSpace(strings.TrimPrefix(line, "tags:"))
					tagStr = strings.Trim(tagStr, "[]")
					for _, t := range strings.Split(tagStr, ",") {
						t = strings.TrimSpace(t)
						t = strings.Trim(t, "\"")
						if t != "" {
							entry.Tags = append(entry.Tags, t)
						}
					}
				case strings.HasPrefix(line, "created_at:"):
					entry.CreatedAt = strings.TrimSpace(strings.TrimPrefix(line, "created_at:"))
					entry.CreatedAt = strings.Trim(entry.CreatedAt, "\"")
				case strings.HasPrefix(line, "processed:"):
					v := strings.TrimSpace(strings.TrimPrefix(line, "processed:"))
					entry.Processed = v == "true" || v == "yes"
				}
			}
		}
	}

	return entry, nil
}

// ── Handlers ───────────────────────────────────────────────────────────────────

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	vaultPath := s.vaultPathFromContext(r.Context())
	if vaultPath == "" {
		writeError(w, 400, "VAULT_NOT_CONFIGURED", "no vault path available")
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleInboxCreate(w, r, vaultPath)
	case http.MethodGet:
		// Check if an ID is specified
		if id := r.PathValue("id"); id != "" {
			s.handleInboxRead(w, r, vaultPath, id)
		} else {
			s.handleInboxList(w, r, vaultPath)
		}
	case http.MethodDelete:
		id := r.PathValue("id")
		if id == "" {
			writeError(w, 400, "MISSING_ID", "inbox entry ID is required for DELETE")
			return
		}
		s.handleInboxDelete(w, r, vaultPath, id)
	default:
		writeError(w, 405, "INVALID_REQUEST", "method not allowed")
	}
}

func (s *Server) handleInboxCreate(w http.ResponseWriter, r *http.Request, vaultPath string) {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024) // 256 KB
	var req inboxCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, 400, "MISSING_CONTENT", "content is required")
		return
	}

	inboxPath, err := s.inboxDir(vaultPath)
	if err != nil {
		s.logger.Error("inbox directory creation failed", "error", err)
		writeError(w, 500, "INTERNAL_ERROR", "failed to create inbox directory")
		return
	}

	// Generate ID: timestamp + slug
	now := time.Now()
	slug := slugify(req.Content)
	if len(slug) == 0 {
		slug = "entry"
	}
	id := fmt.Sprintf("%s-%s", now.Format("20060102-150405"), slug)
	filename := id + ".md"
	filePath := filepath.Join(inboxPath, filename)

	// Build frontmatter
	title := slug
	if req.Source != "" {
		title = fmt.Sprintf("Note from %s", req.Source)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("title: %q\n", title))
	b.WriteString(fmt.Sprintf("created_at: %q\n", now.Format(time.RFC3339)))
	b.WriteString("processed: false\n")
	if req.Source != "" {
		b.WriteString(fmt.Sprintf("source: %q\n", req.Source))
	}
	if len(req.Tags) > 0 {
		tagStr := make([]string, len(req.Tags))
		for i, t := range req.Tags {
			tagStr[i] = fmt.Sprintf("%q", t)
		}
		b.WriteString(fmt.Sprintf("tags: [%s]\n", strings.Join(tagStr, ", ")))
	}
	b.WriteString("---\n")
	b.WriteString(req.Content)
	b.WriteString("\n")

	if err := os.WriteFile(filePath, []byte(b.String()), 0644); err != nil {
		s.logger.Error("inbox write failed", "error", err)
		writeError(w, 500, "WRITE_ERROR", "failed to write inbox entry")
		return
	}

	s.logger.Info("inbox entry created", "id", id, "vault", filepath.Base(vaultPath))
	writeJSON(w, 201, map[string]any{
		"id":     id,
		"status": "created",
	})
}

func (s *Server) handleInboxList(w http.ResponseWriter, r *http.Request, vaultPath string) {
	inboxPath, err := s.inboxDir(vaultPath)
	if err != nil {
		s.logger.Error("inbox directory access failed", "error", err)
		writeError(w, 500, "INTERNAL_ERROR", "failed to access inbox directory")
		return
	}

	entries, err := os.ReadDir(inboxPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, 200, map[string]any{"entries": []inboxEntry{}})
			return
		}
		s.logger.Error("inbox list failed", "error", err)
		writeError(w, 500, "READ_ERROR", "failed to list inbox entries")
		return
	}

	var result []inboxEntry
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		e, err := readInboxEntry(filepath.Join(inboxPath, entry.Name()))
		if err != nil || e == nil {
			continue
		}
		// Omit full content for list view
		e.Content = ""
		if e.CreatedAt == "" {
			e.CreatedAt = info.ModTime().Format(time.RFC3339)
		}
		result = append(result, *e)
	}

	// Sort by creation time, newest first
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt > result[j].CreatedAt
	})

	writeJSON(w, 200, map[string]any{"entries": result})
}

func (s *Server) handleInboxRead(w http.ResponseWriter, r *http.Request, vaultPath, id string) {
	filename := parseInboxFile(id)
	if filename == "" {
		writeError(w, 400, "INVALID_ID", "invalid inbox entry ID: contains path traversal or disallowed characters")
		return
	}

	inboxPath, err := s.inboxDir(vaultPath)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "failed to access inbox directory")
		return
	}

	filePath := filepath.Join(inboxPath, filename)
	entry, err := readInboxEntry(filePath)
	if err != nil {
		s.logger.Error("inbox read failed", "error", err, "id", id)
		writeError(w, 500, "READ_ERROR", "failed to read inbox entry")
		return
	}
	if entry == nil {
		writeError(w, 404, "NOT_FOUND", "inbox entry not found")
		return
	}

	writeJSON(w, 200, entry)
}

func (s *Server) handleInboxDelete(w http.ResponseWriter, r *http.Request, vaultPath, id string) {
	filename := parseInboxFile(id)
	if filename == "" {
		writeError(w, 400, "INVALID_ID", "invalid inbox entry ID: contains path traversal or disallowed characters")
		return
	}

	inboxPath, err := s.inboxDir(vaultPath)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "failed to access inbox directory")
		return
	}

	filePath := filepath.Join(inboxPath, filename)
	entry, err := readInboxEntry(filePath)
	if err != nil {
		s.logger.Error("inbox read for delete failed", "error", err, "id", id)
		writeError(w, 500, "READ_ERROR", "failed to read inbox entry")
		return
	}
	if entry == nil {
		writeError(w, 404, "NOT_FOUND", "inbox entry not found")
		return
	}

	// Frontmatter is at the top of the file
	data, err := os.ReadFile(filePath)
	if err != nil {
		writeError(w, 500, "READ_ERROR", "failed to read file")
		return
	}

	contentStr := string(data)
	// Replace processed: false → processed: true in frontmatter
	updated := strings.Replace(contentStr, "processed: false", "processed: true", 1)
	if updated == contentStr {
		// If already processed or frontmatter has no processed field, append it
		if strings.Contains(contentStr, "processed: true") {
			writeJSON(w, 200, map[string]any{
				"id":        id,
				"status":    "already_processed",
				"processed": true,
			})
			return
		}
		// No processed field — add it after created_at line
		updated = strings.Replace(contentStr, "created_at:", "processed: true\ncreated_at:", 1)
	}

	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		s.logger.Error("inbox delete (soft) failed", "error", err, "id", id)
		writeError(w, 500, "WRITE_ERROR", "failed to update inbox entry")
		return
	}

	s.logger.Info("inbox entry soft-deleted", "id", id)
	writeJSON(w, 200, map[string]any{
		"id":        id,
		"status":    "processed",
		"processed": true,
	})
}

// ── Input reading ──────────────────────────────────────────────────────────────

func readBody(r *http.Request, w http.ResponseWriter) ([]byte, bool) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 500, "READ_ERROR", "failed to read request body")
		return nil, false
	}
	return data, true
}
