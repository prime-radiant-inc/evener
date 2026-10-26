package agent

import (
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// cleanupCountingEnv wraps an ExecutionEnvironment and counts Cleanup() calls. A
// session calls env.Cleanup() exactly once when it Closes, so this lets a test prove
// a session was Closed without holding a reference to it.
type cleanupCountingEnv struct {
	execenv.ExecutionEnvironment
	cleanups atomic.Int32
}

func (e *cleanupCountingEnv) Cleanup() {
	e.cleanups.Add(1)
	e.ExecutionEnvironment.Cleanup()
}

func (e *cleanupCountingEnv) count() int32 { return e.cleanups.Load() }

func TestSubagentManager_TrackGetRemove(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)

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

func TestSubagentManager_BeginReconstructionWaitsForPendingBeforePublishedChild(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	childID := "child"
	published := &subagent{id: childID}
	pending := &subagentReconstruction{done: make(chan struct{})}
	m.subs[childID] = published
	m.reconstructing[childID] = pending

	existing, gotPending, started, err := m.beginReconstruction(childID)
	if err != nil {
		t.Fatalf("beginReconstruction returned error: %v", err)
	}
	if existing != nil {
		t.Fatalf("existing = %p, want nil while reconstruction is pending", existing)
	}
	if gotPending != pending {
		t.Fatalf("pending = %p, want %p", gotPending, pending)
	}
	if started {
		t.Fatal("started = true, want false while reconstruction is pending")
	}
}

func TestSubagentManager_DrainForCloseReturnsAllAndClears(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)

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

// spawnCompletedChild spawns a non-blocking child that completes immediately and
// waits for its result, returning the child's agent_id. The session's adapter must
// supply a finalResponse step for the child's single run.
func spawnCompletedChild(t *testing.T, sess *Session, callPrefix, task string) string {
	t.Helper()
	_ = callPrefix
	agentID := spawnRuntimeAgent(t, sess, delegateArgs{Task: task})
	waitForRuntimeSubagent(t, sess, agentID)
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

// TestParentClose_DrainsAll asserts the parent Session.Close path drains and closes
// every child — including a retained closed record — clearing the map. drainForClose
// collects under the manager mutex and the caller closes children OUTSIDE it; this
// test verifies that path still works with retained closed records present.
//
// Load-bearing: if drainForClose left retained records behind, the post-close map
// would be non-empty.
func TestParentClose_DrainsAll(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("drained child") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// A real child plus a hand-built closed record.
	agentID := spawnCompletedChild(t, sess, "c1", "do work")
	if got := sess.getSub(agentID); got == nil {
		t.Fatal("precondition: completed child must be retained before parent close")
	}
	trackSyntheticChild(t, sess, "closed-retained", SubagentCompleted, true, false, time.Now().UTC(), false)
	if got := sess.getSub("closed-retained"); got == nil {
		t.Fatal("precondition: closed child record must be retained before parent close")
	}

	sess.Close()

	if n := countTracked(sess.subagents); n != 0 {
		t.Errorf("parent close must drain all records (incl. retained closed); %d remain", n)
	}
	if got := sess.getSub(agentID); got != nil {
		t.Errorf("retained closed record must be drained on parent close; still present: %+v", got)
	}
	if got := sess.getSub("closed-retained"); got != nil {
		t.Errorf("synthetic closed record must be drained on parent close; still present: %+v", got)
	}
}

// TestNewSubagentManagerCapPinsDefault2048 pins the retained-terminal default at
// 2048 and the explicit-override path of the constructor.
func TestNewSubagentManagerCapPinsDefault2048(t *testing.T) {
	t.Parallel()
	m := newSubagentManager(0)
	if m.maxRetainedTerminal != 2048 {
		t.Fatalf("default cap = %d, want 2048", m.maxRetainedTerminal)
	}
	m = newSubagentManager(7)
	if m.maxRetainedTerminal != 7 {
		t.Fatalf("explicit cap = %d, want 7", m.maxRetainedTerminal)
	}
}
