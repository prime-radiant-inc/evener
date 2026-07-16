# model-switch-providers-live: mid-session model switching works end-to-end across real providers

**What this covers**: spec `docs/superpowers/specs/2026-07-12-model-switching-design.md`
Acceptance criterion 8 (a live cross-provider ladder) and the "Live ladder"
test-plan bullet. Exercises `Session.SetModel` (`agent/session.go:674`), the
persisted `Switched model: <old> → <new>` marker
(`buildModelSwitchMarkerText`, `agent/session.go:774`), the effort-ladder
clamp re-derivation on switch (`ReasoningEffortLevels`/`SupportsReasoning`,
`appwire/types.go:256-257`), and — for the anthropic-family leg — the
thinking-absence-when-effort=none contract against a **real** wire body, with
`agent/session_replay_provenance_test.go` (Task 6's unit matrix) as the
deterministic backstop for the same rule.

This is an end-to-end test against **real** provider APIs. It needs live
credentials and makes billed calls. It talks to the `serf serve` daemon's
own HTTP surface directly (`POST /input`, `POST /model`, `GET /status`) —
no hub, no browser — so the switch path under test is exactly
`Session.SetModel` via `server/server_handlers.go:handleModel`.

## Pre-state

- A `serf` binary built from this branch:
  `go build -o /tmp/serf-msw ./cmd/serf`.
- An **isolated** provider config so the live `~/.serf/providers.toml` is
  untouched. Instance NAMES below are deployment-local — declare whatever
  names your deployment's credentials resolve to; refs in this card are
  `instanceName/model`. Write `/tmp/msw-cfg/providers.toml`:

  ```toml
  default = "anthropic"

  [instances.anthropic]
  type = "anthropic"

  [instances.openai]
  type = "openai"
  api_style = "responses"

  [instances.kimi]
  type = "kimi-anthropic"
  ```

  and `/tmp/msw-cfg/credentials.toml` (mode `0600` — serf's credential store
  rejects a looser mode):

  ```sh
  install -m 600 /dev/null /tmp/msw-cfg/credentials.toml
  cat > /tmp/msw-cfg/credentials.toml <<EOF
  schema = 0
  [providers]
    [providers.anthropic]
      api_key = "$ANTHROPIC_KEY"
    [providers.openai]
      api_key = "$OPENAI_KEY"
    [providers.kimi]
      api_key = "$KIMI_KEY"
  EOF
  chmod 600 /tmp/msw-cfg/credentials.toml
  ```

- `export SERF_PROVIDERS_CONFIG=/tmp/msw-cfg/providers.toml`. The session's
  canonical private API log captures exact attempts whenever API logging is
  attached; there is no separate raw-body toggle or sidecar.

## Steps

1. **Spawn on the anthropic-family instance.** Start the daemon on a real
   anthropic (or anthropic-family) model, e.g.:

   ```sh
   /tmp/serf-msw serve --addr 127.0.0.1:9331 \
     --model anthropic/claude-opus-4-6 --reasoning-effort none \
     --state-dir /tmp/msw-state --non-interactive --no-project-prompts \
     --dir /tmp &
   curl -s http://127.0.0.1:9331/status   # capture session_id (SID)
   ```

2. **Tool-using turn on leg 1.**
   `curl -s -X POST http://127.0.0.1:9331/input -d
   '{"text":"Use the shell tool to run `echo LEG1`, then reply with the
   single word DONE."}'`. Poll `GET /status` to `idle`. Read
   `sessions/<SID>.transcript.jsonl`'s last turn: assert
   `response_model == "claude-opus-4-6"` and `response_provider ==
   "anthropic"`.

3. **Switch → openai instance.**
   `curl -s -X POST http://127.0.0.1:9331/model -d
   '{"model":"openai/gpt-5.5"}'` (expect 204). Read the transcript: the
   newest turn is `schema.TurnModelSwitch` with text exactly
   `Switched model: anthropic/claude-opus-4-6 → openai/gpt-5.5`. `GET
   /status`: `model` is now `gpt-5.5`, `detailed.reasoningEffortLevels` /
   `detailed.supportsReasoning` (or the daemon's reasoning-info fields)
   match gpt-5.5's catalog entry, not opus-4-6's — this is the effort-ladder
   re-derivation the marker step must trigger.

4. **Tool-using turn on leg 2.** Same `/input` shape as step 2 (`echo LEG2`).
   Poll to idle. Assert `response_model == "gpt-5.5"`,
   `response_provider == "openai"`.

5. **Switch → kimi coding instance (anthropic-family, cross-tag hop).**
   `POST /model {"model":"kimi/kimi-for-coding"}`. Assert the marker
   `Switched model: openai/gpt-5.5 → kimi/kimi-for-coding`. Send a
   tool-using turn (`echo LEG3`), poll to idle, assert `response_model ==
   "kimi-for-coding"`. **Thinking-absence assertion**: effort is fixed at
   `none` for the whole run by step 1's `--reasoning-effort none` launch
   flag — this HTTP surface (`POST /input`, `/model`, `/status`) has **no**
   mid-session effort RPC, so there is nothing to toggle per leg and no
   effort call to make here. In the next idle turn, call
   `read_session_transcript` on the current session with `source=api_log`,
   identify this leg's `api_attempt`, then make a separate call with that
   `attempt_id` and `body=request`. Assert the exact expanded request carries
   **no** `thinking` key. The summary alone exposes byte counts, not body data;
   credentials remain excluded.

6. **Same-provider anthropic-family model hop.** `POST /model
   {"model":"anthropic/claude-sonnet-4-5"}` (or any second catalogued model
   on the SAME anthropic-family instance used in step 1 — the point is a
   same-instance, cross-model hop, not a cross-provider one). Assert the
   marker `Switched model: kimi/kimi-for-coding → anthropic/claude-sonnet-4-5`
   and `detailed.reasoningEffortLevels` matches sonnet-4-5's ladder (not
   opus-4-6's, not kimi's). Send one more tool-using turn, assert
   `response_model == "claude-sonnet-4-5"`.

## Expected

- Every switch (steps 3, 5, 6) persists exactly one `schema.TurnModelSwitch`
  turn with the literal marker text `Switched model: <old
  provider/model> → <new provider/model>` — falsification: marker text
  differs, is missing, or a switch silently no-ops the model.
- Every subsequent turn's persisted `response_model`/`response_provider`
  matches the just-switched target, never the pre-switch model —
  falsification: a turn runs on the old model after a successful switch
  (the profile swap under `s.mu` in `SetModel` didn't take, or a
  concurrently-in-flight round used a stale profile snapshot).
- `reasoningEffortLevels`/`supportsReasoning` (or equivalent daemon status
  fields) change to the NEW model's catalog entry immediately after the
  switch RPC returns — falsification: the ladder stays pinned to the old
  model until the next turn re-derives it (a G2-class staleness regression;
  see `server/appwire_runtime.go` / `UpdateSessionInfo`).
- Step 5's explicitly expanded request body for the anthropic-family kimi leg under
  effort=none has no `thinking` key — falsification: a `thinking` object is
  present despite effort=none (regression in the effort→thinking wiring);
  Task 6's unit matrix in `agent/session_replay_provenance_test.go` is the
  deterministic backstop for this same no-thinking-on-none contract and
  should be re-run alongside this card, not treated as a substitute for it.

## Cleanup

- `curl -s -X POST http://127.0.0.1:9331/shutdown` (or kill the daemon PID).
- `rm -rf /tmp/msw-cfg /tmp/msw-state /tmp/serf-msw` and any one-shot
  session dirs left under `~/.local/state/serf/projects/*/sessions/`.

## Sharp edges

- `POST /model` returns `409 Conflict` (`"session is processing"` or
  `"turn <id> is active"`) if a turn is in flight — always poll `/status`
  to `idle` before switching (`server/server_handlers.go:handleModel`).
- The daemon's own HTTP surface (this card's path) is a strict subset of
  what the hub/appwire layer offers (`thread/model/set`,
  `web-model-switch-mid-session.md`) — it proves `Session.SetModel` and the
  marker/ladder contract directly, but does NOT exercise the hub's
  turn-active/queue-drain rejection semantics; those are covered by
  `web-model-switch-mid-session.md` and are out of scope here.
- Effort is a **launch-only** flag on this HTTP surface — there is no
  `/effort` route and `POST /model`'s body (`ModelRequest`) carries only
  `model`, so effort cannot change mid-session here. The whole ladder runs
  at step 1's `--reasoning-effort none`; the effort-ladder re-derivation
  checked in step 3 is about the per-model `reasoningEffortLevels` **array**
  (a catalog-intrinsic property that changes by model regardless of the
  current effort value), not about the current effort clamping. Some
  anthropic-family coding-plan backends have been observed defaulting
  extended thinking on server-side regardless of the request; see Results
  below.

## Results (this run — 2026-07-13, worktree `model-switching` @ 3668f093)

**Environment reality on this machine** (checked, not assumed): the live
`~/.serf/providers.toml` has NO native `anthropic` instance and NO
`ANTHROPIC_API_KEY`/OpenAI-native credential anywhere (`~/.serf/credentials.toml`
holds only `kimi` and `lunaroute` keys; `~/.serf/providers.toml` declares
`kimi` (type `kimi`, OpenAI-style, UA-gated), `lunaroute` (type `openai`,
openai-compat gateway), `ollama` (local, one model: `gemma4:latest`), and
`openai` (type `openai`, Responses — no key resolves for it). No
`~/.serf/credentials.toml` entry for `openai` or `anthropic`.

Given that, the isolated config actually used for this run substituted:
`kimi` → `type = "kimi-anthropic"` (the sanctioned anthropic-compatible
route, real key, tagged anthropic-family — matches this card's "leg 1 /
leg 3 anthropic-family" role), `lunaroute` → `type = "openai"`,
`api_style = "chat-completions"`, `base_url = "https://gw.lunaroute.com/v1"`
(real key, openai-family, standing in for the "openai instance" leg), and
`ollama` (local, no key, single model `gemma4:latest`, no same-instance hop
partner).

**Rungs run, with outcome:**

- **Step 1-2 (kimi-anthropic, tool-using turn), tried at both default effort
  and `--reasoning-effort none`**: FAILED, not a serf regression. Every
  attempt (via `serf serve` and via a one-shot CLI invocation identical in
  shape to `reasoning-effort-providers.md`'s pattern) returned `kimi error
  (status=400): tool_choice 'required' is incompatible with thinking
  enabled` — but the then-current optional raw sidecar captured a request body
  carrying `"tool_choice":{"type":"any"}` and **no** `thinking` key at all.
  Kimi's coding-plan backend is
  defaulting extended thinking on server-side even when the client sends no
  `thinking` object and effort is `none`; `llm/providers/anthropic/request.go`'s
  downgrade guard (lines 174-183) only fires when serf itself sets
  `body["thinking"]`, so it cannot see or correct a server-side default.
  Confirmed this is unrelated to this branch: `git diff --stat
  665e82a0..HEAD -- llm/providers/anthropic llm/providers/kimi_anthropic`
  is empty — neither adapter changed on this branch. SKIPPED as a live rung
  here; the marker/response_model/ladder assertions for this leg are
  untestable against Kimi's current backend behavior on this host. Not
  filed as a serf bug in this report (adapter code unowned by this task);
  flagging for Jesse to decide whether it merits its own ticket.
- **Step 3-4 (lunaroute as the openai-family leg)**: FAILED — the gateway
  itself returned `HTTP 500 {"error":{"code":"INTERNAL_ERROR","message":"An
  internal error occurred"}}` on a bare raw `curl` to
  `https://gw.lunaroute.com/v1/chat/completions` (no serf involved), and a
  `serf --model lunaroute/glm-5.2-nvfp4` one-shot call hung with no output
  for 2+ minutes before being killed. This is a live third-party outage/bug
  in the gateway, not a serf issue. SKIPPED.
- **Ollama leg (`ollama/gemma4:latest`, local, no credential)**: RAN, but
  is unusable as a ladder rung — `gemma4:latest` cannot reliably call the
  `communicate` result tool; the one-shot run exhausted all 3 bare-text
  retries and exited with `model returned bare text without calling
  communicate after 3 retries`. This is a small-model capability gap, not a
  serf bug, and there is only one model on this instance (no same-instance
  hop partner either). SKIPPED as a ladder rung; confirms `response_model`
  plumbing reaches the daemon status/transcript path (`ollama/gemma4:latest`
  showed correctly in `/status` before the retry exhaustion), but that's
  the only fact this leg could establish here.
- **Steps 5-6 (kimi coding instance, anthropic→anthropic hop)**: SKIPPED —
  no anthropic-family second model was reachable once the sole
  anthropic-family credential (kimi) was blocked at step 1.

**Net**: zero ladder rungs completed live in this environment. All three
credentialed/local instances failed for real, environment-specific reasons
unrelated to the model-switching feature under test (a live provider-side
thinking-default change on Kimi's coding-plan backend, a live 500 on the
lunaroute gateway, and a local-model tool-use capability gap on the only
ollama model present). The card is written to be runnable wherever real
`anthropic`/`openai`/`kimi-anthropic` credentials resolve — it was not
possible to execute it end-to-end on this host today. The deterministic
coverage for the switch mechanics under test (marker persistence, profile
swap atomicity, effort-ladder re-derivation, replay provenance /
thinking-on-none) is carried instead by: `agent/session_model_switch_marker_test.go`,
`server/model_set_test.go`, `server/appwire_runtime_test.go`,
`internal/appprojector/appwire_projection_test.go`,
`internal/apptranscript/apptranscript_test.go`, and Task 6's
`agent/session_replay_provenance_test.go` — all part of the default `go
test ./...` sweep (see the Task 12 report for pass/fail).
