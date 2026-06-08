# Subagent Proactive Notification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wake the parent session's model when a non-blocking child finishes — implement the proactive completion notification from `docs/subagent-management/00-subagent-control-plane.md` (§"Proactive completion notification"): a durable pending-notification queue drained by a dedicated `EntryNotification` turn that **drives a real model turn**, interleaved correctly with the goal engine, and triggered by a server-wired kick.

**Architecture:** A subagent, on terminal run end, enqueues a metadata-only notification on its parent and calls a `notify` closure (sibling of the existing `emit`, threaded through `subagentManager`). A new `EntryNotification` input kind has a dedicated `acceptNotificationInput` branch in `processOneInput` that drains the queue, composes one `TurnSteering` system-reminder, appends it to history, and lets the round loop drive a model request (mirroring `acceptContinuationInput`). A loop-tail hook **before the goal-continuation gate** covers the busy/mid-goal case; the server wires `notifyFunc`→`SubmitNotification` to wake an idle parent.

**Tech Stack:** Go (`agent`, `server`, `cmd/serf` modules). Tests: `go test ./agent/...` and `go test ./server/...`.

**DEPENDS ON:** the core plan (`2026-06-07-subagent-control-plane-core.md`) must be merged first. This plan reuses `reason`, `resultConsumed`, the retained terminal records, and the `run` finalize path it established. **It also upgrades the core plan's tool descriptions** (Task 6): the core plan should teach *spawn + wait/list* until notify is live; this plan flips them to *spawn-and-be-notified*.

**Read before starting:** spec §"Proactive completion notification" (the contract, incl. the 5 numbered delivery steps), and the existing goal-continuation machinery it mirrors: `acceptContinuationInput` (`agent/session_lifecycle.go:644-672`), the loop tail (`:290-314`), `armGoalContinuation` (`:310`, `agent/session_goal.go:141-162`), `SetKickFunc`/`kickFunc` (`agent/session_goal.go:14-21`, `agent/session.go:174`), `SubmitContinuation` (`server/server.go:473`), and the serve-loop dispatch (`cmd/serf/serve.go:376-398`).

**Conventions:** run a new test with `go test ./agent/ -run <Name> -v`; before each commit `go build ./... && make lint`; commit only named files. The verified design facts below were established across five adversarial review rounds — do not re-derive them, implement them.

**Verified facts to implement exactly (do not "improve"):**
- The notification must **drive a model turn** — appending a `TurnSteering` to history works because `prepareModelRequest` (`agent/session_model_call.go:124-126`) rebuilds the request from `s.history` each round, so a turn appended in the accept phase reaches the model **this** turn. A tail-append-then-idle (the rejected v4 design) never wakes the model.
- `acceptNotificationInput` must use the **bool-return shape of `acceptUserInput`** (`:385-387`), not the void shape of `acceptContinuationInput`, so the empty-queue case can return early. On empty queue it sets `sessionEndEmitted` so the no-op turn emits **no** phantom `SESSION_END`.
- It must skip the namer, `UserPromptSubmit`, and `s.turns++`/`MaxTurns` (which `acceptUserInput` at `:594-627` runs). It still emits one `SESSION_END{input_complete}` and bumps `modelResponses` when it does run a model turn — that's expected (a continuation does the same).
- `armGoalContinuation` (`:310`) must be short-circuited for `EntryNotification` (passing `wasContinuation=false` is NOT exclusion — it *resumes* an active goal). `goalRoundCap` needs **no** change (it only clamps `EntryContinuation`).
- The loop-tail notification drain runs **before** the goal-continuation gate, so notifications interleave between goal continuations rather than waiting for the whole chain.

---

## File Structure

| File | Responsibility | Change |
| --- | --- | --- |
| `agent/session.go` | `Session` fields | Modify (add `notifyFunc`, `pendingNotifs` queue + its mutex) |
| `agent/subagent_manager.go` | carry parent `notify` closure (like `emit`) | Modify |
| `agent/subagents.go` | `subagent.notify`/`notifyArmed`; arm on terminal run end | Modify |
| `agent/session_lifecycle.go` | `EntryNotification` kind; `acceptNotificationInput`; loop-tail drain hook; `SetNotifyFunc` | Modify |
| `agent/session_goal.go` | `armGoalContinuation` short-circuit for `EntryNotification` | Modify |
| `server/server.go` | `SubmitNotification` | Modify |
| `cmd/serf/serve.go` | wire `SetNotifyFunc`→`SubmitNotification` | Modify |
| `agent/internal/tool/definitions.go` | upgrade descriptions to spawn-and-be-notified | Modify |
| `docs/subagent-management/00-...md` | flatten notification sections to evergreen | Modify |
| Tests | `agent/notification_test.go` (new), `server/server_test.go` | Create/modify |

`subagentNotification` DTO (used throughout): `{AgentID, Status, Reason string; TurnsUsed int; TranscriptRef string}` — metadata only, **no** child output.

---

## Task 1: Pending-notification queue + parent `notify` closure + arm on terminal end

**Spec:** §"Proactive completion notification" steps 1–2.

**Files:** Modify `agent/session.go`, `agent/subagent_manager.go`, `agent/subagents.go`; Test `agent/notification_test.go` (new).

- [ ] **Step 1: Add the queue to `Session`** (`agent/session.go`):

```go
type subagentNotification struct {
	AgentID, Status, Reason, TranscriptRef string
	TurnsUsed int
}
// guarded by its own mutex; never taken while holding sub.mu or the manager mutex
pendingNotifsMu sync.Mutex
pendingNotifs   []subagentNotification
notifyFunc      func() // nil until the server wires it (Task 5); a nil kick is a no-op
```

Add `func (s *Session) enqueueNotification(n subagentNotification)` (append under `pendingNotifsMu`) and `func (s *Session) drainNotifications() []subagentNotification` (swap-out under the lock). The `notifyFunc` field exists here (nil) so Task 1's kick closure compiles; Task 5 adds its setter and wiring.

- [ ] **Step 2: Thread a `notify` closure through the manager** (mirror `emit`). In `subagentManager` add `notify func(subagentNotification)` set in `newSubagentManager`; in `NewSession` (`agent/session_init.go:114`) pass `s.enqueueNotification` plus a kick (`func(n){ s.enqueueNotification(n); if s.notifyFunc != nil { s.notifyFunc() } }`). On `subagent`, add `notify func(subagentNotification)` (captured from the manager at construction) and `notifyArmed bool`.

- [ ] **Step 3: Failing test — terminal run arms exactly one notification.**

```go
func TestRun_ArmsOneNotificationOnTerminal(t *testing.T) {
	// spawn a non-blocking fake child that completes; assert parent.drainNotifications()
	// returns exactly one entry with the child's agent_id, status=completed, reason=completed,
	// and NO output field. A second drain returns none (armed once).
}
```

Run → FAIL (`notify`/`notifyArmed` undefined).

- [ ] **Step 4: Arm on terminal end.** In `run`'s finalize (after status/`reason` set, under `sub.mu`, before unlocking): if `!a.notifyArmed && a.notify != nil`, set `a.notifyArmed = true`, capture a `subagentNotification` from the snapshot fields, and **after releasing `sub.mu`** call `a.notify(n)`. Reset `notifyArmed=false` in the idle-resume reset block (alongside the core plan's `cancelRequested`/timestamps). Never call `notify` under `sub.mu` or the manager mutex.

- [ ] **Step 5: Run** → PASS. `go build ./...`.

- [ ] **Step 6: Commit.**

```bash
git add agent/session.go agent/subagent_manager.go agent/subagents.go agent/session_init.go agent/notification_test.go
git commit -m "feat(subagent): enqueue a metadata notification on terminal run end"
```

---

## Task 2: `EntryNotification` kind + `acceptNotificationInput` (drives a model turn; empty no-op)

**Spec:** step 3.

**Files:** Modify `agent/session_lifecycle.go`; Test `agent/notification_test.go`.

- [ ] **Step 1: Add the kind.** In the `EntryKind` enum (`agent/session_lifecycle.go:163-171`): `EntryNotification` after `EntryContinuation`.

- [ ] **Step 2: Failing test — a notification turn makes a model request carrying the reminder.**

```go
func TestNotificationTurn_DrivesModelRequestWithReminder(t *testing.T) {
	// seed parent.pendingNotifs with one entry; run ProcessInputKind(ctx,"",nil,EntryNotification)
	// with a fake adapter that records the request; assert the request's history contains the
	// "<subagent-notification ...>" text (TurnSteering), the model WAS called, and the turn is
	// NOT a TurnUserInput (no user bubble) and did NOT increment s.turns.
}

func TestNotificationTurn_EmptyQueueIsNoOp(t *testing.T) {
	// empty pendingNotifs; ProcessInputKind(...EntryNotification): assert the fake adapter was
	// NOT called (no model request) and NO SESSION_END{input_complete} was emitted.
}
```

Run → FAIL.

- [ ] **Step 3: Add the dispatch branch.** In `processOneInput` (`:383`), add a third branch alongside `if kind == EntryContinuation { acceptContinuationInput } else if !acceptUserInput {...}`:

```
else if kind == EntryNotification {
    if !s.acceptNotificationInput(ctx) { return "", false, nil }
}
```

Implement `acceptNotificationInput(ctx) bool` modeled on `acceptContinuationInput` (`:644-672`) for the framing (compose one `TurnSteering`, `appendTurn(schema.TurnSteering, llm.User(reminder))`, skip namer/`UserPromptSubmit`/`s.turns++`/`MaxTurns`) but with `acceptUserInput`'s bool return:
- `notifs := s.drainNotifications()` (Task 4 adds the suppress filter); if empty → set `s.sessionEndEmitted = true` under `s.mu` and `return false` (the loop tail then emits no phantom `SESSION_END`; `processOneInput` early-returns before the round loop).
- else compose the reminder (one `<subagent-notification ...>` block per entry, joined), append it as `TurnSteering`, emit `EventSteeringInjected` (matching the interrupt-marker pattern at `:257-258`), and `return true` so the round loop builds the model request from `s.history`.

- [ ] **Step 4: Run both tests** → PASS. `go build ./...`.

- [ ] **Step 5: Commit.**

```bash
git add agent/session_lifecycle.go agent/notification_test.go
git commit -m "feat(session): EntryNotification turn drains the queue and drives a model turn"
```

---

## Task 3: Goal transparency — tail-drain before the goal gate + `armGoalContinuation` short-circuit

**Spec:** steps 4–5(b).

**Files:** Modify `agent/session_lifecycle.go` (loop tail), `agent/session_goal.go` (short-circuit); Test `agent/notification_test.go`.

- [ ] **Step 1: Failing test — a notification interleaves between goal continuations and doesn't perturb the goal.**

```go
func TestNotification_InterleavesWithActiveGoal(t *testing.T) {
	// set an active goal; mid-chain (a continuation turn just ran) seed a pending notification;
	// assert the NEXT loop iteration is an EntryNotification turn (the reminder reaches the model),
	// that it is NOT counted as a goal continuation (no-progress streak/iteration unchanged), and
	// that the goal RESUMES afterward via the normal idle-settle path.
}
```

Run → FAIL.

- [ ] **Step 2: Tail-drain hook before the goal gate.** In the `ProcessInputKind` loop tail (`:290-314`), after `popFollowUp`/`popQueueHead` and **before** the `armGoalContinuation` gate (`:310`), add: if `len(s.peekNotifications()) > 0`, set `next = ""`, `nextKind = EntryNotification`, and `continue` — so a pending notification runs as the next iteration (the head-drain in Task 2) ahead of any goal continuation. (`peekNotifications` = a non-draining length check under `pendingNotifsMu`; the actual drain happens in `acceptNotificationInput`.)

- [ ] **Step 3: Short-circuit the goal gate for notification turns.** Where the tail calls `armGoalContinuation(progressed, ranKind == EntryContinuation)` (`:310`), guard so a `ranKind == EntryNotification` turn does not arm/advance a goal continuation — e.g. skip the `armGoalContinuation` call entirely for `EntryNotification` (it neither advances nor terminates the goal). The active goal resumes afterward via the existing `settleGoalOnIdle` path (`:318`, `agent/session_goal.go:190-204`) — no change needed there. `goalRoundCap` (`agent/session_goal.go:102`) needs **no** change (it only clamps `EntryContinuation`).

- [ ] **Step 4: Run** → PASS. Also run the existing goal tests to confirm no regression: `go test ./agent/ -run Goal -v`.

- [ ] **Step 5: Commit.**

```bash
git add agent/session_lifecycle.go agent/session_goal.go agent/notification_test.go
git commit -m "feat(session): interleave notifications between goal continuations"
```

---

## Task 4: Suppress-at-drain (consumed / closed / absent)

**Spec:** §"Proactive completion notification" rules (suppress-at-delivery).

**Files:** Modify `agent/session_lifecycle.go` (`acceptNotificationInput` drain) and/or `agent/subagents.go` (a lookup helper); Test `agent/notification_test.go`.

- [ ] **Step 1: Failing tests.**

```go
func TestNotification_SuppressedWhenConsumed(t *testing.T) {
	// arm a notification, then wait()/blocking-consume the result BEFORE the drain;
	// assert the notification turn finds nothing to render (empty after filter → no-op).
}
func TestNotification_SuppressedWhenClosedOrAbsent(t *testing.T) {
	// close_agent the child (record retained as closed, or GC-reclaimed → absent) before drain;
	// assert suppression.
}
```

Run → FAIL.

- [ ] **Step 2: Add the filter.** In `acceptNotificationInput`, after draining the raw queue, for each entry look up the sub by `AgentID` (via the manager) and **drop** it if: the record is absent; the record's status is `closing`/`closed`; or `resultConsumed` is true (read under `sub.mu`, momentarily). Render only the survivors. If none survive → treat as empty (return `false`, suppress `SESSION_END`). Take `sub.mu` only per-entry and never while holding `pendingNotifsMu` or the manager mutex beyond the single `get`.

- [ ] **Step 3: Run** → PASS. `go build ./...`.

- [ ] **Step 4: Commit.**

```bash
git add agent/session_lifecycle.go agent/subagents.go agent/notification_test.go
git commit -m "feat(session): suppress notifications already consumed/closed at drain"
```

---

## Task 5: Server wiring — `SubmitNotification` + `SetNotifyFunc` + serve dispatch

**Spec:** step 5(a); §"Mode applicability".

**Files:** Modify `agent/session_lifecycle.go` (or `agent/session.go`) for `SetNotifyFunc`, `server/server.go`, `cmd/serf/serve.go`; Test `server/server_test.go`.

- [ ] **Step 1: `SetNotifyFunc`.** The `notifyFunc` field was added in Task 1 (nil). Add only `func (s *Session) SetNotifyFunc(f func())` mirroring `SetKickFunc` (`agent/session_goal.go:14-21`). The Task 1 notify kick already calls `s.notifyFunc()` (best-effort) after enqueueing; wiring it here makes the kick live.

- [ ] **Step 2: `SubmitNotification`.** In `server/server.go`, add a method mirroring `SubmitContinuation` (`:473-477`) that pushes `InputMessage{Kind: agent.EntryNotification}` (text-less) onto the 1-slot `inputCh` with the same non-blocking/drop-if-full semantics (a dropped kick is safe — the durable queue + tail-drain cover it).

- [ ] **Step 3: Wire it.** In `cmd/serf/serve.go`, next to the `SetKickFunc` wiring (`:299`), add `sess.SetNotifyFunc(func() { srv.SubmitNotification() })`. Confirm the serve loop's dispatch (`:376-398`) already routes `msg.Kind` through `ProcessInputKind` — it passes `msg.Kind` generically, so `EntryNotification` flows without a new case.

- [ ] **Step 4: Tests.**

```go
func TestSubmitNotification_PushesEntryNotification(t *testing.T) {
	// call SubmitNotification(); assert an InputMessage{Kind: EntryNotification} is readable from InputCh().
}
func TestSubmitNotification_DropIfFull(t *testing.T) {
	// fill the 1-slot channel; SubmitNotification() does not block and drops safely.
}
```

Run → PASS.

- [ ] **Step 5: Verify the idle-wake end to end** (serve mode): a small integration test (or manual `serf` run note) that a non-blocking spawn whose fake child finishes wakes the parent with a `<subagent-notification>` on the next turn. Confirm one-shot `serf run` (`cmd/serf/run.go:210`) wires **no** notifyFunc (so it doesn't deliver — intended).

- [ ] **Step 6: Commit.**

```bash
git add agent/session.go agent/session_goal.go server/server.go cmd/serf/serve.go server/server_test.go
git commit -m "feat(server): wire SubmitNotification to wake an idle parent on child completion"
```

---

## Task 6: Tool-description upgrade + spec evergreen flatten

**Spec:** §"Tool descriptions" (the async-pattern half); §"Implementation plan" step 11 (notification half).

**Files:** Modify `agent/internal/tool/definitions.go`, `docs/subagent-management/00-subagent-control-plane.md`; Test `agent/builtin_agents_test.go`.

- [ ] **Step 1: Flip descriptions to spawn-and-be-notified.** Now that notify is live, update `spawn_agent`/`resume_agent` descriptions from the core plan's "spawn + wait/list" wording to the spec's canonical async pattern: spawn non-blocking → return to your work → you are auto-notified (`<subagent-notification>`) → read with `wait`/`subagent_output`. Keep the named anti-patterns and the one-shot-`serf run` caveat (use `blocking`/`wait` there).

- [ ] **Step 2: Guard test.**

```go
func TestSpawnDescription_TeachesNotification(t *testing.T) {
	d := tool.DefSpawnAgent().Description
	if strings.Contains(d, "call wait() on each agent_id") {
		t.Fatal("stale poll/block guidance")
	}
	if !strings.Contains(d, "notif") {
		t.Fatal("spawn must teach the auto-notification pattern")
	}
}
```

Run → PASS.

- [ ] **Step 3: Flatten the spec.** In `00-subagent-control-plane.md`, convert the notification sections (Contract, Delivery mechanism, Mode applicability) from Target framing to present-tense evergreen reference; move the notify-specific Open Questions to a short "Known limitations" note (one-shot non-delivery; batching bound).

- [ ] **Step 4: Commit.**

```bash
git add agent/internal/tool/definitions.go agent/builtin_agents_test.go docs/subagent-management/00-subagent-control-plane.md
git commit -m "docs(subagent): teach the auto-notification pattern; flatten notify spec to evergreen"
```

---

## Final verification

- [ ] `go build ./...` clean; `make lint` clean (no `lostcancel`); `make test` green.
- [ ] Re-read spec §"Proactive completion notification" + the notify acceptance/test lines; tick each:
  - terminal child drives exactly one `EntryNotification` turn that makes a model request carrying the reminder;
  - the turn is `TurnSteering`-framed, no namer/`UserPromptSubmit`, no `s.turns++`/`MaxTurns`, no goal-continuation arming;
  - empty queue → no model request, no phantom `SESSION_END`;
  - consumed/closed/absent → suppressed at drain;
  - dropped kick with a turn already running still surfaces via the tail `continue` (no double-delivery, pop-once);
  - one-shot `serf run` does not deliver.
- [ ] Goal regression suite green (`go test ./agent/ -run Goal`).

## Known cross-plan note

The core plan's Task 9 rewrote tool descriptions. If the core landed with "spawn-and-be-notified" wording before this plan, Task 6 Step 1 is a no-op verify; if it landed with the honest "spawn + wait/list" interim wording, Task 6 performs the upgrade. Either way the guard test in Step 2 is the source of truth.
