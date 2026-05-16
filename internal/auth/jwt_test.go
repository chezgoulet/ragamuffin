package auth

import (
	"net/http/httptest"
	"testing"
)

func TestJWTAuth_NoAuthHeader(t *testing.T) {
	a := NewJWTAuthenticator("issuer", "audience", "http://example.com/jwks", nil)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestJWTAuth_NoBearer(t *testing.T) {
	a := NewJWTAuthenticator("issuer", "audience", "http://example.com/jwks", nil)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestJWTAuth_InvalidTokenFormat(t *testing.T) {
	a := NewJWTAuthenticator("issuer", "audience", "http://example.com/jwks", nil)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer not-a-jwt")
	_, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for invalid token format")
	}
}

func TestJWTAuth_EmptyToken(t *testing.T) {
	a := NewJWTAuthenticator("issuer", "audience", "http://example.com/jwks", nil)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer ")
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated for empty token, got: %v", err)
	}
}

func TestJWTAuth_MalformedJWT(t *testing.T) {
	a := NewJWTAuthenticator("issuer", "audience", "http://example.com/jwks", nil)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJSUzI1NiIsImtpZCI6InRlc3QifQ.eyJzdWIiOiJ0ZXN0In0.signature")
	_, err := a.Authenticate(req)
	if err == nil {
		t.Fatal("expected error for malformed JWT (cannot fetch JWKS)")
	}
}

func TestJWTAuth_ConfigValidation(t *testing.T) {
	a := NewJWTAuthenticator("", "", "", nil)
	if a.issuer != "" {
		t.Errorf("expected empty issuer")
	}
	if a.cacheTTL != 5*60*1000*1000*1000 { // 5 minutes in nanoseconds
		// cacheTTL is time.Duration = 5 * time.Minute
		if a.cacheTTL == 0 {
			t.Error("expected non-zero cache TTL")
		}
	}
}

func TestJWTAuth_ConfigNotEmpty(t *testing.T) {
	a := NewJWTAuthenticator("my-issuer", "my-audience", "https://example.com/.well-known/jwks.json", nil)
	if a.issuer != "my-issuer" {
		t.Errorf("expected my-issuer, got %q", a.issuer)
	}
	if a.audience != "my-audience" {
		t.Errorf("expected my-audience, got %q", a.audience)
	}
	if a.jwksURL != "https://example.com/.well-known/jwks.json" {
		t.Errorf("expected jwks URL, got %q", a.jwksURL)
	}
}
