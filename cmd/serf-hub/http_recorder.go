package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"primeradiant.com/serf/envvars"
)

// httpRecorderMaxBodyBytes caps the request-body copy a recording keeps. The
// downstream handler still receives the full body; only the recorded copy is
// bounded so a large upload cannot blow up the recording file or memory.
const httpRecorderMaxBodyBytes = 64 << 10

// recordedHTTPRequest is one JSONL line in hub-http.jsonl. It is the raw,
// unscrubbed inbound request; sanitization happens later in serf-fuzz-harvest,
// which for the http fuzz surface reverse-maps only Method+Path (headers and
// body never reach a committed seed). The file is never committed.
type recordedHTTPRequest struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    string              `json:"body,omitempty"`
}

// newHTTPRequestRecorder returns middleware that appends every inbound request
// to <stateRoot>/hub-http.jsonl for fuzz-corpus harvesting. It is opt-in via
// SERF_RECORD_HTTP and default-off: when unset (or the file cannot be opened) it
// returns the identity middleware, so the handler stack is byte-identical to the
// unrecorded one. Recording is side-effect-only — a failed write is swallowed
// and never changes the response.
func newHTTPRequestRecorder(stateRoot string) func(http.Handler) http.Handler {
	identity := func(next http.Handler) http.Handler { return next }
	if !envRecordHTTPEnabled(envvars.SERFRecordHTTP.Getenv()) {
		return identity
	}
	f, err := os.OpenFile(filepath.Join(stateRoot, "hub-http.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return identity
	}
	var mu sync.Mutex
	write := func(rec recordedHTTPRequest) {
		line, err := json.Marshal(rec)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		f.Write(append(line, '\n')) //nolint:errcheck // best-effort; recording never affects the response
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := recordedHTTPRequest{
				Method:  r.Method,
				Path:    r.URL.Path,
				Query:   r.URL.RawQuery,
				Headers: r.Header,
			}
			if r.Body != nil {
				// Read a capped copy for the recording, then restore the body so the
				// downstream handler still sees every byte.
				capped, _ := io.ReadAll(io.LimitReader(r.Body, httpRecorderMaxBodyBytes)) //nolint:errcheck
				rec.Body = string(capped)
				r.Body = struct {
					io.Reader
					io.Closer
				}{Reader: io.MultiReader(bytes.NewReader(capped), r.Body), Closer: r.Body}
			}
			write(rec)
			next.ServeHTTP(w, r)
		})
	}
}

// envRecordHTTPEnabled reports whether SERF_RECORD_HTTP selects recording.
func envRecordHTTPEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
