# model-picker-uncatalogued-model-still-renders: uncatalogued live model still renders, no badges, selectable, launches

**What this covers**: Track B's graceful-degradation rule (spec: "Graceful
degradation: uncatalogued live model still renders, no badges") —
`catalogModelInfo`'s `ollama`-special-case (`cmd/serf-hub/web_spawn.go`: for
`behaviorTag == "ollama"` it requires an exact `"ollama/<id>"` catalog entry,
never the generic bare-id `LookupModelInfo` fallback) means a locally-pulled
Ollama model whose tag isn't one of the ~29 fixed `ollama/*` catalog entries
is uncatalogued by design — a real, no-stub example of this rule, not a
fabricated one.

## Pre-state

- Same hermetic hub, continued from the Recent card.
- Real local `ollama` instance serving `gemma4:e4b` (`ollama pull gemma4:e4b`
  already done; confirmed absent from the embedded catalog's ~29
  `ollama/<name>` entries via `llm/data/litellm_model_catalog.json`).

## Steps

1. `GET /api/models?diagnostics=1`, find the `{"provider":"ollama","model":
   "gemma4:e4b", ...}` entry.
2. Open `/new`, model picker: find the `ollama` group's `gemma4:e4b` row;
   read its full text (name + id) and check for any
   `.chip-picker-model-meta`/`.chip-picker-model-badges` children.
3. Open `/settings/launch-serf`, model picker: same check on the `ollama`
   column's row.
4. TUI `n` → model field → `Enter`: capture-pane, inspect the `OLLAMA` row.
5. Launch it: `POST /api/spawn {"prompt":"hi","harness":"serf","model":
   "ollama/gemma4:e4b","working_dir":"<dir>"}`; after the daemon completes
   the turn, read the session's `.transcript.jsonl`.

## Expected

- Step 1: the entry is exactly
  `{"display_name":"Gemma4:e4b","model":"gemma4:e4b","provider":"ollama"}`
  — no `context_window`, `supports_tools`, `supports_vision`,
  `supports_reasoning`, `input_cost_per_million`, `output_cost_per_million`,
  or `max_output_tokens` keys at all (not merely `null`/`false` — absent).
- Step 2/3/4: the row renders `Gemma4:e4b` / `ollama/gemma4:e4b` (name and
  provider-qualified id both visible) with **no** badge chips and **no**
  ctx/price meta line — confirmed: the web rows are the bare
  `chip-picker-model` node with only a name span, no
  `.chip-picker-model-meta`/`.chip-picker-model-badges` children at all; the
  TUI row is `Gemma4:e4b  ollama/gemma4:e4b` with no ` · ctx · price · caps`
  tail (contrast with the adjacent `Gpt 5.4` row's
  `1M ctx · $2.50/$15.00 · tools,vision,reasoning`).
- Step 5: the spawn succeeds (`{"ref":"local:...",...}`, no error), and the
  transcript contains real `ASSISTANT` turns with actual generated content
  (not an error/failure entry) — confirmed live: session
  `01KWTFQBJDR3XQTDQ12H1Y2R9J`'s transcript shows a `USER_INPUT` "hi", a real
  `ASSISTANT` thinking+response, a `STEERING` correction (the harness's
  tool-call-format nudge), and a subsequent real tool-call round — i.e. the
  uncatalogued model is not just listed but fully functional end to end.
- Falsification: the API entry carries any badge/cost/context key (even
  `null`); a picker row shows blank/placeholder badges instead of omitting
  them; the row is rendered with a disabled/greyed-out state; or the launch
  fails.

## Cleanup

- None beyond the shared hub/tmux teardown — the spawned session is left in
  the scratch state dir, removed with the rest of the hermetic tree.

## Sharp edges

- Don't confuse "uncatalogued" with "disabled" — this model has no
  `DisabledReason` and is fully selectable/launchable; graceful degradation
  here means *fewer rendered facts*, not a blocked action.
- The same `gemma4:e4b` row also appears in the `Recent` group (see
  `model-picker-recent-reflects-last-5-global.md`) with the identical
  no-badges rendering — Recent doesn't re-enrich or hide the degradation,
  it just re-surfaces the same already-built entry.
