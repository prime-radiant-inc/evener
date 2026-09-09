# iPhone execution checkpoint — 8 September 2026

Jesse authorized parallel Luna-medium execution of the [Usable → Useful → Good plan](../../superpowers/plans/2026-09-08-iphone-usable-useful-good.md). Three Luna workers handled disjoint implementation, controller checks, review and private SDK fixtures while the coordinator integrated and verified the results. iPad and dedicated accessibility work remain paused.

## Implemented and installed

Commit `bc519b1c1` adds existing-session vision-model selection to the native composer. The choices are the current session model, disabled image description, or a catalog model. Explicit `supportsVision: false` entries are excluded; absent metadata remains unknown. A current setting remains visible even if it is absent from the catalog.

The shared projection, capability-gated service and identity-scoped notification store carry the setting. Native session controls serialize the action and refresh authoritative state after success. An `actionUnavailable` rejection causes one readback without replay; an ordinary lost reply remains uncertain. Tests cover binding replacement, disposal and refresh failure. Picker selection and filtering reset when switching between model and vision settings. The existing settings-error control opens the appropriate sheet.

Coordinator review identified the missing controller-owned refresh, and independent Luna review identified stale picker state; the coordinator verified the fixes. The full shared-mobile typecheck also exposed six test/harness doubles that needed the required method. Those were updated without adding compatibility fallbacks.

The reviewed Release build was installed and launched on iPhone 17 Pro simulator, iOS 26.5. Its executable SHA-256 is `b0266f0654924c04e4f4473beb390a395ed64ab745a027c419ca040a081fda5c`; bundle SHA-256 is `934a2359e3ab1dd4a7cd54bdb584a28c9212c99d12cbe4a3dbdceb49a9e3b9c1`. The original hub, session and exact ordinary draft reappeared. All seven original draft tables remained equal before and after installation. The prior app was backed up. The two unrelated generated Tauri Apple edits remained byte-for-byte unchanged and unstaged.

This establishes build, install, launch and saved-data preservation. The new vision sheet has **not** passed an on-device interaction journey.

## Verification and real-hub scope

- Coordinator: `make test-native` passed 692 tests in 74 files plus TypeScript.
- Coordinator: shared projection/service/store suites passed 494 tests; shared-mobile Biome and TypeScript passed after the required test-double updates.
- Coordinator: final simulator Release build, install and launch exited zero.
- Coordinator: repeated the actual native `SessionControls` class against a private authenticated real Evener hub and an independently installed SDK. `off`, catalog routing, second-client changes and restoration passed exact state/readback assertions. This executes native controller code on the host; it is not a native UI test.
- Luna workers: existing conversation, decision, draft, organization, media/command, provider/plugin and settings/upgrade controller suites passed within their assigned scope. No additional controller defect was reproduced. These checks do not close the full native tasks.

The scripted external provider drives the real daemon. Its independently checked scenarios cover an ordinary Markdown response, an interrupted held turn followed by a completed new turn, and an answered real question. Early fixture attempts dropped streaming content or matched old prompts; those were repaired before the accepted scenarios. Early vision receipts either captured values after restoration or only exercised shared services; they are superseded by the coordinator-repeated native-controller receipt.

The coordinator shut down all three owned fixtures, separately stopped four detached fixture daemons, removed their ephemeral bearer credentials and verified no owned processes remained. Private scripts and evidence were retained.

The [verification receipt](assets/2026-09-08-iphone-execution.json) retains artifact and log identities. The earlier canonical repository gate at compiled source `fe403ee3a` remains historical; this checkpoint does not claim a fresh full repository, browser-geometry or race gate.

## SDK progress

One new producer is accepted: `evener/launch/updated`. The independently installed SDK observed two project-layer notifications, authoritative `maxRounds: 7` readback and restoration of the original layer. The [receipt](assets/2026-09-08-sdk-launch-producer.json) records exact artifact/capture identities and the failed-provider limitation. The capture contains no `thread/started`; a `thread/start` response and `turn/started` notification do not qualify that producer.

Adding this distinct name to the previous audited union gave 28 of 36 scoped notification producers. The next parallel batch qualified marketplace/plugin updates and sandbox escalation requested/resolved notifications through the installed SDK, bringing the audited union to 32 of 36. Their receipts retain catalog state and actual successful/denied tool results, with no native acceptance claim. A further coordinator-audited batch adds message reset after an interrupted partial response and a working attention transition with matching authoritative navigation summary, bringing the historical union to 34 of 36. Two root-audited installed-SDK receipts now qualify `thread/started` and `evener/thread/resync`, bringing the combined scoped producer union to **36 of 36 distinct notification names**. The failed [resync producer attempt](assets/2026-09-08-sdk-resync-producer.json) remains unqualified. Complete method success/failure/disconnect outcomes, reconnect/replay stress and SDK release readiness remain open. See the [complete union](sdk-notifications-evidence.md#current-two-receipt-producer-qualification--8-september-2026).

## What remains open

The integrated Usable journey remains the critical path. The coordinator used the installed native app to save and connect the owned `iPhone daily journey` hub, select its project and model, and create `local:034LTaHspOapGYPMW40hTr`. The native transcript rendered a real Markdown response. A held native turn was stopped and authoritatively recorded as interrupted; the next native turn completed with a response. A real question opened in the native panel, retained its answer note after leaving and reopening, and resolved through native Send answers with a completed follow-up response. An unsent ordinary draft survived switching to another session and back, then backgrounding and reopening the app. The [native journey receipt](assets/2026-09-08-native-conversation-journey.json) retains exact readback identities and the installed artifact's separate provenance.

Computer Use input became reliable after temporarily connecting the Simulator hardware keyboard; that setting was reversed afterward. Every original row in all seven draft tables remains unchanged. The only retained additions are the owned conversation and question drafts. The original `v4 acceptance` hub was selected again; its unreachable server prevents reopening the original session from its roster, so only profile restoration and preservation of the original draft row are claimed.

The journey exposed a real upstream defect: completed responses retain an `inProgress` reasoning item and display “Reasoning · running.” The source correction closes reasoning at answer/round/terminal boundaries and preserves accumulated reasoning text and truncation ownership in the mobile store when completion omits text. Independent review passed. The coordinator verified the full projector/server suites, the real WebSocket readback regression, 283 mobile conversation tests, 692 native tests, and mobile/native TypeScript and formatting checks; Go lint passed with zero issues. This journey used the previously installed native artifact and older fixture runtime, so it does not qualify those fixes. Native approval, network interruption/reconnect, force-quit/cold launch, corrected-artifact verification and the broader acceptance gates remain open.

The paired physical iPhone 16 Pro is available, but Apple's developer tools report `kAMDMobileImageMounterDeviceLocked`. Jesse was asked to unlock it; no physical install or keychain/network qualification is claimed. A valid existing Apple Development identity and wildcard provisioning profile were discovered without changing signing access.

The subsequent Release build for `generic/platform=iOS` succeeded at source `5307e8515`, with the existing team identity and cached provisioning profile. The coordinator verified the arm64 bundle's signature, embedded profile and application entitlement. The [device-build receipt](assets/2026-09-08-iphone-device-build.json) records its distinct artifact hashes. This closes development build/signature preparation, not physical install or TestFlight acceptance. The [distribution path](ios-build-distribution.md) now has a manual workflow, locked dependencies, iPhone-only source configuration, and locally verified delivery controls. The coordinator reran `make test-native test-api-package`: 692 native tests, TypeScript and the installed-package qualification passed. iOS distribution credentials and App Store Connect access still need verification; no workflow dispatch or upload is claimed.

The next simulator Release artifact now builds and passes signature verification
with the reasoning fix at code source `6a0278e36` and the portable CocoaPods
configuration. Both arm64 and x86_64 slices target iPhone device family 1. The
[build receipt](assets/2026-09-08-ios-pod-lock-portability.json) records two
separate locked dependency installations and a fresh 692-test native gate. It
was subsequently installed and launched; the previous installed app and complete draft database
remain backed up. The rejected approval fixture was stopped after direct source
inspection contradicted its handoff report; it contributes no native acceptance.

The [updated-artifact native journey](assets/2026-09-08-native-joined-journey.json)
records native connection to an owned hub, an ordinary prompt entered entirely
through software-keyboard taps, and a completed Markdown response. Reasoning
retains its exact text with completed status in authoritative readback, and the
native transcript no longer shows a lingering running indicator. Completed
reasoning text was not expanded in the native UI. Native Allow once created the
exact pending write target; native Deny left a different target absent after its
turn completed. A question option and note were sent through the native panel,
with Send answers visible above the software keyboard. Every original row in all
seven draft tables remains preserved; one owned question-draft row was added.

Native Stop produced an Interrupted notice and matching interrupted readback.
The provider controller was then found absent, with no retained cancellation
confirmation or known exit cause, so provider cancellation and a later response
are not qualified by this updated-artifact case. The hub remains live and fixture
cleanup is incomplete. Separate ordinary draft coexistence with a question,
second-client stale decisions, cold launch, reader restoration and reconnect
remain open for this artifact.

The approval journey exposed an unhelpful file-action label. Commit `bfe632f44`
derives a bounded, neutral `Write <path>` summary from the current tool argument
without exposing file contents or claiming that a denied write succeeded. The
coordinator reviewed the Luna change and passed 698 native tests plus TypeScript
and formatting. The subsequent identified Release build is now installed and its `Write <path>` labels were observed natively for both successful and denied actions. The [recovery receipt](assets/2026-09-08-native-recovery-journey.json) records executable and JavaScript hashes and exact draft preservation.

On that build, native Stop cancelled the actual held provider request and produced interrupted `turn_m7`. A later prompt completed once as `turn_m8`. The free-text question answer completed as `turn_m10` while the separate ordinary draft remained unsent and returned intact; Send answers stayed above the software keyboard. A second client then resolved a question while its native sheet was open: the sheet closed, `turn_m12` completed, the exact answer appeared once and the original draft returned. Cold reader restoration, live-hub reconnect, code/link return and two-hub isolation still need current-artifact checks.

Commit `6ad404195` fixes a separate clean-CI TypeScript dependency-resolution failure. Only the checker configuration and check script changed; Expo runtime configuration is untouched. The clean isolated check, 698 native tests plus TypeScript, and independent Luna review passed.

Tasks 1–4 therefore remain open as one integrated device journey. Task 8 has implementation, deterministic and host/controller real-hub evidence, with native selection/reconnect acceptance pending. Useful administration, actual nested work, performance, stress and physical update/distribution retain the gates in the plan. Neither iPhone functional completion nor release readiness is claimed.
