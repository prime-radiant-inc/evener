# Motion

How the hub web UI moves, and why. This is the reference for anyone adding or changing an
animation or transition in `cmd/evener-hub/frontend/src`. The tokens live in
`src/styles/tokens.css`, the shared recipes in `src/styles/motion.module.css`, and
`src/styles/motion.contract.test.ts` enforces the rules that can be checked mechanically.

## Philosophy

Motion in Evener explains something. It never decorates.

Evener is a tool people leave running while agents work. Most of the time the screen should
be still, so that when something does move the eye can trust it. Every animation has to earn
its place by doing one of four jobs:

1. **Acknowledge.** The control heard you: a hover tint, a press that gives under the finger.
2. **Orient.** A surface shows where it came from and where it went: a drawer arrives from
   the edge it lives on and leaves the same way.
3. **Connect.** One object stays one object across a change: the selected thumb travels to
   its new option instead of vanishing and reappearing.
4. **Signal.** The agent's state really changed: a session crosses into needing you, or it
   is working right now.

If an animation does none of these, it does not ship.

Six principles follow from that.

- **Nothing moves on its own.** Motion answers the user's action or a real change of state.
  Data arriving (a new transcript row, a job finishing, a count changing) never animates
  geometry; the attention colors carry that news. The only continuous animations are the
  three named under [Continuous motion](#continuous-motion).
- **Fast, and never in the way.** No animation is longer than the spatial budget. Nothing
  waits for an animation to finish before accepting input, and every animation can be
  interrupted by the next action.
- **What arrives with motion leaves with motion.** An overlay that fades in fades out. The
  exit is quicker and plainer than the entrance: no overshoot, about two thirds of the
  time. The user has already decided to move on.
- **One vocabulary.** There are six recipes. Every animated thing uses one of them, with the
  recipe's tokens. Two things that behave the same way move the same way.
- **Cheap properties.** Animate `transform`, `opacity` and color. Layout properties
  (`height`, `width`, `top`, `margin`, `padding`) are animated only by the three geometry
  surfaces listed under [Spatial](#5-spatial).
- **Reduced motion is honored everywhere.** Under `prefers-reduced-motion: reduce` every
  transform, fade and scroll animation becomes an instant change to the end state. Color
  responses on hover and focus may stay, because they do not move.

A little whimsy is welcome where it is also information: the press that gives, the overlay
that lands with a slight overshoot, the thumb that slides. Whimsy that is only decoration
is not.

## Tokens

| Token | Value | Use |
|---|---|---|
| `--motion-duration-press` | 90ms | A held control giving under the pointer |
| `--motion-duration-exit` | 80ms | A floating layer leaving |
| `--motion-duration-overlay` | 120ms | A floating layer arriving; a revealed body fading in |
| `--motion-duration-hover` | 150ms | Hover, focus and selection responses; small moves |
| `--motion-duration-spatial-exit` | 180ms | An edge-attached surface leaving |
| `--motion-duration-attention` | 200ms | A state crossing into needs-you |
| `--motion-duration-spatial` | 240ms | An edge-attached surface arriving; user-initiated geometry |
| `--motion-duration-activity` | 800ms | One turn of the working ring |
| `--motion-easing-standard` | `ease-out` | Everything, unless a row below says otherwise |
| `--motion-easing-pop` | `cubic-bezier(0.2, 0.9, 0.3, 1.15)` | A floating layer's entrance only |
| `--motion-easing-exit` | `ease-in` | Every exit |
| `--motion-scale-from` | 0.96 | The scale a floating layer grows from and shrinks to |
| `--press-scale-control` | 0.97 | How far a pressed button gives |
| `--press-scale-row` | 0.985 | How far a pressed row gives |

Durations and easings in CSS and in TypeScript come from these tokens. A raw duration or
easing is allowed only in the continuous animations, which are listed by name in the
contract test.

## The six recipes

### 1. Respond

A control's color answers hover, focus, selection and disabled state.

- Properties: `color`, `background-color`, `border-color`, `box-shadow`, and `opacity` for
  an affordance that is revealed on hover (row actions, a copy button, a resize handle).
- `--motion-duration-hover`, `--motion-easing-standard`, both directions.
- Transitions name their properties. `transition: all` is never used.
- Not gated by reduced motion: nothing moves.

### 2. Press

A held control scales down a touch and springs back on release.

- `transform: scale(...)` only, `--motion-duration-press`, `--motion-easing-standard`.
- `--press-scale-control` for buttons, icon buttons, menu triggers and status-bar counters.
  `--press-scale-row` for wide rows: list rows, tree rows, palette rows, tiles.
- `transform` sits in the control's one base `transition` list. Only the `:active` scale
  goes inside `@media (prefers-reduced-motion: no-preference)`.
- Never on a disabled or refused control, and never on a row while a control nested inside
  it is the thing being pressed.

### 3. Float

A layer that floats above the page near what opened it: Dialog, ConfirmDialog, the command
palette, Popover, Menu, HoverCard, the selection-quote bubble, Tooltip, Toast, hold hints.

- Enter: opacity 0 to 1 and scale `--motion-scale-from` to 1, `--motion-duration-overlay`,
  `--motion-easing-pop`.
- Exit: opacity 1 to 0 and scale 1 to `--motion-scale-from`, `--motion-duration-exit`,
  `--motion-easing-exit`.
- Small text-only layers (Tooltip, hold hints) fade without the scale, on the same
  durations, with `--motion-easing-standard` in and `--motion-easing-exit` out.
- A scrim fades with its layer: opacity only, the layer's own durations.
- The transform origin is the side the layer is anchored to, so it grows out of its
  trigger.
- A Menu on a phone is a bottom sheet in spirit and uses the fade without the scale.

### 4. Slide

A surface attached to an edge of the screen: Sheet (left, right, bottom), the Sessions
drawer, the Overview sidebar, and a phone's pane-to-pane navigation.

- Enter: translate in from the edge the surface lives on, `--motion-duration-spatial`,
  `--motion-easing-standard`.
- Exit: translate back out to the same edge, `--motion-duration-spatial-exit`,
  `--motion-easing-exit`.
- The distance is the surface's own size (`100%`), never a pixel constant.
- Phone navigation: going deeper, the new pane slides in from the right over the old one.
  Going back, the top pane slides out to the right and reveals the pane beneath. Switching
  session from the drawer is not a push and does not slide.
- A sheet's scrim follows recipe 3's scrim rule on the sheet's durations.

### 5. Spatial

User-initiated geometry: something the user resized, folded or opened changes shape in
place. These are the only places a layout property is animated.

- The composer folding and unfolding (height).
- An expandable bottom sheet growing to full height (height).
- A zoom column opening, or narrowing into its spine (width).
- `--motion-duration-spatial`, `--motion-easing-standard`, both directions.
- Spatial motion runs through the `src/motion` wrapper, which reads the tokens. Components
  import the wrapper, never the library.
- Never triggered by data. If the change was not the user's own action it is instant.

### 6. Move

One small object travels to a new place and stays itself.

- The SegmentedControl thumb sliding to the chosen option; the Switch thumb crossing its
  track; a chevron turning to point at what it opened.
- `transform` only, `--motion-duration-hover`, `--motion-easing-standard`, both directions.
- Gated by reduced motion: the object is simply in its new place.

## Reveals

Opening a disclosure, a tool row or a folded run shows a body that was not there.

- The body fades in: opacity only, `--motion-duration-overlay`, `--motion-easing-standard`.
- Closing is instant. The body's space closes at once, and a fade over a collapsing box
  would lag the layout it belongs to. The chevron (recipe 6) carries the close.
- The body never slides and its height is never animated: its size belongs to the content.

## Signals

- **Attention onset.** A status color changing as a session crosses into needs-you
  (StatusDot, Cadence, a just-picked wash): color only, `--motion-duration-attention`,
  `--motion-easing-standard`.
- **Arrivals that ask for the user.** The ask dock, a toast, a banner: these are layers and
  follow recipe 3. Rows arriving in the transcript, pending-message stack or lists do not
  animate.

## Continuous motion

Three animations may loop, and only these:

| Animation | Where | Meaning |
|---|---|---|
| The working ring | `StatusDot` in its `working` state | The agent is working right now. Stops the moment the status authority stops reporting work. |
| The pulsing grid | `Loader`, including the transcript's content-free "Thinking…" row | A wait the user is watching. |
| The streaming caret | The end of text that is still streaming | More text is coming. |

Each stops rendering its motion under reduced motion and shows a static equivalent. Their
keyframes are defined once each. Idle pulses, shimmer on live data and anything that
invents progress stay banned.

## Scrolling

- Scrolling caused by data (new rows, restoring a position, following the stream) is
  instant.
- Scrolling the user asked for across a short distance, such as revealing the selected
  session in the rail, may be smooth, and is instant under reduced motion.
- Jumps across a long transcript (jump to bottom, next needs-you) are instant: a smooth
  scroll over thousands of rows is a delay, not an explanation.

## How it is built

- **Recipes are composed, not copied.** `src/styles/motion.module.css` defines each
  recipe's keyframes and classes once. A widget `composes:` the recipe and adds nothing but
  its own transform origin.
- **Exits need presence.** A layer stays mounted while it leaves. The one presence helper
  in `src/widgets/internal` keeps a closing layer in the document, marks it with
  `data-motion="closing"`, and removes it when its exit animation ends. It removes the layer
  at once under reduced motion, and a timeout bounded by the exit token removes it if the
  animation event never fires. A closing layer does not take focus, does not trap focus,
  and ignores the pointer.
- **Reduced motion.** Each stylesheet that declares a transform or opacity animation gates
  it. The contract test fails a stylesheet that animates without a gate.
- **TypeScript.** Code that needs a duration reads it from the token through `src/motion`.
  `useReducedMotion` from that wrapper is the one way to ask about the preference.
- **Third-party chrome.** Dockview ships its own transitions for sashes and drop targets.
  They are outside these recipes and are left as the library sets them.

## Checklist for a change

1. Which of the four jobs does this motion do? If none, remove it.
2. Which recipe is it? Use that recipe's tokens and compose its class.
3. Does it leave the way it arrived?
4. Does it animate only transform, opacity or color? If it animates layout, is it one of
   the three spatial surfaces?
5. What happens under reduced motion?
6. Could it ever run without the user acting or the agent's state changing? Then it is
   idle motion.
