package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// sliceStream replays a fixed event slice then closes. It honors the Stream
// contract: Events returns the SAME pre-filled, already-closed channel on every
// call (a fresh channel per call would make a re-reading pump loop forever).
type sliceStream struct {
	ch     chan StreamEvent
	closed bool
}

func newSliceStream(events ...StreamEvent) *sliceStream {
	ch := make(chan StreamEvent, len(events)+1)
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &sliceStream{ch: ch}
}

func (s *sliceStream) Events() <-chan StreamEvent { return s.ch }
func (s *sliceStream) Close() error               { s.closed = true; return nil }

// FuzzAPILogWrite drives the APILogger write/stream path end to end against a
// real file in a t.TempDir sandbox: NewAPILogger, EnableRawLogging, WrapComplete,
// WrapStream + newAPILogStream + pump + logFinish/logError, the adapter-attempt
// recorder (withAdapterAttemptLogging -> RecordAdapterAttempt -> writeAdapterAttempt
// -> StampEndpointURL), write/writeRaw/writeRawResponse/writeRawError, and Close.
// These were 0% under fuzz; the stub next/stream never touches the network.
//
// Oracles (not bare no-panic):
//   - WrapComplete returns exactly the response and error its next produced
//     (the logger is a transparent middleware).
//   - exactly two api.jsonl lines are produced: one per completed call and one
//     per finished/failed stream (the logger must neither drop nor duplicate a
//     log line).
//   - every line written to api.jsonl is well-formed JSON decoding to an
//     APILogEntry, and every raw.jsonl line decodes to an APIRawLogEntry — proving
//     the writer always emits valid JSONL.
func FuzzAPILogWrite(f *testing.F) {
	f.Add(
		[]byte(`{"model":"gpt-5.2","provider":"openai","messages":[{"role":"user"}],"tools":[{"name":"shell"}]}`),
		[]byte(`{"id":"r1","model":"gpt-5.2","raw":{"endpoint_url":"https://x"}}`),
		true, false, uint8(0),
	)
	f.Add([]byte(`{}`), []byte(`{}`), false, true, uint8(1))
	f.Add([]byte(`{"messages":[]}`), []byte(`null`), true, true, uint8(2))

	f.Fuzz(func(t *testing.T, reqBytes, respBytes []byte, rawLogging, completeErr bool, streamSel uint8) {
		var req Request
		_ = json.Unmarshal(reqBytes, &req)
		var resp Response
		_ = json.Unmarshal(respBytes, &resp)
		resp.RawRequestBody = "REQBODY"
		resp.RawResponseBody = "RESPBODY"

		dir := t.TempDir()
		apiPath := filepath.Join(dir, "api.jsonl")
		logger, err := NewAPILogger(apiPath)
		if err != nil {
			t.Fatalf("NewAPILogger: %v", err)
		}
		rawPath := filepath.Join(dir, "raw.jsonl")
		if rawLogging {
			if err := logger.EnableRawLogging(rawPath); err != nil {
				t.Fatalf("EnableRawLogging: %v", err)
			}
		}

		ctx := WithAPILogContext(context.Background(), "sess", 1)

		// --- Complete path: exactly one api.jsonl line. ---
		var nextErr error
		if completeErr {
			nextErr = NewStreamErrorWithRawBodies("openai", "boom", nil, "ERQ", "ERS")
		}
		next := func(_ context.Context, _ Request) (Response, error) { return resp, nextErr }
		gotResp, gotErr := logger.WrapComplete(next)(ctx, req)
		if !errors.Is(gotErr, nextErr) || (nextErr == nil && gotErr != nil) {
			t.Fatalf("WrapComplete altered the error: got %v want %v", gotErr, nextErr)
		}
		if gotResp.ID != resp.ID || gotResp.Model != resp.Model {
			t.Fatalf("WrapComplete altered the response identity")
		}

		// --- Stream path: exactly one api.jsonl line regardless of branch. ---
		streamNext := buildStreamNext(streamSel, resp, req)
		st, serr := logger.WrapStream(streamNext)(ctx, req)
		if serr == nil && st != nil {
			for range st.Events() { //nolint:revive // drain
			}
			_ = st.Close()
		}

		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// Oracle: api.jsonl has exactly two valid entries.
		lines := readLines(t, apiPath)
		if len(lines) != 2 {
			t.Fatalf("api.jsonl has %d lines, want 2:\n%q", len(lines), lines)
		}
		for _, ln := range lines {
			var e APILogEntry
			if err := json.Unmarshal([]byte(ln), &e); err != nil {
				t.Fatalf("api line is not valid APILogEntry JSON: %v (%q)", err, ln)
			}
		}
		if rawLogging {
			for _, ln := range readLines(t, rawPath) {
				var e APIRawLogEntry
				if err := json.Unmarshal([]byte(ln), &e); err != nil {
					t.Fatalf("raw line is not valid APIRawLogEntry JSON: %v (%q)", err, ln)
				}
			}
		}
	})
}

// buildStreamNext returns a StreamFunc whose behavior the selector chooses, each
// branch producing exactly one api.jsonl line:
//
//	0: a stream that finishes -> logFinish writes one entry.
//	1: next returns an error  -> WrapStream writes one error entry.
//	2: next records an adapter attempt -> writeAdapterAttempt writes one entry,
//	   and the later FINISH is suppressed because an attempt was recorded.
func buildStreamNext(sel uint8, resp Response, req Request) StreamFunc {
	switch sel % 3 {
	case 1:
		return func(_ context.Context, _ Request) (Stream, error) {
			return nil, NewStreamErrorWithRawBodies("openai", "stream boom", nil, "EQ", "ES")
		}
	case 2:
		return func(ctx context.Context, r Request) (Stream, error) {
			rec := AdapterAttemptRecord{
				Request:         r,
				Response:        &resp,
				EndpointURL:     "https://api.example/v1/responses",
				RawRequestBody:  "AQ",
				RawResponseBody: "AS",
			}
			RecordAdapterAttempt(ctx, rec)
			return newSliceStream(StreamEvent{Type: StreamEventFinish, Response: &resp}), nil
		}
	default:
		return func(_ context.Context, _ Request) (Stream, error) {
			return newSliceStream(StreamEvent{Type: StreamEventFinish, Response: &resp}), nil
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if ln := sc.Text(); ln != "" {
			lines = append(lines, ln)
		}
	}
	return lines
}
