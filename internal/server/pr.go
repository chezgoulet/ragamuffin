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
