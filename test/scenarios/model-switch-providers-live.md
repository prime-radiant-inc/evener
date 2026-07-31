# model-switch-providers-live: mid-session model switching works end-to-end across real providers

**What this covers**: spec `docs/superpowers/specs/2026-07-12-model-switching-design.md`
Acceptance criterion 8 (a live cross-provider ladder) and the "Live ladder"
test-plan bullet. Exercises `Session.SetModel` (`agent/session.go:783`), the
persisted `Switched model: <old> → <new>` marker
(`buildModelSwitchMarkerText`, `agent/session.go:900`), the effort-ladder
clamp re-derivation on switch (`ReasoningEffortLevels`/`SupportsReasoning`,
`appwire/types.go:348-349` — a hub/AppWire-layer snapshot, not visible on
this card's daemon HTTP surface; see Sharp edges), and — for the anthropic-family leg — the
thinking-absence-when-effort=none contract against a **real** wire body, with
`agent/session_replay_provenance_test.go` (Task 6's unit matrix) as the
deterministic backstop for the same rule.

This is an end-to-end test against **real** provider APIs. It needs live
credentials and makes billed calls. It talks to the `serf serve` daemon's
own HTTP surface directly (`POST /input`, `POST /model`, `GET /status`) —
no hub, no browser — so the switch path under test is exactly
`Session.SetModel` via `server/server_handlers.go:handleModel`.

## Pre-state

- One `mktemp` run directory naming everything this card creates — the binary,
  the isolated config, the state dir, the workdir — never a fixed `/tmp` name a
  second concurrent run would overwrite mid-run (kata `k2rx`):
  ```sh
  run=$(mktemp -d -t serf-e2e-msw-XXXXXX)
  go build -o "$run/serf" ./cmd/serf
  mkdir -p "$run/cfg" "$run/state" "$run/wd" "$run/rendezvous"
  ```
- An **isolated** provider config so the live `~/.serf/providers.toml` is
  untouched. Instance NAMES below are deployment-local — declare whatever
  names your deployment's credentials resolve to; refs in this card are
  `instanceName/model`. Write `$run/cfg/providers.toml`:

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

  and `$run/cfg/credentials.toml` (mode `0600` — serf's credential store
  rejects a looser mode):

  ```sh
  install -m 600 /dev/null "$run/cfg/credentials.toml"
  cat > "$run/cfg/credentials.toml" <<EOF
  schema = 0
  [providers]
    [providers.anthropic]
      api_key = "$ANTHROPIC_KEY"
    [providers.openai]
      api_key = "$OPENAI_KEY"
    [providers.kimi]
      api_key = "$KIMI_KEY"
  EOF
  chmod 600 "$run/cfg/credentials.toml"
  ```

- `export SERF_PROVIDERS_CONFIG="$run/cfg/providers.toml"`. The session's
  canonical private API log captures exact attempts whenever API logging is
  attached; there is no separate raw-body toggle or sidecar.

## Steps

1. **Spawn on the anthropic-family instance.** Start the daemon on a real
   anthropic (or anthropic-family) model. Bind `127.0.0.1:0` and read the
   address back from the daemon's own startup line — never a port a human
   picked (kata `68fm`) — and give it its own `--run-dir`, so its rendezvous
   entry lands under `$run` instead of `~/.serf/run`, where a real hub watches
   and would otherwise adopt this daemon into a live roster
   (`rendezvous/rendezvous.go`'s package doc; `DefaultDir()` is
   `$HOME/.serf/run`):

   ```sh
   "$run/serf" serve --addr 127.0.0.1:0 \
     --model anthropic/claude-opus-4-6 --reasoning-effort none \
     --state-dir "$run/state" --run-dir "$run/rendezvous" \
     --non-interactive --no-project-prompts \
     --dir "$run/wd" 2>"$run/serve.log" &
   DPID=$!
   echo "$DPID" >"$run/serve.pid"
   for i in $(seq 1 100); do
     ADDR=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/serve.log" 2>/dev/null | grep -oE '127\.0\.0\.1:[0-9]+') || true
     [ -n "$ADDR" ] && break
     kill -0 "$DPID" 2>/dev/null || { echo "daemon exited before listening:" >&2; cat "$run/serve.log" >&2; exit 1; }
     sleep 0.2
   done
   [ -n "$ADDR" ] || { echo "daemon never logged a listening address" >&2; exit 1; }
   DAEMON=http://$ADDR
   curl -s "$DAEMON/status"   # capture session_id (SID)
   ```

2. **Tool-using turn on leg 1.**
   `curl -s -X POST "$DAEMON/input" -d
   '{"text":"Use the shell tool to run `echo LEG1`, then reply with the
   single word DONE."}'`. Poll `GET /status` to `idle`. Read
   `sessions/<SID>.transcript.jsonl`'s last turn: assert
   `response_model == "claude-opus-4-6"` and `response_provider ==
   "anthropic"`.

3. **Switch → openai instance.**
   `curl -s -X POST "$DAEMON/model" -d
   '{"model":"openai/gpt-5.5"}'` (expect 204). Read the transcript: the
   newest turn is `schema.TurnModelSwitch` whose FIRST line is exactly
   `Switched model: anthropic/claude-opus-4-6 → openai/gpt-5.5` (the marker
   can carry further `Warning:` lines for context pressure or dropped
   fallbacks — `agent/session.go:900-908` — so match the first line, not the
   whole entry). `GET /status`: `model` is now `gpt-5.5`.
   Do **not** assert an effort ladder here: the daemon's `/status` payload
   (`StatusInfo`/`DetailedStatus`, `server/server.go:88-136`) has no
   `reasoningEffortLevels`/`supportsReasoning` field at all — those live on
   the hub/AppWire `thread/read` snapshot this card deliberately avoids.
   `model-switch-resume.md` covers the ladder at that layer.

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
   marker's first line `Switched model: kimi/kimi-for-coding → anthropic/claude-sonnet-4-5`.
   Again, no ladder assertion on this surface. Send one more tool-using turn, assert
   `response_model == "claude-sonnet-4-5"`.

## Expected

- Every switch (steps 3, 5, 6) persists exactly one `schema.TurnModelSwitch`
  turn whose first line is the literal marker text `Switched model: <old
  provider/model> → <new provider/model>` — falsification: that line
  differs, is missing, or a switch silently no-ops the model. Trailing
  `Warning:` lines are legitimate marker content, not a mismatch.
- Every subsequent turn's persisted `response_model`/`response_provider`
  matches the just-switched target, never the pre-switch model —
  falsification: a turn runs on the old model after a successful switch
  (the profile swap under `s.mu` in `SetModel` didn't take, or a
  concurrently-in-flight round used a stale profile snapshot).
- The effort-ladder re-derivation (`reasoningEffortLevels`/
  `supportsReasoning` following the new model immediately, rather than
  staying pinned until the next turn — the G2-class staleness regression,
  `server/appwire_runtime.go` / `UpdateSessionInfo`) is **out of scope for
  this card**: it is only observable on the hub/AppWire `thread/read`
  snapshot (`appwire/types.go:348-349`), and this card runs against the
  daemon's raw HTTP surface by design. Assert it in `model-switch-resume.md`
  instead of inventing a `/status` field that does not exist.
- Step 5's explicitly expanded request body for the anthropic-family kimi leg under
  effort=none has no `thinking` key — falsification: a `thinking` object is
  present despite effort=none (regression in the effort→thinking wiring);
  Task 6's unit matrix in `agent/session_replay_provenance_test.go` is the
  deterministic backstop for this same no-thinking-on-none contract and
  should be re-run alongside this card, not treated as a substitute for it.

## Cleanup

- `curl -s -X POST "$DAEMON/shutdown"`, or `kill "$DPID"` — the pid this card
  recorded, never a `pkill -f 'serf serve'` pattern, which would also kill a
  concurrent agent's daemon.
- `rm -rf "$run"` — the binary, config, state dir, workdir, rendezvous dir and
  logs all live under it. Remove it by name, never a `/tmp/serf-e2e-msw-*`
  glob, which would take out every other concurrent run of this card (kata
  `k2rx`). Also remove any one-shot session dirs left under
  `~/.local/state/serf/projects/*/sessions/`.

## Sharp edges

- `POST /model` returns `409 Conflict` (`"session is processing"` or
  `"turn <id> is active"`) if a turn is in flight — always poll `/status`
  to `idle` before switching (`server/server_handlers.go:handleModel`).
- The daemon's own HTTP surface (this card's path) is a strict subset of
  what the hub/appwire layer offers (`thread/model/set`,
  `web-model-switch-mid-session.md`) — it proves `Session.SetModel` and the
  marker contract directly, but reaches no ladder fields at all
  (`DetailedStatus` carries tools/mcp/skills/plugins/hooks/jobs/agents and
  nothing about reasoning — `server/server.go:88-96`), and does NOT exercise the hub's
  turn-active/queue-drain rejection semantics; those are covered by
  `web-model-switch-mid-session.md` and are out of scope here.
- Effort is a **launch-only** flag on this HTTP surface — there is no
  `/effort` route and `POST /model`'s body (`ModelRequest`) carries only
  `model`, so effort cannot change mid-session here. The whole ladder runs
  at step 1's `--reasoning-effort none`. The per-model
  `reasoningEffortLevels` **array** (a catalog-intrinsic property that
  changes by model regardless of the current effort value) is a distinct
  thing from the current effort clamping — but neither is readable here, so
  step 3 no longer checks it. Some
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
