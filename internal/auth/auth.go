package auth

import (
	"fmt"
	"net/http"
	"strings"
)

// Mode represents the authentication mode.
type Mode string

const (
	ModeNone   Mode = "none"
	ModeAPIKey Mode = "api_key"
	ModeJWT    Mode = "jwt"
	ModeOIDC   Mode = "oidc"
)

// ValidModes returns the set of valid auth modes.
func ValidModes() []Mode {
	return []Mode{ModeNone, ModeAPIKey, ModeJWT, ModeOIDC}
}

// ParseMode parses a string into a Mode, returning an error if invalid.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(s)) {
	case ModeNone:
		return ModeNone, nil
	case ModeAPIKey:
		return ModeAPIKey, nil
	case ModeJWT:
		return ModeJWT, nil
	case ModeOIDC:
		return ModeOIDC, nil
	default:
		return "", fmt.Errorf("invalid auth mode %q: must be one of %v", s, ValidModes())
	}
}

// Claims represents the result of authentication.
type Claims struct {
	// Access is a list of granted access levels (e.g. "read", "write").
	Access []string
	// Vaults is the list of vaults the caller can access.
	// Empty or nil means all vaults.
	Vaults []string
}

// HasAccess checks if the claims grant a specific access level.
func (c *Claims) HasAccess(level string) bool {
	for _, a := range c.Access {
		if a == level {
			return true
		}
	}
	return false
}

// HasVaultAccess checks if the claims include access to a specific vault.
func (c *Claims) HasVaultAccess(vault string) bool {
	if len(c.Vaults) == 0 {
		return true // no restriction = access to all vaults
	}
	for _, v := range c.Vaults {
		if v == vault {
			return true
		}
	}
	return false
}

// Authenticator is the interface for authentication implementations.
type Authenticator interface {
	// Authenticate extracts claims from an HTTP request.
	// Returns ErrUnauthenticated if the request lacks valid credentials.
	// Returns (*Claims, nil) on success.
	Authenticate(r *http.Request) (*Claims, error)
}

// ErrUnauthenticated is returned when authentication fails.
var ErrUnauthenticated = fmt.Errorf("unauthenticated")

// PublicPaths are routes that never require auth.
var PublicPaths = map[string]bool{
	"/health":  true,
	"/version": true,
}

// Middleware wraps an HTTP handler with authentication.
// Routes in PublicPaths bypass auth. All others require valid claims.
// Supports `?token=...` query param as an alternative to Authorization header (#424).
func Middleware(auth Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if PublicPaths[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			// Support token in query param (e.g., SSE EventSource connections)
			if r.Header.Get("Authorization") == "" {
				if token := r.URL.Query().Get("token"); token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
			}

			claims, err := auth.Authenticate(r)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="ragamuffin"`)
				http.Error(w, `{"error": true, "code": "UNAUTHORIZED", "message": "authentication required"}`, http.StatusUnauthorized)
				return
			}

			// Store claims in request context for downstream handlers
			ctx := contextWithClaims(r.Context(), claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NoneAuthenticator always permits access with full claims.
type NoneAuthenticator struct{}

func (a *NoneAuthenticator) Authenticate(r *http.Request) (*Claims, error) {
	if r == nil {
		return nil, fmt.Errorf("nil *http.Request passed to NoneAuthenticator")
	}
	return &Claims{Access: []string{"read", "write"}}, nil
}
