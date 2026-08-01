# font-size-presets-visible: the S/M/L/XL font-size presets visibly change text size across the app

**What this covers**: the font-size preset system —
`body[data-font-size="s|m|l|xl"]` selecting a `--font-scale` multiplier for
the `--font-size-*` ramp, set via Settings → Theme and persisted to
`localStorage` key `serf.prefs.fontSize` (documented in
`docs/web-ui/parity/parity-m7-settings.md` "3. Theme").

## Pre-state

- Hub running, browser authenticated, a session with some transcript content
  open (so the effect is visible in the transcript, not just the sidebar).

## Steps

1. Open Settings → Theme. Confirm the current preset (default `m`) is
   indicated.
2. Cycle through S, M, L, XL. After each selection, read
   `document.body.dataset.fontSize`,
   `getComputedStyle(document.body).getPropertyValue('--font-scale')`, and,
   for a representative scaled token,
   `getComputedStyle(document.body).getPropertyValue('--font-size-body')`.
3. With each preset active, screenshot or visually inspect: the sidebar
   session-row text, the transcript prose, and the Settings pane's own text.

## Expected

- Step 2: `--font-scale` is strictly ascending S < M < L < XL
  (`0.9`, `1`, `1.1`, `1.25`), `--font-size-body` routes through that scale,
  and `document.body.dataset.fontSize` matches the selected preset immediately
  (no reload).
- Step 3: text visibly grows across all three named surfaces at each preset
  step — falsification: any surface's text stays the same size as the
  preset changes (would mean that surface isn't wired to the `--font-size-*`
  tokens), or the sizes are out of order (XL smaller than L, etc).

## Cleanup

- Reset the preset to `m` (default) if using a persistent browser profile.

## Sharp edges

- Storage is per-browser (`localStorage`), not synced server-side — a second
  browser/profile will show the default `m` preset regardless of what was
  set elsewhere.
- **This run's actual coverage**: the `claude-in-chrome` browser tool was not
  connected in this session, so the visual/screenshot verification in step 3
  was **not driven live**. Backing evidence used instead:
  - `cmd/serf-hub/frontend/src/styles/display-gates.test.ts` checks the
    four shipped `--font-scale` multipliers and verifies every
    `--font-size-*` ramp member routes through it. This verifies the CSS
    artifact that drives the visual change directly (not simulated), just
    not through an actual browser paint.
  - `cmd/serf-hub/frontend/src/panes/settings/sections/theme.test.tsx`
    exercises the Font size control: choosing XL updates both the preference
    and `document.body.dataset.fontSize`.
  - If re-running with a working browser: perform steps 1-3 for real,
    capture screenshots at each preset, and replace this note with the
    observed visual evidence.
