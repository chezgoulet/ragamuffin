package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/golang-jwt/jwt/v5"
)

// JWTAuthenticator validates JWT bearer tokens using JWKS.
type JWTAuthenticator struct {
	issuer   string
	audience string
	jwksURL  string
	logger   *slog.Logger

	mu        sync.RWMutex
	jwks      map[string]cachedKey
	fetchedAt time.Time
	cacheTTL  time.Duration
}

type cachedKey struct {
	publicKey any
}

// ragaClaims are the custom claims embedded in the JWT.
type ragaClaims struct {
	jwt.RegisteredClaims
	Ragamuffin *ragaAccess `json:"ragamuffin,omitempty"`
}

type ragaAccess struct {
	Access string   `json:"access"`
	Vaults []string `json:"vaults,omitempty"`
}

// NewJWTAuthenticator creates a JWT authenticator.
func NewJWTAuthenticator(issuer, audience, jwksURL string, logger *slog.Logger) *JWTAuthenticator {
	return &JWTAuthenticator{
		issuer:   issuer,
		audience: audience,
		jwksURL:  jwksURL,
		logger:   logger,
		cacheTTL: 5 * time.Minute,
	}
}

// Authenticate validates a JWT bearer token.
func (j *JWTAuthenticator) Authenticate(r *http.Request) (*Claims, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil, ErrUnauthenticated
	}

	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenStr == authHeader || tokenStr == "" {
		return nil, ErrUnauthenticated
	}

	// Parse token header to extract kid (without verification)
	token, _, err := new(jwt.Parser).ParseUnverified(tokenStr, &ragaClaims{})
	if err != nil {
		return nil, fmt.Errorf("parse token: %w", err)
	}

	kid, ok := token.Header["kid"].(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("missing kid in token header")
	}

	// Fetch and cache JWKS
	keys, err := j.GetJWKS()
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}

	ck, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}

	// Verify and parse the token
	parsed, err := jwt.ParseWithClaims(tokenStr, &ragaClaims{}, func(t *jwt.Token) (any, error) {
		return ck.publicKey, nil
	},
		jwt.WithIssuer(j.issuer),
		jwt.WithAudience(j.audience),
		// Pin accepted algorithms as defense-in-depth against alg-confusion.
		// While our key-type parser rejects mismatches, pinning catches
		// future drift between parsing and validation.
		jwt.WithValidMethods([]string{"RS256", "ES256", "ES384", "ES512"}),
	)
	if err != nil {
		return nil, fmt.Errorf("jwt validation: %w", err)
	}

	claims, ok := parsed.Claims.(*ragaClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	if claims.Ragamuffin == nil || claims.Ragamuffin.Access == "" {
		return nil, fmt.Errorf("missing ragamuffin.access claim")
	}

	access := strings.Split(claims.Ragamuffin.Access, "_")
	return &Claims{
		Access: access,
		Vaults: claims.Ragamuffin.Vaults,
	}, nil
}

// GetJWKS fetches and caches JWKS keys. Exported for re-use by the
// OIDC authenticator which shares the same JWKS infrastructure.
func (j *JWTAuthenticator) GetJWKS() (map[string]cachedKey, error) {
	j.mu.RLock()
	if j.jwks != nil && time.Since(j.fetchedAt) < j.cacheTTL {
		defer j.mu.RUnlock()
		return j.jwks, nil
	}
	j.mu.RUnlock()

	j.mu.Lock()
	defer j.mu.Unlock()

	if j.jwks != nil && time.Since(j.fetchedAt) < j.cacheTTL {
		return j.jwks, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch returned %d", resp.StatusCode)
	}

	var jwksResp struct {
		Keys []map[string]any `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&jwksResp); err != nil {
		return nil, fmt.Errorf("decode jwks: %w", err)
	}

	keys := make(map[string]cachedKey)
	for _, raw := range jwksResp.Keys {
		kid, _ := raw["kid"].(string)
		if kid == "" {
			continue
		}
		kty, _ := raw["kty"].(string)

		pubKey, err := parseJWK(kty, raw)
		if err != nil {
			j.logger.Warn("jwt: failed to parse JWK key", "kid", kid, "error", err)
			continue
		}

		keys[kid] = cachedKey{publicKey: pubKey}
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no usable keys in JWKS")
	}

	j.jwks = keys
	j.fetchedAt = time.Now()
	return keys, nil
}

// parseJWK converts a raw JWK to a Go public key.
func parseJWK(kty string, raw map[string]any) (any, error) {
	switch kty {
	case "RSA":
		return parseRSAJWK(raw)
	case "EC":
		return parseECJWK(raw)
	default:
		return nil, fmt.Errorf("unsupported key type: %s", kty)
	}
}

func parseRSAJWK(raw map[string]any) (*rsa.PublicKey, error) {
	nStr, _ := raw["n"].(string)
	eStr, _ := raw["e"].(string)
	if nStr == "" || eStr == "" {
		return nil, fmt.Errorf("missing RSA key parameters")
	}

	nBytes, err := base64.RawURLEncoding.DecodeString(nStr)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(eStr)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}

	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}, nil
}

func parseECJWK(raw map[string]any) (*ecdsa.PublicKey, error) {
	crv, _ := raw["crv"].(string)
	xStr, _ := raw["x"].(string)
	yStr, _ := raw["y"].(string)
	if crv == "" || xStr == "" || yStr == "" {
		return nil, fmt.Errorf("missing EC key parameters")
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(xStr)
	if err != nil {
		return nil, fmt.Errorf("decode x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(yStr)
	if err != nil {
		return nil, fmt.Errorf("decode y: %w", err)
	}

	var curve elliptic.Curve
	switch crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported curve: %s", crv)
	}

	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}
