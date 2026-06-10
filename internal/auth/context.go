package auth

import "context"

type ctxKey string

const claimsKey ctxKey = "auth_claims"

// WithClaims stores claims in the context.
func WithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsKey, claims)
}

// contextWithClaims is a backwards-compatible alias for WithClaims.
func contextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return WithClaims(ctx, claims)
}

// ClaimsFromContext retrieves claims from the context.
// Returns nil if no claims are present.
func ClaimsFromContext(ctx context.Context) *Claims {
	if claims, ok := ctx.Value(claimsKey).(*Claims); ok {
		return claims
	}
	return nil
}
