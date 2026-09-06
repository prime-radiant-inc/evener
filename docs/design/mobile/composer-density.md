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
