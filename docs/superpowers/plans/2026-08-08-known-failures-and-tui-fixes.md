# Known-failures-and-TUI fix plan

Branch: `wip/known-failures-and-tui` (created off `main` at `bd37b4622`).
Repo: `/Users/jesse/git/prime-radiant-inc/serf`.

Six independently known-failing tests/gaps on `main`, each with its own root
cause. Every task must reproduce the failure first (paste the exact command
and output as evidence), then fix the root cause (never the symptom), then
prove green repeatedly with a narrow `-run` scope.

## Global Constraints

- **Model policy (Jesse-directed):** fastest/lightest model that can do the
  task. Haiku for mechanical/small edits, sonnet for everything else
  including reviews. Never opus.
- **TDD / evidence discipline:** reproduce the failure with the exact
  narrow `go test -run <Name> ...` command first and record its output.
  Only then implement the fix. Then re-run the same narrow command
  (repeated — `-count=5` or more, or several consecutive runs, is
  reasonable evidence for a race/flake fix) and record the passing output.
  Implementer self-review is required but does not replace task review.
- **Root cause only.** No `t.Skip`, no widened timeouts, no sleeps-as-fix,
  no serializing a genuine production race just to make the detector quiet.
  Follow `docs/testing.md`'s "Flakes and Timeouts" section for any task
  that is timing-related (C, D, E): await the actual completion; never
  widen or hardcode a timeout to absorb load-dependent work.
- **Scope discipline:** workers verify with a narrow `-run` scope only —
  do not run the full suite or other packages' tests. The controller runs
  full-suite verification after merging every task.
- **Do not touch:** `docs/superpowers/specs/**`, `.kata.toml`,
  `serf-transcript-v2-upgrade`. These are pre-existing, unrelated, already
  either modified or untracked in the working tree — leave them exactly as
  found.
- **Git hygiene:** never `git add -A`. Stage only the files the task
  touches. Commit messages explain the root cause and the fix, not "fix
  test."
- **Lint:** if you run `golangci-lint` locally as part of self-review, run
  `golangci-lint cache clean` first — stale cross-worktree cache entries
  produce false negatives/positives.
- **Style:** match the surrounding file's existing style and comment
  density exactly (this codebase's doc comments are unusually thorough —
  match that bar, don't strip it down).

## Wave plan

- **Wave 1 (parallel, worktree-isolated):** Task 1 (A), Task 2 (B),
  Task 3 (C), Task 4 (D). Fully disjoint packages, verified by file map.
- **Wave 2 (parallel, worktree-isolated):** Task 5 (E), Task 6 (F).
  Verified disjoint: Task 5 touches only `agent/session_slash_command_test.go`
  (and possibly `agent/session_capabilities.go` if the root cause turns out
  to be production-side — still disjoint from Task 6's files either way).
  Task 6's `agent/` edits are `agent/failure_steering.go`,
  `agent/session_queue.go`, `agent/session_stream.go` — zero overlap with
  Task 5's files. Dispatch after Wave 1 is fully merged.

---

## Task 1: TestIdentifierAudit — closed-world SHA-256 inventory gap

**Files:** `identifier_audit_test.go` (inventory map only — this file is a
test file but the inventory itself is the audit's data, editing it is the
intended way to review a new use), reference `cmd/serf-hub/frontend_hash.go`
(read-only reference, likely no change needed there).

**Reproduce first:**

```
go test -run TestIdentifierAudit -count=1 .
```

Confirmed failing on HEAD with://
```
--- FAIL: TestIdentifierAudit (0.15s)
    identifier_audit_test.go:200: identifier audit found forbidden implementation(s):
        cmd/serf-hub/frontend_hash.go: crypto/sha256 import is not in the closed-world inventory
FAIL
```

**Read the audit's own philosophy first.** `identifier_audit_test.go`'s
header comment on `identifierSHA256Inventory` (around line 202) says: "This
closed-world inventory records every currently reviewed crypto/sha256
package operation. A new package use, selector, method shape, alias, or
package initializer fails until its exact AST fingerprint is reviewed
here." The audit's purpose (see the other `TestIdentifierAudit*` tests in
this file, e.g. `TestIdentifierAuditRejectsProjectPathHashInAllowlistedFile`)
is to catch a NEW, unreviewed derivation of a project/session identifier
from a SHA-256 digest — not to forbid SHA-256 for legitimate non-identifier
uses. Every existing inventory entry carries a one-line comment explaining
why no identifier is derived from that digest (e.g.
`agent/internal/tool/breaker.go`'s `errorClass` entry: "Groups normalized
tool-failure text into a stable class key... No identifier is derived from
it.").

`cmd/serf-hub/frontend_hash.go`'s `frontendDistHash` computes a content hash
of the embedded frontend dist tree (sorted paths + file bytes) purely to
identify which build is embedded in a running binary — it is a build
artifact fingerprint, structurally identical in kind to
`fuzz/promoter/emit_go.go`'s `ShortHash` entry (`"New()": true`) or
`internal/apptranscript/turn_index.go`'s `extendPrefixStamp`
(`"New()": true, "Size": true`) already in the inventory. No project or
session identifier is derived from it.

**Expected fix:** add one inventory entry for
`"cmd/serf-hub/frontend_hash.go"` → function `"frontendDistHash"` →
fingerprint `"New()": true` (matching the `sha256.New()` call shape — the
subsequent `h.Sum(nil)` is a method call on the local `hash.Hash` variable
`h`, not a `sha256.<Selector>` call, so it does not need its own fingerprint
entry — confirm this by reading `checkSHA256Findings`, around line 392, and
comparing against the existing `New()`-shaped entries). Add a one-line
comment above the entry (matching the existing style) explaining it is a
build/frontend-dist content fingerprint with no identifier derived from it.
Keep the map alphabetically/path-ordered consistent with its neighbors
(the entry sorts between `cmd/serf-fuzz-harvest/emit.go` and
`cmd/serf-hub/image_serve.go`).

Do not change `cmd/serf-hub/frontend_hash.go` itself unless, after reading
the checker logic, you find the inventory-entry fix does not actually match
the AST fingerprint the checker computes — in that case explain why in your
report before making an implementation change instead.

**Verify:**

```
go test -run TestIdentifierAudit -count=1 .
```

Must pass. Also spot-check the fixture-driven tests in the same file still
pass (`go test -run TestIdentifierAudit -count=1 .` already covers all
`TestIdentifierAudit*` names via Go's prefix-matching `-run`).

---

## Task 2: FuzzHubcoreScenarios — tree-building "session missing from Live tier"

**Package:** `cmd/serf-hub/internal/hubcore`.

**Reproduce first:**

```
cd cmd/serf-hub/internal/hubcore && go test -run FuzzHubcoreScenarios -count=1 .
```

Confirmed failing on HEAD with several seed failures, e.g.:

```
--- FAIL: FuzzHubcoreScenarios (0.14s)
    --- FAIL: FuzzHubcoreScenarios/seed#22 (0.00s)
        --- FAIL: FuzzHubcoreScenarios/seed#22/active (0.00s)
            tree_live_agreement_test.go:79: status "active": session missing from the Live tier
        --- FAIL: FuzzHubcoreScenarios/seed#22/idle (0.00s)
            tree_live_agreement_test.go:79: status "idle": session missing from the Live tier
        --- FAIL: FuzzHubcoreScenarios/seed#22/awaiting (0.00s)
            tree_live_agreement_test.go:79: status "awaiting": session missing from the Live tier
        --- FAIL: FuzzHubcoreScenarios/seed#22/systemError (0.00s)
            tree_live_agreement_test.go:79: status "systemError": session missing from the Live tier
    --- FAIL: FuzzHubcoreScenarios/seed#23 (0.00s)
        tree_live_agreement_test.go:114: session missing: live=false project=false
    --- FAIL: FuzzHubcoreScenarios/seed#24 (0.00s)
        tree_dormant_test.go:120: session missing: live=false project=false
    --- FAIL: FuzzHubcoreScenarios/seed#25 (0.00s)
        tree_test.go:1529: session missing: live=false project=false
    --- FAIL: FuzzHubcoreScenarios/seed#35 (0.00s)
        --- FAIL: FuzzHubcoreScenarios/seed#35/never_ran (0.00s)
            tree_dormant_test.go:81: session missing: live=false project=false
        --- FAIL: FuzzHubcoreScenarios/seed#35/ran_and_went_idle (0.00s)
            tree_dormant_test.go:81: session missing: live=false project=false
        --- FAIL: FuzzHubcoreScenarios/seed#35/legacy_meta_with_no_accepted-input_count (0.00s)
            tree_dormant_test.go:81: session missing: live=false project=false
        --- FAIL: FuzzHubcoreScenarios/seed#35/asked_but_no_response_yet (0.00s)
            tree_dormant_test.go:81: session missing: live=false project=false
FAIL
```

These are deterministic logic failures against the committed seed corpus,
not flakes — they fail the same way every run. `FuzzHubcoreScenarios`
(`cmd/serf-hub/internal/hubcore/scenarios_fuzz_test.go`) replays a fixed
list of `fuzzScenario*` functions selected by a `uint16` from the seed
corpus; `seed#22`, `#23`, `#24`, `#25`, `#35` are seed-corpus entries, not
test names — the actual failing behavioral scenarios are named by their
`t.Run` subtests and pinned by the file:line the assertion lives at:
`tree_live_agreement_test.go:79` and `:114`, `tree_dormant_test.go:81` and
`:120`, `tree_test.go:1529`.

Start by reading those four assertion sites and the `fuzzScenario*`
functions that drive them (grep each file for `fuzzScenario` to find which
scenario function owns which assertion), then read
`cmd/serf-hub/internal/hubcore/tree.go`'s tree-building logic (`buildTree`,
`buildProjectTree`, and whatever decides a session's "Live tier"
membership) to find why some sessions are dropping out of both the Live
tier and the project tier ("live=false project=false" — the session is
missing from the tree's output entirely, not just misclassified).

This is a real logic bug in hubcore's tree construction — diagnose the
actual mechanism (what distinguishes a session the tree keeps from one it
silently drops) rather than adjusting the test assertions. If, after
diagnosis, you conclude a test scenario itself encodes a wrong expectation
rather than the tree logic being wrong, stop and report that distinction
clearly in your report rather than resolving it unilaterally — this would
be a finding for the task review to weigh, not a call to make alone.

**Verify:**

```
cd cmd/serf-hub/internal/hubcore && go test -run FuzzHubcoreScenarios -count=1 .
```

Must pass with no `FAIL` output, including all named seeds/subtests above.

---

## Task 3: `cmd/serf-test-dev-tooling` wave_test.go:284 — load-induced timing flake

**File:** `cmd/serf-test-dev-tooling/wave_test.go`, test
`TestWaveCompletesDespiteBlockedLeakCheck` (starts line 236).

**Reproduce first:** this test passes in isolation; the flake is
load-induced (fails when the machine is under parallel test-suite load, not
standalone). Reproduce by adding artificial scheduling pressure — e.g. run
it concurrently alongside a CPU-loaded `go test ./...` in the background,
or use `go test -run TestWaveCompletesDespiteBlockedLeakCheck -count=20`
while another heavy build/test is running — and capture at least one actual
failure of the specific assertion at line 284 before touching anything.
Document the reproduction attempt and its outcome (including if it takes
several tries) in your report.

**Read `docs/testing.md`'s "Flakes and Timeouts" section first** — it is
binding here. Two rules: root-cause every sighted flake on the spot, and
never widen or hardcode a timeout to absorb load-dependent work. Preference
hierarchy: (1) await the actual async completion, (2) condition-watching
only where no awaitable completion exists, (3) never fixed sleeps/widened
deadlines.

**Diagnosis to start from:** the test injects `checkLeaksTimeout = 50 *
time.Millisecond` and a leak check that blocks forever on a channel for the
"wedged" suite, then asserts:

```go
if elapsed > 2*time.Second {
    t.Fatalf("wave took %v, should complete within ~2s despite blocked leak check; suggests fix didn't work", elapsed)
}
```

`runWave(...)` at line 277 is a synchronous, blocking call — the test
already "awaits the actual completion" for the substantive behavior (the
call returns instead of hanging forever). The `elapsed > 2*time.Second`
check layered on top is a hardcoded wall-clock ceiling over
inherently load-dependent work (process spawn + goroutine scheduling for
the "wedged" and "normal" suites), which is exactly the anti-pattern
`docs/testing.md` names as forbidden — under heavy parallel `-p`/load, real
elapsed time can legitimately exceed 2s even though nothing hung.

Compare this test's sibling `TestTermIgnoringSuiteIsKilledAfterGrace`
(line 197), which faced the identical "prove it doesn't hang" need and
solved it with **no wall-clock assertion at all** — its own comment: "runWave
only returns once the KILLed suite is reaped; no clock here beyond the
injected grace." It proves boundedness structurally (the test call itself
would hang, and Go's own test binary timeout would catch a genuine hang)
and asserts only the substantive outcome (exit code, log content).

Confirm this diagnosis is right for `TestWaveCompletesDespiteBlockedLeakCheck`
specifically (its purpose per its own comment includes "the wave completes
within a short bound (not hung)" as point 1 of 4 — decide whether that
point is actually served by the `2*time.Second` assertion or whether it was
already redundant with the test's other three assertions plus the test
binary's own hang detection), then apply the same idiom this file already
established: drop the hardcoded wall-clock assertion, keep and verify the
three substantive assertions (`FAIL  wedged` + timeout message, `PASS
normal`, nonzero exit code) which fully prove the fix from kata p8ts still
holds. If your diagnosis differs, explain why before deviating from this
file's own established pattern.

**Verify:**

```
go test -run TestWaveCompletesDespiteBlockedLeakCheck -count=1 ./cmd/serf-test-dev-tooling/
```

Then re-run several times, and once more under artificial load if you can
reproduce the load condition, to show the flake is gone — not just that the
happy path still passes.

---

## Task 4: FuzzHubStartupCoverage — real data race, `os.Process` `Wait`/`Release`

**Package:** `cmd/serf-tui/internal/hubstart`.

**Reproduce first:**

```
cd cmd/serf-tui/internal/hubstart && go test -race -run FuzzHubStartupCoverage -count=1 .
```

Confirmed failing deterministically on HEAD with a genuine data race (not a
test-only artifact):

```
WARNING: DATA RACE
Write at 0x00c0003a0780 by goroutine 112:
  os.(*Process).Release()
      .../os/exec.go:284 +0x2c
  primeradiant.com/serf/cmd/serf-tui/internal/hubstart.init.func1()
      hub_start.go:208 +0x18
  primeradiant.com/serf/cmd/serf-tui/internal/hubstart.StartLocalHub()
      hub_start.go:489 +0xc8c
  ...
Previous read at 0x00c0003a0780 by goroutine 116:
  os.(*Process).pidWait.func1()
      .../os/exec_unix.go:64 +0xcc
  ...
  os/exec.(*Cmd).Wait()
      .../os/exec/exec.go:930 +0x60
  primeradiant.com/serf/cmd/serf-tui/internal/hubstart.StartLocalHub.func2()
      hub_start.go:478 +0x30
```

**Diagnosis to start from — this is a production bug, not test-only.**
`StartLocalHub` in `cmd/serf-tui/internal/hubstart/hub_start.go` (read the
whole function, starts line 440) intentionally spawns the hub as a detached
child. To detect an immediate failed startup, it starts a background
goroutine (line 476-479):

```go
exited := make(chan error, 1)
go func() {
    exited <- cmd.Wait()
}()
select {
case err := <-exited:
    ...
case <-time.After(LocalHubImmediateExitWindow):
}
return releaseHubProcess(cmd.Process)  // line 489, calls process.Release()
```

If the process survives the 750ms window (`LocalHubImmediateExitWindow`,
line 204 — the healthy-startup path), the `select` falls through to
`releaseHubProcess(cmd.Process)` (line 208 defines it as
`process.Release()`) while the background goroutine's `cmd.Wait()` call is
**still blocked**, in flight, on that exact same `*os.Process` — it will
keep running until the hub process itself eventually exits, which for a
healthy detached hub may be arbitrarily far in the future. Calling
`Release()` on a `Process` while a `Wait()` for it is concurrently in
flight is exactly the race the detector reports: both touch the same
internal process-tracking state unsynchronized. This is Go's own
documented hazard (`os.Process.Release` doc: "Release only needs to be
called if Wait is not") applied incorrectly — the code calls both, and
lets them race.

There is an existing test constraint you must preserve: `releaseHubProcess`
is a package-level swappable var (line 208) so tests can stub it —
`cmd/serf-tui/internal/hubstart/hub_start_fuzz_test.go:319-322` stubs it to
return an error and (per the fuzz oracle) expects `StartLocalHub` to
surface that error. Whatever fix you choose must keep that contract intact
(some call to `releaseHubProcess` on the healthy-startup path, whose error
still propagates) while eliminating the concurrent `Wait()`/`Release()`
access on the same `*os.Process`.

Read the whole function and its neighbors for context, then choose the
correct fix — do not just serialize/lock around the race unless you
conclude that is actually the right production fix (a mutex around calls
into the *shared os.Process* would still leave `Wait()` and `Release()`
racing on the OS-level process state internally; locking application-level
access doesn't make `os.Process` itself safe for concurrent `Wait`+`Release`
per the stdlib's own contract). One direction worth evaluating: replace the
unconditional blocking `cmd.Wait()` goroutine with a non-blocking/polled
exit check for the duration of the immediate-exit window (so nothing is
ever blocked inside `Wait()` at the moment `Release()` runs), preserving
both the immediate-exit detection and the `releaseHubProcess` error-
propagation contract. If you find a cleaner correct approach, use it —
explain your reasoning in the report either way.

**Verify:**

```
cd cmd/serf-tui/internal/hubstart && go test -race -run FuzzHubStartupCoverage -count=1 .
```

Must pass with no `WARNING: DATA RACE`. Run it at least 5-10 times
(`-count=10`) since race detection is timing-sensitive and a fix that only
happens to dodge the specific interleaving above is not proof.

---

## Task 5: `agent` package — execRecordingEnv race with probeCapabilities under `-race`

**Files:** `agent/session_slash_command_test.go` (test fixture,
`execRecordingEnv` around line 187-196), reference
`agent/session_capabilities.go` (`probeCapabilities`, lines 307-330;
`runProbeScript`, lines 340-349) and `agent/session_init.go:971` (the one
production call site, inside `initSessionState`, called synchronously —
`probeCapabilities`'s own `wg.Wait()` means it fully joins before
`initSessionState`/`NewSession` returns).

**Reproduce first (already confirmed — reproduces immediately, does not
need heavy load):**

```
go test -race ./agent/ -run 'TestExpandSlashCommand_SerfwideDoesNotExecute' -count=10
```

Fails on HEAD with:

```
WARNING: DATA RACE
Read at 0x00c000520040 by goroutine 37:
  primeradiant.com/serf/agent.(*execRecordingEnv).ExecCommand()
      agent/session_slash_command_test.go:194 +0x70
  primeradiant.com/serf/agent.runProbeScript()
      agent/session_capabilities.go:344 +0xc0
  primeradiant.com/serf/agent.probeCapabilities.func2()
      agent/session_capabilities.go:326 +0xbc

Previous write at 0x00c000520040 by goroutine 36:
  primeradiant.com/serf/agent.(*execRecordingEnv).ExecCommand()
      agent/session_slash_command_test.go:194 +0x84
  ...
  primeradiant.com/serf/agent.probeCapabilities.func1()
      agent/session_capabilities.go:320 +0xb4
```

**Diagnosis (verified — confirm it independently, but this is the
established starting point):** `probeCapabilities`
(`agent/session_capabilities.go:307`) deliberately runs its git probe and
tool probe **concurrently** in two goroutines (`wg.Add(2)`) — its own doc
comment (lines 296-306) states this is intentional: "It is two subprocesses
run CONCURRENTLY under INDEPENDENT deadlines... Independence is the
point." Both goroutines call `env.ExecCommand(...)`. In this test, `env` is
`*execRecordingEnv` (`agent/session_slash_command_test.go:187-196), whose
`ExecCommand` does an unsynchronized `e.calls++` (line 194) before
delegating. Two goroutines incrementing the same plain `int` field with no
synchronization is a textbook data race — and it is *always* present the
moment `probeCapabilities` is called against this fixture (both probe
goroutines call `ExecCommand` unconditionally), which is why it reproduces
without needing heavy parallel-suite load, unlike C/D above.

The production concurrency itself is intentional and (per its own doc
comment) correct — `probeCapabilities` fully joins via `wg.Wait()` before
returning, so no goroutine leaks and no session-visible state is read
before the probes complete. The bug is squarely that `execRecordingEnv`, a
**test fixture** built to wrap a shared `env` that production code
legitimately calls concurrently by design, was never made safe for
concurrent use.

Confirm this reasoning yourself (read both `probeCapabilities` and
`execRecordingEnv` fully) before fixing — the task exists specifically
because this needed a real decision, not just a blind patch. If you reach
the same conclusion, fix `execRecordingEnv.calls` to be safe under
concurrent access (e.g. `atomic.Int64`, or a mutex) while preserving its
existing behavior and every existing caller's read of `.calls` (grep
`execRecordingEnv` and `.calls` across `agent/session_slash_command_test.go`
for all read/write sites — the field is read after the constructor
resets it via `env.calls = 0` around line 214, and compared against 0
later in the test body). If instead you conclude `probeCapabilities` or
`session_capabilities.go` has a genuine production bug, fix that instead
and say why the test-fixture theory above is wrong.

**Verify:**

```
go test -race ./agent/ -run 'TestExpandSlashCommand_SerfwideDoesNotExecute' -count=10
```

Must pass clean, no `WARNING: DATA RACE`, all 10 iterations.

---

## Task 6: TUI chip gaps — tick-driven flip, cross-surface cosmetics, AbortError predicate

Three related but separable pieces of work. All in `cmd/serf-tui` plus a
small `agent/` piece. Do all three; they can be one task with sub-steps and
one commit series (or several commits), reviewed together.

### 6a. Tick-driven "in progress" flip (the real gap; requires new machinery)

**Reference (web, already correct):**
`cmd/serf-hub/frontend/src/panes/session/transcript/flow/LivenessLine.tsx`
line 81 explicitly documents the divergence this task closes ("the
web-only difference from the TUI half of this feature"). The web's
`inProgress` (line 89) is computed reactively from a ticking `now`:

```ts
inProgress: lastFrameAt > retry.receivedAt || now - retry.receivedAt >= retry.delayMs,
```

i.e. the chip flips to "in progress" when EITHER a delta has landed since
the retry was reported, OR the reported backoff delay has elapsed —
whichever happens first. `now` ticks independently of deltas (Session.tsx's
`useNowTick`), so the OR's second branch fires even with zero new deltas.

**TUI's current state (the gap):** `cmd/serf-tui/hub_model.go` holds
`modelRetry *appwire.ThreadModelRetryParams` (line 193) and
`modelRetryInProgress bool` (line 198). `modelRetryInProgress` is set
`true` **only** by an actual delta arriving —
`cmd/serf-tui/hub_notifications.go`'s `markModelRetryInProgress` (around
line 578-592), whose own doc comment claims "Deltas drive a re-render on
their own, so the transition needs no timer." That comment is the bug's
premise: it covers the first half of the web's OR (delta arrived) but there
is no second half (delay elapsed) at all, and — confirmed —
`grep -rn "tea.Tick" cmd/serf-tui/*.go` finds **zero** matches anywhere in
this package; nothing re-renders the TUI on a timer, so once a retry is
reported, a reader who gets no further deltas sees a stale "retrying in
Ns" chip forever after Ns has actually elapsed, even though the daemon may
already be mid-retry.

**What to build:**
1. Track when each `modelRetry` was received — there is currently no
   TUI-side equivalent of the web's `retry.receivedAt` (a client-stamped
   time, not part of the wire `appwire.ThreadModelRetryParams`, see
   `cmd/serf-hub/frontend/src/protocol/model.ts:184` for the web
   analog). Add a field (e.g. `modelRetryReceivedAt time.Time`) to
   `hubModel`, set alongside `m.modelRetry = &params` in
   `hub_notifications.go`'s `NotifySerfThreadModelRetry` case (around line
   128-136).
2. Compute the effective in-progress state as the OR: the existing
   delta-driven `m.modelRetryInProgress` flag, OR
   `time.Since(m.modelRetryReceivedAt) >= time.Duration(m.modelRetry.DelayMS) * time.Millisecond`.
   Use the model's injected clock if `hubModel` already has one for
   testability (grep for how other time-dependent fields in `hubModel` get
   their `now` — match that pattern rather than calling `time.Now()`
   directly if an injected clock exists).
3. Add a `tea.Tick`-driven re-render so the flip is actually visible
   without waiting for an unrelated keypress or notification: when a retry
   is pending and not yet in-progress, schedule a tick (e.g. every second)
   that re-evaluates and, once flipped, stops re-scheduling itself (don't
   tick forever once resolved). Wire it into `hub_model.go`'s `Update`
   (starts line 271) alongside the existing `tea.Msg` cases, following
   this codebase's existing Bubble Tea idioms for scheduling a `tea.Cmd`
   (see how other async work already returns a `tea.Cmd`, e.g.
   `cmd/serf-tui/hub_escalation.go`'s `resolveHeadEscalation` /
   `sendHubEscalationResolve`, for the return-a-`tea.Cmd` pattern this
   codebase uses — this is the first `tea.Tick` in the codebase, so there
   is no existing tick idiom to match beyond the general `tea.Cmd`
   pattern).
4. Update `markModelRetryInProgress`'s doc comment (currently claims "the
   transition needs no timer") to reflect the corrected behavior instead of
   leaving a comment that documents the bug you just fixed.

Update `composerRetryChip`'s call site
(`cmd/serf-tui/composer_panel.go:145`) and/or `composerRetryChip` itself
(`cmd/serf-tui/composer_render.go:326`) only if the effective-in-progress
computation is better centralized there rather than in `hub_model.go` —
use your judgment on placement, but the tick must live in the `Update`
loop since rendering (`View`) must stay a pure function of model state
(see `docs/testing.md`'s note on `hubModel.View()` being pure — do not
break that invariant by computing time-based state inside `View`).

### 6b. Cross-surface cosmetics — align TUI to web's literal formatting

**Reference (web, canonical):**
`cmd/serf-hub/frontend/src/panes/session/transcript/flow/liveness.ts`:
- `formatRetryWait` (line 119-125): model tag is `` ` (${retry.model})` ``
  — parens, placed immediately after the cause, before the attempt count.
- `formatExactGap` (line 59-65): under 60s renders as whole seconds
  (`"45s"`); at/above 60s renders whole minutes plus a trailing `" Ss"`
  only when the remainder is non-zero (`"3m"` or `"3m 5s"`). Used for
  `groupElapsedMs` → `"<formatExactGap> on this call"`.

**TUI's current divergence:** `cmd/serf-tui/composer_render.go`,
`composerRetryChip` (line 326-352):
- Model tag (line 347-349): `chip += " — " + model` — renders as
  `"— model"` (em-dash separator, no parens), appended after the wait,
  not `"(model)"` right after the cause.
- Elapsed (line 350): `fmt.Sprintf(" — %dm on this call",
  retry.GroupElapsedMS/60000)` — integer-divides by 60000 unconditionally,
  so any call under 60s renders `"0m on this call"` instead of e.g.
  `"45s on this call"`.

**Fix:** change `composerRetryChip` to match the web's field order and
formatting exactly:
- Add a Go equivalent of `formatExactGap` (seconds under 60s, minutes +
  optional remainder seconds at/above 60s) and use it for the elapsed
  segment instead of the raw `/60000` division.
- Move the model tag to immediately follow `cause`, rendered as
  `" (" + model + ")"` with no leading em-dash, matching
  `${formatRetryCause(retry.errorClass)}${modelTag} — ${attempt} — ${wait} — ${elapsed}`
  from `formatRetryWait`.
- Keep the existing `AttemptCap`-vs-`MaxAttempts` fallback logic in
  `composerRetryChip` unchanged (that part isn't part of this divergence).
- Update `composerRetryChip`'s doc comment where it currently describes
  the old field order/formatting.

Check `cmd/serf-tui/composer_render_test.go`,
`cmd/serf-tui/composer_retry_wiring_test.go`, and
`cmd/serf-tui/hub_model_retry_test.go` for existing assertions on the old
`"— model"` / `"Xm on this call"` format — those are testing the bug and
must be updated to assert the corrected format (a plan-mandated behavior
change to a *test* file, not a case to work around).

### 6c. `agent/` — one shared AbortError-through-cancellation predicate

Three production call sites independently reimplement "does this error
represent this turn's cancellation" via `errors.As(err, &abort)` against
`*llm.AbortError`, each slightly differently:
- `agent/failure_steering.go:130`, `roundWasCancelled`:
  `errors.Is(err, context.Canceled) || errors.As(err, &abort)`
- `agent/session_stream.go:206`, `isTurnCancellation`: checks `ctx.Err()
  != nil` first, then `errors.As(err, &abort)`, then excludes any other
  `llm.Error`, then falls back to `errors.Is(err, context.Canceled) ||
  errors.Is(err, context.DeadlineExceeded)`.
- `agent/session_queue.go:670`, inside `queuedInputDrainContext`: computes
  `isAbort := errors.As(err, &abort)`, then combines it with a
  same-context check (`errors.Is(ctx.Err(), context.Canceled)`) per the
  detailed comment above it (lines 673-677) about the "honest-Unwrap"
  asymmetry between a bare `context.Canceled` and a wrapped `*AbortError`.

Extract the common `var abort *llm.AbortError; errors.As(err, &abort)`
idiom (the actual copy-pasted fragment) into one shared predicate — read
all three call sites and their tests first
(`agent/session_dod_definition_test.go` has direct coverage of
`queuedInputDrainContext`'s AbortError handling around lines 1282-1401;
`agent/session_model_test.go:870` and
`agent/session_model_round_contract_fuzz_test.go:157` cover the others) to
find the right shape for the shared helper — it must not collapse the
real behavioral differences between the three call sites (each does
something different with the result beyond just detecting an AbortError:
`roundWasCancelled` ORs with a bare-Canceled check, `isTurnCancellation`
has an additional `llm.Error` exclusion, `queuedInputDrainContext` has the
documented same-context discrimination). The predicate to extract is
narrowly "was this error an `*llm.AbortError` (and if useful to the
callers, what does it unwrap to)" — not a merge of the three functions'
full logic into one. Place it wherever fits this codebase's existing
convention for small shared error-classification helpers in `agent/`
(look at how `llm.Kind(err)` or similar existing helpers are organized —
grep `func.*error) bool` near `AbortError`/`Kind` in `llm/` and `agent/`
for the established pattern) — a package-level function in `agent/` near
one of the three call sites, or in a small dedicated file, is reasonable;
use your judgment.

**Verify (all of 6a/6b/6c):**

```
go test -run 'TestComposerRetryChip|TestComposerRetry|TestHubModelRetry|TestRoundWasCancelled|TestQueuedInputDrain|TestIsTurnCancellation' -count=1 ./cmd/serf-tui/... ./agent/...
```

(Adjust the `-run` pattern to the actual test names you find — this is a
starting point, not a guarantee every relevant test name matches this
regex. Make sure every test file you touched or whose behavior you changed
is included in your final narrow verification run.) Also manually confirm
(read, don't just assume) that no other production call site reimplements
the same AbortError-detection fragment outside the three named above —
grep `AbortError` across `agent/*.go` (excluding `_test.go`) one more time
after your edit to confirm exactly the intended sites changed.
