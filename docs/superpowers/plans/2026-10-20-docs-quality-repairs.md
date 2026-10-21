# Documentation Quality Repairs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repair every finding in the completed documentation audit, with verified procedures, genuine native walkthroughs, and reviewable screenshot refresh.

**Architecture:** Keep canonical guides and existing production behavior. Extend the current documentation checks and real-runtime tour coordinator; use a small XCTest driver for the actual iPhone app. Generate candidate evidence separately from reviewed publication assets.

**Tech Stack:** Markdown, Node 22, Node test runner, Cheerio, VitePress, Playwright, Go tests, Expo 57.0.23, XCTest, and GitHub Actions.

**Spec:** [Documentation quality repairs](../specs/2026-10-20-docs-quality-repairs.md).

## Global Constraints

- Preserve current product behavior and compatibility policy.
- Native coverage means the real iPhone app in a simulator.
- Use a scripted provider only at the LLM HTTP boundary.
- Use disposable roots, synthetic credentials, and owned simulator instances.
- Default tests remain deterministic and credential-free.
- Assertions test behavior and structured state, not human-readable wording. UI labels may locate controls.
- Run focused local checks. CI remains responsible for expensive whole-repository qualification.
- Publication still uses reviewed committed assets.
- Do not copy another environment's value or disable simulator signing.

## Review Focus

1. A failed or colliding shell destination must not initialize Git or start an agent elsewhere. Task 1 exercises existing directories, files, symlinks, dangling symlinks, unwritable parents, and Git failure.
2. Old cookies and already connected clients must lose access after credential rotation and restart. Task 2 checks bearer, cookie, connection closure, and trusted-client reconnection.
3. A copied history root can be readable while non-resumable. Task 2 separates read-only recovery, legacy rejection, and fresh-root continuation, with original hashes unchanged.
4. A native action must use the native setting scope and queue semantics. Tasks 6 and 7 verify global defaults, steering a queued row, cancellation, and exactly-once continuation through real state.
5. A failed, stale, incomplete, or private capture must never become a complete publication candidate. Task 8 tests these cases, plus unrelated source changes and image/provenance correspondence.

---

## Preparation and ownership

Work in the existing isolated `wip/docs-quality-repairs` checkout. Leave the main checkout's unrelated changes untouched. Read `AGENTS.md`, `docs/developing-evener/testing.md`, and the spec before editing. Read `mobile-native/AGENTS.md` and its required Expo versioned documentation before any native code change. Read relevant product/subsystem guides before changing a behavior contract; this plan does not authorize a product redesign.

Use the subagent-driven execution ledger as the sole detailed progress record. The session task list tracks phase status only. Independent writers need separate worktrees. Shared files named in more than one task are sequential integration points.

Before running a script, read it. Before running make targets, run `make help` and inspect their recipes. Install each worktree's dependencies through the documented preflight; never `npm ci` through a symlink. Use Node 22 for documentation checks and a fresh owned Chromium cache for the contributor rehearsal. Do not overwrite inputs that cannot be regenerated; copy them before inspection or rehearsal.

For each code correction, add the failing behavioral test, observe failure, make the smallest change, and rerun the relevant suite. Prose-only corrections need source-backed procedural verification, not assertions about wording or an invented failing test. Before each commit, inspect status, stage only that task's named files, inspect the staged diff, and allow the normal hooks to run.

### Task 1: Make the Daily work setup safe

**Files:**
- Modify: `docs/daily-work.md`, first shell block.
- Create: `docs-site/scripts/documented-shell.test.mjs`.

**Interfaces:**
- Consumes the actual first shell fence in `docs/daily-work.md`, not a duplicated command.
- Test-local `runDailyBlock(t, {collision, failGit})` returns `{status, callerCwd, agentCwds, gitRoots, preserved}` from an owned fixture. Use real Git except the explicit Git-failure case. The agent process probe records invocation only; this is a shell-safety check, not an agent execution test.

- [x] Add cases named `fresh workspace runs only inside its new directory`, `colliding destinations preserve contents and do not launch`, `unwritable parent does not launch`, and `Git failure prevents launch`. Assert exit status, working directories, Git metadata locations, and preserved bytes. Include an existing directory, file, ordinary symlink, and dangling symlink.
- [x] Run `node --test docs-site/scripts/documented-shell.test.mjs`. Confirm the unsafe current block reaches the invocation probe in collision cases.
- [x] Put workspace creation, directory change, Git initialization, and invocation in one failure-propagating subshell. Preserve the current Retry guidance and unrelated prose.
- [x] Rerun the file. All cases pass; failed setup leaves the caller's working directory and existing contents unchanged.
- [x] Review and commit the two named files as `docs: guard the daily-work workspace setup`.

### Task 2: Verify operator recovery and incident procedures

**Files:**
- Modify: `UPGRADING.md`, `docs/evener-hub.md`, `docs/security.md`, `docs/device-pairing.md`, `docs/troubleshooting.md`, `docs/integrations.md`.
- Create: `cmd/evener-hub/tutorial_recovery_test.go`, `cmd/evener-doctor/recovery_runbook_test.go`, `agent/upgrade_runbook_test.go`.
- Modify only if needed for the touched contract: the obsolete rotation-flag comment in `cmd/evener-hub/internal/hubedge/auth_token.go`; do not add a flag.
- Reuse: `agent/delegate_resource_bootstrap_test.go`, `cmd/evener-doctor/reconstruct_test.go`, `cmd/evener-hub/e2e_turn_control_test.go`, and `cmd/evener-hub/internal/hubedge/auth_token.go`.

**Interfaces:**
- `TestTutorialHubTokenRotationRunbook(t *testing.T)` runs an isolated real hub lifecycle with synthetic credentials and authenticated HTTP/WebSocket clients.
- `TestBackupRestoreRunbook(t *testing.T)` and `TestReconstructionRunbook(t *testing.T)` execute read-only/staging operations against disposable copies and compare source hashes.
- `TestUpgradeRunbookPreservesHistoryAndContinuesInFreshRoot(t *testing.T)` exercises rejected and compatible saved-state fixtures, then a fresh-root task at the scripted-provider boundary.
- Existing public contracts remain `LoadOrCreateAuthToken(hubStateRoot string) (string, error)`, the actual hub service lifecycle, and doctor commands. Do not invent a new recovery API.

- [x] Read the service and doctor dispatch code. Write the three procedures from those actual commands, naming prerequisites, affected clients, checkpoints, and stop conditions. Verify the token location respects `hub_state_root`; keep secrets out of argv, shell history, logs, and examples.
- [x] Add the acceptance tests. Rotation asserts old bearer/cookie rejection after restart, old connection termination, replacement acceptance, and a successful trusted-client request. Also demonstrate that replacing the token file does not change an already running guard. Never use unauthenticated `/api/health` as proof of revocation.
- [x] Add recovery assertions: unchanged source bytes; readable staged history; structured recovery boundary and omissions; legacy rejection without source mutation; compatible restoration; a fresh-root task that completes without reusing the incompatible root. Do not assert error prose.
- [x] Run `go test ./cmd/evener-hub -run '^TestTutorialHubTokenRotationRunbook$' -count=1`, `go test ./cmd/evener-doctor -run 'Test(BackupRestoreRunbook|ReconstructionRunbook|Reconstruct)' -count=1`, and `go test ./agent -run 'Test(UpgradeRunbook|DelegateResourceBootstrap_)' -count=1`. Record what the real operations establish; unsupported recovery paths must be corrected in the guide, not simulated.
- [x] Finish the guides: put the legacy warning before shutdown; distinguish full backup, history restoration, and specialized AgentsView reconstruction; link the shared-token rotation runbook from pairing (the native recovery link remains in deferred Task 7); replace “encrypted transport” with precise bootstrap-envelope wording where applicable.
- [x] Review and commit only the files changed by this task as `docs: verify upgrade, revocation, and recovery procedures`.

### Task 3: Repair contributor onboarding and test recipes

**Files:**
- Modify: `docs/developing-evener/README.md`, `docs/developing-evener/testing.md`, `docs/developing-evener/conventions/go-workspace.md`, `make/repo.mk`.
- Generated output: the make-target table in `docs/developing-evener/README.md`, regenerated from `make/repo.mk` rather than hand-edited.

**Interfaces:**
- The existing `make docs-check` behavior stays unchanged. Its prerequisites must include installed Chromium.
- Retained live-test recipes must identify actual Go packages, selectors, opt-in variables, and credential requirements. An empty `go test -list` selection is failure of a recipe, not a passing test.

- [x] Enumerate the current provider test declarations and their opt-in gates. Remove the absent `llm/providers/openai` recipe and `TestAdapter_E2E_Codex`/`TestAdapter_E2E_Anthropic` claims unless a real maintained replacement is established. Keep historical observations explicitly historical.
- [x] Replace current-reference `evener dev` commands with their owning make targets or `go run ./cmd/evener-dev/bin dev ...`. Check dispatch and retain the warning about root `./...` missing sibling modules.
- [x] Add one concise contributor walkthrough: prerequisites, isolated checkout, `make build-runtime`, `./evener --version`, a small canonical Markdown edit, the documentation gate, generation when required, and review/CI expectations. Link the existing build and testing references.
- [x] Show `npm --prefix docs-site ci` and `(cd docs-site && npx playwright install chromium)` before the docs gate. Correct the prerequisite metadata in `make/repo.mk`, then run `make generate`; inspect the diff and keep only intended generated changes.
- [x] With `PLAYWRIGHT_BROWSERS_PATH` set to a new owned directory, follow the documented install sequence and run `make docs-check`. The Chromium-dependent renderer must actually run. For each retained provider recipe, run `go list <documented-package>` and `go test <documented-package> -list '<documented-selector>'`; verify at least one declaration and source-confirmed opt-in without making live requests.
- [x] Rehearse the first-contribution path in an owned disposable checkout, then remove only that rehearsal's scratch. Review and commit the named source/generated files as `docs: repair contributor setup and executable recipes`.

### Task 4: Fix anchors and Job control navigation

**Files:**
- Modify: `docs-site/scripts/prepare.mjs`, `docs-site/scripts/prepare.test.mjs`, `docs-site/scripts/site.browser.mjs`, `docs/job-control.md`.
- Add a rendered-anchor fixture inside the existing publisher test fixture tree, not a public demonstration page.

**Interfaces:**
- Preserve `headings(tree)` and `prepare({root, manifest, out})` signatures. Reuse the already imported Cheerio parser to collect decoded IDs from raw HTML nodes.
- Use a compact index beside “Model-facing tools,” with existing fragment targets. No global outline-depth change or page split.

- [x] Add a source and rendered fixture with `<a id="a&amp;b"></a>` and `#a%26b`, plus a missing-target negative case. Assert target resolution, not output wording.
- [x] Run `node --test docs-site/scripts/prepare.test.mjs` and observe the valid fixture fail before the decoding fix.
- [x] Extract decoded IDs through HTML parsing and rerun the file. Existing heading and link checks remain strict.
- [x] Add browser cases starting at the top of Job control and using page navigation to reach `#job_stop`, `#job_status`, and `#job_watch`. Cover pointer and keyboard at widths 1200, 320, and 390. Assert the resulting fragment and target geometry beneath sticky navigation.
- [x] Observe the missing-navigation case, add the compact tool index, build with `npm --prefix docs-site run build`, and run `(cd docs-site && npx playwright test)`. Preserve search and Mermaid checks.
- [x] Review and commit the named files as `fix(docs): resolve encoded anchors and expose tool navigation`.

### Task 5: Verify web menu navigation and first-provider setup

**Files:**
- Modify: `docs-site/scripts/tours/web.mjs`, `docs-site/scripts/tours/web.test.mjs`, `cmd/evener-hub/tutorial_config_test.go`, `docs/configuration.md`, `docs/getting-started.md`.
- Create: `cmd/evener-hub/tutorial_provider_test.go`.

**Interfaces:**
- Preserve driver `open(options)` returning `{act(action, args)}`. `configure` continues to accept `{scope:'project', field:'max_rounds', value:number, workspace:string}` but reaches the form through visible navigation.
- Add a `setup-provider` action with `{baseURL:string, credential:string, providerID:string, modelID:string}` in the private driver channel. Values refer only to the isolated scripted provider. The credential must never reach candidate captures or public diagnostics.
- `TestTutorialFirstProviderWeb(t *testing.T)` starts with no configured provider, uses the real setup UI, and proves a subsequent task reaches the intended local provider and completes.

- [x] Add a real-browser navigation assertion: the configuration action must activate the visible settings menu and project entry before editing Max rounds, with no direct settings-route navigation. Observe actual browser events, not a driver-reported success flag. Retain independent layer, provenance, old-launch, and file-effect assertions in `runConfigWeb`.
- [x] Run `node docs-site/scripts/tours/refresh.mjs check --surfaces web --scenario configuration` with that new assertion and observe the current direct-route driver fail it. Replace the route jump with actual menu actions, then rerun. Do not weaken project-isolation assertions to accommodate a different starting page.
- [x] Add first-provider setup from an empty configuration. Use actual UI forms and a local provider endpoint; independently verify saved provider configuration, model discovery, and the resulting real session. Keep this explicit browser journey gated with the tutorial opt-in.
- [x] Run `EVENER_TUTORIAL_TOURS=1 go test ./cmd/evener-hub -run '^TestTutorialFirstProviderWeb$' -count=1 -timeout=10m`, then the configuration tour again. Require actual execution rather than a skipped test.
- [x] Update the two guides to describe the now-tested starting state and menu path. State that local scripted-provider evidence does not qualify external login, billing, or live model behavior.
- [x] Review and commit the named files as `test(docs): exercise visible setup and settings journeys`.

Completed Tasks 1–5 are supported by the execution ledger and accepted task reports: `f3b6fd1f38` (safe shell), `99016cfc36` (recovery), `921c80cb2d` (contributor recipes), `461781fd37` (anchors/navigation), and `dd6497f5d3` (web journeys). Task 5 retains its recorded early parallel-check deviation and out-of-scope dependency advisories; final qualification ran serially.

### Task 6: Prove one real native tour

**Files:**
- Create: `docs-site/scripts/tours/native.mjs`, `docs-site/scripts/tours/native.test.mjs`, `docs-site/scripts/tours/xctest/TutorialTours.swift`, `docs-site/scripts/tours/xctest/TutorialTours.xcodeproj/project.pbxproj`, `docs-site/scripts/tours/xctest/TutorialTours.xcodeproj/xcshareddata/xcschemes/TutorialTours.xcscheme`, `cmd/evener-hub/tutorial_native_test.go`.
- Modify: `docs-site/scripts/tours/driver.mjs`, `cmd/evener-hub/tutorial_tours_test.go`, and shared helpers in `cmd/evener-hub/tutorial_receipt_test.go` only where real semantic duplication requires extraction.
- Do not alter the native production UI unless a necessary accessibility identifier is missing; seek review before expanding scope.

**Interfaces:**
- Extend driver `open` options with `surface:'web'|'native'`; retain private `{id, action, args}` requests and `{id, ok, data}` replies. No success oracle lives in the driver.
- Native `open(options)` returns the same `{act(action,args)}` interface and owns its test process and simulator. Use `XCUIApplication(bundleIdentifier: "com.primeradiant.evener.native")` and real accessibility actions.
- `runReceiptNative(t *testing.T, out string)` shares the receipt file/checker/transcript oracles, not browser navigation. Use the existing `checkpoint(t, id, evidence, capture)` authorization boundary.

- [ ] Resolve the native prerequisites first. Jesse must provide the intended `APPLE_TEAM_ID` or setup that supplies it. Follow the locked native build guide and required Expo documentation. Inspect existing generated project/dependency paths before creating anything. Never disable signing or reuse another session's device.
- [ ] Build the current-source standalone Release simulator app through `mobile-native/ios/Evener.xcworkspace`, scheme `Evener`, using normal signing and an owned derived-data directory. Prove the new XCTest runner can launch the app, find an actual control, and stop cleanly on a newly owned iPhone simulator. A build or launch PID alone does not pass this step.
- [ ] Add protocol tests for unknown actions and failed driver replies, then implement the smallest private control bridge for native `open`, `start`, `capture`, and `close`. Keep credentials out of argv and XCTest attachments. Rerun `TestTutorialCheckpointRejectsFailedEvidence` at the Go coordinator boundary; the driver must not invent a second outcome oracle. Real execution must prove UI readiness separately.
- [ ] Add `runReceiptNative`, selecting native explicitly in `runTour`. Run `EVENER_TUTORIAL_TOURS=1 EVENER_TUTORIAL_SURFACES=native EVENER_TUTORIAL_SCENARIO=receipt EVENER_TUTORIAL_OUT="$OWNED_RAW_OUT" go test ./cmd/evener-hub -run '^TestTutorialTours$' -count=1 -timeout=20m`, where the new absolute directory is private scratch created for this run.
- [ ] Require the independent receipt checker to fail at entry and pass after the actual native-started repair, with checker bytes unchanged and the real tool result persisted. Inspect all three raw screenshots for actual app content and privacy. Stop and report if XCTest cannot execute on the installed runtime; do not proceed with a simulated native screen.
- [ ] Review and commit the named runner/coordinator files as `test(docs): run the receipt tour through the native app`. Publication remains unchanged until the complete manifest migration in Task 8.

### Task 7: Complete native learning and document export

**Files:**
- Modify: `cmd/evener-hub/tutorial_native_test.go`, `docs-site/scripts/tours/native.mjs`, `docs-site/scripts/tours/native.test.mjs`, `docs-site/scripts/tours/xctest/TutorialTours.swift`.
- Extract shared oracles only as needed in `cmd/evener-hub/tutorial_daily_test.go`, `cmd/evener-hub/tutorial_config_test.go`, and `cmd/evener-hub/tutorial_extensions_test.go`.
- Modify: `docs/native-app.md`, `docs/getting-started.md`, `docs/daily-work.md`, `docs/configuration.md`, `docs/extensions.md`.

**Interfaces:**
- Add `runDailyNative(t *testing.T, out string)`, `runConfigNative(t *testing.T, out string)`, and `runExtensionsNative(t *testing.T, out string)`.
- Native `configure` accepts `{scope:'global', field:'max_rounds', value:1|4}`. Web retains its project scope. Native `steer` enqueues text and invokes the queued row's steering action.
- Add `TestTutorialNativeDocumentSharing(t *testing.T)` for actual reader export. Inspect `mobile-native/src/reader/DocumentChip.tsx`, `ReaderScreen.tsx`, and `shareDocumentNative.ts`; document their current supported behavior rather than changing it.

- [ ] Add daily-work checks for real queue depth, steering/user-input transcript distinction, turn identities, provider cancellation, no late cancelled-file effect, and exactly-once continuation in the same session. Implement native queue actions and run that explicit native scenario.
- [ ] Add global-setting checks: old launch remains unchanged, new launches inherit 1 then 4, and independent layer/resolution/meta/file observations agree. Navigate Hub → Launch defaults through actual controls; run the native configuration scenario.
- [ ] Add host-prepared extension checks for command expansion, skill/hook records, correlated MCP results, and unchanged workspace bytes. Invoke both documented slash commands through native controls; run the native extensions scenario.
- [ ] Exercise document export with a known document through the real share sheet. Verify exported bytes in an available local destination, then cancellation and a recoverable failure/retry. Do not claim third-party destination behavior from a system-sheet appearance alone.
- [ ] Write short native walkthrough sections using the observed controls, entry state, expected result, and recovery path. State host-side preparation and global-only configuration. Add outbound sharing separately from inbound text/URL sharing. Keep camera, device, and distribution limits explicit.
- [ ] Run `go test ./cmd/evener-hub -run '^TestTutorial' -count=1` for deterministic helpers. Separately run the explicit Task 6 Go command with each scenario value `receipt`, `daily`, `configuration`, and `extensions`, using a fresh owned raw-output directory for each, and run `EVENER_TUTORIAL_TOURS=1 go test ./cmd/evener-hub -run '^TestTutorialNativeDocumentSharing$' -count=1 -timeout=20m`. Record which tests intentionally skip without opt-in. Review and commit the task's named files as `docs: verify native workflows and document sharing`.

### Task 8: Retain and review fresh tutorial candidates

**Files:**
- Modify: `docs-site/scripts/tours/artifacts.mjs`, `docs-site/scripts/tours/artifacts.test.mjs`, `docs-site/scripts/tours/artifact-test-support.mjs`, `docs-site/scripts/tours/refresh.mjs`, `docs-site/scripts/tutorial-artifacts.test.mjs`, `docs-site/scripts/check.mjs`.
- Create: `docs-site/scripts/tours/review.mjs`, `docs-site/scripts/tours/review.test.mjs`, `.github/workflows/docs-native.yml`.
- Modify: `.github/workflows/docs.yml`, `docs/developing-evener/tutorial-tours.md`, `docs/developing-evener/README.md`, `docs-site/README.md`, `docs-site/publication.json`.
- Replace reviewed assets and manifest under `docs/assets/tutorials/` in the same schema migration. Enumerate the exact generated paths before staging; no unrelated asset deletion.

**Interfaces:**
- Keep `generateCandidate({out, sourceRoot, required, collect, signal})` and `validateCandidate(root, required, options)` signatures.
- Manifest version 2 has surfaces `cli`, `web`, `native`, scenarios `receipt`, `daily`, `configuration`, `extensions`, and checkpoints `entry`, `action`, `verified`. `complete` is true only for all 36 rows. `deferredSurfaces` is empty.
- Native rows use `representation:'native-simulator-screenshot'` and a `native` object with `deviceType`, `runtimeVersion`, `appBundleID`, `appBuildSHA256`, and `screen:{width,height}`. Dimensions must match the actual PNG and recorded simulator screen. Existing CLI/web representations and dimensions remain unchanged.
- `reviewCandidate({candidate, published, sourceRoot, out})` validates a full current candidate and writes a review directory containing `summary.json`, `review.md`, and `refresh.patch`. The patch touches only canonical tutorial assets and their manifest. It never applies or publishes itself.
- Summary shape: `{status:'unchanged'|'review-required', sourceChanged:boolean, changedFiles:string[]}`. Compare relevant source digest and artifact contents; a revision-only change does not demand new published images.

- [ ] Add behavioral tests for native rows and provenance, all 36 checkpoints, honest subset completeness, native-source changes, non-relevant documentation changes, failed evidence, stale source, malformed images, unsafe paths, and private data. Preserve existing security and strict-coverage cases.
- [ ] Observe rejection of native candidates in current validation, then extend the schema and collectors. Include `mobile-native/`, `mobile/src/`, native driver, and XCTest inputs in source hashing. Record tool/runtime/build identity from executed tools, not guessed version strings.
- [x] Add the review-directory generator. Tests require no changes to the candidate, publication tree, or source tree; refuse existing output paths and symlinks; ensure the binary patch reconstructs exactly the reviewed candidate in a disposable copy. An unchanged source digest plus unchanged content yields `unchanged`, regardless of commit distance.
- [x] Retain CLI/web candidates from `.github/workflows/docs.yml` through `check --surfaces cli,web --out "$RUNNER_TEMP/tutorial-candidate"`. Upload only a successfully validated sanitized candidate, never private raw logs. Preserve the existing publication build and browser gates.
- [ ] Add a manually dispatched native workflow on `macos-26`, reusing the native workflow's Node 22.13.1, Ruby 3.3.6, Bundler 2.7.2, and Xcode 26.6 setup after confirming runner availability. Use the existing `secrets.APPLE_TEAM_ID` input only for the simulator build. No TestFlight/distribution steps. Missing tools/team/runtime fail the requested run visibly. Run all surfaces there when producing a complete refresh candidate, avoiding any merge of evidence from different source digests.
- [ ] Run `node --test docs-site/scripts/tours/*.test.mjs docs-site/scripts/tutorial-artifacts.test.mjs`, all CLI/web scenarios, then `node docs-site/scripts/tours/refresh.mjs check --surfaces native`. Generate a full refresh with `node docs-site/scripts/tours/refresh.mjs refresh --surfaces cli,web,native --out "$OWNED_CANDIDATE"`.
- [x] Run `node docs-site/scripts/tours/review.mjs --candidate "$OWNED_CANDIDATE" --out "$OWNED_REVIEW"`. Review every image and selectable transcript against its evidence, privacy, and source. Apply the proposed patch only after that review; update canonical asset references and publication entries. Preserve provenance exactly.
- [ ] Update the developer and user coverage notes from observed results. Build with `npm --prefix docs-site run build` and run `(cd docs-site && npx playwright test)`. The validator, schema tests, and all committed assets must move together; never leave the published set declaring nonexistent native evidence.
- [x] Review and commit the exact tooling, workflow, documentation, and reviewed asset files as `feat(docs): retain reviewable cross-surface tutorial captures`.

Task 8 checked steps cover only the accepted version 1 CLI/web subset (`1c879486a5` tooling, `425b58628b` assets), under the ledger ruling. All 24 images and 12 transcripts were reviewed, with an exact 37-file patch reconstruction, 99 focused tests, 144 build tests, and 9 browser checks. Native schema, source hashing, workflow, and 36-row activation remain unchecked. No remote workflow execution is claimed.

### Task 9: Verify the complete branch and hand it back

**Files:** All changed files; update this plan's checkboxes and evidence notes only where supported by completed checks.

**Interfaces:** The spec's acceptance table is the completion checklist. Audit-era passing tests are not evidence for this branch.

- [ ] Inspect the complete diff for scope, stale commands, duplicated procedures, unsupported claims, and accidental private data. Check each requirement against its owning task and fresh evidence.
- [ ] Run fresh documentation build/browser checks, focused recovery and tutorial-helper tests, and changed native/TypeScript checks. Read gate scripts, run heavy gates serially, and distinguish non-opt-in skips from executed journeys. Use the explicit tour runs as execution evidence.
- [ ] Run simplification on changed implementation code without changing behavior, then rerun affected checks. Obtain a fresh whole-branch review on Astra after the chosen per-task review process. Resolve findings through behavioral evidence, not word matching.
- [ ] Inspect exact source and manifest correspondence and the proposed workflow artifacts. Do not claim remote CI execution unless a workflow actually ran; preserve any missing native prerequisite as an explicit blocker.
- [ ] Remove owned scratch, stopped test processes, and owned simulator instances after retaining only artifacts the report links. Leave installed dependencies and deliverables needed to inspect the work. Do not delete pre-existing devices or another session's scratch.
- [ ] Report the changed guides/tooling, verification commands, reviewed asset provenance, and any limits. Ask separately before pushing or merging; this plan carries no new publication or merge authorization.

Task 9 is ongoing. The four accepted simplifications and unchanged-review procedure correction are implemented; the optional unchanged-output Git bypass was skipped because it would change environmental failure behavior. Fresh verification and capture follow the tooling commit. Final Astra review and overall cleanup remain the parent’s responsibility; no whole-plan acceptance is claimed.

## Self-review before handoff

- [x] Every confirmed defect and recommendation in the audit has an owning task and an acceptance check.
- [x] Native configuration and steering follow native semantics; no web screenshot or demo hub stands in for native runtime evidence.
- [x] Interfaces used across tasks agree; shared files have a sequential integration point.
- [x] Failed setup, old credentials, incompatible history, native scope, and unsafe candidate publication have explicit tests.
- [x] Native toolchain/team prerequisites and the first real-app proof gate remain explicit. No implementation or fresh test success is claimed by this plan.

Jesse approved subagent-driven execution on 2026-10-20 and chose to resolve native setup later. Proceed with independently verifiable repairs. Native execution and captures remain pending; do not activate the complete native publication matrix without that evidence.
