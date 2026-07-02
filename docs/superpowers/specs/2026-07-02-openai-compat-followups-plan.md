# OpenAI-compat follow-ups — execution plan (continuation-safe)

Status: **COMPLETE 2026-07-02** — all waves landed; the roborev refine loop
ran to convergence on top (rounds documented below). Jesse approved: "we need
to fix 1, 2, 3, 4 and the smaller stuff" (the adopt-next list from the Pi
study) + "assign subagents to do the work, then you check them". This file is
the historical execution record; the base spec
`2026-07-02-openai-compat-providers.md` describes what shipped.

Branch: `bot/openaicompat-provider-design`, worktree
`.worktrees/openaicompat-providers`. Gate green throughout (build, full
test suite, -race on touched, `make lint`, jstests); e2e verified against a
fake gateway (wire assertions for every new surface) and LIVE lunaroute
glm-5.2-nvfp4 (xhigh + none; a later re-check hit the gateway's own
503 INFERENCE_UNAVAILABLE outage, confirmed via raw curl — not a serf
regression).

## Review-loop state (all closed)

- job 2113: 5 findings fixed + closed.
- job 2116: reasoning=false leak — fixed by Wave-1 package A + closed.
- job 2148: 6 findings (adapter-level enforcement, catalog fill scoping,
  hub override gaps, header canonicalization) — fixed + closed.
- job 2151: 4 findings (api_key survives WriteFile rewrites via
  scrub/restore, explicit-empty compat tables, mktemp portability,
  .PHONY) — fixed + closed.
- job 2153: 3 findings (ReasoningOff vs ProviderOptions passthrough,
  fallback clamp vs configured levels, this doc's staleness) — fixed;
  loop continues until PASS or until findings stop being real (Jesse's
  stop rule).

## Orchestration protocol (Jesse's direction)

Subagents implement; the orchestrator reviews diffs, runs the gate, and
commits. Rules learned the hard way this session:
- All agents work in THIS worktree on **disjoint file sets**; every agent
  prompt forbids git state mutation (one agent ran a bare `git reset` anyway —
  harmless then, but re-check `git status`/reflog after each wave).
- Commit each reviewed package separately with trailer
  `Claude-Session: https://claude.ai/code/session_01PENuq4zaVHF6BQ3kVR1kSw`.
- Gate per wave: `go build ./... && go test ./... ` + `-race` + golangci on
  touched pkgs + `gofmt -l`; full `make lint` + jstests before the final
  review loop.

## Wave 1 — DONE (3 agents; committed 5f492706, 1774b787, 41d31650)

- **A. reasoning=false guards** (fixes job 2116): guard `req.ReasoningEffort`
  attachment on `profile.SupportsReasoning()` at (1) buildModelRequest main
  path `agent/session_model_call.go:~636`, (2) fallback loop `:~703`
  (`fbProfile.SupportsReasoning()`), (3) vision side-channel
  `agent/session_tools.go:~186`; and make WithLiveModelInfo treat explicit
  `Reasoning=false` as authoritative (no live re-enable, no live levels).
  Tests in `agent/session_effort_clamp_test.go` +
  `agent/provider/instance_models_test.go`.
- **B. openaicompat parsing bundle**: (i) encrypted `reasoning_details`
  round-trip (OpenRouter `{"type":"reasoning.encrypted","id","data"}`) — parse
  in stream+non-stream, carry via `llm.ThinkingData.EncryptedContent`
  (preferred; adding llm fields was out of the agent's file set), replay the
  `reasoning_details` array on assistant messages; mirror Pi
  (`inspo/pi/.../openai-completions.ts` stream loop ~300-450 +
  convertMessages ~960-1010, `pendingReasoningDetailsByToolCallId`); (ii)
  choice-level usage fallback (Moonshot puts usage on `choices[0].usage`;
  top-level wins). Files: `llm/providers/openaicompat/` only.
- **C. hub effort-chip enrichment**: instance-defined models
  (`InstanceConfig.Models`) must win over the embedded catalog in the hub's
  models REST endpoint (`reasoning_effort_levels` from ThinkingLevels keys in
  `llm.ReasoningEffortRank` order; reasoning=false → empty). Files:
  `cmd/serf-hub/`, `server/`. Must keep jstests green.

## Wave 2 — DONE (af363192 headers+compat, bd3dc7bd catalog defaults; plus refresh-model-catalog automation f81c550c/a2f57838)

- **D. providers.toml `[instances.X.headers]`** (+ per-model?
  instance-level is enough — YAGNI per-model until needed): map of header →
  value with the same `$ENV`/`${ENV}`/`$$` resolution as api_key
  (`providercfg.ResolveAPIKey` — generalize or mirror it), resolved at
  adapter construction in `newFromProviders`/factories; empty resolved value
  = omit header. Wire into openaicompat `DefaultHeaders` and the forwarder
  factories (glm/kimi/openrouter/ollama pass-through). Validation +
  Marshal round-trip (headers are NOT secrets by default but may hold $ENV —
  Marshal SHOULD emit them; document). Unlocks Portkey/Helicone/CF-gateway.
- **E. compat additions**: `supports_strict_mode` (emit `strict:false` on
  tool defs unless disabled — check Pi convertTools:1105 for exact rule) and
  thinking formats `qwen-chat-template`
  (`chat_template_kwargs:{enable_thinking,preserve_thinking:true}`) +
  optional raw `chat_template_kwargs` passthrough config (decide: a
  `chat_template_kwargs` TOML table on CompatConfig emitted verbatim when
  thinking on; skip Pi's `$var` indirection — YAGNI). providercfg validation
  + quirks/compat overlay + request.go emission + docs.
- **F. catalog-shipped thinking maps**: extend `llm.ModelInfo` with
  `ThinkingLevels map[string]string` (+ maybe `ThinkingFormat`), populate
  `llm/data/serf_model_catalog_overrides.json` for known z.ai GLM + DeepSeek
  models (mirror Pi's zai.models.ts / deepseek catalogs), teach
  `newOpenAICompatProfile` (levels precedence: instance > catalog > default)
  and the openaicompat adapter (`compatFor` fallback to catalog map when no
  instance entry) to consult them. This gives stock `type="glm"` users the
  xhigh→max map with zero config.

## Wave 3 — DONE (cc06ddb3; scouting found req.SessionID/PromptCacheKey already plumbed, so no session-side changes were needed)

- **G.** Investigate serf's EXISTING `llm.Request.PromptCacheKey` /
  `PromptCacheRetention` fields (seen in `agent/session_model_call.go`
  fallback path — something already sets/clears them; map who populates them
  and for which providers). Then: emit `prompt_cache_key` +
  `prompt_cache_retention:"24h"` on openai-compat when compat allows
  (`supports_long_cache_retention`), session-affinity headers
  (`session_id`/`x-session-affinity`) behind
  `send_session_affinity_headers` compat flag (needs session id plumbed into
  the adapter request path — find what PromptCacheKey already carries),
  anthropic `cache_control` ttl "1h" tie-in. Mirror Pi buildParams:551-568 +
  createClient:521-525. Config surface + docs + tests.

## Wave 4 — DONE (gate/e2e/docs complete; refine loop ran to convergence — see review-loop state above for every job)

1. Full gate: `go build ./... && go test ./...` (+ `-race` touched), full
   `make lint`, jstests, e2e re-run: fake-gateway wire assertions script
   (scratchpad `e2e/fakegw.py` — recreate if gone: POST logger + SSE
   responder; providers.toml with lunaroute-shaped instance) + live lunaroute
   smoke (`--reasoning-effort xhigh` and `none`; key: tomllib-parse
   `~/.serf/credentials.toml` providers.lunaroute.api_key into
   LUNAROUTE_API_KEY, never echo).
2. roborev refine loop — ran to convergence; every job in the review-loop
   state above is closed.
3. Update the base spec §5/§8 + docs/llm-providers.md for D/E/F/G surfaces.
4. Update memory
   (`~/.claude/projects/-home-jesse-git-prime-radiant-serf/memory/`) and
   report to Jesse. Jesse merges/pushes; do NOT push.

## Deliberately out of scope (Jesse has not asked)

Azure/Bedrock/Vertex drivers; `!command` key resolution; baseURL compat
auto-detection; Pi's typed openRouterRouting/vercelGatewayRouting structs
(ProviderOptions passthrough covers).
