# Native Apple redesign verification

The iPhone redesign passes source-level verification. Native visual acceptance remains open: the installed iOS 27 runtime rejects the generated app's lifecycle before it displays the interface.

## Candidate and scope

- Checked on 2026-10-19 UTC.
- Source: `3653674537b19e6c62b5102df37b1197eb7b0778` on `wip/native-apple-design`.
- Base: `34d4ea239ec6c192838b544d6c4dd4e7f70c04a1`.
- Native artifact: the Debug simulator host at `mobile-native/ios/build/Build/Products/Debug-iphonesimulator/Evener.app` compiled during implementation. It did not launch successfully and is not a visually accepted final-candidate artifact.
- Backend: no real-hub end-to-end journey ran. A scripted demonstration hub was started for inspection, then stopped after the launch failure.
- No publication, main-branch merge, deployment or TestFlight upload was performed.

This record supplements [iPhone acceptance](acceptance.md). Historical simulator and TestFlight receipts do not qualify this candidate.

## Implemented surfaces

| Area | Source changes |
| --- | --- |
| Shared controls | Crisp neutral light/dark surfaces, sentence-case 15/20 section headings, adaptive segmented choices, consistent selection marks and 44-point search actions |
| Board | Clearer title/context/age hierarchy, large-text reflow, consistent section headings and unchanged inline project navigation |
| Conversation | Separate title and live-jump actions, protected navigation targets, a 44-point resting composer and viewport-aware expansion without replacing its input |
| New session and model sheets | Readable provider headings and a consistent leading check slot for selected and unselected choices |
| Hub administration | Grouped settings, consistent value summaries and scalar editors, and Save outside the scrolling form |
| Retained settings | Disabled rows and inputs remain readable while editing is unavailable; native disabled semantics and retained drafts remain intact |

The design record is [mobile-native/DESIGN.md](../../../mobile-native/DESIGN.md), with its [Impeccable sidecar](../../../mobile-native/.impeccable/design.json). [The shared web guide](../../web-ui/design-system.md) distinguishes platform palettes while preserving common semantic colors and hierarchy. Web implementation was unchanged.

## Checks completed

Run from the repository root:

```sh
make test-native
```

The final run exited zero after the last source cleanup. The pre-commit formatter subsequently reported no changes.

| Check actually run | Result |
| --- | --- |
| Cold Metro iOS export | Produced an iOS bundle from 2,476 modules |
| Native Vitest suite | 376 files, 6,277 tests passed |
| Shared mobile Vitest suite | 6 files, 762 tests passed |
| Native TypeScript check | Passed with `tsc --noEmit --project tsconfig.check.json` |
| Native/shared Biome gate | Passed, 816 files checked |
| Native script import resolution | Passed |
| Working and staged diff checks | Passed |
| Design record verification | All 50 recorded palette values matched source; sidecar JSON, schema version and canonical color mappings checked |

These checks include Board actions and ordering, conversation send/recovery behavior, draft retention, administration save guards, and component accessibility/layout contracts. They do not prove native text wrapping, hit geometry or visual composition.

## Review and regression evidence

An independent whole-branch review found one important regression: newly grouped retained settings inherited disabled opacity. Supporting values fell to about 1.91:1 in light mode and 2.50:1 in dark mode.

New tests failed before the shared correction and passed afterward. They cover disabled Row, SwitchRow and TextFieldRow content in both themes, plus the real shortcut screen retaining its values through disconnect and reconnect. The contrast oracle accounts for native ancestor opacity. The reviewer independently confirmed the correction and its oracle; opaque supporting text now measures about 7.38:1 and 8.64:1 respectively.

Four independent simplification angles completed. The actionable finding was duplicate Board-row vertical padding, consolidated without changing layout. The proposed reuse of a contiguous-group helper was rejected because it would change model grouping. No efficiency or abstraction-level changes were needed.

The Impeccable finish reviewer requested native captures and withheld visual approval. No current-source native screenshots were accepted.

## Native launch blocker

Device-specific compilation initially failed because Xcode expected CoreSimulator 1174.9.2 while the installed framework was 1171.7. Generic simulator compilation succeeded with:

```sh
cd mobile-native
LANG=en_US.UTF-8 RCT_METRO_PORT=8087 xcodebuild \
  -workspace ios/Evener.xcworkspace -scheme Evener \
  -configuration Debug -sdk iphonesimulator \
  -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath ios/build build
```

Installation and the launch request succeeded, but the application exited. The simulator reported:

> Application failed to launch: UIScene life cycle is required for apps built with this SDK.

The only installed Xcode provides SDK 27.1. The Expo 57.0.20 / React Native 0.86.3 generated host creates its window in AppDelegate and has no scene delegate. Investigation found no supported configuration-only switch to repair this. No SDK metadata was falsified and no system toolchain or native lifecycle was changed.

## Remaining acceptance

- Inspect the running current-source iPhone app in light, dark and large text: Board, conversation, New session, Hub, Hub settings, Launch settings, keyboard shortcuts and scalar editors.
- Confirm actual text containment, touch targets, focus/keyboard behavior, native materials and VoiceOver interaction. Submit those captures for finish review.
- Use a compatible toolchain/runtime, or separately scope a genuine scene-lifecycle migration. Neither alternative has been launch-verified here.
- Run any required current-backend journey and distribution checks against the exact final native artifact.
- iPad and Android delivery remain deferred. Full web/Go gates and CI were not run for this native-only implementation and documentation change.

Integration note: this branch leaves `mobile-native/src/readerPosition.ts` unchanged from its base. Local main already contains the separate open-at-latest change (`a7c4015de0`, PR #4738). Preserve that newer behavior when integrating this branch; the redesign does not supersede it.
