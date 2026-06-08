package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
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

// TestResultSnapshot_CarriesAgentIDAndReason verifies that agent_id and reason are stamped on the snapshot.
func TestResultSnapshot_CarriesAgentIDAndReason(t *testing.T) {
	cases := []struct {
		name        string
		status      SubagentStatus
		err         error
		wantReason  SubagentStatus
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
			if snap.AgentID != "01CHILD" || snap.Reason != tc.wantReason || snap.Success != tc.wantSuccess {
				t.Fatalf("got %+v", snap)
			}
		})
	}
}

// TestBlockingSpawn_SnapshotHasAgentID verifies that a blocking spawn result carries agent_id directly from the snapshot.
func TestBlockingSpawn_SnapshotHasAgentID(t *testing.T) {
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

	res := sess.reg.ExecuteCall(ctx, sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"test task","blocking":true}`),
	})
	if res.IsError {
		t.Fatalf("blocking spawn returned error: %s", res.Output)
	}

	var result subagentResult
	if err := json.Unmarshal([]byte(res.Output), &result); err != nil {
		t.Fatalf("parsing blocking result: %v (output: %s)", err, res.Output)
	}
	if result.AgentID == "" {
		t.Errorf("agent_id must be set in blocking result, got: %s", res.Output)
	}
	if result.Status != SubagentCompleted {
		t.Errorf("status = %q, want %q", result.Status, SubagentCompleted)
	}
	if result.Reason != SubagentCompleted {
		t.Errorf("reason = %q, want %q", result.Reason, SubagentCompleted)
	}
	if !result.Success {
		t.Error("success must be true for a completed agent")
	}
	if result.TurnsUsed < 1 {
		t.Error("turns_used must be >= 1")
	}
	if result.TranscriptRef == "" {
		t.Error("transcript_ref must be set")
	}
}

// TestSpawn_StartEmittedBeforeRunGoroutine asserts that SUBAGENT_START for the
// spawned agent_id appears in the event stream, and that it precedes
// SUBAGENT_END in program order (no longer racing the run goroutine).
func TestSpawn_StartEmittedBeforeRunGoroutine(t *testing.T) {
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

	// Spawn a non-blocking child that completes immediately.
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do work"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))

	// Wait for the child to finish so SUBAGENT_END is guaranteed to have been emitted.
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}

	sess.Close()
	<-evDone

	// Locate the positions of SUBAGENT_START and SUBAGENT_END for our agent_id.
	startIdx := -1
	endIdx := -1
	for i, ev := range evs {
		switch d := ev.Data.(type) {
		case events.SubagentStartData:
			if d.AgentID == agentID {
				startIdx = i
			}
		case events.SubagentEndData:
			if d.AgentID == agentID {
				endIdx = i
			}
		}
	}

	if startIdx == -1 {
		t.Fatalf("SUBAGENT_START not found for agent_id %q in events: %v", agentID, evs)
	}
	if endIdx == -1 {
		t.Fatalf("SUBAGENT_END not found for agent_id %q in events: %v", agentID, evs)
	}
	if startIdx >= endIdx {
		t.Fatalf("SUBAGENT_START (idx=%d) must precede SUBAGENT_END (idx=%d)", startIdx, endIdx)
	}
}

// TestSubagentEndData_HasReason verifies that SUBAGENT_END carries a reason field.
func TestSubagentEndData_HasReason(t *testing.T) {
	d := events.SubagentEndData{AgentID: "x", Status: "completed", TurnsUsed: 2, Reason: "completed"}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"reason":"completed"`) {
		t.Fatalf("missing reason: %s", b)
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

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"do long work"}`),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))

	// Wait until the child's run is blocked inside the in-flight LLM call.
	<-blocked

	cancelRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "cancel_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if cancelRes.IsError {
		t.Fatalf("cancel_agent error: %s", cancelRes.Output)
	}
	var cancelled subagentResult
	if err := json.Unmarshal([]byte(cancelRes.Output), &cancelled); err != nil {
		t.Fatalf("unmarshal cancel result: %v (output: %s)", err, cancelRes.Output)
	}
	if cancelled.Status != SubagentCancelled {
		t.Fatalf("status = %q, want %q (output: %s)", cancelled.Status, SubagentCancelled, cancelRes.Output)
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
	resumeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c3",
		Name:      "resume_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"message":"continue","blocking":true}`, agentID)),
	})
	if resumeRes.IsError {
		t.Fatalf("resume_agent error: %s", resumeRes.Output)
	}
	var resumed subagentResult
	if err := json.Unmarshal([]byte(resumeRes.Output), &resumed); err != nil {
		t.Fatalf("unmarshal resume result: %v (output: %s)", err, resumeRes.Output)
	}
	if resumed.Status != SubagentCompleted {
		t.Fatalf("resumed status = %q, want %q (output: %s)", resumed.Status, SubagentCompleted, resumeRes.Output)
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

// TestCancelAgent_ChildCannotCall asserts cancel_agent is a root-only management
// tool: a depth>0 subagent's registry must not expose it.
func TestCancelAgent_ChildCannotCall(t *testing.T) {
	if !isRootOnlyAgentManagementTool("cancel_agent") {
		t.Fatal("cancel_agent must be a root-only agent-management tool")
	}

	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	subCfg := SessionConfig{MaxSubagentDepth: 2}
	subCfg.spawn.depth = 1
	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), subCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()

	if child.reg.Get("cancel_agent") != nil {
		t.Fatal("depth>0 child must not have cancel_agent registered")
	}
}
