# Audit: Go tests under agent/** (slice go-tests-agent)

Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no test suites were run.
All counts below come from `rg`/`wc`/`awk` over `agent/**/*_test.go` at this commit.

## Inventory

| Measure | Value |
| --- | --- |
| `*_test.go` files under `agent/` | 1,574 |
| Lines of test code | 538,036 |
| `func Test*` | 11,815 |
| `func Fuzz*` | 302 (172 files carry `//go:build evenerfuzz`) |
| `func Benchmark*` | 31 |
| `t.Parallel()` calls | 7,360 |
| `time.Sleep` calls | 81 |
| `t.Skip*` calls | 328 |
| `strings.Contains(` calls | 4,045 |

Per directory (files / lines): root `agent` 957 files; `internal/` 250 files / 74.6K lines
(contextmgr 16.7K, jobstore 14.6K, tool 13.9K, delegatestore 5.5K); `execenv` 132 / 33.3K;
`sandbox` 48 / 15.8K; `doctor` 38 / 12.5K; `schema` 28 / 3.2K; `transcript` 25 / 4.8K; `plugin` 19 / 4.3K.

Root package by filename prefix: `session_*` 481 files, `delegate_*` 135, `cov_*` 64, `job_*` 61,
`jobs_*` 32, `transcript_*` 31, `fuzz*` 27, `memory_*` 22.

Families by filename (agent tree):

- Fuzz-named files: 239 files / 65.2K lines (49 `program_fuzz` files / 22.4K lines; 7 `seqfuzz` / 3.5K).
- Coverage-wave files (`cov_*`, `*covtest*`, `s4cov_*`, `*_coverage_*`): 211 files / 42.1K lines /
  1,632 `Test` funcs.
- `seed100` files: 17 files / 2.8K lines.

Largest files: `session_ask_test.go` 5,220; `session_attention_test.go` 5,048;
`delegate_resource_supervision_test.go` 4,395; `session_dod_definition_test.go` 3,908;
`transcript_test.go` 3,737; `session_retirement_evidence_test.go` 3,650;
`session_tools_transcript_branches_test.go` 3,531; `internal/contextmgr/context_manager_test.go` 3,487;
`session_memory_test.go` 3,482; `execenv/glob_budget_test.go` 3,096.
87 functions exceed 150 lines; 5 exceed 300 (`TestToolCallWireFixtures` 448,
`FuzzRuntimeBoundaryEdges` 394, `TestSkillsLive` 382, `runSecurePathEdgeContractProgram` 381,
`runProcessRuntimeProgram` 348). Most of the long ones are fuzz program runners or wire-fixture tables;
I did not find them to be a separate defect beyond the findings below.

What is healthy: exact duplicate function bodies (>=8 lines) are rare (20 groups repo-wide in this
slice); sleeps are few and the `waitForCondition` / `TRIPWIRE` timeout convention is applied
consistently; `deadline_audit_test.go` and `copyonly_audit_test.go` already guard two classes of
mistake; live tests are opt-in (`liveeval.OptInEnv`); `agenttest` and `testkit_test.go` exist as the
right consolidation points. The debt is that those consolidation points were never finished, and
that most of the volume was added under coverage-percentage pressure.

Product-model note: these are tests only, so the product ownership rules (recovery over lockdown,
shared client derivations) do not constrain the fixes. The binding constraints are AGENTS.md
Testing (no copy as oracle, deterministic default tests, `make test` CI-first) and
`docs/developing-evener/testing.md`, `coverage.md`, `fuzzing.md`. The coverage ratchet
`scripts/coverage/coverage-floors.txt` (agent 94.7) is a constraint on deleting tests; see finding 5.

---

## Finding 1 (high): Three parallel scripted-adapter families; 59 hand-rolled `Complete` fakes

Locations:
- `agent/session_test.go:36-77` `fakeAdapter`; `:183-223` `fakeErrAdapter`; `:225-238` `blockingAdapter`; `:240-` `closeRaceAdapter`
- `agent/internal/agenttest/agenttest.go:29-92` `FakeAdapter`, `:95-146` `ModelTrackingAdapter`
- `agent/internal/agenttest/scripted_adapter.go:20-67` `ScriptedAdapter`
- `agent/session_script_adapter_test.go:21-60` `sessionScriptAdapter`
- `agent/session_dod_test.go:27-60` `errAdapter`, `flaky429Adapter`
- `agent/internal/hooks/clientadapter_test.go:72-82` `fakeProviderAdapter`

Evidence:
- Root `fakeAdapter.Complete` (session_test.go:56-71) is line-for-line the same logic as
  `agenttest.FakeAdapter.Complete` (agenttest.go:44-60): append request, run `steps[i]` or return
  `llm.Assistant("done")`, stamp `Provider`/`Model`. Same for `Requests()`, `LiveModels`. Only the
  field names differ (`name/steps/liveModels` vs `Provider/Steps/LiveModelsFunc`).
- `&fakeAdapter{` appears 911 times in 221 files; `agenttest.FakeAdapter{` 23 times;
  `agenttest.ScriptedAdapter{` 139; `fakeErrAdapter{` 91; `newSessionScriptAdapter(` 11.
- 59 `Complete(` methods on test types across `agent/**/*_test.go` (47 in the root package), 43 of
  which repeat `return nil, llm.ErrStreamUnsupported` for `Stream`. Examples:
  `blockingAdapter`, `closeRaceAdapter`, `errAdapter`, `flaky429Adapter`, `modelAvailabilityAdapter`,
  `barrierFallbackAdapter`, `temperatureSpyAdapter`, `deferredHeadAdapter`, `heldLegAdapter`,
  `retirementAttentionAdapter` (about 50 `*Adapter` struct types in test files).
- Drift: `internal/hooks/clientadapter_test.go:80-82` `Stream` returns `nil, nil` while every other
  fake returns `llm.ErrStreamUnsupported`; a client that probes Stream would nil-deref on that one.
- `agenttest.ScriptedAdapter`'s doc comment (scripted_adapter.go:10) contrasts itself with
  "FakeAdapter (a fixed Steps slice)" while the root package keeps using its own private copy of
  that FakeAdapter.

Impact: any change to the adapter contract (new optional interface such as
`PlanResponsesContinuation`, `LiveModelLister`) must be taught to 3 families plus ~40 one-offs.
The root package already pays this: `agenttest.FakeAdapter` grew `PlanResponsesContinuationFunc`
that the private copy lacks.

Fix plan:
1. Add to `agenttest`: `AdapterFunc{Provider string; Fn func(ctx, llm.Request) (llm.Response, error)}`
   (Name/Complete/Stream-unsupported in one place) and an embeddable `NoStream` struct. Rewrite
   one-off adapters whose only unique behavior is the Complete body (`errAdapter`, `flaky429Adapter`,
   `blockingAdapter`, `closeRaceAdapter`, `hooks.fakeProviderAdapter`, ~25 others) onto it.
2. Extend `agenttest.FakeAdapter` with an error-returning step form (absorbs `fakeErrAdapter`,
   91 literals), then replace root `fakeAdapter`/`fakeErrAdapter` with it. Mechanical rewrite with a
   small Go AST script (rename composite-literal keys `name`->`Provider`, `steps`->`Steps`,
   `liveModels`->`LiveModelsFunc`); keep a one-line `type fakeAdapter = agenttest.FakeAdapter` only
   during the migration PR and delete it in the last PR (no lasting compat shim).
3. Keep `sessionScriptAdapter` (distinct per-session semantics) but move it into `agenttest`.
4. Behavior-preserving; guard is the existing suite. Do the rename in CI, not locally (see
   memory rule: tests run in CI).
5. Docs: add a "which fake to use" paragraph to `docs/developing-evener/testing.md`.

Size: ~-1,500 lines net (adapter bodies) plus ~2,500 touched lines of mechanical rename. Risk: low
per PR; large diff, so split by file group. Cross-cutting with findings 2 and 3.

---

## Finding 2 (medium): Response/tool-call builders reinvented with drift (`communicateCall`, `finalResponse`, `toolCallResponse`)

Locations:
- `agent/communicate_test_helpers_test.go:10-35` `communicateResponse`, `:75` `finalResponse`
- `agent/session_communicate_test.go:24-60` `communicateCall`/`communicateCallArgs` (has `end_reason`)
- `agent/internal/agenttest/agenttest.go:153-241` `CommunicateCall`, `CommunicateCallArgs`, `ToolCallResponse`, `CommunicateResponse`, `FinalResponse`
- `agent/internal/contextmgr/testhelpers_test.go:15-33` `communicateCall` ("mirrors the agent package's test helper")
- `agent/doctor/sessions_test.go:81` another `communicateCall` (different signature)
- `agent/tool_web_fetch_test.go:111-119` `toolCallResponse` (identical to `agenttest.ToolCallResponse`)
- `agent/session_task_effort_test.go:32` `taskEffortToolCall`, `agent/delegate_fork_context_test.go:315` `forkContextToolCall`,
  `agent/session_tools_ask_covtest_test.go:260` `askToolCallContent`, `agent/salvage_test.go:14` `toolCallPart`,
  `agent/sandbox_delegate_live_test.go:34` `liveDelegateToolCall`

Evidence:
- `communicateResponse` (root) and `agenttest.CommunicateResponse` are byte-identical bodies
  (same `communicate_test_call` id, same output skeleton). `finalResponse(` is called 642 times in
  the root package vs `agenttest.FinalResponse` 63 times.
- Drift: root `communicateCallArgs` forwards `end_reason` (session_communicate_test.go:58-59);
  `agenttest.CommunicateCallArgs` (agenttest.go:159-197) does not, so a test that needs
  `end_reason` cannot use the shared helper and the two diverge on the wire shape the real
  communicate tool accepts.
- 172 inline `Kind: llm.ContentToolCall` literals in 61 root files and 696 `llm.ToolCallData{` literals
  (e.g. `session_dod_definition_test.go:30-45` builds `toolMsg` inline).

Impact: the communicate wire shape is the most-asserted contract in the agent tests; it is encoded
in four places that can disagree.

Fix plan:
1. Make `agenttest` canonical: add `end_reason` to `CommunicateCallArgs`, add `ToolCall(id, name, args)`
   and `ToolResult(...)` part builders.
2. Delete root `communicateResponse`, `finalResponse`, `communicateCall*`, `toolCallResponse`,
   `contextmgr.communicateCall`; replace call sites (mechanical, `gofmt -r` handles function-call
   renames; 190 `communicateCall(` + 67 `communicateResponse(` + 642 `finalResponse(`).
3. Replace the 5 per-file tool-call builders with the shared one.
4. Keep `wrapCommunicateResponse`/`communicateWithStructured` as thin callers of the shared builder.

Size: ~-250 lines, ~900 call-site lines touched. Risk: low.

---

## Finding 3 (high): `testkit_test.go` fixture adoption stalled; 884 hand-rolled `NewSession` dances and ~94 per-file session factories

Locations:
- `agent/testkit_test.go:19-30` (stated purpose), `:110-175` `newSession`
- `agent/session_dod_definition_test.go:61,110,168,219,...` (65 `NewSession(`, 0 `newSession(`)
- `agent/session_config_test.go` (40 vs 1), `session_model_test.go` (34 vs 3), `session_ask_test.go` (49 vs 18)
- Per-file factories: `newTestSessionForState` (session_awaiting_test.go:21), `newTestSessionWithPlugins`
  (session_slash_command_test.go:350), `newTestSession` (subagents_test.go:24), plus 91 others named
  `new*Session|Harness|Fixture`

Evidence:
- `testkit_test.go:21-27` says: "Hundreds of tests need a *Session... Before this file each test
  hand-rolled those: the full llm.NewClient()/Register/NewSession/err-check/Close dance... These
  helpers centralize the vocabulary so a signature change touches one place."
- Today: `newSession(t` 833 uses, but raw `NewSession(` is still called 884 times in 217 root test
  files, `llm.NewClient()` 1,044 times, and `SessionConfig{` literals 1,736 times. Typical block
  (session_dod_definition_test.go:61-66): `NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")),
  execenv.NewLocalExecutionEnvironment(dir), SessionConfig{...})` then `t.Fatalf` and a manual `sess.Close()`
  (often forgotten; `newSession` registers `t.Cleanup`).
- Three differently named session constructors (`newTestSession`, `newTestSessionForState`,
  `newTestSessionWithPlugins`) wrap the same call with different defaults.

Impact: exactly the coupling the kit was written to remove. `NewSession` signature or
`SessionConfig` seam changes touch 217 files; tests that forget `Close` leak sessions.

Fix plan:
1. Finish the migration file-by-file starting with the five largest users, converting to
   `newSession(t, withAdapter/withSteps/withConfig/...)`. Each conversion is behavior-preserving;
   the diff is the guard. Add `withTestSessionNamer`-style defaults inside `newSession` (already there).
2. Where a per-file factory is a pure wrapper (the three `newTestSession*`), delete it and inline
   `newSession(t, withConfig(...))`.
3. Add a lint-style audit modeled on `deadline_audit_test.go` that fails a new root test file calling
   `NewSession(` directly outside `testkit_test.go` and an allowlist (files that deliberately test
   `NewSession` itself, e.g. `session_lifecycle_test.go`, `cov_w3init_session_test.go`). Allowlist
   shrinks to zero as migration lands. (An audit, not a new selftest script.)

Size: ~-4,000 lines net, ~10K touched. Risk: low-medium (cleanup ordering and shared workspace
`sharedSessionWorkspace` behavior differ from per-test `t.TempDir()`; convert tests that depend on a
private dir with `withDir`). Ship in ~10 PRs by file group.

---

## Finding 4 (high): 543 error-message phrase assertions in 133 files violate "Copy Is Not a Test Oracle"; the audit does not catch them

Locations (top): `agent/session_tools_transcript_branches_test.go` (71 `strings.Contains(err.Error()`),
`session_tools_jobs_covtest2_test.go` (31), `session_tools_worktree_errors_test.go` (30, e.g. `:337`),
`job_watch_config_test.go` (27, e.g. `:29`), `internal/delegatestore/store_covtest_test.go` (27),
`internal/delegatestore/fold_covtest2_test.go` (26), `transcript_lookup_test.go` (18),
`session_tools_worktree_remove_test.go` (17), `job_watch_branches_test.go` (15, `:399`, `:417`).

Evidence:
- 785 `strings.Contains(err.Error(), ...)` in 190 files; 543 of them match a multi-word literal in 133
  files. Examples:
  - `session_tools_worktree_errors_test.go:337` `if !strings.Contains(err.Error(), "is not a managed worktree")`
  - `plugin_agents_integration_test.go:575` `"delegation not permitted: your delegation_allowance is 0"`
  - `job_watch_config_test.go:29` `"progress_interval_ms must be non-negative"`
  - `session_tools_transcript_branches_test.go:1481` `"max_bytes requires expand_turn or an explicit API body"`
  - `session_tools_worktree_switch_test.go:494` `"inspecting the current worktree lock"`
- 19 exact-equality forms `err.Error() != "..."`, e.g. `delegate_tree_attention_branches_test.go:559`
  `"delegate attention controller is nil"`, `session_lifecycle_tail_coverage_fuzz_test.go:77`.
- `docs/developing-evener/testing.md:62-72` names this exact pattern as banned:
  ``strings.Contains(err.Error(), "not found")``. The two audits (`copyonly`) only flag tests whose
  names say "shows/renders/says", so these slip through. 229 of the 785 are in coverage-wave files.

Impact: every wording tweak to an error breaks tests that carry no behavioral claim; the rule in
AGENTS.md is violated at scale by LLM-written tests.

Fix plan (per AGENTS.md: assert the right error, not the phrase):
1. For errors that have a sentinel, use `errors.Is`. For the many that are `fmt.Errorf("...")` without
   one, introduce a sentinel or typed error in the production code where a caller already distinguishes
   it (worktree refusals, watch-config validation, transcript read argument validation), and
   `errors.As` for typed ones. Where no caller branches on the error, assert `err != nil` plus the
   side effects (nothing written, state unchanged) rather than adding sentinels for tests' sake (YAGNI).
2. Convert in descending order of file count (the top 10 files hold ~270 of the 543).
3. Add a ratchet: extend `internal/devtool/copyonly` (or a sibling audit) with a rule flagging
   `strings.Contains(err.Error(), "<multiword literal>")` and `err.Error() != "<literal>"` in tracked
   Go tests, with an allowlist generated from today's violations that can only shrink.
4. Docs: no new doc; `testing.md` already states the rule; note the new audit rule next to the existing two.

Size: ~-600 / +900 lines in tests, ~150 lines of production sentinels. Risk: low for tests; the
sentinel additions are small, but each one is a production API choice that Jesse should skim.

---

## Finding 5 (medium): Coverage-wave test files organized by sprint label, with stale line-number comments

Locations: `agent/cov_s1_*` ... `cov_s5_*`, `cov_w2*_`, `cov_w3*_` (64 files), `agent/*covtest*_test.go`
(`session_tools_jobs_covtest2_test.go` 1,540 lines, `watch_pure_covtest_test.go` 971,
`job_watch_covtest2_test.go` 922, `delegate_runtime_covtest4_test.go` 786), `agent/s4cov_*`,
`agent/internal/delegatestore/*covtest*`.

Evidence:
- 211 files / 42.1K lines / 1,632 test funcs. Test names carry worker/wave labels:
  `TestS3Cov_*` 59, `TestCov*` 33, `TestW2Tail*` 32, `TestS2Cov_*` 31, `TestW3Sub*` 24, `TestW3Init*` 23,
  `TestS1Cov_*` 22, `TestS5Cov_*` 18 (503 test names contain `Cov` overall). Helper names embed the same
  labels: `s1cov_writeJobLog`, `s4covWriteFile`, `w2tailWriteTranscript`, `w3init_writeOversizeLine`.
- Files are organized by when/which agent wrote them, not by subject: `jobs_cov_test.go`,
  `jobs_covtest2_test.go`, `jobs_pure_covtest_test.go`, `job_watch_covtest2_test.go`,
  `job_watch_cov_test.go` all test the job manager; `delegate_runtime_covtest2/3/4` test one runtime.
- 466 comments of the form `covers X (file.go lines N-M)` in 64 files; they rot. Verified stale:
  `session_tools_jobs_covtest2_test.go:18-20` says `clearDescendantReceiverWatchByID` is at
  `session_tools_jobs.go lines 298-300` but the function is declared at `session_tools_jobs.go:370`;
  lines 296-300 now hold an unrelated loop over `watchHolderSessions()`. `:38` cites lines 129-134 for
  `liveSteerWaitIgnoredReason`, whose declaration is at roughly line 131 with its doc comment above, so even
  the close references are approximate.
- Some are fine behavioral tests (`cov_s3_misc_test.go:10-23` defensive-copy check); the problem is
  the layout, naming and the narration, not every test.
- `scripts/coverage/coverage-floors.txt`: `agent 94.7` ("the number worth driving toward 100%"). This
  is why the files exist; deleting tests lowers the ratchet.

Impact: contributors cannot find where behavior X is tested; line refs mislead; wave labels carry no
meaning once the swarm that wrote them is gone.

Fix plan:
1. Do not delete tests (the floor is a hand-edit down-ratchet; Jesse must approve any lowering).
2. Strip all `(file.go lines N-M)` parentheticals and wave prefixes; rename `TestS3Cov_ToolDefinitions`
   to subject names (e.g. `TestSessionToolDefinitionsReturnsCopy`). Scripted with a Go AST rename.
3. Merge each `*covtest{,2,3,4}` file into the subject's main test file (or one `<subject>_branches_test.go`
   per prod file), at most one extra file per production file. Order: jobs, job_watch, delegate_runtime, session_tools_jobs, watch.
4. Update `docs/developing-evener/coverage.md` with the rule "name a test for the behavior, never for the wave
   or the line it covers".

Size: ~-1,200 lines of comments, ~42K lines moved. Risk: low (pure moves/renames) but huge diff. Because
`TestMain`/parallel tests share package globals, run the rename PRs through CI before merging.

---

## Finding 6 (medium): Nil-receiver and trivia tests pad coverage and lock in over-defensive production guards

Locations: `agent/nil_guards_covtest_test.go:13-70` (259 lines), `agent/cov_s2_getters_test.go:11-39`,
`agent/cov_s2_trivia_test.go:11-25`, `agent/session_attention_cov_test.go:95-110`,
`agent/delegate_runtime_covtest2_test.go:86-100`, `agent/subagents_branches_test.go:575-595`,
`agent/delegate_tree_attention_branches_test.go:471-480`.

Evidence:
- 272 `func Test*Nil*` and 100 `var x *Session` declarations in tests. Examples:
  `TestCovArmDelegateAttention_NilSession` and `_EmptyID` both pin the same literal error text
  `"delegate attention wake identity is incomplete"` (`nil_guards_covtest_test.go:19,31`) for
  `var s *Session` / `&Session{}`; `TestCovDrainSteering_Empty` calls `(&Session{}).drainSteering()`.
- Weak oracles: `cov_s2_getters_test.go:24-25` `if sess.ContextPressure() < 0 { Fatalf }`; `sess.Client() == nil`;
  `TestS2Cov_MutatorsAfterClose` only asserts no panic. `session_attention_cov_test.go:95` and
  `delegate_runtime_covtest2_test.go:86` contain no assertion at all (call and return).
- Production code carrying guards for states no caller reaches (`s == nil` on `*Session` methods, nil-child
  binders) exists only because a test constructs `&Session{}` / nil to execute the branch.

Impact: the tests assert nothing a real caller depends on and will force future refactors to keep dead
guards. They are also the largest reason the zero-value `&Session{}` stays a supported shape.

Fix plan:
1. For each nil-guard test, ask whether any production caller can pass nil. If not, delete the guard in
   production and the test together (the floor drops by a few statements; call it out to Jesse since
   `coverage-floors.txt` is a hand-edit down-ratchet).
2. If a guard is reachable, keep one table-driven test per method family asserting the user-visible outcome.
3. Remove the assertion-free tests outright; replace `>= 0` oracles with a real expectation or delete.

Size: ~-1,200 test lines, ~-150 production lines. Risk: medium: requires reading each guard's callers.
Needs Jesse's call on the coverage floor before executing.

---

## Finding 7 (medium): "Fuzz" targets that ignore their input and replay 60 `TestXxx` functions; `seed100` files named for a percentage

Locations:
- `agent/session_init_seed100_exact_fuzz_test.go:26-28,29-99` `FuzzSessionInitSeed100Exact` (`f.Add(byte(0))`, `func(t, _ byte)`)
- `agent/jobs_seed100_fuzz_test.go:24-45` `runAgentJobsSeed100`, called from `agent/job_manager_error_recovery_fuzz_test.go:35`
- Same shape: `session_tools_worktree_seed100_exact_fuzz_test.go`, `session_init_worktree_seed100_fuzz_test.go`,
  `job_shell_seed100_fuzz_test.go`, `session_queue_stream_tail_fuzz_test.go`, `session_go_tail_coverage_fuzz_test.go`,
  `transcript_read_tools_exact_fuzz_test.go`, `session_tool_round_tail_coverage_fuzz_test.go`,
  `session_tools_aux_exact_fuzz_test.go`, `sandbox/fuzz_contract_structural_test.go` (10 files with `_ byte`/`_ []byte`)
- `jobs_seed100_more_test.go`, `jobs_seed100_range_a/b/c_test.go`, `session_tools_jobs_seed100_range_a..d_test.go`, `*_final_test.go`

Evidence:
- `FuzzSessionInitSeed100Exact` does `t.Run(name, TestInitPlugins_...)` for ~60 existing `Test*` funcs
  (hook_warnings_test.go:55 etc.) regardless of fuzz input. The fuzzer mutates a byte that nothing reads.
  `runAgentJobsSeed100(t, text)` calls 16 deterministic `seed100*` helpers; only `text` (truncated to 32 bytes)
  varies, and only in `seed100Jobs`/`seed100JobTools`.
- 17 `seed100` files / 2,841 lines with names `range_a/b/c/d`, `more`, `final`: positional/history names that say nothing.
  254 discarded results (`_ = `/`_, _ =`) vs 200 assertion calls across them.
- Those `Test*` functions already run in the untagged test track, so replaying them in the fuzz track
  does not add to the union number that `coverage.md` describes; CI's `make fuzz` simply runs them twice.

Impact: misleading fuzz inventory (the 302 `Fuzz*` count includes ~10 non-fuzzers), duplicated CI time,
and the `seed100` naming encodes a coverage goal rather than behavior.

Fix plan:
1. Delete the replay loops in the 10 input-ignoring targets (drop `Test*` re-invocations; keep any
   uniquely fuzz-only helper such as `fuzzSessionInitFaultBoundaries` as ordinary `Test*`).
2. For `seed100*` helpers that are not also standalone tests, convert them to named `Test*` funcs grouped by subject
   (jobs lifecycle, tools jobs, worktree) and drop `range_a/b/c/more/final`.
3. Verify with `make coverage-floor` in CI that the union number is unchanged (expected: unchanged, since the same
   `Test*` run in the test track).
4. Update `docs/developing-evener/fuzzing.md` to say a `Fuzz*` target must consume its input.

Size: ~-2,000 lines of replay scaffolding; ~2.8K lines renamed. Risk: low if the union is verified in CI.

---

## Finding 8 (medium): 21 copies of the byte-program reader in fuzz harnesses; a shared `ByteSource` already exists

Locations: `agent/session_model_stream_fuzz_test.go:52-73` `msfzReader`; `watch_attach_terminal_program_fuzz_test.go:40`
`watpReader`; `watch_restore_clear_history_program_fuzz_test.go:37` `wrcReader`; `session_turn_completion_program_fuzz_test.go:57,402`
`tfpReader`, `sefReader`; `session_tools_shell_program_fuzz_test.go:227` `stpReader`; `job_runtime_recovery_program_fuzz_test.go:69`
`jrrpReader`; `session_persistence_init_fuzz_test.go:126` `pifReader`; `watch_config_validation_program_fuzz_test.go:45` `wcvpReader`;
`root_watch_tree_program_fuzz_test.go:42` `rwlpReader`; `session_tools_jobs_fuzz_test.go:52` `jobtools_reader`;
`watch_observer_fuzz_test.go:42` `wobs_reader`; plus `stmReader`, `wpfpReader`, `jtlpReader`, `tdrpReader`, `sierReader`,
`statefulToolProgramReader`, `stweb_reader`, `seqReader`, `plugin/loader_program_fuzz_test.go` and others.

Evidence: 21 files in `agent/` (28 repo-wide) define `func (r *xReader) next() byte`, each
`{data []byte; pos int}` returning 0 on exhaustion, with copies of `intn` (`int(r.next()) % n`), `bool`, `pick`, `word`.
`msfzReader.next` and `watpReader.next` are identical. A byte-backed `Source` already exists:
`fuzz/schemagen/source.go` (`NewByteSource`, `Bool/Intn/IntRange/String`), written so generators work with `testing.F`.
Per-file name prefixes (`msfz`, `watp`, `wrc`, `tfp`...) make greps for the shared idea impossible.

Impact: ~21 x ~20 lines of copy-paste; behavior differences (`%2 == 0` vs `&1 == 1` for `bool`) are accidental
and change what each corpus means.

Fix plan: move a minimal `ByteReader` (Next/Intn/Bool/Pick) next to `fuzz/schemagen.ByteSource` (or reuse it
directly), delete the 21 per-file readers, keep file-local helpers that build domain values. Committed corpora under
`testdata/fuzz` stay valid only if `Bool`/`Intn` semantics match per file, so port one file at a time and run the
fuzz replay lane in CI (`make fuzz`) per PR; where a file's `bool` convention differs, keep it as a one-line local method.

Size: ~-400 lines. Risk: low-medium (corpus semantics).

---

## Finding 9 (medium): Git repo fixtures duplicated; whole "maintenance probe" and repo-template machinery copy-pasted

Locations:
- `agent/session_tools_worktree_create_test.go:64-162` (`worktreeMaintenanceProbe`, `worktreeMaintenanceAutoEnabled`, `worktreeQuiescentCommitArgs`, `run`, `published`), `:313-345` `buildWorktreeBaseRepo`
- `agent/internal/worktree/predicates_test.go:88-168` (same code renamed `predicate*`), `:196-228` `buildPredicateRepoTemplate`
- Git runners: `agent/sandbox_infra_test.go:326`, `session_env_swap_test.go:21`, `sandbox_delegate_test.go:30`, `session_memory_test.go:1377-1384`,
  `project_docs_test.go:80`, `plugin_revision_preservation_test.go:27`, `sandbox/gitdir_test.go:15`, `internal/worktree/porcelain_test.go:15-37`,
  `execenv/gitpath_mainroot_test.go:59`, `execenv/gitpath_test.go:10`, `execenv/sandbox_escape_test.go:328`, `internal/tool/apply_patch_sandbox_test.go:250`

Evidence: the `worktree`/`predicate` pairs are exact duplicates (md5 of normalized bodies equal for
`worktreeMaintenanceAutoEnabled` 24 lines, `published` 10 lines, `worktreeQuiescentCommitArgs` 8 lines; `run` differs only in names);
`TestWorktreeFixturePublishesAfterSeedMaintenance` vs `TestPredicateFixturePublishesAfterSeedMaintenance` and
`...SetupErrorRetainsCleanupRoot` pairs duplicate the tests of the fixture too. 12 distinct git-runner helpers with different
env scrubbing (`GIT_CONFIG_GLOBAL` handled in 4 ways, one uses `HOME=dir`, one `protocol.file.allow=always`, identity strings `Test`/`t`/`t@e`).
Those tests also test test infrastructure with history comments ("the round-30 run", `session_tools_worktree_create_test.go:353-361`).

Impact: a git behavior (e.g. `maintenance.auto`, a new default `init.defaultBranch`) fix must be applied to two probes and a dozen runners; hermeticity varies per helper.

Fix plan: add `agent/internal/gittest` (non-test package under `internal/`, imported only from tests) with `Run(t, dir, args...)`
(hermetic env: `GIT_CONFIG_GLOBAL/SYSTEM=/dev/null`, fixed identity), `Init(t, dir)`, a cached base-repo template + copy
helper, and the maintenance probe. Port `internal/worktree` and the agent root worktree tests first (deletes ~190 lines of probe),
then the single-use runners. Keep the two "fixture publishes after seed maintenance" tests as one test in `gittest`.

Size: ~-450 lines. Risk: low-medium (hermeticity changes can expose latent test dependence on the dev's git config; verify in CI).

---

## Finding 10 (medium): Transcript/session-log fixture writers hand-rolled 133 times

Locations: `agent/session_tools_find_filters_test.go:42-61` `writeFindTranscript`; `transcript_tools_test.go:287-310,653-690,689-740`
`writeFindSession`, `writeMultiTurnSession`, `writeSessionWithToolTurn`; `cov_w2tail_transcript_read_test.go` `w2tailWriteTranscript`;
`transcript_lookup_test.go:51-61` `writeTranscript`; `s4cov_transcript_read_test.go:14-21` `s4covWriteFile`; `delegate_tree_restore_test.go:1027-1060`
`writeDelegateAttentionTranscript`/`appendDelegateAttentionTurn`; `jobs_activity_past_test.go:988` `writeRawSessionMeta`;
`transcript_test.go:2049` `writeTestAPILog`.

Evidence: 133 `transcript.NewWriter(` calls in 50 root test files. The same sequence `MkdirAll(filepath.Dir(tpath))` ->
`transcript.NewWriter(tpath, Header{SessionID, CreatedAt, ...})` -> loop `Append` with `t.Fatalf` per step -> `Close` appears in
`writeFindTranscript`, `writeFindSession`, `writeMultiTurnSession`, `writeSessionWithToolTurn`, and inline in dozens of tests.
`newBucket`/`newStateHome`/`newBucketUnder` exist (transcript_lookup_test.go:25-45) but live inside one file's test helpers.

Impact: any change to the transcript header/writer/durability contract touches 50 files; error handling differs (some leak the
writer on Append failure, e.g. `writeMultiTurnSession` does not close on error).

Fix plan: add to `testkit_test.go` (or `agenttest`) `seedTranscript(t, bucketDir, header, turns ...schema.Turn) string` and
`seedSessionMeta`, built once with correct close-on-error. Port the eight named helpers first, then the inline sites file by file.

Size: ~-800 lines. Risk: low.

---

## Finding 11 (medium): `execenv/glob_budget_test.go` is 3,096 lines of near-clone tests plus eleven copy-pasted `stub*` setters

Locations: `agent/execenv/glob_budget_test.go:24-72` (five stubs), `:2194-2500` (`TestLoadIgnoreSetRefuses*`), `:2650-2670`;
stubs also in `list_dir_budget_test.go:37-60`, `grep_readcap_test.go:19`, `grep_oversize_test.go:19`, `grep_truncation_test.go:100`,
`grep_pseudofs_test.go:17`, `glob_walk_test.go:253`, `glob_cancel_test.go:347`.

Evidence:
- 75 tests in one file. 18 are `TestLoadIgnoreSetRefuses*` with the identical body: write `.gitignore`, `stubMaxGlobIgnoreTotalBytes`,
  `newGlobBudget("glob")`, `boundedDirFS{FS: os.DirFS(root), budget: budget, ctx: t.Context()}`, `loadIgnoreSet`, `errors.AsType[*globBudgetError]`,
  `budgetErr.kind != budgetRulesTotal`, `len(set.dirs) != 0`. The `boundedDirFS` line appears 31 times; `want budgetRulesTotal` 21 times;
  the only variants are the rule text and the budget constant (compare `:2194-2290`, four consecutive tests differ in 2-3 lines).
- The file mixes three subjects: glob traversal budgets, gitignore discovery, and ignore-rule regex complexity limits.
- Eleven functions `orig := X; X = n; t.Cleanup(func() { X = orig })` (28 occurrences in 20 files), none generic.

Impact: 18 clones -> one table of (name, rules, budget); a change to `loadIgnoreSet` error shape touches 31 places.

Fix plan: (a) `refusesIgnoreRules(t, rulesText, totalBytes)` helper plus a table for the `Refuses*` cases; split the file into
`glob_budget_test.go`, `glob_ignore_discovery_test.go`, `ignore_rule_limits_test.go`; (b) add one generic
`overrideForTest[T any](t *testing.T, target *T, v T)` in `execenv` tests and delete the ten stubs. These tests mutate package globals
and cannot run parallel (see `docs/developing-evener/agent-test-serial-prefix.md`); the generic helper does not change that.

Size: ~-900 lines. Risk: low (behavior-preserving; each case keeps its name as a table row).

---

## Finding 12 (low): Review-round, phase and task narration in comments, file names and test names

Locations: `agent/session_openai_continuation_phase0a/4d/5b/9/10/12_*_test.go`, `agent/task4_token_budget_test.go`,
`agent/delegate_resource_create_test.go:1728` (`task6TranscriptBarrierAdapter`), `agent/session_skill_delivery_test.go:1277`,
`agent/session_tool_round_test.go:23`, `agent/notification_test.go:405`, `agent/memory_migrate_property_test.go:79`,
`agent/doctor/audit_test.go:170,235` ("fix-round-1 Important"), `agent/session_tools_worktree_create_test.go:353-361`.

Evidence: 74 comments reference review/fix rounds (`rg '(review|fix|CI|audit) round[- ]?N|round-N run'`), 226 `phase<N>` tokens,
97 `task<N>` tokens, 87 `Task N` and 16 `Row N` references, 592 `#NNN` PR/issue-number tokens. CLAUDE.md: name by domain,
history goes in the commit message. Names like `phase9RetryAdapter`, `newPhase9ContinuationSession`, `TestSession_OpenAIResponsesContinuationPhase9...`
bind to a plan document, not to behavior.

Fix plan: opportunistic. Rename `phase*`/`task*` identifiers and file names when a file is touched for findings 1-3, and drop
round/finding narration comments (keep only the "why" half). No standalone PR.

Size: ~-400 comment lines; ~40 renames. Risk: low.

---

## Finding 13 (low): Duplicated wait/poll helpers and avoidable real sleeps

Locations: `agent/poll_test.go:18-30` `waitForCondition`; `agent/session_lossless_events_test.go:785-794` `waitFor` (10s, 1ms poll);
`agent/tree_counter_test.go:20-44` `waitForTreeCount`/`waitForDriveCount` (identical to each other, hand-rolled loop);
`agent/job_shell_test.go:866` `waitForSignalCount` (wraps `waitForCondition`); `agent/execenv/detach_test.go:351` `waitForTestFile`.
Sleeps: `agent/jobs_activity_incremental_fold_test.go:190,237` and `agent/internal/foldcache/foldcache_test.go:174,240,277,531`
(`time.Sleep(1100 * time.Millisecond)` x6 to cross a 1s mtime tick); `agent/subagent_intent_report_test.go:255` (1,200 ms).

Evidence: three polling loops with different timeouts (2ms/1ms/10ms polls, 5s/10s/caller) next to the canonical `waitForCondition`;
6.6 s of mandatory serial wall time in six sleeps whose comment (`jobs_activity_incremental_fold_test.go:187-189`) says the sleep only
guarantees mtime moves forward.

Fix plan: delete `waitFor`, `waitForTreeCount`, `waitForDriveCount` in favor of `waitForCondition`; replace the 1.1 s mtime sleeps with
`os.Chtimes` to a later fixed time (deterministic, instant); replace the 1.2 s activity-window sleep with the fake clock that
`agenttest.NewFakeClock` already supplies in neighboring tests.

Size: ~-60 lines. Risk: low (foldcache tests are the only ones needing care about mtime resolution).

---

## Finding 14 (low): `package agent` `TestMain` lives in a feature test file

Locations: `agent/session_tools_worktree_create_test.go:164-250` (`TestMain`), `:1377` `TestMainAppliesTheShardRunFile`;
other packages use `testmain_test.go` (`mcpconfig/`, `plugin/`, `skill/`, `command/`, `execenv/`, 15-20 lines each, via `sandboxtest.Run`).

Evidence: the root package's global setup (HOME redirect, `GOCACHE` env capture, shardrun wiring, `fastGitDirForTest`, `sharedAgentTempRoot`,
`intgMCPServerDir`, cleanup) is ~90 lines inside a 1,380-line worktree test file, next to the worktree maintenance probe. Anyone looking for
package-wide test setup looks for `testmain_test.go` and does not find it.

Fix plan: move `TestMain`, `packageFixtureTempDir`, the shared-root globals and `TestMainAppliesTheShardRunFile` to `agent/testmain_test.go`.
Pure move, no logic change.

Size: ~100 lines moved. Risk: low.

---

## Finding 15 (low): Small helper copies across test packages

Evidence (md5-equal or near-equal bodies, locations):
- `mustJSON`: `agent/session_openai_malformed_tool_call_test.go:488` (returns string), `plugin/hooks_parse_fuzz_test.go:133`, `internal/contextmgr/compaction_seqfuzz_test.go:360` (panics), plus `mustMarshal` `provider/profile_overrides_fuzz_test.go:234`
- `writeFile`: `agent/plugin_integration_live_test.go:166`, `doctor/testhelpers_test.go:12`, `sandbox/seatbelt_live_test.go:503`
- `readTranscriptLines` (21-line identical): `agent/transcript_test.go:31` and `agent/transcript/transcript_test.go:18`
- `equalStrings` x4 (`profile_test.go:1387`, `fuzz_ct_tool_names_test.go:164`, `watch_restore_clear_history_program_fuzz_test.go:829`, `execenv/local_fsfuzz_test.go:288`) where `slices.Equal` exists
- `makePluginDir` (`plugin_support_test.go:11` and `plugin/plugin_test.go:202`), `namedOpenAIProfile` (`session_fallback_test.go:21` and `internal/cheapmodel/caller_test.go:27`),
  `scratchLeaseHeld` (`subagent_teardown_unix_test.go:26` and `execenv/sandbox_scratch_lease_unix_test.go:44`), `nestedOneOfParams`, `widgetSchema`, `marshalToMap`, `toolResultContent`, `captureStack`, `stmSeed`
- `TestClientWithHeaders_NilBase_InjectsHeaders` exists twice (24 lines): `internal/mcphttp/mcphttp_test.go:24` and `internal/mcp/cov_s5_headers_test.go:16`
- `mustNewSessionLog` in `agent/strategy_test_helpers_test.go:13` and `internal/contextmgr/testhelpers_test.go:87`

Fix plan: replace `equalStrings` with `slices.Equal`; delete the duplicate headers test; fold the rest into `agenttest` where more than one package uses them
(`ReadTranscriptLines`, `MakePluginDir`, `NamedOpenAIProfile`, `MustJSON`). Leave single-package copies alone.

Size: ~-250 lines. Risk: low.

---

## Recommended fix sequence for this slice

1. **Finding 1 then 2 (adapter and response-builder consolidation in `agenttest`).** Foundation; shrinks every later diff. Split by file group, rename via AST script, no lasting alias.
2. **Finding 14** (move `TestMain`): trivial, unblocks reading the package setup before the big migrations.
3. **Finding 3** (finish `testkit` adoption + direct-`NewSession` audit). Biggest line reduction; do after 1-2 so the files are touched once.
4. **Finding 4** (error-phrase assertions + ratchet audit). Can proceed in parallel with 3 per-file, but each PR should touch a disjoint file set. Needs Jesse's glance at the few production sentinels.
5. **Finding 10, then 9** (transcript and git fixture helpers) as they are used by the files already being migrated in 3.
6. **Finding 7** (input-ignoring fuzz targets, `seed100`): verify union coverage unchanged in CI before merging.
7. **Finding 8** (shared byte reader), **Finding 11** (glob budget table + generic stub), **Finding 13** (waits/sleeps).
8. **Finding 5 and 6** last, and only after Jesse decides the coverage-floor policy: the renames/merges (5) are safe; the nil-guard deletions (6) lower the agent floor (94.7) and delete production guards, which is a product-of-the-ratchet decision.
9. **Finding 12 and 15**: opportunistic, bundled into whichever PR touches the file.

Open question for Jesse: are we still driving `agent` coverage toward 100% via the floor ratchet? Findings 5, 6 and 7 are
the consequence of that goal; deciding it first determines whether they become deletions or only renames.
