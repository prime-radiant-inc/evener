# Native mobile delivery implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development for bounded implementation packages. Use superpowers:executing-plans for coordinated native verification. Jesse selected active subagent execution; do not ask him to choose it again.

**Goal:** Deliver a beautiful, intuitive, reliable, feature-complete native Evener app on iOS (iPhone and iPad), with multiple hubs, automated tests and manual native E2E, plus an independently usable protocol and API client library.

**Architecture:** Keep the shared Expo/React Native application in `mobile-native/`. Reuse the framework-independent AppWire client and validated shared services. Current web/server behavior defines capability; platform-specific interaction and layout belong in the native app.

**Tech stack:** Expo SDK 57, React Native 0.86, React Navigation, SecureStore, SQLite, TypeScript AppWire client, real isolated Evener hubs with scripted providers at the LLM boundary.

**Spec:** [Full coverage objective](../specs/2026-09-05-native-mobile-coverage.md), [product philosophy](../../design/mobile/philosophy.md), [style guide](../../design/mobile/style-guide.md), and [acceptance backlog](../../design/mobile/backlog.md).

## V1 platform scope

Jesse explicitly selected **iOS-only v1** during takeover continuation. Android
implementation, builds, device testing, lifecycle investigation and release
qualification are deferred. Preserve the existing Android source and evidence;
do not remove its support or bypass shared checks. This decision supersedes
both-platform requirements in the original handoff and historical studies.
Shared feature completeness, multi-hub correctness and the independent SDK
remain required. iPhone/iPad accessibility, physical networking, performance,
signing and install/update qualification remain release gates.

## Global constraints and evidence baseline

- Voice/barge-in is outside v1; retain every other current supported workflow.
- The old Tauri UI is not a feature reference. Only shared code actually imported by the React Native app belongs in native implementation assessments.
- Model/reasoning belong in the composer. Input uses full width; Submit shares the controls area below it. Large text must retain a usable reading and editing experience.
- Keep hub identity attached to connections, navigation, requests, drafts and cached data. Never blindly repeat uncertain mutations.
- Use direct authenticated isolated hubs. No WebSocket forwarding/fault-injection proxies. Deterministic faults belong in client/service tests.
- No production mutations or external messages for qualification. Preserve unrelated work and credentials; do not print tokens.
- Read exact Expo 57 docs before code changes and testing guidance before changing tests. Use existing build/test tooling and normal hooks.
- Jesse authorizes routine decisions and execution. Escalate only a consequential architectural choice or an actual unavailable external prerequisite.

This is the whole-product delivery sequence, not a claim that every later code change is already designed. Before dispatching an implementation package, write its source-specific steps and behavioral tests from the freshly rebased code. Do not invent APIs or carry stale historical absence claims forward.

Audit baseline: `ef3e5a5d3`, 6 September 2026. The last recorded checks passed 349 native tests and both simulator Release builds. These are historical checks, not release acceptance. A fresh fetch found 80 main-only commits and 463 branch-only commits; `origin/main` was `3b1c5f82c`. Integration is the first prerequisite.

## AppWire v4 integration prerequisite

The rebase introduced breaking contracts. Complete these before additional visual work:

- Shared conversation service: item-count paging, opaque cursors, fragment identity/boundaries, stale-cursor recovery and subscription reconciliation.
- Native navigation: representation version 2, snapshot/delta decoding and exact generation/revision/etag bases, including project reveal and mutation readback.
- Provider/settings workflows: credential JSON, descriptor variable mapping, explicit base-URL clearing, and removal of obsolete Codex launch overview fields. Add keybinding settings to the coverage ledger.
- Shared transport: accept and validate the current optional handshake capabilities; preserve current web readiness and native reconnect publication semantics.
- Documentation/examples: current v4 handshake and source-backed migration guidance; historical v3 execution evidence remains historical, not v4 qualification.

The first three packages are assigned to separate workers with disjoint file ownership. Bot owns shared transport, generated artifacts, integration checks and serialized commits. Native/server E2E follows deterministic migration tests on the integrated result.

## Execution and ownership

Bot owns integration, architecture, UX coherence, reviewer triage and final acceptance. Default new implementation/audit/review workers to **Luna (`gpt-5.6-luna`), medium**, as Jesse requested. Use up to three independent workers alongside coordinator work. Reuse existing agents when the session's thread limit prevents new ones; report model limitations honestly.

Every dispatch gets an exact file boundary, current source revision, web/server authority, expected tests, prohibited side effects and a concrete deliverable. Workers may not independently operate shared simulators or hubs. Bot serializes device use. Separate changes that touch the large `screens.tsx`; parallelize independent controllers, protocol examples, tests and documentation. Review each result against source: the first feature audit mistakenly targeted old Tauri screens and had to be corrected.

The reusable work cycle is: source-backed gap → whole-workflow design → failing behavioral test for logic changes → smallest implementation → tests → independent review → native E2E → evidence and commit. Pure layout changes use actual native geometry/interaction rather than brittle markup tests. Do not accumulate additional spacing-only tasks while core journeys remain incomplete.

## Delivery sequence

### 0. Integrate and establish an authoritative acceptance ledger

**Files:** branch history; `docs/design/mobile/backlog.md`; `mobile-native/README.md`; coverage spec above; `make/testing.mk`; existing `.github/workflows/` gates.

- [x] Preserve the unrelated generated Tauri iOS modifications, checkpoint the branch, and rebase onto freshly fetched main. Rebased onto `3b1c5f82c`, yielding `ee142bd5e`; unrelated edits restored with identical binary diff. Backup: `codex/mobile-before-main-rebase-20260906`.
- [ ] Run affected generation and repository/native checks after integration. Record the new source revision before dispatching feature edits.
- [ ] Replace historical status assertions in the coverage inventory with source-verified implementation and qualification columns. Keep historical evidence dated and attributed.
- [ ] Create one acceptance ledger row per workflow below: web/server source, native source, deterministic tests, real-daemon scenario, iOS evidence, Android evidence, source/build identity, remaining acceptance, owner. Missing evidence stays open.
- [x] Add explicit native test/typecheck and clean API-package checks to the existing gate ownership. Implemented in `d7075ce74` and corrected/qualified in `cab95068c`: `make test-native`, `make test-api-package`, merge gate and CI ownership. Full repository gate still requires a successful integrated run.

**Exit:** one source revision, an exhaustive workflow ledger, and gates that actually execute native/package checks. No feature declared absent or finished merely from an old document.

### 1. Reliable hub identity and lifecycle

**Issues:** MOB-004, MOB-010, MOB-017. **Files:** `mobile-native/src/ConnectionProvider.tsx`, `connection.ts`, `location.ts`, `nativeLocation.ts`, `draftRepository.ts`, `creationDraftRepository.ts`, `removeHub.ts`, associated tests; shared `protocol/client.ts` and reconnect tests.

- [ ] **Deferred beyond v1:** Diagnose Android ANR and font-change connection failure using timed lifecycle/transport evidence. Reproduction, thread state and system load must distinguish app failure from emulator pressure. Do not add retries or restart the app merely to hide the symptom.
- [ ] Define and verify per-hub connection/navigation lifetime. Saved multiple profiles alone is insufficient: switching away from a pending operation must retain its destination and unresolved state.
- [ ] Test overlapping IDs, delayed old responses, hub removal, credential rotation/rejection, foreground/background and process death around dispatch. Preserve drafts and reject stale actions without cross-hub writes or automatic replay.
- [ ] Run direct-hub native journeys on iPhone and iPad, then physical LAN/VPN/pairing checks when devices are available. Preserve unfinished resource-editor work and conversation drafts across iOS process death.

**Exit:** the end-to-end identity/recovery scenarios pass on the iOS build; unexplained iOS connection failures prevent lifecycle acceptance. The Android ANR remains recorded for later Android delivery.

### 2. Finish conversation reading and composing as one screen

**Issues:** MOB-001/002/003/009/011. **Files:** `mobile-native/src/screens.tsx`, `TimelineItem.tsx`, `MarkdownResponse.tsx`, `ComposerSettings.tsx`, `CommandCompletion.tsx`, `TranscriptImages.tsx`, `ImageAttachments.tsx`, `location.ts`; design study and style guide.

- [ ] Update one whole-screen study with identical substantial content for reading, keyboard-open composition, running work, failure and pending decision. Show both themes, iPhone/iPad layouts and large text. Consolidate healthy connection chrome and secondary actions as a screen-level design decision.
- [ ] Implement reader restoration using message identity and within-message position; define persistence explicitly. Handle pagination, streaming, image reflow and returning from sheets without stealing the reader's place. Scope disclosure and position by hub/session.
- [ ] Qualify Markdown, long code/tables, links/copy, multiple and authenticated images, error/loading states and return paths. A displayed source path is not proof that its destination opens.
- [ ] Exercise long drafts/model names, model/default reasoning, command selection/failure, Send/Steer/Stop/Queue and attachments with real keyboards. Full-width input and reachable controls must coexist with useful reading space.

**Exit:** open a long session, read history, expand output, compose, switch away and return without losing context; perform the journey on iPhone and iPad at ordinary/largest text, narrow and landscape sizes. Visual acceptance covers the journey, not pixel savings alone.

### 3. Running work, decisions and concurrent updates

**Issue:** MOB-008. **Files:** native `QuestionSheet.tsx`, `questionAnswers.ts`, `questionBatches.ts`, `ApprovalSheet.tsx`, `approvalControls.ts`, `QueueSheet.tsx`, `ActivitySheet.tsx`, `TasksSheet.tsx`, `jobOutput.ts`; shared conversation services/state and their tests.

- [ ] Cover opening-window notification races, stale decisions resolved by another client, concurrent question batches, queue cancellation/promotion/drain and uncertain replies.
- [ ] Qualify goal/task/activity paging and delegate navigation under concurrent updates. Keep consequential decisions visually distinct from routine progress and tool output.
- [ ] Exercise a real scripted-provider session through sandbox execution/approval and question pause/resume; independently read resulting state. Injected UI notifications alone cannot prove execution resumes.
- [ ] Repeat keyboard-open, reconnect and background cases on iOS with no stale actionable controls or duplicated submissions.

**Exit:** a user can understand running work, act on the correct decision, see real progress resume and recover when another client acted first.

### 4. Discovery, organization and session management

**Issue:** MOB-005. **Files:** native `ProjectsScreen.tsx`, `navigationPages.ts`, `navigationTree.ts`, `navigationActions.ts`, `SessionMenu.tsx`, `SessionSheet.tsx`, `sessionControls.ts`, `screens.tsx`; current web rail/pinning/session-action authorities.

- [ ] Map every offered web management action to its native destination and contract, including fork/edit/clear/remove, pin sections, favorite/archive and remaining lifecycle controls. Implement actual gaps instead of recreating existing archive/rename flows.
- [ ] Cover paging, empty/filter states, invalidation, section rename/delete, branch recovery and post-mutation destination changes.
- [ ] Find and manage similarly named sessions on two hubs with multiple pages and long paths; preserve list context on return and resolve uncertain mutations by authoritative readback.
- [ ] Measure usable-list latency on representative data; a tiny fixture cannot qualify roster performance.

**Exit:** every supported web organization/management journey is usable and verified natively, including error and return paths.

### 5. Creation and administration in independent packages

These can run in parallel after lifecycle contracts stabilize, with disjoint file ownership. Each package ships its matching protocol recipe and manual scenario.

| Package | Existing native sources | Required completion evidence |
| --- | --- | --- |
| Creation, launch configuration and trust (MOB-007/015) | `NewSessionScreen.tsx`, `newSession.ts`, `CreationComposerSettings.tsx`, `CreationPlugins.tsx`, `LaunchSettingsScreen.tsx`, `launchSettings.ts`, `RepositoryLaunchReview.tsx`, image/draft controllers | Large catalogs; inherited/effective/edited values; real configuration readback; directory assistance; trust rejection/revision changes; MCP; durable image prompt; nonempty uncertain creation and dispatch-boundary death without duplicates |
| Provider instances and login (MOB-012/013) | `ProvidersScreen.tsx`, `ProviderEditor.tsx`, `providerInstances.ts`, `ProviderSignInSheet.tsx`, `providerSignIn.ts` | Keys/defaults; browser/device flow success/denial/cancel; return from external browser; hub switch and restart; no credential leakage or wrong-hub updates |
| Marketplace/plugin management (MOB-014) | `PluginsScreen.tsx`, `MarketplaceBrowser.tsx`, `marketplaces.ts`, `installedPlugins.ts` | Browse/preview/install/update/enable/remove and sources; actual Git/GitHub update; failure/readback, long lists and iOS refresh |
| Hub overview/preferences and upgrade (MOB-015/016) | `HubSettingsScreen.tsx`, `hubOverview.ts`, launch editors; current settings section contracts | Correct scope and live invalidation; disposable-hub upgrade; progress/failure/reconnect; independent version/readiness readback. Production hub upgrade is not a test fixture |

**Exit:** every supported administrative operation has a purposeful native workflow, validation, safe recovery and iOS acceptance. Generic forms or callable RPC wrappers do not establish this.

### 6. API library and complete independent protocol documentation

**Issue:** MOB-018. **Files:** `docs/appwire-client.md`, `docs/appwire-protocol.md`, `cmd/evener-hub/frontend/src/protocol/{README.md,package.json,examples/coverage.mjs}` and neighboring examples/tests.

Run this lane alongside packages 1–5, not after them. The current report names 14/88 methods and 3/35 notifications across five recipes; it measures declared recipe presence, not verified execution or failure coverage.

- [ ] Map every supported/reserved method and notification from the generated catalog. Link each workflow to executable cases; account for reserved-method rejection separately from usable capabilities.
- [ ] Add streaming/reset/paging/rejoin recipes first, then mutation recovery and decisions, then management, credentials/plugins, trust and upgrade with their native packages.
- [ ] Explain exact presence/default semantics, errors, event ordering, snapshot reconciliation, lifecycle, identity and recovery without requiring implementation-source reading.
- [ ] Verify outside-checkout tarball consumers, ESM/CommonJS, declarations without skipped checks, package contents, and the iOS Metro Release bundle. Execute happy/failure cases against real isolated Evener with a scripted provider boundary; verify effects and cleanup.

**Exit:** an independent client author can implement every supported flow from the guide/reference/library/examples alone. Publishing the package is distinct from local package qualification.

### 7. Release qualification and final audit

**Issues:** MOB-010/017 plus every open acceptance item. **Files/artifacts:** acceptance ledger, native configs, existing CI/Make gates, signed device build records, screenshots/recordings, performance traces.

- [ ] Run `make merge-approval-gate` plus explicitly owned native tests/typecheck/build/package checks. Run applicable browser geometry and race checks where shared web/runtime changes require them.
- [ ] Execute the complete workflow matrix on the same final source and installed iOS artifact. Record build identity, device/OS, fixture, steps, authoritative result and limitations.
- [ ] Qualify VoiceOver, large text/live resizing, reduced motion, light/dark, small devices/iPad landscape, text selection, keyboard/back gestures and process restart.
- [ ] Measure physical-device input/scroll latency, streaming load, long history and memory. Verify signing, clean install, update, credential/draft retention and physical networking.
- [ ] Audit every explicit requirement and catalog mapping against current evidence. Missing, historical-only or indirect evidence remains open. No release-complete claim based solely on simulator builds, green unit tests or screenshots.

**Exit:** all workflows and cross-cutting requirements have final-build evidence and no unresolved required acceptance. Only then may the active goal be marked complete.

## Immediate dispatch queue

1. Bot: integrate current main, preserve unrelated work, validate the result.
2. Luna medium A: current-source workflow/contract ledger and stale inventory correction.
3. Luna medium B: explicit native/package gate integration using existing tooling.
4. Luna medium C: lifecycle failure investigation with bounded source ownership; Bot owns simulator reproduction.
5. After those results: Bot designs the complete conversation journey; Luna workers implement reader continuity, independent decision regressions and SDK streaming recipes in parallel where files do not overlap.

This sequence supersedes the README's isolated-space-fix first priority. Existing backlog IDs and historical evidence stay intact; this plan supplies dependencies and release criteria, not a reduced scope.

## Continuation checkpoint: 6 September 2026

The next implementation work remains the native transcript preference
presentation/editor and the session pin/fork/delete menus. Their supporting
work is now committed:

- `625c59d4e`: independently packaged, revision-aware preference recipe with
  safe default reads, explicit owned-hub writes, conflict/lost-reply readback
  and no mutation replay. Package qualification passed 15 combined recipe
  tests outside the checkout. A direct read on the owned v4 hub returned both
  preference domains at revision 0; no settings mutation was issued.
- `e472f6c2e`: pin/section operations in the existing navigation controller,
  explicit uncertain-state reconciliation, and current-list refresh wiring.
  Ten tests cover deferred receipts/reads and scope changes. Native pin menus
  and durable uncertain state across controller disposal remain unfinished.
- `ee5707b18`: retained activity members, source tool intent and system event
  metadata for a pure native display projection. Seventy-eight projection
  tests and independent review passed; no preference editor/render wiring is
  implied by this metadata slice.
- Shared-mobile fixtures now supply v4 transcript boundaries and use current
  fragment reads/handshakes. Full shared mobile tests passed 2253 tests in 90
  files, plus Biome/typecheck. Native gate passed 402 tests plus typecheck.
  These are scoped integration checks, not final whole-repository certification.

Reader Dynamic Type acceptance is still open: a largest-to-normal text-size
transition visibly drifted despite a saved semantic anchor. Later instrumented
runs held position but did not establish the root cause. See the explicit
failure record in [reader continuity evidence](../../design/mobile/reader-continuity-evidence.md).
Temporary diagnostics were removed and the clean iOS Release bundle
`967d12c51c12ce35682492969f1eaf87ff84dc8c3446f6feca4c518379b2cf11`
was installed. The simulator is back at normal `large` text size.

The original unrelated Apple project and Info.plist modifications still match
the preserved initial patch. Android remains deferred by Jesse's iOS-only v1
decision. No push, merge or release publication has been performed.
