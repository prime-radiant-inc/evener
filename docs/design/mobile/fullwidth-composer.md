# Full-width composer — MOB-011

The text input occupies its own full-width row at every text size. Send, Steer
and built-in command actions are on the controls row after model/reasoning.
Existing action handlers and dispatch guards are unchanged. The presentation
study is updated to show the same placement; the older density studies are
superseded by Jesse's MOB-011 correction.

Verification, 6 September 2026: native TypeScript, Biome and 228 tests pass.
Both Release builds succeeded. On Android with the actual software keyboard,
the input spans x=56..1026 in the 1080-pixel screenshot, up from x=56..815;
Submit shares y=1347..1473 with the attachment/model/reasoning controls. The
118-character fixture is readable over multiple lines. iOS visually shows
the full-width draft and controls-row Submit with the keyboard hidden.

Android submitted the fixture to the owned isolated SecondHub session. Both
apps received the running state, displaying Steer/Stop/Queue. A newer Android
draft remained in the full-width field; Stop from iOS ended the turn and
restored Send without discarding that draft. This uses the scripted provider
boundary and real Evener hub/session runtime, not production sessions.

All four screenshots were inspected. Android's running model label is heavily
truncated; its full accessible name remains available. iOS wraps Queue onto a
second controls line. Those states need further design review. iOS software
keyboard visibility was not established in this check: menu activation attempts
did not yield a keyboard in the captured simulator image. Do not count the
keyboard-hidden image as keyboard acceptance. Large text, long model labels,
empty drafts, slash-command layouts and screen-reader acceptance also remain
open, so MOB-011 is in progress.

![iOS full-width draft](assets/fullwidth-composer/ios.png)

![Android full-width draft with keyboard](assets/fullwidth-composer/android.png)

![iOS running controls](assets/fullwidth-composer/ios-running.png)

![Android running controls with keyboard](assets/fullwidth-composer/android-running.png)
