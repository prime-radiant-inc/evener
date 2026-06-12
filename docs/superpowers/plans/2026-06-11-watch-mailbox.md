# Watch Mailbox Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the watch-send deadlock class structurally inexpressible by moving all watch delivery onto the session's existing mailbox/drain machinery, then make sidecar observers first-class (read grants), close the feedback-loop configs, and fix the `output_match`/blocking-read UX.

**Architecture:** Event observation (jobManager) becomes persist-only + wake; the only delivery executors are loop-owned drains and the notification-accept path. `jm.send` (the jobManager→Session delivery closure) is deleted. Caller-targeted sends ride the notification queue as render-by-key wake tokens. Spec: `docs/superpowers/specs/2026-06-11-job-control-watch-mailbox-design.md` (read it first; §3 is the invariant every task serves).

**Tech Stack:** Go (module `primeradiant.com/serf/agent`), table-driven tests + `agenttest`, `make test` / `make lint` at repo root gate every phase.

**Branch:** `job-control-spec`, rolling forward. No worktree.

**Execution guardrails (from the June run, still binding):**
- NO fake-green: never weaken, skip, or delete a failing assertion to pass. If a test can't go green honestly, STOP and escalate as BLOCKED.
- Existing watch tests assert synchronous delivery; Task 1.7 re-anchors them with an explicit no-weakening rule (delivered-immediately becomes pending-until-drain + delivered-after-drain — equal or stronger).
- Run `go test ./... -race` in `agent/` for every concurrency-touching task, not just `make test`.
- Commit after every green step. Never `git add -A` without `git status` first.

---

## Phase 1 — The delivery rail (spec §4, atomic: the deadlock fix)

### Task 1.1: `wake` plumbing + `hasPendingWatchSends`

**Files:**
- Modify: `agent/jobs.go` (jobManager struct ~:168, newJobManager ~:156)
- Modify: `agent/session_init.go:116` (wiring)
- Modify: `agent/job_watch.go` (new helper near `pendingWatchSendDeliveries` ~:1817)
- Test: `agent/job_watch_test.go`

- [x] **Step 1: Write the failing test**

```go
func TestJobManagerWakeAndHasPendingWatchSends(t *testing.T) {
	jm := newTestJobManager(t) // existing helper pattern in job_watch_test.go; adapt to the file's local constructor
	woke := 0
	jm.wake = func() { woke++ }

	if jm.hasPendingWatchSends() {
		t.Fatal("fresh manager must have no pending watch sends")
	}
	jm.kick()
	if woke != 1 {
		t.Fatalf("kick must call wake once, got %d", woke)
	}
	jm.wake = nil
	jm.kick() // must not panic with nil wake (restore-only managers pass nil)

	// Arrange one pending send via the existing test path that creates a
	// watchConfig with a pending entry (mirror an existing pending-state test's
	// setup in this file), then:
	cfg := installCallerSendWatchWithPending(t, jm) // write this tiny fixture next to the test
	_ = cfg
	if !jm.hasPendingWatchSends() {
		t.Fatal("pending entry must be visible to hasPendingWatchSends")
	}
}
```

The fixture `installCallerSendWatchWithPending` should reuse the same construction an existing pending-state test in `job_watch_test.go` uses (e.g. the tests around `persistPendingWatchSend`/`removePendingWatchSend`) — do not invent a new path; copy the minimal arrange block into a named helper.

- [x] **Step 2: Run it, verify it fails to compile** (`jm.wake`, `jm.kick`, `jm.hasPendingWatchSends` undefined)

Run: `cd agent && go test ./ -run TestJobManagerWakeAndHasPendingWatchSends -v`

- [x] **Step 3: Implement**

In `agent/jobs.go`, add to the `jobManager` struct (next to `enqueue`):

```go
	// wake kicks the owning session's drain loop (wired to Session.notify).
	// nil for restore-only managers. Observation paths call kick() after
	// persisting watch-send intent; they never deliver (spec §3).
	wake func()
```

Below the struct (or near `enqueueWatchNotifications` in `agent/job_watch.go`):

```go
func (jm *jobManager) kick() {
	if jm.wake != nil {
		jm.wake()
	}
}

// hasPendingWatchSends reports whether any live or terminal-flush watch config
// holds undelivered pending sends. Drain-loop tails use it to decide whether a
// wake needs a drain pass.
func (jm *jobManager) hasPendingWatchSends() bool {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	for _, cfg := range jm.watches {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	for cfg := range jm.terminalFlush {
		if len(cfg.pendingOrder) > 0 {
			return true
		}
	}
	return false
}
```

In `agent/session_init.go:116`, change the construction to wire wake after `newJobManager` returns (signature stays — wake is a field, set it right after):

```go
	jm, err := newJobManager(s.stateDir, s.id, s.enqueueJobNotificationAndNotify)
	if err != nil { ... existing ... }
	jm.wake = s.notify
```

Leave the `session_init.go:323` restore-only constructor as-is (nil wake).

- [x] **Step 4: Run the test, verify pass; run `go test ./ -race -run TestJobManagerWake -v`**
- [x] **Step 5: Commit** — `feat(job-control): add jobManager wake + hasPendingWatchSends`

### Task 1.2: Watch-send wake tokens on the notification queue

**Files:**
- Modify: `agent/jobs.go:128` (jobNotification struct)
- Modify: `agent/job_notify.go` (render branch)
- Modify: `agent/session_lifecycle.go:864` (`filterDeliverableJobNotifications`), `:821` (`acceptNotificationInput` settle)
- Modify: `agent/job_watch.go` (token constructor + resolve/settle helpers)
- Test: `agent/job_notify_test.go`, `agent/job_watch_test.go`

- [x] **Step 1: Write the failing tests**

```go
// In job_watch_test.go
func TestWatchSendTokenRenderByKey(t *testing.T) {
	// Arrange: jm with a caller-send watch and ONE current pending state
	// (key K, updateSeq 2, frame "frame-v2"), constructed via the same
	// fixture as Task 1.1.
	//
	// Tokens: two stale (updateSeq 1; one for a cleared key), one current.
	// Act: resolveWatchSendTokens (new) over the three tokens.
	// Assert: exactly one deliverable, frame text "frame-v2";
	// stale tokens produce nothing (no error, no requeue).
}

func TestWatchSendTokenSettleAfterPersist(t *testing.T) {
	// Arrange: one current token resolved to a deliverable.
	// Act: settleWatchSendTokens (new) for it.
	// Assert: jobstore log gains watch_send_delivered for K and the pending
	// entry is removed (cfg.pending empty; hasPendingWatchSends false).
}
```

```go
// In job_notify_test.go
func TestFormatWatchSendNotificationBlock(t *testing.T) {
	n := watchSendTokenNotification("job_w", "dlv_1", "frame text")
	got := formatJobNotificationBlock(n.notification()) // see Step 3: token notifications render via the new branch
	for _, want := range []string{"watch_send", "job_w", "dlv_1", "frame text"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
```

Flesh these out with the real fixture calls while writing them — the assertions above are the contract; the arrange blocks reuse Task 1.1's fixture.

- [x] **Step 2: Run, verify compile failures** (`watchSendToken` etc. undefined)

- [x] **Step 3: Implement**

`agent/jobs.go` — extend the struct (one new pointer field; everything else unchanged):

```go
type jobNotification struct {
	JobID, JobType, Status, Reason, TranscriptRef string
	OutputBytes                                   int64
	ExitCode                                      *int
	// WatchSend marks this entry as a watch-send wake token: render-by-key
	// against the owning jobManager's CURRENT pending state at accept time
	// (spec §4.3). The frame text is deliberately NOT carried here.
	WatchSend *watchSendToken
}
```

`agent/job_watch.go` — the token and its lifecycle:

```go
// watchSendToken identifies a pending caller-targeted watch send. Tokens are
// at-least-once and harmless when stale: render-by-key skips any token whose
// pending state was replaced, cleared, dropped, or already settled.
type watchSendToken struct {
	ChildSessionID string // "" = the session's own jobManager
	Key            jobstore.WatchSendKey
	UpdateSeq      uint64
	DeliveryID     string
}

func watchSendTokenNotification(childSessionID string, state jobstore.WatchSendState) jobNotification {
	return jobNotification{
		JobID:  state.Key.ResolvedWatchedIdentity,
		Status: jobNotificationEventWatch,
		Reason: state.TriggerReason,
		WatchSend: &watchSendToken{
			ChildSessionID: childSessionID,
			Key:            state.Key,
			UpdateSeq:      state.UpdateSeq,
			DeliveryID:     state.DeliveryID,
		},
	}
}

// jobManagerForToken resolves which jobManager owns a token's pending state.
func (s *Session) jobManagerForToken(tok *watchSendToken) *jobManager {
	if tok == nil {
		return nil
	}
	if tok.ChildSessionID == "" {
		return s.jobManager
	}
	if s.subagents == nil {
		return nil
	}
	if sub := s.subagents.get(tok.ChildSessionID); sub != nil && sub.sess != nil {
		return sub.sess.jobManager
	}
	return nil
}

// resolveWatchSendToken returns the CURRENT frame for a token, or ok=false if
// the token is stale (latest-frame-wins; also covers delivery-after-drop).
func (s *Session) resolveWatchSendToken(tok *watchSendToken) (jm *jobManager, cfg *watchConfig, state jobstore.WatchSendState, ok bool) {
	jm = s.jobManagerForToken(tok)
	if jm == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	jm.mu.Lock()
	defer jm.mu.Unlock()
	cfg = jm.watchConfigForKeyLocked(tok.Key) // new: search jm.watches + terminalFlush for a cfg whose pending map holds tok.Key
	if cfg == nil {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	cur := cfg.pending[tok.Key]
	if cur == nil || cur.UpdateSeq != tok.UpdateSeq {
		return nil, nil, jobstore.WatchSendState{}, false
	}
	return jm, cfg, *cur, true
}
```

`watchConfigForKeyLocked` iterates `jm.watches` then `jm.terminalFlush` and returns the first cfg with `cfg.pending[key] != nil`. Settle reuses the existing delivered path:

```go
func (jm *jobManager) settleWatchSendDelivered(cfg *watchConfig, state jobstore.WatchSendState) error {
	delivered := state
	if err := jm.appendWatchSendEvents([]jobstore.Event{{
		Kind:      jobstore.EventWatchSendDelivered,
		TS:        jm.now(),
		WatchSend: &delivered,
	}}); err != nil {
		return err
	}
	jm.removePendingWatchSend(cfg, delivered.Key, delivered.UpdateSeq)
	return nil
}
```

(Extract this from the `watchSendDelivered` arm of `deliverPendingWatchSend` (`agent/job_watch.go:1102-1118`) and have that arm call it — one implementation, not two.)

`agent/job_notify.go` — render branch keyed on the token, NOT on `JobID == ""` (the existing gate at `:51` only handles session-identity watches):

```go
// In formatJobNotificationBlock, BEFORE the existing watch/default branches:
	if n.WatchSend != nil {
		attrs := []string{
			fmt.Sprintf("job_id=%q", n.JobID),
			`event="watch_send"`,
			fmt.Sprintf("delivery_id=%q", n.WatchSend.DeliveryID),
			fmt.Sprintf("trigger=%q", n.Reason),
		}
		return fmt.Sprintf("<job-notification %s>\n%s\n</job-notification>",
			strings.Join(attrs, " "), n.watchSendFrame)
	}
```

`watchSendFrame` is an unexported string field set at render time, not enqueue time — see the filter change next. (Add it to `jobNotification` as `watchSendFrame string` with a comment that it is populated only between filter and format inside one accept pass.)

`agent/session_lifecycle.go` `filterDeliverableJobNotifications` — replace the unconditional watch passthrough:

```go
	for _, n := range raw {
		if n.WatchSend != nil {
			jm, cfg, state, ok := s.resolveWatchSendToken(n.WatchSend)
			if !ok {
				continue // stale token: replaced, cleared, dropped, or settled
			}
			if seenTok[n.WatchSend.Key] { // batch dedupe: map[jobstore.WatchSendKey]bool, declared above the loop
				continue
			}
			seenTok[n.WatchSend.Key] = true
			n.watchSendFrame = state.Frame
			survivors = append(survivors, deliverableJobNotification{notification: n, watchJM: jm, watchCfg: cfg, watchState: state})
			continue
		}
		if n.Status == jobNotificationEventWatch {
			survivors = append(survivors, deliverableJobNotification{notification: n})
			continue
		}
		durableRaw = append(durableRaw, n)
	}
```

Extend `deliverableJobNotification` (`agent/job_notify.go:11`) with the three `watch*` fields (unexported; nil for non-token entries).

`acceptNotificationInput` — settle after the durable append succeeds, right after `markJobNotificationsDelivered(jobNotifs)`:

```go
	for _, d := range jobNotifs {
		if d.watchJM == nil {
			continue
		}
		if err := d.watchJM.settleWatchSendDelivered(d.watchCfg, d.watchState); err != nil {
			s.emit(events.EventWarning, events.WarningData{Message: "watch send settle failed: " + err.Error()})
		}
	}
```

On the `appendTurnDurably` failure path nothing settles — the durable pending survives and restore/re-drain re-tokens it. That is the at-least-once contract.

- [x] **Step 4: Run the three tests + the whole `job_notify`/`job_watch` files; then `go test ./ -race` in agent/**
- [x] **Step 5: Commit** — `feat(job-control): watch-send wake tokens with render-by-key`

### Task 1.3: Deadlock regression test (must fail against today's code)

**Files:**
- Test: `agent/job_watch_deadlock_test.go` (new)

- [x] **Step 1: Write the test against the REAL session loop**

Model it on the live-session tests in `agent/job_watch_observer_test.go` / `session_sync_race_test.go` (fake env + scripted provider). Shape:

```go
func TestCallerSendWatchDoesNotDeadlockOnAssistantEvents(t *testing.T) {
	// Session with a scripted provider whose turn 1 response contains a tool
	// call (so EventToolCallEnd and EventAssistantTextEnd both fire), turn 2
	// communicates. Install via the job_watch tool:
	//   target=caller, events=[assistant.message], send={to: caller, message: "ping"}
	// BEFORE driving the turn. Drive ProcessInput in a goroutine; require
	// completion within 30s:
	done := make(chan struct{})
	go func() { defer close(done); _, _ = drive(t, sess, "run the tool") }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		buf := make([]byte, 1<<20)
		t.Fatalf("session wedged (watch-send deadlock):\n%s", buf[:runtime.Stack(buf, true)])
	}
	// And the tool result must have been persisted (the incident's missing
	// TOOL_RESULTS): assert the transcript/history contains the tool round.
}
```

Add a sibling test for `events=["assistant.tool"]` (wedges on the FIRST tool call today). Use the file's existing session-builder helpers; do not hand-roll a provider.

- [x] **Step 2: Run it, verify it FAILS today** (timeout + stack dump showing `deliverWatchCallerMessageAtBoundary` blocked on `responseSideEffectsMu`)

Run: `cd agent && go test ./ -run TestCallerSendWatchDoesNotDeadlock -v -timeout 120s`

- [x] **Step 3: Commit the failing test on a `t.Skip` guard? NO** — leave it red locally, do NOT commit yet; Task 1.4 makes it green and they commit together (a committed red test breaks the green-gate rule).

### Task 1.4: Observation becomes persist-only

**Files:**
- Modify: `agent/job_watch.go` (`onSessionEvent:792-842`, `feedJobOutput:879-905`, `fireProgressTick:963-995`, split `deliverWatchSend:1032-1055`)
- Modify: `agent/jobs.go` (`armFinalizedJob` — the `deliverWatchSends`/`retryPendingWatchSendsFor*` block at ~:926-936)
- Test: `agent/job_watch_test.go` (invariant test), Task 1.3's test goes green

- [x] **Step 1: Write the invariant test**

```go
func TestObservationRecordsIntentOnly(t *testing.T) {
	// jm with a caller-send watch on assistant.message and a delegate-send
	// watch (send.to = job_obs, a running delegate fixture).
	// Act: jm.onSessionEvent(events.EventAssistantTextEnd, nil)
	// Assert:
	//  - both sends are PENDING in jm state (hasPendingWatchSends true)
	//  - the caller send produced exactly one queue entry with WatchSend != nil
	//    (capture via the enqueue func the test injects)
	//  - wake was called
	//  - NO delivery happened: the delegate fixture's steer queue is empty,
	//    no watch_send_delivered event exists in the jobstore log
}
```

- [x] **Step 2: Run; it fails** (delivery still happens synchronously)

- [x] **Step 3: Implement**

Split `deliverWatchSend` into record + deliver. The record half (replaces the body through the `persistPendingWatchSend` call):

```go
// recordWatchSend persists a fired send as pending and returns its state.
// ok=false means the send was superseded or unresolvable (already handled).
// Pure observation: no delivery, no Session calls (spec §3).
func (jm *jobManager) recordWatchSend(d watchSendDelivery) (state jobstore.WatchSendState, cfg *watchConfig, ok bool, err error) {
	if d.cfg == nil || d.send == nil || !jm.isCurrentWatchSendDelivery(d) {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	target, terr := resolveWatchSendTarget(d.send.To, d.watchedIdentity)
	state = jm.watchSendState(d, target)
	persisted, perr := jm.persistPendingWatchSend(state, d)
	if perr != nil {
		if d.allowAfterTerminalExpiry && !persisted {
			jm.rememberUnpersistedTerminalPendingWatchSend(d.cfg, state)
		}
		return jobstore.WatchSendState{}, nil, false, perr
	}
	if !persisted {
		return jobstore.WatchSendState{}, nil, false, nil
	}
	if terr != nil {
		return jobstore.WatchSendState{}, nil, false, jm.dropWatchSend(state, d.cfg, terr.Error())
	}
	return state, d.cfg, true, nil
}
```

New shared observation tail, used by all four observation sites:

```go
// recordWatchSendsAndKick is the observation-side half of watch delivery:
// persist every fired send, enqueue caller wake tokens, kick the owner.
func (jm *jobManager) recordWatchSendsAndKick(deliveries []watchSendDelivery) {
	deliveries = jm.snapshotWatchSendFrames(deliveries)
	kicked := false
	for _, d := range deliveries {
		state, _, ok, err := jm.recordWatchSend(d)
		if err != nil || !ok {
			continue // recordWatchSend already produced diagnostics/drops
		}
		if state.Key.ResolvedSendTo == runtimeMessageAliasCaller && jm.enqueue != nil {
			jm.enqueue(watchSendTokenNotification("", state))
			kicked = true // enqueueJobNotificationAndNotify already kicks
		}
	}
	if !kicked {
		jm.kick()
	}
}
```

(`ChildSessionID` is `""` here because a jobManager's own enqueue lands on its own session's queue; the parent-discovers-child path is Task 1.5.)

Then, at each site, replace the delivery block:

- `onSessionEvent:837-841` →
  ```go
  	jm.enqueueWatchNotifications(notifications)
  	jm.recordWatchSendsAndKick(deliveries)
  ```
- `feedJobOutput` (`:903-904`) and `fireProgressTick` (`:992-993`): same two-line shape.
- `armFinalizedJob` (`agent/jobs.go:926-936`): replace `snapshotWatchSendFrames`+`deliverWatchSends`+`retryPendingWatchSendsForWatchTarget`+`retryPendingWatchSendsForRunTarget` with `jm.recordWatchSendsAndKick(watchDeliveries)` — the kick makes the owner drain, which performs what the two retry calls did.

`deliverWatchSends` keeps existing internals for now (the drain still uses `deliverPendingWatchSend`); only its *callers* outside the drain disappear. Verify with: `rg -n "deliverWatchSends\(" agent/ --type go | grep -v _test` → only drain internals remain.

- [x] **Step 4: Run the invariant test, Task 1.3's deadlock tests (now green), then the full agent suite with `-race`. Many existing tests will now fail — that is Task 1.7's job; confirm the failures are all "expected delivery, got pending" shaped before proceeding. If any failure is a panic or a different shape, STOP and investigate.**

  DIVERGENCE (implemented): `recordWatchSendsAndKick` guards the kick — `if len(deliveries)==0 { return }` up front, and the trailing kick is `if recorded && !kicked`. The sketch's unconditional `if !kicked { jm.kick() }` fired a spurious wake on EVERY non-matching `emit` (observation runs on every event, most match no watch), which broke `TestWatchCallerDeliveryDoesNotUseJobNotificationWake` (a non-churn shape that correctly flagged it). Guarded version preserves the old "empty deliveries = no-op" while still waking when a delegate pending was recorded. `-race`: 22 expected delivery-deferred failures (all `*Watch*` tests), 0 panics/races/deadlocks. Leaves 3 `unused` lint findings (`deliverWatchSends`, `retryPendingWatchSendsForRunTarget`, `retryPendingWatchSendsForWatchTarget`) — orphaned by the armFinalizedJob caller removal, re-wired in Task 1.5; no pre-commit hook gates them.

- [x] **Step 5: Commit** — `feat(job-control): persist-only watch observation + deadlock regression tests` (include Task 1.3's tests)

### Task 1.5: The drain

**Files:**
- Modify: `agent/job_watch.go` (replace `retryPendingCallerWatchSendsAtBoundary:1741-1760`, repoint `retryRestoredPendingWatchSends:1718`)
- Modify: `agent/session_state.go:122`, `agent/session_tool_round.go:327`, `agent/history_repair.go:126` (call sites)
- Test: `agent/job_watch_test.go`

- [x] **Step 1: Write the failing tests**

```go
func TestDrainDeliversDelegateTargetedSends(t *testing.T) {
	// Pending send to a RUNNING delegate fixture → drainPendingWatchSends →
	// child steering queue has the frame; watch_send_delivered appended.
}

func TestDrainResumesTerminalResumableTarget(t *testing.T) {
	// Pending send to a terminal resumable delegate fixture → drain →
	// resume path invoked (assert via the fixture's spawn/run hook),
	// per spec §4.2's explicit behavior change.
}

func TestDrainEnqueuesTokensForChildCallerPendings(t *testing.T) {
	// Child session fixture whose jobManager holds a caller-targeted pending
	// (restored shape) → parent drainPendingWatchSends → parent queue gains a
	// token with ChildSessionID == child id; nothing rides parentSteer.
}
```

- [x] **Step 2: Run; fail** (drain undefined)

- [x] **Step 3: Implement**

  DIVERGENCE (implemented): kept `deliverPendingWatchSend` and PARAMETERIZED its sender (added `send sendMessageFunc` arg; field read `jm.send` → `send`), per this plan's Step-3 design — NOT the separate `deliverPendingWatchSendFromDrain` copy the coordinator task sketched. The drain passes `s.sendDelegateMessage`; the test-only `deliverWatchSend` and `retryPendingWatchSendDeliveries` pass `jm.send`. Body is otherwise byte-identical (classify → settle/busy-noop/drop preserved). Reuse over duplication.

  DIVERGENCE (deletion scope): deleted ONLY the 4 truly-orphaned functions — `retryPendingCallerWatchSendsAtBoundary`, `deliverWatchSends`, `retryPendingWatchSendsForRunTarget`, `retryPendingWatchSendsForWatchTarget` (rg-verified zero callers, production AND test). KEPT `retryPendingWatchSendsForTarget` (3 live test callers at job_watch_test.go:905/1570/1582), `retryPendingWatchSendsForTargets`, `retryPendingWatchSendDeliveries`, `deliverPendingWatchSend`, and `deliverWatchSend` (12 test callers) — these are transitively reachable from the 22 churned tests; deleting them breaks TEST COMPILATION, which would prevent Step 4 from running the suite at all. The plan/coordinator delete-lists assumed `grep -v _test` = safe-to-delete, but test callers still gate the build. 1.7 re-anchors those tests and can then delete the chain. `withWatchCallerDeliveryBoundary` is now `unused` (sole prod caller deleted) — left for 1.6 as instructed; 1 expected lint finding, net −3 (the prior 3 orphans are gone).

```go
// drainPendingWatchSends is the ONLY executor of watch-send delivery. Call it
// solely from loop-owned code: never from event observation, never under
// responseSideEffectsMu (spec §3/§4.2).
func (s *Session) drainPendingWatchSends(ctx context.Context) error {
	var errs []error
	if s.jobManager != nil {
		errs = append(errs, s.drainJobManagerWatchSends(ctx, s.jobManager, ""))
	}
	if s.subagents != nil {
		for _, child := range s.subagents.sessions() {
			if child == nil || child.jobManager == nil {
				continue
			}
			errs = append(errs, s.drainJobManagerWatchSends(ctx, child.jobManager, child.id))
		}
	}
	return errors.Join(errs...)
}

func (s *Session) drainJobManagerWatchSends(ctx context.Context, jm *jobManager, childSessionID string) error {
	var errs []error
	for _, delivery := range jm.pendingWatchSendDeliveries(nil) {
		target := delivery.state.Key.ResolvedSendTo
		if target == runtimeMessageAliasCaller {
			// Caller sends deliver via the notification rail. Tokens are
			// enqueued at observation time; this re-token covers restored /
			// crash-recovered pendings. Duplicates are harmless (render-by-key
			// + batch dedupe).
			if jm.enqueue != nil && childSessionID == "" {
				jm.enqueue(watchSendTokenNotification("", delivery.state))
			} else if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification(childSessionID, delivery.state))
			}
			continue
		}
		if err := jm.deliverPendingWatchSend(ctx, delivery.cfg, delivery.state, true); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
```

`deliverPendingWatchSend` keeps its delivery body but its `jm.send` call becomes a parameterized sender — change the field read to a passed function: give `deliverPendingWatchSend` the signature `(ctx, cfg, state, ensurePending, send sendMessageFunc)` where `type sendMessageFunc func(context.Context, sendMessageArgs) sendMessageResult`, and have the drain pass `s.sendDelegateMessage`. (`jm.send` field deletion happens in Task 1.6; this step removes its last reader.)

Wait — simpler and equally safe: since the drain is a `Session` method, move the delivery body INTO `session`-side code: add `func (s *Session) deliverPendingWatchSendFromDrain(ctx context.Context, jm *jobManager, cfg *watchConfig, state jobstore.WatchSendState) error` containing the current `deliverPendingWatchSend` logic with `jm.send(...)` replaced by `s.sendDelegateMessage(...)` and every jm-state touch via the existing jm methods (`isCurrentPendingWatchSend`, `appendWatchSendPendingState`, `settleWatchSendDelivered`, `dropWatchSend`). Delete the old `deliverPendingWatchSend` and `deliverWatchSends` once `retryRestoredPendingWatchSends`/`retryPendingWatchSendDeliveries` are repointed (next).

Replace the three boundary call sites:

- `session_state.go:122`: `s.retryPendingCallerWatchSendsAtBoundary(ctx)` → `s.drainPendingWatchSends(ctx)`
- `session_tool_round.go:327`: same replacement
- `history_repair.go:126`: same replacement

Restore (`retryRestoredPendingWatchSends`, called from `session_init.go:407/:747`): keep `classifyRestoredWatchSendTarget` exactly as-is for its drop/keep decisions, but the `watchSendDelivered` arm — runtime aliases — now enqueues a token instead of delivering:

```go
		case watchSendDelivered: // runtime alias (caller): token + kick
			if s.jobManager != nil && s.jobManager.enqueue != nil {
				s.jobManager.enqueue(watchSendTokenNotification("", delivery.state))
			}
```

(`watchSendBusy` arm unchanged — stays pending for the first post-restore drain; `watchSendHardFailure` arm unchanged — drops.)

Delete `retryPendingCallerWatchSendsAtBoundary`, `retryPendingWatchSendsForTarget`, `retryPendingWatchSendsForRunTarget`, `retryPendingWatchSendsForTargets`, `retryPendingWatchSendsForWatchTarget`, and `retryPendingWatchSendDeliveries` once `rg -n "retryPendingWatchSend" agent/ --type go | grep -v _test` shows no remaining callers.

No drain-loop-tail change is needed: a wake submits an `EntryNotification`; an all-token batch renders and settles in the notification turn; an EMPTY accept hits `finishNotificationNoop` → `finishProcessingAtBoundary` → `drainPendingWatchSends` — so delegate-only wakes deliver with zero model turns through the existing flow. Document this in the drain's comment.

- [x] **Step 4: Run the new tests + full agent suite `-race`; same triage rule as Task 1.4 Step 4**

  3 new drain tests PASS. `-race`: 26 failures, ALL Watch-suite "delivery deferred" shape, 0 panics/races/deadlocks (144.9s). Baseline at HEAD `bfd197eb` = 22 watch failures (the task-predicted set). My change adds exactly 4: `TestOrphanToolRepairRetriesPendingCallerWatchSends`, `TestProcessingExitRetriesPendingCallerWatchSends`, `TestWatchSendRestoreRetriesPendingBeforeTerminalNotifications`, `TestDelegateReconstructionWatchSendToCallerDuringParentCloseStaysPending` — all four assert the OLD synchronous caller `jm.send` delivery at the boundary/restore (`caller deliveries = 0, want 1` / `restored watch send did not attempt caller delivery`). Correct shift: caller sends now ride the token rail per §4.2; 1.7 re-anchors. No previously-passing non-watch test regressed.
- [x] **Step 5: Commit** — `feat(job-control): loop-owned watch-send drain`

### Task 1.6: Delete the synchronous caller path and `jm.send`

**Files:**
- Modify: `agent/session_queue.go` (delete `:94-145` — `deliverWatchCallerMessage`, `deliverWatchCallerMessageFromContext`, `deliverWatchCallerMessageAtBoundary`, `withWatchCallerDeliveryBoundary`, `isWatchCallerDeliveryBoundary`, the context key type)
- Modify: `agent/job_delegate.go` (`:214-247` runtime-alias branch; `:574` spawn wiring)
- Modify: `agent/jobs.go` / `agent/session_init.go` (`jm.send` field + wiring)
- Test: existing suites

- [x] **Step 1: Delete in this order, compiling after each:**

1. In `sendDelegateMessage`'s runtime-alias branch, remove the `args.FromWatch` arm entirely (caller-targeted watch sends can no longer reach here — the drain routes them to the rail). Keep `parentSteerDelivered`/`parentSteer`/`trySteer` for non-watch runtime sends. Add a defensive guard at the top of the alias branch:
   ```go
   	if args.FromWatch {
   		return sendMessageFailed(target, errors.New("internal: watch sends to caller route via the notification rail"))
   	}
   ```
2. Delete `parentWatchSteerDelivered` from the spawn config and its wiring at `job_delegate.go:574` (and its declaration site — `rg -n "parentWatchSteerDelivered" agent/`).
3. Delete the five `session_queue.go` functions + the context key.
4. Delete the `send` field from `jobManager` and its wiring (`rg -n "jm.send\b|send:" agent/jobs.go agent/session_init.go`).
5. `go build ./...` at repo root; fix every compile error by deletion or repointing to the drain — if a caller you find is NOT in the expected set {old retry fns (gone), old deliver fns (gone), tests}, STOP and reassess before deleting it.

  DIVERGENCE (jm.send split): Commit A deletes the synchronous CALLER path (the `args.FromWatch` arm, `parentWatchSteerDelivered`, the five `session_queue.go` caller functions + `waitingForToolResultsLocked` which was orphaned, the boundary context key). The `jm.send` field deletion is deferred to Commit B (Task 1.7) — 63 test setters assign `jm.send` and deleting the field here breaks test compilation. Commit A compiles (prod + test binary) with ~25 caller/jm.send tests still red; the 6 caller-mechanism tests that referenced the deleted symbols are converted in A so the package compiles. Added `drainAndAccept` helper early (B.1 placement) because A's converted tests reference it. Updated the deadlock-test comments that named the deleted boundary function.

- [x] **Step 2: Run full agent suite `-race`; triage shape per Task 1.4** — package compiles; remaining 25 failures are all jm.send-capture / Pattern-2 caller tests (Commit B), shape "delivery deferred", no panics.
- [x] **Step 3: Commit** — `feat(job-control)!: delete synchronous watch-send delivery (jm.send)`

### Task 1.7: Re-anchor the existing watch suite

**Files:**
- Modify: `agent/job_watch_test.go`, `agent/job_watch_observer_test.go`, `agent/job_delegate_test.go`, `agent/job_notify_test.go`, others surfaced by the suite

- [x] **Step 1: Add the helper** — `drainAndAccept` added in Commit A (it is referenced by Commit A's converted caller tests, which must compile). Also added three jm-level test helpers in B: `drainWatchSendsVia` (drives the drain's `deliverPendingWatchSend` primitive for delegate pendings with a captured sender, returns joined errors like the live drain), `deliverWatchSendVia` (replaces the deleted `jobManager.deliverWatchSend`, sender as a param), and `waitForJobNotification`.

(Pure-jm tests assert PENDING state; delivery-args tests drive the surviving `deliverPendingWatchSend` primitive with a capture func — the structural deletion is the `jm.send` FIELD, not the ability to drive delivery explicitly.)

- [x] **Step 2: Mechanical re-anchor, file by file.** Done with the no-weakening transform. Two documented semantic shifts where the OLD assertion tested deleted synchronous behavior (re-anchored to the replacement, NOT weakened):
  - `TestWatchSendRestoreRetriesPendingBeforeTerminalNotifications`: the strict `delivered<notification` event ordering is inverted by design (caller sends moved from between-rounds steering to between-inputs notifications, so the terminal `job_notification_pending` is armed at restore and the watch send settles at the accept turn). Re-anchored to "both the watch_send_delivered and the terminal job_notification_pending are appended; the frame renders".
  - `TestWatchSendTerminalPendingPersistenceFailureRetriesFinalization` → `...RetainsFrameForDrain`: a watch-send pending-persist failure during finalize no longer blocks arming (spec §4.1 decouples them); `rememberUnpersistedTerminalPendingWatchSend` retains the final frame in runtime terminalFlush and the next drain re-persists+delivers it. The preserved guarantee (final frame not lost, retried) holds via the drain. Verified with a probe that the frame is retained and delivered.

- [x] **Step 3: Full gates** — `make test` EXIT=0, `make lint` EXIT=0 (all four modules), `go test ./ -race` EXIT=0 (agent, fresh). Residue gate empty.
- [x] **Step 4: Commit** — `test(job-control): re-anchor watch suite on drain/accept delivery`

### Task 1.8: Phase-1 architecture docs

**Files:**
- Modify: `docs/architecture.md` (new section after "How a turn flows", ~line 161)
- Modify: `docs/job-control.md:513-514` (delivery-mechanism amendment), `:38`/`:369` (caller wording)
- Modify: `docs/specs/2026-06-12-job-control-watch-deadlock-design.md` (resolution addendum)
- Modify: `agent/internal/tool/definitions.go` (`DefJobWatch` description: send delivery wording)

- [ ] **Step 1: Write `docs/architecture.md` § "Ownership and mailboxes"** — the invariant (spec §3) in evergreen form: the three queues (steering, job notifications, watch outbox), who appends (anyone, leaf locks only), who drains (the owning session's loop, at named boundaries), the wake path (`notify` → server input channel → `EntryNotification`), the forbidden re-entry (`responseSideEffectsMu` is held across emits; observers must never acquire it), and the lock-order line it protects (`agent/session.go:72-75`). Cite the deadlock note as the motivating incident. ~40 lines, written for a maintainer adding the NEXT event observer.
- [ ] **Step 2: Amend the contract rows** from spec §8 that Phase 1 implements (513-514 delivery mechanism, 38/369 caller wording). Quote-level edits, no restructuring.
- [ ] **Step 3: Add the resolution addendum** to the deadlock note: chosen direction, what review corrected (the §1 list from the spec), pointer to the spec + this plan.
- [ ] **Step 4: Update `DefJobWatch`** description: "Send deliveries coalesce by watch key and retry busy delegates" stays; add "deliveries arrive at session boundaries — caller sends as job notifications".
- [ ] **Step 5: `make lint && make test`; commit** — `docs(job-control): ownership/mailbox architecture + phase-1 contract amendments`

---

## Phase 2 — Create-time guards (spec §6)

### Task 2.1: Feedback-loop guard on resolved kinds

**Files:**
- Modify: `agent/job_watch.go` (`validateWatchEventArgs:292` or a new `validateWatchDeliveryLoop` called from `configureWatch` after `newWatchConfig` resolves kinds)
- Modify: `agent/internal/tool/definitions.go` (`DefJobWatch` description note)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing tests** — table-driven over the four loop shapes (all must be REJECTED):

```go
	cases := []watchArgs{
		{Target: "caller", Events: []string{"assistant.message"}},                                  // notify-self
		{Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: "caller"}}, // send-to-self (the incident)
		{Target: "caller", TriggerEvent: "assistant.message", TriggerEvery: 2, Send: &watchSendArgs{To: "caller"}}, // trigger-only (no Events)
		{Target: jobID, Events: []string{"assistant.message"}},                                     // job target, kind still self-generated
	}
	// And these must be ACCEPTED:
	//  {Target: "caller", Events: []string{"assistant.message"}, Send: &watchSendArgs{To: obsJobID}} — sidecar
	//  {Target: "caller", Events: []string{"job.notification"}}                                     — non-loopy self kind
	//  {Target: jobID, OutputMatch: "ready"}                                                        — not an event watch
```

- [ ] **Step 2: Run; fail.**
- [ ] **Step 3: Implement** in `configureWatch` after `newWatchConfig(a)` (which resolves events ∪ trigger ∪ wildcard into `cfg.eventKinds`/`cfg.wildcardEvents`):

```go
	if err := validateWatchDeliveryLoop(cfg); err != nil {
		return watchResult{}, err
	}
```

```go
// validateWatchDeliveryLoop rejects configs that deliver self-generated event
// kinds back into the session that generates them — a structural feedback
// loop regardless of watch target (spec §6.1).
func validateWatchDeliveryLoop(cfg *watchConfig) error {
	selfDelivery := cfg.send == nil || cfg.send.To == runtimeMessageAliasCaller
	if !selfDelivery {
		return nil
	}
	selfGenerated := cfg.wildcardEvents ||
		cfg.eventKinds[events.EventAssistantTextEnd] ||
		cfg.eventKinds[events.EventToolCallEnd] ||
		cfg.eventKinds[events.EventCommunicate]
	if !selfGenerated {
		return nil
	}
	return errors.New("invalid_request: watching assistant.message/assistant.tool/communicate with delivery back to the caller is a feedback loop (each delivery causes the next event); watch these kinds only with send.to set to an observer job")
}
```

- [ ] **Step 4: Run tests; full suite (existing tests that installed such configs — the incident smoke shape — will need their fixtures changed to sidecar delivery; apply the Task 1.7 no-weakening rule). Commit** — `feat(job-control): reject feedback-loop watch configs at create`

### Task 2.2: Reject `include_excerpt` on session targets

**Files:**
- Modify: `agent/job_watch.go` (`configureWatch`, next to the `:177` output_match/session-target rejection)
- Modify: `agent/job_watch.go:1893-1908` (`buildWatchFrame` — excerpt block gains a session-target guard returning no excerpt, defensive)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing test** — `{Target: "caller", Events: [...], Send: {To: obsID, IncludeExcerpt: true}}` → `invalid_request: include_excerpt requires a concrete job target; session-target frames carry transcript_ref`. And a delivery-side test: a `*`-target frame built for a session identity contains NO `output_read_error`.
- [ ] **Step 2: Run; fail.**
- [ ] **Step 3: Implement** — in `configureWatch` after the `:177` block:

```go
	if !a.Clear && a.Send != nil && a.Send.IncludeExcerpt && isWatchSessionTarget(a.Target) {
		return watchResult{}, errors.New("invalid_request: include_excerpt requires a concrete job target; session-target frames carry transcript_ref")
	}
```

In `buildWatchFrame`, guard the excerpt block: `if cfg.send.IncludeExcerpt && !isWatchSessionTarget(jobID) { ... }` — a `*` watch resolves per-fire, so creation-time validation cannot cover every identity; the delivery guard makes the broken `readOutput("caller")` call unreachable.

- [ ] **Step 4: Run; commit** — `feat(job-control): excerpt validation for session-target watches`

### Task 2.3: Session-target frames carry transcript_ref (spec §5.2)

**Files:**
- Modify: `agent/job_watch.go` (`buildWatchFrame`, `watchSendSnapshot:527` — thread the session's transcript ref into the frame for session identities)
- Modify: `agent/jobs.go` (jobManager gains `transcriptRef string` set at construction — the OWNING session's ref; `agent/session_init.go:116` wiring)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing test** — a session-target frame contains `transcript_ref: <ref>`; a job-target frame does not.
- [ ] **Step 2: Implement** — store `jm.transcriptRef` (wired from `s.TranscriptPath()`-derived ref at init — use the SAME ref shape `decodeRef` consumes; find the encoder near it, `rg -n "encodeRef|decodeRef" agent/`); in `buildWatchFrame`'s frame block, when `isWatchSessionTarget(jobID)`, append `"\ntranscript_ref: " + jm.transcriptRef` instead of any excerpt.
- [ ] **Step 3: Run; commit** — `feat(job-control): session-target watch frames carry transcript_ref`

---

## Phase 3 — output_match level-trigger + blocking grep (spec §7)

### Task 3.1: Offset-carrying feeds

**Files:**
- Modify: `agent/jobs.go` (`appendJobOutput` ~:544 — capture post-append offset; confirm `output.Append`'s return values first: `rg -n "func.*Append" agent/internal/jobstore/output.go`. If it does not return the new length, add a `Len()`/post-append size accessor under the store's own lock — do NOT read file size separately)
- Modify: `agent/job_watch.go` (`feedJobOutput(jobID string, chunk []byte)` → `feedJobOutput(jobID string, chunk []byte, endOffset int64)`)
- Modify: `agent/internal/jobstore/watch.go` (`OutputMatcher` gains `scanOffset int64`; `Feed(chunk []byte, endOffset int64)` discards/slices below `scanOffset`)
- Test: `agent/internal/jobstore/watch_test.go`, `agent/job_watch_test.go`

- [ ] **Step 1: Failing jobstore tests** — `Feed` with `endOffset ≤ scanOffset` matches nothing; a chunk straddling `scanOffset` matches only on the post-offset slice; carry seeding: `SeedCarry([]byte)` then a completing chunk matches a straddling token.
- [ ] **Step 2: Implement** in `OutputMatcher`:

```go
// SetScanOffset marks bytes below off as covered by an attach-time scan;
// Feed ignores them. SeedCarry primes the partial-line carry with the
// retained tail after the last newline so straddling tokens still match.
func (m *OutputMatcher) SetScanOffset(off int64) { m.scanOffset = off }
func (m *OutputMatcher) SeedCarry(tail []byte)   { m.carry = append(m.carry[:0], tail...) }

func (m *OutputMatcher) Feed(chunk []byte, endOffset int64) []string {
	if endOffset <= m.scanOffset {
		return nil
	}
	if start := endOffset - int64(len(chunk)); start < m.scanOffset {
		chunk = chunk[m.scanOffset-start:]
	}
	// ... existing carry/line logic unchanged ...
}
```

(Adjust to the file's actual field names after reading it — the carry buffer exists; verify its name. Keep the existing zero-offset behavior for matchers created without a scan: `scanOffset == 0` is a no-op.)

- [ ] **Step 3: Thread `endOffset`** through `appendJobOutput` → `feedJobOutput` → `Feed`. Single-goroutine pump per job ⇒ offsets monotone (assert with a debug check: if `endOffset` regresses, drop the chunk and emit a warning notification — never panic on the pump).
- [ ] **Step 4: Full suite; commit** — `feat(jobstore): offset-aware output matching`

### Task 3.2: Attach-time scan (running targets)

**Files:**
- Modify: `agent/job_watch.go` (`configureWatch` — after the watch is installed at `:280-285`)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing tests** — (a) job already printed `ready` → attach `output_match:"ready"` watch → exactly ONE pending fire exists without any further output; (b) the fire counts once for `trigger.every`; (c) a job that has printed nothing → attach → no fire; later `Feed` fires normally; (d) token straddling attach (write `rea`, attach, write `dy\n`) → exactly one fire (carry seeding), run under `-race`.
- [ ] **Step 2: Implement** — in `configureWatch`, for a concrete running target with `cfg.outputMatcher != nil`, inside the SAME `jm.mu` critical section that installs the cfg (`jm.watches[key] = cfg`): read retained length N + tail under the store's lock (add a jm helper `retainedOutputForScan(jobID) (data []byte, n int64, err error)` that reads the full retained output, NOT the preview window — reuse `jm.readOutput` with the retention max), `cfg.outputMatcher.SetScanOffset(n)`, `cfg.outputMatcher.SeedCarry(tailAfterLastNewline(data))`. After releasing `jm.mu`, line-scan `data[:n]` with the same regex; on any match, build ONE `watchSendDelivery` (frame carries the LAST matching line as trigger) and route it through `recordWatchSendsAndKick` / `enqueueWatchNotifications` exactly like a `Feed` fire.
- [ ] **Step 3: Run; full suite; commit** — `feat(job-control): level-triggered output_match at attach`

### Task 3.3: Terminal catch-up

**Files:**
- Modify: `agent/job_watch.go` (`configureWatch` terminal-rejection path at `:329-352`; `watchResult:104` + its JSON projection in `agent/session_tools_jobs.go`)
- Modify: `agent/internal/tool/definitions.go` (`DefJobWatch` description: replace the `target_terminal` race warning)
- Modify: `docs/job-control.md:534`, `:506`, `:542/:546` (contract rows from spec §8)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing tests** — terminal job + `output_match` that matches retained output → result `{watching:false, fired:true, terminal_catchup:true}`, one notification (or send routed via a detached terminalFlush config that drains/settles); terminal + no match → `{watching:false, fired:false, terminal_catchup:true, status:"completed"}`, nothing enqueued; terminal + `events` → still `target_terminal`.
- [ ] **Step 2: Implement** — in `configureWatch`, when `validateWatchTarget` returns the terminal error AND the args are output_match-only (`a.OutputMatch != "" && len(a.Events)==0 && a.TriggerEvent=="" && a.ProgressIntervalMS==0`), run the catch-up: scan retained output; on match with `a.Send == nil` → `enqueueWatchNotifications` one watch notification; on match with send → mint a one-shot detached `watchConfig` (own `generation`), register it via `rememberDetachedPendingLocked` in `terminalFlush`, `recordWatchSendsAndKick` one delivery. Extend `watchResult` with `Fired, TerminalCatchup bool` + `Status string` and project them in the tool's JSON (`jobWatchTool` in `session_tools_jobs.go` — find its result struct and add the three fields with `omitempty`).
- [ ] **Step 3: Amend the three contract rows + the tool description in the same commit.**
- [ ] **Step 4: Run; commit** — `feat(job-control): terminal catch-up for output_match watches`

### Task 3.4: Blocking grep

**Files:**
- Modify: `agent/session_tools_jobs.go` (`jobReadOutputTool:201-207`, `waitForJobDoneOrOutput:1015`)
- Modify: `agent/internal/tool/definitions.go` (`DefJobReadOutput` description)
- Modify: `docs/job-control.md` (the `job_read_output` block semantics line — locate via `rg -n "block" docs/job-control.md`)
- Test: `agent/session_tools_jobs_test.go` (or the file's existing test home — `rg -n "jobReadOutputTool" agent/*_test.go`)

- [ ] **Step 1: Failing tests** — (a) match already in retained output → returns immediately with matches, no wait; (b) match appears mid-stream → returns at next poll tick with matches; (c) no match by timeout → normal snapshot, status running, empty matches; (d) terminal without match → final snapshot.
- [ ] **Step 2: Implement** — new `waitForJobGrepMatch(ctx, jm, jobID string, re *regexp.Regexp, limitBytes int, timeout time.Duration) bool` modeled on `waitForJobDoneOrOutput`'s timer/ticker/done-channel shape, with: entry check before the first wait; on each size change, grep ONLY `[lastScanned-lineCarry, newLen)` via a new incremental jm helper `grepOutputFrom(jobID, re, fromOffset)` that re-reads from the last newline before `fromOffset` (cheap seek on the retained file; reuse the existing `grepOutput` line machinery with an offset parameter). Wire in `jobReadOutputTool`: `if shellBoolArg(args, "block") { if grepRE != nil { waitForJobGrepMatch(...) } else { waitForJobDoneOrOutput(...) } }`. Final snapshot path unchanged (it re-greps for the result's `matches` — correctness does not depend on the wait's incremental state).
- [ ] **Step 3: Update the tool description** ("block=true with grep waits until a match exists, the job ends, or the timeout elapses") + the contract line, same commit.
- [ ] **Step 4: Run; full suite; commit** — `feat(job-control): job_read_output blocks until grep match`

---

## Phase 4 — Observer read grants (spec §5.1)

### Task 4.1: `watch_read_grant` jobstore kind

**Files:**
- Modify: `agent/internal/jobstore/event.go` (new kind + payload fields), `fold.go` (grant table on the fold state), `record.go` if the fold state lives there (`rg -n "type.*foldState|func Fold" agent/internal/jobstore/`)
- Test: `agent/internal/jobstore/fold_test.go`

- [ ] **Step 1: Failing test** — append `{kind: watch_read_grant, observer_session_id, watched job_id}` → store exposes `Grants()` containing the pair; reload from disk preserves it; duplicate appends are idempotent.
- [ ] **Step 2: Implement** — `EventWatchReadGrant EventKind = "watch_read_grant"`; payload fields `ObserverSessionID string \`json:"observer_session_id,omitempty"\`` on `Event`; fold into a `map[string]map[string]bool` (observer → watched job ids) exposed via a `Grants` accessor following the store's existing accessor conventions.
- [ ] **Step 3: Run; commit** — `feat(jobstore): watch_read_grant event kind`

### Task 4.2: Mint at watch creation

**Files:**
- Modify: `agent/job_watch.go` (`configureWatch` — after send-target validation; per-fire minting in `recordWatchSendsAndKick` for `watched`-resolved identities)
- Test: `agent/job_watch_test.go`

- [ ] **Step 1: Failing tests** — creating a watch with `send.to=<delegate job>` on job target J appends a grant `{observer_session_id: childID(send.to), watched: J}`; a `target="*"` watch grants nothing at create, then grants the concrete watched job on first recorded send for it; session-target watches grant nothing (nothing to read — frames carry transcript_ref).
- [ ] **Step 2: Implement** — resolve `send.to` job → record → `decodeRef(rec.TranscriptRef)` → childID (the same resolution `sendDelegateMessage` uses at `agent/job_delegate.go:272`); append the grant event (idempotence via the fold — appending an existing pair is a no-op read-side). Per-fire: in `recordWatchSend`, when `resolveWatchSendTarget` resolved a `watched` alias to a concrete job and the send target is a delegate, append the grant for that concrete job before persisting the pending.
- [ ] **Step 3: Run; commit** — `feat(job-control): mint observer read grants at watch creation`

### Task 4.3: Cross-store read-through

**Files:**
- Modify: `agent/job_delegate.go` (spawn config gains `parentGrantedJobRead func(observerSessionID, jobID string) (*jobstore.JobRecord, jobOutputReader, bool)` — define the small reader interface next to it; wire at the spawn sites `rg -n "parentSteer:" agent/job_delegate.go` shows the config literal)
- Modify: `agent/session_tools_jobs.go` (`jobReadOutputTool` / `readJobOutputSnapshot` — on local+nested miss, try the grant hop)
- Test: `agent/job_delegate_test.go`

- [ ] **Step 1: Failing tests** — observer child reads parent-owned watched job by id (content + status round-trip); a different child without a grant gets `target_not_found`; the grant hop works after the observer was resumed under a new job_id (mint→fire→resume→read, the canonical flow); parent session closed → graceful `target_not_found` (no panic).
- [ ] **Step 2: Implement** — parent side: a `Session` method `lookupGrantedJobRead(observerSessionID, jobID)` checking `s.jobManager.store` grants then returning a snapshot + a read closure over `jm.readOutput`; child side: in `readJobOutputSnapshot`'s miss path (`jobReadClosedStoreFallback` returns not-ok), consult `s.cfg.spawn.parentGrantedJobRead` before failing. All reads are jobstore-level (Session-free locking) — never touch parent Session state from the child.
- [ ] **Step 3: Contract:** add the grant rows from spec §8 to `docs/job-control.md` (`job_watch` section + a sentence in the `job_read_output` section), same commit.
- [ ] **Step 4: Run; full suite; commit** — `feat(job-control): observer read grants — cross-store job_read_output`

---

## Phase 5 — Docs closeout (spec §9 remainder)

### Task 5.1: Spec-delta + description audit

**Files:**
- Modify: `docs/superpowers/specs/2026-06-08-job-control-design.md` (delta note at top pointing here for watch delivery/grants/output_match)
- Modify: `docs/job-control.md` (sweep: every §8 row landed? `rg -n "appended while the watch is active|already-terminal|include_excerpt" docs/job-control.md`)
- Audit: `agent/internal/tool/definitions.go` (`DefJobWatch`/`DefJobReadOutput`/`DefJobSendMessage` describe the shipped behavior, no race warnings, grants + transcript_ref mentioned)

- [ ] **Step 1: Execute the sweep; fix stragglers.**
- [ ] **Step 2: Re-read `docs/architecture.md` § Ownership and mailboxes against the final code; correct drift.**
- [ ] **Step 3: `make test && make lint` at root; commit** — `docs(job-control): close out watch-mailbox contract + architecture docs`

### Task 5.2: Live validation

- [ ] **Step 1: Build + run live** per the recipe in the project memory (`go build -o /tmp/serf ./cmd/serf`; source `.env`; `--model oai-work/<model>`): drive the incident smoke shape — background shell job, sidecar observer with an events watch, `output_match` watch attached AFTER the token printed, `job_read_output(block, grep)` — and confirm: no wedge, observer receives a frame and reads the watched job, catch-up fires, blocking grep returns on match.
- [ ] **Step 2: Capture the transcript refs in the final commit message.** Commit any fixes uncovered, each with its own test first.

---

## Self-review notes (already applied)

- Spec §4.2's "drain-loop tail extension" is satisfied structurally by `finishNotificationNoop → finishProcessingAtBoundary → drain` (verified against `session_lifecycle.go:809-857`); no tail surgery task exists, and Task 1.5 documents why.
- Every §8 contract row maps to a task: 513/514/38/369 → 1.8; 534/506/542/546 → 3.3; 516 → 2.2/2.3; grant rows → 4.3; guard row → 2.1.
- Type consistency: `watchSendToken`, `recordWatchSend`, `recordWatchSendsAndKick`, `drainPendingWatchSends`, `settleWatchSendDelivered`, `SetScanOffset`/`SeedCarry` are each defined once and referenced by those exact names throughout.
- Known discovery points are marked as verify-first steps (output.Append return shape in 3.1; fold-state home in 4.1; spawn-config literal sites in 4.3) rather than assumed.
