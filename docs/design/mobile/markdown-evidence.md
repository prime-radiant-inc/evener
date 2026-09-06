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
