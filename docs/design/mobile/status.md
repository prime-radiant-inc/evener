# Native mobile project status

Updated 8 September 2026. Owner: Bot; product direction: Jesse.

**The native app has substantial implementation and verified end-to-end slices. iOS v1 is not release-ready.** The execution order is **Usable → Useful → Good**: first a dependable daily conversation loop, then full iPhone functionality, then fluency, performance and delivery quality. Ordinary lifecycle recovery and data preservation belong in the first milestone. Jesse paused iPad and accessibility work on 8 September; those items are retained for later resumption. Performance, physical-device and distribution qualification remain separate from feature coverage, and the complete SDK outcome matrix remains project work. Passing a build or finding a screen in source does not close a workflow.

## Scope and architecture

- **V1 is iOS-only; active work now targets iPhone.** iPad and accessibility work are paused at Jesse's request, not completed or removed. Android source, build instructions and historical evidence are preserved; Android qualification and its unresolved ANR investigation are deferred beyond v1. Voice/barge-in is outside v1.
- The app remains Expo SDK 57 / React Native in `mobile-native/`, with React Navigation, SecureStore and SQLite. It reuses the framework-independent TypeScript AppWire client and selected shared services/state. The old Tauri UI is not the product reference.
- Current web behavior, server contracts and Jesse's explicit requests define capabilities. The AppWire v4 migration is implemented, including item-based transcript paging, navigation representation v2 and current provider/settings contracts.
- The authoritative worktree is `live-concepts-plan2-integrate` under the external Evener worktree directory named in the [takeover handoff](../../superpowers/handoffs/2026-09-06-native-mobile-takeover.md). The default checkout is not the implementation workspace.
- Routine choices and parallel Luna-medium implementation/testing are authorized. A coordinator owns integration and artifact identity; separate workers can own functional areas, independent fixtures and SDK checks without sharing mutations. Do not assign new iPad or accessibility work while paused.

The [detailed implementation plan](../../superpowers/plans/2026-09-08-iphone-usable-useful-good.md) governs task order, dependencies, worker ownership and milestone exits. This page is the current summary. The [acceptance ledger](acceptance.md) defines each workflow's evidence boundary; the [remaining-work checklist](ios-v1-remaining.md) defines execution order. The [backlog](backlog.md) retains issue IDs and feedback. Dated feature receipts and the original coverage spec remain historical evidence, not a competing current status.

## Implementation and acceptance by area

This table retains all outstanding delivery work. iPad and accessibility entries are paused and do not block the current iPhone functionality milestone. Historical device evidence remains valid within its recorded scope.

| Area | Implemented and recorded evidence | Remaining for iOS v1 |
| --- | --- | --- |
| Hub identity and lifecycle | Secure profiles, hub switching, hub-scoped drafts, reconnect, profile removal; dated real two-hub iPhone and iPad journeys | Current-artifact credential rotation, uncertain writes, process death, overlapping operations, physical LAN/pairing and lifecycle stress |
| Discovery and organization | Search, paged projects/sessions, normalized navigation, archive/favorite, pins/sections and return routes | Current-artifact full management journey, invalidation/fault combinations, nested sessions and representative-data latency |
| Conversation reader | Item paging/fragments, stable identities, live merge, rich Markdown/images, saved item/offset and Latest; repeated largest-text iPhone cold restoration now verified | Streaming/image/paging combinations, authenticated image recovery, current-artifact iPad reader recovery, VoiceOver and long-transcript performance |
| Composer and durable drafts | Send/steer/queue/stop, full-width composer, settings, attachments, uncertain-delivery guards and SQLite recovery | Current-artifact held-turn/conflict/reconnect checks, dispatch-boundary death, keyboard/large-text/accessibility and long streams |
| Creation and session management | Project/harness/model/reasoning/directory selection, image drafts, launch/trust, rename/clear/compaction/fork/deletion routes; dated real-daemon and image-creation evidence | Complete final-artifact creation/management matrix, uncertainty and storage failures, trust/path validation, large catalogs and accessibility |
| Goals, tasks, activity and delegates | Goal commands, task reader, queue controls, branch/output paging and delegate navigation are wired. Output lines already support native selection | Real nested delegate/task lifecycle, stale decisions, reconnect and cross-hub identity, output selection on devices, scale and accessibility |
| Approvals and questions | Native controllers and sheets, exact-definition answers, durable decision drafts and stale-request guards; dated real question pause/resume evidence | Real sandbox approval execution, concurrent batches/approvals, cross-device stale decisions, fault/reconnect and current-artifact accessibility |
| Providers and sign-in | Instances, key/credential JSON, endpoint clearing, browser/device sign-in and recovery; dated interrupted OAuth journey | Current-artifact key/JSON/reset matrix, denial/revocation, credential lifetime and isolation, iPad and physical-device checks |
| Plugins and marketplaces | Advertised browsing and install/update/remove/enable/disable/automatic-update operations are wired | Current-artifact owned marketplace lifecycle, actual update/failure/readback, reconnect, large catalogs and accessibility |
| Hub and launch settings | Overview, environment/fallback/path/MCP editors, hub validation, inheritance and conflict/readback; dated iPad path/MCP/environment/fallback evidence | Current-artifact two-hub precedence/conflict matrix, stale editors, faults, schema completeness and accessibility. The old “missing iOS path editor” label is stale |
| Preferences and upgrades | Transcript/keybinding screens, scoped drafts and conflict recovery; actual disposable-hub upgrade and cold recovery evidence | Current-artifact preferences, lost successful-upgrade reply, overlapping hubs, public release service and physical signed app update |
| Native quality and distribution | Standalone simulator Release builds, dated visual/keyboard/large-text checks and scoped persistence evidence | Full VoiceOver/rotor/custom actions, hardware keyboard, landscape, reduced motion, appearance, performance/memory, signed physical install/update and release decision |

The audit found the advertised goal/task/activity, plugin and settings operations in source. Their historical “partial” labels must not be interpreted as proof that those screens or editors are absent. Conversely, this audit does not establish that every supported workflow is accepted.

## Latest verified changes

**Parallel execution checkpoint:** `bc519b1c1` adds existing-session vision controls, capability-aware catalog choices, identity-scoped updates and controller-owned recovery without replay. The coordinator verified 692 native tests plus TypeScript, 494 shared tests, shared lint/typechecking and the Release simulator build. The installed iPhone app restored the original draft; all seven draft tables and the unrelated Apple edits were preserved. Actual native controller code passed host-executed real-hub/SDK checks. The new sheet's native interaction and the integrated Usable journey remain open. See the [execution checkpoint](2026-09-08-iphone-execution-checkpoint.md) and [receipt](assets/2026-09-08-iphone-execution.json).

**Reader restoration:** `7944778e0` fixes the reproduced largest-text cold-launch failure. Restoration advances as virtualized rows become measurable and resets its bounded failure budget only when measurement progresses. An installed launch and two independent iPhone cold launches restored the complete saved turn-12 anchor at the exact observed endpoint. Earlier offset-only and partial retry candidates did not pass repeated launch and are excluded from the fix claim. See [iPhone evidence](iphone-reader-upgrade-evidence.md).

**Hub-scoped cleanup:** the [iPad removal journey](ipad-reader-removal-evidence.md) removed one hub through the UI, confirmed its anchor was gone after cold launch, and preserved the other hub's complete anchor. The later iPhone cleanup preserved all seven original draft tables and the six other reader entries. These are scoped journeys with distinct artifact identities.

**Shutdown publication:** backend change `4f3e824ac` keeps terminal close emission inside the session's one-time close decision. The current packaged-SDK journey at backend `3284d6ac5` received exact `thread/closed` after the active and queued turns completed and canonical status became `awaiting`. Earlier failures used pre-fix `d2d5eedf9` binaries; they do not demonstrate a remaining current SDK/transport failure. Root verified executable build metadata, event identity and retained hashes. See [queue/close evidence](sdk-queue-close-evidence.md).

**Regression tests:** reviewed tests at `fe403ee3a` require exact completed turn IDs and awaiting state, request transcript turns for the predicate, and use a valid scripted `communicate` end-turn response. Root reproduced the test-fixture failures and then observed all four focused active/idle/in-flight/hub shutdown cases pass. Earlier worker claims of passing tests were superseded by this independent verification.

## Current artifact and device evidence

| Artifact | Identity and scope |
| --- | --- |
| iPhone Release simulator app | Source `bc519b1c1`; installed and launched on iPhone 17 Pro, iOS 26.5; vision UI acceptance pending |
| iPhone JavaScript bundle | SHA-256 `934a2359e3ab1dd4a7cd54bdb584a28c9212c99d12cbe4a3dbdceb49a9e3b9c1` |
| iPhone executable | SHA-256 `b0266f0654924c04e4f4473beb390a395ed64ab745a027c419ca040a081fda5c` |
| Paused iPad Release app | Source `7944778e0`, iPad Pro 11-inch (M5), iOS 26.5; unchanged historical artifact |
| Packaged SDK used by producer fixtures | Source `58d1b079f`; package `@evener/appwire-client@0.1.0`; tarball SHA-256 `2a22e65e84da4fa2466a5406800f7b141ee710d4f4e4f73588fbf4873e73e731`; all 139 regular tarball files matched the separate installed consumer |
| Latest closed-event backend | Source `3284d6ac5`; executable hashes and exact target/readback/event evidence in the [receipt](assets/sdk-queue-close-receipt.json). This is separate from the native app identity |

On iPhone, the reader result is qualified at largest text; code and link copying have earlier-artifact evidence. On the current iPad artifact, clean/cold launch, empty Hubs, keyboard reveal/dismiss and largest-text form reachability have [scoped evidence](ipad-final-artifact-qualification.md). Current-artifact iPad landscape, reader restoration and VoiceOver remain unqualified. A sideways simulator capture did not establish a layout defect.

The prior reader campaign restored the original iPhone conversation and normal text size and removed its owned profiles/fixtures. The latest vision installation again preserved all seven original draft tables and the original profile/session. Current fixture ownership and device limitations are recorded in the [execution checkpoint](2026-09-08-iphone-execution-checkpoint.md). The two pre-existing generated Tauri Apple edits remain preserved and unstaged. Private raw captures, tokens and process metadata are not release assets; committed receipts contain scoped assertions and hashes.

## TypeScript SDK status

The SDK has an independently installable package with ESM/CommonJS entry points and declaration checking. The cookbook covers 90 of 91 cataloged methods; the remaining method is reserved and intentionally unsupported. All 36 notification names have recipes. These are documentation-presence counts, separate from executed producer/outcome coverage.

The audited receipt union establishes scoped producer/readback evidence for **28 of 36 notification names**. It combines the dedicated notification series with separately recorded settings, credential, navigation and launch-layer runs, counting each name once. Queue change, terminal close and the new launch-layer update have coordinator-verified scoped outcomes. The eight names outside this union need consolidated producer evidence or fresh qualification; `thread/started` remains excluded because a start response is not its notification. No all-outcome or single-artifact claim follows from the count. Complete method success/failure/disconnect coverage, remaining producer names, continuous reconnect/replay and broad lifecycle outcomes remain open. Package publication is separate from successful local build/pack/install tests. See [protocol inventory](protocol-coverage.md), [notification evidence](sdk-notifications-evidence.md) and [management evidence](sdk-management-evidence.md).

## Verification record

The current native suite passed **692 tests across 74 files plus TypeScript** at `bc519b1c1`; shared checks and simulator build/install details are in the [execution receipt](assets/2026-09-08-iphone-execution.json). The reader source passed 685 tests. Root's four focused shutdown regressions and scoped Go lint passed at `fe403ee3a`.

The last `make merge-approval-gate` and separate `make vet` both exited **0** on 8 September at compiled source `fe403ee3a`. The canonical run passed lint, build, all Go modules, frontend checks, 685 native tests plus TypeScript, and outside-checkout SDK package qualification. The invocation head was `a35a5cfac`; the later vision change has scoped checks above, not a new canonical-gate claim. The [durable verification receipt](assets/2026-09-08-status-verification.json) records the earlier command results, timestamps, log hashes and preservation of the unrelated Apple diff.

Browser geometry and race checks have separate ownership and were not repeated for this documentation update. Their earlier evidence remains dated in the acceptance ledger. The canonical gate does not establish physical-device behavior, accessibility, performance or release readiness.

## Remaining delivery order

1. **Usable:** connect/install, create/open/read/send/stop, answer questions/approvals and return safely after ordinary interruption. Physical-iPhone development installation starts here.
2. **Useful:** finish navigation/management, media and active-turn controls, goals/delegates, native qualification of the implemented vision choice, provider/plugin administration, launch/preferences and upgrade. Qualify the joined native workflow.
3. **Good:** measure representative-data performance, refine coherent interaction, stress recovery combinations and verify physical install/update and delivery readiness. Correctness defects that block earlier milestones are fixed immediately.
4. **SDK:** run independent installed-package outcome/producer work alongside the app, giving priority to contracts needed by the active native workflow.

The [detailed plan](../../superpowers/plans/2026-09-08-iphone-usable-useful-good.md) defines milestone exit criteria and source-appropriate verification. iPad and accessibility remain paused; Android and voice remain outside current delivery. No percentage-complete or ship-date estimate is supported by the remaining acceptance work.
