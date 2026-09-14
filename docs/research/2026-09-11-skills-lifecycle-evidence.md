# Skills lifecycle live evaluation evidence

Date: 2026-09-12. Branch: `wip/skills-implementation-study`, base
`8c6f1ae4fcc022c8a3c432c010444630fe8dadd5` (Task 14's final commit).
Harness: [`agent/skills_live_test.go`](../../agent/skills_live_test.go)
(`//go:build eval`, explicit `EVENER_LIVE_TESTS=1` opt-in, required
`-skills-eval-model` flag). This is the explicitly opted-in live evaluation the
[skills implementation study](2026-09-10-skills-implementation-study.md) called
for; the delivered contract it measures is documented in
[skills.md](../skills.md).

## Scope and oracle discipline

The behavior under evaluation is what a real model does with skill mentions,
structured selections, and compaction reloads. Typed activation, selection,
delivery, and compaction records are the primary oracle: inventory entries,
`SkillTurnState` input/outcome/reload-reminder records, delivery obligations,
`SKILL_ACTIVATED` events, and `CONTEXT_COMPACTION` events. Opaque fixture
markers (`LIVE_SKILL_a983`, `LIVE_SKILL_b742`) in the model's natural-language
output are supplementary proof of use, never the proof of invocation. Every
run was recorded; no failure was re-rolled until green. Expected relevant sets
were declared before each evaluated run and reported as extra/missing, never
moved after seeing output.

## Configuration

| Item | Value |
|---|---|
| Registry selector | `lunaroute/glm-5.3` |
| Provider instance | `lunaroute` (base `openai-compatible`, base URL `https://gw.lunaroute.com/v1`) |
| Model | `glm-5.3` |
| Model surface / context window | `generic` / 131072 tokens (logged by the harness at run start) |
| Provider config | `EVENER_PROVIDERS_CONFIG` → `~/.config/evener/providers.toml` (user layer) |
| Credentials | the sibling `credentials.toml` store, wired into `registry.Load` exactly as `cmdutil.LoadRegistry` does for every binary |
| Opt-in | `EVENER_LIVE_TESTS=1` |
| Command | `EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' -count=1 -v -timeout 45m -args -skills-eval-model=lunaroute/glm-5.3` |

`-timeout 45m` is a tripwire bound added to the plan's bare command (go test's
default 10m is tight for an eleven-case live corpus under latency variance);
the green run needed 238s. No credentials appear anywhere in the harness, its
output, or this record.

## Complete run log

| Run | Result | Detail |
|---|---|---|
| Exclusion check (`EVENER_LIVE_TESTS` unset) | SKIP, exit 0 | `explicit live-test opt-in required`, 0.02s, no provider request. |
| Flag check (`EVENER_LIVE_TESTS=1`, no `-skills-eval-model`) | FAIL, exit 1 | `-skills-eval-model is required` — the guard fails hard rather than silently picking a model. |
| Live run 1 | FAIL (configuration) | Smoke returned HTTP 401 from the provider: the request went out with no LunaRoute API key. Cause: the template's bare `registry.Load()` reads only the provider layer; the key lives in the `credentials.toml` store, which production binaries wire via `cmdutil.LoadRegistry`. Corpus not run. Fix: the harness now loads the credentials store and passes `registry.WithCredentials(...)` — the same resolution every binary uses. |
| Live run 2 | 9/11 PASS | Smoke, all seven text cases, and `structured_inline` passed. `reload_selection` and `fallback_reminder` failed at `compaction recorded 0 handoff receipts`: the oracle read the transient `PendingHandoffs` list after the compaction turn's own continuation round had already consumed it. Harness oracle defect, not model behavior. |
| Live run 3 | 8/11 PASS | `fenced` failed as a **model behavior variance** (see below). `reload_selection`/`fallback_reminder` failed on a second oracle defect: turn-stamped receipts are not visible through session history in non-persistent sessions, so the attempt to read the selection from `SkillTurnState.Compaction` found nothing. |
| Live run 4 (final) | **11/11 PASS**, 238.4s | Full record below. |
| Live run 5 (committed code) | **11/11 PASS**, 181.7s | Validation of the self-review hardening below; this is the run that executed exactly the committed harness. |

The two compaction-case oracle corrections and one self-review hardening were
made from recorded evidence, never to make a red case pass. Run 4's
diagnostics show the model calling `compact_context` (tool calls
`use_skill use_skill communicate compact_context`), a real fold (checkpoint
turns 9→3), and the handoff consumed within the same turn — so the oracle now
reads the durable typed records (the `compaction_reload` outcome set and the
reload-reminder turn state) that the machinery guarantees regardless of when
consumption happens. The self-review hardening: `fallback_reminder`'s
re-invocation check originally accepted any `model_tool` outcome for
`pkg:probe`, which turn 1's activations could already satisfy; it now requires
an outcome recorded in a turn state AFTER the reminder turn, proving the
invocation happened once the compaction had dropped the body. Run 5 validated
the hardened oracle against the live machinery. Every original assertion (real
compaction event, exact selection, no unselected reload, consumed handoffs,
post-compaction re-invocation, marker use) is still enforced.

## Final run (run 5) per-case record

| Case | Result | Typed evidence |
|---|---|---|
| smoke | PASS 3.04s | `SMOKE_37ab` returned; no skill activation. |
| operative | PASS 3.80s | ordinary activation `[pkg:probe]`; marker `LIVE_SKILL_a983` in output. |
| quotation | PASS 3.20s | activations `[]` — the quoted token was not invoked. |
| fenced | PASS 13.44s | activations `[]` — the fenced code block was treated as text. |
| path | PASS 15.39s | activations `[]` — path/URL literals were not invoked. |
| negation | PASS 3.64s | activations `[]`; output `ACK_927`. |
| multiple | PASS 7.24s | activations `[pkg:probe pkg:second]`; both markers returned. |
| near_miss | PASS 4.66s | activations `[]` — `/pkg:probex` produced no false activation of the real name. |
| structured_inline | PASS 3.80s | route `user_selection`, `UserAuthorized=true`, typed `SkillInputRecord` names `[pkg:probe]` with the original prose and group `live-inline-1`; one `SKILL_ACTIVATED` event; marker present. Activation came from the typed selection — the prose contains no slash token. |
| reload_selection | PASS 76.49s | real compaction (checkpoint turns 9→3, est-tokens 829→432; summarize 4→4) after the model's own `compact_context` call; compaction_reload outcome `pkg:probe(pending)`, invocation `fold-4:pkg:probe`; no `pkg:second` reload outcome; handoffs consumed; marker present on the continuing turn. Expected set declared before the run: `{pkg:probe}`; delivered outcome set matched exactly (no extra, no missing). |
| fallback_reminder | PASS 46.64s | absent-selection compaction (model omitted `reload_skills`); typed reload reminder recorded with `Selection.State=absent` and complete inventory `[pkg:probe pkg:second]`, both `reloadable`; the model re-invoked `pkg:probe` itself after the reminder — `use_skill` tool call, typed `model_tool` outcome `skill-op-4` status `delivered` recorded after the reminder turn; marker present. (This run the model also re-invoked `pkg:second` — both skills are `reloadable` and the reminder permits either; the relevant-skill assertion is on `pkg:probe`.) Expected reminder inventory declared before the run: `{pkg:probe, pkg:second}`; matched exactly. |

## Measured behavior summary and failure categories

Across four executed corpus runs (runs 2–5) of the ten behavior cases:

- **Typed-inline interpretation (7 text cases, 28 executions):** 27/28 matched
  the expected tracked behavior. The one miss: run 3's `fenced` case activated
  `pkg:probe` from a fenced code block the other three runs left inert — a
  real model variance rate of 1/4 for that case, recorded as measured (pass,
  fail, pass, pass across runs 2–5). Everything else — operative requests,
  quotations, paths/URLs, negations, multiple selections, near-miss names —
  was stable across all four runs.
- **Structured selection (4 executions):** 4/4 — runtime activation independent
  of text position, with the typed input and authorization records exact.
- **Compaction reload selection (4 executions):** the model called
  `compact_context` with a valid, exactly-relevant selection (`{pkg:probe}` of
  two loaded skills) every time it got that far; the two earlier failures were
  harness oracle defects, not model behavior.
- **Fallback reminder (4 executions):** the model omitted the selection when
  asked, the complete typed reminder was delivered, and the model re-invoked
  the relevant reloadable skill itself (run 5 re-invoked both listed skills,
  which the reminder permits).

Failure categories, case-level counts across all live runs:
configuration/credential (1: run 1's smoke, blocking the corpus that run),
harness oracle shape (4: two compaction cases in each of runs 2 and 3),
model behavior (1: run 3's `fenced`). No refusals, malformed selections, or
quota/network failures were observed in runs 2–5.

## Reproducing

```sh
EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' \
  -count=1 -v -timeout 45m -args -skills-eval-model=lunaroute/glm-5.3
```

Requires the configured provider layer and its credentials store, network
access to the provider, and explicit opt-in; without `EVENER_LIVE_TESTS=1`
the test skips and issues no provider request. The harness logs the resolved
model identifiers (instance, model, surface, context window) at the start of
every run.

The five raw run logs (runs 1–5) are retained with the task record under
`.superpowers/sdd/2026-09-11-skills-lifecycle/run-logs/`, beside the task
report.

## Final gate and acceptance evidence (Task 16)

Task 16 ran the required gates and mapped every acceptance row of the plan's
coverage table to the concrete committed test that proves it. Gate commands,
exit codes and durations are recorded in the table below; the acceptance
mapping follows.

### Required gates

| Command | Exit | Duration | Result |
|---|---|---|---|
| `make test-api-package` | 0 | 3.5s | `qualified @evener/appwire-client@0.1.0: installed imports, declarations and read-only example` (outside-checkout install, ESM + CommonJS entries) |
| `make vet` | 0 | 8.0s | go vet across every non-fuzz workspace module; no diagnostics emitted |
| `make merge-approval-gate` | 0 | 7m18s | all lint phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, secret-scan), build PASS, `ROOT_FULL=1 make test` waves PASS (root 105.1s, agent, llm, auth, envvars, invariant, identifier, web 114.7s); zero test failures |
| `TMPDIR=/tmp make test-web-browser` | 0 | 3m16s | 6/6 guards PASS: web-layoutguard, web-overflowguard, web-shellguard, web-spawnguard, web-transcriptscrollguard, web-skillguard (the full-stack guard through the real hub; its nine scenarios are asserted present in the milestone report, so none can skip silently) |

### Stage 2/3 gate families and affected suites (all serial, all exit 0)

| Command | Result |
|---|---|
| `go test ./agent/internal/tool ./agent/schema -run '^Test' -count=1` | 379/379 PASS |
| `go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestUseSkill_|TestStandaloneSkillActivation|TestRestoreFrozenSkillBodies|TestPrepareModelRequest_)' -count=1` | 67/67 PASS, 0 skip |
| `go test -race ./agent -run '^(TestSkillDelivery_|TestFoldPublication_)' -count=1` | 29/29 PASS, no race reports (34.9s) |
| `go test ./agent/internal/contextmgr ./agent/schema -run '^Test' -count=1` | 384 PASS + 3 by-design fuzz-engine opt-in skips (`TestCompactionSeqFuzz` and siblings skip with an explicit `make test-fuzz` pointer in every default run; unchanged at the branch base) |
| `go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestSkillReload|TestSkillCompaction_|TestPinnedNote_|TestMaybeElicitNoteBeforeCompaction_|TestApplyPendingForceCompact_|TestSessionCompact_|TestFoldPublication_|TestPrepareModelRequest_)' -count=1` | 157/157 PASS |
| `go test -race ./agent -run '^(TestSkillCompaction_|TestSkillDelivery_|TestSkillReload_|TestFoldPublication_)' -count=1` | 72/72 PASS, no race reports (64.9s) |
| `go test ./agent -run '^(TestClientMutation_|TestSkillActivation_|TestSkillDelivery_)' -count=1` | 181/181 PASS |
| `go test ./server -run '^TestAppWireMutation' -count=1` | 34/34 PASS |
| `go test ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/... -count=1` | all packages ok |
| `go generate ./appwire/...` | exit 0, zero diff — generated outputs fresh |
| `go run ./cmd/evener-dev/bin dev agent-shards -short -count=1` (the gate's agent stream invocation, reproduced standalone) | exit 0; 4 shards, 686 + 3685 + 686 + 691 = 5748 tests |

### Gate findings fixed during this stage (each red → green)

| Finding (gate, phase) | Root cause | Fix |
|---|---|---|
| `lint-evenerfuzz` compile failure (merge gate lint) | Two fuzz drivers still called pre-branch signatures: `ElicitNote` (now takes `[]schema.SkillInventorySummary`) and `clientMutationInput` (now takes `[]string` skill names) | Pass `nil` at the two drivers — the same convention every other caller of both signatures uses; both drivers' deterministic seed replays pass |
| `lint-golangci`: 19 findings (merge gate lint) | Branch-added code never run under golangci before: 12 revive (missing doc comments on exported lifecycle/catalog types; `close` builtin shadowed by a test const), 5 modernize (`errors.As` → `errors.AsType`, `strings.Index` → `strings.Cut`), 1 nilerr, 1 staticcheck QF1002 | Doc comments added; const renamed to `closeTag`; `errors.AsType`/`strings.Cut`/tagged-switch rewrites; the one intentional nil return (`ResolveSkillContent`'s documented empty-body seam) carries a `//nolint:nilerr` with its reason, per repo convention |
| `lint-generated` failure (merge gate lint) | Task 14 hand-wrote the `web-skillguard` row into the GENERATED `docs/developing-evener/testing.md` table without updating the `make/testing.mk` annotation it is generated from | The skillguard text was ported into the `test-web-browser` annotation; `make generate` now reproduces the committed table byte-for-byte |
| `TestNoBareWallClockDeadlineInAgentTests` (merge gate test wave) | The live harness's five per-case wall-clock bounds had no tripwire marker | The audit's sanctioned `// TRIPWIRE:` markers added to each `context.WithTimeout` line, stating the measured case times the bound sits far above; no assertion or bound changed |
| `TestMakeWebCommandsContainNodeProcessState`, `TestMakeTestWebBrowserSuccessIsConciseAndRemovesEvidence` (merge gate test wave) | Task 14 registered `web-skillguard` in `test-web-browser.sh` without updating the fake-toolchain fixtures, and its `npm run build` step ran without `NODE_DISABLE_COMPILE_CACHE=1` — a real violation of the frontend gate contract the process-state test exists to pin | Fake `go` taught the `test` subcommand (records and exits 0); the concise test now models the built frontend (`dist/index.html`) and asserts six verdicts including `web-skillguard`; the production script sets `NODE_DISABLE_COMPILE_CACHE=1` on the skillguard build like every other frontend command |

Every fix above was made against a reproduced standalone failure of the same
phase, and the decisive gate (`make merge-approval-gate`) was rerun in full
after the last one.

### Acceptance coverage mapping

Each row of the plan's acceptance table, mapped to the actual committed
tests and runs that prove it (all names verified against the committed
suites and exercised by the recorded gate runs):

| Spec acceptance area | Actual evidence |
|---|---|
| Shared loading | `TestSkillRenderPreservesCompleteInstructions` and `TestSkillLoadUsesOneCurrentSourceVersion` (agent/skill, same source + complete body through the shared renderer); `TestSkillDelivery_CompleteBody` (complete body at the actual provider boundary); `TestUseSkill_ReturnsBody`; `TestClientMutation_SkillSelectionQueueConsumesCurrentDiskBytes` (selected route loads current disk bytes); live `operative`/`multiple` (complete marker + typed activations). |
| Input | `TestClientMutation_InputShapesMatchDaemonBoundary`, `TestClientMutation_SkillInputRoundTrip`, `TestClientMutation_SkillSelectionStartConsumesAtTurnBoundary`, `TestClientMutation_SkillSelectionSameIDAlteredSelectionConflicts`, `TestClientMutation_SkillSelectionSurvivesRestartBeforeClaim`, `TestClientMutation_SkillSelectionRestartAfterClaimRequeuesSelection`; frontend `skillSelections.test.ts`, `draft.test.ts`, `recoveryDraft.test.ts`; browser guard canonical/draft-remount/queue/attachment scenarios. |
| Resolution | `TestSkillCatalogResolutionAndViews`, `TestResolveSkillUsesUniqueSuffixAndExactPrecedence`, `TestResolveSkillRejectsAmbiguousUnqualifiedName`, `TestResolveSkillContentRejectsAmbiguousSuffix`, `TestSkillDiscoveryPrecedence`, `TestSkillDiscoveryInvalidWinnerAndDiagnostics`, `TestSkillActivation_Resolution`, `TestNormalizeMutationInputSkillNameValidation`. |
| Completeness | `TestSkillRenderPreservesCompleteInstructions` (full opaque bytes incl. delimiter collisions), `TestSkillDelivery_CompleteBody`, `TestSkillDelivery_PreDispatchCompaction`, `TestSkillDelivery_ConfiguredOutputLimit` (explicit limit is a complete-delivery failure, not a tail). |
| Deduplication | `TestSkillDelivery_RevalidateAfterFold` (provisional dedupe corrected by a typed causal notification after a real fold), `TestSkillDelivery_ProjectionModelSwitch`, `TestSkillDelivery_ResponsesContinuationPlanning`, `TestSkillDelivery_FallbackSmallerWindow`, `TestSkillDelivery_TransportRetry`, `TestSkillDelivery_SaveRestore`, `TestSkillReload_Repeated_RetainedContentNotDuplicated`. |
| Failure | `TestSkillActivation_Failure`, `TestSkillActivation_PrepareFailureIsAtomic`, `TestSkillActivation_SaveMetaReturnsFilesystemFailure`, `TestSkillDelivery_MandatoryBudgetFailure`, `TestSkillDelivery_FailedReinvocationPreservesInventory`, `TestSkillReload_Budget_MandatoryMetadataExceeds`, `TestSkillReload_CurrentSource_MissingReplacedSource`, `TestClientMutation_SkillSelectionMissingSecondNameFailsVisible` (zero activations, zero dependent requests), `TestClientMutation_SkillSelectionSteerFailedPreparationKeepsTurnRunning`, `TestSkillReloadSelection_CompactToolSchemaInvalidSchedulesNothing`. |
| Compaction choice | `TestSkillReloadSelection_Presence`, `TestSkillReloadSelection_NudgeListsLoadedSkills`, `TestSkillReloadElicitation_SessionParsesBlockAndStoresSelection`, `TestSkillReloadElicitation_SessionInvalidSelectionPreservesNote`; live `reload_selection` (model's own exact selection). |
| Automatic lifetime | `TestMaybeElicitNoteBeforeCompaction_FiresWhenEnabledAndHighPressure` / `_NoopWhenLowPressure` / `_SkipsWhenNoteAlreadySet`, `TestApplyPendingForceCompact_NoRequest_NoOp`, `TestApplyPendingForceCompact_CompactsWithNote`, `TestSkillCompaction_ClearNoteCancelsAutomaticOperation`, `TestSkillCompaction_ClearNoteLeavesForcedOperation`. |
| Elicitation latch | `TestSkillCompaction_LatchFirstSelectionWins`, `TestSkillCompaction_LatchNonemptyNoteSkipsElicitation`, `TestSkillCompaction_LatchPendingOperationSkipsElicitation`, `TestSkillReloadElicitation_Blocks` (real selection-only and stale-response barriers). |
| Tool availability | `TestSkillReloadSelection_NudgeListsLoadedSkills` (no-tool nudge), `TestSkillReload_Reminder_AbsentSelection` / `_InvalidSelection` / `_ExplicitEmptySelection` (complete reminder classification), `TestSkillActivation_RawRead` (untracked read fallback creates no activation); live `fallback_reminder`. |
| Reload | `TestSkillReload_CurrentSource_ReloadsNewBytes`, `TestSkillReload_CurrentSource_MissingReplacedSource`, `TestSkillLoadUsesOneCurrentSourceVersion`, `TestSkillDelivery_ChangedSourceBeforeDispatch` (changed-content notice), `TestSkillDelivery_DeletedSourceBeforeDispatch` (failure, never false delivery). |
| Repeated lifetime | `TestSkillReload_Repeated_RetainedContentNotDuplicated`, `TestSkillCompaction_AcceptanceSurvivesMetadataRoundTrip`, `TestSkillDelivery_SaveRestore`, `TestFoldPublication_DurablyRecordedTurnSurvivesRestartBeforeRewriteSync`, `TestSkillLifecycleSnapshot_MetaRoundTrip`; handoff-receipt consumption asserted in `TestSkillReload_CurrentSource_ReloadsNewBytes` and the `TestSkillReload_Reminder_*` cases. |
| Publication | `TestFoldPublication_CompetingFoldsCommitTranscriptEntriesInPublishOrder`, `TestPrepareModelRequest_WinningFoldEmitsCompactionEvent`, `TestPrepareModelRequest_LosingFoldAttemptEmitsNoCompactionEvent`, `TestPrepareModelRequest_LosingFoldDoesNotMutateSharedPayloads`, `TestSkillCompaction_AcceptanceForcedRequestSupersedesAutomaticOperation`, `TestSkillCompaction_AcceptanceSecondRequestRejected`, `TestSkillCompaction_StaleCapturedGenerationRejected`. |
| Invocation policy | `TestSkillActivation_PolicyMatrix` (full 5-route × control matrix), `TestSkillActivation_Routes`, `TestSkillControlsStrictBooleanValues`, `TestSkillCatalogResolutionAndViews` (filtered advertisements vs retained full catalog), `TestThreadReadAdvertisesRealSkillControls`, `TestPastThreadReadCarriesSkillCatalog`. |
| Discovery | `TestSkillDiscoveryPrecedence` (every precedence level), `TestPortableProjectDirectory`, `TestSkillDiscoveryInvalidWinnerAndDiagnostics`, `TestSkillSourcesMetadataOnlyFirstManifestReservation`, the `TestPastThreadSkillCatalog*` family, `TestScanSkillsDir_MissingDirIsNoop`. |
| Browser | `TestSkillComposerBrowser` (browserguard tag) via `TMPDIR=/tmp make test-web-browser`: nine scenarios — canonical, draft-remount, queue (held turn + drain), steering, attachment, capability-loss, failed-activation, delayed-send, transport-loss — each asserted present in the driver's milestone report (no silent skip), with actual provider-request/mutation evidence. |
| Role lifetime | `TestRestoreFrozenSkillBodiesValid` and siblings (legacy bytes, optional provenance), `TestSkillActivation_PrepareFrozenAndLegacyDoNotAuthorize`, `TestSkillActivation_LegacyRestoreHasEmptyOrdinaryState` (no historical backfill), `TestSkillDelivery_FrozenPlusOrdinary`, `TestSkillReload_Preload_DualProvenanceReloadsOrdinary`, `TestSkillReload_Preload_OnlySelectionIsNoOp`. |
| Raw file reads | `TestSkillActivation_RawRead` (full and partial `read_file` of the source create no activation or authorization). |
| Budget priority | `TestSkillReload_Budget_OrderedPriority` (mandatory metadata first, new group retained, excess reloads fail individually), `TestSkillReload_Budget_MandatoryMetadataExceeds`, `TestSkillDelivery_MandatoryBudgetFailure` (visible failure, no additional fold). |
| Pending operation | `TestSkillCompaction_SaveFailedRequestIsTypedAndRetryable` (visible typed save failure), `TestSkillCompaction_StaleAcceptanceRejectedWhileOperationPending`, `TestSkillCompaction_StaleCapturedGenerationRejected`, `TestSkillLifecycleSnapshot_PendingCompactionCloneDetached` (no unchanged writes / detached snapshots). |
| Note semantics | `TestPinnedNote_MetaRoundTrip`, `TestPinnedNote_SurvivesResume`, `TestSkillCompaction_ClearNoteCancelsAutomaticOperation` (empty clears), `TestSkillCompaction_ClearNoteLeavesForcedOperation`, `TestSkillReloadSelection_CompactToolSelectionOnlyRequestsCompaction`, `TestSkillReloadSelection_CompactToolUnknownNameKeepsNote`, `TestSkillReloadSelection_CompactToolDoubleCallKeepsFirstSelection`. |
| Historical sessions | `TestSkillActivation_LegacyRestoreHasEmptyOrdinaryState` (ordinary state born empty; pre-lifecycle sessions track future activations only), `TestSkillActivation_RestoreSessionFromMeta` (state round-trips from the saved snapshot, never reconstructed), `TestRestoreFrozenSkillBodiesNamesWithoutBodies`. |
| Delegates | `TestSkillActivation_Routes` (child inventory seeded by exactly its own role preload; parent imports nothing), `TestDelegateResourceCreate_UsesFrozenDescriptorAfterCommit` (frozen skill bodies isolated from post-commit mutation). |
| Protocol support | `TestSkillInputRejectsRawPathAndBody`, `TestSkillInputUnmarshalAcceptsOnlyTypeAndName`, `TestInputBearingParamsDecodeSkillSelection`, `TestInputBearingParamsRejectRawSkillBodyAndPath`, `TestNormalizeMutationInputSkillRejectsForbiddenStructFields`, `TestValidateSkillInputSupport` (false/absent capability fails closed), `TestTurnMutationsConsumeSkillInputWhenWired`, `TestThreadReadAdvertisesRealSkillControls`; frontend `skillInput.test.ts` pins the generated capability/controls/diagnostics type contract. |
| Live behavior | `TestSkillsLive` (eval tag, `EVENER_LIVE_TESTS=1` opt-in + required `-skills-eval-model`): runs 4 and 5 both 11/11 PASS; the Task 15 review's independent sixth run also 11/11. Full per-case record above; the one model-variance failure (`fenced`, run 3) is recorded as measured (1 failure in 4 corpus executions of that case). |
| Documentation/review/PR | `docs/skills.md` implements the delivered contract (coverage verified line-by-line by the Task 15 independent review §6); this evidence document; the per-task independent review reports 1–15 and the two pre-PR review reports are untracked working artifacts retained under the gitignored `.superpowers/sdd/2026-09-11-skills-lifecycle/` directory, with the review resolution summarized in this document. The PR opens after this stage's report by design; it is the remaining delivery step and is not claimed here. |

The plan's table carries 26 data rows (the brief's "24-row" count
undercounts by two); every row is mapped above.

### Honest limitations

- **Live model evaluation** requires the configured provider layer, its
  credentials store, network access, and the explicit opt-in; without
  `EVENER_LIVE_TESTS=1` the suite skips and issues no provider request
  (verified twice, Task 15 and its review). The one recorded model-variance
  failure (`fenced`, run 3 of the corpus) is documented as measured, not
  smoothed over.
- **Browser gates** require Chrome/Chromium on the host and the built
  frontend; `TMPDIR=/tmp` is required for the Unix-socket path length limit.
  The skill guard fails if any of its nine scenarios is missing from the
  driver's milestone report, so a launch failure cannot masquerade as a pass.
- **Deterministic suites** (the default gates, the Stage 2/3 families, and the
  race runs) require neither credentials nor network.

## Post-rebase provenance and rebased-head gates (2026-09-12)

The branch was rebased onto `main` on 2026-09-12 to resolve the merge
conflicts. Every SHA recorded earlier in this document is the pre-rebase
equivalent of the same content; the rebase changed hashes, not substance.
Two rebase-surfaced fixture commits were added on top to reconcile test
fixtures with main's incoming changes:

- `1097c0a5d9` — fix(web): complete skill fixtures in spawn catalog tests after rebase
- `b8680d001b` — fix(native): advertise invocation controls in catalog test fixtures after rebase

Rebased head: `b8680d001b69630aaa2044b3771ca092f9a681e5` (`origin/main` is an
ancestor; working tree clean). All gates below were re-run serially on this
head on 2026-09-12 (host under concurrent roborev load):

| Command | Exit | Duration | Notes |
| --- | --- | --- | --- |
| `make test-api-package` | 0 | 3s | `qualified @evener/appwire-client@0.1.0` |
| `make vet` | 0 | 4s | no diagnostics |
| `make merge-approval-gate` (run 1) | 2 | 492s | environmental: 2 vitest tests timed out at 5000ms (`Session.test.tsx` explicit-Resume, `Spawn.test.tsx` completion-issue-ownership) with load average ~39 on 16 CPUs; both suites re-run standalone: 284/284 PASS, exit 0 |
| `make merge-approval-gate` (run 2) | 0 | 369s | all lint phases PASS, build PASS, ROOT_FULL test waves PASS (root 135.5s, agent 8.2s, llm 9.1s, auth 1.6s, envvars 0.5s, invariant 0.4s, identifier 1.2s, web 194.3s), `test-native` and `test-api-package` PASS |
| `TMPDIR=/tmp make test-web-browser` | 0 | 199s | 6/6 guards PASS: web-layoutguard, web-overflowguard, web-shellguard, web-spawnguard, web-transcriptscrollguard, web-skillguard |
| `go test ./agent/internal/tool ./agent/schema -run '^Test' -count=1` | 0 | <1s | both packages ok |
| `go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestUseSkill_|TestStandaloneSkillActivation|TestRestoreFrozenSkillBodies|TestPrepareModelRequest_)' -count=1` | 0 | 2s | ok |
| `go test -race ./agent -run '^(TestSkillDelivery_|TestFoldPublication_)' -count=1` | 0 | 4s | ok, no race reports |
| `go test ./agent/internal/contextmgr ./agent/schema -run '^Test' -count=1` | 0 | 1s | both packages ok |
| `go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestSkillReload|TestSkillCompaction_|TestPinnedNote_|TestMaybeElicitNoteBeforeCompaction_|TestApplyPendingForceCompact_|TestSessionCompact_|TestFoldPublication_|TestPrepareModelRequest_)' -count=1` | 0 | 2s | 157/157 PASS (verified non-empty: 157 RUN / 157 PASS / 0 FAIL / 0 SKIP), matching the pre-rebase count |
| `go test -race ./agent -run '^(TestSkillCompaction_|TestSkillDelivery_|TestSkillReload_|TestFoldPublication_)' -count=1` | 0 | 5s | ok, no race reports |
| `go test ./agent -run '^(TestClientMutation_|TestSkillActivation_|TestSkillDelivery_)' -count=1` | 0 | 5s | ok |
| `go test ./server -run '^TestAppWireMutation' -count=1` | 0 | 2s | ok |
| `go test ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/... -count=1` | 0 | 1s | all packages ok |
| `npx vitest run src/panes/session/composer src/protocol/reducer.test.ts src/protocol/skillInput.test.ts` (frontend, touched suites incl. `pendingTurnsStore.test.ts`) | 0 | 23s | 30 files / 820 tests PASS |
| `make generate` | 0 | 1s | zero-diff (`git status --porcelain` empty after) |

No assertion, filter, pairing, or tolerance was weakened; the run-1 gate
failure was diagnosed as host-load flakiness (concurrent roborev) and the
gate was re-run in full to a zero exit.

## Post-PR-review fix round (2026-09-12, PR #1168 head de1fa62d24)

A post-review fix round on the rebased head handled two things: the PR's CI
`fuzz` job failing on the committed-seed replay, and the roborev review of the
final head (job 8557, range a361254..de1fa62; verdict "No Critical or High
issues; 8 Medium and 1 Low findings").

### CI fuzz failure: root cause and fix

RED (exact CI reproduction):

```
cd agent && GOENV=off GOFLAGS= GOWORK="$(cd .. && pwd)/go.work" \
  go test -run '^FuzzAgentClonesShareNoMutableState$' -tags evenerfuzz -count=1 .
--- FAIL: FuzzAgentClonesShareNoMutableState/seed#0/delegate_start_descriptor
    clone_aliasing_fuzz_test.go:100: delegate start descriptor.FrozenSkillMetadata:
    copy shares the original's slice backing array
```

Root cause: the branch added `FrozenSkillMetadata []schema.FrozenSkillPreload`
to `delegatestore.Descriptor` (agent/internal/delegatestore/record.go:86-90),
and `cloneDelegateStartDescriptor` (agent/delegate_tree_start.go) deep-copied
every sibling slice but not that one, so clones shared the original's backing
array. `schema.FrozenSkillPreload` is all immutable string fields, so copying
the slice's backing array deep-copies every element. Fix:
`clone.FrozenSkillMetadata = append([]schema.FrozenSkillPreload(nil), descriptor.FrozenSkillMetadata...)`
(commit 3d6169509e). GREEN: the same command now exits 0.

The full `make fuzz` run then surfaced two more stale committed-seed oracles
the skills-lifecycle contract changes had left behind (both fixed in
8d59d5d883; a gofmt nit in that edit fixed in 22861efa9c):

- `FuzzToolRegistryProgram` (agent/internal/tool) listed `use_skill` among the
  tools that must carry a default output limit, contradicting the deliberate
  no-default contract from e97bb663b9 (skill content is complete-or-fail at
  the operation level; `TestToolRegistry_UseSkillHasNoDefaultTailLimit` pins
  it). The oracle now asserts the no-limit contract explicitly.
- `FuzzSkillDiscoveryProgram` (agent/skill) generated fixtures with non-string
  `allowed-tools` entries, which discovery now rejects as `invalid_metadata`
  (`TestSkillControlsRejectMixedAllowedToolsArray` pins the category;
  `invalid_control` is the boolean-controls category — this sentence said
  `invalid_control` before the second post-review round corrected it), and
  called
  `LoadSkillBody` with a bare `SkillFile`-only meta, which the loader's
  source-identity validation rejects. The oracle now uses valid document
  variants, asserts the invalid-controls exclusion explicitly, and loads via
  the discovery-recorded meta.

### Roborev findings (job 8557): verdicts

| # | Finding | Verdict | Evidence and what changed |
| --- | --- | --- | --- |
| M1 | Steering routing ignores selected skills | FIXED | `decideSteerRoute` only looked at text/attachments/queue, so a selection-only Steer/Shift+Enter fell through to the focus-only no-op although `submitAction` carries `skillNames` on steer/drain (Composer.tsx:1187-1188) and the daemon contract counts selection-only input as content. Added `hasSkills` (submitRouting.ts; call site Composer.tsx:1292); red test in submitRouting.test.ts failed ("none" vs "steer") before the fix. |
| M2 | Editing a queued message drops selected skills | DEFERRED | Behaviorally true but the data does not reach the client: the wire queue projection carries no per-entry selections at either layer — `events.QueueChangedData` (agent/events/payloads.go:439-446) and `appwire.QueueState` (appwire/types.go:760-767) both have Depth/Revision/Preview/IDs/ClientMutationIDs/Texts only — and `turn/cancelQueued` returns only a removedImages count. The daemon does hold per-entry `SkillNames` internally (queuedInput, session_lifecycle.go:2098). A fix requires an additive wire change: FIFO-aligned `SkillNames [][]string` on `QueueChangedData` + `appwire.QueueState` (+ appwire/clone.go), the projection copy (internal/appprojector/appwire_projection.go:1053-1060), the agent queue snapshot builder, the hub thread model, and the QueueStrip `onRestoreToComposer` seam + composer restore staging. Deferred as a wire/API change beyond this round. |
| M3 | Clearing a pinned compaction note may not persist | FIXED | The clear-only branch (agent/session_skill_compaction.go) persisted only when it also cancelled an automatic pending operation; with no pending operation the cleared note stayed on disk and reappeared after restart. The clear now always saves (typed error on failure). Red test `TestSkillCompaction_ClearNotePersistsWithoutPendingOperation` (loaded meta still held "keep") passed after the fix; full TestSkillCompaction_ family green. |
| M4 | Skill operation IDs use an in-memory counter | FIXED | Reachable: `mintSkillOperationID` incremented the persisted counter in memory only, while the minted identity could reach the durable transcript SkillState (session_tools.go:1034-1044) before the obligation save (session_skill_delivery.go:197); a crash in that window left the transcript holding `skill-op-N` with the on-disk counter at N-1. The red test reloaded from the pre-save meta and the post-restart mint reissued `skill-op-1`. The mint now persists the counter before returning and reports save failures (callers: skillToolActivate, slash command, both role-preload paths). `TestMintSkillOperationID_SurvivesRestartWithoutInterveningSave`. |
| M5 | No dedup of SkillNames produces duplicate InvocationIDs | FIXED | Duplicates reach the path: `appwire.NormalizeMutationInput` validates per-item but never dedups (appwire/input_test.go:111-121), and the session copies the selection verbatim (session_lifecycle.go:2098), so `["x","x"]` yielded two invocations/obligations/carriers with InvocationID `inputID:x`. `prepareSelectedInput` now dedups by canonical identity, first occurrence winning, request order preserved. `TestPrepareSelectedInput_DuplicateNamesInvokeOnce` (red: 3 items, duplicate ID; green: 2). |
| M6 | Incomplete-identity reload receipt is never consumed | REFUTED | The leave-pending behavior is the designed, pinned semantics: `TestSkillCompaction_CheckpointOnly` (agent/session_skill_compaction_publication_test.go:139-196) deliberately seeds an ordinary record with an EMPTY identity (line 149), selects it, and asserts the handoff receipt stays pending. A fix attempt (mirroring the guard in `consumedReloadPublicationsLocked`) was made, caught by that pinned test in the merge gate, and reverted (151e5b1388): consuming the receipt would silently discard an unreported reload selection. The pre-PR review's defensive-only adjudication stands. |
| M7 | Spawn slash catalog silently drops all skills | FIXED | `hubSpawnSlashCatalog` built `appwire.EvenerSkillInfo{Name, Description}` without the three non-omitempty booleans (app_spawn_slashcatalog.go:157), so `available`/`userInvocable` serialized false and `mergeSlashCommands` (slashCompletion.ts:124) filtered every skill out of the spawn menu; the Go tests only checked names. The spawn catalog now uses session startup's portable discovery (`skill.Discover` + `UserEntries`, the same builder as the past-thread path) with real values, and the test asserts the wire flags (red before: all false). |
| M8 | SkillDiagnostics never carried to the wire | FIXED | The chain was as described: `agent.DetailedStatus.SkillDiagnostics` exists (agent/status.go:126) but `server.DetailedStatus` lacked the field (server.go:171), `agentToServerDetailedStatus` (cmd/evener/serve.go:1604) and `appDiagnosticsFromDetailedStatus` (server/appwire_runtime.go:2325) never assigned it, and the past-thread discovery discarded `catalog.Diagnostics`. Added `server.SkillDiagnosticInfo` + field, both conversion copies, and the past-thread view now returns `{Skills, Diagnostics}` attached onto `EvenerDiagnostics`. Wire test `TestThreadReadCarriesSkillDiagnostics`; hub test `TestDiscoverPastThreadSkillsCarriesDiagnostics`. |
| M9 | evenerfuzz fixture dereferences a nil skillActivate callback | FIXED | Confirmed by replay: `FuzzSessionToolsAuxExact/seed#0` panicked with SIGSEGV — `registerSkillTool` (agent/session_tools_communicate.go:181-183) overwrites the registration and invokes `deps.skillActivate`, which the fixture never set; its assertions also pinned the deleted inline implementation's path-notification prefix (that string exists nowhere else in the tree). The fixture now provides the callback (resolve -> skill.Load -> render) and asserts the current rendered-envelope contract. |

### Re-run gates on the fix-round head

| Command | Exit | Duration | Notes |
| --- | --- | --- | --- |
| seed replay `FuzzAgentClonesShareNoMutableState` (CI repro command) | 1 -> 0 | 0.03s / 0.04s | red before the clone fix, green after |
| `make fuzz` | 0 | 250s | the exact CI fuzz target; zero FAIL lines |
| `make test-api-package` | 0 | ~15s | qualified @evener/appwire-client@0.1.0 |
| `make vet` | 0 | ~5s | no diagnostics |
| `make merge-approval-gate` (run 5) | 0 | 329s | all lint phases PASS (naming, gofmt, evenerfuzz, eval, internal, golangci, generated, fuzz-registry, secret-scan), build PASS, ROOT_FULL waves PASS (root 142.4s, agent 11.2s, llm 12.1s, auth 2.2s, envvars 0.6s, invariant 0.5s, identifier 1.6s, web 255.2s), test-native and test-api-package PASS |
| `TMPDIR=/tmp make test-web-browser` | 0 | 204s | 6/6 guards: web-layoutguard, web-overflowguard, web-shellguard, web-spawnguard, web-transcriptscrollguard, web-skillguard |
| `make generate` | 0 | 1s | zero-diff (`git status --porcelain` empty after) |
| `npx vitest run src/panes/session/composer` | 0 | 26s | 28 files / 638 tests |
| `go test ./agent -run 'Skill|Slash|Mint' -count=1` | 0 | 2.9s | activation/delivery/slash/mint families after M3-M6 |
| `go test ./agent -run 'Subagent|Delegate' -count=1` | 0 | 57s | delegate spawn paths after the durable-mint change |
| `go test ./agent -run '^TestSkillReload' -count=1` | 0 | 1.4s | reload family (post-revert state) |
| `go test ./cmd/evener-hub -run 'PastThread|DiscoverPastThread|SpawnSlashCatalog' -count=1` | 0 | 1.7s | M7/M8 hub families |
| evenerfuzz seed replays (`FuzzSessionToolsAuxExact`, `FuzzToolRegistryProgram`, `FuzzSkillDiscoveryProgram`) | 0 | <1s each | M9 + oracle alignments |

Merge-gate attempts 1-2 failed `lint-gofmt` on a formatting nit in the fuzz
oracle edit (fixed in 22861efa9c); attempt 3 failed
`TestSkillCompaction_CheckpointOnly`, which exposed the M6 fix as wrong and
drove the revert (151e5b1388); attempt 4 passed the agent module but flaked in
web-test on `scripts/browserGuardCdp.test.js` (a real-Chrome CDP probe hit its
3000ms startup deadline under load average ~15 on 16 CPUs; the suite passes
12/12 standalone, and the prior round recorded the same load-flake class);
run 5 passed end to end. No assertion, filter, pairing, or tolerance was
weakened anywhere in this round; the only assertion changes were the two stale
fuzz oracles aligned to the deliberately changed contracts (each with the
pinning unit test named above) and the M9 fixture, whose deleted production
contract no longer exists anywhere in the tree.

## Second post-PR-review fix round (2026-09-12, PR #1168 head e195b04a9f)

This round resumed after the previous agent died on a provider quota limit
mid-round, leaving uncommitted work for the first two findings. Every inherited
hunk was audited against the finding before being kept: mechanism, callers,
lock discipline, and test-helper existence were verified against source, and
each inherited test was proven red against the reverted production code (the
falsification runs below) before being kept. Nothing that weakens an assertion
or changes behavior without a test was retained.

### Inherited-work audit (M-A, M-B)

- M-A (kept in full): the `reloaded` → `appendedNotifications` rename makes
  `prepareModelRequestWithError` rebuild for every appended delivery
  notification — restored carriers AND failure explanations — instead of only
  successful reloads. Verified the rebuild closure
  (`rebuildForAppendedSkillTurns`, session_model_call.go:372-379) collects
  every `s.history` turn past the caller's snapshot, so the failure
  notification turn joins the rebuilt request; both
  `recordSkillDeliveryNotification` call sites (session_skill_delivery.go:295
  failure, :325 reload) are inside `prepareSkillDelivery`, and only the
  failure site previously left the flag unset. Red pre-fix:
  "the dispatch that finalized the failed reload never carried the failure
  explanation to the model" (asserted on the captured provider request, not
  prose).
- M-B (kept in full): the reorder persists obligations (`saveMeta`) BEFORE any
  carrier is published, rolling the batch's obligations back out of memory on
  save failure so nothing half-admits. Restore is meta-only
  (session_init.go:1027: `s.skillLifecycle = meta.Skills.Clone()` — no
  reconstruction from transcript carriers), so the pre-fix window really did
  leave a durable carrier with no durable obligation. The reverse window
  (durable obligation, no carrier) is the designed reload path's problem
  statement and re-delivers at the next dispatch seam. Callers
  (session_lifecycle.go:1637, admitSteeringSelectionBatch) fail the turn
  visibly on the error. Injection is the existing `breakSessionMetaPath`
  (session_skill_compaction_test.go:393), the same seam the compaction
  save-failure tests use. Red pre-fix: "failed admission left live
  obligations" — the old code left the obligation in memory with the carrier
  already recorded.
- One environment note from the resume: `git stash list` was NOT empty at
  handoff — it holds two stashes from OTHER branches (`main`,
  `fix/625-enum-nonstring-constraint`), left untouched.

### Findings verdicts

| # | Finding | Verdict | Evidence and what changed |
| --- | --- | --- | --- |
| M-A | Failed reload notifications never reach the model | FIXED | `recordSkillDeliveryNotification` appended the failure explanation (session_skill_delivery.go:292-297 pre-fix) without setting the commit's rebuild flag, so the outgoing request omitted the explanation while `finalizeSkillDeliveryFailure` dropped the obligation. The commit field is now `appendedNotifications`, set at BOTH append sites; `prepareModelRequestWithError` (session_model_call.go:418) rebuilds on it. `TestSkillDelivery_FailedReloadNotifiesNextDispatch` seeds a pending obligation whose recorded source is gone (the restore-window state), drives `ProcessInput`, and asserts the NEXT captured provider request carries `Skill "opaque" is no longer available from …`, plus the typed `source_missing` outcome, no live obligation, no inventory. Red pre-fix with the intended message. |
| M-B | Obligation/carrier durability window | FIXED | Pre-fix, `admitSkillActivationBatch` recorded the transcript carrier via `recordTurn` BEFORE `saveMeta`; a crash or save failure in that window left a durable carrier with no durable obligation, and restore (meta-only) lost the activation state. Now the batch's obligations persist first; carriers publish only after a successful save, and a failed save rolls the obligations back out of memory (nothing half-admits). `TestSkillActivation_AdmissionSaveFailureAdmitsNothing` injects the save failure via `breakSessionMetaPath`, asserts no live obligation and no recorded skill turn state, restores and asserts the crash window strands nothing, then retries and asserts restore sees exactly one obligation with its carrier. Red pre-fix ("failed admission left live obligations"). |
| L-A | Queue preview/copy ignores skill items | FIXED | `recordContent` (QueueStrip.tsx:112-122) extracted only text and image items, so a skill-only durable record previewed blank and copied as "". It now also extracts the canonical skill names; `recordPreview` appends `[skill: name]` markers to the text/image preview and `handleCopy` copies text + markers. Three vitest additions in QueueStrip.test.tsx (skill-only preview+copy, text+skill copy, blocked skill-bearing row); all three red pre-fix, and the 37 pre-existing QueueStrip tests unchanged. `pendingReconcile.ts`'s `inputPreview` was deliberately NOT touched: its strings are a reconciliation MATCHING key against daemon queue previews, and the finding scoped the fix to `recordContent`. |
| L-B | Handler validation disagrees with advertised capability | FIXED | All four input-bearing handlers validated with a literal `true` while `ThreadCapabilities.SkillInput` is a conjunction over `retrySafeTurns.Start/Steer/Queue/Drain`. Each handler now gates on its OWN seam's wiring (`fn != nil`, the very function the dispatch below requires — fn read hoisted above validation); the advertisement derives from the new `skillInputSupportedLocked()` predicate (appwire_runtime.go:2464-2475). Consumption-at-the-claim is preserved for genuinely wired endpoints: `TestAppWireMutationSkillInputResponseLossRetriesOnce` (Queue-only wiring legitimately consuming a selection at the claim) passes untouched. New `TestSkillInputValidationMatchesAdvertisedCapability`: a partially wired server must not advertise the capability, its wired endpoint still consumes skill selections, its unwired endpoint answers the typed InvalidParams "skill input is unsupported" at the gate (was the bare Unavailable pre-fix), and ordinary text keeps the endpoint's own behavior. Red pre-fix with the intended message. |
| DOC | Evidence text says `invalid_control` where the parser emits `invalid_metadata` | FIXED | Line 307 above now reads `invalid_metadata` with the pinning test corrected to `TestSkillControlsRejectMixedAllowedToolsArray` (agent/skill/metadata_test.go:183-189); `invalid_control` is the boolean-controls category (agent/skill/metadata.go:131-149), `allowed-tools` parse failures emit `invalid_metadata` (metadata.go:151-160). Recorded inline; no history rewritten. |

### Re-run gates on the second fix-round head (all serial)

| Command | Exit | Duration | Notes |
| --- | --- | --- | --- |
| `make fuzz` | 0 | 310s | agent/invariant/fuzz modules under `-tags evenerfuzz`, fuzzcov/harvest, committed-seed replay, rapid replay (55 seed replays), decode goldens — zero FAIL lines |
| `make test-api-package` | 0 | 3s | qualified @evener/appwire-client@0.1.0 |
| `make vet` | 0 | 3s | no diagnostics across the non-fuzz modules |
| `make merge-approval-gate` | 0 | 653s | lint PASS (naming 0s, gofmt 0s, evenerfuzz 36s, eval 29s, internal 0s, golangci 40s, generated 0s, fuzz-registry 3s, secret-scan 8s), build PASS, ROOT_FULL waves PASS (. 144.68s, agent 12.15s, llm 11.64s, auth 1.65s, envvars 0.58s, invariant 0.64s, identifier 1.61s, web 205.14s), test-native PASS (78 files/737 tests + shared 7 files/777 tests + tsc), test-api-package PASS |
| `TMPDIR=/tmp make test-web-browser` (run 1) | 2 | 198s | 5/6 guards PASS; `web-skillguard` failed in its prelude — "expected two live sessions in the rail, found 0". Environmental, not a product failure: the guard's `railRowsExpr()` returns an always-truthy object so `waitPage` returns immediately instead of waiting for rows, and the check ran before the hub hydrated the rail under load average 31.3 (16 CPUs). The failure dump, captured seconds later, shows both sessions present and healthy in the rail, and both helper daemons PASSed. Same load-flake class the previous round recorded for the CDP probe. |
| `TMPDIR=/tmp make test-web-browser` (run 2) | 0 | 197s | 6/6 guards PASS: web-layoutguard, web-overflowguard, web-shellguard, web-spawnguard, web-transcriptscrollguard, web-skillguard |
| `make generate` | 0 | 1s | zero new diff: no generated output (types.gen.ts, docs/appwire-protocol.md, the developing-evener tables) modified; the working tree's modified set is exactly the round's ten intended files |

### Affected Stage 2/3 families and touched frontend suites (all serial, all exit 0)

| Command | Result |
| --- | --- |
| `go test ./agent -run '^(TestSkillActivation_|TestSkillDelivery_|TestSkillReload|TestSkillCompaction_|TestPinnedNote_|TestMaybeElicitNoteBeforeCompaction_|TestApplyPendingForceCompact_|TestSessionCompact_|TestFoldPublication_|TestPrepareModelRequest_)' -count=1` | 118 top-level + 42 subtest PASS, 0 skip, 0 fail |
| `go test -race ./agent -run '^(TestSkillDelivery_|TestSkillActivation_|TestSkillReload|TestSkillCompaction_|TestFoldPublication_)' -count=1` | PASS, no race reports |
| `go test ./server -run '^(TestAppWireMutation|TestSkillInput|TestTurnMutations|TestThreadRead)' -count=1` | 12 top-level + 31 subtest PASS |
| `go test ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/... -count=1` | all packages ok |
| `go test ./server -count=1` (full package) | ok |
| `npx vitest run src/panes/session/composer/queue/` | 5 files / 83 tests PASS (QueueStrip 40 incl. the 3 new) |
| `npx vitest run` on PendingChips.test.tsx + Composer.integration.test.tsx + Composer.test.tsx (the suites that mount QueueStrip) | 3 files / 220 tests PASS |
| `npx vitest run src/panes/session/composer` | 28 files / 641 tests PASS (638 pre-round + the 3 new L-A tests) |

No assertion, filter, pairing, or tolerance was weakened in this round. The
only behavioral changes are the four fixes above, each pinned red-before /
green-after by its named test.
## Third post-PR-review fix round (2026-09-13, PR #1168 head d04a8cc5ed)

The head this round produced is `922b557885695102c46e1ff9fcb1bf5d662a0f4f` (the
header names the baseline the round's findings were raised against, following
the convention of the two rounds above). See the fourth-round section below for
the rebase that superseded it.

This round resumed after the previous agent was stopped having run ZERO tests
and written NO test file, leaving uncommitted scaffolding (a
`SkillInputRecord.Prepared` carrier plus `recordPreparedSelection` wiring whose
comments referenced a `reconcilePendingSkillSelections` that did not exist).
The inherited diff was audited against the findings before being kept: the
design direction (retain the selection on the durable typed input record,
reconcile at restore — the second of the two approaches the finding allowed)
was sound and matches the `reconcileSkillCompactionReceipts` precedent
(agent/transcript_read.go:197), so the carrier and wiring were kept and the
missing reconcile was implemented. Everything below was proven red-first.

### Inherited-work audit (items 1)

- Kept: `schema.SkillInputRecord.Prepared []SkillSelectionInvocation` (+ its
  Clone), `recordPreparedSelection` (agent/session_skill_activation.go:79-98),
  and its three call sites (agent/session_queue.go:1013,
  session_skill_activation.go:156, session_slash_command.go:111). Verified the
  annotated record is the same pointer written into the turn's
  `SkillState.Input` on all three paths, so the prepared invocations are
  durable with the input turn.
- Repaired: the `admitSteeringSelectionBatch` comment claimed re-drive "at the
  next dispatch seam or, after a crash, at restore" — only restore exists; the
  comment now says restore.
- Implemented: the missing `reconcilePendingSkillSelections`
  (agent/session_skill_activation.go:197). Coverage predicate: an invocation
  is never re-driven when the snapshot still holds its obligation, the
  inventory recorded its delivery (`Ordinary.InvocationID`), or ANY decoded
  transcript entry carries its identity in an outcome or obligation (a carrier
  or notification is published only after the obligation save succeeded, and a
  finalized failure keeps its record). Uncovered prepared invocations are
  re-prepared and re-admitted per input record through the same atomic
  prepare/admit path as a live selection. Wired into restore AFTER skills
  discovery and transcript attach and BEFORE `setRestoredTranscript`
  (agent/session_init.go:1214), with a `refreshFromDisk` when carriers were
  appended. An earlier placement (before `initSessionState`) silently
  no-opped because `s.skills` is discovered there
  (session_init.go:1392) — caught by the red test, moved, re-proven.

### Findings verdicts

| # | Finding | Verdict | Evidence and what changed |
| --- | --- | --- | --- |
| 1 | User-selected skill silently lost when metadata persistence fails (also last round's nit N2) | FIXED | The input/steering turn was written and the mutation consumed BEFORE `admitSkillActivationBatch` persisted its obligation; on save failure the obligation rolled back and restore (meta-only) lost the selection. The durable input record now carries the prepared invocations (`Prepared`), and restore re-drives any uncovered one via `reconcilePendingSkillSelections` (agent/session_skill_activation.go:197, wired at session_init.go:1214). `TestSkillActivation_SteeringSelectionReconciledAfterAdmissionSaveFailure` (agent/session_skill_reconcile_test.go:23) drives the REAL steering path (`consumeSteeringMessage`) under the real `breakSessionMetaPath` injection, asserts nothing half-admits live, then restores and asserts exactly one reconciled obligation (route `user_selection`, identity `opaque`, invocation identity matching the steering turn's `Prepared`), the carrier published, the steering turn pinned, and a second restore idempotent. RED first: "restored obligations = [], want exactly one reconciled from the durable steering input record". `TestSkillActivation_AdmittedSelectionIsNotReReconciled` (:125) pins that a successfully admitted selection is NOT re-driven. |
| 2 | Editing a queued message silently drops its skill selections; docs claimed otherwise | FIXED | `QueueState` surfaced only texts/preview/ids, so a queued `{type:"skill"}` item was unrecoverable. Per-entry canonical names now ride the whole projection: `appwire.QueueState.SkillNames` (appwire/types.go:773) and `events.QueueChangedData.SkillNames` (agent/events/payloads.go:450), FIFO-aligned; filled in `queueChangedDataLocked` (agent/session_queue.go:842-847) and `ClientMutationProjection` (agent/session_client_mutation_queue.go:48,66); mapped through `cloneSkillNames` (internal/appprojector/appwire_projection.go). Client: `QueueState.skillNames` regenerated (types.gen.ts), `onRestoreToComposer` gained the `skillNames` parameter (QueueStrip.tsx:76-78), `handleEdit` passes the entry's names (QueueStrip.tsx:402), a skill-only entry stays editable (QueueStrip.tsx:373), and `restoreTextToComposer` unions the restored chips into the draft and persists them (Composer.tsx:1041-1056). Daemon test `TestClientMutationProjection_QueueEntrySkillNames` (agent/session_queue_skills_projection_test.go:23) queues a skill-only and a mixed entry through the real mutation endpoint and asserts BOTH the projection and the depth-2 `QueueChanged` event carry `[[probe] [probe]]` (RED: `queue.SkillNames undefined`; event-capture race fixed with a bounded poll, stable at -count=5). Frontend: two new QueueStrip tests — edit restores text AND chips (`toHaveBeenCalledWith("queued text", undefined, ["probe"])`), and a skill-only entry's edit is enabled (RED: "expected true to be false"); the pre-existing edit test's assertion was updated to the new 3-arg contract with text and loser-safe ordering unchanged. `go generate ./appwire/...` ran; `make generate` exit 0 with zero new diff. docs/skills.md:255 and :353-354 now describe real behavior; no doc was weakened. |
| 3 | Compaction reload reminder discards computed discovery diagnostics | FIXED | `skillInventorySummary` computes per-source diagnostics but the site did `_ = diagnostics` under a comment claiming they "ride the typed turn" — false: `SkillReloadReminder` carried only `Inventory`, and `SkillInventorySummary` only `Availability`. The reminder now carries them: `schema.SkillInventoryDiagnostic` + `SkillReloadReminder.Diagnostics` (agent/schema/skill_lifecycle.go:95-110, cloned at :296), populated via `skillInventoryDiagnostics` (agent/session_skill_reload.go:291, helper at :147-164); the false comment is gone. `TestSkillReloadReminder_CarriesDiscoveryDiagnostics` (agent/session_skill_reload_test.go:985) seeds an inventory entry whose recorded source is unreadable, drives `prepareCompactedSkillReloads`, and asserts the recorded reminder keeps the `unavailable` classification AND an `unreadable_source` diagnostic naming the source. RED first: `reminder.Diagnostics undefined`. |
| 4 | Garbled comment (unbalanced parenthesis) at QueueStrip.tsx:126-127 | FIXED | Rewritten (QueueStrip.tsx:125-128): "the name is the selection's whole user-visible identity - the part of a queued entry that is distinct from its typed text. Without it a skill-only record previews blank and copies as an empty string." |

### Triaged nits from the last review (accepted as-is)

- N3 (140-char preview truncation can hide skill markers; copy unaffected):
  ACCEPTED. The truncated preview is display-only; the copy path
  (`handleCopy`, QueueStrip.tsx:334-346) copies full text plus all skill
  markers untruncated, and this round's item-2 fix makes the EDIT path restore
  selections from the projection's `skillNames` rather than from the preview
  line, so truncation no longer hides anything recoverable.
- N4 (one environmental vitest flake under load): ACCEPTED. Single
  load-induced flake, not a product assertion failure; the touched suites were
  re-run this round (QueueStrip 42/42, composer family 643/643) with no
  recurrence.

### Re-run gates on the third fix-round head (all serial)

| Command | Exit | Duration | Notes |
| --- | --- | --- | --- |
| `make fuzz` | 0 | 382s | full fuzzcov/harvest/seed-replay pipeline, zero FAIL lines |
| `make test-api-package` | 0 | 3s | qualified @evener/appwire-client@0.1.0 |
| `make vet` | 0 | 56s | no diagnostics |
| `make merge-approval-gate` | 0 | 342s | on the final code head f3e01a45b3: lint-naming/gofmt/evenerfuzz/eval/internal/golangci/generated/fuzz-registry PASS, secret-scan PASS, ROOT_FULL waves PASS (. 121.78s, agent 10.51s, llm 8.14s, auth 1.55s, envvars 0.36s, invariant 0.34s, identifier 1.05s, web 167.38s), test-native + test-api-package PASS. Earlier runs failed fast on this round's OWN defects and were fixed, never waved through: (1) gofmt on agent/schema/skill_lifecycle.go struct alignment, (2) revive redefines-builtin-id (`copy :=`) in the new projection test, (3) golangci gofmt on the same test after the rename patch, (4) lint-generated requires generated files COMMITTED — the wire/client commit unblocked it, (5) web-lint biome formatting of the new handleEdit signature. |
| `TMPDIR=/tmp make test-web-browser` | 0 | 201s | 6/6 guards PASS first try (web-layoutguard, web-overflowguard, web-shellguard, web-spawnguard, web-transcriptscrollguard, web-skillguard). Ran on 6d1c8cec9c; the final head f3e01a45b3 adds one Go-test-only commit, which cannot affect browser guards. |
| `make generate` | 0 | 1s | zero-diff on the final head: the working tree's only modified path is this evidence document |

### Affected Stage 2/3 families and touched frontend suites

| Command | Result |
| --- | --- |
| `go test ./agent -run '^(TestSkillActivation_\|TestSkillDelivery_\|TestSkillReload\|TestSkillCompaction_\|TestClientMutation\|TestConsumeSteering\|TestExpandSlashCommand\|TestQueuePersist_\|TestRestore\|TestResume\|TestReconcile\|TestMintSkillOperationID_)' -count=1` | ok (12.402s) |
| `go test -race ./agent -run '^(TestSkillDelivery_\|TestSkillActivation_\|TestSkillReload\|TestSkillCompaction_\|TestClientMutationProjection_)' -count=1` | ok (4.457s) — this run CAUGHT a real data race in this round's new projection test (reading the `captureEvents` slice while its goroutine appended); fixed with a mutex-guarded collector (f3e01a45b3), re-run green, plus `-race -count=3` on the test itself and `-race` on all three other new tests |
| `go test ./appwire/... ./internal/appwirets/... ./internal/appwiredoc/... ./internal/appprojector/... ./agent/events ./agent/schema -count=1` | all packages ok |
| `npx vitest run src/panes/session/composer` (covers queue/) | 28 files / 643 tests PASS, including QueueStrip 42/42 with the 2 new edit-restores-chips tests |
| `npx tsc --noEmit --incremental false` (frontend) | clean |

No assertion, filter, pairing, or tolerance was weakened in this round. The
pre-existing QueueStrip edit test's call assertion was updated to the new
three-argument `onRestoreToComposer` contract with its restored-text and
loser-safe-ordering assertions unchanged.

## Fourth post-PR-review round: rebase onto main + review findings (2026-09-13)

### Rebase onto main (Jesse's ruling)

`origin/main` had advanced to `e2c77cc72e38101f92c52110ec69982a6c94bf3f`
while the branch sat at `922b557885`. The branch was rebased onto that commit;
the safety branch `pre-r4-922b557885` preserves the old head. Both the rebase's
conflict resolution and this round's fixes were performed by the orchestrator
after two workers failed (an openrouter credit error mid-rebase, and a worker
that made no progress in 25 minutes).

The decisive fact for resolving every conflict: main contains none of this
feature's compaction-claim machinery — `claimCompactionLocked`,
`foldPublicationID`, and the `foldCommit` extensions are all ours — while both
sides extend the shared `foldCommit` struct. So the rule throughout was: main's
newer structure wins, and our semantics are re-applied on top. Concretely:

- `foldCommit` = main's `resetEnvContextTrackerLocked` plus our fields, with our
  parameterless `claimCompactionLocked func()` (our final intent; commit
  `5762e87f4c` carried the older `func(publicationID string)` form, which its
  child `6e5d09b3f3` superseded).
- `pushQueueHead` keeps main's error-returning signature with our
  `inputHasContent(entry.Text, entry.Images, entry.SkillNames)` check.
- the drain path keeps main's `refuseBeforeClaimingOnPoisonedTranscript`
  refusal and our `inputHasContent(queued.Text, queued.Images, queued.SkillNames)`.
- the user-input turn keeps main's `appendUserInputTurnRefusingPoison` flow with
  our `buildSelectedUserInputMessage(input, images, skillInputNames(skillInput))`.
- the accepted-turn re-push keeps main's `returnAcceptedUserTurn(queuedIdentity)`:
  our `SkillNames` preservation already lives inside
  `completeClientMutationTurnWithState`.
- the queued-mutation turn keeps main's `appendEnvironmentContext` error handling
  with our `buildSelectedUserInputMessage(queued.Text, queued.Images, queued.SkillNames)`.

Integrity audit: `git range-diff a361254ab2..pre-r4-922b557885 e2c77cc72e..bb8bbf8596`
reports no dropped (`<`) and no added (`>`) patches, and exactly six patches whose
content changed (12, 15, 16, 17, 22, 35) — the six conflicts. `go build ./...`
exits 0; `git merge-base --is-ancestor e2c77cc72e HEAD` holds.

### F1 (Medium, a regression the third round introduced) — FIXED

Editing a queued entry with no skill selection threw a `TypeError`: the daemon
emits a JSON `null` slot for that entry, and the guard only excluded
`undefined`. The throw landed after the text was written, so the entry was
duplicated behind a spurious error toast and never cancelled.

- Fix: `Composer.tsx`'s guard is now
  `if (restoredSkillNames && restoredSkillNames.length > 0)` — null-safe and
  narrowing. (The reviewer's suggested `(restoredSkillNames?.length ?? 0) > 0`
  is runtime-safe but does not narrow the type and fails `tsc`; see the gate
  table.)
- RED test (new): `Composer.integration.test.tsx`, "clicking Edit on a queued
  entry whose daemon skillNames slot is null restores the text and still cancels
  it" — the real composed Composer+QueueStrip tree with `skillNames: [null]`, the
  shape the daemon actually emits. Observed RED (text restored, `turn/cancelQueued`
  never fired), then GREEN; reverting the one-line fix in place returns it to RED.

### F2 (Low, coverage gap) — FIXED

New `TestSkillActivation_FailedPreparationIsNotReDeliveredAtRestore` pins that a
record whose preparation failed (nil `Prepared`) is never re-delivered at
restore. It is deliberately discriminating: the selected source is unavailable
when the steering message is consumed (so preparation fails) and available again
before the restart, so a reconcile that wrongly re-drove the failure would
succeed and deliver it. It also pins its own premise (one typed input record,
zero prepared invocations, zero live and zero persisted obligations).
Load-bearing proof by mutation: with reconcile temporarily changed to re-drive
from `input.Names` when `Prepared` is empty, the test fails with
`restored obligations = [{InvocationID:mutant-redelivery ... Identity:{Name:no-such-skill ...}}], want none`;
reverted, it passes and the production file is byte-identical.

### F3 (Low, documentation) — FIXED

The third-round section above now states the head that round produced
(`922b557885`) alongside its baseline header.

### Gates on the final head

Each gate was run serially and its own exit status captured directly (`make`
followed immediately by reading `$?` — never a trailing `echo`, which masked a
real `lint-gofmt` failure earlier in this delivery).

First run — four passed, three failed:

| Gate | Exit | Duration |
| --- | --- | --- |
| `make generate` (zero-diff) | 0 | 1s |
| `make fuzz` | 0 | 206s |
| `make test-api-package` | 0 | 3s |
| `make vet` | 0 | 6s |
| `make merge-approval-gate` | 2 | 2s |
| `make test-web` | 2 | 192s |
| `TMPDIR=/tmp make test-web-browser` | 2 | 255s |

The three failures were real and two were this round's own work:
`FAIL lint-gofmt` on `agent/session_compaction.go` (a doc comment left over-indented
inside a conflict hunk); `web-typecheck` `TS18048: 'restoredSkillNames' is
possibly 'undefined'` (the first guard form does not narrow); and `web-skillguard`
"expected two live sessions in the rail, found 0" — the rail-row race this
delivery has recorded as load-induced, but not assumed to be one without evidence.

After fixing the formatting and the guard, re-run on the final head:

| Gate | Exit | Duration |
| --- | --- | --- |
| `make merge-approval-gate` | 0 | 1154s |
| `make test-web` | 0 | 188s |
| `TMPDIR=/tmp make test-web-browser` (all six guards PASS, incl. `web-skillguard`) | 0 | 199s |

The only differences between the two browser runs were the Go comment
formatting and the composer guard, neither of which touches the session rail;
the guard passed on re-run, so the first failure was the known flake. Both
results are recorded here rather than only the green one.

No assertion, filter, pairing, or tolerance was weakened in this round. The
`generate` gate stayed zero-diff across both runs.

## Fifth round: merge with main, then the roborev findings (2026-09-13)

### Merge (Jesse's ruling: merge, do not rebase again)

`origin/main` advanced to `42d27f946e` (shared session notes) while the branch sat
at `436445f54f`, so the PR conflicted in seven files. Merging rather than
rebasing was Jesse's call: main moved twice during this delivery and each rebase
cost a full per-commit conflict cycle.

Merge commit `aff47fc615` (parents `436445f54f`, `42d27f946e`) plus carrier
commit `ff9a8254a5`. Nine conflict hunks, all resolved as unions — main's newer
structure with this feature's semantics re-applied:

- `snapshot.go` / `session_state.go`: main's `HumanNote`/`AgentNote`/`SessionURLs`
  alongside our `Skills` lifecycle field.
- `session_init.go`: our delegate-history strip plus main's
  `escapeNotesHistoryTurns`; our `skillLifecycle` restore and
  `resumeSkillCompaction` alongside main's notes-projection seeds.
- `session.go`: main's `agentNote`/`sessionURLs` fields alongside our updated
  compaction-state comments; and at `maybeAutoSave`, main's `autoSaveMeta()` —
  verified byte-for-byte equivalent to our `saveMeta()` (same `stateDir` check,
  same `metaSaveMu` lock, same `Meta()` + fs write), so main's newer call site
  wins.
- `session_client_mutation_queue.go`: our `SkillNames` field alongside main's
  `Kind` stamp and its explanatory comment.
- `server/appwire_runtime.go`: our `SkillInput` capability alongside main's
  `SharedNotes` capability.
- `Composer.tsx`: our `skillSelections` import; main moved `slashCompletion` to
  `src/protocol/`, and the moved test file landed there (verified: 29 files /
  748 tests pass in `src/protocol`), so the composer suite's 627 -> 558 test
  count is a MOVE, not lost coverage.

Two initial resolutions were wrong in an instructive way: the "concatenate both
sides" rule is only valid when a conflict's base section is empty. Two hunks had
a non-empty base that both sides retained, so concatenating duplicated field
declarations and struct-literal fields. The compiler caught both; after repair
each field was verified to appear exactly once, and the merged head builds,
typechecks, and passes the focused agent and composer suites.

### roborev 9351 findings

| Finding | Verdict | Evidence |
| --- | --- | --- |
| M1 reload-receipt durability hole (`agent/session_skill_reload.go`) | FIXED | The reminder turn IS the receipt's durable admission, but it was appended with `recordTurn`, which swallows transcript write failures, and the receipt was consumed regardless — a failed write lost the only reminder with no retry. It now appends through the durable pair (transcript write first, live append only on success), warns, and returns an error WITHOUT consuming. `TestSkillReloadReminder_ConsumesReceiptOnlyAfterDurableAdmission` drives the real `prepareCompactedSkillReloads` over a genuinely failing transcript (`transcriptWriteFailFS`) and asserts the failure is reported, the handoff stays pending, and no reminder turn joined the history. RED first: "a reminder whose transcript write failed reported success". |
| M2 text items merged without a separator (`agent/session_client_mutation_queue.go:507`) | **REFUTED, not fixed** | See the dedicated note below. |
| L1 missing `reload_skills` field treated as absent (`agent/skill_reload_selection.go`) | FIXED | A `<skill-reload-selection>{}</skill-reload-selection>` block produced a nil raw message, which `parseSkillReloadSelection` reports as `absent` rather than `invalid`, so the malformed block was consumed and the note dropped — contradicting the parser's own documented contract. The block must now carry the field; absent means invalid and the note survives verbatim. RED first: the note was stripped and the state read `absent`. |
| L2 inconsistent skill-name canonicalization (composer) | FIXED | One helper, `canonicalSkillNames` in `protocol/composerInput.ts` (trim, drop empty, dedupe preserving first-seen order), now backs all three paths: `buildInput`, `recoveryDraft`'s record extractor AND its merge union, and the queue strip's `recordContent`. RED first at two call sites (`buildInput` emitted padded/duplicate items; the recovery draft kept `" pkg:probe "`, `""` and `"pkg:probe"` as three separate chips). |
| L3 dead `SkillNames` field on `queuedClientMutationIdentity` (`agent/session_queue.go`) | FIXED | The field was written by `withQueuedClientMutation` and never read; the durable-selection context value is the real channel. Dropped the field and the copy, so the compiler proves nothing depended on it. |

### M2: refuted rather than fixed

roborev reported that `queuedInputFromClientMutation` concatenates an entry's
text items without a separator, "so two distinct text items become one merged
word and the turn transcript differs from what `combineClientMutationInputs`
produces with `"\n\n"`".

That comparison is between two different granularities, and I did not change the
behavior:

- `combineClientMutationInputs` joins whole ENTRIES with `"\n\n"` — distinct
  queued messages, where a separator marks real message boundaries.
- `queuedInputFromClientMutation` handles ONE entry — a single queued message.
  Its items are parts of that one message.

Inserting `"\n\n"` between one message's items would invent separators the user
never typed. Concatenation is the faithful reading of the items as the byte
sequence they are. The client cannot produce the multi-item case anyway:
`buildInput` emits exactly one text item (`protocol/composerInput.ts:43`), and
`translateAttachmentMarkers` rewrites image markers inline rather than splitting
the text around them — so nothing in this client splits one message into several
text items.

The pre-existing characterization test
(`agent/session_client_mutation_queue_branches_test.go`, "multiple text items
concatenated", with inputs `"hello "` and `"world"` producing `"hello world"`)
is a behavior pin, not authored intent — it arrived in an unrelated main commit
as part of a large coverage bundle, so it is evidence of what the code does
rather than of what it was designed to do. The refutation therefore rests on the
code facts above (the granularity of entries versus items, the `queuedInput`
type's single text field, and the producer survey), with the pin as a
consistency check rather than as the authority. Changing that assertion to match
roborev would have required evidence that the check itself was wrong; the
evidence points the other way, so the finding is declined on those grounds and
the behavior is unchanged.

### Gates on the merged-plus-findings head

Run serially on the final tree, each gate's own exit status read directly (`make`
followed immediately by `$?` — never a trailing `echo`, which masked a real
`lint-gofmt` failure earlier in this delivery).

First run found two failures; both are recorded here rather than only the green
re-run:

| Gate | First run | Final run |
| --- | --- | --- |
| `make generate` (zero-diff) | exit 0, 0s | exit 0, 0s |
| `make fuzz` | exit 0, 292s | — |
| `make test-api-package` | **exit 2, 3s** | exit 0 |
| `make vet` | exit 0, 6s | — |
| `make merge-approval-gate` | **exit 2, 898s** | exit 0, 1021s |
| `make test-web` | exit 0, 638s | — |
| `TMPDIR=/tmp make test-web-browser` | exit 0, 241s | — |

- `test-api-package` failed because the merge surfaced a real incompatibility:
  this feature makes `mergeSlashCommands` offer only available, user-invocable
  skills, while main's qualification fixture (`src/protocol/scripts/qualify-package.mjs`)
  passes a bare `{name: "writing", description: "writing skill"}`, so `/writing`
  disappeared from the packaged catalog. The script is main's and unchanged by
  this branch; our filter is the documented contract, so the fixture was stale
  and now carries the flags the contract requires. Assertions in it are
  unchanged.
- `merge-approval-gate` failed on `TestQueuedInputFromClientMutation`, the
  existing characterization test for the M2 behavior described below. Reverting
  the M2 change restored it; the gate then passed with no FAIL lines.

No assertion, filter, pairing, or tolerance was weakened. The only assertions
changed in this round are the two new tests added for M1 and L2, and the two
fixture updates forced by this feature's own contract (`clientMutationInput`'s
three-argument call in main's notes test, and the skill fixture above) — each
preserving its original input and intent.

### Fifth-round independent review

Verdict READY (`task-21-round5-review.md`). The reviewer verified rather than
trusted: it reverted each production change in place to confirm the M1, L1 and
L2 tests fail for the intended reason and then restored them; reproduced the
recorded `test-api-package` exit-2 and its fix; reproduced the conflict surface
as exactly nine hunks and confirmed main's notes feature and this feature are
both intact; and independently derived the M2 refutation (no producer in the
repository emits more than one text item per entry, `queuedInput` has a single
text field that cannot represent item boundaries, and nothing depends on a
separator). It found no weakened assertions.

Its non-blocking observations, accepted as-is:

- `QueueStrip`'s `recordContent` dedupe has no test of its own; the shared
  helper is pinned through the other two doors and the queue suite passes.
- `readComposerDraft` does not canonicalize names already persisted in a draft.
  Cosmetic: the wire path is protected by `buildInput`.
- `go test ./agent/... -count=1` in full mode trips `agent/sandbox` bwrap
  integration tests that need a writable mount namespace. They skip under
  `-short`, every standard gate runs modules with `-short`, and this round never
  touched that package — environmental, not product.

### Sixth and seventh rounds: roborev jobs 9791 and 9814

Two roborev verdicts landed on the post-PR heads and are recorded together here.
Job 9791 reviewed the fifth-round head (`42d27f9..b15d086`) and reported three
Medium and two Low findings; job 9814 reviewed the accumulated range
(`42d27f9..247ac1f`) and reported three Medium and one Low, two of which restate
9791's M1' and M2' at the same sites. All seven distinct findings are addressed.
Both rounds were written red-first, and every new test failed for the intended
reason before its change.

#### Round six (job 9791)

**M1' — a carrier turn could outlive its obligation.** The finding named the
tool round (`agent/session_tools.go`): the carrier was recorded, then the
obligation was persisted, so a crash or failed save in between left a durable
carrier whose obligation the snapshot had lost, and a later fold dropped the
body with nothing left to reload it. Enumerating every producer of
obligation-carrying turns found the same defect in a second place the finding
did not name — `admitCompactedSkillReloads` recorded body-carrying reload
carriers inside its budget loop and persisted their obligations afterwards,
under a comment claiming the opposite. Both now persist the obligations first
and record the carrier only after the save succeeds, matching the order
`admitSkillActivationBatch` already documents. The reverse window (an obligation
whose carrier never landed) is deliberately kept: it re-delivers the complete
body from its recorded source at the next dispatch seam, so it is the safe
direction. The rollback that makes the tool-round ordering possible is shared
(`withoutObligationsByInvocationID`), replacing three copies of the same
drop-by-invocation-ID loop.

**M2' — `commitSkillDelivery` had no rollback.** A failed save left the live
snapshot clean while the durable one still held the satisfied obligations, so a
same-process retry saw no pending delivery and skipped the revalidation those
obligations exist to force. It now snapshots the obligations, inventory and
revision before mutating and restores them when the save fails, guarded by an
unchanged revision so a concurrent writer's work is never clobbered.

**M3' — post-compaction reminders could be duplicated.** Two windows, both
closed. (a) The reminder turn is a durable write that precedes the receipt
consumption, so a crash in between left a handoff that re-appended the reminder
after a restart; `reconcileSkillCompactionReceipts` now treats a durable
`ReloadReminder` turn as the admission it is and retires the handoff it names.
(b) When a later receipt's reminder failed the fit check, the function returned
without consuming the reminders it had already appended, so each retry
re-appended them and spent more of the very window the check measures; the
already-admitted reminders are now consumed before the error returns.

**L1' — a comment claiming an ordering the code did not hold.** The tool-round
comment went away with the reordering; the equivalent false comment in
`admitCompactedSkillReloads` is corrected by its fix above.

**L2' — the sticky-draft path bypassed `canonicalSkillNames`.** The fifth-round
review had already logged this as a cosmetic gap: "`readComposerDraft`
(`draft.ts:57-71`) validates but does not canonicalize a persisted draft", where
"a hand-edited or pre-fix persisted draft could still render a padded chip in
composer state". Roborev rated it Low and named the concrete symptom — a
corrupted v2 draft renders blank/duplicate chips with colliding React keys.
`readComposerDraft` now canonicalizes the stored names, and `addSkillSelection`
routes the whole resulting list through the one canonical definition instead of
deduping by exact string.

#### Round seven (job 9814)

**Medium — skill carriers and reload/delivery notifications used the
non-durable `recordTurn` door.** `recordTurn` appends the turn to live history
first, writes through `Append` (no fsync — `AppendDurable`, which calls `Sync`,
is the durable door), and swallows the write error into a warning. For a turn
whose `SkillState` carries a skill's complete instructions that is the wrong
door: a crash before the writer's next sync could lose the only copy of the body
while the lifecycle advanced as if it had been delivered, and a failed write was
reported as success. Every skill carrier and notification now goes through
`recordSkillCarrierDurably`: fsync before the turn joins history, with the write
failure returned so callers keep their obligations and receipts pending, from
which the next dispatch seam recovers the body.

Applying it also corrected an enumeration error in the sixth-round record above,
which counted three producers of obligation-carrying turns. There are five
recording sites, and `recordSkillDeliveryNotification` is a carrier producer the
earlier note missed — it attaches a `SkillDeliveryObligation` and was recording
through the non-durable, error-swallowing door. Its failed write now returns
before the obligation's identity is corrected, since the bytes that correction
describes were never recorded.

**Low — skill-only pending queue rows rendered blank.** Optimistic and outbox
pending entries discarded `skillNames`, and the pending row rendered only text
and image counts, so a queued submission carrying only skills showed an empty
row until the daemon's own queue record arrived. `PendingTurnEntry` now carries
canonical `skillNames` (collected through the same `canonicalSkillNames` the
durable path uses), and pending rows render the same `skillMarkers` a durable
row does. This unit was delegated; its production diff is `+25/-3` across
`pendingReconcile.ts` and `QueueStrip.tsx`, and its added tests are a
`reconcilePendingEntries` unit test (a padded, duplicated skill item collapses
to `["pkg:probe"]`) and a `QueueStrip` render test asserting a skill-only
pending row shows its `[skill: pkg:probe]` marker. The parent inspected the diff
and the tests before committing them.

#### Red-first evidence

```
--- FAIL: TestSkillToolRound_CarrierNeverPrecedesItsDurableObligation
    a failed obligation save published a carrier turn carrying [{InvocationID:inv-tool-round ...}]
--- FAIL: TestSkillDelivery_CommitSaveFailureKeepsObligationsPending
    obligations after the failed save = [], want the pending obligation restored
--- FAIL: TestSkillReloadReminder_DurableReminderConsumedAtRestore
    the durable reminder left its handoff pending: [... PublicationID:pub-durable-reminder ...]
--- FAIL: TestSkillReloadReminder_FitFailureConsumesEarlierReminders
    handoffs after the fit failure = [... pub-fit-1 ... pub-fit-2 ...], want only the undelivered pub-fit-2
--- FAIL: TestSkillReload_AdmittedCarrierWaitsForItsDurableObligation
    a failed admission save published a carrier turn carrying [{InvocationID:pub-reload-carrier:opaque ...}]
--- FAIL: TestSkillReload_CarrierWriteFailureIsVisible
    a failed carrier write must surface an error, not report a recorded body
--- FAIL: TestSkillReload_FailedReloadNotificationWriteFailureIsVisible
    a failed reload-failure notification write must fail the preparation visibly
--- FAIL: TestSkillToolRound_CarrierWriteFailureIsVisible
    a failed carrier write must surface an error, not report a recorded round
```

The frontend suites first failed on the uncanonicalized `readComposerDraft`
round-trip, on `expected [ '   ' ] to deeply equal []` for
`addSkillSelection([], "   ")`, on the missing pending `skillNames` field, and
on the blank pending row. No assertion, filter, pairing, or tolerance was
weakened, and no test was deleted, in either round.

#### Gates

Both rounds ran the full set on their final heads. Round six at `f51554964`:
`make generate` (zero generated diff), `make vet`, `make test-api-package`,
`make test-web` (web-typecheck, web-test, web-lint), `make merge-approval-gate`
(every lint phase PASS, every module test wave PASS), the six-guard browser gate
(6/6), and `make fuzz`. Round seven at `cc5965941`: the same seven, all exit 0.

The first `merge-approval-gate` run of round six failed at `lint-golangci` on the
round's own new code — `mapsloop` flagged the `priorInventory` copy loop in
`commitSkillDelivery`, now a `maps.Copy` call. That is recorded rather than
quietly fixed: the lint gate caught real new code, and every other phase of that
run passed.

#### Independent review

Round six: a fresh reviewer, given the committed range and told not to trust the
author's account, returned READY with all five findings RESOLVED and no new
defects. It reproduced load-bearingness itself by reverting each production
change in place and capturing its own failure output (7/7 — five Go tests plus
both frontend tests), then confirmed every reverted file was restored
byte-identical. It also ran its own completeness sweep over obligation-carrying
turns and answered two adversarial questions: the accepted reverse window really
does re-deliver through the dispatch seam rather than wedge, and the ignored
consumption error in the fit-failure branch is defensible because the durable
reminder turn lets a restart reconcile it. It flagged one inaccuracy in the
review brief rather than the code: the brief said six new Go tests where the
diff adds five.

Round seven's review returned READY with both findings RESOLVED. It reproduced
load-bearingness itself, 5/5 — reverting each production change in place and
capturing its own failure output for all three new Go tests and both new
frontend tests — then confirmed every reverted file restored byte-identically
and the worktree clean. It traced all five durable-carrier call sites and all
remaining `recordTurn` uses, confirming no skill carrier or notification still
takes the non-durable door, and found no blocking defect. Two narrow
duplicate-on-retry behaviours were noted, both in the safe direction: a retried
batch can re-append an outcome-only notice, and a retried admission can
re-record a carrier; neither loses a body. It also falsified a premise in the
review brief rather than in the code — the brief claimed the pending and durable
previews join with different separators; both join with a single space, so the
new row matches the durable one exactly.

#### Disclosed residuals

- `recordTurn` remains the non-durable door for the rest of the session
  machinery (queue turns, event turns, lifecycle markers). That is the
  deliberate default for turns whose loss is not a body-guarantee violation, and
  both findings scoped themselves to skill carriers and reload/delivery
  notifications, which is what changed.
- `admitCompactedSkillReloads` appends its outcome-only notices before the
  admission save, so a failed save followed by a retry can append the same
  notice twice. It cannot lose a body.
- `recordSkillReloadNotification` still accepts an obligation pointer, which
  would make its turn a carrier; no call site passes one, and the ordering
  requirement is documented on the function.

### Eighth round: roborev job 9943

Job 9943 reviewed `42d27f9..191df52` — the head that carried rounds six and
seven — and reported no critical or high issues, three Medium and four Low.
Rounding to that verdict, all seven are addressed here. One of them was a
regression this branch introduced in round seven, which is recorded as such.

**Medium — within-batch reload dedup was lost (introduced in round seven).**
Staging the reload carriers until after the obligation save (which round seven's
durability finding required) removed a reuse check the immediate-record path got
for free: the loop decided "this body is already present" by looking in the live
history, and an earlier carrier in the same batch used to be there. Two pending
handoffs selecting the same skill therefore admitted two complete instruction
bodies, two obligations and two activation events instead of one body and one
reuse notice. `admitCompactedSkillReloads` now tracks the identities it has
staged in the batch, so the reuse decision matches what the history would show
once the carriers are recorded. `TestSkillReload_DuplicateSelectionsAdmitOneBody`
seeds two handoffs selecting one skill and pins exactly one body and one notice;
it failed before the fix with "admitted 2 instruction bodies for one skill".

**Medium — `cloneQueueState` aliased the new queue selections.**
`CloneThread` feeds cached thread state and remote-cache snapshots, and
`QueueState.SkillNames [][]string` was the one mutable field it copied by
reference (outer and inner). `cloneQueueState` now deep-copies both.
`TestCloneThreadDeepCopiesQueueSkillNames` failed before the fix with "inner
slice aliased: original[0][0] = mutated".

**Medium — an incomplete-identity reload selection was never retired.**
A valid selection naming a skill whose recorded identity is incomplete (a legacy
activation) was skipped with no outcome, and the receipts-consumed predicate
requires an outcome per name, so its handoff stayed pending forever: every later
request re-processed the same selection, re-announcing its reloadable names as
already-present and collecting another delivery obligation each time, with no
bound. The special-case skip is gone; the name now goes through
`prepareSkillActivations`, which reports it as a typed `invalid_metadata`
failure with a visible notification and lets the publication retire.
`TestSkillReload_IncompleteIdentitySelectionRetiresReceipt` pins the visible
report, the consumption, and (by preparing twice) that nothing grows on
re-processing; it failed before the fix with "visible invalid_metadata notices
after the first preparation = 0".

That changed one pinned assertion. `TestSkillCompaction_CheckpointOnly` seeded
exactly this legacy-identity selection and asserted "the winning publication must
record exactly one handoff receipt" *after* the request preparation — i.e. it
pinned the un-retired handoff that roborev identified as the defect. It now
asserts the handoff is consumed, and replaces the old transcript-receipt↔handoff
cross-check with a transcript-receipt↔typed-outcome cross-check (the recorded
`invalid_metadata` outcome's invocation identity must carry the publication's
identity from the transcript receipt), so the publication is still tied to its
cycle. Roborev's finding is the independent evidence that the old assertion
encoded the wrong behavior; no other pre-existing assertion changed.

**Low — `mintSkillOperationID` failures were treated as missing skills.**
Both role-preload sites swallowed a persistence error with `continue`, starting
the delegate without the preloads its role configured — indistinguishable from
"that skill is not available". Both now return the error, keeping fail-soft
handling only for genuine resolution failures.
`TestRolePreload_OperationIdentitySaveFailureFailsSpawn` and
`…FailsDescribe` were proved load-bearing by reverting the two production hunks
in place: without the fix they fail with "error = profile is nil, want the
operation-identity persistence failure" (the spawn proceeded past the swallowed
failure), and pass with it.

**Low — `removeSkillSelection` did not canonicalize.** The add/remove contract
this file documents promises identical behavior on a non-canonical list; round
six canonicalized only `addSkillSelection`. Removal now canonicalizes the list
and the target name too.
`removeSkillSelection canonicalizes before filtering, like add does` failed
before the fix with "expected [ ' pkg:probe ', 'pkg:other' ] to deeply equal
[ 'pkg:other' ]".

**Low — the `skillInput` gate measured raw length.** A whitespace-only or
empty-name selection list canonicalizes to nothing, so the request would carry
zero skill items, but the gate refused it as unsupported. It now measures
`canonicalSkillNames(skillNames).length` — the same list `buildComposerInput`
sends. `the skillInput gate measures the names the wire would actually carry`
failed before the fix (the send rejected).

**Low — `SkillLifecycleSnapshot.PendingSelection` was dead persisted state.**
Production wrote and persisted it; the only reader was a test helper. Every
writer already stored the same selection on the compaction operation that owns
the cycle, so the field was a second copy that could drift — and did:
`acceptAutomaticSkillCompaction` left a previous selection in place when an
automatic selection was absent. Per Jesse's decision, the field, its clone
handling, both writers in `session_skill_compaction.go`, its four occurrences in
`reconcileSkillCompactionReceipts` and the two helpers in
`skill_reload_selection.go` are removed. Test assertions that read the old slot
now read the operation that owns the selection through a
`cycleSkillReloadSelection` test helper; the round-trip test was renamed to
`TestSkillCompactionRestore_ReloadSelectionRoundTrip` and now protects the
authority's round-trip. Reading older snapshots that still carry the JSON field
is unaffected — unknown fields are ignored — and nothing else changed about the
compaction contract.

Red-first evidence for this round:

```
--- FAIL: TestSkillReload_DuplicateSelectionsAdmitOneBody
    admitted 2 instruction bodies for one skill, want exactly one (the second selection must reuse it)
--- FAIL: TestCloneThreadDeepCopiesQueueSkillNames
    inner slice aliased: original[0][0] = "mutated", want pkg:a
--- FAIL: TestSkillReload_IncompleteIdentitySelectionRetiresReceipt
    visible invalid_metadata notices after the first preparation = 0, want exactly one
--- FAIL: TestRolePreload_OperationIdentitySaveFailureFailsSpawn   (patch-invert)
    error = profile is nil, want the operation-identity persistence failure
--- FAIL: removeSkillSelection canonicalizes before filtering, like add does  (patch-invert)
    expected [ ' pkg:probe ', 'pkg:other' ] to deeply equal [ 'pkg:other' ]
--- FAIL: the skillInput gate measures the names the wire would actually carry  (patch-invert)
```

The `PendingSelection` removal has no red test of its own: it is a deletion of
dead state, and its coverage is that the re-pointed assertions still hold.

**Gates.** All seven ran on `95eef0a04` and exited 0: `make generate` (zero
generated diff), `make vet`, `make test-api-package`, `make test-web` (all three
phases), `make merge-approval-gate` (every lint phase PASS, every module test
wave PASS), the six-guard browser gate, and `make fuzz`.

**Independent review.** A fresh reviewer returned READY with all seven findings
RESOLVED and no new defects. It proved load-bearingness itself for all seven new
tests — five Go and two frontend — by reverting each production change in place
and capturing its own failure output, including `TestSkillCompaction_CheckpointOnly`
failing under the reverted M-c fix ("a reported selection must consume its
handoff"), which is the independent check that the re-pinned assertion is
load-bearing; it then confirmed every file restored byte-identical and the
worktree clean.

Its assertion audit confirms this round's claim and adds the detail: exactly one
real re-pin (`TestSkillCompaction_CheckpointOnly`), one renamed test, four
fixture or literal deletions — and 23 mechanical read swaps, which are the
deleted slot's observations now reading the operation that owns the selection,
plus one diagnostic-only change in the live harness. It also corrected the
review brief rather than the code: the brief said six new Go tests where five
were added.

## Ninth round: roborev jobs 10047 and 10100 (2026-09-14)

Two verdicts landed on `f1cbf82c6` — an automatically queued job (10047) and an
explicitly triggered one (10100) — reporting no critical or high issues, five
distinct Medium findings and three Lows. Jesse's ruling scoped this round to
**fix the five Mediums and disclose the three Lows as follow-ups**, so the Lows
remain in the tree by decision. All five Mediums were fixed red-first, and every
new test was proved load-bearing by reverting the deciding production hunk in
place and capturing the failure; the files were then restored byte-identical
(sha256-verified where the parent performed the revert itself).

### M1 — an explicit user invocation left `UserAuthorized` false

An explicit user invocation of an unchanged, already model-activated skill took
`planSkillDeliveryCommit`'s `already_present` path, which updated nothing: the
inventory kept the earlier model-route record, `UserAuthorized` stayed false,
and once the source set `disable-model-invocation` the reload the user had
authorized was denied.

`planSkillDeliveryCommit` now builds the activation provenance with
`deliveryActivationRecordLocked` for an unchanged body when the obligation's
route is a genuine user route and the prior record lacks that authorization, and
`commitSkillDelivery`'s `already_present` case writes that record into the
inventory — still no duplicate body, still no `EventSkillActivated`.

`TestSkillDelivery_UserReinvocationRecordsAuthorization` drives a real model
activation then a real `/opaque` re-invocation of the identical body and asserts
the recorded authorization, the single retained envelope, one new-body event,
and that a `compaction_reload` of the recorded source is allowed after the
source disables model invocation. Reverting `agent/session_skill_delivery.go` to
`f1cbf82c6` fails it with "user re-invocation did not record its authorization:
... Route:model_tool ... UserAuthorized:false".

### M5 — the changed-content outcome omitted its previous provenance

`prepareSkillDelivery`'s delivered outcome for changed disk content recorded
only the new identity and controls, although
`schema.SkillActivationOutcome.PreviousIdentity`/`PreviousControls` exist for
exactly that provenance and the reload path already populates them. The outcome
now carries the pre-change identity and the prior inventory record's controls;
the notification text is unchanged.

`TestSkillDelivery_ChangedSourceRecordsPreviousProvenance` changes the source
and folds the old carrier away between the provisional result and dispatch, then
pins both fields on the recorded outcome. Under the same revert it fails with
"outcome previous identity = <nil>, want the pre-change identity ...".

### M2 — a failed admission could leave the selection silently undelivered

`admitSteeringSelectionBatch` only warned on an admission save failure. The
steering prose was already durable, the obligations were rolled back, and the
next model request processed the selection without its instructions. The initial
fix retained the batch in memory and retried it at the head of
`prepareModelRequestWithError`, so the request either carried the selected
instructions or failed visibly; a failure whose obligations *did* reach disk is
deliberately not retained, because the dispatch seam already re-delivers those
bodies (`TestSkillActivation_LostSteeringAdmissionGatesNextDispatch`).

**The round's independent review then found the fix partial**, and that finding
is accepted: `reconcilePendingSkillSelections` discarded its own admission error
and armed nothing, so a *restored* session with an unwritable metadata store
built requests with the steering prose, no instructions and no error. The
retain-and-gate behavior is now one shared helper
(`admitPreparedSkillSelection`) called by both the live steering consumption and
the restore reconciliation. `TestSkillActivation_RestoredSelectionAdmissionGatesNextDispatch`
makes the metadata path unwritable before `RestoreSessionFromMeta`; reverting
the reconcile call site fails it with "the restored session prepared a request
without the selection's instructions and without an error".

### M3/M3b (one root) — the reload budget ignored staged notifications

`admitCompactedSkillReloads` measured instruction bodies only. The reminder and
explanation turns appended by preparation and admission therefore did not count
against the window, so the final rebuilt request could exceed it and fail the
whole turn after the receipts had been consumed.

`prepareCompactedSkillReloads` now reports the input-token cost of the turns it
appended; admission folds that into the running total, adds each notification it
appends itself, and reserves — before admitting any body — one notice per item
still to be processed, released as each item is handled. The reservation is what
keeps a body from consuming headroom a later rejection's explanation needs.

Four tests pin it. `TestSkillReload_Budget_PreparedReminderCountsAgainstAdmission`
(the same body fits when no reminder precedes it, and is rejected with
`context_budget` when one does) and
`TestSkillReload_Budget_StagedNotificationsStayWithinWindow` fail when the
staged-token fold is removed ("reload outcome = status \"pending\" ... want
failed context_budget", and the running-total assertion);
`TestSkillReload_Budget_RejectionExplanationCountsAgainstLaterBodies` pins the
explanation's own tokens. **During parent verification the notification reserve
turned out to be unpinned** — removing it left all three green — so
`TestSkillReload_Budget_LaterExplanationReservedBeforeEarlierBody` was added and
is load-bearing for it ("big reload outcome = status \"pending\" code \"\", want
failed context_budget: the earlier body must not consume headroom the later
explanation needs").

### M4 — a prepared selection could be silently retargeted at restore

The durable prepared record stored only the canonical name, so
`reconcilePendingSkillSelections` re-resolved it against the current catalog: a
same-name source change during the admission-loss window silently retargeted the
selection, against the approved never-retarget-a-collision contract.
`schema.SkillSelectionInvocation` now records the exact
`SkillContentIdentity` preparation resolved, and the re-drive pins that source.

`TestSkillActivation_PreparedSelectionPinsRecordedSource` deletes the recorded
source and plants a same-name replacement elsewhere; without the pin the
reconcile adopts the replacement ("reconciled obligations = [{... Source:
.../.agents/skills/opaque/SKILL.md ...}], want none: the recorded source is gone
and a same-name replacement must never be adopted"), and with it the re-drive
fails visibly on the recorded source.

### Assertion audit

The round adds nine tests (eight fixed-test additions plus the reserve test).
Exactly **15 pre-existing lines were removed, all in
`agent/session_skill_reload_test.go` and all mechanical carrier updates for the
two changed function signatures** — identical inputs, assertions, pairings and
tolerances; zero removed lines elsewhere in the range. The round's independent
review verified this count and verified that each of the nine new tests fails
under its own decisive revert, with none failing to fail. It also falsified one
premise of the review brief rather than the code.

### Disclosed residuals

- A **preparation** failure during restore reconciliation still only warns. The
  recorded source is gone, so there is nothing to admit, and gating on it would
  wedge the session with no in-process recovery path while the durable input
  record re-drives the preparation at the next restore. The round's reviewer
  examined this and agreed the residual is honest rather than a remaining silent
  omission.
- The three Lows are deliberately unfixed per Jesse's ruling, and were confirmed
  still present by the round's review: plugin manifest diagnostics discarded in
  past-thread discovery (`cmd/evener-hub/app_threadread.go` ~363); unreachable
  `skillChipDetails` diagnostic branches against the daemon's filtered catalog
  (`cmd/evener-hub/frontend/src/panes/session/composer/Composer.tsx` ~1011-1018
  against `agent/status.go` ~173-181); and identity-less terminal cancellation
  receipts that never coalesce or prune
  (`agent/session_skill_compaction.go` ~292-304). All three are closed by the
  follow-up PR recorded below.

## Post-rebase provenance: rebase onto main (2026-09-14, PR #1168)

Jesse's ruling for the integration was to rebase and force-push again, after the
PR had gone conflicting as main advanced. Main was fourteen commits ahead of the
merge base, including SDK migration row A3
(`refactor(sdk): move the AppWire TypeScript package to appwire-client/typescript`
— #1241), which turned the web tree's `cmd/evener-hub/frontend/src/protocol/`
files into re-export seams over a new package.

The rebase replayed 75 commits. Git's rerere reused a recorded resolution where
the same conflict recurred, which kept the work to the distinct hunks below.

- `agent/session.go`, `agent/session_state.go`, `agent/session_init.go`: this
  feature and main had independently extracted the same inline metadata-save
  body into a helper (`saveMeta` here, `autoSaveMeta`/`autoSaveMetaLocked`
  there). Main's is kept as the single implementation and this feature's
  `saveMeta` is now a documented delegate to it, so the locking, meta-FS and
  flush sequence exists once. The same files union-resolved main's restored
  notes/agentNote fields with this feature's skill-lifecycle restore, and main's
  escaped notes history with this feature's stripping of inherited delegate
  skill state.
- `server/appwire_runtime.go`: main's `SharedNotes` capability line plus this
  feature's `SkillInput` capability computation; the placeholder `SkillInput:
  false` of an intermediate commit is superseded by the real computation, as it
  was before the rebase.
- `agent/session_client_mutation_queue.go`, `agent/schema/snapshot.go`,
  `agent/schema/turn.go`, `agent/session_client_mutation.go`: union resolutions
  (the steering message's skill names plus main's steering kind; both sides'
  schema and turn fields).
- The four `protocol/` files A3 turned into seams (`composerInput.ts`,
  `slashCompletion.ts`, `submitRouting.ts`, `types.gen.ts`): main's seam is kept
  for the hub path and this feature's changes are re-applied at the package in
  two commits — first the two relocated test files, then the content
  (`canonicalSkillNames` and the skill-selection parameters on
  `buildInput`/`buildComposerInput`, the selection-only steer route
  `hasSkills`, and the regenerated `types.gen.ts`) — plus
  `composerInput.test.ts` and `skillInput.test.ts` moved beside the modules they
  cover. `slashCompletion.ts`, `slashCompletion.test.ts`,
  `submitRouting.test.ts`, `reducer.test.ts` and `scripts/qualify-package.mjs`
  were already carried there by rename detection.

**Preservation audit.** Of the 153 files this feature touched before the rebase,
exactly ten no longer differ from main: the four seam files and six
`protocol/` test/script files that A3 moved into the package. Each was confirmed
present at its new path with a change size identical to the pre-rebase diffstat
(+22, +43, +58, +4, +61, +95, +28, +12, +15 and +16 lines respectively), so no
feature change was dropped by the rebase.

### Gates on the rebased head

Run serially, each gate's own exit status read directly.

| Gate | Result |
| --- | --- |
| `make generate` (zero-diff) | exit 0 |
| `make vet` | exit 0 |
| `make test-web` (typecheck, vitest and biome, including `appwire-client/typescript`) | exit 0, 927s |
| `make merge-approval-gate` | exit 0, 1529s (first run, see below) |
| `TMPDIR=/tmp make test-web-browser` | exit 0, 341s |
| `make fuzz` | exit 0, 498s |
| `make test-api-package` | exit 0 |

The first `merge-approval-gate` run ended exit 2 with every lint phase PASS and
every module test wave PASS, failing only at its last phase,
`make test-api-package`, with `Cannot find package 'ws' imported from
appwire-client/typescript/scripts/qualify-package.mjs`. A3 introduced that
package and this worktree had never installed its dependencies (CI runs
`npm ci --prefix appwire-client/typescript`). That is an environment gap, not a
product failure: `test-api-package` is the gate's final phase so nothing behind
it was masked, `make test-api-package` alone exits 0 once the declared dev
dependencies are installed, and the full gate then exits 0. Both runs are
recorded because the first one is evidence too.

Direct evidence that the relocated SDK tests execute rather than merely being
collected: `npx vitest run composerInput.test.ts skillInput.test.ts
submitRouting.test.ts slashCompletion.test.ts` reports 4 files and 70 tests
passed.

## Tenth round: roborev job 10305 on the rebased head (2026-09-14)

The verdict on the pushed head (`3c44fe1c8`) was "Mostly clean implementation;
one medium-severity durability gap and two minor consistency/defensive-copying
issues", with two of the four reviewers reporting no issues at all.

### Medium — refuted, not fixed (with the finding's requested coverage added)

The finding: "Compaction reload can be silently lost on crash before receipt
commit" — restore unconditionally marks a persisted `published` compaction
operation as delivered, while metadata autosaves are not synchronized with the
transcript receipt write, so a crash between the claim and
`commitTranscriptsLocked` can "silently discard the selected skill reload". Its
fix suggestion: synchronize the two, or require a matching durable receipt
before completing a published operation.

The described harm does not materialize, and the suggested rule would introduce
a worse one:

- **The claim is atomic with its handoff.** `claimCompactionLocked`
  (`agent/session_compaction.go`, the closure at ~712-747) mutates the slot to
  `published` and appends the coalesced handoff — carrying the operation and its
  selected names — inside ONE `s.mu` critical section. A metadata save takes
  `metaSaveMu` and then `Meta()`, which needs `s.mu`, so no save can observe a
  published slot without its handoff. The crash window the finding describes
  persists BOTH.
- **Delivery reads the handoff, not the phase.** `prepareCompactedSkillReloads`
  walks pending handoffs by selection state and skips only cancelled ones, so a
  handoff whose phase was advanced to delivered by the restore's completion
  block is still prepared and admitted.
- **Evidence.** `TestSkillCompactionRestore_PublishedSlotWithoutReceiptStillDelivers`
  constructs exactly that artifact — the published slot and its handoff, with no
  receipt anywhere in the transcript — restores it through the real
  `RestoreSessionFromMeta` path, and pins that the selection survives on the
  handoff, the complete body's carrier is admitted for the model, and the cycle
  reopens.
- **The suggested rule would wedge.** Retaining the published slot leaves it
  occupied, and `requestSkillCompaction` refuses every new intent while a
  published operation owns the slot
  (`agent/session_skill_compaction.go:169-174`). The new test pins that recovery
  does not wedge, which a "retain the published slot" rule would have broken.

This is recorded as a refutation on the evidence, in the same form as round
five's M2, rather than a silent dismissal. The finding's own coverage request —
coverage for a save occurring between the claim and the transcript commit — is
the new test, which passes because the behavior it asks about is already
correct.

### Two new Lows (disclosed, not fixed)

- `PendingChips` renders no skill marker for a skill-only submission while
  `QueueStrip` does (`cmd/evener-hub/frontend/src/panes/session/pending/PendingChips.tsx` ~57):
  the body is blank while a skill-only send is in flight. No data is lost.
- `skillInventorySnapshot` returns a shallow map clone whose values share the
  live `*OrdinarySkillActivation`/`*FrozenSkillPreload` pointers
  (`agent/skill_reload_selection.go:90`). Every current caller only reads, so
  there is no live bug; a future mutating caller would corrupt the live
  snapshot.

## Follow-up PR: the post-merge findings (2026-09-14)

Jesse's ruling at merge time was "squash merge it now, then follow up all the
followups in a new PR right now". This section records that PR: every item from
roborev jobs 10305 and 10460 that was still open, plus one declined item, on a
branch cut from the merged main (`c09997369`).

### Medium — delivery notification and obligation could diverge after a failed save

`prepareSkillDelivery` records its typed notification turn through the durable
transcript door before it finalizes or identity-corrects the matching obligation
in the snapshot. A failed metadata save therefore left a snapshot staler than
the transcript, and a restart re-processed the same missing or changed skill and
appended the same notification again.

`reconcileSkillDeliveryNotifications` (in `agent/transcript_read.go`, called from
the existing receipt reconciliation at restore) now applies the durable
outcomes to the snapshot's pending obligations: a `failed` outcome finalizes its
obligation — the recorded explanation turn IS that delivery — and a `delivered`
outcome adopts the corrected identity the turn carries. Outcomes from another
session are ignored, and the last outcome per invocation identity wins, because
the transcript is chronological.

`TestSkillDelivery_SaveFailureNotificationReconciledAtRestore` covers both
halves; reverting the reconciliation call fails it with "reconciled obligations
= [...] want none: the durable failure notification already finalized it" and,
on its second subtest, "changed identity corrected".

### Medium — authoritative queue rows dropped skill visibility

The daemon's own queue row rendered `queuedEntryPreviewLine` verbatim — text
alone when present, a generic `[skill]`/`[N skills]` otherwise — while pending
and durable rows named the selection from the entry's own canonical names. A
text-plus-skills entry therefore lost its skill indication the moment the
authoritative row replaced the pending one, and a skill-only entry flickered
from named to generic.

`QueueStrip`'s daemon-row branch now appends `skillMarkers(queue.skillNames[index])`
after the truncated preview, and `skillMarkers` moved to the shared
`queue/queueDisplay.ts` so the queue rows, the durable rows and `PendingChips`
render a selection through ONE definition. `PendingChips` uses it too, so an
in-flight skill-only submission finally shows `[skill: name]` instead of a bare
"Sending"/"Steering"/"Draining".

The first version still doubled the label: a skill-only entry rendered the
daemon's generic `[skill]` placeholder AND the named marker
(`[skill] [skill: pkg:probe]`), which the lane's presence-style assertion could
not see. The round's independent reviewer caught it, and the row composition now
drops the generic placeholder when the entry carries its own canonical names
(text previews keep their text, and a non-skill preview such as an image
placeholder is untouched). The exact-text assertion in
`QueueStrip.test.tsx` — "a skill-only authoritative row drops the daemon's
generic placeholder instead of doubling it" — fails without that rule.

The pins are `QueueStrip.test.tsx`'s "an authoritative row appends its skill
markers to the daemon's preview text" and "a skill-only authoritative row shows
its named marker rather than staying generic" (2 failures under the reverted row
composition) and `PendingChips.test.tsx`'s two marker tests (2 failures under
the reverted chip body).

### The four remaining Lows

- **Cancellation receipts accumulated without bound.** Every
  `cancelSkillCompaction` appended an identity-less `cancelled` receipt that
  `removeSkillCompactionHandoffsLocked` (publication-ID-keyed) could never
  remove, so `PendingHandoffs` — persisted and cloned on every autosave — and
  the per-request scan grew by one per cancellation for the session's life.
  `retireSkillCompactionCancellationsLocked` now retires them, called from the
  per-request reload scan; a failed retirement save only warns and leaves them
  for the next request. Reverting the call fails
  `TestSkillCompaction_CancellationReceiptsAreRetired` with three accumulated
  records where none may remain.
- **A transient admission-save failure duplicated reload notifications.**
  Retrying after such a failure re-derived the same deterministic
  `publication:name` invocation and appended the same notice again.
  `skillReloadOutcomeRecorded` now reports whether live history already carries
  an outcome for that invocation, and all three notice sites (reload failure,
  reuse, `context_budget`) record only when it does not.
  `TestSkillReload_FailedNoticeNotReappendedAfterSaveFailure` and
  `…ReuseNoticeNotReappendedAfterSaveFailure` both fail with "notices after the
  retry = 2, want 1" when the guard is forced to false. This **removes a
  residual disclosed in round six/seven** ("a retried batch can re-append an
  outcome-only notice"), which roborev is the independent evidence for fixing;
  no existing assertion was weakened, and the disclosure above is superseded by
  this note.
- **Plugin manifest diagnostics were discarded in past-thread discovery.**
  `discoverPastThreadSkills` dropped `plugin.SkillSources`' diagnostics while
  converting the catalog's. Both now convert through one
  `pastThreadSkillDiagnostic` helper, plugin diagnostics first, matching session
  startup's ordering. Reverting the loop fails
  `TestDiscoverPastThreadSkillsCarriesPluginDiagnostics` with "plugin collision
  diagnostic dropped: []".
- **`skillInventorySnapshot` returned a shallow clone sharing live pointers.**
  The deep copy moved into `schema.CloneSkillInventory`, now the single
  implementation used by `SkillLifecycleSnapshot.Clone` and by the non-lifecycle
  accessor, so a mutating caller can never reach the live snapshot. Reverting
  the accessor to `maps.Clone` fails
  `TestSkillInventorySnapshotDeepCopiesEntries` with "snapshot aliases the live
  lifecycle inventory".
- **`cloneDescriptor` did not clone `FrozenSkillMetadata`**, leaving the clone's
  slice header aliased to the store's. Fixed alongside the other slices.
  Reverting it fails `TestApplyAndFoldCloneCreatedDescriptor/frozen_skill_metadata`
  with the mutated description visible in the accepted state.
- **`skillChipDetails`' two diagnostic branches were unreachable.** The daemon
  publishes only available, user-invocable skills, so `!info.available` and
  `!info.userInvocable` could never fire and their messages could never be
  shown. Both are removed and the comment now records why a present entry is
  always usable; the reachable "no longer in this session's skill catalog"
  branch is unchanged. The pin is `Composer.test.tsx`'s "a selected skill's
  tooltip never invents an unavailable or non-user-invocable diagnostic", which
  plants a flagged entry and asserts the tooltip is exactly the description;
  restoring the branches fails it. Its sibling — "a selected skill the catalog
  no longer reports says so in its tooltip" — covers the retained branch and is
  a coverage pin rather than a load-bearing one for this removal, which the
  round's reviewer noted and this record states plainly.

### Declined: `InputItem.UnmarshalJSON` strictness (not fixed, deliberately)

Roborev's remaining item asked that a skill item's struct-field validation
accept structurally-empty extra keys, since `NormalizeMutationInput` accepts
Go-constructed items whose `Text`/`URL`/`Data`/`Path`/`Metadata` are empty while
`UnmarshalJSON` rejects the corresponding raw keys.

This is declined on evidence rather than convenience:

- The wire contract is documented and pinned:
  `docs/skills.md` lists `{"type":"skill","name":"plugin:release-notes","text":""}`
  among the REJECTED forms, and `TestSkillInputRejectsRawPathAndBody` pins the
  same three structurally-empty bodies (`path`, `text`, and a non-empty `body`).
  Implementing the finding would mean weakening a documented, tested security
  contract — exactly the re-pin the rules require independent evidence for, and
  the independent evidence points the other way.
- The strictness is the point: `UnmarshalJSON` cannot see fields the `InputItem`
  struct never names, so `{"type":"skill","name":"x","path":"/etc/passwd"}` would
  otherwise decode as canonical and silently drop the smuggled path. A
  zero-value check would have to enumerate the raw keys anyway to reject unknown
  ones; the canonical-only rule is simpler and is what the docs promise.
- The asymmetry is deliberate and already documented on both sides: the wire is
  untrusted, Go callers are not. No client emits extra keys —
  `appwire-client/typescript/composerInput.ts` builds exactly
  `{type: "skill", name}`.

The lane that hit this refused to re-pin the assertion and reported the exact
conflict instead, which is why the original files remain byte-identical.
