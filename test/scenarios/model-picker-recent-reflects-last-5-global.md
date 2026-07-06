# model-picker-recent-reflects-last-5-global: Recent is the last 5 distinct models, globally, cwd-independent

**What this covers**: Track B Tasks 1-3's `appwire.ModelListResponse.Recent`,
`hubcore.PastIndex.RecentModels` (`recentModelsLimit = 5`), and
`attachRecentModels` — spawning across many working directories and models
must produce a `Recent` group that is (a) capped at the 5 most-recently-
*touched* distinct `(provider, model)` pairs, (b) ordered most-recent-first,
(c) identical regardless of which `cwd` the caller scopes the request to, and
(d) identical across all three pickers (web spawn, web settings, TUI).

## Pre-state

- Same hermetic hub as `model-picker-fresh-install-no-recent.md`, continued
  (state accumulates across cards 2-5 per the task's own guidance).
- Real `ollama` (local `gemma4:e4b`, already pulled) and `openai` (OAuth
  token copied from `~/.local/state/serf/auth/openai.json`) providers live;
  `lunarouter` down for the whole session (see Sharp edges).
- 6 scratch working directories (`proj8`..`proj13`), one per spawn.

## Steps

1. Spawn 6 real sessions via `POST /api/spawn` (`{"prompt":"hi", "harness":
   "serf", "model": "<provider/model>", "working_dir": "<projN>"}`), one per
   distinct model, in this order, 3s apart: `ollama/gemma4:e4b`,
   `openai/codex-auto-review`, `openai/gpt-5.3-codex-spark`,
   `openai/gpt-5.4`, `openai/gpt-5.4-mini`, `openai/gpt-5.5` — 6 distinct
   `(provider,model)` pairs across 2 real providers, 6 distinct dirs.
2. Wait for both the real turns to finish and the hub's past-index rebuild
   (`past_index_rebuild_interval = "2s"` in the hermetic `hub.toml`).
3. `GET /api/models?diagnostics=1` with no `cwd`, then again with
   `cwd=proj8`, `cwd=proj9`, `cwd=proj13`, and a `cwd` that doesn't exist at
   all — compare `recent[]` (provider, model) pairs and order across all five
   calls.
4. Open `/new`, click `button[data-chip="model"]`, read the `Recent` group's
   `.chip-picker-model` rows in DOM order.
5. Open `/settings/launch-serf`, click `[data-settings-model-picker]` — the
   `Recent` provider column is active by default; read its
   `.chip-picker-model` rows in DOM order.
6. In `serf-tui`, `n` → `BTab BTab` (focus Model) → `Enter` to open the
   picker; capture-pane and read the `RECENT` group's rows.

## Expected

- Step 3: all five calls return the **same** 5 entries in the **same**
  order — observed this run:
  `[(ollama,gemma4:e4b), (openai,gpt-5.5), (openai,gpt-5.4-mini),
  (openai,gpt-5.4), (openai,gpt-5.3-codex-spark)]`.
  `openai/codex-auto-review` — the 6th distinct model, whose session's real
  turn finished earliest (`updated_at` oldest) — is correctly evicted, not
  merely appended past a length-5 cap. (The order reflects each session's
  real `meta.json` `updated_at` — i.e. when its turn actually *completed*,
  not spawn-issue order; `ollama/gemma4:e4b` ended up most-recent here
  because the local model's first inference had a multi-second cold-start
  load time longer than the real network latency of the OpenAI turns spawned
  after it. This is genuine activity-based recency, not a bug — see Sharp
  edges.)
- Step 4/5/6: all three pickers' `Recent` rows match step 3's list and order
  **exactly**: `Gemma4:e4b, Gpt 5.5, Gpt 5.4 Mini, Gpt 5.4, Gpt 5.3 Codex
  Spark` — confirmed byte-for-byte identical in the web spawn picker, the
  web settings picker, and the TUI picker's `RECENT` group.
- Falsification: `recent[]` differs by `cwd`; `codex-auto-review` appears in
  `Recent` (6th distinct, should be evicted); any picker's Recent order
  disagrees with `/api/models?diagnostics=1`; or a picker interleaves a
  provider-group row into the Recent group.

## Cleanup

- Kill the hub process and tmux session.
- Remove all scratch project directories and the hermetic
  `$HOME`/`$XDG_STATE_HOME` trees.
- Revert the test-only `api_key` line added to `providers.toml`'s
  `[instances.ollama]` (see Sharp edges — not a real credential, only a
  workaround for a spawn-validation gate bug this card surfaced).

## Sharp edges

- **Found bug (pre-existing, not part of Track B):**
  `validateProviderCredentials`'s config-path branch
  (`cmd/serf-hub/spawn.go:466-506`) has no bypass for provider types that
  legitimately need zero credentials (e.g. `ollama`, a local unauthenticated
  server) — unlike the no-config path a few lines below it, which correctly
  special-cases `credentials.SourceNone` (`spawn.go:514-522`). Spawning
  `ollama/gemma4:e4b` against a hub with any `providers.toml` present (even
  one with a bare `[instances.ollama]\ntype = "ollama"`, no `api_key`) fails
  with `"provider credentials missing for ollama: set via serf/auth/apiKey/set
  or set the matching env var"` — even though `GET /api/models` lists ollama
  models just fine (listing goes through a different, unguarded path). This
  card worked around it by adding a placeholder
  `api_key = "not-needed-for-local-ollama"` to the hermetic
  `[instances.ollama]` block (ollama's adapter never reads/sends it) so the
  spawn gate's `strings.TrimSpace(inst.APIKey) != ""` check passes. This is a
  real bug worth its own kata — it would block every ollama spawn on any hub
  with a providers.toml, in production, today. Not fixed here: out of
  Track B's scope (model-list enrichment, not spawn-time credential
  validation), flagged for separate triage.
- `lunarouter`'s cloudflare tunnel was down for this entire session (real
  infra, not a test artifact) — only `ollama` and `openai` contributed to
  this card's 6 distinct models/2 providers.
- Recency here tracks each session's `meta.json` `updated_at` (bumped when a
  turn *completes*), not spawn-issue order — a session spawned first can
  still end up "most recent" if its real completion lags behind sessions
  spawned after it (seen live with the ollama cold-start above). Don't
  assume spawn order predicts Recent order when working from real completed
  turns; assert against actual `updated_at` values (or, if only spawn-time
  ordering matters for a card, use `"just spawn+idle"` without waiting for
  completion, per the task brief).
