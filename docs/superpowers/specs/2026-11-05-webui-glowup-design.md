# Web UI glow-up — design and delivery spec

Status: approved by Jesse 2026-11-05. Branch `webui-glowup`.

## Intent

The Impeccable critique scored the web hub 26/40. The transcript was authored
for Evener; the shell around it was interchangeable. Accent blue carried the
primary CTA, the user's own bubble, the selected settings row, links and every
focus ring. The warm paper was 3-9 RGB points from white, so it read neutral.
The display ramp differed by two points at each step, so a page title did not
look like one.

Jesse chose **visual identity first, then the rest**, with **everything** in
scope.

## Approaches

- **Keep the neutral shell and change only type and states.** Lowest risk, but
  leaves the shell interchangeable and the "warm paper" invisible.
- **Make the editorial world visible (chosen).** Warm the ground, make the
  primary action ink on paper, spend accent once, and give the ramp real display
  steps. Same interaction law and widget API.
- **Replace the visual world.** A new palette, type pairing and surface system
  across every surface. Larger than the critique's finding and it would discard
  an authored transcript.

## Goals

- Make the editorial world visible in the frame without adding decoration.
- Spend accent once: focus, links, selection. The primary action is ink on
  paper; the user's own words are a paper slip.
- Give the type ramp real display and section steps, and use a UI face with a
  point of view.
- Name attention and completion instead of encoding them only in dots and
  counts.
- Bring the browser's own surfaces (selection, caret, scrollbars,
  theme-color) into the palette.
- Fix the objective accessibility defect the critique's detector found.

## Non-goals

- The native iPhone app and its design tokens.
- Backend behavior, RPC shapes, persistence formats and session activity
  contracts.
- Widget API changes. The only new variant-like treatment is CSS.
- Migrating every raw control in the adoption catalog. The ask-dock field is
  fixed; the remaining catalog sites stay for a later change.

## Behavior

**Palette and action.** Dark page `#141412`, rail `#191917`, pane `#201F1C`;
light page `#F4F0E6`, rail `#EBE6D8`, pane `#FBF8F0`. The primary Button is
`--ink-hi` on `--surface-0`; accent remains focus, links and selection. The
user message is `--surface-inset` plus one `--edge` hairline. Settings
selection keeps its accent wash and a 1px accent rule.

**Type.** Schibsted Grotesk Variable replaces Inter for operations. Source
Serif 4 and JetBrains Mono remain. The ramp is 12/13/15/20/26/34. The phone
top bar uses the serif face. Markdown `h3` is never smaller than its prose.

**Rail.** The `evener` wordmark sits above the brand row. The brand row keeps
the identity (the hub name, or `hub` before it arrives) and the three action
buttons on one row. The named `<N> needs you` Chip sits on its own header line
below it, so it cannot clip at the 280px default width. The chip is a passive
label, not a control.

**Attention.** The floating needs-you control shows a 6px attention dot,
"Needs you", and the destination title clamped to two lines. Its accessible
name remains "Next needs you: <title>", so assistive technology still hears
the next action.

**Cold start.** A disabled Start names its reason beside the prompt card and
in its tooltip. When no session is open, the Welcome chord hints also render
on `/new`. The provider notice keeps Connect and its re-check action.

**Completion.** A settled completed turn with agent output closes with
"Done in <work time>" and, when present, "N tool calls". A real `0ms` turn
closes with "Done" and no fabricated duration. The cap uses the existing
run-column wrapper so it shares the turn gutter. With Round timings on, the
turn separator carries the exact duration and the cap reads "Done" so the
same duration is not printed twice. A failed turn keeps its existing
diagnostic end-cap. A user-only turn gets no end-cap.

**Browser surfaces.** `::selection` uses a dedicated 40%-accent
`--selection-bg` wash and high ink, so the highlight is visibly distinct from
the page. The token contract holds it to ≥1.4:1 against the page ground.
`caret-color` and `accent-color` use accent. App scrollers use a thin
`--ink-low` thumb. `applyTheme` rewrites `<meta name="theme-color">` to the
active `--surface-0`, and the app root re-syncs once after mount so a
dev-server stylesheet that arrives late still gets the right chrome. The static
HTML and PWA manifest still name the dark page for a cold load.

**Ask dock.** The answer field uses the app face, `--ink-low` placeholder and
the shared focus ring.

## Recovery and preservation

- Theme, font size, density, transcript width and sidebar width preferences
  persist as before. The theme-color sync is derived, not stored.
- Transcript scroll, disclosure and read-only transcript behavior are
  unchanged. The new end-cap is a render-only element; it adds no wire field.
- The needs-you chip and control are derived from existing attention data.
- Reduced-motion behavior is unchanged; the new elements do not animate at
  rest.
- The browser guard fixtures are updated only where the new font metrics or
  palette moved their pinned boundary. No guard is weakened: the attention
  sweep now requires a two-line clamp, and the chevron sweep widens to find
  its binding width.

## Whole-journey test plan

1. Cold start at 1440: `/new` light and dark; the Start reason shows when the
   provider is missing; the chord hints render; Start and Connect are distinct.
2. Session at 1440: light and dark; rail wordmark and needs-you chip; the
   floating attention control names the destination and clamps two lines; the
   composer focus ring is accent.
3. Phone at 390: session light and dark; serif top-bar title; no horizontal
   overflow; the attention control stays inside the pane.
4. Completion: a settled completed turn shows its end-cap; a failed turn keeps
   its diagnostic; a user-only turn shows none.
5. Settings and galleries: `/dev/type` shows Schibsted and the 20/26/34 steps;
   `/dev/surfaces` renders both themes.
6. Guards: `make test-web`, `npm run layoutguard`, `make test-web-browser` (all
   guards; rerun any failing guard alone and archive that output if the
   parallel run times out).
7. Detector: `impeccable detect --json cmd/evener-hub/frontend/src` returns 0.
8. `node scripts/ds-audit.mjs` raises no literal, raw-control or
   re-implemented-widget count.

## Acceptance

Jesse approved the critique's priority direction and "everything". The change
lands when the gates above pass, the design law and decisions are updated in
the same commit, and the branch is reviewed and merged through the repository's
normal PR path.
