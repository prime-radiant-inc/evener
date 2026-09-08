# Native mobile project status

Updated 8 September 2026. Owner: Bot; product direction: Jesse.

**The native app has substantial implementation and verified end-to-end slices. iOS v1 is not release-ready.** The main remaining work is completing workflow, lifecycle, accessibility, performance and physical-device qualification against identified artifacts, fixing defects those checks expose, and finishing the SDK outcome matrix. Passing a build or finding a screen in source does not close a workflow.

## Scope and architecture

- **V1 is iOS-only: iPhone and iPad.** Android source, build instructions and historical evidence are preserved; Android qualification and its unresolved ANR investigation are deferred beyond v1. Voice/barge-in is outside v1.
- The app remains Expo SDK 57 / React Native in `mobile-native/`, with React Navigation, SecureStore and SQLite. It reuses the framework-independent TypeScript AppWire client and selected shared services/state. The old Tauri UI is not the product reference.
- Current web behavior, server contracts and Jesse's explicit requests define capabilities. The AppWire v4 migration is implemented, including item-based transcript paging, navigation representation v2 and current provider/settings contracts.
- The authoritative worktree is `live-concepts-plan2-integrate` under the external Evener worktree directory named in the [takeover handoff](../../superpowers/handoffs/2026-09-06-native-mobile-takeover.md). The default checkout is not the implementation workspace.
- Routine choices and parallel Luna-medium implementation/testing are authorized. A coordinator owns integration and artifact identity; separate workers can test iPhone/iPad or independent fixtures without sharing mutations.

This page is the current summary. The [acceptance ledger](acceptance.md) defines each workflow's evidence boundary; the [remaining-work checklist](ios-v1-remaining.md) defines execution order. The [backlog](backlog.md) retains issue IDs and feedback. Dated feature receipts and the original coverage spec remain historical evidence, not a competing current status.

## Implementation and acceptance by area

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

**Reader restoration:** `7944778e0` fixes the reproduced largest-text cold-launch failure. Restoration advances as virtualized rows become measurable and resets its bounded failure budget only when measurement progresses. An installed launch and two independent iPhone cold launches restored the complete saved turn-12 anchor at the exact observed endpoint. Earlier offset-only and partial retry candidates did not pass repeated launch and are excluded from the fix claim. See [iPhone evidence](iphone-reader-upgrade-evidence.md).

**Hub-scoped cleanup:** the [iPad removal journey](ipad-reader-removal-evidence.md) removed one hub through the UI, confirmed its anchor was gone after cold launch, and preserved the other hub's complete anchor. The later iPhone cleanup preserved all seven original draft tables and the six other reader entries. These are scoped journeys with distinct artifact identities.

**Shutdown publication:** backend change `4f3e824ac` keeps terminal close emission inside the session's one-time close decision. The current packaged-SDK journey at backend `3284d6ac5` received exact `thread/closed` after the active and queued turns completed and canonical status became `awaiting`. Earlier failures used pre-fix `d2d5eedf9` binaries; they do not demonstrate a remaining current SDK/transport failure. Root verified executable build metadata, event identity and retained hashes. See [queue/close evidence](sdk-queue-close-evidence.md).

**Regression tests:** reviewed tests at `fe403ee3a` require exact completed turn IDs and awaiting state, request transcript turns for the predicate, and use a valid scripted `communicate` end-turn response. Root reproduced the test-fixture failures and then observed all four focused active/idle/in-flight/hub shutdown cases pass. Earlier worker claims of passing tests were superseded by this independent verification.

## Current artifact and device evidence

| Artifact | Identity and scope |
| --- | --- |
| Native Release simulator app | Native source `7944778e0`; installed on iPhone 17 Pro and iPad Pro 11-inch (M5), both iOS 26.5 |
| Native JavaScript bundle | SHA-256 `af754632eef5f34e9b231e3cd9c30282d5aeb038003479e921993c42629d7c48` |
| Native executable | SHA-256 `964c2bb36433c222362ad6ebc923229b441d613d9475abd8abe92e2930a8ab95` |
| Packaged SDK used by producer fixtures | Source `58d1b079f`; package `@evener/appwire-client@0.1.0`; tarball SHA-256 `2a22e65e84da4fa2466a5406800f7b141ee710d4f4e4f73588fbf4873e73e731`; all 139 regular tarball files matched the separate installed consumer |
| Latest closed-event backend | Source `3284d6ac5`; executable hashes and exact target/readback/event evidence in the [receipt](assets/sdk-queue-close-receipt.json). This is separate from the native app identity |

On iPhone, the reader result is qualified at largest text; code and link copying have earlier-artifact evidence. On the current iPad artifact, clean/cold launch, empty Hubs, keyboard reveal/dismiss and largest-text form reachability have [scoped evidence](ipad-final-artifact-qualification.md). Current-artifact iPad landscape, reader restoration and VoiceOver remain unqualified. A sideways simulator capture did not establish a layout defect.

The original iPhone conversation and normal text size were restored, original drafts remained equal, owned test profiles and diagnostic state were removed, and the owned reader/SDK fixtures were shut down. The two pre-existing generated Tauri Apple edits remain preserved and unstaged. Private raw captures, tokens and process metadata are not release assets; committed receipts contain scoped assertions and hashes.

## TypeScript SDK status

The SDK has an independently installable package with ESM/CommonJS entry points and declaration checking. The cookbook covers 90 of 91 cataloged methods; the remaining method is reserved and intentionally unsupported. All 36 notification names have recipes. These are documentation-presence counts, separate from executed producer/outcome coverage.

The audited receipt union establishes scoped producer/readback evidence for **27 of 36 notification names**. It combines the dedicated notification series with separately recorded settings, credential and navigation runs, counting each name once. Queue change and terminal close now have coordinator-verified scoped outcomes. The nine names outside this union need consolidated producer evidence or fresh qualification; no all-outcome or single-artifact claim follows from the count. Complete method success/failure/disconnect coverage, remaining producer names, continuous reconnect/replay and broad lifecycle outcomes remain open. Package publication is separate from successful local build/pack/install tests. See [protocol inventory](protocol-coverage.md), [notification evidence](sdk-notifications-evidence.md) and [management evidence](sdk-management-evidence.md).

## Verification record

The native suite passed **685 tests across 74 files plus TypeScript** for the reader source. Root's four focused shutdown regressions and scoped Go lint passed at `fe403ee3a`.

The current `make merge-approval-gate` and separate `make vet` both exited **0** on 8 September. The canonical run passed lint, build, all Go modules, frontend checks, 685 native tests plus TypeScript, and outside-checkout SDK package qualification. Compiled source is `fe403ee3a`; the invocation head was `a35a5cfac`, and subsequent changes were documentation only. The [durable verification receipt](assets/2026-09-08-status-verification.json) records command results, timestamps, log hashes and preservation of the unrelated Apple diff.

Browser geometry and race checks have separate ownership and were not repeated for this documentation update. Their earlier evidence remains dated in the acceptance ledger. The canonical gate does not establish physical-device behavior, accessibility, performance or release readiness.

## Remaining delivery order

1. Keep the verified integration baseline intact; repeat affected gates when source changes and retain native/backend/SDK artifact identities separately.
2. Run the remaining iPhone/iPad workflows using isolated authenticated v4 hubs and scripted providers, preserving authoritative API/state readback. Fix reproduced defects before closing each workflow.
3. Complete the SDK's catalog-derived success/failure/disconnect and notification producer matrix through the installed package.
4. Qualify cross-cutting native behavior: simultaneous hubs, pending operations, cold/background recovery, live/paged/image reflow, stale decisions, gestures, keyboard, large text, appearance and accessibility.
5. Measure representative data performance and memory, then verify physical iPhone/iPad networking/pairing, signed installation and update/relaunch.
6. Assemble one final release record with every required workflow and artifact identified. Release acceptance stays open until that record is complete.

No percentage-complete or ship-date estimate is supported by the remaining acceptance work. The [full to-do list](ios-v1-remaining.md) and [workflow ledger](acceptance.md) define completion.
