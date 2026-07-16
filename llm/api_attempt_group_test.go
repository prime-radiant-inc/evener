package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
	apilog "primeradiant.com/serf/llm/apilog"
)

type recordingAPIAttemptSink struct {
	mu                 sync.Mutex
	attempts           []apilog.APIAttemptRecord
	settlements        []apilog.APIAttemptGroupSettlement
	events             []string
	failAttempt        error
	failSettlement     error
	failureObserverFn  func(APILogFailure)
	attemptContextErrs []error
	settleContextErrs  []error
}

func (s *recordingAPIAttemptSink) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "append_attempt")
	s.attemptContextErrs = append(s.attemptContextErrs, ctx.Err())
	if s.failAttempt != nil {
		return s.failAttempt
	}
	s.attempts = append(s.attempts, rec)
	return nil
}

func (s *recordingAPIAttemptSink) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "append_settlement")
	s.settleContextErrs = append(s.settleContextErrs, ctx.Err())
	if s.failSettlement != nil {
		return s.failSettlement
	}
	s.settlements = append(s.settlements, rec)
	return nil
}

func (s *recordingAPIAttemptSink) apiLogFailureObserver() func(APILogFailure) {
	return s.failureObserverFn
}

func (s *recordingAPIAttemptSink) appendEvent(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingAPIAttemptSink) snapshot() (attempts []apilog.APIAttemptRecord, settlements []apilog.APIAttemptGroupSettlement, events []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(attempts, s.attempts...), append(settlements, s.settlements...), append(events, s.events...)
}

func (s *recordingAPIAttemptSink) contextErrors() (attempts, settlements []error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(attempts, s.attemptContextErrs...), append(settlements, s.settleContextErrs...)
}

func testAPIAttemptMeta(startedAt time.Time) APIAttemptMeta {
	return APIAttemptMeta{
		ProviderInstance: "primary",
		RequestModel:     "model-a",
		HistoryMode:      HistoryModeFullHistory,
		EndpointFamily:   "responses",
		Method:           http.MethodPost,
		Endpoint:         "https://provider.invalid/v1/responses",
		Headers:          http.Header{"Content-Type": []string{"application/json"}},
		RequestBody:      []byte(`{"input":"hello"}`),
		StartedAt:        startedAt,
	}
}

func testAPIAttemptResult(finishedAt time.Time, outcome apilog.AttemptOutcomeClass, err error) APIAttemptResult {
	resp := &Response{
		ID:       "resp-1",
		Model:    "model-a-2026",
		Provider: "primary",
		Message:  Assistant("hello"),
		Finish:   FinishReason{Reason: FinishReasonStop},
		Usage:    Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
	}
	if outcome != apilog.AttemptSuccess {
		resp = nil
	}
	return APIAttemptResult{
		StatusCode:   http.StatusOK,
		ResponseBody: []byte(`{"output":"hello"}`),
		Response:     resp,
		Outcome:      outcome,
		ErrorClass:   "provider",
		Err:          err,
		FinishedAt:   finishedAt,
	}
}

func TestAPIAttemptGroupAppendsAttemptsThenOneSettlement(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_group")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	first := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	first.Complete(testAPIAttemptResult(startedAt.Add(15*time.Millisecond), apilog.AttemptTransportFail, errors.New("temporary")))
	first.Complete(testAPIAttemptResult(startedAt.Add(20*time.Millisecond), apilog.AttemptSuccess, nil))
	second := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt.Add(time.Second)))
	second.Complete(testAPIAttemptResult(startedAt.Add(time.Second+25*time.Millisecond), apilog.AttemptSuccess, nil))
	group.Settle(ctx, apilog.AttemptSuccess)
	group.Settle(ctx, apilog.AttemptProviderReject)

	attempts, settlements, events := sink.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(attempts))
	}
	if len(settlements) != 1 {
		t.Fatalf("settlement count = %d, want 1", len(settlements))
	}
	if got := []int{attempts[0].AttemptIndex, attempts[1].AttemptIndex}; !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("attempt indexes = %v, want [1 2]", got)
	}
	for i, attempt := range attempts {
		if err := identifier.ValidateAPIAttemptID(attempt.AttemptID); err != nil {
			t.Fatalf("attempt %d ID %q: %v", i+1, attempt.AttemptID, err)
		}
		if attempt.AttemptGroupID != group.ID {
			t.Fatalf("attempt %d group = %q, want %q", i+1, attempt.AttemptGroupID, group.ID)
		}
	}
	if attempts[0].AttemptID == attempts[1].AttemptID {
		t.Fatalf("attempt IDs are not unique: %q", attempts[0].AttemptID)
	}
	settlement := settlements[0]
	if settlement.FinalAttemptID != attempts[1].AttemptID || settlement.FinalAttemptCount != 2 || settlement.Outcome != apilog.AttemptSuccess {
		t.Fatalf("settlement = %+v, want final attempt 2 success", settlement)
	}
	if settlement.ForensicIncomplete {
		t.Fatal("successful attempt appends marked settlement forensic-incomplete")
	}
	if want := []string{"append_attempt", "append_attempt", "append_settlement"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("append order = %v, want %v", events, want)
	}
	if attempts[0].Request.HistoryMode != string(HistoryModeFullHistory) || attempts[0].LatencyMS != 15 {
		t.Fatalf("first attempt metadata = %+v", attempts[0])
	}
	requestBody, err := apilog.DecodeBody(attempts[0].Request.Body)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if !reflect.DeepEqual(requestBody, []byte(`{"input":"hello"}`)) {
		t.Fatalf("request body = %q", requestBody)
	}
}

func TestAPIAttemptCompleteWaitsForSynchronousAppend(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	settlementCalled := make(chan struct{})
	sink := APIAttemptSinkFunc{
		Attempt: func(context.Context, apilog.APIAttemptRecord) error {
			close(entered)
			<-release
			return nil
		},
		Settlement: func(context.Context, apilog.APIAttemptGroupSettlement) error {
			close(settlementCalled)
			return nil
		},
	}
	group := NewAPIAttemptGroup("ag_sync")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	done := make(chan struct{})
	go func() {
		attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
		close(done)
	}()
	<-entered
	settleStarted := make(chan struct{})
	settleDone := make(chan struct{})
	go func() {
		close(settleStarted)
		group.Settle(ctx, apilog.AttemptSuccess)
		close(settleDone)
	}()
	<-settleStarted
	select {
	case <-done:
		t.Fatal("Complete returned before AppendAttempt finished")
	default:
	}
	select {
	case <-settlementCalled:
		t.Fatal("settlement append overtook admitted attempt append")
	default:
	}
	close(release)
	<-done
	<-settleDone
	select {
	case <-settlementCalled:
	default:
		t.Fatal("settlement was not appended after the attempt completed")
	}
}

func TestAPIAttemptCompleteAndSettleAreExactlyOnceUnderConcurrency(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_exactly_once")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	result := testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil)

	var completeWG sync.WaitGroup
	for range 8 {
		completeWG.Add(1)
		go func() {
			defer completeWG.Done()
			attempt.Complete(result)
		}()
	}
	completeWG.Wait()

	var settleWG sync.WaitGroup
	for range 8 {
		settleWG.Add(1)
		go func() {
			defer settleWG.Done()
			group.Settle(ctx, apilog.AttemptSuccess)
		}()
	}
	settleWG.Wait()

	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 1 || len(settlements) != 1 {
		t.Fatalf("attempts/settlements = %d/%d, want 1/1", len(attempts), len(settlements))
	}
}

func TestAPIAttemptGroupZeroAttemptCancellationSettlement(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_cancel_before_transport")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	group.Settle(ctx, apilog.AttemptCallerCancel)

	attempts, settlements, events := sink.snapshot()
	if len(attempts) != 0 || len(settlements) != 1 {
		t.Fatalf("attempts/settlements = %d/%d, want 0/1", len(attempts), len(settlements))
	}
	got := settlements[0]
	if got.FinalAttemptCount != 0 || got.FinalAttemptID != "" || got.Outcome != apilog.AttemptCallerCancel {
		t.Fatalf("zero-attempt settlement = %+v", got)
	}
	if !reflect.DeepEqual(events, []string{"append_settlement"}) {
		t.Fatalf("events = %v", events)
	}
}

func TestCancellationDuringAPIAttemptAppendsBeforeSettlement(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_cancel_during_transport")
	base, cancel := context.WithCancel(context.Background())
	cancel()
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(base, group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptCallerCancel, context.Canceled))
	group.Settle(ctx, apilog.AttemptCallerCancel)

	attempts, settlements, events := sink.snapshot()
	if len(attempts) != 1 || attempts[0].Outcome != apilog.AttemptCallerCancel {
		t.Fatalf("attempts = %+v", attempts)
	}
	if len(settlements) != 1 || settlements[0].FinalAttemptID != attempts[0].AttemptID {
		t.Fatalf("settlements = %+v", settlements)
	}
	if !reflect.DeepEqual(events, []string{"append_attempt", "append_settlement"}) {
		t.Fatalf("events = %v", events)
	}
	attemptContextErrs, settlementContextErrs := sink.contextErrors()
	if !reflect.DeepEqual(attemptContextErrs, []error{nil}) || !reflect.DeepEqual(settlementContextErrs, []error{nil}) {
		t.Fatalf("durability contexts carried cancellation: attempts=%v settlements=%v", attemptContextErrs, settlementContextErrs)
	}
}

func TestAPILogStorageFailurePreservesProviderResultAndMarksSettlement(t *testing.T) {
	storageErr := errors.New("disk full")
	sink := &recordingAPIAttemptSink{failAttempt: storageErr}
	var failures []APILogFailure
	sink.failureObserverFn = func(failure APILogFailure) {
		sink.appendEvent("observe_failure")
		failures = append(failures, failure)
	}
	group := NewAPIAttemptGroup("ag_storage_failure")
	ctx := WithAPILogContext(context.Background(), "sess-storage", 1)
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), sink)
	providerErr := errors.New("provider rejection")
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	callProvider := func() (Response, error) {
		attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
		resp := Response{ID: "provider-response-identity", Model: "model-a"}
		attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptProviderReject, providerErr))
		sink.appendEvent("provider_return")
		return resp, providerErr
	}
	gotResp, gotErr := callProvider()
	if gotResp.ID != "provider-response-identity" || gotErr != providerErr {
		t.Fatalf("provider result changed by storage failure: (%+v, %v)", gotResp, gotErr)
	}
	group.Settle(ctx, apilog.AttemptProviderReject)

	_, settlements, events := sink.snapshot()
	if len(failures) != 1 {
		t.Fatalf("failure observer calls = %d, want 1", len(failures))
	}
	failure := failures[0]
	if failure.Operation != "append_attempt" || failure.SessionID != "sess-storage" || failure.AttemptGroupID != group.ID || failure.AttemptID == "" || !errors.Is(failure.Err, storageErr) {
		t.Fatalf("failure = %+v", failure)
	}
	if len(settlements) != 1 || !settlements[0].ForensicIncomplete {
		t.Fatalf("settlements = %+v, want forensic-incomplete", settlements)
	}
	wantEvents := []string{"append_attempt", "observe_failure", "provider_return", "append_settlement"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
}

func TestAPIAttemptSettlementFailureObservedExactlyOnce(t *testing.T) {
	storageErr := errors.New("settlement sync failed")
	sink := &recordingAPIAttemptSink{failSettlement: storageErr}
	var failures []APILogFailure
	sink.failureObserverFn = func(failure APILogFailure) {
		sink.appendEvent("observe_failure")
		failures = append(failures, failure)
	}
	group := NewAPIAttemptGroup("ag_settlement_failure")
	ctx := WithAPILogContext(context.Background(), "sess-settlement", 1)
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), sink)
	group.Settle(ctx, apilog.AttemptCallerCancel)
	group.Settle(ctx, apilog.AttemptSuccess)

	_, settlements, events := sink.snapshot()
	if len(settlements) != 0 {
		t.Fatalf("persisted settlements = %d, want 0", len(settlements))
	}
	if len(failures) != 1 {
		t.Fatalf("failure observer calls = %d, want 1", len(failures))
	}
	if failure := failures[0]; failure.Operation != "append_settlement" || failure.AttemptID != "" || !errors.Is(failure.Err, storageErr) {
		t.Fatalf("failure = %+v", failure)
	}
	if want := []string{"append_settlement", "observe_failure"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestAPILoggerCanonicalCrashWindowLeavesDecodableUnsettledAttempt(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	group := NewAPIAttemptGroup("ag_crash_window")
	ctx := WithAPILogContext(context.Background(), "sess-crash", 1)
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(ctx, group), logger)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(filepath.Join(stateDir, "sessions", "sess-crash.api.jsonl"))
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	decoder := apilog.NewDecoder(f, 1<<20)
	if record, err := decoder.Next(); err != nil {
		t.Fatalf("decode attempt: %v", err)
	} else if _, ok := record.(apilog.APIAttemptRecord); !ok {
		t.Fatalf("record type = %T, want APIAttemptRecord", record)
	}
	if record, err := decoder.Next(); !errors.Is(err, io.EOF) || record != nil {
		t.Fatalf("record after crash window = (%T, %v), want clean EOF without settlement", record, err)
	}
}

type APIAttemptSinkFunc struct {
	Attempt    func(context.Context, apilog.APIAttemptRecord) error
	Settlement func(context.Context, apilog.APIAttemptGroupSettlement) error
}

func (f APIAttemptSinkFunc) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	if f.Attempt == nil {
		return nil
	}
	return f.Attempt(ctx, rec)
}

func (f APIAttemptSinkFunc) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error {
	if f.Settlement == nil {
		return nil
	}
	return f.Settlement(ctx, rec)
}
