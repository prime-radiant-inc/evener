# go-tests-rest: Go test files outside agent/**

Audit date 2026-10-25, worktree `.worktrees/debt-audit` on origin/main. Read-only; no suites run.

## Inventory

- 1802 `*_test.go` files outside `agent/`, 548,072 lines.
- `cmd/evener-hub` (top-level package `hub`): 759 test files under `cmd/evener-hub/` (463 directly in the package, 192,767 lines, about 1,180 helper funcs). `app_rpc_test.go` alone is 13,451 lines with 215 `Test` funcs and 28 helpers.
- Next: `cmd/evener` 107 files, `llm/providers` 94, `internal/plugins` 55, `internal/appserver` 35, `cmd/evener-dev` 33, `internal/apptranscript` 31, `auth/openai` 28, `llm/registry` 27, `internal/transcriptindex` 27.
- Largest files: `app_rpc_test.go` 13,451; `app_instances_test.go` 5,442; `plugins/marketplace_migration_test.go` 4,261; `sshconn/manager_test.go` 4,151; `sshconn/version_test.go` 3,811; `app_relay_test.go` 3,653.
- Largest single funcs: `TestCredentialProbeAccessTransitions` 719 lines (`app_credential_probe_transitions_test.go:346`), `TestAgentCascadeBrowser` 591, `skillGuardAssert` 448, `TestClientRequestWrappersRoundTrip` 445 (this one is a sound table).
- 152 files are named by history or coverage mechanics (`cov_*`, `*_gaps_*`, `*_coverage_*`, `round12`, `pass6`): 28,816 lines.
- 22 `TestMain` files outside agent. 93 types named fake/stub/scripted/mock/spy/recording. 54 `Complete(` methods that are `llm.ProviderAdapter` fakes in 40 files.
- `time.Sleep` appears 212 times in 117 files; `deadline := time.Now().Add` / `time.After` 926 times in 223 files.

Negative results (checked, clean):
- Dead helpers: a script counting word-matches of every non-Test func in each package directory found none unreferenced (the one hit, `assertGreets`, is a string fixture inside `internal/devtool/copyonly/inventory_test.go`). `unused` lint is doing its job.
- Browser-guard tests (11 `*_browser_test.go`, 9,740 lines) are `//go:build browserguard` gated and skip without Chrome, so they respect the determinism rule.
- `TestClientRequestWrappersRoundTrip` (`appwire/cov_rhub_appwire_test.go:86`) looks like a mock test but asserts the real marshal/unmarshal path against declared wire JSON. Keep.
- Copy-assertion governance already exists: `evener dev copy-oracle`, `internal/devtool/copyonly/allowlist.go`, `copyonly_audit_test.go`. Finding 14 is about the Go backlog that tool inventories, not about a missing mechanism.

Context read: AGENTS.md, `docs/developing-evener/testing.md` ("Copy Is Not a Test Oracle"), `docs/developing-evener/coverage.md`.

---

## go-tests-rest-1: ~54 hand-rolled `llm.ProviderAdapter` fakes; the shared scripted adapter is unreachable

Severity: high. Category: duplication.

Locations:
- Shared one exists but is internal to agent: `agent/internal/agenttest/scripted_adapter.go:18` (`ScriptedAdapter`), `agent/internal/agenttest/agenttest.go:44` (`FakeAdapter`). Go `internal/` rules make them unimportable from `cmd/`, `server/`, `internal/`.
- `cmd/evener/scripted_provider_test.go:25-110` (`scriptedProvider`, mutex + requests + steps + errorSteps + response stamping + session-namer shortcut).
- `cmd/evener-hub`: `memory_preservation_test.go:35`, `app_retirement_resume_test.go:474,529`, `summary_failure_daemonlog_test.go:135`, `app_session_activity_relay_test.go:48`, `doc_worktree_integration_test.go:84`, `app_models_test.go:34,56`, `cov_final_webspawn_fuzz_test.go:25`.
- `cmd/evener`: `serve_test.go:874,917`, `serve_state_test.go:36`, `serve_stop_parks_queued_message_test.go:40`, `serve_retirement_process_test.go:516`, `serve_delegate_send_interrupt_test.go:57`, `serve_fast_cheap_model_test.go:17`, `skill_browser_helper_test.go:531`, `memharness_test.go:60`, `launchcheck/coverage_test.go:304`.
- `llm`: 18 distinct `Name()` fakes in the root package alone (`client_test.go:17,34,66,99,357,726`, `generate_test.go:22,49`, `stream_generate_test.go:21`, `client_registry_test.go:90`, `token_count_test.go:248`, four fuzz-file adapters, ...).
- Plus `server/` (`blockingServerAdapter`, `parityProvider`), `cmd/llmcall/main_test.go:25`, `cmd/evener-doctor/reconstruct_test.go:899`, `cmdutil/cov_rp_listmodels_test.go:21`.

Evidence: counts above (54 `Complete(` fakes, 40 files). Every one repeats `Name()`, an unsupported `Stream` returning `llm.ErrStreamUnsupported`, a mutex, a request log and `Provider`/`Model`/`Finish` stamping. `cmd/evener-hub/memory_preservation_test.go:35-70` and `cmd/evener/scripted_provider_test.go:172-190` each hand-marshal the `communicate` tool call (`"end_turn": true`, `ToolCallData{Name:"communicate"}`); 20 test files reference `Name: "communicate"` or `"end_turn": true` independently. The `communicate` argument shape is a production contract; any change must be chased through ~20 literals.

Impact: a contract change in `llm.Request`/`Response` or the communicate tool args breaks tests in dozens of unrelated places; each new test grows another adapter type (AGENTS.md says "scripted provider at the LLM boundary" is the standard, so this is the most repeated pattern in the slice).

Fix plan:
1. Add a non-test package `llm/llmtest` (llm module, no agent dependency) holding `ScriptedAdapter` (move/generalize `agenttest.ScriptedAdapter`: Responder func, fault hook, request log, stamping), `StepAdapter` (fixed steps), `RecordingAdapter`, and builders `Text(msg)`, `ToolCalls(...)`, `Communicate(msg)` that own the communicate JSON shape.
2. Make `agenttest.ScriptedAdapter`/`FakeAdapter` thin aliases or delete them in favor of `llmtest` (agent audit owns that side; coordinate).
3. Migrate by package, biggest first: `cmd/evener` (`scriptedProvider` becomes a use of `llmtest`), then `cmd/evener-hub`, `server`, `llm` root. One-off adapters that block or inject faults keep a small custom type embedding `llmtest.Base` (Name/Stream/stamping).
4. Delete the local types as each package migrates. Tests stay behavior-identical; run each package's targeted tests in CI.
5. Docs: add one paragraph to `docs/developing-evener/testing.md` pointing at `llmtest` as the scripted-provider home.

Size: add ~250 LOC, delete ~1,500-2,500 LOC across ~40 files. Risk: medium (touches many tests; do it per package, no behavior change). Needs Jesse's nod on the new package since AGENTS.md asks before adding shared seams.

---

## go-tests-rest-2: `cmd/evener-hub/cov_*` replay matrices: 6,031 lines that execute but rarely assert

Severity: medium. Category: test-debt.

Locations: 40 files `cmd/evener-hub/cov_*_test.go` (33 carry no build tag), e.g. `cov_exact_tails_fuzz_test.go:60-200`, `cov_exact_app_rpc_fuzz_test.go` (478 lines), `cov_auth_instances_fuzz_test.go` (425), `cov_small_faults_pass5_fuzz_test.go`, `cov_final_main_fuzz_test.go`.

Evidence: 46 `Fuzz*` funcs, 801 discarded-result statements (`_ = f(x)`, `_, _ = f(x)`) versus 246 `t.Fatal/t.Error` lines in the whole set. `FuzzExactTails` (`cov_exact_tails_fuzz_test.go:56`) swaps production seams (`listEvenerLaunchModelContractFn`, `spawnMkdirAll`, `spawnMkdirTemp`, `spawnWriteFile`, `spawnRemoveAll`, `listRendezvousForWait`, `projectSessionLive`, `hubTranscriptRootForList`) one after another and calls ~25 unrelated functions in one body, ignoring results. `f.Add(uint8(0))` with an ignored byte means it is a single deterministic call list dressed as a fuzz target. `docs/developing-evener/coverage.md` already admits these count as EXECUTED, not TESTED, and are "a large share of the fuzz track" for this package.

Impact: they inflate the coverage-floor number for `cmd/evener-hub`, pin production seams (the `spawn*` function vars exist for these tests), and cannot fail on a wrong answer. Every refactor of a covered function drags a change in an unrelated mega-body.

Fix plan:
1. For each file, list the call sites whose production function already has a behavior test (cross-check via `make coverage-gaps`); delete those call sites.
2. Convert the remainder that guard real error arms into table-driven `Test` funcs asserting `errors.Is`/`errors.As` or side effects (the pattern `cov_auth_instances_fuzz_test.go` is cited for in coverage.md), grouped by subsystem file (spawn, tree, threadread) with names that say what is pinned.
3. Delete production seam vars that only these files use (e.g. `spawnMkdirAll`, `spawnRemoveAll`) once the call sites are gone.
4. Keep a floor: do not lower `scripts/coverage/coverage-floors.txt` silently; re-bless it in the same PR only after Jesse agrees the number should drop, because AGENTS.md/CLAUDE.md say reducing coverage is worse than failing tests. This is a discuss-first item.
5. Update the `cmd/evener-hub/cov_*` paragraph in `docs/developing-evener/coverage.md` to match.

Size: net -3,000 to -4,500 LOC over several PRs. Risk: medium (coverage ratchet, seam removal).

---

## go-tests-rest-3: Coverage-chasing unit tests with line-number citations that have already rotted

Severity: medium. Category: doc-drift.

Locations: `appwire/jsonrpc_coverage_test.go:8-9,29,53,106`, `appwire/types_covtest_test.go:20,84`, `appwire/protocol_gaps_test.go`, `appwire/client_gaps2_test.go`, `cmd/evener-hub/app_rpc_gaps_test.go:12-96`, `cmd/evener-hub/app_auth_credentialjson_guard_test.go:25,30`, and the `cov_au_*` files in `auth/openai`.

Evidence: 121 comment lines cite production `file.go:NNN`; 122 test doc comments read "covers every X branch/arm". Spot checks of the citations are wrong today: `jsonrpc_coverage_test.go:8` says `Kind()` is `jsonrpc.go:123-136` but `Kind` is at `appwire/jsonrpc.go:101` and 123 is inside `UnmarshalJSON`; `types_covtest_test.go:20` says `ClientMutationTurnID (types.go:696)` but it is at `appwire/types.go:1951`; `app_auth_credentialjson_guard_test.go:25` cites `app_auth.go:491` (now an unrelated comment about the fingerprint key). `app_rpc_gaps_test.go` tests `hubLaunchConfigRoot` with "TestHubLaunchConfigRootSet covers the path where cfg.LaunchConfigRoot is set" and `allowsPastFallbackAfterLiveReadFailure` with a nil source, with the comment "we can't create a real Source ... but we can test the logic with nil source".

Impact: misleading pointers; tests named for the line they hit rather than the behavior a user depends on; tiny single-branch tests that fail loudly on a harmless refactor.

Fix plan: strip all `file.go:NNN` citations (replace with the function name where a pointer is needed); rename `Test…Covers…`/`Cov…` tests to the behavior; fold branch-per-test files into table tests beside the behavior tests of the same function; move any survivors out of `*_gaps_*`/`*_coverage_*`/`*covtest*` filenames into the file named for the subject (see finding 4). Mechanical, no behavior change.

Size: ~-800 LOC, ~120 comment edits. Risk: low.

---

## go-tests-rest-4: Test files and test names recording review history instead of subject

Severity: medium. Category: slop.

Locations:
- Files named by round: `cmd/evener-hub/app_host_manage_round12/15/16/17_test.go` (735 lines), `cmd/evener-hub/internal/hostops/quarantine_round1..6_test.go` (1,544 lines), `cmd/evener-hub/internal/sshconn/deploy_round8/10_test.go`, `round11/12/13_test.go` (1,785 lines).
- Test names: 50 funcs match `Round[0-9]+|Pass[0-9]+|Issue[0-9]+`, e.g. `TestRound10PushVerifiesByteCount`, `TestRound12PIDCommandsAreStructurallySafe`, `TestRound11EmbeddedInstallerMatchesTheReviewedScript` (`sshconn/round11_test.go:21`), `FuzzRPCSourcesPass6`.
- Comment narration: 366 hits for "round N" in 77 files, 139 "roborev" in 59 files, 507 `PR #NNN`/`#NNNN` in 201 files; e.g. `hostops/quarantine_round3_test.go:21` "the round-three H1 test", `app_auth_instance_test.go:547` "roborev round 3, F2 ... round 6, F2 ... round 8, F2", `app_force_stop_persist_refusal_test.go:22,120,176` "RoboRev reported the ...". 605 `§N` references to specs that are not named ("registry spec 08 §6/§19", `app_host_manage_update_test.go:249`); `internal/devtool/gatesurface/drift_test.go:11` cites `docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md`, which does not exist.

Impact: a reader cannot find the tests for a subsystem by file name (`hostops` quarantine tests are in 7 files, 6 of them numbered rounds); CLAUDE.md says names describe the domain, not history; PR/round narration belongs in commit messages.

Fix plan: per package, fold `*_roundN_*` files into one file per behavior area (`quarantine_custody_test.go`, `quarantine_epoch_test.go`, `deploy_installer_test.go` ...), rename `TestRoundN…` to the pinned behavior (the existing second halves of the names already say it), delete round/PR/roborev sentences and keep the invariant explanation. For `§` references, either link the spec path or drop. Do it with `git mv` plus a rename script so the diff is reviewable; no assertion changes. Add a cheap lint (grep in `evener dev` or the existing test-name linters) rejecting `Round[0-9]|Pass[0-9]` in new test names.

Size: ~5,000 lines moved, ~600 comment lines edited, net -400. Risk: low (pure rename/move; run package tests in CI).

---

## go-tests-rest-5: A scripted AppWire server loop pasted into 16 test files; `appwire/appwiretest` is the unused canonical home

Severity: medium. Category: duplication.

Locations: the `net.Pipe()` + `appwire.NewStreamTransport(serverConn)` + `for { msg := server.Recv(ctx) ... ResponseMessage(msg.Request.ID, ...) }` loop appears in `cmd/evener-hub/remote_hub_source_rpc_test.go:75-125` (`newPushableScriptedRemoteHub`), `cmd/evener-hub/internal/appsource/remote_hub_subscription_test.go:20-110` (`newPushableRemote`), `internal/appsource/remote_hub_source_test.go:107` (`newScriptedRemote`), plus `remote_hub_admin_test.go`, `remote_hub_source_paging_test.go`, `remote_hub_subscription_roundten_test.go`, `local_daemon_test.go`, `paging_corrections_test.go`, `recovery_test.go`, `transport_seams_test.go`, `cmd/evener-hub/app_host_admin_test.go`, `app_host_manage_update_test.go`, `app_host_probe_test.go`, `sshconn/fakes_test.go`, `appwire/frame_recorder_test.go`, `stream_transport_test.go`, `ws_transport_test.go`.

Evidence: 16 files, 29 `ResponseMessage(msg.Request.ID …)` sites. The two sibling implementations differ only in their handling of `initialize` and `thread/unsubscribe` (the appsource copy auto-answers both with features; the hub copy delegates `initialize` to the handler). `appwire/appwiretest/scripted_transport.go` is a third in-memory transport (`ScriptedTransport`), used only by `appwire/optimistic_test.go`, while `appwire/client_test.go:11` defines a fourth (`memoryTransport`).

Impact: an AppWire handshake change (new required initialize field, new framework teardown call) must be mirrored by hand in each copy; the copies already disagree on defaults.

Fix plan: add `appwiretest.ScriptedServer` (`NewScriptedServer(t, handle) (*appwire.Client, *Server)` with `Calls()`, `Push(method, params)`, `OnInitialize` option, automatic empty reply for `thread/unsubscribe`) next to `ScriptedTransport`. Port `newPushableScriptedRemoteHub`/`newPushableRemote`/`newScriptedRemote` onto it first (three copies, ~300 LOC), then the remaining files. Fold `appwire/client_test.go:memoryTransport` into `ScriptedTransport` while there. Update the `appwiretest` package doc.

Size: +120, about -700 LOC. Risk: low-medium (concurrency in the helper; port one package at a time).

---

## go-tests-rest-6: Eight-plus private "wait until" helpers and 212 raw sleeps

Severity: medium. Category: duplication.

Locations: `waitFor`/`waitUntil` with the same body but different signatures and timeouts: `cmd/evener-dev/modulelint_wave_test.go:20` (10s, 10ms), `cmd/evener-hub/app_models_test.go:933` (5s, 1ms, `msg string`, cond-first), `sshconn/manager_test.go:2922` (5s, 1ms), `internal/appserver/websocket_worker_queue_test.go:108` (5s, 2ms), `agent/session_lossless_events_test.go:785`. Further families: `awaitFile` (two signatures, `cmd/evener-hub/e2e_control_invariant_test.go:99`), `waitForFile`, `waitForFileContent`, `waitForQueueDepth` (two, different args), `waitForRendezvous*` (`cmd/evener/serve_test.go:739-790` and `cmd/evener/skill_browser_helper_test.go:350-380` both build an fsnotify watcher over the run dir), `waitForLog`, `waitForEvent`, `waitForRemoteSubscribeCall(s)`, `waitForAuthUpdated(Raw|Broadcast)`. 21 inline `for time.Now().Before(deadline)` loops in `cmd/evener-hub` tests alone.

Evidence: the five `waitFor`/`waitUntil` bodies are the same loop; argument order and failure text differ (`what, cond` vs `cond, msg`). `time.Sleep`: 212 in 117 files; worst `app_host_admin_test.go` (19: five `250ms`/`150ms`/`100ms` fixed sleeps, e.g. lines 811, 1107, 1160, 1178 used as negative waits). `app_rpc_test.go:7864` sleeps 200ms in a goroutine to order a notification.

Impact: each helper picks its own timeout (1 to 30s) so a CI-load flake gets fixed in one copy only; fixed sleeps either waste time or flake.

Fix plan: add `internal/testwait` (root module; `agent` and `llm` modules cannot import root, so give those one copy in `llm/llmtest`/`agenttest` or put the package in `envvars`-style leaf module only if Jesse agrees) with `Until(t, what, cond)`, `UntilFile`, `Rendezvous(t, runDir, opts)`; honor a shared default timeout constant. Replace the five identical helpers and the 21 inline loops first; convert fixed-sleep negative checks to a deterministic barrier (the code already has the pattern in `waitForGateContenders`).

Size: +90, about -450. Risk: low. Note the module boundary question for Jesse.

---

## go-tests-rest-7: Git fixture helpers copied ~12 times with drifting hermeticity

Severity: medium. Category: inconsistency.

Locations: `runGit`: `buildinfo/dirty_check_test.go:111`, `cmd/evener-hub/internal/launchconfig/worktree_identity_test.go:13`, `cmdutil/statedir_test.go:123`, `identifier/git_test.go:11`, `repofixture_test.go:12`. `newLinkedWorktree`: `launchconfig/worktree_identity_test.go:33`, `cmdutil/statedir_test.go:143`, `identifier/project_integration_test.go:9`. `gitIn`: `internal/plugins/git_test.go:32`, `sshconn/deploy_test.go:1243`. Others: `scripts_fuzz_shared_test.go:46` (`gitOutput`), `cmd/evener/scratch_reclaim_unix_test.go:231` (`newGitWorkspace`), `buildinfo/dirty_check_test.go:180`, and inline `exec.Command("git"...)` in hubcore `tree_test.go:303`, `project_delete_test.go:614`, `spawn_test.go:1065`, `server/appwire_environment_replay_test.go:53`.

Evidence: 42 `exec.Command("git"` sites in 31 files. Environment handling differs: `buildinfo` sets `GIT_CONFIG_NOSYSTEM=1`, `GIT_CEILING_DIRECTORIES`; `sshconn` sets `GIT_CONFIG_GLOBAL=/dev/null`; `cmdutil`, `launchconfig`, `identifier`, `plugins` only add author vars on top of `os.Environ()`; `repofixture_test.go` and `app_git_head_test.go` pass nothing. Only 9 of the 31 files isolate global git config at all. `identifier` additionally adds `-c protocol.file.allow=always`; `launchconfig` and `cmdutil` skip the test when git is missing, `identifier/git_test.go:15` too, others `t.Fatalf`. Three byte-identical `newLinkedWorktree` bodies.

Impact: a developer's `~/.gitconfig` (signing, `init.defaultBranch`, hooksPath, `safe.directory`) can change these tests' outcome, against the rule that default tests must not depend on ambient machine state.

Fix plan: one `internal/gittest` package (root module; `identifier` is its own module so it keeps one tiny local copy or the package moves to a leaf module): `Run(t, dir, args...) string` with a hermetic env (NOSYSTEM, GLOBAL=/dev/null, ceiling dir, fixed identity, `protocol.file.allow`), `InitRepo`, `LinkedWorktree`, `Commit`. Port the five `runGit` and three `newLinkedWorktree` copies first, then the inline sites. Tests keep their assertions.

Size: +100, about -350. Risk: low.

---

## go-tests-rest-8: `TestMain` env isolation re-implemented in `cmd/evener` and `launchcheck`, with drift

Severity: medium. Category: duplication.

Locations: shared implementation `cmd/evener-hub/internal/hubtestenv/hubtestenv.go` (251 lines, used by 3 packages: hub, hubcore, fspaths, `sshconn` test). Re-implemented: `cmd/evener/testmain_test.go:16-133` (`retiredEvenerEnvVars`, `productEvenerEnvVars`, GOCACHE/GOPATH/GOMODCACHE pin via `go env`, EVENER_* scrub, HOME/XDG redirect), `cmd/evener/internal/launchcheck/testmain_test.go:11-58`, and the GOCACHE pin in `cmd/evener-hub/testmain_test.go:172`, `cmd/evener-dev/agentshards_edges_test.go:59`, `gatemodulerunner_test.go:90`, `install_test.go:1579`.

Evidence: three `productEvenerEnvVars` implementations (`hubtestenv.go:193`, `cmd/evener/testmain_test.go:26`, `launchcheck/testmain_test.go:21`) with the comment "Mirrors the hub". Drift is real: the hub list retires `EVENER_API_TOKEN` and `EVENER_HUB_ADDR` (`hubtestenv.go:35`); the CLI and launchcheck copies list only `EVENER_API_TOKEN` (`cmd/evener/testmain_test.go:19`, `launchcheck/testmain_test.go:14`). `cmd/evener-hub/internal/` cannot be imported from `cmd/evener` (Go internal rule), which is why it forked.

Impact: a stale `EVENER_HUB_ADDR` in a developer shell leaks into CLI tests but not hub tests; any new scrub rule must be added in three places.

Fix plan: move `hubtestenv` to `internal/testenv` (root module, importable by both `cmd/evener-hub` and `cmd/evener`), delete the two forks, keep per-package TestMain keeping only their own defaults and re-exec hooks. The retired list lives once. The assertion tests (`TestGoSubprocessesCacheOutsideTheTestRoot`, host-temp test) stay and can run against the shared function.

Size: +0 net new, about -180 LOC. Risk: low.

---

## go-tests-rest-9: Provider-package test scaffolding copied across anthropic/google/chatcompletions/responses

Severity: medium. Category: duplication.

Locations:
- `stream_attempts_test.go` in four packages: `llm/providers/anthropic/stream_attempts_test.go` and `responses/stream_attempts_test.go` are both 107 lines and differ in only 42 diff lines (provider constant, request helper, attempt-group name); `google/` (160) and `chatcompletions/` (209) share the same `stallingSSEClient`, `idleAttemptRoundTripper` and test names (`TestStreamAppendsTheAttemptBeforeTheTerminalEvent`, `TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout`). `stallingSSEClient` is byte-identical in anthropic and responses.
- `resolved`, `userReq`, `build`, `jsonOf` redefined per package: `chatcompletions/request_test.go:15-43`, `responses/request_test.go:15-66`, `anthropic/protocol_test.go:13-32`, `google/protocol_test.go:12-31` (`protoBuild` identical in anthropic and google), plus `llm/registry_shape_test.go:10` (`resolved`).
- `captureSink` (an `apilog` attempt sink) in `anthropic/protocol_transport_test.go:20`, `google/protocol_transport_test.go:20`, `responses/transport_test.go`, `chatcompletions/stream_attempts_test.go:19`, `internal/protocolhttp/protocolhttp_test.go`; 16 `AppendAttempt` fakes in 14 test files in total (`llm/api_attempt_group_test.go:20`, `provider_wire_outcomes_test.go:18`, `provider_wire_redirect_test.go:34`, ...).
- `buildFuzzRequest` in `anthropic/protocol_fuzz_test.go:119` and `google/protocol_fuzz_test.go:81`, with 4-way duplicated request-building bodies in `chatcompletions/requestbuild_fuzz_test.go` and `responses/requestbuild_fuzz_test.go` (clone scan: 18 identical 14-line windows across those four files).
- `collectStream` differs only in how it builds the server (`anthropic/inband_error_test.go:15`, `google/inband_error_test.go:14`).

Impact: a change to attempt recording (the central `llm` contract) is repeated in four stream test files and 16 sinks; helper drift already shows in `userReq` (the openai-responses copy adds a system message, chat does not).

Fix plan: `llm/providers/internal/providertest` (non-test package inside the llm module): `RecordingSink`, `StallingSSEClient`, `StreamAttemptContract(t, factory)` that runs the two shared stream-attempt behaviors against any protocol, `FuzzRequest(...)`, `ResolvedFor(protocol, mutate)`. Each protocol's test file shrinks to its provider constants. Keep protocol-specific tests (chunked gzip, in-band errors) local.

Size: +250, about -900 LOC. Risk: medium (shared contract helper must stay readable; do anthropic+responses first, since those are 90% identical).

---

## go-tests-rest-10: Hand-rolled stdlib replacements and per-package file writers

Severity: low. Category: slop.

Locations:
- `itoa`: `cmd/evener-dev/modulelint_signal_test.go:11` (`strconv.Itoa` wrapper), `internal/apptranscript/failed_tool_calls_test.go:80` (hand-rolled digit loop), `fuzz/schemagen/schemagen_test.go:385` (hand-rolled with sign handling). The apptranscript version returns `""` for negatives wrongly handled (no sign case).
- `contains(s, sub)` hand-rolled substring loop: `appwire/cov_rhub_appwire_test.go:701`; `contains(values, want)` wrapping `slices.Contains`: `auth/openai/config_test.go:165`; `contains(haystack, needle []byte)` plus `bytesIndex` re-implementing `bytes.Index`: `cmd/evener-hub/spawn_daemonlog_gaps_test.go:167-190`.
- Write-file wrappers with the same body and different names/modes: `cmd/evener-doctor/main_test.go:49` `mustWrite` (0644), `cmd/evener-fuzz-harvest/coverage_test.go:455` `mustHarvestWrite` and `harvest_test.go:78` `writeFile`, `cmd/evener-fuzzregistry/coverage_test.go:337` `mustRegistryWrite` and `main_test.go:404` `writeFile`, `launchconfig/resolver_test.go:16` `writeFile`, `tools/tool-fluency/.../offline_coverage_test.go:418` `mustWrite`; two `splitLines` (`appwire/cov_rhub_appwire_test.go:954`, `apptranscript/failed_tool_calls_test.go:385`); `jsonOf` x2 in providers (see 9); `mustMarshal`/`mustMarshalJSON`/`mustPagingJSON`/`mustOwnerJSON` in hub.

Evidence: file:line list above; the three `itoa` implementations behave differently on negatives.

Impact: small, but all of it is code that shouldn't exist (`strconv.Itoa`, `strings.Contains`, `bytes.Index`, `os.WriteFile` with `MkdirAll`).

Fix plan: replace call sites with stdlib (`strconv.Itoa`, `strings.Contains`, `bytes.Contains`, `slices.Contains`, `strings.Split`) and delete the helpers; where a mkdir+write helper is genuinely used, use one `testfs.WriteFile(t, path, content)` (could live beside `internal/testwait` from finding 6). Mechanical.

Size: about -150 LOC. Risk: low.

---

## go-tests-rest-11: Nine copies of the `mainDeps{...}` hub-boot literal

Severity: low. Category: duplication.

Locations: `cmd/evener-hub/main_test.go:136,452,570`, `main_hublock_test.go:46,113`, `main_ephemeral_port_test.go:64`, `cov_final_main_fuzz_test.go:60`, `cov_main_listener_fuzz_test.go:147`, `cov_main_bootstrap_pass6_fuzz_test.go:47`.

Evidence: clone scan found the same 15-line block (`loadRegistry: hermeticRegistryLoader, loadConfig..., ensureDirs..., acquireLock..., newToken "hub-token", loadAuthToken "auth-token", loadCredentials, startLivePrefetch no-op, startLaunchPrefetch no-op, notifyContext, listen`) in 9 places; each differs in only 1-3 fields (`acquireLock`, `serve`, `notifyContext`). `mainDeps` has about 17 optional fields and grows with each seam (`rosterProbeTimeout`, `afterWeb`).

Impact: adding a `mainDeps` field requires fixing nine literals or letting some nil.

Fix plan: add `hermeticMainDeps(t, ctx, cfg) mainDeps` next to `hermeticRegistryLoader` (`main_test.go:501`) and have each test override its 1-3 fields. Change the 9 sites.

Size: +25, about -120 LOC. Risk: low.

---

## go-tests-rest-12: `app_rpc_test.go` is a 13,451-line grab bag with ~30 private source types

Severity: medium. Category: complexity.

Locations: `cmd/evener-hub/app_rpc_test.go` (215 tests, 28 helpers); source stubs from `:446` (`localItemPackingRPCSource`) to `:11309` (`sessionUnavailableOnceSource`): about 30 `*Source` types (`deletionFenceRelaySource :1452`, `scriptedRelaySource :7547`, `blockingRecoveryRelaySource :7585`, `failFirstRelaySource :8075`, `startResumeRelaySource :8209`, `forkingRelaySource :8232`, ...). Also `app_instances_test.go` 5,442 lines, `app_relay_test.go` 3,653, `navigation_service_test.go` 2,964.

Evidence: test name prefixes in `app_rpc_test.go` span deletion fences, relay canonical stop, thread start/read/unsubscribe, instance edit, replay-turn projection, launch path validation, credentials store helpers (`newTestCredentialsStore :12648`), a roster helper (`rosterOverRunDir`), and the shared `newHubRPCTestServer(WithWeb)` fixture builder at `:12642-12700` that every other RPC test file depends on. There are already 22 other `app_rpc_*_test.go` files, so the topic split has begun and stopped.

Impact: navigation and review of a 13k-line file is painful; the shared fixture is buried at line 12,642; edits conflict constantly.

Fix plan: move the shared builders (`newHubRPCTestServer*`, `rosterOverRunDir`, `newTestCredentialsStore`) to `hub_rpc_fixture_test.go`; split tests by the topic prefixes already present into `app_rpc_<topic>_test.go` (thread_start, relay_stop, deletion_fence, replay_items, launch_path...). Pure `git mv`-style move, no content edits, verified by `go vet` and diff of the sorted `Test` name list before/after (use the name-only gate from memory: diff --name-only against main).

Size: about 13k lines moved, net 0. Risk: low.

---

## go-tests-rest-13: Hub fixture sprawl: 1,307 `hubcore.WebConfig{` literals and ~60 bespoke `newXFixture/Harness`

Severity: medium. Category: duplication.

Locations: `cmd/evener-hub/*_test.go`. Builders: `newTestWebServer` (`webnext_test.go:17`), `newHubRPCTestServerWithWeb` (`app_rpc_test.go:12677`, the only one that isolates state roots and credentials), `newListingWebServer`, `newSessionSeenWeb` (`app_session_seen_test.go:13`), `newPromoteWeb`, `newPinNavigationAppWireWeb`, `newSharedBareIDPinWeb`, `newLaunchModelsTestWeb`, `newFuzzWebServer`, and host manager builders `planTestManager` (`app_host_ops_test.go:94`), `pipelineFixture.manager` (`app_host_pipeline_test.go:39-80`), `newUpdateFixture` (`app_host_manage_update_test.go:44`), `newRemnantFixture` (`app_host_remnants_test.go:58`), `bootHostManager`, `newInstancesFixture`, `newReachFixture`, `newAccessProbeFixture`, `newVertexADCProbeFixture`.

Evidence: 1,307 `hubcore.WebConfig{` literals vs 285 `NewWebServer(` calls. Only `newHubRPCTestServerWithWeb` redirects `HubStateRoot`, `LaunchConfigRoot`, `PluginRoot`, `CredsStore` and `Registry` to per-test temp dirs (its comments, `app_rpc_test.go:12677-12740`, explain the shared-HOME race that motivated it). `newTestWebServer` and `newSessionSeenWeb` build a bare `WebConfig{}` and so fall back to the package-wide TestMain root, which `newHubRPCTestServerWithWeb` documents as unsafe for parallel tests. Four different host-manager builders each re-create `hostreg.New` + `hostops.Open(StorePath(t.TempDir()))` + `hub.toml` writing.

Impact: whether a test is isolated depends on which builder it happened to call; fixes to isolation must be repeated.

Fix plan: make `newHubRPCTestServerWithWeb`'s defaulting a standalone `testWebConfig(t, cfg) hubcore.WebConfig` and route `newTestWebServer`, `newSessionSeenWeb`, `newPromoteWeb`, `newLaunchModelsTestWeb`, etc. through it. Provide one `hostManagerFixture` (registry + store + hub.toml + optional seams) used by `planTestManager`, `pipelineFixture`, `newUpdateFixture`, `newRemnantFixture`. Do not touch tests that need exotic wiring. Evaluate hot spots with `grep -c 'hubcore.WebConfig{'` before/after.

Size: +80, about -400 LOC. Risk: medium (isolation changes can flip parallel-order flakes; CI is source of truth).

---

## go-tests-rest-14: Go error/output text still used as the oracle in 371 assertions across 142 files

Severity: medium. Category: test-debt.

Locations (top counts of `strings.Contains(err.Error(), "…")`): `cmd/evener-hub/internal/sshconn/deploy_test.go` 15, `internal/plugins/marketplaces_path_scrub_test.go` 13, `sshconn/version_test.go` 13, `hubcore/pin_section_nonretry_test.go` 12, `launchconfig/io_test.go` 10, `app_rpc_test.go` 9, `llm/registry/schema_test.go` 8, `app_instances_test.go` 8, `cmd/evener/run_test.go` 7. Examples: `appwire/optimistic_test.go:59` (`"steer is not available"`), `auth/openai/cov_au_service_test.go:108,132` (`"port in use"`, `"status 400"`), `cmd/evener-dev/shardplan_test.go:146`, `cmd/evener-dev/agentshards_gaps_test.go:66`.

Evidence: 371 `strings.Contains(err.Error(), "<literal>")` hits in 142 files; 751 hits when widened to message/detail/output/body vars in 227 files; 9 `.Error() == "<literal>"`. `docs/developing-evener/testing.md` forbids exactly this ("Never write ... `strings.Contains(err.Error(), "not found")`"). The inventory tool `evener dev copy-oracle` (cmd/evener-dev/copyoracle.go) and allowlist exist; this is the backlog it reports for Go.

Impact: every wording change breaks tests; many of these also fail to prove the right error was returned.

Fix plan: run `evener dev copy-oracle --only go --top 30`; convert the top ten files area by area: expose or reuse sentinel errors / typed errors / `appwire.WireError.Code` (the production side often already has the code, e.g. `appwire.ErrorMutationOutcomeUnknown`) and assert with `errors.Is/As` or structured fields; add sentinels in production only where a caller would legitimately branch on them (no sentinel just for a test). Wire the ratchet so the count only goes down.

Size: about 400 assertions touched, +60 LOC of sentinels. Risk: low-medium (production signature additions need review). Convert in per-package PRs.

---

## go-tests-rest-15: `sshconn` fake runner makes every test a mini remote-shell parser of production command text

Severity: medium. Category: test-debt.

Locations: `cmd/evener-hub/internal/sshconn/fakes_test.go:24-94` (`fakeRunner` with `runFn`/`startFn`, `requireProtocolUpgrade`), used across `manager_test.go`, `version_test.go`, `deploy_test.go`, `deploy_round8/10_test.go`, `round11/12/13_test.go`, `update_host_test.go`, `remove_host_test.go`, and the hub's `app_host_*` tests.

Evidence: 435 `runFn` references in 79 test files; 237 `strings.Join(argv, " ")` classifications; 91 occurrences of the literal `systemctl restart`; `switch { case strings.Contains(joined, "nohup"): … case strings.Contains(joined, "mkdir -p /home/dev/.local/bin"): … }` is re-written per test (`version_test.go`, `manager_test.go`). `requireProtocolUpgrade` (`fakes_test.go:75`) decides "installed" by matching the production strings `mv "$tmp"` and `evener-install.XXXXXX`. 950 `strings.Contains` calls in the package's tests. Production builds these commands in `version.go:191,1569-1571`.

Impact: any rewording of the remote command (quoting, flag order) breaks dozens of tests that only needed to know "this was the restart step"; tests can pass against a command that is shell-invalid because nothing runs it.

Fix plan: give `fakes_test.go` a `fakeHost` that parses argv once into an operation enum (`opRestartService`, `opNohupLaunch`, `opInstall`, `opKill`, `opProbe`) using one classifier, and let tests register responses per operation. Better: have production export the operation kind alongside the command (the code already builds each command in one function), so the classifier is the production table and tests assert "restart issued" rather than command text. Real-shell coverage already exists for some commands (`remoteinstall` script tests); keep those.

Size: +200, about -900 LOC across 79 files. Risk: medium-high (largest package in the slice; port in slices: manager, version, deploy).

---

## Recommended fix sequence

1. Finding 10 (stdlib replacements, `itoa`/`contains`/`bytesIndex`): tiny, mechanical, builds confidence.
2. Finding 3 (stale line refs) and finding 4 (round/history names): pure renames/comment edits, no assertion changes; do them package by package with the name-only diff gate. Also lands the lint that stops new `Round`/`Pass` names.
3. Finding 12 (split `app_rpc_test.go`) and finding 11 (`hermeticMainDeps`): zero-behavior moves that make the later hub fixture work (13) reviewable.
4. Finding 8 (shared `testenv`), finding 7 (`gittest`), finding 6 (`testwait`): small leaf helper packages; each needs Jesse to approve the new shared package and the module-boundary choice (root vs leaf module).
5. Finding 5 (`appwiretest.ScriptedServer`) then finding 9 (`providertest`): helper homes that already half-exist; port the three most-identical copies first.
6. Finding 1 (`llmtest`, the highest-value change): do after 5 and 9 so adapters, sinks, and AppWire fixtures share one test-support story; migrate `cmd/evener`, then hub, then `server`, then `llm`. Coordinate with the agent audit on `agenttest`.
7. Finding 13 (hub fixture builders) and finding 15 (`sshconn` fake host): the larger refactors, once the helper packages exist.
8. Finding 14 (copy-oracle backlog): continuous, area by area, using the ratchet.
9. Finding 2 (`cov_*` matrices): last and discuss-first, because it interacts with the coverage floors; prune the call sites already covered by behavior tests, convert the rest to asserting tests, then re-bless floors with Jesse's agreement.

