package mcpstatus_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/cmd/serf-hub/internal/mcpstatus"
)

func TestProbeMCPStatus_StdioFound(t *testing.T) {
	// "sh" exists on every supported platform.
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "stdio", Command: "sh"})
	if got != "available" {
		t.Errorf("status=%q, want available", got)
	}
}

func TestProbeMCPStatus_StdioMissing(t *testing.T) {
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "stdio", Command: "definitely-not-installed-xyz123"})
	if got != "missing" {
		t.Errorf("status=%q, want missing", got)
	}
}

func TestProbeMCPStatus_StdioDefaultType(t *testing.T) {
	// Empty Type defaults to stdio.
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Command: "sh"})
	if got != "available" {
		t.Errorf("status=%q, want available", got)
	}
}

func TestProbeMCPStatus_HTTPReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http", URL: srv.URL})
	if got != "available" {
		t.Errorf("status=%q, want available", got)
	}
}

func TestProbeMCPStatus_HTTPUnreachable(t *testing.T) {
	// Bind to a closed port (127.0.0.1:1 is reliably unreachable).
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http", URL: "http://127.0.0.1:1/"})
	if got != "unreachable" {
		t.Errorf("status=%q, want unreachable", got)
	}
}

func TestProbeMCPStatus_StdioEmptyCommand(t *testing.T) {
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "stdio"})
	if got != "unknown" {
		t.Errorf("status=%q, want unknown", got)
	}
}

func TestProbeMCPStatus_HTTPEmptyURL(t *testing.T) {
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http"})
	if got != "unknown" {
		t.Errorf("status=%q, want unknown", got)
	}
}

func TestProbeMCPStatus_UnknownType(t *testing.T) {
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "carrier-pigeon", Command: "sh"})
	if got != "unknown" {
		t.Errorf("status=%q, want unknown", got)
	}
}

func TestProbeMCPStatus_HTTPWithHeaders(t *testing.T) {
	var seenAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{
		Type:    "http",
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer xyz"},
	})
	if got != "available" {
		t.Errorf("status=%q, want available", got)
	}
	if !strings.HasPrefix(seenAuth, "Bearer ") {
		t.Errorf("server saw Authorization=%q, want Bearer prefix", seenAuth)
	}
}

func TestProbeMCPStatus_SSEReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "sse", URL: srv.URL})
	if got != "available" {
		t.Errorf("status=%q, want available", got)
	}
}

func TestProbeMCPStatus_HTTPHeadErrorStatus(t *testing.T) {
	// A non-2xx HEAD response is still a response: client.Do returns err == nil,
	// so the probe reports "available" straight from the HEAD path without ever
	// reaching the GET fallback. Any HTTP status counts as reachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}))
	defer srv.Close()
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http", URL: srv.URL})
	if got != "available" {
		t.Errorf("status=%q, want available (HEAD returned a status, no fallback needed)", got)
	}
}

func TestProbeMCPStatus_HTTPHeadConnectionReset(t *testing.T) {
	// Server hijacks and closes the connection on HEAD, forcing a network error
	// (err != nil) — the only condition under which the SUT falls back to GET.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http", URL: srv.URL})
	if got != "available" {
		t.Errorf("status=%q, want available (HEAD failed, GET succeeded)", got)
	}
}

func TestProbeMCPStatus_HTTPInvalidURL(t *testing.T) {
	// Malformed URL should trigger http.NewRequestWithContext error on HEAD.
	got := mcpstatus.ProbeMCPStatus(mcpconfig.ServerConfig{Type: "http", URL: "://malformed"})
	if got != "unknown" {
		t.Errorf("status=%q, want unknown for malformed URL", got)
	}
}
