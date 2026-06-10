package agent

import (
	"context"
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
	m := newSubagentManager(fe.emit)

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
	m := newSubagentManager(fe.emit)

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
	m := newSubagentManager(fe.emit)
	m.emit(events.EventJobStarted, nil)
	m.emit(events.EventJobFinished, nil)
	if got := fe.count(); got != 2 {
		t.Fatalf("captured emit forwarded %d events, want 2", got)
	}
}

// spawnCompletedChild spawns a non-blocking child that completes immediately and
// waits for its result, returning the child's agent_id. The session's adapter must
// supply a finalResponse step for the child's single run.
func spawnCompletedChild(t *testing.T, sess *Session, callPrefix, task string) string {
	t.Helper()
	_ = callPrefix
	agentID := spawnRuntimeAgent(t, sess, task, "", 0, "", "", nil)
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

	_, err = sess.spawnAgent(context.Background(), "overflow", "", "", 0, "", "", nil, nil)
	if err == nil {
		t.Fatal("spawn at cap must fail")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "retained delegate limit") || !strings.Contains(msg, "job records finish") {
		t.Errorf("cap error must name the delegate retention remedy; got %q", err.Error())
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

	// A real child plus a hand-built closed record.
	agentID := spawnCompletedChild(t, sess, "c1", "do work")
	if got := sess.getSub(agentID); got == nil {
		t.Fatal("precondition: completed child must be retained before parent close")
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
