package cmdutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
)

type loggingTestAdapter struct{}

func (loggingTestAdapter) Name() string { return "test" }

func (loggingTestAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
		ProviderInstance: "test", RequestModel: req.Model, Method: http.MethodPost,
		Endpoint: "https://example.test/v1/responses", RequestBody: []byte(`{"input":"hi"}`), StartedAt: startedAt,
	})
	resp := llm.Response{
		Provider: req.Provider, Model: req.Model, Message: llm.Assistant("ok"),
		Finish: llm.FinishReason{Reason: llm.FinishReasonStop},
		Usage:  llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}
	attempt.Complete(llm.APIAttemptResult{
		StatusCode: http.StatusOK, ResponseBody: []byte(`{"output":"ok"}`), Response: &resp,
		Outcome: apilog.AttemptSuccess, FinishedAt: startedAt.Add(time.Millisecond),
	})
	return resp, nil
}

func (loggingTestAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func TestAttachAPILoggerWritesAPIJSONL(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	closeLog, err := AttachAPILogger(client, dir, nil)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}
	apiPath := filepath.Join(dir, "sessions", "sess-1.api.jsonl")
	if _, err := os.Stat(apiPath); !os.IsNotExist(err) {
		t.Fatalf("fresh session API log opened before first append: %v", err)
	}

	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "sess-1"), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}

	f, err := os.Open(apiPath)
	if err != nil {
		t.Fatalf("open sessions/sess-1.api.jsonl: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	first, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode attempt: %v", err)
	}
	attempt, ok := first.(apilog.APIAttemptRecord)
	if !ok || attempt.Request.Endpoint != "https://example.test/v1/responses" || attempt.Request.Body.Data != `{"input":"hi"}` {
		t.Fatalf("attempt = %+v", first)
	}
	second, err := decoder.Next()
	if err != nil {
		t.Fatalf("decode settlement: %v", err)
	}
	settlement, ok := second.(apilog.APIAttemptGroupSettlement)
	if !ok || settlement.AttemptGroupID != attempt.AttemptGroupID || settlement.FinalAttemptCount != 1 {
		t.Fatalf("settlement = %+v", second)
	}
	if tail, err := decoder.Next(); tail != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("tail = (%T, %v)", tail, err)
	}
	// The project-level api.jsonl is frozen: never written by new sessions.
	if _, err := os.Stat(filepath.Join(dir, "api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("project-level api.jsonl was written (stat err=%v)", err)
	}
}

func TestAttachAPILoggerRejectsRunningResumedSession(t *testing.T) {
	stateDir := t.TempDir()
	const sessionID = "sess-running"
	apiPath := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
	owner, err := llm.NewAPILogger(apiPath)
	if err != nil {
		t.Fatalf("NewAPILogger owner: %v", err)
	}
	defer owner.Close() //nolint:errcheck

	closeLog, err := AttachAPILogger(llm.NewClient(), stateDir, nil, sessionID)
	if err == nil {
		_ = closeLog()
		t.Fatal("AttachAPILogger acquired a running resumed session")
	}
	if closeLog != nil {
		t.Fatal("AttachAPILogger returned a closer after reservation failed")
	}
	for _, guidance := range []string{"already running", "send work", "fork"} {
		if !strings.Contains(err.Error(), guidance) {
			t.Fatalf("reservation error = %q, want %q guidance", err, guidance)
		}
	}
}

// TestAttachSessionAPILoggerOwnershipOnlyDiscardsRecords pins the default-off
// attach contract: with recordAttempts=false the per-session file is still
// created (and locked) by the reserve boundary, but a completed provider call
// writes no records to it.
func TestAttachSessionAPILoggerOwnershipOnlyDiscardsRecords(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	reserve, closeLog, err := AttachSessionAPILogger(client, dir, nil, false)
	if err != nil {
		t.Fatalf("AttachSessionAPILogger: %v", err)
	}
	const sessionID = "sess-1"
	if err := reserve(sessionID); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	apiPath := filepath.Join(dir, "sessions", sessionID+".api.jsonl")

	if _, err := client.Complete(llm.WithAPILogContext(context.Background(), sessionID), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}

	info, err := os.Stat(apiPath)
	if err != nil {
		t.Fatalf("ownership API log not created by reserve: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("ownership API log size = %d, want 0 (records must be discarded)", info.Size())
	}
}
