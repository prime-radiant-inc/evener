package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// newTestSession creates a minimal *Session backed by a no-op fakeAdapter.
// Close is registered via t.Cleanup.
func newTestSession(t *testing.T) *Session {
	t.Helper()
	return newSession(t, withConfig(minimalTestSessionConfig(t)))
}

func TestSubagentInheritsSelectedPluginDirs(t *testing.T) {
	t.Parallel()
	want := []string{"/plugins/selected-alpha", "/plugins/selected-beta"}
	parent := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		PluginDirs:       want,
		testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	prepared := prepareStableChild(t, parent, delegateArgs{Task: "inspect"})
	defer prepared.sub.sess.Close()
	if !slices.Equal(prepared.sub.sess.cfg.PluginDirs, want) {
		t.Fatalf("child PluginDirs = %v, want %v", prepared.sub.sess.cfg.PluginDirs, want)
	}
}

func TestEnsureRecoveryReaderPreservesPolicyShape(t *testing.T) {
	t.Parallel()
	reg := tool.NewRegistry()
	register := func(name string, limit schema.ToolOutputLimit) {
		t.Helper()
		if err := reg.Register(tool.RegisteredTool{
			Definition: llm.ToolDefinition{Name: name, Description: "test tool"},
			Limit:      limit,
			Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
				return "ok", nil
			},
		}); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	register("limited_text", schema.ToolOutputLimit{MaxChars: 10})
	register("control_only", schema.ToolOutputLimit{MaxChars: -1})
	register("read_transcript", schema.ToolOutputLimit{MaxChars: 10})

	storage := []string{"first", "limited_text", "hidden-sentinel", "second-sentinel"}
	backing := storage[:2]
	got := ensureRecoveryReader(backing, reg)
	if !slices.Equal(got, []string{"first", "limited_text", "read_transcript"}) {
		t.Fatalf("limited policy = %v", got)
	}
	if !slices.Equal(backing, []string{"first", "limited_text"}) {
		t.Fatalf("caller-owned input mutated: %v", backing)
	}
	if !slices.Equal(storage, []string{"first", "limited_text", "hidden-sentinel", "second-sentinel"}) {
		t.Fatalf("caller backing storage mutated: %v", storage)
	}
	if &got[0] == &backing[0] {
		t.Fatal("injected result aliases caller-owned backing storage")
	}

	already := []string{"limited_text", "read_transcript"}
	if got := ensureRecoveryReader(already, reg); !slices.Equal(got, already) {
		t.Fatalf("existing reader changed policy: %v", got)
	}
	control := []string{"control_only", "unknown"}
	if got := ensureRecoveryReader(control, reg); !slices.Equal(got, control) {
		t.Fatalf("unlimited/control-only policy changed: %v", got)
	}
	if got := ensureRecoveryReader(control, nil); !slices.Equal(got, control) {
		t.Fatalf("nil registry changed policy: %v", got)
	}
}

func TestExplicitAllowedToolsInjectRecoveryReaderOnly(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{
		spawn:    spawnConfig{allowedToolNames: []string{"grep"}},
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	registered := s.reg.RegisteredNames()
	for _, want := range []string{"grep", "read_transcript", "communicate"} {
		if !registered[want] {
			t.Fatalf("explicit tools = %v, want %q", s.reg.Names(), want)
		}
	}
	if registered["read_file"] || registered["shell"] {
		t.Fatalf("recovery policy broadened unrelated tools: %v", s.reg.Names())
	}
}

func TestFrozenSubagentToolNamesIncludeRecoveryReader(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.pluginAgents["task7-reader"] = plugin.Agent{
		Name:         "task7-reader",
		PluginName:   "test",
		SystemPrompt: "Read the requested files.",
		Tools:        []string{"read_file"},
	}
	prepared := prepareStableChild(t, s, delegateArgs{Task: "inspect", AgentType: "task7-reader"})
	defer prepared.sub.sess.Close()
	got := prepared.frozenToolNames
	if !slices.Equal(got, []string{"communicate", "compact_context", "job_list", "job_status", "job_stop", "job_watch", "read_file", "read_transcript", "task_list", "use_skill"}) {
		t.Fatalf("frozen names = %v", got)
	}
}

func TestSubagentFollowUpProvenanceUnionsLaunchActiveAndCompleted(t *testing.T) {
	t.Parallel()
	child := &Session{state: SessionProcessing}
	child.replaceActiveProvenance(testProvenance("watch_completed", "wg_1"))
	child.finishProcessingAtBoundary(context.Background(), SessionIdle)
	child.unionActiveProvenance(testProvenance("watch_active", "wg_1"))

	sub := &subagent{sess: child}
	got := sub.followUpProvenance(testProvenance("watch_launch", "wg_1"))

	for _, watchID := range []string{"watch_launch", "watch_completed", "watch_active"} {
		if !provenance.ContainsWatch(got, watchID, "wg_1") {
			t.Fatalf("follow-up provenance = %+v, want %s/wg_1", got, watchID)
		}
	}
}

func TestSubagentRunSnapshotsFinalProvenance(t *testing.T) {
	t.Parallel()
	child := newTestSession(t)
	sub := &subagent{
		id:      child.ID(),
		sess:    child,
		done:    make(chan struct{}),
		running: true,
		status:  SubagentRunning,
	}

	sub.run(context.Background(), "do the task", testProvenance("watch_launch", "wg_1"))

	sub.mu.Lock()
	got := provenance.Clone(sub.runProvenance)
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("subagent run should be terminal after run returns")
	}
	if !provenance.ContainsWatch(got, "watch_launch", "wg_1") {
		t.Fatalf("run provenance = %+v, want watch_launch/wg_1", got)
	}
}

// spawnRuntimeDelegate creates and launches a stable delegate.
func spawnRuntimeDelegate(t *testing.T, sess *Session, args delegateArgs) delegateResult {
	t.Helper()
	result := sess.createDelegate(context.Background(), args)
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	if result.DelegateID == "" || result.ChildSessionID == "" {
		t.Fatalf("create result missing delegate identity: %+v", result)
	}
	return result
}

// spawnRuntimeAgent is spawnRuntimeDelegate returning the child session id,
// which getSub resolves to the tracked run.
func spawnRuntimeAgent(t *testing.T, sess *Session, args delegateArgs) string {
	t.Helper()
	return spawnRuntimeDelegate(t, sess, args).ChildSessionID
}

func waitForRuntimeSubagent(t *testing.T, sess *Session, agentID string) subagentResult {
	t.Helper()
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatalf("subagent %q not found", agentID)
	}
	select {
	case <-sub.done:
	case <-time.After(30 * time.Second): // TRIPWIRE: subagent is a scripted in-process adapter, no real I/O; only fires on a genuine hang.
		t.Fatalf("timed out waiting for subagent %q", agentID)
	}
	sub.mu.Lock()
	result := sub.resultSnapshotLocked()
	sub.resultConsumed = true
	sub.mu.Unlock()
	return result
}

// TestResultSnapshot_CurrentShape verifies baseline snapshot fields for a completed subagent.
func TestResultSnapshot_CurrentShape(t *testing.T) {
	t.Parallel()
	a := &subagent{id: "01CHILD", status: SubagentCompleted, result: "done", turnsUsed: 3, sess: newTestSession(t)}
	snap := a.resultSnapshotLocked()
	if snap.Status != SubagentCompleted || snap.Output != "done" || !snap.Success || snap.TurnsUsed != 3 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.TranscriptRef == "" {
		t.Fatal("transcript_ref must be set")
	}
}

// TestResultSnapshot_CarriesAgentIDAndStatus verifies that agent_id and the run
// outcome (status) are stamped on the snapshot, and success derives from status.
func TestResultSnapshot_CarriesAgentIDAndStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		status      SubagentStatus
		err         error
		wantStatus  SubagentStatus
		wantSuccess bool
	}{
		{"completed", SubagentCompleted, nil, SubagentCompleted, true},
		{"failed", SubagentFailed, errors.New("boom"), SubagentFailed, false},
		{"cancelled", SubagentCancelled, context.Canceled, SubagentCancelled, false},
		{"exhausted", SubagentExhausted, &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 1}, SubagentExhausted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &subagent{id: "01CHILD", status: tc.status, err: tc.err, sess: newTestSession(t)}
			snap := a.resultSnapshotLocked()
			if snap.AgentID != "01CHILD" || snap.Status != tc.wantStatus || snap.Success != tc.wantSuccess {
				t.Fatalf("got %+v", snap)
			}
		})
	}
}

func TestSubagentStatusExhaustedIsTerminal(t *testing.T) {
	t.Parallel()
	if !terminalStatus(SubagentExhausted) {
		t.Fatal("exhausted subagent status must be terminal")
	}
}

// TestJobFinishedData_JSONShape verifies JOB_FINISHED carries the job terminal
// notification fields under the wire keys used by appwire/UI consumers.
func TestJobFinishedData_JSONShape(t *testing.T) {
	t.Parallel()
	code := 0
	d := events.JobFinishedData{
		JobID:         "job_X",
		JobType:       "shell",
		Status:        "completed",
		Reason:        "communicated",
		ExitCode:      &code,
		OutputBytes:   42,
		TranscriptRef: "local:child",
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"status":"completed"`) {
		t.Fatalf("missing status: %s", b)
	}
	for _, want := range []string{`"job_id":"job_X"`, `"job_type":"shell"`, `"reason":"communicated"`, `"exit_code":0`, `"output_bytes":42`, `"transcript_ref":"local:child"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %s: %s", want, b)
		}
	}
}

// cancelBlockAdapter blocks on its first Complete call until the run context is
// cancelled (returning context.Canceled, like a real interrupted LLM call), then
// returns finalResponse on every subsequent call. The single-shot `blocked` close
// signals the test that the first call is in-flight so it can deterministically
// drive the cancel without sleeps.
type cancelBlockAdapter struct {
	name    string
	blocked chan struct{}

	mu      sync.Mutex
	started bool
	resume  string
}

func (a *cancelBlockAdapter) Name() string { return a.name }

func (a *cancelBlockAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	first := !a.started
	a.started = true
	a.mu.Unlock()
	if first {
		close(a.blocked)
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	}
	resp := agenttest.FinalResponse(a.resume)
	resp.Provider = a.name
	resp.Model = req.Model
	return resp, nil
}

func (a *cancelBlockAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// TestStableDelegate_StopRunningChildThenResume stops a delegate whose run is
// blocked inside an in-flight LLM call, asserts the run finalizes as cancelled
// and the delegate goes idle and resumable, then sends it a message and asserts
// a fresh round runs to completion. Timing is controlled by the adapter's
// `blocked` channel and the context cancel — no sleeps.
func TestStableDelegate_StopRunningChildThenResume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	blocked := make(chan struct{})
	c.Register(&cancelBlockAdapter{name: "openai", blocked: blocked, resume: "resumed work"})

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	created := spawnRuntimeDelegate(t, sess, delegateArgs{Task: "do long work"})
	agentID := created.ChildSessionID

	// Wait until the child's run is blocked inside the in-flight LLM call.
	<-blocked

	value, stopErr := jobStopTool(context.Background(), sess, map[string]any{
		"target":      created.DelegateID,
		"max_wait_ms": 60_000,
	}, jobToolResultDefaultMaxChar)
	invocation := stableJobStopInvocation{value: value, err: stopErr}
	assertStableStopCompleted(t, invocation, created.DelegateID, delegateLifecycleRunning, "cancelled_by_request")
	if state := stableJobStopState(t, invocation); state.Resumable == nil || !*state.Resumable {
		t.Fatalf("stopped delegate resumable = %#v, want true", state.Resumable)
	}

	// The child must be idle (resumable), not removed from tracking.
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("stopped child must remain tracked")
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("child must be idle after stop")
	}

	// A following send runs a fresh round to completion.
	outcome := (delegateRuntime{owner: sess}).send(context.Background(), created.DelegateID, "continue", 60_000)
	abortUnpersistedStableDelegateOutcome(t, outcome)
	if outcome.result.Err != nil {
		t.Fatalf("send after stop: %v", outcome.result.Err)
	}
	if outcome.result.Status != jobstore.StatusCompleted {
		t.Fatalf("resumed status = %q, want %q (result: %+v)", outcome.result.Status, jobstore.StatusCompleted, outcome.result)
	}
}

// TestCancelAgent_GenuineFailureRacingCancelStaysFailed proves the discriminator
// keys on error identity: when the run finishes with a genuine non-context.Canceled
// error while a cancel was requested, the status must be SubagentFailed (the real
// failure is surfaced), never SubagentCancelled.
//
// The injected error ("provider boom 500") is a plain errors.New, which Classify
// treats as retryable. To prevent the retry layer from masking it with a downstream
// substitute error, we set LLMRetryPolicy.MaxRetries=0: the Retry loop executes
// exactly one attempt and returns the injected error verbatim regardless of its
// retryability class (the loop exits when attempt==maxRetries). No sleeps occur.
func TestCancelAgent_GenuineFailureRacingCancelStaysFailed(t *testing.T) {
	t.Parallel()
	const wantErrMarker = "provider boom 500"
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, errors.New(wantErrMarker)
			},
		},
	})
	noRetry := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
		LLMRetryPolicy:   &noRetry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// The sub drives sub.run directly (not via the manager), so it is intentionally
	// not tracked: tracking a sub whose sess is the parent session would make the
	// deferred sess.Close() drain it and re-enter Close on the same session.
	sub := &subagent{
		id:              sess.id,
		sess:            sess,
		running:         true,
		status:          SubagentRunning,
		done:            make(chan struct{}),
		cancelRequested: true, // a cancel is racing this run's genuine failure
	}

	sub.run(context.Background(), "do work", nil)

	sub.mu.Lock()
	status := sub.status
	runErr := sub.err
	sub.mu.Unlock()
	if status != SubagentFailed {
		t.Fatalf("status = %q, want %q (genuine failure must not be masked as cancelled)", status, SubagentFailed)
	}
	if runErr == nil || !strings.Contains(runErr.Error(), wantErrMarker) {
		t.Fatalf("sub.err = %v, want it to contain %q (injected non-context error must reach finalize verbatim)", runErr, wantErrMarker)
	}
}

// TestCancelAgent_JoinedCanceledAndExhaustionStaysCancelledWithJoinedError pins
// the interaction between the cancelled status and the budget-exhaustion payload
// when both are present in one joined run error: cancelRequested with
// errors.Join(context.Canceled, budgetExhaustionError) publishes SubagentCancelled
// (the cancel branch wins the status) and must keep the joined error verbatim —
// errors.Is(a.err, context.Canceled) stays true and the exhaustion component is
// not substituted for the whole error. The pre-classifier switch overwrote a.err
// only inside its budgetExhausted case, which this branch never reached.
func TestCancelAgent_JoinedCanceledAndExhaustionStaysCancelledWithJoinedError(t *testing.T) {
	t.Parallel()
	joined := errors.Join(context.Canceled, &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 23, Resumable: false})
	sess := newSession(t,
		withAdapter(&fakeErrAdapter{
			name: "openai",
			steps: []func(req llm.Request) (llm.Response, error){
				func(req llm.Request) (llm.Response, error) {
					return llm.Response{}, joined
				},
			},
		}),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			LLMRetryPolicy:   &llm.RetryPolicy{MaxRetries: 0},
		}),
	)
	// The sub drives sub.run directly (not via the manager), so it is
	// intentionally not tracked; see the note in
	// TestCancelAgent_GenuineFailureRacingCancelStaysFailed.
	sub := &subagent{
		id:              sess.id,
		sess:            sess,
		running:         true,
		status:          SubagentRunning,
		done:            make(chan struct{}),
		cancelRequested: true, // a cancel is racing this joined cancel+exhaustion error
	}

	sub.run(context.Background(), "do work", nil)

	sub.mu.Lock()
	status := sub.status
	runErr := sub.err
	sub.mu.Unlock()
	if status != SubagentCancelled {
		t.Fatalf("status = %q, want %q (cancel plus context.Canceled must publish cancelled even with a joined exhaustion component)", status, SubagentCancelled)
	}
	if runErr == nil || !errors.Is(runErr, context.Canceled) {
		t.Fatalf("sub.err = %v, want the joined error kept verbatim so errors.Is(sub.err, context.Canceled) holds", runErr)
	}
	if runErr.Error() != joined.Error() {
		t.Fatalf("sub.err = %q, want the original joined error text %q", runErr.Error(), joined.Error())
	}
	var exhaustion *budgetExhaustionError
	if !errors.As(runErr, &exhaustion) || exhaustion == nil {
		t.Fatalf("sub.err = %v, want the joined error preserved (errors.As still finds its exhaustion component)", runErr)
	}
}

// TestCancelAgent_NotRunning asserts cancelling an idle/terminal child returns the
// "is not running" error rather than fabricating a cancelled outcome.
func TestCancelAgent_NotRunning(t *testing.T) {
	t.Parallel()
	sess := newTestSession(t)
	// The child's sess is a distinct session (as in production), so the parent's
	// teardown closes a different session rather than re-entering its own Close.
	sub := &subagent{
		id:      "01IDLE",
		sess:    newTestSession(t),
		running: false,
		status:  SubagentCompleted,
		done:    make(chan struct{}),
	}
	close(sub.done)
	sess.subagents.track(sub)

	_, err := sess.cancelAgent("01IDLE")
	if err == nil {
		t.Fatal("cancelAgent on idle child must error")
	}
	if !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("error = %q, want it to contain \"is not running\"", err.Error())
	}
}

// resumeBlockAdapter is a two-step adapter used by TestSubagentTimestamps_ResetOnResume.
// Step 0 (first run): completes immediately.
// Step 1 (resumed run): closes started to signal the test that the run is in-flight,
// then blocks until the test closes release, allowing in-flight inspection.
type resumeBlockAdapter struct {
	name    string
	started chan struct{} // closed by step 1 when in-flight
	release chan struct{} // closed by the test to unblock step 1

	mu   sync.Mutex
	step int
}

func (a *resumeBlockAdapter) Name() string { return a.name }

func (a *resumeBlockAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	step := a.step
	a.step++
	a.mu.Unlock()

	if step == 0 {
		resp := agenttest.FinalResponse("first run")
		resp.Provider = a.name
		resp.Model = req.Model
		return resp, nil
	}
	// Step 1: signal in-flight, then block until released.
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	resp := agenttest.FinalResponse("second run")
	resp.Provider = a.name
	resp.Model = req.Model
	return resp, nil
}

func (a *resumeBlockAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// TestSubagentTimestamps_ResetOnResume verifies that timestamp fields are set at
// spawn, stamped at run-end, and correctly reset when the idle agent is resumed:
// createdAt is preserved, startedAt is re-stamped to a strictly later time, and
// endedAt is cleared to nil. The resumed run is observed IN-FLIGHT (blocked inside
// the adapter's second step) so the assertions see the reset state before finalize
// re-sets the fields. This makes the test fail if either reset line is removed.
func TestSubagentTimestamps_ResetOnResume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	adapter := &resumeBlockAdapter{
		name:    "openai",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c.Register(adapter)

	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// TRIPWIRE: adapter is a scripted in-process call, no real I/O; this
	// normally completes in well under a second. 30s only fires on a genuine
	// hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Non-blocking spawn; the first run (adapter step 0) completes immediately.
	created := spawnRuntimeDelegate(t, sess, delegateArgs{Task: "timestamp test"})
	agentID := created.ChildSessionID

	// Wait for the first run to complete so we observe a stable post-finalize state.
	waitForRuntimeSubagent(t, sess, agentID)

	// Capture timestamps and agentType after first run.
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("sub must be tracked after first run")
	}

	sub.mu.Lock()
	createdAt0 := sub.createdAt
	startedAt0 := sub.startedAt
	endedAt0 := sub.endedAt
	agentTypeVal := sub.agentType
	sub.mu.Unlock()

	if createdAt0.IsZero() {
		t.Error("createdAt must be set after spawn")
	}
	if startedAt0.IsZero() {
		t.Error("startedAt must be set after spawn")
	}
	if endedAt0 == nil {
		t.Error("endedAt must be non-nil after first run completes")
	}
	// An untyped create resolves to the default agent type.
	if agentTypeVal != "default" {
		t.Errorf("agentType = %q, want %q for an untyped create", agentTypeVal, "default")
	}

	// Capture a reference point just before the sleep so we can make a robust
	// clock-independent assertion: the resume timestamp must be after beforeResume.
	// Using beforeResume (captured here, after startedAt0) plus a 50ms guard is more
	// reliable than comparing against startedAt0 directly, because on platforms with
	// coarse clock resolution (e.g. 10-15ms ticks) a 2ms sleep cannot guarantee the
	// two time.Now() calls land in different ticks.
	beforeResume := time.Now()
	time.Sleep(50 * time.Millisecond)

	// Non-blocking resume: a send with no wait starts the run and returns
	// immediately while adapter step 1 blocks inside Complete waiting for release.
	if outcome := (delegateRuntime{owner: sess}).send(ctx, created.DelegateID, "continue", 0); outcome.result.Err != nil {
		t.Fatalf("send: %v", outcome.result.Err)
	}

	// Wait (channel receive, no sleep) until the resumed run is blocked in-flight.
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for resumed run to reach in-flight state")
	}

	// Inspect IN-FLIGHT: the send has already reset the fields but finalize has not
	// yet run. These are the load-bearing assertions:
	//   - endedAt == nil proves resume CLEARED it (catches deleted `sub.endedAt = nil`)
	//   - startedAt.After(beforeResume) proves resume RE-STAMPED it to a strictly later
	//     time (catches deleted `sub.startedAt = resumeTime`); beforeResume >= startedAt0
	//     so this transitively proves startedAt advanced past the first-run value.
	sub.mu.Lock()
	createdAtInFlight := sub.createdAt
	startedAtInFlight := sub.startedAt
	endedAtInFlight := sub.endedAt
	runningInFlight := sub.running
	sub.mu.Unlock()

	if endedAtInFlight != nil {
		t.Errorf("endedAt must be nil in-flight (resume must clear it); got %v", endedAtInFlight)
	}
	if !startedAtInFlight.After(beforeResume) {
		t.Errorf("startedAt must be strictly after pre-resume snapshot; beforeResume=%v startedAtInFlight=%v", beforeResume, startedAtInFlight)
	}
	if !runningInFlight {
		t.Error("running must be true while resumed run is in-flight")
	}
	if !createdAtInFlight.Equal(createdAt0) {
		t.Errorf("createdAt must be unchanged on resume; was %v now %v", createdAt0, createdAtInFlight)
	}

	// Release the adapter so the resumed run can finish; then wait for it.
	close(adapter.release)
	waitForRuntimeSubagent(t, sess, agentID)

	// Secondary: finalize must have re-set endedAt.
	sub.mu.Lock()
	endedAtFinal := sub.endedAt
	sub.mu.Unlock()
	if endedAtFinal == nil {
		t.Error("endedAt must be non-nil after resumed run completes (finalize must set it)")
	}
}

// TestSubagentCannotCallRootOnlyControlTools asserts depth>0 subagents keep
// delegate_send for caller-route messages while root-only controls stay unavailable.
func TestSubagentCannotCallRootOnlyControlTools(t *testing.T) {
	t.Parallel()
	if len(rootOnlyJobPresenceTools) != 1 || rootOnlyJobPresenceTools[0] != "delegate" {
		t.Fatalf("rootOnlyJobPresenceTools = %v, want exactly [delegate]", rootOnlyJobPresenceTools)
	}
	if !isRootOnlyJobPresenceTool("delegate") {
		t.Fatal("delegate must be a root-only job-presence tool")
	}
	if isRootOnlyJobPresenceTool("job_watch") {
		t.Fatal("job_watch must NOT be a root-only job-presence tool: a session that can run jobs watches its own jobs")
	}
	if isRootOnlyJobPresenceTool("delegate_send") {
		t.Fatal("delegate_send must not be a root-only job-presence tool")
	}
	if !isRootOnlySubagentTool("delegate") {
		t.Fatal("delegate must be a root-only subagent tool")
	}
	if isRootOnlySubagentTool("job_watch") {
		t.Fatal("job_watch must NOT be a root-only subagent tool: each cross-session watch source authorizes itself")
	}
	if !isRootOnlySubagentTool("manage_worktree") {
		t.Fatal("manage_worktree must be a root-only subagent tool")
	}
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = "parent" // real child: allowance comes from the spawn carrier (0), not MaxSubagentDepth
	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if child.reg.Get("delegate") != nil {
		t.Fatal("depth>0 child must not have delegate registered")
	}
	if child.reg.Get("job_watch") == nil {
		t.Fatal("depth>0 child must keep job_watch registered: it can run jobs, so it can watch its own")
	}
	if child.reg.Get("manage_worktree") != nil {
		t.Fatal("depth>0 child must not have manage_worktree registered")
	}
	if child.reg.Get("delegate_send") == nil {
		t.Fatal("depth>0 child must keep delegate_send registered")
	}
	for _, name := range []string{"shell", "job_status", "job_list", "job_stop", "read_transcript"} {
		if child.reg.Get(name) == nil {
			t.Fatalf("depth>0 child must keep %s registered", name)
		}
	}
	if !hasCachedCallableToolDefinition(child, "job_watch") {
		t.Fatal("depth>0 child must advertise job_watch")
	}
	if hasCachedCallableToolDefinition(child, "manage_worktree") {
		t.Fatal("depth>0 child must not advertise manage_worktree")
	}
	if !hasCachedCallableToolDefinition(child, "delegate_send") {
		t.Fatal("depth>0 child must advertise delegate_send")
	}
	for _, name := range []string{"shell", "job_status", "job_list", "job_stop", "read_transcript"} {
		if !hasCachedCallableToolDefinition(child, name) {
			t.Fatalf("depth>0 child must advertise %s", name)
		}
	}
}

func TestWatchParentChildGetsJobWatchButNotDelegate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = "parent"
	subCfg.spawn.parentWatchGranted = true

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if child.reg.Get("job_watch") == nil {
		t.Fatal("watch_parent child must have job_watch registered")
	}
	if !hasCachedCallableToolDefinition(child, "job_watch") {
		t.Fatal("watch_parent child must advertise job_watch")
	}
	if child.reg.Get("delegate") != nil {
		t.Fatal("watch_parent child must not get delegate without delegation_allowance")
	}
	if hasCachedCallableToolDefinition(child, "delegate") {
		t.Fatal("watch_parent child must not advertise delegate without delegation_allowance")
	}
}

func TestWatchParentGrantIsNotInheritedByGrandchild(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 3, StateDir: t.TempDir()}
	subCfg.spawn.depth = 1
	subCfg.spawn.parentSessionID = "parent"
	subCfg.spawn.delegationAllowance = 1
	subCfg.spawn.parentWatchGranted = true
	subCfg.testOnly.sandboxProber = bwrapCapableProber(dir)

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(child.Close)
	if child.reg.Get("job_watch") == nil {
		t.Fatal("test setup: watch_parent child must have job_watch registered")
	}

	child.pluginAgents = map[string]plugin.Agent{
		"typed-child": {
			Name:  "typed-child",
			Tools: []string{"read_file"},
		},
	}
	prepared := prepareStableChild(t, child, delegateArgs{Task: "grandchild task", AgentType: "typed-child", DelegationAllowance: new(0)})

	grandchild := prepared.sub.sess
	if grandchild.cfg.spawn.parentWatchGranted {
		t.Fatal("grandchild must not inherit parentWatchGranted without watch_parent")
	}
	// job_watch is not the parent-watch grant: a typed leaf keeps it to wait on
	// its OWN jobs. What closes `parent` is the grant flag below (and the watch
	// source's own authorization), never tool absence — the old absence pin
	// would re-break #2645 for any typed leaf.
	if grandchild.reg.Get("job_watch") == nil {
		t.Fatal("grandchild must keep job_watch to watch its own jobs")
	}
	if !hasCachedCallableToolDefinition(grandchild, "job_watch") {
		t.Fatal("grandchild must advertise the job_watch it can call")
	}
	if grandchild.reg.Get("delegate") != nil {
		t.Fatal("grandchild with delegation_allowance 0 must not have delegate registered")
	}
	// Presence is not isolation: the grandchild holds the job tools for its own
	// jobs but must not reach its parent's, so the non-inheritance claim stays
	// behavioral rather than a tool-presence check.
	assertCannotReachParentJobs(t, grandchild, startBackgroundJob(t, child))
}

func hasCachedCallableToolDefinition(s *Session, name string) bool {
	if mappedName := s.profile.ToolNameMap()[name]; mappedName != "" {
		name = mappedName
	}
	for _, def := range s.cachedToolDefs {
		if def.Name == name {
			return true
		}
	}
	return false
}

// TestSelectSubagentModelAllowsRecursionWithAllowance verifies that a depth-1
// session with delegationAllowance == 1 passes the delegation gate (spec §1
// seam 1): the gate checks the allowance, not the session's depth.
func TestSelectSubagentModelAllowsRecursionWithAllowance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Construct a depth-1 child session with delegationAllowance = 1 via spawnConfig,
	// mirroring a real recursion-capable child handed down from a root.
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 1

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.selectSubagentModel(context.Background(), "", ""); err != nil {
		t.Fatalf("delegation gate fired on a session with delegationAllowance=1: %v", err)
	}
}

// TestBaseSubagentPolicyAllowsDelegateWithAllowance verifies seam 4 (spec §1):
// baseSubagentToolPolicy is allowance-aware in its default (untyped) case.
//
// Positive case (canDelegate=true): the default child gets NO deny-list, so
// delegate and job_watch are NOT denied.
// Negative case (canDelegate=false): the default child denies delegate but
// keeps job_watch — a session that runs jobs watches its own jobs.
// Typed cases: allowance NEVER injects delegate into a typed agent's
// allow-list; job_watch is intrinsic to every typed surface regardless of it.
func TestBaseSubagentPolicyAllowsDelegateWithAllowance(t *testing.T) {
	t.Parallel()
	t.Run("default child with canDelegate=true: delegate and job_watch not denied", func(t *testing.T) {
		allTools, allowed, denied := baseSubagentToolPolicy(nil, true)
		if allTools {
			t.Fatal("default child must not be allTools")
		}
		if len(allowed) != 0 {
			t.Fatalf("default child must not have an allow-list, got %v", allowed)
		}
		for _, tool := range []string{"delegate", "job_watch"} {
			for _, d := range denied {
				if d == tool {
					t.Errorf("default child with canDelegate=true: %q must not be in denied list (got %v)", tool, denied)
				}
			}
		}
	})

	t.Run("default child with canDelegate=false: delegate denied, job_watch kept", func(t *testing.T) {
		_, _, denied := baseSubagentToolPolicy(nil, false)
		if !slices.Contains(denied, "delegate") {
			t.Errorf("default child with canDelegate=false: delegate must be in denied list (got %v)", denied)
		}
		if slices.Contains(denied, "job_watch") {
			t.Errorf("default child with canDelegate=false: job_watch must NOT be denied — a session that can run jobs watches its own jobs (got %v)", denied)
		}
	})

	t.Run("AllTools agent: canDelegate does not change allTools result", func(t *testing.T) {
		allTools, allowed, denied := baseSubagentToolPolicy(&plugin.Agent{AllTools: true}, false)
		if !allTools {
			t.Fatal("AllTools agent must return allTools=true")
		}
		if len(allowed) != 0 || len(denied) != 0 {
			t.Fatalf("AllTools agent must return empty allowed/denied, got allowed=%v denied=%v", allowed, denied)
		}
		// canDelegate=true must also leave AllTools case alone
		allTools2, _, _ := baseSubagentToolPolicy(&plugin.Agent{AllTools: true}, true)
		if !allTools2 {
			t.Fatal("AllTools agent with canDelegate=true must still return allTools=true")
		}
	})

	t.Run("explicit-Tools agent: canDelegate=true does NOT inject delegate into allow-list", func(t *testing.T) {
		ag := &plugin.Agent{Tools: []string{"read_file"}}
		_, allowed, denied := baseSubagentToolPolicy(ag, true)
		if len(denied) != 0 {
			t.Fatalf("explicit-Tools agent must have no deny-list, got %v", denied)
		}
		if slices.Contains(allowed, "delegate") {
			t.Errorf("canDelegate=true must NOT inject delegate into a typed agent's allow-list (got %v)", allowed)
		}
		// The job-supervision tools are intrinsic, not injected for delegation:
		// every allow-list carries them so a leaf can enumerate, await, inspect,
		// and stop its own promoted background job (#2645).
		for _, want := range []string{"read_file", "task_list", "job_list", "job_status", "job_stop", "job_watch"} {
			if !slices.Contains(allowed, want) {
				t.Errorf("explicit-Tools agent must contain %q in allow-list (got %v)", want, allowed)
			}
		}
	})
}
