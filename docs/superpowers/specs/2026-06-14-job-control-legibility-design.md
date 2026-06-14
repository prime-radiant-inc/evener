# Job-Control Legibility — Design Spec

**Goal:** Make job-control state and outcomes legible to the model at runtime, in one
additive PR, without changing any control-flow behavior.

**Source:** Triage of `jobs-report.md` (12 friction items) against the code on
`job-control-spec`. This PR takes the items that are *additive and low-risk* (new
fields, sharper messages, docs, one regression test) and bundles them. The three
items that require real concurrency/lifecycle design are deferred with rationale at
the end — cramming them in here would violate KISS and "one PR".

**Principle:** Every change is additive. No tool changes what it *does*; tools only
report *more clearly* what already happened. That keeps the PR reviewable and keeps
existing tests green except where they assert the absence of a new field.

**Non-goals:** launch-time watch attach, send-and-wait for live delegates, watch
ids / lifecycle state machine. (See Deferred.)

---

## Disposition of all 12 report items

| # | Report item | Disposition |
|---|-------------|-------------|
| 1 | Allowance hard to see/understand | **In** — surface in `job_list`, sharpen grant error |
| 2 | `max_wait_ms` misleading for live steer | **In (2a)** flag it; **Deferred (2b)** real send-and-wait |
| 3 | Watches racy; no launch-time attach | **Deferred** — own PR (highest-value next) |
| 4 | `clear=true` not idempotent | **In** — already idempotent in code; lock it with a test + doc |
| 5 | Watch lifecycle opaque (no ids) | **Deferred** — own PR |
| 6 | Delayed notifications confusing | **Deferred** — folds into #3/#5 |
| 7 | Sidecars too complex | **In (7a)** cookbook doc; **Deferred (7b)** helper |
| 8 | Structured-result invalid hard to observe | **In** — docs only; the flag already exists |
| 9 | Inconsistent error categories | **In (bounded)** — fix prefix-less leaks + document vocabulary |
| 10 | `job_stop` imprecise result | **In** — add `outcome` + `previous_status` |
| 11 | Root-only/leaf tools easy to forget | **In** — covered by #1 + docs |
| 12 | No guidance on wait primitives | **In** — decision table doc |

---

## Change 1 — Surface delegation allowance (items 1, 11)

**Files:**
- Modify: `agent/session_tools_jobs.go` — `jobListResult` (`:621`), `jobListTool` assembly (`:519`)
- Modify: `agent/job_delegate.go` — grant-rule error (`:146-148`)

**1a. Add allowance to `job_list`.** Add a field to `jobListResult`:

```go
type jobListResult struct {
	Jobs                []jobListEntry   `json:"jobs"`
	Count               int              `json:"count"`
	Watches             []watchListEntry `json:"watches"`
	DelegationAllowance int              `json:"delegation_allowance"`
}
```

Populate it at the assembly site (`:519`). Read under the session lock, matching the
existing pattern in `createDelegate` (`job_delegate.go:143-145`):

```go
s.mu.Lock()
allowance := s.delegationAllowance
s.mu.Unlock()
return marshalBoundedJobListResult(jobListResult{
	Jobs:                jobs,
	Count:               len(jobs),
	Watches:             jm.liveWatchSummaries(),
	DelegationAllowance: allowance,
}, maxChars)
```

This makes "what can I grant a child?" answerable from a tool the model already
calls, instead of only from the system prompt. It also resolves #11: a leaf agent
sees `delegation_allowance: 0` here, which is the same signal as the absent `delegate`
tool — no separate runtime probe needed.

**1b. Sharpen the grant error** to enumerate the valid range. Replace `job_delegate.go:147`:

```go
if args.DelegationAllowance >= ownAllowance {
	return delegateStartFailed(fmt.Errorf(
		"invalid_request: delegation_allowance must be less than your own allowance (%d); valid grants: %s",
		ownAllowance, validGrantRange(ownAllowance)))
}
```

Add the helper next to `createDelegate`:

```go
// validGrantRange renders the inclusive set a caller with `own` allowance may grant.
func validGrantRange(own int) string {
	if own <= 1 {
		return "0"
	}
	return fmt.Sprintf("0..%d", own-1)
}
```

**Tests** (`agent/job_delegate_test.go`, `agent/session_tools_jobs_test.go`):
- `job_list` result includes `delegation_allowance` equal to the session's allowance.
- A child with allowance 0 sees `delegation_allowance: 0`.
- Grant of `>= own` returns an error string containing `valid grants: 0` (own=1) and
  `valid grants: 0..2` (own=3).

---

## Change 2a — Flag ignored `max_wait_ms` on a live steer (item 2)

A live steer returns `action:"sent", status:"running"` immediately
(`job_delegate.go:928-936`); `max_wait_ms` is silently ignored. Don't change that —
just say so in the result, so the model never mistakes "delivered" for "answered".

**Files:**
- Modify: `agent/job_delegate.go` — `sendMessageResult` (`:102-124`)
- Modify: `agent/session_tools_jobs.go` — `jobSendMessageDelegateResult` (`:678-694`),
  mapping (`:749-770`), handler `jobSendMessageTool` (`:113`)

Add the field to the internal result type:

```go
type sendMessageResult struct {
	// ... existing fields ...
	WaitIgnoredReason string
}
```

Add to the JSON type:

```go
type jobSendMessageDelegateResult struct {
	// ... existing fields ...
	WaitIgnoredReason string `json:"wait_ignored_reason,omitempty"`
}
```

Copy it through in the mapping (`:749`): `WaitIgnoredReason: res.WaitIgnoredReason`.

Set it in the handler `jobSendMessageTool`, between `res := s.sendDelegateMessage(...)`
(`:138`) and `marshalSendMessageResult(res, maxChars)` (`:142`) — both the requested
wait and the computed result are in scope there. The rule lives at the boundary, not
inside `sendRunningDelegateMessage` (which has no knowledge of the requested wait):

```go
res := s.sendDelegateMessage(ctx, a)
if res.Err != nil {
	return "", res.Err
}
if a.BlockTimeoutMS > 0 && res.Status == jobstore.StatusRunning &&
	(res.Action == "sent" || res.Action == "busy") {
	res.WaitIgnoredReason = "live steer returns on delivery; max_wait_ms applies only to resumed jobs"
}
return marshalSendMessageResult(res, maxChars)
```

(`a.BlockTimeoutMS` is the clamped `max_wait_ms`, set at `:135`. A resumed job takes a
different branch and is unaffected.)

**Tests** (`agent/job_delegate_test.go`):
- Live steer with `max_wait_ms>0` → result has `wait_ignored_reason` set and
  `status:"running"`.
- Live steer with `max_wait_ms` absent/0 → `wait_ignored_reason` omitted.
- Resumed-job send with `max_wait_ms>0` → `wait_ignored_reason` omitted (it was honored).

---

## Change 10 — Precise `job_stop` result semantics (item 10)

`job_stop` returns `{job_id, status, reason}` (`session_tools_jobs.go:589-593`); the
caller can't tell a cancel from a race. Add two additive fields.

**Files:**
- Modify: `agent/session_tools_jobs.go` — `jobStopResult` (`:672-676`), `jobStopTool`
  body (`:540-594`)

```go
type jobStopResult struct {
	JobID          string  `json:"job_id"`
	Status         string  `json:"status"`
	Reason         *string `json:"reason"`
	PreviousStatus string  `json:"previous_status"`
	Outcome        string  `json:"outcome"`
}
```

`outcome` is one of: `cancelled_by_request`, `already_terminal`,
`completed_during_stop`, `stop_requested`.

Capture the pre-stop status *before* the stop signal. The body currently fetches `rec`
only after the stop (`:559`); add a read before it using the existing resolver:

```go
var previousStatus jobstore.Status
if _, pre, err := s.nestedOrLocalJobManager(jobID); err == nil && pre != nil {
	previousStatus = pre.Status
}
```

Classify after the final `rec` is known (replacing the return at `:589`):

```go
outcome := classifyStopOutcome(previousStatus, rec)
return marshalBoundedJSON(jobStopResult{
	JobID:          rec.JobID,
	Status:         string(rec.Status),
	Reason:         stringPtrOrNil(rec.Reason),
	PreviousStatus: string(previousStatus),
	Outcome:        outcome,
}, maxChars)
```

```go
// classifyStopOutcome distinguishes a stop that cancelled a live job from one that
// raced with, or arrived after, the job's own completion.
func classifyStopOutcome(previous jobstore.Status, rec *jobstore.JobRecord) string {
	if previous.IsTerminal() {
		return "already_terminal"
	}
	if rec == nil || !rec.Status.IsTerminal() {
		return "stop_requested" // still finalizing (e.g. reason "stop_pending")
	}
	if reason := stringValue(rec.Reason); reason == "cancelled" || reason == "stopped_by_parent" {
		return "cancelled_by_request"
	}
	return "completed_during_stop"
}
```

(Reuse an existing `*string`→`string` helper if one is already in the file; otherwise
inline the nil check. `"stop_pending"` is the synthetic reason set at `:582`.)

**Tests** (`agent/session_tools_jobs_test.go`):
- Stop a live shell job → `outcome:"cancelled_by_request"`, `previous_status:"running"`.
- Stop an already-terminal job → `outcome:"already_terminal"`, `status == previous_status`.
- Stop with `max_wait_ms` that times out on a still-running job → `outcome:"stop_requested"`,
  `reason:"stop_pending"`.

---

## Change 9 — Plug prefix-less error leaks + document the vocabulary (item 9, bounded)

Most job-tool errors already carry a stable prefix (`invalid_request:`,
`target_not_found:`, `target_not_resumable:`, `target_not_messageable:`,
`not_controllable:`, `target_terminal:`). The bounded fix is to stop the *leaks* —
errors that reach the model with no prefix — and document the prefix set as the stable
contract. A full constants-file refactor of already-prefixed sites is **out** (churn
without behavior change); note it as optional follow-up.

**Files:**
- Modify: `agent/job_delegate.go` — `"task is required"` (`:132`), `"target is required"`
  (`:222`), `"message is required"` (`:225`)
- Modify: `agent/session_tools_jobs.go` — `"job_id is required"` (`:533`), grep compile
  error (`:293-295`)

Prefix the bare "required" errors to match their siblings, e.g.:

```go
return delegateStartFailed(errors.New("invalid_request: task is required"))
```

Wrap the raw `regexp.Compile` error so it carries the category instead of leaking
`error parsing regexp: ...`:

```go
if _, err := regexp.Compile(grep); err != nil {
	return "", fmt.Errorf("invalid_request: invalid grep pattern: %w", err)
}
```

**Tests:** assert each touched error string now begins with `invalid_request:`.

**Doc:** the stable prefix vocabulary goes in `docs/job-control.md` (Change D) as the
machine-readable contract callers may branch on.

---

## Change 4 — Lock in `clear=true` idempotency (item 4)

`clearWatch` already returns `{watching:false}, nil` unconditionally
(`job_watch.go:994-997`), including for a terminal target. The report's
`target_terminal`-on-clear complaint is not reproducible against this code — it was
likely an older build. Don't change behavior; pin it with a regression test so it
can't silently regress, and document the guarantee.

**Files:**
- Test: `agent/job_watch_test.go`

**Test:** install a watch, let the target reach terminal, then `job_watch(clear=true)`
twice. Both calls return `watching:false` with no error. A second clear on a
never-watched target also returns `watching:false`, no error.

If this test *fails*, an upstream guard returns `target_terminal` before reaching
`clearWatch`; fix that guard so clear is unconditionally idempotent. (Expected: passes
as-is, closing the item.)

---

## Change D — Documentation (items 7a, 8, 9, 11, 12)

All doc edits land in the existing canonical `docs/job-control.md` (DRY — one home),
with a one-line pointer added from `README.md` if not already present.

**D1. Wait-primitive decision table (item 12).** A table mapping intent → primitive,
covering every wait mechanism that exists today:

| Intent | Use |
|--------|-----|
| Run a command, wait up to N for it | `shell(max_wait_ms=N)` (promotes to bg job at the bound) |
| Start a delegate, wait up to N for its result | `delegate(max_wait_ms=N)` |
| Be told when a backgrounded job finishes | background completion notification (automatic) |
| Wait until a job's output contains X | `job_read_output(grep=X, max_wait_ms=N)` or `job_watch(output_match=X)` |
| Re-observe progress on a long job | `job_watch(progress_interval_ms=N)` (running jobs only) |
| Resume a finished delegate and wait for its answer | `job_send_message(max_wait_ms=N)` |
| Steer a *running* delegate | `job_send_message` — returns on delivery; **`max_wait_ms` is ignored** (see `wait_ignored_reason`) |

Call out explicitly: `output_match` is the only watch type with terminal catch-up;
`events`/`progress`/`every` reject a terminal target with `target_terminal`.

**D2. Sidecar-observer cookbook (item 7a).** Document the working 7-step pattern from
the report as a copy-pasteable recipe: start observer → start watched job → `job_watch`
with `send.to=<observer>` → trigger → observer reads target → observer calls back via
`job_send_message(target="caller")` → clear the watch. Note that the watch grants the
observer read permission on the target.

**D3. Structured-result validation flow (item 8).** Document that
`structured_result_valid` + `structured_result_reason` are already returned by
`delegate` / `job_read_output` / `job_send_message`, with the four reasons
(`schema_validation_failed`, `schema_result_missing`, `schema_result_too_large`,
`schema_capture_failed`). State the order of operations: `communicate` accepts and
stores the raw `output` (coercing only the envelope's `message` to a string,
`session_tools_communicate.go:42`+`:90`); the `result_schema` is validated later at
delegate finalization (`jobs.go` `boundedStructuredResult`). So an invalid structured
value passes the `communicate` accept and surfaces as `structured_result_valid:false`
in the *parent's* read — that is the documented way to observe an invalid case.

**D4. Stable error-code vocabulary (item 9).** List the prefix set as the contract
callers may branch on: `invalid_request`, `target_not_found`, `target_not_resumable`,
`target_not_messageable`, `not_controllable`, `target_terminal`,
`active_delegate_not_found`, `active_delegate_ambiguous`. Note the prefix is stable;
the text after the colon is human-readable and may change.

**D5. Allowance + leaf note (item 11).** One paragraph: a session's
`delegation_allowance` shows in `job_list`; `0` means leaf (no `delegate` tool); a
child may be granted any value in `valid grants` from the delegate error. Note the
built-in `subagent` role is a non-delegating leaf regardless of granted allowance.

---

## Deferred (with rationale)

- **#3 launch-time watch attach** — atomically install a watch when a job starts, before
  it can race to terminal. Highest-value *next* change; eliminates the race behind #3,
  #6, and most of #7. Needs a watch spec threaded through the delegate/job-start path and
  installed before the run goroutine launches — real design, its own PR.
- **#2b send-and-wait for a live delegate** — "ask a running delegate and get its next
  answer." Genuine capability gap, but needs steering into the in-flight drive turn and
  capturing the next assistant output/terminal state. Concurrency work; own PR.
- **#5/#6 watch ids + lifecycle state** — stable ids and
  `{active|fired|auto_removed_terminal|cleared|replaced}` in `job_list`. The entry
  already carries `deliveries` + `created_at`; full lifecycle is a state machine — own PR.
- **#7b higher-level "watch + summarize" helper** — YAGNI until the D2 cookbook proves
  insufficient.
- **#9 constants-file refactor** of already-prefixed error sites — churn without behavior
  change; do opportunistically, not in this PR.

---

## Sequencing within the PR

Independent changes; any order. Suggested: 1 → 2a → 10 → 9 (code), then 4 (test), then
D (docs). Each is a small commit. Full gate (`make` / `go test ./agent/...`) green
before opening the PR.
