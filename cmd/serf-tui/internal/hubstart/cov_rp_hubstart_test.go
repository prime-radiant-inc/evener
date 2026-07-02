package hubstart

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAuthToken(t *testing.T) {
	// Explicit value short-circuits any file lookup.
	if got := ResolveAuthToken("explicit-tok", ""); got != "explicit-tok" {
		t.Fatalf("explicit token = %q, want explicit-tok", got)
	}

	// A token file under $HOME/.serf is read when no explicit value is given.
	home := t.TempDir()
	t.Setenv("HOME", home)
	serfDir := filepath.Join(home, ".serf")
	if err := os.MkdirAll(serfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serfDir, "auth-token"), []byte("  file-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, tokenFile := resolveAuthToken("", "")
	if tok != "file-tok" {
		t.Fatalf("resolveAuthToken file = %q, want file-tok", tok)
	}
	if !strings.HasSuffix(tokenFile, filepath.Join(".serf", "auth-token")) {
		t.Fatalf("token file path = %q", tokenFile)
	}
	if got := ResolveAuthToken("", ""); got != "file-tok" {
		t.Fatalf("ResolveAuthToken file = %q, want file-tok", got)
	}
}

func TestResolveAuthTokenMissingWarns(t *testing.T) {
	home := t.TempDir() // no .serf/auth-token created
	t.Setenv("HOME", home)

	warn := captureStderr(t, func() {
		if got := ResolveAuthToken("", ""); got != "" {
			t.Errorf("missing token = %q, want empty", got)
		}
	})
	if !strings.Contains(warn, "no hub auth token found") {
		t.Fatalf("expected a warning on stderr, got %q", warn)
	}
}

func TestAuthTokenFilePathWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	// With no resolvable home dir, the path falls back to a relative location.
	if got := AuthTokenFilePath(""); got != filepath.Join(".serf", "auth-token") {
		t.Fatalf("AuthTokenFilePath without HOME = %q", got)
	}
}

func TestStartupErrorScreenAllKinds(t *testing.T) {
	kinds := map[StartupErrorKind]string{
		StartupErrorMissingHubBinary:  "Cannot find serf-hub binary",
		StartupErrorBindFailure:       "Hub failed to bind",
		StartupErrorUnhealthyHub:      "did not become healthy",
		StartupErrorIncompatibleAPI:   "Hub API is incompatible",
		StartupErrorStaleEnvironment:  "different state/auth environment",
		StartupErrorRemoteNoAutoStart: "only auto-starts local Hubs",
		StartupErrorHubUnavailable:    "Hub is not reachable",
	}
	for kind, want := range kinds {
		screen := StartupErrorScreen(StartupError{Kind: kind, Addr: "http://x", Detail: "boom"})
		if !strings.Contains(screen, want) {
			t.Errorf("kind %q screen missing %q:\n%s", kind, want, screen)
		}
		if !strings.Contains(screen, "Serf TUI startup failed") {
			t.Errorf("kind %q screen missing banner", kind)
		}
	}

	// A non-StartupError falls back to the generic template.
	generic := StartupErrorScreen(errors.New("raw failure"))
	if !strings.Contains(generic, "raw failure") {
		t.Errorf("generic screen missing raw error: %q", generic)
	}

	// Detail falls back to the wrapped error when Detail is empty.
	wrapped := StartupErrorScreen(StartupError{Kind: StartupErrorUnhealthyHub, Addr: "http://x", Err: errors.New("wrapped detail")})
	if !strings.Contains(wrapped, "wrapped detail") {
		t.Errorf("expected wrapped detail in screen: %q", wrapped)
	}
}

func TestCheckHubEnvironment(t *testing.T) {
	ctx := context.Background()

	// Empty stateDir is always a no-op.
	if err := checkHubEnvironment(ctx, HubAddress{BaseURL: "http://unused"}, http.DefaultClient, ""); err != nil {
		t.Fatalf("empty stateDir should be nil, got %v", err)
	}

	// A hub whose state glob matches the requested stateDir is compatible.
	stateDir := t.TempDir()
	matching := filepath.Join(filepath.Clean(stateDir), "projects", "*")
	srv := healthServer(t, `{"state_glob":`+quote(matching)+`}`, http.StatusOK)
	defer srv.Close()
	if err := checkHubEnvironment(ctx, HubAddress{BaseURL: srv.URL}, srv.Client(), stateDir); err != nil {
		t.Fatalf("matching glob should be nil, got %v", err)
	}

	// A mismatched glob is a stale-environment error.
	mismatch := healthServer(t, `{"state_glob":"/somewhere/else/projects/*"}`, http.StatusOK)
	defer mismatch.Close()
	err := checkHubEnvironment(ctx, HubAddress{BaseURL: mismatch.URL}, mismatch.Client(), stateDir)
	var se StartupError
	if !errors.As(err, &se) || se.Kind != StartupErrorStaleEnvironment {
		t.Fatalf("mismatched glob = %v, want StaleEnvironment", err)
	}

	// An unreachable/erroring health endpoint is treated as no conflict.
	broken := healthServer(t, `boom`, http.StatusInternalServerError)
	defer broken.Close()
	if err := checkHubEnvironment(ctx, HubAddress{BaseURL: broken.URL}, broken.Client(), stateDir); err != nil {
		t.Fatalf("broken health should be nil (best-effort), got %v", err)
	}
}

func healthServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func quote(s string) string { return `"` + s + `"` }

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
