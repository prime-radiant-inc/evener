# Abort-error unit coverage + tmux e2e harness isolation

Branch: `wip/parked-minors` (= `main` at `f37f3b256`).
Repo: `/Users/jesse/git/prime-radiant-inc/serf`.

Two independent tasks, dispatched as one parallel wave. Task 1 is a small,
fully-specified test addition. Task 2 is a real investigation into
load-sensitive tmux e2e flakiness, landing in a harness fix.

## Global Constraints

- **Model policy (Jesse-directed):** lightest possible model per task. Haiku
  for Task 1. Sonnet for Task 2 and for all task reviews.
- **TDD / evidence discipline:** for Task 1, write the failing/absent test
  first conceptually (there is no existing test file), then implement. For
  Task 2, reproduce (or seriously attempt to reproduce) before fixing, and
  record the attempt's outcome honestly — including if it doesn't reproduce.
- **Root cause only.** No `t.Skip`, no widened timeouts, no sleeps-as-fix.
  Follow `docs/testing.md`'s "Flakes and Timeouts" section: await the actual
  completion; condition-poll only where no awaitable completion exists;
  never widen/hardcode a timeout to absorb load-dependent work.
- **Scope discipline:** workers verify with a narrow `-run` scope only — do
  not run the full suite or other packages' tests. The controller runs full
  verification (`go test ./... -count=1`, plus targeted `-race`) after both
  tasks merge.
- **Do not touch:** `.kata.toml`, `serf-transcript-v2-upgrade`. These are
  pre-existing, unrelated, already either modified or untracked in the
  working tree — leave them exactly as found.
- **Git hygiene:** never `git add -A`. Stage only the files the task
  touches. Commit messages explain the root cause and the fix, not "fix
  test."
- **Style:** match the surrounding file's existing style and comment
  density exactly (this codebase's doc comments are unusually thorough —
  match that bar, don't strip it down).

## Wave plan

Single parallel wave, worktree-isolated: Task 1 touches only a new file
`agent/abort_error_test.go`. Task 2 touches only
`cmd/evener-tui/tmux_e2e_test.go`. Fully disjoint packages and files.

---

## Task 1: Dedicated unit test for `isAbortError`

**File (new):** `agent/abort_error_test.go` (package `agent`).

**Context.** `agent/abort_error.go` defines:

```go
func isAbortError(err error) bool {
	var abort *llm.AbortError
	return errors.As(err, &abort)
}
```

It was extracted (see `git log --oneline -- agent/abort_error.go`, commit
`73a2d6d96`) from three call sites that each reimplemented the same
`errors.As(err, &abort)` fragment:
`agent/failure_steering.go:130` (`roundWasCancelled`),
`agent/session_stream.go:206` (`isTurnCancellation`), and
`agent/session_queue.go:670` (inside `queuedInputDrainContext`). Those three
call sites have indirect test coverage through their own tests, but
`isAbortError` itself has **no dedicated test file** — confirmed by `find
agent -iname '*abort*'` returning only `abort_error.go`, and `grep -rn
isAbortError agent/*.go` showing only the three call sites plus the
definition, no `_test.go` hits.

**Types you need** (both already exist in `llm/`, do not modify them):

```go
// llm/sdk_errors.go
type AbortError struct{ nonHTTPBaseError }
func NewAbortError(message string, cause error) error {
	return &AbortError{nonHTTPBaseError{message: message, retryable: false, cause: cause}}
}

// llm/provider_unhealthy.go
type ProviderUnhealthyError struct {
	Shape    string // "stall" | "cap"
	Attempts int
	Elapsed  time.Duration
	LastErr  error
}
func (e *ProviderUnhealthyError) Unwrap() error { return e.LastErr }
```

`ProviderUnhealthyError.Unwrap` returns `LastErr`, so wrapping an
`*llm.AbortError` there makes `errors.As` (and therefore `isAbortError`)
find it through the chain — this is the "wrapped in a different error type"
case, distinct from `fmt.Errorf("...: %w", ...)` wrapping.

**Write a table-driven test** `TestIsAbortError` covering exactly these
cases (add more only if you find another meaningfully distinct shape while
reading `llm/sdk_errors.go` and `llm/provider_unhealthy.go`; do not remove
any of these):

1. **Direct `*llm.AbortError`** — `llm.NewAbortError("aborted", nil)` →
   want `true`.
2. **`fmt.Errorf`-wrapped `*llm.AbortError`** —
   `fmt.Errorf("during turn: %w", llm.NewAbortError("aborted", nil))` →
   want `true`.
3. **`*llm.ProviderUnhealthyError` wrapping an abort** —
   `&llm.ProviderUnhealthyError{Shape: "stall", Attempts: 3, Elapsed: time.Second, LastErr: llm.NewAbortError("aborted", nil)}`
   → want `true` (proves `isAbortError` walks through `Unwrap`, not just a
   direct type assertion).
4. **Plain unrelated error** — `errors.New("boom")` → want `false`.
5. **`nil` error** — want `false`.

Standard Go table-driven shape: `[]struct{ name string; err error; want bool }`,
one `t.Run(tt.name, ...)` per case, asserting `isAbortError(tt.err) ==
tt.want`. This is a pure, deterministic function with no I/O — no fixtures,
no mocks needed.

**Verify:**

```
go test -run TestIsAbortError -count=1 ./agent/
```

Must pass, all 5 (or more) subtests green. This is a test-only commit —
touch nothing outside `agent/abort_error_test.go`.

---

## Task 2: tmux e2e flakiness under heavy concurrent load

**File:** `cmd/evener-tui/tmux_e2e_test.go` (package `main`, `cmd/evener-tui`).

**Reported symptoms (two independent sightings, both under heavy concurrent
load — e.g. this file's suite running alongside other CPU-hungry test
packages, not standalone):**

1. `TestTUITmuxE2E_FailedForkPreservesDraft` segfaulted once; not
   reproducible when run in isolation.
2. A tmux e2e test timed out on an earlier occasion (no further detail
   available in-repo — this file and `docs/testing.md`/`docs/agentic-testing.md`
   contain no other reference to either incident; treat both as
   load-triggered, not as bugs with a known repro script).

**Read the whole file first** (2357 lines) — it already has real hardening
in place, and the task is to find what's still missing, not to re-litigate
what's already solved:

- `uniqueTmuxSessionName()` (line 52) already makes every tmux **session
  name** unique (nanosecond timestamp + an `atomic.Int64` counter), so
  session-name collision is already ruled out.
- `tmuxSessionSlots` (line 63, buffered channel cap 6) already bounds how
  many **live** TUI sessions run concurrently, with a documented rationale
  about render starvation under too much parallelism.
- `WaitFor`/`WaitForWithout`/`WaitUntil`/`WaitForHistory` already poll for
  real readiness conditions (text appearing, pane size matching, a
  structural predicate) rather than sleeping a fixed duration — this is not
  a "missing readiness wait" bug in the general case.
- `CaptureStable` and the "A Single `tmux capture-pane` Can Lie" section of
  `docs/testing.md` (read it) already root-caused and fixed a *different*,
  previously-diagnosed load-sensitive issue (torn-frame captures from
  bubbletea's unsynchronized ANSI writes) — do not re-diagnose that one;
  it's a good model for the rigor expected here, not the same bug.

**The lead to verify:** every tmux invocation in this file — `runTmux`
(line 1459, used by most call sites) and the several call sites that build
`exec.Command("tmux", ...)` directly instead of through `runTmux`
(`sendFirstCtrlCAndAssertNoQuitWarning`'s two `pipe-pane` calls around lines
990-999, `PaneDeadStatus` line 1420, `WaitForExit`'s `has-session` line
1410, `Capture`/`CaptureHistory` lines 1197/1206, `Close` line 1175,
`waitForTmuxPaneSize` line 1161) — connects to tmux with **no `-L`
(socket name) or `-S` (socket path) flag anywhere in this file** (confirm:
`grep -n '"-L"\|"-S"' cmd/evener-tui/tmux_e2e_test.go`). Every one of these
processes therefore talks to the **same single default tmux server**
(the one at tmux's default socket, `$TMUX_TMPDIR` or
`/tmp/tmux-$UID/default`) — unique session names give each test its own
session, but all those sessions live on one shared server process, and
every `capture-pane`/`send-keys`/`new-session`/`kill-session`/etc. across
**every parallel test in this file** is a distinct short-lived client
process connecting to that one server's socket. tmux's server is a single
event-driven process multiplexing all connected clients; with up to 6 live
sessions each polling `capture-pane` every `tuiE2EPollInterval` (10ms), plus
whatever other packages' tests are consuming CPU concurrently when the
*full* suite runs, this is a lot of concurrent connect/read/exit churn
against one shared, stateful process — exactly the kind of load this
file's own tests are the heaviest consumer of.

**Confirm this independently before implementing anything** (don't just
take this brief's word for it):

- Confirm the `-L`/`-S` grep result yourself.
- Get direct evidence the server is shared: run two of this file's e2e
  tests concurrently (e.g. `go test -run
  'TestTUITmuxE2E_DashboardProjectAndSpawn|TestTUITmuxE2E_BrowseAndFork'
  -count=1 ./cmd/evener-tui/` while they're both mid-run) and, from another
  shell, run plain `tmux ls` (no `-L`) — if it lists sessions from both
  tests at once on the one default server, that confirms the shared-server
  hypothesis directly.
- Search for any other repo artifact that might narrow down the actual
  segfault (it is not documented in `docs/testing.md`,
  `docs/agentic-testing.md`, or anywhere else in the tracked repo, per an
  earlier grep) — check `git log --all --oneline -- cmd/evener-tui/tmux_e2e_test.go`
  and any `.superpowers/sdd/**` reports touching this file for prior
  mentions, in case there's more context than this brief has. If you find
  something that changes the diagnosis, follow the evidence, not this
  brief.
- Consider and rule out (or in) alternative explanations before settling:
  resource limits (`ulimit -n`/process limits — checked locally: `ulimit -n`
  is 1048576 and `kern.maxprocperuid` is 10666, both generous, so exhaustion
  is unlikely on this machine, but say so explicitly rather than assuming
  it generalizes to CI or every dev machine), and whether the segfault
  could be in the `serf-tui` binary itself (visible via
  `#{pane_dead_status}`, which is the **exit status of the process that ran
  in the pane** — i.e. `serf-tui`, not tmux or the test binary) rather than
  in the tmux server. If the crash is in `serf-tui` itself, the shared-tmux-
  server fix below is still worth doing as isolation hardening, but say
  explicitly whether you believe it addresses the actual crash mechanism or
  only removes a contributing-load factor.

**The fix, if the shared-server diagnosis holds (or even if only partially
confirmed — this is real isolation hardening either way):** give every
test's tmux session its own dedicated tmux **server**, not just its own
session name, by threading a unique `-L <socket>` (global tmux flag, must
precede the subcommand) through every tmux invocation in this file. The
simplest shape: reuse the same string `uniqueTmuxSessionName()` already
produces for both the session name and the socket name (they're different
tmux namespaces — a session name and a socket name can safely be identical
strings) — one test, one dedicated server process, holding exactly one
session. This requires:

- Storing the socket name on `tmuxTUI` (alongside the existing `session`
  field).
- Generating it once in `startTUITmuxSized` and `startTUITmuxAltScreen`
  (both currently do `session := uniqueTmuxSessionName()` — same idea, one
  value serves both), and threading it into `pinTmuxWindowSize`/
  `waitForTmuxPaneSize` (currently take only `session string`).
- Updating `runTmux` (and every call site) to prepend `"-L", socket` before
  the subcommand args — decide the cleanest signature (e.g. an explicit
  `socket` parameter, or a small wrapper method on `*tmuxTUI` that the
  struct's own methods use, with a package-level `runTmux` kept only for
  the pre-construction calls in `startTUITmuxSized`/`startTUITmuxAltScreen`
  before the `tmuxTUI` value exists). Use your judgment on the exact shape;
  keep it consistent with this file's existing style.
- Updating every direct `exec.Command("tmux", ...)` call site listed above
  to include `"-L", <socket>` as the first two args after `"tmux"`.

Do **not** touch `tmuxSessionSlots`, `uniqueTmuxSessionName`, or any of the
`WaitFor*`/`CaptureStable` polling logic unless your independent
investigation finds a real defect in them — they are not implicated by the
shared-server diagnosis.

**If, after real investigation, you cannot get any evidence for the
shared-server (or any other) diagnosis, and the segfault stays genuinely
irreproducible:** say so honestly in your report — do not fabricate a
reproduction. In that case still land the `-L` socket-isolation change
described above as defensive harness hardening (removing a real,
independently-confirmed shared-mutable-resource risk is worth doing on its
own merits even without a captured crash), and be explicit in your report
that this is hardening-without-confirmed-root-cause, not a proven fix.

**Verify:**

```
go test -run 'TestTUITmuxE2E_FailedForkPreservesDraft' -count=5 ./cmd/evener-tui/
```

Then the full file, once, to confirm no regression:

```
go test -run TestTUITmuxE2E ./cmd/evener-tui/ -count=1
```

Then, if you can arrange artificial concurrent load (e.g. a background `go
build ./...` or another `go test ./...` running concurrently while you
repeat the narrow run above several times, or `-count=10` on the full tmux
e2e set), do so and record the outcome — this is the condition the original
symptoms were reported under. Document whatever you observe, pass or fail,
honestly.

---

## Final verification (controller, not workers)

After both tasks are reviewed clean and merged:

```
go test ./... -count=1
```

Expect 64/64 packages passing (baseline per dispatch instructions). Then
targeted `-race` on the touched packages:

```
go test -race ./agent/... -run TestIsAbortError -count=1
go test -race -run TestTUITmuxE2E ./cmd/evener-tui/ -count=1
```
