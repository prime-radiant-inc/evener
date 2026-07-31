# model-picker-dated-snapshot-sorts-last: bare family id before its dated snapshot, within a provider

**What this covers**: Track B Tasks 4 (`sortModelEntriesDatedLast`,
`cmd/serf-hub/web_spawn.go:226`, over `isDatedSnapshotModelID`, `:197`)
and 11 (TUI `modelPickerItems`, `cmd/serf-tui/hub_commands.go:469-482`) —
within one provider's group, a bare model id (e.g. `claude-opus-4-6`)
must render before its dated snapshot (`claude-opus-4-6-20251101`),
regardless of the order the live listing returned them in.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map. What changed since this card was written is *where* the
rule is observable, and it is good news: **the exact assertion is now
browser-free.** `sortModelEntriesDatedLast` runs inside
`modelDescriptorsToAPIModels` (`web_spawn.go:309`) and again on the
live-model fallback (`:470`), so the order is visible in the
`GET /api/models` JSON directly — no picker, no Chrome, no
provider-with-a-dated-pair required in a *rendered* list, only in the
hub's enumeration. The old card had no such level to check at and fell
back to unit tests for everything.

The browser half is a second-order confirmation that the rendered list
preserves the server's order, and it belongs on the **settings** picker
specifically — see Sharp edges for why the spawn picker is a different
list.

## Pre-state

- Hub running on an isolated `$HOME` and a kernel-assigned port (Setup
  checklist in `docs/agentic-testing.md`), with `--serf` resolvable.
- For the browser step only: a frontend built with `make build-web`
  before the hub binary.
- A provider whose live listing carries **both** a bare model id and its
  dated snapshot. This is the constraint that blocked the original live
  run — see "Live e2e coverage" below; the environments investigated
  there still apply.

## Steps

### Browser-free (the exact assertion)

1. `GET /api/models?diagnostics=1` and read the provider's group in
   response order:
   ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/models?diagnostics=1" \
     | jq -r '.models[] | "\(.provider)/\(.model)"'
   ```
   Within each provider run, every id matching `-\d{8}(-v\d+)?$`
   (`datedSnapshotSuffix`, `web_spawn.go:193`) must come after every id
   in that run that doesn't. Providers themselves sort ascending
   (`sortModelEntriesDatedLast`, `:226-241`).
2. Same check through the TUI's own path: `modelPickerItems` applies the
   identical predicate over `buildModelPickerItems`'s unordered output
   (`hub_commands.go:469-482`, the sort at `:471-480`).

### Browser (does the rendered list preserve it)

3. Open `/settings/launch-serf`, click the model field's trigger (the
   `<button>` whose accessible name ends `— change model`,
   `widgets/modelCatalog/index.tsx:380-398`), and read the listbox in
   DOM order:
   ```javascript
   ({
     port: location.port,
     rows: [...document.querySelectorAll('[role="listbox"][aria-label="Model"] li')]
       .map(li => ({ role: li.getAttribute("role"), text: li.textContent.trim() })),
   })
   ```
   `role="presentation"` rows are group heads (and unavailable-provider
   lines — see Sharp edges); `role="option"` rows are the models, in the
   server's order (`toCatalogOptions` preserves it,
   `widgets/modelCatalog/catalogView.ts:24-28`).
   The settings page renders **two** model pickers — the schema declares
   both `model` (label `Model`) and `fast_cheap_model` (label `Fast cheap
   model`) as `modelPicker` controls
   (`cmd/serf-hub/internal/launchconfig/schema.go:85,87`). Open the one
   whose block label reads exactly `Model`; only one panel is open at a
   time, so the listbox query above is unambiguous once it is.
4. In a tmux session running `serf-tui --hub-addr 127.0.0.1:$PORT`,
   press `n` (opens the spawn form focused on Prompt,
   `cmd/serf-tui/hub_keys.go:95`, `hub_spawn.go:300-311`), then
   `BTab BTab` to reach the Model field (field order is
   Prompt/Harness/Model/Dir, `cmd/serf-tui/hub_model.go:27-30`, and
   focus wraps backwards through Dir — `advanceSpawnFocus`,
   `hub_spawn.go:222-230`), then `Enter` to open the picker overlay;
   `capture-pane`.

## Expected

- **Step 1 (exact)**: within the provider's run, `claude-opus-4-6`
  precedes `claude-opus-4-6-20251101`, and no dated id appears before a
  bare one in the same run. Falsify: any dated id ahead of a bare id
  within one provider, or the provider runs interleaved.
- **Step 3**: the `role="option"` rows appear in exactly step 1's order.
  Falsify: the DOM order differs from the JSON order — the picker is
  re-sorting, which it must not do.
- **Step 4**: under the provider's uppercased group header
  (`strings.ToUpper(item.Group)`,
  `cmd/serf-tui/internal/tuipick/model_picker.go:165-167`) the bare row
  precedes the dated row.
- Note the display names are prettified and the dated suffix is
  *stripped from the label*, so the two rows read identically
  (`prettifyModelDisplayName`, `web_spawn.go:209-221`, and its test
  expecting `"Claude Opus 4 6"`). Distinguish them by the qualified id
  in the row's meta/id text, not by the display name.

## Live e2e coverage: environment-blocked when this card was recorded

No live-reachable provider in that environment exposed both a bare model
id and its dated snapshot through the production listing pipeline, so
the *browser* steps could not be executed against a real hub. That was a
genuine, investigated environmental gap — no stub or mock was
substituted (this project's standing rule against mocks in e2e tests).
Investigated and ruled out:

- **`lunarouter`** (the one instance whose live catalog plausibly carries
  Anthropic dated snapshots) — its `trycloudflare.com` tunnel could not
  be resolved (`Could not resolve host`) for the entire session,
  re-checked repeatedly including at card-writing time. Real infra being
  down, not a test artifact.
- **`openai`** (the OAuth-authenticated live account) enumerated only 5
  models (`codex-auto-review`, `gpt-5.3-codex-spark`, `gpt-5.4`,
  `gpt-5.4-mini`, `gpt-5.5`) — no dated snapshot ids at all, confirmed
  via repeated live `/api/models` calls.
- **`ollama`** model ids always carry a `name:tag` suffix (e.g.
  `gemma4:e4b`, and even an `ollama cp`'d rename came back as
  `claude-opus-4-5:latest`) — the trailing `:tag` breaks
  `isDatedSnapshotModelID`'s end-anchored `-\d{8}(-v\d+)?$` match no
  matter what the name portion is, so Ollama structurally cannot produce
  a test id this rule would recognize as "dated".
- **`openrouter`** (no API key needed for its live, unauthenticated
  public `/models` listing — verified reachable, 340 real models,
  including `qwen/qwen3.5-plus-20260420` which *does* match the 8-digit
  dated regex) was ruled out one layer further upstream:
  `launchCheckModelVisible`
  (`cmd/serf/internal/launchcheck/launchcheck.go:274-291`) filters every
  `openrouter`-tagged model to ones with an exact embedded-catalog match
  AND `SupportsTools` (`:286-288`) — none of the real openrouter ids
  (including the qwen dated pair) satisfy that filter, so they never
  reach the picker at all regardless of this track's own code.

Given no live path existed, the rule was verified as passing, on that
build, via:

- `TestIsDatedSnapshotModelID` (`cmd/serf-hub/app_models_test.go:151`).
- `TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast`
  (`cmd/serf-hub/app_models_test.go:169-193`) — asserts
  `modelDescriptorsToAPIModels([{anthropic, claude-opus-4-6-20251101},
  {anthropic, claude-opus-4-6}, {openai, gpt-5.2}], nil)` returns the
  anthropic group in order `[claude-opus-4-6, claude-opus-4-6-20251101]`
  regardless of input order.
- `TestModelPickerItems_SortsDatedSnapshotLastWithinProvider`
  (`cmd/serf-tui/hub_model_picker_items_test.go:59-70`) — the same
  assertion for the TUI's `modelPickerItems`.

Both are still the fallback when no dated pair is reachable:

```bash
go test ./cmd/serf-hub/ -run TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast
go test ./cmd/serf-tui/ -run TestModelPickerItems_SortsDatedSnapshotLastWithinProvider
```

Step 1 above is the part of that gap the rewrite closes: it needs a
provider that *enumerates* a dated pair, not one whose rows a browser
can reach.

## Re-running this card live

If `lunarouter`'s tunnel is restored, or Anthropic OAuth credentials are
added to this hub's isolated `$XDG_STATE_HOME/serf/auth/anthropic.json`
(mirroring the `openai.json` pattern used elsewhere in this set), re-run
steps 1-4 against a provider group containing a known Anthropic
dated-snapshot family (`claude-opus-4-6` / `claude-opus-4-6-20251101`,
or `claude-opus-4-7` / `claude-opus-4-7-20260416` — both present in
`llm/data/litellm_model_catalog.json` with identical metadata on the
bare and dated entries).

## Cleanup

Kill the hub by the PID you captured and the tmux session you named;
remove your `$run` dir. Nothing is spawned by this card.

## Sharp edges

- **The spawn picker is not sorted by this rule, and that is not a
  bug — it is a different list.** The settings picker's set IS
  `/api/models` (`fetchModelCatalog()`,
  `panes/settings/sections/launchShared/fields.tsx:189-198`), so it
  inherits `sortModelEntriesDatedLast`. The spawn picker's set is the
  harness/cwd-scoped appwire `model/list`, merged with `/api/models`
  only for *metadata* — `mergeScopedCatalog` builds its `models` array
  by mapping over the scoped list, so the scoped order wins
  (`widgets/modelCatalog/scopedCatalog.ts:17-29`,
  `panes/spawn/ModelField.tsx:33-46`). `model/list` returns
  `serfLaunchModelList`'s data untouched (`cmd/serf-hub/app_models.go:76-81`);
  the launch check sorts models by raw id within a provider
  (`launchcheck.go:175`). For a bare/dated *pair* that by-id sort gives
  the same answer (the bare id is a prefix, so it sorts first), which is
  why the pair assertion still holds there — but the stronger property
  ("every dated id after every bare id in the group") is only
  guaranteed on `/api/models` and in the TUI. Assert group-wide ordering
  on the settings picker; a spawn-picker failure of the *group-wide*
  form is expected, not a regression.
- **`role="presentation"` is two different things.** The listbox uses it
  for provider/`Recent` group heads *and* for unavailable-provider lines
  (`widgets/modelCatalog/index.tsx:263-277`). A group head is a bare
  provider name; an unavailable line reads `<provider> — <message>`
  (`unavailableLine`, `widgets/modelCatalog/pickerRows.ts:60-65`). Only
  `role="option"` rows are models.
- Don't confuse "dated" with "has a date-shaped substring anywhere" —
  the regex is anchored to the *end* of the id (`-\d{8}(-v\d+)?$`,
  `web_spawn.go:193`), applied to the segment after the last `/`
  (`:197-202`). That is why Ollama's `:latest`/`:tag` suffix defeats it,
  and why OpenRouter's `-02-15`-style partial dates (5 digits, not 8)
  are correctly treated as non-dated by the same rule that correctly
  flags `-20260420` as dated.
- The `openrouter` launch-check visibility filter is unrelated to and
  upstream of anything in this track — it's why `GET /api/models` for an
  `openrouter` instance only ever showed 6-7 curated
  deepseek/inception/minimax models instead of openrouter's real
  340-model live catalog. Worth knowing if a future card wants to use
  openrouter for anything beyond a quick reachability check.
