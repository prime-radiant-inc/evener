# model-picker-recent-reflects-last-5-global: Recent is the last 5 distinct models, globally, cwd-independent

**What this covers**: Track B Tasks 1-3's `appwire.ModelListResponse.Recent`,
`hubcore.PastIndex.RecentModels` (`recentModelsLimit = 5`,
`cmd/serf-hub/app_models.go:30`), and `attachRecentModels`
(`app_models.go#attachRecentModels`) — spawning across many working directories and
models must produce a `Recent` group that is (a) capped at the 5
most-recently-*touched* distinct `(provider, model)` pairs, (b) ordered
most-recent-first, (c) identical regardless of which `cwd` the caller
scopes the request to, and (d) consistent across the three pickers
(web spawn, web settings, TUI) — with one scoping caveat that is by
design, in Sharp edges.

`RecentModels` itself is where (a)-(c) live: it walks the Past index in
its global most-recently-updated-first order, skips blank
provider/model, dedupes on first occurrence, and stops at the limit
(`cmd/serf-hub/internal/hubcore/past.go:653-677`). Nothing in it
consults a cwd.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the
selector map. The old `.chip-picker-model` / `[data-settings-model-picker]`
selectors are gone with the vanilla frontend (`660376f78`). Both web
pickers are the same shared ARIA combobox
(`widgets/modelCatalog/`): the `Recent` head is an
`li[role="presentation"]` labelled exactly `Recent`
(`widgets/modelCatalog/pickerRows.ts:31,88-99`) and its rows are the
`li[role="option"]` entries between it and the next
`role="presentation"` row.

**There is no localStorage key for Recent.** It is entirely
server-derived from session metas; the only spawn-related client storage
is the unrelated `serf-hub.spawn-defaults.*` namespace. Don't seed
Recent — spawn real sessions.

## Pre-state

- Same hermetic hub as `model-picker-fresh-install-no-recent.md`,
  continued (state accumulates across cards 2-5 per the task's own
  guidance) — including its isolated `$HOME`/`$XDG_STATE_HOME` and its
  one-time copy-in of `providers.toml`/`credentials.toml`. Do the
  credential copy-in the way that card describes rather than reaching
  for a real state path from here.
- `past_index_rebuild_interval = "2s"` in the hermetic `hub.toml`. The
  default is 60s (`cmd/serf-hub/config.go:33,62,134-135`) and a
  just-finished session does not reach `RecentModels` until the index
  rebuilds — on the default this card reads as a total failure for a
  minute.
- Real `ollama` (local `gemma4:e4b`, already pulled) and `openai`
  providers live; `lunarouter` down for the whole session (see Sharp
  edges).
- 6 scratch working directories (`proj8`..`proj13`), one per spawn.

## Steps

### Browser-free (the exact assertions — (a), (b) and (c) are all checkable here)

1. Spawn 6 real sessions via `POST /api/spawn`
   (`{"prompt":"hi","harness":"serf","model":"<provider/model>","working_dir":"<projN>"}`),
   one per distinct model, in this order, 3s apart: `ollama/gemma4:e4b`,
   `openai/codex-auto-review`, `openai/gpt-5.3-codex-spark`,
   `openai/gpt-5.4`, `openai/gpt-5.4-mini`, `openai/gpt-5.5` — 6
   distinct `(provider,model)` pairs across 2 real providers, 6 distinct
   dirs.
2. Wait for both the real turns to finish (poll
   `GET /api/sessions/local:$SID` for `state: idle`) and for the hub's
   past-index rebuild.
3. `GET /api/models?diagnostics=1` with no `cwd`, then again with
   `cwd=proj8`, `cwd=proj9`, `cwd=proj13`, and a `cwd` that doesn't
   exist at all — compare `recent[]` `(provider, model)` pairs and order
   across all five calls:
   ```bash
   for q in "" "&cwd=$proj8" "&cwd=$proj9" "&cwd=$proj13" "&cwd=/nope/xyz"; do
     curl -s -H "Authorization: Bearer $TOKEN" \
       "$HUB/api/models?diagnostics=1$q" \
       | jq -c '[.recent[] | "\(.provider)/\(.model)"]'
   done
   ```
4. Cross-check the recency claim against the sessions' own metas — the
   order tracks each session's `updated_at`, not spawn order:
   ```bash
   find "$XDG_STATE_HOME/serf/projects" -name '*.meta.json' \
     -exec jq -r '[.updated_at, .profile_id, .model] | @tsv' {} \; | sort -r
   ```

### Browser

5. Open `/new`, open the Model field's trigger (the `<button>` whose
   accessible name ends `— change model`), and read the rows in DOM
   order, tagging each with its role so the `Recent` block is
   delimitable:
   ```javascript
   ({
     port: location.port,
     rows: [...document.querySelectorAll('[role="listbox"][aria-label="Model"] li')]
       .map(li => ({ role: li.getAttribute("role"), text: li.textContent.trim() })),
   })
   ```
6. Open `/settings/launch-serf`, open its model field, run the identical
   snippet.
   The settings page renders **two** model pickers — the schema declares
   both `model` (label `Model`) and `fast_cheap_model` (label `Fast cheap
   model`) as `modelPicker` controls
   (`cmd/serf-hub/internal/launchconfig/schema.go:85,87`). Open the one
   whose block label reads exactly `Model`; only one panel is open at a
   time, so the listbox query above is unambiguous once it is.

### TUI

7. `serf-tui --hub-addr 127.0.0.1:$PORT`, `n` → `BTab BTab` (focus
   Model) → `Enter` to open the picker; `capture-pane` and read the
   `RECENT` group's rows.

## Expected

- **Step 3 (exact)**: all five calls return the **same** 5 entries in
  the **same** order — observed on the recorded run:
  `[(ollama,gemma4:e4b), (openai,gpt-5.5), (openai,gpt-5.4-mini),
  (openai,gpt-5.4), (openai,gpt-5.3-codex-spark)]`.
  `openai/codex-auto-review` — the 6th distinct model, whose session's
  real turn finished earliest (`updated_at` oldest) — is correctly
  evicted, not merely appended past a length-5 cap.
- **Step 4**: step 3's order matches the `updated_at`-descending order
  of the metas. On the recorded run `ollama/gemma4:e4b` came out
  most-recent even though it was spawned first, because the local
  model's cold-start load ran longer than the OpenAI turns' network
  latency. That is genuine activity-based recency, not a bug — see
  Sharp edges.
- **Steps 5/6 (settings picker is the strict one)**: the rows between
  the `Recent` head and the next `role="presentation"` row are step 3's
  5 models, in step 3's order, rendered as their display names
  (`Gemma4:e4b`, `Gpt 5.5`, `Gpt 5.4 Mini`, `Gpt 5.4`,
  `Gpt 5.3 Codex Spark`). Each Recent row's meta text **leads with the
  provider name** — `rowMeta(entry, true)` for the Recent block only
  (`pickerRows.ts:37-45,96`), because Recent mixes providers and
  the row would otherwise be unattributable.
- **Step 7**: the `RECENT` header (uppercased,
  `cmd/serf-tui/internal/tuipick/model_picker.go:165-167`) leads the
  list with the same 5 rows in the same order; the Recent block is
  literally prepended to the provider-grouped items
  (`cmd/serf-tui/hub_commands.go:493-500`).
- Falsification: `recent[]` differs by `cwd`; `codex-auto-review`
  appears in `Recent` (6th distinct, should be evicted); the settings
  picker's or the TUI's Recent order disagrees with
  `/api/models?diagnostics=1`; a Recent row is missing its provider
  prefix; or a provider-group row is interleaved into the Recent block.

## Cleanup

- Shut down every session you spawned:
  `POST $HUB/api/sessions/local:$SID/shutdown` (the old
  `$HUB/s/$SID/shutdown` shim 404s silently and leaves the daemon
  running).
- Kill the hub by the PID you captured and the tmux session you named.
- Remove all scratch project directories and the hermetic
  `$HOME`/`$XDG_STATE_HOME` trees.

## Sharp edges

- **The ollama credential-gate bug this card used to work around is
  fixed — do not re-add the workaround.** Spawning `ollama/gemma4:e4b`
  against a hub with any `providers.toml` present used to fail with
  `"provider credentials missing for ollama: set via serf/auth/apiKey/set
  or set the matching env var"`, because
  `validateProviderCredentials`'s config-path branch had no equivalent
  of the no-config branch's `credentials.SourceNone` bypass. This card
  worked around it by adding a placeholder `api_key` to the hermetic
  `[instances.ollama]` block. Commit `1b717fe72` fixed it properly: the
  config path now returns nil for any instance whose *behavior tag*
  declares auth mode `none` (`cmd/serf-hub/spawn.go:567-573`,
  `envvars.RequiresNoCredential`, `envvars/providers.go#RequiresNoCredential`; ollama's
  `AuthModes: []string{"none"}` at `envvars/providers.go:155-160`),
  pinned by `TestValidateProviderCredentials_ConfigInstanceAuthModeNone`
  (`cmd/serf-hub/spawn_test.go:1243`). Step 1 spawns ollama with no
  credential at all and must succeed. If it doesn't, that is a fresh
  regression of `1b717fe72` — file it, don't paper over it with an
  `api_key` line.
- **The spawn picker's Recent is legitimately narrower than the
  settings picker's.** `/api/models` Recent is already filtered to
  models the hub offers (`recentModelEntriesFromDescriptors`,
  `cmd/serf-hub/web_spawn.go#recentModelEntriesFromDescriptors`), and the *spawn* picker filters
  it a second time to the harness/cwd-scoped set it actually renders
  (`mergeScopedCatalog`, `widgets/modelCatalog/scopedCatalog.ts:26-27`).
  So on a non-default harness, or a cwd whose project config narrows the
  set, the spawn picker's Recent can be a strict subset of step 3's list
  while the settings picker's matches it exactly. Assert
  order-and-membership strictly on the settings picker; on the spawn
  picker assert that its Recent is a subsequence of step 3's, in the
  same relative order.
- **Recency tracks `updated_at` (bumped when a turn *completes*), not
  spawn order.** A session spawned first can still end up "most recent"
  if its completion lags behind sessions spawned after it (seen live
  with the ollama cold-start above). Don't assume spawn order predicts
  Recent order when working from real completed turns; assert against
  actual `updated_at` values (step 4). If only spawn-time ordering
  matters for some future card, spawn-and-leave-idle without waiting for
  completion.
- **Recent is capped before it is filtered.** `RecentModels(5)` takes
  the 5 most recent distinct pairs from the index, and *then*
  `attachRecentModels` / `recentModelEntriesFromDescriptors` drop any
  the hub no longer offers (`app_models.go#attachRecentModels`,
  `web_spawn.go:248-264`). So a hub that retired one of the top 5 shows
  **4** Recent rows, not 4-plus-the-6th. That is correct; a card
  asserting "always exactly 5" would be wrong.
- `lunarouter`'s cloudflare tunnel was down for this entire session
  (real infra, not a test artifact) — only `ollama` and `openai`
  contributed to this card's 6 distinct models / 2 providers.
