# Provider Registry and Capability Resolution

**Date:** 2026-08-28
**Status:** Draft for review
**Replaces:** the LiteLLM-vendored model catalog, `providercfg.CompatConfig`,
`openaicompat.ProviderQuirks` presets, the vendor wrapper adapter packages, the
behavior-tag split, and `api_style`. No backward compatibility is kept.

## 1. Goals

1. **Any provider models.dev knows about works with an API key and nothing
   else.** Base URL, key variable, wire protocol, model list, context windows,
   output caps, pricing, effort ladders, and modalities all come from data.
2. **The request shape is a pure function of one materialized record.** Given
   `Resolved{Protocol, Transport, Model, Caps}`, an adapter produces the same
   body every time. No runtime guessing, no fallback-on-error, no hidden state.
3. **Every capability fact lives in exactly one place**, with provenance.
   `evener models inspect groq/openai/gpt-oss-120b` prints the resolved record
   and which layer set each field.
4. **Unknown OpenAI-compatible endpoints get a request body that any of them
   accepts.** Platform-specific extras are opt-in per provider, never default.
5. **The catalog stays current between releases** via a cached background
   refresh from models.dev, never touching the network under test.
6. **Azure OpenAI, Amazon Bedrock, and Google Vertex fit the same model** as a
   transport variation on an existing protocol, not as new adapters.

### Non-goals

- Automatic inference of per-field endpoint support from error messages
  (Groq's 400 says only "invalid JSON body"; the signal isn't there).
- Bedrock Converse as a protocol. Claude on Bedrock uses the Anthropic
  protocol over a Bedrock transport; OpenAI models on Bedrock use the
  OpenAI-compatible endpoints. Converse can be added later as a fifth
  protocol if non-Claude Bedrock models matter.
- Image generation, embeddings, audio. The registry carries the rows but no
  adapter consumes them.

## 2. What is wrong today

Verified against HEAD on 2026-08-28; file references are current.

- **Capability knowledge in five layers** that must agree by hand:
  `llm.ModelInfo` flags (`llm/model_catalog.go:17`), `openaicompat.ProviderQuirks`
  (25 fields, `llm/providers/openaicompat/quirks.go`), `providercfg.CompatConfig`
  (a near-1:1 shadow whose `supports_store` and `supports_usage_in_streaming`
  map with *inverted* polarity onto `SendStoreFalse`/`OmitStreamUsage`,
  `compat.go:100-107`), loose `Profile` fields (`agent/provider/profile.go:44`),
  and the hub's `enrichModelDescriptors` (`cmd/evener-hub/app_models.go:283`).
- **The provider type list is hardcoded in seven places**: `knownTypes`
  (`llm/providercfg/load.go:18`), `CompatFamily` (`:92`), the constructor
  switch in `agent/provider/resolve.go:53`, `envvars/providers.go:69`,
  `cmdutil/seed.go`, `isOpenAICompatTag` (`cmdutil/cmdutil.go:40`), and
  `rebuildOnSameProviderChange` (`profile.go:742`).
- **Five catalog lookup precedence rules**: `LookupModelInfo`,
  `resolveOpenAICompatCatalogModel` (`profile.go:1216`), `fillFromCatalog`
  (`compat.go:279`, exact-only), `GetPrice`'s longest-prefix scan over all
  3055 entries, and `ResolveLiveModelInfo`.
- **Responses-vs-Chat is decided three ways that disagree**: `api_style`
  (where `auto` registers the identical factory as `responses`,
  `openai/adapter.go:227-229`); the openai adapter's fallback on 404/422/empty
  stream gated by *substring matching on the provider's error text*
  (`openai/adapter.go:885-936`); and `openaicompat.Adaptive`, which only
  env-seeded instances get. A Groq 400 matches none of them and kills the
  request.
- **Groq, DeepSeek, xAI, Mistral, Cerebras, Together, Fireworks, Azure,
  Bedrock, and Vertex have no provider type.** The escape hatch,
  `type="openai", api_style="chat-completions", base_url=…`, resolves catalog
  keys under `openai-compatible/<model>` while LiteLLM keys them
  `groq/<model>`, so those models get no context window, cap, price, or
  effort ladder.
- **Model-name prefix matching in wire builders**: `gpt-5.6`
  (`responses.go:1007`), `gpt-5.4/5.5/gpt-6` (`:1042`), `gpt-5/gpt-6`
  (`:1060`), `gemini-3` (`google/request.go:251`), the Claude
  numeric-generation parse (`anthropic/request.go:261-272`),
  `claude-opus-4-`/`claude-sonnet-4-` (`anthropic/models.go:98`), and
  `minimax/` under openrouter (`profile.go:1287`).
- **Errors are classified by status code**, so Groq's 413 "tokens per minute
  … Limit 8000, Requested 24414" surfaces as an opaque failure rather than a
  rate limit with the provider's message.
- `cmd/llmcall` builds its client with `llm.NewFromEnv` and cannot see
  `providers.toml` at all.

## 3. Concepts

Four nouns. Only the first is code.

| Noun | What it is | Where it lives |
|---|---|---|
| **Protocol** | A wire format: how to encode a request body, decode a stream, list models. Exactly one Go package each. | `llm/providers/{openaichat,openairesponses,anthropic,google}` |
| **Transport** | How to reach an endpoint: auth scheme, URL template, stream framing, constant headers/body fields. Data, plus a small pluggable authenticator per scheme. | `registry.Transport` |
| **Provider** | A named endpoint definition: id, display name, transport, default protocol, caps, models. Data. | `registry.Provider` |
| **Model** | A row under a provider: id, limits, cost, modalities, reasoning facts, caps, optional protocol/transport override. Data. | `registry.Model` |

**Resolution** merges layered `Provider` records and produces one `Resolved`
record per `instance/model` reference. Adapters consume `Resolved` and
nothing else.

### 3.1 Why keyed by provider name, not endpoint

A URL says nothing once a gateway (Portkey, Helicone, a LiteLLM proxy, a
corporate reverse proxy) sits in front of the vendor, and Pi's
`baseUrl.includes("deepseek.com")` detection is its most fragile part. So
behavior attaches to a **named provider record**; the endpoint is a field. A
gateway instance says `base = "openai"` plus its own `base_url` and gets
OpenAI's behavior explicitly, trimmed with `fields`. An endpoint nobody has a
record for uses a protocol-only pseudo-provider (`openai-compatible`,
`anthropic-compatible`, `google-compatible`) with baseline caps.

## 4. Data model

All types live in a new leaf package `llm/registry`. Optional scalars are
pointers so that "unset" is distinguishable from `false`/`0` at every layer.

```go
type Provider struct {
    ID          string            // registry id; the instance name for user entries
    Base        string            // user entries only: registry id this entry layers on
    Name        string            // display name
    Doc         string            // upstream documentation URL
    Protocol    string            // default protocol for models without their own
    Transport   Transport
    APIKeyEnv   []string          // env vars consulted for the key, in order
    Headers     map[string]string // constant request headers ($ENV refs allowed); non-secret
    CredentialHeaders map[string]string // secret headers; scrubbed from logs and hub rewrites
    Caps        Caps              // provider-level capability overlay
    Models      map[string]Model  // keyed by model id
    DefaultModel string           // curated: what to pick when the user gives none
    CheapModel   string           // curated: the provider's cheap/fast model
    Hidden       bool             // registry rows evener cannot drive (unsupported protocol)
}

type Model struct {
    ID        string
    Name      string
    AliasOf   string     // this id is another row (Azure deployment → catalog model); the alias target's id goes on the wire unless WireID is set
    WireID    string     // id to send when it differs from both ID and AliasOf (deployment names)
    Protocol  string     // override of the provider default
    Transport *Transport // field-wise overlay on the provider transport (Bedrock Mantle models, Azure Claude models)
    Headers   map[string]string // model-level constant headers (Anthropic beta headers)
    Caps      Caps
    Status    string     // "", "beta", "deprecated"
}

type Transport struct {
    Auth       string            // bearer | header | none | aws-sigv4 | gcp-adc | oauth-openai-codex
    AuthHeader string            // auth=header: header name (x-api-key, api-key, x-goog-api-key)
    BaseURL    string            // may contain {var} placeholders
    Endpoint   string            // path template; protocol default when empty
    Framing    string            // sse (default) | aws-eventstream
    Vars       map[string]string // template variables; values or $ENV refs
    VarsEnv    map[string]string // var name → env var consulted when Vars lacks it
    Body       map[string]any    // constant JSON paths set after build (anthropic_version, text.verbosity)
}
```

### 4.1 Caps

One flat struct shared by every protocol. Fields a protocol does not use are
ignored by it. Two generic maps carry most of the variation; the explicit
fields are the transforms that cannot be expressed as "send this field or
don't".

```go
type Caps struct {
    // Model facts. Catalog-sourced; user layers may correct them.
    ContextWindow    *int
    MaxOutputTokens  *int
    Tools            *bool
    StructuredOutput *bool     // json_schema response format accepted
    Reasoning        *bool
    ReasoningControl *string   // effort | budget | toggle | none
    EffortValues     []string  // wire-spelled ladder, ascending; replace on overlay
    InputModalities  []string  // text, image, pdf, audio; replace on overlay
    KnowledgeCutoff  *string   // YYYY-MM or YYYY-MM-DD
    Cost             *Cost     // $/M tokens; replace on overlay

    // Optional wire fields. JSON paths → allowed. Key-wise merge.
    // Baseline per protocol (§8.2); unknown path = not sent.
    Fields map[string]bool

    // Structural request shaping.
    MaxTokensField   *string   // openai-chat: max_tokens | max_completion_tokens
    ThinkingFormat   *string   // §8.4
    ReasoningField   *string   // openai-chat: reasoning_content | reasoning | reasoning_text
    ReasoningSummary *string   // openai-responses: auto | detailed | none
    ChatTemplateKwargs map[string]any
    FinishReasonMap  map[string]string // replace on overlay
    CacheControl     *string   // anthropic-style cache_control markers on openai-chat gateways
    CacheTTL         *string   // anthropic: "" | 1h
    StrictTools      *bool     // openai-responses: strict:true + schema strictify
    ToolChoiceForcing *bool    // false → downgrade required/named tool_choice to auto
    MaxStopSequences *int
    ImageDetail      *string   // openai-responses: original | high | low
    ResponsesLite    *bool     // ChatGPT backend request shaping for gpt-5.6

    // Message transforms (openai-chat).
    AssistantAfterToolResult *bool
    ThinkingAsText           *bool
    EmptyReasoningContent    *bool
    StripEmptyContent        *bool
    ToolResultName           *bool
    ToolStream               *bool
    SessionAffinityHeaders   *bool

    // Protocol features.
    MultimodalToolResults *bool // google: images inside function responses
    WebSearch             *bool // provider-side web search tool available
}

type Cost struct {
    Input, Output, CacheRead, CacheWrite float64 // $ per million tokens
    Tiers []CostTier                             // context-size tiers
}
```

Merge rule, applied field by field in layer order (§5): a pointer or scalar
set at a later layer replaces; a `nil` inherits. Slices (`EffortValues`,
`InputModalities`) and `Cost`/`FinishReasonMap` replace wholesale.
`Fields`, `ChatTemplateKwargs`, `Transport.Vars`, `Transport.Body`, and
`Headers` merge key-wise. A model's `Transport` overlays the provider's the
same way, field by field. This is one reflect-driven function (~80 lines)
that also records `map[fieldPath]layerName` provenance.

### 4.2 Resolved

```go
type Resolved struct {
    Instance   string      // user-facing instance name (routing, logs, errors)
    ProviderID string      // registry id the instance is based on
    Protocol   string
    Transport  Transport   // vars substituted, $ENV expanded, endpoint filled
    ModelID    string      // id to send on the wire (after AliasOf and [1m] handling)
    Model      Model       // merged row (may be synthesized; see §7.3)
    Caps       Caps        // fully merged; every Fields baseline key present
    Headers    map[string]string
    Credential Credential  // resolved key/token source, never logged
    Provenance map[string]string
    Warnings   []string    // "model not in catalog", "protocol unverified"
}
```

`Resolved` is serializable (minus `Credential`) and is what `evener models
inspect` prints and what the API attempt log records in summary form
(protocol, base URL, pruned fields, provenance for anything the user
overrode).

## 5. Layers

Every layer is a set of `Provider` records in the same schema. Later wins.

| # | Layer | Source | Refreshable |
|---|---|---|---|
| 1 | **Upstream snapshot** | `llm/data/models.dev.json.gz`, the raw models.dev `api.json`, converted at load (§6.1) | `make refresh-model-catalog` |
| 2 | **Upstream cache** | `<state-root>/catalog/models.dev.json` + `.meta` (etag, fetched-at), same converter | background, 24h (§6.4) |
| 3 | **Curated overlay** | `llm/data/providers_overlay.toml`, hand-maintained | with the release |
| 4 | **User config** | `<state-root>/providers.toml` | by the user or hub |
| 5 | **Live listing** | the instance's model-list endpoint | per process, cached |

Layer 2 replaces layer 1 wholesale when its fetched-at timestamp is newer
than the embedded snapshot's (recorded by the refresh script in
`llm/data/models.dev.meta.json`); they are never merged. Layers 3 and 4 overlay field-wise. Layer 5 **only fills fields
that are still unset** after 1–4 and establishes existence of models the
catalog lacks; it never overrides catalog values.

Layer 4 entries are instances. An instance whose name matches a registry id
inherits that record; otherwise `base = "<id>"` names the record to inherit.
An instance with no base and no protocol is a load error.

## 6. Sources

### 6.1 models.dev converter

One function, `registry.FromModelsDev([]byte) ([]Provider, error)`, run on
both the embedded snapshot and the runtime cache. It is the only code that
knows models.dev's schema. Verified against the 2026-08-28 `api.json` (207
providers).

Provider-level mapping:

| models.dev | registry |
|---|---|
| `id`, `name`, `doc` | `ID`, `Name`, `Doc` |
| `api` | `Transport.BaseURL`; `${VAR}` placeholders become `{VAR}` and `VAR` is added to `Transport.VarsEnv` |
| `env[]` | entries referenced by a `${VAR}` template → `VarsEnv`; remaining entries matching `*_API_KEY`, `*_KEY`, `*_TOKEN` → `APIKeyEnv`; anything left → `VarsEnv` keyed by its own name. The heuristic misfires on `AWS_SECRET_ACCESS_KEY` and on `GOOGLE_APPLICATION_CREDENTIALS`; the curated overlay pins `api_key_env` for `amazon-bedrock` (`AWS_BEARER_TOKEN_BEDROCK` only; SigV4 reads the AWS credential chain itself) and for the Vertex providers (empty; gcp-adc reads ADC itself) |
| `npm` | `Protocol` and default `Transport.Auth` via the table below |

`npm` → protocol:

| `npm` | Protocol | Auth |
|---|---|---|
| `@ai-sdk/openai-compatible` (167 providers), `@ai-sdk/groq`, `@ai-sdk/cerebras`, `@ai-sdk/togetherai`, `@ai-sdk/deepinfra`, `@ai-sdk/perplexity`, `@ai-sdk/mistral`, `@openrouter/ai-sdk-provider`, `@ai-sdk/gateway`, `@ai-sdk/vercel`, any other `@ai-sdk/*` not listed | `openai-chat` | bearer |
| `@ai-sdk/openai`, `@ai-sdk/azure`, `@ai-sdk/xai` | `openai-responses` | bearer (Azure: header `api-key`) |
| `@ai-sdk/anthropic`, `@ai-sdk/google-vertex/anthropic` | `anthropic` | header `x-api-key` (Vertex: gcp-adc) |
| `@ai-sdk/google` | `google` | header `x-goog-api-key` |
| `@ai-sdk/google-vertex` | `google` | gcp-adc |
| `@ai-sdk/amazon-bedrock` | `anthropic` over `aws-sigv4` + `aws-eventstream` (§9.3) | aws-sigv4 |
| `@ai-sdk/amazon-bedrock/mantle` (per-model) | `openai-responses` or `openai-chat` per `shape` | bearer |
| `@ai-sdk/cohere`, `watsonx-ai-provider`, `@jerome-benoit/sap-ai-provider-v2`, `@qvac/ai-sdk-provider`, other non-OpenAI-shaped SDKs | `Hidden = true` | — |

The table is a Go map in the converter. An `npm` value not in the map falls
to `openai-chat` with `Warnings += "protocol unverified"`; the warning rides
through to `Resolved` so `inspect` and the hub show it.

Per-model `provider` overrides (271 models carry `npm`, 142 carry `api`, 34
carry `shape`) map the same way onto `Model.Protocol` and
`Model.Transport`: `shape: "responses"` → `openai-responses`, `shape:
"completions"` → `openai-chat`, `api` → a model-level `BaseURL` template.
This is what makes Bedrock's OpenAI models (`api:
https://bedrock-mantle.${AWS_REGION}.api.aws/openai/v1`, `shape: responses`)
and Azure's Claude models (`npm: @ai-sdk/anthropic`, `api:
https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`) resolve
without curated entries.

Model-level mapping:

| models.dev | `Caps` / `Model` |
|---|---|
| `limit.context`, `limit.output` (0 → unset) | `ContextWindow`, `MaxOutputTokens` |
| `cost.{input,output,cache_read,cache_write,tiers[]}` | `Cost` |
| `tool_call`, `structured_output` | `Tools`, `StructuredOutput` |
| `reasoning` | `Reasoning` |
| `reasoning_options[].type` = `effort` / `budget_tokens` / `toggle` | `ReasoningControl`; `effort.values` → `EffortValues` (with `none` dropped; evener's `none` clears the setting) |
| `temperature: false` | `Fields["temperature"] = false`, `Fields["top_p"] = false` |
| `modalities.input` | `InputModalities` |
| `knowledge` | `KnowledgeCutoff` |
| `status` | `Model.Status` |
| `interleaved.field` | `ReasoningField` |
| `modalities.output` lacking `text` | row dropped (image/audio/embedding models) |

Nothing else is derived. Models without `tool_call` are kept and flagged so
`llmcall` can use them and the hub picker can hide them.

### 6.2 Curated overlay

`llm/data/providers_overlay.toml`, same schema as `providers.toml` (§10),
loaded as layer 3. It carries only what models.dev lacks or gets wrong. Its
initial contents, in full:

- **Base URLs models.dev omits** because the provider has a vendor SDK:
  `openai` (`https://api.openai.com/v1`), `anthropic`
  (`https://api.anthropic.com`), `google`
  (`https://generativelanguage.googleapis.com/v1beta`), `groq`
  (`https://api.groq.com/openai/v1`), `xai` (`https://api.x.ai/v1`),
  `cerebras` (`https://api.cerebras.ai/v1`), `mistral`
  (`https://api.mistral.ai/v1`), `together` (`https://api.together.ai/v1`),
  `azure` (`https://{AZURE_RESOURCE_NAME}.openai.azure.com/openai/v1`),
  `amazon-bedrock` (`https://bedrock-runtime.{AWS_REGION}.amazonaws.com`),
  `google-vertex` and `google-vertex-anthropic`
  (`https://{GOOGLE_VERTEX_LOCATION}-aiplatform.googleapis.com/v1/projects/{GOOGLE_VERTEX_PROJECT}/locations/{GOOGLE_VERTEX_LOCATION}`,
  with the `global` and `{us,eu}.rep` host forms selected by the location
  value, §9.4).
- **OpenAI platform extras** (`fields` on `openai` only): `store`,
  `prompt_cache_key`, `prompt_cache_retention`, `include`, `truncation`,
  `safety_identifier`, `service_tier`, `previous_response_id`,
  `conversation`, `max_tool_calls`, `background`, `text.verbosity`,
  `reasoning.summary`, `metadata`. Plus `StrictTools = true`,
  `ReasoningSummary = "detailed"` on gpt-5+ rows, `ImageDetail`.
- **`openai-codex`**: the ChatGPT OAuth backend. `base = "openai"`,
  `Transport.Auth = "oauth-openai-codex"`, `BaseURL =
  https://chatgpt.com/backend-api/codex`, the twelve fields the backend
  rejects set `false`, `ResponsesLite = true` on the `gpt-5.6*` rows, and
  the `gpt-5.6` → `gpt-5.6-{sol,terra,luna}` aliases.
- **Anthropic**: `CacheTTL`, `WebSearch = true`, and the `[1m]` rows: for
  each Claude 4.x model whose 1M window is a beta, a row
  `claude-sonnet-4-5[1m]` with `alias_of = "claude-sonnet-4-5"`,
  `context_window = 1000000`, and `headers = { anthropic-beta =
  "context-1m-2025-08-07" }`. Claude 5 rows carry 1M natively in models.dev
  and need no alias. `Fields["temperature"] = false` on Claude 5 rows is
  already true from models.dev `temperature: false`; listed here only if
  upstream regresses.
- **Kimi** (`kimi-for-coding`, `moonshotai`): `Headers["User-Agent"] =
  "claude-cli/2.1.177 (external, cli)"`, `Fields` off for
  `temperature`/`top_p`/`frequency_penalty`/`presence_penalty`,
  `StructuredOutput = false`, `ToolChoiceForcing = false`.
- **z.ai** (`zai`, `zai-coding-plan`, `zhipuai`): `ThinkingFormat = "zai"`,
  `StripEmptyContent`, `MaxStopSequences = 1`, `FinishReasonMap`,
  `Fields["developer_role"] = false`.
- **DeepSeek**: `ThinkingFormat = "deepseek"`, `EmptyReasoningContent`,
  `MaxTokensField = "max_tokens"`.
- **OpenRouter**: `ThinkingFormat = "openrouter"`, `ToolChoiceForcing =
  false` under reasoning (kept as the existing quirk), `CacheControl =
  "anthropic"` on `anthropic/*` rows, `SessionAffinityHeaders`.
- **Ollama** (`ollama`, not in models.dev): `Protocol = openai-chat`,
  `BaseURL = http://localhost:11434/v1`, `Auth = none`, no models (live only),
  `DefaultModel` unset so it is never auto-selected.
- **Pseudo-providers** `openai-compatible`, `anthropic-compatible`,
  `google-compatible`: protocol only, no base URL, no models.
- `DefaultModel` and `CheapModel` for openai, anthropic, google, groq, zai,
  deepseek, openrouter.

Everything else that lives in `evener_model_catalog_overrides.json` today
either exists upstream now (`gpt-5.6*`, `claude-*-5`, `deepseek-v4-*`) or
becomes a row in this file. The 2026-07-02 "silently deleted a model on
refresh" failure cannot recur: overlay rows materialize models when the
upstream row is missing, and the refresh script reports which overlay rows
are now redundant.

### 6.3 User config

`providers.toml`, §10.

### 6.4 Refresh and cache

- `registry.Load(opts)` reads the embedded snapshot, then, if
  `<state-root>/catalog/models.dev.json` exists and its `generated_at` is
  newer, uses that instead.
- If the cache is absent or older than 24h and `opts.Offline` is false, a
  goroutine fetches `https://models.dev/api.json` with `If-None-Match`,
  validates it with the converter (must parse, must contain ≥ 90% of the
  provider count of the embedded snapshot), and writes it atomically
  (temp + rename). The running process keeps its already-loaded registry; the
  refresh takes effect on the next load. A failed refresh logs one line and
  keeps the cache.
- `EVENER_OFFLINE=1` sets `opts.Offline`. `cmdutil` test helpers set it, the
  registry fetcher is an injected `func(ctx, etag) (…)`, and the default test
  state root is a temp dir, so default tests cannot reach the network.
- `make refresh-model-catalog` replaces the embedded snapshot (curl → gzip),
  runs the converter tests, and prints: providers added/removed, models
  added/removed, overlay rows that upstream now covers.
- The embedded file is raw upstream JSON, gzipped (4.4 MB → roughly 500 KB).
  Parsing costs about 30 ms once, lazily, as today.

## 7. Resolution

`registry.Resolve(ref string) (Resolved, error)` is the single lookup path.
It replaces `LookupModelInfo`, `resolveOpenAICompatCatalogModel`,
`fillFromCatalog`, `GetPrice`'s prefix scan, `ResolveLiveModelInfo`,
`cmdutil.ParseModelRef` + `SelectProfile`, and the profile constructors.

### 7.1 Reference syntax

`instance/model`, split on the **first** slash; the model half may contain
slashes (`groq/openai/gpt-oss-120b`, `openrouter/anthropic/claude-opus-5`).
A bare model id with no slash is resolved against the default instance.
There is no special suffix handling: `claude-sonnet-4-5[1m]` is an ordinary
alias row in the curated overlay (§6.2), and `AliasOf` puts the base id on
the wire.

### 7.2 Model lookup order

Within the merged provider record, in order; the first hit wins and is
recorded in `Provenance["model"]`:

1. exact id in the instance's `models` (layer 4)
2. exact id in the provider's merged `models`
3. `AliasOf` chain (one hop)
4. cloud region prefix stripped: `us.`, `eu.`, `apac.`, `global.`, `jp.`
   (Bedrock inference profiles); `@<version>` suffix normalised to the
   catalog's spelling (Vertex)
5. dated family: `-YYYYMMDD` or `-YYYYMMDD-v<N>` suffix removed
6. live listing (layer 5), which establishes existence with provider-level
   caps only

No substring or longest-prefix matching anywhere.

### 7.3 Unknown models

A model id that matches nothing is still resolvable: `Resolved.Model` is
synthesized from provider-level caps, `Warnings` carries `model not in
catalog`, and the wire id is passed through verbatim. Context window is
unset, which the agent treats as "unknown" (no compaction budget until the
live listing or a user row supplies one). The hub shows the warning next to
the model. This is how a model released this morning works before the cache
refreshes.

### 7.4 What the agent reads from `Resolved`

`agent/provider.Profile` becomes a thin wrapper: `Resolved` plus tool
definitions, doc files, and the per-session overrides that exist today
(`WithCommunicateOutputSchema`, `WithAllowedDecisions`, `WithContextWindow`
for live windows). The constructor switch, the per-type default windows,
knowledge cutoffs, effort ladders, and `CheapModel()` switch are deleted;
those values come from `Caps` and the curated `DefaultModel`/`CheapModel`.
Every session branch that keys on `BehaviorTag()` today keys on
`Resolved.Protocol` or a specific `Caps` field instead. `ProviderOptions` is
keyed by protocol.

`ClampReasoningEffort` is called in exactly one place, in `Resolve`'s
per-request companion `registry.ShapeRequest(req, resolved)`, which clamps
the effort to `EffortValues`, applies `MaxOutputTokens` when the request has
none, and drops sampling parameters whose `Fields` entry is false. Adapters
receive a request that is already legal for the endpoint.

## 8. Protocol adapters

### 8.1 Interface

```go
type Protocol interface {
    ID() string
    Complete(ctx, req llm.Request, res registry.Resolved) (llm.Response, error)
    Stream(ctx, req llm.Request, res registry.Resolved) (llm.Stream, error)
    ListModels(ctx, res registry.Resolved) ([]registry.Model, error)
}
```

One instance of each protocol is registered at init; adapters hold no
per-provider state. Base URL, headers, auth, and caps arrive in `Resolved`.
Today there are two Chat Completions implementations
(`llm/providers/openai/chatcompletions.go`, used as the openai adapter's
fallback, and `llm/providers/openaicompat`, which owns the quirks); they
consolidate into the single `openaichat` protocol package, and
`llm/providers/openai` keeps only the Responses implementation.
`llm.Client` becomes: resolve `req.Provider/req.Model` → look up the protocol
by `res.Protocol` → call it. The `nameToTag` map, `RegisterInstanceAdapterFactory`,
`RegisterEnvAdapterFactory`, `NewFromEnv`, `NewFromProviders`, and the
`providerfwd` forwarding packages are deleted. Middleware and the
provider-name stamping on responses/errors/streams stay.

Transport authentication is a small interface implemented once per scheme:

```go
type Authenticator interface {
    Apply(ctx, *http.Request, Credential) error // sets headers or signs
}
```

`bearer`, `header`, and `none` are trivial. `aws-sigv4` uses
`github.com/aws/aws-sdk-go-v2/aws/signer/v4` with credentials from the
standard AWS chain, or a bearer `AWS_BEARER_TOKEN_BEDROCK` when set.
`gcp-adc` uses `golang.org/x/oauth2/google` application-default credentials.
`oauth-openai-codex` is the existing `auth/<instance>.json` flow, moved
behind this interface. Authenticators are cached per instance (token
refresh state lives there).

### 8.2 The pruner and per-protocol baselines

Every adapter builds its full body as today, applies `Transport.Body`
constants, then calls `registry.Prune(body, res.Caps.Fields)`, which deletes
each JSON path whose flag is false. Deleted paths are recorded on the API
attempt log entry as `pruned_fields`. Paths that are always sent (`model`
unless the endpoint template contains `{model}`, `messages`/`input`,
`tools`, `stream`, the max-tokens field) are not in `Fields` and cannot be
pruned.

Baselines (the flags a protocol starts from before any layer):

| Protocol | `true` | `false` |
|---|---|---|
| `openai-chat` | `temperature`, `top_p`, `stop`, `tool_choice`, `response_format`, `stream_options`, `reasoning_effort` | `store`, `frequency_penalty`, `presence_penalty`, `developer_role`, `parallel_tool_calls`, `prompt_cache_key`, `prompt_cache_retention`, `service_tier`, `metadata`, `tools[].function.strict`, `logprobs`, `n`, `seed`, `user` |
| `openai-responses` | `temperature`, `top_p`, `tool_choice`, `text.format`, `reasoning.effort`, `instructions` | `store`, `include`, `truncation`, `safety_identifier`, `service_tier`, `prompt_cache_key`, `prompt_cache_retention`, `previous_response_id`, `conversation`, `metadata`, `max_tool_calls`, `background`, `parallel_tool_calls`, `text.verbosity`, `reasoning.summary`, `tools[].strict` |
| `anthropic` | `temperature`, `top_p`, `stop_sequences`, `tool_choice`, `thinking`, `output_config`, `metadata`, `cache_control` | `service_tier`, `fallbacks`, `container` |
| `google` | `generationConfig.temperature`, `generationConfig.topP`, `generationConfig.stopSequences`, `generationConfig.responseSchema`, `generationConfig.thinkingConfig`, `toolConfig`, `safetySettings` | `cachedContent`, `labels` |

`developer_role` is a pseudo-path: false means the system prompt is sent as
`system`, true as `developer`. `parallel_tool_calls` is off because omitting
it yields OpenAI's default (parallel on) while sending it 400s on several
compatible servers. `cache_control` on the Anthropic baseline is the
ephemeral marker set; `CacheTTL` adds the `ttl`.

The consequence for Groq: the baseline Responses body contains none of the
fields Groq's documentation lists as unsupported (`previous_response_id`,
`store`, `truncation`, `include`, `safety_identifier`, `prompt_cache_key`,
`prompt`), so `protocol = "openai-responses"` on a `groq` instance works with
no groq-specific entry anywhere. `protocol = "openai-chat"` is the registry
default for groq (its `npm` is `@ai-sdk/groq`); Responses is opt-in per
instance or via the probe (§11.2). Groq is in the curated overlay only for
its base URL.

### 8.3 Model-prefix branches become caps

Each of the prefix checks in §2 becomes a model-level cap in the curated
overlay, set on the rows it applies to:

| Today | Cap |
|---|---|
| `responsesLiteModel` (`gpt-5.6`) | `ResponsesLite` on `openai-codex` gpt-5.6 rows |
| `defaultImageDetail` (`gpt-5.4/5.5/gpt-6`) | `ImageDetail = "original"` on those rows; baseline `"high"` |
| `reasoningSummaryLevel` (`gpt-5/gpt-6`) | `ReasoningSummary = "detailed"`; baseline `"auto"` |
| `isClaude5OrNewer` | `Fields["temperature"]=false`, `Transport.Body["thinking.display"]="summarized"` on Claude 5 rows |
| `geminiSupportsMultimodalFunctionResponse` (`gemini-3`) | `MultimodalToolResults = true` on gemini-3 rows |
| `[1m]` synthesis for `claude-opus-4-`/`claude-sonnet-4-` | curated `[1m]` alias rows (§6.2) |
| `minimax/` under openrouter | `Transport.Body["reasoning.enabled"]=true` on those rows |
| `codexModelVariants` | `AliasOf` rows under `openai-codex` |

A new model generation means adding rows to the overlay, not editing an
adapter.

### 8.4 Reasoning

`ReasoningControl` says what the model accepts; `ThinkingFormat` says how
the openai-chat protocol spells it. The nine chat dialects that exist today
(`openai`, `openrouter`, `deepseek`, `together`, `zai`, `qwen`,
`qwen-chat-template`, `chat-template`, `string-thinking`) are kept verbatim
in `openaichat`. The anthropic protocol uses `ReasoningControl`: `effort` →
`thinking: {type: adaptive}` + `output_config.effort`; `budget` →
`thinking.budget_tokens` from the existing effort→budget table. Google maps
effort onto `thinkingConfig` as today. The `none` effort clears the control
on every protocol; nothing is ever sent to force thinking off.

Replay of prior thinking on the chat protocol writes back to
`ReasoningField`, which comes from models.dev `interleaved.field` when
present, else from the field the text arrived on (today's `Signature`
mechanism), else `reasoning_content`.

## 9. Transports and the cloud providers

The transport axis exists so Azure, Bedrock, and Vertex are data.

### 9.1 URL assembly

`Transport.BaseURL` may contain `{VAR}` placeholders. `Endpoint` is a path
template that may contain `{model}` and `{VAR}`; when empty the protocol
default applies (`/chat/completions`, `/responses`, `/v1/messages`,
`/models/{model}:streamGenerateContent`). Variables resolve from
`Transport.Vars` (instance config), then `VarsEnv`, then a load error naming
the variable and the instance. If the final path contains `{model}`, `model`
is not sent in the body.

### 9.2 Azure OpenAI (verified 2026-08-28, Microsoft Learn "v1 API")

- `base_url = https://{AZURE_RESOURCE_NAME}.openai.azure.com/openai/v1`
  (`services.ai.azure.com/openai/v1` also accepted upstream).
- `auth = header`, `auth_header = api-key`; Entra bearer tokens work through
  `auth = bearer` with the token in `api_key`.
- No `api-version` parameter on v1.
- Both `/responses` and `/chat/completions` exist; `model` in the body is
  the **deployment name**. A user maps deployments to catalog rows with
  `alias_of`:

```toml
[providers.azure]
api_key = "$AZURE_API_KEY"
[providers.azure.vars]
AZURE_RESOURCE_NAME = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"
```

- Claude on Azure Foundry: models.dev already marks those rows `npm:
  @ai-sdk/anthropic` with `api:
  https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`, so
  they resolve to the anthropic protocol at that base URL with the same
  `api-key` header. One instance, two protocols.

### 9.3 Amazon Bedrock (verified 2026-08-28, AWS API reference and Chat Completions docs)

Two paths, both under one `amazon-bedrock` instance:

- **Claude and other InvokeModel models**: `anthropic` protocol,
  `auth = aws-sigv4` (service `bedrock`, region from `AWS_REGION`), `endpoint
  = /model/{model}/invoke-with-response-stream`, `framing =
  aws-eventstream` (`Content-Type: application/vnd.amazon.eventstream`; each
  `chunk.bytes` payload is one Anthropic SSE event's JSON), `body.anthropic_version
  = "bedrock-2023-05-31"`, and `model` omitted from the body because the
  path contains `{model}`. `AWS_BEARER_TOKEN_BEDROCK`, when set, is used as
  a bearer instead of SigV4. Non-streaming uses `/model/{model}/invoke`.
- **OpenAI-shaped models**: models.dev marks them `@ai-sdk/amazon-bedrock/mantle`
  with `api: https://bedrock-mantle.${AWS_REGION}.api.aws/openai/v1` and
  `shape: responses` (or `/v1` + `completions`); the converter turns that
  into a model-level transport with bearer auth. AWS also documents
  `https://bedrock-runtime.{region}.amazonaws.com/openai/v1/chat/completions`
  with SigV4 or the bearer token; the overlay can point rows there if the
  Mantle quota is the wrong one.
- Catalog ids: models.dev lists both plain (`anthropic.claude-opus-5`) and
  inference-profile (`us.anthropic.claude-opus-5`, `global.…`) rows; §7.2's
  prefix strip covers user-supplied profiles the catalog lacks.

The event-stream decoder is the one genuinely new piece of transport code
(~150 lines, or `github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream`).
The Anthropic adapter's SSE parser takes an `io.Reader` of events; the
framing layer produces the same reader from either wire format.

### 9.4 Google Vertex (verified 2026-08-28, Claude on Google Cloud docs)

- Host by location: `global` → `https://aiplatform.googleapis.com`; `us` /
  `eu` → `https://aiplatform.{loc}.rep.googleapis.com`; anything else →
  `https://{loc}-aiplatform.googleapis.com`. The overlay expresses this as a
  `base_url` template plus a `location_host` rule the transport layer knows
  (three-line function; the only location-aware code).
- Path prefix `/v1/projects/{GOOGLE_VERTEX_PROJECT}/locations/{GOOGLE_VERTEX_LOCATION}`.
- **Gemini**: `google` protocol, `endpoint =
  /publishers/google/models/{model}:streamGenerateContent?alt=sse`,
  `auth = gcp-adc`.
- **Claude**: `anthropic` protocol, `endpoint =
  /publishers/anthropic/models/{model}:streamRawPredict` (`:rawPredict`
  non-streaming), `body.anthropic_version = "vertex-2023-10-16"`, `model`
  omitted from the body, `auth = gcp-adc`. Model ids are Vertex's
  (`claude-opus-5`, `claude-sonnet-4-5@20250929`), which models.dev already
  lists under `google-vertex-anthropic`.
- Vertex's OpenAI-compatible MaaS endpoint for Llama/DeepSeek/Kimi rows is
  carried per-model by models.dev (`api: …/endpoints/openapi`, `npm:
  @ai-sdk/openai-compatible`) and needs nothing from us.

## 10. `providers.toml`

Same schema as the registry. Every key is optional except that an instance
must end up with a protocol and a base URL after layering.

```toml
default = "groq"

[providers.groq]                        # name matches a registry id → inherits it
api_key  = "$GROQ_API_KEY"              # optional; registry says GROQ_API_KEY already
protocol = "openai-responses"           # override the registry default (openai-chat)

[providers.work]                        # name differs → say what it is based on
base     = "openai"
base_url = "https://gw.example.com/v1"
protocol = "openai-chat"
headers  = { "X-Portkey-Provider" = "openai" }
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.work.fields]
store = false                            # trim something the gateway rejects
[providers.work.models."glm-5.2-nvfp4"]
context_window    = 1048576
max_output_tokens = 131072
effort_values     = ["high", "max"]
thinking_format   = "zai"

[providers.local]
base     = "openai-compatible"           # protocol-only pseudo-provider
base_url = "http://localhost:8080/v1"
auth     = "none"

[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
AWS_REGION = "us-east-1"

[providers.vertex]
base = "google-vertex-anthropic"
[providers.vertex.vars]
GOOGLE_VERTEX_PROJECT  = "my-project"
GOOGLE_VERTEX_LOCATION = "global"
```

TOML keys map onto the structs in §4 as follows: `base`, `api_key`,
`api_key_env`, `headers`, `credential_headers`, `default_model`,
`cheap_model` → `Provider`; `base_url`, `auth`, `auth_header`, `endpoint`,
`framing`, `vars`, `body` → `Provider.Transport`; `protocol` → `Provider.Protocol`;
every `Caps` field by its snake_case name (`context_window`,
`effort_values`, `thinking_format`, `fields`, …) at the instance level →
`Provider.Caps`, and inside `[providers.X.models."<id>"]` → `Model.Caps`,
where `alias_of`, `wire_id`, `protocol`, `headers`, and the transport keys are
also accepted.

Rules, enforced at load with errors that name the instance and key:

- names are lowercase, no slash, unique; `base` must name a registry id
- `protocol` must be a registered protocol
- `auth` ∈ `bearer | header | none | aws-sigv4 | gcp-adc | oauth-openai-codex`
- `fields` keys must be paths the named protocol's pruner knows (typo guard)
- `thinking_format`, `max_tokens_field`, `cache_control`, `reasoning_control`
  are validated against their vocabularies
- `effort_values` entries non-empty; `"off"` rejected
- `$ENV` expansion in `api_key`, `headers`, `credential_headers`, `vars` uses
  today's `$NAME` / `${NAME}` / `$$` rules and happens at resolve time, so one
  instance's missing variable never blocks another
- `WriteFile` keeps today's scrub-and-restore so hub rewrites never persist a
  credential the user did not author

`type`, `api_style`, `quirks`, `[instances.*]`, and `compat` are gone. A file
that still uses them fails to load with a message pointing at this document.

Credentials keep the existing `internal/credentials.Store` resolution (file
entry by instance name → env vars by instance name → env vars from the
registry's `APIKeyEnv`). The credential tag concept is replaced by the
provider id.

## 11. Commands and the hub

### 11.1 `evener models`

- `list [--provider X] [--all]` — resolved rows with protocol, context,
  output cap, cost, effort ladder, warnings. Hidden and non-text rows only
  with `--all`.
- `inspect <ref>` — the full `Resolved` record with provenance per field,
  the pruned-field list the protocol would apply, and the request skeleton
  (endpoint, auth scheme, headers with secrets masked).
- `refresh [--force]` — fetch models.dev into the cache now; print the diff.

### 11.2 `evener providers`

- `list` — instances, base, protocol, endpoint, credential source, live
  reachability if `--check`.
- `probe <instance> [--write]` — `GET /models`, then a one-token request
  (`max_tokens: 1`, one-word prompt, no tools) against `/responses` and
  `/chat/completions` (OpenAI protocols only), reporting which succeed. `--write` records `protocol` and any discovered
  models into `providers.toml`. The runtime never probes on its own.
- `add <name> --base X [--base-url …] [--protocol …] [--var K=V]` — writes
  the entry, runs `probe --write` unless `--no-probe`.

The hub's instance CRUD (`cmd/evener-hub/app_instances.go`) calls the same
functions; the add form offers the registry's provider list, a protocol
select, base URL, and `vars` derived from the chosen provider's `VarsEnv`.
`model/list` returns `Resolved`-derived rows straight from the registry, so
`enrichModelDescriptors` and `applyInstanceModelOverride` are deleted.

`cmd/llmcall` uses `cmdutil.LoadClient` like everything else.

## 12. Errors

One classifier, `llm.ClassifyHTTPError(status, headers, body)`, replaces the
status-only switch. It reads the structured body first (`error.code`,
`error.type`, `error.message` for OpenAI-shaped bodies; `error.type` for
Anthropic; the exception name for Bedrock), then the status, then message
patterns:

| Signal | Kind |
|---|---|
| 429; 413 whose message matches `tokens per minute\|TPM\|requests per`; Bedrock `ThrottlingException`; OpenAI `rate_limit_exceeded` | `KindRateLimit` (retryable, honors `retry-after` and `x-ratelimit-reset-*`) |
| 400/413 matching `context length\|maximum context\|too many tokens\|reduce the length` without a rate wording; Anthropic `invalid_request_error` with `prompt is too long` | `KindContextLength` |
| 400 naming an unrecognized parameter (`Unrecognized request argument\|unknown field\|is not supported\|invalid JSON body`) | `KindInvalidRequest` with `Hint: run evener models inspect <ref> and set fields.<name>=false` |
| 401/403 | as today |
| 404 with `model` in the message | `KindNotFound` |

The provider's message is always included verbatim. `BehaviorTag()` on error
types is removed; `Provider()` returns the instance name and a new
`Protocol()` returns the protocol id.

## 13. Testing

- **Converter**: a checked-in 40-provider excerpt of models.dev
  (`llm/registry/testdata/models.dev.sample.json`) covering every `npm`
  in the table, per-model `provider` overrides, every `reasoning_options`
  shape, tiers, and a hidden provider. Table tests assert the converted
  records; a fuzz target feeds mutated JSON.
- **Merge and provenance**: property tests that a later layer's set field
  always wins, that `nil` always inherits, that map layers merge key-wise,
  and that every provenance entry names a real layer.
- **Resolution**: golden `Resolved` records (JSON) for a fixed set of
  references: `groq/openai/gpt-oss-120b` (chat and responses), `openai/gpt-5.5`,
  `openai-codex/gpt-5.6`, `anthropic/claude-opus-5[1m]`,
  `azure/gpt55-prod`, `bedrock/us.anthropic.claude-opus-5`,
  `vertex/claude-sonnet-4-5@20250929`, `openrouter/anthropic/claude-opus-5`,
  `local/whatever` (unknown model), `ollama/llama3:8b` (live-only).
- **Pruner**: every baseline key, nested paths, array-element paths
  (`tools[].strict`), unknown key rejection at load.
- **Wire captures**: the existing per-protocol golden bodies
  (`wire_capture_test.go`) are regenerated from `Resolved` inputs; a new
  case per cloud transport asserts endpoint, auth headers (signature
  presence, not value), body constants, and model omission.
- **Framing**: an AWS event-stream fixture recorded from a real Bedrock call
  (opt-in `EVENER_BEDROCK_E2E=1` to re-record) decoded into the same events
  as the SSE fixture.
- **Refresh**: injected fetcher; asserts cache write, ETag round-trip,
  offline short-circuit, the ≥ 90% sanity floor, and that no test path
  constructs a real HTTP client.
- **Error classifier**: table of real captured bodies (Groq 400 and 413,
  OpenAI unrecognized-argument 400, Anthropic prompt-too-long, Bedrock
  throttling).
- **Config**: every load-error rule in §10 has a failing fixture; the old
  keys produce the pointer-to-doc error.
- **Cross-adapter differential** (`llm/providers/difftest`) keeps running
  unchanged; it exercises decoding, which this design does not touch.
- Live provider behavior stays behind `EVENER_LIVE_TESTS=1` as the repo
  rules require.

## 14. Implementation order

Work happens on one branch and lands as one cut-over; there is no
compatibility period. Order is chosen so each step has passing tests.

1. **`llm/registry`**: types, converter, overlay loader, config loader,
   merge with provenance, `Resolve`, `ShapeRequest`, `Prune`, embedded
   snapshot, cache/refresh with injected fetcher. `evener models
   list|inspect|refresh`. Nothing else consumes it yet. (~1,800 lines + data.)
2. **Protocols consume `Resolved`**: pruner wired into all four adapters;
   the two Chat Completions implementations consolidated; the
   `bearer`/`header`/`none` authenticators (all exist today in some form)
   and the `oauth-openai-codex` move behind the `Authenticator` interface;
   the four quirk/compat paths, the prefix branches, `Adaptive`, the
   Responses→Chat fallback, and the wrapper packages deleted; `llm.Client`
   routes by protocol; error classifier. (~net −3,000 lines.)
3. **Agent, hub, llmcall**: `Profile` over `Resolved`; behavior-tag
   branches replaced; hub `model/list` and instance CRUD on the registry;
   `providers probe|add`; `llmcall` on `LoadClient`; docs
   (`docs/llm-providers.md` rewritten around §3–§10). (~net −1,500 lines.)
4. **Cloud transports**: `aws-sigv4` + event-stream framing, then
   `gcp-adc`, each with its recorded-fixture tests. Azure needs nothing
   beyond steps 1–2 (templates plus `header` auth). (~700 lines.)

## 15. Decisions taken and open questions

Decided:

- models.dev is the only upstream. LiteLLM's extra breadth is regional
  Bedrock/Azure/Vertex keys and non-chat modes we do not use; models.dev has
  those providers with per-model transport hints LiteLLM lacks.
- The embedded snapshot is raw upstream JSON. One converter, no generator.
- Protocol selection is explicit. The probe is a tool that writes config.
- One flat `Caps` plus `Fields`. Not four typed compat shapes.
- Claude on Bedrock via InvokeModel + event-stream framing, not Converse.

Open, for Jesse:

1. **Bedrock framing dependency**: hand-write the ~150-line event-stream
   decoder or take `aws-sdk-go-v2/aws/protocol/eventstream` (and the signer
   package, which we need anyway)? Recommendation: take the SDK packages;
   they are small modules.
2. **Ollama's "never the default" rule**: keep by leaving `DefaultModel`
   empty (proposed), or drop it now that the default instance is explicit
   in config?
3. **Snapshot size**: embed all 207 providers (~500 KB gzipped) or have the
   refresh script drop `Hidden` providers before embedding? Proposed: embed
   everything; the filter is one line if it ever matters.
