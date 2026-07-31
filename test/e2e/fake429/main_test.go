package main

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFake429Smoke proves fake429's two behaviors on a kernel-assigned port:
// a request whose path ends "/models" answers 200 (the launch-check /
// model-catalog call), and every other request (a completion endpoint)
// answers 429 with the CONFIGURED Retry-After header — not just any 429.
//
// It binds "127.0.0.1:0" and reads the real port back from ln.Addr(); it
// never dials a fixed port (kata 68fm is the same lesson for serf-hub — see
// cmd/serf-hub/main_ephemeral_port_test.go and docs/agentic-testing.md).
func TestFake429Smoke(t *testing.T) {
	const retryAfter = "3" // deliberately not defaultRetryAfterSeconds ("8"),
	// so a handler that ignores the configured value and always answers "8"
	// is caught below.

	ln, err := listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	serveDone := make(chan error, 1)
	go func() { serveDone <- serve(ln, retryAfter) }()
	t.Cleanup(func() {
		_ = ln.Close()
		<-serveDone // don't leak the serve goroutine past the test
	})

	base := "http://" + ln.Addr().String()

	// Await readiness by dialing, not by sleeping a guessed interval: Listen
	// already put the socket in the kernel's backlog before this goroutine
	// was even started, so the first attempt should succeed, but retry on a
	// short bounded interval rather than assume — the same idiom
	// cmd/serf-hub/main_ephemeral_port_test.go uses for the same reason.
	var modelsResp *http.Response
	var dialErr error
	for range 50 {
		modelsResp, dialErr = http.Get(base + "/v1/models")
		if dialErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dialErr != nil {
		t.Fatalf("dial %s: %v", base, dialErr)
	}

	// Behavior 1: the model-catalog endpoint answers 200.
	if modelsResp.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/models: status = %d, want %d", modelsResp.StatusCode, http.StatusOK)
	}
	_, _ = io.Copy(io.Discard, modelsResp.Body)
	_ = modelsResp.Body.Close()

	// Behavior 2: a completion endpoint answers 429 with the CONFIGURED
	// Retry-After.
	completionsResp, err := http.Post(base+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer func() { _ = completionsResp.Body.Close() }()

	if completionsResp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("POST /v1/chat/completions: status = %d, want %d", completionsResp.StatusCode, http.StatusTooManyRequests)
	}
	if got := completionsResp.Header.Get("Retry-After"); got != retryAfter {
		t.Errorf("Retry-After header = %q, want %q (the configured value, not a hardcoded default)", got, retryAfter)
	}
	if _, err := strconv.Atoi(completionsResp.Header.Get("Retry-After")); err != nil {
		t.Errorf("Retry-After header %q is not an integer: %v", completionsResp.Header.Get("Retry-After"), err)
	}
}
