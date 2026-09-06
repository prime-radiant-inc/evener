# Native Markdown evidence

Observed 5 September 2026 on `cfadce32c` plus the two explicit quote/background and inline-code-border styles committed with this evidence.

## Builds and checks

Both release apps rebuilt and launched successfully: iPhone 17 Pro / iOS 26.5 and Pixel 7 / Android 35. The final Android APK SHA-256 is `592dcb5e02ad5c79620a0715ac8a8010d7787c0a09eed3b07401f1c10edc1f91`. Native tests: 48 passed in ten files. TypeScript, targeted Biome and diff whitespace checks passed. No whole-repository merge gate was run.

## Native observations

The scripted reading-reference hub on port 9198 serves [the committed conversation fixture](fixtures/conversation.md) through the shared conversation service. Both installed apps render headings, bold paragraphs, lists, quotes and syntax-colored code. Tables were visible on iOS and their row content appeared in the native snapshot. These are fixture observations, not production-session mutations.

After the native touch-target patch, Android reported the code-copy button at 126 by 126 pixels with physical density 420dpi: 48 by 48dp. Tapping it and pasting into the composer preserved all four code lines, including the deliberately long line. The test composer was cleared without sending. On iOS, long-pressing the code and choosing the native Copy action produced the same complete four-line clipboard value. iOS header measurement is patched to a minimum 44pt; direct hit-target measurement remains unverified.

Switching both running apps to dark appearance exposed the renderer default quote background `#F9FAFB`. Our style had overridden text and border but omitted background. Explicitly using the conversation background fixed the reproduced defect in both rebuilt apps. Inline-code border color now also uses the app border instead of the renderer's pink default.

[Android dark appearance](assets/markdown/android-dark.png) and [iOS dark appearance](assets/markdown/ios-dark.png) are screenshots of the final installed builds, visually inspected after the correction.

## Remaining acceptance work

This slice is not fully accepted yet. Verify whole-response copy, link actions, read-only task checkboxes, horizontal code/table scrolling, incomplete Markdown, streaming updates, large text, images/math and physical-device behavior. iOS snapshots expose code and table rows but omit ordinary prose despite it being visibly rendered; actual VoiceOver navigation must establish whether this is a snapshot limitation or an accessibility defect. Do not infer screen-reader support or measured fluency from the library API.

## Reading interaction and font-size follow-up

On the installed `2d73a9000` builds:

- Android horizontal dragging inside the table moved from the Situation column to What you see / What remains saved. Native bounds and visible cell content changed while the outer conversation remained at the same vertical position.
- Tapping the iOS host-file link opened the native destination dialog. Copy destination placed `/Users/jesse/git/prime-radiant/evener/mobile-native/src/draftDocument.ts:1` on the simulator clipboard exactly.
- The Simulator app accessibility tree exposed the prose, heading roles, links, quote and all table rows. The XcodeBuildMCP snapshot omission is not evidence that those elements are absent. Actual VoiceOver traversal is still required. Task-list elements were exposed as bullet points without checked-state values, which needs accessibility investigation.
- Android at font scale 1.5 reflowed the reading content and kept visible composer controls legible after reopening the conversation. Changing the system font scale recreated the activity and returned to Hubs. The generated MainActivity configuration-change list omits `fontScale`; native navigation restoration is not implemented. Preserve the selected hub/session through recreation before accepting lifecycle continuity.
- iOS at `accessibility-large` scaled Markdown and composer text. A live change clipped Latest/Send labels within their previous bounds; reopening the conversation recomputed correct button sizes. The Action component uses a minimum height, not a fixed height. This requires layout invalidation investigation rather than merely increasing a hardcoded button size or treating reopening as a fix.
- iOS table horizontal scrolling did not move under the attempted Simulator drag/scroll automation. This is not yet classified as a product defect: the native table uses an enabled horizontal RCTUIScrollView, but actual gesture delivery and offset changes need stronger runtime observation.

Evidence screenshots: [iOS live font-change defect](assets/markdown/ios-live-font-change-clipping.png), [iOS after reopening at large text](assets/markdown/ios-large-reopened.png), [Android reading at 1.5 font scale](assets/markdown/android-large.png). These capture the defects and limits, not release acceptance.
