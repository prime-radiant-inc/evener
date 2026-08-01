# font-size-presets-visible: the S/M/L/XL font-size presets visibly change text size across the app

**What this covers**: the font-size preset system —
`body[data-font-size="s|m|l|xl"]` selecting a `--font-scale` multiplier for
the `--font-size-*` ramp, set via Settings → Theme and persisted to
`localStorage` key `serf.prefs.fontSize` (documented in
`docs/web-ui/parity/parity-m7-settings.md` "3. Theme").

## Pre-state

- Complete the full **Setup checklist** in `docs/agentic-testing.md` for this
  run: one unique `run=$(mktemp -d ...)` owns the fresh binaries, an isolated
  `$HOME=$run/home` (with `XDG_STATE_HOME` unset), and a hub bound by the kernel
  to `127.0.0.1:0`. Keep its exact PID in `$run/hub.pid`; never use an ambient
  hub, assigned port, real home, or shared state.
- Before the first browser call, claim a browser profile owned only by this run,
  e.g. `set_profile font-size-<basename-of-$run>`. Do not use the ambient/shared
  default or any pre-existing persistent profile. Authenticate that browser to
  this run's `$HUB`, then open a session containing transcript content so the
  effect is visible beyond the sidebar.
- Create `RESULTS=$run/results/font-size-presets-visible` with
  `mkdir -p "$RESULTS"`. Every measurement export and S/M/L/XL screenshot from
  this card must be written there, beneath the run root that this card owns.

## Steps

1. Confirm `HOME` is `$run/home`, the page's `location.port` equals this run's
   kernel-assigned `$PORT`, and the claimed browser profile is the unique
   profile named above. Open Settings → Theme and confirm the fresh profile's
   current preset (default `m`) is indicated.
2. Cycle through S, M, L, XL. After each selection, read
   `document.body.dataset.fontSize`,
   `getComputedStyle(document.body).getPropertyValue('--font-scale')`, and,
   for a representative scaled token,
   `getComputedStyle(document.body).getPropertyValue('--font-size-body')`.
   Save the four labeled result objects to `$RESULTS/measurements.json`.
3. With each preset active, screenshot or visually inspect: the sidebar
   session-row text, the transcript prose, and the Settings pane's own text.
   Save the captures as `$RESULTS/font-s.png`, `font-m.png`, `font-l.png`, and
   `font-xl.png`; do not leave the only copy in a shared browser-tool cache.

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

- Close only the tabs/browser instance claimed for this run, shut down the
  session this card spawned, and kill only the hub PID recorded in
  `$run/hub.pid`. Then remove this card's exact `$run` directory.
- Do **not** reset `fontSize`, clear storage, or otherwise overwrite an ambient
  or pre-existing persistent browser profile. Such a profile is outside this
  card's ownership and must never be used by the steps above.

## Sharp edges

- Storage is per-browser (`localStorage`), not synced server-side. That is why
  this card requires a new run-owned profile: its default `m` state and every
  mutation belong to this run, and cleanup never edits someone else's profile.
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
  - If re-running with a working browser: first establish the run-owned hub,
    home, profile, and results directory above; then perform steps 1-3, retain
    all four captures below `$RESULTS`, and replace this note with the observed
    visual evidence.
