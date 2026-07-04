# Attention & Status Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serf daemons report `awaiting` on the existing status field whenever the ball is in the user's court, and every surface (sidebar NeedsYou tier, badges, OS notifications, TUI) lights up from that truth — plus a first-class red `errored` lane.

**Architecture:** Daemon-truth (spec v5, `docs/superpowers/specs/2026-07-03-attention-status-model-design.md` — READ IT FIRST). The agent arms `awaiting` at the drain-loop settle (only reachable on clean turn completion); a wire-state helper reports `active` while delegated children run; the hub un-collapses `systemError→errored` in `NormalizeState` and adds an attention watcher that broadcasts `serf/attention/changed`; notifications.js goes event-driven.

**Tech Stack:** Go (agent + server + hub modules under go.work), Go html/template, vanilla JS + JSDOM tests (`cmd/serf-hub/jstest`), bubbletea/lipgloss TUI.

**Working rules for every task:**
- Run Go tests per-module: `cd <module> && go test ./<pkg>/ -run <Name> -count=1` (modules: repo root `.`, `agent`). Full gates: `make test-short` and `make lint` from repo root before declaring the plan done.
- jstest: `cd cmd/serf-hub/jstest && sh run-all.sh` (or `node test-<name>.js` for one file).
- Commit after every green task with the message given in the task.
- NEVER use `git add -A`. Add the named files only.

---

### Task 1: `SessionAwaiting` + the settle-state decision function (agent)

**Files:**
- Modify: `agent/session_state.go` (add const + pure decision func + wire-state helper)
- Modify: `agent/session_goal.go` (make `settleGoalOnIdle` report whether it kicked)
- Test: `agent/session_awaiting_test.go` (new)

- [ ] **Step 1: Write the failing table test for the pure decision function**

Create `agent/session_awaiting_test.go`:

```go
package agent

import "testing"

func TestSettleTerminalState(t *testing.T) {
	cases := []struct {
		name                                            string
		hadOutput, goalKicked, notifsPending, queuePending, childrenLive bool
		want                                            SessionState
	}{
		{"clean turn with output arms awaiting", true, false, false, false, false, SessionAwaiting},
		{"no user-visible output stays idle", false, false, false, false, false, SessionIdle},
		{"goal kick suppresses", true, true, false, false, false, SessionIdle},
		{"pending notifications suppress", true, false, true, false, false, SessionIdle},
		{"queued input suppresses", true, false, false, true, false, SessionIdle},
		{"live children suppress", true, false, false, false, true, SessionIdle},
		{"all suppressors at once", true, true, true, true, true, SessionIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := settleTerminalState(c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive)
			if got != c.want {
				t.Fatalf("settleTerminalState(%v,%v,%v,%v,%v) = %q, want %q",
					c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it — must fail to compile**

Run: `cd agent && go test ./ -run TestSettleTerminalState -count=1`
Expected: FAIL — `undefined: SessionAwaiting`, `undefined: settleTerminalState`.

- [ ] **Step 3: Implement in `agent/session_state.go`**

Add to the const block (after `SessionProcessing`):

```go
	// SessionAwaiting indicates the session is idle with the ball in the
	// user's court: the last completed turn ended with agent output and no
	// autonomous work (goal kick, pending notifications, queued input, live
	// child subagents) is in flight. It is the daemon-truth source for the
	// hub's "needs you" attention state (spec: attention-status-model v5).
	SessionAwaiting SessionState = "awaiting"
```

Add at the end of the file:

```go
// settleTerminalState decides the terminal session state at the drain-loop
// settle. It runs ONLY on the clean-completion path (interrupted and failed
// turns return from ProcessInputKind before the settle), so turn outcome is
// implied by reachability. awaiting arms only when the turn produced
// user-visible output and nothing autonomous will move the session next.
func settleTerminalState(hadOutput, goalKicked, notifsPending, queuePending, childrenLive bool) SessionState {
	if !hadOutput || goalKicked || notifsPending || queuePending || childrenLive {
		return SessionIdle
	}
	return SessionAwaiting
}
```

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./ -run TestSettleTerminalState -count=1`
Expected: PASS

- [ ] **Step 5: Write the failing test for `settleGoalOnIdle` returning kicked**

Append to `agent/session_awaiting_test.go`:

```go
func TestSettleGoalOnIdle_ReportsKick(t *testing.T) {
	sess := newTestSessionForState(t)
	// No goal set: settle must report kicked=false.
	if kicked := sess.settleGoalOnIdle(); kicked {
		t.Fatal("settleGoalOnIdle with no goal reported kicked=true")
	}
	// Active goal + wired kick: settle must kick and report it.
	kickCh := make(chan string, 1)
	sess.SetKickFunc(func(p string) { kickCh <- p })
	if _, err := sess.SetGoal(nil, "test objective"); err != nil { //nolint:staticcheck // ctx unused by SetGoal
		t.Fatal(err)
	}
	<-kickCh // drain the SetGoal idle-kick itself
	sess.mu.Lock()
	sess.goalInTurn = true // simulate the turn-tail window
	sess.mu.Unlock()
	if kicked := sess.settleGoalOnIdle(); !kicked {
		t.Fatal("settleGoalOnIdle with an active goal did not report kicked=true")
	}
	select {
	case <-kickCh:
	default:
		t.Fatal("settleGoalOnIdle reported kicked but no kick arrived")
	}
}
```

And add the tiny session constructor helper at the top of the test file (mirrors `session_lifecycle_test.go`'s pattern):

```go
func newTestSessionForState(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}
```

Add the imports the file now needs: `"primeradiant.com/serf/llm"` and `"primeradiant.com/serf/agent/internal/..."` — copy the exact import set from `agent/session_lifecycle_test.go` for `llm` and `execenv` (it imports `primeradiant.com/serf/agent/execenv` or similar; use whatever path `session_lifecycle_test.go` uses for `execenv.NewLocalExecutionEnvironment`).

- [ ] **Step 6: Run — fails (settleGoalOnIdle returns nothing)**

Run: `cd agent && go test ./ -run TestSettleGoalOnIdle_ReportsKick -count=1`
Expected: FAIL to compile — `sess.settleGoalOnIdle() used as value`.

- [ ] **Step 7: Change `settleGoalOnIdle` in `agent/session_goal.go`**

Replace the existing function with (body identical, signature + return added):

```go
// settleGoalOnIdle runs at the drain loop's idle transition. Under s.mu it clears
// the in-turn flag and, if an active goal was set in the turn-tail window (after
// the gate's store read but before the flag clear), captures the first continuation
// prompt so the goal is kicked rather than stranded active-but-idle until the next
// user message (spec §7). The kick is issued outside the lock. Mutually exclusive
// on s.mu with SetGoal's "set goal + read flag", so the goal is kicked exactly once.
// It reports whether it kicked, so the settle-state upgrade knows autonomy is in
// flight (attention-status-model v5: a kicked goal suppresses awaiting).
func (s *Session) settleGoalOnIdle() bool {
	s.mu.Lock()
	s.goalInTurn = false
	kick := s.kickFunc
	var prompt string
	if kick != nil {
		if snap, ok := s.getOrCreateGoalStore().Snapshot(); ok && snap.Status == goal.StatusActive {
			prompt = goal.Render(snap.Objective)
		}
	}
	s.mu.Unlock()
	if prompt != "" {
		kick(prompt)
		return true
	}
	return false
}
```

The existing call site in `session_lifecycle.go` (`s.settleGoalOnIdle()`) still compiles — the return is captured in Task 2.

- [ ] **Step 8: Run both tests + the agent package**

Run: `cd agent && go test ./ -run 'TestSettleTerminalState|TestSettleGoalOnIdle_ReportsKick' -count=1`
Expected: PASS. Then `cd agent && go build ./...` — expected: clean.

- [ ] **Step 9: Commit**

```bash
git add agent/session_state.go agent/session_goal.go agent/session_awaiting_test.go
git commit -m "feat(agent): SessionAwaiting state + settle decision function"
```

---

### Task 2: Arm `awaiting` at the drain-loop settle (agent)

**Files:**
- Modify: `agent/session_lifecycle.go` (the settle block only — the block that calls `s.settleGoalOnIdle()` then emits `EventSessionEnd` with `Reason: "input_complete"`)
- Modify: `agent/session_state.go` (add `armAwaitingAtSettle` + `autonomyInFlight`)
- Test: `agent/session_awaiting_test.go`

- [ ] **Step 1: Write the failing lifecycle tests**

Append to `agent/session_awaiting_test.go` (uses the `fakeAdapter`/`finalResponse`/`collectEvents` helpers that already exist in the agent test package):

```go
func TestProcessInput_CleanCompletionArmsAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("done") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	eventsPtr, mu, doneCh := collectEvents(sess)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after clean completion = %q, want %q", got, SessionAwaiting)
	}
	sess.Close()
	<-doneCh
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *eventsPtr {
		if ev.Kind == events.EventSessionEnd {
			d, ok := ev.Data.(events.SessionEndData)
			if !ok {
				t.Fatal("SessionEnd data type")
			}
			if d.Reason == "input_complete" && d.State != string(SessionAwaiting) {
				t.Fatalf("SessionEnd.State = %q, want %q", d.State, SessionAwaiting)
			}
		}
	}
}

func TestProcessInput_InterruptStaysIdle(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	blocker := make(chan struct{})
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { <-blocker; return finalResponse("late") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _, _ = sess.ProcessInput(ctx, "hello", nil); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(blocker)
	<-done
	if got := sess.State(); got == SessionAwaiting {
		t.Fatalf("interrupted turn must not arm awaiting; state = %q", got)
	}
}

func TestProcessInput_NextInputClearsAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("one") },
		func(req llm.Request) llm.Response { return finalResponse("two") },
	}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want awaiting before second input", got)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.State(); got != SessionAwaiting {
		t.Fatalf("state after second clean turn = %q, want awaiting again", got)
	}
}
```

NOTE for the implementer: `ProcessInput` must ACCEPT input while state is `SessionAwaiting`. Search `agent/` for any guard comparing `s.state` to `SessionIdle` as an input precondition (e.g. `processInputKindWithProvenance` only rejects `closingOrClosedLocked`; also check `Enqueue`, `Close`, self-compaction triggers via `grep -n 'SessionIdle' agent/*.go`). Any guard that means "not busy" must accept `SessionAwaiting` too. Add a one-line fix per site with a test if any site rejects awaiting; per the spec's round-3 B11 review, the known gates key on `SessionProcessing`, so expect zero-to-few sites.

- [ ] **Step 2: Run — first test fails (state stays idle)**

Run: `cd agent && go test ./ -run 'TestProcessInput_CleanCompletionArmsAwaiting|TestProcessInput_InterruptStaysIdle|TestProcessInput_NextInputClearsAwaiting' -count=1`
Expected: `TestProcessInput_CleanCompletionArmsAwaiting` FAILS (`state = "idle", want "awaiting"`); the interrupt test may already pass.

- [ ] **Step 3: Implement the settle upgrade**

Add to `agent/session_state.go`:

```go
// autonomyInFlight reports whether autonomous work will move this session
// without user input: pending job notifications, queued input, or live child
// subagents. Reads take each signal's own lock sequentially — never nested —
// per the settle lock discipline (spec v5). A restored-but-unkicked goal is
// deliberately NOT autonomy: nothing will move until the user acts, and amber
// is what surfaces that stall.
func (s *Session) autonomyInFlight() bool {
	if s.peekNotifications() > 0 {
		return true
	}
	if s.QueueDepth() > 0 {
		return true
	}
	return len(s.liveSubagentSessions()) > 0
}

// armAwaitingAtSettle upgrades idle -> awaiting at the drain-loop settle when
// settleTerminalState says the ball is in the user's court. It runs after
// settleGoalOnIdle (so the goal kick is known) and before the EventSessionEnd
// emit (so the emitted State carries the upgrade). The upgrade respects the
// same closed-guard as finishProcessingAtBoundary and only ever upgrades from
// SessionIdle, so interrupt/failure paths (which never reach the settle) and
// closed sessions are untouched.
func (s *Session) armAwaitingAtSettle(hadOutput, goalKicked bool) {
	target := settleTerminalState(hadOutput, goalKicked,
		s.peekNotifications() > 0, s.QueueDepth() > 0, len(s.liveSubagentSessions()) > 0)
	if target != SessionAwaiting {
		return
	}
	s.mu.Lock()
	if s.state == SessionIdle && !s.closingOrClosedLocked() {
		s.state = SessionAwaiting
	}
	s.mu.Unlock()
}
```

If `liveSubagentSessions` is unexported-with-lock semantics that differ (check its definition — it is used bare in `agent/jobs_nested.go:487`), call it exactly as jobs_nested.go does.

In `agent/session_lifecycle.go`, change the settle block. Current code:

```go
		s.settleGoalOnIdle()
		s.mu.Lock()
		if !s.sessionEndEmitted {
```

New code:

```go
		goalKicked := s.settleGoalOnIdle()
		s.armAwaitingAtSettle(strings.TrimSpace(strings.Join(outputs, "\n")) != "", goalKicked)
		s.mu.Lock()
		if !s.sessionEndEmitted {
```

(The emit block below it already reads `state := s.state` under the same lock and emits `State: string(state)` — the upgrade flows into `EventSessionEnd` with zero further changes.)

- [ ] **Step 4: Run the three tests — pass; then the whole agent package**

Run: `cd agent && go test ./ -run 'TestProcessInput_' -count=1` → the three new tests PASS.
Run: `cd agent && go test ./... -count=1` → expected PASS. If pre-existing tests assert `SessionIdle` after a clean `ProcessInput`, update those assertions to `SessionAwaiting` — that is the intended behavior change (list each updated test in the commit message). If a pre-existing test fails for any OTHER reason, STOP and re-examine; do not paper over.

- [ ] **Step 5: Commit**

```bash
git add agent/session_state.go agent/session_lifecycle.go agent/session_awaiting_test.go
git commit -m "feat(agent): arm awaiting at the drain-loop settle

Clean turn completion with output and no autonomy in flight upgrades
idle->awaiting before the EventSessionEnd emit. Interrupt/failure paths
never reach the settle, so they stay idle by construction."
```

---

### Task 3: Wire state — delegating parents read `active` (agent + server + serve loop)

**Files:**
- Modify: `agent/session_state.go` (add `WireState`)
- Modify: `agent/session_lifecycle.go` (emit wire state in the settle's `EventSessionEnd`)
- Modify: `cmd/serf/serve.go` (mirror wire state)
- Test: `agent/session_awaiting_test.go`, `server/server_test.go` (or a new `server/awaiting_status_test.go`)

- [ ] **Step 1: Write the failing test for `WireState`**

Append to `agent/session_awaiting_test.go`:

```go
func TestWireState_ChildrenInFlightReadsActive(t *testing.T) {
	sess := newTestSessionForState(t)
	// Idle with no autonomy: wire state == raw state.
	if got := sess.WireState(); got != string(SessionIdle) {
		t.Fatalf("WireState idle = %q", got)
	}
	// Simulate a pending job notification (autonomy in flight while idle):
	sess.enqueueJobNotification(jobNotification{})
	if got := sess.WireState(); got != string(SessionProcessing) {
		t.Fatalf("WireState with pending notification = %q, want %q", got, SessionProcessing)
	}
}
```

(If `jobNotification{}`'s zero value is unusable, construct the minimal literal its fields require — read the struct definition next to `pendingJobNotifs` in `agent/session.go`.)

- [ ] **Step 2: Run — fails to compile (`WireState` undefined)**

Run: `cd agent && go test ./ -run TestWireState_ -count=1`

- [ ] **Step 3: Implement `WireState` in `agent/session_state.go`**

```go
// WireState is the externally-reported session state. It equals State()
// except for one honest override: an idle session whose autonomy is still in
// flight (live child subagents, undelivered job notifications, queued input)
// reads as "active" — a delegating parent is working through its children,
// not settled (spec v5, round-4 A6). awaiting never coexists with autonomy
// (the settle suppressors guarantee it), so this only ever upgrades idle.
func (s *Session) WireState() string {
	state := s.State()
	if state == SessionIdle && s.autonomyInFlight() {
		return string(SessionProcessing)
	}
	return string(state)
}
```

- [ ] **Step 4: Make the settle emit + serve mirror agree (kills the bridge/serve race)**

In `agent/session_lifecycle.go`'s settle emit block, the emit currently sends `State: string(state)` where `state := s.state`. Change ONLY the emitted value to route through the same override so the bridge (which forwards `d.State`) and the serve mirror write identical values:

```go
		s.mu.Lock()
		if !s.sessionEndEmitted {
			s.sessionEndEmitted = true
			turns := s.modelResponses
			s.mu.Unlock()
			s.emit(events.EventSessionEnd, events.SessionEndData{
				Reason: "input_complete",
				State:  s.WireState(),
				Turns:  turns,
			})
		} else {
			s.mu.Unlock()
		}
```

(Note: `state := s.state` local goes away; `s.WireState()` re-reads under its own locking AFTER `s.mu.Unlock()` — do not call it while holding `s.mu`.)

In `cmd/serf/serve.go`, change the post-turn mirror line:

```go
				srv.SetState(string(sess.State()))
```

to:

```go
				srv.SetState(sess.WireState())
```

- [ ] **Step 5: Server-level status test**

Add `server/awaiting_status_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestStatusReportsAwaitingAndSendCapability(t *testing.T) {
	srv := NewServer()
	srv.SetState("awaiting")
	srv.SetProcessing(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/status", nil)
	srv.handleStatus(rec, req)
	var got StatusInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "awaiting" {
		t.Fatalf("State = %q, want awaiting", got.State)
	}
	if !got.Capabilities.Send {
		t.Fatal("Send capability must be true for an awaiting session")
	}
	if s := appStatus(got.State, false); s != appwire.ThreadStatusAwaiting {
		t.Fatalf("appStatus(awaiting,false) = %q", s)
	}
}
```

(If `NewServer()` requires args, copy the constructor call from an existing test in `server/server_test.go` — match its exact pattern. `Capabilities.Send` is `!processing && !closed` per `server_handlers.go:275`, so this pins the round-3 B11 verification.)

- [ ] **Step 6: Run**

Run: `cd agent && go test ./ -run 'TestWireState_' -count=1 && cd .. && go test ./server/ -run TestStatusReportsAwaiting -count=1 && go build ./... && cd agent && go build ./...`
Expected: PASS, clean builds.

- [ ] **Step 7: Commit**

```bash
git add agent/session_state.go agent/session_lifecycle.go cmd/serf/serve.go server/awaiting_status_test.go agent/session_awaiting_test.go
git commit -m "feat(agent,server): WireState override — delegating parents report active

EventSessionEnd and the serve-loop mirror both emit WireState, so the
bridge-forward and the mirror write identical values (no last-writer
race), and /status + appwire inherit awaiting/active truthfully."
```

---

### Task 4: Resume recompute — restored agent-last sessions are `awaiting` (agent)

**Files:**
- Modify: `agent/session_init.go` (at the end of the restore path — find `RestoreSessionFromMeta`; the goal-restore "loaded but idle" comment is at ~:405)
- Modify: `agent/session_state.go` (helper)
- Test: `agent/session_awaiting_test.go`

- [ ] **Step 1: Failing test**

```go
func TestRestore_AgentLastTurnResumesAwaiting(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return finalResponse("answer") },
	}})
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "question", nil); err != nil {
		t.Fatal(err)
	}
	id := sess.ID()
	sess.Close()

	c2 := llm.NewClient()
	c2.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSession(c2, dir, id) // use the EXACT restore constructor RestoreSessionFromMeta tests use — copy from an existing restore test
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.State(); got != SessionAwaiting {
		t.Fatalf("restored agent-last session state = %q, want awaiting", got)
	}
}
```

IMPLEMENTER NOTE: the restore constructor name/signature and the `SessionConfig` field for the state dir must be copied from an existing restore test — `grep -rn 'RestoreSessionFromMeta\|RestoreSession(' agent/*_test.go` and mirror the closest one. The behavioral assertion (restored agent-last ⇒ awaiting) is the contract; the harness lines adapt to what exists.

- [ ] **Step 2: Run — fails (idle)**

- [ ] **Step 3: Implement**

In `agent/session_state.go`:

```go
// recomputeRestoredState reruns the settle decision for a restored session:
// if the persisted transcript ends with agent output (the ball was in the
// user's court when the daemon stopped) and no autonomy is in flight, the
// session resumes awaiting rather than idle. Restored active goals are
// deliberately not autonomy — they are not re-kicked on restore ("loaded but
// idle"), so amber is what surfaces the stall (spec v5, round-3 A2).
func (s *Session) recomputeRestoredState() {
	s.mu.Lock()
	agentLast := false
	if n := len(s.history); n > 0 {
		agentLast = s.history[n-1].Kind == schema.TurnAssistant
	}
	idle := s.state == SessionIdle && !s.closingOrClosedLocked()
	s.mu.Unlock()
	if !idle || !agentLast {
		return
	}
	if s.autonomyInFlight() {
		return
	}
	s.mu.Lock()
	if s.state == SessionIdle && !s.closingOrClosedLocked() {
		s.state = SessionAwaiting
	}
	s.mu.Unlock()
}
```

IMPLEMENTER NOTE: verify the assistant-turn kind constant with `grep -n 'Turn.*TurnKind\|TurnAssistant\|TurnUserInput' agent/schema/turn.go` — the spec's review round 3 verified the kinds are USER_INPUT/STEERING/ASSISTANT/TOOL_RESULTS; use the exact Go constant name for the assistant kind (adjust `schema.TurnAssistant` if the actual name differs, e.g. includes a suffix). If the last persisted turn after a completed exchange is a TOOL_RESULTS or other trailing kind rather than ASSISTANT, walk backward past non-{user,steering,assistant} kinds and decide on the first user/steering/assistant turn found (agent-last means that turn is assistant). Write it exactly that way and let the test drive.

Call it at the end of the restore path: find where `RestoreSessionFromMeta` finishes assembling the session (after history + goal restore, before returning) and add `s.recomputeRestoredState()`.

- [ ] **Step 4: Run agent package**

`cd agent && go test ./... -count=1` — PASS (fix any restore-path tests that assert idle after agent-last restore; those assertions change intentionally).

- [ ] **Step 5: Commit**

```bash
git add agent/session_state.go agent/session_init.go agent/session_awaiting_test.go
git commit -m "feat(agent): restored agent-last sessions resume awaiting"
```

---

### Task 5: Hub — `errored` un-collapse in hubcore (NormalizeState, ranks, rollups, NeedsYou + archive filter)

**Files:**
- Modify: `cmd/serf-hub/internal/hubcore/tree.go`
- Test: `cmd/serf-hub/internal/hubcore/tree_test.go`

- [ ] **Step 1: Flip the NormalizeState golden + add rank/tier tests (failing)**

In `tree_test.go`, change the golden case `{"systemError", "awaiting"}` to `{"systemError", "errored"}`.

Append:

```go
func TestAttentionRanks_Errored(t *testing.T) {
	if AttentionRank("errored") <= AttentionRank("awaiting") {
		t.Fatal("errored must outrank awaiting")
	}
	if rollupRank("errored") <= rollupRank("awaiting") {
		t.Fatal("rollupRank: errored must outrank awaiting")
	}
}

func TestNeedsYou_AdmitsErroredAndWarning_RanksErroredFirst(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01AWAIT", UpdatedAt: now.Add(-1 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01WARN", UpdatedAt: now.Add(-2 * time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01AWAIT", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01WARN", Status: appwire.ThreadStatusWarning},
	}
	tree := buildTree(metas, live)
	if len(tree.NeedsYou) != 3 {
		t.Fatalf("NeedsYou len = %d, want 3", len(tree.NeedsYou))
	}
	if tree.NeedsYou[0].ID != "01ERR" || tree.NeedsYou[0].State != "errored" {
		t.Fatalf("[0] = %s/%s, want 01ERR/errored (errors first, real state on node)", tree.NeedsYou[0].ID, tree.NeedsYou[0].State)
	}
	// Then oldest-first among the amber family: WARN (-2h) before AWAIT (-1h).
	if tree.NeedsYou[1].ID != "01WARN" || tree.NeedsYou[2].ID != "01AWAIT" {
		t.Fatalf("amber order = %s,%s want 01WARN,01AWAIT", tree.NeedsYou[1].ID, tree.NeedsYou[2].ID)
	}
}

func TestNeedsYou_ArchivedLiveAwaitingExcluded(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}}}
	live := []LiveEntry{{Entry: rendezvous.Entry{PID: 1}, SessionID: "01ARCH", Status: appwire.ThreadStatusAwaiting}}
	archived := true
	_ = archived
	tree := BuildTree(metas, live, map[ArchiveKey]bool{{Kind: "session", ID: "01ARCH"}: true})
	if len(tree.NeedsYou) != 0 {
		t.Fatalf("archived live awaiting session must not appear in NeedsYou; got %d", len(tree.NeedsYou))
	}
}
```

- [ ] **Step 2: Run — fail**

Run: `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestNormalizeState|TestAttentionRanks_Errored|TestNeedsYou_' -count=1`

- [ ] **Step 3: Implement in `tree.go`**

1. `NormalizeState`: change the systemError case to
```go
	case appwire.ThreadStatusSystemError:
		return "errored" // first-class red error lane (spec v5) — no longer grouped with awaiting
```
2. `AttentionRank`: add `case "errored": return 5` above `"awaiting"`.
3. `rollupRank`: add `case "errored": return 5` above `"awaiting"`.
4. Rollup counting switch: `case "awaiting", "warning", "errored": rollupAttn++`.
5. NeedsYou filter: replace
```go
		if stateFor(le.SessionID) != "awaiting" {
			continue
		}
```
with
```go
		st := stateFor(le.SessionID)
		if st != "awaiting" && st != "warning" && st != "errored" {
			continue
		}
		// Archive suppression: an archived session is out of the inbox even
		// while live — archive is a clearing verb (spec v5, round-4 A4/B7).
		if d := decisionFor(decisions, le.SessionID); d != nil && *d {
			continue
		}
```
(The `decisions` map is already a parameter of `BuildTree` in scope here; confirm the variable name at the call site.)
6. Node construction: `State: "awaiting"` → `State: st`.
7. Tier sort: replace the UpdatedAt-only sort with
```go
	// Errors first, then longest-waiting first within a rank (spec v5).
	sort.SliceStable(needsYou, func(i, j int) bool {
		ri, rj := AttentionRank(needsYou[i].State), AttentionRank(needsYou[j].State)
		if ri != rj {
			return ri > rj
		}
		return needsYou[i].UpdatedAt.Before(needsYou[j].UpdatedAt)
	})
```

- [ ] **Step 4: Run the whole hub module test slice for tree + app_rpc golden churn**

Run: `go test ./cmd/serf-hub/... -count=1 -run 'Tree|NormalizeState|Sidebar|AppRPC|Rpc'` then the full `go test ./cmd/serf-hub/... -count=1`.
Expected: the new tests PASS. Any test iterating systemError→awaiting (`app_rpc_test.go:1108` region, sidebar goldens) fails — update those expectations to `errored` (intended churn; name them in the commit).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/tree.go cmd/serf-hub/internal/hubcore/tree_test.go <other updated test files>
git commit -m "feat(hub): errored is a first-class normalized state; NeedsYou gains archive filter + errors-first sort"
```

---

### Task 6: Hub — errored render lane (template, CSS, subagentDone, stateLabel)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/sidebar.html` (NeedsYou block lines ~23,29; subagentRow glyph)
- Modify: `cmd/serf-hub/web.go` (`subagentDone`)
- Modify: `cmd/serf-hub/web_format.go` (`stateLabel`)
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/web_test.go` (template render assertions — follow the existing sidebar-rendering test pattern found via `grep -n 'needs-you\|NeedsYou' cmd/serf-hub/web_test.go`)

- [ ] **Step 1: Failing Go tests**

Add to the file where existing sidebar template tests live (create `cmd/serf-hub/web_errored_lane_test.go` if none fits):

```go
func TestSubagentDone_ErroredIsNotDone(t *testing.T) {
	fn := sidebarTemplateFuncs["subagentDone"].(func(string) bool)
	if fn("errored") {
		t.Fatal("errored subagent must not fold into Completed(N)")
	}
	if !fn("ended") {
		t.Fatal("ended stays done")
	}
}

func TestStateLabel_ErroredAndNeedsYou(t *testing.T) {
	if got := stateLabel("errored"); got != "Error" {
		t.Fatalf("stateLabel(errored) = %q, want Error", got)
	}
	if got := stateLabel("awaiting"); got != "Needs you" {
		t.Fatalf("stateLabel(awaiting) = %q, want \"Needs you\"", got)
	}
}
```

- [ ] **Step 2: Run — fail.** `go test ./cmd/serf-hub/ -run 'TestSubagentDone_Errored|TestStateLabel_Errored' -count=1`

- [ ] **Step 3: Implement**

`web.go` `subagentDone`: `case "active", "awaiting", "warning", "errored": return false`.

`web_format.go` `stateLabel`: change `case "awaiting": return "Awaiting"` to `return "Needs you"`, and add `case "errored": return "Error"`.

`sidebar.html` NeedsYou block: both hard-coded `data-state="awaiting"` become `data-state="{{.State}}"` (row line ~23 and dot line ~29).

`sidebar.html` `subagentRow` glyph chain: add errored before the terminal branch:
```html
{{if eq .State "active"}}⟳{{else if eq .State "awaiting"}}◆{{else if eq .State "warning"}}✕{{else if eq .State "errored"}}✕{{else}}✓{{end}}
```

`style.css` — add after the `.sb-row[data-state="warning"]` rule (~line 594):
```css
.sb-row[data-state="errored"] {
  background: color-mix(in srgb, var(--error) 5%, transparent);
  border-left-color: var(--error);
}
```
Add to the shape block (~line 984), a distinct triangle so errored ≠ active's disc for colorblind users:
```css
.status-dot[data-state="errored"] {
  border-radius: 0;
  clip-path: polygon(50% 0, 100% 100%, 0 100%); /* triangle — red alone must not be the only channel */
  transform: none;
}
```
Add to the rollup-dot block (~line 671):
```css
.project-rollup-dot[data-state="errored"]  { background: var(--error); }
```
(Verify `--error` exists: `grep -n '\-\-error' cmd/serf-hub/assets/style.css` — it is used at :4322. If the light theme defines a variant, no extra work; the variable resolves per theme.)

- [ ] **Step 4: Run + eyeball**

`go test ./cmd/serf-hub/... -count=1` — PASS (update goldens that embed the old glyph chain or `data-state="awaiting"` literals in NeedsYou fixtures).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/web.go cmd/serf-hub/web_format.go cmd/serf-hub/templates/partials/sidebar.html cmd/serf-hub/assets/style.css cmd/serf-hub/web_errored_lane_test.go <updated goldens>
git commit -m "feat(hub): errored render lane — tier passthrough, row accent, triangle dot, rollup dot, labels, subagent glyph"
```

---

### Task 7: Hub — attention derive + watcher + `serf/attention/changed` broadcast

**Files:**
- Create: `cmd/serf-hub/internal/hubcore/attention.go`
- Create: `cmd/serf-hub/internal/hubcore/attention_test.go`
- Modify: `appwire/types.go` (method const), `appwire/protocol.go` (catalog entry — find the existing entries and mirror one)
- Modify: `cmd/serf-hub/main.go` (wire the watcher loop)
- Modify: `cmd/serf-hub/web_api_archive.go` (poke on archive change)
- Modify: `cmd/serf-hub/web_api_tree.go` (include `attentionSummary` in the `/api/tree` JSON response)

- [ ] **Step 1: Failing tests for derive + differ**

`attention_test.go`:

```go
package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func TestDeriveAttention_SummaryCountsTierEligibleOnly(t *testing.T) {
	now := time.Now()
	metas := []schema.SessionMeta{
		{ID: "01A", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01SUB", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}, IsSubagent: true, ParentSessionID: "01A"},
		{ID: "01ARCH", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01ERR", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
		{ID: "01WORK", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/p/x"}},
	}
	live := []LiveEntry{
		{Entry: rendezvous.Entry{PID: 1}, SessionID: "01A", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 2}, SessionID: "01SUB", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 3}, SessionID: "01ARCH", Status: appwire.ThreadStatusAwaiting},
		{Entry: rendezvous.Entry{PID: 4}, SessionID: "01ERR", Status: appwire.ThreadStatusSystemError},
		{Entry: rendezvous.Entry{PID: 5}, SessionID: "01WORK", Status: appwire.ThreadStatusActive},
	}
	decisions := map[ArchiveKey]bool{{Kind: "session", ID: "01ARCH"}: true}
	m, sum := DeriveAttention(metas, live, decisions, now)
	if sum.NeedsYou != 1 || sum.Error != 1 || sum.Working != 1 {
		t.Fatalf("summary = %+v, want NeedsYou:1 Error:1 Working:1 (subagent + archived excluded)", sum)
	}
	if m["01A"].Level != "needs_you" || m["01ERR"].Level != "error" || m["01WORK"].Level != "working" {
		t.Fatalf("levels = %v", m)
	}
	if _, ok := m["01SUB"]; ok {
		t.Fatal("subagent must not carry attention")
	}
	if _, ok := m["01ARCH"]; ok {
		t.Fatal("archived must not carry attention")
	}
}

func TestAttentionWatcher_DiffEmitsOncePerChangeAndSeedsSilently(t *testing.T) {
	var emitted []AttentionChangedPayload
	w := NewAttentionWatcher(func(p AttentionChangedPayload) { emitted = append(emitted, p) })
	first := map[string]AttentionEntry{"01A": {ID: "01A", Level: "needs_you"}}
	w.Tick(first, AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatalf("first tick must seed silently, emitted %d", len(emitted))
	}
	w.Tick(first, AttentionSummary{NeedsYou: 1})
	if len(emitted) != 0 {
		t.Fatal("no change, no emit")
	}
	second := map[string]AttentionEntry{"01A": {ID: "01A", Level: "working"}}
	w.Tick(second, AttentionSummary{Working: 1})
	if len(emitted) != 1 || len(emitted[0].Changed) != 1 ||
		emitted[0].Changed[0].Level != "working" || emitted[0].Changed[0].PrevLevel != "needs_you" {
		t.Fatalf("emitted = %+v", emitted)
	}
	// A session disappearing (daemon gone) transitions to idle-family: emit with prevLevel.
	w.Tick(map[string]AttentionEntry{}, AttentionSummary{})
	if len(emitted) != 2 || emitted[1].Changed[0].Level != "idle" {
		t.Fatalf("disappearance emit = %+v", emitted)
	}
}
```

- [ ] **Step 2: Run — fail to compile.** `go test ./cmd/serf-hub/internal/hubcore/ -run 'TestDeriveAttention|TestAttentionWatcher' -count=1`

- [ ] **Step 3: Implement `attention.go`**

```go
package hubcore

import (
	"time"

	"primeradiant.com/serf/agent/schema"
)

// AttentionEntry is one live session's derived attention level plus the
// labels notification clients need. Levels: "working" | "needs_you" |
// "error" | "idle" (spec v5 semantics table).
type AttentionEntry struct {
	ID      string `json:"threadId"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Level   string `json:"level"`
}

// AttentionSummary is the authoritative badge count set, computed over the
// tier-eligible population (top-level, unarchived, live) — the same
// definition as the NeedsYou tier by construction.
type AttentionSummary struct {
	NeedsYou int `json:"needsYou"`
	Error    int `json:"error"`
	Working  int `json:"working"`
}

// AttentionChanged is one session's level transition.
type AttentionChanged struct {
	AttentionEntry
	PrevLevel string `json:"prevLevel"`
}

// AttentionChangedPayload is the serf/attention/changed notification body.
type AttentionChangedPayload struct {
	Changed []AttentionChanged `json:"changed"`
	Summary AttentionSummary   `json:"summary"`
}

// attentionLevel maps a normalized UI state to an attention level.
func attentionLevel(normalized string) string {
	switch normalized {
	case "active":
		return "working"
	case "awaiting", "warning":
		return "needs_you"
	case "errored":
		return "error"
	default:
		return "idle"
	}
}

// DeriveAttention computes the attention map + summary over the same inputs
// BuildTree consumes. Only tier-eligible sessions (live, top-level,
// unarchived) carry attention; everything else is absent from the map
// (equivalently: idle). Cheap by construction — in-memory inputs only, no
// disk, no BuildTree (spec v5 watcher section).
func DeriveAttention(metas []schema.SessionMeta, live []LiveEntry, decisions map[ArchiveKey]bool, now time.Time) (map[string]AttentionEntry, AttentionSummary) {
	metaByID := make(map[string]*schema.SessionMeta, len(metas))
	for i := range metas {
		metaByID[metas[i].ID] = &metas[i]
	}
	out := make(map[string]AttentionEntry, len(live))
	var sum AttentionSummary
	for _, le := range live {
		if le.SessionID == "" {
			continue
		}
		meta := metaByID[le.SessionID]
		if meta != nil && meta.IsSubagent {
			continue
		}
		archived := false
		lastActivity := now
		if meta != nil {
			lastActivity = OrderUpdatedAt(meta.UpdatedAt, meta.CreatedAt)
		}
		if classifySession(decisionFor(decisions, le.SessionID), lastActivity, now) == "archived" {
			archived = true
		}
		if archived {
			continue
		}
		level := attentionLevel(NormalizeState(le.Status))
		e := AttentionEntry{ID: le.SessionID, Level: level}
		if meta != nil {
			e.Title = nodeTitle(*meta, nodeKind(*meta))
			e.Project = projectName(*meta)
		} else {
			e.Title = ShortID(le.SessionID)
		}
		out[le.SessionID] = e
		switch level {
		case "needs_you":
			sum.NeedsYou++
		case "error":
			sum.Error++
		case "working":
			sum.Working++
		}
	}
	return out, sum
}

// AttentionWatcher diffs successive attention maps and emits one payload per
// changed set. The first tick seeds silently (hub restart must not re-notify —
// spec v5). Not safe for concurrent Tick calls; the caller owns a single loop.
type AttentionWatcher struct {
	prev   map[string]AttentionEntry
	seeded bool
	emit   func(AttentionChangedPayload)
}

// NewAttentionWatcher wires the emit callback (BroadcastAll in production,
// a recorder in tests).
func NewAttentionWatcher(emit func(AttentionChangedPayload)) *AttentionWatcher {
	return &AttentionWatcher{emit: emit}
}

// Tick diffs cur against the previous map and emits transitions, including
// disappearances (session gone ⇒ level "idle").
func (w *AttentionWatcher) Tick(cur map[string]AttentionEntry, sum AttentionSummary) {
	if !w.seeded {
		w.prev = cur
		w.seeded = true
		return
	}
	var changed []AttentionChanged
	for id, e := range cur {
		prev, had := w.prev[id]
		if !had || prev.Level != e.Level {
			pl := "idle"
			if had {
				pl = prev.Level
			}
			changed = append(changed, AttentionChanged{AttentionEntry: e, PrevLevel: pl})
		}
	}
	for id, prev := range w.prev {
		if _, still := cur[id]; !still {
			gone := prev
			gone.Level = "idle"
			changed = append(changed, AttentionChanged{AttentionEntry: gone, PrevLevel: prev.Level})
		}
	}
	w.prev = cur
	if len(changed) == 0 {
		return
	}
	w.emit(AttentionChangedPayload{Changed: changed, Summary: sum})
}
```

(`nodeTitle`, `nodeKind`, `projectName`, `ShortID`, `OrderUpdatedAt`, `classifySession`, `decisionFor` all already exist in package hubcore — see tree.go. If `nodeTitle`/`nodeKind` signatures differ, match tree.go's NeedsYou-builder usage exactly.)

- [ ] **Step 4: Run — pass.** Then the method const + wiring.

`appwire/types.go`, in the Notify const block after `NotifySerfLaunchUpdated`:
```go
	NotifySerfAttentionChanged  = "serf/attention/changed"
```
`appwire/protocol.go`: find the catalog entry for `NotifySerfLaunchUpdated` (grep) and add a sibling entry for `serf/attention/changed` with a one-line description: "Hub-derived attention transitions for live sessions plus authoritative badge summary. Hub-originated; never sent by daemons." Then run `make generate` from repo root and confirm `docs/appwire-protocol.md` picked it up (`git diff --stat docs/appwire-protocol.md`).

`cmd/serf-hub/main.go` — after `archive` and the roster/past wiring (grep for `go roster.Watch(ctx)` at ~:209), add the watcher loop:

```go
	attentionPoke := make(chan struct{}, 1)
	pokeAttention := func() {
		select {
		case attentionPoke <- struct{}{}:
		default:
		}
	}
	go func() {
		w := hubcore.NewAttentionWatcher(func(p hubcore.AttentionChangedPayload) {
			appRPCServer.BroadcastAll(appwire.NotifySerfAttentionChanged, p)
		})
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		run := func() {
			decisions, _ := archive.Decisions()
			m, sum := hubcore.DeriveAttention(past.AllMetas(), roster.List(), decisions, time.Now())
			w.Tick(m, sum)
		}
		run()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				run()
			case <-attentionPoke:
				run()
			}
		}
	}()
```

IMPLEMENTER NOTE: the variable names (`appRPCServer`, `past`, `roster`, `archive`, `ctx`) must match main.go's actual locals — read the surrounding wiring and adapt names only, not structure. The appserver instance with `BroadcastAll` is the one constructed for `/rpc` (`newHubAppServer` returns it or it hangs off the web config — grep `BroadcastAll` usage in `app_rpc.go:669` for how to reach it; thread `pokeAttention` into `WebConfig` if the archive handler lives behind it).

`cmd/serf-hub/web_api_archive.go`: after the successful `Archive.Set(...)`, call the poke (thread it in via the server struct/config the handler hangs off — a `PokeAttention func()` field defaulting to nil, guarded `if s.cfg.PokeAttention != nil`).

`cmd/serf-hub/web_api_tree.go`: in the `/api/tree` handler, alongside building the response, compute `_, sum := hubcore.DeriveAttention(metas, live, decisions, time.Now())` from the same inputs it already gathered and add `AttentionSummary: sum` to the response struct (add the field to the response type with `json:"attentionSummary"`; if the response type lives in `hubapi`, add it there with the same tag).

- [ ] **Step 5: Run module tests + generate gate**

`go test ./cmd/serf-hub/... -count=1 && make generate && git diff --exit-code docs/appwire-protocol.md || true` — the doc SHOULD show the new entry as a committed change (add it), and `make lint-generated` must pass after committing.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/attention.go cmd/serf-hub/internal/hubcore/attention_test.go appwire/types.go appwire/protocol.go docs/appwire-protocol.md cmd/serf-hub/main.go cmd/serf-hub/web_api_archive.go cmd/serf-hub/web_api_tree.go <touched config/type files>
git commit -m "feat(hub): attention watcher broadcasts serf/attention/changed; /api/tree carries attentionSummary"
```

---

### Task 8: Web — notifications.js event-driven rewrite

**Files:**
- Modify: `cmd/serf-hub/assets/notifications.js` (first IIFE)
- Modify: `cmd/serf-hub/assets/sidebar.js` (allowlist)
- Modify: `cmd/serf-hub/assets/renderer.js` (one dispatch line in the thread-status handler)
- Modify: `cmd/serf-hub/templates/partials/settings/notifications.html` (copy)
- Test: `cmd/serf-hub/jstest/test-notifications-attention.js` (new), `cmd/serf-hub/jstest/test-notifications-migration.js` (new)

- [ ] **Step 1: Write the failing jstests**

`cmd/serf-hub/jstest/test-notifications-attention.js`:

```js
// Attention-driven notifications: baseline-before-edge, summary-driven
// counts, transition-into needs_you/error fires, focused suppression.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const src = fs.readFileSync("../assets/notifications.js", "utf8");

function boot(opts) {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>serf hub</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => !!(opts && opts.focused);
  let notifHandler = null;
  w.SerfAppwire = {
    onNotification(h) { notifHandler = notifHandler || h; },
    onConnectionRestored() {},
  };
  const fired = [];
  w.Notification = function (title, o) { fired.push({ title, body: (o || {}).body }); };
  w.Notification.permission = "granted";
  // Baseline fetch: /api/tree with an attentionSummary.
  w.fetch = (url) => Promise.resolve({
    json: () => Promise.resolve({ attentionSummary: (opts && opts.summary) || { needsYou: 0, error: 0, working: 0 } }),
  });
  w.localStorage.setItem("serf-hub.notifications", JSON.stringify({ title: true, favicon: true, os: true, sound: false }));
  w.localStorage.setItem("serf-hub.notifications.v", "2");
  w.eval(src);
  return { w, fireNotif: (m, p) => notifHandler && notifHandler(m, p), fired };
}

(async () => {
  // 1) Baseline populates the title count from the summary.
  const a = boot({ summary: { needsYou: 2, error: 0, working: 1 } });
  await new Promise((r) => setTimeout(r, 20));
  if (!a.w.document.title.startsWith("(2) ")) {
    throw new Error("title after baseline = " + a.w.document.title);
  }
  // 2) A changed event updates counts and fires OS on into-needs_you (unfocused).
  a.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01X", title: "T", project: "p", level: "needs_you", prevLevel: "working" }],
    summary: { needsYou: 3, error: 0, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (!a.w.document.title.startsWith("(3) ")) throw new Error("title after delta = " + a.w.document.title);
  if (a.fired.length !== 1) throw new Error("OS fired " + a.fired.length + " times, want 1");
  // 3) Focused tab suppresses OS but still counts.
  const b = boot({ focused: true, summary: { needsYou: 0, error: 0, working: 0 } });
  await new Promise((r) => setTimeout(r, 20));
  b.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01Y", title: "U", project: "p", level: "error", prevLevel: "working" }],
    summary: { needsYou: 0, error: 1, working: 0 },
  });
  await new Promise((r) => setTimeout(r, 5));
  if (b.fired.length !== 0) throw new Error("focused tab must suppress OS");
  if (!b.w.document.title.startsWith("(1) ")) throw new Error("error counts in title: " + b.w.document.title);
  // 4) No baseline yet -> no edge firing (event before fetch resolves).
  const c = boot({ summary: { needsYou: 0, error: 0, working: 0 } });
  c.fireNotif("serf/attention/changed", {
    changed: [{ threadId: "01Z", title: "V", project: "p", level: "needs_you", prevLevel: "idle" }],
    summary: { needsYou: 1, error: 0, working: 0 },
  });
  if (c.fired.length !== 0) throw new Error("no-baseline event must not fire OS");
  console.log("ok");
})().catch((e) => { console.error(e); process.exit(1); });
```

`cmd/serf-hub/jstest/test-notifications-migration.js`:

```js
// Versioned prefs migration: absent blob -> new defaults ON (title+favicon);
// existing partial blob -> absent keys backfilled explicit false.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync("../assets/notifications.js", "utf8");

function boot(pre) {
  const dom = new JSDOM(`<!DOCTYPE html><html><head><title>t</title></head><body></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.document.hasFocus = () => false;
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.fetch = () => Promise.resolve({ json: () => Promise.resolve({ attentionSummary: { needsYou: 0, error: 0, working: 0 } }) });
  if (pre) w.localStorage.setItem("serf-hub.notifications", JSON.stringify(pre));
  w.eval(src);
  return w;
}

const fresh = boot(null);
const freshPrefs = JSON.parse(fresh.localStorage.getItem("serf-hub.notifications"));
if (freshPrefs.title !== true || freshPrefs.favicon !== true || freshPrefs.os !== false || freshPrefs.sound !== false) {
  throw new Error("fresh defaults wrong: " + JSON.stringify(freshPrefs));
}
if (fresh.localStorage.getItem("serf-hub.notifications.v") !== "2") throw new Error("version stamp missing");

const legacy = boot({ os: true });
const legacyPrefs = JSON.parse(legacy.localStorage.getItem("serf-hub.notifications"));
if (legacyPrefs.title !== false || legacyPrefs.favicon !== false || legacyPrefs.os !== true) {
  throw new Error("legacy backfill wrong: " + JSON.stringify(legacyPrefs));
}
console.log("ok");
```

- [ ] **Step 2: Run — both fail.** `cd cmd/serf-hub/jstest && node test-notifications-attention.js; node test-notifications-migration.js`

- [ ] **Step 3: Rewrite the first IIFE of `notifications.js`**

Keep: `PREFS_KEY`, `PLAIN_FAVICON`, `STATE_COLORS` (keyed by level now: `{error: "#f7768e", needs_you: "#e0af68", working: "#7aa2f7"}`), `SECTION_LABELS`/`activeSection`/`syncSettingsHeader`, `setFavicon`/`buildFaviconDataURI`, `fireOsNotification` (parameterize title/id from the changed entry: `new Notification("serf · " + (entry.title || entry.threadId))`, click navigates `/s/<threadId>`), `playTone`, the `serf-hub:notifications-changed` listener, the htmx:afterSettle re-apply, the second IIFE untouched.

Delete: `POLL_MS`, `poll`, `startPolling`, `prevState` transition machinery, `isAlertTransition`, `detectTransitions`, `/api/search` usage.

Add (structure — write it in the file's existing style):

```js
  const PREFS_VERSION_KEY = "serf-hub.notifications.v";
  const DEFAULT_PREFS = { title: true, favicon: true, os: false, sound: false };
  let summary = null; // null until baseline: no edge-firing before it (spec v5)

  function migratePrefs() {
    if (localStorage.getItem(PREFS_VERSION_KEY) === "2") return;
    const raw = localStorage.getItem(PREFS_KEY);
    if (!raw) {
      writePrefs(Object.assign({}, DEFAULT_PREFS));
    } else {
      // Legacy blob: keys the user never touched were implicitly OFF under
      // the old defaults — make that explicit so the new ON defaults do not
      // flip behavior they chose (round-4 A4).
      let cur = {};
      try { cur = JSON.parse(raw) || {}; } catch (e) { cur = {}; }
      for (const k of ["title", "favicon", "os", "sound"]) {
        if (typeof cur[k] !== "boolean") cur[k] = false;
      }
      writePrefs(cur);
    }
    localStorage.setItem(PREFS_VERSION_KEY, "2");
  }

  function applyCounts() {
    const prefs = readPrefs();
    applyTitle(prefs, summary);   // rewrite applyTitle to take summary: count = summary ? summary.needsYou + summary.error : 0
    applyFavicon(prefs, summary); // topLevel = summary.error>0 ? "error" : summary.needsYou>0 ? "needs_you" : summary.working>0 ? "working" : null
  }

  function fetchBaseline() {
    return fetch("/api/tree").then((r) => r.json()).then((resp) => {
      summary = (resp && resp.attentionSummary) || { needsYou: 0, error: 0, working: 0 };
      applyCounts();
    }).catch(() => {});
  }

  function isLeaderTab(cb) {
    // One tab fires OS/sound. Web Locks held-lock election; localStorage
    // heartbeat fallback for environments without navigator.locks.
    if (navigator.locks && navigator.locks.request) {
      navigator.locks.request("serf-hub-os-leader", { ifAvailable: true }, (lock) => {
        if (lock) { cb(true); return new Promise(() => {}); } // hold forever while this tab lives
        cb(false);
        return Promise.resolve();
      }).catch(() => cb(true));
      return;
    }
    cb(true); // no Web Locks: fire (duplicate risk accepted on legacy browsers)
  }
  let leader = false;
  isLeaderTab((v) => { leader = v; });

  function onAttentionChanged(params) {
    const prefs = readPrefs();
    const hadBaseline = summary !== null;
    if (params && params.summary) summary = params.summary;
    applyCounts();
    if (!hadBaseline) return; // no edge-firing without a baseline
    if (document.hasFocus && document.hasFocus()) return;
    if (!leader) return;
    for (const ch of (params && params.changed) || []) {
      const into = ch.level === "needs_you" || ch.level === "error";
      const was = ch.prevLevel === "needs_you" || ch.prevLevel === "error";
      if (into && !was) {
        if (prefs.os) fireOsNotification(ch);
        if (prefs.sound) playTone();
      }
    }
  }

  function init() {
    initialized = true;
    migratePrefs();
    fetchBaseline();
    if (window.SerfAppwire && typeof window.SerfAppwire.onNotification === "function") {
      window.SerfAppwire.onNotification(function (method, params) {
        if (method === "serf/attention/changed") onAttentionChanged(params);
      });
    }
    if (window.SerfAppwire && typeof window.SerfAppwire.onConnectionRestored === "function") {
      window.SerfAppwire.onConnectionRestored(fetchBaseline);
    }
    document.addEventListener("serf-hub:thread-status", fetchBaseline); // own-thread instant reconcile (renderer dispatches on relay status change)
  }
```

IMPLEMENTER NOTES: (a) check `appwire.js`'s `onNotification` handler invocation — if handlers receive only `method`, extend the dispatcher to pass `(method, params)` (sidebar.js's handler ignores extra args, safe); (b) keep `window.serfHubNotifications` test surface exporting `{init, _readPrefs: readPrefs}` plus `_onAttentionChanged: onAttentionChanged` for tests; (c) rewrite `applyTitle(prefs, summary)` to render `(N)` from `summary.needsYou + summary.error`, keeping the section-title logic; (d) `onPrefsChanged` keeps its permission-revert logic but ends with `applyCounts()` instead of `poll()`.

In `cmd/serf-hub/assets/renderer.js`, find the thread-status handler (search `showConnectionBanner` neighborhood / where `THREAD_STATUS_CHANGED` events update the status pill — the handler that processes decoded `["THREAD_STATUS_CHANGED", ...]` events) and add one line after it applies the status:

```js
    document.dispatchEvent(new CustomEvent("serf-hub:thread-status", { detail: { status: status } }));
```

In `cmd/serf-hub/assets/sidebar.js`, add to `notificationAffectsSidebar`:

```js
      case "serf/attention/changed":
```

In `templates/partials/settings/notifications.html`: change the page help line to `Title and favicon default on; OS notification and sound are opt-in. Saved per-browser.`, the OS help to `Native notification when a thread needs you or errors.`, and the sound help to `Short tone on the same transitions.` (unchanged). Title/favicon `<span class="state">` initial text can stay `OFF` — `applySettingsState()` syncs it from prefs on render.

- [ ] **Step 4: Run jstests + full suite**

`cd cmd/serf-hub/jstest && node test-notifications-attention.js && node test-notifications-migration.js && sh run-all.sh`
Expected: new tests OK; fix any existing notification tests that assert the poll (update or delete them with justification in the commit — the poll is intentionally gone).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/assets/notifications.js cmd/serf-hub/assets/sidebar.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/templates/partials/settings/notifications.html cmd/serf-hub/jstest/test-notifications-attention.js cmd/serf-hub/jstest/test-notifications-migration.js
git commit -m "feat(web): event-driven notifications — summary-driven badges, baseline-before-edge, leader election, ON-by-default title/favicon with versioned migration"
```

---

### Task 9: TUI — errored lane + needs-you count

**Files:**
- Modify: `cmd/serf-tui/internal/tuitheme/tokens.go` (StateError token, both themes)
- Modify: `cmd/serf-tui/hub_dashboard_view.go` (stateLabel, statusDot, stateColor, attentionRankLabel, row-tint gate)
- Test: `cmd/serf-tui/hub_dashboard_view_test.go` (append; create if absent following the package's existing test files)

- [ ] **Step 1: Failing test**

```go
func TestErroredLane_TUI(t *testing.T) {
	if got := stateLabel("systemError"); got != "errored" {
		t.Fatalf("stateLabel(systemError) = %q, want errored", got)
	}
	if got := stateLabel("errored"); got != "errored" {
		t.Fatalf("stateLabel(errored) = %q", got)
	}
	if attentionRankLabel("systemError") <= attentionRankLabel("awaiting") {
		t.Fatal("errored must outrank awaiting in the TUI")
	}
	if stateColor("errored") == tuitheme.ActiveTheme().TextDim {
		t.Fatal("errored must not fall through to TextDim")
	}
}
```

- [ ] **Step 2: Run — fail.** `go test ./cmd/serf-tui/ -run TestErroredLane_TUI -count=1`

- [ ] **Step 3: Implement**

`tokens.go`: add `StateError lipgloss.Color` to the Theme struct's state-color group; set `StateError: lipgloss.Color("#d16969")` in `darkTheme` and `StateError: lipgloss.Color("#8a2a2a")` in `lightTheme` (calm reds consistent with the quiet-ink palette).

`hub_dashboard_view.go`:
- `stateLabel`: add `case "systemerror", "errored": return "errored"` (the switch lowercases input).
- `statusDot`: add `case "errored": return "●"`.
- `stateColor`: add `case "errored": return th.StateError`.
- `attentionRankLabel`: add `case "errored": return 5`.
- Row tint gate: `if row.state == "awaiting" || row.state == "active" || row.state == "warning" || stateLabel(row.state) == "errored" {` (use `stateLabel` so raw `systemError` tints too).

Add the `◆N` needs-you count to the dashboard header: find where the dashboard renders its header/title line (grep `hubRow`/`renderDashboard` in `hub_dashboard_view.go`), count rows with `attentionRankLabel(row.state) >= 4` — wait, that includes active (3)? No: rank 4 = awaiting, 5 = errored; warning = 2. Count `stateLabel(row.state)` ∈ {awaiting, warning, errored} and render `◆N` styled with `th.StateAwaiting` when N > 0, appended to the header line. Add a test asserting the count function (extract `needsYouCount(rows []hubRow) int` as a pure function and test it directly).

- [ ] **Step 4: Run.** `go test ./cmd/serf-tui/... -count=1` — PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-tui/internal/tuitheme/tokens.go cmd/serf-tui/hub_dashboard_view.go cmd/serf-tui/hub_dashboard_view_test.go
git commit -m "feat(tui): errored state lane (label, dot, color, rank, tint) + needs-you count"
```

---

### Task 10: Hub — stale-wedge unification (shared helper + targeted watcher probes)

**Files:**
- Modify: `cmd/serf-hub/app_threadlist.go` (extract the heuristic's core)
- Create: `cmd/serf-hub/internal/hubcore/wedge.go` + `wedge_test.go`
- Modify: `cmd/serf-hub/main.go` (watcher applies it to long-active sessions)

- [ ] **Step 1: Read `sanitizeStaleProcessingStatus` (app_threadlist.go:254-283) fully.** Extract its decision core — "given a transcript tail summary, is this active session actually wedged?" — into `hubcore` as:

```go
// StallThreshold is how long a session may report active with no transcript
// growth before the watcher runs the wedge probe. New constant: the wedge
// heuristic itself has no age gate; this mirrors the web client's
// LIVENESS_STALL_MS (3 min) so hub and client agree on "suspiciously quiet"
// (spec v5, round-4 B6).
const StallThreshold = 3 * time.Minute
```

plus a function whose body is MOVED VERBATIM from the private helper(s) `sanitizeStaleProcessingStatus` calls (the transcript-tail read + wedge signature check), renamed `WedgedStatus(...)`, with `app_threadlist.go` re-calling it so behavior is unchanged. The exact signature follows what the existing code passes (Past lookup result + transcript path). This is a mechanical move-and-rename: no logic changes, existing `app_threadlist` tests must stay green untouched.

- [ ] **Step 2: Write `wedge_test.go`** — port/duplicate the assertions the existing sanitize tests make (grep `sanitizeStaleProcessing` in `cmd/serf-hub/*_test.go`) against the moved function directly, plus one test that `StallThreshold == 3*time.Minute`.

- [ ] **Step 3: Watcher integration.** In the main.go watcher `run()` from Task 7, after deriving the map: for each entry with `Level == "working"`, if the roster has reported it active continuously past `StallThreshold` (track first-seen-active timestamps in a local map inside the loop), call the moved `WedgedStatus` probe; on wedge, override that entry's level to `"error"` before `w.Tick`. Add a unit test for the first-seen-active tracking as a pure helper (`staleActives(prevSeen map[string]time.Time, cur map[string]AttentionEntry, now time.Time) []string`).

- [ ] **Step 4: Run.** `go test ./cmd/serf-hub/... -count=1` — PASS, including untouched app_threadlist tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/internal/hubcore/wedge.go cmd/serf-hub/internal/hubcore/wedge_test.go cmd/serf-hub/app_threadlist.go cmd/serf-hub/main.go
git commit -m "refactor(hub): stale-wedge heuristic shared in hubcore; watcher probes long-active sessions"
```

---

### Task 11: Full gates + e2e scenario card

**Files:**
- Create: the e2e scenario card where `docs/agentic-testing.md` says scenario cards live (read that doc first; follow its format exactly)
- Modify: `docs/superpowers/specs/2026-07-03-attention-status-model-design.md` (status line → Implemented, with commit range)

- [ ] **Step 1: Full gates**

```bash
make test-short && make lint
cd cmd/serf-hub/jstest && sh run-all.sh
```
Expected: all green. Fix anything red before proceeding — test output must be pristine.

- [ ] **Step 2: Write the e2e scenario card** (content; adapt the framing to the house format from docs/agentic-testing.md):

> **Scenario: attention — needs-you end to end.** Build serf + serf-hub; start hub with a scratch HOME; spawn a session with prompt "Reply with exactly the word PONG." via /new (cheap model). Assert within 10s of the reply landing: (1) the session's sidebar row shows `data-state="awaiting"`; (2) the NeedsYou tier lists it; (3) the tab title gains "(1)" (title channel defaults on); all WITHOUT opening another tab or refreshing. Then reply "thanks — nothing else." in the open thread and assert the row leaves the NeedsYou tier and the title count clears (own-tab: immediately on the next status event; allow ≤6s for the broadcast reconcile). Interrupt variant: spawn a session with a long `sleep 60` prompt, interrupt it mid-run, assert the row shows idle (never awaiting). Goal variant (integration, not e2e): covered by TestProcessInput settle tests.

- [ ] **Step 3: Update the spec status header and commit**

```bash
git add docs/superpowers/specs/2026-07-03-attention-status-model-design.md <scenario card path>
git commit -m "docs: attention model implemented; e2e scenario card"
```

---

## Self-review checklist (run after writing, before execution)

- Spec coverage: daemon arming ✓(T1-2), wire projection ✓(T3), resume ✓(T4), gating pins ✓(T3 step 5 + T2 note), errored lane hubcore ✓(T5), render lane ✓(T6), watcher+broadcast+summary ✓(T7), notifications rewrite+migration+leader ✓(T8), TUI ✓(T9), wedge ✓(T10), e2e ✓(T11). Out-of-scope items (seen-tracking, ask detection) correctly absent.
- Known judgment calls delegated to the implementer WITH verification instructions (restore-constructor signature, TurnKind constant name, main.go local names, appwire onNotification arity) — each has a grep command and a hard behavioral contract via tests.
- Type consistency: `settleTerminalState` args order matches both call sites; `AttentionEntry.Level` strings match `attentionLevel` outputs match jstest fixtures (`needs_you`/`error`/`working`/`idle`).
