# Serf Code Coverage Audit

**Worktree:** `.worktrees/coverage-audit`  
**Generated:** 2026-06-27  
**Overall Module Coverage:** 71.1% (root module), 82.7% (agent module)

---

## 1. All Packages by Coverage (Worst to Best)

| Package | Coverage | Category | Verdict |
|---------|----------|----------|---------|
| `tools/tool-fluency/cmd/serf-fluency` | 12.4% | main (tool) | Integration only |
| `hubapi` | 27.4% | HTTP client | **Unit test** |
| `cmd/llmcall` | 30.0% | main (CLI) | Integration only |
| `cmd/serf-hub/internal/fspaths` | 37.6% | FS utility | **Unit test** |
| `internal/diagnostic` | 38.6% | Pure logic | **Unit test** |
| `cmd/serf-tui/internal/clipboard` | 41.5% | OS integration | Integration only |
| `cmd/serf-tui/internal/modeldisplay` | 48.0% | Pure string | **Unit test** |
| `cmd/serf-tui/internal/tuitheme` | 51.4% | TUI theme | Extract + test pure |
| `cmd/serf-tui/internal/launchconfig` | 54.1% | Config logic | **Unit test** |
| `cmd/serf-doctor` | 54.7% | main (CLI) | Extract + test |
| `appwire` | 56.3% | Wire protocol | **Unit test** |
| `appwire/appwiretest` | 50.0% | Test helper | Test the helper |
| `cmd/serf-hub/internal/appsource` | 61.3% | Source impl | Inject mock client |
| `cmd/serf-tui/internal/tuiprim` | 56.4% | UI render | **Unit test** (pure) |
| `internal/credentials` | 61.8% | Pure logic | **Unit test** |
| `cmd/serf-tui/internal/tuipick` | 60.9% | UI logic | **Unit test** |
| `cmd/serf-tui/internal/hubstart` | 59.7% | Network glue | Integration preferred |
| `cmd/serf-internalcheck` | 70.5% | Lint tool | **Unit test** |
| `cmd/serf-tui/internal/transcript` | 69.7% | Reducer | **Unit test** (pure) |
| `cmd/serf-tui/internal/hubdiagnostics` | 69.2% | UI string | **Unit test** |
| `internal/selfupdate` | 73.7% | Network/FS | Already DI'd, testable |
| `cmd/serf` | 73.4% | main (CLI) | Extract + test |
| `cmd/serf-tui` | 77.6% | main (CLI) | Extract + test |
| `cmd/serf-hub` | 74.3% | Web+CLI | Extract handlers |
| `rendezvous` | 69.6% | FS utility | **Unit test** |
| `cmd/serf-tui/internal/tuiprim` | 68.2% | UI render | **Unit test** (pure) |
| `cmd/serf-tui/internal/msgrender` | 76.0% | UI render | **Unit test** (pure) |
| `server` | 79.1% | RPC server | **Unit test** (httptest) |
| `cmd/serf-tui/internal/transcript` | 74.4% | Reducer | **Unit test** (pure) |
| `cmdutil` | 65.5% | Mixed | Pure parts testable |
| `internal/apptranscript` | 76.1% | Transcript | **Unit test** |
| `internal/appserver` | 85.5% | WebSocket | Mock transport |
| `internal/appprojector` | 89.5% | Projector | Error paths |
| `internal/binresolve` | 87.5% | FS utility | **Unit test** |
| `cmd/serf-hub/internal/mcpstatus` | 86.1% | Status probe | **Unit test** |
| `cmd/serf/internal/cliprompt` | 88.9% | Prompt | **Unit test** |
| `cmd/serf/internal/launchcheck` | 86.0% | Launch check | Mock provider |
| `cmd/serf-hub/internal/hubcore` | 84.1% | Hub core | Error paths + sort |
| `cmd/serf-hub/internal/launchconfig` | 81.7% | Config | Error paths |
| `cmd/serf-hub/internal/hostlock` | 81.8% | Lock | **Unit test** |
| `cmd/serf-tui/internal/toolsummary` | 81.8% | UI render | **Unit test** |
| `cmd/serf-hub/internal/hubedge` | 85.0% | Auth | Error paths |
| `buildinfo` | 100.0% | ✓ | |
| `frontmatter` | 100.0% | ✓ | |
| `cmd/serf-hub/internal/editorurl` | 100.0% | ✓ | |
| `cmd/serf-hub/internal/httpsec` | 100.0% | ✓ | |

### Agent Module Sub-Packages

| Package | Coverage | Notes |
|---------|----------|-------|
| `agent/events` | 4.5% | **Critical gap** — event data structs |
| `agent/plugin` | 56.9% | `ParseAgent` = 70 uncovered stmts |
| `agent/transcript` | 50.7% | `AppendAPICall` = 21 uncovered stmts |
| `agent/provider` | 60.2% | `addDecisionToSchema` = 34 uncovered |
| `agent/internal/diagnostic` | 58.7% | Pure logic, easy wins |
| `agent/internal/goal` | 70.7% | Error paths |
| `agent/internal/tool` | 73.2% | Error paths |
| `agent/internal/mcp` | 77.1% | Error paths |
| `agent/internal/jobstore` | 77.2% | `FoldOrdered` = 18 uncovered |
| `agent/execenv` | 78.8% | `Glob` = 20 uncovered |
| `agent` (root) | 85.5% | Core session logic |
| `agent/internal/contextmgr` | 85.9% | `ElicitNote` = 19 uncovered |
| `agent/internal/sessionlog` | 89.9% | Error paths |
| `agent/internal/hooks` | 90.3% | Error paths |
| `agent/mcpconfig` | 90.2% | Error paths |
| `agent/internal/atif` | 87.6% | Error paths |
| `agent/doctor` | 82.9% | Error paths |
| `agent/provenance` | 83.7% | Error paths |
| `agent/internal/frontmatter` | 100.0% | ✓ |
| `agent/internal/toolname` | 100.0% | ✓ |

---

## 2. The "Rob Pike Proud" Remediation Plan

### Principles

1. **Test the interface, not the implementation.** Prefer `package foo_test` over `package foo`. The 6 `agent_test` files in this codebase are the right pattern; the 95 `package agent` files test internals that could change without breaking contracts.
2. **Table-driven tests for pure logic.** Any function with deterministic inputs/outputs gets a `[]struct{ name string; in T; want U }` table.
3. **No mock frameworks.** Use `httptest.Server` for HTTP, `t.TempDir()` for filesystem, and real interface implementations for everything else. Hand-rolled stubs are already present and correct; keep that pattern.
4. **Don't test `main()` or CLI glue.** `flag.Parse`, `os.Exit`, and wiring code are integration-test territory. Extract the decision logic and test that.
5. **Error paths are the easiest wins.** Most uncovered statements are `if err != nil { return ... }`. These are the fastest to cover and the most valuable (error handling is where bugs live).

### Phase 1: Pure Logic, No Refactoring (Week 1)

These packages have no external dependencies and no uncovered code that requires refactoring. Just add table-driven tests.

| Priority | Package | Why | Estimate |
|----------|---------|-----|----------|
| 1 | `internal/diagnostic` | Pure string/error classification. 4 uncovered functions (`FromFields`, `normalizeSource`, `defaultForSource`, `serfFailure`). | 2 hrs |
| 2 | `cmd/serf-tui/internal/modeldisplay` | One function: `AbbreviatePath`. Pure string manipulation. | 30 min |
| 3 | `cmd/serf-tui/internal/hubdiagnostics` | One branch in `defaultHubDiagnosticTitle`. | 15 min |
| 4 | `internal/credentials` | Pure map logic (`Layers`, `InstanceLayers`, `APIKeyFor`). | 1 hr |
| 5 | `cmd/serf/internal/rvreg` | One function: `Remove`. Create temp file, remove it. | 15 min |
| 6 | `cmd/serf-tui/internal/tuitext` | Missing package. `NonEmptyStrings`, `TruncateText`. Pure. | 1 hr |
| 7 | `cmd/serf-tui/internal/inputhistory` | Missing package. `UnescapeHistory`. Trivial. | 15 min |
| 8 | `cmd/serf-hub/internal/strutil` | Missing package. `FirstNonEmpty`. Trivial. | 15 min |
| 9 | `agent/events` | 4.5% coverage on event data structs. These are pure data types with constructors. | 3 hrs |
| 10 | `agent/internal/diagnostic` | 58.7% coverage. Pure logic in the agent module. | 2 hrs |

**Phase 1 total:** ~10 hours, adds ~500 statements of coverage, lifts overall by ~2-3%.

### Phase 2: Filesystem + HTTP, Minimal Setup (Week 2)

These need `t.TempDir()` or `httptest.Server` but no code changes.

| Priority | Package | Why | Estimate |
|----------|---------|-----|----------|
| 1 | `hubapi` | 27.4% — all API methods are untyped HTTP wrappers. Test with `httptest.Server`. | 4 hrs |
| 2 | `cmd/serf-hub/internal/fspaths` | `CompleteDirs`, `ValidateLaunchPath` with `t.TempDir()`. | 2 hrs |
| 3 | `rendezvous` | `Write` error paths with permission tricks. | 1 hr |
| 4 | `internal/selfupdate` | Already DI'd (`HTTPClient` field). Mock HTTP + `t.TempDir()`. | 3 hrs |
| 5 | `server` | 79.1% — HTTP handlers. `httptest.Server` + mock `*Server` dependencies. | 4 hrs |
| 6 | `internal/binresolve` | `SiblingDir` error path, `Resolve` edge cases. | 1 hr |
| 7 | `internal/apptranscript` | `TurnsFromFile` with temp fixture. | 1 hr |
| 8 | `cmd/serf-tui/internal/transcript` | Pure reducer. Build state, apply messages, assert. | 3 hrs |
| 9 | `cmd/serf-tui/internal/tuiprim` | Pure UI rendering. String assertions. | 2 hrs |
| 10 | `cmd/serf-tui/internal/msgrender` | Pure rendering. String assertions. | 2 hrs |

**Phase 2 total:** ~23 hours, adds ~1,500 statements of coverage, lifts overall by ~5-7%.

### Phase 3: Extract and Test (Week 3)

These require extracting logic from `main()` or from untestable functions before they can be unit-tested. The extraction is minimal — no new abstractions, just moving functions.

| Priority | Package | Refactoring | Estimate |
|----------|---------|-------------|----------|
| 1 | `cmd/serf-hub` | Extract HTTP handlers from `main()` into functions that take `(w http.ResponseWriter, r *http.Request)` and return `error`. Test with `httptest`. | 6 hrs |
| 2 | `cmd/serf-doctor` | Extract `cmdAPILog` and `cmdTree` from `main()` to match the tested `cmdLocate`/`cmdWatches` pattern. | 2 hrs |
| 3 | `cmd/serf` | Extract flag-parsing and decision logic from `main()` into testable functions. | 4 hrs |
| 4 | `cmd/serf-internalcheck` | Test `run()` with mock args/stderr. | 1 hr |
| 5 | `cmd/serf-tui/internal/tuitheme` | Extract `colorToHex` and `relativeLuminanceHex` from `termenv` dependency. | 1 hr |
| 6 | `cmd/serf-hub/internal/appsource` | Add constructor that accepts `*appwire.Client` so tests can inject `appwiretest.ScriptedTransport`. | 3 hrs |
| 7 | `appwire` | 56.3% — extract pure serialization from network calls. | 4 hrs |

**Phase 3 total:** ~21 hours, adds ~2,000 statements of coverage, lifts overall by ~8-10%.

### Phase 4: Core Agent Module (Week 4)

The agent module is 82.7% — already respectable. The biggest gaps are `ParseAgent` (70 stmts), `addDecisionToSchema` (34 stmts), and `AppendAPICall` (21 stmts). These are complex functions that may need careful table-driven tests with large fixture data.

| Priority | Target | Why | Estimate |
|----------|--------|-----|----------|
| 1 | `agent/plugin` (56.9%) | `ParseAgent` is the single largest uncovered function. Table-driven with agent YAML fixtures. | 4 hrs |
| 2 | `agent/provider` (60.2%) | `addDecisionToSchema` and `replaceCommunicateOutputSchema` are schema transforms. | 3 hrs |
| 3 | `agent/transcript` (50.7%) | `AppendAPICall` is a transcript builder. | 2 hrs |
| 4 | `agent/internal/jobstore` (77.2%) | `FoldOrdered` is a pure fold over job records. | 1 hr |
| 5 | `agent/execenv` (78.8%) | `Glob` is directory globbing. | 1 hr |
| 6 | `agent/internal/contextmgr` (85.9%) | `ElicitNote` is a context manager message. | 1 hr |
| 7 | `agent` (root, 85.5%) | `LoadSessionHistoricalJobRecords` and `resultSizeNote`. | 2 hrs |

**Phase 4 total:** ~14 hours, adds ~200 statements of coverage, lifts agent module to ~90%.

### Phase 5: Near-100% Polish (Ongoing)

The 11 packages at 80-99% need 365 statements total to reach 100%. These are mostly error paths:

| Package | Stmts to 100% | Easy Wins |
|---------|---------------|-----------|
| `hostlock` | 2 | `AcquireLock` error path |
| `hubedge` | 9 | `AuthGuard`, `LoadOrCreateAuthToken` errors |
| `mcpstatus` | 5 | `ProbeMCPStatus` error path |
| `cliprompt` | 1 | `Read` error path |
| `binresolve` | 4 | `SiblingDir` error path |
| `launchcheck` | 18 | Validation errors |
| `appprojector` | 39 | `Project` edge cases |
| `appserver` | 36 | `WireError`, `ServeWebSocket` errors |
| `launchconfig` | 98 | `SaveLayer`, `SaveMeta` errors |
| `hubcore` | 118 | `Rebuild`, `searchFTS`, `open` errors |
| `toolsummary` | 33 | `SummarizeTool` edge cases |

**Phase 5 total:** ~8 hours, adds 365 statements, lifts overall by ~1-2%.

---

## 3. What NOT to Test

These are intentionally left uncovered. Testing them would be mock theatre, not engineering.

| Package | Reason |
|---------|--------|
| `tools/tool-fluency/cmd/serf-fluency` | `main` package that spawns binaries and calls live LLM providers. Integration tool. |
| `cmd/llmcall` | `main` package with live provider calls, stdin, signals. |
| `cmd/serf-tui/internal/clipboard` | OS integration (xclip, wl-paste, osascript). The covered `clipboard_paste.go` has the testable logic. |
| `cmd/serf-docscheck` | Lint tool; tested by `make lint-generated` in CI. |
| `internal/appwiredoc` | `go generate` tool; tested by CI doc-check. |
| `internal/bundled` | `//go:embed` only; tested by compilation. |
| `internal/httpguard` | Security middleware; actually should have tests, but it's tiny. |
| `test/scenarios` | Integration tests in `test/` directory; run separately. |

---

## 4. Testing Best Practices Assessment

### Strengths
- **Hand-rolled test doubles, not frameworks.** The codebase uses `stubProbeAdapter`, `fakeAdapter`, `spyStrategy` — simple structs implementing interfaces. This is exactly right.
- **Race tests.** `*_race_test.go` files show deliberate race-condition testing.
- **Parallel tests.** ~75 test functions call `t.Parallel()`.
- **Test helpers are well-separated.** `communicate_test_helpers_test.go`, `testkit_test.go`, etc.

### Weaknesses
- **Only 6 of ~100+ test files use `package agent_test`.** The rest test internals, making the tests fragile to refactoring. Target: 30% external-package tests.
- **Table-driven tests are underused.** ~16 files use tables; many more have dozens of individual `TestFoo` functions. Target: use tables for any function with 3+ test cases.
- **Some packages have no tests at all.** 11 packages in `go list ./...` have zero test files.

### Remediation
1. For new tests, default to `package foo_test` unless testing unexported behavior is the explicit goal.
2. When adding tests to existing files, refactor individual `TestFoo`/`TestBar` functions into tables where the test logic is identical.
3. Add a `make coverage` target that fails CI if coverage drops below the current baseline (71.1%).

---

## 5. Expected Outcome

| Phase | Hours | Coverage Lift | Cumulative Coverage |
|-------|-------|---------------|---------------------|
| Baseline | — | — | 71.1% |
| Phase 1 (Pure logic) | 10 | +2-3% | 73-74% |
| Phase 2 (FS/HTTP) | 23 | +5-7% | 78-81% |
| Phase 3 (Extract) | 21 | +8-10% | 86-91% |
| Phase 4 (Agent core) | 14 | +2-3% | 88-94% |
| Phase 5 (Polish) | 8 | +1-2% | 89-96% |
| **Total** | **76** | **+18-25%** | **89-96%** |

**Realistic target:** 85% overall with 64 hours of focused work. The 90%+ numbers require testing `main()` packages and OS integration, which violates the "Rob Pike proud" principle.

The key insight: **most uncovered code is error paths.** Covering `if err != nil` branches is mechanically easy, high-value, and requires no refactoring. That's where the first 10% of coverage lift comes from.
