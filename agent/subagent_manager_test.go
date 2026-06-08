package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// cleanupCountingEnv wraps an ExecutionEnvironment and counts Cleanup() calls. A
// session calls env.Cleanup() exactly once when it Closes, so this lets a test prove
// a session was Closed without holding a reference to it.
type cleanupCountingEnv struct {
	execenv.ExecutionEnvironment
	cleanups int32
}

func (e *cleanupCountingEnv) Cleanup() {
	atomic.AddInt32(&e.cleanups, 1)
	e.ExecutionEnvironment.Cleanup()
}

func (e *cleanupCountingEnv) count() int32 { return atomic.LoadInt32(&e.cleanups) }

// fakeEmit records the events forwarded through a manager's emit closure.
type fakeEmit struct {
	mu     sync.Mutex
	events []events.EventKind
}

func (f *fakeEmit) emit(kind events.EventKind, _ events.EventData) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, kind)
}

func (f *fakeEmit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestSubagentManager_TrackGetRemove(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)

	if got := m.get("missing"); got != nil {
		t.Fatalf("get on empty manager: got %v, want nil", got)
	}

	a := &subagent{id: "a"}
	b := &subagent{id: "b"}
	m.track(a)
	m.track(b)

	if got := m.get("a"); got != a {
		t.Fatalf("get(a): got %v, want %v", got, a)
	}
	if got := m.get("b"); got != b {
		t.Fatalf("get(b): got %v, want %v", got, b)
	}

	m.remove("a")
	if got := m.get("a"); got != nil {
		t.Fatalf("get(a) after remove: got %v, want nil", got)
	}
	if got := m.get("b"); got != b {
		t.Fatalf("get(b) after removing a: got %v, want %v", got, b)
	}
}

func TestSubagentManager_DrainForCloseReturnsAllAndClears(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)

	want := map[string]*subagent{
		"a": {id: "a"},
		"b": {id: "b"},
		"c": {id: "c"},
	}
	for _, sub := range want {
		m.track(sub)
	}

	drained := m.drainForClose()
	if len(drained) != len(want) {
		t.Fatalf("drainForClose returned %d entries, want %d", len(drained), len(want))
	}
	seen := map[string]bool{}
	for _, sub := range drained {
		if want[sub.id] != sub {
			t.Fatalf("drainForClose returned unexpected subagent for id %q", sub.id)
		}
		seen[sub.id] = true
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("drainForClose did not return subagent %q", id)
		}
	}

	// The map must be cleared: a subsequent get returns nil and a second
	// drain returns nothing.
	for id := range want {
		if got := m.get(id); got != nil {
			t.Fatalf("get(%q) after drain: got %v, want nil", id, got)
		}
	}
	if again := m.drainForClose(); len(again) != 0 {
		t.Fatalf("second drainForClose returned %d entries, want 0", len(again))
	}
}

func TestSubagentManager_EmitClosureIsCaptured(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)
	m.emit(events.EventSubagentStart, nil)
	m.emit(events.EventSubagentEnd, nil)
	if got := fe.count(); got != 2 {
		t.Fatalf("captured emit forwarded %d events, want 2", got)
	}
}

func TestSubagentManager_InfosEnumeratesTracked(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)
	m.track(&subagent{id: "a", status: SubagentRunning, turnsUsed: 3})
	m.track(&subagent{id: "b", status: SubagentCompleted, turnsUsed: 7})

	infos := m.infos()
	if len(infos) != 2 {
		t.Fatalf("infos returned %d entries, want 2", len(infos))
	}
	byID := map[string]SubagentInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	if byID["a"].Status != SubagentRunning || byID["a"].TurnsUsed != 3 {
		t.Fatalf("infos[a] = %+v, want running/3", byID["a"])
	}
	if byID["b"].Status != SubagentCompleted || byID["b"].TurnsUsed != 7 {
		t.Fatalf("infos[b] = %+v, want completed/7", byID["b"])
	}
}

// TestInfos_HidesClosedByDefault proves the /status enumeration path drops closed
// records so retained-but-closed children (Task 7) don't accumulate, while a
// completed record stays visible. The closed record is constructed directly — at
// this task nothing produces one — so this exercises the filter, not the close path.
func TestInfos_HidesClosedByDefault(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)
	m.track(&subagent{id: "done", status: SubagentCompleted, turnsUsed: 2})
	m.track(&subagent{id: "gone", status: SubagentCompleted, closed: true, turnsUsed: 5})

	infos := m.infos()
	byID := map[string]SubagentInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	if _, ok := byID["gone"]; ok {
		t.Errorf("infos() must hide closed records; got %+v", infos)
	}
	if _, ok := byID["done"]; !ok {
		t.Errorf("infos() must keep completed records; got %+v", infos)
	}
}

// TestListAgents_IncludeClosed proves include_closed=true surfaces a closed record
// that the default filter would hide.
func TestListAgents_IncludeClosed(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)
	m.track(&subagent{id: "done", status: SubagentCompleted, turnsUsed: 2})
	m.track(&subagent{id: "gone", status: SubagentCompleted, closed: true, turnsUsed: 5})

	hidden, count := m.listAgents("01ROOT", "", false)
	if count != 1 || len(hidden) != 1 || hidden[0].ID != "done" {
		t.Fatalf("default list must hide closed; got count=%d agents=%+v", count, hidden)
	}

	shown, count := m.listAgents("01ROOT", "", true)
	byID := map[string]SubagentInfo{}
	for _, info := range shown {
		byID[info.ID] = info
	}
	if count != 2 || len(shown) != 2 {
		t.Fatalf("include_closed must surface closed; got count=%d agents=%+v", count, shown)
	}
	if _, ok := byID["gone"]; !ok {
		t.Errorf("include_closed=true must include closed record; got %+v", shown)
	}

	// The legacy status="closed" sentinel maps to include_closed (no outcome filter)
	// even when the flag is false: closed records become visible alongside the rest.
	sentinel, count := m.listAgents("01ROOT", "closed", false)
	sentinelByID := map[string]SubagentInfo{}
	for _, info := range sentinel {
		sentinelByID[info.ID] = info
	}
	if count != 2 || len(sentinel) != 2 {
		t.Fatalf("status=closed must surface closed records; got count=%d agents=%+v", count, sentinel)
	}
	if _, ok := sentinelByID["gone"]; !ok {
		t.Errorf("status=closed sentinel must include the closed record; got %+v", sentinel)
	}
}

// TestListAgents_RunningChildAppearsImmediately spawns a child non-blocking whose
// run blocks inside an in-flight LLM call, then drives list_agents through its
// registered tool and asserts the child shows up running, not closed, with no
// result available. The full record shape (ids/task/transcript_ref/parent/timestamps)
// is asserted here too. Releasing the run via sess.Close() (deferred) drains the
// goroutine — no sleeps.
func TestListAgents_RunningChildAppearsImmediately(t *testing.T) {
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

	const task = "do long work"
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"task":%q}`, task)),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))

	// Block until the child's run is in-flight inside the LLM call.
	<-blocked

	listRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2",
		Name:      "list_agents",
		Arguments: json.RawMessage(`{}`),
	})
	if listRes.IsError {
		t.Fatalf("list_agents error: %s", listRes.Output)
	}

	var payload struct {
		Agents []SubagentInfo `json:"agents"`
		Count  int            `json:"count"`
	}
	if err := json.Unmarshal([]byte(listRes.Output), &payload); err != nil {
		t.Fatalf("unmarshal list result: %v (output: %s)", err, listRes.Output)
	}
	if payload.Count != 1 || len(payload.Agents) != 1 {
		t.Fatalf("list_agents count=%d agents=%d, want 1/1 (output: %s)", payload.Count, len(payload.Agents), listRes.Output)
	}
	got := payload.Agents[0]
	if got.Status != SubagentRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.Closed {
		t.Errorf("closed = true, want false for a running child")
	}
	if got.ResultAvailable {
		t.Errorf("result_available must be false for a running child")
	}
	// Full record shape.
	if got.ID != agentID || got.AgentID != agentID {
		t.Errorf("id=%q agent_id=%q, want both %q", got.ID, got.AgentID, agentID)
	}
	if got.Task != task {
		t.Errorf("task = %q, want %q", got.Task, task)
	}
	if got.ParentSessionID != sess.ID() {
		t.Errorf("parent_session_id = %q, want %q", got.ParentSessionID, sess.ID())
	}
	wantRef := encodeRef("", agentID)
	if got.TranscriptRef != wantRef {
		t.Errorf("transcript_ref = %q, want %q", got.TranscriptRef, wantRef)
	}
	if got.CreatedAt.IsZero() || got.StartedAt.IsZero() {
		t.Errorf("created_at/started_at must be set; got created=%v started=%v", got.CreatedAt, got.StartedAt)
	}
	if got.EndedAt != nil {
		t.Errorf("ended_at must be nil while running; got %v", got.EndedAt)
	}
}

// spawnCompletedChild spawns a non-blocking child that completes immediately and
// waits for its result, returning the child's agent_id. The session's adapter must
// supply a finalResponse step for the child's single run.
func spawnCompletedChild(t *testing.T, sess *Session, callPrefix, task string) string {
	t.Helper()
	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        callPrefix + "-spawn",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"task":%q}`, task)),
	})
	if spawnRes.IsError {
		t.Fatalf("spawn_agent error: %s", spawnRes.Output)
	}
	var spawned map[string]any
	if err := json.Unmarshal([]byte(spawnRes.Output), &spawned); err != nil {
		t.Fatalf("unmarshal spawn result: %v", err)
	}
	agentID := strings.TrimSpace(fmt.Sprint(spawned["agent_id"]))
	waitRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        callPrefix + "-wait",
		Name:      "wait",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q,"timeout_ms":5000}`, agentID)),
	})
	if waitRes.IsError {
		t.Fatalf("wait error: %s", waitRes.Output)
	}
	return agentID
}

// countTracked returns how many child records the manager currently holds.
func countTracked(m *subagentManager) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs)
}

// trackSyntheticChild tracks a hand-built child record. status is always a run
// outcome (running|completed|failed|cancelled); closed/closeTimedOut are the close
// flags. A closed record keeps the outcome it finished with.
func trackSyntheticChild(t *testing.T, sess *Session, id string, status SubagentStatus, closed, consumed bool, endedAt time.Time, closeTimedOut bool) {
	t.Helper()
	child, err := NewSession(sess.client, NewOpenAIProfile("gpt-5.2"), sess.env, SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatalf("trackSyntheticChild NewSession: %v", err)
	}
	ended := endedAt
	sub := &subagent{
		id:             id,
		sess:           child,
		status:         status,
		closed:         closed,
		resultConsumed: consumed,
		closeTimedOut:  closeTimedOut,
	}
	if !endedAt.IsZero() {
		sub.endedAt = &ended
	}
	sess.subagents.track(sub)
}

// TestClose_RetainsAsClosed drives a completed child through close and asserts the
// record is RETAINED as closed (not removed): still tracked, closed=true with the
// run outcome (completed) preserved in status, hidden from the default enumeration
// but visible with include_closed, and the close snapshot still reports the run
// outcome (status=completed, success=true).
//
// Load-bearing: if close still removed the record, getSub would be nil; if close
// clobbered the outcome, status would not be completed and success would be false.
func TestClose_RetainsAsClosed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("child output") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	agentID := spawnCompletedChild(t, sess, "c1", "do work")

	closeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2-close",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}

	var result subagentResult
	if err := json.Unmarshal([]byte(closeRes.Output), &result); err != nil {
		t.Fatalf("close_agent result not JSON: %q (err: %v)", closeRes.Output, err)
	}
	if !result.Closed {
		t.Errorf("close snapshot closed = false, want true")
	}
	if result.Status != SubagentCompleted {
		t.Errorf("close snapshot status = %q, want %q (run outcome must survive close)", result.Status, SubagentCompleted)
	}
	if !result.Success {
		t.Errorf("close snapshot success = false, want true for a child that completed before close")
	}
	if result.Output != "child output" {
		t.Errorf("close snapshot output = %q, want %q", result.Output, "child output")
	}

	// The record is RETAINED, not removed.
	sub := sess.getSub(agentID)
	if sub == nil {
		t.Fatal("close_agent must retain the record (got nil from getSub)")
	}
	sub.mu.Lock()
	status := sub.status
	closed := sub.closed
	sub.mu.Unlock()
	if !closed {
		t.Errorf("retained record closed = false, want true")
	}
	if status != SubagentCompleted {
		t.Errorf("retained record status = %q, want %q (run outcome preserved)", status, SubagentCompleted)
	}

	// Default enumeration hides the closed record; include_closed surfaces it.
	if infos := sess.subagents.infos(); len(infos) != 0 {
		t.Errorf("infos() must hide the closed record; got %+v", infos)
	}
	shown, count := sess.subagents.listAgents(sess.ID(), "", true)
	if count != 1 || len(shown) != 1 || shown[0].ID != agentID {
		t.Fatalf("include_closed must surface the closed record; got count=%d agents=%+v", count, shown)
	}
	if !shown[0].Closed {
		t.Errorf("closed record closed = false, want true")
	}
	if shown[0].Status != SubagentCompleted {
		t.Errorf("closed record status = %q, want %q", shown[0].Status, SubagentCompleted)
	}
	if shown[0].CloseTimedOut {
		t.Errorf("close_timed_out must be false for a clean close")
	}
}

// TestRetention_FailLoudAtCap fills the retention cap with UNCONSUMED terminal
// records (none reclaimable), then asserts the next spawn fails loudly naming the
// remedy and does NOT track a new child (no leak — the created session is Closed
// before the error return).
//
// Load-bearing: if the cap were not enforced, spawn would succeed and the tracked
// count would grow; if the error did not name the remedy, the message assertion
// fails.
func TestRetention_FailLoudAtCap(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	env := &cleanupCountingEnv{ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(dir)}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), env, SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.subagents.maxRetainedTerminal = 2
	ended := time.Now()
	trackSyntheticChild(t, sess, "t1", SubagentCompleted, false, false, ended, false)
	trackSyntheticChild(t, sess, "t2", SubagentFailed, false, false, ended, false)

	before := countTracked(sess.subagents)
	cleanupsBefore := env.count()

	spawnRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c1-spawn",
		Name:      "spawn_agent",
		Arguments: json.RawMessage(`{"task":"overflow"}`),
	})
	if !spawnRes.IsError {
		t.Fatalf("spawn at cap must fail; got success: %s", spawnRes.Output)
	}
	msg := strings.ToLower(spawnRes.Output)
	if !strings.Contains(msg, "close_agent") || !strings.Contains(msg, "wait") {
		t.Errorf("cap error must name the remedy (close_agent/wait); got %q", spawnRes.Output)
	}

	// No new child was tracked: the spawn was rejected before track.
	if after := countTracked(sess.subagents); after != before {
		t.Errorf("failed spawn must not track a child: tracked %d → %d", before, after)
	}
	// No leak: the already-created child Session was Closed before the error return,
	// which invokes the (shared) env's Cleanup exactly once during the spawn call.
	if got := env.count() - cleanupsBefore; got != 1 {
		t.Errorf("failed spawn must Close the created child session (env Cleanup delta = %d, want 1)", got)
	}
}

// TestRetention_GCReclaimsConsumedFirst fills the cap with terminal records, one of
// which is CONSUMED, and asserts the next spawn succeeds by reclaiming the consumed
// record (it is removed) while the new child is tracked.
//
// Load-bearing: if GC did not reclaim the consumed record, the spawn would fail at
// the cap; the assertions on the consumed record being gone and the new child being
// present both break if reclamation is wrong.
func TestRetention_GCReclaimsConsumedFirst(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("fresh child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.subagents.maxRetainedTerminal = 2
	older := time.Now()
	newer := older.Add(time.Minute)

	// "consumed" is the only reclaimable record; it gets its own counting env so the
	// test can prove GC eviction CLOSES its (still-live) child Session — a consumed
	// completed child is not closed when its run finishes, so evicting it must close
	// it to avoid a leak. "held" is unconsumed and must stay.
	consumedEnv := &cleanupCountingEnv{ExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	consumedChild, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), consumedEnv, SessionConfig{MaxSubagentDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	consumedEnded := older
	sess.subagents.track(&subagent{id: "consumed", sess: consumedChild, status: SubagentCompleted, resultConsumed: true, endedAt: &consumedEnded})
	trackSyntheticChild(t, sess, "held", SubagentCompleted, false, false, newer, false)

	cleanupsBefore := consumedEnv.count()
	newID := spawnCompletedChild(t, sess, "c1", "fresh")

	if got := sess.getSub("consumed"); got != nil {
		t.Errorf("consumed terminal record must be reclaimed; still tracked: %+v", got)
	}
	if got := consumedEnv.count() - cleanupsBefore; got != 1 {
		t.Errorf("GC eviction must Close the consumed child's session (env Cleanup delta = %d, want 1)", got)
	}
	if got := sess.getSub("held"); got == nil {
		t.Errorf("unconsumed terminal record must be retained")
	}
	if got := sess.getSub(newID); got == nil {
		t.Errorf("newly spawned child must be tracked")
	}
}

// TestRetention_ClosingDoesNotCountTowardCap fills the manager with close-timed-out
// records beyond the cap value, then asserts a spawn still succeeds —
// close_timed_out records never deadlock spawns (a stuck close must not wedge the
// cap).
//
// Load-bearing: if close_timed_out records were counted, the count (3) would exceed
// the cap (2) and the spawn would fail.
func TestRetention_ClosingDoesNotCountTowardCap(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("ok child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.subagents.maxRetainedTerminal = 2
	zero := time.Time{}
	trackSyntheticChild(t, sess, "cl1", SubagentCompleted, false, false, zero, true)
	trackSyntheticChild(t, sess, "cl2", SubagentCompleted, false, false, zero, true)
	trackSyntheticChild(t, sess, "cl3", SubagentCompleted, false, false, zero, true)

	newID := spawnCompletedChild(t, sess, "c1", "should-succeed")
	if got := sess.getSub(newID); got == nil {
		t.Errorf("spawn must succeed when only close_timed_out records are present")
	}
}

// TestParentClose_DrainsAll asserts the parent Session.Close path drains and closes
// every child — including a retained closed record — clearing the map. drainForClose
// collects under the manager mutex and the caller closes children OUTSIDE it; this
// test verifies that path still works with retained closed records present.
//
// Load-bearing: if drainForClose left retained records behind, the post-close map
// would be non-empty.
func TestParentClose_DrainsAll(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("drained child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A real child closed-and-retained, plus a hand-built closed record.
	agentID := spawnCompletedChild(t, sess, "c1", "do work")
	closeRes := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "c2-close",
		Name:      "close_agent",
		Arguments: json.RawMessage(fmt.Sprintf(`{"agent_id":%q}`, agentID)),
	})
	if closeRes.IsError {
		t.Fatalf("close_agent error: %s", closeRes.Output)
	}
	if got := sess.getSub(agentID); got == nil {
		t.Fatal("precondition: closed child must be retained before parent close")
	}

	sess.Close()

	if n := countTracked(sess.subagents); n != 0 {
		t.Errorf("parent close must drain all records (incl. retained closed); %d remain", n)
	}
	if got := sess.getSub(agentID); got != nil {
		t.Errorf("retained closed record must be drained on parent close; still present: %+v", got)
	}
}

// TestRetention_GCEvictsClosedBeforeConsumed guards the class-priority ordering in
// the reserveSlot comparator: a closed record must be evicted BEFORE a consumed
// SubagentCompleted record even when the closed record has a NEWER endedAt.
// If the comparator were sorted by endedAt alone, the newer-but-closed record would
// survive and the older-but-consumed record would be (wrongly) evicted.
//
// Setup: maxRetainedTerminal = 2, two counted reclaimable records:
//   - "cls" — closed,    NEWER endedAt  (should be evicted: class priority)
//   - "cns" — SubagentCompleted, consumed, OLDER endedAt  (should be retained)
//
// Trigger: one additional spawn drives reserveSlot to evict exactly one slot.
// Assert: "cls" gone, "cns" still tracked — class priority dominates endedAt order.
func TestRetention_GCEvictsClosedBeforeConsumed(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("new child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.subagents.maxRetainedTerminal = 2
	older := time.Now()
	newer := older.Add(time.Minute)

	// "cns": consumed completed — older endedAt, reclaimable but LOWER class priority.
	trackSyntheticChild(t, sess, "cns", SubagentCompleted, false, true, older, false)
	// "cls": closed — newer endedAt, reclaimable with HIGHER class priority.
	trackSyntheticChild(t, sess, "cls", SubagentCompleted, true, false, newer, false)

	// counted == maxRetainedTerminal (2), so the next spawn triggers exactly one eviction.
	newID := spawnCompletedChild(t, sess, "c1", "trigger-gc")

	if got := sess.getSub("cls"); got != nil {
		t.Errorf("closed record must be evicted before consumed record (class priority); 'cls' still tracked")
	}
	if got := sess.getSub("cns"); got == nil {
		t.Errorf("consumed completed record must be retained when a closed record was available to evict; 'cns' gone")
	}
	if got := sess.getSub(newID); got == nil {
		t.Errorf("newly spawned child must be tracked after eviction")
	}
}

// TestRetention_GCEvictsOldestWithinClass guards the within-class oldest-first
// tie-break in the reserveSlot comparator: when two consumed SubagentCompleted
// records compete for eviction, the one with the OLDER endedAt must go first.
//
// Setup: maxRetainedTerminal = 2, two consumed completed records with different endedAt.
// Trigger: one spawn drives reserveSlot to evict exactly one slot.
// Assert: the older-endedAt record is gone; the newer-endedAt record is retained.
func TestRetention_GCEvictsOldestWithinClass(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("new child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	sess.subagents.maxRetainedTerminal = 2
	older := time.Now()
	newer := older.Add(time.Minute)

	// Both are consumed completed — same class, tie-broken by endedAt oldest-first.
	trackSyntheticChild(t, sess, "old", SubagentCompleted, false, true, older, false)
	trackSyntheticChild(t, sess, "new", SubagentCompleted, false, true, newer, false)

	newID := spawnCompletedChild(t, sess, "c1", "trigger-gc")

	if got := sess.getSub("old"); got != nil {
		t.Errorf("oldest-endedAt record must be evicted first within same class; 'old' still tracked")
	}
	if got := sess.getSub("new"); got == nil {
		t.Errorf("newer-endedAt record must be retained when older was available to evict; 'new' gone")
	}
	if got := sess.getSub(newID); got == nil {
		t.Errorf("newly spawned child must be tracked after eviction")
	}
}

// TestInfo_SurfacesCloseTimedOut proves the close_timed_out flag set on the close
// timeout path reaches the info/list record. A close-timed-out record stays visible
// in the default list (closed is still false), keeps its run outcome in status, and
// its CloseTimedOut must reflect the field.
//
// Load-bearing: if infoLocked dropped the field, CloseTimedOut would be false here.
func TestInfo_SurfacesCloseTimedOut(t *testing.T) {
	fe := &fakeEmit{}
	m := newSubagentManager(fe.emit, nil)
	m.track(&subagent{id: "stuck", status: SubagentCompleted, closeTimedOut: true})

	agents, count := m.listAgents("01ROOT", "", false)
	if count != 1 || len(agents) != 1 {
		t.Fatalf("close-timed-out record must appear in default list; got count=%d agents=%+v", count, agents)
	}
	if agents[0].Status != SubagentCompleted {
		t.Errorf("status = %q, want %q (run outcome preserved)", agents[0].Status, SubagentCompleted)
	}
	if !agents[0].CloseTimedOut {
		t.Errorf("close_timed_out must surface as true on a timed-out close")
	}
	if agents[0].Closed {
		t.Errorf("closed must be false for a close-timed-out record (close not confirmed)")
	}
}

// TestSubagentRecord_NoReasonKey_ClosedFlag pins the merged wire model: a running
// record marshals with status="running" and NO "reason" key; a closed record keeps
// its run outcome in status with closed=true (close never clobbers the outcome).
func TestSubagentRecord_NoReasonKey_ClosedFlag(t *testing.T) {
	running := subagentResult{AgentID: "a", Status: SubagentRunning}
	b, _ := json.Marshal(running)
	if strings.Contains(string(b), "\"reason\"") {
		t.Fatalf("running result must not carry a reason key: %s", b)
	}

	closed := subagentResult{AgentID: "a", Status: SubagentCompleted, Closed: true, Success: true}
	b2, _ := json.Marshal(closed)
	if !strings.Contains(string(b2), "\"status\":\"completed\"") || !strings.Contains(string(b2), "\"closed\":true") {
		t.Fatalf("closed record must keep its outcome in status and set closed: %s", b2)
	}
}
