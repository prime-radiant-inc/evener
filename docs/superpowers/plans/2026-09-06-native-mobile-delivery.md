# Native mobile delivery implementation plan

> **For agentic workers:** Use superpowers:subagent-driven-development for bounded implementation packages. Use superpowers:executing-plans for coordinated native verification. Jesse selected active subagent execution; do not ask him to choose it again.

**Goal:** Deliver a beautiful, intuitive, reliable, feature-complete native Evener app on iOS (iPhone and iPad), with multiple hubs, automated tests and manual native E2E, plus an independently usable protocol and API client library.

**Architecture:** Keep the shared Expo/React Native application in `mobile-native/`. Reuse the framework-independent AppWire client and validated shared services. Current web/server behavior defines capability; platform-specific interaction and layout belong in the native app.

**Tech stack:** Expo SDK 57, React Native 0.86, React Navigation, SecureStore, SQLite, TypeScript AppWire client, real isolated Evener hubs with scripted providers at the LLM boundary.

**Spec:** [Full coverage objective](../specs/2026-09-05-native-mobile-coverage.md), [product philosophy](../../design/mobile/philosophy.md), [style guide](../../design/mobile/style-guide.md), and [acceptance backlog](../../design/mobile/backlog.md).

The [current project status](../../design/mobile/status.md) and
[iOS v1 remaining work](../../design/mobile/ios-v1-remaining.md) summarize
verified progress and current gates. The dated checkpoints below preserve
implementation history.

## V1 platform scope

Jesse explicitly selected **iOS-only v1** during takeover continuation. Android
implementation, builds, device testing, lifecycle investigation and release
qualification are deferred. Preserve the existing Android source and evidence;
do not remove its support or bypass shared checks. This decision supersedes
both-platform requirements in the original handoff and historical studies.
Shared feature completeness, multi-hub correctness and the independent SDK
remain required. On 8 September Jesse paused iPad and accessibility work and
selected full iPhone functionality as the immediate milestone. Preserve those
requirements and evidence, but do not dispatch paused work or let it block the
functional milestone. The [active checklist](../../design/mobile/ios-v1-remaining.md)
supersedes iPad/accessibility sequencing below. Physical networking, performance,
signing and install/update remain separate release qualification work.

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
- [x] Run affected generation and repository/native checks after integration. The [8 September receipt](../../design/mobile/assets/2026-09-08-status-verification.json) records canonical gate and vet exit 0 at compiled source `fe403ee3a`. Repeat affected gates after source changes.
- [ ] Replace historical status assertions in the coverage inventory with source-verified implementation and qualification columns. Keep historical evidence dated and attributed.
- [x] Create the [acceptance ledger](../../design/mobile/acceptance.md) with one row per workflow: web/server source, native source, deterministic tests, real-daemon scenario, iOS evidence, Android evidence, source/build identity, remaining acceptance, owner. Missing evidence stays open.
- [x] Add explicit native test/typecheck and clean API-package checks to the existing gate ownership. Implemented in `d7075ce74` and corrected/qualified in `cab95068c`: `make test-native`, `make test-api-package`, merge gate and CI ownership. Current integrated gate evidence is recorded in the [status page](../../design/mobile/status.md#verification-record).

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
- [x] Implement persisted reader restoration by hub/session, item identity and within-item offset. `7944778e0` fixes virtualized measurement progress during cold restoration; repeated iPhone largest-text launches passed.
- [ ] Qualify the full pagination, streaming, image reflow and sheet-return combinations on iPhone/iPad without stealing the reader's place. Scoped cold-launch evidence does not close this matrix.
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

Run this lane alongside packages 1–5, not after them. The current inventory records 90/91 catalog methods across 34 recipes and handles all 36 notification names as catalog entries; [27/36 notification names have scoped producer/readback evidence across the audited receipts](../../design/mobile/sdk-notifications-evidence.md#queue-and-thread-close-producer-evidence). These are catalog and bounded evidence counts, not complete execution, failure, or outcome coverage.

- [ ] Map every supported/reserved method and notification from the generated catalog. Link each workflow to executable cases; account for reserved-method rejection separately from usable capabilities.
- [x] Add recipes for the supported method and notification catalog, including streaming/rejoin, decisions, management, credentials/plugins, trust and upgrade.
- [ ] Complete the catalog-derived success/failure/disconnect and producer outcome matrix; recipe presence does not close it.
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

This historical queue is superseded by the current status snapshot and the
remaining-work checklist. Do not dispatch the old v4-integration or stale
inventory work packages again. The canonical gate and separate vet now have a [passing durable receipt](../../design/mobile/assets/2026-09-08-status-verification.json). Current next work is to qualify the remaining iPhone/iPad
workflow and recovery cases on identity-matched artifacts; complete the SDK
success/failure/disconnect and notification producer matrix; then run the
accessibility, performance, physical-device and signing/update release matrix.
Keep implementation-present items (reader, pin/section, fork, keybindings,
plugins and hub settings) separate from their still-open qualification.

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

## Activity continuation checkpoint

Source verification found that the real server returns a continuation suffix,
but the native/web merge replaced the target session and lost its loaded prefix.
A second early return suppressed delegate child expansion at equal projection
revisions. Both paths now use the existing ordered ID merge. Regular root reads
still replace missing entries; continuation ownership and revision guards remain.
The internal merge no longer takes an unnecessary target ID, while both readers
retain their advertised-cursor and per-branch request/error checks.

One corrected native fixture and four new shared merge regressions failed before
the fix. The 22 focused frontend tests, all 585 native tests plus TypeScript and
the canonical frontend gate pass. Luna medium proposed and reviewed the repair;
the coordinator verified server/caller contracts, corrected test fixtures and
ran validation. No current-device nested-paging acceptance is claimed yet.

Next: real v4 activity/tasks/jobs and packaged SDK coverage, then remaining fault,
multi-hub, transcript, accessibility, physical-device and release qualification.
The installed iOS source remains goal-qualified `d48fe9471`; rebuild before its
next changed-code acceptance. The two unrelated Apple edits remain preserved.

## Saved goal and task workflow checkpoint — 7 September

`a35ad596f` preserves typed goal continuation notices and stable turn IDs in the
semantic transcript and all saved grouping paths. `0f95ef200` adds the validated
packaged task-list recipe. `6277c3f30` puts native task-control reminders behind
the existing disclosure control while keeping critical notices visible.

The owned direct v4 hub/daemon now runs `0f95ef200`; the latest installed iOS
Release app is `6277c3f30`, bundle SHA
`08b72d59e4429755ff1c7f27ddf194d83b94ceb53f7e27a57426ce9f0df552ef`.
The [combined receipt](../../design/mobile/assets/goal-task-v4-receipt.json)
records an independent SDK consumer, real task mutations and native updates,
goal continuation/file/completion, stable goal notice identities after shutdown
and relaunch, retained task details, and native disclosure open/close behavior.
Full model input remains durable. The owned provider is removed/stopped and all
six earlier drafts are unchanged. No old transcript was rewritten.

Validation passes: 592 native tests and TypeScript, 124 SDK contracts across
twelve files, independent tarball qualification, canonical web gate, full agent/
schema/transcript/apptranscript/appprojector suites, focused hub checks, vet and
the lint gate. Luna medium proposed implementations and reviewed semantics and
test boundaries; the coordinator integrated and executed the checks.

This is a bounded goal/task workflow checkpoint. Goal notice parity is not whole
transcript parity; live-only diagnostics differ. The SDK catalog has sixteen
recipes covering 48 of 91 method names, with outcome coverage still incomplete.
Next: real activity continuation paging and SDK jobs/output workflows, followed
by remaining fault, reader, multi-hub, accessibility, iPad, physical-device and
release qualification. iOS-only v1 scope continues; Android source is preserved.

## Activity and output checkpoint — 7 September

`7b48a7314` validates shared job-output byte windows and adds the packaged
single-page output recipe. `cb0dca5cc` repairs continuation summary counts and
preserves independently pending ancestor branches. The latter is installed on
iOS as a Release bundle with SHA
`22974d41d3b24501cb710c4f4da74eced60f7caaec5d0a769e7c709716e677ff`.

[Direct v4 acceptance](../../design/mobile/activity.md#direct-v4-ios-paging-and-output--7-september)
now covers the actual iOS root/child paging controls over a 5011-job persisted
fixture, output earlier/refresh/return, and independent packed SDK output reads.
The native-controller harness also asserts exact ordered identities, complete
counts, cursor ownership, stale-page no-ops and ordinary root refresh replacement.
The single-page SDK recipe reconstructs Unicode output under an external paging
harness; it does not introduce an automatic paging workflow. Fixture records do
not prove shell/delegate execution. The owned hub remains source `0f95ef200`
on direct port 54211, with no provider or production-state changes.

593 native tests plus TypeScript, 131 SDK contracts/thirteen files, isolated
package qualification, canonical web gate and all five browser guards pass.
Luna medium supplied bounded implementation/test proposals and independent
review; the coordinator integrated and ran verification. Six earlier drafts and
the unrelated Apple patch remain intact. The catalog now has seventeen recipes
covering 49 of 91 method names; notification and outcome coverage remains partial.

Next: packaged activity-tree traversal and complete delegate metadata, remaining
runtime/concurrent-writer faults, reader and multi-hub journeys, accessibility,
iPad/physical devices, performance and release gates. The output UI produced two
automation snapshot-settling timeouts; subsequent snapshots verified success,
but no performance conclusion follows. iOS-only v1 remains active; Android
qualification and interactive voice remain deferred.


## Shared activity traversal and native ANSI checkpoint

`e0cd5690f` moves the existing activity parser, merge and native reader into the
shared SDK and adds bounded, privacy-safe traversal. It also renders ANSI native
output and fixes a simulator-discovered Anser dependency mismatch by aligning
native/runtime tests with the explicitly declared parser version.

[Direct evidence](../../design/mobile/activity.md#sdk-traversal-and-native-ansi-output--7-september)
records complete three/four-page persisted activity reads by an independently
installed SDK, exact job ordering/counts, one-page partial and wrong-thread
outcomes, byte-exact ANSI output, and rebuilt iPhone dark/light rendering.
601 native tests and TypeScript, 141 SDK contracts, isolated package
qualification, canonical web gate and all five browser guards pass. Six drafts
and Jesse's Apple migration patch are preserved.

The catalog has eighteen recipes covering 50 of 91 method names. This is
presence/read evidence, not all outcomes or release qualification. Next: complete
delegate metadata and timing, then the remaining fault, reader, multi-hub,
accessibility and iOS release matrix. Android qualification remains deferred.


## Delegate detail implementation package — source 86226672a

The shared ActivityDelegate parser already preserves model/profile selection,
run timestamps and snapshot durations, usage, lifecycle/resumability, worktree,
warning/diagnostic and result/validity/limit data. Native ActivitySheet currently
renders only mandate/task, status/reason and the transcript action. Preserve
row identity/folds, server transcript refs and the existing hub/session lifetime.

1. Add small pure native presentation helpers with tests for timestamp-driven
   live versus terminal duration, future/invalid dates, frozen duration fallback,
   actual/requested model precedence, and presence-safe string/JSON packets.
   Do not infer absent timestamps/counts/booleans or equate an old outcome with
   a current run's status. Reuse existing elapsed/mandate formatting where valid.
2. Add a native delegate detail component: instruction preview with full
   disclosure; model/reasoning and timing; separate expandable usage/worktree,
   warnings/diagnostics, latest report and structured result with validity;
   lifecycle/resume/budget/monitoring facts in a secondary details section.
   Zero, false and explicit JSON null remain visible when supplied. Exact paths
   and structured data are selectable. No invented stop/send/resume RPC buttons.
3. Only mounted live details tick, while connected and foregrounded. Stop the
   timer on unmount/background/disconnect; resume from current wall time, never
   advance a server-only snapshot duration. Keep model formatting pure with an
   explicit now value for deterministic tests. Native geometry/interaction
   verifies UI rather than string snapshots or mocked native renderer stacks.
4. Expand the independent client guide with activity ownership, continuation,
   metadata presence and stale/latest-report semantics from actual types and
   shared implementation. The existing packaged traversal remains the SDK lane.
5. Root integrates, independently reviews and runs native/package/web checks as
   affected, then builds iPhone Release. A new direct-hub delegate fixture checks
   detail disclosures, falsy/null data, live/terminal clocks and return to parent.
   State clearly whether evidence is projection-only or real execution.

Luna medium workers own presentation helpers/tests and the client-guide section
independently; Bot owns native composition/integration and serialized simulator
and hub operations. Preserve all six drafts and the unrelated Apple patch.

### Direct-wire usage correction — source 71750ee63

The iPhone fixture exposed a parser mismatch: EvenerUsage omits zero-valued
numeric fields, while parseUsage required both inputTokens and outputTokens.
Confirm raw versus parsed delegate rows, reproduce output-only and other omitted
zero combinations in parser tests, and normalize the present usage object's
omitted pair members to zero while rejecting malformed supplied counts. Keep an
absent usage object absent. This matches the current Go wire contract and the
existing token-accounting interpretation; it does not add a compatibility path.
Luna owns the parser/tests; Bot owns integration, guide clarification, fresh
package and iPhone builds, and repeating the direct fixture acceptance.

### Delegate detail checkpoint — ef12d750e

Implemented in 71750ee63, with current-wire usage repair in ef12d750e. The
independent SDK and rebuilt iPhone retain all three fixture delegates. Native
disclosures preserve model/timing, zero usage/ahead, false exhaustion resume and
JSON null/string distinctions; child navigation returns to its parent on the
same hub. See [acceptance and limits](../../design/mobile/activity.md#delegate-details-and-current-wire-usage--7-september).

610 native tests plus TS, canonical frontend, five browser guards and external
package qualification pass. Six drafts and the unrelated Apple patch are intact.
This is projection evidence, not actual delegate execution or timer fault
qualification. Next prioritize concurrent/stale questions and decisions, uncertain
delivery and reconnect, then reader continuity and multi-hub pending operations.
iOS release, accessibility, physical-device and performance gates remain open;
the global goal stays active, with Android qualification deferred.

## Running-work recovery package — source 490b761a7

Audit question and approval ownership, stale decisions, concurrent batches and
lost replies against the current services, native sheets and SDK workflows.
Luna medium independently reviews each decision family, identifying concrete
contract failures and missing behavioral tests before edits. Bot inspects the
existing real scripted-provider harness and prepares a fresh owned session.

Reproduce confirmed defects with deterministic tests at the real service/client
boundary, apply minimal fixes, and preserve drafts and pending selections.
Then verify a real decision resolved from a second client while iOS is open,
including stale UI prevention, authoritative readback and the next reachable
pending decision. Do not claim uncertainty/reconnect qualification from a mock
receipt alone or replay an uncertain mutation automatically. Record actual
execution versus projection evidence, keep provider configuration cleanup
explicit, and preserve the unrelated Apple patch and six prior drafts.

### Running-work recovery checkpoint — approval fix 04dd97a46

The real second-client question handoff passed: the SDK answered the first batch,
the iPhone replaced the open sheet with the next question and cleared the old
answer note, and the iPhone answered the next batch once. Provider ancestry checks
and independent hub reads confirm both answer rounds. The stale SDK review was
rejected before another dispatch. Source, artifacts, cleanup and limits are in
[question recovery evidence](../../design/mobile/real-question-harness-evidence.md#second-client-question-handoff--7-september).

The approval audit reproduced a lost-acknowledgment retry defect. The native
controller and sheet now require successful current-session readback before
another decision after uncertainty, reject malformed receipts and serialize
recovery. Seventeen focused approval tests, all 620 native tests, TypeScript,
and the iPhone Release build pass. See [approval recovery evidence](../../design/mobile/approval-evidence.md#uncertain-decision-recovery--7-september)
for the actual regression and controller-lifetime limits. Luna medium supplied
implementation and bounded review proposals; Bot verified causal findings,
integrated the final code and ran the actual SDK/hub/iPhone journeys.

Native lost-acknowledgment/reconnect acceptance, ordinary-composer interaction,
simultaneous decisions, reader continuity, multi-hub operations and release gates
remain open. The SDK has eighteen recipes covering 50 of 91 catalog method names
and three notification names, with 141 behavioral contracts; these counts do not
qualify every workflow outcome. iOS-only v1 and the global goal remain active.

## SDK session settings and native question drafts — source ab1e39fff

Keep the next SDK work aligned with actual client workflows: a read-only command
catalog recipe and a reviewed session-settings recipe for model, reasoning and
vision choices. Luna medium implementers own disjoint example/contract files;
Bot owns catalog/qualification registration, documentation, source review and
outside-checkout tests. Read actual server contracts, preserve raw presence and
fallback semantics, validate receipts and readback, and never replay uncertain
mutations. Mutation examples require their existing explicit owned-hub opt-in.

In parallel, qualify ordinary composer draft interaction with native answers:
create a fresh owned real ask_user session, retain a nonempty ordinary draft,
select answers and a note, stop/launch before submission, verify no auto-send,
then submit consecutive answers and confirm the ordinary draft survives both
accepted answers and another launch. Observe actual provider ancestry and hub
input rows, not only local persistence. Preserve the six pre-existing drafts,
clean up the owned session/provider, and retain source/build provenance. Do not
use forwarding/fault proxies or claim lost-acknowledgment acceptance from this
process-restart journey.

### SDK and ordinary-draft checkpoint — cd975b991

The command and session-settings recipes are implemented, source-reviewed and
qualified from an independent tarball. Four command and twelve settings
contracts raise the packaged suite to 157. Actual owned-hub acceptance confirms
seven settings mutations, stale-review rejection before dispatch, seven change
notifications, exact settings restoration, and command counts 0/1/0 around an
owned temporary file. The canonical frontend gate, all browser guards and
outside-package qualification pass. See [SDK evidence](../../design/mobile/sdk-management-evidence.md#packaged-session-settings-and-command-catalog--7-september).

Actual native question acceptance now includes a nonempty ordinary composer
draft, restored first answer/note after stop/launch, two native answer sends,
provider ancestry checks and unchanged ordinary draft after a second launch.
The hub contains exactly the initial prompt and two answers. Six pre-existing
drafts, the new draft and the unrelated Apple patch are preserved. The owned
sessions/provider and temporary command file are cleaned up. See [native draft evidence](../../design/mobile/real-question-harness-evidence.md#ordinary-composer-draft-and-native-answers--7-september).

An independent review proposal would have reclassified an acknowledged setter
as uncertain when later readback differed. Ruling: retain the known ACK and
return actual readback; clamping and concurrent changes are valid outcomes,
and neither proves failed delivery. A behavioral contract now checks unchanged
readback explicitly. No asynchronous close change is needed: client.close is
synchronous. Required thread identity remains required, with optional source
metadata preserved separately.

Next qualify reader continuity across live/persisted turn identity changes,
then actual native uncertain-delivery/reconnect and multi-hub pending workflows.
SDK creation, credentials, upgrades and full notification/outcome coverage remain
open. iOS-only v1, physical/accessibility/performance and release gates remain
unfinished; the full goal stays active.


## Reader geometry checkpoint — source 5329dbe47

Reproduced and repaired obsolete-key geometry after exact-position anchor
resolution. The native helper and screen now use the resolved row's current
measurement; sixteen reader tests, all 621 native tests, TypeScript and iPhone
Release pass. Settled older-page navigation/restart preserves the retained
marker-09 boundary, seven drafts and the unrelated Apple patch. Luna medium
reviewed the supplied algorithm; root integrated and verified source because
agent workspace access was inconsistent. See reader-continuity-evidence.md.

The separate lifecycle investigation found a concrete live/persisted mismatch:
the saved question anchor is turn_m1:1:0, whereas persisted ENVIRONMENT occupies
(1,0) and shifts that user to turn_m1:2:0. Turn IDs remain, coordinates do not.
agent.maybeAppendEnvironmentContext persists a standalone environment turn but
has no corresponding live projector event. Next reproduce the event-to-file
projection divergence with a real Session and scripted provider, then repair
that source contract. Do not add fuzzy reader matches or classify unrelated
position matches as successful recovery. SDK bounded anchor traversal needs no
new seek protocol. Completed-question recaps also remain unfinished. Goal stays
active with iOS-only v1 and deferred Android release qualification.


## Environment identity checkpoint — source ed43c914e

The real Session/scripted-provider regression reproduced an environment turn
missing from the live projection, shifting the first user from entry 1 live to
entry 2 persisted. The session now assigns the environment an existing stable
turn ID and emits a typed event; the projector keeps it separate from the
reserved/active user turn. Full agent/events/projector/transcript/server tests,
focused race checks, tagged event fuzz and full make lint pass. Luna medium
reviewed proposals and event coverage; Bot integrated and verified actual source.

The independently installed SDK completed two scripted turns on a separate
isolated hub, then verified all environment/user/assistant keys, positions and
turn IDs after actual notLoaded shutdown. The driver now waits for that
authoritative ended state, since shutdown ACK only dispatches the operation.
The initial premature assertion is excluded from acceptance. Metadata and
source/binary/driver provenance are archived in reader-continuity-evidence.md.
The owned instance, hub and provider were cleaned up; original hub, seven native
drafts and unrelated Apple changes are preserved. Package qualification passes
again. Native lifecycle viewport, historical anchors, uncertain-delivery and
multi-hub acceptance remain open. iOS-only v1 and the full goal stay active.


## Next bounded packages — source 5361555a6

1. Restore useful completed-question recaps. In shared conversation projection,
   preserve any authoritative tool description; when absent and valid ask_user
   question arguments exist, derive a concise description from their headers.
   Keep completed/errored calls as non-actionable activity and retain arguments,
   outputs and errors for disclosure. Do not infer selected answers from later
   prose. Behavioral tests cover authored description, multiple headers, malformed
   arguments, answered/failed status and unchanged pending question controls.
   Luna medium owns project.ts/project.test.ts; Bot owns native integration.
2. Produce a complete source-backed SDK support inventory for all current methods
   and notifications, distinguishing router scope/reserved methods, recipe
   presence and actual acceptance. Identify missing lifecycle and recovery docs
   explicitly. Luna medium owns the dedicated protocol-coverage document.
3. Bot qualifies the newly repaired environment identity on the installed native
   iPhone reader using a fresh owned direct hub and scripted provider. Capture
   saved anchor and settled viewport before and after actual ended-state reads
   and process restart. Preserve existing drafts and restore the original hub.


## Native lifecycle and question recap checkpoint — source 5361555a6

A separate real hub/scripted provider produced 24 turns for actual iPhone
reading. After loading older history and dragging to the first assistant, the
reader retained key turn_m1:2:1 and offset 143.33333333333331 across shutdown,
process restart and final bundle reinstall. All 49 live/persisted identities
matched through independent SDK paged reads. Owned profile/instance/processes
were cleaned up and original hub, seven drafts and Apple patch preserved.
See reader-continuity-evidence.md for source/artifact limits.

Completed question activities derive missing descriptions from validated
headers, preserve authored descriptions and all details, and never infer answers.
RED/green regressions, 88 shared projection tests, 621 native tests, TypeScript
and Release pass; the installed app displays both completed question recaps
with its ordinary draft unchanged. Luna medium proposed/reviewed this code and
authored protocol-coverage.md; Bot verified actual sources and results. The
inventory matches all 91 method scopes and 36 notification names exactly once,
with valid local evidence links and reserved support distinguished from required
v1 functionality. This does not qualify every method.

Next: native multi-hub pending/uncertain operations and keyboard-open hub-form
focus. Simulator HID modifiers and offscreen targets were unreliable during
setup; native paste completed it, but focus/scroll behavior needs a separate
observed journey. Historical reader anchors, ongoing streaming/image reflow and
iPad/physical/accessibility/release gates remain open. iOS-only v1 and the full
goal stay active.

## Hub selection intent and gate follow-up — source e18c50c3e

The full merge gate at 7d7e49535 passed lint, build, the other Go packages and
web checks, but failed the root source-citation audit because two question
scenarios still referenced the deleted answer-composition module. Luna repaired
the citations; Bot verified both source-path and symbol audits and committed
e18c50c3e. All five browser guards and independent SDK package qualification
also pass. These results do not turn the failed full gate into a pass.

The next native fix preserves the latest hub selection when secure storage is
slow. Save completion currently selects its hub unconditionally, even after a
newer selection or disconnect, and a token update uses a captured selection when
deciding which connection to retry. Extract the existing async orchestration into
a small selection owner, exercise it with real HubProfiles and a deferred
SecureStorage boundary, and gate both selection and post-save navigation on the
current intent. Restoration and removal must use the same ownership. Do not add
a renderer dependency or disable unrelated hub actions. Luna medium implements
and reviews; Bot verifies integration, the native build and the full gate.

An installed-iPhone blank-form check exposed every input above the keyboard after
scrolling and retained focus during origin/token selection. It did not reproduce
the earlier automation problem. Evidence and its limits are in multiple-hubs.md;
seven drafts and the unrelated Apple patch remain preserved.

### Selection integration checkpoint

Luna implemented the selection owner and provider/form wiring. Bot expanded the
deferred-storage tests, observed three roster/removal failures, and fixed read
ordering plus the confirmed-removal fallback. A further RED/green regression
preserves reconnect after committed credentials when readback fails. Eighteen
selection cases plus the existing connection/removal cases pass (29 focused).
The full native gate passed 638 tests and TypeScript before the final extra
switch-away-and-back test variant; that variant also passes the focused suite.

The rebuilt iPhone app restored the original conversation. Native save/connect,
cleared form fields, selected-profile removal and return to the original hub
passed, with seven drafts preserved. The temporary profile used the same owned
direct hub, so this is wiring evidence rather than distinct-hub or delayed-native
operation acceptance. See multiple-hubs.md for artifacts and source bundle hash.
The full gate will be rerun on the committed integration; broader iOS release,
multi-hub uncertain operations and complete SDK workflow qualification remain
open. Android release qualification stays deferred.

### Verified integration — b9e1b8926

The complete canonical merge gate exited zero: lint/build, full root and other
module/web tests, 639 native tests, native TypeScript and independent package
qualification. `make vet` exited zero separately. Five browser guards passed
before the native-only selection integration. Independent review found no
remaining actionable issue in this change. The acceptance ledger now links the
scoped hub/SDK evidence and explicitly applies iOS-only release requirements.
Its updates are documentation only. The original hub, seven drafts and unrelated
Apple patch remain preserved. The full project goal remains active; the release
and workflow gaps above are still open.

### Credential workflow qualification

The credential recipe is being added for auth list/status, API-key set/clear,
credential-JSON set and logout. Its independent package contracts and documented
readback semantics still need integration. Browser/device OAuth, provider
execution, continuous reconnect recovery and the remaining notification paths
remain separate qualification work; catalog recipe counts do not imply complete
workflow coverage.

Luna's auth review found two server contract defects while preparing this work:
device polling did not bind a flow to its provider instance, and non-Codex logout
reported removal even without a stored credential. The fixes reject mismatched
flows before polling and derive removal from actual stored-file presence.
Regression tests preserve the original flow through pending and authorization,
exclude credentials under the wrong instance, and cover absent, environment-only,
stored and environment-backed logout cases. Bot independently ran the complete
`TestAuth_` suite successfully; a separate Luna review found no actionable issue.
Fresh external package qualification also passed. The unpublished SDK remains
pre-release; these checks do not close iOS release acceptance or SDK workflow gaps.


### Stored credential recipe acceptance

The six-method credential recipe now requires a complete reviewed auth snapshot,
handles the real null-list response, validates known fields while preserving
future data, and emits only validated top-level summary booleans. Root review
caught optional-review and nil-list gaps, then a nested future-field privacy
issue; Luna repaired each with RED/green regressions and a separate Luna review
confirmed all three fixes. Twelve new contracts bring the outside-package suite
to 169 tests. Independent package qualification, `make test-web`, targeted Biome
and the secret scan passed. The cookbook covers 60/91 method names in 21 recipes.

A fresh independent tarball ran all credential methods on an isolated authenticated
hub built from 3c5feb442. Twenty-one checks passed, including actual stored-key/JSON
changes, environment/ADC preservation, stale-review refusal, malformed-JSON
rejection, CLI output and eleven updates seen by an independent observer. The
first observer callback was incorrectly authored; its notification evidence was
excluded and the corrected driver passed. See sdk-management-evidence.md for
hashes, request-count scope and the archived driver. Owned instance/credential/ADC
state was cleaned up and the hub exited zero; original hub, seven drafts and
unrelated Apple changes remain preserved. This closes the bounded stored-credential
recipe package, not OAuth, continuous recovery or iOS release acceptance.

The complete canonical merge gate and separate vet passed at 700873dc9, including all Go modules/web, 639 native tests and TypeScript, and external SDK qualification. Acceptance documentation now corrects the stale no-preference-screen claim without treating dated feature artifacts as a current release matrix. The worktree retains only the two unrelated Apple edits after the documentation checkpoint. Next implementation/qualification remains the distinct-hub pending/uncertain native journey and broader iOS release matrix.


## Direct two-hub iOS acceptance — 7 September 2026

Progress: rebuilt `4bd05e280` iPhone Release (bundle SHA
`6a5c839fca5e79be53091c43773102cfa21ddca390dbeb5958499ed7ffd2c8df`).
Two direct isolated hubs from `700873dc9` preserve the same copied session ref
and instance ID. Native A/B profile creation with distinct credentials, separate
ordinary drafts, a held A model turn completing while viewing B, B background
and cold launch, A real process restart, A draft/transcript restoration and
scoped profile removal passed. Independent installed SDK reads verify provider
markers, native input routing and no draft submission. Details and hashes are in
`docs/design/mobile/multiple-hubs.md` and its `ios-twins-20260907.json` receipt.

Luna medium implemented/tested the scripted LLM fixture and reviewed binding
paths; Bot integrated harness fixes and ran native/SDK acceptance. No proxy or
lost-RPC-reply qualification is claimed. Both owned sessions and instances were
cleaned up; all four fixture processes exited zero. The original v4 acceptance
conversation and all seven drafts are restored/verified. Two unrelated Apple
file diffs remain byte-identical and unstaged. Android release work is deferred
for iOS-only v1. The overall delivery goal remains active: credential rotation,
native pending-RPC/uncertain-write cases and the complete accessibility/device/
signing/update matrix remain open. No push, merge or publication.

## OAuth recovery and SDK steering checkpoint — 7 September 2026

Progress: native source `759e7b10f` was rebuilt as iPhone Release and qualified
against fresh direct hub auth handlers with a scripted external OAuth service.
Held device and browser exchanges completed after the app's actual connection
closed; same-process foreground and read-only credential status recovery
passed. A separate restart and cancel-then-switch passed. The receipt in
`docs/design/mobile/assets/ios-oauth-recovery-receipt.json` records exact scope,
native identity and cleanup. All seven original drafts, the original hub and
conversation, and the unrelated Apple patch remain preserved.

SDK source `5429db421` adds direct steering and drain composer input. Luna
medium implemented/tested the recipes, prepared the isolated runtime and
reviewed evidence; Bot integrated corrections, checked the actual transcript
and reviewed artifact boundaries. Thirteen queue contracts and independent
package qualification passed. A fresh real hub with a scripted model produced
correlated receipts and transcript entries for steer and appended drain text.
The cookbook now contains 22 recipes covering 65/91 method names and 3/36
notification names. Remaining catalog workflows, broader reconnect recovery,
real-account OAuth and the iOS device/accessibility/performance/signing matrix
remain open. Android release qualification remains deferred beyond iOS v1.

`make merge-approval-gate` and `make vet` exited zero for the code committed
as `5429db421`, including all Go modules, web, 663 native tests/73 files,
native TypeScript and external package qualification. Only acceptance
documentation changed after this code checkpoint. Runtime fixtures are stopped
and their owned credential files removed. No push, merge or publication.

## Native upgrade and SDK lineage checkpoint — 7 September 2026

Root qualified actual native installation and recovery on iPhone Release source
`4f630af16`. The disposable fixture ran real self-update with a scripted download
boundary and isolated install prefix: failure, read-only refresh, deliberate
review/rearm, canceled held download, cold-launch recovery, successful third
attempt, installed byte/symlink verification, then a real hub started from the
installed binary on the same address. Native Reconnect and independent SDK
readback agreed on running commit `bb044658d`. Source/bundle/backend identities
and limits are in assets/ios-upgrade-receipt.json. Fixture exited zero, both owned
processes/listeners and tokens were removed, original hub/conversation restored,
seven drafts verified and Apple patch preserved.

Commit `ccdde59b9` adds session-lineage and maintenance-check recipes: resume,
fork, transcript targets, preview, ping, credential testing and plugin checks.
Root repaired worker implementation defects with behavioral regressions for
unknown fork children, acknowledged child readback failure, omitted ended-session
instances, captured inputs and deliberate external probes. The server now emits
an empty preview array according to its generated contract; a wire JSON test
reproduced the prior null and passes after the fix. Twenty-five SDK contracts
and focused Go preview tests pass. The canonical integration gate and separate
vet exited zero at `ccdde59b9`: lint, build, all Go modules and web, 671 native
tests/73 files, native TypeScript and external SDK package qualification.
Cookbook presence is 83/91 methods in 26 recipes, notifications 3/36.

Root's decoded API-log audit rejected the prior SDK compaction completion
claim. Its zero-tool request generated the session name (response schema name),
followed by two tool-capable rounds; no compaction summary was proved. The
corrected receipt documents acknowledgment only. Next bounded work: qualify
actual compaction completion on a fresh owned session and real scripted provider;
then actual resume/fork/preview/maintenance recipes, remaining seven supported
method recipes, notification/streaming recovery, and the outstanding native
workflow/iPad/physical/accessibility/performance/signing release matrix.
Android release qualification remains deferred beyond the approved iOS-only v1.


## iPad settings, branding and SDK warning checkpoint — 8 September

Three Luna medium lanes prepared settings fixtures, SDK producer qualification
and branding/release reconciliation; Bot reviewed the implementation, operated
the native iPad and independently checked raw receipts and cleanup.

- `2952b7ff6` qualifies a real content-filter `warning` through the packaged SDK,
  raising distinct actual notification producer/readback coverage to 15/36.
- `2e37bca74` fixes a duplicate settings conflict notice discovered during the
  real second-client stale-save journey. Project paths, MCP configuration,
  environment, fallback selection, explicit empty and inheritance were saved
  and independently read back; scope is in the iPad settings evidence.
- `4bf389d58` adds the production Evener icon and launch mark, with actual iPad
  SpringBoard/cold-launch evidence. Native tests (678/74 plus TypeScript), the
  final Release build and secret scanning passed.
- The corrected encoded-image limit rejected a source JPEG below 8 MiB after
  conversion, retained the later valid image, and restored that draft after
  process restart. The independent expanded PNG is a reference only; the
  actual native encoded byte count is not asserted.

Owned fixtures, profiles and drafts were cleaned; two photos imported for this
case were moved to Recently Deleted, preserving all baseline images. Bot
rechecked the original seven iPhone draft records, original hub and unrelated
Apple patch. Android remains preserved and deferred beyond iOS-only v1.

Next: remaining native recovery/reader and workflow cases on current iPhone and
iPad artifacts, global/settings and accessibility matrices, independent SDK
outcome coverage, measured performance, then physical-device/signing/update and
final repository/release qualification. These scoped passes do not close those
release requirements.

## Reader recovery, accessibility and SDK checkpoint — 8 September 2026

Three Luna medium lanes handled implementation, independent testing and evidence;
root integrated changes, operated the iPad simulator and verified raw receipts.

- `4256cd00a` adds six actual SDK notification producers, bringing this evidence
  series to 21/36 distinct names. Outcome and reconnect/replay qualification is
  still incomplete; the SDK remains pre-release.
- `6dec4867c` exposes launch setting values to accessibility and fixes clipped
  iPad headings at the largest text size. The original iPhone received the
  branded artifact with all seven baseline drafts preserved.
- `0493bd9fb`, `39d109036` and `66e55d887` preserve stable historical items and
  caller-owned cursors through overlapping projection reads. Final real provider
  503/SSE retry at turn m70, native cold launch and largest text retain the
  complete m4 reader anchor at offset 106. Endpoint evidence is scoped in
  [the reader receipt](../../design/mobile/ipad-reader-recovery-evidence.md).
- `7b954fe90` fixes Latest using actual native content extent, verified at normal
  and largest text sizes. `517f70ae3` removes reader anchors with their hub;
  four storage/isolation regressions pass, with its fresh native removal journey
  still open. Final native gate: 682 tests/74 files plus TypeScript. Shared
  service/store suite: 392 tests; mobile Biome/TypeScript checks pass.

The final Release removal build starts with empty fixture state. Native profile
and drafts were removed through the app; the old orphaned anchor was explicitly
cleaned during test teardown and is not counted as product removal proof. Owned
reader processes/listeners, tokens and logs are gone. Original hub, seven iPhone
drafts and unrelated Apple patch remain intact. No push, merge or publication.

Next: actual native removal and remaining recovery/workflow combinations,
rich-content reader and global/settings matrix, remaining SDK producer/outcome
cases, VoiceOver and measured performance, physical iPhone/iPad LAN/pairing,
signing/distribution/update, then final repository and requirement-by-requirement
release qualification. iOS-only v1 remains approved; Android is preserved and
its release qualification deferred. These passes do not close the release goal.

## Current status snapshot — 8 September 2026

The [current native status snapshot](../../design/mobile/status.md) is the
canonical summary. This plan remains the dependency-ordered implementation
and qualification record; dated checkpoints are provenance, not a
replacement for current evidence.

Jesse's approved v1 scope is iOS only. Android implementation and evidence are
preserved, but Android release qualification is deferred. Voice/barge-in is
outside v1. The current reader source `7944778e0` has repeated largest-text
cold-launch proof of the exact saved anchor and verified hub-scoped anchor
removal, with 685 native tests plus TypeScript. The reader result is scoped;
rich streaming/image combinations, wider iPad reader coverage, VoiceOver,
physical devices, performance and signing/install/update remain open.

The production shutdown/runtime correction is `4f3e824ac`; exact real-SDK
`thread/closed` qualification is current at `3284d6ac5`. Close failures tied to
`d2d5eedf9` came from stale binaries and are historical. The current compiled
test fix is `fe403ee3a`. Preserve these source/build boundaries in future
receipts and do not turn a scoped run into a release claim.

The SDK catalog has 91 methods; recipes cover the 90 supported methods and
all 36 notification names. One method is reserved and unsupported. These counts
measure recipe/catalog presence; qualified outcomes, failures, disconnects,
reconnect and replay remain narrower, so whole-SDK readiness is still open.
Current source inspection finds goals, tasks, activity, plugins and hub
settings operations wired. Task mutations are not advertised, and native job
output is selectable. Track their remaining work as separate implementation
and workflow-qualification items.

The launch-settings path gaps are stale where the [iPad launch-settings
evidence](../../design/mobile/ipad-launch-settings-evidence.md) records
successful path/MCP/environment/fallback validation, conflict handling and
readback. Its remaining global, two-hub, fault, accessibility, Dynamic Type,
physical-device, performance and signed-update limits still apply.

The [status snapshot](../../design/mobile/status.md#verification-record) records
the current canonical gate, separate vet result and exact source identity.
The unrelated Apple changes remain preserved.
