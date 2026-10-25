# Audit gaps, completeness critique, 2026-10-25

Read-only review of SYNTHESIS.md and ROADMAP.md against the repo at this worktree. Nothing was run except grep, wc, git log and git grep. Counts below are from `git grep`/`wc` on tracked files (non-test unless stated).

## 1. Directories no slice covered, or covered only thinly

Coverage test: `grep -rl <path> raw/ verified/` and ROADMAP.

| Area | Size | Coverage | Verdict |
|---|---|---|---|
| `tools/prompt-eval/**` (incl. `memory-lab`) | 264 files, 1.4 MB, 238 files in `memory-lab/scenarios` | 0 mentions in raw, verified or ROADMAP | Not audited at all |
| `agent/testdata/fuzz` corpus | 3,248 files, 7.6 MB of the 4,169 tracked `testdata/fuzz` files | 1 verified mention, 0 in ROADMAP | Not audited |
| `mobile-native/src/generated/mermaidPage.ts` | 3.57 MB single generated file | Not mentioned | Not audited |
| `cmd/evener-fuzz-harvest` | 3.1k lines Go incl. tests | 0 verified mentions | Not audited |
| `cmd/evener` | 7.3k prod, 28.9k test (3.9x), `serve.go` 2,489 | Folded into `internal-misc-cmds` with ~30 other dirs; `serve` 1,597-line finding only | Thin |
| `cmd/evener-dev` | 4.5k prod, 8.0k test; `agentshards.go` 1,353 | 4 verified mentions | Thin |
| `cmd/evener-doctor` | 1.2k prod, 4.0k test | 1 verified mention | Thin |
| `internal/plugins` | 8.9k prod, 22.9k test (2.6x); `marketplaces.go` 1,670, `marketplace_migration.go` 1,282, `resolve.go` 1,146 | 5 verified files, mostly migrations | Thin on the three god files |
| `docs-site/` | 36 files, own `package.json` (vitepress, playwright, cheerio, remark), ~10 test files, own CI workflow `.github/workflows/docs.yml` | Mentioned in ROADMAP 3 times for docs content; code and test debt not audited | Not audited as code |
| `test/e2e/{fakellm,fake429,turncpu}` | 3.6k lines; `fakellm/cmd/main_test.go` 875 | 1 raw mention | Thin; overlaps the "54 provider fakes" finding (`go-tests-rest-1`) |
| `test/scenarios/` | 127 markdown scenario cards | Not mentioned | Not audited (staleness of cards vs shipped behavior unknown) |
| Python (8 `.py` files) | `memcheck.py`, `test_memcheck.py` 217, `test_bookkeeping.py` 691, `make_seed.py` | 0 | Third language, see spot check 2 |

Slices that did well and need no extra work: auth/openai, rendezvous, cmdutil, tool-fluency (13 raw mentions).

## 2. Spot checks of the gaps (real debt found)

### G1. Ungated Python test suite in `tools/prompt-eval/memory-lab` (medium)
- Evidence: `git ls-files | grep '\.py$'` gives 8 files; the memory-lab ones are `memcheck.py`, `test_memcheck.py` (217 lines), `test_bookkeeping.py` (691 lines), `scenarios/index-overflow/test_make_seed.py` (18). `git grep -n "test_memcheck\|test_bookkeeping\|memcheck.py" -- . ':!tools/prompt-eval'` returns nothing; `git grep -il "pytest\|python3" -- Makefile make .github` finds only `.github/workflows/docs.yml:21` (`setup-python`, for docs examples). So no Makefile target, lint or CI job runs these tests, and `AGENTS.md` does not mention them.
- Related: `tools/prompt-eval/memory-lab/memory-lab` is a 1,250-line extensionless script; `git grep -n "memory-lab" -- Makefile make scripts .github AGENTS.md docs/developing-evener` finds nothing, the only referrers are `docs-site/scripts/check.mjs`, three docs/superpowers plans/specs and `tools/prompt-eval/README.md`. The whole lab was added 2026-10-06 and last touched 2026-10-13.
- Fix: decide whether the lab is a supported tool. If yes, add a `make test-memory-lab` target to the lanes (python3 only, no network) or port the 900 lines of Python tests; if it is a one-off experiment record, move it out of the tree (history keeps it). Net: either +10 lines of Makefile or about -9,000 lines. Needs Jesse's call.

### G2. Generated seed output committed beside its generator (low to medium)
- Evidence: `scenarios/index-overflow/` has 124 tracked files (121 `.md` pages, 1,393 lines) and `make_seed.py` whose docstring says "120 pages by hand drift; this keeps the seed ... in step". It is 124 of the memory-lab's 238 files.
- Fix: keep the generator and have the lab call it into a temp dir (it already supports `--out`), delete the 121 pages. About -1,400 lines, 121 files. Guard: the existing `test_make_seed.py` if G1 is resolved.

### G3. Fuzz corpus bloat in `agent/testdata/fuzz` (medium)
- Evidence: `git ls-files agent/testdata/fuzz/FuzzToolArgsValidate | wc -l` is 2,327 files totalling 7,568,562 bytes (`xargs cat | wc -c`), against 7,618,143 bytes for all of `agent/testdata/fuzz` (3,248 files). A sampled file is `int(1)` plus a `[]byte("{\"await_reply\":false,\"message\":\"xxxx...` run of `x`. History shows the directory was last touched by the 2026-08-18 serf rename commits. `agent/internal/jobstore` adds 396 more.
- Who writes the corpus: seeds come from `cmd/evener-fuzz-harvest`, whose `Emitter` (`emit.go`, `write`) writes content-addressed files into `agent/testdata/fuzz/FuzzToolArgsValidate` (`dirToolArgsValidate` in `discover.go`; the tool-call path is `transcript.go`, `emitIntBytes`) and dedupes only identical encoded seeds and oversized ones (`maxSeedBytes`). Crashers come from Go's own fuzzing toolchain (`go test -fuzz` writes failing inputs into `testdata/fuzz/<FuzzName>/`). `fuzz/promoter` is not a corpus writer: it emits generated regression-test files (`promoter/emit_go.go`). An earlier version of this section credited the promoter; that was wrong.
- Correction to the cost claim: `FuzzToolArgsValidate` is in `agent/tool_args_fuzz_test.go`, which carries `//go:build evenerfuzz` (checked with `head -3` and `grep -n 'go:build' agent/tool_args_fuzz_test.go`). The default `go test ./agent` does not compile that target, so it does not replay the 2,327 inputs. They replay only under `-tags evenerfuzz` (`make lint-evenerfuzz` only compiles them). The real cost is repository weight (7.6 MB per clone) and review noise when the harvester re-runs.
- Fix: maintain the corpus where it is written. Give `cmd/evener-fuzz-harvest` a per-target cap and a coverage-based dedupe (keep one seed per new edge, using the target under `-tags evenerfuzz`), and prune the existing files in one PR with a before/after `-tags evenerfuzz -run FuzzToolArgsValidate` coverage report from CI. Do not touch `fuzz/promoter` for this. Keep every file that Go's fuzzing toolchain wrote as a crasher (tell them apart from harvester seeds by checking `git log --diff-filter=A` for each file's adding commit), since those are regressions. Medium risk of dropping a crash regression; no production code changes.

### G4. 3.57 MB generated bundle committed to git (low)
- Evidence: `mobile-native/src/generated/mermaidPage.ts` is 3,573,568 bytes, a single `MERMAID_PAGE_HTML` string literal generated by `scripts/build-mermaid-page.mts`. `git log --oneline -- <file> | wc -l` is 2 revisions so far (2026-09-29, 2026-10-04), but every `mermaid` bump rewrites 3.5 MB into history. It has a freshness test (`mobile-native/src/mermaidPage.test.ts` imports the builder) and `mobile-native/biome.jsonc:24` excludes it, so it is guarded. Debt is repo weight and diff noise, not drift.
- Related drift: mermaid is pinned three ways, `11.17.2` in `cmd/evener-hub/frontend/package.json:38` and `mobile-native/package.json:68`, `12.2.0` in `docs-site/package.json:20`.
- Fix: build the page at bundle time (Metro/esbuild step in the native build) and gitignore it, or keep it and accept the cost. Low priority; align docs-site to one mermaid major if the site renders the same diagrams.

### G5. JS toolchain version skew across the four npm trees (medium)
- Evidence (package.json lines):

| Package | frontend | mobile-native | appwire-client | docs-site |
|---|---|---|---|---|
| `@biomejs/biome` | `2.5.5` (exact) | `^2.5.12` | none | none |
| `vitest` | `^4.1.11` | `^5.0.0` | n/a | n/a |
| `react` | `^19.2.7` | `19.2.3` (exact) | n/a | n/a |
| `vite` | `^8.1.5` | n/a | n/a | `^6.4.3` (override) |
| `typescript` | `^6.0.3` | `~6.0.3` | `~6.0.3` | n/a |

  Mixed exact, caret and tilde pinning. `AGENTS.md` says Biome formatting is checked in CI per tree; two Biome minors can format the same shared files (`appwire-client/typescript` is gated by the frontend's Biome only) differently from what mobile-native tooling would produce. Vitest 4 vs 5 means the shared client's tests run under one major while the app that consumes it runs another.
- Fix: one pin policy (exact versions, matching across trees where the code is shared), enforced by a ~30-line check in `internal/devtool` or an existing lint script. Not a new audit test: put it in `make lint-package-imports` neighbours.

### G6. Root `go.mod` has contradictory local-module versions (low)
- Evidence: `go.mod:75` requires `primeradiant.com/evener/auth v0.0.0` while `agent/go.mod:19` and `llm/go.mod:13` require `auth v0.1.0`; the root `go.mod` carries two replaces for it (`auth v0.0.0` and `auth v0.1.0`, both `=> ./auth`) plus replaces for every other local module even though `go.work` already `use`s them. `go.work.sum` has 160 lines; root `go.mod` has 63 requires, 37 indirect.
- Fix: one version string for all local modules, delete replaces that `go.work` makes redundant only if nothing builds with `GOWORK=off` (check `make` targets and `scripts/` first; the install script and CI may). About -10 lines. Low.

### G7. Production code uses `afero` as a test seam in 46 files (medium, missed theme)
- Evidence: `git grep -l 'spf13/afero' -- '*.go' ':!*_test.go'` gives 46 files; 83 `afero.NewOsFs()` constructions in production; `afero.NewMemMapFs()` appears 465 times across test files. Example `agent/session_queue_persist.go:49-75`: `saveQueuesFS(fs afero.Fs, ...)` exists only so tests can pass a fake FS, and the same function writes `path + ".tmp"` with no fsync (the defect class in SYNTHESIS section 2). SYNTHESIS has 0 mentions of afero, ROADMAP 2 (only as a constraint in W1-01).
- Why it matters: it is the largest single "seam" in T1 and the root of why atomic-write helpers cannot move to `execsupport` cleanly (W1-01 has to invent an FS interface to avoid the dependency). Fault injection through an in-memory FS also cannot reproduce real rename/fsync/symlink behavior, so tests pass where production writers are weak.
- Decision (2026-10-25, DECISIONS.md): phase `afero` out. New code and the shared writer use the real `os` package, tests use `t.TempDir()`, callers migrate as their files are touched, and `afero` leaves `go.mod` after the last one. The options below are kept for the record. Option A (simplest, chosen): tests use `t.TempDir()` real files, production calls `os` directly, fault injection only where truly needed, drop `afero` from `go.mod` over time. Option B: keep afero but forbid it in new production signatures. This should be added to T1 and decided before W1-01 designs its interface.

## 3. Roadmap items that look unsafe or too big

1. **W1-01 / W1-02 / W1-03, always-on fsync (high risk of regressions the plan does not name).** The plan sets "durability always on" and lists `agent/session_queue_persist.go:71` among the first callers. The caller frequency of `saveQueuesFS` was not traced here; by name it persists queue state, so check its call sites before choosing a sync policy. Adding file fsync plus directory fsync there changes a hot path from a page-cache rename to two disk syncs per mutation. The memory/CPU-reduction work (project memory 2026-09-24) already fights daemon idle cost. Fix the plan: measure per caller class, give the shared writer an explicit `NoSync` option for rebuildable state (queue snapshot is re-derivable from the transcript only if that is true; confirm first), and state which callers stay non-durable. The afero decision (G7) is made: the shared writer takes no FS interface. Q4 is answered too: the writer lives in `execsupport` and is durable by default.
2. **W1-54 (coverage-wave deletion) omits the fuzz registry.** `scripts/fuzz/fuzz-targets.txt` (592 lines) is "the SINGLE source of truth for every fuzz surface" and its header says native rows are machine-checked against source by the static gap gate. A loop over the `Fuzz*` funcs defined in `cmd/evener-hub/cov_*_test.go` finds 46 that are registered by name. Deleting the files without removing those rows fails the gap gate. ROADMAP has 0 occurrences of `fuzz-targets` (verified with grep) and 3 of `testing-budget`. Add both files, plus `scripts/coverage/coverage-floors.txt` (already named) and `scripts/coverage/fuzzcov-ignore.txt`, to the W1-54 file list, and make the PR delete rows and files together.
3. **W1-22 (-2,500 LOC in one PR).** `agent/subagents.go` is 2,536 lines in total and the plan lists 11 symbols plus the `stableController` branches; the plan itself says "the surface is larger than reported; re-measure". It depends on W1-21 which touches 32 test files. Split into: (a) delete `sendInput`/`cancelAgent`/hooks, (b) delete `spawnAgent` and `prepareSubagentRun*`, (c) remove `stableController`. Each one compiles on its own. Also add a "delegate lifecycle" behavior test that goes through the controller path before (a), since T1 says tests move before code is deleted.
4. **W1-58 (delete plans "not cited outside docs/superpowers").** Citation by `git grep` misses uncommitted and branch-only work: `git status` in this checkout shows untracked `docs/superpowers/plans/2026-10-22-delegate-terminal-reports.md` and two untracked specs for the same feature. Specs are also referenced from memory notes and PR descriptions, not from code. Rule should be "plan whose PR merged to main", determined by `gh pr list --search`, not by citations; and exclude anything touched in the last 30 days.
5. **W1-19 (client-mutation skeleton, durable input).** Plan marks it medium-high; agree, and add that it must land after W1-23 (deleting the unused public queue API) rather than in parallel, because both edit `agent/queue.go`. (Both touch `agent/queue.go`; the lane table lists them in the same lane.)
6. **W0-12 (-1,500 LOC, "75 functions" not re-derived).** The audit says the count was not verified. The plan handles it with "regenerate at PR start", which is right, but then W0-12 sits in the "start now" set with W0-02. It should wait for the deadcode reachability pass (G8) so the list is a command output, not a recollection.
7. **W1-50 "convert phrase assertions per area" (900+ asserts) has no ordering vs W1-54.** Converting assertions in files that W1-54 then deletes is wasted work. Order: W1-54 first, then W1-50 on what survives.
8. **W2-03 "extract leaf packages from package agent".** Import cycles are the real hazard: `agent` is one package of about 105k lines with 274-field `Session`. The plan says design discussion first, which is right; add a prerequisite: run `go list -deps` / `goda` style check to prove each candidate has no back-edge to `Session`, and require that list in the design doc.

## 4. Debt categories the audit missed

### M1. Logging is ad hoc and uneven (medium)
- Evidence: only 4 non-test files import `log/slog` (`agent/delegate_runtime.go`, `jobs_activity.go`, `session_attention.go`, `summary_diagnostics.go`, 7 slog calls total) and 10 import `log` (6 of them test harnesses or examples; production: `agent/internal/tool/registry.go`, `cmd/evener-hub/app_force_stop.go`, `app_threadlifecycle.go`, `internal/appserver/server.go`, `llm/registry/load.go`). 36 `log.Printf/Println/Fatal` hits. 131 `os.Stderr` references in non-test code; 18 `fmt.Fprintf(os.Stderr, "[hub] ...` sites, 104 `[hub]` string literals, a private logger in `cmd/evener-hub/internal/hubcore/prober.go:30`, and 14 files with their own `logf func(format string, args ...any)` field or var (for example `transcriptIndexRepairLog`, `Logger func(...)` in hub deps). No levels, no component field, no single place to redirect.
- Impact: diagnosing a daemon (the repo has `evener-doctor`, forensics scenario cards, a "hub web UI slow load" investigation) depends on stderr text with hand-written prefixes. Tests that capture stderr become copy oracles.
- Fix (simplest): one tiny `slog`-based helper in `envvars` or `execsupport` that returns a component-tagged `*slog.Logger` writing to stderr in the current format, then replace the 14 `logf` fields with `*slog.Logger` injected where already injected. No framework, no config. About +60, -300 lines. Not a priority over the roadmap's deletions; do it opportunistically when a file is touched.

### M2. Concurrency and cancellation hygiene (medium; needs a dedicated pass)
- Evidence counts (non-test): 81 `go func() {` literals, 125 `go` statements total, only 2 `errgroup` uses and 11 `.Go(func` calls (from `x/sync` which the module already requires), 28 files using `sync.WaitGroup`, 234 `sync.Mutex`/`RWMutex` declarations, 22 `recover()` calls, 255 `context.Background()` calls (83 in `agent`, 26 in `hubcore`, 22 in `cmd/evener-hub`, 19 in `cmd/evener`, 16 in `agent/execenv`), 45 `panic(` calls. Slice reports covered lock-order and Session (274 fields, 22 mutexes) but nobody looked at goroutine lifetime or at why 255 call sites mint a fresh root context instead of inheriting one.
- Risk: a fresh `context.Background()` inside a request or turn path means shutdown and cancel do not reach the work (leaked goroutines, slow stop). Not verified per site; this is a hypothesis to test, not a finding.
- Fix: one audit slice with a script: list each `context.Background()` outside `main`/`init`/test and classify (root owner, detached-on-purpose, bug). Then add a `go vet` style check only if more than a handful are bugs. No new meta-test otherwise.

### M3. Config sprawl partly covered but not as a category (low to medium)
- Evidence: 37 distinct `EVENER_*` names in non-test code against 25 direct `os.Getenv`/`LookupEnv` reads (the rest go through `envvars`); `cmd/evener-dev/agentshards.go` alone has 7 direct reads and `agent/sandbox/sandboxtest/sandboxtest.go` 5. Items exist per slice (`hub-root-rest-3`: 30 retention knobs through 6 layers; Q20: 23 undocumented retention knobs) but there is no cross-cutting finding for "which knobs have a documented default, an owner, and a test". SYNTHESIS section 6 says env-var names are in sync, which this does not contradict.
- Fix: fold into Q20: one table (knob, default, doc row, consumer) generated from `envvars` and `hub.toml` decoders, delete knobs nobody sets. Not a new audit category in the roadmap.

### M4. Dependency bloat (low)
- Evidence: root `go.mod` direct deps include `afero` (see G7), `pgregory.net/rapid` (non-test use only in `fuzz/` and `cmd/evener-fuzzregistry`, so a tooling dep in the product module), `golang.org/x/tools` (non-test use only in `cmd/evener-internalcheck/main.go`), `golang.org/x/mod` (only `cmd/evener-fuzzregistry`), plus `modernc.org/sqlite`, `chroma/v2`, `html-to-markdown/v2`. Two JSON Schema libraries appear in the module graphs (`santhosh-tekuri/jsonschema/v5` imported in 14 places; `google/jsonschema-go` indirect via the MCP SDK), which is fine as is. `go.sum` 179 lines; no unusual bloat.
- Fix: move `rapid`, `x/tools`, `x/mod` consumers (`cmd/evener-fuzzregistry`, `cmd/evener-internalcheck`, `fuzz`) into the existing `fuzz` module so the shipped binaries' module graph does not carry them. Verify with `go mod why`; about -10 lines of `go.mod`, a few moves. Low.

### M5. Four test-orchestration layers (medium)
- Evidence: `cmd/evener-dev` (4.5k prod, `agentshards.go` 1,353, `shardplan.go` 611, `webgate.go` 514), `internal/devtool` (1.8k, `shardrun`), `scripts/gate/*.sh`, and `Makefile`/`make/*.mk` all decide how tests are split, timed and budgeted, alongside 20+ root-level `gate*_test.go` / `timingbudget*_test.go` files. The build-gates slice treated the root audit tests and scripts; the Go dev tools (about 6.3k prod, 8k+ test) were only covered by 4 mentions and no finding about their overlap. A shard planner written in Go that is itself driven by shell and re-tested by root Go tests is three languages deep for one job.
- Fix: Q11 was decided as keep-all-gate-tooling (DECISIONS.md), so only the overlap question remains: does `cmd/evener-dev` plus `internal/devtool` stay the single runner, with the shell scripts reduced to thin calls?" Needs a read of the three layers; do not estimate until then.

### M6. Binary and generated artifacts in git (low, mostly covered)
- Evidence: x-docs-1 covers PNG weight (about 70 MB). Not covered: G3 fuzz corpus (7.6 MB), G4 mermaid bundle (3.6 MB), `llm/registry/testdata/models.dev.sample.json` (1.5 MB, the third largest tracked file by `du`), `llm/testdata` 2.1 MB total. A size budget in `make lint` ("no tracked file over 1 MB except an allowlist") would catch regressions with 15 lines of shell and replaces nothing.

## 5. Suggested additions to the plan

| Add | Where | Size |
|---|---|---|
| Decide afero (G7) before W1-01 | Q4 | decided 2026-10-25: phase out; reflected in W1-01..W1-03 |
| Name `fuzz-targets.txt` and `fuzzcov-ignore.txt` in W1-54; delete rows with files | W1-54 | edit plan |
| Measure fsync cost per caller before W1-01 migrations | W1-01 | edit plan |
| Split W1-22 into three PRs | W1-22 | edit plan |
| W1-58 rule: PR-merged status, not citations | W1-58 | edit plan |
| Put G1/G2 (memory-lab) under Q11 | Decisions | decision |
| New W1 entries: G3 corpus dedupe, G5 npm pin policy, M1 logger helper, M4 tooling deps move | Wave 1 | about -2,000 corpus files, 0 code lines, +30 (pin check), +60/-300 (logger), -10 (go.mod) |
| New audit slice: concurrency/cancellation (M2) and test-orchestration overlap (M5) | before Wave 2 | read-only |
