# model-picker-badges-match-catalog-data: rendered badges match the embedded catalog's real values

**What this covers**: Track B Tasks 4-9's capability badges (tools/vision/
reasoning/web-search/context-window/max-output/price) for a real, live,
catalogued model — cross-checked against the typed AppWire `model/list`
response and the embedded catalog's raw LiteLLM data
(`llm/data/litellm_model_catalog.json`) — across the web spawn picker, web
settings picker, and the TUI's compact meta tail.

The typed `model/list` response is the single source for both web pickers and
the TUI. Rich descriptor fields are optional so a provider can return only its
launchable identity; the hub fills missing catalog metadata without replacing
explicit live values. This card verifies that contract end to end.

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

1. Send `model/list` over the authenticated AppWire connection and find the
   `gpt-5.5` entry in `result.data`.
2. Open `/new`, open the Model field's trigger, and find the `gpt-5.5`
   `li[role="option"]` row. Read its child `span` elements: the first is the
   display name, an optional second is the selected check mark, and the final
   one is the metadata string when metadata is present.
3. Open `/settings/launch-evener`, open the `Model` field's trigger, and make
   the same assertion on the `openai` column's `gpt-5.5` row.
4. TUI `n` → model field → `Enter`; capture-pane, read the `Gpt 5.5` row's
   compact tail.

## Expected

- Step 1 (`model/list`, authoritative): `contextWindow: 1050000`,
  `maxOutputTokens: 128000`, `inputCostPerMillion: 5`,
  `outputCostPerMillion: 30`, `supportsTools: true`, `supportsVision: true`,
  `supportsReasoning: true`, `supportsWebSearch: true` — an exact
  match for the catalog's raw per-token costs times 1e6
  (`5e-6 × 1e6 = 5`, `3e-5 × 1e6 = 30`) and `max_input_tokens`.
- Step 2 (web spawn): the `li[role="option"]` row has display name `Gpt 5.5`,
  id `gpt-5.5` in its model text, badges
  `["tools","vision","reasoning","web search"]`, and meta
  `"1.1M ctx · 128K out · $5.00/M in · $30.00/M out"` — confirmed live.
- Step 3 (web settings): the corresponding ARIA row has name `Gpt 5.5`,
  badges including `tools`, `vision`, `reasoning`, `web search`, and metadata
  including `1.1M ctx · $5.00...`.
- Step 4 (TUI): `Gpt 5.5  openai/gpt-5.5  1M ctx · $5.00/$30.00 ·
  tools,vision,reasoning` — confirmed live. Context window and price agree
  with steps 1-3 exactly (`1M` here vs `1.1M` in the web picker is just a
  formatting difference — `formatModelContextWindow`'s integer-truncating
  `n/1_000_000` vs `formatCtx`'s `toFixed(1)`, both correctly derived from
  the same `1050000`). The TUI's compact tail omits `web search` by design
  (`modelInfoMetaTail` only surfaces tools/vision/reasoning + ctx/price,
  confirmed by reading `cmd/evener-tui/hub_commands.go`) — not a bug.
- Falsification: any rendered number disagrees with the catalog's raw
  per-token values (after the documented ×1e6/rounding); a badge is present
  in the API but absent from a picker (or vice versa) for reasons other than
  the TUI's documented web-search omission.

## Cleanup

- None beyond the shared hub/tmux teardown.

## Sharp edges

- **The typed response is authoritative.** Both web pickers call `model/list`
  with the appropriate scope, and the TUI calls the same method through the
  generated AppWire client. If metadata differs between surfaces, inspect the
  response and the hub enrichment path before blaming the row renderer.
- The web settings picker and the TUI use the same typed descriptor fields;
  only their compact formatting differs. The TUI intentionally omits the web
  search badge from its compact tail.
