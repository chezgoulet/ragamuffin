// Package server provides HTTP handlers and middleware for the Ragamuffin API.
//
// This file consolidates response helper functions for consistent error and
// JSON response formatting.
package server

import (
	"encoding/json"
	"net/http"
)

// errResp is the standard error response format for API endpoints.
type errResp struct {
	Error   bool   `json:"error"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError sends a structured JSON error response with the given status
// code, error code identifier, and human-readable message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errResp{Error: true, Code: code, Message: message})
}

// writeJSON sends a JSON-encoded response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
