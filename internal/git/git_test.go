package git

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ── genBranch ───────────────────────────────────────────────────────────────

func TestGenBranch_Format(t *testing.T) {
	b := genBranch()
	if !strings.HasPrefix(b, "ragamuffin/draft-") {
		t.Errorf("expected prefix 'ragamuffin/draft-', got %q", b)
	}
	// After prefix: 6 bytes → 12 hex chars
	hexPart := strings.TrimPrefix(b, "ragamuffin/draft-")
	if len(hexPart) != 12 {
		t.Errorf("expected 12 hex chars, got %d (%q)", len(hexPart), hexPart)
	}
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex char %q in branch %q", c, b)
		}
	}
}

func TestGenBranch_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		b := genBranch()
		if seen[b] {
			t.Errorf("duplicate branch: %q", b)
		}
		seen[b] = true
	}
}

// ── New provider selection ──────────────────────────────────────────────────

func TestNew_DefaultGithub(t *testing.T) {
	p := New("", "token", "")
	if _, ok := p.(*githubProvider); !ok {
		t.Errorf("expected *githubProvider for empty provider, got %T", p)
	}
}

func TestNew_Github(t *testing.T) {
	p := New("github", "token", "")
	if _, ok := p.(*githubProvider); !ok {
		t.Errorf("expected *githubProvider, got %T", p)
	}
}

func TestNew_Gitlab(t *testing.T) {
	p := New("gitlab", "token", "")
	if _, ok := p.(*gitlabProvider); !ok {
		t.Errorf("expected *gitlabProvider, got %T", p)
	}
}

func TestNew_Gitea(t *testing.T) {
	p := New("gitea", "token", "https://git.example.com")
	if _, ok := p.(*giteaProvider); !ok {
		t.Errorf("expected *giteaProvider, got %T", p)
	}
}




// ── GitHub provider direct test ─────────────────────────────────────────────

func TestGithubProvider_Do_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected 'Bearer test-token', got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("expected GitHub Accept header")
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	p := &githubProvider{token: "test-token", client: srv.Client()}
	var result map[string]string
	err := p.do(context.Background(), "GET", srv.URL, nil, &result)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("expected 'ok', got %q", result["status"])
	}
}

func TestGithubProvider_Do_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
	}))
	defer srv.Close()

	p := &githubProvider{token: "bad", client: srv.Client()}
	err := p.do(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got %q", err.Error())
	}
}

func TestGithubProvider_Do_WithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type: application/json")
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["key"] != "value" {
			t.Errorf("expected key=value, got %v", body)
		}
		json.NewEncoder(w).Encode(map[string]string{"result": "created"})
	}))
	defer srv.Close()

	p := &githubProvider{token: "t", client: srv.Client()}
	var out map[string]string
	err := p.do(context.Background(), "POST", srv.URL, map[string]string{"key": "value"}, &out)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if out["result"] != "created" {
		t.Errorf("expected 'created', got %q", out["result"])
	}
}

// ── GitLab provider direct test ─────────────────────────────────────────────

func TestGitlabProvider_Do_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
			t.Errorf("expected PRIVATE-TOKEN header")
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	p := &gitlabProvider{token: "test-token", client: srv.Client()}
	var result map[string]string
	err := p.do(context.Background(), "GET", srv.URL, nil, &result)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
}

func TestGitlabProvider_Do_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer srv.Close()

	p := &gitlabProvider{token: "t", client: srv.Client()}
	err := p.do(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

// ── Gitea provider direct test ──────────────────────────────────────────────

func TestGiteaProvider_Do_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected 'Bearer test-token', got %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	p := &giteaProvider{token: "test-token", client: srv.Client(), baseURL: srv.URL + "/api/v1"}
	var result map[string]string
	err := p.do(context.Background(), "GET", srv.URL, nil, &result)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
}

func TestGiteaProvider_Do_409IsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"message": "Conflict"})
	}))
	defer srv.Close()

	p := &giteaProvider{token: "t", client: srv.Client(), baseURL: ""}
	// 409 is treated as non-error for Gitea
	err := p.do(context.Background(), "GET", srv.URL, nil, nil)
	if err != nil {
		t.Errorf("expected no error for 409, got %v", err)
	}
}

func TestGiteaProvider_Do_Non409Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		json.NewEncoder(w).Encode(map[string]string{"message": "Forbidden"})
	}))
	defer srv.Close()

	p := &giteaProvider{token: "t", client: srv.Client()}
	err := p.do(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for 403")
	}
}

// ── error handling ──────────────────────────────────────────────────────────

func TestProvider_RequestFailure(t *testing.T) {
	// A stopped server gives connection refused
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	p := &githubProvider{token: "t", client: srv.Client()}
	err := p.do(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("expected error for closed server")
	}
}
