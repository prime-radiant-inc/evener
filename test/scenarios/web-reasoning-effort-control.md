# web-reasoning-effort-control: the web effort control shows only the current model's levels and survives a cold attach

**What this covers**: spec Acceptance criterion 6 for the web client — the
effort control offers only the current model's levels, and it displays the
current model AND current effort for a cold-attached client (no prior
notification, no reload). Exercises the status strip's
`ReasoningEffortControl` (`panes/session/chrome/StatusRow.tsx:109-165`) plus
the `⌘K` "Set reasoning effort" palette command
(`shell/palette/commands.ts:392-429`).

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map there is the single place these hooks are maintained. The web
half used to drive `window.EvenerModelSwitch.effortLevels()` and read
`[data-effort-display]` out of `assets/search.js` / `assets/model-switch.js`;
all four died with the vanilla frontend (`660376f78`). There is no renderer
handle, and the effort control is no longer read-only — see Sharp edges.

## Pre-state

- A hub with a web client able to attach to the session (isolated `$HOME`,
  kernel-assigned port — see the Setup checklist in
  `docs/developing-evener/agentic-testing.md`).
- A session on a reasoning-capable model with a known ladder (e.g.
  `openai/gpt-5.5`), idle. Confirm the ladder over the wire before asserting
  on the UI: `thread/read`'s `reasoningEffortLevels` /
  `supportsReasoning` (`appwire/types_thread.go#EvenerThread`).
- A real SPA bundle. A checkout that has never run `make build-web` ships a
  one-line `frontend/dist/PLACEHOLDER` and serves no app at all (rebuild
  matrix item 3 in the runbook).

## Steps

1. **[browser] Web, live convergence.** In the web client, set a non-default
   effort (via the strip's `<select>` or the `⌘K` palette command), then read
   the status strip. The control is a real native `<select>` laid
   transparently over a readout, so there are two things to read and they
   must agree:
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

2. **[browser] Web, cold attach.** Reload the session page fresh (or open a
   brand-new tab with no prior notification history) and re-run the same
   `eval` immediately after `[data-testid="session-chrome"]` mounts.

3. **[browser] Non-reasoning model.** Switch the session (or spawn a second
   one) to a model with `supportsReasoning: false` and re-run the `eval`.

## Expected

- **Step 1** (live convergence): `selectValue` reflects the effort just set,
  with **no web reload** — driven by
  `thread/reasoning-effort/changed` (`appwire-client/typescript/reducer.ts:702-705`).
  `readout` shows the same word, and `options` is `[""]` followed by the
  model's own ladder minus `"none"` (`StatusRow.tsx:129`). An unset effort —
  and evener's `"none"`, which clears to the provider default — both render as
  the leading `(default)` option, value `""` (`:128,139,159`). The palette's
  own rows are `(default)` plus the same ladder
  (`shell/palette/commands.ts:410`). Falsification: `selectValue` is
  stale until a reload, or `readout` and `selectValue` disagree.
- **Step 2** (AC 6, web cold attach): `readout`, `selectValue` and `model` are
  all correct on first paint — the effort snapshot rides in on `thread/read`
  (`appwire/types_thread.go#EvenerThread`), not on a later notification. Falsification:
  the readout shows `(default)` for a session that has an explicit effort
  set, or the control is missing entirely on a reasoning-capable model.
- **Step 3**: on a non-reasoning model the control renders nothing —
  `control: false` with `readout`/`selectValue` `undefined`. That is the
  correct answer, not a failed query. Falsification: a picker or control
  appears on a model whose wire `supportsReasoning` is false.

## Cleanup

- Close web tabs.
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
  (`StatusRow.tsx:126`). On a non-reasoning model, step 3's `eval` returns
  `control: false` with `readout`/`selectValue` `undefined` — that is the
  correct answer, not a failed query. The wrapper
  `[data-testid="status-row-effort"]` (`:132`) is the hook to assert that
  absence against. Check `supportsReasoning` on the wire before calling a
  missing control a regression.
- `supportsReasoning` is a plain boolean in the web model (coerced from the
  wire's optional field, `appwire-client/typescript/reducer.ts`), so there is no
  "undefined vs false" distinction to worry about on the web side.
