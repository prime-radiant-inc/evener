# Native Apple redesign verification

The iPhone redesign runs in the native app against a real local hub. The bounded visual review accepted the corrected screens. Native checks also caught and resolved composer growth and Dynamic Type regressions that screenshots and renderer tests missed.

**Integration qualification:** The native captures and simulator observations below were recorded before integrating main's Expo upgrade and share support. Jesse chose Expo's built-in scene startup, superseding this branch's custom adapter. The integrated host has not been rebuilt or launched locally: iOS prebuild now requires `APPLE_TEAM_ID`, which is absent in this environment. Earlier simulator results do not verify that replacement host or the share extension.

## Candidate and scope

- Checked on 2026-10-19 UTC, on branch `wip/native-apple-design`.
- Original base: `34d4ea239ec6c192838b544d6c4dd4e7f70c04a1`. Scene startup is committed at `340fc1eff5`; the final UI corrections and this evidence are in the commit containing this record.
- Native host: `mobile-native/ios/build/Build/Products/Debug-iphonesimulator/Evener.app`, built from this branch. The installed Debug host loaded the final JavaScript from Metro on port 8087. This is not a standalone release bundle or TestFlight build.
- Device: iPhone 18 Pro simulator, iOS 27.0, 402×874 points. Captures are 1206×2622 pixels. Ordinary text uses the `large` category; maximum text uses `accessibility-extra-extra-extra-large`.
- Backend: current-source real `evener hub`, AppWire and session daemon, with the maintained `test/e2e/fakellm/cmd` scripted provider only at the LLM boundary. No live provider credentials or production hub data were used.
- No publication, main-branch merge, deployment or TestFlight upload was performed. iPad and Android delivery remain deferred.

This record supplements [iPhone acceptance](acceptance.md). It qualifies the checks below, not every release criterion in that guide.

## Implemented surfaces

| Area | Source changes |
| --- | --- |
| Shared controls | Crisp neutral light/dark surfaces, sentence-case 15/20 section headings, adaptive segmented choices, consistent selection marks and 44-point search actions |
| Board | Clearer title/context/age hierarchy, large-text reflow, readable project names and unchanged inline project navigation |
| Conversation | Separate title and live-jump actions, protected navigation targets, intrinsically sized composer and viewport-aware expansion without replacing its input |
| New session and model sheets | Readable provider headings and a consistent leading check slot for selected and unselected choices |
| Hub administration | Grouped settings, consistent value summaries and scalar editors, inset search, and Save outside the scrolling form |
| Retained settings | Disabled rows and inputs remain readable while editing is unavailable; native disabled semantics and retained drafts remain intact |
| iOS startup | Expo's built-in scene support owns the window and startup; installed-template tests cover plist preservation and repeated evaluation |

The design record is [mobile-native/DESIGN.md](../../../mobile-native/DESIGN.md), with its [Impeccable sidecar](../../../mobile-native/.impeccable/design.json) and [product context](../../../mobile-native/PRODUCT.md). [The shared web guide](../../web-ui/design-system.md) distinguishes platform palettes while preserving common semantic colors and hierarchy. Web implementation was unchanged.

## Native captures

The first inspection found three material visual defects: clipped composer text at maximum size, an uninset launch-settings search field, and a truncated Board project name. One correction batch and one finish confirmation cleared those findings. Subsequent functional composer corrections were checked with real native input and the four conversation captures were refreshed from the final source.

| Surface | Ordinary light | Ordinary dark | Maximum text |
| --- | --- | --- | --- |
| Board, with real session row and draft | [Light](assets/2026-10-19-apple-board-light.png) | [Dark](assets/2026-10-19-apple-board-dark.png) | [Large](assets/2026-10-19-apple-board-large-text.png) |
| Conversation | [Light](assets/2026-10-19-apple-conversation-light.png) | [Dark](assets/2026-10-19-apple-conversation-dark.png) | [Large](assets/2026-10-19-apple-conversation-large-text.png), [keyboard open](assets/2026-10-19-apple-composer-keyboard-large-text.png) |
| Hub | [Light](assets/2026-10-19-apple-hub-light.png) | Not captured | [Large](assets/2026-10-19-apple-hub-large-text.png) |
| Hub settings | [Light](assets/2026-10-19-apple-hub-settings-light.png) | Not captured | Not captured |
| Launch defaults | [Empty search](assets/2026-10-19-apple-launch-defaults-light.png), [entered query](assets/2026-10-19-apple-launch-search-query.png) | Not captured | Not captured |
| Launch scalar editor | [Light](assets/2026-10-19-apple-launch-editor-light.png) | Not captured | [Large](assets/2026-10-19-apple-launch-editor-large-text.png) |
| Web keyboard shortcuts | [Light](assets/2026-10-19-apple-shortcuts-light.png) | [Dark](assets/2026-10-19-apple-shortcuts-dark.png) | [Large](assets/2026-10-19-apple-shortcuts-large-text.png) |
| New session | [Light](assets/2026-10-19-apple-new-session-light.png) | Not captured | [Large](assets/2026-10-19-apple-new-session-large-text.png), [options](assets/2026-10-19-apple-new-session-options-large-text.png) |
| Model picker | [Light](assets/2026-10-19-apple-model-picker-light.png) | Not captured | [Large](assets/2026-10-19-apple-model-picker-large-text.png) |

These 22 captures show the actual native interface, not browser mockups. Scrolled text at a viewport edge is not a complete-page capture. In the largest Board capture, the floating compose button overlaps the project disclosure symbol; the name remains readable, and the capture alone does not establish blocked row access.

## Automated checks

Run from the repository root:

```sh
make test-native
```

The final run exited zero after the last composer correction. The test phases emitted no React act warnings or fixture debug logs. The cold export reported its expected empty-cache rebuild warning; npm printed an update notice and Vitest printed performance suggestions.

| Check actually run | Result |
| --- | --- |
| Cold Metro iOS export | Produced one iOS bundle from 2,476 modules |
| Native Vitest suite | 377 files, 6,295 tests passed |
| Shared mobile Vitest suite | 6 files, 762 tests passed |
| Native TypeScript check | `tsc --noEmit --project tsconfig.check.json` passed |
| Native/shared Biome gate | 816 files checked, no changes |
| Native script import resolution | 7 tools resolved across 143 first-party modules, without executing those socket-owning tools |

Additional targeted verification:

- `cd mobile-native && npm test -- src/session/Composer.test.tsx src/LaunchSettingsScreen.test.tsx src/board/ProjectTreeRow.test.tsx src/usePinNavigation.retry.test.tsx`: 4 files, 62 tests passed in the independent final review.
- `cd mobile-native && npm run check`: TypeScript passed after the final input-scaling change.
- `cd mobile-native && npx biome check --write src/session/Composer.tsx src/session/Composer.test.tsx`: no changes.
- Scene-plugin tests: 10 tests passed, exercising actual Expo config mods against the Expo 57 fixture. Two real prebuild runs produced identical generated AppDelegate and Info.plist content.
- Design record verification: all 50 recorded palette values matched source; sidecar JSON, schema version and canonical color mappings were checked during documentation review. The final correction batch changes layout guidance, not palette tokens.

Renderer tests establish component contracts and behavior. They do not execute UIKit text measurement or establish native gesture, keyboard or VoiceOver behavior.

## Native behavior checked

### Real hub, reconnect and install-over preservation

The isolated hub used fresh config/state/cache/tmp roots with inherited HOME and a clean process environment. Its provider pointed only to the local scripted LLM endpoint. The existing real hub and daemon executed a session and a follow-up through the native app. The scripted replies are labeled as such in the captures.

The app returned from Home to the same conversation and exact unsent draft. Transport observation showed the old socket disappear and a new socket connect in the same app process; this is reconnect evidence, rather than merely a retained screen. Sending a follow-up produced another real daemon turn through the scripted provider.

Installing the new native host over the existing app, without uninstalling or resetting it, retained the saved hub credential, open conversation and exact draft: `Retain this unsent native draft 7319.` The final JavaScript launch retained that same 37-character draft.

### Composer typing and Dynamic Type

The native checks exercised the installed React Native input, rather than injecting renderer callbacks:

| Regression or action | Before correction | Final native result |
| --- | --- | --- |
| Long soft-wrapped typing | An explicit height kept a 326-character draft at 44pt | Native intrinsic layout grows; a settled 960-character soft-wrapped draft reaches the 144pt cap |
| Expansion at cap | iOS could stop content-size callbacks, leaving no Expand action | Frame measurement exposes Expand at 144pt; the full editor measured 428pt and retained all 960 characters |
| Done and clear | Fixed-height feedback could keep stale bounds | Done retained all text; clearing shrank to 44pt and removed Expand |
| Existing draft, normal → largest → normal | Manually scaled font props left the field at 86pt until clear/retype | Native font scaling remeasured 44 → 257.333 → 44pt with the exact 37-character draft unchanged and native `IsEditing` retained |

The inline field keeps the same React identity and no explicit height. Native font scaling handles its base 17/24 text; scaled minimum and maximum constraints protect line containment and transcript space. Add and Expand keep bounded symbols inside 44-point targets.

These observations prove text and focus retention in the tested transition. They do not prove exact caret position or selection-range preservation.

### Native startup and links

Device-specific compilation initially failed because Xcode expected CoreSimulator 1174.9.2 while the installed framework was 1171.7. Generic simulator compilation succeeded. The original Expo host then exited because SDK 27 requires UIScene. The approved scene migration removed that launch blocker through source-controlled prebuild integration, not hand edits to generated files or falsified SDK metadata.

The pre-integration host compiled and launched, dismissed its splash, and connected to the real hub:

```sh
cd mobile-native
npx expo prebuild --platform ios --no-clean --no-install
LANG=en_US.UTF-8 RCT_METRO_PORT=8087 xcodebuild -quiet \
  -workspace ios/Evener.xcworkspace -scheme Evener \
  -configuration Debug -sdk iphonesimulator \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath ios/build build
```

LLDB observed the cold test URL in `RCTHost.launchOptions` and the warm URL at `RCTLinkingManager.application:openURL:options:`. Breakpoints were removed and the debugger detached. This verifies the native-to-React-Native boundary, not exactly-once JavaScript URL events, external universal-link association or OAuth.

Home/return exercises foreground recovery. It does not exercise OS scene eviction and root reattachment; that ownership path received source review only.

## Review and regression evidence

- **Retained-setting contrast:** whole-branch review found disabled opacity lowered supporting values to about 1.91:1 light and 2.50:1 dark. Direct shared-control tests and real shortcut-screen disconnect/reconnect tests failed before the correction and pass after it. The oracle includes ancestor opacity. Opaque supporting text measures about 7.38:1 and 8.64:1 respectively.
- **Visual corrections:** search inset and project-label reflow tests failed before their changes. The finish reviewer accepted corrected glyph containment, search placeholder/query states, complete project identity and recorded calibration provenance. Its verdict covers those scored fixes, not whole-product release acceptance.
- **Composer measurement:** independent review found the explicit-height feedback loop. Native reproduction confirmed it. Tests rejected the fixed height and missing cap action before the fixes. The native-scaling contract test failed with `false` instead of `true`, then all 32 Composer tests passed. Final source review found no remaining confirmed defect; it independently ran the four targeted suites above.
- **Retry-test diagnostics:** awaited React `act` around mount reconciliation and fake-clock retries removed 16 act warnings. The journal case now proves an intermediate failed timed retry before recovery. Two fixture debug logs were removed without suppressing production output or weakening assertions.
- **Simplification:** four independent angles completed. Duplicate Board-row vertical padding was consolidated. Reusing a contiguous-group helper was rejected because it would change model grouping. The final Composer review found no worthwhile further abstraction.

## Tooling limitations and remaining acceptance

- Native commands emitted the machine's `Install Failed: Authorization is required to install the packages` preamble even when the requested simulator operation exited zero and visibly succeeded. Native tooling output was not pristine.
- The idb bridge rejected two stale-element writes without changing the draft. The documented standard `--api ax` backend completed them. Queued HID typing was read only after all 960 expected characters arrived; an earlier bounded probe stopped at 691 while input was still queued.
- VoiceOver traversal, Large Content Viewer interaction, exact caret/selection retention, complete scrolling reachability, all gesture/motion states, landscape and a physical iPhone remain unverified.
- No full dark/large-text matrix exists beyond the captures listed above. Broader error, offline and active-work states have automated coverage, not complete native visual coverage in this pass.
- OS scene eviction/root reattachment, JavaScript exactly-once links, universal links, OAuth and uncertain-delivery recovery through installation remain unverified.
- Full web/Go gates, CI, distribution signing and TestFlight were not run for this native implementation and documentation change.

## Cleanup

Removed only the added `http://127.0.0.1:52514` hub through the app's normal Remove this hub action, which owns its credential and draft cleanup. The app returned to the empty saved-hub screen. Light appearance and normal text size were restored. The temporary Metro, hub, scripted provider and owned session daemon were stopped; private test state, credentials, logs, binaries, backup copy and automation scratch were removed. The installed app, native build artifact and the 22 repository captures remain. No production profile or app-wide data reset was used.

Integration note: main's newer open-at-latest behavior, Agents/Jobs activity surfaces and share support are retained. Current scene ownership is documented in [iOS build and distribution](ios-build-distribution.md); Expo owns the window and the generated AppDelegate supplies its React factory. The installed-template scene tests cover generated startup, unrelated plist preservation and repeated evaluation. They do not replace a native build or simulator run.

## Integrated automated verification

After integrating Expo 57.0.23 and removing the custom adapter, `make test-native` exited zero: 3 installed-template scene tests, 6,376 native tests, 769 shared tests, a cold iOS bundle from 2,495 modules, TypeScript, Biome and script-import checks passed. No React act warnings remained. The cold-cache warning, npm notice and Vitest performance advice remained informational.

The first integrated run exposed a camera-permission test reading the retired scene manifest from the local generated `ios` directory. Its corrected test evaluates the real Expo config against an empty native-project directory, with the installed fonts available. Permission assertions now check generated camera access and absent microphone access rather than pinning permission prose. The share-action and held-start tests also now own their asynchronous React updates; their existing behavior assertions remain.

Dependency installation reported 37 audit findings, including the inherited critical `shell-quote` 1.10.0 advisory [GHSA-pqg4-j6r4-53mv](https://github.com/advisories/GHSA-pqg4-j6r4-53mv). Both merge inputs locked that version. No dependency-audit remediation was made in this design change.

### Local branch-review corrections

RoboRev found two regressions in the integrated design:

- **Model-sheet scroll ownership:** five stacked effort choices could consume the medium sheet's non-scrolling header. Search stays pinned; effort choices and errors now use the model list's scrolling header. Tests at font scales 1, 2 and 3.1 each failed before the fix because none of the five choices belonged to the list, then passed after it. Model selection, effort changes and vision choices retain their behavior coverage.
- **Hub text selection:** grouped detail rows had replaced selectable `Copy` controls. Runtime values, agent names and paths, and MCP names now compose the existing selectable control inside grouped spacing. Thirteen runtime/MCP cases failed before the first repair; four adjacent identifier cases then failed before its extension. All 17 pass, checking native selection props, uncapped values, accessible values and machine-path typography.

The integrated focused run passed 137 tests across six files: `HubSettingsScreen.details`, `HubSettingsScreen.mcp`, `HubSettingsScreen.reconnect`, `session/ModelSheet`, `sheet/Grouped` and `newSession/ModelPicker`. Biome checked all five changed TypeScript files without fixes. These corrections were not followed by another full local gate; CI owns the final test gates. Renderer checks do not verify actual medium-detent gestures, the native copy menu or VoiceOver traversal.

The next local review found that a rejected model change could leave its error in the offscreen header. The sheet now requests a scroll to its error, placed before the growing effort choices, after each refused action. Twelve cases across font scales 1, 2 and 3.1 failed with no scroll request before the fix, then passed. They exercise real session controls and the conversation service against rejected wire requests, covering model, vision-model, vision-off and effort changes, repeated identical refusals and a successful retry. The native scroll command is observed at the renderer boundary, not executed by UIKit. Four affected suites passed 152 tests; touched-file Biome passed. Full gates remain CI-owned.

A subsequent review found shortcut Save errors below the growing introduction and nonselectable effective launch values. A bounded audit checked the same two patterns across all changed settings/model screens. It also identified misplaced Launch defaults errors and selection losses in overview values, shortcut previews, unknown-rule diagnostics and hub identities.

The two settings pages now put refusal feedback first in the scrolling content. Six normal/large-text tests failed because the first content was a heading rather than the error, then passed after the move. Shortcut validation uses per-refusal reports so the same invalid chord reveals its error again. Launch Save tests cover two refusals followed by a confirmed save and authoritative read-back. Data-bearing grouped text opts into native selection without changing ordinary action text. Nine selection cases failed before repair across the shared controls, Hub summary, shortcut editor/conflict diagnostics and Launch defaults. Four affected suites passed 129 tests, and touched-file Biome passed. These are renderer-boundary checks of content order, native props and scroll requests; they do not measure UIKit geometry or execute the copy gesture.

The scalar editor retains effective-value selection through the same grouped-row opt-in. Its existing offscreen-refusal gap is also corrected: each failed Done reveals the first content block through the ordinary page or model list. Nine cases failed before the fix. The final two-file check passed 22 tests, covering repeated parse/path/conflict refusals, success after correction, inheritance, draft/search preservation and stale validation replies after a connection change. These checks ran again after integration; no native-host or full-gate result is implied.

The combined settings correction passed 151 tests across six affected files. Biome checked all 11 touched TypeScript files without fixes. Full gates remain assigned to CI.
