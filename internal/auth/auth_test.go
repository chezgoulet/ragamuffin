package auth

import (
	"net/http/httptest"
	"testing"
)

func TestNoneAuthenticator_AlwaysPermits(t *testing.T) {
	a := &NoneAuthenticator{}
	req := httptest.NewRequest("GET", "/health", nil)
	claims, err := a.Authenticate(req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
}

func TestNoneAuthenticator_EmptyRequest(t *testing.T) {
	a := &NoneAuthenticator{}
	claims, err := a.Authenticate(nil)
	if err != nil {
		t.Fatalf("expected no error for nil request, got: %v", err)
	}
	if claims == nil {
		t.Fatal("expected non-nil claims")
	}
}

func TestParseMode_ValidModes(t *testing.T) {
	tests := []struct {
		input string
		want  Mode
	}{
		{"none", ModeNone},
		{"api_key", ModeAPIKey},
		{"jwt", ModeJWT},
		{"NONE", ModeNone},
		{"API_KEY", ModeAPIKey},
		{"Jwt", ModeJWT},
	}
	for _, tt := range tests {
		got, err := ParseMode(tt.input)
		if err != nil {
			t.Errorf("ParseMode(%q): unexpected error: %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("ParseMode(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestParseMode_Invalid(t *testing.T) {
	_, err := ParseMode("invalid")
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

func TestParseMode_Empty(t *testing.T) {
	_, err := ParseMode("")
	if err == nil {
		t.Fatal("expected error for empty mode")
	}
}

func TestPublicPaths_Skipped(t *testing.T) {
	for path := range PublicPaths {
		// Verifies all PublicPaths keys are non-empty strings
		if path == "" {
			t.Error("empty public path")
		}
	}
}

func TestClaims_HasAccess(t *testing.T) {
	c := &Claims{Access: []string{"read", "write"}}
	if !c.HasAccess("read") {
		t.Error("expected read access")
	}
	if !c.HasAccess("write") {
		t.Error("expected write access")
	}
}

func TestClaims_HasAccessDenied(t *testing.T) {
	c := &Claims{Access: []string{"read"}}
	if c.HasAccess("write") {
		t.Error("expected no write access")
	}
}

func TestClaims_HasVaultAccess(t *testing.T) {
	c := &Claims{Access: []string{"read"}, Vaults: []string{"docs", "code"}}
	if !c.HasVaultAccess("docs") {
		t.Error("expected access to docs vault")
	}
	if c.HasVaultAccess("secret") {
		t.Error("expected no access to secret vault")
	}
}

func TestClaims_HasVaultAccess_NilList(t *testing.T) {
	c := &Claims{Access: []string{"read"}, Vaults: nil}
	// nil vaults list means access to all vaults
	if !c.HasVaultAccess("anything") {
		t.Error("expected access to any vault when vaults list is nil")
	}
}

func TestClaims_HasVaultAccess_EmptyList(t *testing.T) {
	c := &Claims{Access: []string{"read"}, Vaults: []string{}}
	// Empty and nil both mean unrestricted access
	if !c.HasVaultAccess("anything") {
		t.Error("expected access to any vault when vaults list is empty (same as nil)")
	}
}
