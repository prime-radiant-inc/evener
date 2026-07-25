# The agent suite's serial prefix — what it costs and why most of it is stuck

Measured on an idle 10-core M-series box, `agent` package, gate flags
(`-short -count=1 -parallel 32 -p 4`). Numbers from `go test -json`, so the
work totals are parallelism-independent.

## The mechanism

Go runs every non-`t.Parallel()` test to completion **before releasing any
parallel test**. So a serial test's cost is paid one-at-a-time on a single core
while the rest of the suite waits.

| | |
|---|---|
| serial tests | 410, **14.4s** of work |
| parallel tests | 2364, **583s** of work |
| first parallel test starts at | **t=18.0s** |
| total wall | ~39.6s |

Nearly half the wall clock is the prefix. This is also why `-parallel` tuning
does nothing: raising it 8 → 64 leaves wall time flat, because the prefix is a
serialization ceiling, not a concurrency shortfall.

## Where the 14.4s actually lives

| cost | share | family |
|---|---|---|
| 5.4s | 37% | real-git `wtRepo` harness — 4.5s of it in the two `dispose` files alone (since fixed, below) |
| 2.6s | 18% | `*SeqFuzz` (shared sequence state) |
| 6.5s | 45% | everything else |

The top 25 tests are 56% of the cost, and they are dominated by the real-git
family — the part that cannot be parallelized (below). The long tail is ~350
sub-0.15s tests whose cost is per-test fixed overhead, not logic.

## What a t.Parallel() sweep actually buys: ~4s, at the price of flakes

Converted 340 tests across 90 files (screening out direct `t.Setenv`/`t.Chdir`,
swappable package-level seams, the git harness, and seqfuzz), each file verified
green individually:

| | pristine | swept |
|---|---|---|
| prefix ends at | 18.0s | 11.6s |
| serial work | 14.4s | 10.1s |
| **wall time** | **~39.6s** | **~35.6s** |
| **12-run stress** | **12/12 pass** | **10/12 pass** |

6.4s came off the prefix but only ~4s off the wall clock, because the freed
tests then compete for the same cores as the existing parallel bulk.

The sweep was **not landed**: trading a clean baseline for a 1-in-6 flake rate
is a bad deal on a gate.

## Why the broad sweep flaked

Two blockers, both verified rather than assumed. Note the first is about the
*sweep*, not about git tests in general — the dispose files parallelized fine on
their own (see below). What breaks is converting the wider package around them.

1. **Package-wide contention reaches the real-git tests.** `TestP3Sweep_*` and
   `TestP3CloseResidue_*` flaked under the 340-test sweep *even with every
   git-harness file reverted*: the added CPU contention from unrelated files was
   enough on its own. These tests drive actual `git`
   (init/commit/worktree add/merge/push/fetch), so they are the most
   timing-exposed work in the package. The coupling is package-wide, not
   per-file — which is why a targeted change succeeded where the broad one did
   not.

2. **Process-global state that static screening cannot see.** Two classes bit
   this effort:
   - **Swappable package-level seams.** Tests assign to `openAPILogFile`,
     `openTranscriptFile`, etc. and count calls. Under concurrency a sibling's
     traffic goes through the swapped hook, so the counter reads 7 (or 200)
     instead of 0. Detectable statically once you know to look for assignment
     to a package-level func var — which is why the screen now does.
   - **OS-level `flock` on the API log.** `TestSessionCloseReleasesAPILogRoute`
     and `TestNewSessionReleasesOwnershipWhenInitializationFails` assert a
     session releases its log lock on close. They fail under parallelism with
     `API log target is already running` even though each uses its own
     `t.TempDir()` and `lsof` shows no holder. Worth noting these cost 0.01s
     each — no reason to pursue them for speed.

A third trap: hazards reached **through helpers**. `mustExec` calls `t.Setenv`
internally, so a test whose own body looks clean still panics with
"test using t.Setenv or t.Chdir can not use t.Parallel". The screen must follow
callees transitively.

## Verification lesson

**One green run proves nothing here.** The 90-file sweep passed a per-file
single-run gate and then failed 6 of 12 full runs. `scripts/parallelize-tests.sh`
now takes `--runs N` and keeps a file only if every run passes.

## What was landed instead: 25 tests, same win, no flakes

Investigating "move the real-git tests to their own package" turned up a much
cheaper answer. Two facts reframed it:

- **Most real-git files were already parallel.** Of the 13 wtRepo files, only
  the two `dispose` suites were fully serial. The "real-git tests are serial"
  premise was wrong.
- **They were serial for no stated reason.** 25 of the 26 dispose tests carried
  no `t.Parallel()` and no comment explaining why. Each already owns an isolated
  repo and git env via the harness, so they parallelize as-is. The one genuine
  exception — `TestDispose_Depth2Cascade_SharedBudget`, which asserts on the
  process-global `closeBudgetMintHook` — stays serial.

| | pristine | 340-test sweep | **25 dispose tests** |
|---|---|---|---|
| prefix ends at | 18.0s | 11.6s | **11.9s** |
| serial work | 14.4s | 10.1s | **10.2s** |
| wall time | ~39.6s | ~35.6s | **~35.2s** |
| 12-run stress | 12/12 | 10/12 | **12/12** |

25 tests bought the same prefix reduction as 340, with none of the flakes,
in a 25-line diff. The broad sweep's extra 315 conversions contributed
essentially nothing to wall time while introducing every flake — the cost was
never spread across the long tail, it was concentrated in a handful of files.

## What is left, and what it would take

The residual ~10.2s prefix is spread thin: ~350 sub-0.15s tests whose cost is
per-test fixed overhead, plus 2.6s of `*SeqFuzz`. There is no remaining
concentration to exploit, and the measurements above show converting the tail
does not move wall time.

Moving the real-git tests into their own package was scoped and rejected: those
files reference **102 unexported identifiers** from package `agent`
(`worktreeDispose`, `disposeOneDelegateLane`, `jobManager`, `emit`, …), so an
external test package would require exporting all of them — a large, invasive
change to production code for a test-layout benefit. If the package split is
ever done, it should be driven by the compile-time argument (168K test LOC in
one compilation unit, ~5s per test-file edit), not by this prefix.
