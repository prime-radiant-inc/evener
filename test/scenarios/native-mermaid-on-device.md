# native-mermaid-on-device: verify inline mermaid diagrams on a real iOS and Android device

**What this covers**: the class of mermaid-inline-diagram behavior that
vitest cannot observe — a `react-native-webview` hosting the bundled,
lockdown-CSP page. The unit suites already pin the lockdown props, the
`ready`/`height`/`error` message protocol, height flow, and the
accessibility-action wiring (`mobile-native/src/MermaidDiagram.test.tsx`,
`mobile-native/src/mermaidPage.test.ts`); this card is the manual runbook
the design spec names for the rest. See
`docs/superpowers/specs/2026-09-29-mermaid-inline-diagrams-design.md`
"Testing" ("Native real render: manual runbook on device") and
"Security (web)" for the page's CSP contract.

This card is **not agent-run**: it needs a physical iPhone and a physical
Android phone, and repeated app switching that a simulator or a browser
cannot stand in for. Record the source revision (`git rev-parse HEAD`),
the installed artifact (TestFlight build number, or a dev build from the
same revision), and the two OS versions at the top of the run.

**Pre-state**:

- A physical iPhone and a physical Android phone, each running a build
  whose source matches the recorded revision. iOS ships through
  TestFlight (`docs/design/mobile/ios-build-distribution.md`); Android
  runs a dev build from the same revision (`npx expo run:android` or an
  installable APK). The simulator does not count — it has no TalkBack,
  no WebKit-vs-Chromium difference, and no real re-layout under a finger.
- One hub reachable from the phone, and one agent session whose
  transcript contains every shape this card needs. Ask the agent, in the
  session, to emit them, then leave the session loaded:
  - a short valid diagram — ```` ```mermaid ```` with `graph LR; A-->B`;
  - an invalid diagram — ```` ```mermaid ```` with `graph LR; A--`
    (truncated edge);
  - a prose-only agent message;
  - a mixed message (prose around a valid diagram);
  - a diagram-only agent message (the fence is the whole message);
  - a message whose prose uses a reference-style link definition
    (`[label][ref]` with `[ref]: https://example.com` on the *other*
    side of a diagram) — for the step below.
- Enough messages after the first diagram to scroll it fully off-window.

## Steps

1. **The page renders under `default-src 'none'` on both platforms.**
   Open the session on each phone and let the valid diagram draw.
   Expected: the diagram renders in the app's palette. If the source
   block or *"Couldn't render this diagram."* appears instead, or the
   region stays blank/white, the page failed under its CSP meta — this
   is the item the spec calls the largest residual risk. Then put the
   phone in airplane mode, hard-kill the app, and reopen the session:
   the diagram must still draw, because the page is one inlined bundle
   and fetches nothing (`mobile-native/scripts/build-mermaid-page.mts`
   inlines mermaid + DOMPurify and emits the CSP meta).

2. **A theme switch re-renders mounted diagrams.**
   With a diagram on screen, flip system appearance (Light ⇄ Dark) while
   the diagram stays mounted. Expected: the diagram recolors to the app
   palette in place — no blank frame, no reload, no scroll needed. If the
   colors stay stale, the palette effect did not re-post
   (`mobile-native/src/MermaidDiagram.tsx`, the `useEffect` keyed on
   `colors.palette`). Repeat from the app's Display setting if it changes
   the palette independently.

3. **The error fallback shows source + note for invalid mermaid.**
   Scroll to the invalid diagram. Expected: the mermaid source in a code
   font, with *"Couldn't render this diagram."* beneath it. If you see a
   blank box, a half-drawn diagram, or a redbox, the page's `error` reply
   did not land on the row. Scroll away and back: the fallback must stay
   a fallback (a repost of the same failing source fails the same way).

4. **Tap-to-open reaches the fullscreen viewer.**
   Tap anywhere inside the inline diagram's region. Expected: the
   fullscreen viewer opens. The inline WebView is wrapped in
   `pointerEvents="none"` for exactly this — a tap must land on the
   Pressable (`testID="mermaid-open"`), never be swallowed by the
   WebView. If the tap does nothing, or scrolls the transcript, the
   pointer-events layer is gone. Try the diagram's center and its edges.

5. **VoiceOver (iOS) / TalkBack (Android) actions.**
   With the screen reader on:
   - Focus a prose-only agent message. Expected: the message's actions
     are present — *Copy*, *Quote in reply*, *Select text*.
   - Focus the diagram-only agent message. Expected: those same three
     actions **plus** *Open fullscreen*. The inline Pressable is
     `accessible={false}`, so the appended `"Open fullscreen"` action on
     the diagram wrapper (`mobile-native/src/MermaidDiagram.tsx`,
     `OPEN_ACTION`) is the only way a screen-reader user can open the
     viewer.
   - Invoke *Open fullscreen* from the actions/rotor. Expected: the
     viewer opens.
   If a prose segment loses the message actions, or the diagram-only
   message has no *Open fullscreen*, fail this step. VoiceOver and
   TalkBack surface the actions differently (rotor vs. actions menu), so
   run both.

6. **FlatList recycling keeps the height-cached placeholder.**
   Scroll a mounted diagram fully off-window, then back. Expected: the
   row reappears at its cached height and re-renders with **no layout
   jump** — it must not collapse to the 120pt placeholder and then snap
   open. The height cache is keyed by the full mermaid source
   (`mobile-native/src/MermaidDiagram.tsx`, `heightCache`); a first-ever
   render of new content may briefly show the placeholder, but a recycled
   diagram you have already seen must not.

7. **The fullscreen viewer: zoom, source toggle, copy.**
   In the open viewer:
   - the diagram fills the screen and pinch-to-zoom changes its scale
     (the host posts the `mode: "zoom"` render message, and the page
     rewrites its viewport meta in response to allow scaling);
   - *Show source* reveals the mermaid source in a code font; *Show
     diagram* returns to the diagram;
   - *Copy source* puts the exact mermaid source on the clipboard (paste
     it somewhere to confirm it is the source, not the rendered text);
   - *Done* closes the viewer, and reopening starts on the diagram (the
     source toggle resets on close).

8. **A link definition across a diagram still resolves (md4c).**
   The spec assigns md4c segment resolution to this runbook. On the
   mixed message from Pre-state, tap the reference link in the prose
   segment. Expected: it resolves to the definition on the other side of
   the diagram — the native splitter prepends the message's link
   definitions to each prose segment (`mobile-native/src/markdownSegments.ts`).
   If the link is dead or shows the raw `[ref]`, the definitions were not
   prepended.

## Expected

Every step above passes on **both** an iPhone and an Android phone. A
step that passes on one platform and fails on the other is a failure: the
page listens for `postMessage` on `window` (iOS) *and* `document`
(Android) precisely because RN delivers them differently
(`mobile-native/src/mermaidPageContent.ts`, `bootstrapMermaidPage`).

## Cleanup

Close the viewer, restore the system appearance to what it was, and
force-quit and relaunch each app once to confirm the session reloads and
the diagrams re-render with no crash. Nothing on the hub side changes;
the session can stay if you want it for the next run.

## Sharp edges

- **The pointer-events layer is load-bearing.** The tap in step 4 only
  works because the inline WebView sits under a `pointerEvents="none"`
  view. A future change that drops that wrapper silently kills
  tap-to-open on device while every unit test stays green.
- **The theme effect's dependency is the palette object.** A re-render
  that produces an equal-but-new palette object re-posts the render; a
  flip that does not change the palette identity will leave a diagram
  stale. Step 2 is the only place this shows.
- **The height cache is keyed by source, not by position.** A diagram
  whose source is byte-different in a recycled row will not hit the cache
  — that is expected; the check is that a *previously seen* source does.
- **A first render under airplane mode still needs the bundle.** If
  step 1's airplane-mode leg is blank, the page is loading something
  external and the CSP/lockdown contract is broken.
- **Simulator evidence does not close this card.** Accessibility
  actions, pinch-zoom, and real touch routing differ on a physical
  device; record the hardware.
