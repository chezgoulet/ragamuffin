package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// APIKeyAuthenticator validates static API keys from Authorization header.
// Supports per-vault scoped keys in multi-tenant mode.
type APIKeyAuthenticator struct {
	readKey  string
	writeKey string
	// Per-vault scoped keys: vault name -> {read, write}
	vaultKeys  map[string]struct{ readKey, writeKey string }
	writePaths map[string]bool // paths requiring write access
}

// NewAPIKeyAuthenticator creates an API key authenticator.
// readKey and writeKey are the global unscoped keys.
// Per-vault scoped keys are loaded from environment variables.
func NewAPIKeyAuthenticator(readKey, writeKey string, vaultNames []string, isMultiTenant bool) *APIKeyAuthenticator {
	a := &APIKeyAuthenticator{
		readKey:    readKey,
		writeKey:   writeKey,
		vaultKeys:  make(map[string]struct{ readKey, writeKey string }),
		writePaths: defaultWritePaths(),
	}

	// Load per-vault scoped keys from environment
	for _, name := range vaultNames {
		prefix := fmt.Sprintf("RAGAMUFFIN_AUTH_READ_KEY_%s", strings.ToUpper(name))
		if rk := os.Getenv(prefix); rk != "" {
			entry := a.vaultKeys[name]
			entry.readKey = rk
			a.vaultKeys[name] = entry
		}

		prefix = fmt.Sprintf("RAGAMUFFIN_AUTH_WRITE_KEY_%s", strings.ToUpper(name))
		if wk := os.Getenv(prefix); wk != "" {
			entry := a.vaultKeys[name]
			entry.writeKey = wk
			a.vaultKeys[name] = entry
		}
	}

	return a
}

// defaultWritePaths returns the set of paths requiring write access.
func defaultWritePaths() map[string]bool {
	return map[string]bool{
		"/draft": true,
	}
}

// Authenticate extracts claims from the Authorization header.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (*Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, ErrUnauthenticated
	}

	// Extract Bearer token
	token := strings.TrimPrefix(authHeader, "Bearer ")
	// Reject when the prefix was absent (token == authHeader) or the token
	// is empty ("Bearer " with nothing after it). An empty token must never
	// reach the key comparison — subtle.ConstantTimeCompare("", "") == 1,
	// so an empty token would otherwise match an unset key.
	if token == authHeader || token == "" {
		return nil, ErrUnauthenticated
	}

	// Check global keys first — constant-time comparison to prevent timing attacks
	if constantTimeEqual(token, a.writeKey) {
		return &Claims{Access: []string{"read", "write"}}, nil
	}
	if constantTimeEqual(token, a.readKey) {
		return &Claims{Access: []string{"read"}}, nil
	}

	// Check per-vault scoped keys — extract vault name from path
	vault := vaultNameFromPath(r.URL.Path)
	if vault != "" {
		if keys, ok := a.vaultKeys[vault]; ok {
			if constantTimeEqual(token, keys.writeKey) {
				return &Claims{Access: []string{"read", "write"}, Vaults: []string{vault}}, nil
			}
			if constantTimeEqual(token, keys.readKey) {
				return &Claims{Access: []string{"read"}, Vaults: []string{vault}}, nil
			}
		}
	}

	// No matching key found
	return nil, ErrUnauthenticated
}

// constantTimeEqual compares two strings in constant time to prevent timing attacks.
// Returns false when lengths differ (short-circuit is safe — lengths are public).
func constantTimeEqual(a, b string) bool {
	// An unset (empty) configured key must never authenticate, regardless of
	// the presented token. Guard here as defense-in-depth so every call site
	// is covered, not just Authenticate.
	if b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// vaultNameFromPath extracts vault name from a /vault/{name}/... path.
func vaultNameFromPath(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "vault" {
		return parts[1]
	}
	return ""
}
