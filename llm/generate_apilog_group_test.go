package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	apilog "primeradiant.com/serf/llm/apilog"
)

type logicalGroupScriptedAdapter struct {
	mu      sync.Mutex
	results []error
	calls   int
}

func (*logicalGroupScriptedAdapter) Name() string { return "scripted" }

func (a *logicalGroupScriptedAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err, startedAt := a.next(req)
	attempt := BeginAPIAttempt(ctx, logicalGroupAttemptMeta(req, startedAt))
	attempt.Complete(logicalGroupAttemptResult(resp, err, startedAt))
	return resp, err
}

func (a *logicalGroupScriptedAdapter) Stream(ctx context.Context, req Request) (Stream, error) {
	resp, err, startedAt := a.next(req)
	attempt := BeginAPIAttempt(ctx, logicalGroupAttemptMeta(req, startedAt))
	attempt.Complete(logicalGroupAttemptResult(resp, err, startedAt))
	if err != nil {
		return nil, err
	}

	_, cancel := context.WithCancel(ctx)
	stream := NewChanStream(cancel)
	go func() {
		defer stream.CloseSend()
		stream.Send(StreamEvent{Type: StreamEventStreamStart})
		response := resp
		stream.Send(StreamEvent{Type: StreamEventFinish, FinishReason: &response.Finish, Response: &response})
		cancel()
	}()
	return stream, nil
}

func (a *logicalGroupScriptedAdapter) next(req Request) (Response, error, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	index := a.calls
	a.calls++
	var err error
	if index < len(a.results) {
		err = a.results[index]
	}
	resp := Response{
		Provider: "scripted",
		Model:    req.Model,
		Message:  Assistant("ok"),
		Finish:   FinishReason{Reason: FinishReasonStop},
	}
	return resp, err, time.Unix(1_700_000_000+int64(index), 0).UTC()
}

func logicalGroupAttemptMeta(req Request, startedAt time.Time) APIAttemptMeta {
	return APIAttemptMeta{
		ProviderInstance: "scripted",
		RequestModel:     req.Model,
		Method:           http.MethodPost,
		Endpoint:         "https://scripted.invalid/v1/complete",
		RequestBody:      []byte(`{"input":"test"}`),
		StartedAt:        startedAt,
	}
}

func logicalGroupAttemptResult(resp Response, err error, startedAt time.Time) APIAttemptResult {
	result := APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"ok"}`),
		Response:     &resp,
		Outcome:      apilog.AttemptSuccess,
		FinishedAt:   startedAt.Add(time.Millisecond),
	}
	if err != nil {
		result.StatusCode = http.StatusInternalServerError
		result.ResponseBody = []byte(`{"error":"retry"}`)
		result.Response = nil
		result.Outcome = apilog.AttemptProviderReject
		result.ErrorClass = "retryable"
		result.Err = err
	}
	return result
}

func TestGenerateOwnsOneLogicalAttemptGroupAcrossRetries(t *testing.T) {
	tests := []struct {
		name        string
		succeedLast bool
		wantOutcome apilog.AttemptOutcomeClass
	}{
		{name: "failure then success", succeedLast: true, wantOutcome: apilog.AttemptSuccess},
		{name: "all failures", wantOutcome: apilog.AttemptProviderReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstErr := ErrorFromHTTPStatus("scripted", http.StatusInternalServerError, "first", nil, nil)
			lastErr := ErrorFromHTTPStatus("scripted", http.StatusInternalServerError, "last", nil, nil)
			results := []error{firstErr, lastErr}
			if tt.succeedLast {
				results[1] = nil
			}
			client, logger, path := logicalGroupTestClient(t, results)
			prompt := "test"
			_, gotErr := Generate(context.Background(), GenerateOptions{
				Client: client, Model: "model-a", Provider: "scripted", Prompt: &prompt,
				RetryPolicy: logicalGroupRetryPolicy(), Sleep: logicalGroupNoSleep,
			})
			if tt.succeedLast && gotErr != nil {
				t.Fatalf("Generate error = %v, want nil", gotErr)
			}
			if !tt.succeedLast && gotErr != lastErr {
				t.Fatalf("Generate error = %v, want final provider error %v", gotErr, lastErr)
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("Close API logger: %v", err)
			}
			assertLogicalGroupRecords(t, readLogicalGroupRecords(t, path), tt.wantOutcome)
		})
	}
}

func TestStreamGenerateOwnsOneLogicalAttemptGroupAcrossRetries(t *testing.T) {
	tests := []struct {
		name        string
		succeedLast bool
		wantOutcome apilog.AttemptOutcomeClass
	}{
		{name: "failure then success", succeedLast: true, wantOutcome: apilog.AttemptSuccess},
		{name: "all failures", wantOutcome: apilog.AttemptProviderReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			firstErr := ErrorFromHTTPStatus("scripted", http.StatusInternalServerError, "first", nil, nil)
			lastErr := ErrorFromHTTPStatus("scripted", http.StatusInternalServerError, "last", nil, nil)
			results := []error{firstErr, lastErr}
			if tt.succeedLast {
				results[1] = nil
			}
			client, logger, path := logicalGroupTestClient(t, results)
			prompt := "test"
			stream, err := StreamGenerate(context.Background(), GenerateOptions{
				Client: client, Model: "model-a", Provider: "scripted", Prompt: &prompt,
				RetryPolicy: logicalGroupRetryPolicy(), Sleep: logicalGroupNoSleep,
			})
			if err != nil {
				t.Fatalf("StreamGenerate: %v", err)
			}
			_, gotErr := stream.Response()
			if tt.succeedLast && gotErr != nil {
				t.Fatalf("StreamGenerate response error = %v, want nil", gotErr)
			}
			if !tt.succeedLast && gotErr != lastErr {
				t.Fatalf("StreamGenerate response error = %v, want final provider error %v", gotErr, lastErr)
			}
			if err := logger.Close(); err != nil {
				t.Fatalf("Close API logger: %v", err)
			}
			assertLogicalGroupRecords(t, readLogicalGroupRecords(t, path), tt.wantOutcome)
		})
	}
}

func TestAPIAttemptGroupScopeReusesCallerGroupWithoutSettlingIt(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_caller_owned")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)

	scopedCtx, scope := BeginAPIAttemptGroupScope(ctx)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(scopedCtx, logicalGroupAttemptMeta(Request{Model: "model-a"}, startedAt)).Complete(
		logicalGroupAttemptResult(Response{Model: "model-a", Message: Assistant("ok")}, nil, startedAt),
	)
	scope.SettleResult(nil)

	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 1 || attempts[0].AttemptGroupID != group.ID {
		t.Fatalf("attempts = %+v, want one attempt in caller group %q", attempts, group.ID)
	}
	if len(settlements) != 0 {
		t.Fatalf("scope settlements = %+v, want caller-owned group left unsettled", settlements)
	}

	group.SettleResult(scopedCtx, nil)
	_, settlements, _ = sink.snapshot()
	if len(settlements) != 1 || settlements[0].AttemptGroupID != group.ID {
		t.Fatalf("caller settlement = %+v, want one settlement for %q", settlements, group.ID)
	}
}

func logicalGroupTestClient(t *testing.T, results []error) (*Client, *APILogger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.jsonl")
	logger, err := NewAPILogger(path)
	if err != nil {
		t.Fatalf("NewAPILogger: %v", err)
	}
	client := NewClient()
	client.Register(&logicalGroupScriptedAdapter{results: results})
	client.Use(logger)
	return client, logger, path
}

func logicalGroupRetryPolicy() *RetryPolicy {
	return &RetryPolicy{MaxRetries: 1, BaseDelay: 0, MaxDelay: 0, BackoffMultiplier: 2}
}

func logicalGroupNoSleep(context.Context, time.Duration) error { return nil }

func readLogicalGroupRecords(t *testing.T, path string) []apilog.APILogRecord {
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

func assertLogicalGroupRecords(t *testing.T, records []apilog.APILogRecord, wantOutcome apilog.AttemptOutcomeClass) {
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
