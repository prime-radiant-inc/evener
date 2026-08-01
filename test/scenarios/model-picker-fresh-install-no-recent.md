# model-picker-fresh-install-no-recent: no Recent group before any session history

**What this covers**: Track B (model picker) Tasks 1-3's
`attachRecentModels` / `PastIndex.RecentModels` — with an empty Past
index (no `meta.json` anywhere under the state glob), every model picker
must show only the provider-grouped catalog and never an
empty/degenerate "Recent" group. Three surfaces: web spawn (`/new`), web
settings (`/settings/launch-serf`), and the TUI's `n` new-session
picker.

The rule is enforced independently at three layers, which is what makes
it worth an e2e card at all:

- **Wire**: `recent` is always present and always an array, never null —
  `writeModelsResponse` coerces nil to `[]`
  (`cmd/serf-hub/web_spawn.go#writeModelsResponse`), and
  `recentModelEntriesFromDescriptors` returns nil for an empty ref list
  (`:248-251`).
- **Web**: `buildPickerRows` emits the `Recent` group head *only* when
  the filtered recent list is non-empty
  (`widgets/modelCatalog/pickerRows.ts:88-97`).
- **TUI**: `modelPickerItemsFromResponse` early-returns before building
  any Recent items when `len(resp.Recent) == 0`
  (`cmd/serf-tui/hub_commands.go:493-495`).

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map. The old `button[data-chip="model"]` / `.chip-picker-group`
/ `[data-settings-model-picker]` / `.chip-picker-provider` selectors this
card used are all gone with the vanilla frontend (`660376f78`). Both
pickers are now the **same** shared ARIA combobox widget
(`widgets/modelCatalog/`): `panes/spawn/ModelField.tsx:48` and
`panes/settings/sections/launchShared/fields.tsx:198` render the same
`<ModelCatalog>`, differing only in which `loadCatalog` they inject. So
there is one markup to assert against, not two.

## Pre-state

- Hermetic `$HOME`/`$XDG_STATE_HOME` scratch dirs with no prior sessions
  (`$XDG_STATE_HOME/serf/projects` does not exist yet) and no `index.db`
  entries — verified via `find $XDG_STATE_HOME/serf/projects` returning
  "no such file". This is the whole precondition: `RecentModels` reads
  the Past index (`cmd/serf-hub/internal/hubcore/past.go#RecentModels`), and
  an empty index is the only way to get an empty Recent honestly.
- Real `providers.toml`/`credentials.toml` copied **in** from `~/.serf`
  so at least one provider enumerates live (`ollama` + `openai` on this
  run; `lunarouter`'s cloudflare tunnel was down for the whole session —
  see Sharp edges). This is a one-time READ out of the real `~/.serf`
  into the isolated `$HOME` — the sanctioned "copy in a scratch
  credentials.toml first" recipe from `docs/agentic-testing.md`. Nothing
  in this card ever writes to the real `~/.serf`.
- `serf-hub` built fresh and started against that hermetic env on a
  kernel-assigned port (`-addr 127.0.0.1:0`, read `$PORT` back out of
  the hub's own log — Setup checklist). A frontend built with
  `make build-web` before the hub binary, or the browser steps have no
  app to drive.
- Browser authenticated via `/auth?token=...`.

## Steps

### Browser-free (the authoritative record)

1. ```bash
   curl -s -H "Authorization: Bearer $TOKEN" "$HUB/api/models?diagnostics=1" \
     | jq '{recent, providers: ([.models[].provider] | unique)}'
   ```
   `?diagnostics=1` is not optional here — the bare response is a
   models-only array with no `recent` key at all
   (`writeModelsResponse`, `web_spawn.go#writeModelsResponse`), so checking the
   default shape proves nothing.

### Browser

2. Open `/new`, click the Model field's trigger (the `<button>` whose
   accessible name ends `— change model`,
   `widgets/modelCatalog/index.tsx:380-398`), and read the group heads:
   ```javascript
   ({
     port: location.port,
     groups: [...document.querySelectorAll('[role="listbox"][aria-label="Model"] li[role="presentation"]')]
       .map(li => li.textContent.trim()),
     optionCount: document.querySelectorAll('[role="listbox"][aria-label="Model"] li[role="option"]').length,
   })
   ```
3. Open `/settings/launch-serf`, open its model field, run the identical
   snippet. Same widget, same markup — the assertion does not change.
   The settings page renders **two** model pickers — the schema declares
   both `model` (label `Model`) and `fast_cheap_model` (label `Fast cheap
   model`) as `modelPicker` controls
   (`cmd/serf-hub/internal/launchconfig/schema.go:85,87`). Open the one
   whose block label reads exactly `Model`; only one panel is open at a
   time, so the listbox query above is unambiguous once it is.

### TUI

4. In a tmux session running `serf-tui --hub-addr 127.0.0.1:$PORT`, send
   `n` (opens the spawn form focused on Prompt,
   `cmd/serf-tui/hub_keys.go:95`), then `BTab BTab` to reach the Model
   field (field order Prompt/Harness/Model/Dir,
   `cmd/serf-tui/hub_model.go:27-30`; backwards focus wraps through Dir,
   `hub_spawn.go:222-230`), then `Enter` to open the picker overlay;
   `capture-pane`.

## Expected

- **Step 1**: `recent` is `[]` — present, empty, never omitted and never
  `null`. `providers` is the set that actually enumerated (`["ollama",
  "openai"]` on this run).
- **Steps 2/3**: `groups` is exactly the enumerated provider names, e.g.
  `["ollama","openai"]` — no `"Recent"` entry. `optionCount` is
  non-zero (a picker that rendered nothing at all would trivially
  "pass" the no-Recent assertion; this is the guard against that false
  green).
- **Step 4**: capture-pane shows group headers `OLLAMA` and `OPENAI`
  only — uppercased by `strings.ToUpper(item.Group)`
  (`cmd/serf-tui/internal/tuipick/model_picker.go:165-167`) — e.g.:
  ```
  OLLAMA
  > Gemma4:e4b  ollama/gemma4:e4b  (active)
  OPENAI
    Codex Auto Review  openai/codex-auto-review
    Gpt 5.4  openai/gpt-5.4  1M ctx · $2.50/$15.00 · tools,vision,reasoning
    ...
  ```
  with no `RECENT` header anywhere above `OLLAMA`.
- Falsification: any picker renders a `Recent`/`RECENT` group (even an
  empty one with a header and no rows), or
  `/api/models?diagnostics=1`'s `recent` field is missing or `null`
  instead of `[]`.

## Cleanup

- Kill the hub by the PID you captured and the tmux session you named;
  remove the hermetic scratch `$HOME`/`$XDG_STATE_HOME` trees (or keep
  them — the next card, `model-picker-recent-reflects-last-5-global`,
  reuses this exact state dir to seed history on top of a
  verified-empty starting point).

## Sharp edges

- **The two pickers are the same component now.** This card used to warn
  that spawn and settings had different DOM shapes for the same "Recent"
  concept (a flat grouped list vs. a two-column provider sidebar) and
  that asserting the wrong selector on the wrong page could silently
  find nothing and misreport a false pass. That hazard is gone: both
  render `widgets/modelCatalog/`'s `<ModelCatalog>`, and `Recent`
  appears in both as a plain `li[role="presentation"]` group head
  labelled `Recent` (`pickerRows.ts:31,89-91`). The false-pass shape it
  warned about is still real in general, which is why step 2's snippet
  also reports `optionCount` — an assertion of *absence* is worthless
  without evidence the list rendered at all.
- **`role="presentation"` is two different things.** The listbox uses it
  for group heads *and* for unavailable-provider lines
  (`widgets/modelCatalog/index.tsx:263-277`). An unavailable line reads
  `<provider> — <message>` (`unavailableLine`, `pickerRows.ts:60-65`),
  so a down provider shows up in `groups` above. That is expected —
  check for the exact string `Recent`, not for the group count.
- **The spawn picker's Recent is additionally scoped.** Even with a
  populated index, `mergeScopedCatalog` filters Recent to models the
  current harness/cwd actually offers
  (`widgets/modelCatalog/scopedCatalog.ts:26-27`). Irrelevant to this
  card (empty stays empty) but it matters the moment you seed history —
  see `model-picker-recent-reflects-last-5-global.md`.
- `lunarouter` (a `type=openai chat-completions` instance pointed at a
  `trycloudflare.com` tunnel) was unreachable for this entire session
  (DNS resolution failure — the tunnel had expired) and surfaced only as
  a `diagnostics[]` entry, not a picker group. Real infra being down,
  not a test artifact; re-verify with `lunarouter` live if convenient.
- The unit-level counterpart is
  `TestModelPickerItemsFromResponse_NoRecentOmitsGroup`
  (`cmd/serf-tui/hub_model_picker_items_test.go#TestModelPickerItemsFromResponse_NoRecentOmitsGroup`) for the TUI and
  `pickerRows.test.ts:54-55` ("no Recent group when the envelope carries
  none") for the web. If those pass and the live picker still shows a
  Recent header, the bug is in what the hub put in `recent`, not in the
  rendering.
