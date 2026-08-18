# Dev tooling in Go: three gate runners, one small binary, no janitor

Status: proposed (v2 — simplified)
Decision trail: Jesse, 2026-08-17 — "how many of these scripts should be
replaced with a small standard library and then tools written in a
programming language?", "reclaim test debris should not exist. The tests
should be cleaning up after themselves.", and "think through the spec about
what we really need for the project and how to make this as clean and simple
as possible." v1 of this spec (in this file's git history) answered the
first question with a four-phase, ~8,700-line program porting 18 scripts.
This v2 is the second pass: the program is three ports, one small binary,
and the janitor's deletion. Everything else stays shell, on purpose.

## What the project actually needs

Each need, and the cheapest thing that genuinely satisfies it:

1. **The hazard class stays unrepresentable.** Done, in shell, by the
   #102 estate work: scratch-lib.sh, the count-pinned recursive-delete
   audit, and docs/testing.md's four rules. No Go required.
2. **Tests clean up after themselves, including after SIGKILL.** Mostly
   done, in shell, by #105: the four coverage runners reclaim their own
   dead-pid scratch via covscratch-lib.sh's pid-suffix discipline. What
   remains is the shard runner's debris class (still swept by the janitor)
   and test-cost.sh, which leaks its scratch on every run. Fixing those two
   retires `reclaim-test-debris.sh` entirely.
3. **Gate-critical logic gets honest tests.** The three runners the gate
   itself stands on — `agent-test-shards.sh` (311 lines, with a Python
   bin-packer embedded in bash), `run-module-lint.sh` (177), and
   `run-module-tests.sh` (439) — carry 1,835 lines of stub-world selftests
   between them (280 + 868 + 687), every one exercising fake toolchains on
   PATH. That is mock-testing by construction (rule 4), and it is where the
   estate's remaining mass and remaining risk live. These three port to Go,
   where the packing, scheduling, and summary logic get real unit tests and
   the orchestration gets real child processes.
4. **Everything else already works and is hand-run.** The coverage and fuzz
   families are operator tools with owners, real diagnostics, and (now)
   self-reclaiming scratch. Porting them moves working code between
   languages for aesthetics. They stay shell under a port-on-touch policy.

## What changed between v1 and v2

- **#105 (merged) killed v1's Phase 1.** Owner-side scratch reclamation
  works in shell: name-first/trap-first/mkdir with a `PREFIX.$$` pid
  suffix, and a shared reclaimer that removes only dead-pid siblings
  (measured zero-width abandon window, versus a real leak window for the
  mktemp-then-register shape). The six-tool coverage port, the Go scratch
  estate, and the `ratchet`/`covprofile`/`gatesurface` packages existed to
  justify each other. All cut.
- **v1's P3/P4 (fuzz absorb + campaign ports, ~3,500 Go) are cut** in favor
  of port-on-touch. The fuzz tools are hand-run, only `fuzz-gap-check` is
  CI-blocking, and half the stack (serf-fuzzcov, serf-fuzzregistry,
  serf-fuzz-harvest) is already Go. Nothing is on fire there.
- **Library packages are earned, not declared.** `report` and `procgroup`
  get extracted because both surviving ports need them on day one.
  `waves` never existed and still doesn't. `gatesurface` shrinks from a
  package to a 20-line drift-tripwire test.

## End state

- One binary, **`cmd/serf-dev`** (root module, like
  `cmd/serf-test-dev-tooling`), three subcommands: `agent-shards`,
  `module-lint`, `module-tests`. Make target names and their env/variable
  interfaces (`MODULES`, `ROOT_FULL`, `AGENT_SHARD_*`, `LINT_PARALLEL`,
  `WAVE1`/`WAVE2`, pass-through go-test flags) do not change; recipes swap
  `scripts/X.sh` for `go run ./cmd/serf-dev X`.
- **`scripts/reclaim-test-debris.sh` does not exist.** Every scratch owner
  reclaims its own stale scratch at startup, in whichever language it is
  written.
- The coverage and fuzz families stay shell, self-reclaiming via
  covscratch-lib.sh, policed by the audits, with their existing selftests
  as their living contracts until each tool's port-on-touch moment.
- The dev-tooling wave shrinks from 21 suites to 17 (the three runners'
  selftests and the janitor's die with their scripts).

## Design

### cmd/serf-dev

A dispatch `main.go` and one file per subcommand. `module-tests` invokes
the shard packer **in-process** (same binary — no subprocess hop), while
`agent-shards` remains independently invocable for operators. Output
contracts are ported byte-shape-for-byte-shape; the appendix tables are the
acceptance bar, and warts port as warts with follow-up katas.

### internal/devtool/scratch

The Go twin of the #105 discipline, not a new invention:

    dir, err := scratch.Acquire(prefix)  // TMPDIR-rooted "prefix.<pid>";
                                         // mkdir fails loudly on collision;
                                         // refuses TMPDIR resolving to /
                                         // or $HOME
    defer dir.Release()                  // removes only what Acquire made;
                                         // no path argument anywhere
    dir.KeepOnFailure(why)               // retained-logs contract: Release
                                         // reports the path instead

`scratch.ReclaimOwn(prefix)`, called by Acquire before minting, carries
covscratch-lib.sh's rules verbatim: directories only, direct children only,
exact prefix match, symlinks never matched or followed, pid parsed from the
basename suffix, live pids (including our own) always kept, removal applied
only to the validated path, every failed removal reported and never fatal.
Pid liveness replaces the janitor's mtime-freshness + `.heartbeat` scheme,
which retires with it. SIGINT/SIGTERM/SIGHUP run Release via signal
context; SIGKILL's debris lives exactly until the same tool's next run.

### internal/devtool/report and internal/devtool/procgroup

`report`: the one-summary-per-run discipline the lint selftest pinned —
exactly one `PASS`/`FAIL <tool> (<category>: …)` line on every exit path,
category ∈ {setup, not-checked, findings, results-lost, interrupted},
per-unit verdict lines as results arrive, failed units' logs replayed,
retained-log pointers. `procgroup`: spawn a child in its own process
group, TERM-then-KILL with a deadline, signal exits 129/130/143. Both have
two consumers the day they land; neither grows speculative surface.

### Gate-surface drift tripwire

`module-tests` needs `GATE_TEST_RUN`/`GATE_FUZZ_TEST_SKIP` as Go constants;
the shell coverage runners keep reading `scripts/gate-surface-lib.sh`. One
Go test pins the shell file's values byte-equal to the Go constants so the
two homes cannot drift. It dies if the last shell consumer ever ports.

## Testing doctrine for the ports

- Logic (LPT packing, survey caching, wave scheduling, summary formatting,
  flag/env validation) lives in pure functions with table tests — this is
  most of what the stub selftests were straining to reach through PATH
  fakes.
- Orchestration is tested with real child processes, the way wave_test.go
  already does it. Never a mocked exec seam, never a fake `go` or
  `golangci-lint` on PATH, never assertions on argv strings.
- Every test lands red-first. Destructive paths follow docs/testing.md:
  markers over weapons, decoys over live targets, one meta-level.
  ReclaimOwn's tests use fixture TMPDIRs seeded with decoy directories and
  dead/live/own-pid names.
- Timeouts follow the flakes policy: await completions; ceilings are
  tripwires sized to be unreachable on a healthy loaded machine.

## The program

Two steps; 1a/1b/1c are independent lanes.

**1a — shard runner + the janitor's death.** Build `scratch`; port
`agent-test-shards.sh` → `serf-dev agent-shards` (parity table below);
update `run-module-tests.sh`'s call site to the new invocation. Delete the
script, its selftest (280), `reclaim-test-debris.sh` (208), and its
selftest (140); remove both from the Makefile, docs, and audit allowlists.
Recorded one-time hand cleanup on the kata: `/tmp/serf-gocache-k3` and any
pre-port `agent-test-shards.*` debris (old random-suffix names carry no pid
and are invisible to the new reclaim). Exit criteria: gate green; a
`kill -9` mid-run leaves a directory the NEXT run removes, pinned by a real
Go test; janitor gone everywhere its name appears.

**1b — lint runner.** Build `report` + `procgroup`; port
`run-module-lint.sh` → `serf-dev module-lint`. Delete the script and its
selftest (868) — the estate's two pinned vanishing-scratch fixture weapons
die here, shrinking the audit's exception list to zero weapons. Exit
criteria: gate green; real-process signal tests prove the one-summary
contract on every exit path.

**1c — test-cost cleans up after itself.** Shell, a few lines: trap-first
scratch named `test-cost.$$` plus `reclaim_own_scratch` at startup (its
scratch is `mktemp -t` today — also fixing the macOS TMPDIR-ignoring mint).
It stays shell; it is a profiler someone runs by hand, and its logic is one
loop.

**2 — module-tests runner.** After 1a lands: port `run-module-tests.sh` →
`serf-dev module-tests`, calling the shard packer in-process. Delete the
script and its selftest (687); land the gate-surface drift tripwire.
Semantics preserved exactly: wave 1 = root alone (latency-measured; do not
"fill" it), wave 2 = rest concurrent with the web stream overlapped, `web`
module-name collision refused (exit 2), one verdict line per module as it
finishes, failing module's output replayed at the end, retained logs on
failure/interrupt, `SERF_ROOT_PACKAGE_LIST_TIMEOUT` validation. Exit
criteria: gate green twice consecutively.

Each port PR deletes its script and selftest in the same change — no
parallel implementations — swaps the Makefile recipe, shrinks the audit
allowlists, and carries its parity table in the PR body.

## Port-on-touch (replaces v1's P3 and P4)

The coverage family (test-coverage-floor, coverage-union, coverage-gaps,
web-coverage-floor, covstmt-lib, gate-surface-lib) and the fuzz family
(run-fuzz, fuzz-triage, fuzz-continuous, fuzz-bisect, fuzz-oracle-audit,
fuzz-drive, fuzz-coverage, fuzz-coverage-global, fuzz-gap-check,
fuzz-registry-check, fuzz-mutation-score) stay shell until one of them next
needs a **behavior change** or a testing ask its selftest cannot honestly
answer. That moment is its port, into serf-dev, under this doctrine. Floor
blessings, registry entries, and line tweaks do not trigger it. v1 of this
spec (git history of this file) holds the full per-tool contract extraction
for whoever executes a future port.

## What does NOT change

- Make target names, their variable interfaces, and their output contracts.
  CI and operator muscle memory are interfaces.
- Floors, registry, ledger, and ignore-list file formats and locations.
- docs/testing.md's four standing rules; the audits keep policing whatever
  shell remains, including covscratch-lib's sanctioned reclaim.
- `cmd/serf-test-dev-tooling` keeps running the surviving wave; it is not
  consolidated into serf-dev.
- MEMCAP/run-capped.sh capping topology; serf-dev subprocesses inherit
  `SERF_CAPPED` semantics untouched.

## Non-goals

- No coverage-family or fuzz-family ports now; no registry-data extraction.
- No feature work during ports (parity first; warts fixed via follow-ups).
- No janitor replacement service: reclamation is owner-side or it is the
  old design back again.
- No CI behavior changes.

## Sizing (lines, per the estimating convention)

| Step | Go added (incl. tests) | Shell deleted |
|---|---|---|
| 1a | ~800 (scratch + agent-shards) | ~940 |
| 1b | ~500 (report + procgroup + module-lint) | ~1,045 |
| 1c | +8 shell | 0 |
| 2 | ~900 (module-tests + tripwire) | ~1,126 |

Net: ~2,200 lines of Go for ~3,100 of shell — against v1's ~8,700 for
~9,100. The counts that matter: fake toolchains in the gate path go to
zero, the janitor goes to zero, the wave drops 21 → 17, and the audit's
pinned fixture weapons go to zero.

## Flagged for Jesse, deliberately NOT in this program

The fuzz-family selftests (fuzz-coverage-global-selftest at ~946 lines,
plus triage 209 / drive 176 / continuous 166 / oracle-audit 146 /
bisect 123) pin hand-run tools through fake toolchains — the same standard
we deleted deploy-hub-selftest under. Consistency says they shrink or die,
but that is estate deletion beyond the signed-off kill list, so it is a
recommendation awaiting a decision, not part of this spec.

## Appendix: parity contracts for the three ports

### agent-shards (1a)
- Env: `AGENT_SHARD_COUNT` (4), `AGENT_SHARD_PARALLEL` (3),
  `AGENT_SHARD_SKIP`, `AGENT_SHARD_NO_SURVEY`, `AGENT_SHARD_RESURVEY`,
  `AGENT_SHARD_CACHE_DIR` (`$(go env GOCACHE)/serf-agent-shards`); go-test
  flags pass through.
- Contract: every test in exactly one shard, proven before running;
  cost-balanced LPT packing from a survey cached by test-set identity; one
  PASS/FAIL line per shard with wall time; logs deleted only on
  normal+green completion, else `full logs:` pointer; exit nonzero on any
  shard failure or partition discrepancy.
- Port deltas (sanctioned, not warts): scratch becomes
  `agent-test-shards.<pid>` with startup ReclaimOwn; `.heartbeat` retires
  with the janitor.

### module-lint (1b)
- Env-only interface: `MODULES`, `LINT_PARALLEL` (positive int, no leading
  zeroes, else exit 2).
- Contract: bounded-concurrency waves with a start gate; exactly one
  summary line per run — `PASS lint (N modules, Ss)` / `FAIL lint
  (<category>: …)`, category ∈ {setup, not-checked, findings, results-lost,
  interrupted}; failed modules' logs replayed under `----- module -----`
  fences; signal exits 129/130/143; logs retained on findings with pointer;
  `--allow-parallel-runners` passed to golangci-lint.

### module-tests (2)
- Env: `MODULES`, `ROOT_FULL`, `WEB`/`WEB_DIR`, `WAVE1`/`WAVE2`,
  `AGENT_SHARDS`, `AGENT_PARALLEL`, `AGENT_P`, `ROOT_P`,
  `SERF_ROOT_PACKAGE_LIST_TIMEOUT` (positive int, else exit 2); go-test
  flags pass through, `-short` stripped for root under `ROOT_FULL=1`.
- Contract: wave 1 = root alone, wave 2 = rest concurrent, web stream
  overlapped; `web` module-name collision refused (exit 2) when `WEB=1`;
  one verdict line per module as it finishes; failing module's full output
  replayed at the end; retained logs on failure/interrupt; the gate surface
  (`GATE_TEST_RUN`/`GATE_FUZZ_TEST_SKIP`) identical to the shell lib's,
  pinned by the drift tripwire.
