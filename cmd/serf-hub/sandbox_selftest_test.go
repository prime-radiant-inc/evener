package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// denyTransport is an http.RoundTripper that records every outbound request and
// refuses it. Installed as http.DefaultTransport for the duration of the
// containment self-test, it is the network tripwire: the hub's source registry
// dials with http.DefaultClient (whose nil Transport falls through to
// http.DefaultTransport), so any accidental daemon/provider/upgrade dial lands
// here and is both blocked and counted.
type denyTransport struct {
	mu       sync.Mutex
	attempts []string
}

func (d *denyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.mu.Lock()
	d.attempts = append(d.attempts, req.Method+" "+req.URL.String())
	d.mu.Unlock()
	return nil, fmt.Errorf("sandbox: network blocked: %s", req.URL)
}

func (d *denyTransport) Attempts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.attempts...)
}

// installDenyTransport swaps http.DefaultTransport for a recording deny-dialer
// and restores it when the test ends.
func installDenyTransport(t *testing.T) *denyTransport {
	t.Helper()
	deny := &denyTransport{}
	orig := http.DefaultTransport
	http.DefaultTransport = deny
	t.Cleanup(func() { http.DefaultTransport = orig })
	return deny
}

// TestSandboxContainsMutatingHandlers is the B0 containment proof. It drives the
// hub's MUTATING handlers — spawn, git-head, models, dir-create, an action verb
// — through the full handler stack and asserts that none of them spawned a real
// process, shelled out, hit the network, or created a file outside the sandbox.
func TestSandboxContainsMutatingHandlers(t *testing.T) {
	deny := installDenyTransport(t)
	s := newSandbox(t)
	handler := s.Web.Handler()

	do := func(method, target string, body any) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			rdr = bytes.NewReader(raw)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, target, rdr)
		req.Host = "127.0.0.1:9180"
		req.Header.Set("HX-Request", "true") // required by /doc/file; harmless elsewhere
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("5xx from %s %s: %d body=%s", method, target, rec.Code, rec.Body.String())
		}
		return rec
	}

	// 1. Spawn: the request reaches the recording Spawner, never a subprocess.
	rec := do(http.MethodPost, "/api/spawn", map[string]any{
		"harness":     "serf",
		"working_dir": s.CWD,
		"model":       "openai/gpt-5.5",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("spawn: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := len(s.Spawner.Spawns()); got != 1 {
		t.Fatalf("spawn did not reach the recording spawner: recorded %d spawns", got)
	}

	// 2. git-head: the response carries the seam's sentinel branch, proving no
	// real `git` ran.
	rec = do(http.MethodGet, "/api/git/head?cwd="+s.CWD, nil)
	var gh struct {
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gh); err != nil {
		t.Fatalf("git/head body: %v", err)
	}
	if gh.Branch != sandboxGitBranch {
		t.Fatalf("git/head did not use the seam: branch=%q want %q", gh.Branch, sandboxGitBranch)
	}

	// 3. models: the response is the seam's fixed list, proving no provider call.
	rec = do(http.MethodGet, "/api/models", nil)
	if !bytes.Contains(rec.Body.Bytes(), []byte("fake-model")) {
		t.Fatalf("models did not use the seam: body=%s", rec.Body.String())
	}

	// 4. dir-create: a path OUTSIDE the sandbox root is recorded but never made.
	forbiddenRoot := t.TempDir() // outside s.Root
	forbidden := filepath.Join(forbiddenRoot, "should-not-be-created", "deep")
	rec = do(http.MethodPost, "/api/dirs/create", map[string]any{"path": forbidden})
	if rec.Code != http.StatusOK {
		t.Fatalf("dirs/create: want 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(forbidden); !os.IsNotExist(err) {
		t.Fatalf("dir-create escaped the sandbox: %s exists on disk (err=%v)", forbidden, err)
	}
	if paths := s.Mkdir.Paths(); len(paths) != 1 || !strings.HasPrefix(paths[0], forbiddenRoot) {
		t.Fatalf("dir-create did not reach the seam: recorded %v", paths)
	}

	// 5. action verb: clear on a non-live session resolves before any daemon
	// dial — a contained 404, not a hang or a network call.
	rec = do(http.MethodPost, "/api/sessions/"+sandboxSessionID+"/clear", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("clear on non-live session: want 404, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Network tripwire: nothing above may have dialed.
	if attempts := deny.Attempts(); len(attempts) != 0 {
		t.Fatalf("sandbox made %d network attempt(s): %v", len(attempts), attempts)
	}

	// Path-escape tripwire: the out-of-root secret must never have been served.
	// (Re-checked here directly; the read-only FuzzWebHandler oracle guards it too.)
	rec = do(http.MethodGet, "/doc/file?session="+sandboxSessionID+"&path=../fuzz-secret.txt", nil)
	if bytes.Contains(rec.Body.Bytes(), s.Secret) {
		t.Fatalf("path escape: /doc/file served the out-of-root secret")
	}
}
