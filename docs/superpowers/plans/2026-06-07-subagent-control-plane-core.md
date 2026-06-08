# Subagent Control Plane (Core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the converged subagent control-plane core from `docs/subagent-management/00-subagent-control-plane.md` — the unified result snapshot, the `status`/`reason` axis, `cancel_agent`, `list_agents`, fail-loud retention, `subagent_output`, and the corrected tool descriptions — additively over the existing subagent machinery.

**Architecture:** All work lives in the `agent` package (plus the four `Def*` tool schemas in `agent/internal/tool` and one server DTO note). It extends the existing `subagent`/`subagentManager`/`Session` types; it does **not** introduce a new package, event bus, or job store. The proactive **notification** subsystem (`EntryNotification`, server wiring) is a **separate follow-on plan** — this plan stops at the tool/registry/retention/diagnostics core.

**Tech Stack:** Go (go.work multi-module; the `agent`, `llm` modules). Tests are standard `go test` table tests alongside the code (`agent/*_test.go`). Lint gate: `make lint` (golangci across all modules) — `go vet`'s `lostcancel` is in force.

**Read before starting:** `docs/subagent-management/00-subagent-control-plane.md` is the contract. This plan is the execution runbook; where it says "per spec §X" the spec holds the exhaustive field list, and the plan must not contradict it.

**Conventions for every task:** run the single new test with `go test ./agent/ -run <TestName> -v`; before each commit run `go build ./...` and `go test ./agent/ -run <Tests touched>`; commit only the files the task names. Do not run `git add -A`.

---

## File Structure

| File | Responsibility | Change |
| --- | --- | --- |
| `agent/subagents.go` | `subagent` struct, `subagentResult`, `SubagentStatus`, spawn/resume/wait/close/run, `resultSnapshotLocked`, `cancelAgent` | Modify (add fields, `reason`, cancel, snapshot helper) |
| `agent/subagent_manager.go` | per-parent registry: track/get/markClosed/drainForClose/infos, GC | Modify (retention, status filter, GC) |
| `agent/status.go` | `SubagentInfo` DTO | Modify (additive fields) |
| `agent/session_tools_subagent.go` | model-facing tool handlers + registration | Modify (cancel/list/output handlers; drop wrapper `agent_id` re-injection) |
| `agent/internal/tool/definitions.go` | tool schemas + descriptions | Modify (new `Def*`; rewrite descriptions) |
| `agent/redact.go` | **new** credential redactor (`standard`/`strict`) | Create |
| `agent/subagent_output.go` | **new** `subagent_output` dispatcher | Create |
| `agent/events/payloads.go` | `SubagentEndData` | Modify (add `reason`) |
| Tests | `agent/subagents_test.go`, `agent/subagent_manager_test.go`, `agent/subagent_output_test.go` (new), `agent/redact_test.go` (new) | Create/modify |

Root-only tool set becomes the seven of spec §"Root-only tool set"; every new tool joins `rootOnlyAgentManagementTools` in the same commit it is added.

---

## Task 1: Unified result snapshot (`agent_id` + `reason`), drop wrapper re-injection

**Spec:** §"Unified result snapshot", plan step 1. Goal: every snapshot carries `agent_id`, `status`, `reason`; `success == (reason=="completed")`; delete the blocking-wrapper `agent_id` parse-and-inject.

**Files:**
- Modify: `agent/subagents.go` (`subagentResult`, `resultSnapshotLocked` ~`:574-585`)
- Modify: `agent/session_tools_subagent.go` (blocking spawn ~`:71-95`, blocking resume ~`:127-145`)
- Test: `agent/subagents_test.go`

- [ ] **Step 1: Characterization test for the CURRENT snapshot shape** (lock today's bytes before changing them).

```go
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
```

Run: `go test ./agent/ -run TestResultSnapshot_CurrentShape -v` → PASS (documents current behavior). (`newTestSession` — reuse the existing subagent-test helper; grep `func newTestSession` / nearby helpers in `agent/*_test.go` and match the established pattern.)

- [ ] **Step 2: Failing test for the new fields.**

```go
func TestResultSnapshot_CarriesAgentIDAndReason(t *testing.T) {
	cases := []struct {
		name   string
		status SubagentStatus
		err    error
		wantReason  string
		wantSuccess bool
	}{
		{"completed", SubagentCompleted, nil, "completed", true},
		{"failed", SubagentFailed, errors.New("boom"), "failed", false},
		{"cancelled", SubagentCancelled, context.Canceled, "cancelled", false},
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
```

Run: `go test ./agent/ -run TestResultSnapshot_CarriesAgentIDAndReason -v` → FAIL (`AgentID`/`Reason` undefined).

- [ ] **Step 3: Add `SubagentCancelled` and the snapshot fields.** In `agent/subagents.go`, add the status const (full axis lands in Task 2; add `SubagentCancelled` here so this test compiles) and extend `subagentResult`:

```go
const SubagentCancelled SubagentStatus = "cancelled"

type subagentResult struct {
	AgentID       string         `json:"agent_id"`
	Status        SubagentStatus `json:"status"`
	Reason        SubagentStatus `json:"reason,omitempty"`
	Output        string         `json:"output"`
	Success       bool           `json:"success"`
	TurnsUsed     int            `json:"turns_used"`
	TranscriptRef string         `json:"transcript_ref,omitempty"`
}
```

In `resultSnapshotLocked`, set `AgentID: a.id`, `Reason: a.status` (the run outcome — equals status for a just-ended run), and `Success: a.status == SubagentCompleted` (equivalently `reason=="completed"`; for the `cancelled` case `a.err` is non-nil so the old `a.err==nil` would already be false — keep both equivalent and pick `reason=="completed"` to match the spec).

- [ ] **Step 4: Run both snapshot tests** → PASS. Run `go build ./...`.

- [ ] **Step 5: Delete the blocking-wrapper `agent_id` re-injection.** In `agent/session_tools_subagent.go`, the blocking spawn/resume paths currently parse `waitAgent`'s JSON and splice in `agent_id`. Since the snapshot now carries it, remove that parse-and-inject and return the wait result directly. Keep the non-blocking spawn `{agent_id,status:running}` and non-blocking resume `"ok"` returns unchanged.

- [ ] **Step 6: Test blocking spawn carries `agent_id` without the wrapper.**

```go
func TestBlockingSpawn_SnapshotHasAgentID(t *testing.T) {
	// drive a fake-adapter child to completion via blocking spawn; assert the
	// returned JSON has agent_id, status, reason, success, turns_used, transcript_ref.
}
```

Model it on the existing blocking-spawn test (grep `blocking` in `agent/*_test.go`). Run → PASS.

- [ ] **Step 7: Commit.**

```bash
git add agent/subagents.go agent/session_tools_subagent.go agent/subagents_test.go
git commit -m "feat(subagent): stamp agent_id/reason on result snapshot; drop wrapper re-injection"
```

---

## Task 2: Terminal axis + typing (`status` = job lifecycle, `reason` = run outcome)

**Spec:** §"Two axes", §"Result-lifecycle state machine", plan step 2.

**Files:**
- Modify: `agent/subagents.go` (status consts)
- Modify: `agent/events/payloads.go` (`SubagentEndData.Reason`)
- Test: `agent/subagents_test.go`

- [ ] **Step 1: Add the job-lifecycle constants.** In `agent/subagents.go` add `SubagentClosing SubagentStatus = "closing"` and `SubagentClosed SubagentStatus = "closed"` (cancelled already added in Task 1). Keep `running|completed|failed`. There is **no** `registered` state.

- [ ] **Step 2: Failing test: `SUBAGENT_END` carries `reason`.**

```go
func TestSubagentEndData_HasReason(t *testing.T) {
	d := events.SubagentEndData{AgentID: "x", Status: "completed", TurnsUsed: 2, Reason: "completed"}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"reason":"completed"`) {
		t.Fatalf("missing reason: %s", b)
	}
}
```

Run → FAIL.

- [ ] **Step 3: Add `Reason` to `SubagentEndData`** (`agent/events/payloads.go`), additive:

```go
type SubagentEndData struct {
	AgentID   string `json:"agent_id"`
	Status    string `json:"status"`
	TurnsUsed int    `json:"turns_used"`
	Reason    string `json:"reason,omitempty"`
}
```

Populate `Reason` at the `emit(EventSubagentEnd, ...)` site in `run` (`agent/subagents.go` ~`:542`) from `a.status`.

- [ ] **Step 4: Run** → PASS. `go build ./...`.

- [ ] **Step 5: Commit.**

```bash
git add agent/subagents.go agent/events/payloads.go agent/subagents_test.go
git commit -m "feat(subagent): add closing/closed statuses and reason on SUBAGENT_END"
```

---

## Task 3: Deterministic `SUBAGENT_START` ordering

**Spec:** §"Lifecycle events" (ordering fix), fact 1, plan step 3.

**Files:** Modify `agent/subagents.go` (spawn ~`:346-354`); Test `agent/subagents_test.go`.

- [ ] **Step 1: Failing test — START is emitted before the run goroutine does any work.** Drive a fake adapter whose first call signals a channel; assert the parent stream has `SUBAGENT_START` for the returned `agent_id` before/independent of `SUBAGENT_END`. Reuse the event-collection pattern from `TestSession_SubagentEndEvent_EmittedOnce` (`agent/session_dod_test.go`).

```go
func TestSpawn_StartEmittedBeforeRunGoroutine(t *testing.T) {
	// spawn non-blocking with an immediately-completing fake child;
	// assert the SUBAGENT_START event appears (no longer racing END).
}
```

- [ ] **Step 2: Move the emit.** In `spawnAgent`, relocate `s.emit(events.EventSubagentStart, ...)` to **before** the `go func(){ ... sub.run(...) }()` launch (it is currently after, `:351` vs `:346-349`). Keep it under the same `sendersWG`/`closingOrClosedLocked` gate. Idle-resume already emits before launch — leave it.

- [ ] **Step 3: Run** → PASS. `go build ./...`.

- [ ] **Step 4: Commit.**

```bash
git add agent/subagents.go agent/subagents_test.go
git commit -m "fix(subagent): emit SUBAGENT_START before launching the run goroutine"
```

---

## Task 4: `cancel_agent`

**Spec:** §"`cancel_agent`", plan step 4. The error-identity discriminator and the gate were verified sound across three review rounds — implement exactly as written.

**Files:**
- Modify: `agent/subagents.go` (`subagent` fields; spawn `:346-349` and idle-resume `:402-405` launch sites; `run` `:501-548`; new `cancelAgent`)
- Modify: `agent/session_tools_subagent.go` (handler + registration)
- Modify: `agent/internal/tool/definitions.go` (`DefCancelAgent`)
- Test: `agent/subagents_test.go`

- [ ] **Step 1: Add run-local fields.** On `subagent`: `cancel context.CancelFunc` and `cancelRequested bool`. Add `cancelRequested` to the idle-resume reset block (`:386-394`) alongside `startedAt`/`endedAt` (Task 5).

- [ ] **Step 2: Per-run cancellable context + defer cancel (no `lostcancel`).** At **both** gated launch sites, replace the bare `context.Background()`:

```go
runCtx, runCancel := context.WithCancel(context.Background())
sub.mu.Lock(); sub.cancel = runCancel; sub.cancelRequested = false; sub.mu.Unlock()
// (spawn only) emit SUBAGENT_START here — already moved in Task 3
go func() { defer s.sendersWG.Done(); defer runCancel(); sub.run(runCtx, input) }()
```

The `defer runCancel()` is mandatory — `go vet`'s `lostcancel` fails the build otherwise.

- [ ] **Step 3: `run` status mapping (error identity) + side-effect skip.** In `run`, under `sub.mu` at finalize:

```go
switch {
case a.cancelRequested && errors.Is(err, context.Canceled):
	a.status = SubagentCancelled
case err != nil:
	a.status = SubagentFailed
default:
	a.status = SubagentCompleted
}
```

And gate the nudge + `runSubagentStopHook` on `!a.cancelRequested` (skip both when a cancel was requested, regardless of `err` — covers the late-cancel `err==nil` case). Capture `cancelRequested` under `sub.mu` before using it.

- [ ] **Step 4: `cancelAgent` method.**

```go
func (s *Session) cancelAgent(agentID string) (any, error) {
	sub := s.getSub(agentID)
	if sub == nil {
		return "", fmt.Errorf("unknown agent_id: %s", agentID)
	}
	sub.mu.Lock()
	if !sub.running {
		sub.mu.Unlock()
		return "", fmt.Errorf("agent %s is not running", agentID)
	}
	sub.cancelRequested = true
	cancel := sub.cancel
	done := sub.done
	sub.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		return "", fmt.Errorf("timed out cancelling subagent %s", agentID)
	}
	sub.mu.Lock()
	result := sub.resultSnapshotLocked()
	sub.resultConsumed = true
	sub.mu.Unlock()
	b, _ := json.Marshal(result)
	return string(b), nil
}
```

- [ ] **Step 5: Schema + registration.** Add `DefCancelAgent()` to `agent/internal/tool/definitions.go` (`{agent_id: required string}`, root-only description per spec §"Tool descriptions"). Register it in `registerSubagentTools` (`agent/session_tools_subagent.go`) and **add `"cancel_agent"` to `rootOnlyAgentManagementTools`** (`agent/subagents.go:52`).

- [ ] **Step 6: Tests.**

```go
func TestCancelAgent_RunningChildBecomesCancelledAndResumable(t *testing.T) { /* cancel a blocked fake child; assert status cancelled, child idle; a following resume runs a fresh round */ }
func TestCancelAgent_GenuineFailureRacingCancelStaysFailed(t *testing.T) { /* fake child returns a non-context error while cancelRequested set; assert status==failed, not cancelled */ }
func TestCancelAgent_NotRunning(t *testing.T) { /* cancel an idle/terminal child → "is not running" */ }
func TestCancelAgent_ChildCannotCall(t *testing.T) { /* depth>0 registry lacks cancel_agent */ }
```

The second test is the round-4 regression guard — drive a fake adapter that returns `errors.New("provider 500")` and set `cancelRequested` before finalize; assert `SubagentFailed`.

- [ ] **Step 7: Run all four** → PASS. `go build ./... && go vet ./agent/...` (confirm no `lostcancel`). `make lint`.

- [ ] **Step 8: Commit.**

```bash
git add agent/subagents.go agent/session_tools_subagent.go agent/internal/tool/definitions.go agent/subagents_test.go
git commit -m "feat(subagent): add cancel_agent (interrupt run, keep child resumable)"
```

---

## Task 5: Record source state (`agent_type`, timestamps)

**Spec:** §"`list_agents`" source-state note, plan step 5. These are net-new fields, not "already available."

**Files:** Modify `agent/subagents.go` (`subagent` struct; capture in spawn/run/resume); Test `agent/subagents_test.go`.

- [ ] **Step 1: Add fields.** On `subagent`: `agentType string`, `createdAt time.Time`, `startedAt time.Time`, `endedAt *time.Time`. The spawn task/parent IDs are reachable from `sub.sess.cfg.spawn` — do **not** duplicate them.

- [ ] **Step 2: Capture points.** Set `agentType` + `createdAt` + `startedAt` at construction in `spawnAgent` (`agentType` is currently discarded ~`:155-164` — keep it). Set `endedAt` (`now`) in `run`'s finalize. In the idle-resume reset, re-stamp `startedAt` and clear `endedAt` (`endedAt = nil`).

- [ ] **Step 3: Test round-trip across resume.**

```go
func TestSubagentTimestamps_ResetOnResume(t *testing.T) {
	// spawn → complete (endedAt set) → resume → assert running with endedAt nil, startedAt re-stamped.
}
```

- [ ] **Step 4: Run** → PASS. `go build ./...`.

- [ ] **Step 5: Commit.**

```bash
git add agent/subagents.go agent/subagents_test.go
git commit -m "feat(subagent): record agent_type and run timestamps for list_agents"
```

---

## Task 6: `list_agents` + extended `SubagentInfo` + closed filter

**Spec:** §"`list_agents`", plan step 6.

**Files:**
- Modify: `agent/status.go` (`SubagentInfo`)
- Modify: `agent/subagent_manager.go` (`infoLocked`, `infos` filter, list method)
- Modify: `agent/internal/tool/definitions.go` (`DefListAgents`), `agent/session_tools_subagent.go` (handler + registration)
- Test: `agent/subagent_manager_test.go`

- [ ] **Step 1: Extend `SubagentInfo` additively** (`agent/status.go`) — keep `id`/`status`/`turns_used`; add `agent_id`, `reason`, `task`, `agent_type`, `parent_session_id`, `result_available`, `result_consumed`, `transcript_ref`, `created_at`, `started_at`, `ended_at`, `close_timed_out` per spec §"`list_agents`" record. Timestamps as `time.Time`/`*time.Time`.

- [ ] **Step 2: One snapshot builder.** Add `func (a *subagent) infoLocked(parentID string) SubagentInfo` and call it from both `subagentManager.infos()` and the new list method. `result_available = (terminal && !resultConsumed)`; `reason = a.status` when terminal else empty.

- [ ] **Step 3: `infos()` hides `closed` by default** (existing `/status` path). Keep `running`/`completed`/`failed`/`cancelled` visible.

- [ ] **Step 4: `list_agents` query method + filter** (status enum incl. `all`; `include_closed`), returning `{agents:[...],count:N}`.

- [ ] **Step 5: Tests.**

```go
func TestListAgents_RunningChildAppearsImmediately(t *testing.T) { /* non-blocking spawn → list shows it running with reason null */ }
func TestInfos_HidesClosedByDefault(t *testing.T) { /* a closed record is absent from infos(); completed stays */ }
func TestListAgents_IncludeClosed(t *testing.T) { /* include_closed=true surfaces closed */ }
```

Do **not** change `TestSubagentManager_InfosEnumeratesTracked` (it tracks running+completed, both still visible) — add the new closed-filter test instead.

- [ ] **Step 6: Schema + registration.** `DefListAgents()` per spec; register; **add `"list_agents"` to `rootOnlyAgentManagementTools`**. (Note in the plan, not code: the rich fields reach `list_agents` but not `/status` unless `server.SubagentStatusInfo` + `cmd/serf/serve.go:528-534` projector are extended — out of scope here.)

- [ ] **Step 7: Run** → PASS. `go build ./...`. `make lint`.

- [ ] **Step 8: Commit.**

```bash
git add agent/status.go agent/subagent_manager.go agent/internal/tool/definitions.go agent/session_tools_subagent.go agent/subagent_manager_test.go
git commit -m "feat(subagent): add list_agents registry query; hide closed from /status"
```

---

## Task 7: Fail-loud retention + GC

**Spec:** §"Retention and GC (fail loud)", plan step 7.

**Files:** Modify `agent/subagent_manager.go` (markClosed, GC, cap), `agent/subagents.go` (`closeAgent`, spawn cap-check + `Close()` on fail); Test `agent/subagent_manager_test.go`.

- [ ] **Step 1: `markClosed` replaces `remove`-on-close.** `closeAgent` (both the active-run path and the `done==nil` path) marks `closing` → closes session → marks `closed` and **retains** the final snapshot. On close timeout: keep `closing`, set `close_timed_out=true`, retain, return the close-timeout error. (Per spec, the `done==nil` branch is effectively unreachable for tracked subs but must still mark `closed` if reached.)

- [ ] **Step 2: Cap + fail-loud at spawn.** Add `maxRetainedTerminal` (default 128). In `spawnAgent`, **after** `NewSession` but **before** `track`: count retained records of status `completed|failed|cancelled|closed` only (`running`/`closing`/`close_timed_out` excluded); GC reclaimable (`closed`, then consumed terminal), oldest by `endedAt`; if still at cap, **`subSess.Close()` then return** an error naming the remedy (`close_agent`/`wait`). This mirrors the existing created-but-not-tracked cleanup at `agent/subagents.go:300`/`:335` — do not leak the session.

- [ ] **Step 3: Tests.**

```go
func TestClose_RetainsAsClosed(t *testing.T) { /* close → record stays, status closed, hidden from default list */ }
func TestRetention_FailLoudAtCap(t *testing.T) { /* fill cap with unconsumed terminal; next spawn fails naming remedy; created child is Closed (no leak) */ }
func TestRetention_GCReclaimsConsumedFirst(t *testing.T) { /* a consumed terminal is reclaimed so spawn succeeds */ }
func TestRetention_ClosingDoesNotCountTowardCap(t *testing.T) { /* close-timed-out records don't deadlock spawns */ }
func TestParentClose_DrainsAll(t *testing.T) { /* drainForClose still closes everything without holding the manager mutex */ }
```

- [ ] **Step 4: Run** → PASS. `go build ./...`. `make lint`.

- [ ] **Step 5: Commit.**

```bash
git add agent/subagent_manager.go agent/subagents.go agent/subagent_manager_test.go
git commit -m "feat(subagent): retain terminal records; fail-loud spawn at cap with no leak"
```

---

## Task 8: Redaction helper + `subagent_output`

**Spec:** §"`subagent_output`", §"Tool descriptions", plan step 8. Note: serf has **no** credential redactor today — build it first.

**Files:** Create `agent/redact.go`, `agent/subagent_output.go`, `agent/redact_test.go`, `agent/subagent_output_test.go`; Modify `agent/internal/tool/definitions.go`, `agent/session_tools_subagent.go`.

- [ ] **Step 1: Redactor failing test.**

```go
func TestRedactStandard_MasksCredentials(t *testing.T) {
	in := "token=sk-ABC123 Authorization: Bearer xyz\nAWS_SECRET_ACCESS_KEY=deadbeef"
	out := redact(in, redactStandard)
	for _, secret := range []string{"sk-ABC123", "xyz", "deadbeef"} {
		if strings.Contains(out, secret) {
			t.Fatalf("leaked %q: %s", secret, out)
		}
	}
}
```

- [ ] **Step 2: Implement `redact(s string, mode redactMode) string`** in `agent/redact.go` — `standard` masks API keys / bearer/OAuth tokens / `Authorization`/`Cookie`/`*-Key` headers / credential-looking env values; `strict` additionally omits high-risk tool args and raw JSONL bodies; `none` returns input unchanged (and is gated by the caller, never by default). Keep it regex-table-driven and small.

- [ ] **Step 3: Run redact test** → PASS.

- [ ] **Step 4: `subagent_output` dispatcher.** `agent/subagent_output.go`: flat schema with **runtime XOR validation** of `agent_id`/`transcript_ref` (not `oneOf`); `view=result` returns the retained snapshot **without consuming** (a closed record reports `status:"closed"`); `outline|markdown|jsonl` delegate to the existing transcript renderer (note: the renderer's enum is `outline|markdown|jsonl` — view name is `jsonl`, not `raw_jsonl`); enforce `max_bytes` after redaction; report `truncated`. Tolerate closed/absent registry entries for transcript-only reads.

- [ ] **Step 5: Schema + registration.** `DefSubagentOutput()`; register; **add `"subagent_output"` to `rootOnlyAgentManagementTools`** (now seven total).

- [ ] **Step 6: Tests.**

```go
func TestSubagentOutput_ResultIsNonConsuming(t *testing.T) { /* view=result then a following wait still returns+consumes */ }
func TestSubagentOutput_XORValidation(t *testing.T) { /* both agent_id+transcript_ref → error; neither → error */ }
func TestSubagentOutput_MaxBytesTruncatesAfterRedaction(t *testing.T) {}
func TestSubagentOutput_ChildCannotCall(t *testing.T) {}
```

- [ ] **Step 7: Run** → PASS. `go build ./...`. `make lint`.

- [ ] **Step 8: Commit.**

```bash
git add agent/redact.go agent/redact_test.go agent/subagent_output.go agent/subagent_output_test.go agent/internal/tool/definitions.go agent/session_tools_subagent.go
git commit -m "feat(subagent): add subagent_output diagnostic with standard/strict redaction"
```

---

## Task 9: Rewrite the model-facing tool descriptions

**Spec:** §"Tool descriptions (model-facing contract)", plan step 11 (in-code half).

**Files:** Modify `agent/internal/tool/definitions.go` (descriptions of all seven tools); Test `agent/builtin_agents_test.go` / `agent/plugin_agents_integration_test.go` (description assertions).

- [ ] **Step 1: Rewrite descriptions** per spec — `spawn_agent` (drop "then call wait()"; teach spawn-and-be-notified; keep parallel-fan-out); `resume_agent` (iterate same job; steer vs new run); `wait` (`transcript_ref` not `transcript`; the 120s rationale); `cancel_agent`; `close_agent` (destroy + retain a `closed` record — replace "removes the sub-agent"); `list_agents`; `subagent_output`. Lead with the shared mental model (job vs run; the async pattern; named anti-patterns; untrusted-output framing).

- [ ] **Step 2: Guard test — no stale wording.**

```go
func TestToolDescriptions_NoStaleWording(t *testing.T) {
	if strings.Contains(tool.DefWait().Description, "transcript_ref") == false {
		t.Fatal("wait must advertise transcript_ref")
	}
	for _, d := range []string{tool.DefWait().Description} {
		if strings.Contains(d, "`transcript`") { t.Fatal("stale transcript wording") }
	}
	if strings.Contains(tool.DefCloseAgent().Description, "removes the sub-agent") {
		t.Fatal("close now retains a closed record")
	}
}
```

- [ ] **Step 3: Run** → PASS. `go build ./...`.

- [ ] **Step 4: Commit.**

```bash
git add agent/internal/tool/definitions.go agent/builtin_agents_test.go
git commit -m "docs(subagent): rewrite tool descriptions for the job-control model"
```

---

## Task 10: Evergreen-doc cleanup

**Spec:** plan step 11 (docs half). The proactive-notification text in `00` stays (it's the contract for the follow-on plan); only retire `01`–`05`.

**Files:** Modify `docs/subagent-management/00-subagent-control-plane.md`; Delete `01`–`05`; Modify `06`–`10` (repoint references).

- [ ] **Step 1: Flatten `00`** — convert the Current/Target framing to present-tense reference for everything now implemented (Tasks 1–9). Leave the notification sections marked as the follow-on.

- [ ] **Step 2: Delete `01`–`05`.** `git rm docs/subagent-management/0{1,2,3,4,5}-*.md`.

- [ ] **Step 3: Repoint references.** Grep `06`–`10` for `01-`…`05-` cross-references; repoint to `00`. (`rg '0[1-5]-(job-registry|lifecycle-events|context-and-resume|raw-output|capability)' docs/subagent-management/`.)

- [ ] **Step 4: Verify** no dangling links: `rg '\b0[1-5]-' docs/subagent-management/06* 07* 08* 09* 10*` → no hits.

- [ ] **Step 5: Commit.**

```bash
git add docs/subagent-management/
git commit -m "docs(subagent): retire 01-05 (superseded by the control-plane spec)"
```

---

## Final verification

- [ ] `go build ./...` clean.
- [ ] `make lint` clean across all modules (no `lostcancel`).
- [ ] `make test` (or `go test ./agent/...`) green.
- [ ] Acceptance walk-through: re-read spec §"Acceptance criteria" and tick each line against a test (note the notification-related acceptance lines are deferred to the follow-on plan).
- [ ] The seven root-only tools are all in `rootOnlyAgentManagementTools` and a `depth>0` child registry has none of them (one integration test asserting all seven).

## Out of scope (follow-on plan)

The proactive **notification** subsystem — `EntryNotification` + `acceptNotificationInput` (drives a model turn, empty-queue no-op), the goal-gate interleave, the durable pending-notification queue, the parent `notify` closure, and the server-wired `notifyFunc`/`SubmitNotification` — is a separate plan (`2026-06-07-subagent-proactive-notification.md`, to be written). It depends on this plan's terminal-state machinery (`reason`, `resultConsumed`, the retained records) being in place.
