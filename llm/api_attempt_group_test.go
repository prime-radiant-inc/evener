package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	attemptEntered     chan struct{}
	attemptRelease     <-chan struct{}
	attemptEnterOnce   sync.Once
}

func (s *recordingAPIAttemptSink) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "append_attempt")
	s.attemptContextErrs = append(s.attemptContextErrs, ctx.Err())
	if s.attemptEntered != nil {
		s.attemptEnterOnce.Do(func() { close(s.attemptEntered) })
	}
	if s.attemptRelease != nil {
		<-s.attemptRelease
	}
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

func TestAPIAttemptGroupSettlementOutcomeMatchesFinalAttemptAfterOutOfOrderCompletions(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_out_of_order")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	first := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	second := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt.Add(time.Millisecond)))
	finalErr := errors.New("final attempt rejected")
	second.Complete(testAPIAttemptResult(startedAt.Add(2*time.Millisecond), apilog.AttemptProviderReject, finalErr))
	first.Complete(testAPIAttemptResult(startedAt.Add(3*time.Millisecond), apilog.AttemptSuccess, nil))
	group.SettleResult(ctx, finalErr)

	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 2 || len(settlements) != 1 {
		t.Fatalf("attempts/settlements = %d/%d, want 2/1", len(attempts), len(settlements))
	}
	settlement := settlements[0]
	if settlement.FinalAttemptID != second.id || settlement.FinalAttemptCount != 2 {
		t.Fatalf("settlement final attempt = %+v, want second begun attempt", settlement)
	}
	if settlement.Outcome != apilog.AttemptProviderReject {
		t.Fatalf("settlement outcome = %q, want final attempt outcome %q", settlement.Outcome, apilog.AttemptProviderReject)
	}
}

func TestAPIAttemptGroupSettlementPreservesProviderTimeoutWithLiveParent(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_provider_timeout")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	timeoutErr := NewRequestTimeoutError("primary", "request timed out", context.DeadlineExceeded)

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptProviderTimeout, timeoutErr))
	group.SettleResult(ctx, timeoutErr)

	_, settlements, _ := sink.snapshot()
	if len(settlements) != 1 {
		t.Fatalf("settlements = %d, want 1", len(settlements))
	}
	if settlements[0].Outcome != apilog.AttemptProviderTimeout {
		t.Fatalf("settlement outcome = %q, want %q", settlements[0].Outcome, apilog.AttemptProviderTimeout)
	}
}

func TestAPIAttemptGroupSettlementParentCancellationOverridesAttemptOutcome(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("ag_parent_canceled")
	parent, cancel := context.WithCancel(context.Background())
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(parent, group), sink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	timeoutErr := NewRequestTimeoutError("primary", "request timed out", context.DeadlineExceeded)

	attempt := BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt))
	attempt.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptProviderTimeout, timeoutErr))
	cancel()
	group.SettleResult(ctx, timeoutErr)

	_, settlements, _ := sink.snapshot()
	if len(settlements) != 1 {
		t.Fatalf("settlements = %d, want 1", len(settlements))
	}
	if settlements[0].Outcome != apilog.AttemptCallerCancel {
		t.Fatalf("settlement outcome = %q, want %q", settlements[0].Outcome, apilog.AttemptCallerCancel)
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

func TestClientSessionGroupSettlesFailuresBeforeProviderMiddleware(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{
			name: "request validation",
			req:  Request{Provider: "missing"},
		},
		{
			name: "provider resolution",
			req: Request{
				Provider: "missing",
				Model:    "model-a",
				Messages: []Message{User("hello")},
			},
		},
	}
	for _, tt := range tests {
		for _, operation := range []string{"complete", "stream"} {
			t.Run(tt.name+"/"+operation, func(t *testing.T) {
				stateDir := t.TempDir()
				logger, err := NewSessionAPILogger(stateDir)
				if err != nil {
					t.Fatalf("NewSessionAPILogger: %v", err)
				}
				client := NewClient()
				client.Use(logger)

				suffix := strings.ReplaceAll(tt.name, " ", "-") + "-" + operation
				sessionID := "sess-preflight-" + suffix
				group := NewAPIAttemptGroup("ag_preflight_" + strings.ReplaceAll(suffix, "-", "_"))
				ctx := WithAPILogContext(context.Background(), sessionID, 1)
				ctx = WithAPIAttemptGroup(ctx, group)
				var callErr error
				if operation == "complete" {
					_, callErr = client.Complete(ctx, tt.req)
				} else {
					_, callErr = client.Stream(ctx, tt.req)
				}
				if callErr == nil {
					t.Fatalf("%s succeeded", operation)
				}
				group.SettleResult(ctx, callErr)
				if err := logger.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}

				settlement := readOnlyCanonicalSettlement(t, filepath.Join(stateDir, "sessions", sessionID+".api.jsonl"))
				if settlement.AttemptGroupID != group.ID || settlement.FinalAttemptID != "" || settlement.FinalAttemptCount != 0 {
					t.Fatalf("zero-attempt settlement = %+v", settlement)
				}
				if settlement.Outcome != apilog.AttemptTransportFail {
					t.Fatalf("settlement outcome = %q, want %q", settlement.Outcome, apilog.AttemptTransportFail)
				}
			})
		}
	}
}

func TestAPIAttemptGroupNoSinkThenLateSinkRemainsInert(t *testing.T) {
	group := NewAPIAttemptGroup("ag_no_sink")
	ctx := WithAPIAttemptGroup(context.Background(), group)
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptSuccess, nil))

	lateSink := &recordingAPIAttemptSink{}
	lateCtx := WithAPIAttemptSink(ctx, lateSink)
	group.Settle(lateCtx, apilog.AttemptSuccess)

	attempts, settlements, events := lateSink.snapshot()
	if len(attempts) != 0 || len(settlements) != 0 || len(events) != 0 {
		t.Fatalf("late sink received attempts=%d settlements=%d events=%v, want consistently inert group", len(attempts), len(settlements), events)
	}
}

func TestAPIAttemptGroupResolvesCurrentObserverFromBoundSink(t *testing.T) {
	storageErr := errors.New("append failed")
	sink := &recordingAPIAttemptSink{failAttempt: storageErr}
	var oldObserverCalls, currentObserverCalls int
	sink.failureObserverFn = func(APILogFailure) { oldObserverCalls++ }
	group := NewAPIAttemptGroup("ag_observer_replacement")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	sink.failureObserverFn = func(APILogFailure) { currentObserverCalls++ }
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	BeginAPIAttempt(ctx, testAPIAttemptMeta(startedAt)).Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptTransportFail, errors.New("transport failed")))

	if oldObserverCalls != 0 || currentObserverCalls != 1 {
		t.Fatalf("observer calls old/current = %d/%d, want 0/1", oldObserverCalls, currentObserverCalls)
	}
}

func TestAPIAttemptGroupCompetingSinkCannotReplaceBoundObserverSource(t *testing.T) {
	storageErr := errors.New("append failed")
	appendEntered := make(chan struct{})
	releaseAppend := make(chan struct{})
	boundSink := &recordingAPIAttemptSink{
		failAttempt:    storageErr,
		attemptEntered: appendEntered,
		attemptRelease: releaseAppend,
	}
	competingObserverCalls := make(chan APILogFailure, 2)
	competingSink := &recordingAPIAttemptSink{
		failureObserverFn: func(failure APILogFailure) { competingObserverCalls <- failure },
	}
	group := NewAPIAttemptGroup("ag_competing_sinks")
	ctxA := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), boundSink)
	ctxB := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), competingSink)
	startedAt := time.Unix(1_700_000_000, 0).UTC()

	first := BeginAPIAttempt(ctxA, testAPIAttemptMeta(startedAt))
	firstDone := make(chan struct{})
	go func() {
		first.Complete(testAPIAttemptResult(startedAt.Add(time.Millisecond), apilog.AttemptTransportFail, errors.New("first failed")))
		close(firstDone)
	}()
	<-appendEntered

	secondBegan := make(chan struct{})
	secondDone := make(chan struct{})
	go func() {
		second := BeginAPIAttempt(ctxB, testAPIAttemptMeta(startedAt.Add(time.Second)))
		close(secondBegan)
		second.Complete(testAPIAttemptResult(startedAt.Add(time.Second+time.Millisecond), apilog.AttemptTransportFail, errors.New("second failed")))
		close(secondDone)
	}()
	<-secondBegan
	close(releaseAppend)
	<-firstDone
	<-secondDone
	group.Settle(ctxB, apilog.AttemptTransportFail)

	_, boundSettlements, boundEvents := boundSink.snapshot()
	competingAttempts, competingSettlements, competingEvents := competingSink.snapshot()
	if !reflect.DeepEqual(boundEvents, []string{"append_attempt", "append_attempt", "append_settlement"}) {
		t.Fatalf("bound sink events = %v", boundEvents)
	}
	if len(boundSettlements) != 1 || boundSettlements[0].FinalAttemptCount != 2 || !boundSettlements[0].ForensicIncomplete {
		t.Fatalf("bound sink settlements = %+v", boundSettlements)
	}
	if len(competingAttempts) != 0 || len(competingSettlements) != 0 || len(competingEvents) != 0 {
		t.Fatalf("competing sink received attempts=%d settlements=%d events=%v", len(competingAttempts), len(competingSettlements), competingEvents)
	}
	select {
	case failure := <-competingObserverCalls:
		t.Fatalf("competing sink observer received bound-sink failure: %+v", failure)
	default:
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
