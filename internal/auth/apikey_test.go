package auth

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestAPIKey_NoAuthHeader(t *testing.T) {
	a := NewAPIKeyAuthenticator("read-key", "", nil, false)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestAPIKey_ValidReadKey(t *testing.T) {
	a := NewAPIKeyAuthenticator("read-key", "", nil, false)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer read-key")
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !claims.HasAccess("read") {
		t.Error("expected read access")
	}
	if claims.HasAccess("write") {
		t.Error("expected no write access")
	}
}

func TestAPIKey_ValidWriteKey(t *testing.T) {
	a := NewAPIKeyAuthenticator("read-key", "write-key", nil, false)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer write-key")
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !claims.HasAccess("read") {
		t.Error("expected read access")
	}
	if !claims.HasAccess("write") {
		t.Error("expected write access")
	}
}

func TestAPIKey_WrongKey(t *testing.T) {
	a := NewAPIKeyAuthenticator("read-key", "write-key", nil, false)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestAPIKey_NoBearer(t *testing.T) {
	a := NewAPIKeyAuthenticator("read-key", "", nil, false)
	req := httptest.NewRequest("GET", "/recall?query=test", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated, got: %v", err)
	}
}

func TestAPIKey_ScopedVaultRead(t *testing.T) {
	os.Setenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS", "docs-read-key")
	defer os.Unsetenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS")

	a := NewAPIKeyAuthenticator("", "", []string{"docs", "code"}, true)
	req := httptest.NewRequest("GET", "/vault/docs/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer docs-read-key")
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !claims.HasAccess("read") {
		t.Error("expected read access")
	}
	if claims.HasAccess("write") {
		t.Error("expected no write access for read key")
	}
}

func TestAPIKey_ScopedVaultWrongVault(t *testing.T) {
	os.Setenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS", "docs-read-key")
	defer os.Unsetenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS")

	a := NewAPIKeyAuthenticator("", "", []string{"docs", "code"}, true)
	req := httptest.NewRequest("GET", "/vault/code/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer docs-read-key")
	_, err := a.Authenticate(req)
	if err != ErrUnauthenticated {
		t.Fatalf("expected ErrUnauthenticated for wrong vault, got: %v", err)
	}
}

func TestAPIKey_ScopedVaultWrite(t *testing.T) {
	os.Setenv("RAGAMUFFIN_AUTH_WRITE_KEY_DOCS", "docs-write-key")
	defer os.Unsetenv("RAGAMUFFIN_AUTH_WRITE_KEY_DOCS")

	a := NewAPIKeyAuthenticator("", "", []string{"docs"}, true)
	req := httptest.NewRequest("POST", "/vault/docs/reindex", nil)
	req.Header.Set("Authorization", "Bearer docs-write-key")
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !claims.HasAccess("read") {
		t.Error("expected read access from write key")
	}
	if !claims.HasAccess("write") {
		t.Error("expected write access")
	}
}

func TestAPIKey_GlobalKeyWithMultiTenant(t *testing.T) {
	os.Setenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS", "docs-read-key")
	defer os.Unsetenv("RAGAMUFFIN_AUTH_READ_KEY_DOCS")

	a := NewAPIKeyAuthenticator("global-read", "", []string{"docs"}, true)
	// Global key should still work in multi-tenant mode
	req := httptest.NewRequest("GET", "/vault/docs/recall?query=test", nil)
	req.Header.Set("Authorization", "Bearer global-read")
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error with global key, got: %v", err)
	}
	if !claims.HasAccess("read") {
		t.Error("expected read access from global key")
	}
}
