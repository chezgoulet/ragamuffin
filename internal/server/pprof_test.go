package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPprofServer_RejectsNonLoopback(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:6060",
		":6060",
		"192.168.1.10:6060",
		"example.com:6060",
		"127.0.0.1",
		"127.0.0.1:not-a-port",
	} {
		t.Run(addr, func(t *testing.T) {
			if _, err := NewPprofServer(addr); err == nil {
				t.Fatalf("NewPprofServer(%q) = nil error, want rejection", addr)
			}
		})
	}
}

func TestNewPprofServer_AcceptsLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6060", "localhost:6060", "[::1]:6060"} {
		t.Run(addr, func(t *testing.T) {
			srv, err := NewPprofServer(addr)
			if err != nil {
				t.Fatalf("NewPprofServer(%q) error: %v", addr, err)
			}
			if srv.Addr != addr {
				t.Errorf("Addr = %q, want %q", srv.Addr, addr)
			}
		})
	}
}

func TestNewPprofServer_ServesIndex(t *testing.T) {
	srv, err := NewPprofServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("NewPprofServer: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/ = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !containsAll(body, "heap", "goroutine") {
		t.Errorf("pprof index missing expected profiles: %s", body)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
