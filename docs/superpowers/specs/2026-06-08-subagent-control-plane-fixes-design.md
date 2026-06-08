# Subagent Control Plane — Post-Merge Fixes & Simplification (Design)

Status: Approved design (brainstorming-style). Drives one implementation plan.

## Context

The subagent control plane (core + proactive notification) shipped to `main` (`579be5ba`). A post-mortem plus an adversarial `/par` review of the evergreen doc surfaced four things to fix. Jesse's decisions are baked into this design:

1. **Remove secret redaction entirely** — "We won't get it right and claiming it will just get us in trouble." The shipped `subagent_output` redactor is a porous regex table that the doc/schema advertise as a security boundary, and `redaction:"none"`/`include_provider_raw` are ungated/inert (a model can pull a child's unredacted output by asking). Worse than nothing: false confidence.
2. **Merge `reason` into `status`** — `reason` is the run outcome (`completed`/`failed`/`cancelled`), redundant with `status` for every state except `closing`/`closed`. It exists only because `close_agent` overwrites `status` with `closed`, destroying the outcome, so C7 built `reason` + `lastOutcome` + `reasonLocked` purely to preserve it. Stop overwriting the outcome; make close a separate flag; delete the whole preserve mechanism.
3. **Correct the remaining `00` doc drift** the `/par` found (phantom record fields, a mis-attributed `defer`, two minor precision gaps).
4. **(Already done, `e3a26d56` on this branch)** the notification/goal-interleave description in `00` was corrected to the shipped fold-then-continue-inline design.

**Base / location.** Branch off `main` (`579be5ba`). Work is staged in the worktree at `/Users/jesse/prime-radiant/toil-suite/serf-wt-docfix` (branch `docs-fix-notification-goal`, which already carries fix #4). The implementation may continue on that branch or a fresh branch off main; it must NOT be merged without Jesse's review (main has branch protection: PR + `build-and-test`).

**Pre-1.0:** no backward-compatibility shims. Rename/remove freely.

---

## Fix 1 — Merge `reason` into `status`; delete the preserve-the-outcome mechanism

### The model
- **`status` becomes the run outcome only, immutable once terminal:** `running` → `completed` | `failed` | `cancelled`. After a run finalizes, `status` is never overwritten again. Drop `closing` and `closed` from the `SubagentStatus` enum.
- **Close-ness moves to flags on the record:**
  - `closed bool` — the session was torn down and the record is retained as history.
  - `close_timed_out bool` — (already exists) the `close_agent` wait exceeded its bound; the session was not confirmed closed (`closed` stays `false`).
- **A closed record is `{status:"completed", closed:true, …}`** — the outcome was never clobbered, so nothing needs preserving.
- **Deleted:** the `reason` field (everywhere it appears on the record/snapshot), the `lastOutcome` subagent field, and `reasonLocked()`. `runOutcomeReason()` is deleted or inlined (a plain `status ∈ {completed,failed,cancelled}` terminal check is all that remains).
- **`success` = `status == "completed"`** (replacing `reason == "completed"`).

### State → representation table
| state | `status` | `closed` | `close_timed_out` |
| --- | --- | --- | --- |
| run in progress | `running` | false | false |
| idle, resumable | `completed`/`failed`/`cancelled` | false | false |
| closed (retained) | `completed`/`failed`/`cancelled` | **true** | false |
| close timed out (stuck) | `completed`/`failed`/`cancelled` | false | **true** |

`close_agent` is synchronous (it blocks on the session-close wait, ≤5s), so the prior `closing` status was only ever observable by a *concurrent* reader during that window. The merged model does not give that transient its own record state: during the wait a concurrent reader sees the idle-terminal row; safety against a concurrent `resume`/`close` is already enforced by the child `Session`'s own `closing` guard (not the record's status), and the stuck case is captured by `close_timed_out`. Dropping the transient `closing` record-status is deliberate.

### Surfaces that change
- `subagentResult` (`agent/subagents.go`): drop `Reason`; add `Closed`; `Success` from `status`. Snapshot shape: `{agent_id, status, closed, output, success, turns_used, transcript_ref}`.
- `SubagentInfo` (`agent/status.go`): drop `Reason`; add `Closed` (keep `CloseTimedOut`).
- `events.SubagentEndData` (`agent/events/payloads.go`): drop the redundant `Reason` (the event already carries `status` = outcome). This removes the documented "SUBAGENT_END is the exception" wart — the record and the event now agree.
- `run` finalize: set `status` to the outcome (already does); remove the `lastOutcome` assignment.
- idle-resume reset: remove the `lastOutcome` reset; reset `closed`/`close_timed_out` to false (a resumed job is alive again).
- `close_agent` / the close path (`agent/subagents.go`): on success set `closed = true` (do NOT touch `status`); on timeout set `close_timed_out = true`, leave `closed = false`. Drop the `closing`/`closed` status writes and `markClosed`'s status mutation.
- `infoLocked` (`agent/subagent_manager.go`): emit `status`/`closed`/`close_timed_out`; `result_available = terminalStatus(status) && !resultConsumed && !closed`.
- `infos()` / `subagentMatchesFilter` / `listAgents`: hide records with `closed==true` by default; the `status` filter now filters by outcome; closed visibility is a `closed`/`include_closed` filter (and the old `status=closed` sentinel maps to `include_closed`). The `list_agents` schema's `status` enum loses `closing`/`closed`; add the `closed` field to the record.
- `reserveSlot` / `countsTowardCap` (`agent/subagent_manager.go`): a slot is occupied by a terminal record (`status ∈ {completed,failed,cancelled}`) that is **not** `close_timed_out`. GC reclaims `closed==true` records first, then consumed terminals, oldest by `endedAt` (unchanged intent).

### Doc (`00`) changes for Fix 1
Collapse the "Two axes: status and reason" section to a single `status` axis (run outcome) plus the `closed`/`close_timed_out` flags. Collapse the result-lifecycle table (no more `status ≠ reason` rows). Update the unified-snapshot shape, the `list_agents` record + schema, the `success` definition, and remove the `SUBAGENT_END` "status carries outcome, the one exception" paragraph (now the norm). Fix the absent-vs-`null` wire bug in passing (there is no `reason` to be `null`; `status` is always present).

### Acceptance (Fix 1)
- No `reason`/`lastOutcome`/`reasonLocked` symbols remain (`rg` clean).
- A child that completed then was closed reports `status:"completed", closed:true, success:true`; failed-then-closed → `status:"failed", closed:true, success:false`.
- `list_agents` default hides `closed:true`; `include_closed`/the closed filter surfaces them; running children show `status:"running"` and no `reason` key.
- The retention cap still GCs-then-fails, `close_timed_out` records still don't count, parent close still drains all.
- `SUBAGENT_END` carries `status` (outcome) and no `reason`.
- Full `make test` + `make lint` green; the existing subagent/notification/goal suites pass (updated where they assert `reason`).

---

## Fix 2 — Remove secret redaction entirely

### What changes
- **Delete** `agent/redact.go` and `agent/redact_test.go` (the `redact`/`redactMode`/`redactStandard`/`redactStrict`/`redactNone` surface).
- **`subagent_output`** (`agent/subagent_output.go`): remove `redactModeFromArg`, the `Redaction` result field, and every `redact(...)` call. The dispatcher returns the child's result snapshot / rendered transcript **raw**, still: non-consuming, `agent_id` XOR `transcript_ref`, `view ∈ {result,outline,markdown,jsonl}`, `max_bytes`-bounded with `truncated` reported, framed "archived evidence, not active instructions." This is exactly what `wait` already returns — `subagent_output` just stops pretending to sanitize.
- **Schema** (`agent/internal/tool/definitions.go` `DefSubagentOutput`): drop the `redaction` and `include_provider_raw` params. Rewrite the description to drop all redaction/sanitization language (keep non-consuming, XOR, views, `max_bytes`, archived-evidence).
- **Tests** (`agent/subagent_output_test.go`): delete the redaction-specific tests (env-dump-redaction, `MaxBytesTruncatesAfterRedaction` → re-anchor to truncate-after-render). Keep non-consuming/XOR/views/child-cannot-call.
- **Doc** (`00`): delete the `subagent_output` "Redaction" paragraph and the `redaction`/`include_provider_raw` schema fields; remove "redacted"/"gates provider-raw" from the per-tool description line and Implementation-map step 8; the notification section's "keeps the notification independent of the redactor" line can stay (it's now trivially true — there is no redactor).

### Acceptance (Fix 2)
- No `redact`/`Redact`/`redaction`/`include_provider_raw` symbols remain in `agent/` (the unrelated provider "redacted thinking" blocks in `llm/` are out of scope and untouched).
- `subagent_output{view:"result"}` on a child whose output contains `API_KEY=…` returns it **verbatim** (raw), bounded by `max_bytes`; the tool description makes no sanitization claim.
- `subagent_output` is still non-consuming, XOR-validated, root-only; `make test` + `make lint` green.

---

## Fix 3 — Correct the remaining `00` doc drift (`/par` findings)

Doc-only edits to `docs/subagent-management/00-subagent-control-plane.md`:
- **"Source state" phantom fields:** remove `parent_tool_call_id` and the optional diagnostics (`model`, `token_usage`, `tool_counts`, `last_error`) from the `list_agents` record provenance — none are on `SubagentInfo`. Correct the `parent_session_id` sourcing note (it is the parent session id passed to `infoLocked`, not read field-by-field from `cfg.spawn`).
- **`defer runCancel()` attribution:** the cancel section says "`run` calls `runCancel` on exit"; it actually lives in the launch-site goroutine wrapper (`go func(){ defer s.sendersWG.Done(); defer runCancel(); sub.run(...) }()`), not inside `run`. Fix the prose and the illustrative snippet (which currently omits the `defer runCancel()` line its prose references).
- **Gate-skip guard precision:** the notification section says the goal gate is skipped "for exactly one case: `ranKind == EntryNotification`"; the real guard is `ranKind != EntryNotification && !haveDeferredCont`. Note the `&& !haveDeferredCont` ("one fold per turn"); behavior is unchanged.
- **Suppress-at-drain set:** the delivery-time drop list says "consumed/closed/absent"; the code also drops `closing`/`closed` (post-Fix-1: a record with `closed==true` — keep the wording consistent with the merged model).

### Acceptance (Fix 3)
- Each corrected passage matches the code (verifiable by reading the cited symbols); `serf-docscheck`/`make lint` pass on the doc.

---

## Fix 4 — Notification/goal-interleave doc (DONE)

Already corrected in `e3a26d56` on this branch: `00` now describes the shipped fold-at-gate-first → defer → interleave → inline-continue → re-validate design, with the three invariants (breaker-accrual, inline-no-`kickFunc`, re-validate-on-clear/retarget). No further work; listed here for completeness so the plan doesn't re-touch it.

---

## Cross-cutting

- **Order of implementation:** Fix 1 (data model) is the largest and most interconnected; do it first with a characterization net so the `reason`→`status` flip is visible and the retention/list/snapshot tests are updated deliberately. Fix 2 (redaction removal) is independent and can follow. Fix 3 + the Fix-1 doc edits land together as the `00` reconciliation. Each fix is its own commit (or small group), TDD, `make test`/`make lint` green per commit (run the FULL `make lint`, not just golangci — `serf-namingcheck`/`docscheck` matter).
- **Tests:** update every test that asserts `reason` (subagent snapshot/list/event tests, the notification tests that seed records). Delete redaction tests. Add: a "closed record keeps its outcome" test, a "running record has no reason key / status is the outcome" wire test, a "subagent_output returns raw output" test.
- **Non-goals:** no change to the seven-tool surface (tools stay: spawn/resume/wait/close/cancel/list/subagent_output); no change to cancel's error-identity discriminator, the notification delivery mechanism, the retention fail-loud policy, or the goal-interleave logic (only their `reason`→`status` and doc touch-ups). The `llm/` provider "redacted thinking" code is unrelated and untouched. Approvals / `tools:all` intersection remain deferred (06/10).
- **Definition of done:** all four fixes implemented (1-3) or confirmed (4); `make test` + `make lint` green across all modules; `00` reads accurately against the code end-to-end; a short live re-check that `subagent_output` returns raw output and `list_agents` shows `status`+`closed`. Then pause for Jesse's review before merge (branch protection).
