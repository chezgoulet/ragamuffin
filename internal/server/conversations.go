// handleConversationsByID routes to the appropriate sub-handler based on the URL path.
// Patterns: /v1/conversations/{id}/turns
func (s *Server) handleConversationsByID(w http.ResponseWriter, r *http.Request) {
	// Parse path: /v1/conversations/{id}/turns or /v1/conversations/{id}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/v1/conversations/"), "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, 404, "NOT_FOUND", "conversation session ID required")
		return
	}
	sessionID := parts[0]

	if len(parts) == 2 && parts[1] == "turns" {
		s.handleConversationTurnAppend(w, r, sessionID)
		return
	}

	writeError(w, 404, "NOT_FOUND", fmt.Sprintf("unknown path: %s", r.URL.Path))
}
