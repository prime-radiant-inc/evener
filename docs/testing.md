# Testing

## Test Reliability Policy

The default test suite must be deterministic. Running `make test` or
`go test ./...` must not depend on provider credentials, model availability,
network access, quota, current model behavior, wall-clock timing outside the
process, or ambient developer machine state.

Use this boundary when adding or fixing tests:

- If the test verifies Evener plumbing, use a scripted provider at the LLM
  boundary and exercise the real Evener code below it. Examples: CLI flag/config
  wiring, appwire RPC, daemon input queues, session loops, tool execution,
  transcript writes, event emission, goal continuation routing, hook dispatch,
  and prompt composition.
- If the test verifies model behavior, keep it live. Examples: whether a
  specific model chooses a tool from a natural-language instruction, follows an
  output contract, supports a provider feature, honors a live API wire shape, or
  behaves well across multi-turn goal prompts.
- Live tests must be explicitly opt-in with a `EVENER_*_E2E=1` or
  `EVENER_LIVE_TESTS=1` style environment variable in addition to the provider
  credential. A provider key by itself must never make the default suite issue
  live requests.
- Do not use sleeps, polling races, or large string snapshots to prove behavior
  when a structured event, state field, file result, or fake transport script can
  prove the same contract.
- Do not mock Evener internals to make a test pass. Keep the fake boundary at an
  external dependency: LLM provider, network server, filesystem root, clock, or
  process launcher.

When a test needs a model, name that as the behavior under test and keep it out
of the default suite. When the model is only a way to drive Evener, replace it with
a scripted `llm.ProviderAdapter` response and assert the Evener side effects.

## Flakes and Timeouts

Two standing rules (Jesse, 2026-07-20/21), both absolute:

**A sighted flake gets a root-cause fix on the spot.** Even one failure,
even under heavy load, even in code you don't own. "Pre-existing" and
"another session's area" are not exemptions; a deferred flake gets
re-triaged at full cost by every session that hits it, and it poisons
every run's signal in between. The fix must address the mechanism, not
the symptom. Honest irreproducibility after serious looped effort is
reportable, but only with mechanism analysis, not a shrug.

**Never widen or hardcode a timeout to absorb awaitable work.** A
widened deadline hides a guess that re-flakes as the suite grows, and it
converts real failures into timeout mysteries. The preference hierarchy:

1. Await the actual async completion — the import promise, event,
   callback, channel, or process exit.
2. Condition-watching (`findBy`/`waitFor`/poll-until) only where no
   awaitable completion exists; any ceiling there is a tripwire bound,
   never the mechanism.
3. Never fixed sleeps, fixed flush counts, or widened deadlines to paper
   over load-dependent work. Where pacing itself is under test, use the
   codebase's clock abstraction.

The repo's track record backs the policy: every flake family root-caused
here (vite-cache digest, tmux palette capture, auth stopwatch, composer
projection refresh) turned out to be an awaitable completion nobody was
awaiting, and zero were fixed by widening a timeout.

## Destructive Operations and the Tooling Test Estate

Four standing rules (Jesse, 2026-08-17), set after a selftest's cleanup
deleted a home directory (kata 5hs2). The test that did it was the deepest
meta-layer in the repo — a selftest of the selftest library, proving a
delete-guard by handing it live targets.

**No recursive delete takes an argument a caller could clobber.** Scratch
is minted by `scratch_dir <var> <prefix>` and reclaimed by the no-argument
`scratch_rm` (scripts/lib/scratch-lib.sh); a delete that cannot be handed a
path cannot be handed the wrong one. A second sanctioned pattern serves
the runners that keep scratch on failure or reclaim it across runs: the
directory is named `<prefix>.$$`, the trap is armed before `mkdir` so a
signal in the window abandons nothing, and the next run reclaims
leftovers whose pid suffix no longer answers `kill -0`
(scripts/lib/covscratch-lib.sh). Every remaining variable-fed recursive
delete in the repository's shell — scripts/ and the repo-root
install.sh — lives on `TestNoScriptFeedsVariableToRecursiveDelete`'s
count-pinned list with the reason it is allowed to exist. Some entries
are permanent by design (the two sanctioned patterns' own deletes); the
list grows only with an explicitly reviewed reason and shrinks as
scripts convert or retire.

Two parts of the rule the audit cannot hold, which is why it is written
here for people. Its scan is textual, so `find -exec rm -rf {} +` and
`xargs -I{} rm -rf {}` both read as deletes of the literal `{}`
placeholder rather than a variable, and slip through — the operand is
present and visibly not a variable, so neither is a shape the predicate
can refuse. A recursive delete piped a path with no placeholder at all —
the ordinary `| xargs rm -rf` shape, or a delete split across a
backslash-newline continuation — is instead refused outright: the
predicate cannot tell what it deletes, and treats that as unreadable
rather than silently passing it. And the count pin is narrower than it
sounds — it stops a file gaining an *extra* variable-fed delete and it
fails a listed file whose lines went away, but it cannot see one blessed
delete being swapped for a different one inside an already-listed file.
Read the deletes in any diff that touches a listed file.

**Hazards are banned by construction and enforced statically, never
proven by firing live weapons.** A test never contains a live destructive
command, even one asserted to be unreachable — an unreachable weapon is a
weapon plus a claim, and one typo or one mutation run collects on it. To
prove a guard stops before a delete, put a marker line where the delete
would be and assert the marker is never reached. Fixtures aim at seeded
decoy directories, never at `/`, `$HOME`, or anything a person would
miss. Where deletion itself is the behaviour under test, the delete
targets fixture-owned paths and sits on the audit's count-pinned list
with that reason. No shell suite holds such a line today: the last two
(run-module-lint-selftest's vanishing-scratch fakes) retired when that
runner ported to Go, where the equivalent tests remove only their own
`t.TempDir` scratch.

**Test depth caps at one meta-level.** Tools get selftests; the shared
libraries under them get one direct test file each; nothing tests the
tests. A library property is proven once, directly — that every consumer
actually goes through the library is a static audit's job, not a reason
to re-run consumers under sabotage.

**A selftest earns its gate slot by pinning outcomes of a tool the gate
or CI depends on.** Outcomes are exit codes, summaries, refusals, and
file effects; the argv a script hands a faked binary is an implementation
restated, and asserting on it is testing the mock. Fake-toolchain
selftests are banned outright, not merely denied a gate slot: a tool that
can only be tested by faking `go`, `gh`, or its sibling tools on PATH has
outgrown shell, and the move is the port (the dev-tooling spec's
port-on-touch trigger), never the suite. Feeding a data seam fixture
input — the registry listing fuzz-bisect's and fuzz-oracle-audit's suites
stub while the git history, replays, and verdicts stay real — is not a
faked toolchain. Hand-run conveniences
fail loudly in front of whoever ran them and get no suite. New tooling
that accumulates real logic belongs in Go under `go test`, where the type
system, `-race`, and ordinary unit tests replace an entire shell-fixture
harness; shell stays for glue.

## Canonical Gate Matrix

This table is the authoritative answer to which checks run when, what they
prove, what they require, and what counts as a failure. Test assertions remain
deterministic when live opt-ins are unset; dependency installation, disk
capacity, browser availability, and CI tool setup are explicit prerequisites.

| Gate and exact command | Scope | What it proves | Trigger | Determinism and external requirements | Failure or unavailable-tool behavior | Owner and follow-up |
| --- | --- | --- | --- | --- | --- | --- |
| <code>make lint</code> | Go lint, tagged compile floors, generated outputs, secrets | TOML naming, evenerfuzz/eval compile floors, internal-type check, golangci-lint for every non-fuzz module (struct-tag casing via tagliatelle, gofmt/goimports formatting, and library doc comments via revive's exported rule), generated AppWire outputs, the fuzz-target registry (scripts/fuzz/fuzz-targets.txt matches AST-discovered native/Rapid declarations), and the repo secret scan | Local pre-merge; required CI | No provider calls or model behavior; needs Go and golangci-lint. Local gitleaks absence warns and returns zero; CI sets EVENER_GITLEAKS_REQUIRED=1 | Any lint family or generated-output diff is nonzero. Missing golangci-lint is not-checked and nonzero; required gitleaks absence is nonzero | Evener CI/tooling; no new follow-up currently |
| <code>make build</code> (same runtime target as <code>make build-runtime</code>) | Runtime Go binaries plus embedded frontend | build-web completes before the evener/evener-hub pair is built, so the runtime pair contains the fresh SPA | Local pre-merge; required CI together with <code>make build-go</code> | Needs Go, Node/npm, the frontend install, and enough disk. Build metadata includes the current SHA, dirty state, time, and channel. Runtime Go builds use a disposable process home while preserving the caller's Go caches and copied go env settings | Frontend preflight, build, or pair-script failure is nonzero; stale/failed embedding is not a pass | Evener CI/build; release wiring follows make dist |
| <code>make build-go</code> | Every non-fuzz Go workspace module | Compiles all packages in the seven modules listed by <code>GO_MODULES</code>, including packages that root-level <code>go build ./...</code> does not visit under <code>go.work</code> | Required CI build job; local compile diagnostic | Deterministic Go compilation; no provider calls or frontend/browser requirements | Any module or package compilation failure is nonzero; the loop stops at the first failing module | Evener CI/build; no new follow-up currently |
| <code>make build-web</code> | Frontend build | TypeScript typecheck and Vite production build complete and refresh frontend/dist for Go embedding | Frontend CI; prerequisite of runtime/release builds | Needs Node/npm and may run npm ci when the install is absent or stale; no provider credentials. Node's automatic compile cache is disabled for preflight and build processes | npm, typecheck, or Vite failure is nonzero | Frontend CI; no new follow-up currently |
| <code>make test</code> | Non-fuzz Go modules and frontend | Root short-mode tests, other module tests, and frontend typecheck/Vitest/Biome. Every stream receives distinct private process-home, temporary, and XDG roots; Go streams also receive copied go env settings | Local quick check; included by the merge gate | Uses scripted/fake external boundaries for default tests. Existing Go build/module caches remain reusable outside the disposable roots. web-preflight may install dependencies. Runs ZERO fuzz-family tests, even at reduced depth — see <code>make test-fuzz</code> | Any module, frontend stream, or setup failure is nonzero. A live opt-in in the environment intentionally changes the scope. A failed or interrupted run retains and prints its log directory, including stream scratch | Evener CI/tooling and frontend; no new follow-up currently |
| <code>ROOT_FULL=1 make test</code> | Full intended non-fuzz root suite plus all non-root modules and frontend | Removes root -short mode while retaining the non-fuzz Test/Example name filter and explicit fuzz-owned exclusions; preserves the complete non-fuzz post-merge surface | Local pre-merge/post-merge; required CI equivalent is <code>ROOT_FULL=1 WEB=0 make test</code> because the web job owns test-web | Same requirements as make test; ROOT_FULL=1 does not enable providers or fuzz search | Any root/module failure is nonzero; skipped live tests remain explicitly skipped unless opted in | Evener CI/tooling; no new follow-up currently |
| <code>make test-fuzz</code> | The seqfuzz/schemafuzz stateful <code>rapid.Check</code> family (delegate, watch, lifecycle, jobs descendant-merge, tool-args schema, jobstore, two context-compaction surfaces, appserver router, appserver multi-session) | Each surface's rapid state machine runs its full default check count (no <code>-short</code> reduction), catching sequence bugs the focused unit suites cannot | Local pre-merge/post-merge for these surfaces; not run in CI's default `make test` job | <code>EVENER_FUZZ_TESTS=1</code> opts each test back in from its default <code>t.Skip</code>; no network, no provider calls; fully offline (deny exec env, fake clock, scripted adapters) | Any surface's oracle/invariant failure or panic is nonzero | Evener agent/fuzz tooling; no new follow-up currently |
| <code>make test-web</code> | Frontend typecheck, Vitest, and Biome lint | jsdom/unit-level frontend behavior, type safety, and source lint | Local pre-merge; required CI web job | Deterministic after Node dependencies are installed; each check owns a private process home plus temporary/XDG roots and disables Node's automatic compile cache; no real browser, provider, or network service | Any of the three streams is nonzero; missing/unhealthy frontend install fails preflight. Failure or interruption retains and names its owned evidence root | Frontend CI; no new follow-up currently |
| <code>make test-web-browser</code> | Frontend layout, overflow, and Spawn browser guards | Headless Chrome evaluates real CSS geometry, the real Session reducer/tree, and the real Spawn staging/breakpoint path | Required CI web job; local pre-merge on a Chrome-capable host | Make invokes each guard's Node entrypoint directly so interruption reaches the process that owns cleanup. Each guard receives private process-home, temporary, and XDG roots and disables Node's compile cache; Chrome uses a private profile, disables crash reporting, keeps Crashpad state beneath that profile, and uses a mock keychain for Darwin fresh profiles so network startup cannot block on ambient credentials. Cleanup first awaits Chrome's CDP <code>Browser.close</code>, then falls back through bounded TERM-to-KILL escalation when CDP fails or stays pending. On POSIX, Chrome and Vite run in detached process groups and cleanup removes the profile only after each exact group disappears; Win32 uses direct-child exit handling. macOS Crashpad escapes Chrome's process group, so cleanup captures before shutdown and rescans afterward for only the handler with the canonical random profile's exact database argument. The escaped helper is observation-only: cleanup waits for that exact identity within a bounded grace period and retains the private profile on failure rather than signaling a reusable numeric PID. Chrome/Chromium is required; no WebKit/Safari runner exists | Every guard runs. Green output contains only the three verdicts and removes the owned root. Any guard error, Vite failure, cleanup failure, or missing Chrome/Chromium is nonzero; failed guard logs are replayed and failed or interrupted roots are retained and named. Interruption waits for owned cleanup. WebKit/Safari is an explicit unsupported/manual gap, never a pass | Frontend/Evener CI; WebKit/Safari gap is a deliberate ceiling, not a follow-up (Jesse, 2026-08-07, kata 7tf6): a spike proved Playwright WebKit cannot diverge vh/dvh (no browser-chrome emulation exists in its API), and the iOS-simulator+safaridriver route needs sudo enablement plus WebDriver plumbing we chose not to carry. Dynamic-viewport enforcement is the CSS-text contract in layoutguard's mobile-shell-viewport-height case plus the AppShell.test.tsx source contract; true geometry verification stays manual/on-device |
| <code>make test-race</code> | Go non-fuzz modules under the race detector | Data races in the same non-fuzz module surface; frontend is intentionally not duplicated | Required CI; local diagnostic | Needs a race-capable Go toolchain and more CPU/memory; WEB=0, AGENT_SHARDS=0 | Any race report, test failure, or setup failure is nonzero; a slow or unavailable toolchain is a limitation/failure | Evener CI/tooling; no new follow-up currently |
| <code>make vet</code> | Go vet across all non-fuzz workspace modules | Go vet diagnostics for every module, independent of the tagged lint floors | Required CI; local diagnostic | Deterministic Go analysis; no provider calls | Any module vet failure is nonzero | Evener CI/tooling; no new follow-up currently |
| <code>make fuzz</code> | Tagged fuzz contracts, committed seed/crasher replay, Rapid replay, golden replay, and fuzz-tool packages | Fuzz invariants compile and execute, committed fuzz inputs remain safe, Rapid properties (including the seqfuzz/schemafuzz family that <code>make test-fuzz</code> also owns) replay under a fixed coverage seed bank, and decode goldens remain stable | Required CI deterministic corpus gate; local pre-merge when warranted | No fuzz search or provider calls; uses committed inputs and evenerfuzz tags; sets <code>EVENER_FUZZ_TESTS=1</code> so the seqfuzz/schemafuzz family's default skip does not swallow the replay; memory caps are best-effort by platform | Any compile, replay, invariant, Rapid, or golden failure is nonzero. Search campaigns belong to make fuzz-nightly, not this gate | Evener fuzz/tooling; no new follow-up currently |
| <code>make fuzz-gap-check</code> | Static decode/parse fuzz-target coverage | Every discovered decode/parse package has a registered fuzz target or an explicit ignore | Required CI; local quick check | Seconds, deterministic, no network or corpus replay | An uncovered package or registry/tool failure is nonzero | Evener fuzz/tooling; no new follow-up currently |
| <code>make fuzz-corpus-scan</code> | Gitleaks over committed fuzz corpora | Fuzz seeds do not contain secrets | Required CI; local harvester feedback | Needs gitleaks for a meaningful scan; local absence warns and returns zero unless EVENER_GITLEAKS_REQUIRED=1 | A finding or required-tool absence is nonzero; a local warning is an explicit limitation, not evidence of a scan | Evener security/tooling; no new follow-up currently |
| <code>make test-dev-tooling</code> | The scripts/*-selftest.sh suites that pin evener's own dev tooling | Each suite is the only thing pinning its script's contract; a suite that leaves anything in its private TMPDIR after passing fails, which is what enforces suite cleanup | Final step of <code>make merge-approval-gate</code>, and on demand; not part of <code>make test</code> because these suites test tooling, not the product | Each suite is offline and deterministic; the wave runner (<code>cmd/evener-test-dev-tooling</code>) gives every suite its own process group and private TMPDIR, is quiet on success, and replays a failing suite's whole log | Any suite exit nonzero, or a passing suite leaving files behind, is nonzero | Evener CI/tooling; no new follow-up currently |
| <code>make merge-approval-gate</code> | Serial local composition of lint, runtime build, full deterministic test, and the dev-tooling self-test wave | The canonical local/post-merge contract: make lint, make build, ROOT_FULL=1 make test, then make test-dev-tooling | Local pre-merge/post-merge; CI keeps equivalent checks in separate named jobs | Does not run fuzz search, race testing, provider calls, or browser guards; those have separate owners | The first failing phase stops the gate and returns nonzero; do not infer a verdict from partial logs. Live/e2e tests self-probe their host capabilities and skip individually on restricted hosts (internal/e2ecap) | Evener CI/tooling; no new follow-up currently |
| <code>make dist DIST_GOOS=... DIST_GOARCH=...</code> | Release/distribution binaries | The archive contains evener, evener-hub, evener-tui, and evener-doctor built for the requested target with a fresh SPA | Release/snapshot CI; manual distribution verification | Cross-compilation and frontend dependencies; release CI has networked setup for tool/dependency installation | Any build, archive, inspection, checksum, or upload failure is nonzero; unavailable release tooling blocks release | Release engineering; no Evener launcher work is implied |
| <code>scripts/web/web-preflight.sh</code> | Frontend dependency/setup health | The worktree has a lockfile-compatible install and a real local TypeScript compiler | Setup prerequisite for web/build/browser gates | May access npm when a real install is missing/stale; refuses unsafe npm ci through a mismatched shared symlink | Missing, mismatched, or unhealthy install is nonzero; npm/network unavailability is a setup failure | Worktree/frontend tooling; shared install management stays outside Evener |
| <code>make coverage-floor</code> (<code>CHECK=1</code> to gate, <code>BLESS=1</code> to raise) | Per-module Go statement coverage reached by ANY deterministic test (test track unioned with the deterministic fuzz-seed replay), plus the frontend's vitest line coverage | How much of each module is exercised at all, ratcheted against <code>scripts/coverage/coverage-floors.txt</code>. Neither track alone is honest: the gate's <code>-run '^(Test|Example)'</code> filter excludes every fuzz target, and whole families of behavioural checks live in <code>check*</code> functions only a evenerfuzz-tagged "program" target calls | Local/on-demand; not required CI (heavier than <code>make test</code>) | Deterministic; no provider calls. The fuzz half replays committed seed corpora only (<code>go test</code> without <code>-fuzz</code>), never a search | A row below its floor beyond the tolerance band is nonzero; a floored row that cannot be measured fails loudly rather than skipping | Evener CI/tooling; no new follow-up currently |
| <code>make test-timing-budget</code> (<code>CHECK=1</code> to enforce) / <code>make test-rebaseline</code> to reset | Per-Go-package test wall time, plus one aggregate for the frontend, against <code>testing-budget.json</code> | A timing regression does not silently erode the wins <code>docs/superpowers/specs/2026-08-01-test-gate-runtime-design.md</code> recorded: fail at 1.5x the checked-in budget, warn at 1.1x, plus a flat per-test ceiling regardless of package (<code>perTestCeilingSeconds</code> in that file, currently 3s) (kata b6rv) | Local/on-demand; not required CI — deliberately NOT part of <code>make merge-approval-gate</code>, because measuring durations means a second full non-fuzz <code>go test -json</code> run, which would double the very gate runtime this ratchet exists to protect. Wiring real enforcement into CI needs the durations sourced from the gate's own run instead of a second one, which is future work, not this kata's | Deterministic; no provider calls. Reuses <code>gate-surface-lib.sh</code>, so it measures the same surface <code>ROOT_FULL=1 make test</code> proves | A package over 1.5x its budget or any per-test ceiling breach is nonzero, but only under <code>CHECK=1</code> in a CI-shaped environment (<code>$CI</code> set, or <code>--strict</code>); a local run only warns. A missing or empty <code>testing-budget.json</code> is an explicit warn state — <code>CHECK=1</code> always exits zero until <code>make test-rebaseline</code> lands a measured baseline. The first clean-host baseline (121 packages) is checked in, so ratios are enforced under <code>CHECK=1</code> in a CI-shaped environment | Evener CI/tooling; deciding how to wire enforcement without duplicating the gate's own test run is an open follow-up (kata b6rv) |
| <code>EVENER_LIVE_TESTS=1</code> (umbrella opt-in)<br><code>EVENER_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v</code><br><code>EVENER_OPENAI_CODEX_E2E=1 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v</code><br><code>EVENER_ANTHROPIC_E2E=1 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v</code> | Provider/live/e2e | Real MCP and provider wire/API behavior, credentials, and model/provider contracts | Explicit manual/nightly opt-in; never default CI; <code>EVENER_LIVE_TESTS=1</code> also enables applicable live suites | Requires the named opt-in plus the corresponding tool, credentials, model access, and network; provider keys alone do not enable it | Tests without opt-in skip explicitly. With opt-in, configuration/API failures are nonzero; unavailable optional tools or credentials must be reported as skips/limitations, not passes | Provider owners; no default-gate follow-up |
| <code>EVENER_E2E_LIVE=1 scripts/coverage/e2e-cover.sh --merge-unit</code><br><code>EVENER_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -count=1 -v</code> | Live service coverage and host sandbox parity | Exercises real binaries/services or the host Seatbelt backend beyond deterministic unit coverage | Manual/platform-specific; not required CI | Needs provider/network services for live scenario scripts or macOS Seatbelt; EVENER_E2E_LIVE is not a correctness gate because the coverage script intentionally continues past scenario failures | Missing platform/service is a limitation; live scenario failures must be read from script output rather than treated as coverage success | E2E/sandbox owners; hardening the coverage script is a separate follow-up |
| Launcher health checks, managed-service restart, SDD/Kata semantics | Operational/external workflow | None are Evener-owned gate proofs in the current Makefile or workflows | Outside this repository's gates | Owned by the launcher, worktree manager, or SDD/Kata tooling | Do not add or silently imply these checks in Evener CI | Launcher/worktree manager/SDD owners; outside this change |

## Post-Merge Gate

Run the canonical local gate from the repository root:

~~~sh
make merge-approval-gate
~~~

For diagnosis and evidence, that target expands serially to:

~~~sh
make lint
make build
ROOT_FULL=1 make test
make test-dev-tooling
~~~

Live/e2e tests police their own requirements: the cmd/evener-hub
`TestE2E_*` and cmd/evener-tui `TestTUITmuxE2E_*` families call
`internal/e2ecap`'s `RequireLoopbackBind`/`RequireProcessInspect`, which probe
the capability at test time and `t.Skip` with the probe's reason when the host
lacks it. On an unrestricted host nothing skips and the gate's coverage and
exit code are unchanged; on a sandboxed host the infeasible tests skip
individually instead of failing into a denied bind.

ROOT_FULL=1 makes the protected first wave remove root's ordinary -short mode
while retaining the non-fuzz Test/Example name filter.
The runner still excludes the explicitly fuzz-owned sanity functions; their
deterministic replay is part of make fuzz. The retired standalone go test ./...
also ran ordinary tests in cmd/evener-fuzzcov and cmd/evener-fuzz-harvest; all fuzz
coverage, including those tests and the excluded root fuzz-tool packages, is
explicitly owned and run by make fuzz. Ordinary make test remains the default
local command and keeps the root wave in short mode unless ROOT_FULL=1 is
explicitly set. The CI web job runs make test-web, make build-web, and
make test-web-browser; the Go job runs ROOT_FULL=1 WEB=0 make test so frontend
tests are not duplicated.

The `make test` runner gives every Go module and frontend stream a distinct
private `HOME` plus temporary and XDG roots beneath its per-run log directory.
For Go streams, the runner copies ambient GOENV settings into that owned root
and preserves configured or platform-default GOPATH/GOCACHE locations so
ordinary reusable module and build caches stay warm. Frontend commands disable
Node's automatic compile cache. After every child process
exits successfully, a green run removes the complete per-run directory and its
process-lifetime scratch. This process-exit cleanup is not a session-end policy:
it does not delete scratch that a resumable live application still owns. A
failed or interrupted run instead retains the directory and prints its path so
the evidence that produced the failure remains available. Standard reusable
caches outside the owned roots are audited separately rather than claimed as
temporary cleanup.

make test-dev-tooling runs the scripts/*-selftest.sh suites that pin evener's
own tooling (cmd/evener-test-dev-tooling). They test tooling, not the product,
so they run here — where tooling regressions matter — and on demand, not
inside every inner-loop make test.

The matrix intentionally does not make browser guards part of make lint or
make test: those default gates remain usable without Chrome, while CI still
requires the browser-specific gate in its web job.

### Frontend setup boundary

`make test-web` and `make test-web-browser` are deterministic after the
frontend dependencies are installed. Establishing that install may require
npm/network access or an existing compatible worktree install, so an
unavailable setup is a reported prerequisite failure rather than a
deterministic test pass.

The frontend unit gate caps Vitest at four workers because `make test` runs it
beside the Go test waves. Vitest's host-sized default pool oversubscribed a
10-core host under that combined load and starved otherwise causal IndexedDB
mutation completions past their test deadlines. The fixed upper bound leaves
capacity for the sibling streams; it does not widen a timeout or replace an
awaitable completion with polling.
Vitest file isolation prevents worker-count or file assignment from sharing
module stores, panes, or mocks; per-file teardown is still required for timers,
clients, and listeners.

### Whole-system residue audit

On 2026-08-08, Apple container 1.2.2 ran a cold and then warm
`make build && make test` cycle in one disposable arm64 container from a
committed-only source archive. The audit image used
`golang:1.26.5-bookworm` and `node:26.5.0-bookworm`; every filesystem snapshot
recorded path metadata and every file hash, excluding only the virtual kernel
filesystems `/proc`, `/sys`, `/dev`, and `/run`.

The cold cycle added 31,792 paths, all beneath these exact declared output and
reusable-cache roots:

```text
/work/evener/evener
/work/evener/evener-hub
/work/evener/cmd/evener-hub/frontend/dist
/work/evener/cmd/evener-hub/frontend/node_modules
/root/.cache/go-build
/root/.npm
/go/pkg/mod
```

The warm comparison added 11 paths and changed 127 existing file hashes, all
inside those roots. It removed no path, changed no path metadata, and found
zero undeclared per-run paths. Before, after cold, and after warm, persistent
processes were only the container init and its `sleep infinity` child (plus the
ephemeral snapshot command); no listener or Unix socket remained. Explicit
runtime-home scans also found no Chrome, Chromium, Crashpad, Vite, or browser
profile residue.

Scratch created by the build and default test streams is owned by the enclosing
test process: each stream receives private home, temporary, and XDG roots, and
the runner removes those roots only after its children exit. Browser profiles
belong to the browser-guard application lifecycle and are removed only after
the exact Chrome/Vite owners exit; failed cleanup retains that evidence. No
resumable Evener session owned anything removed in this audit: the cycle launched
no resumable application session, and continuation state created by tests lived
inside their process-owned state root. Apple container is diagnostic evidence
for this contract, not a local or CI gate dependency.

## The seqfuzz/schemafuzz Family Lives Only in `make test-fuzz`

A Jesse ruling: no fuzz-family test — including a smoke-depth iteration —
belongs in `make test`, `go test ./...`, or any of their variants. This is a
different exclusion from the `evenerfuzz`-tagged native `FuzzXxx` targets and
`seed100`-style edge suites, which never compile into a default build because
of their build tag. The tests this ruling targets are ordinary
`func TestXxx(t *testing.T)` functions that call `rapid.Check` to run a
stateful/sequence property fuzzer — no build tag hides them, so a plain
`go test ./agent` used to run them, at whatever depth `rapid.Check`'s own
`testing.Short()` awareness picked.

Each test in the family is now individually gated at the top of its body:

```go
func TestDelegateSeqFuzz(t *testing.T) {
	if os.Getenv("EVENER_FUZZ_TESTS") != "1" {
		t.Skip("fuzz: skipped by default; run `make test-fuzz`, or EVENER_FUZZ_TESTS=1 go test ./agent -run TestDelegateSeqFuzz -count=1 -v")
	}
	...
```

The gate is an env-var check rather than reusing `-short`, precisely so that
`go test ./agent` with no flags stays fuzz-free too — matching the ruling's
spirit rather than only its `-short` letter. The gate is the first statement
in every one of these functions, including the two (`TestDelegateSeqFuzz`,
`TestLifecycleSeqFuzz`) that resolve `fuzz/promoter.PersistPaths` before
deciding whether to call `t.Parallel()`: the skip must fire before any of that
path resolution or parallelism decision runs.

The family, by file and function:

| File | Function |
| --- | --- |
| `agent/delegate_seqfuzz_test.go` | `TestDelegateSeqFuzz` |
| `agent/watch_seqfuzz_test.go` | `TestWatchSeqFuzz` |
| `agent/lifecycle_seqfuzz_test.go` | `TestLifecycleSeqFuzz` |
| `agent/jobs_fc2_seqfuzz_test.go` | `TestJobsFc2DescendantMergeSeqFuzz` |
| `agent/registry_schemafuzz_test.go` | `TestToolArgsSchemaFuzz` |
| `agent/internal/contextmgr/compaction_seqfuzz_test.go` | `TestCompactionSeqFuzz` |
| `agent/internal/contextmgr/maybecompact_fc1_seqfuzz_test.go` | `TestFc1MaybeCompactSeqFuzz` |
| `agent/internal/jobstore/seqfuzz_test.go` | `TestJobstoreSeqFuzz` |
| `internal/appserver/router_seqfuzz_test.go` | `TestRouterSeqFuzz` |
| `internal/appserver/hub_multisession_seqfuzz_test.go` | `TestHubMultiSessionSeqFuzz` |

A test that merely has "fuzz" in its name is not automatically in this family
— `TestDelegateSeqFuzzReplayClean`, `TestToolArgsAdapter_PromotesDeterministicFailure`,
and the other promoter-adapter tests alongside these files replay one fixed,
deterministic artifact (or a synthetic failure) rather than running a
`rapid.Check` search; they stay in the default suite because they are fast and
their coverage is otherwise lost. The `job_delegate_seed100_fuzz_test.go` file
and its siblings named for the same "seed100" convention are native `FuzzXxx`
targets under the `evenerfuzz` build tag — already excluded from every default
build, not part of this change.

Three other entry points drive this same rapid family and needed
`EVENER_FUZZ_TESTS=1` threaded through so this ruling would not silently blind
them: `make fuzz`'s fixed-seed rapid replay loop (`scripts/fuzz/run-fuzz.sh`'s
`rapid` case, used by `fuzz-nightly`/`fuzz-triage`/`fuzz-continuous` too) and
`scripts/fuzz/fuzz-oracle-audit.sh`'s `run_seeds`, which replays a mutation against
`TestJobstoreSeqFuzz` and must never read that test's now-default skip as the
oracle failing to catch the mutation.

The default gate's coverage number and the fuzz family's coverage are measured
separately, by design. `go test ./agent -short`'s `-cover` output measures only
the imperative test suite — the seqfuzz/schemafuzz family t.Skip()s there.
`make coverage-floor` answers the honest "how much is exercised at all" number:
per module it unions the test track with the deterministic native seed-corpus
replay (the rapid family is env-gated and not part of either track). Do not read
a default-gate coverage number as "whole-repo coverage including fuzz" — it
never was, and now it's explicit.

The corollary runs the other way too, and it is the one that misleads: a
default-gate number is not "how much of this package is tested". Several
packages keep whole families of behavioural checks in `check*` functions that
only a native *program* fuzz target calls — `FuzzLaunchConfigBehaviorProgram`
invokes 98 of them. These `Fuzz*BehaviorProgram` targets carry no `evenerfuzz`
build tag; what excludes them from the test track is the same
`-run '^(Test|Example)'` filter (`scripts/lib/gate-surface-lib.sh`'s
`GATE_TEST_RUN`) that excludes every other `FuzzXxx` name, so the test track
cannot see that work at all: `cmd/evener-hub/internal/appsource` reads 66.4%
there and 83.1% under its own program target, and four modules that look
incomplete on both tracks separately are in fact fully covered.

So before concluding a package is under-tested — and certainly before writing a
test to raise its number — read `make coverage-floor`, which unions the two
tracks. The test you were about to write may already exist under the other build
tag.

`cmd/evener-hub/cov_*_test.go` pulls the union number in the other direction.
(The `cov_`-prefixed name is not the marker — some, like
`agent/execenv/cov_s4_local_test.go`, are ordinary untagged `TestXxx`
suites. The marker is the shape below: a `FuzzXxx` target, not a `TestXxx`
function — the `//go:build evenerfuzz` tag is not part of it, and most of
`cmd/evener-hub`'s cov_* files carry no such tag, including
`cov_auth_instances_fuzz_test.go` below. Its own seed shape isn't universal
either: it ignores a single seed byte, `f.Add(byte(0))`, but several other
cov_* files seed multiple bytes that select between behaviors.) Each of
`cmd/evener-hub`'s cov_* files is a deterministic replay matrix that calls
production functions and discards most results (`_ = f(x)`). Their oracle is
real — a panic or a `-race` failure still fails the build — but thin: a call
site with no assertion cannot fail on a wrong answer, only a crash. So
statements these files reach count as EXECUTED toward coverage-floor, not
TESTED — read the number that way for `cmd/evener-hub`, where they are a large
share of the fuzz track. Upgrading a call site's target from panic-net to an
assertion against an independently-written literal (see
`cov_auth_instances_fuzz_test.go`) turns EXECUTED into TESTED for that call
site; it is not required for the rest of the file to keep earning its lines.

## Proving a Type Survives a Round Trip

When two code paths must agree about a struct — a decoder and a
projector, a live path and a reload path — a hand-written fixture proves
only that today's fields survive. A field added next month passes,
because nothing in the test knows it exists.

Build the fixture by walking the **type** with reflection instead. Fill
every field with a distinguishable value, decode the same bytes through
both paths, and report divergent fields by name.

`agent/session_client_mutation_doctor_drift_test.go` is the worked
example. `clientMutationSnapshot` is unexported, so evener-doctor's
mutations reader mirrors the persisted shape and decodes it with
`DisallowUnknownFields`; the test walks the snapshot type with
reflection and marshals it the way the save path does, so a field the
mirror has never heard of makes the doctor refuse the store and name it.
Verified by adding a synthetic field to `clientMutationSnapshot` and
watching the test name it unprompted, with no test edit.

The gotchas in a fixture builder are all in the leaves. This example
demonstrates two: `json.RawMessage` must contain valid JSON, and any
other `[]byte` marshals as base64. Two more are general technique that
this particular type never exercises, because `clientMutationSnapshot`
carries no `time.Time`, no float and no `any` — a `time.Time` needs whole
seconds wherever the encoding is RFC 3339, and floats must be integral to
survive widening through `any`. The last gotcha is the structural one: an
unhandled `reflect.Kind` must fail loudly rather than skip, because a
builder that silently skips a kind is a test that silently stops covering
it. `fillEveryField` has no `reflect.Interface` case, so an `any` field
added to the snapshot tomorrow stops the test rather than going quietly
untested.

Two corollaries:

- **Always prove it by mutation.** Route one path through a struct that
  omits a field and confirm the test names that field. A drift test that
  has never failed is a decoration.
- **Delete the round-trip test when the second path dies.** Once a
  mirror type is gone, both sides of its round-trip test are the same
  `json.Unmarshal` and it cannot fail. Keeping it leaves a comment
  claiming coverage that no longer exists.

## The Three Browser Guards, and Why There Are Three

jsdom evaluates no cascade and reports zero for every box, so an entire class
of frontend defect is structurally invisible to `vitest`. Three checks in
`cmd/evener-hub/frontend` cover it, and the split matters:

- **`npm run layoutguard`** measures HAND-AUTHORED markup against the real
  `tokens.css` and component stylesheets, in headless Chrome. Cheap — static
  files, no build. Right for "does this CSS rule still hold its box".
- **`npm run overflowguard`** renders the REAL Session pane through the REAL
  reducer and asserts nothing inside it scrolls sideways, at four widths.
- **`npm run spawnguard`** renders the real Spawn pane through its staging and
  breakpoint path, asserting the responsive form remains usable at three widths.

The first two checks cover static geometry and the Session pane; the third
covers the Spawn pane, which has a separate responsive layout and failure modes.
The second exists because the first could not have caught the bug that
prompted it. Hand-authored markup freezes whatever was current when the case
was written, so restoring the old glyph would have left the guard green while
the app broke. All three are owned by `make test-web-browser`, which is required
by the CI web job and remains separate from `make lint` and `make test` because
it needs Chrome.

Three traps, each of which produced a false-green here. They are listed in the
order they were found, because each was hiding the next.

**A fixture must use REGISTERED types.** The overflow harness seeded item types
`"thinking"` and `"notification"`. Neither is registered — the set is
`agentMessage / reasoning / steering / systemMessage / userMessage / warning`
(`grep registerItemRenderer`) — so both fell through to `RawItemView`, the
debug fallback. The guard measured a debug renderer for two of five items and
reported PASS. **An unregistered type does not throw; it silently renders
something else.**

**`scrollWidth > clientWidth` is not "scrollable".** It is true for any element
whose content exceeds its box, *including* one deliberately clipping with
`text-overflow: ellipsis` — the recommended fix for overflow, flagged as an
instance of it. Only computed `overflow-x: auto|scroll` puts a scrollbar under
a reader's finger. Which is also the CSS fact behind the original bug worth
knowing on its own: **`overflow-y: auto` with no `overflow-x` declared computes
`overflow-x` to `auto`, not `visible`** — so every such element is silently a
horizontal scroll container too.

**`display` on a `<details>` replaces the UA's skipped-contents mechanism.**
`display: flex` on a `<details>` keeps its non-summary children laid out while
COLLAPSED — a collapsed disclosure leaks a sliver of its body. Scope such rules
to `.details[open]`.

Fixing trap one immediately exposed trap two, which was hiding trap three, a
real regression in code committed minutes earlier. A guard is only worth its
run time once it has been mutation-tested: break the thing on purpose and
confirm it fails, naming the right element.

Do the break-and-restore with a path-scoped stash, never with `git checkout
--` on a file that carries your own uncommitted work — checkout discards
everything in the file, mutation and real edits alike (learned the hard way,
2026-08-09; the mistake was caught and repaired, but only by re-doing the
work):

```sh
git stash push -- path/to/file   # set your real work aside; file returns to HEAD
# revert the declaration, run the guard, confirm it fails for the right reason
git checkout -- path/to/file     # now safe: drops only the mutation, your work is in the stash
git stash pop                    # your work returns exactly as it was
```

`git stash pop` restores the most recent stash, so run the cycle straight
through without interleaving other stash operations.

Three more things about writing a layoutguard case, each learned by watching a
case pass when it should not have (katas hk8v, edhz):

- **A state a page script cannot reach needs `CSS.forcePseudoState`.** There is
  no way to synthesize a trusted hover, and a programmatic `.focus()` does
  **not** match `:focus-visible` — measured, the element stayed unmatched at
  opacity 0. A case declares `forcePseudoStates` in its `case.json` and the
  runner pins them before measuring. This proves the *cascade* applies the rule;
  whether Chrome's own heuristic calls a given focus "visible" is Chrome's
  contract, not ours. Put each state on its own copy of the markup in one
  harness so a single measurement covers all of them with a resting control.
- **Switch transitions off in the harness.** `getComputedStyle` reads the value
  at that instant, so a measure taken right after a state changes reads the
  *start* of a 120ms opacity ramp, not where the cascade settles. Waiting for
  `transitionend` instead hangs forever in exactly the regression the case
  exists to catch — no rule, so no transition, so no event.
- **A fixture can make a declaration unfalsifiable.** An `<img>` with no height
  still gets one from its intrinsic aspect ratio, so a *square* test image makes
  `height: 100%` redundant: deleting the declaration left the case green. The
  fixture is now 8x4. Include `styles/global.css` in any case that measures
  boxes — `box-sizing: border-box` lives there, and it is the difference between
  an 80px and an 82px tile.

Geometry also cannot see `object-fit`: dropping `object-fit: cover` leaves every
box identical and only changes how pixels are scaled inside the image box. Say
so in the case rather than letting a pass imply coverage it does not have.

## A Single `tmux capture-pane` Can Lie

`tmux capture-pane` returns tmux's OWN terminal-grid state, not a snapshot of
what the program last rendered. tmux updates that grid incrementally as bytes
arrive from the pty; `cmd/evener-tui` writes each frame through bubbletea's
default renderer as one unsynchronized ANSI byte stream — bubbletea v1.3.10
has no terminal synchronized-output-mode support at all (grep `standard_renderer.go`
for `2026`/`SyncUpdate`; nothing), and a single frame commonly runs several
KB, well past any platform's atomic-pipe-write guarantee. Under load (rapid
re-renders, CPU contention — kata nxq6's report: "shortly after a turn
started and notifications were arriving"), `capture-pane` can land while tmux
is still mid-write and read a pane that is blank above the last few lines,
with those last few lines already showing current content.

nxq6 investigated the alternative — a real partial-repaint bug, some update
path writing the composer without repainting the frame above it — first, and
ruled it out two ways before touching tmux at all:

- `hubModel.View()` composes the full frame synchronously from model state on
  every call (`cmd/evener-tui/hub_model.go`, `sessionView()` in
  `cmd/evener-tui/hub_session_view.go`); there is no code path that writes only
  the composer, and the session breadcrumb (`topBar`) is provably non-empty
  for any reachable state, so `tuiprim.AppShell.View()` cannot legitimately
  drop it while keeping the footer.
- The kata's own report is inconsistent with a `View()` bug on logical
  grounds: `View()` is a pure function of model state, so an inert keypress
  cannot change its return value — yet "any key... brought back... content
  that had not changed" is exactly what a render/transport-path bug looks
  like from the outside, and exactly what a `View()` bug cannot produce.

`cmd/evener-tui/hub_partial_repaint_nxq6_test.go` drives realistic notification
bursts through `hubModel.Update` directly (no terminal involved) and checks
that invariant after every step, both as a fixed scenario and as a fuzz
target; a mutation test (temporarily dropping `topBar` from `AppShell.View()`)
confirmed the check fails the way it should before the mutation was reverted.

**The fix**: never trust a lone `Capture()` (or a lone `capture-pane`) for a
negative assertion ("X is absent"). `WaitFor` in
`cmd/evener-tui/tmux_e2e_test.go` already retries until its wanted substrings
appear, which self-corrects for POSITIVE assertions the same way a capture
race self-heals — but the screen it returns is only guaranteed to contain
what it waited for, not to be a complete frame, so a
`strings.Contains(screen, unwanted)` check against that same screen can still
land mid-render. Use `(*tmuxTUI).CaptureStable()` instead: it polls until two
consecutive captures match. A torn frame cannot do that — it converges within
milliseconds — while a pane that is genuinely still changing (an active
stream) legitimately does not, so `CaptureStable` keeps polling rather than
settling on a stable-but-wrong frame. `TestTUITmuxE2E_CaptureStableDuringStream`
exercises it under a live rapid-notification burst.

For a check scripted OUTSIDE this harness — an agent driving `tmux
capture-pane` directly to verify TUI behavior, the scripted-verification
workflow this hazard cost real time in during e79v's verification (kata
nxq6's motivating example) — the same fix applies without the Go helper:

```sh
prev=$(tmux capture-pane -p -t "$SESSION")
sleep 0.02
cur=$(tmux capture-pane -p -t "$SESSION")
while [ "$prev" != "$cur" ]; do
  prev=$cur
  sleep 0.02
  cur=$(tmux capture-pane -p -t "$SESSION")
done
# $cur is now safe to grep for an absence.
```

A live repro under CPU-loaded, multi-session, wide-pane bursts (~7,000
captures across several attempts) never caught the pattern directly — tmux
usually wins this race on a healthy machine, which is consistent with the
above rather than a counter-example to it. Treat the mechanism as
evidence-backed, not directly observed.

The pty lies in the other direction too. **An e2e test must await a render
between consecutive printable command-mode key presses**: tmux coalesces
back-to-back `send-keys` writes into one pty read when the pane's reader
lags, bubbletea reports every printable rune of one read as a single
KeyMsg, and a command mode that matches on `msg.String()` reads "kkf" as
one unmatchable message instead of three keys (kata fazd — reproducible on
an idle machine with one `send-keys -l` write). The dashboard replays such
bursts rune by rune (`replayKeyBurst` in `cmd/evener-tui/hub_keys.go`), but
browse mode's batch-as-text behavior is pinned pending a design decision
(kata 7hh0), so its command sequences must never coalesce in the first
place: see `TestTUITmuxE2E_FailedForkPreservesDraft`, which awaits the
selection render after each `k`. Arrow and control keys are escape
sequences and immune; only printable runes batch.

## A Test That Never Runs

A test that does not execute is worse than a missing test: it reports the
coverage without providing it, and the suite stays green either way. Two shapes
in this repo produce one, and neither announces itself.

**Registered `check*` functions.** Several packages drive their behavioral
contracts through a fuzz entry point that replays one check selected by the fuzz
input — `FuzzFSPathsBehaviorProgram` and friends in `cmd/evener-hub/internal/`
(`fspaths`, `hostlock`, `hubedge`, `codexlaunch`, `launchconfig`). A
`check*(t *testing.T)` function in those packages runs **only** if it appears in
its `checks := []func(*testing.T){…}` seed table. Write one, forget the table
entry, and `go test` passes without ever calling it.

The reachability proxy is `golangci-lint run ./path/to/pkg/`, whose `unused`
linter reports the unregistered check as a dead function:

```
paths_test.go:411:6: func checkSanitizeDirPrefix_PreservesLoneTrailingDot is unused (unused)
```

`go vet` does **not** catch this — verified by unregistering a real check and
running both. Run the linter after adding a check, and state which table each
new check is registered in when handing work off.

**A shell selftest that redirects `TMPDIR`.** macOS `mktemp -t` resolves
against the per-user temp path (`confstr(_CS_DARWIN_USER_TEMP_DIR)`) and
ignores `TMPDIR`, so a selftest that sets `TMPDIR=$scratch` to observe a
script's temp handling observes nothing: the script writes to the real temp
dir, six assertions in run-module-lint-selftest.sh were unfalsifiable, and
every run littered the machine (found during kata cqne). Fake the `mktemp`
binary on `PATH` instead of faking the environment.

**A stylesheet assertion that matches its own comment.** A test that greps CSS
text (`expect(css).toContain("flex: none")`) will match the declaration quoted
in a doc comment above the rule. One of these passed with its implementation
deleted. Strip comments before matching:

```ts
const css = readFileSync(…, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
```

The general rule: **prove a new test can fail.** Break the thing it covers,
watch it go red, then put it back. A test you have only ever seen pass has not
been tested. Two corollaries, both from real incidents here:

- "No tests" is not "tests passed". A broken file makes vitest print
  `Tests no tests` next to a transform error; a grep for `Tests ` reads that as
  benign. Check exit codes, never a grep of piped output.
- Assert the mechanism, not a side effect a broken implementation also produces.
  An "onAdd called once" assertion passed with validation entirely removed,
  because committing the add unmounted the panel either way. Asserting the
  validate call itself distinguishes them.

**A fixture whose content nothing reads.** `read_file`'s PDF fixture was
`[]byte("pdf")` for years and every assertion around it passed, because the
document path examines its payload in exactly one place and it is not where
anyone looks. `execenv.detectDocumentFormat` decides on the FILENAME first
(`.pdf` wins outright) and only sniffs the five-byte `%PDF-` signature when the
extension does not claim a PDF; downstream, `tool.dispatchedResult` exempts
`application/pdf` from raster decoding entirely, so nothing past the environment
ever looks at the bytes again. That exemption is deliberate — a PDF is not
something `llm.RasterMediaType` can decode — and it is now pinned as a stated
contract rather than left to be rediscovered.

The consequence for anyone writing a case here: a document test named `*.pdf`
cannot be byte-sensitive, whatever you put in it. The only oracle that reads the
content is the same path with an extension that does NOT claim a PDF, run twice
against different bytes (`TestReadFile_RealPDF_DetectedByItsBytes`). The
exemption is also stated as a contract next to the fixture that used to imply
it, in `session_tools_core_exact_fuzz_test.go` — but that file is
`//go:build evenerfuzz`, so it is read by `make fuzz-seeds`, not `make test`.
Several untagged tests hold the same rule, so nothing is lost by the tag;
the statement is there for the reader, not for the gate. And beware
the size of the claim a passing case licenses: five bytes are checked, so
"handles a real PDF" means "accepts the signature", and a fixture is only
honestly a PDF if something proved it parses — `validPDFFixture` computes its
cross-reference offsets from the bytes as they are written and
`TestValidPDFFixtureCrossReferencesResolve` resolves them back. A previous
attempt pasted an opaque blob whose comment claimed validity while three of its
four offsets pointed at nothing; every `%PDF`/`xref`/`trailer`/`startxref`
marker was present, which is why grepping for markers is not validation.

## Real `git` in Worktree Tests

`git` is an external dependency like the LLM provider, and the same boundary rule
applies: keep it real when git's own behavior is the thing under test, and script
it when git is only a way to reach a Evener decision.

This matters more here than the rule alone suggests. A real-git worktree test
averages **~1.2s and ~14 `git` subprocesses**; the same test on the scripted
boundary runs in **~0.04s**. The `agent` package's real-git tests once accounted
for **48% of its total test-work** (303s of 630s) while being 9% of its tests —
almost all of it process spawn, not assertions.

### Which harness

Use the **real-git** harness (`newWorktreeRepo` and friends, in
`agent/session_tools_worktree_create_test.go`) when the assertion depends on
something only git can produce:

- real registry effects — `worktree add/remove/lock/unlock/prune` actually
  landing, `.git` pointer file contents, deregistration
- real ancestry or patch-equivalence — `merge-base --is-ancestor`, `git cherry`,
  merged/unmerged/adopted outcomes over real commits. The scripted model refuses
  these outright, so a converted test fails loudly rather than reading as merged.
- dirtiness from a *modified or staged tracked file*, real ahead-counts, and
  git's own refusal to remove a dirty worktree without `--force`. The scripted
  model derives dirtiness from untracked files it can see on disk, so an
  untracked-file assertion may stay scripted; it refuses to answer `rev-list
  --count` at all (like the ancestry verdicts above), so an ahead-count
  assertion belongs on real git.
- the real `--porcelain` output shape, including flags like `prunable` and a
  reasonless `worktree lock`
- git's ref rules — e.g. that it rejects the branch name `HEAD`
- symlink canonicalization against git's canonical registry path
- `ResolveMainRepoRoot`'s structural walk and its git-binary fallback
- concurrency that relies on git's own index/ref locking to serialize

Use the **scripted** boundary (`scriptedLaneRepo` in
`agent/worktree_scripted_lane_test.go`, or `scriptedWorktreeSession` in
`agent/session_tools_worktree_scripted_test.go`) when the subject is Evener's own
behavior:

- which validation or refusal rung fires, and its error text
- what event or warning was emitted, and how many times
- what Evener wrote to its own state — sidecars, jobstore records, disposed marks,
  gate flags, `SessionMeta`
- control flow — budget expiry, ordering, retries, "declined to touch"
- argument validation that returns before any git call

Both harnesses keep sidecars and `.git` pointer files as real files on disk. Only
the `git` subprocess boundary is replaced, via
`SessionConfig.testOnly.worktreeGitRunner`.

### The failure mode to avoid

`scriptedWorktreeGit` is a *semantic model* of git, not git. If you script a
behavior the model does not really implement, the test passes while proving
nothing.

A concrete example that was live in this repo: the model's
`check-ref-format --branch` arm hardcoded a rejection of `HEAD`, so a test whose
entire purpose was "real git rejects the reserved name HEAD" would have passed
against the fake regardless of git's actual rules. That hardcoding was removed
and the test stays on real git.

So: the model's unknown-argv arms return an `unsupported argv` error **on
purpose**. If a converted test reaches a command the model does not implement, it
fails loudly rather than silently passing. When that happens, either model the
command honestly or leave the test on real git — never stub the specific answer
the assertion is looking for.

`merge-base --is-ancestor` and `cherry` refuse for the same reason even though
the model recognizes them: their answer is a verdict over a commit graph the
model does not have, so any answer would be that stub.

### Adding a worktree test

Default to the scripted boundary. Reach for real git only when you can name the
git behavior the assertion depends on, and say so in the test's comment so the
next reader does not "optimize" it onto the fake.

## Seeding Hub Fixtures

A hub fixture's session and project identifiers are encodings, not names: a
session id is a 22-character base62 UUIDv7 payload, and a project directory is
`<readable>-<10 base62>`. A hand-written placeholder that looks plausible is
rejected, and `PastIndex.Rebuild` then leaves the seeded session out of the
index — so the fixture is invisible rather than wrong, and the test failure
points nowhere near the id.

Mint them with `cmd/evener-hub/internal/hubtest` instead of writing them out:

```go
sessionID := hubtest.SessionID(t)            // e.g. 02wMz5Txv1C3Hut0M8GCeB
projectDir := hubtest.ProjectID(t, "alpha")  // alpha-0123456789
```

When the fixture has a real checkout on disk, use `identifier.ResolveProject`
instead — the hub cross-checks a project's id against its working directory,
and only the resolved id matches.

Rebuild now names what it refused to index and why, so a seeding mistake that
does slip through shows up on the hub's stderr:

```
[hub] past index: skipped /…/projects/alpha-0123456789/sessions/placeholder.meta.json: invalid session id (want a 22-character base62 UUIDv7 payload): invalid UUID payload
```

## A Disposable Hub Needs Its Own HOME

The hub is a host singleton guarded by an exclusive flock on
`$XDG_STATE_HOME/evener/hub.lock` (`$HOME/.local/state/evener/hub.lock` when
`XDG_STATE_HOME` is unset), and that path is deliberately not independently
overridable (kata av1j): the lock, the run dir, the state root, and the auth
token all derive from `cmdutil.DefaultStateRoot()` (XDG_STATE_HOME, else
`os.UserHomeDir()`), so they only stay coherent when they move **together**.
The blessed way to run a second, disposable hub — an e2e harness, a scratch
verification hub — is a fresh HOME:

```sh
HOME=$(mktemp -d) ./evener-hub -addr 127.0.0.1:0 -evener ./evener
```

Never point a test hub at the real HOME "just for a quick check": if the
real hub happens to be running you collide (the flock error names the
lock file it lost); if it happens to be **stopped**, the test hub
silently claims the real `~/.local/state/evener` — the dangerous case,
because nothing fails. A lock-path-only override was considered and
rejected for exactly that reason: it would unbundle the singleton guard
from the state it guards.

## A Live Run Uses the Machine's Build Cache

None of the live-test commands below carries a `GOCACHE=` prefix, and adding
one is a regression. A per-command cache under `/tmp` duplicates the
machine's warm cache on the checkout's volume, which has filled to 100%
twice mid-fleet-run (kata 98x9); one stray `/tmp/evener-gocache` grew from
1.3G to 2.9G in an hour. There
is nothing to isolate, either: the build cache is content-addressed, so a
live run and the default suite cannot corrupt each other's entries. If the
cache's volume stalls, nothing here catches it ahead of time (kata r07s's
`--check` diagnosis was retired with disk-reclaim.sh); a stalled external
volume now surfaces as an ordinary hung `go` command, same as any other
unmounted-volume failure.

## MCP Server E2E

The MCP manager has opt-in live tests against `npx -y
@modelcontextprotocol/server-everything`. They are intentionally not part of the
default suite because they depend on an ambient Node/npm toolchain and may fetch
or use cached packages outside the repository.

Run:

```sh
EVENER_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
```

`EVENER_LIVE_TESTS=1` also enables these tests with the other live test suites.

## Environment Variable Tests

Supported runtime environment variables are defined in the `envvars` package
and documented in `docs/environment.md`. Production code, help text, and test
helpers should use those rows instead of hard-coded env names.

When adding a runtime env var:

- Add one `envvars.Var` row.
- Use the row's `Name`, `Getenv`, `LookupEnv`, `Trimmed`, or `Assignment`
  helper at call sites.
- Document it in `docs/environment.md`.
- Keep live-test opt-in gates explicit; a provider credential alone must not
  make a default test issue network requests.

## OpenAI Codex Backend E2E

The OpenAI adapter has opt-in live tests for the ChatGPT/Codex Responses backend.
They are intentionally not part of normal CI because they require stored OpenAI
OAuth credentials and make live requests to `https://chatgpt.com/backend-api/codex/responses`.

Run:

```sh
EVENER_OPENAI_CODEX_E2E=1 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Optional model override:

```sh
EVENER_OPENAI_CODEX_E2E=1 EVENER_OPENAI_CODEX_E2E_MODEL=gpt-5.4 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Prerequisites:

- `evener openai login` has completed and stored OAuth credentials.
- The active account can use the Codex backend.
- Network access to `chatgpt.com` is available.

The suite currently checks:

- OpenAI env resolution uses the stored OAuth/Codex transport.
- Requests hit `/backend-api/codex/responses`.
- Codex session metadata fields can be sent:
  - `prompt_cache_key`
  - `session-id`
  - `thread-id`
  - `x-client-request-id`
  - `client_metadata` installation ID
- Reasoning requests ask for `reasoning.encrypted_content`.
- Tool-call replay with preserved assistant messages still works.
- Selected public Responses API controls are accepted or explicitly reported as unsupported by the Codex backend.

Observed live result on 2026-05-21:

- The Codex backend accepted the transport/session metadata path.
- The Codex backend accepted explicit `store:false`.
- The Codex backend rejected these public Responses parameters:
  - `safety_identifier`
  - `prompt_cache_retention`
  - `truncation`
  - `max_tool_calls`
  - `background`
- The Codex backend rejected `service_tier:auto` with `Unsupported service_tier: auto`.
- For low-effort `gpt-5.4` prompts tested, responses contained `reasoning.effort` in the raw response but did not include an output `reasoning.encrypted_content` item. The adapter still supports encrypted reasoning round-trip when the backend returns that item, covered by unit tests.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.

## Anthropic Messages API E2E

The Anthropic adapter has opt-in live tests for the Anthropic Messages API. They
are intentionally not part of normal CI because they require `ANTHROPIC_API_KEY`
and make live requests to `https://api.anthropic.com/v1/messages`.

Run:

```sh
EVENER_ANTHROPIC_E2E=1 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Optional model override:

```sh
EVENER_ANTHROPIC_E2E=1 EVENER_ANTHROPIC_E2E_MODEL=claude-sonnet-4-5-20250929 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Prerequisites:

- `ANTHROPIC_API_KEY` is set.
- The active account can use the selected model.
- Network access to `api.anthropic.com` is available.

The suite currently checks:

- Requests hit `/v1/messages`.
- `service_tier: "standard_only"` is serialized and accepted.
- Automatic prompt caching request shape remains enabled through top-level `cache_control`.
- Extended thinking requests work when the selected model emits thinking blocks; returned thinking is replayed into the next request.
- Tool use and tool-result replay work across turns.

Docs-backed behaviors covered by unit tests and this live suite:

- Anthropic documents `service_tier` values `auto` and `standard_only`, and reports the assigned tier in `usage.service_tier`.
- Anthropic documents automatic prompt caching through top-level `cache_control`.
- Anthropic documents `thinking` and `redacted_thinking` blocks, including signatures/data that must be preserved when round-tripping tool-use conversations.

Observed live result on 2026-05-21:

- `service_tier: "standard_only"` was accepted.
- The live transport/service-tier/cache-shape test passed against `api.anthropic.com`.
- The live extended-thinking replay test passed against the default e2e model.
- The live tool-use/tool-result replay test passed against the default e2e model.
- Short prompts may not report cache write/read activity because they do not cross cache thresholds; the test logs this instead of failing.
- Some prompts/models may not emit visible thinking blocks even when reasoning is requested; the test logs this instead of failing. Unit tests cover thinking/signature and redacted-thinking round-trip shapes.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.
