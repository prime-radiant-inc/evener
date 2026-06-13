# Recursion campaign — execution brief

**Status:** Ready to dispatch the moment the live matrix (task #5) is green on
the post-max_wait surface. · **Date:** 2026-06-13 · **Pattern:** the
2026-06-08-job-control-EXECUTION.md cadence (fresh implementer per task,
sequential, reviewed, committed per green task).

## Why sequential, not parallel worktrees

Unlike max_wait (five disjoint schemas + docs + cards), the recursion plan's
18 tasks repeatedly touch the SAME files (`agent/subagents.go`,
`agent/session_init.go`, `agent/job_delegate.go`, `agent/session_lifecycle.go`,
`agent/session_tools.go`) with hard semantic dependencies (seams must flip in
order; drive-down builds on the seam flips). Parallel worktrees would merge-
conflict constantly. One tree, one writer at a time, opus coordinating.

## Dispatch shape

One opus orchestrator. Per plan task, in order:

1. Spawn a **sonnet implementer** with: the task's plan section verbatim
   (docs/superpowers/plans/2026-06-13-recursive-subagents.md), the spec
   sections it implements, the relevant resolved decision (#1-6 in the plan
   head), and the house rules block below. It works in the MAIN tree.
2. Implementer reports: red-test evidence (the failing run output, verbatim),
   green evidence, lint output, commit hash.
3. Spawn a **sonnet spec-reviewer** (separate, read-only): does the diff
   implement exactly the cited spec sections, nothing more or less? Does the
   contract amendment ride the commit (for tasks the plan marks)? Bounce on
   any deviation.
4. Orchestrator grep-verifies the implementer's claims against the tree
   (commit-message-lies guard) before advancing.
5. Phase boundaries: orchestrator runs all three gates itself
   (`PATH="$HOME/go/bin:$PATH" make test && make lint &&
   go test ./agent/... -race`) before opening the next phase.

## House rules block (verbatim into every implementer prompt)

- TDD red-first; run the failing test and capture its output BEFORE
  implementing. No fake-green: never weaken/delete an assertion; BLOCKED →
  report, don't improvise.
- Task 3/14 mechanism is MANDATORY (plan head decision #4): unskipped red run
  captured → land with tracked `t.Skip` → Task 14 unskips, re-shows red,
  implements.
- Spec §5 rule: any sweep hit or semantics question the plan/spec doesn't
  answer → STOP and surface; never decide.
- Contract amendments ride the code commit (the plan names which task carries
  which §8 clause).
- `~/go/bin/golangci-lint run` on changed packages per task; commit per green
  task; never `git add -A` blind; never push.
- NEVER `pkill -f`/`pgrep -f` patterns that can self-match.
- Production stays DARK until Task 9 (allowance set only via spawnConfig in
  tests before then) — preserve that property; it is load-bearing for
  rollout §10.

## Progress journal

The orchestrator maintains `docs/superpowers/plans/2026-06-13-recursive-subagents.md`
checkboxes (mark per task, commit the tick with the task) AND appends one
line per task to `/tmp/recursion-run.md` (host-local observability ledger:
task, commit, red-evidence pointer, reviewer verdict, gate status). Silence
is never success.

## Phase 5 split

Task 17 (contract residue + architecture.md + rollout disclosure) runs under
the orchestrator like any task. Task 18 authors the coordinator-pattern cards
but the LIVE RUN is the orchestrating session's (needs hub + OAuth; use the
task #5 runner recipe and rules verbatim — gpt-5.5, verbatim card text, no
pgrep -f). The campaign is not done until those cards pass live.

## Done means (from PRI-2204 + spec)

§9 matrix green · §8 amendments landed with their code · coordinator cards
pass live · §10 dark rollout (double opt-in; counter's single-level bind
disclosed) · all gates green · roborev sweep (task #8) · unpushed.
