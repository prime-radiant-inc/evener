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
| 5.4s | 37% | real-git `wtRepo` harness (`Dispose_*`, `P3Sweep_*`, worktree tools) |
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

## Why the remaining 10.1s is stuck

Two hard blockers, both verified rather than assumed:

1. **The real-git `wtRepo` harness.** These tests drive actual `git`
   (init/commit/worktree add/merge/push/fetch) against a shared base repo.
   Parallelizing them fails — and, more importantly, parallelizing *other*
   files still breaks them: `TestP3Sweep_*` and `TestP3CloseResidue_*` flaked
   even with every git-harness file reverted, because the added CPU contention
   alone is enough. The coupling is package-wide, not per-file.

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

## Recommendation

The cheap win is ~4s and costs suite reliability, so it is not worth taking as-is.
Getting the full prefix back requires decoupling the real-git harness — giving
each test an isolated repo, or moving that suite into its own package so its
serial prefix runs concurrently with the rest of the module. That is the
monolith-split refactor, and it needs a design decision, not a sweep.
