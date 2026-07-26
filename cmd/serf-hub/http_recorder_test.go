package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/envvars"
)

// readHTTPRecordings parses hub-http.jsonl into its recorded entries.
func readHTTPRecordings(t *testing.T, path string) []recordedHTTPRequest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open recording: %v", err)
	}
	defer f.Close() //nolint:errcheck
	var out []recordedHTTPRequest
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec recordedHTTPRequest
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan recording: %v", err)
	}
	return out
}

// With SERF_RECORD_HTTP unset the middleware is identity: no file is written and
// the wrapped handler's behavior is byte-identical.
func TestHTTPRecorderDisabledIsNoOp(t *testing.T) {
	t.Setenv(envvars.SERFRecordHTTP.Name, "")
	root := t.TempDir()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("seen:" + string(body))) //nolint:errcheck
	})
	mw := newHTTPRequestRecorder(root)
	handler := mw(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/health?x=1", strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot || rec.Body.String() != "seen:payload" {
		t.Fatalf("behavior changed with recorder disabled: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "hub-http.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("recording file created while disabled: err=%v", err)
	}
}

// With SERF_RECORD_HTTP set the middleware records each request and still passes
// the request through unchanged (the body remains readable downstream).
func TestHTTPRecorderRecordsAndPreservesBody(t *testing.T) {
	t.Setenv(envvars.SERFRecordHTTP.Name, "1")
	root := t.TempDir()

	var seenBody string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck
		seenBody = string(body)
		w.WriteHeader(http.StatusOK)
	})
	handler := newHTTPRequestRecorder(root)(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/sessions/abc?foo=bar", strings.NewReader("hello-body"))
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if seenBody != "hello-body" {
		t.Fatalf("downstream body altered: %q", seenBody)
	}

	recs := readHTTPRecordings(t, filepath.Join(root, "hub-http.jsonl"))
	if len(recs) != 1 {
		t.Fatalf("got %d recordings, want 1", len(recs))
	}
	got := recs[0]
	if got.Method != http.MethodPost || got.Path != "/api/sessions/abc" || got.Query != "foo=bar" || got.Body != "hello-body" {
		t.Fatalf("recording mismatch: %+v", got)
	}
	if got.Headers["Hx-Request"][0] != "true" {
		t.Fatalf("headers not recorded: %+v", got.Headers)
	}
}

// Oversized bodies are capped, not buffered without bound, and the downstream
// handler still sees the full body.
func TestHTTPRecorderCapsBody(t *testing.T) {
	t.Setenv(envvars.SERFRecordHTTP.Name, "on")
	root := t.TempDir()

	big := strings.Repeat("A", httpRecorderMaxBodyBytes+5000)
	var seenLen int
	handler := newHTTPRequestRecorder(root)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck
		seenLen = len(body)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if seenLen != len(big) {
		t.Fatalf("downstream saw %d bytes, want full %d", seenLen, len(big))
	}
	recs := readHTTPRecordings(t, filepath.Join(root, "hub-http.jsonl"))
	if len(recs) != 1 || len(recs[0].Body) != httpRecorderMaxBodyBytes {
		t.Fatalf("body not capped to %d: got %d recordings, body len %d", httpRecorderMaxBodyBytes, len(recs), bodyLen(recs))
	}
}

// TestReadHTTPRecordings_ScanErrorFailsTest verifies that a scan failure
// while reading hub-http.jsonl (here, a line exceeding the 16MB buffer cap
// set on the scanner) fails the test loudly rather than silently returning
// whatever was decoded before the error - Scan() returns false identically
// for a clean EOF and a real read error, so only scanner.Err() tells them
// apart.
//
// readHTTPRecordings calls t.Fatalf on its own T, and a failing subtest
// always marks its parent as failed too, so the only way to observe "did it
// fail correctly" without permanently red-ing this test binary is to run
// the failing case in a subprocess and check its exit status.
const scanErrorSubprocEnv = "SERF_TEST_SCANERROR_SUBPROC"

func TestReadHTTPRecordings_ScanErrorFailsTest(t *testing.T) {
	// In the subprocess, use the fixture path the parent already wrote -
	// don't regenerate it, or a fresh t.TempDir() path would never match
	// and we'd recurse into another subprocess forever.
	if path := os.Getenv(scanErrorSubprocEnv); path != "" {
		readHTTPRecordings(t, path)
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "hub-http.jsonl")
	tooLong := strings.Repeat("x", 16*1024*1024+10)
	if err := os.WriteFile(path, []byte(tooLong+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestReadHTTPRecordings_ScanErrorFailsTest$")
	cmd.Env = append(os.Environ(), scanErrorSubprocEnv+"="+path)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected readHTTPRecordings to fail the test on a scan error, but the subprocess exited cleanly:\n%s", out)
	}
}

func bodyLen(recs []recordedHTTPRequest) int {
	if len(recs) == 0 {
		return -1
	}
	return len(recs[0].Body)
}
