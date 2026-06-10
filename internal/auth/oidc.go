package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/golang-jwt/jwt/v5"
)

// OIDC discovery TTL — re-discover after 1 hour in case the JWKS URI changes.
const oidcDiscoveryTTL = 1 * time.Hour

// OIDCAuthenticator validates JWT bearer tokens against an OIDC provider.
// It uses OIDC discovery (.well-known/openid-configuration) to locate the
// JWKS endpoint, eliminating the need for separate JWKS URL config.
type OIDCAuthenticator struct {
	issuer   string
	clientID string
	logger   *slog.Logger

	mu sync.Mutex
	// Resolved via discovery
	jwksURL      string
	discoveredAt time.Time

	// Shared JWT validation infrastructure
	jwtAuth *JWTAuthenticator
}

// NewOIDCAuthenticator creates an OIDC authenticator. The issuer URL must
// be the full OIDC issuer (e.g., https://authentik.house.internal/application/o/ragamuffin/).
// discovery is performed lazily on the first Authenticate call.
func NewOIDCAuthenticator(issuer, clientID string, logger *slog.Logger) *OIDCAuthenticator {
	return &OIDCAuthenticator{
		issuer:   strings.TrimRight(issuer, "/"),
		clientID: clientID,
		logger:   logger,
	}
}

// StartDiscovery attempts eager OIDC discovery at startup. Non-fatal — logs
// failure and falls back to lazy discovery on first Authenticate call (#410).
func (o *OIDCAuthenticator) StartDiscovery(ctx context.Context) {
	if err := o.ensureDiscovered(ctx); err != nil {
		o.logger.Warn("oidc: eager discovery failed, will retry on first request", "error", err)
	}
}

// ensureDiscovered performs OIDC discovery if not cached or TTL expired,
// resolving the JWKS URL from the provider's .well-known/openid-configuration.
// Thread-safe. Retries with backoff on transient failures (#410).
func (o *OIDCAuthenticator) ensureDiscovered(ctx context.Context) error {
	o.mu.Lock()
	if o.jwksURL != "" && time.Since(o.discoveredAt) < oidcDiscoveryTTL {
		o.mu.Unlock()
		return nil
	}
	cachedURL := o.jwksURL
	o.mu.Unlock()

	discoveryURL := o.issuer + "/.well-known/openid-configuration"

	// Retry with backoff: 3 attempts, 1s/2s/4s delay
	var lastErr error
	delays := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			o.logger.Warn("oidc: retrying discovery", "attempt", attempt, "error", lastErr)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delays[attempt-1]):
			}
		}

		o.logger.Info("oidc: discovering provider config", "url", discoveryURL, "attempt", attempt+1)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
		if err != nil {
			lastErr = fmt.Errorf("oidc discovery request: %w", err)
			continue
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("oidc discovery fetch: %w", err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("oidc discovery returned %d", resp.StatusCode)
			continue
		}

		var discovery struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
			resp.Body.Close()
			lastErr = fmt.Errorf("oidc discovery decode: %w", err)
			continue
		}
		resp.Body.Close()

		if discovery.JWKSURI == "" {
			lastErr = fmt.Errorf("oidc discovery: no jwks_uri in provider config")
			continue
		}

		o.mu.Lock()
		o.jwksURL = discovery.JWKSURI
		o.discoveredAt = time.Now()
		o.jwtAuth = NewJWTAuthenticator(o.issuer, o.clientID, o.jwksURL, o.logger)
		o.mu.Unlock()

		o.logger.Info("oidc: discovery complete", "jwks_url", o.jwksURL)
		return nil
	}

	// All attempts failed. If we had a cached result and it's just TTL expiry,
	// keep using the stale cache rather than rejecting requests.
	o.mu.Lock()
	if cachedURL != "" && o.jwksURL == cachedURL {
		o.logger.Warn("oidc: discovery failed, keeping stale cached JWKS URL", "error", lastErr)
		o.discoveredAt = time.Now() // reset timer so we don't retry immediately
		o.mu.Unlock()
		return nil // return nil so existing cached data is used
	}
	o.mu.Unlock()

	return fmt.Errorf("oidc discovery failed after %d attempts: %w", len(delays)+1, lastErr)
}

// Authenticate validates a JWT bearer token against the OIDC provider's JWKS.
// Performs lazy OIDC discovery on first call.
func (o *OIDCAuthenticator) Authenticate(r *http.Request) (*Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, ErrUnauthenticated
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenStr == authHeader || tokenStr == "" {
		return nil, ErrUnauthenticated
	}

	// Parse token header to extract kid (without verification)
	token, _, err := new(jwt.Parser).ParseUnverified(tokenStr, &ragaClaimsOIDC{})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("missing kid in token header")
	}

	// Ensure OIDC discovery has resolved the JWKS URL
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := o.ensureDiscovered(ctx); err != nil {
		return nil, fmt.Errorf("oidc: %w", err)
	}

	// Verify and parse the token using the embedded JWT authenticator
	parsed, err := jwt.ParseWithClaims(tokenStr, &ragaClaimsOIDC{}, func(t *jwt.Token) (any, error) {
		// Fetch keys from the same JWKS (reuse the cached keys)
		keys, err := o.jwtAuth.GetJWKS()
		if err != nil {
			return nil, fmt.Errorf("jwks: %w", err)
		}
		ck, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("key %q not found in JWKS", kid)
		}
		return ck.publicKey, nil
	},
		jwt.WithIssuer(o.issuer),
		jwt.WithAudience(o.clientID),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt validation: %w", err)
	}

	claims, ok := parsed.Claims.(*ragaClaimsOIDC)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	// Extract ragamuffin access from custom claim or standard claim paths
	access := make([]string, 0)
	if claims.Ragamuffin != nil && claims.Ragamuffin.Access != "" {
		access = strings.Split(claims.Ragamuffin.Access, "_")
	}

	// Determine agent identity: preferred_username from ID token, or sub
	identity := claims.Subject
	if claims.PreferredUsername != "" {
		identity = claims.PreferredUsername
	}

	var vaults []string
	if claims.Ragamuffin != nil {
		vaults = claims.Ragamuffin.Vaults
	}

	o.logger.Debug("oidc: authenticated", "identity", identity, "access", access, "vaults", vaults)

	return &Claims{
		Access: access,
		Vaults: vaults,
	}, nil
}

// ragaClaimsOIDC extends the standard JWT claims with OIDC and custom ragamuffin fields.
type ragaClaimsOIDC struct {
	jwt.RegisteredClaims
	Ragamuffin        *ragaAccess `json:"ragamuffin,omitempty"`
	PreferredUsername string      `json:"preferred_username,omitempty"`
}
