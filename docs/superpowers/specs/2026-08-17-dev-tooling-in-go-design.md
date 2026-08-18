# Dev tooling in Go: one small library, real tools, and the end of the janitor

Status: proposed
Decision: Jesse, 2026-08-17 — "how many of these scripts should be replaced
with a small standard library and then tools written in a programming
language?" and "reclaim test debris should not exist. The tests should be
cleaning up after themselves." This spec is the answer; implementation awaits
Jesse's approval of this document.

## Thesis

The shell selftest estate existed because the tools accumulated real logic —
bin-packing, ratchet arithmetic, wave scheduling, coverage-profile set
algebra, ledger state machines — and shell logic can only be tested by faking
binaries on PATH. That produced 6,669 lines of stub-world selftests for 5,876
lines of scripts, a machine-destroying meta-layer, and five gate flakes in
one day. The 2026-08-17 gutting (katas jvpe/yns5) removed the theater; this
spec removes the cause.

The selection metric, applied to the post-gutting estate: **a script whose
tests needed a fake toolchain is a script that outgrew shell.** Those move to
Go, where the logic gets ordinary unit tests, the type system, and `-race`.
Scripts that wrap one OS invocation stay shell — wrapping is shell's job.

Three facts from the contract extraction sharpen the plan beyond the first
sketch:

1. **Half the fuzz stack is already Go.** `cmd/serf-fuzzcov`,
   `cmd/serf-fuzzregistry`, and `cmd/serf-fuzz-harvest` own the measurement,
   registry validation, and seed harvesting. Several fuzz shells are thin
   glue around them (`fuzz-gap-check.sh` 22 lines, `fuzz-registry-check.sh`
   15, `fuzz-coverage.sh` 65). Those don't get "ported" — they get deleted,
   with their two or three commands moving into Makefile recipes or into the
   Go tools they wrap.
2. **`run-fuzz.sh` is 86% data.** 690 of its 802 lines are the static
   `TARGETS` registry; the logic is a ~60-line dispatcher. The port is a
   registry-data extraction, not an 800-line rewrite.
3. **The janitor's guards are real and must be inherited, not discarded.**
   Heartbeat/freshness liveness, positive GOCACHE identity verification with
   a TOCTOU re-check, symlink refusal, direct-children-only enumeration —
   the self-reclaiming scratch library absorbs these, with an owner.

The in-repo existence proof for the whole approach is
`cmd/serf-test-dev-tooling`: Go, real-process tests, no selftest, no fixture
toolchain, no allowlist entries.

## End state

- One binary, **`cmd/serf-dev`**, one subcommand per retired script, invoked
  from the Makefile as `go run ./cmd/serf-dev <subcommand> ...`. Subcommand
  names mirror script basenames (`module-tests`, `module-lint`,
  `agent-shards`, `test-cost`, `coverage-floor`, `coverage-union`,
  `coverage-gaps`, `web-coverage-floor`, `fuzz-triage`, `fuzz-continuous`,
  `fuzz-bisect`, `fuzz-oracle-audit`, `fuzz-drive`, `fuzz-run`) so operators
  and docs relearn nothing. Make target names and their `CHECK=1`/`BLESS=1`/
  `FUZZ_ARGS` interfaces do not change.
- One library, **`internal/devtool/`**, holding exactly the pieces two or
  more tools share. No speculative packages: a package exists only when its
  second consumer lands.
- **`scripts/reclaim-test-debris.sh` does not exist.** Every scratch owner
  reclaims its own stale scratch at startup. Tests clean up after
  themselves, including after the exits no shell trap can see.
- The existing Go fuzz tools absorb the shell-side validation that grew
  around them; the fuzz registry lives in a declarative file owned by
  `serf-fuzzregistry`, not in a bash array.
- The shell that remains is glue, each piece wrapping one thing:
  `private-go-home.sh`, `build-runtime-pair.sh`, `setup-gocache.sh`,
  `gitleaks-scan.sh`, `run-capped.sh` (a decision ladder around one
  `systemd-run` exec — confirmed glue), `seatbelt-smoke.sh`,
  `agent-chrome.sh`, `web-preflight.sh`, the e2e stack harnesses
  (`e2e-webui-turn-controls.sh`, `e2e-ratelimited-provider.sh`,
  `e2e-cover.sh`), `deploy-hub.sh`, `refresh-model-catalog.sh`,
  `live-eval-isolation.sh`, `scratch-lib.sh` + `selftest-lib.sh`, and the
  surviving glue selftests (~8 suites in the wave). The audits in
  scriptmktemp_audit_test.go keep policing whatever shell remains.
- The report tools (`report-tmp-debris.sh`, `report-orphaned-worktrees.sh`)
  stay shell (read-only, hand-run); folding them into `serf-dev` is optional
  follow-up, not part of this program.

## The library: internal/devtool

**`scratch`** — the Go twin of scripts/scratch-lib.sh, plus the capability
that retires the janitor.

    dir, err := scratch.Acquire(prefix)   // mint under TMPDIR, validated
    defer dir.Release()                   // idempotent; removes only what
                                          // Acquire minted (no path argument
                                          // anywhere in the API)
    dir.KeepOnFailure(why)                // flips Release to retain+report,
                                          // for the retained-logs contract

  Same invariants as the shell guard, enforced in one place: explicit
  template under TMPDIR (never the Darwin per-user dir), canonicalized after
  creation, basename must match what was requested, containment checked
  before anything is handed out, removal reaches only the tracked path.

  **Self-reclamation** (the janitor-killer): `Acquire` writes
  `<dir>/.owner` (pid + start time, refreshed periodically for long runs —
  subsuming agent-test-shards' `.heartbeat`) and, before minting, sweeps
  sibling directories under the canonicalized TMPDIR that match its own
  prefix exactly and are BOTH older than an age floor (default 30m, the
  janitor's) AND owned by a dead pid (or ownerless and past the floor).
  Inherited safety, verbatim from the janitor's checklist: directories only,
  direct children only, exact prefix match, symlinks never matched or
  followed, `rm` equivalent applied to the validated path only, every failed
  removal reported, and the sweep refuses to run when TMPDIR resolves to `/`
  or `$HOME`. SIGKILL, OOM and power cuts leave debris exactly until the
  next run of the same tool — which is also when the debris starts costing
  something. The GOCACHE-identity guard does not carry over because nothing
  in the new world deletes caches; the one-off `/tmp/serf-gocache-k3` gets
  deleted by hand once, recorded on the kata.

**`procgroup`** — spawn a child in its own process group; terminate the
  whole group with TERM-then-KILL and a deadline; enumerate and reap
  descendants. Replaces the `process_descendants`/`stop_children`/heartbeat
  bash copied across three runners. darwin/linux only.

**`report`** — the output discipline the selftests used to pin: one
  line-per-unit stream, exactly one summary line per run on every exit path,
  the failure-class vocabulary (`setup:`, `not-checked:`, `findings:`,
  `results-lost:`, `interrupted:`), and retained-log pointers.
  run-module-lint's `fail_lint` contract becomes a type every runner shares.

**`ratchet`** — floors files (`<name> <pct>` lines, `#` header notes):
  parse, check-with-tolerance, bless. Two bless disciplines exist today and
  both are contracts: partial bless preserving unmeasured floors and header
  notes (test-coverage-floor, coverage-union) and whole-file rewrite with no
  partial-bless footgun (web-coverage-floor). The package supports both,
  each pinned by the table tests of the tool that owns it. Bless on a red
  suite is refused (web-coverage-floor's rule, generalized).

**`covprofile`** — statement counting from Go cover profiles: dedupe blocks
  by position, covered-if-any-occurrence-hit, union by concatenation,
  boundary-mismatch detection with the negligible-variance carve-out, and
  the uncovered-blocks rollups coverage-gaps prints. Replaces covstmt-lib.sh
  (Python embedded in bash) and the awk merge/validators in
  fuzz-coverage-global.sh — the latter likely landing inside
  `cmd/serf-fuzzcov` rather than serf-dev, since serf-fuzzcov already owns
  profile accounting.

**`gatesurface`** — the two constants from gate-surface-lib.sh
  (GATE_TEST_RUN, GATE_FUZZ_TEST_SKIP). Transition discipline: while any
  shell consumer of gate-surface-lib.sh survives, a Go test pins the shell
  file's values byte-equal to the Go constants so the two homes cannot
  drift; when the last shell consumer ports (P2), the shell file dies and
  the tripwire with it.

**`waves`** — only if P2 shows run-module-tests and serf-test-dev-tooling
  genuinely share a scheduler shape; otherwise run-module-tests gets its own
  small wave loop and this package never exists. YAGNI decides at
  implementation time.

## Testing doctrine for the ports

- Logic (packing, ratchet math, profile algebra, ledger state machines,
  plan validation) lives in pure functions with table tests. This is most of
  what the stub-world selftests were straining to reach through PATH fakes.
- Process orchestration is tested with REAL child processes — tiny fixture
  programs compiled from testdata, the way wave_test.go already does it.
  Never a mocked exec seam: mock-behavior tests are banned (CLAUDE.md), and
  the fake-toolchain selftest was that ban violated in shell.
- Every test lands red-first; destructive paths follow docs/testing.md:
  markers over weapons, decoys over live targets, depth capped at one
  meta-level. scratch's reclamation tests operate on fixture TMPDIRs seeded
  with sentinel decoys and fake `.owner` files for dead/live/absent pids.
- Timeouts follow the Flakes policy: await completions; any ceiling is a
  tripwire sized to be unreachable on a healthy loaded machine.

## Migration phases

Each port PR deletes the script and its selftest in the same change — no
parallel implementations — swaps the Makefile recipe, shrinks the audit
allowlists, and carries a parity table (flags, env, durable files, exit
codes, output shapes; appendix below is the source) in its body. Contract
parity is the acceptance bar. **Warts are preserved at parity and fixed in
follow-up katas, never smuggled into a port** (the known warts are listed in
the appendix; each gets its own kata at port time).

**P1 — scratch, the measurement family, and the janitor's death.**
Build `scratch`, `ratchet`, `covprofile`, `gatesurface` (+ drift tripwire).
Port six tools: `agent-test-shards.sh` (311; embeds a python3 bin-packer —
three languages in one file), `test-cost.sh` (139; today it leaks its
mktemp dir on every run with no cleanup trap, invisible to the janitor —
the port fixes this by construction), `test-coverage-floor.sh` (189),
`coverage-union.sh` (184), `coverage-gaps.sh` (115),
`web-coverage-floor.sh` (172).
Delete: those six scripts, their five selftests (~1,540 lines),
covstmt-lib.sh, **and `reclaim-test-debris.sh` + its selftest (~400)**.
Both live debris classes (`agent-test-shards.*`, `serf-testcov.*`/
`serf-covunion.*`/`serf-fuzzcov.*`/`serf-fuzzcov-global.*`) get Go owners
that self-reclaim. The fuzz-coverage prefixes' owners port in P3; until
then those two runners keep their shell scratch-lib traps (clean on every
trappable exit), and their stale-debris exposure window is P1→P3 — accepted
and noted, since the accumulated measurement was 61 dirs over months, not
days.
Exit criteria: gate green; a kill -9 mid-coverage-run leaves a directory
that the NEXT run of the same tool removes (pinned by a real test); the
audit lists shrink by the ported entries; janitor gone from the Makefile
list, docs/conventions/agent-fleets.md, and the audits;
`/tmp/serf-gocache-k3` hand-deleted and recorded.

**P2 — the gate runners.**
Build `procgroup`, `report` (and `waves` if earned).
Port: `run-module-tests.sh` (432) and `run-module-lint.sh` (177) — the two
highest-blast-radius scripts (`make test`, `make lint`, CI). Contracts that
must survive byte-for-byte where the selftests pinned them: the
one-summary-per-run vocabulary, per-module verdict lines, retained-log
behavior on failure and interruption, the web-stream name collision
refusal, LINT_PARALLEL validation, wave scheduling semantics, signal exit
codes (129/130/143). Their selftests (687 + 868) become Go tests; the two
pinned fixture weapons (vanishing-scratch fakes) die with the lint suite,
removing the audit's only delete-is-the-behaviour entries.
Delete: both scripts, both selftests, gate-surface-lib.sh (Go becomes the
sole owner; drift tripwire retired).
Exit criteria: gate green twice consecutively; real-process signal tests
prove the summary discipline on every exit path.

**P3 — fuzz measurement: absorb, don't port.**
The registry moves out of `run-fuzz.sh` into a declarative file owned by
`cmd/serf-fuzzregistry` (schema: today's `tag:module:pkg:name[:coverpkg
[:focus]]` plus an explicit OS column, replacing the Linux-only append
hack). `serf-fuzzregistry` provides `--list` output byte-compatible with
`run-fuzz.sh --list` for the transition, since seven consumers parse it.
`fuzz-coverage-global.sh`'s validation/merge logic (its awk profile
validator, plan schema check, preflight denominator check) moves INTO
`cmd/serf-fuzzcov`, which already owns accounting; the script shrinks to a
Makefile recipe. `fuzz-gap-check.sh`, `fuzz-registry-check.sh`, and
`fuzz-coverage.sh` are deleted in favor of direct recipes around the Go
tools (their replay loop moves to serf-dev `fuzz-run --replay` or into
serf-fuzzcov). `fuzz-mutation-score.sh` (91 lines, mostly gremlins glue)
stays shell unless it grows; reclassified out of the rewrite set.
Delete: four scripts + fuzz-coverage-global-selftest (914) — the estate's
largest surviving suite.
Exit criteria: `make fuzz`, `make fuzz-gap-check` (CI-blocking) and the
coverage targets behave identically; registry drift detection unchanged;
plan TSV contract enforced in one place (Go) instead of two (shell+Go).

**P4 — fuzz campaign tools.**
Port into serf-dev: `fuzz-triage.sh` (495; ledger state machine, dedup
layers, flake-guard, corpus promotion, and the no-checkout git plumbing
that commits crasher branches without touching the working tree — that
plumbing is a feature and ports as-is), `fuzz-continuous.sh` (256),
`fuzz-oracle-audit.sh` (155; worktree-isolated mutation audit),
`fuzz-bisect.sh` (151; the self-contained-probe design survives — the probe
must remain generated-with-literals because `git bisect run` visits commits
where no current tool exists), `fuzz-drive.sh` (247; LIVE paid calls,
stays behind explicit invocation), and the ~60-line dispatcher left of
`run-fuzz.sh`.
Delete: six scripts + five selftests (~800 lines of the estate's remainder).
Exit criteria: ledger/bucket JSON formats unchanged (fuzz/state/ is durable
history); `make fuzz-nightly` and friends behave identically; the appendix
warts preserved and their follow-up katas filed.

## What does NOT change

- Make target names, their `CHECK=1`/`BLESS=1`/`FUZZ_ARGS`/`MIN=` variable
  interfaces, and their output contracts. CI and operator muscle memory are
  interfaces.
- Floors, registry, ledger, and ignore-list file formats and locations
  (checked in; blessing and triage history must survive).
- docs/testing.md's four standing rules — this spec is their §4 executed.
- `cmd/serf-test-dev-tooling` keeps running the surviving glue selftests;
  it is not consolidated into serf-dev in this program.
- scratch-lib.sh stays for the glue scripts, with its direct selftest; the
  audits keep their count-pinned lists for whatever shell remains.
- MEMCAP/run-capped.sh capping topology (which targets are capped, the
  re-entrancy guard, the shared slice) is preserved exactly; serf-dev
  subprocesses inherit `SERF_CAPPED` semantics untouched.

## Non-goals

- No feature work during ports (parity first; warts fixed via follow-ups).
- No rewrite of glue scripts.
- No janitor replacement service: self-reclamation is owner-side or it is
  the old design back again.
- No CI behavior changes: the only CI-blocking fuzz gate stays
  `fuzz-gap-check` until someone decides otherwise, separately.

## Sizing (lines, per the estimating convention)

| Phase | Go added (incl. tests) | Shell/python-in-bash deleted |
|---|---|---|
| P1 | ~2,800 | ~3,050 (6 tools, 5 suites, covstmt, janitor+suite) |
| P2 | ~2,400 | ~2,200 (2 tools, 2 suites, gate-surface) |
| P3 | ~900 (mostly into serf-fuzzcov/-registry) | ~1,700 (4 tools, 1 suite, registry data relocated) |
| P4 | ~2,600 | ~2,150 (6 tools, 5 suites) |

Net: ~9,100 lines of shell (and Python-in-bash, and one bash-embedded
bin-packer) retired for ~8,700 of Go that ordinary tooling can type-check,
race-test, and unit-test. The counts that matter more: fake toolchains in
tests go to zero, the janitor goes to zero, and the audit allowlist trends
toward the two glue entries that deserve to exist.

## Appendix: per-tool contracts (the parity checklists)

Condensed from the 2026-08-17 contract extraction; each table becomes the
parity checklist in its port PR. "Warts" are preserved at parity; each gets
a follow-up kata filed at port time.

### agent-test-shards (P1)
- Env: `AGENT_SHARD_COUNT` (4), `AGENT_SHARD_PARALLEL` (3),
  `AGENT_SHARD_SKIP`, `AGENT_SHARD_NO_SURVEY`, `AGENT_SHARD_RESURVEY`,
  `AGENT_SHARD_CACHE_DIR` (`$(go env GOCACHE)/serf-agent-shards`); go-test
  flags pass through.
- Contract: every test in exactly one shard, proven before running;
  cost-balanced LPT packing from a survey cached by test-set identity; one
  PASS/FAIL line per shard with wall time; logs deleted only on
  normal+green completion, else `full logs:` pointer; exit nonzero on any
  shard failure or partition discrepancy.
- Caller: run-module-tests.sh (P2 caller ports later — the Go tool must
  remain invocable standalone in between).

### test-cost (P1)
- Flags: `--dir` (required), `--run` (`^Test`), `--top` (40), `--min-ms`
  (0), `--reps` (1, reports minimum), `--json FILE`; unknown → exit 2.
- Contract: survey pass then per-test isolated timing at `-parallel 1`;
  stdout table `isolated/in-suite/stretch/test` + totals + advisory; exit 1
  when no tests match.
- Wart fixed by construction: today it leaks its scratch (no trap, not in
  any reclaim list).

### test-coverage-floor / coverage-union / coverage-gaps /
### web-coverage-floor (P1)
- Shared: floors files with `#` headers; `--check` (tolerance 0.5pp),
  `--bless`; `-coverpkg` scoped to the module's own packages, never
  `./...`; gate surface (`GATE_TEST_RUN`/`GATE_FUZZ_TEST_SKIP`) identical
  to the gate's; statement accounting = dedupe-by-position,
  covered-if-any-hit.
- test-coverage-floor: root measured without `-short`, others with;
  UNMEASURED floored module fails `--check` loudly; partial bless preserves
  unmeasured floors + header notes.
- coverage-union: union = concatenation; boundary mismatch fails, ≤1%
  variance reported as `(+N boundary-variant)`; row shape
  `module covered total union% test% fuzz% floor`.
- coverage-gaps: ranks uncovered by COUNT not percentage; `--by
  package|file`, `--zero`, `--in PATTERN` block mode; exit 2 usage / 1
  missing profile.
- web-coverage-floor: per-AREA floors (top dirs under src/ + `(root)` +
  `total`); deletes stale coverage-summary.json before measuring; bless
  refused on a red suite; bless rewrites every area (no partial).

### run-module-lint (P2)
- Env-only interface: `MODULES`, `LINT_PARALLEL` (positive int, no leading
  zeroes, else exit 2).
- Contract: FIFO start-gate wave scheduling; exactly one summary line per
  run — `PASS lint (N modules, Ss)` / `FAIL lint (<category>: …)`,
  category ∈ {setup, not-checked, findings, results-lost, interrupted};
  failed modules' logs replayed under `----- module -----` fences; signal
  exits 129/130/143; logs retained on findings with pointer.

### run-module-tests (P2)
- Env: `MODULES`, `ROOT_FULL`, `WEB`/`WEB_DIR`, `WAVE1`/`WAVE2`,
  `AGENT_SHARDS`, `AGENT_PARALLEL`, `AGENT_P`, `ROOT_P`,
  `SERF_ROOT_PACKAGE_LIST_TIMEOUT` (positive int, else exit 2); go-test
  flags pass through, `-short` stripped for root under ROOT_FULL=1.
- Contract: wave 1 = root alone (latency-sensitive; measured, do not
  "fill" without re-measuring), wave 2 = rest concurrent, web stream
  overlapped; `web` module-name collision refused (exit 2) when WEB=1; one
  verdict line per module as it finishes; failing module's full output
  replayed at the end; retained logs on failure/interrupt.

### fuzz registry + measurement (P3)
- `run-fuzz --list` byte-format: `tag:module:pkg:name[:coverpkg[:focus]]`,
  one per line, consumed by seven tools — byte-compatible during
  transition. Wart: today's output is OS-dependent (two Linux-only entries
  appended); replaced by an explicit OS column in the registry file.
- fuzz-registry-check stdout contract: headerless 4-column TSV
  `kind\tmodule\tpkg\tname`, only emitted when driftless (serf-fuzzcov's
  plan input; schema enforced today in two places, one after P3).
- fuzz-coverage-global: `--check/--bless/--modules/--format text|json`;
  floors + exclusions + shared ignore file; progress on stderr, report on
  stdout; expected gate failures still print JSON then exit 1; rapid
  replay pins RAPID_SEED ∈ {1,2,3,5,8}, RAPID_CHECKS=100, RAPID_STEPS=30.
  Wart: `-global-minimum 95` hardcoded.
- fuzz-coverage: forwards args to serf-fuzzcov; replay failure forces exit
  1 only when `--check` present (literal scan of `$@` — wart).

### fuzz campaign (P4)
- fuzz-triage: `--time/--dry-run/--no-pr/--no-corpus [targets]`; ledger
  `fuzz/state/ledger.json` + `buckets.json` (atomic jq+mv updates); K=5
  flake-guard replays; dedup by ledger, open PRs, and signature; corpus
  promotion content-deduped, smallest-first, 8 seeds/32KB caps; crasher
  branches `fuzz/crash-<sig12>` committed via read-tree/commit-tree
  plumbing WITHOUT touching HEAD or the working tree (feature, ports
  as-is; pre-commit hooks intentionally bypassed). Warts: exit code ~always
  0 (results live in the ledger); commit messages embed a fixed session
  URL.
- fuzz-continuous: `--total/--time/--sweep/--max-turns/--dry-run/--no-pr/
  --drive-every/--drive-providers [targets]`; round-robin or sweep
  rotation; ledger before/after diff per turn; exit 1 iff new signatures
  this session; SIGINT finishes the turn then summarizes.
- fuzz-bisect: `--target/--crasher/--good/--bad`; crasher must begin `go
  test fuzz v1`; bracket verified at both endpoints before bisecting;
  generated self-contained probe (literals only — bisect visits commits
  where no tool exists); probe exits 0/1/125 (skip on build failure).
  Wart: exits 0 even when bisection does not converge.
- fuzz-oracle-audit: manifest `fuzz/mutations/manifest.tsv`; throwaway
  worktree per audit; verdicts `ok/BLIND/ROT/ERR` (a build failure scores
  ERROR, never "caught"); exit 0 only if all audited mutations caught;
  `--gap-only` lists unaudited native targets, exit 0.
- fuzz-drive: LIVE paid calls; provider list/task dir/run caps/retry
  budget; transient classifier (rate limit/429/overloaded/timeout regex)
  with exponential backoff; first non-transient failure skips the
  provider; gitleaks gate before staging seeds; summary + branch/PR
  disposition notes. Wart: unlike triage, it switches the developer's
  working tree onto the corpus branch (`git checkout -b`) — follow-up kata
  to unify on the no-checkout plumbing.
- run-fuzz dispatcher: three invocation shapes (native `-fuzz` with
  budget, test replay, rapid with RAPID_CHECKS/SERF_FUZZ_TESTS=1), all
  under run-capped; per-target banners; aggregates failures, exits 1 if
  any.

### Janitor safety inheritance map (P1)
| Janitor guard | Where it lives in `scratch` |
|---|---|
| direct-children-only, exact prefix, dirs only | Acquire's sweep enumerator |
| symlink never matched/followed | sweep enumerator (Lstat) |
| freshness floor (30m default) | sweep age check |
| `.heartbeat` liveness (10m) | `.owner` pid liveness + refresh |
| indeterminate marker → keep | unreadable/foreign `.owner` → keep |
| every failed removal reported, partial = nonzero | sweep error accounting |
| GOCACHE positive-identity + TOCTOU recheck | not carried: nothing in the new world deletes caches; the one-off path is hand-deleted once |
| dry-run mode | `scratch.SweepReport()` used by tests; no CLI needed |
