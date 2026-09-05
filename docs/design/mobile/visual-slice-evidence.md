# Native visual slice evidence

5 September 2026, approximately 15:45–16:10 Pacific. Implementation: `10d959506c7d87525a575f310044bb51bbe21532`. This is a foundation slice, not completion of the native app or certification of accessibility/performance.

## Branch and verification

Rebased the mobile branch onto main `5e10d445f`. Retained `codex/mobile-before-ui-rebase-20260905` as a recovery reference. Temporarily stashed only the two existing Tauri Apple signing/configuration edits, then restored them unchanged. They remain uncommitted and separate from this work.

- Native Vitest: 24 tests across five files passed; TypeScript check passed after final source edits.
- Focused Go roster tests passed after rebase: `go test ./cmd/evener-hub -run 'TestHub(ThreadList|RPCThreadList)' -count=1`.
- Both-platform Expo export passed during the visual pass; final standalone builds subsequently packaged the committed source.
- iPhone 17 Pro, iOS 26.5: Release build/install/launch through XcodeBuildMCP. Android 35 Pixel emulator: Release build/install/launch with ARM64 architecture.
- Final Android APK SHA-256: `89856d2e724b9901f344c9da939860a7ccdd183966f4cb56da2832bcf6245528`.
- Whitespace checks pass. Frontend formatting was applied with two-space indentation. A broad Biome check also reports existing dependency-array diagnostics in the native screen lifetimes; their identity dependencies must not be removed as a cosmetic lint fix. No full merge-approval gate was run for this slice.

## Manual interaction

The Playground at port 9196 is a scripted WebSocket boundary. It verified saving a second hub, compact list navigation, sending, stopping, cross-device transcript display, and long composition on both platforms. Its capabilities intentionally omit steer and queue.

The Native E2E profile uses the real isolated Evener hub on port 56491 and the scripted provider on port 55873. Both platforms opened the same fixture session `034JlVxdyY11DDFNjgDibK`.

1. Expanded an existing read-file activity on iOS; input, output and duration remained available.
2. Temporarily held the test provider with an automatic resume guard so the running state remained inspectable. No production process was affected.
3. Sent `Visual slice iOS verification. Read NOTES.md.` from iOS; the draft cleared and running controls appeared.
4. Entered `Visual slice iOS steering.` and tapped Steer on iOS; the draft cleared.
5. Entered `Visual slice Android queued input.` and tapped Queue on Android; the queue increased from one to two.
6. Stopped from Android; both clients returned to idle controls while retaining the two queued messages. Resumed the test provider.
7. Used Latest and composed a long unsent draft on both platforms. The software keyboard and submit action remained visible together at default text size.

The test session's transcript and mutation records contain the verification strings. This supports receipt and cross-device control synchronization, not model compliance with the steering instruction. No production session was mutated.

VoiceOver/TalkBack runtime accessibility trees expose `You:` and `Evener:` attribution even though those labels are not repeated visually. This verifies the semantic labels, not a complete screen-reader task walkthrough.

## Problems found and addressed

- Android originally covered the composer when the software keyboard opened. The manifest already requested adjustResize; the React Native wrapper left Android behavior unset. Explicit `height` avoidance fixes the observed overlap. iOS retains `padding`. The same correction applies to hub entry and session creation; those two forms still need their complete large-text keyboard walkthroughs.
- Original visual changes removed spoken attribution, reduced the input hit area and doubled transcript spacing. Independent review caught all three; final code restores attribution, 44/48 input minimums and a single 24-unit separator.
- Fixed status chrome crowded the composer at large text sizes. Status/recovery details now scroll with the conversation. Compact recovery access remains by the composer and opens the detailed header after dismissing the keyboard.
- Latest remains reachable by the composer. A proposed scroll-only visibility flag was rejected because streamed content can grow without a scroll event.

## Remaining work

- **iOS live text-size change:** changing Dynamic Type while the app is open can leave native text measurement stale, visibly clipping text and buttons. Cold launch at the selected largest size wraps correctly. Root cause below the native text/React layout boundary is not yet established; do not claim this is solved. Preserve draft/focus while investigating rather than remounting the whole app.
- Android font scale 2.0 and dark appearance showed a reachable composer above the keyboard. iOS cold launch at largest accessibility text showed reachable controls; the retained iOS large screenshot has the software keyboard closed. The largest iOS keyboard-open combination still needs explicit final-source evidence.
- Android changing font scale caused an activity restart and returned to Hubs. Draft persistence across route exits/process or activity recreation is still missing and is the next reliability slice.
- Rich Markdown, readable internal notices, code/attachment rendering, structured approvals, queue inspection/cancel/promotion, roster pagination/search and the remaining capability map are unfinished. The current raw SYSTEM-REMINDER presentation is visibly unsuitable; preserve its information while designing readable disclosure.
- Physical-device frame pacing, gesture cancellation, screen-reader workflow coverage, reduced-motion behavior and distribution signing remain unverified.

## Images

The keyboard and running captures below use the committed build. Large/dark captures were collected during the same pass before the final Latest visibility simplification; they document geometry observations rather than exact final-build screenshots.

- [iOS long draft and software keyboard](assets/visual-slice/ios-keyboard.png)
- [Android long draft and software keyboard](assets/visual-slice/android-keyboard.png)
- [Android running controls and queue](assets/visual-slice/android-running.png)
- [Android 2.0 text, dark, keyboard open](assets/visual-slice/android-large-dark.png)
- [iOS largest text after cold launch, keyboard closed](assets/visual-slice/ios-large-dark.png)
