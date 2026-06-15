# Job output as a navigable, context-managed resource

**Goal:** Stop auto-injecting large tool output. Default to a *small, legible* window plus a handle the agent navigates — the way Claude Code's own `Read`/`Grep`/`Bash`/`Task` tools protect the context budget. The agent decides what to pull in; the tool's job is to make that cheap and unmistakable.

**Source:** `/tmp/subagent-tools-test-report.md` §2.2, §8.2 — a test agent saw `truncated=true`, assumed permanent data loss, and reasoned itself into a phantom "grep is broken" finding.

## Root cause

`truncated` is a single opaque boolean that conflates two unrelated situations:

- **windowed** — the full log is retained on disk (≤ 8 MiB, `maxJobOutputRetentionBytes`) and is fully reachable via `job_read_output` head/tail/grep; you just received a slice.
- **evicted** — bytes past the 8 MiB cap were permanently dropped (`OutputStore.retainedStart > 0`).

The disambiguating metadata already exists in the store (`total`, `retainedStart`, persisted in the meta sidecar, `jobstore/output.go:45-46`) but is surfaced inconsistently: `job_read_output` exposes `total_bytes` but not `retained_start`; **shell exposes neither — just the boolean.** And the inline budget is 64 KB, so even a trivial ~100 KB output trips `truncated`. The agent can't tell recoverable from unrecoverable, so it reasons defensively and wrongly.

`grep` already scans the **full retained window from the start** (`jobs.go:686-710`, match-capped at 100) — it is **not** broken. The report's §2.2 is a misdiagnosis caused entirely by the opaque flag.

## Design

Philosophy: **small bounded default + legible window metadata + file-like navigation (agent-driven); auto-summary is opt-in/last-resort, never the default.**

**Decisions**

1. **Default peek tail = 1 KiB** (was 64 KiB). Tunable constant; the architectural win is independent of the exact number, so we ship Jesse's bet and **E2E feel-test it** (per the launch-watch lesson — live testing caught what unit tests missed).
2. **Split the dual-use constant.** `shellInlineOutputBytes` is today both the ride-whole/ephemeral threshold *and* the snapshot tail size. Split into:
   - `shellRideWholeBytes` — completed output at or below this rides back **whole + ephemeral** (no durable job, no clutter). Recommend lowering from 64 KiB; start at **8 KiB** and E2E-tune. Keeps `ls`/`git status`/small `diff` clutter-free without auto-injecting 64 KB.
   - `shellDefaultTailBytes = 1024` — the peek tail shown alongside the handle when output exceeds the ride-whole threshold, and for running/backgrounded snapshots.
3. **Window metadata on every output-bearing result** (shell *and* `job_read_output`): `total_bytes` (lifetime), `dropped_bytes` (= `retainedStart`; permanently evicted), and the shown byte-range. `truncated` becomes a derived convenience, not the only signal.
4. **`output_status` enum** — `complete` | `windowed` | `evicted` — plus, when not `complete`, a one-line navigation hint carrying the literal recovery call (e.g. `job_read_output(job_id, head_bytes=…)` / `grep=…`). Targets the *behavioral* failure: even a careless agent is told "this is a slice; here's how to get the rest."
5. **Navigation contract.** The small window always carries the handle (`job_id`) + metadata + hint. Tool descriptions reframe output as a navigable resource over the full retained log; correct `grep`'s description ("scans the full retained output, up to 8 MiB" — not "what you saw inline"). No new resource abstraction — serf already has the file + head/tail/grep primitives; this is contract + steering, not capacity.
6. **Digest mode: opt-in, deferred.** A `job_read_output` digest mode (line/byte counts + head+tail + error/warn lines) is a *mode the agent requests*, not an auto-injection. Out of scope for v1; revisit only if the small-window + navigation contract proves insufficient in live use.

## Part A — output context-management (TDD each task)

- **A1.** Split constants: introduce `shellRideWholeBytes` (8 KiB) and `shellDefaultTailBytes` (1 KiB); repoint `job_shell.go:198` (snapshot tail) → `shellDefaultTailBytes`, `marshalCompleteOrHandleResult:246` (ride-whole) → `shellRideWholeBytes`, and the keep-case tail → `shellDefaultTailBytes` (≤1 KiB peek, not `maxChars`). Tests: a 4 KiB completed command rides whole + ephemeral (no `job_id`); a ~9 KiB command becomes a handle (`job_id`, `truncated`) with a ≤1 KiB tail; `job_read_output` returns the full retained bytes.
- **A2.** Add `total_bytes` + `dropped_bytes` + shown-range to the shell wire result (`shellToolResult`). `dropped_bytes` from `OutputStore.retainedStart`; `total_bytes` from `OutputStore.total`. Test: completed 2 KiB output → `total_bytes=2048, dropped_bytes=0, output_status=windowed`.
- **A3.** Add `dropped_bytes` (= `retained_start`) to the `job_read_output` result (it already has `total_bytes`). Test: a job whose output exceeded 8 MiB reports `dropped_bytes>0, output_status=evicted`.
- **A4.** Add the `output_status` enum + navigation hint to both results, derived from `(dropped_bytes, total_bytes, shown_bytes)`. Tests for all three states.
- **A5.** Default `job_read_output` read window (when neither `head_bytes` nor `tail_bytes` is given) ≤ `shellDefaultTailBytes`. Verify current default first; set to 1 KiB tail if larger.
- **A6.** Tool-description rewrites: `shell`, `job_read_output` (navigable-resource framing + per-state hints), and the `grep` corpus clarification.
- **A7.** E2E scenario card: real `serf` session (gpt-5.5 OAuth), commands at <1 KiB, ~5 KiB, >8 KiB, and a >8 MiB generator; assert the agent reads the tail, recognizes `windowed`, pulls the head via `job_read_output`, and distinguishes `evicted`. Feel-test whether 1 KiB / 8 KiB are too aggressive (round-trip annoyance) and tune.

## Part B — smaller subagent-tools-test-report fixes (same PR)

- **B1.** Default subagent depth → 2: `session_config.go:262` (`MaxSubagentDepth` normalize `<=0` → `2`); fix the now-stale comment at `job_delegate.go:142`. Update any test asserting default 1. (Flag: enables depth-2 trees by default → more fan-out/cost.)
- **B2.** Elide `delegation_allowance` from the `delegate` schema when `s.delegationAllowance == 1` (only legal grant is 0 → param is a no-op); build the per-session variant in `rebuildToolDefsCache` (`session_tools.go:584`). Plus a clearer out-of-range error for allowance ≥ 2.
- **B3.** `job_send_message` description + `job-control.md`: a send to a running delegate live-steers only while it is *mid-turn* (`action:"sent"`, same `job_id`); idle/finished → resume (`action:"resumed"`, new `job_id`).
- **B4.** `job-control.md` clarifications: `include_descendants`/`include_nested` widen *scope* (add rows), not format; one-watch-per-`(target, send_to)` replacement; send-to-completed = resume.

## Part C — solve the `max_wait_ms=0` overload (not just document it)

**Root reason it's inconsistent:** shell's common case is foreground, every job tool's common case is background/snapshot — *opposite* defaults. Under strict-schema auto-fill the auto-filled `0` must map to each tool's common case, so the same param name silently flips meaning (shell `0`=wait-default; job tools `0`=don't-wait). No description or rename fixes a silent same-name flip; the fix is to stop overloading the name.

- **C1.** **shell drops `max_wait_ms`, gains `background: bool`.** Default `false` → run foreground, wait up to the ~120 s promotion cap, auto-promote to a durable background job if it exceeds (current behavior, minus the explicit custom foreground budget — YAGNI). `true` → background immediately, return `job_id`. Auto-filled `false` = foreground = shell's correct default; backgrounding is an explicit opt-in, no magic-zero. Update `definitions.go` schema + description, `parseShellArgs` (`session_tools_shell.go`), and the shell promotion path. `max_runtime_ms` (process-runtime cap) is unaffected.
- **C2.** **`max_wait_ms` keeps one meaning** on delegate / job_read_output / job_send_message / job_stop: `0` = don't wait / return now; `>0` = wait up to N. Already consistent — only normalize the description wording across the four.
- **C3.** Update scenario docs + `job-control.md` for the shell `background` arg; grep tests/hub for any `max_wait_ms` shell callers.

The shell↔delegate asymmetry (bool vs. `max_wait_ms=0`) is intentional: it mirrors their opposite defaults and removes the silent flip that fooled the test agent.

## Decision log

- **Ride-whole = 8 KiB, peek tail = 1 KiB.** 1 KiB ride-whole was considered (Jesse's gut) but rejected: the 1–8 KiB bucket is "commands you ran to read the output" (focused `git diff`, `git status`, short test run); a 1 KiB threshold forces each into a mandatory second read + job-list clutter. 8 KiB rides those whole, still far under `Read`'s 2000-line default. Both tunable; E2E feel-tested.
- **`max_wait_ms` overload solved structurally** (shell `background: bool`), not documented around — per Jesse: it's a serious issue, not a wording gap.

## Out of scope (report asks we are declining)

- Unify `max_wait_ms` semantics across tools (unsafe: would background every strict-schema shell call).
- Make `grep` search beyond the retained window (it already scans the full ≤8 MiB window).
- Nested/hierarchical `job_list` JSON (filters are scope, not format; a `depth` field is the lighter answer if needed).
- Auto-widening the inline window (the original wrong instinct — the opposite of context-management).
