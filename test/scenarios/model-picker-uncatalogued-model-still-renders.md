# model-picker-uncatalogued-model-still-renders: uncatalogued live model still renders, no badges, selectable, launches

**What this covers**: Track B's graceful-degradation rule (spec:
"Graceful degradation: uncatalogued live model still renders, no
badges"). A live model the registry carries no capabilities for must
render with its name and provider-qualified id, carry **no**
badge/cost/context metadata at all, stay selectable, and launch.

Ollama supplies a real, no-stub example of the rule. Every capability on
a model descriptor comes from the registry's resolved record
(`cmdutil.ModelDescriptorFromResolved`, `cmdutil/cmdutil.go#ModelDescriptorFromResolved`),
and a locally-pulled ollama model has no curated row of its own, so it
arrives with the provider-level defaults and nothing else — uncatalogued
**by design**, not by accident. The hub adds nothing on top:
`enrichModelListResponse` (`cmd/evener-hub/app_models.go#enrichModelListResponse`)
only fills a blank display name and sorts.

**Surface**: see `docs/developing-evener/agentic-testing.md`, "Driving the web UI" — the
selector map. The old `.chip-picker-model-meta` /
`.chip-picker-model-badges` selectors are gone with the vanilla frontend
(`660376f78`), and the replacement is not a rename: the current row has
**no meta element at all** when there is no metadata. The row is
`li[role="option"]` containing a name `<span>`, an optional `✓` when
selected, and a meta `<span>` rendered only when the meta string is
non-empty — `{row.meta !== "" && <span …>{row.meta}</span>}`
(`widgets/modelCatalog/index.tsx:299-305`), where the meta string comes
from `rowMeta` (`widgets/modelCatalog/pickerRows.ts:37-45`) and is `""`
when the entry carries no capabilities, no cost and no context window.
So the assertion is **absence of the element**, not an empty one.

## Pre-state

- Same hermetic hub, continued from
  `model-picker-recent-reflects-last-5-global.md` (isolated
  `$HOME`/`$XDG_STATE_HOME`, kernel-assigned port — Setup checklist in
  `docs/developing-evener/agentic-testing.md`).
- Real local `ollama` instance serving `gemma4:e4b` (`ollama pull
  gemma4:e4b` already done). Confirm it is genuinely absent from the
  catalog's `ollama/*` keys before relying on it:
  ```bash
  jq -r 'keys[] | select(startswith("ollama/"))' llm/data/litellm_model_catalog.json | grep -c gemma4
  ```
  must print `0` (`ollama/codegemma` is the only gemma-ish key there).
  Substitute any pulled model that satisfies this if `gemma4:e4b` is
  ever added to the catalog.
- For the browser steps only: a frontend built with `make build-web`
  before the hub binary.

## Steps

### Browser-free (the exact assertions — everything but "is it rendered")

1. Send `model/list` over the authenticated AppWire connection and inspect
   `result.data` for the ollama entry with `model == "gemma4:e4b"`.
2. Compare against a *catalogued* neighbour from the same response (any
   `openai` row) so the difference is visible rather than asserted in a
   vacuum.
3. Launch it with `thread/start` over the authenticated AppWire connection,
   using `{"harness":"evener","cwd":"<dir>","model":"ollama/gemma4:e4b","input":[{"type":"text","text":"hi"}]}`.
   Capture `result.thread.evener.ref`, poll `GET /api/sessions/local:$SID`
   to `state: idle`, then read the transcript with `go run ./cmd/evener
   doctor transcript "$SID" --state-dir "$state" --format outline --range last:30`.

### Browser

4. Open `/new`, open the Model field's trigger (the `<button>` whose
   accessible name ends `— change model`,
   `widgets/modelCatalog/index.tsx#ModelCatalog`), and inspect the row:
   ```javascript
   (() => {
     const rows = [...document.querySelectorAll('[role="listbox"][aria-label="Model"] li[role="option"]')];
     const row = rows.find(li => li.textContent.includes("Gemma4:e4b"));
     return {
       port: location.port,
       found: !!row,
       text: row?.textContent.trim(),
       childSpans: row ? [...row.children].map(c => c.tagName + ":" + c.textContent.trim()) : null,
       ariaDisabled: row?.getAttribute("aria-disabled"),
     };
   })()
   ```
5. Open `/settings/launch-evener` and run the identical snippet — the same
   widget, so the same expectation.
   The settings page renders **two** model pickers — the schema declares
   both `model` (label `Model`) and `fast_cheap_model` (label `Fast cheap
   model`) as `modelPicker` controls
   (`cmd/evener-hub/internal/launchconfig/schema.go:85,87`). Open the one
   whose block label reads exactly `Model`; only one panel is open at a
   time, so the listbox query above is unambiguous once it is.
6. TUI: `n` → `BTab BTab` (focus Model) → `Enter`; `capture-pane` and
   inspect the row under the `OLLAMA` header.

## Expected

- **Step 1 (exact)**: the entry is exactly
  `{"displayName":"Gemma4:e4b","model":"gemma4:e4b","provider":"ollama"}`
  — the badge/cost/context keys are **absent**, not `null` and not
  `false`: `supportsTools`, `supportsVision`, `supportsReasoning`,
  `supportsWebSearch`, `contextWindow`, `maxOutputTokens`,
  `inputCostPerMillion`, `outputCostPerMillion`. The display name
  is still the prettified id
  (`prettifyModelDisplayName`, `cmd/evener-hub/app_models.go#prettifyModelDisplayName`) —
  degradation drops facts, it does not drop the row. This exact key set
  is pinned by
  `TestEnrichModelListResponse_ModelWithoutCapsStillRenders`
  (`cmd/evener-hub/app_models_test.go#TestEnrichModelListResponse_ModelWithoutCapsStillRenders`).
- **Step 2**: the openai neighbour carries those keys, proving the
  absence in step 1 is about the model and not about the endpoint.
- **Steps 4/5**: `found` is true; `text` is exactly `Gemma4:e4b`;
  `childSpans` holds the single name span (plus a `✓` span only if this
  is the currently selected model) and **no third span** — the meta
  element is not in the DOM. `ariaDisabled` is null: the row is a normal
  selectable option. Falsify: the row is missing entirely; a meta span
  exists (empty or otherwise); the row renders as
  `ollama/gemma4:e4b` instead of the display name (see Sharp edges —
  that means the `model/list` enrichment failed, a different fault);
  or the row is greyed/disabled.
- **Step 6**: the TUI row reads `Gemma4:e4b  ollama/gemma4:e4b` with no
  ` · ctx · price · caps` tail — `modelInfoMetaTail` returns `""` for a
  descriptor carrying none of those fields
  (`cmd/evener-tui/hub_commands.go#modelInfoMetaTail`), pinned by
  `TestModelPickerItems_DescriptorWithoutMetadataStillRendersEmptyMeta`
  (`cmd/evener-tui/hub_model_picker_items_test.go#TestModelPickerItems_DescriptorWithoutMetadataStillRendersEmptyMeta`). Contrast the
  adjacent `Gpt 5.4` row's
  `1M ctx · $2.50/$15.00 · tools,vision,reasoning`.
- **Step 3**: `thread/start` returns a
  `result.thread.evener.ref` and the transcript outline shows real
  `USER_INPUT` and `ASSISTANT` turns with generated
  content — not a turn failure. On the recorded run the transcript also
  carried a `STEERING` correction (the harness's tool-call-format nudge)
  and a subsequent real tool-call round, i.e. the uncatalogued model is
  not just listed but fully functional end to end. Falsify: the launch
  is rejected, or the only assistant turn is an error.

## Cleanup

- `POST $HUB/api/sessions/local:$SID/shutdown` for the session step 3
  spawned (the old `$HUB/s/$SID/shutdown` shim 404s silently). Beyond
  that, nothing: the session's state is inside the hermetic tree removed
  with the rest of the shared teardown.

## Sharp edges

- **The web assertion is "no element", and an over-broad selector
  reports that trivially.** `document.querySelector('.meta')` finds
  nothing whether the row degraded correctly or the picker failed to
  render at all. Step 4's snippet reports `found` and the row's actual
  children so absence is only ever read off a row that demonstrably
  exists. Same reason step 2 exists at the JSON level.
- **A blank display name means a different failure.** The picker's label
  falls back to the qualified `provider/model` when `displayName` is
  empty (`toCatalogOptions`, `widgets/modelCatalog/catalogView.ts`).
  The typed `model/list` call is the single source for the spawn picker, so a
  request failure is shown by the picker's error alert (`ModelCatalog`'s
  `openPicker`) rather than silently becoming a second, less-informed list.
  If a successful row reads `ollama/gemma4:e4b` rather than `Gemma4:e4b`, inspect
  the `model/list` response before concluding anything about the catalog.
- Don't confuse "uncatalogued" with "disabled" — this model has no
  `DisabledReason` and is fully selectable and launchable. The TUI only
  sets `DisabledReason` from a provider's launch diagnostic
  (`cmd/evener-tui/hub_commands.go:468-492`), and graceful degradation
  here means *fewer rendered facts*, never a blocked action.
- The same `gemma4:e4b` row also appears in the `Recent` group (see
  `model-picker-recent-reflects-last-5-global.md`) — Recent reuses the
  same already-built entry, so the degradation carries over unchanged.
  One difference is expected and is not a badge: a Recent row's meta
  leads with the provider name (`rowMeta(entry, true)`,
  `pickerRows.ts:96`), so for this model the Recent row's meta is the
  single word `ollama` and the meta span **is** present there. Assert
  the no-meta-span rule on the provider-group row, not the Recent one.
- The empty-meta rule itself is pinned in isolation by
  `widgets/modelCatalog/pickerRows.test.ts:118` ("an entry with no
  metadata at all yields an empty string"). If that passes and the live
  row still shows a meta line, the enrichment invented metadata — look
  at `model/list`, not at the widget.
