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

## Transcript preferences integration checkpoint

The mobile transcript editor, confirmed-config reader projection, and durable
hub-scoped drafts are now implemented. `cd85889d7` preserves proposal revisions
and uncertain checkpoints before writes, fences late replies by operation
identity, and requires an explicitly reviewed revision to rebase a conflict.

The native provider binds one preferences model to each negotiated connection.
The reader consumes the existing unfiltered conversation projection, preserves
activity member/attachment identity, and avoids older-page requests for an
anchor whose source is already loaded but hidden by preferences. Typed critical
notices stay outside collapsed diagnostic groups. Manual disclosure choices
retain the same source-scoped IDs.

`make test-native` passed 436 tests in 54 files plus TypeScript. Release
build/install/launch succeeded; the final bundle hash is
`04f4ea31620e28a43983a53e7ab1d4b58f238cabea6759ace32cb6313bcfb0b9`.
The owned-hub journey verified unsaved draft survival across app restart,
explicit save/readback, external conflict review without an implicit write,
all advanced setting flags, and restoration of the original hub config.
See [transcript preference evidence](../../design/mobile/transcript-preferences-evidence.md)
for artifact boundaries and screenshots.

Remaining next work includes session pin/section and selected-turn fork/delete
menus, native keybinding editing, further independent SDK recipes, and the
cross-cutting lifecycle/reader/release matrix. The Dynamic Type drift remains
an open failure. These scoped checks do not replace the final canonical merge
gate and requirement-by-requirement acceptance audit.

## Pinned section management checkpoint

`f08473f48` adds native catalog/member destinations and rename/delete with durable
name proposals, exact route restoration and shared operation recovery. The owned
iOS journey verified restart without replay, rename/readback, stale confirmation,
cancel/delete at maximum text size, retained session history, assignment, and
external deletion recovery. The final bundle is
`0dada2f69bedc5522b37b941222cc75f2b042901e03855aa3e5762323a8c378e`;
492 native tests, TypeScript and touched Biome checks pass.

See [organization evidence](../../design/mobile/organization-evidence.md#ios-pinned-section-management-checkpoint-2026-09-07)
for final artifacts and limits. Ordinary fork, ended-session deletion, durable
archive/favorite recovery, keybindings, further SDK recipes, the reader continuity
failure and the cross-cutting release matrix remain open. Android stays deferred
for iOS-only v1. The preserved unrelated Apple project/plist edits still match
their original patch.

## Ordinary fork checkpoint

The iOS selected-message fork is implemented in `ff3da051b`, with hub capability
and return-to-parent subscription fixes in `0b2d95109` and `be164c5ad`. The final
Release bundle is
`045b7da4af947e8f2ccb77b3b7b2b1aa8d1a524fd32679ac42cc221b45ed3827`.
Owned iOS and independent SDK reads verify the divergence boundary, preserved
parent history, editable unsent child draft, exact draft/destination restoration
after restart, and live parent updates after Back. Native tests pass 510 cases
plus TypeScript; shared package checks, full hub tests and scoped hub lint pass.

See [fork evidence](../../design/mobile/fork-evidence.md) for artifact boundaries,
screenshots and the remaining concurrent-source protocol limitation. Next is
ended-session deletion, followed by durable archive/favorite recovery. Native
keybindings, further SDK recipes, Dynamic Type reader drift and the full iOS
release matrix remain unfinished. Android remains deferred for v1. These scoped
results do not replace the final canonical merge gate or release acceptance.

## Ended-session deletion checkpoint

`29ac49032` adds native ended-session deletion with explicit confirmation, durable
target/results, receipt-aware readback and retained local drafts. `d2188a67d`
fixes the stale Sessions list found during acceptance: external changes now
refresh a focused list while keeping its active search. Final Release bundle:
`fe5e44502fe66d9a6f5e4fbd6a650e63eb12d4c3a69d9159d92c26da762d8e99`.

The owned iOS journeys verified cancel, stale confirmation, maximum text controls,
restart without replay, actual deletion, retained drafts and untouched original
reader/fork histories. The final source passes 540 native tests, TypeScript and
touched Biome checks. See [deletion evidence](../../design/mobile/session-deletion-evidence.md)
for the two artifact boundaries and exact observed results.

Durable archive/favorite recovery is next. Native keybindings, additional SDK
recipes, reader Dynamic Type drift, overlapping lifecycle failures and the full
iOS release matrix remain unfinished. Android remains preserved and deferred for
v1. No push, merge or publication has been performed.

## Archive and favorite recovery checkpoint

`60611f595` and `ef1cdf93e` add durable archive/favorite recovery to project
browsing, preserve project/filter/tier destinations, and require an explicit
read-only continuation when archive intent has no confirmed reply. Final Release:
`7f14a22312fa38916cb64b7700a10788a6cd6ef1d4cbf40c6fb952f78182f7a2`.

The final owned iOS journeys passed project pin/unpin/archive/unarchive, local
session archive/unarchive, restored filters/tiers, rejection of a stale native
menu and unresolved-request continuation at maximum text size. SDK/SQLite reads
confirmed no replay, preserved histories/drafts and an empty recovery journal.
The final native gate passes 553 tests plus TypeScript and touched Biome. See
[organization recovery evidence](../../design/mobile/organization-recovery-evidence.md) for source,
device, fixture and test boundaries; Luna reviewed supplied contracts/algorithms,
and root executed the gate and simulator qualification.

Next is native keybinding configuration. Additional SDK recipes, reader Dynamic
Type drift, overlapping lifecycle failures, full VoiceOver/iPad/device coverage,
signing/update qualification and the final canonical merge gate remain open.
Android sources are preserved and qualification remains deferred for iOS-only v1.

## Native shortcut settings checkpoint

`9ca6eeb71`, `cadf744a8`, and `d65d234f5` add hub web-shortcut configuration to
native settings, durable proposals/unknown-write recovery, explicit revision
review, saved form restoration, and iOS UnicodeSets compilation with authored
previews. Final Release bundle:
`b0d1e4067fda6e6e5b63ffa3290861f64d0b18658c6134addc42dd4d292cdb64`.

Owned iOS observations cover persisted proposals, stale comparison rejection,
maximum-text review controls, explicit save, Unbind, Restore default, unknown
rule retention, pattern validation and unsaved form restoration without send.
The initial conflict/max-text and final pattern/editor artifacts are separately
identified in [shortcut evidence](../../design/mobile/keybinding-evidence.md).
The final native gate passes 581 tests plus TypeScript. Compilation adds about
1 MB of bundled code; runtime performance and the documented V8 case-folding
discrepancy remain qualification limits. The owned hub ends at default shortcuts,
with an empty journal and all four retained draft hashes unchanged.

Next are additional SDK recovery recipes and the outstanding reader Dynamic
Type drift. Full VoiceOver/iPad/physical-device coverage, multi-hub lifecycle and
performance qualification, signing/install/update, protocol limitations already
recorded above, and the final canonical merge gate remain open. Android sources
remain preserved and deferred for iOS-only v1; voice/barge-in remains outside v1.

## SDK management recipe checkpoint

The packaged SDK now contains provider-instance and plugin-management examples
with explicit owned-hub writes, independent readback, preserved uncertain
outcomes and no mutation replay. All 52 example contract checks, the package
qualification gate and the frontend gate pass. Owned-hub execution verified the
provider create/edit/default/restore/remove sequence and restoration of the
original instance list. Plugin mutation execution still requires a disposable
marketplace fixture. See [SDK management evidence](../../design/mobile/sdk-management-evidence.md)
for the tarball identity and precise execution boundaries.

The current clean native Release reproduced the text-size reader drift again:
marker 09 at maximum size became marker 16 after returning to normal size,
while the persisted anchor remained marker 09. Root-cause tracing and repair
are next. Additional SDK recovery work, native qualification and the final
canonical merge gate remain open. iOS-only v1 scope is unchanged.

## Reader reflow recovery checkpoint

Event-order evidence identified the reproduced reader failure: an approximate
search marker survived a later exact restoration and prevented another search
when Dynamic Type reflow unmounted the anchor. The fix resets that search state
on a measured result, retaining the exact-layout guard and bounded retries.
The regression failed against the old behavior; all 583 native tests and
TypeScript now pass.

Clean Release bundle
`1a52905ae64b7ea081cc7850eae5d6848af7062cf32756dcd73caaed895c6d18`
passed the original message-anchor size round trip, restart restoration and a
second round trip anchored to the interruption notice. See [reader evidence](../../design/mobile/reader-continuity-evidence.md)
for the captured failure, causal trace, final artifact and before/after images.
Temporary probes were removed and all four retained draft hashes were verified.

Next are the remaining SDK acceptance/recovery recipes and the full reader
continuity matrix, followed by VoiceOver/iPad/physical-device, multi-hub,
performance, signing/install/update and final merge qualification. This scoped
repair does not complete iOS-only v1. Android and voice remain deferred as above.

## Git plugin and SDK decisions checkpoint

Direct owned-v4 acceptance now verifies native Git upgrade rejection, preserved
installed content, recovery to a new revision, and SDK-driven detail updates and
removal. Native and SDK marketplace refresh each fetched a changed Git commit;
cleanup restored the complete original marketplace registry and empty plugin
list. See [plugin evidence](../../design/mobile/plugins-evidence.md#direct-v4-git-upgrade-and-recovery).

Marketplace and sandbox approval recipes bring the packaged examples to 81
passing contract tests across seven files. The final package and frontend gates
pass. Live alias browsing exposed and repaired a recipe identity assumption.
Approval decisions retain full-card and instance preflight, one dispatch and
uncertain recovery; tool execution remains explicitly unverified. The SDK
catalog names 42/91 requests across 12 recipes, which measures presence only.

Next is actual scripted-provider approval/denial execution and further SDK
decision/recovery work. The broader reader, VoiceOver/iPad/physical-device,
multi-hub, performance, signing/install/update and final canonical merge gates
remain open. No branch was pushed, package published or release declared.
Unrelated Apple project/plist edits remain byte-for-byte preserved.

## Real sandbox decision checkpoint

The clean iPhone Release and packed SDK now each complete Allow and Deny through
an actual restricted `read_file` waiter on the direct owned v4 hub. Independent
provider sentinel observations, completed-turn events and transcript readback
confirm execution versus denial; no notification is injected. The SDK sends one
resolve request and continues to label its own acknowledgment execution-unverified.
The fixture provider registry is restored, five created sessions shut down,
and all four retained drafts and the original Apple patch remain unchanged.
See [decision evidence](../../design/mobile/approval-evidence.md#direct-v4-execution-through-native-and-packaged-sdk-decisions).

Next are structured question execution and its SDK recipe, followed by remaining
decision/queue/goal recovery and release qualification. Single-file approval
success does not close repeated grants, concurrent/replaced decisions,
disconnect/acknowledgment loss, background/death or the broader reader,
VoiceOver/iPad/physical-device, multi-hub, performance, signing/install/update
and canonical merge gates. iOS-only v1 remains the release scope.

## Shared question formatter checkpoint

The web, native and mobile consumers now use one pure answer formatter in the
protocol package, exposed through its public ESM/CommonJS entry point. The
mobile duplicate's unescaped question headers reproduced an extra answer line
and now pass the shared framing regression. Existing formatter behavior is
preserved; formatting does not validate or submit selections.

The package's external tarball consumer compiles readonly answer arrays and
executes the export from ESM and CommonJS. `make test-api-package` and
`make test-web` pass, as do all 583 native tests and native TypeScript, 91 focused
mobile tests, and mobile typecheck/boundary checks. Luna reviewed the source
delta and evidence. A current native rebuild and real multi-question execution
remain next, together with the guarded SDK question recipe. This checkpoint
does not establish new simulator or release acceptance.

## Direct v4 structured question checkpoint

The packaged question recipe now performs a complete pending-batch review,
explicit selection validation, one guarded answer request and receipt/readback
recovery without replay. All 95 recipe contract tests, package qualification
and canonical frontend gates pass. The recipe list contains 13 examples and
42/91 method names; presence remains distinct from acceptance.

Real native and external SDK cases delivered two structured answers, multiple
selections and a note through the actual scripted-provider question flow.
Native stop/launch preserved choices without sending. Manual testing exposed a
page-sheet keyboard coordinate mismatch; native iOS ScrollView insets fixed
the overlap, and the rebuilt Release's action was verified above the keyboard.
Native TypeScript and all 583 tests pass after that change. See [current evidence](../../design/mobile/real-question-harness-evidence.md#direct-v4-questions-restart-and-keyboard-qualification)
for exact hashes, diagnostic limits and cleanup. All fixture sessions are shut
down, the provider registry restored and earlier drafts/Apple edits preserved.

Next are remaining decision/queue/goal and SDK coverage, ordinary-draft and
uncertain-answer native journeys, the whole conversation review and final iOS
release qualification. Current question proof covers one two-question call,
not the entire decision matrix. Physical devices, iPad/VoiceOver, multi-hub,
reader/performance, signing/install/update and canonical merge gates remain open.

## Queue receipt and SDK checkpoint

The shared native conversation service now validates cancel, promote and drain
responses before the UI treats them as acknowledged. It checks the mutation,
thread and required instance identity, operation projection, queue entry IDs,
turn identity where applicable, and cancellation echo. Decoder failures follow
the existing unconfirmed-action path; the UI still refreshes without replaying.
The demonstration hub now emits the same queue receipt shape as the real v4
server: cancellation is removed, with the affected entry IDs, and promotion/
drain carry both queue IDs and a turn ID.

The packaged `queue.mjs` recipe provides read-only review and explicitly enabled
queue/cancel/promote/drain operations. It sends server instance and entry/revision
preconditions, validates receipts, reads back after success or failure, preserves
uncertainty and never retries automatically. Full queue text is exposed only in
an explicitly requested private review file. The coverage catalog now lists
14 recipes covering 46/91 method names and three notification names; this counts
recipe presence, not execution or outcome coverage.

Verification: six native receipt regression cases failed before the decoder
change. The focused conversation service suite now passes 107 tests. Broader
checks pass 2,267 mobile tests, 583 native tests plus TypeScript, mobile
TypeScript/boundaries, the canonical web gate, 105 SDK contracts across nine
files, and the independent package gate. The native gate initially exposed the
demo hub's invalid receipts; correcting their construction restored the existing
real-WebSocket queue integration test.

Luna medium implementation proposals and review informed this slice; the
coordinator corrected proposal mismatches against actual generated/server
contracts and executed all reported checks. Real direct-hub iOS queue actions,
uncertain native delivery, concurrent queue changes, goals/tasks/jobs and
release qualification remain open. The installed iOS app has not yet been
rebuilt with this queue service change. The two unrelated Apple project/plist
edits remain untouched and unstaged.

## Direct v4 queue checkpoint

Rebuilt iOS and the independently packed SDK now prove active-turn queue enqueue,
cancel, promote and drain through the real owned v4 hub. Provider request
sentinels and completed steering transcript entries confirm delivery; cancellation
and an ordinary native draft remain absent from provider input. Native restart
preserves the draft. The SDK returns uncertain after its successful cancellation
ACK is deliberately discarded, performs no replay, rejects stale recipe actions
before dispatch, and the server independently rejects stale entry/revision RPCs.
See [queue evidence](../../design/mobile/queue-evidence.md#direct-v4-ios-and-packaged-sdk-qualification--7-september)
and its exact-source receipt. Cleanup restores the provider registry, stops the
two fixture sessions/provider, and verifies the four pre-existing draft hashes.
Current iOS source is 8fb3e343c. Native uncertainty/socket loss/concurrency,
held queues, accessibility and release-wide work remain open. Goal SDK proposals
and native acknowledgment-validation findings are ready for the next slice.

## Goal acknowledgment and SDK checkpoint

The native service now requires a boolean `started` acknowledgment from
`goal/set`. Five malformed-response regressions failed before this check; after
the fix, the 114-test service suite passes. Two native tests exercise the real
SQLite draft path: malformed set retains the command as unconfirmed, malformed
clear retains the ordinary draft, and another attempt sends no additional RPC.

The packaged `goals.mjs` recipe supports read-only review, set and clear.
Mutations require explicit ownership opt-in, the reviewed instance and complete
goal state, and current goal capability. The recipe dispatches only the actual
`{ref, objective}` wire parameters once. Its reviewed-goal check is nonatomic:
the server has no goal revision, expected-instance or mutation-ID precondition
and can resume an exited daemon. A malformed/lost reply remains uncertain even
when readback changes; a boolean acknowledgment is not goal execution proof.

Validation passes: 2,274 mobile tests, 585 native tests plus TypeScript, mobile
TypeScript, the canonical web gate, 112 SDK contracts across ten files, and
independent package qualification. The catalog lists 15 recipes covering 47/91
method names and three notification names, measuring presence only. Luna medium
proposals and independent review informed the implementation; the coordinator
corrected proposal mismatches and ran all reported checks.

Direct iOS/SDK goal continuation and successful completion, current-build goal
edit/clear and native uncertain delivery remain open. The installed iOS bundle
still corresponds to the preceding queue-qualified `8fb3e343c` source; rebuild
for the goal acceptance run. Full tasks/jobs, multi-hub, accessibility, physical
device and release qualification remain active work.

## Direct v4 goal continuation checkpoint

Current iOS source `d48fe9471` and a fresh independent SDK package now prove
set/edit/clear plus successful autonomous goal continuation. Two owned sessions
each completed two turns, with one automatic continuation and a verified real
file write before terminal goal completion. Native clear/relaunch preserves its
ordinary draft; the five earlier checked drafts also retain their hashes.
The SDK observes valid true/false acknowledgments, retains uncertainty after a
deliberately discarded edit reply without replay, and blocks a stale reviewed
goal before dispatch. [Goal evidence](../../design/mobile/goal-controls.md#direct-v4-ios-and-sdk-continuation--7-september)
records exact source/hashes, native wire-observation limits, and completed cleanup.

This is a bounded simulator/SDK proof. Native fault injection, concurrent writers,
tasks/jobs, remaining SDK methods, full transcript polish, multi-hub, accessibility,
physical-device and distribution qualification remain open. Luna medium review
also identified acknowledgment information lost when an SDK mutation succeeds
but its follow-up read fails; fix that error classification next.

## SDK acknowledged-readback failure checkpoint

The shared recipe helper now throws `AcknowledgedReadbackError` when a validated
mutation reply is followed by a failed read. The error retains acknowledged
outcome, unverified execution, method and original cause. All seven affected CLIs
emit a bounded acknowledged/readback-unavailable diagnostic and still exit 1.
Uncertain writes and ordered dual errors retain their existing behavior, with
no replay. A regression reproduced the lost acknowledgment before the fix.
All 116 contracts across eleven files, independent package qualification and
the canonical web gate pass. Luna medium reviewed the fix; the coordinator ran
the checks. The earlier real goal proof remains tied to its recorded `d48fe9471`
bundle/package hashes rather than silently claiming this newer SDK source.
