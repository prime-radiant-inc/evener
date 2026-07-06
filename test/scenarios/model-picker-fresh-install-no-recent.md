# model-picker-fresh-install-no-recent: no Recent group before any session history

**What this covers**: Track B (model picker) Tasks 1-3's `attachRecentModels`/
`PastIndex.RecentModels` — with an empty Past index (no `meta.json` anywhere
under the state glob), the model picker must show only the provider-grouped
catalog, never an empty/degenerate "Recent" group, across all three pickers:
web spawn (`/new`), web settings (`/settings/launch-serf`), and the TUI's `n`
new-session picker.

## Pre-state

- Hermetic `$HOME`/`$XDG_STATE_HOME` scratch dirs with no prior sessions
  (`$XDG_STATE_HOME/serf/projects` does not exist yet) and no `index.db`
  entries — verified via `find $XDG_STATE_HOME/serf/projects` returning "no
  such file".
- Real `providers.toml`/`credentials.toml` copied in from `~/.serf` so at
  least one provider enumerates live (`ollama` + `openai` on this run;
  `lunarouter`'s cloudflare tunnel was down for the whole session — see
  Sharp edges).
- `serf-hub` built fresh (`go build -o /tmp/serf-hub ./cmd/serf-hub`) and
  started with `--serf /tmp/serf` against the hermetic env; browser
  authenticated via the printed `/auth?token=...` URL.

## Steps

1. `curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:19180/api/models?diagnostics=1`
   — the authoritative record.
2. Open `/new` in the browser, click `button[data-chip="model"]`, read
   `Array.from(document.querySelectorAll('.chip-picker-group')).map(e=>e.textContent)`.
3. Open `/settings/launch-serf`, click `[data-settings-model-picker]`, read
   `Array.from(document.querySelectorAll('.chip-picker-provider')).map(e=>e.textContent.trim())`
   (the settings picker uses a two-column provider-list layout, not the
   spawn form's flat grouped list — "Recent" would appear as a pinned
   pseudo-provider entry here, per `assets/settings-pickers.js`'s comment
   "Recent is a pinned-first pseudo-provider").
4. In a tmux session running `serf-tui --hub-addr 127.0.0.1:19180`, send `n`,
   then `BTab BTab` to focus the Model field, then `Enter` to open the
   picker overlay; capture-pane.

## Expected

- Step 1: `recent` key is `[]` (present, empty — never omitted/null).
- Step 2: group list is exactly `["ollama","openai"]` — no `"Recent"` entry.
- Step 3: provider list is exactly `["ollama","openai"]` — no `"Recent"`
  entry.
- Step 4: capture-pane shows group headers `OLLAMA` and `OPENAI` only, e.g.:
  ```
  OLLAMA
  > Gemma4:e4b  ollama/gemma4:e4b  (active)
  OPENAI
    Codex Auto Review  openai/codex-auto-review
    Gpt 5.4  openai/gpt-5.4  1M ctx · $2.50/$15.00 · tools,vision,reasoning
    ...
  ```
  no `RECENT` header anywhere above `OLLAMA`.
- Falsification: any picker renders a `Recent`/`RECENT` group (even an
  empty one with a header and no rows), or `/api/models?diagnostics=1`'s
  `recent` field is missing/null instead of `[]`.

## Cleanup

- Kill the hub process and tmux session; remove the hermetic scratch
  `$HOME`/`$XDG_STATE_HOME` trees (or keep them — the next card,
  `model-picker-recent-reflects-last-5-global`, reuses this exact state dir
  to seed history on top of a verified-empty starting point).

## Sharp edges

- `lunarouter` (a `type=openai chat-completions` instance pointed at a
  `trycloudflare.com` tunnel) was unreachable for this entire session (DNS
  resolution failure — the tunnel had expired) and surfaced only as a
  `diagnostics[]` entry, not a picker group. This is real infra being down,
  not a test artifact; re-verify with `lunarouter` live if convenient.
- The settings picker (`settings-pickers.js`) and the spawn picker
  (`spawn.js`) use *different* DOM structures for the same "Recent" concept
  — a flat grouped list with `.chip-picker-group` headers on the spawn form,
  vs. a two-column `.chip-picker-provider` sidebar list on settings. A card
  asserting on the wrong selector for the wrong page silently finds nothing
  and can misreport a false pass — confirm the picker's per-page class names
  before asserting absence.
