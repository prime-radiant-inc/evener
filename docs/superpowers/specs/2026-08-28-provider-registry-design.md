# Provider Registry and Capability Resolution

**Date:** 2026-08-28 (revision 5, after three adversarial review rounds)
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
- **Errors are classified by status code and a few message patterns**
  (`llm/errors.go:262-375`), not by the structured body, so an
  unrecognized-parameter 400 (Groq's "invalid JSON body") gives the user no
  hint about which field to turn off. Groq's 413 TPM cap is already reported
  verbatim as a context-length error, which is the right class for a
  per-request size ceiling.
- `cmd/llmcall` builds its client with `llm.NewFromEnv` and cannot see
  `providers.toml` at all.

## 3. Concepts

Five nouns. Only the first is code.

| Noun | What it is | Where it lives |
|---|---|---|
| **Protocol** | A wire format: how to encode a request body, decode a stream, list models, count tokens. Exactly one Go package each. | `llm/providers/{chatcompletions,responses,anthropic,google}` |
| **Transport** | How to reach an endpoint: auth scheme, URL templates, constant headers and body fields. Data, plus one small authenticator per scheme and, for the Codex backend, one request-preparation hook. | `registry.Transport` |
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
`anthropic-compatible`, `google-compatible`) with baseline caps and the
`generic` surface.

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

All types live in a new leaf package `llm/registry`, which imports nothing
from `llm`. `llm` imports `registry`; the request-shaping helpers that need
`llm.Request` (`ShapeRequest`, the `Protocol` and `Transport` interfaces)
live in `llm` itself (§8.1). Optional scalars are pointers so that "unset"
is distinguishable from `false`/`0` at every layer.

```go
type Provider struct {
    ID            string            // registry id; the instance name for user entries
    Base          string            // id of the record this one layers on (curated and user layers)
    InheritModels *bool             // default true; false = start with no rows from Base
    Implicit      *bool             // curated: may become an instance from a credential alone (§5.1)
    Name          string            // display name
    Doc           string            // upstream documentation URL
    Protocol      string            // default protocol for models without their own
    Surface       string            // fallback surface for rows without one and for synthesized rows
    Transport     Transport
    APIKeyEnv     []string          // env vars consulted for the key, in order
    Headers       map[string]string // constant request headers ($ENV refs allowed); non-secret
    CredentialHeaders map[string]string // secret headers; scrubbed from logs and hub rewrites
    Caps          Caps              // provider-level capability overlay
    Models        map[string]Model  // keyed by model id; keys may contain `*` (glob rows, §4.1)
    DefaultModel  string            // curated: what to pick when the user gives none
    CheapModel    string            // curated: the provider's cheap/fast model (bare id, same provider)
    Hidden        bool              // rows evener cannot drive (unsupported protocol, no base URL)
}

type Model struct {
    ID        string
    WireID    string     // id sent on the wire; defaults to ID
    AliasOf   string     // seed facts from another row ("id" in this provider, or "provider-id/id"); §4.2
    Protocol  string     // override of the provider default
    Transport *Transport // field-wise overlay on the provider transport, or a named preset (§4.3)
    Headers   map[string]string // model-level constant headers (Anthropic beta headers)
    Surface   string     // openai | anthropic | google | generic
    Caps      Caps
    Status    string     // "", "beta", "deprecated"
}

type Transport struct {
    Preset      string            // name of a `[transports.X]` record to start from (§4.3)
    Auth        string            // bearer | header | none | gcp-adc | oauth-openai-codex
    AuthHeader  string            // auth=header: header name (x-api-key, api-key, x-goog-api-key)
    BaseURL     string            // may contain {VAR} placeholders
    HostRule    string            // "" | vertex-location | ollama-host (§9.1)
    Endpoint    string            // non-streaming completion path; protocol default when empty
    StreamEndpoint      string    // streaming completion path; protocol default when empty
    ModelsEndpoint      string    // listing path; protocol default when empty; "-" means unsupported
    CountTokensEndpoint string    // token-count path; protocol default when empty; "-" means unsupported
    Vars        map[string]string // template variables; values or $ENV refs
    VarsEnv     map[string]string // var name → env var consulted when the user layer's Vars lack it
    Body        map[string]any    // constant JSON paths set after the prune (§8.2); parents are created
}
```

### 4.1 Caps

One flat struct shared by every protocol. Fields a protocol does not use are
ignored by it. `Fields` carries "send this optional wire field or not"; the
explicit fields are the transforms that cannot be expressed that way.

```go
type Caps struct {
    // Model facts. Catalog-sourced; user layers may correct them. This block
    // plus Surface is what an alias row inherits (§4.2).
    ContextWindow     *int       // input budget the agent plans against
    MaxOutputTokens   *int
    Tools             *bool
    StructuredOutput  *bool      // json_schema accepted; false downgrades to json_object at build time
    Reasoning         *bool      // false: no reasoning controls, no thinking replay, empty effort list
    ReasoningControls []string   // subset of effort, budget_tokens, toggle (models.dev names); replace on overlay
    EffortValues      []string   // wire-spelled ladder, ascending; replace on overlay; non-empty implies effort
    InputModalities   []string   // text, image, pdf, audio; replace on overlay
    KnowledgeCutoff   *string    // YYYY-MM or YYYY-MM-DD
    Cost              *Cost      // $/M tokens; replace on overlay

    // Optional wire fields: JSON path → send. Key-wise merge.
    // Every key must be in the row's protocol prunable set (§8.2).
    Fields map[string]bool

    // Structural request shaping.
    MaxTokensField    *string    // openai-chat: max_tokens (baseline) | max_completion_tokens
    ThinkingFormat    *string    // openai-chat dialect, §8.4
    ThinkingShape     *string    // anthropic: adaptive | budget | budget+effort; derived when unset (§7.4)
    ThinkingDisplay   *string    // anthropic adaptive: "" | summarized
    ThinkingAlwaysOn  *bool      // send the thinking/reasoning object even when no effort is requested (§8.4)
    ReasoningField    *string    // openai-chat replay: reasoning_content | reasoning | reasoning_text | reasoning_details
    ReasoningSummary  *string    // openai-responses: none (omit, baseline) | auto | detailed
    ChatTemplateKwargs map[string]any
    FinishReasonMap   map[string]string // replace on overlay
    CacheControl      *string    // anthropic-style cache_control markers on openai-chat gateways
    CacheTTL          *string    // anthropic: "" | 1h
    StrictTools       *bool      // openai-responses: strict:true + schema strictify; chat: strict:false marker
    ToolChoiceForcing *bool      // false → downgrade required/named tool_choice to auto
    MaxStopSequences  *int
    ImageDetail       *string    // openai-responses: original | high | low | omit
    ResponsesLite     *bool      // Codex backend input-item shaping for gpt-5.6 (§9.5)

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

**Merge order.** Layers apply in order 1→5 (§5). Within one layer, for a
given row id, the order is: the provider-level `Caps`; then every top-level
glob row (`[models."<glob>"]`, applied to all providers) whose pattern
matches the id; then every provider glob row (`[providers.X.models."<glob>"]`)
that matches; then the exact row. Glob patterns use `*` only, match the full
id, and when several match they apply in order of pattern length (shorter
first, so the more specific pattern wins). Across layers, later always wins
regardless of level: a layer-3 provider-level pin beats a layer-1 row fact
(that is how Bedrock's `StructuredOutput = false` overrides models.dev's
per-row `true`, §9.3), and a layer-4 instance-level `context_window`
rewrites every row of that instance (the documented meaning of an
instance-wide overlay; put it on the row when only one model is meant). A
pointer or scalar set at a later step replaces; a `nil` inherits. Slices
(`EffortValues`, `InputModalities`, `ReasoningControls`) and
`Cost`/`FinishReasonMap` replace wholesale. `Fields`, `ChatTemplateKwargs`,
`Transport.Vars`, `Transport.Body`, and `Headers` merge key-wise. This is one
reflect-driven function (~100 lines) that also records
`map[fieldPath]"layer/level[/glob]"` provenance.

### 4.2 Alias and base inheritance

`AliasOf` is not a lookup step; it is a seeding rule. The target row is
merged first, through every layer. Its final **facts** (the "Model facts"
block of `Caps` plus `Surface`) become the alias row's row-level values at
"layer 0", before any real layer applies. Everything else (`Fields`,
`Transport`, `Headers`, the structural and transform caps, `WireID`,
`Protocol`) is never imported from another row: the alias row gets them from
its own provider and its own layers like any row. Consequences the reviewers
walked through: `openai-codex/gpt-5.6` inherits GPT-5.6's window, cost, and
effort ladder from `openai/gpt-5.6` but keeps Codex's provider-level `fields`
off-list; `claude-sonnet-4-5[1m]` inherits the target's facts and then its
own `context_window = 1000000` at layer 3 wins over the inherited 200000;
`amazon-bedrock`'s provider-level `WebSearch = false` is untouched by an
alias because `WebSearch` is not a fact. A target that does not exist is a
load error (`alias_of` may name `"id"` in the same provider or
`"provider-id/id"`).

`Base` inheritance is the same at every layer: the record's merged form is
its base's merged form with the record's own fields overlaid, models
included unless `InheritModels = false`.

**Cross-protocol rule.** Any record (instance or model) whose resolved
protocol differs from the record it inherits from does **not** inherit the
protocol-specific transport fields (`Endpoint`, `StreamEndpoint`,
`ModelsEndpoint`, `CountTokensEndpoint`, `Body`) or the inherited `Fields`;
it starts from its own protocol's defaults and baseline, then takes its own
`Transport` (or preset) and its own `Fields`. Auth, base URL, headers, vars,
and credentials are inherited as usual. This keeps a `[providers.work] base
= "openai", protocol = "openai-chat"` instance from counting tokens against
`/responses/input_tokens`, and keeps `google-vertex`'s Claude rows off the
Gemini endpoint (§9.4).

### 4.3 Transport presets

The curated overlay declares named transports under `[transports.<name>]`
(same fields as `Transport`). A provider or model may say `transport =
"<name>"` to start from one and overlay its own fields. The converter maps
per-model `npm` values to presets so cross-protocol rows get the right
endpoints without a curated row each: `@ai-sdk/google-vertex/anthropic` →
`vertex-anthropic`, `@ai-sdk/google-vertex` → `vertex-gemini`,
`@ai-sdk/amazon-bedrock/mantle` → `bedrock-mantle-openai` (with the row's own
`api` template overriding the preset's `BaseURL`). A per-model `npm` override
with no preset changes the protocol but never the provider's `Auth`.

### 4.4 Resolved

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
    Caps       Caps        // fully merged and derived (§7.4); every prunable path of the protocol present in Fields
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
- **Implicit**: every registry provider marked `implicit = true` in the
  curated overlay (§6.2) that is not shadowed by an explicit entry of the
  same name and whose credential resolves without network access: a
  credentials-store entry for the id, or one of its `APIKeyEnv` variables
  set, or for `oauth-openai-codex` its OAuth record (`auth/openai-codex.json`),
  or for `gcp-adc` the `GOOGLE_APPLICATION_CREDENTIALS` variable or the
  well-known ADC file (the metadata-server probe in
  `FindDefaultCredentials` is never run at load; it runs at first request).
  Providers with `auth = none` on the implicit list (Ollama) are implicit
  unconditionally. Nothing else becomes an instance from an environment
  variable alone: `GITHUB_TOKEN`, `HF_TOKEN`, or `DATABRICKS_TOKEN` in a shell
  must not conjure a `github-copilot`, `huggingface`, or `databricks`
  instance.

Implicit instances are computed identically by every process from the same
inputs; the hub no longer materializes `providers.toml` at startup and passes
nothing to children beyond `EVENER_PROVIDERS_CONFIG` when a file exists. The
hub lists implicit instances flagged *from environment*; editing one or
making it the default writes a shadowing explicit entry; removing one is
refused with a message naming the variable or record that makes it exist.

The **default instance** is `default` from `providers.toml` when set; else
the first explicit instance by sorted name (today's rule,
`providercfg/load.go:257-260`); else the first implicit instance in the
curated `default_order` list (§6.2) that has a `DefaultModel`; else the
first implicit instance by sorted name that has a `DefaultModel`. Providers
without a `DefaultModel` (Ollama, the pseudo-providers) are never defaulted
to, which replaces `NonDefaultEligible`. `openai-codex` precedes `openai` in
`default_order`, preserving today's "stored OAuth beats API key" choice.
When no instance exists at all, resolving a bare model id fails with "no
default instance: set `default` in providers.toml or export a provider key".

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
| `@ai-sdk/amazon-bedrock/mantle` (per-model) | `openai-responses` or `openai-chat` per `shape`, preset `bedrock-mantle-openai` | bearer |
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
"completions"` → `openai-chat`, `api` → a model-level `BaseURL` template,
`npm` in the preset table of §4.3 → that preset. Per-model overrides never
change the provider's `Auth` unless the preset does (Bedrock Mantle's does).
This is what makes Bedrock's OpenAI models (`api:
https://bedrock-mantle.${AWS_REGION}.api.aws/v1` or `…/openai/v1`, `shape:
responses`), Azure Foundry's Claude models (`npm: @ai-sdk/anthropic`, `api:
https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`), and
Vertex's Claude rows under `google-vertex` (`npm:
@ai-sdk/google-vertex/anthropic`, no `api`) resolve without curated rows.

Two hiding rules beyond the `npm` table: an `amazon-bedrock` row with no
per-model override whose id, after the region prefix, does not start with
`anthropic.` is `Hidden` (the Messages endpoint serves Claude only, and the
seven OpenAI rows without a Mantle override, such as
`global.openai.gpt-5.6-sol`, would otherwise resolve to it); a row on a
`Hidden` provider is hidden with it.

**Base URL convention.** models.dev base URLs include the version segment
(`…/v1`, `…/anthropic/v1`, `…/paas/v4`; DeepSeek's `https://api.deepseek.com`
is a known exception that serves both spellings); the AI SDK appends the
resource. The registry follows the same convention. Protocol defaults:

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
| `temperature: false` | the row's protocol temperature and top-p paths set `false` in `Fields` (`temperature`/`top_p` on the OpenAI protocols and anthropic, `generationConfig.temperature`/`generationConfig.topP` on google) |
| `modalities.input` | `InputModalities` |
| `knowledge` | `KnowledgeCutoff` |
| `status` | `Model.Status` |
| `interleaved` (boolean on 65 rows, `{field: …}` on 893) | `ReasoningField` from the object form only |
| `family` | `Surface`: `claude*` → `anthropic`; (`gpt*` except `gpt-oss*`), `o`, `o-mini`, `o-pro` → `openai`; `gemini*`, `gemma*` → `google`; anything else, including the 666 rows with no `family`, → unset, so the provider's `Surface` applies (§6.2), else `generic` |
| id ending `@default` (Vertex rows) | the row is re-keyed to the id without the suffix (`ID` and `WireID` both `claude-opus-5`); other `@<version>` ids are kept and sent verbatim |
| `modalities.output` lacking `text` | row dropped (image/audio/embedding models) |

Nothing else is derived at conversion time; the derived caps of §7.4 are
computed on the final merged row. Models without `tool_call` are kept and
flagged so `llmcall` can use them and the hub picker can hide them.

### 6.2 Curated overlay

`llm/data/providers_overlay.toml`, same schema as `providers.toml` (§10),
loaded as layer 3. It carries only what models.dev lacks or gets wrong. Its
initial contents, in full. Glob rows are written as such (`"gpt-5*"`), and
the merge order of §4.1 applies to them.

- **Transports** (`[transports.*]`, §4.3): `vertex-anthropic` (`endpoint =
  /publishers/anthropic/models/{model}:rawPredict`, `stream_endpoint =
  …:streamRawPredict`, `models_endpoint = "-"`, `count_tokens_endpoint =
  "-"`, `body = { anthropic_version = "vertex-2023-10-16" }`, `auth =
  gcp-adc`); `vertex-gemini` (`endpoint =
  /publishers/google/models/{model}:generateContent`, `stream_endpoint =
  …:streamGenerateContent?alt=sse`, `models_endpoint = "-"`,
  `count_tokens_endpoint = "-"`, `auth = gcp-adc`); `bedrock-mantle-openai`
  (`auth = bearer`, `models_endpoint = /models`, `count_tokens_endpoint =
  "-"`).
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
- **Implicit list** (`implicit = true`): `anthropic`, `openai`,
  `openai-codex`, `google`, `google-vertex`, `google-vertex-anthropic`,
  `amazon-bedrock`, `azure`, `groq`, `xai`, `mistral`, `cerebras`,
  `deepseek`, `zai`, `zai-coding-plan`, `openrouter`, `togetherai`,
  `kimi-for-coding`, `moonshotai`, `minimax`, `ollama`. Everything else needs
  a `providers.toml` entry.
- **Provider surfaces**: `anthropic`, `amazon-bedrock`,
  `google-vertex-anthropic`, `kimi-for-coding` → `anthropic`; `openai`,
  `openai-codex`, `azure` → `openai`; `google`, `google-vertex` → `google`.
  Everything else, including the three pseudo-providers, is `generic`
  unless a row's family says otherwise.
- **`default_order`** = `anthropic, openai-codex, openai, google, groq, zai,
  deepseek, openrouter`; `default_model` and `cheap_model` for each of
  those.
- **OpenAI** (`openai`): `fields` on: `store`, `prompt_cache_key`, `include`,
  `truncation`, `safety_identifier`, `service_tier`, `previous_response_id`,
  `conversation`, `max_tool_calls`, `background`, `metadata`;
  `prompt_cache_retention` on the `gpt-5*` and `gpt-4.1*` glob rows except
  the `gpt-5.6*` glob row, where it stays off (GPT-5.6 moved to
  `prompt_cache_options.ttl` with different semantics; today's builder
  skips the legacy field there, `responses.go:112-118`).
  `MaxTokensField = "max_completion_tokens"` (so `base = "openai"` gateways
  over chat inherit the spelling OpenAI reasoning models require).
  `StrictTools = true`, `WebSearch = true`, `ReasoningSummary = "auto"` with
  `"detailed"` on the `gpt-5*` and `gpt-6*` glob rows, `ImageDetail =
  "original"` on `gpt-5.4*`/`gpt-5.5*`/`gpt-6*`, `CountTokensEndpoint =
  /responses/input_tokens`, and `headers = { OpenAI-Organization =
  "$OPENAI_ORG_ID", OpenAI-Project = "$OPENAI_PROJECT_ID" }` (an unset `$VAR`
  in `headers` drops the header, §10). The `gpt-5.6*` glob row also sets
  `thinking_always_on = true` and `image_detail = "omit"` (the platform-side
  half of today's `responsesLiteModel`: the reasoning object with a summary
  and `include: [reasoning.encrypted_content]` on every request, no image
  `detail`, `responses.go:145-166,1166,1290`).
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
  `max_output_tokens`, `previous_response_id`, `conversation`,
  `service_tier`, `safety_identifier`, `prompt_cache_retention`,
  `truncation`, `max_tool_calls`, `background`; `prompt_cache_key` stays on
  (inherited from `openai`; today's builder sends it on Codex,
  `responses.go:97-99`, as does Pi). Rows, each with `alias_of` pointing at
  the matching `openai/…` row for facts: `gpt-5.6` with `wire_id =
  "gpt-5.6-sol"` (the backend rejects the bare slug,
  `responses.go:1030-1036`), `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`.
  A `gpt-5.6*` glob row on this provider sets `responses_lite = true`,
  `thinking_always_on = true`, `image_detail = "omit"`, `reasoning_summary =
  "detailed"`, and `body = { "reasoning.context" = "all_turns",
  "text.verbosity" = "low", parallel_tool_calls = false }` (today's Codex
  shaping, `responses.go:41-75,153-158,177-186`). Only these rows are valid
  on this transport: an unknown id is an error, not a warning (§7.3). The
  transport's own behaviors are enumerated in §9.5.
- **Anthropic**: `api_key_env` as today, `CacheTTL`, `WebSearch = true`,
  provider-level `thinking_shape = "adaptive"` so an uncataloged Claude id
  on this instance gets adaptive thinking plus `output_config.effort` (what
  today's generation parse gives an unknown `claude-*-5` id), and the
  corrections to upstream: `claude-sonnet-4-5` and
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
  `ThinkingDisplay = "summarized"` on the `claude-*-5` glob rows (Fable 5,
  Opus 5, Sonnet 5, Mythos). `Fields["temperature"] = false` on Claude 5
  rows is already true from models.dev `temperature: false`; listed here
  only if upstream regresses. The refresh script lists every pinned row
  whose upstream value changed so pins get re-examined.
- **Top-level glob rows** (`[models."<glob>"]`, applied to every provider):
  `"*claude-opus-4-5*"` → `thinking_shape = "budget+effort"` (models.dev
  lists `effort` + `budget_tokens` for both Opus 4.5 and Opus 4.6, and only
  4.6 takes the adaptive body; this covers the rows on `anthropic`, `azure`,
  `azure-cognitive-services`, both Vertex providers, and every
  `amazon-bedrock` spelling). `"*gemini-3*"` → `multimodal_tool_results =
  true`.
- **Google** (`google`, `google-vertex`, `google-vertex-anthropic`):
  `api_key_env = [GEMINI_API_KEY, GOOGLE_API_KEY]` on `google`; `WebSearch =
  true` on all three (Anthropic's Vertex page lists the web search tool as
  supported).
- **Kimi**: `moonshotai` and `moonshotai-cn` (openai-chat): `Fields` off for
  `temperature`/`top_p`/`frequency_penalty`/`presence_penalty`,
  `StructuredOutput = false`, `ToolChoiceForcing = false`.
  `kimi-for-coding` (anthropic): `Headers["User-Agent"] = "claude-cli/2.1.177
  (external, cli)"`, `surface = "anthropic"` (today's kimi-anthropic profile
  uses `CLAUDE.md` and the Anthropic tool set, `profile.go:969-990`), and
  `thinking_shape = "budget+effort"` on the `k3*` glob row (today's shape for
  `k3`/`k3-256k`: `supports_effort_parameter` without adaptive,
  `evener_model_catalog_overrides.json`).
- **z.ai** (`zai`, `zai-coding-plan`, `zhipuai`, `zhipuai-coding-plan`):
  `ThinkingFormat = "zai"`, `StripEmptyContent`, `MaxStopSequences = 1`,
  `FinishReasonMap = { sensitive = "content_filter", network_error = "error"
  }`, `StructuredOutput = false`, `ToolChoiceForcing = false`,
  `Fields["developer_role"] = false`.
- **DeepSeek**: `ThinkingFormat = "deepseek"`, `EmptyReasoningContent`.
- **OpenRouter**: `ThinkingFormat = "openrouter"`, `ToolChoiceForcing =
  false`, `SessionAffinityHeaders`; `CacheControl = "anthropic"` on the
  `anthropic/*` glob row; on the `minimax/*` glob row `reasoning_controls =
  ["toggle"]`, `thinking_always_on = true`, `reasoning_field =
  "reasoning_details"` (today's unconditional `reasoning: {enabled: true}`
  plus the `reasoning_details` replay path, `profile.go:1283-1292`,
  `openaicompat/request.go:22-33,381-411`).
- **Bedrock** (`amazon-bedrock`): §9.3, plus two rows Anthropic's model table
  has and models.dev lacks: `anthropic.claude-haiku-4-5` with `alias_of =
  "anthropic.claude-haiku-4-5-20251001-v1:0"`, and
  `anthropic.claude-mythos-preview` with no `alias_of` (no upstream row
  exists yet; it takes provider-level caps and the `anthropic` surface, and
  the refresh script's overlay report flags it when upstream adds one).
- **Ollama** (`ollama`, not in models.dev): `Protocol = openai-chat`,
  `BaseURL = {OLLAMA_HOST}` with `VarsEnv = { OLLAMA_HOST = OLLAMA_HOST }`,
  `vars = { OLLAMA_HOST = "localhost" }` as the curated default, and
  `HostRule = ollama-host` (§9.1), `Auth = none`, no models (live only), no
  `DefaultModel`.
- **Pseudo-providers** `openai-compatible`, `anthropic-compatible`,
  `google-compatible`: protocol only, `generic` surface, no base URL, no
  models, `Hidden` (usable only as a `base`).

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
  covers, dangling overlay aliases, and overlay pins whose upstream value
  changed.
- The embedded file is raw upstream JSON, gzipped (4.4 MB → 439 KB).
  Parsing costs about 30 ms once, lazily, as today.

## 7. Resolution

`(*Registry).Resolve(ref string) (Resolved, error)` is the single lookup
path. It replaces `LookupModelInfo`, `resolveOpenAICompatCatalogModel`,
`fillFromCatalog`, `GetPrice`'s prefix scan, `ResolveLiveModelInfo`,
`cmdutil.ParseModelRef` + `SelectProfile`, and the profile constructors.
`(*Registry).FindModel(id string) []Ref` answers the other question the
agent asks ("which instances serve this model id?") for plugin-agent model
declarations (§7.5).

### 7.1 Reference syntax

`instance/model`, split on the **first** slash; the model half may contain
slashes (`groq/openai/gpt-oss-120b`, `openrouter/anthropic/claude-opus-5`).
A bare model id with no slash is resolved against the default instance.
There is no suffix handling: `claude-sonnet-4-5[1m]` is an ordinary alias
row in the curated overlay whose `wire_id` names the base model. Dated rows
are addressed by their catalog spelling (`vertex/claude-sonnet-4-5@20250929`,
as Anthropic's Vertex table lists it).

### 7.2 Model lookup order

Within the merged provider record, in order; the first hit wins and is
recorded in `Provenance["model"]`:

1. exact id in the instance's own `models` (layer 4)
2. exact id in the provider's merged `models` (alias rows are ordinary rows
   here; their facts were seeded at merge time, §4.2)
3. cloud region prefix stripped: `us.`, `eu.`, `apac.`, `au.`, `jp.`,
   `global.` (Bedrock inference profiles)
4. dated family: `-YYYYMMDD`, `-YYYYMMDD-v<N>`, `-YYYYMMDD-v<N>:<M>`, or
   `@YYYYMMDD` suffix removed
5. live listing (layer 5), which establishes existence with provider-level
   caps only

Steps 1–2 use the matched row's `WireID`. Steps 3–5 use the **reference
verbatim** as the wire id (a `global.` profile keeps its prefix, a dated
snapshot keeps its date); `Provenance["model"]` records the row that
supplied the facts. No substring or longest-prefix matching anywhere.

### 7.3 Unknown models

A model id that matches nothing is still resolvable: `Resolved.Model` is
synthesized from provider-level caps and the provider's `Surface`,
`Warnings` carries `model not in catalog`, and the wire id is the reference
verbatim. Context window is unset, which the agent treats as "unknown" (no
compaction budget until the live listing or a user row supplies one); the
anthropic protocol's required `max_tokens` falls back to 32000 as today
(`anthropic/request.go:16-19`). Reasoning follows §8.4's unknown-model
rule. The hub shows the warning next to the model. This is how a model
released this morning works before the cache refreshes. The one exception is
the `oauth-openai-codex` transport, whose backend enforces a model
allowlist: an unknown id there is a resolve error.

### 7.4 Derived caps

After the merge, and only where the field is still unset, `Resolve` derives:

- `ThinkingShape`: `adaptive` when `effort ∈ ReasoningControls` and
  `Surface == anthropic`; else `budget` when `budget_tokens ∈
  ReasoningControls`; else unset (no thinking object). Third-party
  anthropic-protocol vendors therefore get a shape only from an overlay pin
  (`kimi-for-coding`, §6.2).
- `ThinkingAlwaysOn = true` when the **final** `ThinkingShape` is
  `adaptive` (today's builder sends the adaptive object for every
  adaptive-capable model whether or not an effort is requested,
  `anthropic/request.go:146-157`). An overlay pin to `budget+effort` leaves
  it unset, so Opus 4.5 with no effort sends no thinking object, as today.
- `effort ∈ ReasoningControls` when `EffortValues` is non-empty from any
  layer, including live and user rows (the §10 `glm-5.2-nvfp4` example
  declares only `effort_values`).
- `Surface` from the row, else the provider, else `generic`.

Provenance records `derived` for each.

### 7.5 What the agent reads from `Resolved`

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
| plugin-agent `model:` declarations (`resolvePluginAgentCatalogRef`, `subagent_model_selection.go:155-190`, today via `catalog.ResolveAlias`) | `Registry.FindModel` | `instance/model` resolves directly; a bare id resolves to the session's current instance when that instance serves it, else to the single instance that does, is *ambiguous* when several do, *unavailable* when none, preserving today's fallback-with-warning |
| `ProviderOptions` map key and the API-log tag (`session_model_call.go:248`) | `Protocol` | options are protocol extras (beta headers, safety settings) |
| `openAIPromptCacheSupported` (`session.go:1343`) | `PromptCacheKey` set iff `Fields["prompt_cache_key"]`; `PromptCacheRetention = "24h"` set iff `Fields["prompt_cache_retention"]` | two independent gates, so Codex keeps the cache key and GPT-5.6 drops only the legacy retention field |
| `Client.BehaviorTagOf` identity fallback for replay scope (`client.go:432`, `session_model_call.go:1171`) | `Instance` + `Protocol`, both recorded on every turn | turns produced by instances no longer configured still carry what the replay needs |

`llm.ShapeRequest(req, resolved)` is the single place the request-level
shaping happens and the only caller of `ClampReasoningEffort`. It runs in
this order: clear reasoning controls when `Caps.Reasoning == false`; clamp
the effort to `EffortValues` (an unknown model with no ladder passes the
requested effort through unchanged, as today's openai dialect does); apply
`MaxOutputTokens` when the request has none; apply the
Responses-continuation store override when a continuation is planned
(§7.6); drop request-level sampling parameters whose `Fields` entry is
false. Adapters receive a request that is already legal for the endpoint;
the body-level prune (§8.2) is the second, mechanical pass.

### 7.6 Responses continuation

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
  use; `stream` is part of the returned body and is excluded from the hash
  along with `input`, `previous_response_id`, `conversation`, and `store`,
  `responses_continuation_fingerprint.go`);
- the **storage-scope fingerprint** hashes the instance name, resolved base
  URL, endpoint path, the credential fingerprint and source the store
  already computes, the `OpenAI-Organization`/`OpenAI-Project` header values
  when present, the conversation id when set, and the OAuth
  account/workspace claims on the Codex transport; the storage policy label
  comes from the built body's `store` value as today (`adapter.go:425-434`);
  the `ContinuationHasher` stays on the client, keyed by state dir;
- `EndpointFamily` is `codex` when `Transport.Auth == oauth-openai-codex`,
  else `public`; the support registry keeps its per-family defaults
  (`llm/responses_continuation.go:232-244`);
- `CanFallbackToChat` and `FullHistoryFallbackMessages` are deleted with the
  fallback. A rejected anchor (`ErrorCode() == "previous_response_not_found"`,
  `session_model_call.go:1027`) is handled by the session as today.

## 8. Protocol adapters

### 8.1 Interfaces and the client

```go
// package llm
type Protocol interface {
    ID() string
    PrunablePaths() []string   // the optional paths this protocol can emit that no cap governs (§8.2)
    BuildBody(req Request, res registry.Resolved) (map[string]any, error)
    Complete(ctx, req Request, res registry.Resolved) (Response, error)
    Stream(ctx, req Request, res registry.Resolved) (Stream, error)
    ListModels(ctx, res registry.Resolved) ([]registry.Model, error)   // ErrUnsupported when ModelsEndpoint is "-"
    CountTokens(ctx, req Request, res registry.Resolved) (int, error)  // ErrUnsupported when CountTokensEndpoint is "-"
}

type Authenticator interface {
    Apply(ctx, *http.Request, registry.Credential) error // sets auth headers
}

// Optional, implemented only by the Codex transport (§9.5).
type RequestPreparer interface {
    PrepareRequest(ctx, *http.Request, body map[string]any, req Request, res registry.Resolved) error
    RequiresStreamingComplete() bool
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
by `res.Protocol` → call it. It keeps one override map, `Register(name,
adapter)`, consulted by instance name before registry dispatch, so the
scripted providers and fake adapters the agent, hub, and `cmdutil` tests use
(516 registration sites across 243 test files, `agent/testkit_test.go:73`,
`agent/session_test.go:41`, `scripted_provider_test.go`) keep working
unchanged; an override adapter receives the request as today, without a
`Resolved`. The `nameToTag` map, `RegisterInstanceAdapterFactory`,
`RegisterEnvAdapterFactory`, `NewFromEnv`, `NewFromProviders`, the
`providerfwd` forwarding packages, `llm.ErrorClassFallback`/
`isEndpointFallbackSignal` (`llm/classify.go:119`) and its consumers in
`contextmgr` and `session_init.go:1379`, and the
`ModelCompatibilityValidator` interface are deleted. Middleware, API attempt
logging, and the provider-name stamping on responses/errors/streams stay.
Callers that list models or count tokens (`launchcheck.go:170,229`,
`SetModel`'s membership preflight at `agent/session.go:1120`,
`modelavailability.Capture`, the hub's `fetchLiveModels`, `providers list
--check`, `probe`) treat `ErrUnsupported` as "registry-only listing" /
"estimate-only counting", never as a failure.

`bearer`, `header`, and `none` are trivial authenticators.
`oauth-openai-codex` is the existing `auth/<instance>.json` flow moved
behind the interface, with its token-refresh state cached per instance; the
same transport implements `RequestPreparer` (§9.5). `gcp-adc` sends a bearer
token from application-default credentials (`golang.org/x/oauth2/google`,
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
   `ThinkingShape`/`ThinkingFormat`/`ReasoningControls`/`ThinkingAlwaysOn`,
   `CacheControl`, `StructuredOutput` (false downgrades `json_schema` to
   `json_object`) all act here.
2. **Prune** by `Fields`, a denylist over an enumerated set. Each protocol
   package declares `PrunablePaths()`: every optional JSON path its builder
   can emit **that no cap governs**. The registry seeds `Caps.Fields` with
   the protocol baseline below; layers overlay it; `registry.Prune(body,
   res.Caps.Fields)` deletes each prunable path whose flag is false and
   records the deleted paths on the API attempt log entry as
   `pruned_fields`. A `fields` key in the overlay or `providers.toml` that is
   not in the row's resolved-protocol set is a load error (typo guard); keys
   inherited from a `base` on another protocol are ignored (§4.2).
3. **Body constants** from `Transport.Body` are set, creating parent
   objects as needed, so they survive the prune and never depend on build
   state.
4. **Transport preparation** (`RequestPreparer`, Codex only) runs last and
   may rename fields and add headers.

Paths outside the prunable set are always sent (`model` unless the
endpoint template contains `{model}`, `messages`/`input`, `tools` unless
`ResponsesLite` moved them into `input`, `stream`, `tool_choice`,
`response_format`/`text.format`, `instructions`/`system`, and every
cap-governed path: `input[].content[].detail`, `input[].phase`,
`reasoning.effort`/`reasoning_effort`, `reasoning.summary`, `include`'s
`reasoning.encrypted_content` entry, `thinking`, `output_config`,
`generationConfig.thinkingConfig`, `cache_control`).

Prunable sets and baselines (before any layer):

| Protocol | baseline `true` | baseline `false` |
|---|---|---|
| `openai-chat` | `temperature`, `top_p`, `stop`, `stream_options`, `max_tokens`* | `store`, `frequency_penalty`, `presence_penalty`, `developer_role`, `parallel_tool_calls`, `prompt_cache_key`, `prompt_cache_retention`, `service_tier`, `metadata`, `logprobs`, `n`, `seed`, `user` |
| `openai-responses` | `temperature`, `top_p`, `max_output_tokens` | `store`, `include`, `truncation`, `safety_identifier`, `service_tier`, `prompt_cache_key`, `prompt_cache_retention`, `previous_response_id`, `conversation`, `metadata`, `max_tool_calls`, `background`, `parallel_tool_calls`, `text.verbosity`, `reasoning.context` |
| `anthropic` | `temperature`, `top_p`, `stop_sequences`, `max_tokens` | `metadata`, `service_tier`, `fallbacks`, `container` |
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
not a Responses API parameter (openai-python's `ResponseCreateParams` has
none), so it is not in that set; today's `responses.go:96` emission was
dead. When `store` is enabled the builder sends `store: false` unless a
continuation is planned (today's privacy default, `responses.go:34`); when
it is disabled nothing is sent and the endpoint's own default applies. When
`include` is enabled the builder adds `reasoning.encrypted_content` whenever
it emits a `reasoning` object (`responses.go:160-166`); with
`ThinkingAlwaysOn` that is every request. `reasoning.summary` is omitted at
the `ReasoningSummary = none` baseline and sent as `auto` or `detailed` only
where the overlay says so. Anthropic's `metadata` accepts only `user_id`, so
it is off at baseline.

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
| `responsesLiteModel` (`gpt-5.6`) on the platform API | `thinking_always_on` + `image_detail = "omit"` + `prompt_cache_retention` off on the `openai` `gpt-5.6*` glob row (§6.2) |
| `codexLite` (`gpt-5.6` on the Codex backend) | `responses_lite` + `body` constants on the `openai-codex` `gpt-5.6*` glob row (§6.2, §9.5) |
| `defaultImageDetail` (`gpt-5.4/5.5/gpt-6`) | `ImageDetail = "original"` on those rows; baseline `"high"` |
| `reasoningSummaryLevel` (`gpt-5/gpt-6`) | `ReasoningSummary = "detailed"` on those rows, `"auto"` on the rest of `openai`; baseline `none` |
| `isClaude5OrNewer` | `Fields["temperature"]=false` (from models.dev), `ThinkingShape = "adaptive"` + `ThinkingAlwaysOn` (derived), `ThinkingDisplay = "summarized"` on the `claude-*-5` glob row |
| `adaptiveThinking` for Opus/Sonnet 4.6+ | derived (§7.4): adaptive, always on |
| the Opus 4.5 hybrid | top-level `"*claude-opus-4-5*"` glob row |
| `geminiSupportsMultimodalFunctionResponse` (`gemini-3`) | top-level `"*gemini-3*"` glob row |
| `[1m]` synthesis for `claude-opus-4-`/`claude-sonnet-4-` | curated `[1m]` alias rows (§6.2) |
| `minimax/` under openrouter | the `minimax/*` glob row (§6.2) |
| `codexModelVariants` | `wire_id` + rows under `openai-codex` (§6.2) |
| `openAIModelSupports24hPromptCache` | `prompt_cache_retention` on the `gpt-5*`/`gpt-4.1*` glob rows |

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

**openai-chat.** The dialect table is today's `applyThinkingFormat`
(`request.go:230-291`, documented in `docs/llm-providers.md` "thinking_format:
exact wire JSON per dialect") kept verbatim; the only change is the gate
that decides *whether* the effort value is sent. Let *effort-capable* mean
`effort ∈ ReasoningControls` (including the `EffortValues` implication of
§7.4), and *enable-capable* mean `toggle ∈ ReasoningControls`.

| `ThinkingFormat` | when an effort is set | with `ThinkingAlwaysOn` and no effort |
|---|---|---|
| `openai` (default) | `reasoning_effort: <wire>` if effort-capable, else nothing (Chat Completions has no toggle) | nothing |
| `openrouter` | `reasoning: {effort: <wire>}` if effort-capable, else `reasoning: {enabled: true}` if enable-capable | `reasoning: {enabled: true}` |
| `zai` | always `thinking: {type: enabled, clear_thinking: false}`; plus `reasoning_effort: <wire>` if effort-capable | `thinking: {type: enabled, clear_thinking: false}` |
| `deepseek` | always `thinking: {type: enabled}`; plus `reasoning_effort: <wire>` if effort-capable | `thinking: {type: enabled}` |
| `together` | always `reasoning: {enabled: true}`; plus `reasoning_effort: <wire>` if effort-capable | `reasoning: {enabled: true}` |
| `qwen` | `enable_thinking: true` | `enable_thinking: true` |
| `qwen-chat-template` | `chat_template_kwargs: {enable_thinking: true, preserve_thinking: true}` | same |
| `chat-template` | `chat_template_kwargs: <ChatTemplateKwargs>` (omitted when empty) | same |
| `string-thinking` | `thinking: <wire>` | nothing |

Rows that carry both `effort` and `toggle` (every DeepSeek v4 row, the
OpenRouter `anthropic/*` rows) simply satisfy both conditions. `none` sends
nothing on every dialect.

**openai-responses.** `reasoning: {effort: <wire>}` when an effort is set and
the row is effort-capable; `reasoning.summary` from `ReasoningSummary`
(omitted at `none`); with `ThinkingAlwaysOn` and no effort, `reasoning:
{summary: …}` alone, as today's lite handling (`responses.go:145-151`).
`include: [reasoning.encrypted_content]` accompanies every `reasoning`
object when `Fields["include"]` is on.

**anthropic.** `ThinkingShape` picks one of three bodies the builder already
knows (`anthropic/request.go:131-176`): `adaptive` → `thinking: {type:
adaptive}` plus `display` from `ThinkingDisplay`, sent whenever
`ThinkingAlwaysOn` (derived for adaptive rows) or an effort is set, plus
`output_config.effort` when an effort is set; `budget` → `thinking: {type:
enabled, budget_tokens}` from the existing effort→budget table, only when an
effort is set; `budget+effort` (Opus 4.5, Kimi K3) → both. Unset shape → no
thinking object.

**google.** Effort → `thinkingConfig` as today; `none` sends no
`thinkingConfig`.

**Unknown models** (§7.3) have no ladder and no controls; on the OpenAI
protocols the requested effort passes through unclamped and is sent by the
dialect's effort rule as today; on the anthropic protocol the provider-level
`thinking_shape` applies (`adaptive` on `anthropic`, so a new Claude id gets
today's generation-parse behavior).

The `none` effort clears the control on every protocol; nothing is ever sent
to force thinking off. A `thinking_levels` map (today's per-model level →
wire-string table) is not needed: a wire-spelled `EffortValues` ladder under
`ClampReasoningEffort` reproduces it, because below-range requests raise to
the lowest supported value and the top tier resolves to the model's own
spelling.

**Replay.** Prior thinking on the chat protocol writes back to
`ReasoningField`: `reasoning_content`, `reasoning`, or `reasoning_text` as a
string field; `reasoning_details` as OpenRouter's array of `reasoning.text`
items (today's `request.go:381-411` path). The value comes from models.dev
`interleaved.field` when present, else from the field the text arrived on
(today's `Signature` mechanism), else `reasoning_content`. Independently of
`ReasoningField`, a thinking part that carries serialized `reasoning_details`
items (`reasoning.encrypted` items, or `reasoning.text` items bearing a
`signature`, `response.go:86-88`) is always replayed through the
`reasoning_details` array with its text merged into the signature-bearing
item, exactly as today's `request.go:375-382` does regardless of the
`useReasoningDetails` flag; that is what keeps Claude and Gemini/o-series
reasoning intact across tool calls on OpenRouter.

## 9. Transports and the cloud providers

The transport axis exists so Azure, Bedrock, and Vertex are data. None of
them needs request signing or non-SSE framing.

### 9.1 URL assembly

`Transport.BaseURL` may contain `{VAR}` placeholders. `Endpoint`,
`StreamEndpoint`, `ModelsEndpoint`, and `CountTokensEndpoint` are path
templates that may contain `{model}` and `{VAR}`; empty means the protocol
default (§6.1), `-` means unsupported. Variables resolve in this order: the
user layer's `Vars` (instance config), then the environment through
`VarsEnv`, then `Vars` from the curated and upstream layers (defaults), then
a resolve error naming the variable and the instance. `HostRule` names one
of two normalizers, the only host-aware code in the system:
`vertex-location` derives the Vertex host from the location variable
(§9.4); `ollama-host` is today's `envvars.NormalizeOllamaHost`, applied to
the **variable value** before substitution, producing the full base URL
(`localhost` → `http://localhost:11434/v1`, `::1` → `http://[::1]:11434/v1`,
`http://proxy/ollama/v1` kept; the repo already fixed the
`OLLAMA_HOST=localhost` → "unsupported protocol scheme" bug once), which is
why the Ollama template is `{OLLAMA_HOST}` with nothing appended. If the
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
alias_of = "gpt-5.5"          # facts from the catalog row; wire id stays gpt55-prod
```

- Claude on Azure Foundry: models.dev already marks those rows `npm:
  @ai-sdk/anthropic` with `api:
  https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`, so
  they resolve to the anthropic protocol at that base URL with the
  `x-api-key` header the per-model `npm` mapping gives them (Anthropic's
  Foundry page accepts `api-key` or `x-api-key` plus `anthropic-version`).
  Deployment names apply here too. One instance, two protocols.

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
- **OpenAI-shaped models**: models.dev marks nine rows
  `@ai-sdk/amazon-bedrock/mantle` with `api:
  https://bedrock-mantle.${AWS_REGION}.api.aws/v1` (gpt-oss) or `…/openai/v1`
  (gpt-5.x, grok) and `shape: responses`; the converter gives them the
  `bedrock-mantle-openai` preset (bearer auth from the same
  `AWS_BEARER_TOKEN_BEDROCK`; AWS documents the bearer path for both the
  `bedrock-mantle` and `bedrock-runtime` OpenAI-compatible endpoints). The
  other non-Claude rows on `amazon-bedrock` (seven OpenAI spellings without
  the override, Nova, Llama, DeepSeek, Mistral, Qwen, …) are hidden by the
  §6.1 rule; an overlay row can unhide any of them with the right preset.
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
- **Gemini** (`google-vertex`): `transport = "vertex-gemini"` (§6.2).
- **Claude** (`google-vertex-anthropic`, and the thirteen Claude rows under
  `google-vertex`, which the converter gives the same preset): `transport =
  "vertex-anthropic"`: `anthropic` protocol, `:rawPredict` /
  `:streamRawPredict`, `body.anthropic_version = "vertex-2023-10-16"`,
  `model` omitted from the body because the path contains `{model}`. The
  `anthropic-version` header is still sent; Vertex tolerates it, and the
  protocol sets it in code (`anthropic/adapter.go:128-131`). Wire ids follow
  the converter's `@default` re-keying (§6.1): `claude-opus-5`,
  `claude-sonnet-4-5@20250929`.
- Vertex's OpenAI-compatible MaaS rows (Llama/DeepSeek/Kimi, per-model
  `npm: @ai-sdk/openai-compatible` and `api: …/endpoints/openapi`) resolve
  to `openai-chat` at that URL and keep the provider's `gcp-adc` auth
  (per-model overrides never change `Auth`, §4.3); the `{GOOGLE_VERTEX_ENDPOINT}`
  variable those templates introduce comes from `VarsEnv` like any other.

### 9.5 The Codex transport (`oauth-openai-codex`)

This is the one transport with behavior beyond auth, all of it existing code
in `llm/providers/openai/adapter.go` and `responses.go` that moves behind the
`Authenticator` + `RequestPreparer` pair (§8.1). Listed so nothing is lost:

- per-request headers from the request: `session-id`, `thread-id`,
  `x-client-request-id` (`setRequestHeaders`); `ChatGPT-Account-ID` from the
  token claims; `originator` and `User-Agent`;
- `x-openai-internal-codex-responses-lite: true` when `Caps.ResponsesLite`
  is set (without it the backend hangs);
- `metadata` is renamed to `client_metadata` and merged with
  `req.ClientMetadata` (the session puts the Codex installation id there,
  `session.go:1354-1358`; `responses.go:132-136`) in `PrepareRequest`, after
  the prune and the constants, so the `metadata` prune flag (on, inherited
  from `openai`) governs whether it is sent at all;
- `ResponsesLite` in the protocol builder: tools become a developer
  `additional_tools` input item (always present), instructions become a
  developer message after it, the top-level `instructions` is emptied and
  `tools` is not sent (`responses.go:41-75`); the `reasoning.context`,
  `text.verbosity`, and `parallel_tool_calls` constants come from the row's
  `body` (§6.2);
- `RequiresStreamingComplete()` is true (`requiresStreamingComplete`);
- the model allowlist is the registry's `openai-codex` row set (§6.2, §7.3);
- `evener openai login`, `status`, and `logout` default `--instance` to
  `openai-codex`, and the hub's OAuth sign-in eligibility keys on
  `Transport.Auth == oauth-openai-codex`, replacing the "`openai` instance
  silently becomes Codex when `auth/<name>.json` exists" rule
  (`adapter.go:108-112`, `app_auth.go:561-569`, `cmd/evener/openai_login.go:95`).

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
stream_options = false                   # this gateway rejects stream_options
[providers.work.models."glm-5.2-nvfp4"]
context_window    = 1048576
max_output_tokens = 131072
effort_values     = ["high", "max"]      # implies the effort control (§7.4)
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
`default_model`, `cheap_model` → `Provider`; `transport` (a preset name),
`base_url`, `host_rule`, `auth`, `auth_header`, `endpoint`,
`stream_endpoint`, `models_endpoint`, `count_tokens_endpoint`, `vars`, `body`
→ `Provider.Transport`; `protocol` → `Provider.Protocol`; every `Caps` field
by its snake_case name (`context_window`, `effort_values`,
`reasoning_controls`, `thinking_format`, `fields`, …) at the instance level
→ `Provider.Caps`, and inside `[providers.X.models."<id or glob>"]` →
`Model.Caps`, where `alias_of`, `wire_id`, `protocol`, `surface`, `headers`,
and the transport keys are also accepted. A top-level `[models."<glob>"]`
table is accepted in the curated overlay and in `providers.toml` alike.

Rules, enforced at load with errors that name the instance and key:

- names are lowercase, no slash, unique; `base` must name a registry id;
  `alias_of` must name an existing row; `transport` must name a preset
- `protocol` must be a registered protocol; `surface` one of the four values
- `auth` ∈ `bearer | header | none | gcp-adc | oauth-openai-codex`
- `fields` keys must be in the row's resolved-protocol prunable set (typo
  guard)
- `thinking_format`, `thinking_shape`, `max_tokens_field`, `cache_control`,
  `reasoning_field`, `host_rule`, `image_detail` are validated against their
  vocabularies; `reasoning_controls` entries must be `effort`,
  `budget_tokens`, or `toggle`
- `effort_values` entries non-empty; `"off"` rejected
- `$ENV` expansion in `api_key`, `credential_headers`, and `vars` uses
  today's `$NAME` / `${NAME}` / `$$` rules and happens at resolve time, so one
  instance's missing variable never blocks another; an unset variable there
  is a resolve error. In `headers` an unset variable **drops the header**
  (that is how the optional `OpenAI-Organization`/`OpenAI-Project` headers
  work); an empty-string value removes an inherited header of that name.
- **credential inheritance stops at the endpoint**: an instance that sets
  `base_url` to anything other than its base's `base_url` template does not
  inherit the base's `APIKeyEnv` or credentials-store entry; unless its
  `auth` is `none`, `gcp-adc`, or `oauth-openai-codex`, it must set
  `api_key`, `api_key_env`, or `credential_headers`, else resolving it fails
  with "no credential for <instance>" (today's `CredentialTag` returns `""`
  for exactly this shape, `providercfg.go:177-186`, so a gateway never
  receives the vendor key by accident). An instance that keeps the template
  and only supplies `vars` (the `bedrock` and `vertex` examples) inherits
  normally.
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
`make test-web` covers them. The spawn credential gate (`spawn.go:649-684`)
keys on `Transport.Auth`: `none` needs nothing; `oauth-openai-codex` is
satisfied by the instance's OAuth record; `gcp-adc` by the ADC variable or
file; everything else by a resolved key or credential header.

`model/list` returns `Resolved`-derived rows straight from the registry, so
`enrichModelDescriptors` and `applyInstanceModelOverride` are deleted.

`cmd/llmcall` uses `cmdutil.LoadClient` like everything else.

## 12. Errors

`llm.ClassifyHTTPError(status, headers, body)` extends today's
`errorFromHTTPStatus`/`classifyByMessage` (`llm/errors.go:262-375`), which
already match context-length, content-filter, quota, and not-found messages,
carry `RetryAfter`, mark `cyber_policy_violation` retryable, and treat 413 as
context length. It reads the structured body first (`error.code`,
`error.type`, `error.message` for OpenAI-shaped bodies; `error.type` for
Anthropic), then the status, then message patterns. New or changed rows:

| Signal | Kind |
|---|---|
| 429 or 403 whose body matches the usage-limit codes (`usage_limit_reached`, `insufficient_quota`, Kimi's quota 403; `llm/usagelimit.go`) | `KindQuotaExceeded`, non-retryable, carries the reset time (unchanged from today; listed so it is not lost) |
| other 429; OpenAI `rate_limit_exceeded` | `KindRateLimit` (retryable, honors `retry-after` and `x-ratelimit-reset-*`) |
| 413 (any wording, including Groq's per-request TPM ceiling, which recurs on retry); 400 matching `context length\|maximum context\|too many tokens\|reduce the length`; Anthropic `prompt is too long` | `KindContextLength`, non-retryable, message verbatim (unchanged) |
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
  row with no per-model `api` and a Mantle row), both `interleaved` shapes,
  every `reasoning_options` combination, `limit.input`, `@default` ids,
  tiers, a hidden provider, a provider with no `api`, non-Claude Bedrock
  rows, and rows with and without `family`. Table tests assert the
  converted records; a fuzz target feeds mutated JSON.
- **Merge and provenance**: property tests that a later layer's set field
  always wins regardless of level, that within a layer the order is
  provider → top-level glob → provider glob → row with longer patterns
  winning among globs, that `nil` always inherits, that map layers merge
  key-wise, that `Base` chains resolve models (and stop with
  `inherit_models = false`), that alias seeding imports facts only, that
  cross-protocol records do not inherit protocol-specific transport fields
  or `Fields`, that a dangling `alias_of` or unknown `transport` fails load,
  and that every provenance entry names a real layer.
- **Instances**: implicit-instance derivation from env and store fixtures
  (only `implicit = true` providers; gcp-adc from the env var and the
  well-known file, never the metadata server: the test asserts no HTTP
  client is constructed); default selection in all five branches;
  shadowing by explicit entries; the credential-inheritance stop for
  gateways and its non-application to `vars`-only instances.
- **Resolution**: golden `Resolved` records (JSON) for a fixed set of
  references: `groq/openai/gpt-oss-120b` (chat and responses),
  `openai/gpt-5.5`, `openai/gpt-5.6` (asserting `thinking_always_on`,
  `image_detail = omit`, no `prompt_cache_retention`), `openai-codex/gpt-5.6`
  (asserting `WireID = gpt-5.6-sol`, the lite body constants, inherited
  facts, and Codex's off-list intact), `anthropic/claude-sonnet-4-5[1m]`
  (asserting `WireID`, window 1000000, and beta header),
  `anthropic/claude-opus-4-6` with no effort (asserting the adaptive
  object), `anthropic/claude-opus-4-5` (asserting `budget+effort` and no
  always-on), `anthropic/claude-opus-5`, `azure/gpt55-prod` (asserting
  `WireID = gpt55-prod`), `azure/claude-opus-4-5` (asserting `budget+effort`
  via the top-level glob), `bedrock/anthropic.claude-sonnet-5` (asserting
  `StructuredOutput = false` from the provider pin over the row's `true`),
  `bedrock/global.anthropic.claude-opus-5` (asserting the verbatim wire id),
  `vertex/claude-opus-5` and `google-vertex/claude-opus-5` (asserting the
  `vertex-anthropic` endpoints, not Gemini's), `openrouter/anthropic/claude-opus-5`
  (asserting `Surface = anthropic`), `openrouter/minimax/minimax-m2.7`
  (asserting the enable object with no effort), `kimi-for-coding/k3`
  (asserting `budget+effort` and the anthropic surface),
  `anthropic/some-new-model` (unknown, asserting `Surface = anthropic`,
  adaptive shape, `max_tokens = 32000`), `work/glm-5.2-nvfp4` (asserting
  the effort control from `effort_values`), `local/whatever` (unknown
  model), `ollama/llama3:8b` (live-only, `OLLAMA_HOST=localhost` and `::1`).
- **Pruner**: every prunable path per protocol, nested paths, array-element
  paths, the `PrunablePaths()` contract (a builder test that every optional
  path a protocol emits is either in its declared set or governed by a
  named cap), unknown-key rejection at load, and build → prune → constants
  → prepare ordering (a constant on a pruned parent survives; the Codex
  rename runs last).
- **Wire captures**: the existing per-protocol golden bodies
  (`wire_capture_test.go`) are regenerated from `Resolved` inputs; new cases
  per cloud transport assert endpoint, stream endpoint, auth header name,
  body constants, model omission, and the Codex header set; an
  `openrouter/anthropic/claude-opus-5` multi-turn tool-use case asserts the
  signed `reasoning_details` round trip.
- **Continuation**: plan derivation from `Resolved` for OpenAI proper (on),
  Groq Responses (off), the `work` gateway (off, chat protocol), Azure (on),
  and Codex (family `codex`); request-fingerprint stability across two
  builds of the same request and across `Complete`/`Stream`.
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
  Responses, Bedrock `global.` routing, Kimi K3's thinking shape, and one
  request per cloud transport once step 4 lands.

## 14. Implementation order

The repo is a `go.work` workspace, so a deleted `llm` symbol breaks every
module at once. The order below keeps the tree green at every step by
building the new packages beside the old ones and cutting over last.

1. **`llm/registry`** (additive): types, converter, overlay with transports
   and glob rows, config loader, merge with provenance and alias seeding,
   instances, `Resolve`, `FindModel`, derived caps, `Prune`, embedded
   snapshot, cache/refresh with injected fetcher; `llm.ShapeRequest` and the
   interfaces of §8.1 in `llm`. `evener models list|inspect|refresh` reads
   it. Nothing else consumes it yet. The §13 golden `Resolved` records for
   `azure/…`, `bedrock/…`, and `vertex/…` are written here, so the data model
   is proven for the cloud providers before any cloud call is made. (~2,400
   lines + ~2,000 lines of tests + data.)
2. **New protocol packages** (additive): `chatcompletions` (consolidating
   `openaicompat`, `openai/chatcompletions.go`, and
   `internal/openaichat`), `responses`, and `Resolved`-driven types added
   inside the existing `anthropic` and `google` packages next to the old
   adapters, each with `BuildBody` and `PrunablePaths`; the authenticators,
   the Codex transport, the error classifier, wire captures and the adapted
   difftest. The old adapters keep running. (~1,500 new lines, mostly
   moved; ~3,000 lines of tests moved or regenerated.)
3. **Cut-over** (one commit series, tree green at its end): `llm.Client`
   routes by protocol with the override map and plans continuation from
   `Resolved` + `BuildBody`; `agent/provider.Profile` wraps `Resolved`; the
   twelve tag and catalog branches move per §7.5; hub `model/list`, instance
   CRUD, appwire types, generated TS, and frontend dialogs; `providers
   probe|add`; `llmcall` on `LoadClient`; credentials store takes the
   registry table; `envvars` roster, `cmdutil/seed.go`, `materialize.go`,
   `providercfg`, `model_catalog*.go`, the LiteLLM data, the wrapper
   packages, `openaicompat`, the old `openai` adapter, and the old
   `anthropic`/`google` adapter types deleted, along with the fuzz targets
   that exercise them (`llm/client_capabilities_fuzz_test.go`,
   `lcfg_config_surface_fuzz_test.go`, `client_config_edges_fuzz_test.go`,
   `core_contracts_fuzz_test.go`, `cmdutil/coverage_program_fuzz_test.go`,
   rewritten against the registry where they still have a subject);
   `docs/llm-providers.md` rewritten around §3–§10. (~net −4,000 lines
   including tests.)
4. **Cloud providers** (later phase, additive): the `gcp-adc`
   authenticator over `golang.org/x/oauth2/google` (~150 lines), plus the
   live-verified coverage of §13 for Azure, Bedrock, and Vertex against the
   overlay entries of §9. Nothing in steps 1–3 is provisional for them: the
   transport fields (`Auth`, `AuthHeader`, `BaseURL`/`Endpoint`/
   `StreamEndpoint` templates with `{VAR}`, `HostRule`, presets, the `-`
   endpoint sentinel, `Body` constants, model-level `Transport` overlays, the
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
- Merge precedence: layers in order; within a layer provider → top-level
  glob → provider glob → row; later layer wins regardless of level; alias
  seeds facts only; the live layer never overrides the user layer.
- Implicit instances only for the curated list; everything else is opt-in
  through `providers.toml`.
- Bedrock via Anthropic's Messages endpoint on `bedrock-mantle`, bearer
  token only. No SigV4, no event-stream framing, no AWS SDK dependency.
  Global routing ships now, as `global.` inference-profile model ids on the
  regional host (Jesse verified live, 2026-08-28).
- Surface is a model attribute derived from models.dev `family`, with a
  curated provider-level fallback.
- Adaptive thinking on Claude rows is always on, as today.
- 413 stays a context-length error; the classifier's new work is the
  structured body and the unrecognized-parameter hint.
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
§4, §7.5); the Codex transport's full behavior list and prunable max-tokens
(§6.2, §8.2, §9.5); Vertex `@default` wire ids (§6.1); Bedrock over the
Messages endpoint (§9.3); the pruner as a denylist over `PrunablePaths()`
with build-time caps (§8.2); continuation planning in `llm.Client` (§7.6);
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
(§6.2, §8.4); adaptive thinking always on for every adaptive row (§7.4,
§8.4); the live layer never overriding the user layer, plus `Reasoning` and
`ThinkingAlwaysOn` from live (§5); `Provider.Surface` fallback, the
corrected family rule, and instance-level `surface` (§4, §6.1, §6.2, §10);
the cross-protocol inheritance rule (§4.2, §9.4); `openai-codex` with
`inherit_models = false`, cross-provider `alias_of`, and `wire_id =
gpt-5.6-sol` (§4, §6.2); `openai-codex` in `default_order`, no-network
implicitness for `gcp-adc`, and the "no default instance" error (§5.1); the
`[1m]` rows limited to upstream-present bases with a dangling-alias load
error (§4.2, §6.2); `max_completion_tokens` pinned on `openai` with
`max_tokens` as the chat baseline (§6.2, §8.2); `FindModel` for
plugin-agent model declarations (§7, §7.5); cap-governed paths removed from
the prunable sets and the build → prune → constants order (§8.2);
credential inheritance stopping at the endpoint (§10); the continuation
request fingerprint via `BuildBody` and the full scope component list
(§7.6, §8.1); `Reasoning = false` semantics (§8.4); Azure and xAI Responses
extras (§6.2); the probe's 16-token minimum (§11.2); `StreamEndpoint` and
the google defaults (§4, §6.1, §9.4); implicit-instance hub semantics, the
login default, and the additive `InstanceEntry` (§5.1, §9.5, §11.3); the
anthropic 32000 fallback cap and the two Bedrock overlay rows (§6.2, §7.3);
`HostRule = ollama-host` (§6.2, §9.1); `ReasoningSummary` baseline `none`
(§4.1, §8.2); the three additional tag sites and the corrected web-search
row (§7.5); `req.ClientMetadata` (§9.5); the package names
`chatcompletions`/`responses` (§3, §8.1); `CostTier` (§4.1);
`davinci`/`babbage`/`sora` in the non-chat list (§5); `GEMINI_API_KEY`
ordering (§6.1, §6.2); the google temperature path (§6.1); `ThinkingShape`
limited to the anthropic surface (§7.4).

Revision 5 incorporated the third adversarial review (two reviewers, 26
scored findings, all accepted): alias as a facts-only seeding step with
defined precedence, removed from the lookup order (§4.2, §7.2); named
transport presets and the converter's `npm` → preset mapping so Vertex's
Claude rows under `google-vertex` get the rawPredict endpoints (§4.3,
§6.1, §9.4); the Bedrock Mythos row without a dangling alias (§6.2);
`kimi-for-coding` surface and thinking shape pins (§6.2, §7.4); glob rows
(top-level and per-provider) with a defined order, used for the Opus 4.5
hybrid across every provider, `gemini-3`, `gpt-5*`, `claude-*-5`,
`minimax/*`, `anthropic/*` (§4.1, §6.2); derived caps computed on the final
merged row, with `ThinkingAlwaysOn` only for a final adaptive shape (§7.4);
`@default` rows re-keyed by the converter (§6.1); verbatim wire ids for
prefix/dated/live hits (§7.2); split prompt-cache gates so Codex keeps
`prompt_cache_key` (§7.5, §6.2); the credential-inheritance rule keyed on a
`base_url` override with the keyless auth schemes exempt (§10); the curated
`implicit` list and current-instance preference in `FindModel` (§5.1, §6.2,
§7.5); pseudo-providers as `generic` (§3.1, §6.2); the variable resolution
order with curated `Vars` as defaults (§9.1); the cross-protocol rule
extended to instances and the `work` example corrected (§4.2, §10);
non-Claude Bedrock rows hidden by rule (§6.1, §9.3); the OpenRouter
`reasoning_details` round-trip rule (§8.4); effort implied by
`EffortValues`, provider-level `thinking_shape = adaptive` on `anthropic`,
and pass-through for unknown models (§6.2, §7.4, §8.4); the platform-side
half of `responsesLiteModel` on `openai`'s `gpt-5.6*` rows and the full
Codex lite description (§6.2, §8.3, §9.5); the `llm` ↔ `registry` import
direction with `ShapeRequest` and the interfaces in `llm` (§4, §8.1); the
`Client.Register` override map for test doubles and the fuzz-target list
(§8.1, §14); the spawn gate keyed on every auth scheme (§11.3); `ollama-host`
applied to the variable value (§6.2, §9.1); the concrete per-dialect
reasoning table including `clear_thinking: false` (§8.4); 413 kept as
context length and the §2 premise corrected (§2, §12); the `RequestPreparer`
hook for the Codex transport and its place in the assembly order (§8.1,
§8.2, §9.5); `stop` removed from the Responses set (§8.2); the
`anthropic-version` header left in place on Vertex (§9.4); Vertex MaaS rows
keeping `gcp-adc` (§4.3, §9.4); Anthropic `metadata` off at baseline (§8.2);
`status`/`logout` defaults (§9.5); `WebSearch` on `google-vertex-anthropic`
(§6.2); the storage-scope components (§7.6); the Bedrock golden on a row
that contests the pin and the `default_order` fallback (§5.1, §13); the
Azure Claude auth header (§9.2); the family-rule wording (§6.1).
