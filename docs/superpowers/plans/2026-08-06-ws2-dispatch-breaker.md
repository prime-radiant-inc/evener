# WS2: Failure-Aware Tool-Dispatch Breaker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No tool call with identical arguments and an identical error class
executes more than twice in a session. The 300-identical-`set_viewport`
pattern becomes impossible by construction, uniformly for native tools, MCP
tools, and anything registered later.

**Architecture:** A per-session failure ledger owned by the session's
`*tool.Registry` and consulted inside `Registry.ExecuteCall` — the single
chokepoint every tool call passes through (`agent/session_tools.go:440`
and `:541` are its only callers, and MCP tools register into the same
registry, surfacing `result.IsError` through the executor error path at
`agent/internal/mcp/manager.go:423-437`). Two enforcement effects: a nudge
appended to the second consecutive identical failure, and a park that
refuses the third dispatch. A fourth touchpoint makes the existing loop
detector failure-aware.

**Tech stack:** Go, module `agent/` (`agent/internal/tool`, `agent`).

**Context (verified against this branch, 2026-08-06):**
- `Registry.ExecuteCall` (`agent/internal/tool/registry.go:480`) is stateless
  today: lookup → JSON parse → schema validate → middleware → `t.Exec` →
  `truncateResult`. Executor errors become `ExecResult{IsError: true}` with
  `res.Err` preserved.
- `Registry.Clone` (`registry.go:280`) copies the tool map and middleware
  slice only. Each session gets its own registry instance: `s.reg = reg`
  at `agent/session_init.go:985`, sourced from `newProfileToolRegistry`,
  which hands out `Clone()`s of a cached prototype
  (`agent/session_tool_registry.go:266-283`).
- `shortHash` already exists in the `tool` package (`registry.go:738`) and
  independently in `agent` (`agent/runtime_dir.go:59`). **Do not assume they
  agree** — the ledger is keyed inside the `tool` package and queried by
  passing raw `(name, argumentsJSON)`, never by passing a pre-built
  signature string across the package boundary.
- Loop detection: `detectLoop(signatures, window)`
  (`agent/session_loopdetect.go:45-70`), fed from
  `injectPostToolSteering` (`agent/session_tool_round.go:323-343`), which
  builds signatures as `call.Name + ":" + shortHash(call.Arguments)`.
  Its caller (`agent/session_lifecycle.go:1178`) has the round's
  `results` in scope already.
- `rerunToolWithGrant` (`agent/session_tools.go:532-545`) re-dispatches the
  *same* call after a human approves a sandbox denial. That is a
  human-authorized retry and must not be judged by the breaker.

## Global Constraints

Decided with Jesse 2026-08-06 — **do not relitigate**:

- Nudge at **2** consecutive identical failures. Exact text, verbatim:
  `You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach`
- Park at **3**: the call is **not executed**; the result is a structural
  intervention carrying the error digest plus the decided sentence
  `this exact call has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach`
- **No session-level tool fencing.** The per-signature park is the only
  enforcement tier.
- Loop-detector integration: a detected loop whose window is all failures
  skips tier-1 advice and goes straight to the structural intervention.
  Success loops keep today's steering-only escalation.
- Uniform at the registry/dispatch layer: native + MCP + future tools.
  MCP `IsError` counts as a failure.
- Parked calls are recorded as ordinary error tool results — no new turn
  kind, no new event type.

Repo-wide:

- Await behavior, never timeouts. No test sleeps, no widened timeouts.
- Smallest reasonable change; no backward-compatibility shims.
- Error and intervention messages name the failing invariant in plain terms.
- TDD: a failing test precedes every implementation step.
- Multi-module gates before every commit: `go build ./...` and
  `go test ./...` in **both** the root module and `agent/`, exit codes only,
  never a grep'd pipe.

## Design decisions this plan fixes

**Ledger key.** `(tool name, args hash, error class)`. Args hash is the
`tool` package's own `shortHash(call.Arguments)` over the raw argument
bytes. Note the consequence, and accept it: two semantically identical calls
whose JSON differs (key order, whitespace) are different signatures. That
matches the loop detector's existing behavior; a model re-emitting the same
call reproduces the same bytes in practice.

**Error class.** No error taxonomy exists at the dispatch layer (`llm/classify.go`
and `llm/errorkind.go` classify *API* errors, not tool results). Per the
spec's allowance, define one concretely in this workstream:

```
errorClass(output string) string  // SHA256[:8] of the normalized digest
```

Normalization, in order: take the first non-blank line; trim; collapse
internal whitespace runs to one space; lowercase; replace every run of
ASCII digits with `#`; truncate to 200 runes. Digit-stripping is what makes
`go test` timeouts (`... after 120.4s`) and job-id-bearing errors class-equal
across attempts — without it the 28-identical-timeout pattern never trips.
It is safe here because the tool name and argument bytes are already pinned
by the key, so only the error text varies.

**Counter semantics.** Per signature: a success **deletes** the entry; a
failure whose class differs from the recorded class **replaces** the entry
with count 1; a failure whose class matches **increments**. Calls to *other*
signatures do not reset a signature's streak — "consecutive" is per
signature, so interleaving a `read_file` between two identical failing
writes still trips the breaker. This is deliberate: the observed pathologies
interleave.

**Read-only polling never trips.** `job_status` loops return success, and
success deletes the entry. No allowlist, no tool-name special cases.

**Where the state lives.** On the `*tool.Registry` instance, which is
one-per-session and rebuilt on resume. `Clone()` must start a **fresh**
ledger — a clone is a new dispatch scope, and the prototype cached in
`profileToolRegistryCache` must never leak failure state between sessions.

**Concurrency.** Tool batches can dispatch in parallel; the ledger carries
its own mutex, taken separately for the pre-dispatch check and the
post-dispatch record. Never held across `t.Exec`.

**Bounded growth.** At most 256 live entries, FIFO eviction of the oldest
inserted signature. Only failing signatures are stored, and each holds at
most two ~500-byte snippets.

**WS9 hook.** Parked results are ordinary error results whose output begins
with the stable prefix `serf did not execute this call:`. WS9's `--health`
counts them by that prefix; nothing in WS2 implements health counting.

---

### Task 1: The failure ledger (pure)

**Files:**
- Create: `agent/internal/tool/breaker.go`, `agent/internal/tool/breaker_test.go`

**Interfaces:**
- Produces (unexported to the `tool` package):
  - `type failureLedger struct` with `mu sync.Mutex`, `entries map[string]*failureEntry`, `order []string`.
  - `type failureEntry struct { class string; count int; snippets []string }`
  - `func newFailureLedger() *failureLedger`
  - `func (l *failureLedger) check(name string, args []byte) (streak int, snippets []string)` —
    the pre-dispatch read: current consecutive-failure count for the
    signature and the recorded snippets, without mutating.
  - `func (l *failureLedger) record(name string, args []byte, isErr bool, output string) int` —
    the post-dispatch write, returning the new streak (0 on success).
  - `func errorClass(output string) string` — the normalization above.
- Consumes: the package's existing `shortHash`.

- [ ] **Step 1: Failing tests** covering, each as a named subtest:
  identical failure twice → streak 2; a success in between → streak resets to
  1; a *different* error class for the same signature → streak resets to 1
  and the snippet list is replaced; a different args hash → independent
  streak; interleaved other-tool calls → streak preserved; `errorClass`
  equality across `timed out after 12.4s` / `timed out after 130.9s`;
  `errorClass` inequality across `file not found` / `permission denied`;
  eviction at 257 distinct failing signatures keeps the newest; concurrent
  `record` from 50 goroutines under `-race` yields a consistent total.
- [ ] **Step 2: Implement; tests green.**
- [ ] **Step 3: Gates (root + agent), commit**
  (`feat(tool): per-signature consecutive-failure ledger with error classing`).

### Task 2: Nudge at 2, park at 3, inside `ExecuteCall`

**Files:**
- Modify: `agent/internal/tool/registry.go`
- Modify: `agent/session_tools.go` (breaker bypass on the grant rerun)
- Create/extend: `agent/internal/tool/breaker_dispatch_test.go`

**Interfaces:**
- `Registry` gains an unexported `breaker *failureLedger`, allocated in
  `NewRegistry`. `Clone` allocates a fresh one (it already calls
  `NewRegistry`; assert this in a test so a future refactor cannot silently
  share it).
- `ExecuteCall` changes at exactly two points:
  1. **Before** the tool lookup, unless bypassed: `streak, snippets := breaker.check(name, call.Arguments)`.
     If `streak >= 2`, return a parked `ExecResult` — `IsError: true`,
     `PrevalOnly` untouched (false; the call was refused by the breaker, not
     by pre-validation) — built by `parkedResult(...)` and **do not execute**.
     The park itself is recorded as a failure of the same class so the entry
     stays alive and later identical calls stay parked.
  2. **After** the result is computed, on every return path that produced a
     real dispatch: `newStreak := breaker.record(name, call.Arguments, res.IsError, res.Output)`.
     If `res.IsError && newStreak == 2`, append to `res.Output` (and
     `res.FullOutput` when set) a blank line followed by the verbatim nudge
     text. Appending after truncation is intentional — the nudge must
     survive the limiter.
     Restructure the tail of `ExecuteCall` minimally so all result paths
     funnel through one record point; the error/Image/State/Text branches
     keep their current shapes.
- Parked result text, exact template:

```
serf did not execute this call: <tool> with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach.

The failures so far:
1. <snippet 1>
2. <snippet 2>
```

  (Snippets are the recorded failure outputs, first 500 chars each. The
  third failure is this refusal.)
- Bypass: `func WithBreakerBypass(ctx context.Context) context.Context` plus
  an unexported context key in the `tool` package. `rerunToolWithGrant`
  wraps its ctx with it, because a human just approved that exact retry.

- [ ] **Step 1: Failing test** — register a fake tool that always returns the
  same error. Assert: call 1 plain error, no nudge; call 2 error whose output
  ends with the exact nudge sentence; call 3 not executed (the fake's
  invocation counter stays at 2) and its output contains the decided park
  sentence plus both snippets; call 4 with the same args still parked;
  call 5 with different args executes.
- [ ] **Step 2: Failing test** — a fake tool that fails, fails, then succeeds:
  no park, and a subsequent failure starts a fresh streak. Plus a
  `job_status`-shaped tool that always succeeds, called 10 times: never
  nudged, never parked.
- [ ] **Step 3: Failing test** — `Clone()` of a registry with a live failing
  streak dispatches the same call without parking (fresh ledger), and a
  `WithBreakerBypass` ctx executes a would-be-parked call.
- [ ] **Step 4: Implement; all green.**
- [ ] **Step 5: Wire the bypass into `rerunToolWithGrant`; agent-module test
  that an approved sandbox-denial rerun is not parked.**
- [ ] **Step 6: Gates, commit**
  (`feat(tool): nudge identical failures at 2 and park the dispatch at 3`).

### Task 3: Failure-aware loop detection

**Files:**
- Modify: `agent/session_tool_round.go`, `agent/session_loopdetect.go`,
  `agent/session_lifecycle.go`
- Create: `agent/session_loopdetect_failure_test.go`

**Interfaces:**
- `injectPostToolSteering` gains a `results []tool.ExecResult` parameter
  (the call site at `session_lifecycle.go:1178` already holds `results`) and
  a parallel `toolSigFailed *[]bool` accumulator alongside `toolSigs`.
  Results are matched to calls positionally, as `persistToolResults` already
  does; a length mismatch means the round was aborted — in that case treat
  every signature as non-failing and skip the failure path rather than
  guessing.
- **Failure-loop rule:** the detected loop is a *failure* loop iff **every**
  entry in the detection window failed. A mixed window contains real
  progress and keeps today's steering. Implement as
  `func allFailed(failed []bool, windowSize int) bool` next to `detectLoop`,
  tested independently.
- When `detectLoop` fires and `allFailed` holds, emit the structural
  intervention instead of `stuckEscalation(count)` — and do **not** bump
  reasoning effort (tier 1's effort bump is skipped by construction, since
  we never enter `stuckEscalation`). `s.loopDetectionCount` still
  increments, so a later mixed loop escalates from the right tier.
  Message text:

```
Every one of the last N tool calls failed, and they repeat the same pattern: <tool>[, <tool>] . Repeating a failing call cannot make it succeed. The most recent failure was:

<last failing result, first 500 chars>

Stop. Either change the arguments, or take a different approach to the goal.
```

  Emitted through the existing `events.EventLoopDetection` +
  `appendSteeringTurn(..., events.SteeringKindLoopDetected)` path — no new
  event kind.

- [ ] **Step 1: Failing unit tests** for `allFailed`: all-true window → true;
  one success in the window → false; short slice → false.
- [ ] **Step 2: Failing session test** — drive a session (existing session
  test harness, fake LLM) whose model emits a failing call and a succeeding
  call alternately until the loop window fills: assert the tier-1
  `stuckEscalation` text appears and the structural text does not.
- [ ] **Step 3: Failing session test** — all-failing repeated calls fill the
  window: assert the structural intervention text is injected, the tier-1
  text is not, and reasoning effort is unchanged.
- [ ] **Step 4: Implement; green.** Update the fuzz/coverage callers of
  `injectPostToolSteering` (`agent/session_tool_round_tail_coverage_fuzz_test.go`)
  for the new signature.
- [ ] **Step 5: Gates, commit**
  (`feat(agent): failure loops skip straight to structural intervention`).

### Task 4: MCP coverage proof and docs

**Files:**
- Create: `agent/internal/mcp/breaker_iserror_test.go` (or extend
  `cov_channelb_test.go`, whose stub-server harness already exercises the
  `IsError` path)
- Modify: the tool-behavior docs page that describes loop detection, if one
  exists — locate it with a search over `docs/` for `loop detection` /
  `stuck`; if none exists, add a short section to the nearest agent-behavior
  doc rather than creating a new file.

**Interfaces:**
- Test: an MCP stub server whose tool always returns
  `result.IsError = true` with the same body, registered into a real
  `tool.Registry` through `mcp.Manager` exactly as production does. Assert
  the same 1/2/3 progression as Task 2: nudge on the second, refusal on the
  third, with the stub server's call counter proving the third request never
  reached it.
- Docs: one paragraph stating the nudge threshold, the park threshold, that
  the ledger is per-session and per-signature, that successes and different
  errors reset it, and that a parked result is an ordinary error result.

- [ ] **Step 1: Failing MCP test as described.**
- [ ] **Step 2: Green (expected: no production change needed — if a change
  *is* needed, that is a genuine gap in Task 2's uniformity and gets fixed
  in `registry.go`, not in the MCP layer).**
- [ ] **Step 3: Docs paragraph.**
- [ ] **Step 4: Gates, commit**
  (`test(mcp): prove IsError results feed the dispatch breaker; document it`).

## Acceptance (whole workstream)

- A tool that always fails identically is dispatched at most twice per
  session per argument set, proven by an executor invocation counter in both
  the native and MCP tests.
- The second failure's result ends with the decided nudge sentence,
  character for character.
- The third result is an error result beginning `serf did not execute this
  call:` and containing the decided park sentence.
- A success or a different error class resets the streak; a 10-call
  successful polling loop is never nudged or parked.
- An all-failing loop produces the structural intervention; a mixed loop
  produces today's tier-1 steering.
- `go build ./...` and `go test ./...` exit 0 in the root module and in
  `agent/`.
