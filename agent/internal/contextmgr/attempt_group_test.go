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

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	apilog "primeradiant.com/evener/llm/apilog"
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
	cm := NewManager(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001"), client, cheapmodel.New(client))

	got, err := cm.ElicitNote(context.Background(), []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("preserve this")},
	}, nil)
	if err != nil || got != "- survived" {
		t.Fatalf("ElicitNote = (%q, %v), want fallback success", got, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close API logger: %v", err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), 2, apilog.AttemptSuccess)
}

func TestSummarizeWithLLMOwnsOneLogicalAttemptGroupWhenAllRoutesFail(t *testing.T) {
	cheapErr := llm.ErrorFromHTTPStatus("anthropic", http.StatusBadRequest, "cheap unavailable", nil, nil)
	activeErr := llm.ErrorFromHTTPStatus("openai", http.StatusBadRequest, "active unavailable", nil, nil)
	client, logger, path := contextManagerAttemptClient(t,
		&contextManagerAttemptAdapter{name: "anthropic", err: cheapErr},
		&contextManagerAttemptAdapter{name: "openai", err: activeErr},
	)
	cm := NewManager(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001"), client, cheapmodel.New(client))
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("task")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("old")},
		{Kind: schema.TurnAssistant, Message: llm.Assistant("recent")},
	}

	_, gotErr := cm.summarizeFold(context.Background(), history, 1, "", nil)
	if !errors.Is(gotErr, activeErr) {
		t.Fatalf("summarizeFold error = %v, want final provider error %v", gotErr, activeErr)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close API logger: %v", err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), 2, apilog.AttemptProviderReject)
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

func assertContextManagerLogicalGroup(t *testing.T, records []apilog.APILogRecord, attempts int, wantOutcome apilog.AttemptOutcomeClass) {
	t.Helper()
	if len(records) != attempts+1 {
		t.Fatalf("record count = %d, want %d (%d attempts and one settlement)", len(records), attempts+1, attempts)
	}
	groupID := ""
	lastAttemptID := ""
	for i, record := range records[:attempts] {
		attempt, ok := record.(apilog.APIAttemptRecord)
		if !ok {
			t.Fatalf("record %d = %T, want APIAttemptRecord", i+1, record)
		}
		if attempt.AttemptGroupID == "" || (groupID != "" && attempt.AttemptGroupID != groupID) {
			t.Fatalf("group IDs = (%q, %q), want one non-empty group", groupID, attempt.AttemptGroupID)
		}
		groupID = attempt.AttemptGroupID
		if attempt.AttemptIndex != i+1 {
			t.Fatalf("attempt indexes = %d at position %d, want sequential from 1", attempt.AttemptIndex, i+1)
		}
		lastAttemptID = attempt.AttemptID
	}
	settlement, ok := records[attempts].(apilog.APIAttemptGroupSettlement)
	if !ok {
		t.Fatalf("record %d = %T, want APIAttemptGroupSettlement", attempts+1, records[attempts])
	}
	if settlement.AttemptGroupID != groupID {
		t.Fatalf("group IDs = (%q, %q), want one non-empty group", groupID, settlement.AttemptGroupID)
	}
	if settlement.FinalAttemptID != lastAttemptID || settlement.FinalAttemptCount != attempts || settlement.Outcome != wantOutcome {
		t.Fatalf("settlement = %+v, want final attempt %q, count %d, outcome %q", settlement, lastAttemptID, attempts, wantOutcome)
	}
}
