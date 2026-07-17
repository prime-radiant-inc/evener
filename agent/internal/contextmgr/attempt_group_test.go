package contextmgr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
	apilog "primeradiant.com/serf/llm/apilog"
)

type contextManagerAttemptAdapter struct {
	name string
	err  error
	text string
}

func (a *contextManagerAttemptAdapter) Name() string { return a.name }

func (a *contextManagerAttemptAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	response := llm.Response{
		Provider: a.name,
		Model:    req.Model,
		Message:  llm.Assistant(a.text),
		Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
	}
	attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
		ProviderInstance: a.name,
		RequestModel:     req.Model,
		Method:           http.MethodPost,
		Endpoint:         "https://" + a.name + ".invalid/v1/complete",
		RequestBody:      []byte(`{"input":"summary"}`),
		StartedAt:        startedAt,
	})
	result := llm.APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"summary"}`),
		Response:     &response,
		Outcome:      apilog.AttemptSuccess,
		FinishedAt:   startedAt.Add(time.Millisecond),
	}
	if a.err != nil {
		result.StatusCode = http.StatusBadRequest
		result.ResponseBody = []byte(`{"error":"unavailable"}`)
		result.Response = nil
		result.Outcome = apilog.AttemptProviderReject
		result.ErrorClass = "permanent"
		result.Err = a.err
	}
	attempt.Complete(result)
	return response, a.err
}

func (*contextManagerAttemptAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func TestElicitNoteOwnsOneLogicalAttemptGroupAcrossRouteFallback(t *testing.T) {
	cheapErr := llm.ErrorFromHTTPStatus("anthropic", http.StatusBadRequest, "cheap unavailable", nil, nil)
	client, logger, path := contextManagerAttemptClient(t,
		&contextManagerAttemptAdapter{name: "anthropic", err: cheapErr},
		&contextManagerAttemptAdapter{name: "openai", text: "- survived"},
	)
	cm := NewManager(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001"), client)

	got, err := cm.ElicitNote(context.Background(), []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("preserve this")},
	})
	if err != nil || got != "- survived" {
		t.Fatalf("ElicitNote = (%q, %v), want fallback success", got, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close API logger: %v", err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), apilog.AttemptSuccess)
}

func TestSummarizeWithLLMOwnsOneLogicalAttemptGroupWhenAllRoutesFail(t *testing.T) {
	cheapErr := llm.ErrorFromHTTPStatus("anthropic", http.StatusBadRequest, "cheap unavailable", nil, nil)
	activeErr := llm.ErrorFromHTTPStatus("openai", http.StatusBadRequest, "active unavailable", nil, nil)
	client, logger, path := contextManagerAttemptClient(t,
		&contextManagerAttemptAdapter{name: "anthropic", err: cheapErr},
		&contextManagerAttemptAdapter{name: "openai", err: activeErr},
	)
	cm := NewManager(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001"), client)
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	_, gotErr := cm.summarizeWithLLM(context.Background(), history, 1)
	if gotErr != activeErr {
		t.Fatalf("summarizeWithLLM error = %v, want final provider error %v", gotErr, activeErr)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close API logger: %v", err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), apilog.AttemptProviderReject)
}

func contextManagerAttemptClient(t *testing.T, adapters ...llm.ProviderAdapter) (*llm.Client, *llm.APILogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := llm.NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	client := llm.NewClient()
	for _, adapter := range adapters {
		client.Register(adapter)
	}
	client.Use(logger)
	return client, logger, path
}

func readContextManagerAttemptRecords(t *testing.T, path string) []apilog.APILogRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open API log: %v", err)
	}
	defer file.Close()

	decoder := apilog.NewDecoder(file, 4<<20)
	var records []apilog.APILogRecord
	for {
		record, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("decode API log: %v", err)
		}
		records = append(records, record)
	}
}

func assertContextManagerLogicalGroup(t *testing.T, records []apilog.APILogRecord, wantOutcome apilog.AttemptOutcomeClass) {
	t.Helper()
	if len(records) != 3 {
		t.Fatalf("record count = %d, want 3 (two attempts and one settlement): %#v", len(records), records)
	}
	first, ok := records[0].(apilog.APIAttemptRecord)
	if !ok {
		t.Fatalf("record 1 = %T, want APIAttemptRecord", records[0])
	}
	second, ok := records[1].(apilog.APIAttemptRecord)
	if !ok {
		t.Fatalf("record 2 = %T, want APIAttemptRecord", records[1])
	}
	settlement, ok := records[2].(apilog.APIAttemptGroupSettlement)
	if !ok {
		t.Fatalf("record 3 = %T, want APIAttemptGroupSettlement", records[2])
	}
	if first.AttemptGroupID == "" || second.AttemptGroupID != first.AttemptGroupID || settlement.AttemptGroupID != first.AttemptGroupID {
		t.Fatalf("group IDs = (%q, %q, %q), want one non-empty group", first.AttemptGroupID, second.AttemptGroupID, settlement.AttemptGroupID)
	}
	if first.AttemptIndex != 1 || second.AttemptIndex != 2 {
		t.Fatalf("attempt indexes = (%d, %d), want (1, 2)", first.AttemptIndex, second.AttemptIndex)
	}
	if settlement.FinalAttemptID != second.AttemptID || settlement.FinalAttemptCount != 2 || settlement.Outcome != wantOutcome {
		t.Fatalf("settlement = %+v, want final attempt %q, count 2, outcome %q", settlement, second.AttemptID, wantOutcome)
	}
}
