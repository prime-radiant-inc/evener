# Testing

## Test Reliability Policy

The default test suite must be deterministic. Running `make test` or
`go test ./...` must not depend on provider credentials, model availability,
network access, quota, current model behavior, wall-clock timing outside the
process, or ambient developer machine state.

Use this boundary when adding or fixing tests:

- If the test verifies Serf plumbing, use a scripted provider at the LLM
  boundary and exercise the real Serf code below it. Examples: CLI flag/config
  wiring, appwire RPC, daemon input queues, session loops, tool execution,
  transcript writes, event emission, goal continuation routing, hook dispatch,
  and prompt composition.
- If the test verifies model behavior, keep it live. Examples: whether a
  specific model chooses a tool from a natural-language instruction, follows an
  output contract, supports a provider feature, honors a live API wire shape, or
  behaves well across multi-turn goal prompts.
- Live tests must be explicitly opt-in with a `SERF_*_E2E=1` or
  `SERF_LIVE_TESTS=1` style environment variable in addition to the provider
  credential. A provider key by itself must never make the default suite issue
  live requests.
- Do not use sleeps, polling races, or large string snapshots to prove behavior
  when a structured event, state field, file result, or fake transport script can
  prove the same contract.
- Do not mock Serf internals to make a test pass. Keep the fake boundary at an
  external dependency: LLM provider, network server, filesystem root, clock, or
  process launcher.

When a test needs a model, name that as the behavior under test and keep it out
of the default suite. When the model is only a way to drive Serf, replace it with
a scripted `llm.ProviderAdapter` response and assert the Serf side effects.

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

## Canonical Gate Matrix

This table is the authoritative answer to which checks run when, what they
prove, what they require, and what counts as a failure. Test assertions remain
deterministic when live opt-ins are unset; dependency installation, disk
capacity, browser availability, and CI tool setup are explicit prerequisites.

| Gate and exact command | Scope | What it proves | Trigger | Determinism and external requirements | Failure or unavailable-tool behavior | Owner and follow-up |
| --- | --- | --- | --- | --- | --- | --- |
| <code>make lint</code> | Go lint, tagged compile floors, generated outputs, secrets | Naming, gofmt, serffuzz/eval compile floors, internal/docs checks, golangci-lint for every non-fuzz module, generated AppWire outputs, and the repo secret scan | Local pre-merge; required CI | No provider calls or model behavior; needs Go and golangci-lint. Local gitleaks absence warns and returns zero; CI sets SERF_GITLEAKS_REQUIRED=1 | Any lint family or generated-output diff is nonzero. Missing golangci-lint is not-checked and nonzero; required gitleaks absence is nonzero | Serf CI/tooling; no new follow-up currently |
| <code>make build</code> (same runtime target as <code>make build-runtime</code>) | Runtime Go binaries plus embedded frontend | build-web completes before the serf/serf-hub pair is built, so the runtime pair contains the fresh SPA | Local pre-merge; required CI together with <code>make build-go</code> | Needs Go, Node/npm, the frontend install, and enough disk. Build metadata includes the current SHA, dirty state, time, and channel | Frontend preflight, build, or pair-script failure is nonzero; stale/failed embedding is not a pass | Serf CI/build; release wiring follows make dist |
| <code>make build-go</code> | Every non-fuzz Go workspace module | Compiles all packages in the seven modules listed by <code>GO_MODULES</code>, including packages that root-level <code>go build ./...</code> does not visit under <code>go.work</code> | Required CI build job; local compile diagnostic | Deterministic Go compilation; no provider calls or frontend/browser requirements | Any module or package compilation failure is nonzero; the loop stops at the first failing module | Serf CI/build; no new follow-up currently |
| <code>make build-web</code> | Frontend build | TypeScript typecheck and Vite production build complete and refresh frontend/dist for Go embedding | Frontend CI; prerequisite of runtime/release builds | Needs Node/npm and may run npm ci when the install is absent or stale; no provider credentials | npm, typecheck, or Vite failure is nonzero | Frontend CI; no new follow-up currently |
| <code>make test</code> | Non-fuzz Go modules and frontend | Root short-mode tests, other module tests, and frontend typecheck/Vitest/Biome | Local quick check; included by the merge gate | Uses scripted/fake external boundaries for default tests. web-preflight may install dependencies. Runs ZERO fuzz-family tests, even at reduced depth — see <code>make test-fuzz</code> | Any module, frontend stream, or setup failure is nonzero. A live opt-in in the environment intentionally changes the scope | Serf CI/tooling and frontend; no new follow-up currently |
| <code>ROOT_FULL=1 make test</code> | Full intended non-fuzz root suite plus all non-root modules and frontend | Removes root -short mode while retaining the non-fuzz Test/Example name filter and explicit fuzz-owned exclusions; preserves the complete non-fuzz post-merge surface | Local pre-merge/post-merge; required CI equivalent is <code>ROOT_FULL=1 WEB=0 make test</code> because the web job owns test-web | Same requirements as make test; ROOT_FULL=1 does not enable providers or fuzz search | Any root/module failure is nonzero; skipped live tests remain explicitly skipped unless opted in | Serf CI/tooling; no new follow-up currently |
| <code>make test-fuzz</code> | The seqfuzz/schemafuzz stateful <code>rapid.Check</code> family (delegate, watch, lifecycle, jobs descendant-merge, tool-args schema, jobstore, two context-compaction surfaces, appserver router, appserver multi-session) | Each surface's rapid state machine runs its full default check count (no <code>-short</code> reduction), catching sequence bugs the focused unit suites cannot | Local pre-merge/post-merge for these surfaces; not run in CI's default `make test` job | <code>SERF_FUZZ_TESTS=1</code> opts each test back in from its default <code>t.Skip</code>; no network, no provider calls; fully offline (deny exec env, fake clock, scripted adapters) | Any surface's oracle/invariant failure or panic is nonzero | Serf agent/fuzz tooling; no new follow-up currently |
| <code>make test-web</code> | Frontend typecheck, Vitest, and Biome lint | jsdom/unit-level frontend behavior, type safety, and source lint | Local pre-merge; required CI web job | Deterministic after Node dependencies are installed; no real browser, provider, or network service | Any of the three streams is nonzero; missing/unhealthy frontend install fails preflight | Frontend CI; no new follow-up currently |
| <code>make test-web-browser</code> | Frontend layout, overflow, and Spawn browser guards | Headless Chrome evaluates real CSS geometry, the real Session reducer/tree, and the real Spawn staging/breakpoint path | Required CI web job; local pre-merge on a Chrome-capable host | Uses the three existing npm scripts and private Vite/Chrome profiles with OS-selected local ports; Chrome/Chromium is required; no WebKit/Safari runner exists | Every guard runs. Any guard error, Vite failure, or missing Chrome/Chromium is nonzero; WebKit/Safari is an explicit unsupported/manual gap, never a pass | Frontend/Serf CI; WebKit/Safari runner remains a follow-up gap |
| <code>make test-race</code> | Go non-fuzz modules under the race detector | Data races in the same non-fuzz module surface; frontend is intentionally not duplicated | Required CI; local diagnostic | Needs a race-capable Go toolchain and more CPU/memory; WEB=0, AGENT_SHARDS=0 | Any race report, test failure, or setup failure is nonzero; a slow or unavailable toolchain is a limitation/failure | Serf CI/tooling; no new follow-up currently |
| <code>make vet</code> | Go vet across all non-fuzz workspace modules | Go vet diagnostics for every module, independent of the tagged lint floors | Required CI; local diagnostic | Deterministic Go analysis; no provider calls | Any module vet failure is nonzero | Serf CI/tooling; no new follow-up currently |
| <code>make fuzz</code> | Tagged fuzz contracts, committed seed/crasher replay, Rapid replay, golden replay, and fuzz-tool packages | Fuzz invariants compile and execute, committed fuzz inputs remain safe, Rapid properties (including the seqfuzz/schemafuzz family that <code>make test-fuzz</code> also owns) replay under a fixed coverage seed bank, and decode goldens remain stable | Required CI deterministic corpus gate; local pre-merge when warranted | No fuzz search or provider calls; uses committed inputs and serffuzz tags; sets <code>SERF_FUZZ_TESTS=1</code> so the seqfuzz/schemafuzz family's default skip does not swallow the replay; memory caps are best-effort by platform | Any compile, replay, invariant, Rapid, or golden failure is nonzero. Search campaigns belong to make fuzz-nightly, not this gate | Serf fuzz/tooling; no new follow-up currently |
| <code>make fuzz-gap-check</code> | Static decode/parse fuzz-target coverage | Every discovered decode/parse package has a registered fuzz target or an explicit ignore | Required CI; local quick check | Seconds, deterministic, no network or corpus replay | An uncovered package or registry/tool failure is nonzero | Serf fuzz/tooling; no new follow-up currently |
| <code>make fuzz-corpus-scan</code> | Gitleaks over committed fuzz corpora | Fuzz seeds do not contain secrets | Required CI; local harvester feedback | Needs gitleaks for a meaningful scan; local absence warns and returns zero unless SERF_GITLEAKS_REQUIRED=1 | A finding or required-tool absence is nonzero; a local warning is an explicit limitation, not evidence of a scan | Serf security/tooling; no new follow-up currently |
| <code>make test-dev-tooling</code> | The scripts/*-selftest.sh suites that pin serf's own dev tooling | Each suite is the only thing pinning its script's contract; a suite that leaves anything in its private TMPDIR after passing fails, which is what enforces suite cleanup | Final step of <code>make merge-approval-gate</code>, and on demand; not part of <code>make test</code> because these suites test tooling, not the product | Each suite is offline and deterministic; the wave runner (<code>cmd/serf-test-dev-tooling</code>) gives every suite its own process group and private TMPDIR, is quiet on success, and replays a failing suite's whole log | Any suite exit nonzero, or a passing suite leaving files behind, is nonzero | Serf CI/tooling; no new follow-up currently |
| <code>make merge-approval-gate</code> | Serial local composition of lint, runtime build, full deterministic test, and the dev-tooling self-test wave | The canonical local/post-merge contract: make lint, make build, ROOT_FULL=1 make test, then make test-dev-tooling | Local pre-merge/post-merge; CI keeps equivalent checks in separate named jobs | Does not run fuzz search, race testing, provider calls, or browser guards; those have separate owners | The first failing phase stops the gate and returns nonzero; do not infer a verdict from partial logs | Serf CI/tooling; no new follow-up currently |
| <code>make dist DIST_GOOS=... DIST_GOARCH=...</code> | Release/distribution binaries | The archive contains serf, serf-hub, serf-tui, and serf-doctor built for the requested target with a fresh SPA | Release/snapshot CI; manual distribution verification | Cross-compilation and frontend dependencies; release CI has networked setup for tool/dependency installation | Any build, archive, inspection, checksum, or upload failure is nonzero; unavailable release tooling blocks release | Release engineering; no Serf launcher work is implied |
| <code>scripts/web-preflight.sh</code> | Frontend dependency/setup health | The worktree has a lockfile-compatible install and a real local TypeScript compiler | Setup prerequisite for web/build/browser gates | May access npm when a real install is missing/stale; refuses unsafe npm ci through a mismatched shared symlink | Missing, mismatched, or unhealthy install is nonzero; npm/network unavailability is a setup failure | Worktree/frontend tooling; shared install management stays outside Serf |
| <code>SERF_LIVE_TESTS=1</code> (umbrella opt-in)<br><code>SERF_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v</code><br><code>SERF_OPENAI_CODEX_E2E=1 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v</code><br><code>SERF_ANTHROPIC_E2E=1 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v</code> | Provider/live/e2e | Real MCP and provider wire/API behavior, credentials, and model/provider contracts | Explicit manual/nightly opt-in; never default CI; <code>SERF_LIVE_TESTS=1</code> also enables applicable live suites | Requires the named opt-in plus the corresponding tool, credentials, model access, and network; provider keys alone do not enable it | Tests without opt-in skip explicitly. With opt-in, configuration/API failures are nonzero; unavailable optional tools or credentials must be reported as skips/limitations, not passes | Provider owners; no default-gate follow-up |
| <code>SERF_E2E_LIVE=1 scripts/e2e-cover.sh --merge-unit</code><br><code>SERF_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -count=1 -v</code> | Live service coverage and host sandbox parity | Exercises real binaries/services or the host Seatbelt backend beyond deterministic unit coverage | Manual/platform-specific; not required CI | Needs provider/network services for live scenario scripts or macOS Seatbelt; SERF_E2E_LIVE is not a correctness gate because the coverage script intentionally continues past scenario failures | Missing platform/service is a limitation; live scenario failures must be read from script output rather than treated as coverage success | E2E/sandbox owners; hardening the coverage script is a separate follow-up |
| Launcher health checks, managed-service restart, SDD/Kata semantics | Operational/external workflow | None are Serf-owned gate proofs in the current Makefile or workflows | Outside this repository's gates | Owned by the launcher, worktree manager, or SDD/Kata tooling | Do not add or silently imply these checks in Serf CI | Launcher/worktree manager/SDD owners; outside this change |

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

ROOT_FULL=1 makes the protected first wave remove root's ordinary -short mode
while retaining the non-fuzz Test/Example name filter.
The runner still excludes the explicitly fuzz-owned sanity functions; their
deterministic replay is part of make fuzz. The retired standalone go test ./...
also ran ordinary tests in cmd/serf-fuzzcov and cmd/serf-fuzz-harvest; all fuzz
coverage, including those tests and the excluded root fuzz-tool packages, is
explicitly owned and run by make fuzz. Ordinary make test remains the default
local command and keeps the root wave in short mode unless ROOT_FULL=1 is
explicitly set. The CI web job runs make test-web, make build-web, and
make test-web-browser; the Go job runs ROOT_FULL=1 WEB=0 make test so frontend
tests are not duplicated.

make test-dev-tooling runs the scripts/*-selftest.sh suites that pin serf's
own tooling (cmd/serf-test-dev-tooling). They test tooling, not the product,
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

## The seqfuzz/schemafuzz Family Lives Only in `make test-fuzz`

A Jesse ruling: no fuzz-family test — including a smoke-depth iteration —
belongs in `make test`, `go test ./...`, or any of their variants. This is a
different exclusion from the `serffuzz`-tagged native `FuzzXxx` targets and
`seed100`-style edge suites, which never compile into a default build because
of their build tag. The tests this ruling targets are ordinary
`func TestXxx(t *testing.T)` functions that call `rapid.Check` to run a
stateful/sequence property fuzzer — no build tag hides them, so a plain
`go test ./agent` used to run them, at whatever depth `rapid.Check`'s own
`testing.Short()` awareness picked.

Each test in the family is now individually gated at the top of its body:

```go
func TestDelegateSeqFuzz(t *testing.T) {
	if os.Getenv("SERF_FUZZ_TESTS") != "1" {
		t.Skip("fuzz: skipped by default; run `make test-fuzz`, or SERF_FUZZ_TESTS=1 go test ./agent -run TestDelegateSeqFuzz -count=1 -v")
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
targets under the `serffuzz` build tag — already excluded from every default
build, not part of this change.

Three other entry points drive this same rapid family and needed
`SERF_FUZZ_TESTS=1` threaded through so this ruling would not silently blind
them: `make fuzz`'s fixed-seed rapid replay loop (`scripts/run-fuzz.sh`'s
`rapid` case, used by `fuzz-nightly`/`fuzz-triage`/`fuzz-continuous` too),
`scripts/fuzz-oracle-audit.sh`'s `run_seeds`, which replays a mutation against
`TestJobstoreSeqFuzz` and must never read that test's now-default skip as the
oracle failing to catch the mutation, and `scripts/fuzz-coverage-global.sh`'s
Rapid replay branch (`fuzz-coverage-global-selftest.sh` now asserts the
opt-in actually propagates — not just that it's in the source — by proving a
gated replay's coverage profile carries a nonzero execution count, and by
mutation-testing that assertion itself: stripping the opt-in from a copy of
the runner reproduces a zero-count profile and makes the assertion fail).

Fuzz-family coverage and the default gate's coverage number are tracked on
two separate tracks, not one. `go test ./agent -short`'s `-cover` output (and
the coverage floor it feeds) measures only the imperative test suite — the
seqfuzz/schemafuzz family t.Skip()s there by design. Coverage contributed by
this family is instead `make fuzz-coverage-global`'s job: it is the ONLY
coverage target that replays Rapid surfaces (with `SERF_FUZZ_TESTS=1`, as
above), against its own ratchet. `make fuzz-coverage`
(`scripts/fuzz-coverage.sh`) does not participate in this track at all — its
target loop is `[ "$tag" = native ] || continue`, so it replays only native
`FuzzXxx` corpora and never touches a Rapid target, gated or not. Do not read
a default-gate coverage number as "whole-repo coverage including fuzz" — it
never was, and now it's explicit.

## Proving a Type Survives a Round Trip

When two code paths must agree about a struct — a decoder and a
projector, a live path and a reload path — a hand-written fixture proves
only that today's fields survive. A field added next month passes,
because nothing in the test knows it exists.

Build the fixture by walking the **type** with reflection instead. Fill
every field with a distinguishable value, decode the same bytes through
both paths, and report divergent fields by name.

`cmd/serf-hub/app_threadread_decode_fidelity_test.go` is the worked
example. It catches a field added tomorrow with no test edit and no new
fuzz seed — verified by adding a synthetic field and watching the test
name it unprompted.

The gotchas in the fixture builder are all in the leaves: `time.Time`
needs whole seconds, `json.RawMessage` must contain valid JSON, floats
must be integral to survive widening through `any`, and an unhandled
`reflect.Kind` must fail loudly rather than skip — a builder that
silently skips a kind is a test that silently stops covering it.

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
`cmd/serf-hub/frontend` cover it, and the split matters:

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
arrive from the pty; `cmd/serf-tui` writes each frame through bubbletea's
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
  every call (`cmd/serf-tui/hub_model.go`, `sessionView()` in
  `cmd/serf-tui/hub_session_view.go`); there is no code path that writes only
  the composer, and the session breadcrumb (`topBar`) is provably non-empty
  for any reachable state, so `tuiprim.AppShell.View()` cannot legitimately
  drop it while keeping the footer.
- The kata's own report is inconsistent with a `View()` bug on logical
  grounds: `View()` is a pure function of model state, so an inert keypress
  cannot change its return value — yet "any key... brought back... content
  that had not changed" is exactly what a render/transport-path bug looks
  like from the outside, and exactly what a `View()` bug cannot produce.

`cmd/serf-tui/hub_partial_repaint_nxq6_test.go` drives realistic notification
bursts through `hubModel.Update` directly (no terminal involved) and checks
that invariant after every step, both as a fixed scenario and as a fuzz
target; a mutation test (temporarily dropping `topBar` from `AppShell.View()`)
confirmed the check fails the way it should before the mutation was reverted.

**The fix**: never trust a lone `Capture()` (or a lone `capture-pane`) for a
negative assertion ("X is absent"). `WaitFor` in
`cmd/serf-tui/tmux_e2e_test.go` already retries until its wanted substrings
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

## A Test That Never Runs

A test that does not execute is worse than a missing test: it reports the
coverage without providing it, and the suite stays green either way. Two shapes
in this repo produce one, and neither announces itself.

**Registered `check*` functions.** Several packages drive their behavioral
contracts through a fuzz entry point that replays one check selected by the fuzz
input — `FuzzFSPathsBehaviorProgram` and friends in `cmd/serf-hub/internal/`
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
## Real `git` in Worktree Tests

`git` is an external dependency like the LLM provider, and the same boundary rule
applies: keep it real when git's own behavior is the thing under test, and script
it when git is only a way to reach a Serf decision.

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
`agent/session_tools_worktree_scripted_test.go`) when the subject is Serf's own
behavior:

- which validation or refusal rung fires, and its error text
- what event or warning was emitted, and how many times
- what Serf wrote to its own state — sidecars, jobstore records, disposed marks,
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

Mint them with `cmd/serf-hub/internal/hubtest` instead of writing them out:

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
`$HOME/.serf/hub.lock`, and that path is deliberately not overridable
(kata av1j): the lock, the run dir, the state root, and the auth token
all derive from `os.UserHomeDir()`, so they only stay coherent when they
move **together**. The blessed way to run a second, disposable hub — an
e2e harness, a scratch verification hub — is a fresh HOME:

```sh
HOME=$(mktemp -d) ./serf-hub -addr 127.0.0.1:0 -serf ./serf
```

Never point a test hub at the real HOME "just for a quick check": if the
real hub happens to be running you collide (the flock error names the
lock file it lost); if it happens to be **stopped**, the test hub
silently claims the real `~/.serf` and state root — the dangerous case,
because nothing fails. A lock-path-only override was considered and
rejected for exactly that reason: it would unbundle the singleton guard
from the state it guards.

## A Live Run Uses the Machine's Build Cache

None of the live-test commands below carries a `GOCACHE=` prefix, and adding
one is a regression. A per-command cache under `/tmp` duplicates the
machine's warm cache on the checkout's volume, which has filled to 100%
twice mid-fleet-run (kata 98x9); one stray `/tmp/serf-gocache` grew from
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
SERF_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
```

`SERF_LIVE_TESTS=1` also enables these tests with the other live test suites.

## Environment Variable Tests

Supported runtime environment variables are defined in the `envvars` package
and documented in `docs/environment.md`. Production code, help text, and test
helpers should use those rows instead of hard-coded env names. The default test
suite includes an audit that fails when a supported env var is used as a raw Go
string outside `envvars`.

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
SERF_OPENAI_CODEX_E2E=1 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Optional model override:

```sh
SERF_OPENAI_CODEX_E2E=1 SERF_OPENAI_CODEX_E2E_MODEL=gpt-5.4 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Prerequisites:

- `serf openai login` has completed and stored OAuth credentials.
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
SERF_ANTHROPIC_E2E=1 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Optional model override:

```sh
SERF_ANTHROPIC_E2E=1 SERF_ANTHROPIC_E2E_MODEL=claude-sonnet-4-5-20250929 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
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
