package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzSessionAPILogWrite drives the per-session routing mode of APILogger
// (NewSessionAPILogger + sessionLogBaseName + sessionFile) with fuzzed session
// ids through the same complete/stream write paths as FuzzAPILogWrite.
//
// Oracles (not bare no-panic):
//   - the routed session file exists under <stateDir>/sessions/ and holds
//     exactly two valid APILogEntry JSONL lines (one complete + one stream);
//   - a hostile session id never escapes the sessions dir (only the expected
//     basename appears, everything else routes to unattributed);
//   - the frozen project-level api.jsonl is never created;
//   - raw entries, when enabled, land in the sibling .api-raw.jsonl and decode
//     as APIRawLogEntry.
func FuzzSessionAPILogWrite(f *testing.F) {
	f.Add("01SESSION", []byte(`{"model":"gpt-5.2","provider":"openai"}`), true, false)
	f.Add("", []byte(`{}`), false, true)
	f.Add("../../etc/passwd", []byte(`{"messages":[]}`), true, true)
	f.Add("a b\x00c", []byte(`{}`), false, false)

	f.Fuzz(func(t *testing.T, sessionID string, reqBytes []byte, rawLogging, completeErr bool) {
		var req Request
		_ = json.Unmarshal(reqBytes, &req)
		resp := Response{ID: "r1", Model: req.Model, RawRequestBody: "REQBODY", RawResponseBody: "RESPBODY"}

		dir := t.TempDir()
		logger, err := NewSessionAPILogger(dir)
		if err != nil {
			t.Fatalf("NewSessionAPILogger: %v", err)
		}
		if rawLogging {
			logger.EnableSessionRawLogging()
		}

		ctx := WithAPILogContext(context.Background(), sessionID, 1)

		var nextErr error
		if completeErr {
			nextErr = NewStreamErrorWithRawBodies("openai", "boom", nil, "ERQ", "ERS")
		}
		next := func(_ context.Context, _ Request) (Response, error) { return resp, nextErr }
		gotResp, gotErr := logger.WrapComplete(next)(ctx, req)
		if !errors.Is(gotErr, nextErr) || (nextErr == nil && gotErr != nil) {
			t.Fatalf("WrapComplete altered the error: got %v want %v", gotErr, nextErr)
		}
		if gotResp.ID != resp.ID {
			t.Fatalf("WrapComplete altered the response identity")
		}

		streamNext := func(context.Context, Request) (Stream, error) {
			return newSliceStream(StreamEvent{Type: StreamEventFinish, Response: &resp}), nil
		}
		st, serr := logger.WrapStream(streamNext)(ctx, req)
		if serr == nil && st != nil {
			for range st.Events() { //nolint:revive // drain
			}
			_ = st.Close()
		}

		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		base := sessionLogBaseName(sessionID)
		sessDir := filepath.Join(dir, "sessions")
		routed := filepath.Join(sessDir, base+".api.jsonl")
		lines := readLines(t, routed)
		if len(lines) != 2 {
			t.Fatalf("routed log %q has %d lines, want 2:\n%q", routed, len(lines), lines)
		}
		for _, ln := range lines {
			var e APILogEntry
			if err := json.Unmarshal([]byte(ln), &e); err != nil {
				t.Fatalf("api line is not valid APILogEntry JSON: %v (%q)", err, ln)
			}
		}
		// No file other than the routed pair may exist, and nothing escapes
		// the sessions dir.
		entries, err := os.ReadDir(sessDir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		for _, e := range entries {
			if e.Name() != base+".api.jsonl" && e.Name() != base+".api-raw.jsonl" {
				t.Fatalf("unexpected file %q in sessions dir (session id %q)", e.Name(), sessionID)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "api.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("frozen project-level api.jsonl was written (stat err=%v)", err)
		}
		if rawLogging {
			for _, ln := range readLines(t, filepath.Join(sessDir, base+".api-raw.jsonl")) {
				var e APIRawLogEntry
				if err := json.Unmarshal([]byte(ln), &e); err != nil {
					t.Fatalf("raw line is not valid APIRawLogEntry JSON: %v (%q)", err, ln)
				}
				if !strings.Contains(ln, "REQBODY") && !strings.Contains(ln, "ERQ") {
					t.Fatalf("raw line lost the request body: %q", ln)
				}
			}
		}
	})
}
