# LLM Provider Architecture

How serf turns a `provider/model` string into an API call, and the one
invariant that holds the whole system together. This documents the **current**
implementation (as of 2026-05) so you can navigate it quickly; companion doc:
[`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
(credentials, OAuth, and how the hub spawns sessions).

## The one mental model to keep

A provider is identified by a single string, and **that string is the same
thing in four places at once**:

```
"openai"  ==  profile.ID()  ==  adapter.Name()  ==  the provider "type"
```

- It's the **routing key**: a request carries `req.Provider`, and the client
  looks up `client.providers[req.Provider]` to pick the adapter.
- It's the **profile id**: `req.Provider` is set from `profile.ID()`.
- It's the **adapter name**: adapters register under `adapter.Name()`.
- It's the **behavior selector**: code branches on the string (`== "openai"`,
  `switch provider`) to choose provider-specific behavior.

Everything works today because all four coincide. **This invariant is
load-bearing in a dozen-plus places** (see [§ Provider identity is
load-bearing](#provider-identity-is-load-bearing)); any change that lets the
name differ from the type (e.g. user-named provider instances) must re-key all
of them. If you're here to add a provider or touch routing, read that section.

## Request lifecycle

```
model string "openai/gpt-5"
  │  cmdutil.ParseModelRef        cmdutil/cmdutil.go:94   → {Provider:"openai", Model:"gpt-5"}
  ▼
cmdutil.SelectProfile(provider, model)   cmdutil/cmdutil.go:40
  │  hardcoded switch on provider → a ProviderProfile (rejects unknown names)
  ▼
ProviderProfile  (agent/profile.go)      profile.ID() == "openai"
  │  carries: context window, tool surface, quirks, ProviderOptions key, CheapModel
  ▼
agent.Session     sets req.Provider = s.profile.ID()    agent/session.go:1460,2662
  ▼
llm.Client.Complete / Stream             llm/client.go:90
  │  prov = normalizeProviderName(req.Provider)   (gemini→google)  client.go:236
  │  adapter = c.providers[prov]                  (else "unknown provider")
  ▼
ProviderAdapter.Complete/Stream          llm/providers/<x>/adapter.go
  ▼
vendor HTTP API
```

Models are addressed `provider/model`. `ParseModelRef` splits on the **first**
`/`, so the model half may contain slashes (`openrouter/anthropic/claude-…`,
`ollama/llama3:8b`) — see meta-providers below.

## Provider profiles (`agent/profile.go`)

A **profile** is the provider-shaped half of a session: it knows the context
window, the tool surface (e.g. OpenAI's Codex `apply_patch`/`exec_command`
remaps), reasoning/effort handling, the embedded model-catalog lookups, and the
`ProviderOptions` map key the adapter will read.

Constructors (each hardcodes its `id` except the compat one):

| Constructor | id | `profile.go` |
|---|---|---|
| `NewOpenAIProfile` | `openai` | :579 |
| `NewAnthropicProfile` | `anthropic` | :671 |
| `NewGeminiProfile` | `gemini` | :698 |
| `NewMiniMaxProfile` | `minimax` | :731 |
| `NewOpenRouterAnthropicProfile` | `openrouter-anthropic` | :893 |
| `NewOpenAICompatProfile(id, …)` | caller-supplied (`kimi`/`glm`/`openrouter`/`ollama`) | :1014 |

`NewOpenAICompatProfile` is the only one parameterized by id — `SelectProfile`
passes `kimi`/`glm`/`openrouter`/`ollama` into it (`cmdutil.go:68-70`).

Profile methods that switch on `p.id` (i.e. assume id == type):
- `CheapModel()` (`:344`) — the cheap model for naming/compaction/side-channel calls.
- `WithModel(model)` (`:506`, anthropic variant `:629`) — re-targets the session
  to a new model; on a `provider/` prefix it dispatches via the same hardcoded
  vendor switch, building a fresh profile.
- `decidePrefixAction(id, prefix)` (`:396`) — the **meta-provider** logic:
  whether a `prefix/rest` model keeps, strips, or switches. This is why
  `openrouter/anthropic/claude-…` keeps `anthropic/claude-…` as the upstream
  model namespace rather than treating it as a provider switch.
- `rebuildOnSameProviderChange(id)` (`:498`), `suppressBareCatalogLookup(id)`
  (`:929`), and the `id == "openrouter"` MiniMax-reasoning gate (`:1007`).

`ProviderOptions` is a `map[string]any` keyed by the provider name; each profile
writes its options under its own key (`"openai"` `:590`, `"anthropic"` `:616`,
`"gemini"` `:710`, `"openai-compatible"` `:1008`) and the matching adapter reads
the same key.

## Adapters and the registry (`llm/`)

The `ProviderAdapter` interface is small (`llm/client.go:9`):

```go
type ProviderAdapter interface {
    Name() string
    Complete(ctx, Request) (Response, error)
    Stream(ctx, Request) (Stream, error)
}
```

Adapters self-register from env: each package's `init()` calls
`RegisterEnvAdapterFactory` (`llm/env_registry.go:31`); `llm.NewFromEnv`
(`:45`) runs every factory and registers the ones whose env vars are present.
`Client.Register` keys the adapter by `adapter.Name()` (`client.go:39`); the
**default provider** is the first registered adapter that is not
`NonDefaultEligible` (`client.go:41`) — ollama opts out so a stray
`OLLAMA_HOST` never becomes the silent default. Registration order is package
**import order**, so the env-driven default is effectively alphabetical (with
both `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` set, the default is *anthropic*).

`normalizeProviderName` (`client.go:236`) rewrites `gemini`→`google` before the
map lookup — the Gemini profile's id is `gemini` but the adapter registers as
`google`. This is the one place name≠type already, papered over by a rewrite.

### Wire protocols (they differ — this matters)

| Type | Adapter pkg | Endpoint | Notes |
|---|---|---|---|
| `openai` | `openai` | `/v1/responses` (Responses API), or `/backend-api/codex/responses` at `chatgpt.com` for OAuth | `adapter.go:32-34`. OAuth → Codex backend; API key → standard API |
| `openai-compatible` | `openaicompat` | `/chat/completions` | for vLLM/LiteLLM/Ollama-style servers; **not** the Responses API |
| `anthropic` | `anthropic` | `/v1/messages` | base URL overridable (`ANTHROPIC_BASE_URL`) |
| `google`/`gemini` | `google` | Gemini API | its own protocol |
| `kimi`, `glm`, `openrouter` | thin wrappers → `openaicompat` | `/chat/completions` | own base URL + `QuirksPreset(...)` |
| `minimax`, `openrouter-anthropic` | thin wrappers → `anthropic` | `/v1/messages` | own base URL |
| `ollama` | `ollama` → `openaicompat` | `/v1/chat/completions` | rewrites `resp.Provider` to `ollama` |

Because `openai` (Responses) and `openai-compatible` (Chat Completions) are
**different protocols**, you cannot reach a vLLM box by pointing the `openai`
adapter at it; that's what the openaicompat adapter is for.

**`resp.Provider` is hardcoded per adapter**, not copied from `req.Provider`
(e.g. `anthropic/adapter.go:1096`, `openaicompat/adapter.go:1208`). The thin
wrappers (kimi/glm/openrouter/minimax/openrouter-anthropic) do **not** rewrite
it, so their responses report the inner adapter's name (`openai-compatible` or
`anthropic`) — only `ollama` rewrites it (`ollama/adapter.go:51,69,114`). The
web model-list deliberately ignores `m.Provider` for this reason
(`cmd/serf-hub/web.go:2067`).

## Provider identity is load-bearing

The single provider string is assumed to equal the type in many places. This is
the map to consult before changing how providers are identified (e.g. adding
named instances where name ≠ type). Each site silently misbehaves if `req.Provider`
/ `profile.ID()` is not the canonical type string.

**Routing (hard failures):**
- `req.Provider = s.profile.ID()` at every LLM call site — main loop
  `session.go:2662`, vision `:1460`, fallbacks `:2709`, plus side-channel calls:
  `session_namer.go:56`, `fork_summarize.go:21`, `context_manager.go:947`,
  `tool_web_search.go:20`, `tool_web_fetch.go:124`, `strategy_*`, `eval_probes.go`,
  `live_model_metadata.go:23` (`ListModels`). All resolve through
  `client.providers[...]`.
- `cmdutil.SelectProfile` (`cmdutil.go:57`) — hardcoded switch; unknown provider
  is a hard error. The hub's launch gate runs this in a **subprocess** (see the
  companion doc), so an unrecognized provider fails before the daemon starts.

**Behavior (silent regressions):**
- 24h prompt cache: `session.go:1382` `req.Provider == "openai"`.
- Gemini native web_search registration: `session.go:4785` `id == "gemini"`.
- Finish-reason normalization: `types.go:260` `switch provider` (anthropic/google
  vs default) — called by adapters with their literal type string.
- Cross-provider fallback guard: `session.go:4140`
  `fbProfile.ID() != s.profile.ID()` — treats equal ids as "same provider".
- `normalizeProviderName` gemini→google (`client.go:236`).

**Profile model-handling (model switching / fallbacks):** `WithModel`,
`decidePrefixAction`, `CheapModel`, `rebuildOnSameProviderChange`, and the
`id == "openrouter"` reasoning gate — all `switch p.id` (see above).

**Prompt sections:** `SectionResolver.provider` (`session.go:3972`,
`section_resolver.go`) builds prompt-section filenames like
`tools.provider-openai_append.md`; a non-canonical id finds no file and silently
drops provider-specific prompt text.

**Persistence / resume:** session meta + snapshot persist `ProfileID`
(`agent/snapshot.go`, `agent/transcript.go`); the hub maps it back via
`resumeProviderFromProfileID` (`cmd/serf-hub/app_rpc.go:1735`), a hardcoded
whitelist that returns `""` (drops the provider) for anything unknown.

## Adding or changing a provider

- **A new vendor with an existing wire protocol** (OpenAI Chat Completions or
  Anthropic Messages): usually a thin wrapper package (see `kimi`/`minimax`) —
  a `Name()`, a base URL, a `QuirksPreset`, registered via
  `RegisterEnvAdapterFactory`, plus a `SelectProfile` case and the credential
  maps (companion doc). Watch the `resp.Provider`-not-rewritten quirk.
- **A new wire protocol:** a full adapter package implementing
  `ProviderAdapter` + a profile constructor.
- **Touching identity/routing:** re-key every site in [Provider identity is
  load-bearing](#provider-identity-is-load-bearing). PRI-1880's spec + reviews
  explore what a name≠type ("instance") model would require.

## See also

- [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md) —
  credentials store, the full provider env-var reference, OpenAI OAuth, and the
  hub → `launch-check` → `serf serve` process model.
- [`ollama.md`](ollama.md) — running serf against a local Ollama server.
- Historical point-in-time audits live under `docs/audit-logs/` (Feb 2026;
  they predate the `internal/llm` → `llm/` move — paths there are stale).
