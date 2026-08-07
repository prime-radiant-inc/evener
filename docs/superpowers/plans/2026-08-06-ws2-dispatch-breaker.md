# WS2: Failure-Aware Tool-Dispatch Breaker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No tool call with identical arguments and an identical error class
executes more than twice in a session — it is refused on the third attempt,
uniformly for native tools, MCP tools, and anything registered later. A call
that keeps returning the byte-identical *result* is nudged from the second
occurrence onward but always executes.

**What this does not do (read before relying on it).** The
300-identical-`set_viewport` incident is **not** impossible by construction.
Those 153 failures were reported `is_error: false`, so they reach only the
repetition trigger, which never parks (Jesse's ruling below), and they are
invisible to the failure-aware loop detector, which keys on `IsError`. What
WS2 gives that incident is a nudge on every repeat, not a stop. Refusal is
reserved for calls that genuinely report failure.

**Architecture:** A per-session ledger owned by the session's
`*tool.Registry` and consulted inside `Registry.ExecuteCall` — the single
chokepoint every tool call passes through (`agent/session_tools.go:440`
and `:541` are its only callers, and MCP tools register into the same
registry). The ledger carries **two independent triggers**, both keyed on
`tool name + args hash`, both nudging from the second occurrence — but only
the failure trigger ever refuses a call:

- **(a) Failure trigger** — consecutive failures sharing an error class.
  Nudges at 2, **parks at 3**.
- **(b) Repetition trigger** — consecutive calls returning byte-identical
  result bodies, regardless of error status. Repetition itself is the
  signal. **Nudge-only: it never parks** (Jesse, 2026-08-06).

**Why (b) never parks (Jesse's ruling, 2026-08-06).** Parking on repetition
assumes that identical arguments imply an identical result forever. That is
false for every tool that observes mutable state, and the consequences were
concrete: parking `communicate` refuses the session's only exit door, so
`setCommunicateResult` never runs and the turn spins to
`max_tool_rounds_per_input exhausted` (six agent tests proved it); parking a
third `read_file` refused exactly the read whose environment had changed
underneath it. A registration-time opt-out flag was offered and declined —
Jesse took the trade-off knowingly: repetition applies steering pressure, and
only genuine repeated *failures* are ever refused.

Trigger (b) exists because serf cannot rely on a tool's error signal. Verified
2026-08-06 from session `034163AU8MmLapfXKT7nMu`: all 153 identical
`set_viewport` failures are recorded `is_error: false` with the failure as
plain body text (`Error: set_viewport requires payload with width and
height: {...}`), byte-identical every time. That is the chrome MCP plugin not
setting `isError` (filed upstream as obra/superpowers-chrome#44), not a serf
recording bug — `ExecResult.IsError` is copied verbatim into the transcript at
`agent/session_tool_round.go:247`. **serf must not sniff error text or special-case
MCP** (Jesse, 2026-08-06); the generic repetition trigger catches the pattern
without serf knowing anything about any tool's error conventions.

**Tech stack:** Go, module `agent/` (`agent/internal/tool`, `agent`).

**Context (verified against this branch, 2026-08-06):**
- `Registry.ExecuteCall` (`agent/internal/tool/registry.go:480`) is stateless
  today: lookup → JSON parse → schema validate → middleware → `t.Exec` →
  `truncateResult`. Executor errors become `ExecResult{IsError: true}` with
  `res.Err` preserved. MCP's Channel-B path (`agent/internal/mcp/manager.go:423-437`)
  turns a server-set `result.IsError` into exactly such an executor error, so
  MCP failures reach the ledger through the ordinary dispatch layer — never
  through any recorded `is_error` flag.
- `Registry.Clone` (`registry.go:280`) copies the tool map and middleware
  slice only. Each session gets its own registry instance: `s.reg = reg`
  at `agent/session_init.go:985`, sourced from `newProfileToolRegistry`,
  which hands out `Clone()`s of a cached prototype
  (`agent/session_tool_registry.go:266-283`).
- `shortHash` exists in the `tool` package (`registry.go:738`) and
  independently in `agent` (`agent/runtime_dir.go:59`). **Do not assume they
  agree** — the ledger is keyed inside the `tool` package and queried by
  passing raw `(name, argumentsJSON)`, never a pre-built signature string.
- Loop detection: `detectLoop(signatures, window)`
  (`agent/session_loopdetect.go:45-70`), fed from `injectPostToolSteering`
  (`agent/session_tool_round.go:323-343`). Its caller
  (`agent/session_lifecycle.go:1178`) already holds the round's `results`.
- `rerunToolWithGrant` (`agent/session_tools.go:532-545`) re-dispatches the
  same call after a human approves a sandbox denial. That is a
  human-authorized retry and must not be judged by the breaker.
- `projectJobStatus` (`agent/session_tools_jobs.go:1883`) stamps
  `running_for_ms` and `quiet_for_ms` from `now` on every non-terminal job, so
  live polling is never byte-identical and cannot trip trigger (b).

## Global Constraints

Decided with Jesse — **do not relitigate**:

- Failure trigger: nudge at **2**, park at **3**.
- Repetition trigger: **nudge only, from 2 onward — it never parks**
  (Jesse, 2026-08-06).
- Failure-trigger nudge text, verbatim:
  `You just ran the same tool twice with the same arguments and got the same failure. Consider an alternate approach`
- Failure-trigger park sentence, verbatim (the tool name leads it, so the
  model reads which call was refused):
  `serf did not execute this call: <tool> with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach.`
- Park means the call is **not executed**.
- **No session-level tool fencing.** The per-signature failure park is the
  only enforcement tier.
- **No error-text sniffing and nothing MCP-specific.** Trigger (b) is generic
  or it does not exist.
- Loop-detector integration: a detected loop whose window is all failures
  skips tier-1 advice and goes straight to the structural intervention.
  Success loops keep today's steering-only escalation.
- Parked calls are recorded as ordinary error tool results — no new turn
  kind, no new event type.

Repo-wide:

- Await behavior, never timeouts. No test sleeps, no widened timeouts.
- Smallest reasonable change; no backward-compatibility shims.
- Error and intervention messages name the failing invariant in plain terms.
- TDD: a failing test precedes every implementation step.
- Multi-module gates before every commit: `go build ./...` and
  `go test ./...` in **both** the root module and `agent/`, exit codes only.
  Redirect output to a file and check the real exit code — never read `$?`
  after piping `go test` through `tail` or `grep`.

## Design decisions this plan fixes

**Ledger key.** `(tool name, args hash)`, where the args hash is the `tool`
package's `shortHash` over the raw argument bytes. Consequence, accepted: two
semantically identical calls whose JSON differs (key order, whitespace) are
different signatures. That matches the loop detector's existing behavior.

**Error class (trigger a).** No error taxonomy exists at the dispatch layer
(`llm/classify.go` and `llm/errorkind.go` classify *API* errors). Defined here:
`errorClass(output)` = SHA256[:8] of a normalized digest — first non-blank
line, trimmed, internal whitespace runs collapsed to one space, lowercased,
runs of ASCII digits replaced by `#`, truncated to 200 runes. Digit-stripping
is what makes `go test` timeouts (`... after 120.4s`) class-equal across
attempts. Safe because tool name and argument bytes are already pinned by the
key.

**Body hash (trigger b).** `shortHash` over the result body bytes, computed
**before** any nudge text is appended. Store the hash, never the body.

**Counter semantics.** Per signature, both counters live in one entry:
- Body counter: matching body hash increments; a different body hash resets it
  to 1.
- Failure counter: a failure whose class matches increments; a failure with a
  different class replaces the class and resets to 1; a **success sets the
  failure counter to 0** and clears the class and snippets.
- A success no longer deletes the entry — the entry must survive to carry the
  body hash. Entry lifetime is bounded by eviction, not by success.
- Calls to *other* signatures never reset a signature's counters:
  "consecutive" is per signature, because the observed pathologies interleave.

**Trigger precedence.** If both fire, the failure trigger's nudge wins (it is
the decided text and its streak is the one that can park). The repetition
nudge is appended only when the failure trigger has nothing to say.

**Parked calls are not recorded.** A park leaves the ledger entry untouched, so
the counters stay at their tripped values and every later identical call stays
parked. Recording the park's own output would reset the body hash and unpark
the next call — a bug the tests must pin.

**Read-only polling.** Live `job_status` bodies carry `running_for_ms` /
`quiet_for_ms` and are never byte-identical (verified above), so polling never
trips the repetition counter at all. Polling a *terminal* job repeatedly does
draw the repetition nudge — the intended "you already have this answer" case —
and still executes. No allowlist, no tool-name special cases.

**Where the state lives.** On the `*tool.Registry` instance, one per session,
rebuilt on resume. `Clone()` must start a **fresh** ledger — a clone is a new
dispatch scope, and the prototype cached in `profileToolRegistryCache` must
never leak state between sessions.

**Concurrency.** Tool batches can dispatch in parallel; the ledger carries its
own mutex, taken separately for the pre-dispatch check and the post-dispatch
record, never held across `t.Exec`.

**Bounded growth.** At most 512 live entries (raised from 256 because
successful signatures now retain entries), **LRU** eviction: every `record`
moves its signature to the most-recently-used end. Insertion-order FIFO was
wrong — a signature's survival tracked age since first sight, so a recurring
failing call surrounded by one-off traffic was evicted before it could trip,
which silently broke the "other signatures never reset a counter" invariant.

**What the ledger judges.** The untruncated result body (`FullOutput`). A
`TruncTail` tool renders every over-limit error behind the same truncation
banner; classing on the truncated text would read two unrelated failures as
one class and park a call that never repeated itself.

**WS9 hook.** Parked results are ordinary error results whose output begins
with the stable prefix `serf did not execute this call:`. WS9's `--health`
counts them by that prefix; nothing in WS2 implements health counting.

---

### Task 1: The failure ledger (pure) — DONE

**Files:** `agent/internal/tool/breaker.go`, `agent/internal/tool/breaker_test.go`

Trigger (a) only: `failureLedger` with `check`/`record`, `errorClass`
normalization, per-signature streaks, bounded eviction. Delivered and reviewed;
see the workstream ledger for shas.

### Task 2: The repetition trigger

**Files:**
- Modify: `agent/internal/tool/breaker.go`, `agent/internal/tool/breaker_test.go`

**Interfaces:**
- `failureEntry` gains `bodyHash string` and `bodyCount int`.
- `check(name string, args []byte) (failStreak int, repeatStreak int, snippets []string)`
- `record(name string, args []byte, isErr bool, output string) (failStreak int, repeatStreak int)`
- Success stops deleting the entry: it zeroes the failure counter, clears the
  class and snippets, and updates the body counter. Eviction cap rises to 512.
- Body hashing uses the package's `shortHash` over `[]byte(output)`.

- [x] **Step 1: Failing tests:** three identical successful bodies → repeat
  streak 3 while failure streak stays 0; a changed body resets the repeat
  streak to 1; identical *failure* bodies advance both counters together;
  a success after failures zeroes the failure counter but leaves the entry and
  its body counter live; different args hash → independent counters;
  interleaved other-tool calls preserve both counters; entries surviving
  success do not corrupt eviction at 513 signatures; `-race` concurrency
  over both counters.
- [x] **Step 2: Implement; tests green.**
- [x] **Step 3: Gates (root + agent, plus `-race` on `./internal/tool/`), commit**
  (`feat(tool): count consecutive identical result bodies alongside failures`).

### Task 3: Nudge at 2, park at 3, inside `ExecuteCall`

**Files:**
- Modify: `agent/internal/tool/registry.go`
- Modify: `agent/session_tools.go` (breaker bypass on the grant rerun)
- Create: `agent/internal/tool/breaker_dispatch_test.go`

**Interfaces:**
- `Registry` gains an unexported `breaker *failureLedger`, allocated in
  `NewRegistry`. `Clone` allocates a fresh one (it already calls
  `NewRegistry`; pin this with a test so a future refactor cannot share it).
- `ExecuteCall` changes at exactly two points:
  1. **Before** the tool lookup, unless bypassed:
     `failStreak, _, snippets := breaker.check(name, call.Arguments)`.
     If `failStreak >= 2`, return a parked `ExecResult` (`IsError: true`,
     `PrevalOnly` false — the breaker refused it, pre-validation did not) and
     **do not execute, and do not record**. The repetition counter never parks,
     so it is not consulted here.
  2. **After** the result is computed:
     `failStreak, repeatStreak := breaker.record(name, call.Arguments, res.IsError, res.Output)`
     — computed on the body **before** any nudge is appended. Then, if
     `failStreak >= 2`, append the failure nudge; else if `repeatStreak >= 2`,
     append the repetition nudge. Append a blank line then the text to
     `res.Output` (and `res.FullOutput` when non-empty). Appending after
     truncation is intentional: the nudge must survive the limiter.
     Restructure the tail of `ExecuteCall` minimally so every dispatched
     result funnels through one record point.
- Repetition nudge text: `You have now made this same call twice and received the identical result. Repeating it will not change the answer — use the result you already have, or change your approach.`
  It repeats on every subsequent identical result, because nothing else stops
  the loop once parking is off the table.
- Failure park text, exact template:

```
serf did not execute this call: <tool> with these exact arguments has now failed 3 times with the same error; it will not be executed again until you change the arguments or the approach.

The failures so far:
1. <snippet 1>
2. <snippet 2>
```

- Bypass: `func WithBreakerBypass(ctx context.Context) context.Context` plus an
  unexported context key in the `tool` package. `rerunToolWithGrant` wraps its
  ctx with it, because a human just approved that exact retry.

- [x] **Step 1: Failing test (failure path)** — a fake tool that always returns
  the same error. Call 1 plain; call 2 ends with the exact failure nudge; call
  3 not executed (the fake's invocation counter stays at 2) and carries the
  decided failure park sentence plus both snippets; call 4 with the same args
  still parked; call 5 with different args executes.
- [x] **Step 2: Failing test (repetition path)** — a fake tool returning a
  byte-identical body with `IsError: false` (the `set_viewport` shape). Call 1
  plain; calls 2 and 3 both carry the repetition nudge; the executor's
  invocation counter proves **every** call ran, since repetition never parks.
- [x] **Step 3: Failing test (park does not unpark)** — after a failure park,
  two more identical calls stay parked and the executor is never invoked again.
- [x] **Step 4: Failing test (no false positives)** — a tool whose body changes
  every call (a counter in the output) is never nudged across 10 calls; a
  fail/fail/succeed sequence does not park; and an identical-body **success**
  loop is never parked, only nudged (the `communicate` and mutable-state
  `read_file` regressions that drove the 2026-08-06 ruling).
- [x] **Step 5: Failing test (isolation + bypass)** — `Clone()` of a registry
  with a tripped signature dispatches it (fresh ledger); a `WithBreakerBypass`
  ctx executes a would-be-parked call.
- [x] **Step 6: Implement; all green.**
- [x] **Step 7: Wire the bypass into `rerunToolWithGrant`; agent-module test
  that an approved sandbox-denial rerun is not parked.**
- [x] **Step 8: Gates, commit**
  (`feat(tool): nudge repeated identical calls at 2 and park the dispatch at 3`).

### Task 4: Failure-aware loop detection

**Files:**
- Modify: `agent/session_tool_round.go`, `agent/session_loopdetect.go`,
  `agent/session_lifecycle.go`
- Create: `agent/session_loopdetect_failure_test.go`

**Interfaces:**
- `injectPostToolSteering` gains a `results []tool.ExecResult` parameter (the
  call site at `session_lifecycle.go:1178` already holds `results`) and a
  parallel `toolSigFailed *[]bool` accumulator alongside `toolSigs`. Results
  match calls positionally, as `persistToolResults` already does; a length
  mismatch means the round was aborted — treat every signature as non-failing
  and skip the failure path rather than guessing.
- **Failure-loop rule:** the loop is a *failure* loop iff **every** entry in
  the detection window failed. A mixed window contains real progress and keeps
  today's steering. Implement `allFailed(failed []bool, windowSize int) bool`
  next to `detectLoop`; test it independently.
- When `detectLoop` fires and `allFailed` holds, emit the structural
  intervention instead of `stuckEscalation(count)`, and do **not** bump
  reasoning effort (skipped by construction, since `stuckEscalation` is never
  entered). `s.loopDetectionCount` still increments so a later mixed loop
  escalates from the right tier. Message:

```
Every one of the last N tool calls failed, and they repeat the same pattern: <tool>[, <tool>]. Repeating a failing call cannot make it succeed. The most recent failure was:

<last failing result, first 500 chars>

Stop. Either change the arguments, or take a different approach to the goal.
```

  Emitted through the existing `events.EventLoopDetection` +
  `appendSteeringTurn(..., events.SteeringKindLoopDetected)` path.

- [x] **Step 1: Failing unit tests** for `allFailed`: all-true window → true;
  one success in the window → false; short slice → false.
- [x] **Step 2: Failing session test** — alternating failing and succeeding
  calls fill the window: tier-1 `stuckEscalation` text appears, structural text
  does not.
- [x] **Step 3: Failing session test** — all-failing repeats fill the window:
  structural text injected, tier-1 text absent, reasoning effort unchanged.
- [x] **Step 4: Implement; green.** Update the fuzz/coverage callers of
  `injectPostToolSteering`
  (`agent/session_tool_round_tail_coverage_fuzz_test.go`) for the new
  signature.
- [x] **Step 5: Gates, commit**
  (`feat(agent): failure loops skip straight to structural intervention`).

### Task 5: MCP coverage, replay acceptance, and docs

**Files:**
- Create: `agent/internal/mcp/breaker_iserror_test.go` (or extend
  `cov_channelb_test.go`, whose stub-server harness already exercises the
  `IsError` path)
- Modify: the docs page describing loop detection — find it by searching
  `docs/` for `loop detection` / `stuck`; if none exists, add a short section to
  the nearest agent-behavior doc rather than creating a new file.

**Interfaces:**
- **MCP failure test:** a stub MCP server whose tool always returns
  `result.IsError = true` with the same body, registered into a real
  `tool.Registry` through `mcp.Manager` exactly as production does. Assert the
  same 1/2/3 progression as Task 3, with the stub's request counter proving the
  third request never reached it. This is the "count failures where the Go
  error actually exists" proof: the ledger must see the dispatch-layer error
  from `CallTool`, never a recorded flag.
- **Replay acceptance test:** the `034163AU8MmLapfXKT7nMu` shape — a stub MCP
  server returning `IsError: false` with the byte-identical body
  `Error: set_viewport requires payload with width and height: {width,height,deviceScaleFactor?,mobile?}`
  — carries the repetition nudge from call 2 onward, proving serf needs no
  knowledge of the plugin's error convention. Every call still executes: the
  repetition trigger never parks.
- Docs: one paragraph stating both triggers, that the failure trigger nudges at
  2 and parks at 3 while repetition only ever nudges, that the ledger is
  per-session and per-signature, what resets each counter, and that a parked
  result is an ordinary error result.

- [x] **Step 1: Failing MCP failure test.**
- [x] **Step 2: Failing replay acceptance test.**
- [x] **Step 3: Green (expected: no production change — if one *is* needed,
  that is a real gap in Task 3's uniformity and gets fixed in `registry.go`,
  never in the MCP layer, and never by sniffing error text).**
- [x] **Step 4: Docs paragraph.**
- [x] **Step 5: Gates, commit**
  (`test(mcp): prove IsError and identical-body loops both feed the breaker`).

## Acceptance (whole workstream)

- A tool that always fails identically is dispatched at most twice per session
  per argument set, proven by an executor invocation counter in both the native
  and MCP tests.
- A replay of the `034163AU8MmLapfXKT7nMu` shape — identical args, identical
  `is_error: false` bodies — carries the repetition nudge from call 2 onward,
  and still executes every call.
- The second occurrence's result ends with the decided nudge sentence for its
  trigger, character for character; a third identical *failure* is an error
  result beginning `serf did not execute this call:` carrying the decided park
  sentence.
- `communicate` is never refused by the breaker, so a session can always reach
  its exit door; three identical successful `read_file` calls all execute.
- A changed body, a success, or a different error class resets the relevant
  counter; a live `job_status` polling loop is never nudged or parked.
- A park is never undone by the parked result itself.
- An all-failing loop produces the structural intervention; a mixed loop
  produces today's tier-1 steering.
- `go build ./...` and `go test ./...` exit 0 in the root module and in
  `agent/`.
