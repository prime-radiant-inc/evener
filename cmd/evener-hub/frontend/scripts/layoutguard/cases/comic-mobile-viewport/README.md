# Comic pane viewport geometry

Run from `cmd/evener-hub/frontend`:

```sh
node scripts/layoutguard/run.mjs comic-mobile-viewport
```

The ordinary `layoutguard` command discovers this case in `make test-web-browser`.
It mounts the real `ComicConversationBody`, a retained composer source and real
`PaneScaffold`, inside the production AppShell CSS height chain. The static thread
model and never-connected real client make this geometry evidence, not network
E2E or mode-entry proof. `comicguard` owns the real menu and network journey.
A 24px host header ensures the Comic pane must fit its allocation rather than
claiming the whole viewport. The standalone Comic picker was removed from the
product, so its empty/50-row scrolling and pagination assertions are retired.
Native CDP wheel/touch now move the actual Comic transcript and must record
trusted input, changed scroll position and a retained away anchor.
Before stopping the touch fling, a separate early Appearance click checks all
choices, Escape/focus return, exact draft/reader height, and the retained source
identity/offset against an independent DOM Range. Its current viewport offset
must match that Range exactly, including if prior native movement continues.
Draft setup selects and replaces the editor content through native input.

A native stationary touch then stops the previous fling before the independent
keyboard geometry matrix. Trusted pointer/touch start and completion, matching
IDs and no remaining touches are required. Waiting for the first `scrollend`
would be insufficient: reader restoration can emit it while inertia continues.
The causal regression was a source save before Appearance pointerdown, with no
JavaScript scroll writer; the native-stop control preserved the exact source.
No sleep, reader-settlement wait or source-equality tolerance is added.

The entire matrix runs twice: bundled fonts, then network-blocked font requests.
The second run must report `data-comic-font=unavailable` and a visible real
fallback notice, not a test-inserted substitute. Both runs hit-test Appearance,
Latest and the single Session actions menu. The real Appearance popup contains
all View/Scene/expression choices at phone and desktop sizes. Opening/dismissing
it must preserve draft, saved source and reader height, return focus, and keep
every choice reachable within the viewport, including 360px keyboards.
Activation uses one native pointer sequence on a stable, hit-tested trigger.
Failures report the matrix stage, trigger/popup geometry and current focus.

Headless Chrome resolves `100vh` and `100dvh` equally, even with mobile emulation.
The case records those dimensions, then resolves viewport-unit tokens in the
actual **shell** stylesheet against a modeled 760px large viewport and 620px
dynamic viewport. The real cascade must contain the pane, composer, editor and
Send within 620px, with positive reader space and unchanged literal slices.
Removing the model checks native resolution again. Comic itself must fit its
host instead of owning viewport height.

The keyboard check replaces only `window.visualViewport` with a real
`EventTarget` while the layout viewport stays 620px. The production
`useKeyboardInset` and AppShell CSS handle resize, scroll, pinch and close.
At 320/390px, a 440px keyboard viewport, 40px offset, pinch/unpinch and close
must put the pane footer at 440/480/620/480/620px. Editor and Send remain
contained and hit-testable. Native-input drafts and literal source slices
survive, the reader retains positive space and restores its original height.
At 1360px the desktop layout must not move. Native viewport behavior is restored
before the next width.

A separate 360px keyboard sequence also requires a full rendered text line, reachable
input and restoration on close. The scaffold footer owns clearance. Mobile
places the existing Session actions menu beside the composer's You heading,
alongside its long liveness status, while desktop retains its separate footer
menu/status. View/Scene/expression are behind Appearance at every width;
history/Latest and Send/Stop remain immediate.

CDP `Emulation.setSafeAreaInsetsOverride` supplies an actual 34px browser
`env(safe-area-inset-bottom)`, independently measured by a probe. The footer's
content edge must end at 586px when closed and 440/480px or 360/400px when the
keyboard covers the safe area. Both Send with a native-input draft and Stop with
an empty active-thread composer remain hit-testable. Pinch/unpinch, partial close
with 12px keyboard inset and full close preserve the union of the two insets,
not their sum. Drafts, source slices and recovered reader heights stay checked.

These are browser-API simulations, not physical Safari, software-keyboard,
mobile toolbar or touch-inertia qualification. `case.json` records the measured
integrated-pane collapse before compact spacing/menu placement, not obsolete
standalone picker mutation coverage.
