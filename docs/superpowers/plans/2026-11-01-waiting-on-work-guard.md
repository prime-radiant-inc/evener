# Waiting-on-Work Guard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The communicate result tool refuses `end_turn=true` with `end_reason="waiting_on_work"` when nothing can provably wake the resting session, so a session can never rest forever on a claim that background work will wake it.

**Architecture:** One new Session predicate, `hasWakeableWork`, enumerates everything that can wake a resting session: `autonomyInFlight()` (pending notifications, queued input, delegate deliveries and attention, working children), running managed jobs (`sessionRunningJobIDs`, not detached processes because their completion sends no notification), a pending question (`HasPendingAsk`), and an armed watch in any job manager that can deliver to this session (`watchHolderSessions()` walk + a new `hasArmedWatch`). The communicate Exec consults it through a new `toolDeps.wakeableWork` hook and refuses the call before the first durable write, so the model keeps its turn and corrects itself: `done`, `needs_response`, or actually starting the work.

**Tech Stack:** Go, `agent` package (same-package tests), scripted `llm.ProviderAdapter` (`newExecutionSession`), golden prompt snapshots.

**Spec:** `docs/superpowers/specs/2026-11-01-waiting-on-work-guard-design.md`

## Global Constraints

- The refusal returns before `deps.deliverCommunicate` (`agent/session_tools_communicate.go:129`): no communicate transcript entry, no terminal capture (`deps.setCommunicateTerminal`, :191), no steering drain.
- The 2026-08-06 warn-first ruling for ending while jobs still run is untouched; the guard only reads state.
- A nil `deps.wakeableWork` never refuses (direct `toolDeps` constructions predate the hook).
- Delegates are inert: `endReason` is only computed when `deps.offersEndReason` (:98-103), so the guard needs no explicit delegate exemption.
- Tests assert behavior only, never error or copy text (`docs/developing-evener/testing.md`, "Copy Is Not a Test Oracle"). Package `agent`, `t.Parallel()` on every new test, no sleeps or wall-clock waits; `newTestJM` freezes the manager clock so seeded timers never fire.
- Go error style: lowercase, precedent `errors.New("communicate requires end_turn")` (:94). The refusal's text is reviewed in the diff, not asserted in tests.
- Local runs use `-count=1`. Format with gofmt; lint with the pinned `/Users/jesse/.local/bin/golangci-lint`.
- Goldens regenerate with `-update-prompt`; only root-session goldens may change (delegates do not offer `end_reason`).
- No new session state; no restore, hub, or client change; no prompt rewrite beyond the `end_reason` property description.

## Review Focus

1. **A spent one-shot watch must not count.** A one-shot that already fired stays in `jm.watches` until teardown; counting it would refuse legitimate turns. Its pending delivery is covered by the notification term instead. → Task 1, one row per spent signal from `watchOneShotHasFired`'s contract (job_watch.go:3078-3091: `firedPendingEnd`, `lastClockFire`, `deliveryTimes`).
2. **A pending job notification counts.** A job that finished mid-turn and was not yet consumed means the next wake already exists. → Task 2, `enqueueJobNotificationAndNotify` row.
3. **`end_turn=false` with `waiting_on_work` passes.** The reason is inert while the turn continues; only an ending call can rest on the claim. → Task 3 deps row.
4. **A delegate's ending call is never refused.** Delegates do not offer `end_reason` at all. → Task 3 deps row (`offered: false`).
5. **A refusal leaves nothing durable.** Neither the transcript entry nor the terminal capture happens. → Task 3 refusal row asserts both.

---

### Task 1: Armed-watch predicate on the job manager

**Files:**
- Modify: `agent/job_watch.go` (beside `liveTimerCountLocked`, :2149-2158)
- Test: Create `agent/job_watch_armed_test.go`

**Interfaces:**
- Produces: `func (jm *jobManager) hasArmedWatch() bool`
- Produces: `func (jm *jobManager) hasArmedWatchLocked() bool` (caller holds `jm.mu`)

- [ ] **Step 1: Write the failing test.** `TestJobManagerHasArmedWatch` (`t.Parallel()`), a table over `newTestJM(t)` fixtures (frozen clock; no timer fires during the test):

  - empty manager → `false`
  - repeating event watch: `jm.configureWatch(watchArgs{Target: "*", Events: []string{"job.notification"}})` → `true` (repeating watches never report spent, job_watch.go:3085)
  - armed timer: `jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, AfterSeconds: 3600})` → `true`
  - spent one-shot, seeded directly under `jm.mu` into `jm.watches` (map key type is `watchKey`, value `*watchConfig`) — three rows, one per spent signal: `&watchConfig{oneShot: true, firedPendingEnd: true}`; `&watchConfig{oneShot: true, lastClockFire: time.Now()}`; `&watchConfig{oneShot: true, deliveryTimes: []time.Time{time.Now()}}` → each `false`

  Assert only via `hasArmedWatch()` (the locking wrapper), so the table also covers the lock discipline.

- [ ] **Step 2: Run it to verify it fails.**

  Run: `go test ./agent -run TestJobManagerHasArmedWatch -count=1`
  Expected: FAIL (compile error: `hasArmedWatch` undefined).

- [ ] **Step 3: Implement both methods in `agent/job_watch.go`, beside `liveTimerCountLocked`.**

  `hasArmedWatchLocked` ranges `jm.watches` and returns `true` on the first `!watchOneShotHasFired(cfg)`; empty map → `false`. `hasArmedWatch` takes `jm.mu`, defers unlock, and delegates. Comment says the count is receiver-agnostic on purpose: the caller asks "could anything here deliver a wake to a session it holds," so overcounting is safe and undercounting is the only error.

- [ ] **Step 4: Run the test to verify it passes.**

  Run: `go test ./agent -run TestJobManagerHasArmedWatch -count=1`
  Expected: PASS

- [ ] **Step 5: Commit.**

  ```bash
  git add agent/job_watch.go agent/job_watch_armed_test.go
  git commit -m "feat(agent): count armed watches on the job manager"
  ```

---

### Task 2: Session wakeable-work enumeration

**Files:**
- Modify: `agent/session_state.go` (beside `autonomyInFlight`, :242)
- Test: Create `agent/session_waiting_on_work_test.go`

**Interfaces:**
- Consumes: Task 1's `hasArmedWatch`.
- Produces: `func (s *Session) hasWakeableWork() bool`

- [ ] **Step 1: Write the failing test.** `TestSessionHasWakeableWork` (`t.Parallel()`), a table over `newTestSessionForState(t)` fixtures (session_awaiting_test.go:21-29). One row per wake source, each asserting `true`, plus the bare session asserting `false`:

  - pending job notification: `s.enqueueJobNotificationAndNotify(jobNotification{})` (session.go:1201)
  - working subagent: track a running child the way session_awaiting_test.go:216-239 does
  - running managed job: append a `jobstore.EventJobStarted` event to `s.jobManager` with `OwnerSessionID: s.ID()`; a detached process receipt alone does not count because completion sends no notification
  - pending question: under `s.mu`, append a zero-value `askQuestion` to `s.askPending` (session.go:705; `askPendingCount` is length-based)
  - armed watch in own manager: `s.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600})`, with `defer s.Close()` on this row so the timer goroutine stops (the manager's timer validation requires `Source: "self"`, job_watch.go:1075-1078 — confirmed by Task 1)

  `autonomyInFlight`'s remaining terms (queued input, delegate deliveries, delegate attention) already own their tests (`TestAutonomyInFlight*`); the two rows above cover the representative inputs plus every term this change adds.

- [ ] **Step 2: Run it to verify it fails.**

  Run: `go test ./agent -run TestSessionHasWakeableWork -count=1`
  Expected: FAIL (compile error: `hasWakeableWork` undefined).

- [ ] **Step 3: Implement `hasWakeableWork` in `agent/session_state.go`, beside `autonomyInFlight`.**

  ```go
  func (s *Session) hasWakeableWork() bool {
      if s.autonomyInFlight() || s.HasPendingAsk() {
          return true
      }
      if len(sessionRunningJobIDs(s)) > 0 {
          return true
      }
      for _, holder := range s.watchHolderSessions() {
          if holder.jobManager.hasArmedWatch() {
              return true
          }
      }
      return false
  }
  ```

  Comment: used by the communicate handler to refuse `waiting_on_work` when nothing can wake a resting session; the watch term overcounts on purpose (receiver-agnostic, spent one-shots excluded) because refusing too little is the safe direction — see the spec.

- [ ] **Step 4: Run the test to verify it passes.**

  Run: `go test ./agent -run TestSessionHasWakeableWork -count=1`
  Expected: PASS

- [ ] **Step 5: Commit.**

  ```bash
  git add agent/session_state.go agent/session_waiting_on_work_test.go
  git commit -m "feat(agent): enumerate wakeable work for a resting session"
  ```

---

### Task 3: The communicate refusal

**Files:**
- Modify: `agent/session_tool_registry.go` (field after `runningJobIDs`, :146-154; wiring after :380)
- Modify: `agent/session_tools_communicate.go` (guard after the second `deps.abort` check, :125-127, immediately before `deps.deliverCommunicate`, :129)
- Test: Modify `agent/session_communicate_end_reason_test.go` (table `TestCommunicateDeliversItsEndReason`, :73-126)
- Test: Extend `agent/session_waiting_on_work_test.go` (the replay test)

**Interfaces:**
- Consumes: Task 2's `hasWakeableWork`.
- Produces: `toolDeps.wakeableWork func() bool` (nil means never refuse) and the Exec refusal itself.

- [ ] **Step 1: Extend the deps-level table in `TestCommunicateDeliversItsEndReason`.**

  Add a `wakeable bool` column and set in the deps literal (after `offersEndReason`, :105): `wakeableWork: func() bool { return tc.wakeable }`. Add a `captured *int` counter beside `delivered`, incremented inside the `setCommunicateTerminal` literal (:104). New/changed rows:

  - `"root states waiting_on_work"` (existing row, `wakeable: false`) → now `wantErr: true` (the refusal)
  - `"root states waiting_on_work with a live timer"` — `wakeable: true` → `want: "waiting_on_work"` (still delivered)
  - `"root states done with nothing live"` — done, `wakeable: false` → accepted
  - `"root states needs_response with nothing live"` — `needs_response`, `wakeable: false` → accepted
  - `"root keeps the turn going with waiting_on_work"` — `end_turn: false`, `end_reason: waiting_on_work`, `wakeable: false` → accepted, `want: ""`
  - `"delegate ends its turn"` (existing, `offered: false`, `wakeable: false`) → stays accepted

  For every refusal row assert BOTH `len(delivered) == 0` AND the terminal-capture counter was never hit (the existing `wantErr` branch already checks `delivered`; extend it with the counter check for the new rows — keep the whole branch common).

- [ ] **Step 2: Write the session-level replay test** (in `agent/session_waiting_on_work_test.go`), replaying the original failure: a session says it is starting work, ends its turn `waiting_on_work`, and the refusal must force a correction round. Use the scripted-adapter harness (`session_execution_test.go:48-52`; pattern `TestCommunicateRecordsItsEndReason`, :129-143):

  ```go
  func TestWaitingOnWorkRefusalKeepsTheSessionAlive(t *testing.T) {
      t.Parallel()
      s, adapter := newExecutionSession(t)
      adapter.script(
          respond(toolCallResponse(communicateCallArgs("comm-1", map[string]any{"message": "starting the rebase now", "end_turn": true, "end_reason": "waiting_on_work"}))),
          respond(toolCallResponse(communicateCallArgs("comm-2", map[string]any{"message": "done", "end_turn": true}))),
      )
      if _, err := s.ProcessInput(context.Background(), "talk", nil); err != nil {
          t.Fatal(err)
      }
      s.Close()
      communicates := entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate)
      if len(communicates) != 1 || communicates[0].Communicate.EndReason != "done" {
          t.Fatalf("communicates = %+v; want exactly the done call from the correction round", communicates)
      }
  }
  ```

  The assertion is airtight without extra bookkeeping: the first script step produces the `waiting_on_work` call, so if it had been delivered the single communicate entry would carry `waiting_on_work` and the turn would have ended before round two; only a refusal can leave `done` as the recorded reason. `"done"` is the enum token `tool.CommunicateEndReasonDone`'s value — assert it as a raw string or the constant, matching the file's existing rows.

- [ ] **Step 3: Run both to verify they fail.**

  Run: `go test ./agent -run 'TestCommunicateDeliversItsEndReason|TestWaitingOnWorkRefusalKeepsTheSessionAlive' -count=1`
  Expected: FAIL — the new rows deliver instead of refusing; the replay records `waiting_on_work`.

- [ ] **Step 4: Inventory existing session-level `waiting_on_work` usages.**

  Run: `rg -n '"waiting_on_work"' agent --glob '*_test.go'`
  Preflight already classified the current hits; re-run only to catch new arrivals:
  - `session_tools_misc_contract_fuzz_test.go:155` — state-machine contract, no communicate Exec runs; leave untouched.
  - `session_awaiting_test.go:105` (`TestProcessInput_CleanCompletionRestsByEndReason`) and `:348` (`TestRestore_RestsByEndReason`) — real sessions end a turn `waiting_on_work` with nothing live, so the guard would refuse them and break their clean-completion premise. Fix: before the turn, seed one live wake source on the session's own manager — `sess.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, AfterSeconds: 3600})` — so the ending is legitimate and delivered; all rest-state assertions stay exactly as they are. In the restore test the seed goes inside the `restoreAfter` setup callback, while the session is live (the restored state is derived from the persisted reason and is unaffected).
  - `session_communicate_end_reason_test.go:42` — enum list, unchanged; `:83` — the row this task rewrites.
  For every other hit that scripts a REAL session to end `waiting_on_work`: seed a Task 2 wake source the same way, because the refusal is the new correct behavior — never weaken the guard to keep an old expectation. Deps-harness hits stay as-is (they set `wakeableWork` explicitly). The restore-contract test (session_tools_misc_contract_fuzz_test.go) must stay untouched by this change; if it needs anything, stop and re-check the design before proceeding.

- [ ] **Step 5: Implement.**

  In `agent/session_tool_registry.go`, add the field to `toolDeps` after `runningJobIDs` (:146-154):

  ```go
  // wakeableWork reports whether anything can provably wake this session once
  // its current turn ends and it rests: pending autonomy input, running managed
  // jobs, a pending question, or an armed watch in any manager that can deliver
  // to this session. Detached processes are excluded because their completion
  // sends no notification. The communicate handler refuses end_reason
  // waiting_on_work when it returns false. Nil never refuses.
  wakeableWork func() bool
  ```

  and wire it after the `runningJobIDs` line (:380): `wakeableWork: s.hasWakeableWork,`

  In `agent/session_tools_communicate.go`, after the second `deps.abort(ctx)` check (:125-127) and immediately before `deps.deliverCommunicate` (:129):

  ```go
  if endTurn && endReason == tool.CommunicateEndReasonWaitingOnWork &&
      deps.wakeableWork != nil && !deps.wakeableWork() {
      return nil, errors.New("end_reason waiting_on_work requires work that will wake this session; none is pending: end with done if the work is finished, needs_response if your partner must act, or start the work or arm a watch before ending the turn")
  }
  ```

- [ ] **Step 6: Run both tests, then the file's neighbors, to verify they pass.**

  Run: `go test ./agent -run 'TestCommunicate|TestWaitingOnWork|TestSessionHasWakeableWork|TestJobManagerHasArmedWatch' -count=1`
  Expected: PASS

- [ ] **Step 7: Commit.**

  ```bash
  git add agent/session_tool_registry.go agent/session_tools_communicate.go \
    agent/session_communicate_end_reason_test.go agent/session_waiting_on_work_test.go
  git commit -m "feat(agent): refuse waiting_on_work when nothing can wake the session"
  ```

---

### Task 4: Contract copy, goldens, and docs

**Files:**
- Modify: `agent/internal/tool/definitions.go` (`end_reason` property description, :776 region)
- Regenerate: `agent/testdata/messagingprompt/root.md`, `agent/testdata/messagingprompt/root-renamed-result-tool.md`
- Modify: `docs/architecture.md` (`end_reason` paragraph, :237-243 region)
- Modify: `docs/product/subsystems.md` (S09 row)

**Interfaces:** Consumes Task 3's refusal; nothing downstream consumes this task.

- [ ] **Step 1: State the refusal in the `end_reason` description** where `CommunicateEndReasonWaitingOnWork` is described (`agent/internal/tool/definitions.go`, :776 region; the reasons' copy lives with `WithCommunicateEndReason`). Add one clause to the waiting_on_work sentence saying the call is refused when nothing can wake the session and naming the alternatives (`done`, `needs_response`, or starting the work). The enum token itself (`waiting_on_work`) must not change — it is a wire contract value.

- [ ] **Step 2: Regenerate the goldens and read the diff.**

  Run: `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1 -update-prompt`
  Expected: PASS, and the git diff touches ONLY the two root goldens. If any other golden moves (a delegate golden, any other prompt file), stop and re-check before committing.

- [ ] **Step 3: Reconcile prose elsewhere.** Grep `waiting_on_work` across `agent/prompts/` and `docs/`; add the same one-clause contract to `docs/architecture.md`'s `end_reason` paragraph and the S09 row in `docs/product/subsystems.md`. The system prompt template (`agent/prompts/system.md.tmpl`) describes ending behavior generically and is expected to need no change — verify, don't rewrite it if it already tells the truth.

- [ ] **Step 4: Run the golden test clean and commit.**

  Run: `go test ./agent -run 'TestDelegateMessagingGuidanceGoldens$' -count=1`
  Expected: PASS (no `-update-prompt`)

  ```bash
  git add agent/internal/tool/definitions.go agent/testdata/messagingprompt/root.md \
    agent/testdata/messagingprompt/root-renamed-result-tool.md docs/architecture.md docs/product/subsystems.md
  git commit -m "docs(agent): state the waiting_on_work refusal contract"
  ```

---

### Task 5: Gates

**Files:** none (verification only; commit only if a fix was needed)

- [ ] **Step 1: Focused suite.**

  Run: `go test ./agent -run 'TestJobManagerHasArmedWatch|TestSessionHasWakeableWork|TestCommunicate|TestWaitingOnWork|TestDelegateMessagingGuidanceGoldens$' -count=1`
  Expected: PASS

- [ ] **Step 2: Formatting and vet.**

  Run: `gofmt -l agent/job_watch.go agent/session_state.go agent/session_tool_registry.go agent/session_tools_communicate.go agent/job_watch_armed_test.go agent/session_waiting_on_work_test.go agent/session_communicate_end_reason_test.go && go vet ./agent`
  Expected: gofmt prints nothing; vet exits 0.

- [ ] **Step 3: Lint.**

  Run: `/Users/jesse/.local/bin/golangci-lint run ./agent/...`
  Expected: exit 0 (the pinned 2.13.1 binary the repo gates on; modernize prefers `new(expr)` for pointer literals).

- [ ] **Step 4: Meta-guard audits.**

  Run: `go test ./agent -run 'TestNoBareWallClockDeadlineInAgentTests' -count=1 && go test . -run TestNoNewCopyOnlyTests -count=1`
  Expected: PASS — proves the new tests contain neither bare wall-clock deadlines nor copy-only assertions. (The copy audit lives in the root package as `TestNoNewCopyOnlyTests`, not in `agent`.)

- [ ] **Step 5: Full agent package once, as the local source of truth for the change's blast radius.**

  Run: `go test ./agent -count=1`
  Expected: PASS. Fix anything this change caused; a failure it did not cause gets reported to Jesse, not silently patched into scope.

---

## Post-plan pipeline (already agreed with Jesse, outside this plan's tasks)

1. `parallel-adversarial-review` — two competing adversarial delegates over the branch diff.
2. `simplify-code` — four reviewer angles over `git diff main...HEAD`, apply fixes, re-run the suite.
3. Report, then ask Jesse about pushing a PR.
