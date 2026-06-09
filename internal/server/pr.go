// pr.go — createPR helper for the /draft endpoint.
//
// The draft handler accepts ?mode=pr to create a PR instead of writing a local
// file. createPR delegates to the configured GitProvider, using the first repo
// from the GitRepos config list and the configured GitBaseBranch.

package server

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// createPR opens a pull request via the configured git provider.
func (s *Server) createPR(title, content, path, description string) (prURL, branch string, err error) {
	if !s.cfg.HasGit() {
		return "", "", fmt.Errorf("git provider not configured")
	}

	repo := s.cfg.GitRepos
	if idx := strings.IndexByte(repo, ','); idx != -1 {
		repo = repo[:idx] // first repo in list
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", "", fmt.Errorf("no git repos configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return s.gitProvider.CreatePR(ctx, repo, s.cfg.GitBaseBranch, title, content, path, description)
}
