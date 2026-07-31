# tui-effort-command: serf-tui `/effort` shows only the current model's levels, and both surfaces display current model + effort on a cold attach

**What this covers**: spec Acceptance criterion 6 — TUI `/effort` and the
web effort picker both show only the current model's levels, and both
surfaces display the current model AND current effort for a cold-attached
client (no prior notification). Exercises `/effort`
(`cmd/serf-tui/hub_command_registry.go:348-361`), the session header's
`effort` part (`hub_session_view.go:52`), and (for the web half of the
criterion) both the `⌘K` "Set reasoning effort" palette command
(`cmd/serf-hub/frontend/src/shell/palette/commands.ts:393-408`) and the
inline effort control in the session status row
(`panes/session/chrome/StatusRow.tsx:130-162`).

## Pre-state

- Hub + serf-tui + a web client, both able to attach to the same session.
- A session on a reasoning-capable model with a known ladder (e.g.
  `openai/gpt-5.5`, levels `low`/`medium`/`high` or similar — confirm via
  `thread/read`'s `reasoningEffortLevels`), idle.

## Steps

1. **TUI, live.** Attach the TUI, capture-pane; confirm the header's
   `effort` part shows the current effort (or empty/default if unset).
   Type `/effort`, `Enter` (bare form). Capture-pane.
2. Compare the picker's listed levels against `thread/read`'s
   `reasoningEffortLevels` for the current model — confirm exact match, no
   extra levels from a different model's ladder.
3. Pick a different level, `Enter`. Confirm `thread/reasoning-effort/set`
   fires and the header's `effort` part updates live via
   `thread/reasoning-effort/changed`.
4. **TUI, cold attach.** Detach/restart the TUI (fresh attach, no live
   notification received yet). Capture-pane immediately: read the header's
   `model` and `effort` parts.
5. **Web, live.** In the web client (already attached, no reload since step
   3), open the `⌘K` palette, run "Set reasoning effort". Confirm the
   offered levels match the *current* model's ladder (the thread snapshot's
   `reasoningEffortLevels`, `appwire/types.go:348-349`, which
   `StatusRow.tsx:128-129` filters `none` out of), and confirm they reflect
   the level set in step 3 (i.e. web already converged live via
   `thread/reasoning-effort/changed`).
6. **Web, cold attach.** Reload the web client's session page fresh (or open
   a brand-new tab with no prior notification history). Read
   `[data-testid="status-row-effort-value"]` and
   `[data-testid="model-switch-value"]` (`ModelSwitch.tsx:137`)
   immediately after load.
7. **Known-empty ladder case.** Switch the session (or spawn a second one)
   to a model with `supportsReasoning: false`. Repeat `/effort` in the TUI:
   confirm it does NOT open a picker and instead shows an informative
   message ("does not support reasoning effort").

## Expected

- Step 2: the TUI picker's level set is exactly `reasoningEffortLevels` for
  the *current* model — no stale levels from a previously attached model.
- Step 3: header `effort` part updates without a manual refresh.
- Step 4 (AC 6, TUI cold attach): both `model` and `effort` parts are
  populated correctly on the very first render after attach — sourced from
  the thread snapshot (`detail.Model`, `detail.ReasoningEffort`,
  `detail.SupportsReasoning`, `detail.ReasoningEffortLevels`), not from a
  notification that hasn't arrived yet.
- Step 5 (web live convergence, sanity check supporting AC 1/6 together):
  levels match the model's ladder; the level reflects step 3's TUI-driven
  change without a web reload.
- Step 6 (AC 6, web cold attach): `[data-testid="status-row-effort-value"]`
  shows the current effort and `[data-testid="model-switch-value"]` shows
  the current model immediately on load — driven by the thread snapshot at
  init, not a notification. Falsification: the chip is
  blank/hidden or shows a stale value until some later event fires.
- Step 7: `supportsReasoning === false` is a KNOWN-empty answer — no picker
  opens; the TUI shows the informative message. This distinguishes it from
  an *unknown* ladder (which falls back to the default vocabulary
  client-side, per `effortLevels()`'s documented fallback) — falsification
  if an empty picker (rather than the message) is shown, or if a picker with
  the wrong (fallback) levels opens.

## Cleanup

- Detach/kill TUI and close web tabs.
- Restore the session's model/effort if this card mutated shared test
  fixtures; shut down sessions spawned solely for this card.

## Sharp edges

- `supportsReasoning` starts `undefined` in the web JS state (distinct from
  `false`) until a snapshot or notification says otherwise — don't confuse
  "unknown, falls back to default levels" with "known-empty, no levels" when
  reading `effortLevels()`'s behavior; step 7 is specifically the
  known-empty case.
- The web effort control **is** interactive: the status row renders a real
  native `<select id="status-row-reasoning-effort">` under a transparent
  overlay, with the visible readout in
  `[data-testid="status-row-effort-value"]` and the whole control wrapped
  in `[data-testid="status-row-effort"]` (`StatusRow.tsx:130-162`). The
  `⌘K` palette command still exists, but it is not the only path — an
  older version of this card called the web control read-only, which would
  make a working control read as a regression.
- `/effort` gates on the `ChangeModel` capability (same gate as `/model`) —
  if a session's capabilities haven't hydrated yet, the command may
  correctly report unavailable; wait for capabilities before asserting the
  known-empty-ladder message in step 7.
