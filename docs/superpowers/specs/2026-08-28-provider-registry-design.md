# Provider Registry and Capability Resolution

**Date:** 2026-08-28 (revision 4, after two adversarial review rounds)
**Status:** Draft for review
**Replaces:** the LiteLLM-vendored model catalog, `providercfg.CompatConfig`,
`openaicompat.ProviderQuirks` presets, the vendor wrapper adapter packages, the
behavior-tag split, env-seeded adapter factories, and `api_style`. No backward
compatibility is kept.

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
- Bedrock's legacy `InvokeModel`/`Converse` path (ARN-versioned ids, AWS
  event-stream framing, SigV4). Anthropic serves Opus 4.7 and later on Bedrock
  through the Messages API over plain HTTPS/SSE (§9.3); that is the only
  Bedrock path this design supports. Claude Opus 4.6 and earlier on Bedrock
  are out of scope.
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
  effort ladder. It also sends `max_tokens` to OpenAI reasoning models, which
  reject it (`openaicompat/quirks.go:35`, versus
  `openai/chatcompletions.go:414`).
- **Model-name prefix matching in wire builders and the session**: `gpt-5.6`
  (`responses.go:1007`), `gpt-5.4/5.5/gpt-6` (`:1042`), `gpt-5/gpt-6`
  (`:1060`), `gemini-3` (`google/request.go:251`), the Claude
  numeric-generation parse (`anthropic/request.go:261-272`),
  `claude-opus-4-`/`claude-sonnet-4-` (`anthropic/models.go:98`),
  `minimax/` under openrouter (`profile.go:1287`), and
  `openAIModelSupports24hPromptCache` (`agent/session.go:1362`).
- **Errors are classified mostly by status code**, so Groq's 413 "tokens per
  minute … Limit 8000, Requested 24414" surfaces as an opaque failure rather
  than a rate limit with the provider's message.
- `cmd/llmcall` builds its client with `llm.NewFromEnv` and cannot see
  `providers.toml` at all.

## 3. Concepts

Five nouns. Only the first is code.

| Noun | What it is | Where it lives |
|---|---|---|
| **Protocol** | A wire format: how to encode a request body, decode a stream, list models, count tokens. Exactly one Go package each. | `llm/providers/{chatcompletions,responses,anthropic,google}` |
| **Transport** | How to reach an endpoint: auth scheme, URL templates, constant headers and body fields. Data, plus one small authenticator per scheme. | `registry.Transport` |
| **Provider** | A named endpoint definition: id, display name, transport, default protocol, surface, caps, models. Data. | `registry.Provider` |
| **Model** | A row under a provider: id, limits, cost, modalities, reasoning facts, caps, surface, optional protocol/transport override. Data. | `registry.Model` |
| **Surface** | The agent-facing vendor family a model was trained for: which doc files to read, which tool set and tool names to offer, which prompt sections apply. One of `openai`, `anthropic`, `google`, `generic`. A model attribute with a provider-level fallback, independent of the endpoint serving it. | `registry.Model.Surface`, `registry.Provider.Surface` |

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

### 3.2 Why Surface is separate from Protocol and Provider

Claude served by OpenRouter over Chat Completions still wants `CLAUDE.md`,
`edit_file`, and the Anthropic prompt sections; GPT served by Azure over
Responses still wants `AGENTS.md`, `apply_patch`, and the OpenAI append. Today
this is fused into the behavior tag, which is why `openrouter-anthropic` exists
as a separate adapter. Surface is derived from the model row (models.dev
`family`) with the provider's surface as the fallback, so it survives any
routing and also covers a model released this morning on the `anthropic`
instance.

## 4. Data model

All types live in a new leaf package `llm/registry`. Optional scalars are
pointers so that "unset" is distinguishable from `false`/`0` at every layer.

```go
type Provider struct {
    ID            string            // registry id; the instance name for user entries
    Base          string            // id of the record this one layers on (curated and user layers)
    InheritModels *bool             // default true; false = start with no rows from Base
    Name          string            // display name
    Doc           string            // upstream documentation URL
    Protocol      string            // default protocol for models without their own
    Surface       string            // fallback surface for rows without one and for synthesized rows
    Transport     Transport
    APIKeyEnv     []string          // env vars consulted for the key, in order
    Headers       map[string]string // constant request headers ($ENV refs allowed); non-secret
    CredentialHeaders map[string]string // secret headers; scrubbed from logs and hub rewrites
    Caps          Caps              // provider-level capability overlay
    Models        map[string]Model  // keyed by model id
    DefaultModel  string            // curated: what to pick when the user gives none
    CheapModel    string            // curated: the provider's cheap/fast model (bare id, same provider)
    Hidden        bool              // rows evener cannot drive (unsupported protocol, no base URL)
}

type Model struct {
    ID        string
    WireID    string     // id sent on the wire; defaults to ID
    AliasOf   string     // inherit facts and caps from another row ("id" in this provider, or "provider-id/id"); never affects WireID
    Protocol  string     // override of the provider default
    Transport *Transport // field-wise overlay on the provider transport (Bedrock Mantle rows, Azure Claude rows)
    Headers   map[string]string // model-level constant headers (Anthropic beta headers)
    Surface   string     // openai | anthropic | google | generic
    Caps      Caps
    Status    string     // "", "beta", "deprecated"
}

type Transport struct {
    Auth        string            // bearer | header | none | gcp-adc | oauth-openai-codex
    AuthHeader  string            // auth=header: header name (x-api-key, api-key, x-goog-api-key)
    BaseURL     string            // may contain {VAR} placeholders
    HostRule    string            // "" | vertex-location | ollama-host (§9.1)
    Endpoint    string            // non-streaming completion path; protocol default when empty
    StreamEndpoint      string    // streaming completion path; protocol default when empty
    ModelsEndpoint      string    // listing path; protocol default when empty; "-" means unsupported
    CountTokensEndpoint string    // token-count path; protocol default when empty; "-" means unsupported
    Vars        map[string]string // template variables; values or $ENV refs
    VarsEnv     map[string]string // var name → env var consulted when Vars lacks it
    Body        map[string]any    // constant JSON paths set after the prune (§8.2); parents are created
}
```

`Base` inheritance is the same at every layer: the record's merged form is
its base's merged form with the record's own fields overlaid, models
included unless `InheritModels = false`. A model whose `Protocol` differs
from the provider's does **not** inherit the provider's protocol-specific
transport fields (`Endpoint`, `StreamEndpoint`, `ModelsEndpoint`,
`CountTokensEndpoint`, `Body`) or the provider's `Fields`; it starts from its
own protocol's defaults and baseline, then takes its own `Transport` overlay.
This is what keeps `google-vertex`'s thirteen Claude rows (models.dev marks
them `@ai-sdk/google-vertex/anthropic` with no per-model `api`) off the
Gemini endpoint. A dangling `AliasOf` target is a load error.

### 4.1 Caps

One flat struct shared by every protocol. Fields a protocol does not use are
ignored by it. `Fields` carries "send this optional wire field or not"; the
explicit fields are the transforms that cannot be expressed that way.

```go
type Caps struct {
    // Model facts. Catalog-sourced; user layers may correct them.
    ContextWindow     *int       // input budget the agent plans against
    MaxOutputTokens   *int
    Tools             *bool
    StructuredOutput  *bool      // json_schema accepted; false downgrades to json_object at build time
    Reasoning         *bool      // false: no reasoning controls, no thinking replay, empty effort list
    ReasoningControls []string   // subset of effort, budget_tokens, toggle (models.dev names); replace on overlay
    EffortValues      []string   // wire-spelled ladder, ascending; replace on overlay
    InputModalities   []string   // text, image, pdf, audio; replace on overlay
    KnowledgeCutoff   *string    // YYYY-MM or YYYY-MM-DD
    Cost              *Cost      // $/M tokens; replace on overlay

    // Optional wire fields: JSON path → send. Key-wise merge.
    // Every key must be in the row's protocol prunable set (§8.2).
    Fields map[string]bool

    // Structural request shaping.
    MaxTokensField    *string    // openai-chat: max_tokens (baseline) | max_completion_tokens
    ThinkingFormat    *string    // openai-chat dialect, §8.4
    ThinkingShape     *string    // anthropic: adaptive | budget | budget+effort
    ThinkingDisplay   *string    // anthropic adaptive: "" | summarized
    ThinkingAlwaysOn  *bool      // send the enable object even when no effort is requested (§8.4)
    ReasoningField    *string    // openai-chat replay: reasoning_content | reasoning | reasoning_text | reasoning_details
    ReasoningSummary  *string    // openai-responses: none (omit, baseline) | auto | detailed
    ChatTemplateKwargs map[string]any
    FinishReasonMap   map[string]string // replace on overlay
    CacheControl      *string    // anthropic-style cache_control markers on openai-chat gateways
    CacheTTL          *string    // anthropic: "" | 1h
    StrictTools       *bool      // openai-responses: strict:true + schema strictify; chat: strict:false marker
    ToolChoiceForcing *bool      // false → downgrade required/named tool_choice to auto
    MaxStopSequences  *int
    ImageDetail       *string    // openai-responses: original | high | low
    ResponsesLite     *bool      // ChatGPT backend input-item shaping for gpt-5.6

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
    WebSearch             *bool // provider-side web search available
}

type Cost struct {
    Input, Output, CacheRead, CacheWrite float64 // $ per million tokens
    Tiers []CostTier
}

type CostTier struct {
    InputTokensAbove int     // models.dev tier.size when tier.type == "context"
    Input, Output, CacheRead, CacheWrite float64
}
```

**Merge order.** Layers apply in order 1→5 (§5). Within one layer the
provider-level `Caps` apply first and the matching model row's `Caps`
second. Across layers, later always wins regardless of level: a layer-3
provider-level pin beats a layer-1 model-level fact (that is how Bedrock's
`StructuredOutput = false` overrides models.dev's per-row `true`, §9.3), and
a layer-4 instance-level `context_window` rewrites every row of that
instance (the documented meaning of an instance-wide overlay; put it on the
row when only one model is meant). A pointer or scalar set at a later step
replaces; a `nil` inherits. Slices (`EffortValues`, `InputModalities`,
`ReasoningControls`) and `Cost`/`FinishReasonMap` replace wholesale.
`Fields`, `ChatTemplateKwargs`, `Transport.Vars`, `Transport.Body`, and
`Headers` merge key-wise. A model's `Transport` overlays the provider's the
same way (subject to the cross-protocol rule in §4). This is one
reflect-driven function (~80 lines) that also records
`map[fieldPath]"layer/level"` provenance.

### 4.2 Resolved

```go
type Resolved struct {
    Instance   string      // user-facing instance name (routing, logs, errors)
    ProviderID string      // registry id the instance is based on (Base chain root)
    Protocol   string
    Surface    string
    Transport  Transport   // vars substituted, $ENV expanded, host rule applied, endpoints filled
    ModelID    string      // the reference as given
    WireID     string      // id sent on the wire
    Model      Model       // merged row (may be synthesized; see §7.3)
    Caps       Caps        // fully merged; every prunable path of the protocol present in Fields
    Headers    map[string]string
    Credential Credential  // resolved key/token source, never logged
    Provenance map[string]string
    Warnings   []string    // "model not in catalog", "protocol unverified", …
}
```

`Resolved` is serializable (minus `Credential`) and is what `evener models
inspect` prints and what the API attempt log records in summary form
(protocol, base URL, pruned fields, provenance for anything the user
overrode).

## 5. Layers

Every layer is a set of `Provider` records in the same schema. Later wins,
with one exception for the live layer.

| # | Layer | Source | Refreshable |
|---|---|---|---|
| 1 | **Upstream snapshot** | `llm/data/models.dev.json.gz`, the raw models.dev `api.json`, converted at load (§6.1) | `make refresh-model-catalog` |
| 2 | **Upstream cache** | `<state-root>/catalog/models.dev.json` + `.meta` (etag, fetched-at), same converter | background, 24h (§6.4) |
| 3 | **Curated overlay** | `llm/data/providers_overlay.toml`, hand-maintained | with the release |
| 4 | **User config** | `<state-root>/providers.toml` | by the user or hub |
| 5 | **Live listing** | the instance's `ModelsEndpoint` | per process, cached |

Layer 2 replaces layer 1 wholesale when its fetched-at timestamp is newer
than the embedded snapshot's (recorded by the refresh script in
`llm/data/models.dev.meta.json`); they are never merged. Layers 3 and 4
overlay field-wise.

Layer 5 establishes existence of models the catalog lacks and supplies the
model facts it explicitly advertises (`Tools`, `InputModalities`,
`ContextWindow`, `MaxOutputTokens`, `EffortValues`, `Cost`, `Reasoning`, and
`ThinkingAlwaysOn` from OpenRouter's `reasoning.mandatory`), with
`Provenance = "live"`. It overrides layers 1–3 for those facts (today's
rule that OpenRouter's `supported_parameters` is authoritative over the
catalog, `llm/model_catalog.go:297-330`) but **never a field the user layer
set** (today's `WithLiveModelInfo` rule that `providers.toml` rows beat live
enrichment). It never touches wire-shaping caps. Live rows whose id matches
the non-chat pattern list (`embedding`, `whisper`, `tts`, `dall-e`,
`moderation`, `audio`, `transcribe`, `image`, `realtime`, `davinci`,
`babbage`, `sora`; one list in `registry`, replacing `nonChatModelSubstrings`
and `skipOpenAIModel`) are dropped.

### 5.1 Instances

An **instance** is a named, usable provider. Instances come from two places:

- **Explicit**: every `[providers.X]` entry in `providers.toml`.
- **Implicit**: every non-hidden registry provider that is not shadowed by an
  explicit entry of the same name and whose credential resolves without
  network access: a credentials-store entry for the id, or one of its
  `APIKeyEnv` variables set, or for `oauth-openai-codex` its OAuth record
  (`auth/openai-codex.json`), or for `gcp-adc` the
  `GOOGLE_APPLICATION_CREDENTIALS` variable or the well-known ADC file
  (`FindDefaultCredentials`' metadata-server probe is never run at load; it
  runs at first request). Providers with `auth = none` (Ollama) are implicit
  unconditionally.

Implicit instances are computed identically by every process from the same
inputs; the hub no longer materializes `providers.toml` at startup and passes
nothing to children beyond `EVENER_PROVIDERS_CONFIG` when a file exists. The
hub lists implicit instances flagged *from environment*; editing one or
making it the default writes a shadowing explicit entry; removing one is
refused with a message naming the variable or record that makes it exist.

The **default instance** is `default` from `providers.toml` when set; else
the first explicit instance in file order; else the first implicit instance
in the curated `default_order` list (§6.2) that has a `DefaultModel`.
Providers without a `DefaultModel` (Ollama, the pseudo-providers) are never
defaulted to, which replaces `NonDefaultEligible`. `openai-codex` precedes
`openai` in `default_order`, preserving today's "stored OAuth beats API key"
choice. When no instance exists at all, resolving a bare model id fails with
"no default instance: set `default` in providers.toml or export a provider
key".

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
| `api` | `Transport.BaseURL`, trailing slash trimmed; `${VAR}` placeholders become `{VAR}` and `VAR` is added to `Transport.VarsEnv` keyed by itself |
| `env[]` | entries referenced by a `{VAR}` template → `VarsEnv`; remaining entries matching `*_API_KEY`, `*_KEY`, `*_TOKEN`, `*_PAT` → `APIKeyEnv`; anything left → `VarsEnv` keyed by its own name. The heuristic misfires on `AWS_SECRET_ACCESS_KEY` and would order Google's keys `GOOGLE_API_KEY` first; the curated overlay pins `api_key_env` for `amazon-bedrock`, `google` (`GEMINI_API_KEY` first, as today), and the Vertex providers (empty) |
| `npm` | `Protocol` and default `Transport.Auth` via the table below |

`npm` → protocol:

| `npm` | Protocol | Auth |
|---|---|---|
| `@ai-sdk/openai-compatible` (167 providers), `@ai-sdk/groq`, `@ai-sdk/cerebras`, `@ai-sdk/togetherai`, `@ai-sdk/deepinfra`, `@ai-sdk/perplexity`, `@ai-sdk/mistral`, `@openrouter/ai-sdk-provider`, `@ai-sdk/gateway`, `@ai-sdk/vercel` | `openai-chat` | bearer |
| `@ai-sdk/openai`, `@ai-sdk/azure`, `@ai-sdk/xai` | `openai-responses` | bearer (Azure: header `api-key`) |
| `@ai-sdk/anthropic`, `@ai-sdk/google-vertex/anthropic`, `@ai-sdk/amazon-bedrock` | `anthropic` | header `x-api-key` (Vertex: `gcp-adc`, §9.4) |
| `@ai-sdk/google` | `google` | header `x-goog-api-key` |
| `@ai-sdk/google-vertex` | `google` | `gcp-adc` |
| `@ai-sdk/amazon-bedrock/mantle` (per-model) | `openai-responses` or `openai-chat` per `shape` | bearer |
| `@ai-sdk/cohere`, `watsonx-ai-provider`, `@jerome-benoit/sap-ai-provider-v2`, `@qvac/ai-sdk-provider`, `@saladtechnologies-oss/ai-sdk-provider`, `merge-gateway-ai-sdk-provider`, `ai-gateway-provider`, `@aihubmix/ai-sdk-provider`, `gitlab-ai-provider`, `venice-ai-sdk-provider` | `Hidden = true` | — |
| anything else | `openai-chat` + `Warnings += "protocol unverified"` | bearer |

The table is a Go map in the converter. A provider that ends up with no
`BaseURL` after all layers is `Hidden` automatically (`models list --all`
shows it as *needs base_url*); of the 26 models.dev providers without
`api`, the curated overlay supplies URLs for the ones in §6.2 and the rest
stay hidden until someone adds a verified URL.

Per-model `provider` overrides (271 models carry `npm`, 142 carry `api`, 34
carry `shape`) map the same way onto `Model.Protocol` and
`Model.Transport`: `shape: "responses"` → `openai-responses`, `shape:
"completions"` → `openai-chat`, `api` → a model-level `BaseURL` template.
This is what makes Bedrock's OpenAI models (`api:
https://bedrock-mantle.${AWS_REGION}.api.aws/v1` or `…/openai/v1`, `shape:
responses`) and Azure Foundry's Claude models (`npm: @ai-sdk/anthropic`,
`api: https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`)
resolve without curated entries.

**Base URL convention.** models.dev base URLs include the version segment
(`…/v1`, `…/anthropic/v1`, `…/paas/v4`); the AI SDK appends the resource.
The registry follows the same convention. Protocol defaults:

| Protocol | `Endpoint` | `StreamEndpoint` | `ModelsEndpoint` | `CountTokensEndpoint` |
|---|---|---|---|---|
| `openai-chat` | `/chat/completions` | same (stream flag in body) | `/models` | `-` |
| `openai-responses` | `/responses` | same | `/models` | `-` (the `openai` overlay sets `/responses/input_tokens`, today's `token_count.go:119` path) |
| `anthropic` | `/messages` | same | `/models` | `/messages/count_tokens` |
| `google` | `/models/{model}:generateContent` | `/models/{model}:streamGenerateContent?alt=sse` | `/models` | `/models/{model}:countTokens` |

Every curated base URL in §6.2 carries its version segment.

Model-level mapping:

| models.dev | `Caps` / `Model` |
|---|---|
| `limit.input` when present, else `limit.context` (0 → unset) | `ContextWindow` — the agent budgets input against it (`agent/internal/contextmgr/context_manager.go:218`); 638 rows have `input != context`, e.g. `gpt-5` 272000 vs 400000 |
| `limit.output` (0 → unset) | `MaxOutputTokens` |
| `cost.{input,output,cache_read,cache_write,tiers[]}` | `Cost` |
| `tool_call`, `structured_output` | `Tools`, `StructuredOutput` |
| `reasoning` | `Reasoning` |
| `reasoning_options[].type` (a list; 752 rows carry two or three) | `ReasoningControls` as the set of types present, models.dev spelling (`effort`, `budget_tokens`, `toggle`); the `effort` entry's `values` → `EffortValues` with `none` dropped (evener's `none` clears the setting); values outside evener's vocabulary are kept verbatim and clamp as the nearest rank |
| `temperature: false` | `Fields[<temperature path of the row's protocol>] = false` and likewise for top-p (`temperature`/`top_p` on the OpenAI protocols and anthropic, `generationConfig.temperature`/`generationConfig.topP` on google) |
| `modalities.input` | `InputModalities` |
| `knowledge` | `KnowledgeCutoff` |
| `status` | `Model.Status` |
| `interleaved` (boolean on 65 rows, `{field: …}` on 893) | `ReasoningField` from the object form only |
| `family` | `Surface`: `claude*` → `anthropic`; `gpt*` except `gpt-oss*`, `o`, `o-mini`, `o-pro`, `gpt-codex*` → `openai`; `gemini*`, `gemma*` → `google`; anything else, including the 666 rows with no `family`, → unset, so the provider's `Surface` applies (§6.2), else `generic` |
| id ending `@default` (Vertex rows) | `WireID` = id without the suffix; other `@<version>` ids are sent verbatim |
| `modalities.output` lacking `text` | row dropped (image/audio/embedding models) |

`ThinkingShape` is derived per row: `adaptive` when `effort ∈
ReasoningControls` **and** the row's surface is `anthropic` (adaptive
thinking is a Claude API feature; third-party anthropic-protocol vendors
such as `kimi-for-coding` keep the budget shape), else `budget` when
`budget_tokens ∈ ReasoningControls`, else unset. `ThinkingAlwaysOn` is set
`true` whenever `ThinkingShape` resolves to `adaptive` (today's builder sends
the adaptive object for every adaptive-capable model whether or not an
effort is requested, `anthropic/request.go:146-157`). Nothing else is
derived. Models without `tool_call` are kept and flagged so `llmcall` can use
them and the hub picker can hide them.

### 6.2 Curated overlay

`llm/data/providers_overlay.toml`, same schema as `providers.toml` (§10),
loaded as layer 3. It carries only what models.dev lacks or gets wrong. Its
initial contents, in full:

- **Base URLs models.dev omits** because the provider has a vendor SDK:
  `openai` (`https://api.openai.com/v1`), `anthropic`
  (`https://api.anthropic.com/v1`), `google`
  (`https://generativelanguage.googleapis.com/v1beta`), `groq`
  (`https://api.groq.com/openai/v1`), `xai` (`https://api.x.ai/v1`),
  `cerebras` (`https://api.cerebras.ai/v1`), `mistral`
  (`https://api.mistral.ai/v1`), `togetherai` (`https://api.together.ai/v1`,
  the models.dev id is `togetherai`), `azure` and
  `azure-cognitive-services` (§9.2), `amazon-bedrock` (§9.3), `google-vertex`
  and `google-vertex-anthropic` (§9.4).
- **Provider surfaces**: `anthropic`, `amazon-bedrock`,
  `google-vertex-anthropic`, `anthropic-compatible` → `anthropic`; `openai`,
  `openai-codex`, `azure`, `openai-compatible` → `openai`; `google`,
  `google-vertex`, `google-compatible` → `google`. Everything else is
  `generic` unless a row's family says otherwise.
- **`default_order`** = `anthropic, openai-codex, openai, google, groq, zai,
  deepseek, openrouter`; `default_model` and `cheap_model` for each of
  those.
- **OpenAI** (`openai`): `fields` on: `store`, `prompt_cache_key`, `include`,
  `truncation`, `safety_identifier`, `service_tier`, `previous_response_id`,
  `conversation`, `max_tool_calls`, `background`, `metadata`, `stop`;
  `prompt_cache_retention` only on the `gpt-5*` and `gpt-4.1*` rows (replacing
  `openAIModelSupports24hPromptCache`). `MaxTokensField =
  "max_completion_tokens"` (so `base = "openai"` gateways over chat inherit
  the spelling OpenAI reasoning models require). `StrictTools = true`,
  `WebSearch = true`, `ReasoningSummary = "auto"` with `"detailed"` on gpt-5+
  rows, `ImageDetail = "original"` on gpt-5.4/5.5/6 rows,
  `CountTokensEndpoint = /responses/input_tokens`, and `headers = {
  OpenAI-Organization = "$OPENAI_ORG_ID", OpenAI-Project =
  "$OPENAI_PROJECT_ID" }` (an unset `$VAR` in `headers` drops the header,
  §10).
- **Azure** (`azure`, `azure-cognitive-services`): `fields` on: `store`,
  `include`, `previous_response_id`, `metadata` (Microsoft's v1 changelog
  lists encrypted reasoning items, and the Responses how-to documents `store`
  and `previous_response_id`). **xAI**: `store`, `include` (what Pi enables
  for it). Everything else on those two stays at baseline.
- **`openai-codex`**: `base = "openai"`, `inherit_models = false`,
  `Transport.Auth = "oauth-openai-codex"`, `BaseURL =
  https://chatgpt.com/backend-api/codex`, `ModelsEndpoint =
  /models?client_version=0.0.0`, `CountTokensEndpoint = "-"`. `fields` off
  for everything the backend rejects: `temperature`, `top_p`,
  `max_output_tokens`, `stop`, `previous_response_id`, `conversation`,
  `service_tier`, `safety_identifier`, `prompt_cache_retention`,
  `truncation`, `max_tool_calls`, `background`. Rows, all with `alias_of`
  pointing at the matching `openai/…` row for facts: `gpt-5.6` with `wire_id
  = "gpt-5.6-sol"` (the backend rejects the bare slug,
  `responses.go:1030-1036`), `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`,
  each with `ResponsesLite = true` and `body = { "reasoning.context" =
  "all_turns", "text.verbosity" = "low", parallel_tool_calls = false }`
  (today's lite shaping, `responses.go:74,151-186`). Only these rows are
  valid on this transport: an unknown id is an error, not a warning (§7.3).
  The transport's own behaviors are enumerated in §9.5.
- **Anthropic**: `api_key_env` as today, `CacheTTL`, `WebSearch = true`, and
  the corrections to upstream: `claude-sonnet-4-5` and
  `claude-sonnet-4-5-20250929` pinned to `context_window = 200000`
  (models.dev says 1000000; Anthropic's context-window page, fetched
  2026-08-28, lists 1M only for Opus 4.6+, Sonnet 4.6+, Sonnet 5, Fable 5,
  Mythos, says Sonnet 4.5 is 200k, and says "no beta header" for the 1M
  models; models.dev already has Opus 4.5 and Haiku 4.5 at 200000; the same
  Sonnet 4.5 rows on gateways are left to those gateways' live listings).
  Two `[1m]` rows, `claude-sonnet-4-5[1m]` and
  `claude-sonnet-4-5-20250929[1m]`, each with `alias_of` and `wire_id` naming
  the base row, `context_window = 1000000`, and `headers = { anthropic-beta =
  "context-1m-2025-08-07" }`; these are the only 4.x rows upstream still
  carries (Sonnet 4, Opus 4, and Opus 4.1 are gone from models.dev), and the
  live test in §13 verifies the beta still works before the rows ship.
  `ThinkingShape = "budget+effort"` on the `claude-opus-4-5*` rows
  (models.dev lists `effort` + `budget_tokens` for both Opus 4.5 and Opus
  4.6, and only 4.6 takes the adaptive body); every other Claude row gets
  the converter default of §6.1. `ThinkingDisplay = "summarized"` on Claude 5
  rows. `Fields["temperature"] = false` on Claude 5 rows is already true
  from models.dev `temperature: false`; listed here only if upstream
  regresses. The refresh script lists every pinned row whose upstream value
  changed so pins get re-examined.
- **Google** (`google`, `google-vertex`): `api_key_env = [GEMINI_API_KEY,
  GOOGLE_API_KEY]` on `google`; `WebSearch = true` on both;
  `MultimodalToolResults = true` on `gemini-3*` rows.
- **Kimi**: `moonshotai` and `moonshotai-cn` (openai-chat): `Fields` off for
  `temperature`/`top_p`/`frequency_penalty`/`presence_penalty`,
  `StructuredOutput = false`, `ToolChoiceForcing = false`.
  `kimi-for-coding` (anthropic): `Headers["User-Agent"] = "claude-cli/2.1.177
  (external, cli)"`.
- **z.ai** (`zai`, `zai-coding-plan`, `zhipuai`, `zhipuai-coding-plan`):
  `ThinkingFormat = "zai"`, `StripEmptyContent`, `MaxStopSequences = 1`,
  `FinishReasonMap = { sensitive = "content_filter", network_error = "error"
  }`, `StructuredOutput = false`, `ToolChoiceForcing = false`,
  `Fields["developer_role"] = false`.
- **DeepSeek**: `ThinkingFormat = "deepseek"`, `EmptyReasoningContent`.
- **OpenRouter**: `ThinkingFormat = "openrouter"`, `ToolChoiceForcing =
  false`, `CacheControl = "anthropic"` on `anthropic/*` rows,
  `SessionAffinityHeaders`; on the `minimax/*` rows `reasoning_controls =
  ["toggle"]`, `thinking_always_on = true`, `reasoning_field =
  "reasoning_details"` (today's unconditional `reasoning: {enabled: true}`
  plus the `reasoning_details` replay path, `profile.go:1283-1292`,
  `openaicompat/request.go:22-33,381-411`).
- **Bedrock** (`amazon-bedrock`): §9.3, plus two rows Anthropic's model table
  has and models.dev lacks: `anthropic.claude-haiku-4-5` with `alias_of =
  "anthropic.claude-haiku-4-5-20251001-v1:0"`, and
  `anthropic.claude-mythos-preview` with `alias_of =
  "anthropic/claude-mythos-preview"`-style facts from the `anthropic`
  provider when that row exists upstream, else provider-level caps.
- **Ollama** (`ollama`, not in models.dev): `Protocol = openai-chat`,
  `BaseURL = {OLLAMA_HOST}/v1` with `VarsEnv = { OLLAMA_HOST = OLLAMA_HOST }`,
  a default of `http://localhost:11434`, and `HostRule = ollama-host` (§9.1),
  `Auth = none`, no models (live only), no `DefaultModel`.
- **Pseudo-providers** `openai-compatible`, `anthropic-compatible`,
  `google-compatible`: protocol and surface only, no base URL, no models,
  `Hidden` (usable only as a `base`).

Everything in `evener_model_catalog_overrides.json` today either exists
upstream now (`gpt-5.6*`, `claude-*-5`, `deepseek-v4-*`) or becomes a row in
this file. The 2026-07-02 "silently deleted a model on refresh" failure
cannot recur: overlay rows materialize models when the upstream row is
missing, and the refresh script reports overlay rows that upstream now
covers.

### 6.3 User config

`providers.toml`, §10.

### 6.4 Refresh and cache

- `registry.Load(opts)` reads the embedded snapshot, then, if
  `<state-root>/catalog/models.dev.json` exists and its fetched-at is newer,
  uses that instead.
- If the cache is absent or older than 24h and `opts.Offline` is false, a
  goroutine fetches `https://models.dev/api.json` with `If-None-Match`,
  validates it with the converter (must parse; must contain ≥ 90% of the
  provider count and ≥ 90% of the model count of the embedded snapshot), and
  writes it atomically (temp + rename). The running process keeps its
  already-loaded registry; the refresh takes effect on the next load. A
  failed refresh logs one line and keeps the cache.
- `EVENER_OFFLINE=1` sets `opts.Offline`. `cmdutil` test helpers set it, the
  registry fetcher is an injected `func(ctx, etag) (…)`, and the default test
  state root is a temp dir, so default tests cannot reach the network.
  Nothing else in `registry.Load` or `Resolve` performs I/O beyond reading
  local files and environment variables.
- `make refresh-model-catalog` replaces the embedded snapshot (curl → gzip),
  writes `models.dev.meta.json`, runs the converter tests, and prints:
  providers added/removed, models added/removed, overlay rows upstream now
  covers, and overlay pins whose upstream value changed.
- The embedded file is raw upstream JSON, gzipped (4.4 MB → 439 KB).
  Parsing costs about 30 ms once, lazily, as today.

## 7. Resolution

`(*Registry).Resolve(ref string) (Resolved, error)` is the single lookup
path. It replaces `LookupModelInfo`, `resolveOpenAICompatCatalogModel`,
`fillFromCatalog`, `GetPrice`'s prefix scan, `ResolveLiveModelInfo`,
`cmdutil.ParseModelRef` + `SelectProfile`, and the profile constructors.
`(*Registry).FindModel(id string) []Ref` answers the other question the
agent asks ("which instances serve this model id?") for plugin-agent model
declarations (§7.4).

### 7.1 Reference syntax

`instance/model`, split on the **first** slash; the model half may contain
slashes (`groq/openai/gpt-oss-120b`, `openrouter/anthropic/claude-opus-5`).
A bare model id with no slash is resolved against the default instance.
There is no suffix handling: `claude-sonnet-4-5[1m]` is an ordinary alias
row in the curated overlay whose `wire_id` names the base model.

### 7.2 Model lookup order

Within the merged provider record, in order; the first hit wins and is
recorded in `Provenance["model"]`:

1. exact id in the instance's own `models` (layer 4)
2. exact id in the provider's merged `models`
3. `AliasOf` chain (one hop) for facts and caps; the row's own `WireID` still
   applies
4. cloud region prefix stripped: `us.`, `eu.`, `apac.`, `au.`, `jp.`,
   `global.` (Bedrock inference profiles)
5. dated family: `-YYYYMMDD`, `-YYYYMMDD-v<N>`, `-YYYYMMDD-v<N>:<M>`, or
   `@YYYYMMDD` suffix removed
6. live listing (layer 5), which establishes existence with provider-level
   caps only

No substring or longest-prefix matching anywhere.

### 7.3 Unknown models

A model id that matches nothing is still resolvable: `Resolved.Model` is
synthesized from provider-level caps and the provider's `Surface`,
`Warnings` carries `model not in catalog`, and the wire id is the reference
verbatim. Context window is unset, which the agent treats as "unknown" (no
compaction budget until the live listing or a user row supplies one); the
anthropic protocol's required `max_tokens` falls back to 32000 as today
(`anthropic/request.go:16-19`). The hub shows the warning next to the model.
This is how a model released this morning works before the cache refreshes.
The one exception is the `oauth-openai-codex` transport, whose backend
enforces a model allowlist: an unknown id there is a resolve error.

### 7.4 What the agent reads from `Resolved`

`agent/provider.Profile` becomes a thin wrapper: `Resolved` plus tool
definitions, doc files, and the per-session overrides that exist today
(`WithCommunicateOutputSchema`, `WithAllowedDecisions`, `WithContextWindow`
for live windows, `WithCheapModel` for `--fast-cheap-model`, which keeps its
`provider/model` form and may cross instances). The constructor switch, the
per-type default windows, knowledge cutoffs, effort ladders, and the
`CheapModel()` switch are deleted; those values come from `Caps` and the
curated `DefaultModel`/`CheapModel`.

Every branch that keys on `BehaviorTag()` or the LiteLLM catalog today moves
to exactly one of five keys:

| Today | New key | Why |
|---|---|---|
| doc files, tool set (`openAICodexCapabilities` / `anthropicStyleCapabilities` / `geminiStyleCapabilities`), tool name map (`profile.go:838-942`), and `reapplyProviderSpecificTools(oldTag, newTag)` on model switch (`session.go:1099-1141`) | `Surface` | trained-for vendor conventions; `generic` = today's openai-compat profile (`AGENTS.md`, codex tool set, no name map) |
| prompt sections `<name>.provider-<tag>` (`agent/section_resolver.go:80`, `session_prompts.go:321`) | `Surface` | the only file is `tools.provider-openai_append`, which should not reach a Groq Llama over Responses |
| the registered `web_search` function tool (`session_tool_registry.go:289`, executor in `agent/tool_web_search.go`) | `Protocol == google && Caps.WebSearch` | the google builder cannot combine `google_search` with function declarations (`google/request.go:67-72`), so evener runs a separate Gemini call; a protocol limitation, and Vertex Gemini gets it too |
| `model_fallbacks` cross-provider refusal (`session_init.go:1337`) | `Surface` equality | the refusal exists because surfaces differ; same-surface fallbacks across instances are allowed |
| `unrepresentableContentKinds` (`session_set_model.go:28`) | `Protocol` | it is about the request builder |
| `modelSwitchVisible` / `VisibleLiveModel` (`session_init.go:490`, `session_set_model.go:119-125`) | the live-layer rule of §5 | the openrouter-only tool gating is now "live `Tools = false` hides the row" for every provider |
| sandbox net-off web egress allowlist (`sandbox/provider_web.go:15`) | `ProviderID` | vendor identity; the `gemini` key becomes `google` |
| subagent target comparison (`subagent_model_selection.go:183`) | `Instance` | routing identity |
| plugin-agent `model:` declarations (`resolvePluginAgentCatalogRef`, `subagent_model_selection.go:155-190`, today via `catalog.ResolveAlias`) | `Registry.FindModel` | `instance/model` resolves directly; a bare id resolves when exactly one instance serves it, is *ambiguous* when several do, *unavailable* when none, preserving today's fallback-with-warning |
| `ProviderOptions` map key and the API-log tag (`session_model_call.go:248`) | `Protocol` | options are protocol extras (beta headers, safety settings) |
| `openAIPromptCacheSupported` (`session.go:1343`) | `Fields["prompt_cache_key"] && Fields["prompt_cache_retention"]` | rows, not prefixes |
| `Client.BehaviorTagOf` identity fallback for replay scope (`client.go:432`, `session_model_call.go:1171`) | `Instance` + `Protocol`, both recorded on every turn | turns produced by instances no longer configured still carry what the replay needs |

`ClampReasoningEffort` is called in exactly one place, in `Resolve`'s
per-request companion `registry.ShapeRequest(req, resolved)`, which runs in
this order: clear reasoning controls when `Caps.Reasoning == false`; clamp
the effort to `EffortValues`; apply `MaxOutputTokens` when the request has
none; apply the Responses-continuation store override when a continuation
is planned (§7.5); drop request-level sampling parameters whose `Fields`
entry is false. Adapters receive a request that is already legal for the
endpoint; the body-level prune (§8.2) is the second, mechanical pass.

### 7.5 Responses continuation

Today the plan comes from per-instance adapter state
(`openai.Adapter.PlanResponsesContinuation`, `openai/adapter.go:365-437`) and
the session gates on `EndpointFamily`, `ContinuationStorageAllowed`, and
`CanFallbackToChat` (`agent/session_model_call.go:301-360`), matching
anchors on both a request fingerprint and a storage-scope fingerprint
(`:416-421`). Under this design `llm.Client` computes the plan from
`Resolved` plus the built body:

- continuation is available iff `Protocol == openai-responses` and
  `Fields["previous_response_id"]` and `Fields["store"]` are both true after
  layering (so Groq, gateways with `store = false`, and the Codex transport
  get no continuation, and OpenAI proper and Azure keep it);
- the **request fingerprint** is what it is today: the client calls the
  protocol's `BuildBody(req, res)` (the same function `Complete`/`Stream`
  use) and hashes the body minus `input`, `previous_response_id`,
  `conversation`, and `store` (`responses_continuation_fingerprint.go`);
- the **storage-scope fingerprint** hashes the instance name, resolved base
  URL, endpoint path, the credential fingerprint the store already computes,
  the `OpenAI-Organization`/`OpenAI-Project` header values when present, and
  the OAuth account/workspace claims on the Codex transport; the storage
  policy label comes from the built body's `store` value as today
  (`adapter.go:425-434`); the `ContinuationHasher` stays on the client, keyed
  by state dir;
- `EndpointFamily` is `codex` when `Transport.Auth == oauth-openai-codex`,
  else `public`; the support registry keeps its per-family defaults
  (`llm/responses_continuation.go:232-244`);
- `CanFallbackToChat` and `FullHistoryFallbackMessages` are deleted with the
  fallback. A rejected anchor (`ErrorCode() == "previous_response_not_found"`,
  `session_model_call.go:1027`) is handled by the session as today.

## 8. Protocol adapters

### 8.1 Interface

```go
type Protocol interface {
    ID() string
    PrunablePaths() []string   // the optional paths this protocol can emit that no cap governs (§8.2)
    BuildBody(req llm.Request, res registry.Resolved) (map[string]any, error)
    Complete(ctx, req llm.Request, res registry.Resolved) (llm.Response, error)
    Stream(ctx, req llm.Request, res registry.Resolved) (llm.Stream, error)
    ListModels(ctx, res registry.Resolved) ([]registry.Model, error)   // ErrUnsupported when ModelsEndpoint is "-"
    CountTokens(ctx, req llm.Request, res registry.Resolved) (int, error) // ErrUnsupported when CountTokensEndpoint is "-"
}
```

One instance of each protocol is registered at init; adapters hold no
per-provider state. Base URL, headers, auth, and caps arrive in `Resolved`.
Today there are three Chat Completions sources
(`llm/providers/openai/chatcompletions.go`, used as the openai adapter's
fallback; `llm/providers/openaicompat`, which owns the quirks; and the shared
helper package `llm/providers/internal/openaichat` both import); they
consolidate into the single `chatcompletions` protocol package, and
`llm/providers/openai` becomes `responses` with only the Responses
implementation. Package names avoid the existing `internal/openaichat`.

`llm.Client` becomes: resolve `req.Provider/req.Model` → look up the protocol
by `res.Protocol` → call it. The `nameToTag` map,
`RegisterInstanceAdapterFactory`, `RegisterEnvAdapterFactory`, `NewFromEnv`,
`NewFromProviders`, the `providerfwd` forwarding packages,
`llm.ErrorClassFallback`/`isEndpointFallbackSignal` (`llm/classify.go:119`)
and its consumers in `contextmgr` and `session_init.go:1379`, and the
`ModelCompatibilityValidator` interface are deleted. Middleware, API attempt
logging, and the provider-name stamping on responses/errors/streams stay.
Callers that list models or count tokens (`launchcheck.go:170,229`,
`SetModel`'s membership preflight at `agent/session.go:1120`,
`modelavailability.Capture`, the hub's `fetchLiveModels`, `providers list
--check`, `probe`) treat `ErrUnsupported` as "registry-only listing" /
"estimate-only counting", never as a failure.

Transport authentication is a small interface implemented once per scheme:

```go
type Authenticator interface {
    Apply(ctx, *http.Request, Credential) error // sets headers
}
```

`bearer`, `header`, and `none` are trivial. `oauth-openai-codex` is the
existing `auth/<instance>.json` flow moved behind this interface, with its
token-refresh state cached per instance. `gcp-adc` sends a bearer token from
application-default credentials (`golang.org/x/oauth2/google`,
`FindDefaultCredentials`, called at first request, never at load), cached
per instance and refreshed by the token source. Nothing else is needed for
Azure, Bedrock, or Vertex (§9).

### 8.2 The pruner and per-protocol baselines

Body assembly runs in a fixed order:

1. **Build** from the request and the caps. Every cap that changes
   structure acts here: `StrictTools` decides whether `strict: true` is
   emitted **and** whether `strictifyJSONSchema` runs (`responses.go:888`
   rewrites every schema to `additionalProperties: false` + all-required;
   pruning `strict` afterwards would leave the mutated schema);
   `ReasoningSummary`, `ImageDetail`, `ResponsesLite`, `MaxTokensField`,
   `ThinkingShape`/`ThinkingFormat`/`ReasoningControls`, `CacheControl`,
   `StructuredOutput` (false downgrades `json_schema` to `json_object`) all
   act here.
2. **Prune** by `Fields`, a denylist over an enumerated set. Each protocol
   package declares `PrunablePaths()`: every optional JSON path its builder
   can emit **that no cap governs**. The registry seeds `Caps.Fields` with
   the protocol baseline below; layers overlay it; `registry.Prune(body,
   res.Caps.Fields)` deletes each prunable path whose flag is false and
   records the deleted paths on the API attempt log entry as
   `pruned_fields`. A `fields` key in the overlay or `providers.toml` that is
   not in the row's resolved-protocol set is a load error (typo guard); keys
   inherited from a `base` on another protocol are ignored.
3. **Body constants** from `Transport.Body` are set last, creating parent
   objects as needed, so they survive the prune and never depend on build
   state.

Paths outside the prunable set are always sent (`model` unless the
endpoint template contains `{model}`, `messages`/`input`, `tools`, `stream`,
`tool_choice`, `response_format`/`text.format`, `instructions`/`system`, and
every cap-governed path: `input[].content[].detail`, `input[].phase`,
`reasoning.effort`/`reasoning_effort`, `reasoning.summary`, `thinking`,
`output_config`, `generationConfig.thinkingConfig`, `cache_control`).

Prunable sets and baselines (before any layer):

| Protocol | baseline `true` | baseline `false` |
|---|---|---|
| `openai-chat` | `temperature`, `top_p`, `stop`, `stream_options`, `max_tokens`* | `store`, `frequency_penalty`, `presence_penalty`, `developer_role`, `parallel_tool_calls`, `prompt_cache_key`, `prompt_cache_retention`, `service_tier`, `metadata`, `logprobs`, `n`, `seed`, `user` |
| `openai-responses` | `temperature`, `top_p`, `max_output_tokens` | `stop`, `store`, `include`, `truncation`, `safety_identifier`, `service_tier`, `prompt_cache_key`, `prompt_cache_retention`, `previous_response_id`, `conversation`, `metadata`, `max_tool_calls`, `background`, `parallel_tool_calls`, `text.verbosity`, `reasoning.context` |
| `anthropic` | `temperature`, `top_p`, `stop_sequences`, `metadata`, `max_tokens` | `service_tier`, `fallbacks`, `container` |
| `google` | `generationConfig.temperature`, `generationConfig.topP`, `generationConfig.stopSequences`, `toolConfig`, `safetySettings` | `cachedContent`, `labels` |

\* `max_tokens` here means whichever spelling `MaxTokensField` selects,
`max_tokens` by default (the compatible-server default; the `openai` overlay
pins `max_completion_tokens`). The max-tokens path is prunable because the
Codex backend rejects it; `ShapeRequest` still fills the request-level
value, and the prune removes it on transports that say so. `developer_role`
is a pseudo-path: false means the system prompt is sent as `system`, true as
`developer`. `parallel_tool_calls` is off because omitting it yields OpenAI's
default (parallel on) while sending it 400s on several compatible servers;
the Codex lite rows set it to `false` through `Transport.Body`. `stop` is
off on Responses because Groq's Responses documentation does not list it and
the agent never sets stop sequences; the `openai` overlay turns it on. When
`store` is enabled the builder sends `store: false` unless a continuation is
planned (today's privacy default, `responses.go:34`); when it is disabled
nothing is sent and the endpoint's own default applies. `reasoning.summary`
is omitted at the `ReasoningSummary = none` baseline and sent as `auto` or
`detailed` only where the overlay says so.

The consequence for Groq: the baseline Responses body sent is `model`,
`instructions`, `input`, `tools`, `tool_choice`, `temperature`, `top_p`,
`max_output_tokens`, `stream`, `reasoning.effort`, `text.format`, with
`strict` absent, schemas unmodified, and no `reasoning.summary`, which
contains none of the fields Groq documents as unsupported
(`previous_response_id`, `store`, `truncation`, `include`,
`safety_identifier`, `prompt_cache_key`, `prompt`). So `protocol =
"openai-responses"` on a `groq` instance works with no groq-specific entry
anywhere; `openai-chat` remains groq's registry default (its `npm` is
`@ai-sdk/groq`) and Responses is opt-in per instance or via the probe
(§11.2). Groq is in the curated overlay only for its base URL.

### 8.3 Model-prefix branches become caps

Each of the prefix checks in §2 becomes a model-level cap in the curated
overlay, set on the rows it applies to:

| Today | Cap |
|---|---|
| `responsesLiteModel` (`gpt-5.6`) | `ResponsesLite` + `body` constants on the `openai-codex` gpt-5.6 rows (§6.2) |
| `defaultImageDetail` (`gpt-5.4/5.5/gpt-6`) | `ImageDetail = "original"` on those rows; baseline `"high"` |
| `reasoningSummaryLevel` (`gpt-5/gpt-6`) | `ReasoningSummary = "detailed"` on those rows, `"auto"` on the rest of `openai`; baseline `none` |
| `isClaude5OrNewer` | `Fields["temperature"]=false` (from models.dev), `ThinkingShape = "adaptive"` + `ThinkingAlwaysOn` (converter default), `ThinkingDisplay = "summarized"` on Claude 5 rows |
| `adaptiveThinking` for Opus/Sonnet 4.6+ | converter default (§6.1): adaptive, always on |
| `geminiSupportsMultimodalFunctionResponse` (`gemini-3`) | `MultimodalToolResults = true` on gemini-3 rows |
| `[1m]` synthesis for `claude-opus-4-`/`claude-sonnet-4-` | curated `[1m]` alias rows (§6.2) |
| `minimax/` under openrouter | `reasoning_controls = ["toggle"]`, `thinking_always_on`, `reasoning_field = "reasoning_details"` on those rows |
| `codexModelVariants` | `wire_id` + rows under `openai-codex` (§6.2) |
| `openAIModelSupports24hPromptCache` | `Fields["prompt_cache_retention"]` on gpt-5/gpt-4.1 rows |

A new model generation means adding rows to the overlay, not editing an
adapter.

### 8.4 Reasoning

`Reasoning` gates everything: `false` empties `ReasoningControls` and
`EffortValues` (so the hub shows no effort chip), sends no reasoning field
on any protocol, drops replayed thinking from history, and strips reasoning
keys from `ProviderOptions` (today's `ReasoningOff` transforms,
`openaicompat/compat.go:189`, `request.go:113,230,353,374`). When `true`,
`ReasoningControls` says what the model accepts, `ThinkingShape` says how the
anthropic protocol spells it, and `ThinkingFormat` says how the openai-chat
protocol spells it. `Fields` plays no part in reasoning.

- **openai-chat**: the nine dialects that exist today (`openai`,
  `openrouter`, `deepseek`, `together`, `zai`, `qwen`, `qwen-chat-template`,
  `chat-template`, `string-thinking`) are kept verbatim. With `effort ∈
  ReasoningControls` and an effort set, the dialect's effort field is sent
  (`reasoning_effort`, `reasoning.effort`, `thinking: <string>`, …). With
  `toggle` (and no `effort`) the dialect's enable object is sent when an
  effort is requested and the effort value itself is dropped: openrouter →
  `reasoning: {enabled: true}`, zai and deepseek → `thinking: {type:
  enabled}`, qwen → `enable_thinking: true`, qwen-chat-template and
  chat-template → their kwargs, openai → nothing (Chat Completions has no
  toggle). `ThinkingAlwaysOn` sends that enable object even when no effort
  is requested (the OpenRouter MiniMax rows).
- **openai-responses**: `reasoning.effort` when set and `effort ∈
  ReasoningControls`; `reasoning.summary` from `ReasoningSummary`.
- **anthropic**: `ThinkingShape` picks one of three bodies the builder
  already knows (`anthropic/request.go:131-176`): `adaptive` → `thinking:
  {type: adaptive}` plus `display` from `ThinkingDisplay`, sent whenever
  `ThinkingAlwaysOn` (the converter default for adaptive rows) or an effort
  is set, plus `output_config.effort` when an effort is set; `budget` →
  `thinking: {type: enabled, budget_tokens}` from the existing effort→budget
  table, only when an effort is set; `budget+effort` (Opus 4.5) → both.
  Unset shape → no thinking object.
- **google**: effort → `thinkingConfig` as today; `none` sends no
  `thinkingConfig`.

The `none` effort clears the control on every protocol; nothing is ever sent
to force thinking off. A `thinking_levels` map (today's per-model level →
wire-string table) is not needed: a wire-spelled `EffortValues` ladder under
`ClampReasoningEffort` reproduces it, because below-range requests raise to
the lowest supported value and the top tier resolves to the model's own
spelling.

Replay of prior thinking on the chat protocol writes back to
`ReasoningField`: `reasoning_content`, `reasoning`, or `reasoning_text` as a
string field; `reasoning_details` as OpenRouter's array of `reasoning.text`
items (today's `request.go:381-411` path). The value comes from models.dev
`interleaved.field` when present, else from the field the text arrived on
(today's `Signature` mechanism), else `reasoning_content`.

## 9. Transports and the cloud providers

The transport axis exists so Azure, Bedrock, and Vertex are data. None of
them needs request signing or non-SSE framing.

### 9.1 URL assembly

`Transport.BaseURL` may contain `{VAR}` placeholders. `Endpoint`,
`StreamEndpoint`, `ModelsEndpoint`, and `CountTokensEndpoint` are path
templates that may contain `{model}` and `{VAR}`; empty means the protocol
default (§6.1), `-` means unsupported. Variables resolve from
`Transport.Vars` (instance config), then `VarsEnv`, then a resolve error
naming the variable and the instance. `HostRule` names one of two
normalizers applied to the assembled base URL, the only host-aware code in
the system: `vertex-location` (§9.4) and `ollama-host`, which is today's
`envvars.NormalizeOllamaHost` (`localhost`, `0.0.0.0:11434`, `::1`, bare
hosts, URLs with or without `/v1`; the repo already fixed the
`OLLAMA_HOST=localhost` → "unsupported protocol scheme" bug once). If the
final completion path contains `{model}`, `model` is not sent in the body.

### 9.2 Azure OpenAI and Azure Foundry (verified 2026-08-28, Microsoft Learn "v1 API")

- `base_url = https://{AZURE_RESOURCE_NAME}.openai.azure.com/openai/v1`
  (`services.ai.azure.com/openai/v1` also accepted upstream).
- `auth = header`, `auth_header = api-key`; Entra bearer tokens work through
  `auth = bearer` with the token in `api_key`.
- No `api-version` parameter on v1.
- Both `/responses` and `/chat/completions` exist; `model` in the body is
  the **deployment name**. A deployment row's own id is its wire id, and
  `alias_of` pulls the catalog facts:

```toml
[providers.azure]
api_key = "$AZURE_API_KEY"
[providers.azure.vars]
AZURE_RESOURCE_NAME = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"          # facts and caps from the catalog row; wire id stays gpt55-prod
```

- Claude on Azure Foundry: models.dev already marks those rows `npm:
  @ai-sdk/anthropic` with `api:
  https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`, so
  they resolve to the anthropic protocol at that base URL with the same
  `api-key` header (Anthropic's Foundry page accepts `api-key` or
  `x-api-key` plus `anthropic-version`). Deployment names apply here too. One
  instance, two protocols.

### 9.3 Amazon Bedrock (verified 2026-08-28, Anthropic "Claude in Amazon Bedrock (Opus 4.7 and later)" and AWS Chat Completions docs)

Anthropic serves Claude on Bedrock through the Messages API:
`https://bedrock-mantle.{region}.api.aws/anthropic/v1/messages`, "standard
SSE streaming and the same request body shape as Anthropic's first-party
API", `model` in the body with an `anthropic.` prefix
(`anthropic.claude-opus-5`), `anthropic-version: 2023-06-01`. Bearer tokens
go in `x-api-key`; SigV4 (signing name `bedrock-mantle`) requires Anthropic's
dedicated client and is not supported here. So:

- `amazon-bedrock`: `anthropic` protocol, `base_url =
  https://bedrock-mantle.{AWS_REGION}.api.aws/anthropic/v1`, `auth = header`,
  `auth_header = x-api-key`, `api_key_env = [AWS_BEARER_TOKEN_BEDROCK]`,
  `ModelsEndpoint = "-"`, `CountTokensEndpoint = "-"` (estimate-only; exact
  counting is tracked in
  [#565](https://github.com/prime-radiant-inc/evener/issues/565)),
  provider-level `StructuredOutput = false` and `WebSearch = false` (both
  listed as unsupported on that page; models.dev marks the Sonnet 5, Haiku
  4.5, Sonnet 4.6, Opus 4.5, and Opus 4.6 rows `structured_output: true` at
  the row level, which the layer-3 provider-level pin beats under §4.1's
  merge order).
- **Global vs regional routing** is expressed in the model id, not the
  host: `bedrock-mantle` hosts are regional (AWS lists fourteen,
  `bedrock-mantle.<region>.api.aws`), and AWS's cross-Region inference
  routes a request whose model is a `global.`, `us.`, `eu.`, `jp.`, or
  `au.` inference-profile id across the profile's regions. models.dev lists
  those rows (`global.anthropic.claude-opus-5`, `us.anthropic.claude-fable-5`,
  …) alongside the in-region ids (`anthropic.claude-opus-5`), so
  `bedrock/global.anthropic.claude-opus-5` resolves to its own row and sends
  that id verbatim; §7.2's prefix strip only serves ids the catalog lacks.
  Jesse verified the global profile live on 2026-08-28; no `Warnings` entry
  is attached to profile ids.
- **OpenAI-shaped models**: models.dev marks them `@ai-sdk/amazon-bedrock/mantle`
  with `api: https://bedrock-mantle.${AWS_REGION}.api.aws/v1` (gpt-oss) or
  `…/openai/v1` (gpt-5.x, grok) and `shape: responses`; the converter turns
  that into a model-level transport with bearer auth from the same
  `AWS_BEARER_TOKEN_BEDROCK` (AWS documents the bearer path for both the
  `bedrock-mantle` and `bedrock-runtime` OpenAI-compatible endpoints).
- Catalog ids: models.dev also lists legacy ARN-style rows
  (`us.anthropic.claude-sonnet-4-5-20250929-v1:0`); they resolve for
  metadata, but the endpoint above only serves the models on Anthropic's
  table (Fable 5, Opus 5/4.8/4.7, Sonnet 5, Haiku 4.5, Mythos Preview), so a
  request for an older id fails at the provider with its own message. The
  two table ids models.dev lacks are overlay rows (§6.2).

Claude Opus 4.6 and earlier on Bedrock use the legacy `InvokeModel` path
(ARN ids, AWS event-stream framing, SigV4, `anthropic_version:
bedrock-2023-05-31`, betas as a body array, `stream` and `model` stripped).
That is a fifth transport with its own framing and signing; it is out of
scope and would be added only if someone needs those model versions.

### 9.4 Google Vertex (verified 2026-08-28, Anthropic "Claude on Google Cloud")

- Host by location: `global` → `https://aiplatform.googleapis.com`; `us` /
  `eu` → `https://aiplatform.{loc}.rep.googleapis.com`; anything else →
  `https://{loc}-aiplatform.googleapis.com`. The overlay writes `base_url =
  {GOOGLE_VERTEX_HOST}/v1/projects/{GOOGLE_VERTEX_PROJECT}/locations/{GOOGLE_VERTEX_LOCATION}`
  with `host_rule = vertex-location`, which derives `GOOGLE_VERTEX_HOST` from
  the location. A regional (non-`global`, non-`us`/`eu`) location paired
  with a model newer than Sonnet 4.6 adds a `Warnings` entry (Anthropic:
  "specific regional endpoints support Claude Sonnet 4.6 and earlier").
- `auth = gcp-adc` (§8.1); `api_key_env` is empty. `ModelsEndpoint = "-"`,
  `CountTokensEndpoint = "-"` (Vertex's count-tokens is a separate publisher
  call; estimate-only, exact counting tracked in
  [#565](https://github.com/prime-radiant-inc/evener/issues/565)).
- **Gemini** (`google-vertex`): `google` protocol, `endpoint =
  /publishers/google/models/{model}:generateContent`, `stream_endpoint =
  /publishers/google/models/{model}:streamGenerateContent?alt=sse`.
- **Claude** (`google-vertex-anthropic`, and the thirteen Claude rows under
  `google-vertex` via the cross-protocol rule of §4): `anthropic` protocol,
  `endpoint = /publishers/anthropic/models/{model}:rawPredict`,
  `stream_endpoint = /publishers/anthropic/models/{model}:streamRawPredict`,
  `body.anthropic_version = "vertex-2023-10-16"`, `model` omitted from the
  body because the path contains `{model}`, and the `anthropic-version`
  header not sent (the transport's `Headers` overlay sets it to the empty
  string, which the header merge treats as "remove"). Wire ids follow the
  converter's `@default` rule (§6.1): `claude-opus-5`,
  `claude-sonnet-4-5@20250929`.
- Vertex's OpenAI-compatible MaaS endpoint for Llama/DeepSeek/Kimi rows is
  carried per-model by models.dev (`api: …/endpoints/openapi`, `npm:
  @ai-sdk/openai-compatible`) and needs nothing from us.

### 9.5 The Codex transport (`oauth-openai-codex`)

This is the one transport with behavior beyond auth, all of it existing code
in `llm/providers/openai/adapter.go` that moves behind the transport. Listed
so nothing is lost:

- per-request headers from the request: `session-id`, `thread-id`,
  `x-client-request-id` (`setRequestHeaders`); `ChatGPT-Account-ID` from the
  token claims; `originator` and `User-Agent`;
- `x-openai-internal-codex-responses-lite: true` when `ResponsesLite` is set
  (without it the backend hangs);
- `metadata` is sent as `client_metadata`, merged with `req.ClientMetadata`
  (the session puts the Codex installation id there,
  `session.go:1354-1358`; `responses.go:132-136`). This is the one field
  rename in the design; it lives in the transport, not the protocol;
- `Complete` runs through `Stream` (`requiresStreamingComplete`);
- the model allowlist is the registry's `openai-codex` row set (§6.2, §7.3);
- `evener openai login` defaults `--instance` to `openai-codex`, and the
  hub's OAuth sign-in eligibility keys on `Transport.Auth ==
  oauth-openai-codex`, replacing the "`openai` instance silently becomes
  Codex when `auth/<name>.json` exists" rule (`adapter.go:108-112`,
  `app_auth.go:561-569`, `cmd/evener/openai_login.go:95`).

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
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }   # required: a gateway never inherits OpenAI's key
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

TOML keys map onto the structs in §4 as follows: `base`, `inherit_models`,
`api_key`, `api_key_env`, `headers`, `credential_headers`, `surface`,
`default_model`, `cheap_model` → `Provider`; `base_url`, `host_rule`, `auth`,
`auth_header`, `endpoint`, `stream_endpoint`, `models_endpoint`,
`count_tokens_endpoint`, `vars`, `body` → `Provider.Transport`; `protocol` →
`Provider.Protocol`; every `Caps` field by its snake_case name
(`context_window`, `effort_values`, `reasoning_controls`, `thinking_format`,
`fields`, …) at the instance level → `Provider.Caps`, and inside
`[providers.X.models."<id>"]` → `Model.Caps`, where `alias_of`, `wire_id`,
`protocol`, `surface`, `headers`, and the transport keys are also accepted.

Rules, enforced at load with errors that name the instance and key:

- names are lowercase, no slash, unique; `base` must name a registry id;
  `alias_of` must name an existing row
- `protocol` must be a registered protocol; `surface` one of the four values
- `auth` ∈ `bearer | header | none | gcp-adc | oauth-openai-codex`
- `fields` keys must be in the row's resolved-protocol prunable set (typo
  guard)
- `thinking_format`, `thinking_shape`, `max_tokens_field`, `cache_control`,
  `reasoning_field`, `host_rule` are validated against their vocabularies;
  `reasoning_controls` entries must be `effort`, `budget_tokens`, or `toggle`
- `effort_values` entries non-empty; `"off"` rejected
- `$ENV` expansion in `api_key`, `credential_headers`, and `vars` uses
  today's `$NAME` / `${NAME}` / `$$` rules and happens at resolve time, so one
  instance's missing variable never blocks another; an unset variable there
  is a resolve error. In `headers` an unset variable **drops the header**
  (that is how the optional `OpenAI-Organization`/`OpenAI-Project` headers
  work); an empty-string value removes an inherited header of that name.
- **credential inheritance stops at the endpoint**: an instance whose
  resolved base URL host differs from its base's registry host does not
  inherit the base's `APIKeyEnv` or credentials-store entry; it must set
  `api_key`, `api_key_env`, or `credential_headers`, else resolving it fails
  with "no credential for <instance>" (today's `CredentialTag` returns `""`
  for exactly this shape, `providercfg.go:177-186`, so a gateway never
  receives the vendor key by accident)
- `WriteFile` keeps today's scrub-and-restore so hub rewrites never persist a
  credential the user did not author
- when both `auth = bearer` and a `credential_headers.Authorization` are
  present, the header wins and no bearer is derived from the key

`type`, `api_style`, `quirks`, `[instances.*]`, and `compat` are gone. A file
that still uses them fails to load with a message pointing at this document.

Credentials keep the existing `internal/credentials.Store` semantics (file
entry by instance name → env vars by instance name → the provider's env
vars), with one change of direction: the store no longer owns a provider
roster. It is constructed with the registry's `(id → APIKeyEnv)` table, so
`envvars.Providers()`, `envvars.APIKeyVars`, and `envvars.AuthModes` are
deleted along with the seven-provider list; the generic env helpers and
`NormalizeOllamaHost` in `envvars` stay.

## 11. Commands, hub, and wire types

### 11.1 `evener models`

- `list [--provider X] [--all]` — resolved rows with protocol, surface,
  context, output cap, cost, effort ladder, warnings. Hidden and non-text
  rows only with `--all`.
- `inspect <ref>` — the full `Resolved` record with provenance per field,
  the pruned-field list the protocol would apply, and the request skeleton
  (endpoint, auth scheme, headers with secrets masked).
- `refresh [--force]` — fetch models.dev into the cache now; print the diff.

### 11.2 `evener providers`

- `list [--check]` — instances (explicit and implicit), base, protocol,
  endpoint, credential source, live reachability with `--check`.
- `probe <instance> [--write]` — `GET` the models endpoint when supported,
  then a minimal request (`max_output_tokens`/`max_tokens: 16`, the
  smallest value OpenAI's Responses endpoint accepts; one-word prompt; one
  trivial tool with an optional parameter; `text.format` set) against
  `/responses` and `/chat/completions` (OpenAI protocols only), reporting
  which succeed. A 400 whose message names the max-tokens field is
  reported as *inconclusive*, not unsupported. `--write` records the
  protocol that succeeded (when both do, the registry default is kept and
  the report says both work) and any discovered models into
  `providers.toml`. The runtime never probes on its own.
- `add <name> --base X [--base-url …] [--protocol …] [--var K=V]` — writes
  the entry, runs `probe --write` unless `--no-probe`.

### 11.3 Hub and appwire

The hub's instance CRUD (`cmd/evener-hub/app_instances.go`) calls the same
functions, with the implicit-instance semantics of §5.1 (edit and
set-default write a shadowing entry; remove is refused). The appwire types
change shape (`appwire/types.go:2488-2523`): `InstanceEntry` drops `Type` and
`APIStyle` and gains `Base`, `Protocol`, `Surface`, `BaseURL`, `Vars`,
`Auth`, `Implicit`; its existing `IsDefault`, `HasStoredOAuth`,
`StoredEmail`, `CredentialRequired`, `ActiveSource`, and `AuthModes` stay,
with `AuthModes` derived from `Transport.Auth`. `InstanceCreateParams` and
`InstanceEditParams` follow; `InstanceListResponse.AvailableTypes` becomes
`AvailableProviders` (registry ids with display names and `VarsEnv`, so the
add form can render the right variable inputs). `protocol/types.gen.ts` is
regenerated and the frontend instance dialogs and row
(`panes/settings/sections/credentials/`, `InstanceRow.tsx`) are updated;
`make test-web` covers them. The spawn credential gate (`spawn.go:649-665`)
keys on `Transport.Auth == none` instead of `envvars.RequiresNoCredential`.

`model/list` returns `Resolved`-derived rows straight from the registry, so
`enrichModelDescriptors` and `applyInstanceModelOverride` are deleted.

`cmd/llmcall` uses `cmdutil.LoadClient` like everything else.

## 12. Errors

`llm.ClassifyHTTPError(status, headers, body)` extends today's
`errorFromHTTPStatus`/`classifyByMessage` (`llm/errors.go:262-375`), which
already match context-length, content-filter, quota, and not-found messages,
carry `RetryAfter`, and mark `cyber_policy_violation` retryable. It reads the
structured body first (`error.code`, `error.type`, `error.message` for
OpenAI-shaped bodies; `error.type` for Anthropic), then the status, then
message patterns. New or changed rows:

| Signal | Kind |
|---|---|
| 429 or 403 whose body matches the usage-limit codes (`usage_limit_reached`, `insufficient_quota`, Kimi's quota 403; `llm/usagelimit.go`) | `KindQuotaExceeded`, non-retryable, carries the reset time (unchanged from today; listed so it is not lost) |
| other 429; 413 whose message matches `tokens per minute\|TPM\|requests per`; OpenAI `rate_limit_exceeded` | `KindRateLimit` (retryable, honors `retry-after` and `x-ratelimit-reset-*`) |
| 400/413 matching `context length\|maximum context\|too many tokens\|reduce the length` without a rate wording; Anthropic `prompt is too long`; any other 413 | `KindContextLength` (today's 413 default) |
| 400 naming an unrecognized parameter (`Unrecognized request argument\|unknown field\|is not supported\|invalid JSON body`) | `KindInvalidRequest` with `Hint: run evener models inspect <ref> and set fields.<name> = false` |
| 401/403 otherwise, 404 with `model` in the message | as today |

The provider's message is always included verbatim. `ErrorCode()` survives
(the session keys continuation-anchor rejection on it). `BehaviorTag()` on
error types is removed; `Provider()` returns the instance name and a new
`Protocol()` returns the protocol id.

## 13. Testing

- **Converter**: a checked-in 40-provider excerpt of models.dev
  (`llm/registry/testdata/models.dev.sample.json`) covering every `npm`
  in the table, per-model `provider` overrides (including a cross-protocol
  row with no per-model `api`), both `interleaved` shapes, every
  `reasoning_options` combination, `limit.input`, `@default` ids, tiers, a
  hidden provider, a provider with no `api`, and rows with and without
  `family`. Table tests assert the converted records; a fuzz target feeds
  mutated JSON.
- **Merge and provenance**: property tests that a later layer's set field
  always wins regardless of level, that within a layer the row beats the
  provider, that `nil` always inherits, that map layers merge key-wise,
  that `Base` chains resolve models (and stop with `inherit_models =
  false`), that cross-protocol rows do not inherit protocol-specific
  transport fields, that a dangling `alias_of` fails load, and that every
  provenance entry names a real layer.
- **Instances**: implicit-instance derivation from env and store fixtures
  (including gcp-adc from the env var and the well-known file, never the
  metadata server: the test asserts no HTTP client is constructed); default
  selection in all four branches; shadowing by explicit entries; the
  credential-inheritance stop for gateways.
- **Resolution**: golden `Resolved` records (JSON) for a fixed set of
  references: `groq/openai/gpt-oss-120b` (chat and responses),
  `openai/gpt-5.5`, `openai-codex/gpt-5.6` (asserting `WireID =
  gpt-5.6-sol` and the lite body constants),
  `anthropic/claude-sonnet-4-5[1m]` (asserting `WireID`, window, and beta
  header), `anthropic/claude-opus-4-6` with no effort (asserting the
  adaptive object is sent), `anthropic/claude-opus-5`, `azure/gpt55-prod`
  (asserting `WireID = gpt55-prod`), `bedrock/anthropic.claude-opus-5`
  (asserting `StructuredOutput = false` from the provider pin),
  `vertex/claude-opus-5` (asserting the anthropic endpoints, not Gemini's),
  `openrouter/anthropic/claude-opus-5` (asserting `Surface = anthropic`),
  `openrouter/minimax/minimax-m2.7` (asserting the toggle enable object with
  no effort), `anthropic/some-new-model` (unknown, asserting `Surface =
  anthropic` and `max_tokens = 32000`), `local/whatever` (unknown model),
  `ollama/llama3:8b` (live-only, `OLLAMA_HOST=localhost`).
- **Pruner**: every prunable path per protocol, nested paths, array-element
  paths, the `PrunablePaths()` contract (a builder test that every optional
  path a protocol emits is either in its declared set or governed by a
  named cap), unknown-key rejection at load, and build → prune → constants
  ordering (a constant on a pruned parent survives).
- **Wire captures**: the existing per-protocol golden bodies
  (`wire_capture_test.go`) are regenerated from `Resolved` inputs; new cases
  per cloud transport assert endpoint, stream endpoint, auth header name,
  body constants, model omission, and the Codex header set.
- **Continuation**: plan derivation from `Resolved` for OpenAI proper (on),
  Groq Responses (off), the `work` gateway with `store = false` (off), Azure
  (on), and Codex (family `codex`); request-fingerprint stability across
  two builds of the same request.
- **Refresh**: injected fetcher; asserts cache write, ETag round-trip,
  offline short-circuit, both sanity floors, and that no test path
  constructs a real HTTP client.
- **Error classifier**: table of real captured bodies (Groq 400 and 413,
  OpenAI unrecognized-argument 400, Anthropic prompt-too-long, OpenAI
  `insufficient_quota`, ChatGPT `usage_limit_reached`).
- **Config**: every load-error rule in §10 has a failing fixture; the old
  keys produce the pointer-to-doc error.
- **Cross-adapter differential** (`llm/providers/difftest`) is adapted: it
  constructs adapters by struct literal and calls the two-argument interface
  today (`differential_fuzz_test.go:89-92`), so it moves to `Resolved`
  inputs; the openai-compat leg becomes the `chatcompletions` leg.
- **Live** (`EVENER_LIVE_TESTS=1`): the `[1m]` beta on Sonnet 4.5, Groq
  Responses, Bedrock `global.` routing, and one request per cloud transport
  once step 4 lands.

## 14. Implementation order

The repo is a `go.work` workspace, so a deleted `llm` symbol breaks every
module at once. The order below keeps the tree green at every step by
building the new packages beside the old ones and cutting over last.

1. **`llm/registry`** (additive): types, converter, overlay, config loader,
   merge with provenance, instances, `Resolve`, `FindModel`, `ShapeRequest`,
   `Prune`, embedded snapshot, cache/refresh with injected fetcher. `evener
   models list|inspect|refresh` reads it. Nothing else consumes it yet. The
   §13 golden `Resolved` records for `azure/…`, `bedrock/…`, and `vertex/…`
   are written here, so the data model is proven for the cloud providers
   before any cloud call is made. (~2,200 lines + ~1,800 lines of tests +
   data.)
2. **New protocol packages** (additive): `chatcompletions` (consolidating
   `openaicompat`, `openai/chatcompletions.go`, and
   `internal/openaichat`), `responses`, and `Resolved`-driven `anthropic` and
   `google`, each with `BuildBody` and `PrunablePaths`, the authenticators,
   the Codex transport, the error classifier, wire captures and the adapted
   difftest. The old adapters keep running. (~1,500 new lines, mostly
   moved; ~3,000 lines of tests moved or regenerated.)
3. **Cut-over** (one commit series, tree green at its end): `llm.Client`
   routes by protocol and plans continuation from `Resolved` + `BuildBody`;
   `agent/provider.Profile` wraps `Resolved`; the twelve tag and catalog
   branches move per §7.4; hub `model/list`, instance CRUD, appwire types,
   generated TS, and frontend dialogs; `providers probe|add`; `llmcall` on
   `LoadClient`; credentials store takes the registry table; `envvars`
   roster, `cmdutil/seed.go`, `materialize.go`, `providercfg`,
   `model_catalog*.go`, the LiteLLM data, the wrapper packages,
   `openaicompat`, and the old `openai` adapter deleted;
   `docs/llm-providers.md` rewritten around §3–§10. (~net −4,000 lines
   including tests.)
4. **Cloud providers** (later phase, additive): the `gcp-adc`
   authenticator over `golang.org/x/oauth2/google` (~150 lines), plus the
   live-verified coverage of §13 for Azure, Bedrock, and Vertex against the
   overlay entries of §9. Nothing in steps 1–3 is provisional for them: the
   transport fields (`Auth`, `AuthHeader`, `BaseURL`/`Endpoint`/
   `StreamEndpoint` templates with `{VAR}`, `HostRule`, the `-` endpoint
   sentinel, `Body` constants, model-level `Transport` overlays, the
   cross-protocol inheritance rule, `WireID`) and the `Fields` baselines are
   designed for these three from the start.

## 15. Decisions taken

- models.dev is the only upstream. LiteLLM's extra breadth is regional
  Bedrock/Azure/Vertex keys and non-chat modes we do not use; models.dev has
  those providers with per-model transport hints LiteLLM lacks.
- The embedded snapshot is raw upstream JSON. One converter, no generator.
- Protocol selection is explicit. The probe is a tool that writes config.
- One flat `Caps` plus `Fields` as a denylist over an enumerated set of
  paths no cap governs. Not four typed compat shapes.
- Merge precedence: layers in order, row over provider within a layer,
  later layer wins regardless of level; the live layer never overrides the
  user layer.
- Bedrock via Anthropic's Messages endpoint on `bedrock-mantle`, bearer
  token only. No SigV4, no event-stream framing, no AWS SDK dependency.
  Global routing ships now, as `global.` inference-profile model ids on the
  regional host (Jesse verified live, 2026-08-28).
- Surface is a model attribute derived from models.dev `family`, with a
  curated provider-level fallback.
- Adaptive thinking on Claude rows is always on, as today.
- Ollama is never the default because it has no `DefaultModel`; the
  `NonDefaultEligible` interface goes away.
- Vertex and Bedrock token counting is estimate-only; exact counting is
  [#565](https://github.com/prime-radiant-inc/evener/issues/565).
- Azure, Bedrock, and Vertex may land as a later phase (§14 step 4), but the
  data model and transport axis support them from step 1.
- The snapshot embeds all 207 providers (439 KB gzipped); hidden rows cost
  nothing at runtime.

No open questions remain.

## Review log

Revision 2 incorporated the first adversarial review (two reviewers, 23
scored findings, all accepted): implicit instances and default selection
(§5.1); `WireID` defaulting to the row id and `alias_of` as facts-only (§4,
§9.2); the `/v1` base-URL convention (§6.1); `limit.input` (§6.1); Claude
4.5 window pins and `[1m]` rows with `wire_id` (§6.2); automatic hiding of
URL-less providers and the `togetherai` id (§6.1, §6.2);
`ReasoningControls` + `ThinkingShape` (§4.1, §8.4); live listing overriding
advertised facts (§5); OpenAI and Google `WebSearch` (§6.2); `Surface` (§3,
§4, §7.4); the Codex transport's full behavior list and prunable max-tokens
(§6.2, §8.2, §9.5); Vertex `@default` wire ids (§6.1); Bedrock over the
Messages endpoint (§9.3); the pruner as a denylist over `PrunablePaths()`
with build-time caps (§8.2); continuation planning in `llm.Client` (§7.5);
per-transport listing and count-tokens endpoints (§4, §8.1); the additive
implementation order (§14); appwire/frontend/`envvars` scope (§10, §11.3);
`KindQuotaExceeded` (§12).

Revision 3 applied Jesse's rulings on Bedrock global routing (§9.3) and
estimate-only token counting with #565 (§9.3–§9.4), and stated in §14 that
the cloud providers may be a later phase while the data model supports them
from step 1.

Revision 4 incorporated the second adversarial review (two reviewers, 25
scored findings, all accepted): provider-vs-row merge precedence (§4.1);
`toggle` reasoning per dialect and the OpenRouter MiniMax rows as data
(§6.2, §8.4); adaptive thinking always on for every adaptive row (§6.1,
§8.4); the live layer never overriding the user layer, plus `Reasoning` and
`ThinkingAlwaysOn` from live (§5); `Provider.Surface` fallback, the
corrected family rule, and instance-level `surface` (§4, §6.1, §6.2, §10);
the cross-protocol inheritance rule for model-level protocol overrides (§4,
§9.4); `openai-codex` with `inherit_models = false`, cross-provider
`alias_of`, and `wire_id = gpt-5.6-sol` (§4, §6.2); `openai-codex` in
`default_order`, no-network implicitness for `gcp-adc`, and the "no default
instance" error (§5.1); the `[1m]` rows limited to upstream-present bases
with a dangling-alias load error (§4, §6.2); `max_completion_tokens` pinned
on `openai` with `max_tokens` as the chat baseline (§6.2, §8.2);
`FindModel` for plugin-agent model declarations (§7, §7.4); cap-governed
paths removed from the prunable sets and the build → prune → constants
order (§8.2); credential inheritance stopping at the endpoint (§10); the
continuation request fingerprint via `BuildBody` and the full scope
component list (§7.5, §8.1); `Reasoning = false` semantics (§8.4); Azure and
xAI Responses extras (§6.2); the probe's 16-token minimum (§11.2);
`StreamEndpoint` and the google defaults (§4, §6.1, §9.4); implicit-instance
hub semantics, the login default, and the additive `InstanceEntry` (§5.1,
§9.5, §11.3); the anthropic 32000 fallback cap and the two Bedrock overlay
rows (§6.2, §7.3); `HostRule = ollama-host` (§6.2, §9.1); `ReasoningSummary`
baseline `none` (§4.1, §8.2); the three additional tag sites and the
corrected web-search row (§7.4); `req.ClientMetadata` (§9.5); the package
names `chatcompletions`/`responses` (§3, §8.1); `CostTier` (§4.1);
`davinci`/`babbage`/`sora` in the non-chat list and the 413 default (§5,
§12); `GEMINI_API_KEY` ordering (§6.1, §6.2); the google temperature path
(§6.1); `ThinkingShape` limited to the anthropic surface (§6.1).
