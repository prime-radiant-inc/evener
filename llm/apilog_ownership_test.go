package llm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestSessionOwnershipAPILoggerDiscardsRecordsButKeepsOwnership pins the
// default-off contract: the ownership logger still creates and privately locks
// each session's API-log target (so process ownership is unchanged), but
// records admitted through the sink never reach storage.
func TestSessionOwnershipAPILoggerDiscardsRecordsButKeepsOwnership(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionOwnershipAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	const sessionID = "sess-ownership"
	if err := logger.ReserveSession(sessionID); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}
	completeCanonicalGroup(t, logger, sessionID, "ag_ownership", time.Unix(1_700_000_000, 0).UTC())
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := filepath.Join(stateDir, "sessions", sessionID+".api.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("ownership API log not created: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ownership API log mode = %04o, want 0600", info.Mode().Perm())
	}
	if info.Size() != 0 {
		t.Fatalf("ownership API log size = %d, want 0 (records must be discarded)", info.Size())
	}
}

// TestSessionOwnershipAPILoggerNeverOpensUnreservedSessions verifies the
// discarded append path does not create session leaves at all: without a
// reserve there is no ownership to hold, so nothing touches the filesystem.
func TestSessionOwnershipAPILoggerNeverOpensUnreservedSessions(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionOwnershipAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	completeCanonicalGroup(t, logger, "sess-never-reserved", "ag_never_reserved", time.Unix(1_700_000_000, 0).UTC())
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "sess-never-reserved.api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("discarded append created a session leaf: %v", err)
	}
}

// TestSessionOwnershipAPILoggerEnforcesOwnership crosses modes: an ownership
// logger holding a session blocks a recording logger from reserving it, and
// releases cleanly. The flock is the session-ownership mechanism, so it must
// behave identically in both modes.
func TestSessionOwnershipAPILoggerEnforcesOwnership(t *testing.T) {
	stateDir := t.TempDir()
	owner, err := NewSessionOwnershipAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := owner.ReserveSession("sess-cross-mode"); err != nil {
		t.Fatalf("ReserveSession: %v", err)
	}

	contender, err := NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	defer contender.Close() //nolint:errcheck
	if err := contender.ReserveSession("sess-cross-mode"); !errors.Is(err, ErrAPILogTargetLocked) {
		t.Fatalf("recording ReserveSession against ownership lock = %v, want ErrAPILogTargetLocked", err)
	}

	if err := owner.ReleaseSession("sess-cross-mode"); err != nil {
		t.Fatalf("ReleaseSession: %v", err)
	}
	if err := contender.ReserveSession("sess-cross-mode"); err != nil {
		t.Fatalf("ReserveSession after release: %v", err)
	}
}

// TestSessionOwnershipAPILoggerSettlesWithoutFailureObservations proves the
// discarded settlement path reports success to callers (the group's settlement
// error surfacing stays quiet when nothing was supposed to be written).
func TestSessionOwnershipAPILoggerSettlesWithoutFailureObservations(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionOwnershipAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	var failures []APILogFailure
	logger.SetFailureObserver(func(failure APILogFailure) { failures = append(failures, failure) })

	ctx := WithAPILogContext(WithAPIAttemptSink(context.Background(), logger), "sess-quiet")
	group := NewAPIAttemptGroup("ag_ownership_quiet")
	ctx = WithAPIAttemptGroup(ctx, group)
	group.SettleResult(ctx, nil)
	if len(failures) != 0 {
		t.Fatalf("discarded settlement reported failures: %+v", failures)
	}
}

type ownershipBindingTestAdapter struct {
	active   *bool
	protocol *string
}

func (ownershipBindingTestAdapter) Name() string { return "binding" }

func (a ownershipBindingTestAdapter) Complete(ctx context.Context, req Request) (Response, error) {
	startedAt := time.Unix(1_700_000_000, 0).UTC()
	attempt := BeginAPIAttempt(ctx, APIAttemptMeta{
		ProviderInstance: "binding", RequestModel: req.Model, Protocol: "openai-responses",
		Method: http.MethodPost, Endpoint: "https://example.test/v1/responses",
		RequestBody: []byte(`{"input":"hi"}`), StartedAt: startedAt,
	})
	if a.active != nil {
		*a.active = attempt.Active()
	}
	if a.protocol != nil {
		*a.protocol = apiAttemptGroupFromContext(ctx).Protocol()
	}
	resp := Response{
		Provider: req.Provider, Model: req.Model, Message: Assistant("ok"),
		Finish: FinishReason{Reason: FinishReasonStop},
	}
	attempt.Complete(APIAttemptResult{
		StatusCode: http.StatusOK, ResponseBody: []byte(`{"output":"ok"}`), Response: &resp,
		Outcome: apilog.AttemptSuccess, FinishedAt: startedAt.Add(time.Millisecond),
	})
	return resp, nil
}

func (ownershipBindingTestAdapter) Stream(context.Context, Request) (Stream, error) {
	return nil, nil
}

// TestSessionOwnershipLoggerMiddlewareStampsProtocolWithoutEvidence pins why
// the ownership logger stays attached as client middleware in default-off
// mode: its group binding is what stamps the protocol the agent records as
// response_protocol on turns. It also pins that the attempt is inert: every
// record is discarded, so retaining request and response bodies and building
// the record would be work nobody keeps, repeated over the whole conversation
// on every model round.
func TestSessionOwnershipLoggerMiddlewareStampsProtocolWithoutEvidence(t *testing.T) {
	stateDir := t.TempDir()
	logger, err := NewSessionOwnershipAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client := NewClient()
	active := true
	var protocol string
	client.Register(ownershipBindingTestAdapter{active: &active, protocol: &protocol})
	client.Use(logger)

	if _, err := client.Complete(WithAPILogContext(context.Background(), "sess-bind"), Request{
		Provider: "binding",
		Model:    "m",
		Messages: []Message{User("hi")},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if protocol != "openai-responses" {
		t.Fatalf("group protocol = %q, want openai-responses -- turns would lose response_protocol", protocol)
	}
	if active {
		t.Fatal("ownership logger made an evidence-retaining attempt for a record it discards")
	}
	// The discarded append never opens the session leaf: no reserve happened
	// and nothing reaches storage.
	if _, err := os.Stat(filepath.Join(stateDir, "sessions", "sess-bind.api.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("discarded append reached storage: %v", err)
	}
}

// TestAPIAttemptContextActiveFollowsGroupBoundSink pins that transports decide
// whether to retain evidence from the sink BeginAPIAttempt will append to: the
// one the group bound first, not whichever sink the context carries now. A
// group bound to a persisting sink must keep recording when a later context
// carries the discarding ownership logger, and a group bound to that logger
// must not retain evidence a later persisting sink in the context can't get.
func TestAPIAttemptContextActiveFollowsGroupBoundSink(t *testing.T) {
	ownership, err := NewSessionOwnershipAPILogger(t.TempDir())
	if err != nil {
		t.Fatalf("NewSessionOwnershipAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = ownership.Close() })
	persisting := &recordingAPIAttemptSink{}

	persistingGroup := NewAPIAttemptGroup("ag_persisting")
	BeginAPIAttempt(WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), persistingGroup), persisting), APIAttemptMeta{}).Complete(APIAttemptResult{})
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), persistingGroup), ownership)
	if !APIAttemptContextActive(ctx) {
		t.Fatal("group bound to a persisting sink reported inactive under a discarding context sink -- its attempts would go unrecorded")
	}

	discardingGroup := NewAPIAttemptGroup("ag_discarding")
	BeginAPIAttempt(WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), discardingGroup), ownership), APIAttemptMeta{})
	ctx = WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), discardingGroup), persisting)
	if APIAttemptContextActive(ctx) {
		t.Fatal("group bound to the discarding logger reported active under a persisting context sink")
	}
}
