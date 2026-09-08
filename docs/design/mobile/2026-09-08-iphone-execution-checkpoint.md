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

Adding this distinct name to the previous audited union gave 28 of 36 scoped notification producers. The next parallel batch qualified marketplace/plugin updates and sandbox escalation requested/resolved notifications through the installed SDK, bringing the audited union to 32 of 36. Their receipts retain catalog state and actual successful/denied tool results, with no native acceptance claim. A further coordinator-audited batch adds message reset after an interrupted partial response and a working attention transition with matching authoritative navigation summary, bringing the union to 34 of 36. Complete method outcomes, remaining producers and broad reconnect/replay qualification remain open. See the [complete union and exclusions](sdk-notifications-evidence.md#queue-and-thread-close-producer-evidence).

## What remains open

The integrated Usable journey remains the critical path: connect to the owned hub in the installed iPhone app, create/open, send, answer/approve, stop/continue, leave and return. The original hub is currently unreachable; its profile and draft were preserved. The coordinator has not entered a new fixture profile through the UI.

Computer Use can read the simulator and use exposed controls, but coordinate actions needed to reach the navigation header fail with `windowNotFoundAtPosition` or `noWindowsAvailable`. The updated app visibly restores its saved draft; this tooling failure is not evidence of an app navigation defect or a successful interaction check.

The paired physical iPhone 16 Pro is available, but Apple's developer tools report `kAMDMobileImageMounterDeviceLocked`. Jesse was asked to unlock it; no physical install or keychain/network qualification is claimed. A valid existing Apple Development identity and wildcard provisioning profile were discovered without changing signing access.

The subsequent Release build for `generic/platform=iOS` succeeded at source `5307e8515`, with the existing team identity and cached provisioning profile. The coordinator verified the arm64 bundle's signature, embedded profile and application entitlement. The [device-build receipt](assets/2026-09-08-iphone-device-build.json) records its distinct artifact hashes. This closes development build/signature preparation, not physical install or TestFlight acceptance. The [distribution path](ios-build-distribution.md) reuses the organization's existing Mac runner and shared-credential conventions; iOS distribution credentials and App Store Connect access still need verification.

Tasks 1–4 therefore remain open as one integrated device journey. Task 8 has implementation, deterministic and host/controller real-hub evidence, with native selection/reconnect acceptance pending. Useful administration, actual nested work, performance, stress and physical update/distribution retain the gates in the plan. Neither iPhone functional completion nor release readiness is claimed.
