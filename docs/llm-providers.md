# LLM Provider Architecture

How serf turns a `provider/model` string into an API call, and the two
identities that hold the system together. This documents the **current**
implementation (as of 2026-05, after the PRI-1880 *Phase 1a* behavior-tag
separation, *Phase 1b* config-driven instances, and *Phase 1c*
all-config-driven / hub materialization). Companion doc:
[`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
(credentials, OAuth, and how the hub spawns sessions).

## The mental model to keep

A provider has **two distinct identities**, and the whole design turns on not
confusing them:

```
instance NAME  =  profile.ID()  =  req.Provider  =  adapter registration name   → ROUTING + IDENTITY
behavior TAG   =  profile.BehaviorTag()  =  providerconfig.BehaviorTag(type,style) → ALL provider-conditional BEHAVIOR
```

- The **name** routes and identifies: a request carries `req.Provider`, the
  client looks up `client.providers[name]` to pick the adapter, and responses /
  error labels report the name (which instance answered).
- The **tag** drives behavior: every place that used to branch on the provider
  string (`== "openai"`, `switch provider`) now keys on `BehaviorTag()`. The tag
  is derived from a provider *type* + an `apiStyle` by one pure function,
  `providerconfig.BehaviorTag` (`internal/providerconfig/providerconfig.go:34`).

**They always coincide for the default env-seeded instances** — a config seeded
from the environment names each instance after its type, so `name == type == tag`.
A custom instance defined in `providers.toml` (where `name != type` — e.g. an
`openai`-type instance named `work`) behaves like its underlying provider while
routing and identifying under its own name. **If you change how providers are
identified, keep this split: route on the name, branch on the tag.**

> Pre-1a this was a single string equal "in four places at once." Phase 1a split
> *behavior* (the tag) from *name* (routing/identity) and moved provider switching
> out of the profile and up to the session (below); Phase 1b then made the name
> config-driven (`providers.toml`); Phase 1c collapsed the env/config duality so
> the config path is always active. The old audits and the v1–v6 spec revisions
> under `docs/superpowers/specs/` describe the journey.

## Request lifecycle

```
model string "openai/gpt-5"
  │  cmdutil.ParseModelRef          cmdutil/cmdutil.go   → {Provider:"openai", Model:"gpt-5"}
  ▼
cmdutil.SelectProfile(provider, model)            (wraps agent profile construction)
  │  builds a ProviderProfile; rejects unknown provider names
  ▼
ProviderProfile  (agent/profile.go)   profile.ID()=="openai"  profile.BehaviorTag()=="openai"
  │  carries: instance name (id), behavior tag, context window, tool surface,
  │           quirks, ProviderOptions key, CheapModel
  ▼
agent.Session   sets req.Provider = s.profile.ID()   (the NAME)
  │  provider-conditional behavior branches on s.profile.BehaviorTag()  (the TAG)
  ▼
llm.Client.Complete / Stream            llm/client.go
  │  prov = normalizeProviderName(req.Provider)   (now just lower/trim)
  │  adapter = c.providers[prov]                  (else "unknown provider")
  │  resp.Provider + error labels stamped back to req.Provider (the NAME), centrally
  ▼
ProviderAdapter.Complete/Stream         llm/providers/<x>/adapter.go
  ▼
vendor HTTP API
```

Models are addressed `provider/model`. `ParseModelRef` splits on the **first**
`/`, so the model half may contain slashes (`openrouter/anthropic/claude-…`,
`ollama/llama3:8b`) — see meta-providers below.

## The behavior tag (`internal/providerconfig`)

`internal/providerconfig` is a **leaf package** (imports nothing from `llm`/
`agent`/`cmdutil`, so all of them can import it without a cycle). It owns the
shared vocabulary:

- `BehaviorTag(typ, style string) string` (`:34`) — the one definition of "what
  behavior does this provider have." It returns the type for every type **except
  `openai`**, which splits by `apiStyle`: `openai`+`chat-completions` →
  `openai-compatible`; everything else (incl. `openai`+`responses`) → the type.
- `NameToTag(cfg) map[string]string` (`:43`) — instance name → tag, populated from
  a loaded `providers.toml` and pushed into the client via `SetNameToTag` (below).
- `Config`/`InstanceConfig` (`:27`/`:18`) + `Load`/`LoadFile` (`load.go:35,113`) —
  the `providers.toml` parser/validator; `DefaultStateRoot()` (`:53`) resolves
  `~/.serf`. See "Config-driven instances" below.

The set of behavior tags is exactly the old set of distinct provider behaviors:
`openai`, `openai-compatible`, `anthropic`, `google`, `openrouter`,
`openrouter-anthropic`, `kimi`, `kimi-anthropic`, `glm`, `minimax`, `ollama`.

## Config-driven instances (`providers.toml`)

`providers.toml` is the **always-on** model for instance configuration. Every
client build goes through the config path — either loading the file when it
exists, or seeding the config in memory from the environment when it does not.
The `providers.toml` at `<state-root>/providers.toml` holds **descriptors**,
which is what lets `name != type` exist. Serf itself never writes credentials
into it (`WriteFile` scrubs struct-held keys and restores only what the
on-disk file already carried, so hub rewrites preserve — and never invent —
inline keys); a hand-authored instance MAY
carry an `api_key` — a literal or, better, a `$ENV`/`${ENV}` reference resolved
at point of use (see the compat section below):

```toml
default = "work"          # optional; the default instance (else first by sorted name)

[instances.work]          # the table key IS the instance NAME — routing/identity
type      = "openai"      # the provider TYPE — drives the behavior tag
api_style = "responses"   # openai only: "responses"/"auto" → openai tag; "chat-completions" → openai-compatible
base_url  = "..."         # optional override (captured from env when seeded)
quirks    = "..."         # optional; selects a quirks preset (openai-compatible types) —
                           # see "OpenAI-compatible compat & per-model config" below for the
                           # composable `[instances.X.compat]` / `[instances.X.models]` alternative
# api_key = "$WORK_KEY"  # optional, hand-authored only: literal or $ENV reference;
                          # omitted → credentials store / env resolution as below.
                          # Serf never writes this field back when rewriting the file.
```

- **Load or seed** — `cmdutil.LoadClient` (`cmdutil/load_client.go:31`):
  - File present → `providerconfig.LoadFile` parses + validates it. A corrupt file
    fails loudly. The path is `SERF_PROVIDERS_CONFIG`, else
    `DefaultStateRoot()/providers.toml`.
  - File absent → `cmdutil.seedConfigFromEnv` (`cmdutil/materialize.go:18`) builds
    a descriptors-only `Config` from a `NewFromEnv` detection pass; **nothing is
    written to disk**.
  - `LoadClient` always returns `hasConfig == true`; `BuildResolveProfile`
    (`load_client.go:77`) always uses `ResolveProfileFromConfig`.
- **Hub materialization** — `cmdutil.MaterializeProvidersConfig`
  (`cmdutil/materialize.go:54`) seeds and **atomically writes** `providers.toml`
  (temp-file + rename, mode `0644`). The hub (`cmd/serf-hub/main.go:120–131`)
  calls this once on startup when the file is absent, then passes the path to
  spawned children via `SERF_PROVIDERS_CONFIG` so they load the same file instead
  of re-seeding. A failed write is a hard error.
- **Descriptors-only seeding** — `providerconfig.Seed` builds env-derived
  instances without credentials, and `WriteFile`'s scrub/restore means the
  hub-materialized file stays credential-free unless the user hand-authors an
  `api_key`. The openai seed captures `OPENAI_BASE_URL` like every other type.
- **Credential injection** — after loading or seeding the descriptor config,
  `LoadClient` calls `credentials.Store.ResolveKey(name, typ)` (`store.go:184`)
  for each instance and sets the resolved key on the **in-memory** config only;
  the file stays descriptors-only. Resolution order: file entry keyed by instance
  name → env vars for the instance name (e.g. `OPENAI_COMPATIBLE_API_KEY`) → env
  vars for the type (e.g. `OPENAI_API_KEY`). OpenAI OAuth is resolved later by
  `openai.NewForInstance` from `auth/<name>.json` and is unaffected.
- **Build** — `llm.NewFromProviders(cfg, …)` (`providers_config.go:45`) constructs
  one adapter per instance through a **parallel registry**:
  `RegisterInstanceAdapterFactory(type, apiStyle, factory)` (`:28`) mirrors the env
  registry so `llm` builds per-instance adapters without importing
  `llm/providers/*` (no cycle). Each adapter package exposes a
  `NewForInstance(params)` (the openai adapter takes name / api-key / base-url /
  org / project / chatgpt-base / state-home; the thin wrappers default base URL +
  `QuirksPreset` by type). The openai instance factory restores `OPENAI_ORG_ID` /
  `OPENAI_PROJECT_ID` / `OPENAI_CHATGPT_BASE_URL` from env
  (`openai/adapter.go:167–177`) so the default instance behaves identically to the
  old env path.
- **Tag wiring** — `NewFromProviders` calls
  `c.SetNameToTag(providerconfig.NameToTag(cfg))` (`:89`); the client then maps each
  instance name to its tag.
- **Profile** — `agent.ResolveProfileFromConfig(cfg, "instance/model")`
  (`agent/resolve.go:26`) finds the instance, builds the profile from its
  `(type, apiStyle)` seed (so the tag is right), then `WithProviderID(raw, name)`
  stamps the instance name **without** changing the tag — the "build-then-rename"
  pattern.

## OpenAI-compatible compat & per-model config (`[instances.X.compat]` / `[instances.X.models]`)

Beyond the four descriptor fields above, an instance whose type routes through
the openai-compat adapter — `kimi`, `glm`, `openrouter`, `ollama`, or `openai`
with `api_style = "chat-completions"` (`providercfg.CompatFamily`,
`llm/providercfg/load.go`) — may also carry a `compat` table and a `models`
table. Any other type rejects them at load (`load.go:211-217`).

```toml
[instances.lunaroute]
type      = "openai"
api_style = "chat-completions"
base_url  = "https://gw.lunaroute.com/v1"
api_key   = "$LUNAROUTE_API_KEY"

[instances.lunaroute.compat]                    # instance-wide overrides
thinking_format = "..."

[instances.lunaroute.models."glm-5.2-nvfp4"]    # per-model catalog overlay + wire behavior
context_window    = 1048576
max_output_tokens = 131072
reasoning         = true
thinking_levels   = { minimal = "high", low = "high", medium = "high", high = "high", xhigh = "max" }

[instances.lunaroute.models."glm-5.2-nvfp4".compat]   # per-model compat, overlays the instance's
thinking_format           = "zai"
supports_reasoning_effort = true
tool_stream               = true
```

### Overlay precedence

Three layers combine field-by-field — later wins, `nil`/unset always inherits
(`ApplyCompatConfig`, `llm/providers/openaicompat/compat.go:60-146`):

1. **Quirks preset** — the base `ProviderQuirks` selected by `quirks = "..."`
   or the provider type's built-in default (`QuirksPreset`,
   `llm/providers/openaicompat/quirks.go:66`).
2. **Instance compat** (`[instances.X.compat]`) overlays the preset.
3. **Per-model compat** (`[instances.X.models."<id>".compat]`) overlays the
   instance's already-overlaid quirks.

`resolveModelCompat` (`compat.go:141`) builds one `ModelCompat` per declared
model at adapter-construction time; `compatFor(model)` (`compat.go:164`)
returns that entry when the model was declared, else the instance-wide
quirks. `FinishReasonMap` is the one field that **replaces wholesale** rather
than merging key-by-key when set at a given layer (`compat.go:126-131`).

### `[instances.X.compat]` / `[instances.X.models."<id>".compat]` fields

Every field is optional; unset means "inherit from the layer below"
(`providercfg.CompatConfig`, `llm/providercfg/providercfg.go:44-108`):

| TOML key | Type | Meaning |
|---|---|---|
| `thinking_format` | string | wire dialect for reasoning; see the table below |
| `supports_strict_mode` | bool | when **explicitly true**, add `strict: false` inside every tool's `function` object; `nil`/`false` sends no `strict` field (serf's default — see the Pi divergence note below) |
| `chat_template_kwargs` | table (scalars) | emitted verbatim as the request's `chat_template_kwargs` when `thinking_format = "chat-template"` and an effort is set; replaces wholesale on overlay (like `finish_reason_map`) |
| `supports_reasoning_effort` | bool | gate the `reasoning_effort` field for formats that treat it as optional (openai/deepseek/together); default follows the format (see below) |
| `max_tokens_field` | string | `"max_tokens"` (default) or `"max_completion_tokens"` |
| `tool_stream` | bool | send z.ai's `tool_stream: true` when tools are present |
| `supports_store` | bool | send `store: false` to opt out of server-side retention |
| `supports_developer_role` | bool | send the system prompt under the `"developer"` role |
| `supports_usage_in_streaming` | bool | `nil`/`true` sends `stream_options: {include_usage: true}`; `false` omits it |
| `requires_tool_result_name` | bool | add a `name` field to `tool`-role messages |
| `requires_assistant_after_tool_result` | bool | insert an empty assistant message between a tool result and a following user message |
| `requires_thinking_as_text` | bool | replay assistant thinking as plain text instead of a reasoning field |
| `requires_reasoning_content_on_assistant` | bool | add `reasoning_content: ""` to a replayed assistant message that carries none |
| `cache_control_format` | string | only `"anthropic"` is accepted — applies Anthropic `cache_control` markers (system prompt, last tool, last message) for gateways that forward them |
| `supports_long_cache_retention` | bool | when true, emit `prompt_cache_key` + `prompt_cache_retention: "24h"` (and, with `cache_control_format = "anthropic"`, add `ttl: "1h"` to the ephemeral markers) — see "Prompt caching through gateways" below |
| `send_session_affinity_headers` | bool | when true and the request carries a session id, send the `session_id` / `x-client-request-id` / `x-session-affinity` request headers so a gateway can pin a conversation's turns to one backend/cache |
| `lock_temperature` / `lock_top_p` / `lock_frequency_penalty` / `lock_presence_penalty` | bool | drop that sampling param from the request |
| `tool_choice_auto_only` | bool | force any `tool_choice` other than `auto`/`none` down to `auto` |
| `max_stop_sequences` | int (≥0) | truncate the `stop` array to this length |
| `strip_empty_content` | bool | drop empty-text content parts before sending |
| `no_json_schema` | bool | downgrade `response_format: json_schema` to `json_object` |
| `finish_reason_map` | table of string→string | remap raw finish reasons (replaces wholesale, not merged) |
| `translate_max_to_xhigh` | bool | map serf's `max` wire value to `xhigh` when no `thinking_levels` map applies (OpenRouter vocabulary) |

`thinking_format`, `max_tokens_field`, and `cache_control_format` are
validated at load (`load.go:101-116`): an unrecognized value is a hard config
error, not a silent no-op.

### Prompt caching through gateways

serf sets `req.SessionID` on every request and derives a stable prompt cache
key from it. On the native OpenAI Responses path the session emits
`prompt_cache_key` + `prompt_cache_retention` itself; the openai-compat path
stays silent unless you opt in, because a gateway that doesn't know these fields
should never see them. Three independent knobs turn caching on for a gateway
that does:

- **`supports_long_cache_retention`** — emits `prompt_cache_key` +
  `prompt_cache_retention: "24h"` on the request. The key is `req.PromptCacheKey`
  when set, else `"serf-session-" + req.SessionID` — the exact convention the
  session uses on the openai path, so a request routed either way caches on the
  same key. When there is no key material (no explicit key and no session id),
  neither field is emitted.
- **`send_session_affinity_headers`** — sends `session_id`,
  `x-client-request-id`, and `x-session-affinity` (all set to the session id) as
  request headers so a load-balancing gateway can pin a conversation's turns to
  one backend, keeping its cache warm. A user-configured `[instances.X.headers]`
  entry of the same name overrides the derived value. Emitted only when the
  request carries a session id.
- **`cache_control_format = "anthropic"`** with `supports_long_cache_retention`
  — the ephemeral `cache_control` markers gain `ttl: "1h"`. Without long
  retention the marker stays `{"type": "ephemeral"}`.

Enable these for gateways that forward OpenAI 24h prompt caching, Anthropic
`cache_control`, or that route by session (Portkey, Helicone, LiteLLM, and
similar). Leave them off for a bare model server that would reject the fields.

### `[instances.X.models."<id>"]` fields

(`providercfg.ModelConfig`, `providercfg.go:91-101`)

| TOML key | Meaning |
|---|---|
| `context_window` | overlays the catalog's context window for this model (must be ≥0) |
| `max_output_tokens` | sent as the output cap when a request doesn't set one (`ModelCompat.DefaultMaxTokens`, `compat.go:20`) |
| `reasoning` | `true`/`false` — declares whether the model accepts a reasoning-effort control at all; `false` clears the profile's effort levels entirely (`agent/provider/profile.go:1130-1135`) |
| `thinking_levels` | map of serf effort level → wire string (see below) |
| `compat` | per-model `CompatConfig`, overlays the instance's |

### `thinking_levels` semantics

- **The map, when present, is complete authority.** The model's supported
  effort ladder is exactly its keys, in serf rank order — that feeds
  `ReasoningEffortLevels()`, the session clamp, the spawn-form effort chip, and
  the `task_list` tool's effort enum (`agent/provider/profile.go:1136-1137`,
  `orderedEffortLevels` at `:1183`). A level absent from the map is
  unsupported and gets clamped away by `llm.ClampReasoningEffort`
  (`llm/types.go:587`) before it ever reaches the adapter.
- **Keys** are serf's canonical levels — `minimal`, `low`, `medium`, `high`,
  `xhigh` — normalized lowercase at load; `max` is accepted as an input alias
  and folded into `xhigh` (serf's rank table treats them as one tier).
  `"off"` is rejected at load — serf's `none` effort clears the setting to the
  provider default rather than forcing an explicit disable, so there's no slot
  for an explicit "off" wire value (`llm/providercfg/load.go:149-152`).
- **Values** are the literal wire strings sent to the provider — they need not
  match serf's vocabulary (the lunaroute example below maps every level to
  `"high"` except the top tier).
- **No map declared** → the model's effort passes through by name (today's
  backward-compatible default) and the `translate_max_to_xhigh` quirk (if set)
  still applies (`wireEffort`, `llm/providers/openaicompat/compat.go:28-46`).
- **Empty values** are rejected at load — a key must map to a non-empty wire
  string (`load.go:157-160`).

### `thinking_format`: exact wire JSON per dialect

`applyThinkingFormat` (`llm/providers/openaicompat/request.go:141-179`) runs
after the session's clamp and the `thinking_levels` translation. **When no
reasoning effort is set on the request, nothing is emitted for any format** —
serf's `none` clears the setting to the provider default rather than forcing
an explicit disable (`request.go:147-149`). The table below is what's sent
once an effort **is** set (`wire` = the post-clamp, post-`thinking_levels`
value):

| `thinking_format` | Wire body added |
|---|---|
| `""` / `"openai"` (default) | `reasoning_effort: <wire>`, gated on `supports_reasoning_effort` (default **true**) |
| `"zai"` | always `thinking: {"type":"enabled","clear_thinking":false}`; plus `reasoning_effort: <wire>`, gated on `supports_reasoning_effort` (default **false**) |
| `"deepseek"` | always `thinking: {"type":"enabled"}`; plus `reasoning_effort: <wire>`, gated on `supports_reasoning_effort` (default **true**) |
| `"openrouter"` | `reasoning: {"effort": <wire>}` — unconditional; `supports_reasoning_effort` has no effect on this format |
| `"together"` | always `reasoning: {"enabled": true}`; plus `reasoning_effort: <wire>`, gated on `supports_reasoning_effort` (default **true**) |
| `"qwen"` | `enable_thinking: true` — unconditional; the effort value itself is never sent on the wire |
| `"qwen-chat-template"` | `chat_template_kwargs: {"enable_thinking": true, "preserve_thinking": true}` — the effort value itself is never sent on the wire |
| `"chat-template"` | `chat_template_kwargs: <compat.chat_template_kwargs verbatim>`; omitted entirely when the resolved kwargs are empty |
| `"string-thinking"` | `thinking: <wire>` (the effort string is the field's value, not an object) — unconditional |

`clear_thinking: false` on the `zai` format keeps z.ai from pruning prior-turn
reasoning server-side; serf manages thinking replay itself.

**Divergences from Pi** (`applyThinkingFormat`,
`llm/providers/openaicompat/request.go:152`): Pi's `convertTools` always sends
`strict: false` and its `qwen-chat-template` always sends `enable_thinking`
(`false` when reasoning is off). serf deliberately does neither by default —
`supports_strict_mode` is opt-in (flipping the wire shape of every existing serf
request is not worth the risk), and serf's `none`-clears convention means the
`qwen-chat-template`/`chat-template` bodies are emitted **only** when an effort
is set (nothing otherwise). serf also skips Pi's per-value `$var` indirection in
`chat_template_kwargs` (YAGNI) — the table is sent verbatim.

The built-in `glm` type's `QuirksPreset("glm-5")`
(`llm/providers/openaicompat/quirks.go:77-89`) now sets `ThinkingFormat: "zai"`
by default, so a plain `type = "glm"` instance speaks z.ai's thinking dialect
with **no config needed**. The preset does *not* turn on `tool_stream` by
default — set `compat.tool_stream = true` explicitly if the gateway supports
incremental tool-call argument streaming.

### Stock catalog defaults for z.ai GLM & DeepSeek v4 (zero config)

Current z.ai GLM and DeepSeek v4 models ship their effort ladders, context
windows, and output caps in the Serf catalog overrides
(`llm/data/serf_model_catalog_overrides.json`), so a plain `type = "glm"` (or a
DeepSeek openai-compat instance) gets the right shape with **no
`[instances.X.models]` entry at all**. Two things flow from the catalog when a
model has no instance entry:

- `newOpenAICompatProfile` reads `context_window` and (for effort-parameter
  models) `reasoning_effort_levels` from the catalog for the profile's context
  window and effort ladder.
- the adapter's `compatFor` → `fillFromCatalog` (`compat.go`) seeds
  `DefaultMaxTokens` from `max_output_tokens`, and turns the `reasoning_effort`
  gate **on** when `supports_effort_parameter = true`. This gate fill is
  **positive-only**: a `true` in the catalog flips the gate on, but a missing or
  `false` flag is left as "no opinion" (nil) — it never turns the gate *off*, so
  it can't regress openai-format providers whose gate defaults on.

The effort ladders are stored **wire-spelled** (e.g. `["high", "max"]`), which
under `ClampReasoningEffort` reproduces Pi's per-model `thinkingLevelMap`
exactly: below-range requests raise to the lowest supported level and the top
tier resolves to the model's own spelling (`xhigh`/`max` → `"max"`). So no
separate per-model thinking-map field is needed — the levels list suffices.

| model | context | max_output | effort param | catalog levels |
|-------|---------|------------|--------------|----------------|
| `glm-4.5-air` | 131072 | 98304 | no | default ladder |
| `glm-4.7` | 204800 | 131072 | no | default ladder |
| `glm-5-turbo` | 200000 | 131072 | no | default ladder |
| `glm-5.1` | 200000 | 131072 | no | default ladder |
| `glm-5.2` | 1000000 | 131072 | yes | `["high","max"]` |
| `glm-5v-turbo` | 200000 | 131072 | no | default ladder |
| `deepseek-v4-flash` | 1000000 | 384000 | yes | `["high","max"]` |
| `deepseek-v4-pro` | 1000000 | 384000 | yes | `["high","max"]` |

Models with "no" effort param keep the profile's default effort ladder; the
`zai` thinking format only toggles thinking on/off for them (no
`reasoning_effort` field on the wire). An explicit `[instances.X.models."<id>"]`
entry always wins over these catalog defaults.

### Reasoning replay: same field it arrived on

When replaying assistant thinking on a later turn, the adapter doesn't
hardcode `reasoning_content` — it replays to whichever field the reasoning
text actually arrived on: `reasoning_content`, `reasoning`, or
`reasoning_text` (`reasoningReplayField`, `request.go:339-354`). The field
name rides in the content part's `Signature`, set when the field was parsed
off a streamed delta or a non-streamed response
(`llm/providers/openaicompat/response.go:89,189`). A signature that isn't one
of those three field names (e.g. an Anthropic crypto blob riding a
cross-provider transcript) falls back to `reasoning_content`.

### `$ENV` / `${ENV}` / `$$` in `api_key`

`api_key` accepts environment-variable references, resolved by
`providercfg.ResolveAPIKey` (`llm/providercfg/apikey.go:16`):

- `$NAME` or `${NAME}` substitutes the named variable.
- `$$` escapes a literal `$`.
- A value with no `$` passes through unchanged.
- A referenced variable that is unset or empty is an **error** — never a
  silently-empty key that would surface later as an opaque 401.
- Resolution happens **at the point of use** — adapter construction
  (`llm/providers_config.go:96`) and live `/models` probes
  (`cmdutil/cmdutil.go:88`) — not at `Load`, so one instance's missing
  variable never blocks unrelated instances. The runtime load path
  (`cmdutil.LoadClient` → `llm.NewFromAvailableProviders`) skips a failing
  instance and keeps the rest of the client usable.
- On-disk `api_key` handling lives in `providercfg.WriteFile`
  (`llm/providercfg/mutate.go`): struct-held keys are scrubbed (a loaded
  config may carry in-memory credentials-store injections that must never
  reach the 0644 file) and each instance's key is restored from whatever the
  existing file already carried. So hub rewrites (edit/set-default/remove)
  PRESERVE a hand-authored `api_key` — literal or `$ENV` reference — and
  serf itself never introduces one the user didn't write.

### `[instances.X.headers]` — extra request headers (all types)

> **Header-only authentication** is honored end-to-end for the
> openai-compat family only (`providercfg.CompatFamily`): those adapters send
> no bearer without a key, and a configured `Authorization` header suppresses
> credential-store injection so nothing clobbers it. Other provider types
> (openai responses, the anthropic family, google) cannot authenticate
> header-only — they require an api_key/OAuth/store credential, and their
> headers are supplementary (store injection still applies).

Any instance — **not just the compat family** — may carry a `headers` table of
extra HTTP headers sent on every request to its endpoint. This is how an
instance sits behind a gateway (Portkey, Helicone, a Cloudflare worker) that
needs its own auth or routing headers.

```toml
[instances.work]
type    = "anthropic"
headers = { "X-Portkey-Provider" = "anthropic", "Authorization" = "$PORTKEY_KEY" }
```

- **`$ENV` resolution** — header values accept the same `$NAME` / `${NAME}` /
  `$$` expansion as `api_key`, resolved by `providercfg.ResolveHeaderValue`
  (`llm/providercfg/apikey.go:26`, sharing the core with `ResolveAPIKey`) at the
  same choke point — `newFromProviders` (`llm/providers_config.go:113`) — so a
  missing variable fails just that instance (its name **and** the header key are
  named in the error), leaving unrelated instances usable.
- **Precedence** — a user header is merged into the adapter's `DefaultHeaders`
  and **overrides a provider-set default of the same name** (e.g. kimi's
  coding-plan `User-Agent`), but that default **survives** when the user sets no
  header of that name (`llm.MergeHeaders`, `llm/headers.go:26`). Hard-wired
  protocol headers the adapter sets last — `Authorization`/`x-api-key`,
  `Content-Type`, `anthropic-version` — still win over any configured header.
- **Delivery** — wired through every adapter's `DefaultHeaders` seam:
  openai-compat family (`kimi`/`glm`/`openrouter`/`ollama`/`openai` +
  `chat-completions`) via `OpenAICompatInstanceParams.Headers`, and the
  non-compat adapters (`anthropic`, `openai` responses, `google`, `minimax`,
  `kimi-anthropic`, `openrouter-anthropic`) via their own `*InstanceParams.Headers`.
- **Marshal round-trips the raw value** — unlike `api_key`, `providercfg.Marshal`
  writes header values back **verbatim**, including any `$ENV` reference. That is
  exactly why `$ENV` form is the recommended way to hold a secret in a header:
  the reference, not the secret, lands on disk. Header **names** must be
  non-empty; values are otherwise unrestricted.

### Worked example: lunaroute GLM gateway

```toml
[instances.lunaroute]
type      = "openai"
api_style = "chat-completions"
base_url  = "https://gw.lunaroute.com/v1"
api_key   = "$LUNAROUTE_API_KEY"

[instances.lunaroute.models."glm-5.2-nvfp4"]
context_window    = 1048576
max_output_tokens = 131072
reasoning         = true
thinking_levels   = { minimal = "high", low = "high", medium = "high", high = "high", xhigh = "max" }

[instances.lunaroute.models."glm-5.2-nvfp4".compat]
thinking_format           = "zai"
supports_reasoning_effort = true
tool_stream               = true
```

This is `type = "openai"` with `api_style = "chat-completions"` — a plain
gateway, not serf's `glm` type — so it routes through the openai-compat
adapter (`providercfg.CompatFamily`) but starts from the empty
`ProviderQuirks{}`, not the `glm-5` preset; every wire behavior here comes
from the `compat` table. `glm-5.2-nvfp4` gets a 1M-token context window and a
128K output cap from the model table (instead of the compat-family default of
128K context / no output cap), advertises `minimal`–`xhigh` as its effort
ladder (instead of the default `low`/`medium`/`high`), maps every level to the
gateway's coarse `"high"` except the top tier (`"max"`), and speaks z.ai's
`thinking:{...}` + `reasoning_effort` dialect with tool-call argument
streaming enabled.

## Provider profiles (`agent/profile.go`)

A **profile** is the provider-shaped half of a session. It now carries **both**
an `id` (the instance name) and a `behaviorTag`. Constructors stamp the tag via
`providerconfig.BehaviorTag`:

| Constructor | id (default) | behaviorTag | `profile.go` |
|---|---|---|---|
| `NewOpenAIProfile` | `openai` | `openai` | tag at :596 |
| `NewAnthropicProfile` | `anthropic` | `anthropic` | :686 |
| `NewGeminiProfile` | **`google`** | `google` | id+tag at :707-708 |
| `NewMiniMaxProfile` | `minimax` | `minimax` | :742 |
| `newKimiAnthropicProfile` | `kimi-anthropic` | `kimi-anthropic` | (anthropic-style) |
| `NewOpenRouterAnthropicProfile` | `openrouter-anthropic` | `openrouter-anthropic` | :904 |
| `NewOpenAICompatProfile(id, …)` | caller-supplied (`kimi`/`glm`/`openrouter`/`ollama`) | = id | :1027 |

`BehaviorTag()` is on the `ProviderProfile` interface (`:302`).
`WithProviderID(profile, name)` (in `profile_overrides.go`) renames an instance —
it overrides the `id` while **preserving the tag** (this is how a `name != type`
instance is constructed; used in tests and by the config path's
`ResolveProfileFromConfig`).

**Gemini is now canonical `google`.** `NewGeminiProfile` sets `id == "google"`
(not `"gemini"`); the `gemini`→`google` rewrites in routing and catalog lookup
were removed. `cmdutil.SelectProfile` still accepts `gemini` *and* `google` as
input aliases (so `--model gemini/…` works), both yielding an id-`google`
profile; `gemini` survives only as an input alias and as the `ProviderOptions`
key the Google adapter reads (it reads both `"google"` and `"gemini"`), and as
the catalog **ingest** normalization (`model_catalog.go normalizeCatalogProvider`).

Profile methods that branch on provider behavior now switch on **`p.behaviorTag`**
(not `p.id`):
- `CheapModel()` (`:355`).
- `decidePrefixAction(behaviorTag, instanceName, prefix)` (`:411`) — the
  **meta-provider** logic: a self-prefix (`prefix == instanceName`) is stripped;
  meta-provider upstream namespaces (openrouter/openrouter-anthropic/minimax, by
  tag) are kept; a cross-provider prefix yields `prefixActionSwitch`, which the
  *session* (not the profile) acts on (below).
- `rebuildOnSameProviderChange(behaviorTag)` (`:529`), the catalog lookups
  (`resolveOpenAICompatCatalogModel`/`suppressBareCatalogLookup`, keyed on tag),
  and the `behaviorTag == "openrouter"` MiniMax-reasoning gate.
- `WithModel(model)` (`:506`, anthropic variant) re-targets the model **within
  the current instance only** — it strips/keeps prefixes and rebuilds for the
  catalog, but **no longer switches providers** (the switch arm was removed).

`ProviderOptions` is a `map[string]any` keyed by the behavior tag's contract
string; each profile writes its options under that key and the matching adapter
reads the same key. This is a type-level contract, unaffected by the instance
name.

## Switching providers happens at the session, not the profile

Pre-1a, `WithModel` rebuilt a fresh profile via a hardcoded vendor switch. Now
the **session** owns cross-provider/instance switching, via a resolver injected
at construction (cycle-free: `cmd/serf` builds the closure; `agent` just holds
the field):

- `SessionConfig.ResolveProfile func(ref) (ProviderProfile, error)`
  (`session.go:201`), wired in `cmd/serf/serve.go` + `run.go` to
  `cmdutil.SelectProfile`.
- `Session.resolveProfileForRef(ref)` (`session.go:1283`): if `decidePrefixAction`
  classifies the ref as a cross-provider switch **and** a resolver is set → call
  the resolver and swap `s.profile` (preserving communicate/allowed-decisions
  overrides via `preserveBaseOverrides`, and re-running provider-conditional tool
  registration via `reapplyProviderSpecificTools`, `:1307`); otherwise →
  `profile.WithModel(ref)` (within-instance).
- `SetModel`, subagent model overrides, and `model_fallbacks` all route through
  this. The cross-provider-fallback guard compares `BehaviorTag()` (a fallback to
  a *different* tag errors; same-tag is allowed).

## Adapters and the registry (`llm/`)

The `ProviderAdapter` interface is small (`llm/client.go`): `Name()`,
`Complete`, `Stream`. Adapters self-register from env — each package's `init()`
calls `RegisterEnvAdapterFactory` (`llm/env_registry.go`); `llm.NewFromEnv` runs
every factory and registers the ones whose env vars are present. `Client.Register`
keys the adapter by `adapter.Name()`; the **default provider** is the first
registered adapter that is not `NonDefaultEligible` (ollama opts out). Registration
order is package **import order**, so the env-driven default is effectively
alphabetical (with both `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` set, the default
is *anthropic*).

**Identity is stamped centrally** (Phase 1a). `llm.Client.Complete`/`Stream` set
`resp.Provider = req.Provider` and rewrite errors with the instance name +
behavior tag — `RewriteErrorProvider(err, prov)` + `StampErrorBehaviorTag(err,
tag)` (`client.go:116-117`), and a `providerStampStream` wrapper does the same for
streamed `StreamEventError`/`StreamEventFinish` events (covering both the session
consumer and `llm.StreamGenerate`). The empty-provider no-op is preserved
(`context.Canceled` stays unlabeled). So adapters' hardcoded `resp.Provider`/error
literals no longer leak — the response and errors report **the instance name**.
`Client.nameToTag` (`:20`, set via `SetNameToTag` `:31`) maps instance name → tag
for llm-layer logic. `NewFromProviders` populates it from `NameToTag(cfg)`
(`providers_config.go:89`); for env-seeded defaults where name == type, the tag
equals the name so the fallback in `behaviorTagFor` (`:263`) is also correct.

`normalizeProviderName` is now just lowercase + trim (the `gemini`→`google`
rewrite is gone — the Gemini profile's id is `google`).

### Wire protocols (they differ — this matters)

| Tag | Adapter pkg | Endpoint | Notes |
|---|---|---|---|
| `openai` | `openai` | `/v1/responses`, or `/backend-api/codex/responses` at `chatgpt.com` for OAuth | OAuth → Codex backend; API key → standard API |
| `openai-compatible` | `openaicompat` | env provider: `/responses` preferred, `/chat/completions` fallback; explicit `api_style="chat-completions"`: `/chat/completions` | vLLM/LiteLLM/Ollama-style |
| `anthropic` | `anthropic` | `/v1/messages` | base URL overridable |
| `google` | `google` | Gemini API | its own protocol |
| `kimi`, `glm`, `openrouter` | thin wrappers → `openaicompat` | `/chat/completions` | own base URL + `QuirksPreset(...)` (by type) |
| `minimax`, `openrouter-anthropic`, `kimi-anthropic` | thin wrappers → `anthropic` | `/v1/messages` | own base URL (`kimi-anthropic` = Kimi coding plan at `https://api.kimi.com/coding`) |
| `ollama` | `ollama` → `openaicompat` | `/v1/chat/completions` | NonDefaultEligible |

**Kimi coding plan — coding-agent User-Agent.** Kimi For Coding gates its
OpenAI-route endpoint behind a coding-agent User-Agent allowlist and 403s
anything else ("only available for Coding Agents such as Kimi CLI, Claude Code,
…"); the Anthropic route is currently ungated. Both the `kimi` and
`kimi-anthropic` adapters announce Claude Code's User-Agent
(`claude-cli/<version> (external, cli)` — the format the gate accepts) via the
adapter `DefaultHeaders`, sourced from the shared constant in
`llm/providers/internal/kimicoding`. So either Kimi route is accepted; the
default Go User-Agent is not.

`openai-compatible` remains the compatibility adapter because it owns provider
quirks and the `openai-compatible` option namespace. Env-seeded compatible
instances now try `/responses` first and fall back to `/chat/completions` on
endpoint/model mismatch; explicit `api_style="chat-completions"` remains forced
Chat Completions.

## Reasoning effort

One per-session knob ordered `minimal < low < medium < high < xhigh == max`
(`xhigh` and `max` are aliases for the top tier — OpenRouter/OpenAI advertise
`xhigh`, Anthropic and the serf catalog say `max`; `none` clears it). The
vocabulary and its helpers — `ClampReasoningEffort`, `NormalizeReasoningEffort`,
`ReasoningEffortRank`, `ReasoningBudget` — live in `llm/types.go`.

**Per-model clamping.** Each model advertises the levels it supports
(`reasoning_effort_levels` in the catalog → `Profile.ReasoningEffortLevels()`).
`buildModelRequest` (`agent/session_model_call.go`) clamps the requested effort to
that set before the call, so an over-range request (e.g. `xhigh` to a model
capped at `high`) is reduced rather than rejected (this is what fixed the Kimi
`xhigh` 400). The adapters clamp again as a backstop against the actual wire model
(`anthropic/response.go:clampEffort`; the openai-compat enum). Catalog lookups go
through `llm.(*ModelCatalog).LookupModelInfo`, which canonicalizes the `[1m]`
suffix, a provider namespace, and dated/family snapshots.

**Provider mapping.** openai-compatible (`kimi`, `glm`, OpenRouter) send the
`reasoning_effort` enum directly. Anthropic adaptive-thinking models (opus-4-6,
sonnet-4-6) send `output_config.effort`; legacy models (opus-4-5, kimi-for-coding)
map the effort to a `thinking.budget_tokens` via `llm.ReasoningBudget`. When
thinking is enabled the Anthropic builder downgrades a forced `tool_choice` to
`auto` and keeps `max_tokens` above the thinking budget (Anthropic rejects both
otherwise).

**Setting it.** Launch: `--reasoning-effort`, `SERF_REASONING_EFFORT`,
`reasoning_effort` in `launch.toml`, or the spawn-form effort chip (per-model
levels). Runtime: the `/effort` command (web/TUI) → the
`thread/reasoning-effort/set` appwire method → `Session.SetReasoningEffort`.

## What keys on what (the map to consult before touching identity)

**Routing + identity (the NAME — `req.Provider`/`profile.ID()`):**
- `req.Provider = s.profile.ID()` at every LLM call site (main loop, vision,
  fallbacks, and the side-channel calls in `session_namer.go`,
  `fork_summarize.go`, `context_manager.go`, `tool_web_*`, `strategy_*`,
  `eval_probes.go`, `ListModels`), all resolving through `client.providers[...]`.
- `cmdutil.SelectProfile` — rejects unknown names; the hub's launch gate runs it
  in a subprocess (companion doc).
- `resp.Provider` + error labels — stamped centrally to the instance name.
- Resume: session meta persists `ProfileID`; the hub now **passes it through**
  (`resumeRequestForConfig`, `cmd/serf-hub/app_rpc.go`) and errors on empty —
  the old hardcoded allowlist was removed (downstream `SelectProfile` validates).

**Behavior (the TAG — `profile.BehaviorTag()`):**
- 24h prompt cache: `session.go:1450` `BehaviorTag() == "openai"`.
- Gemini native web_search registration: `session.go:4881` `BehaviorTag() ==
  "google"` (and re-applied on switch by `reapplyProviderSpecificTools`).
- Cross-provider fallback guard: compares `BehaviorTag()`.
- Profile model-handling: `CheapModel`, `decidePrefixAction`,
  `rebuildOnSameProviderChange`, catalog lookups, the openrouter gate — all on
  the tag.
- Prompt sections: `SectionResolver.provider = s.profile.BehaviorTag()`
  (`session.go`) → filenames like `tools.provider-openai_append.md`.
- OpenAI Responses→chat endpoint-fallback signal: `classify.go:118` on the
  error's `BehaviorTag()` (falls back to `Provider()` when the tag is empty).
- Provider-failure diagnostics classify on the structured `llm.Error` (provider +
  tag), not a hardcoded name list (`internal/diagnostic`, `diagnostics.js`).

## Phase 1a, 1b & 1c done / Phase 2 next

**Phase 1a (done, behavior-preserving):** the name/tag split above; switching
moved to the session; central identity stamping; Gemini canonicalized to
`google`; `internal/providerconfig` leaf. Verified by a renamed-instance
integration backstop (`agent/provider_instance_integration_test.go`) + a
full-tree sweep.

**Phase 1b (done):** the config-driven instance machinery above — the
`providers.toml` loader, `NewFromProviders` + the instance-adapter registry,
`SetNameToTag` wired from the config, per-instance OAuth (companion doc), the
`openai` `apiStyle` recipe + custom base-URL instances + the `openai-compatible`
fold-in, config-aware launch-check/serve, and the picker/launch **behavior**
filters re-keyed off the tag (`launch_check.go:146`, `cmd/serf-hub/web.go:2035`,
`app_rpc.go launchProviderAllowsUnreportedModels:1543`). The design lives in
[`docs/superpowers/specs/2026-05-29-provider-type-instance-model-design.md`](superpowers/specs/2026-05-29-provider-type-instance-model-design.md).

**Phase 1c (done):** collapsed the env/config duality — `LoadClient` is
always-config-driven (load-or-seed-in-memory); the hub materializes
`providers.toml` on startup when absent; descriptors-only file (no `api_key`);
`credentials.Store.ResolveKey(name, typ)` injects credentials into the in-memory
config only; `llm.NewFromEnv` retained as the seed's detection input only. The
design lives in
[`docs/superpowers/specs/2026-05-29-provider-instances-phase-1c-all-config.md`](superpowers/specs/2026-05-29-provider-instances-phase-1c-all-config.md).

**Phase 2 (next, terminal — no Phase 3):** one provider/instance **CRUD UI**
replacing the duplicate Providers + Credentials screens — **in both the web hub
and `serf-tui`** — with the model picker routing/displaying by instance name.
Until then those settings screens stay type-based.

## Adding or changing a provider

- **A new vendor with an existing wire protocol:** a thin wrapper package (see
  `kimi`/`minimax`) — `Name()`, base URL, `QuirksPreset` (by type),
  `RegisterEnvAdapterFactory`, a `SelectProfile` case + a profile constructor that
  stamps the right `behaviorTag`, plus the credential maps (companion doc).
- **A new wire protocol:** a full adapter package + a profile constructor.
- **Touching identity/routing:** keep the split — route on the name, branch on
  the tag; add new behavior switches on `BehaviorTag()`, never on `profile.ID()`.

## See also

- [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md) —
  credentials store, provider env-var reference, OpenAI OAuth, and the hub →
  `launch-check` → `serf serve` process model.
- [`ollama.md`](ollama.md) — running serf against a local Ollama server.
- [`superpowers/specs/2026-05-29-provider-type-instance-model-design.md`](superpowers/specs/2026-05-29-provider-type-instance-model-design.md)
  — the type/instance design (Phase 1a, 1b & 1c implemented; Phase 2 pending).
- [`superpowers/specs/2026-05-29-provider-instances-phase-1c-all-config.md`](superpowers/specs/2026-05-29-provider-instances-phase-1c-all-config.md)
  — Phase 1c design: all-config-driven / hub materialization.
- Historical point-in-time audits live under `docs/audit-logs/` (Feb 2026; paths
  there predate the `internal/llm` → `llm/` move).
