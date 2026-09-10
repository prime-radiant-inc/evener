# Native iPhone checkpoint status

This checkpoint brings the existing Expo/React Native iPhone application and its shared session core into the main repository. It is an integration baseline, not a declaration that iPhone v1 acceptance is complete. iPad and dedicated accessibility work are paused; Android and voice are outside this release.

The app includes hub profiles and secure credentials, project-organized session browsing with automatic paging, conversation reading and live updates, text/image drafts, send/steer/queue/stop controls, questions and approvals, session creation and management, goals/tasks/delegates, and provider/plugin/settings screens. Current web and server contracts define those capabilities.

## Candidate verification

The corrected candidate preserves current-main generated protocol definitions, strict connection validation, keybinding actions and web behavior. Pure activity, composer, catalog, input and display helpers are shared with the web. The eight existing mobile runtime modules retain their current paths; higher-level web-state extraction has not started.

The native gate runs the native tests, shared-session tests and strict native typechecking. Current local results are 718 native tests across 76 files and 673 shared-session tests across seven files, plus passing TypeScript checks. The independent package gate installs a tarball outside the checkout and runs its read-only example against a scripted external WebSocket server. CI has separate native, package and web jobs. See [acceptance](acceptance.md) for the remaining artifact and product gates.

A fresh iOS project generation and locked CocoaPods deployment install pass with the committed dependency locks. A current-source simulator journey, reviewed TestFlight build and physical iPhone update acceptance remain required. The most recent verified App Store Connect artifact is version 0.1.0 build 3; Apple reports it as valid and available to internal testers. That artifact predates this checkpoint and does not qualify it.

## Historical evidence

The full implementation history, design research, dated simulator receipts, SDK outcome matrix and unfinished work remain preserved on the development branch `live-concepts-plan2-integrate` (status checkpoint `04ae937af`). This landing intentionally contains the runtime dependency closure and a concise acceptance index. Historical evidence is not reclassified as evidence for a newly built artifact. Both unrelated unfinished Apple project files remain preserved on that development branch.

The [remaining work](ios-v1-remaining.md), [checkpoint order](2026-09-09-iphone-main-checkpoint.md), [native app guide](../../../mobile-native/README.md) and [build guide](ios-build-distribution.md) describe the next steps.
