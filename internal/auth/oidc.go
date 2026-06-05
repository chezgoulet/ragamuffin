package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/golang-jwt/jwt/v5"
)

// OIDCAuthenticator validates JWT bearer tokens against an OIDC provider.
// It uses OIDC discovery (.well-known/openid-configuration) to locate the
// JWKS endpoint, eliminating the need for separate JWKS URL config.
type OIDCAuthenticator struct {
	issuer   string
	clientID string
	logger   *slog.Logger

	// Resolved via discovery
	jwksURL  string

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

// ensureDiscovered performs OIDC discovery if not already done, resolving
// the JWKS URL from the provider's .well-known/openid-configuration.
func (o *OIDCAuthenticator) ensureDiscovered(ctx context.Context) error {
	if o.jwksURL != "" {
		return nil
	}

	discoveryURL := o.issuer + "/.well-known/openid-configuration"
	o.logger.Info("oidc: discovering provider config", "url", discoveryURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return fmt.Errorf("oidc discovery request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("oidc discovery fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc discovery returned %d", resp.StatusCode)
	}

	var discovery struct {
		Issuer    string `json:"issuer"`
		JWKSURI  string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&discovery); err != nil {
		return fmt.Errorf("oidc discovery decode: %w", err)
	}

	if discovery.JWKSURI == "" {
		return fmt.Errorf("oidc discovery: no jwks_uri in provider config")
	}

	o.jwksURL = discovery.JWKSURI
	o.jwtAuth = NewJWTAuthenticator(o.issuer, o.clientID, o.jwksURL, o.logger)
	o.logger.Info("oidc: discovery complete", "jwks_url", o.jwksURL)
	return nil
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
