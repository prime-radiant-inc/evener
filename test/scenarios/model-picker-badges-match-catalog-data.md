# model-picker-badges-match-catalog-data: rendered badges match the embedded catalog's real values

**What this covers**: Track B Tasks 4-9's capability badges (tools/vision/
reasoning/web-search/context-window/max-output/price) for a real, live,
catalogued model — cross-checked against both `/api/models?diagnostics=1`
and the embedded catalog's raw LiteLLM data
(`llm/data/litellm_model_catalog.json`) — across the web spawn picker, web
settings picker, and the TUI's compact meta tail.

**This card found and fixed a real bug**: the web spawn picker's model-list
data source (`listModelsWithDiagnosticsForHarness` in `assets/spawn.js`)
preferred `window.SerfAppwire.listModelsWithDiagnostics(...)` — the appwire
RPC path — whenever `window.SerfAppwire` was defined, which is
*unconditional* (`appwire.js` loads in every page via the shared
`templates/app.html` shell). That RPC response is backed by
`appwire.ModelDescriptor{Provider, Model}` — a wire type with **no**
`display_name`/badge/context/price fields at all; those only exist on the
REST `/api/models` JSON entries built by `modelDescriptorsToAPIModels`
(`web_spawn.go`). The practical effect, live and reproducible on this build
before the fix: the web spawn picker **never** rendered a prettified name or
any badge for any model — every row showed the bare lowercase id with no
meta line, in every real browser session, regardless of how catalogued the
model was. The web settings picker was unaffected (its `settings-pickers.js`
fetches `/api/models?diagnostics=1` directly, never through
`window.SerfAppwire`). See Sharp edges for the fix and the (also
bug-masking) jstest fixtures this surfaced.

## Pre-state

- Same hermetic hub, continued.
- Real live `openai` model `gpt-5.5`, catalogued in
  `llm/data/litellm_model_catalog.json` (top-level `"gpt-5.5"` key, not an
  `azure_ai/`-prefixed one) with: `input_cost_per_token: 5e-6`,
  `output_cost_per_token: 3e-5`, `max_input_tokens: 1050000`,
  `max_output_tokens: 128000`, `supports_function_calling`,
  `supports_vision`, `supports_reasoning`, `supports_web_search` all `true`.
  Substituted for the brief's suggested `anthropic/claude-opus-4-6` example —
  no live Anthropic credentials were available in this environment (see
  `model-picker-dated-snapshot-sorts-last.md`'s Sharp edges) — `gpt-5.5` is
  equally catalogued and was already live and reachable.

## Steps

1. `GET /api/models?diagnostics=1`, find the `gpt-5.5` entry.
2. Open `/new`, model picker, find the `gpt-5.5` row; read
   `.chip-picker-model-name`, `.chip-picker-model-id`,
   `.chip-picker-badge` (all), `.chip-picker-model-meta`.
3. Open `/settings/launch-serf`, model picker, same reads on the `openai`
   column's `gpt-5.5` row.
4. TUI `n` → model field → `Enter`; capture-pane, read the `Gpt 5.5` row's
   compact tail.

## Expected

- Step 1 (`/api/models?diagnostics=1`, authoritative): `context_window:
  1050000`, `max_output_tokens: 128000`, `input_cost_per_million: 5`,
  `output_cost_per_million: 30`, `supports_tools: true`, `supports_vision:
  true`, `supports_reasoning: true`, `supports_web_search: true` — an exact
  match for the catalog's raw per-token costs times 1e6
  (`5e-6 × 1e6 = 5`, `3e-5 × 1e6 = 30`) and `max_input_tokens`.
- Step 2 (web spawn, **after the fix**): name `Gpt 5.5`, id `gpt-5.5`,
  badges `["tools","vision","reasoning","web search"]`, meta `"1.1M ctx ·
  128K out · $5.00/M in · $30.00/M out"` — confirmed live.
  Before the fix: name `gpt-5.5` (raw, unprettified), badges `[]`, no meta
  line at all — confirmed live, this is the bug this card caught.
- Step 3 (web settings): name `Gpt 5.5`, badges include `tools`, `vision`,
  `reasoning`, `web search`, meta includes `1.1M ctx · $5.00...` — confirmed
  live (this picker was never affected by the bug).
- Step 4 (TUI): `Gpt 5.5  openai/gpt-5.5  1M ctx · $5.00/$30.00 ·
  tools,vision,reasoning` — confirmed live. Context window and price agree
  with steps 1-3 exactly (`1M` here vs `1.1M` in the web picker is just a
  formatting difference — `formatModelContextWindow`'s integer-truncating
  `n/1_000_000` vs `formatCtx`'s `toFixed(1)`, both correctly derived from
  the same `1050000`). The TUI's compact tail omits `web search` by design
  (`modelInfoMetaTail` only surfaces tools/vision/reasoning + ctx/price,
  confirmed by reading `cmd/serf-tui/hub_commands.go`) — not a bug.
- Falsification: any rendered number disagrees with the catalog's raw
  per-token values (after the documented ×1e6/rounding); a badge is present
  in the API but absent from a picker (or vice versa) for reasons other than
  the TUI's documented web-search omission.

## Cleanup

- None beyond the shared hub/tmux teardown.

## Sharp edges

- **The bug and the fix.** `cmd/serf-hub/assets/spawn.js`'s
  `listModelsWithDiagnosticsForHarness` (used solely by `openModelPicker`)
  was changed to always fetch `/api/models?diagnostics=1` (REST), dropping
  the `window.SerfAppwire.listModelsWithDiagnostics` branch entirely — this
  matches the already-established pattern in the same file
  (`fetchEnrichedModelsForHarness`/`openEffortPicker`, whose own comment
  already said "the appwire model list returns provider/model only") and in
  `settings-pickers.js`. Rebuilt `/tmp/serf-hub` and restarted the hub to
  pick up the embedded-asset change (assets are `//go:embed`-baked into the
  binary, not served from disk — editing the `.js` source alone does
  nothing until rebuild).
- **Tests that tested mocked behavior, not real logic** (surfaced by this
  card, flagged per this project's standing rule against exactly this):
  `cmd/serf-hub/jstest/test-spawn-model-picker-badges.js` and
  `test-spawn-model-picker-recent.js` both stubbed
  `window.SerfAppwire.listModelsWithDiagnostics` to return hand-built
  enriched objects (`display_name`, `supports_tools`, `context_window`,
  etc.) that the *real* `appwire.js` implementation
  (`function listModelsWithDiagnostics(params) { return
  request(...).then((resp) => ({ models: resp.data || [], ... })); }`,
  where `resp.data` is `appwire.ModelDescriptor[]`) can never actually
  produce — so both tests passed green on every run of Tasks 7-9's own gates
  while the feature they claimed to cover was dead in production. Fixed by
  changing both to mock `window.fetch("/api/models?...")` instead (matching
  the pattern `test-spawn-model-picker-recent-fetch-fallback.js` already
  used), which now exercises the real, sole code path.
  `cmd/serf-hub/jstest/test-spawn.js` (a pre-existing, larger, non-Track-B
  test file covering spawn-form navigation/harness switching) had two more
  instances of the same pattern (`formDom`, `diagModelDom`) and needed the
  same treatment, plus a `setTimeout(r,0)` flush after each model-picker
  `.click()` — a real native `fetch()` Promise resolves on a microtask,
  unlike the old stub's synchronous fake-thenable, so the picker's DOM is
  built one tick later than it used to be.
- The web settings picker and the TUI were never affected — they always
  read the enriched REST endpoint (`settings-pickers.js` directly;
  `cmd/serf-tui/hub_commands.go`'s `modelInfoMetaTail` reads
  `llm.EmbeddedModelCatalog()` directly in-process, no HTTP round trip at
  all). Only the web spawn form's model picker was silently broken.
