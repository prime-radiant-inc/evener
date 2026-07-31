# tui-effort-command: serf-tui `/effort` shows only the current model's levels, and both surfaces display current model + effort on a cold attach

**What this covers**: spec Acceptance criterion 6 — TUI `/effort` and the
web effort control both show only the current model's levels, and both
surfaces display the current model AND current effort for a cold-attached
client (no prior notification). Exercises `/effort`
(`cmd/serf-tui/hub_command_registry.go:354-392`), the session header's
`effort` part (`hub_session_view.go:51-52`'s `addPart`), and — for the web half of
the criterion — the status strip's `ReasoningEffortControl`
(`panes/session/chrome/StatusRow.tsx:109-165`) plus the `⌘K`
"Set reasoning effort" palette command (`shell/palette/commands.ts:392-429`).

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The web
half used to drive `window.SerfModelSwitch.effortLevels()` and read
`[data-effort-display]` out of `assets/search.js` / `assets/model-switch.js`;
all four died with the vanilla frontend (`660376f78`). There is no renderer
handle, and the effort control is no longer read-only — see Sharp edges.

## Pre-state

- Hub + serf-tui + a web client, all able to attach to the same session
  (isolated `$HOME`, kernel-assigned port — see the Setup checklist in
  `docs/agentic-testing.md`).
- A session on a reasoning-capable model with a known ladder (e.g.
  `openai/gpt-5.5`), idle. Confirm the ladder over the wire before asserting
  on either UI: `thread/read`'s `reasoningEffortLevels` /
  `supportsReasoning` (`appwire/types.go:347-349`).
- For the web steps: a real SPA bundle. A checkout that has never run
  `make build-web` ships a one-line `frontend/dist/PLACEHOLDER` and serves no
  app at all (rebuild matrix item 3 in the runbook).

## Steps

1. **[TUI] Live.** Attach the TUI, capture-pane; confirm the header's
   `effort` part shows the current effort (or empty/default if unset).
   Type `/effort`, `Enter` (bare form). Capture-pane.
2. **[TUI]** Compare the picker's listed levels against `thread/read`'s
   `reasoningEffortLevels` for the current model — confirm exact match, no
   extra levels from a different model's ladder.
3. **[TUI]** Pick a different level, `Enter`. Confirm
   `thread/reasoning-effort/set` fires and the header's `effort` part updates
   live via `thread/reasoning-effort/changed`
   (`appwire/types.go:20,97,1122-1127`).
4. **[TUI] Cold attach.** Detach/restart the TUI (fresh attach, no live
   notification received yet). Capture-pane immediately: read the header's
   `model` and `effort` parts.

5. **[browser] Web, live convergence.** In the web client (already attached,
   no reload since step 3), read the status strip. The control is a real
   native `<select>` laid transparently over a readout, so there are two
   things to read and they must agree:
   ```javascript
   (() => {
     const sel = document.getElementById("status-row-reasoning-effort");
     return {
       port: location.port,                       // page-identity check, always
       path: location.pathname,                   // /s/local:<SID>
       control: !!document.querySelector('[data-testid="status-row-effort"]'),
       readout: document.querySelector('[data-testid="status-row-effort-value"]')?.textContent,
       selectValue: sel?.value,
       options: sel ? Array.from(sel.options, (o) => o.value) : null,
       // ModelSwitch.tsx:137
       model: document.querySelector('[data-testid="model-switch-value"]')?.textContent,
     };
   })()
   ```
   Then open the `⌘K` palette and run **Set reasoning effort**; confirm the
   offered rows match the *current* model's ladder.

6. **[browser] Web, cold attach.** Reload the session page fresh (or open a
   brand-new tab with no prior notification history) and re-run the same
   `eval` immediately after `[data-testid="session-chrome"]` mounts.

7. **[TUI] Known-empty ladder case.** Switch the session (or spawn a second
   one) to a model with `supportsReasoning: false`. Repeat `/effort` in the
   TUI: confirm it does NOT open a picker and instead shows an informative
   message.

## Expected

- Step 2: the TUI picker's level set is exactly `reasoningEffortLevels` for
  the *current* model — no stale levels from a previously attached model.
- Step 3: header `effort` part updates without a manual refresh.
- Step 4 (AC 6, TUI cold attach): both `model` and `effort` parts are
  populated correctly on the very first render after attach — sourced from
  the thread snapshot (`detail.Model`, `detail.ReasoningEffort`,
  `detail.SupportsReasoning`, `detail.ReasoningEffortLevels`), not from a
  notification that hasn't arrived yet.
- Step 5 (web live convergence): `selectValue` equals the level set from the
  TUI in step 3, with **no web reload** — driven by
  `thread/reasoning-effort/changed` (`protocol/reducer.ts:702-705`).
  `readout` shows the same word, and `options` is `[""]` followed by the
  model's own ladder minus `"none"` (`StatusRow.tsx:129`). An unset effort —
  and serf's `"none"`, which clears to the provider default — both render as
  the leading `(default)` option, value `""` (`:128,139,159`). The palette's
  own rows are `(default)` plus the same ladder
  (`shell/palette/commands.ts:410`). Falsification: `selectValue` is
  stale until a reload, or `readout` and `selectValue` disagree.
- Step 6 (AC 6, web cold attach): `readout`, `selectValue` and `model` are
  all correct on first paint — the effort snapshot rides in on `thread/read`
  (`appwire/types.go:347-349`), not on a later notification. Falsification:
  the readout shows `(default)` for a session that has an explicit effort
  set, or the control is missing entirely on a reasoning-capable model.
- Step 7: `supportsReasoning === false` is a KNOWN-empty answer — no picker
  opens; the TUI prints `This model does not support reasoning effort.`
  (`hub_command_registry.go:366`). A reasoning model with a genuinely empty
  ladder prints `No reasoning effort levels available for this model.`
  (`:370`) instead. Falsification: an empty picker opens rather than either
  message being printed.

## Cleanup

- Detach/kill TUI (`tmux kill-session -t "$TMUX_SESSION"`) and close web tabs.
- Restore the session's model/effort if this card mutated shared test
  fixtures; shut down sessions spawned solely for this card via
  `POST $HUB/api/sessions/local:$SID/shutdown`.

## Sharp edges

- **The web effort control is a live `<select>`, not a read-only span.**
  This card used to say the opposite and told readers not to look for a click
  target. `StatusRow.tsx:151-162` renders a real native `<select>` at zero
  opacity over the `status-row-effort-value` readout, precisely so it keeps
  tab order, arrow keys, type-ahead and the platform dropdown. Setting effort
  from the web no longer goes exclusively through `⌘K`. The readout is
  `aria-hidden` (`:138`) because the select already speaks its own value, so
  an accessibility-tree query finds the labelled select ("Reasoning effort",
  `:144-146`), not the visible text.
- **The strip and the palette disagree about a reasoning model with an empty
  ladder, by design and in opposite directions.** `StatusRow` falls back to
  `["minimal","low","medium","high"]` when `supportsReasoning` is true and the
  ladder is empty (`:81,120-125`) — the wire really can emit that pair, and
  `StatusRow.tsx:96-100` records why. The palette's enum source yields
  **zero** options in the same case (`commands.ts:406-411`, whose own comment
  states the rule), so the command offers nothing.
  Don't treat one as evidence about the other; assert each against the model
  it is actually reading.
- **The control renders nothing at all when there are no levels**
  (`StatusRow.tsx:126`). On a non-reasoning model, step 5's `eval` returns
  `control: false` with `readout`/`selectValue` `undefined` — that is the
  correct answer, not a failed query. The wrapper
  `[data-testid="status-row-effort"]` (`:132`) is the hook to assert that
  absence against. Check `supportsReasoning` on the wire before calling a
  missing control a regression.
- `supportsReasoning` is a plain boolean in the web model (coerced from the
  wire's optional field, `protocol/reducer.ts`), so the old "undefined vs
  false" distinction the TUI half warned about does not exist on the web
  side any more. Step 7's known-empty case is a TUI assertion.
- `/effort` gates on the `ChangeModel` capability (same gate as `/model` —
  there is no separate effort capability on the wire,
  `hub_command_registry.go:348-353`); if a session's capabilities haven't
  hydrated yet, the command may correctly report unavailable. Wait for
  capabilities before asserting the known-empty-ladder message in step 7.
