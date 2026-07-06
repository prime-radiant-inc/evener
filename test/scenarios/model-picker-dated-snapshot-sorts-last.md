# model-picker-dated-snapshot-sorts-last: bare family id before its dated snapshot, within a provider

**What this covers**: Track B Tasks 4 (web `sortModelEntriesDatedLast`/
`isDatedSnapshotModelID`, `cmd/serf-hub/web_spawn.go`) and 11 (TUI
`modelPickerItems`, `cmd/serf-tui/hub_commands.go`) — within one provider's
group, a bare model id (e.g. `claude-opus-4-6`) must render before its dated
snapshot (`claude-opus-4-6-20251101`), regardless of the order the live
listing returned them in.

## Live e2e coverage: environment-blocked, documented per the skill's
## over-specification guidance — verified instead via the existing unit tests

No live-reachable provider in this environment exposes both a bare model id
and its dated snapshot through the actual production listing pipeline, so
this card could not be executed against the real running hub + browser/TUI.
This is a genuine, investigated, environmental gap — not a shortcut — and no
stub/mock was substituted (per this project's standing rule against mocks in
e2e tests). Investigated and ruled out:

- **`lunarouter`** (the one instance whose live catalog plausibly carries
  Anthropic dated snapshots) — its `trycloudflare.com` tunnel could not be
  resolved (`Could not resolve host`) for the entire session, re-checked
  repeatedly including at card-writing time. Real infra being down, not a
  test artifact.
- **`openai`** (the OAuth-authenticated live account used throughout this
  session) enumerates only 5 models (`codex-auto-review`,
  `gpt-5.3-codex-spark`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.5`) — no dated
  snapshot ids at all, confirmed via repeated live `/api/models` calls.
- **`ollama`** model ids always carry a `name:tag` suffix (e.g.
  `gemma4:e4b`, and even a `ollama cp`'d rename came back as
  `claude-opus-4-5:latest`) — the trailing `:tag` breaks
  `isDatedSnapshotModelID`'s `-\d{8}(-v\d+)?$` suffix match no matter what
  the name portion is, so Ollama structurally cannot produce a test id this
  rule would recognize as "dated."
- **`openrouter`** (no API key needed for its live, unauthenticated public
  `/models` listing — verified reachable, 340 real models, including
  `qwen/qwen3.5-plus-20260420` which *does* match the 8-digit dated regex)
  was ruled out one layer further upstream: `cmd/serf/internal/launchcheck
  /launchcheck.go`'s `launchCheckModelVisible` filters every `openrouter`-
  tagged model to ones with an *exact* embedded-catalog key match AND
  `SupportsTools` — none of the real openrouter-listed ids (including the
  qwen dated pair) satisfy that filter, so they never reach the picker at
  all regardless of this track's own code.

Given no live path exists, this rule is instead verified as passing, on this
exact build, via:
- `TestIsDatedSnapshotModelID` and
  `TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast`
  (`cmd/serf-hub/app_models_test.go:89-128`) — asserts
  `modelDescriptorsToAPIModels([{anthropic, claude-opus-4-6-20251101},
  {anthropic, claude-opus-4-6}, {openai, gpt-5.2}], nil)` returns the
  anthropic group in order `[claude-opus-4-6, claude-opus-4-6-20251101]`
  regardless of input order.
- `TestModelPickerItems_SortsDatedSnapshotLastWithinProvider`
  (`cmd/serf-tui/hub_model_picker_items_test.go:59-70`) — same assertion for
  the TUI's `modelPickerItems`.
- Both re-run and confirmed green during this task:
  `go test ./cmd/serf-hub/ -run TestModelDescriptorsToAPIModels_UsesPrettifiedDisplayNameAndSortsDatedLast` and
  `go test ./cmd/serf-tui/ -run TestModelPickerItems_SortsDatedSnapshotLastWithinProvider`.

## Partial live evidence (sort/group machinery itself, not the dated-vs-bare rule)

The surrounding provider-grouping and stable-sort machinery this rule is
built on top of *was* exercised live and repeatedly across every other card
in this set (`model-picker-fresh-install-no-recent.md`,
`model-picker-recent-reflects-last-5-global.md`): the web spawn/settings
pickers and the TUI all group `ollama`/`openai` rows correctly, and — after
this session's own spawns —
`model-picker-recent-reflects-last-5-global.md`'s Recent group demonstrably
sorts by real recency with a stable, deterministic order across five
different query scopes and three pickers. `sortModelEntriesDatedLast`'s
dated-check is a small, independently-tested predicate layered on that same
already-live-verified stable sort.

## Re-running this card live

If `lunarouter`'s tunnel is restored, or Anthropic OAuth credentials are
added to this hub's `$XDG_STATE_HOME/serf/auth/anthropic.json` (mirroring
the `openai.json` pattern used elsewhere in this session), re-run: open
`/new` and `/settings/launch-serf`'s model pickers, find the provider group
containing a known Anthropic dated-snapshot family (`claude-opus-4-6` /
`claude-opus-4-6-20251101`, or `claude-opus-4-7` / `claude-opus-4-7-20260416`
— both present in `llm/data/litellm_model_catalog.json` with identical
metadata on the bare and dated entries), and assert the bare id's row
precedes the dated id's row in DOM order; repeat for the TUI's `n` picker.

## Sharp edges

- Don't confuse "dated" with "has a date-shaped substring anywhere" — the
  regex is anchored to the *end* of the id (`-\d{8}(-v\d+)?$`), which is why
  Ollama's `:latest`/`:tag` suffix defeats it and why OpenRouter's
  `-02-15`-style partial dates (5 digits, not 8) are correctly treated as
  non-dated by the same rule that correctly flags `-20260420` as dated.
- The `openrouter` launch-check visibility filter
  (`launchCheckModelVisible`, `cmd/serf/internal/launchcheck/launchcheck.go:280-297`)
  is unrelated to and upstream of anything in this track — it's why
  `GET /api/models` for an `openrouter` instance in this session only ever
  showed 6-7 curated deepseek/inception/minimax models instead of
  openrouter's full real 340-model live catalog. Worth knowing if a future
  card wants to use openrouter for anything beyond a quick reachability
  check.
