# Comic mobile viewport geometry

Run from `cmd/evener-hub/frontend`:

```sh
node scripts/layoutguard/run.mjs comic-mobile-viewport
```

The ordinary `layoutguard` command discovers this case, so it runs in
`make test-web-browser`. It mounts real `ComicPicker` and `ComicSession`
components with static store data and a never-connected real client, not mocked
network E2E. Empty and 50-row pickers run at 320, 390 and 1360px. Native CDP
wheel/touch gestures must expose the lower row and pagination to hit-testing;
a native click must select the exact lower-row ref. No programmatic scrolling
is used to reach the targets.

Headless desktop Chrome resolves `100vh` and `100dvh` equally, even with mobile
emulation. The case records those native dimensions, then resolves viewport-unit
tokens in the actual Comic stylesheet against a **modeled** 760px large viewport
and 620px dynamic viewport. The browser applies the resulting rules through its
real cascade. The root, composer, editor and submit control must fit within 620px,
with positive reader space and unchanged literal source slices. Removing the
model also checks native resolution.

The keyboard check replaces only `window.visualViewport` with a real
`EventTarget` while the layout viewport remains 620px. The fixture mounts the
production `useKeyboardInset`, not a copied calculation or manually set CSS
variable. Resize to height 440, scroll to offsetTop 40, pinch/unpinch and close
must place the mobile composer bottom at 440/480/620/480/620px. The editor and
Send must be contained and hit-testable, the reader must retain positive space
and recover its original height, and native text input must survive unchanged
alongside literal source slices. The 1360px desktop layout must not move. Native
viewport behavior is restored before the next picker check. `ComicApp.test.tsx`
separately mounts the real app and regular shell to check hook wiring and cleanup.

A separate mobile sequence opens at height 360, scrolls to offsetTop 40, and
closes. The editor and Send must remain contained and hit-testable within the
360/400px visible bottom, with the draft unchanged. The reader may collapse
while the keyboard is open; closing must restore its original height and literal
source slices. This supplements, rather than relaxes, the 440px positive-reader
checks above.

A separate safe-area sequence uses CDP `Emulation.setSafeAreaInsetsOverride` to
set the browser's actual `env(safe-area-inset-bottom)` to 34px. An independent
probe verifies the environment value; no production selector or padding rule is
replaced. At 320/390px the composer must end at 586px with the keyboard closed,
440/480px when opened/offset, and 360/400px with the shorter keyboard. Both Send
with a native-input draft and Stop with an empty active-thread composer must be
contained and hit-testable. Pinch/unpinch, partial close (12px keyboard inset),
and full close retain the larger clearance rather than adding the insets. Drafts,
literal slices and recovered reader heights remain checked. The short-open
reader may collapse; all closed states must restore it.

These are browser-API boundary simulations, not physical mobile browser chrome,
software keyboard, Safari or touch-inertia qualification. The 440px keyboard
boundary leaves space for the fixture's wrapped mobile headers and composer;
this does not qualify every keyboard size or phone orientation.

Before the repair, native wheel and touch could not scroll the mobile picker,
and the modeled session/composer ended at 760px. With dynamic minimum height and
a bounded picker scrollport, the same case passes at all three widths.
Removing the mobile session's keyboard padding leaves the composer at 620px,
the editor bottom at 559px and Send at 607px, below the 440/480px boundary.
With keyboard-only padding and a real 34px safe-area environment, the closed
composer still ended at 620px and both Send and Stop at 607px, below the 586px
safe boundary. The same assertions pass with the larger-inset reservation.
