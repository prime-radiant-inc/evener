package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/plugin"
	"primeradiant.com/serf/llm"
)

// newTestSession creates a minimal *Session backed by a no-op fakeAdapter.
// The caller must defer sess.Close().
func newTestSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatalf("newTestSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func spawnRuntimeAgent(t *testing.T, sess *Session, task, model string, maxTurns int, agentType, reasoningEffort string, grantTools []string) string {
	t.Helper()
	result, err := sess.spawnAgent(context.Background(), task, model, "", maxTurns, agentType, reasoningEffort, nil, grantTools)
	if err != nil {
		t.Fatalf("spawnAgent: %v", err)
	}
	var spawned struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result.(string)), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v (out=%q)", err, result)
	}
	if spawned.AgentID == "" {
		t.Fatalf("spawn result missing agent_id: %s", result)
	}
	return spawned.AgentID
}

func waitForRuntimeSubagent(t *testing.T, sess *Session, agentID string) subagentResult {
	t.Helper()
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatalf("subagent %q not found", agentID)
	}
	select {
	case <-sub.done:
	case <-time.After(10 * time.Second):
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

// TestBlockingSpawn_SnapshotHasAgentID verifies that a blocking spawn result carries agent_id directly from the snapshot.
func TestDelegateForeground_SnapshotHasJobID(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return finalResponse("subagent done")
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := sess.createDelegate(ctx, delegateArgs{Task: "test task", Background: false, BlockTimeoutMS: 5000})
	if result.Err != nil {
		t.Fatalf("delegate returned error: %v", result.Err)
	}
	if result.JobID == "" {
		t.Fatalf("job_id must be set in delegate result: %+v", result)
	}
	if result.Status != "completed" {
		t.Errorf("status = %q, want completed", result.Status)
	}
	if result.TranscriptRef == "" {
		t.Error("transcript_ref must be set")
	}
}

// TestDelegateJobStartedEmittedBeforeJobFinished asserts that JOB_STARTED for
// the delegate job appears in the event stream, and that it precedes
// JOB_FINISHED in program order.
func TestDelegateJobStartedEmittedBeforeJobFinished(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("child done") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Collect events into a slice; closed after sess.Close().
	var evs []events.SessionEvent
	evDone := make(chan struct{})
	go func() {
		defer close(evDone)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	res := sess.createDelegate(context.Background(), delegateArgs{Task: "do work", BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("delegate error: %v", res.Err)
	}
	if res.JobID == "" {
		t.Fatal("delegate returned empty job_id")
	}

	sess.Close()
	<-evDone

	// Locate the positions of JOB_STARTED and JOB_FINISHED for our job_id.
	startIdx := -1
	endIdx := -1
	for i, ev := range evs {
		switch d := ev.Data.(type) {
		case events.JobStartedData:
			if d.JobID == res.JobID {
				startIdx = i
			}
		case events.JobFinishedData:
			if d.JobID == res.JobID {
				endIdx = i
			}
		}
	}

	if startIdx == -1 {
		t.Fatalf("JOB_STARTED not found for job_id %q in events: %v", res.JobID, evs)
	}
	if endIdx == -1 {
		t.Fatalf("JOB_FINISHED not found for job_id %q in events: %v", res.JobID, evs)
	}
	if startIdx >= endIdx {
		t.Fatalf("JOB_STARTED (idx=%d) must precede JOB_FINISHED (idx=%d)", startIdx, endIdx)
	}
}

// TestJobFinishedData_JSONShape verifies JOB_FINISHED carries the job terminal
// notification fields under the wire keys used by appwire/UI consumers.
func TestJobFinishedData_JSONShape(t *testing.T) {
	code := 0
	d := events.JobFinishedData{
		JobID:         "job_X",
		JobType:       "delegate",
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
	for _, want := range []string{`"job_id":"job_X"`, `"job_type":"delegate"`, `"reason":"communicated"`, `"exit_code":0`, `"output_bytes":42`, `"transcript_ref":"local:child"`} {
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
	resp := finalResponse(a.resume)
	resp.Provider = a.name
	resp.Model = req.Model
	return resp, nil
}

func (a *cancelBlockAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

// TestCancelAgent_RunningChildBecomesCancelledAndResumable cancels a child whose
// run is blocked inside an in-flight LLM call, asserts the run finalizes as
// cancelled and the child goes idle, then resumes it and asserts a fresh round
// runs to completion. Timing is controlled by the adapter's `blocked` channel and
// the context cancel — no sleeps.
func TestCancelAgent_RunningChildBecomesCancelledAndResumable(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	blocked := make(chan struct{})
	c.Register(&cancelBlockAdapter{name: "openai", blocked: blocked, resume: "resumed work"})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnRuntimeAgent(t, sess, "do long work", "", 0, "", "", nil)

	// Wait until the child's run is blocked inside the in-flight LLM call.
	<-blocked

	cancelOut, err := sess.cancelAgent(agentID)
	if err != nil {
		t.Fatalf("cancelAgent: %v", err)
	}
	var cancelled subagentResult
	if err := json.Unmarshal([]byte(cancelOut.(string)), &cancelled); err != nil {
		t.Fatalf("unmarshal cancel result: %v (output: %s)", err, cancelOut)
	}
	if cancelled.Status != SubagentCancelled {
		t.Fatalf("status = %q, want %q (output: %s)", cancelled.Status, SubagentCancelled, cancelOut)
	}
	if cancelled.Success {
		t.Errorf("cancelled run must not report success")
	}

	// The child must be idle (resumable), not removed from tracking.
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("cancelled child must remain tracked")
	}
	sub.mu.Lock()
	running := sub.running
	sub.mu.Unlock()
	if running {
		t.Fatal("child must be idle after cancel")
	}

	// A following resume runs a fresh round to completion.
	if _, err := sess.sendInput(context.Background(), agentID, "continue"); err != nil {
		t.Fatalf("sendInput: %v", err)
	}
	resumed := waitForRuntimeSubagent(t, sess, agentID)
	if resumed.Status != SubagentCompleted {
		t.Fatalf("resumed status = %q, want %q (output: %+v)", resumed.Status, SubagentCompleted, resumed)
	}
	if !resumed.Success {
		t.Errorf("resumed run must report success")
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
		emit:            sess.emit,
		running:         true,
		status:          SubagentRunning,
		done:            make(chan struct{}),
		cancelRequested: true, // a cancel is racing this run's genuine failure
	}

	sub.run(context.Background(), "do work")

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

// TestCancelAgent_NotRunning asserts cancelling an idle/terminal child returns the
// "is not running" error rather than fabricating a cancelled outcome.
func TestCancelAgent_NotRunning(t *testing.T) {
	sess := newTestSession(t)
	// The child's sess is a distinct session (as in production), so the parent's
	// teardown closes a different session rather than re-entering its own Close.
	sub := &subagent{
		id:      "01IDLE",
		sess:    newTestSession(t),
		emit:    sess.emit,
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
		resp := finalResponse("first run")
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
	resp := finalResponse("second run")
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
	dir := t.TempDir()
	c := llm.NewClient()

	adapter := &resumeBlockAdapter{
		name:    "openai",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c.Register(adapter)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Non-blocking spawn; the first run (adapter step 0) completes immediately.
	agentID := spawnRuntimeAgent(t, sess, "timestamp test", "", 0, "", "", nil)

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
	// Default spawns have no agent_type; empty string is the correct value.
	if agentTypeVal != "" {
		t.Errorf("agentType = %q, want empty for default spawn", agentTypeVal)
	}

	// Ensure startedAt0 is strictly before any resume timestamp. time.Now() on some
	// platforms has coarse resolution, so we sleep a short but reliable duration.
	time.Sleep(2 * time.Millisecond)

	// Non-blocking resume: sendInput spawns the goroutine and returns "ok" immediately
	// while adapter step 1 blocks inside Complete waiting for release.
	if _, err := sess.sendInput(ctx, agentID, "continue"); err != nil {
		t.Fatalf("sendInput: %v", err)
	}

	// Wait (channel receive, no sleep) until the resumed run is blocked in-flight.
	select {
	case <-adapter.started:
	case <-ctx.Done():
		t.Fatal("timed out waiting for resumed run to reach in-flight state")
	}

	// Inspect IN-FLIGHT: sendInput has already reset the fields but finalize has not
	// yet run. These are the load-bearing assertions:
	//   - endedAt == nil proves resume CLEARED it (catches deleted `sub.endedAt = nil`)
	//   - startedAt.After(startedAt0) proves resume RE-STAMPED it (catches deleted
	//     `sub.startedAt = resumeTime`)
	sub.mu.Lock()
	createdAtInFlight := sub.createdAt
	startedAtInFlight := sub.startedAt
	endedAtInFlight := sub.endedAt
	runningInFlight := sub.running
	sub.mu.Unlock()

	if endedAtInFlight != nil {
		t.Errorf("endedAt must be nil in-flight (resume must clear it); got %v", endedAtInFlight)
	}
	if !startedAtInFlight.After(startedAt0) {
		t.Errorf("startedAt must be strictly after startedAt0 in-flight; startedAt0=%v startedAtInFlight=%v", startedAt0, startedAtInFlight)
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
// job_send_message for aliases while root-only controls stay unavailable.
func TestSubagentCannotCallRootOnlyControlTools(t *testing.T) {
	if len(rootOnlyJobPresenceTools) != 2 || rootOnlyJobPresenceTools[0] != "delegate" || rootOnlyJobPresenceTools[1] != "job_watch" {
		t.Fatalf("rootOnlyJobPresenceTools = %v, want exactly [delegate job_watch]", rootOnlyJobPresenceTools)
	}
	if !isRootOnlyJobPresenceTool("delegate") {
		t.Fatal("delegate must be a root-only job-presence tool")
	}
	if !isRootOnlyJobPresenceTool("job_watch") {
		t.Fatal("job_watch must be a root-only job-presence tool")
	}
	if isRootOnlyJobPresenceTool("job_send_message") {
		t.Fatal("job_send_message must not be a root-only job-presence tool")
	}
	if !isRootOnlySubagentTool("delegate") {
		t.Fatal("delegate must be a root-only subagent tool")
	}
	if !isRootOnlySubagentTool("job_watch") {
		t.Fatal("job_watch must be a root-only subagent tool")
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
	if child.reg.Get("job_watch") != nil {
		t.Fatal("depth>0 child must not have job_watch registered")
	}
	if child.reg.Get("job_send_message") == nil {
		t.Fatal("depth>0 child must keep job_send_message registered")
	}
	for _, name := range []string{"shell", "job_read_output", "job_list", "job_stop"} {
		if child.reg.Get(name) == nil {
			t.Fatalf("depth>0 child must keep %s registered", name)
		}
	}
	if hasCachedCallableToolDefinition(child, "job_watch") {
		t.Fatal("depth>0 child must not advertise job_watch")
	}
	if !hasCachedCallableToolDefinition(child, "job_send_message") {
		t.Fatal("depth>0 child must advertise job_send_message")
	}
	for _, name := range []string{"shell", "job_read_output", "job_list", "job_stop"} {
		if !hasCachedCallableToolDefinition(child, name) {
			t.Fatalf("depth>0 child must advertise %s", name)
		}
	}
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

// TestPrepareSubagentRunAllowsRecursionWithAllowance verifies that a depth-1
// session with delegationAllowance == 1 passes the spawn gate (spec §1 seam 1).
// Today this fails with "subagent management is top-level only" because the gate
// checks depth > 0 instead of checking allowance.
func TestPrepareSubagentRunAllowsRecursionWithAllowance(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Construct a depth-1 child session with delegationAllowance = 1 via spawnConfig,
	// mirroring a real recursion-capable child handed down from a root.
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 1

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	_, gotErr := sess.prepareSubagentRun(context.Background(), "task", "", dir, 5, "", "", nil, nil)

	// The allowance gate must be cleared. Any later error is fine (no model, env,
	// etc.) but it MUST NOT be the old depth-gate strings.
	if gotErr != nil {
		msg := gotErr.Error()
		if msg == "subagent management is top-level only" || msg == "subagent depth limit reached" {
			t.Fatalf("depth gate fired on a session with delegationAllowance=1: %v", gotErr)
		}
	}
}

// TestPrepareSubagentRunRejectsZeroAllowance verifies that a session with
// delegationAllowance == 0 is rejected with the exact allowance-gate error
// (spec §1 seam 2). Before the implementation the gate returns the old depth
// string, not the allowance string, so this test is red today.
func TestPrepareSubagentRunRejectsZeroAllowance(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	// Root session: depth=0, delegationAllowance will be 0 because we override it
	// directly after construction (the zero value — no delegation permitted).
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Force allowance to 0 — leaf behavior.
	sess.mu.Lock()
	sess.delegationAllowance = 0
	sess.mu.Unlock()

	_, gotErr := sess.prepareSubagentRun(context.Background(), "task", "", dir, 5, "", "", nil, nil)

	const wantErr = "delegation not permitted: your delegation_allowance is 0"
	if gotErr == nil {
		t.Fatalf("expected error %q, got nil", wantErr)
	}
	if gotErr.Error() != wantErr {
		t.Fatalf("got error %q, want %q", gotErr.Error(), wantErr)
	}
}

// TestBaseSubagentPolicyAllowsDelegateWithAllowance verifies seam 4 (spec §1):
// baseSubagentToolPolicy is allowance-aware in its default (untyped) case.
//
// Positive case (canDelegate=true): the default child gets NO deny-list, so
// delegate and job_watch are NOT denied.
// Negative case (canDelegate=false): today's behavior preserved — default child
// denies delegate and job_watch.
// Typed cases: allowance NEVER injects tools into a typed agent's surface.
func TestBaseSubagentPolicyAllowsDelegateWithAllowance(t *testing.T) {
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

	t.Run("default child with canDelegate=false: delegate and job_watch are denied", func(t *testing.T) {
		_, _, denied := baseSubagentToolPolicy(nil, false)
		for _, tool := range []string{"delegate", "job_watch"} {
			found := false
			for _, d := range denied {
				if d == tool {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("default child with canDelegate=false: %q must be in denied list (got %v)", tool, denied)
			}
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
		for _, tool := range []string{"delegate", "job_watch"} {
			for _, a := range allowed {
				if a == tool {
					t.Errorf("canDelegate=true must NOT inject %q into a typed agent's allow-list (got %v)", tool, allowed)
				}
			}
		}
		// Must contain read_file and task_list
		for _, want := range []string{"read_file", "task_list"} {
			found := false
			for _, a := range allowed {
				if a == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("explicit-Tools agent must contain %q in allow-list (got %v)", want, allowed)
			}
		}
	})
}
