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
| Documentation/review/PR | `docs/skills.md` implements the delivered contract (coverage verified line-by-line by the Task 15 independent review §6); this evidence document; per-task independent reviews 1–15 committed under `.superpowers/sdd/2026-09-11-skills-lifecycle/`. The final independent implementation review and the PR open after this stage's report by design; they are the remaining delivery steps and are not claimed here. |

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
