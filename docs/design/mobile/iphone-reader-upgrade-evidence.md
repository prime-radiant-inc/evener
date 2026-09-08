# iPhone reader upgrade and rich content evidence

The in-place iPhone 17 Pro simulator upgrade to native source `517f70ae3`
preserved every row in all seven draft tables: nine conversation drafts,
eight question drafts, three question positions and one creation draft.
The app reconnected to the existing authenticated hub. Exact source,
installed bundle and binary hashes and private before/after readbacks are in
[the receipt](assets/iphone-reader-upgrade-receipt.json).

A separate authenticated fixture supplied 12 completed turns through a real
Evener hub and daemon with a scripted external provider. On the installed
Release app, paragraphs, code, table rows and a link rendered in the reader.
The native code copy control returned the exact 52-byte code; long-pressing
the link and choosing Copy destination returned its exact 41-byte URL. The
prior simulator clipboard was restored. Latest moved to the final response
with the content clear of the composer. These are bounded native observations,
not a full rich-content or accessibility qualification.

![Fixture reader](assets/iphone-reader-rich.jpg)

![Latest response](assets/iphone-reader-latest.jpg)

The largest-text cold-launch check found a regression: the stored anchor
identified the final response in turn 12, while the settled viewport showed
turn 8. This reproduced on two cold launches. An initial candidate that
removed the intra-row offset from approximate scrolling did not resolve it;
its raw capture is retained separately. The follow-up below qualifies the correction.

Native code blocks expose a custom Copy accessibility action. Multiline links
use UIKit accessibility paths, so their zero rectangular AX frame alone does
not establish a focus defect. VoiceOver focus, speech and custom-action
activation still require direct qualification.


The correction in `7944778e0` lets restoration retry as rows become measured
and resets the failure budget when the measured window advances. The trace
showed that a fixed total failure limit stopped restoration while content was
still growing; unchanged geometry remains bounded. The final Release build
passed an installed launch and two independent cold launches at the largest
text size. All three retained the complete saved turn-12 anchor unchanged and
showed its table row at the exact pre-cold endpoint (`y=174.000651`). The raw AX
sequence associates that row with turn 12, avoiding confusion with repeated
fixture text. All 685 native tests and TypeScript passed.

![Restored largest-text reader](assets/iphone-reader-restored-largest.png)

This closes the reproduced cold-launch case. Wider rich-content, accessibility,
performance and final release-artifact qualification remain in the
[iOS v1 checklist](ios-v1-remaining.md).

The owned iPhone fixture profile was removed through the app, its reader anchor
was absent after cold launch, and all seven original draft tables remained
exactly equal to their pre-upgrade state. The original conversation and normal
text size were restored. All six owned fixture processes and listeners were
closed; temporary tokens and diagnostic trace keys were removed.
