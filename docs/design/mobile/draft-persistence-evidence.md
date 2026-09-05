# Native draft persistence evidence

Observed 5 September 2026 on source `b4d7060cf`, following the approved visual direction. This is a continuity slice, not a feature-complete or performance-certified app.

## Builds and checks

- iOS: Release, iPhone 17 Pro simulator, iOS 26.5, bundle `com.primeradiant.evener.native`. Expo SQLite 57.0.2 installed through CocoaPods; final build/run succeeded in 12.9 seconds after the initial native module build.
- Android: Release, Pixel 7 emulator, Android 35, package `com.primeradiant.evener.mobile`. Final APK SHA-256: `3891261beb2638dec9eaa1a0fbd2117ef64092e2227cb4128bd64e79c9622fce`.
- `npm test`: 44 tests in nine files passed. `npm run check` and targeted Biome checks passed. No whole-repository merge gate was run for this slice.
- Independent review found two hub-removal failure paths. Both were fixed and re-reviewed without further material findings: deletion during an awaited credential operation, and a failed profile-list refresh after successful removal.

## Runtime observations

| Check | Observed result |
| --- | --- |
| Android cold launch | Typed `Android persistent draft  September 5`, force-stopped the app, relaunched, reopened Playground / Mobile playground. Composer retained the text. |
| Android hub switch | Typed different text in Native E2E / Fake Session, switched back to Playground. The original Playground text remained; no cross-hub draft appeared. |
| Accepted send | Sent the saved Playground draft on each platform. The fixture transcript received it and the composer cleared. Android remained empty after force-stop and installation of the final APK. iOS SQLite inspection confirmed the accepted draft row was absent. |
| iOS cold launch | Typed a draft in Playground, stopped and relaunched the app, reopened the same session. Composer retained the actual entered text. The simulator typing tool's double-space correction produced periods; this was input behavior, not a storage normalization. |
| iOS interrupted delivery | A local WebSocket proxy forwarded `turn/start` to the scripted hub but withheld its JSON-RPC acknowledgement. The transcript received `Delivery checkpoint iOS`. Stopped the app before the response completed; cold launch restored the separate “Delivery unconfirmed” record. |
| Android interrupted delivery | Repeated through the proxy with `Delivery checkpoint Android`, force-stopped and reopened Demonstration 2. The recovery record and delivered transcript item both appeared. |
| No automatic replay | Proxy logged exactly two `turn/start` requests total, one from each explicit test send. Cold launches, reopen, and recovery produced no additional request. |
| Newer text | On iOS, typed `Newer draft stays separate` while uncertain text existed, then cold launched again. Both survived. Restore was disabled; Dismiss removed only the uncertain record. SQLite inspection showed the two separate values before dismissal. |
| Explicit restore | On Android, Restore moved the uncertain text into an empty composer without sending it. |
| Remove test hub | Removed the temporary proxy profile through each platform's native confirmation dialog, which names local drafts. Both lists removed the profile. iOS SQLite count was zero afterward. Other saved hubs remained; Android reopened Native E2E with its distinct unsent draft intact. |

The fault proxy listened on `0.0.0.0:9197`, forwarding to `ws://127.0.0.1:9196/rpc`. It recorded IDs of `turn/start` requests and dropped only matching response messages; notifications and other responses passed through unchanged. Thus the server genuinely received the input while the client lacked acceptance confirmation. The temporary proxy was stopped and its saved profiles removed after testing. The existing playground and isolated real-hub fixtures remain available. Production sessions were not mutated.

## Screenshots

[iOS interrupted delivery](assets/drafts/ios-delivery-unconfirmed.png), [Android interrupted delivery](assets/drafts/android-delivery-unconfirmed.png), and [iOS preserving a newer draft](assets/drafts/ios-newer-draft.png) were captured from installed native apps and inspected.

## Behavioral tests and limits

The repository tests use real SQLite, including temporary disk databases closed and reopened, with long Unicode and multiline input, exact whitespace, independent hub/session keys, targeted deletion and SQL-like text. Document tests cover checkpoint ordering, failed reads/writes, retry, rejected or unconfirmed operations, explicit recovery, and newer text during acceptance. Library/removal tests cover route reuse, stale completion, cleanup failures, and partial SecureStore failures.

These manual mutation checks used a scripted demonstration hub, not a model or the production hub. The prior real isolated Evener running-turn evidence remains in [visual slice evidence](visual-slice-evidence.md). The tests do not certify physical-device storage, encryption beyond the app sandbox, low-disk behavior on actual phones, or typing performance. SQLite writes are synchronous and must be measured on representative devices before claiming fluency. Screen-reader, large-text and reading-position work remains in the app roadmap. The UI typing tool rejected Unicode and line breaks, so exact preservation of those inputs is established by repository tests rather than this manual run.
