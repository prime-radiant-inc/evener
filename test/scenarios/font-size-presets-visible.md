# font-size-presets-visible: the S/M/L/XL font-size presets visibly change text size across the app

**What this covers**: the font-size preset system (commit `08304245`) —
`body[data-font-size="s|m|l|xl"]` cascading `--text-*` custom properties,
set via Settings → Appearance and persisted to `localStorage` key
`serf-hub.appearance.fontSize` (documented in
`docs/web-ui/design-system.md` "Font-size presets").

## Pre-state

- Hub running, browser authenticated, a session with some transcript content
  open (so the effect is visible in the transcript, not just the sidebar).

## Steps

1. Open Settings → Appearance. Confirm the current preset (default `m`) is
   indicated.
2. Cycle through S, M, L, XL. After each selection, read
   `document.body.dataset.fontSize` and `getComputedStyle(document.body).getPropertyValue('--text-md')`
   (or another representative token).
3. With each preset active, screenshot or visually inspect: the sidebar
   session-row text, the transcript prose, and the Settings pane's own text.

## Expected

- Step 2: `--text-*` token values are strictly ascending S < M < L < XL for
  every token in the scale (`--text-2xs` through `--text-2xl`), and
  `document.body.dataset.fontSize` matches the selected preset immediately
  (no reload).
- Step 3: text visibly grows across all three named surfaces at each preset
  step — falsification: any surface's text stays the same size as the
  preset changes (would mean that surface isn't wired to the `--text-*`
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
  - `cmd/serf-hub/jstest/test-font-size-presets.js` parses the actual
    `style.css` and asserts, for each of the four
    `body[data-font-size="..."]` blocks, that every `--text-*` token is
    defined and that the four presets' values are strictly ascending in the
    documented S < M < L < XL order — i.e. the CSS artifact that would drive
    the visual change was verified directly (not simulated), just not
    rendered through an actual browser paint. This test passed as part of
    the `make lint` jstest gate.
  - The preset-switching UI logic itself (button click →
    `localStorage.setItem("serf-hub.appearance.fontSize", ...)` →
    `document.body.dataset.fontSize = ...`, in
    `cmd/serf-hub/assets/settings-appearance.js`) has **no direct jstest
    coverage** — `cmd/serf-hub/jstest/test-settings-appearance.js` covers a
    different part of the Appearance section and contains no reference to
    font size at all. This bullet's claim was verified only by reading
    `settings-appearance.js` directly, not by an automated test; that's a
    real gap, not a false one.
  - `docs/web-ui/design-system.md`'s "Font-size presets" subsection
    (added in Phase X, task X1) documents the four presets' scale factors
    (~90%/100%/~115%/~130%) consistently with the CSS.
  - If re-running with a working browser: perform steps 1-3 for real,
    capture screenshots at each preset, and replace this note with the
    observed visual evidence.
