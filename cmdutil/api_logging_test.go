package cmdutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
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

	apiPath := filepath.Join(dir, "sessions", "sess-1.api.jsonl")
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
