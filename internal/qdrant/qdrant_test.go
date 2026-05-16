package qdrant

import (
	"testing"
)

// ── grpcTarget ────────────────────────────────────────────────────────────────

func TestGrpcTarget_StripsHTTPS(t *testing.T) {
	got := grpcTarget("https://qdrant.example.com:6334")
	want := "qdrant.example.com:6334"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "https://...", got, want)
	}
}

func TestGrpcTarget_StripsHTTP(t *testing.T) {
	got := grpcTarget("http://localhost:6334")
	want := "localhost:6334"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "http://...", got, want)
	}
}

func TestGrpcTarget_BareHostPort(t *testing.T) {
	got := grpcTarget("localhost:6334")
	want := "localhost:6334"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "localhost:6334", got, want)
	}
}

func TestGrpcTarget_IPWithPort(t *testing.T) {
	got := grpcTarget("10.0.0.1:6334")
	want := "10.0.0.1:6334"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "10.0.0.1:6334", got, want)
	}
}

func TestGrpcTarget_HostnameWithHTTPS(t *testing.T) {
	got := grpcTarget("https://my-qdrant.internal:6333")
	want := "my-qdrant.internal:6333"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "https://my-qdrant.internal:6333", got, want)
	}
}

func TestGrpcTarget_NoPort(t *testing.T) {
	got := grpcTarget("qdrant.internal")
	want := "qdrant.internal"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "qdrant.internal", got, want)
	}
}

func TestGrpcTarget_HTTPSNoPort(t *testing.T) {
	got := grpcTarget("https://qdrant.internal")
	want := "qdrant.internal"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "https://qdrant.internal", got, want)
	}
}

func TestGrpcTarget_SchemeAndPath(t *testing.T) {
	got := grpcTarget("http://qdrant:6334/path")
	want := "qdrant:6334/path"
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "http://qdrant:6334/path", got, want)
	}
}

func TestGrpcTarget_EmptyString(t *testing.T) {
	got := grpcTarget("")
	want := ""
	if got != want {
		t.Errorf("grpcTarget(%q) = %q, want %q", "", got, want)
	}
}

// ── Client construction requires a real Qdrant gRPC server ──────────────────
//
// The remaining Client methods (Health, Scroll, Upsert, Delete, etc.) require
// a live gRPC connection. Integration tests with a test Qdrant container are
// in testutil/qdrant_test.go or should be run via Docker Compose.
