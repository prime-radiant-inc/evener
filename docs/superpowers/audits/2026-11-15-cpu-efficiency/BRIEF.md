# CPU efficiency audit: shared brief

Audited tree: worktree `cpu-efficiency-audit`, branch off `origin/main` at
`377ac0867f580bc65981dee425bf83b3f89af72e` (2026-11-14).
Go 1.27.4, darwin/arm64, 18 cores. Go workspace modules: root, `agent`, `llm`,
`auth`, `envvars`, `execsupport`, `fuzz`, `identifier`, `invariant`.

## Goal

Find where evener burns CPU unnecessarily, and design fixes. This is a runtime
CPU-efficiency audit, not a code-simplification audit. Nothing gets implemented.

What counts as a finding:

- Per-turn (per model round) work that scales with history, delegate count,
  tool count, or session count, and did not have to.
- Idle CPU: timers, polling loops, tickers, wakeups, fsnotify storms, relay
  fanout that wakes with nothing to send, background sweeps.
- Redundant re-derivation: rebuilding prompts, tool definitions, indexes,
  projections, or summaries that a cache could hold; re-parsing the same bytes;
  repeated JSON round-trips of large payloads.
- Allocation and copy churn large enough to matter: whole-transcript copies,
  string concatenation in loops, `fmt.Sprintf` on hot logging paths, regexp
  compile inside loops, `map`/`slice` growth without sizing, `json.Marshal` of
  values that are then re-parsed.
- Algorithmic blowups: O(n²) scans over turns, sessions, delegates, or events;
  per-item sorts that could be one sort; linear scans per event over an
  unbounded list.
- Process/syscall overhead: extra `git` or shell spawns per tool call, `stat`
  storms, fsync per small write, per-request directory walks.
- Frontend CPU: work per streamed event, per clock tick, per transcript page;
  re-renders that re-derive whole lists; layout thrash; timers that run while
  hidden.
- Lock contention and goroutine churn on the hot path.

Not interesting on its own: startup cost that happens once per process, test
code, fuzz harnesses, one-shot CLI commands, and micro-optimizations under a
few microseconds that cannot compound.

## Evidence standard

Label every finding:

- **executed**: you ran a command and quote its output (benchmark, pprof, `time`,
  a Go test that counts calls, `/usr/bin/time -l`, an instrumented run).
- **read**: you read the code and traced the call path; no run.
- **suspected**: plausible from shape alone; say what would confirm it.

Cite `path:line` at `377ac0867f`. Give the call path from a real entry point
(model round, AppWire request, hub event, browser event) down to the expensive
line, naming each function. A finding with no reachable call path is dead code,
not CPU.

For each finding, state the **scale**: how often it runs (per token delta, per
round, per tool call, per session per second, per browser event) and how the
cost grows (constant, linear in history, quadratic in delegates). A 40 µs cost
per streamed delta at 500 deltas per turn is 20 ms per turn and matters; 40 µs
once at process start does not.

## Deliverable

Write one file: `docs/superpowers/audits/2026-11-15-cpu-efficiency/<lane>.md`.

Structure:

1. **Verdict**: three sentences: the biggest CPU waste in this lane, the
   second, and what you checked that turned out clean.
2. **Findings, ranked** by (CPU saved × frequency) ÷ fix risk. Each finding:
   - Title with severity: **Critical / High / Medium / Low**.
   - `path:line` and the call path from the entry point.
   - Mechanism: what the code does and why it costs CPU.
   - Evidence: label plus the command and output, or the code trace.
   - Scale: measured or estimated cost per unit and how it grows.
   - **Fix design**: the change, the API/function sketch, and why it is safe.
     Name what could break and what would need a test.
   - **How to verify the win**: the exact benchmark or command that should
     show the before/after delta.
3. **Clean**: what you checked and found already efficient, with the evidence,
   so nobody re-audits it.
4. **Open questions**: anything that needs Jesse's ruling (a behavior change,
   a cache with a staleness window, a tradeoff between CPU and memory).

Design fixes for the real codebase: name existing types and functions you would
reuse, and prefer a small change in the owning package over a new subsystem.

## Rules

- Read-only on source. The only file you write is your lane report. No git
  commits, no `git add`, no branch changes.
- Do not run `make lint`, `make vet`, `make test`, or the full suite.
- Do not run CPU-burner jobs or parallel heavy benchmarks; the host is shared
  with other auditors. One benchmark at a time, `-benchtime` small.
- Use your own `$EVENER_SCRATCH_DIR` for build output and profiles, and delete
  what you no longer need.
- If you build: `go build -o "$EVENER_SCRATCH_DIR/evener" ./cmd/evener` from the
  worktree root takes ~2–3 minutes cold.
- Report back with: top five findings as one-liners, the report path, and what
  you could not measure.
