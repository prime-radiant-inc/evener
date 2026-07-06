# cost-estimate-display-and-gating: `~$` cost appears on a real turn and is CSS-gated by the Show-cost toggle

**What this covers**: `llm/pricing.go` `GetPrice`/`EstimateCost` → `appwire.EstimateCost` →
the three cost-bearing surfaces added in Track C: the input-strip status row
(`cmd/serf-hub/web_workspace.go` `renderInputStrip`), the details panel
(`tokensAndCostRows`/`detailsRow`, `data-row="cost"`), and the per-turn
`.turn-meta` badge (`cmd/serf-hub/assets/renderer.js`). The Show-cost toggle
(`cmd/serf-hub/assets/settings-display.js`, default ON) gates all three via a
single CSS rule keyed on `body[data-show-cost="false"]` — no page reload, no
server round trip.

## Pre-state

- Hub running, isolated instance recommended (fake `$HOME`, non-9180 port).
- A model with pricing data in `llm/pricing.go` (e.g. `openai/gpt-5.5`).
- Browser authenticated against the hub, OR (browser tool unavailable) a
  Bearer token and `curl` with `-H "HX-Request: true"` against
  `/_partials/s/<id>/state` and `/_partials/s/<id>/details` — both are
  server-rendered fragments, so this is real server output, not a mock.

## Steps

1. Spawn a session and send one prompt to completion (poll
   `/api/sessions/local:<id>` until `state` leaves `active`).
2. **Browser path**: load `/s/<id>`, confirm the status row (`#input-status`)
   shows a `.status-item.cost` span reading `~$<amount>` next to the tokens
   item. Open the details panel (`[data-details-trigger]`) and confirm a
   `cost` row (`data-row="cost"`) with the same estimate.
   **curl fallback**: `curl -H "Authorization: Bearer $TOKEN" -H "HX-Request: true" .../_partials/s/<id>/state` and `.../_partials/s/<id>/details`.
3. Open Settings → Display, toggle "Show estimated cost" OFF
   (`[data-composer="showCost"]`). Confirm, WITHOUT a page reload or new
   network request for turn data: `.status-item.cost`, `[data-row="cost"]`,
   and `.turn-meta .cost` (if a turn badge is present) all get
   `display: none` — i.e. `body[data-show-cost="false"]` is set and the CSS
   rule in `style.css` applies.
4. Toggle it back ON; confirm all three reappear immediately (no reload).

## Expected

- Step 2: real cost estimate rendered on both the status row and the details
  panel, computed from actual `usage` tokens returned by the live turn (not a
  placeholder). Falsification: `~$` missing, or a `NaN`/`$0.00` value, or the
  Go handler 500s.
- Step 3: `document.body.dataset.showCost === "false"` is set synchronously
  on toggle (no HTTP request fired for the toggle itself — it's a
  `localStorage` write + attribute flip), and the three cost spans become
  invisible immediately. Falsification: a network request accompanies the
  toggle, or the page visibly reloads/flashes, or any cost span remains
  visible.
- Step 4: reverse of step 3, immediate.

## Cleanup

- Shut down the spawned session; toggle Show-cost back to its default (ON)
  if you changed it in a persistent browser profile.

## Sharp edges

- The toggle is CSS-only by design (Track C, commit `4cf84591`): the server
  always renders all three cost markers regardless of the toggle state; only
  `body[data-show-cost]` + the stylesheet rule
  (`cmd/serf-hub/assets/style.css:5199-5202`) decide visibility. Don't assert
  on server-side conditional rendering — there isn't any.
- **This run's actual coverage**: the `claude-in-chrome` browser tool was not
  connected in this session (`tabs_context_mcp` returned "Browser extension is
  not connected" on three attempts), so the toggle-interaction half of this
  card (step 3/4) was **not driven live**. What WAS driven live against a
  real isolated hub (fake `$HOME`, port 9197) + a real completed
  `openai/gpt-5.5` turn:
  - `GET /_partials/s/<id>/state` (real HX-Request fragment) returned:
    `<span class="status-item cost"><span class="status-key">cost</span> <span class="status-value">~$0.07</span></span>`
    alongside the tokens/work/context items — confirms step 2's status-row
    assertion with real server output and a real cost computation.
  - `GET /_partials/s/<id>/details` returned a `data-row="cost"` `<dt>`/`<dd>`
    pair with the same `~$0.07` — confirms step 2's details-panel assertion.
  - The CSS rule backing steps 3/4 was inspected directly in
    `cmd/serf-hub/assets/style.css:5199-5202` and confirmed to gate exactly
    the three selectors named above with `display: none`, no page-reload
    mechanism involved.
  - The toggle's live JS behavior (localStorage write + `data-show-cost`
    flip, no reload) is covered by `cmd/serf-hub/jstest/test-show-cost-gating.js`
    (asserts the CSS rule text directly) — run as part of the `make lint`
    jstest gate and passing. It does not simulate a live click-toggle-observe
    loop; the CSS-rule assertion plus the manual style.css read above is the
    substitute for that.
  - If re-running this card with a working browser: perform steps 3/4 for
    real and replace this note with the observed DOM/CSS computed-style
    values.
