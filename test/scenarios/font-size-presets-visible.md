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
- Coordinate browser ownership before the first browser call, following
  `docs/conventions/agent-fleets.md`. Use one of two honest modes: either this
  run is the designated browser verifier and holds an exclusive serialized
  browser-verification slot, or it has a genuinely distinct browser server and
  process whose PID and data directory are recorded below `$run`. A different
  profile name on the shared browser server is not isolation; do not call
  `set_profile`. In serialized mode, act only in a new tab on this run's unique
  `$PORT` origin. Never close the shared browser/server, change its
  server-global configuration, clear its profile, or touch any pre-existing
  tab. Authenticate the run-owned tab to this run's `$HUB`, then open a session
  containing transcript content so the effect is visible beyond the sidebar.
- Create `RESULTS=$run/results/font-size-presets-visible` with
  `mkdir -p "$RESULTS"`. Every measurement export and S/M/L/XL screenshot from
  this card must be written there, beneath the run root that this card owns.

## Steps

1. Confirm `HOME` is `$run/home` and record which browser-ownership mode is in
   force: the designated serialized verifier slot, or the exact PID and data
   directory for a distinct run-owned browser process. In every browser eval,
   assert that the page's `location.port` equals this run's kernel-assigned
   `$PORT`. Open Settings → Theme and record the currently indicated preset.
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

- In serialized mode, release the designated verifier slot after the last
  measurement. Do not close the shared browser/server, call `set_profile`,
  clear its profile, or mutate any pre-existing tab. Only the new run tab and
  this run's unique-port origin were in scope during verification.
- In distinct-process mode, stop only the exact run-owned browser PID. Then, in
  either mode, shut down only the session this card spawned, kill only the hub
  PID recorded in `$run/hub.pid`, and remove this card's exact `$run`
  directory. The distinct browser data directory and all results disappear as
  children of that one run root.

## Sharp edges

- Storage is per-origin browser state (`localStorage`), not synced server-side.
  In serialized mode this card changes only the unique `$PORT` origin in its
  run tab; in distinct-process mode the state also lives in the browser data
  directory below `$run`. Neither mode permits clearing or rewriting the
  shared profile.
- **`set_profile` is prohibited.** It changes one sticky value on the shared
  MCP server process and does not create per-agent isolation. The measured
  authority and current coordination rule are in
  `docs/conventions/agent-fleets.md` under “Chrome is one shared instance.”
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
    home, results directory, and one of the two browser-ownership modes above;
    then perform steps 1-3, retain all four captures below `$RESULTS`, and
    replace this note with the observed visual evidence.
