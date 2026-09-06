# Composer density

This layout follows the existing style guide and Jesse's requirement that
model and reasoning live inside the composer. It changes placement, spacing,
and transcript navigation presentation. The existing capability gates,
submission callbacks, uncertain-delivery protection, and settings semantics
are retained. No old-mobile behavior establishes a feature requirement.

The draft and primary action share the first row. Model and reasoning remain
separately tappable in the footer, alongside attachment access and available
Stop/Queue actions. Latest becomes an accessible arrow over the transcript;
the transcript has bottom padding so its last line can clear that control.
The same submission renderer serves both placements to preserve gating.

## Verification

- TypeScript and touched-file Biome checks pass; all 223 native tests pass.
- Rebuilt and launched iOS and Android Release apps with the final layout.
- iOS: model sheet opened from the footer and dismissed with the draft intact;
  sending reached isolated Hub B; Latest scrolled to the submitted message.
- Android: software keyboard leaves the active-turn input, Steer, model,
  reasoning, Stop, and Queue reachable. Stop ended the iOS-started turn and
  iOS returned to Send with its separate unsent draft preserved.
- At the tested default sizes, active-turn settings and secondary actions
  occupy one footer row on both platforms. Existing 44/48-unit action regions
  remain in use. This is observed layout evidence, not a full accessibility
  or device-matrix certification.

Inspected captures: [iOS idle](assets/composer-density/ios-idle.png),
[iOS running](assets/composer-density/ios-running.png), and
[Android with keyboard](assets/composer-density/android-keyboard.png).

Large text, long drafts, long model names, iOS software-keyboard geometry,
light mode, screen-reader order, and command-specific action widths require
further native checks. Raw transcript notices and overall conversation
hierarchy remain unfinished. These screenshots do not establish final visual
acceptance.

## Large-text correction and native keyboard checks

Testing at iOS's largest accessibility content size exposed clipped custom
Work/Session navigation buttons in the fixed-height native bar. Sessions and
conversation headers now use the installed native-stack library's native iOS
bar-button items. UIKit owns their sizing; Android retains its existing header
rendering. Callbacks and disabled conditions are shared between presentations.
The native Work button was exercised and opened the expected Tasks/Activity
menu after this change.

Above font scale 1.4, the draft takes the composer width and its primary action
moves beside attachment access before the settings row. Normal-size placement
is unchanged. Latest uses a fixed-size arrow with its accessible name and
44/48-unit touch region, rather than scaling a decorative glyph as prose.

Both final Release apps were rebuilt and launched. TypeScript, touched Biome,
and all 223 native tests pass. Native captures were inspected with long drafts
and software keyboards visible: iOS at accessibility-extra-extra-extra-large,
Android at font scale 2.0. Draft, Send, attachment, model, and reasoning remain
reachable. The 426-character iOS draft remained intact across relaunch and
text-size changes; Android's long paragraph wrapped inside its bounded input.
No test draft was submitted in this pass. Simulator text settings were restored
to iOS large and Android 1.0 afterward.

Captures: [iOS largest size with keyboard](assets/composer-accessibility/ios-largest-keyboard.png)
and [Android large text with keyboard](assets/composer-accessibility/android-large-keyboard.png).

This resolves the observed header clipping and narrow large-text input. It
does not certify active-turn or slash-command layouts at extreme sizes,
multiline editing/selection gestures, screen-reader focus order, all model
label lengths, light mode, or smaller physical devices. The large iOS input
is intentionally a scrolling viewport and does not show its entire draft at
once. Further accessibility and visual acceptance remains required.
