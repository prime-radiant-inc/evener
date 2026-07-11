package llm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionLoggerComplete(t *testing.T, logger *APILogger, sessionID string, round int) {
	t.Helper()
	next := func(ctx context.Context, req Request) (Response, error) {
		return Response{
			Provider:        req.Provider,
			Model:           req.Model,
			Message:         Assistant("ok"),
			Finish:          FinishReason{Reason: FinishReasonStop},
			Usage:           Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
			RawRequestBody:  `{"input":"hi"}`,
			RawResponseBody: `{"output":"ok"}`,
		}, nil
	}
	ctx := context.Background()
	if sessionID != "" || round != 0 {
		ctx = WithAPILogContext(ctx, sessionID, round)
	}
	if _, err := logger.WrapComplete(next)(ctx, Request{Provider: "test", Model: "m", Messages: []Message{User("hi")}}); err != nil {
		t.Fatalf("WrapComplete: %v", err)
	}
}

func TestSessionAPILoggerRoutesEntriesBySessionID(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}

	sessionLoggerComplete(t, logger, "01SESSA", 1)
	sessionLoggerComplete(t, logger, "01SESSB", 2)
	sessionLoggerComplete(t, logger, "01SESSA", 3)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dataA, err := os.ReadFile(filepath.Join(dir, "sessions", "01SESSA.api.jsonl"))
	if err != nil {
		t.Fatalf("read session A log: %v", err)
	}
	if got := strings.Count(string(dataA), "\n"); got != 2 {
		t.Fatalf("session A line count = %d, want 2:\n%s", got, dataA)
	}
	for _, want := range []string{`"session_id":"01SESSA"`, `"round":1`, `"round":3`} {
		if !strings.Contains(string(dataA), want) {
			t.Fatalf("session A log missing %s:\n%s", want, dataA)
		}
	}
	dataB, err := os.ReadFile(filepath.Join(dir, "sessions", "01SESSB.api.jsonl"))
	if err != nil {
		t.Fatalf("read session B log: %v", err)
	}
	if !strings.Contains(string(dataB), `"round":2`) {
		t.Fatalf("session B log missing round 2:\n%s", dataB)
	}
	if strings.Contains(string(dataB), "01SESSA") {
		t.Fatalf("session B log contains session A entries:\n%s", dataB)
	}
	// The frozen project-level file must never be created.
	if _, err := os.Stat(filepath.Join(dir, "api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("project-level api.jsonl was written (stat err=%v)", err)
	}
}

func TestSessionAPILoggerRoutesUnattributedEntries(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	sessionLoggerComplete(t, logger, "", 0)
	// A session id that is not a safe filename component must not escape the
	// sessions dir; it routes to the unattributed bucket.
	sessionLoggerComplete(t, logger, "../evil", 9)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sessions", "unattributed.api.jsonl"))
	if err != nil {
		t.Fatalf("read unattributed log: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Fatalf("unattributed line count = %d, want 2:\n%s", got, data)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("path-traversal session id escaped the sessions dir")
	}
}

func TestSessionAPILoggerRawRoutesBySessionID(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewSessionAPILogger(dir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	logger.EnableSessionRawLogging()
	sessionLoggerComplete(t, logger, "01SESSR", 5)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sessions", "01SESSR.api-raw.jsonl"))
	if err != nil {
		t.Fatalf("read session raw log: %v", err)
	}
	for _, want := range []string{`"session_id":"01SESSR"`, `"request_body":"{\"input\":\"hi\"}"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("session raw log missing %s:\n%s", want, data)
		}
	}
}
