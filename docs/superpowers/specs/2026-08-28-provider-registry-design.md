# Provider Registry and Capability Resolution

**Date:** 2026-08-28 (revision 12, after ten adversarial review rounds)
**Status:** Draft for review
**Replaces:** the LiteLLM-vendored model catalog, `providercfg.CompatConfig`,
`openaicompat.ProviderQuirks` presets, the vendor wrapper adapter packages, the
behavior-tag split, env-seeded adapter factories, and `api_style`. This is a
**flag day** (Jesse, 2026-08-29): no runtime compatibility code, no
migration. An old-schema `providers.toml` fails to load with a pointer to
this document, old instance names and environment variables stop meaning
anything, and §14.1 lists what a user does once after upgrading.

## 1. Goals

1. **Any provider on the curated implicit list works with an API key and
   nothing else (the three cloud providers also need their resource,
   region, or project variables); any other provider models.dev knows about
   works with a one-line `[providers.X]` header.** Base URL, key variable,
   wire protocol,
   model list, context windows, output caps, pricing, effort ladders, and
   modalities all come from data.
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
| **Provider** | A named endpoint definition: id, display name, transport, default protocol, surface, family default, caps, models. Data. | `registry.Provider` |
| **Model** | A row under a provider: id, family, limits, cost, modalities, reasoning facts, caps, surface, optional protocol/transport override. Data. | `registry.Model` |
| **Surface** | The agent-facing vendor family a model was trained for: which doc files to read, which tool set and tool names to offer, which prompt sections apply. One of `openai`, `anthropic`, `google`, `generic`. A model attribute; the provider supplies it only for rows that carry no family at all and for synthesized rows. Surface never changes the wire shape (§7.4). | `registry.Model.Surface`, `registry.Provider.Surface` |

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
Responses still wants `AGENTS.md`, `apply_patch`, and the OpenAI append; a
Llama served by Azure wants neither; Kimi and MiniMax over the Anthropic
protocol want the Anthropic tool set because their native tool-call format
is Anthropic's (`profile.go:993-1001`). Today this is fused into the
behavior tag, which is why `openrouter-anthropic` exists as a separate
adapter (MiniMax's tool-call arguments corrupt over OpenRouter's chat
endpoint, `profile.go:997-1003`; under this design that route is a user
entry `base = "openrouter", protocol = "anthropic"` with a `minimax/*` glob
row pinning `surface = "anthropic"`, §4.2, §14.1). Surface is derived
from the model row (models.dev `family`) with overlay pins where a vendor
wants a surface its family does not imply, so it survives any routing; the
provider's surface applies only where the row says nothing at all (no
`family`, or a model released this morning on the `anthropic` instance).

## 4. Data model

All types live in a new leaf package `llm/registry`, which imports nothing
from `llm`. `llm` imports `registry`; the request-shaping helpers that need
`llm.Request` (`ShapeRequest`, the `Protocol`, `Authenticator`, and
`RequestPreparer` interfaces) live in `llm` itself (§8.1). Optional scalars
are pointers so that "unset" is distinguishable from `false`/`0` at every
layer.

```go
type Provider struct {
    ID            string            // registry id; the instance name for user entries
    Base          string            // id of the record this one layers on (curated and user layers)
    InheritModels *bool             // default true; false = start with no rows from Base
    Implicit      *bool             // curated: may become an instance from a credential alone (§5.1)
    Name          string            // display name
    Doc           string            // upstream documentation URL
    Protocol      string            // default protocol for models without their own
    Surface       string            // fallback surface for family-less rows and synthesized rows
    Family        string            // curated: family assumed for synthesized rows ("claude" on anthropic, amazon-bedrock, google-vertex-anthropic; §7.4)
    Transport     Transport
    APIKeyEnv     []string          // env vars consulted for the key, in order
    APIKey        string            // hand-authored literal or $ENV reference; scrubbed like CredentialHeaders
    Headers       map[string]string // constant request headers ($ENV refs allowed); non-secret
    CredentialHeaders map[string]string // secret headers; scrubbed from logs and hub rewrites
    Caps          Caps              // provider-level capability overlay
    Models        map[string]Model  // keyed by model id; keys may contain `*` (glob rows, §4.1)
    DefaultModel  string            // curated: what to pick when the user gives none
    CheapModel    string            // curated: the provider's cheap/fast model (bare id, same provider)
    Hidden        bool              // recomputed after merge and against the environment: no resolvable base URL (a {VAR} template whose variable is unset counts as none) or unregistered protocol; never inherited through Base
}

type Model struct {
    ID        string
    WireID    string     // id sent on the wire; defaults to ID
    AliasOf   string     // seed facts from another row ("id" in this provider, or "provider-id/id"); §4.2
    Family    string     // models.dev family, verbatim (claude-opus, gpt, kimi-k3, …); the key for wire-shape derivation (§7.4)
    Protocol  string     // override of the provider default
    Transport *Transport // field-wise overlay on the provider transport, or a named preset (§4.3)
    Headers   map[string]string // model-level constant headers (Anthropic beta headers)
    Surface   string     // openai | anthropic | google | generic
    Caps      Caps
    Status    string     // "", "beta", "deprecated"
    Hidden    bool       // recomputed after merge (§6.1 row rules); cleared when any layer, or a same-provider alias import, supplies a protocol or transport for the row
}

type Transport struct {
    Preset      string            // name of a `[transports.X]` record to start from (§4.3)
    Auth        string            // bearer | optional-bearer | header | none | gcp-adc | oauth-openai-codex
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

`optional-bearer` sends `Authorization: Bearer` when a key resolves and
nothing otherwise; the spawn gate treats it like `none`. It exists for
Ollama, whose `OLLAMA_API_KEY` is optional today
(`llm/providers/ollama/adapter.go:158-176`, `docs/ollama.md`).

```go
type Credential struct {
    Value  string // the key, token, or canonical serialization of the credential headers; empty when nothing resolved
    Source string // api_key | credential_headers | store | env:<VAR> | oauth | adc | none (the §10 order)
}
```

The continuation scope (§7.6) never stores `Value`; `llm.Client` HMACs it
with the state-dir `ContinuationHasher`, as `authScopeForAPIKey` does today
(`openai/adapter.go:278-290`).

### 4.1 Caps

One flat struct shared by every protocol. Fields a protocol does not use are
ignored by it. `Fields` carries "send this optional wire field or not"; the
explicit fields are the transforms that cannot be expressed that way.

```go
type Caps struct {
    // Model facts. Catalog-sourced; user layers may correct them. This block
    // plus Surface and Family is what an alias row inherits (§4.2).
    ContextWindow     *int       // input budget the agent plans against
    MaxOutputTokens   *int       // catalog/live values ≥ ContextWindow are cleared (§7.4)
    Tools             *bool
    StructuredOutput  *bool      // json_schema accepted; false downgrades to json_object at build time
    Sampling          *bool      // false: the model rejects temperature/top_p; the builder omits both (models.dev `temperature: false`)
    Reasoning         *bool      // false: no reasoning controls, no thinking replay, empty effort list
    ReasoningControls []string   // subset of effort, budget_tokens, toggle (models.dev names); replace on overlay
    EffortValues      []string   // wire-spelled ladder, ascending; replace on overlay; non-empty implies effort
    DefaultEffort     *string    // the effort the model runs at when the request omits one (§7.4)
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
    ThinkingDisplay   *string    // anthropic adaptive: "" | summarized; derived when unset (§7.4)
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

**Merge order.** Resolution of a reference proceeds through the layers in
the effective order **1 → 2 → 3 → live → 4**, then the derivations of §7.4.
The live layer (§5) sits between the curated overlay and the user config so
that it overrides catalog facts but never a field the user set; it
contributes only its advertised facts. Within one layer, for the reference
being resolved, the order is: the provider-level `Caps`; then every
top-level glob row (`[models."<glob>"]`, applied to all providers) whose
pattern matches; then every provider glob row
(`[providers.X.models."<glob>"]`) that matches; then the exact row entry for
the id the lookup matched (§7.2). Glob patterns use `*` only and match the
full id, case-sensitively like every other id comparison (models.dev's
MiniMax ids are mixed-case, `MiniMax-M2.7`); when several match they apply
in order of pattern length (shorter first, so the more specific pattern
wins), then lexically. A glob is tested against the **reference** and, when
the lookup matched a different id (a stripped prefix or date, §7.2) or the
row is an alias (§4.2), against that matched or target id as well, with
target-matching globs applied before reference-matching ones. Because the
glob test uses the reference, glob rows reach live-only and synthesized
ids. Across layers, later always wins regardless of level: a layer-3
provider glob or provider-level pin beats a layer-1 row fact, and a layer-4
instance-level `context_window` rewrites every row of that instance (the
documented meaning of an instance-wide overlay; put it on the row when only
one model is meant). A pointer or scalar set at a later step replaces; a
`nil` inherits. Slices (`EffortValues`, `InputModalities`,
`ReasoningControls`) and `Cost`/`FinishReasonMap` replace wholesale.
`Fields`, `ChatTemplateKwargs`, `Transport.Vars`, `Transport.Body`, and
`Headers` merge key-wise. This is one reflect-driven function (~120 lines)
that also records `map[fieldPath]"layer/level[/glob]"` provenance.

### 4.2 Alias and base inheritance

`AliasOf` is not a lookup step; it is a seeding rule. The target row is
resolved first through layers 1–4 (its own live facts, if any, apply to it
independently). Its final **facts** (the "Model facts" block of `Caps`,
`Surface`, and `Family`) become the alias row's row-level values at "layer
0", before any real layer applies. A **same-provider** alias that sets
neither `protocol` nor `transport` also takes the target's row-level
`Protocol` and `Transport` (so an Azure Foundry deployment alias of a Claude
row lands on the Anthropic endpoint, §9.2); a cross-provider alias never
imports them (`anthropic/claude-mythos-5` seeds facts from
`azure/claude-mythos-5` and stays on the `anthropic` provider's transport).
Everything else (`Fields`, `Headers`, the structural and transform caps,
`WireID`) is never imported from another row: the alias row gets them from
its own provider and its own layers like any row, and glob pins that match
the target id reach it through §4.1's target-matching rule.
Consequences: `openai-codex/gpt-5.6` inherits GPT-5.6's window, cost, and
effort ladder from `openai/gpt-5.6` but keeps Codex's provider-level
`fields` off-list and its own provider-level transport; `claude-sonnet-4-5[1m]`
inherits the target's facts and then its own `context_window = 1000000` at
layer 3 wins over the inherited 200000; `azure/claude-prod` aliased to
`claude-opus-4-5` gets the Foundry transport and the `*claude-opus-4-5*`
top-level glob's `budget+effort`; `amazon-bedrock`'s provider-level
`WebSearch = false` is untouched by an alias because `WebSearch` is not a
fact. Aliases are one hop: a target that is itself an alias, a missing
target, or a cycle is a load error (`alias_of` may name `"id"` in the same
provider or `"provider-id/id"`).

`Base` inheritance is the same at every layer: the record's merged form is
its base's merged form with the record's own fields overlaid, models
included unless `InheritModels = false`. An explicit `base` always wins over
a name match (`[providers.openai] base = "openai-codex"` is a Codex instance
named `openai`); `base` names resolve against the curated registry (layers
1–3) only, never against user instances, so chains cannot loop through the
user layer, and an instance's `ProviderID` is the first id reached through
`base` (so `[providers.openai] base = "openai-codex"` has `ProviderID =
openai-codex`), else the instance's own name when it is a registry id.
`Hidden` is not inherited; it is recomputed after the merge at both
levels, against the environment. `Resolve` on a hidden row or a hidden
provider still succeeds (the user named it): a template variable that does
not resolve is left unsubstituted in `Transport.BaseURL` with `Warnings:
unresolved variable <NAME>`, exactly parallel to `Warnings: no credential`
(§5.2), and the §9.1 error naming the variable and the instance fires at
the first request. `Hidden` therefore governs listings and implicitness
(§5.1, §11.1), never resolvability, and `evener models inspect
azure/gpt55-prod` works with `AZURE_RESOURCE_NAME` unset. A dangling
`alias_of` in the **curated** layer (upstream dropped the target between
releases; the runtime cache of §6.4 can do that) degrades the row to
`Hidden` with `Warnings: dangling alias` instead of failing the load; a
dangling alias in the user layer, and any dangling alias seen by the refresh
script, is an error.

**Cross-protocol rule.** Any record (instance or model) whose resolved
protocol differs from the record it inherits from does **not** inherit the
protocol-specific transport fields (`Endpoint`, `StreamEndpoint`,
`ModelsEndpoint`, `CountTokensEndpoint`, `Body`) or the inherited `Fields`;
it starts from its own protocol's defaults and baseline, then takes its own
`Transport` (or preset) and its own `Fields`. Everything not in that list is
inherited as usual: `Auth`, `AuthHeader`, `BaseURL`, `HostRule`, `Vars`,
`VarsEnv`, `Headers`, `CredentialHeaders`, and credentials. This keeps a
`[providers.work] base = "openai", protocol = "openai-chat"` instance from
counting tokens against `/responses/input_tokens`, keeps `google-vertex`'s
Claude rows off the Gemini endpoint while they still resolve
`{GOOGLE_VERTEX_HOST}` through the provider's host rule (§9.4), and makes
`[providers.openrouter-anthropic] base = "openrouter", protocol =
"anthropic"` reach OpenRouter's `/v1/messages` with OpenRouter's key.

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
    ProviderID string      // first registry id reached through Base, else the instance's own name (§4.2)
    Protocol   string
    Surface    string
    Transport  Transport   // vars substituted, $ENV expanded, host rule applied, endpoints filled
    ModelID    string      // the reference as given
    WireID     string      // id sent on the wire
    Model      Model       // merged row (may be synthesized; see §7.3)
    Caps       Caps        // fully merged and derived (§7.4); every prunable path of the protocol present in Fields
    Headers    map[string]string
    Credential Credential  // resolved key/token source, never logged; may be empty (§5.2)
    Provenance map[string]string
    Warnings   []string    // "model not in catalog", "protocol unverified", "no credential", "hidden", …
}
```

`Resolved` is serializable (minus `Credential`) and is what `evener models
inspect` prints and what the API attempt log records in summary form
(protocol, base URL, pruned fields, provenance for anything the user
overrode).

## 5. Layers

Every layer is a set of `Provider` records in the same schema, applied in
the effective order of §4.1 (1 → 2 → 3 → live → 4).

| # | Layer | Source | Refreshable |
|---|---|---|---|
| 1 | **Upstream snapshot** | `llm/data/models.dev.json.gz`, the raw models.dev `api.json`, converted at load (§6.1) | `make refresh-model-catalog` |
| 2 | **Upstream cache** | `<state-root>/catalog/models.dev.json` + `.meta` (etag, fetched-at), same converter | background, 24h (§6.4) |
| 3 | **Curated overlay** | `llm/data/providers_overlay.toml`, hand-maintained | with the release |
| 4 | **User config** | `<config-root>/providers.toml` (`cmdutil.DefaultConfigRoot()`, `$XDG_CONFIG_HOME/evener`, or the `EVENER_PROVIDERS_CONFIG` path, called `<path>` below; `credentials.toml` is its sibling, as today, `cmd/evener-hub/main.go:222-243`; OAuth records stay under the state root at `auth/<instance>.json`, `auth/openai/storage.go:79-99`) | by the user or hub |
| live | **Live listing** | the instance's `ModelsEndpoint` | per process, cached |

Layer 2 replaces layer 1 wholesale when its fetched-at timestamp is newer
than the embedded snapshot's (recorded by the refresh script in
`llm/data/models.dev.meta.json`); they are never merged. Layers 3 and 4
overlay field-wise.

The live layer establishes existence of models the catalog lacks and
supplies the model facts it explicitly advertises (`Tools`,
`InputModalities`, `ContextWindow`, `MaxOutputTokens`, `EffortValues`,
`Cost`, `Reasoning`) plus one structural cap, `ThinkingAlwaysOn`, set only
when OpenRouter's `reasoning.mandatory` is `true` (today's rule,
`openaicompat/models.go:118-120`; a listed `false` sets nothing), with
`Provenance = "live"`. Sitting after layer 3 and before layer 4, it
overrides catalog and curated facts (today's rule that OpenRouter's
`supported_parameters` is authoritative over the catalog,
`llm/model_catalog.go:297-330`) but **never a field the user layer set**
(today's `WithLiveModelInfo` rule that `providers.toml` rows beat live
enrichment). It never touches any other wire-shaping cap. Live rows whose
id matches the non-chat pattern list (`embedding`, `whisper`, `tts`,
`dall-e`, `moderation`, `audio`, `transcribe`, `image`, `realtime`,
`davinci`, `babbage`, `sora`; one list in `registry`, replacing
`nonChatModelSubstrings` and `skipOpenAIModel`) are dropped.

### 5.1 Instances

An **instance** is a named, usable provider. Instances come from two places:

- **Explicit**: every `[providers.X]` entry in `providers.toml`.
- **Implicit**: every registry provider marked `implicit = true` in the
  curated overlay (§6.2) that is not shadowed by an explicit entry of the
  same name, is not `Hidden` after layering against the environment (§4),
  and whose credential resolves without network access. The credential
  test depends on the transport's auth scheme, never on inherited key
  variables from another scheme: `bearer`/`header` need a credentials-store
  entry for the id or one of the provider's own `APIKeyEnv` variables set;
  `oauth-openai-codex` needs the instance's OAuth record (§9.5) and nothing
  else (`openai-codex` pins `api_key_env = []`, so `OPENAI_API_KEY` alone
  yields `openai` and not a dead Codex default); `gcp-adc` needs the
  `GOOGLE_APPLICATION_CREDENTIALS` variable or the well-known ADC file (the
  metadata-server probe in `FindDefaultCredentials` is never run at load; it
  runs at first request); `none` and `optional-bearer` need nothing beyond
  the base URL resolving (Ollama's does by default; a pseudo-provider's
  only when its `<ID>_BASE_URL` is set). "Not `Hidden`" means the base URL
  template resolves, so the cloud providers need their variables as well as
  their credential: `google-vertex*` need `GOOGLE_VERTEX_PROJECT` and
  `GOOGLE_VERTEX_LOCATION`, `azure*` need `AZURE_RESOURCE_NAME`, and
  `amazon-bedrock` needs `AWS_REGION`; the ADC file alone makes no Vertex
  instance. Nothing else becomes an instance
  from an environment variable alone: `GITHUB_TOKEN`, `HF_TOKEN`, or
  `DATABRICKS_TOKEN` in a shell must not conjure a `github-copilot`,
  `huggingface`, or `databricks` instance.

Implicit instances are computed identically by every process from the same
inputs; the hub no longer materializes `providers.toml` at startup and passes
nothing to children beyond `EVENER_PROVIDERS_CONFIG` when a file exists. The
hub lists implicit instances flagged *from environment*; editing one or
making it the default writes a shadowing explicit entry; removing one is
refused with a message naming the variable or record that makes it exist.

The **default instance** is `default` from `providers.toml` when set; else
the first instance, explicit or implicit, that has a `DefaultModel` in this
ranking: the curated `default_order` (§6.2), where an explicit entry whose
name is a curated implicit id keeps that id's position (a shadowing entry
the hub or `probe --write` creates changes fields, not rank, so editing
`groq` never promotes it over `anthropic`), followed by every other explicit
instance by sorted name. This is a change from today's rule (the file
loader picks the first sorted name with no eligibility skip,
`providercfg/load.go:257-262`), disclosed in §14.1. A `default` that names
neither an explicit instance
nor a curated implicit id is a load error (a non-implicit registry id such
as `huggingface` names no instance without a `[providers.huggingface]`
entry); a `default` that names a curated implicit id whose credential does
not resolve in this environment is a warning that falls through to the
chain above (so `evener models inspect` and a shell without the key keep
working, §5.2); a `default` naming a curated implicit id that is hidden for
an unset variable is the same warning. An explicit entry that sets
`default_model` on a provider
that has none (`[providers.ollama] default_model = "llama3:8b"`) is
eligible like any other; the user asked for it. Every provider on the
implicit list except `azure` (deployment names) and `ollama` (live only)
carries a curated `DefaultModel`, so an exported `XAI_API_KEY` alone yields
a working default. `openai-codex` precedes `openai` in `default_order`,
preserving today's "stored OAuth beats API key" choice. When no instance
exists at all, resolving a bare model id fails with "no default instance:
set `default` in providers.toml or export a provider key"; when instances
exist but none has a `DefaultModel`, the error names them ("azure has no
default model; pass `azure/<deployment>` or set `default`").

### 5.2 Resolving without a credential

Instance existence and resolvability are separate. `Resolve` succeeds for
any explicit instance, any implicit instance, and any curated `implicit`
provider id even when no credential resolves; the record carries `Warnings:
no credential` (omitted for `none` and `optional-bearer` instances) and an
empty `Credential`, and the "no credential for <instance>" error is raised
at the first request. This is what lets `evener models inspect
openai/gpt-5.5` work on a machine without a key, and what the test suites
rely on: `registry.Load` takes an option `WithInstances(map[string]Provider)`
that injects named test instances (`openai`, `work`, `tiny`, `compat-x`, …)
before layering, so `agent/profile_testhelpers_test.go`'s
`resolveTestProfile` becomes a call to `Resolve` against injected
instances.

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
| `api` | `Transport.BaseURL`, trailing slash trimmed; `${VAR}` placeholders become `{VAR}` and `VAR` is added to `Transport.VarsEnv` keyed by itself (the same rule applies to placeholders in per-model `api` templates, which is where `{GOOGLE_VERTEX_ENDPOINT}` comes from) |
| `env[]` | entries referenced by a `{VAR}` template → `VarsEnv`; remaining entries matching `*_API_KEY`, `*_KEY`, `*_TOKEN`, `*_PAT` → `APIKeyEnv`; anything left → `VarsEnv` keyed by its own name. The heuristic misfires on `AWS_SECRET_ACCESS_KEY` and would order Google's keys `GOOGLE_API_KEY` first; the curated overlay pins `api_key_env` for `amazon-bedrock`, `google` (`GEMINI_API_KEY` first, as today), the Vertex providers (empty), and the providers whose evener variable differs from models.dev's (§6.2) |
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

Row-level hiding (`Model.Hidden`, recomputed after merge and cleared when
any layer or a same-provider alias import supplies a `protocol` or
`transport` for the row): an `amazon-bedrock` row with no per-model
override whose id, after the region prefix, does not start with
`anthropic.` (the Messages endpoint serves Claude only, and the seven OpenAI
rows without a Mantle override, such as `global.openai.gpt-5.6-sol`, would
otherwise resolve to it); a `google-vertex` row whose id contains `/` and
carries no per-model `api` (models.dev lists `openai/gpt-oss-*-maas` without
the MaaS template); and every row of a `Hidden` provider. Rows whose
`modalities.output` lacks `text` are dropped outright, not hidden.

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
| `limit.output` (0 → unset) | `MaxOutputTokens` (cleared at derivation when ≥ `ContextWindow`, §7.4) |
| `cost.{input,output,cache_read,cache_write,tiers[]}` | `Cost` |
| `tool_call`, `structured_output` | `Tools`, `StructuredOutput` |
| `reasoning` | `Reasoning` |
| `reasoning_options[].type` (a list; 752 rows carry two or three, 1237 text rows carry an empty list) | `ReasoningControls` as the set of types present, models.dev spelling (`effort`, `budget_tokens`, `toggle`); the `effort` entry's `values` → `EffortValues` verbatim, `none` included — it is models.dev's name for the off level, and a row that lists it is one the user can turn thinking off on (§8.4); values outside evener's vocabulary (`default`, `null`, one descending ladder) are kept verbatim; `ClampReasoningEffort` skips entries it cannot rank and passes the request through, as today (`llm/types.go:699-733`) |
| no models.dev field | `DefaultEffort` — models.dev states nothing about what a model does when the effort is omitted, so every value is curated (§6.2) or live (the Codex backend's `default_reasoning_level`) |
| `temperature: false` | `Sampling = false`, a fact (so alias rows inherit it); the builder then omits the protocol's temperature and top-p paths (`temperature`/`top_p` on the OpenAI protocols and anthropic, `generationConfig.temperature`/`generationConfig.topP` on google) regardless of `Fields` |
| `modalities.input` | `InputModalities` |
| `knowledge` | `KnowledgeCutoff` |
| `status` | `Model.Status` |
| `interleaved` (boolean on 65 rows, `{field: …}` on 893) | `ReasoningField` from the object form only |
| `family` | `Model.Family` verbatim, and `Surface`: `claude*` → `anthropic`; (`gpt*` except `gpt-oss*`), `o`, `o-mini`, `o-pro` → `openai`; `gemini*`, `gemma*` → `google`; any other family → `generic`; no `family` at all (666 rows) → unset, so the provider's `Surface` applies (§6.2), else `generic`. A Llama on Azure or a `gpt-oss` on Bedrock therefore stays `generic` wherever it is served |
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
- **Base URLs and their environment overrides.** Every implicit provider's
  `base_url` is a template `{BASE_URL}` with the curated default in `vars`
  and `vars_env = { BASE_URL = "<ID>_BASE_URL" }`, so an environment
  variable overrides the URL (§9.1 order) and a gateway or proxy needs no
  config file: `OPENAI_BASE_URL` (default `https://api.openai.com/v1`),
  `ANTHROPIC_BASE_URL` (`https://api.anthropic.com/v1`), `GOOGLE_BASE_URL`
  (`https://generativelanguage.googleapis.com/v1beta`), `GROQ_BASE_URL`
  (`https://api.groq.com/openai/v1`), `XAI_BASE_URL` (`https://api.x.ai/v1`),
  `CEREBRAS_BASE_URL` (`https://api.cerebras.ai/v1`), `MISTRAL_BASE_URL`
  (`https://api.mistral.ai/v1`), `TOGETHERAI_BASE_URL`
  (`https://api.together.ai/v1`, the models.dev id is `togetherai`), and for
  the providers whose URL models.dev supplies (`openrouter`, `deepseek`,
  `zai`, `zai-coding-plan`, `moonshotai`, `kimi-for-coding`, `minimax`) the
  same `<ID>_BASE_URL` name with models.dev's `api` as the default. The
  variable's value is the full base URL **including the version segment**
  (`https://proxy.example/v1`), matching models.dev's convention (§6.1);
  today's version-less values (`ANTHROPIC_BASE_URL`, `GEMINI_BASE_URL`,
  `MINIMAX_BASE_URL`, `KIMI_CODING_BASE_URL`, and `OPENAI_BASE_URL` in its
  documented `https://api.openai.com` form, which `joinBaseURLDedupV1`
  tolerates today) and the `KIMI_*`/`GLM_*`/`OPENAI_CHATGPT_BASE_URL` names
  stop working (flag day, §14.1). `azure` and `azure-cognitive-services` (§9.2), `amazon-bedrock`
  (§9.3), `google-vertex` and `google-vertex-anthropic` (§9.4) keep their own
  templates.
- **Key variable pins** where the converter's heuristic (§6.1) gives the
  wrong answer: `google` → `api_key_env = [GEMINI_API_KEY, GOOGLE_API_KEY]`;
  `amazon-bedrock` → `[AWS_BEARER_TOKEN_BEDROCK]`; the Vertex providers →
  empty. Everything else uses models.dev's variables verbatim
  (`MOONSHOT_API_KEY`, `KIMI_API_KEY` for `kimi-for-coding`, `ZHIPU_API_KEY`
  for `zai`, …).
- **Implicit list** (`implicit = true`), which is also `default_order`, in
  this order: `anthropic`, `openai-codex`, `openai`, `google`, `groq`, `zai`,
  `deepseek`, `openrouter`, `xai`, `mistral`, `cerebras`, `togetherai`,
  `moonshotai`, `kimi-for-coding`, `minimax`, `zai-coding-plan`,
  `google-vertex-anthropic`, `google-vertex`, `amazon-bedrock`, `azure`,
  `ollama`. Everything else needs a `providers.toml` entry. Each of these
  except `azure` and `ollama` carries a `default_model` and a `cheap_model`
  chosen from the snapshot when the overlay is authored (the flagship and
  the cheapest tool-capable text row), so it can be the default instance
  (§5.1).
- **Provider surfaces and families** (used only for family-less and
  synthesized rows, §6.1, §7.4): `anthropic`, `amazon-bedrock`,
  `google-vertex-anthropic` → `surface = "anthropic"`, `family = "claude"`;
  `kimi-for-coding`, `minimax` → `surface = "anthropic"` (no family);
  `openai`, `openai-codex`, `azure` → `openai`; `google`, `google-vertex` →
  `google`. Everything else, including the three pseudo-providers, is
  `generic` with no family.
- **OpenAI** (`openai`): `fields` on: `store`, `prompt_cache_key`, `include`,
  `truncation`, `safety_identifier`, `service_tier`, `previous_response_id`,
  `conversation`, `max_tool_calls`, `background`, `metadata`;
  `prompt_cache_retention = true` on the `gpt-5*` and `gpt-4.1*` glob rows and
  `prompt_cache_retention = false` explicitly on the `gpt-5.6*` glob row
  (applied after `gpt-5*` by §4.1's length rule; GPT-5.6 moved to
  `prompt_cache_options.ttl` with different semantics and today's builder
  skips the legacy field there, `responses.go:112-118`). `MaxTokensField =
  "max_completion_tokens"` (so `base = "openai"` gateways over chat inherit
  the spelling OpenAI reasoning models require). `StrictTools = true`,
  `WebSearch = true`, `ReasoningSummary = "auto"` with `"detailed"` on the
  `gpt-5*` and `gpt-6*` glob rows, `ImageDetail = "original"` on
  `gpt-5.4*`/`gpt-5.5*`/`gpt-6*`, `CountTokensEndpoint =
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
  `Transport.Auth = "oauth-openai-codex"`, `api_key_env = []` (the OAuth
  record is the only credential; an inherited `OPENAI_API_KEY` must not make
  this instance implicit, §5.1), `base_url = {BASE_URL}` with the default
  `https://chatgpt.com/backend-api/codex` and `vars_env = { BASE_URL =
  "OPENAI_CODEX_BASE_URL" }`, `ModelsEndpoint =
  /models?client_version=0.0.0`, `CountTokensEndpoint = "-"`, `headers = {
  OpenAI-Organization = "", OpenAI-Project = "" }` (removes the inherited
  platform headers; today's Codex adapter is built without org/project,
  `adapter.go:169-180`). `fields` off for everything the backend rejects:
  `temperature`, `top_p`, `max_output_tokens`, `previous_response_id`,
  `conversation`, `service_tier`, `safety_identifier`,
  `prompt_cache_retention`, `truncation`, `max_tool_calls`, `background`;
  `prompt_cache_key` and `metadata` stay on (inherited from `openai`;
  today's builder sends the cache key on Codex, `responses.go:97-99`, as
  does Pi, and §9.5 covers `metadata`). Rows, each with `alias_of` pointing
  at the matching `openai/…` row for facts: `gpt-5.6` with `wire_id =
  "gpt-5.6-sol"` (the backend rejects the bare slug,
  `responses.go:1030-1036`), `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`.
  A `gpt-5.6*` glob row on this provider sets `responses_lite = true`,
  `thinking_always_on = true`, `image_detail = "omit"`, `reasoning_summary =
  "detailed"`, and `body = { "reasoning.context" = "all_turns",
  "text.verbosity" = "low", parallel_tool_calls = false }` (today's Codex
  shaping, `responses.go:41-75,153-158,177-186`). Only these rows are valid
  on this transport: an unknown id is an error, not a warning (§7.3). The
  transport's own behaviors are enumerated in §9.5.
- **Anthropic**: `CacheTTL`, `WebSearch = true`, and the corrections to
  upstream: `claude-sonnet-4-5` and `claude-sonnet-4-5-20250929` pinned to
  `context_window = 1000000`, agreeing with models.dev. Anthropic's
  context-window page, fetched 2026-08-28, said Sonnet 4.5 was 200k and the
  rows were pinned down to match; live verification on 2026-08-31 overrode
  that — `/v1/models` reports `max_input_tokens = 1000000` for both spellings
  with and without the `context-1m-2025-08-07` beta, so the 1M window is GA
  and the pin now records it. models.dev already has Opus 4.5 and Haiku 4.5
  at 200000; the same Sonnet 4.5 rows on gateways are left to those gateways'
  live listings. **`[1m]` rows** where the base row's window is not the 1M
  one: `claude-opus-4-5[1m]` and `claude-opus-4-5-20251101[1m]`, each
  `alias_of` and `wire_id` naming the base row, `context_window = 1000000`,
  and `headers = { anthropic-beta = "context-1m-2025-08-07" }` — Opus 4.5's
  1M is still a beta opt-in (`/v1/models` reports 200000 for it either way).
  `claude-sonnet-4-5[1m]` and `claude-sonnet-4-5-20250929[1m]` survive as
  **pure alias rows** — no window, no header — solely to keep the `[1m]`
  spelling resolvable and fold it onto the base row (§7, `canonicalModelID`);
  the live test in §13 pins that they still resolve and are accepted. The
  4.6+ rows are 1M natively, so `claude-opus-4-6[1m]` is not a row and a
  saved ref spelled that way fails to resolve (flag day, §14.1).
  Two Mythos rows models.dev lacks under `anthropic`: `claude-mythos-5` with
  `alias_of = "azure/claude-mythos-5"` (facts only; the transport stays
  `anthropic`'s, §4.2), and `claude-mythos-preview` with `context_window`,
  `max_output_tokens`, `cost`, `reasoning = true`, and `effort_values`
  carried over from the LiteLLM snapshot's entry before that file is
  deleted, and `family = "claude-mythos"` so §7.4 derives the adaptive
  shape; the refresh report flags both when upstream adds them. No
  provider-level thinking pin: shapes, always-on, and display are derived
  per row (§7.4), and the provider `family = "claude"` covers an
  uncataloged id. `Sampling = false` on Claude 5 rows is already true from
  models.dev `temperature: false`; listed here only if upstream regresses. The refresh script lists every pinned row whose
  upstream value changed so pins get re-examined.
- **Top-level glob rows** (`[models."<glob>"]`, applied to every provider):
  `"*claude-opus-4-5*"` → `thinking_shape = "budget+effort"` (models.dev
  lists `effort` + `budget_tokens` for both Opus 4.5 and Opus 4.6, and only
  4.6 takes the adaptive body; this covers the rows on `anthropic`, `azure`,
  `azure-cognitive-services`, both Vertex providers, every `amazon-bedrock`
  spelling, and any alias of them). `"*gemini-3*"` →
  `multimodal_tool_results = true`. `default_effort = "high"` on each
  adaptive Claude generation (4.6, 4.7, 4.8 and the 5 family), two globs
  apiece because OpenRouter spells the versions with dots where Anthropic
  uses dashes and `matchGlob` matches literal substrings. This is a fixed
  list, not a rule: **a new adaptive Claude id ships at the `medium` fallback
  of §7.4 until a glob for it is added here.**
- **Google** (`google`, `google-vertex`, `google-vertex-anthropic`):
  `WebSearch = true` on all three (Anthropic's Vertex page lists the web
  search tool as supported).
- **Kimi**: `moonshotai` and `moonshotai-cn` (openai-chat): `Fields` off for
  `temperature`/`top_p`/`frequency_penalty`/`presence_penalty`,
  `StructuredOutput = false`, `ToolChoiceForcing = false`.
  `kimi-for-coding` (anthropic): `Headers["User-Agent"] = "claude-cli/2.1.177
  (external, cli)"`; a `"*"` glob row with `surface = "anthropic"` (today's
  kimi-anthropic profile uses `CLAUDE.md` and the Anthropic tool set,
  `profile.go:969-990`; a row-level pin because the family rule of §6.1
  would otherwise yield `generic` for `kimi-k2`/`kimi-k3`) and
  `thinking_shape = "budget"` (today's path for `kimi-for-coding` and
  `-highspeed`); the `k3*` glob row overrides to `thinking_shape =
  "budget+effort"` (today's shape for `k3`/`k3-256k`:
  `supports_effort_parameter` without adaptive,
  `evener_model_catalog_overrides.json`).
- **MiniMax** (`minimax`, `minimax-cn`, `minimax-coding-plan`,
  `minimax-cn-coding-plan`; anthropic protocol): a `"*"` glob row with
  `surface = "anthropic"` and `thinking_shape = "budget"` (today's
  `newMiniMaxProfile`, `profile.go:948-965`, uses the Anthropic tool set
  because MiniMax's native tool-call format is Anthropic's, and the wrapper
  goes through the anthropic adapter's budget path).
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
  "reasoning_details"` (today's `reasoning: {enabled: true}` when no effort
  is set plus the `reasoning_details` replay path, `profile.go:1283-1292`,
  `openaicompat/request.go:22-33,381-411`; with an effort set the dialect
  now sends `reasoning: {effort}` instead, which OpenRouter documents as
  equivalent to `enabled` at that effort).
- **Bedrock** (`amazon-bedrock`): §9.3, with the `StructuredOutput = false`
  and `WebSearch = false` pins on the provider glob row `"*anthropic.*"`
  (the Messages endpoint is what lacks them; the nine Mantle OpenAI rows
  keep their own values), plus two rows Anthropic's model table has and
  models.dev lacks: `anthropic.claude-haiku-4-5` with `alias_of =
  "anthropic.claude-haiku-4-5-20251001-v1:0"`, and
  `anthropic.claude-mythos-preview` with `alias_of =
  "anthropic/claude-mythos-preview"` (the overlay row above; facts only).
- **Ollama** (`ollama`, not in models.dev): `Protocol = openai-chat`,
  `BaseURL = {OLLAMA_HOST}` with `VarsEnv = { OLLAMA_HOST = OLLAMA_HOST }`,
  `vars = { OLLAMA_HOST = "localhost" }` as the curated default, `HostRule =
  ollama-host` (§9.1, which also honors `OLLAMA_BASE_URL` first, as today's
  `ResolveOllamaBaseURL` does), `Auth = optional-bearer`, `api_key_env =
  [OLLAMA_API_KEY]`, no models (live only), no `DefaultModel`, and a
  provider-level `context_window = 131072` (today's 128K compat default,
  `profile.go:1256-1257`) so compaction still fires for a live-only model
  whose listing reports no window; the LiteLLM `ollama/*` rows (8192 for
  `llama3.1`, …) are gone, so a user who wants the real window sets it on
  a row (`[providers.ollama.models."llama3.1*"] context_window = 8192`,
  disclosed in §14.1). The pseudo-providers carry the same provider-level
  default.
- **Pseudo-providers** `openai-compatible`, `anthropic-compatible`,
  `google-compatible`: protocol only, `generic` surface, no models. Each
  has `base_url = {BASE_URL}` with no default and `vars_env = { BASE_URL =
  "<ID>_BASE_URL" }`, `auth = optional-bearer`, `api_key_env =
  [<ID>_API_KEY]`, and `implicit = true`, so `OPENAI_COMPATIBLE_BASE_URL`
  plus `OPENAI_COMPATIBLE_API_KEY` in the environment is an instance with
  no config file (today's env-seeded compat instance,
  `openaicompat/adapter.go:110-116`); with the variable unset the record
  has no resolvable base URL, is `Hidden`, and is therefore not an instance
  (§5.1), usable only as a `base`. No `DefaultModel`, so never the default;
  the pseudo-providers are not in `default_order`, and `FindModel` (§7.5)
  orders them after every `default_order` entry. The `<ID>` in a variable
  name is the registry id uppercased with `-` → `_`
  (`KIMI_FOR_CODING_BASE_URL`, `ZAI_CODING_PLAN_BASE_URL`).

Everything in `evener_model_catalog_overrides.json` today either exists
upstream now (`gpt-5.6*`, `claude-opus-5`, `claude-sonnet-5`,
`claude-fable-5`, `deepseek-v4-*`) or becomes a row in this file (the Mythos
rows, the `[1m]` rows, the Kimi and Bedrock rows); the one entry not carried
is the `openrouter/anthropic/claude-3-haiku-20240307` test fixture, which
moves into the test data that uses it. The 2026-07-02 "silently deleted a
model on refresh" failure cannot recur: overlay rows materialize models when
the upstream row is missing, and the refresh script reports overlay rows
that upstream now covers.

### 6.3 User config

`providers.toml`, §10.

### 6.4 Refresh and cache

- `registry.Load(opts)` reads the embedded snapshot, then, if
  `<state-root>/catalog/models.dev.json` exists and its fetched-at is newer,
  uses that instead.
- If the cache is absent or older than 24h and `opts.Offline` is false, a
  goroutine fetches `https://models.dev/api.json` with `If-None-Match`,
  validates it by running the **full layered load** against it (the
  converter must accept it; it must contain ≥ 90% of the provider count and
  ≥ 90% of the model count of the embedded snapshot; the curated overlay
  must still load on top of it, with any dangling curated alias reported
  rather than fatal, §4.2), and writes it atomically (temp + rename). The
  running process keeps its already-loaded registry; the refresh takes
  effect on the next load. A failed refresh or validation logs one line and
  keeps the previous cache.
- `EVENER_OFFLINE=1` sets `opts.Offline`, and `registry.Load` defaults
  `Offline` to true when `testing.Testing()` is true (the guard the repo
  already uses, `agent/session_init.go:55-62`, with the same opt-out: an
  injected `WithFetcher(func(ctx, etag) (…))` or an explicit `Offline:
  false` overrides the default), so a bare `Load()` from any test package
  never starts the refresh goroutine while the refresh tests of §13 can
  drive it. The refresh itself is one function, `registry.Refresh(ctx,
  fetcher, force)`, called by the background goroutine and by `evener
  models refresh`, so the sanity floors and the ETag round-trip are testable
  directly. `cmdutil` test helpers set the variable too, and the default
  test state root is a temp dir, so default tests cannot reach the network.
  Nothing else in `registry.Load` or `Resolve` performs I/O beyond reading
  local files and environment variables.
- `make refresh-model-catalog` replaces the embedded snapshot (curl → gzip),
  writes `models.dev.meta.json`, runs the converter tests, and prints:
  providers added/removed, models added/removed, overlay rows upstream now
  covers, dangling overlay aliases, rows whose output cap is ≥ their
  context window (§7.4), and overlay pins whose upstream value changed.
- The embedded file is raw upstream JSON, gzipped (4.4 MB → 439 KB).
  Parsing costs about 30 ms once, lazily, as today.

## 7. Resolution

`(*Registry).Resolve(ref string) (Resolved, error)` is the single lookup
path. It replaces `LookupModelInfo`, `resolveOpenAICompatCatalogModel`,
`fillFromCatalog`, `GetPrice`'s prefix scan, `ResolveLiveModelInfo`,
`SelectProfile`, and the profile constructors. It does **not** replace
`cmdutil.ParseModelRef`, which validates a CLI argument (provider and model
both required, provider lowercased) rather than implementing §7.1's
reference syntax with its default-instance fallback.
`(*Registry).FindModel(id string) []Ref` answers the other question the
agent asks ("which instances serve this model id?") for plugin-agent model
declarations (§7.5): an instance *serves* an id when §7.2 steps 1–4 match it
on the merged record or the instance's already-cached live listing matches
it; `FindModel` never performs network I/O.

### 7.1 Reference syntax

`instance/model`, split on the **first** slash; the model half may contain
slashes (`groq/openai/gpt-oss-120b`, `openrouter/anthropic/claude-opus-5`).
A bare model id with no slash is resolved against the default instance.
There is no suffix handling: `claude-sonnet-4-5[1m]` is an ordinary alias
row in the curated overlay whose `wire_id` names the base model. Dated rows
are addressed by their catalog spelling (`vertex/claude-sonnet-4-5@20250929`,
as Anthropic's Vertex table lists it). Id comparison is case-sensitive.

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
5. live listing, which establishes existence with provider-level caps only
6. nothing matched: the row is synthesized (§7.3)

Steps 1–2 use the matched row's `WireID`. Steps 3–6 use the **reference
verbatim** as the wire id (a `global.` profile keeps its prefix, a dated
snapshot keeps its date); `Provenance["model"]` records the row that
supplied the facts. The layered merge of §4.1 then runs for the reference
(glob rows tested against the reference and the matched id, then the
matched row's own entry per layer, the live facts after layer 3), followed
by the derivations of §7.4. No substring or longest-prefix matching
anywhere.

### 7.3 Unknown models

A model id that matches nothing is still resolvable: `Resolved.Model` is
synthesized from provider-level caps, matching glob rows, the provider's
`Surface`, and the provider's `Family` (§6.2), `Warnings` carries `model not
in catalog`, and the wire id is the reference verbatim. Context window is
unset, which the agent treats as "unknown" (no compaction budget until the
live listing or a user row supplies one); the anthropic protocol's required
`max_tokens` falls back to 32000 as today (`anthropic/request.go:16-19`).
Reasoning follows §7.4's empty-controls rule. The hub shows the warning next
to the model. This is how a model released this morning works before the
cache refreshes. The one exception is the `oauth-openai-codex` transport,
whose backend enforces a model allowlist: an unknown id there is a resolve
error.

### 7.4 Derived caps

After the merge, and only where the field is still unset (step 2
excepted), `Resolve` derives, in this order:

1. **Effort control.** When `Reasoning != false` and `ReasoningControls` is
   empty after all layers, `effort` is added with whatever `EffortValues` the
   row has (possibly none). This is the pass-through rule: a row that
   declares nothing about its controls (1237 cataloged `reasoning: true` text
   rows with an empty `reasoning_options`, every live-only row, every
   synthesized row) accepts the requested effort as today's
   `supportsEffort(true)` default does (`openaicompat/compat.go:73-78`). Only
   a row that explicitly lists controls without `effort` (toggle-only,
   budget-only) suppresses the effort field on the dialects that gate on it
   (§8.4). Independently, a non-empty `EffortValues` implies `effort`.
2. **`MaxOutputTokens`** is cleared when it is ≥ `ContextWindow` and came
   from a catalog or live layer (today's junk-cap guard,
   `openaicompat/compat.go:309-318`; 1223 snapshot rows, including
   `moonshotai/kimi-k2.5` at 262144/262144, would otherwise send a cap the
   provider rejects). A user-layer value is kept as written.
3. **`Surface`** from the row, else the provider (family-less and
   synthesized rows only, §6.1), else `generic`.
4. **`ThinkingShape`**, on the `anthropic` protocol only and only when
   `Reasoning != false` (steps 5 and 6 share that gate, so a
   `reasoning = false` row shows no thinking in `inspect`), keyed on family
   (never on `Surface`, so a surface pin cannot change the wire shape). Let
   *claude* mean the row's `Family` starts with `claude`, or the row is
   synthesized and the provider's curated `Family` is `claude`. Then:
   `adaptive` when `effort ∈ ReasoningControls` and *claude*; else `budget`
   when `budget_tokens ∈ ReasoningControls`, or when `toggle` is the only
   control (`minimax/MiniMax-M3`; today's wrapper sends the budget object
   for any effort, `anthropic/request.go:158-166`); else, for a row whose
   controls were empty before step 1, `adaptive` when *claude* (an
   uncataloged Claude id, today's generation parse) and `budget` otherwise;
   else unset (no thinking object). Cataloged Sonnet 4.5 and Haiku 4.5 rows
   carry `budget_tokens` only and therefore derive `budget`, the shape the
   API requires of them; the Kimi and MiniMax pins of §6.2 make those rows
   explicit rather than relying on this rule.
5. **`ThinkingAlwaysOn = true`**, on the `anthropic` protocol only, when the
   final `ThinkingShape` is `adaptive` (today's builder sends the adaptive
   object for every adaptive-capable model whether or not an effort is
   requested, `anthropic/request.go:146-157`). On the OpenAI protocols
   `ThinkingAlwaysOn` comes only from the live layer or an overlay row, so
   `openrouter/anthropic/claude-opus-4.6` with no effort sends no reasoning
   object, as today.
6. **`ThinkingDisplay = summarized`** when the final shape is `adaptive` and
   `budget_tokens ∉ ReasoningControls` (models.dev: the 4.6 rows still carry
   `budget_tokens`; the 4.7+ and Claude 5 rows are effort-only; synthesized
   Claude rows qualify). This is today's `isClaude5OrNewer` display rule
   (`anthropic/request.go:150-156`, and the `claude5_request_shape` pins on
   Opus 4.7/4.8 and Mythos), applied on every instance that serves those
   rows.

Provenance records `derived` for each.

**The effort a request carries** (amended 2026-08-30, Jesse: a session with
nothing configured was reaching gateways with no reasoning field at all,
and a lunaroute-fronted glm-5.3 spent 25k reasoning tokens on its first turn
that way, while mandatory-thinking models reject a reasoning-less request
outright). One rule decides it, in `agent`, for the primary request and each
fallback alike:

- A model that does not reason (`Reasoning = false`) never gets an effort,
  whatever is configured.
- An explicit off (`none`, and the disable aliases that normalize to it) is
  carried on every reasoning model, never replaced by a default and never
  clamped into a thinking tier. Which models can be told off, and how, is the
  adapters' call (§8.4): they send the off value where the row's
  `EffortValues` lists an off level and the dialect has one, and omit the
  control otherwise. Carrying it rather than dropping it is also what keeps
  an off distinguishable from "nothing configured", without which a
  mandatory-thinking row's builder default reads an off as unset and switches
  thinking back on.
- A configured effort is clamped to `EffortValues`.
- Nothing configured: the row's own `DefaultEffort`, else `medium`, clamped.

`ThinkingAlwaysOn` is not a branch in that rule: a mandatory-thinking model
is a reasoning model and takes the default like any other, and the dialect
default of §8.4 remains the backstop for the requests that still reach an
adapter with no effort. The consequence is deliberate: rows whose provider
default was dynamic or off (Gemini 2.5 and the budget-shaped Claude 4.5
generation, the zai and qwen thinking toggles) now run at `medium` unless
told otherwise, and adaptive Claude keeps running at `high` because its
rows state `default_effort = "high"` — Anthropic's own default when
`output_config.effort` is omitted, which an injected `medium` would
silently downgrade.

`ShapeRequest` (§7.5) still adds no effort of its own: it clamps what the
caller set, and the caller is the one rule above.

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
| sandbox net-off web egress allowlist (`agent/sandbox/provider_web.go:16-21`) | `ProviderID` | vendor identity; the `gemini` key becomes `google` |
| subagent target comparison (`subagent_model_selection.go:183`) | `Instance` | routing identity |
| plugin-agent `model:` declarations (`resolvePluginAgentCatalogRef`, `subagent_model_selection.go:155-190`, today via `catalog.ResolveAlias`) | `Registry.FindModel` | `instance/model` resolves directly; a bare id resolves to the session's current instance when that instance serves it, else to the highest-ranked serving instance under §5.1's default ranking (explicit or implicit alike), and to *unavailable* when none serves it, preserving today's fallback-with-warning. With both `openai-codex` and `openai` present, a bare `gpt-5.6` from an `anthropic` session therefore goes to `openai-codex` |
| `ProviderOptions` map key and the API-log tag (`session_model_call.go:248`) | `Protocol` | options are protocol extras (beta headers, safety settings) |
| `openAIPromptCacheSupported` (`session.go:1343`) | `PromptCacheKey` set iff `Fields["prompt_cache_key"]`; `PromptCacheRetention = "24h"` set iff `Fields["prompt_cache_retention"]` | two independent gates, so Codex keeps the cache key and GPT-5.6 drops only the legacy retention field |
| the `ThinkingAlwaysOn` → `medium` injection (`session_model_call.go:806-816`) | `Caps.DefaultEffort` | §7.4: always-on is not a special case; the default is a model fact the one rule reads, not a branch on the always-on flag |
| `Client.BehaviorTagOf` identity fallback for replay scope (`client.go:432`, `session_model_call.go:1171`) | `Instance` + `Protocol`, both recorded on every turn | turns produced by instances no longer configured still carry what the replay needs; a turn written before the cut-over carries only `ResponseProvider`/`ResponseModel` (`agent/schema/turn.go:189-190`), so the replay resolves that instance name at replay time and treats an unknown instance as not eligible |

`llm.ShapeRequest(req, resolved)` is the single place the request-level
shaping happens and the only caller of `ClampReasoningEffort`. It runs in
this order: clear reasoning controls when `Caps.Reasoning == false`; clamp
the effort to `EffortValues` when one is set (an empty ladder passes the
requested effort through unchanged; no effort is ever added here — §7.4's
rule in `agent` is what sets one); apply
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
  URL, endpoint path, the `ContinuationHasher` HMAC of `Credential.Value`
  and its `Source`, the `OpenAI-Organization`/`OpenAI-Project` header values
  when present, the conversation id when set, and the OAuth
  account/workspace claims on the Codex transport; the storage policy label
  comes from the built body's `store` value as today (`adapter.go:425-434`);
  the `ContinuationHasher` stays on the client, keyed by state dir;
- `EndpointFamily` is `codex` when `Transport.Auth == oauth-openai-codex`,
  else `public`; the support registry keeps its per-family defaults
  (`llm/responses_continuation.go:232-244`);
- an override adapter registered on the client (§8.1) that implements
  today's `ResponsesContinuationPlanner` is consulted instead, exactly as
  `client.go:189-211` does now, so the thirteen test files that drive the
  session's anchor logic through fakes keep working;
- `CanFallbackToChat` and `FullHistoryFallbackMessages` are deleted with the
  fallback. A rejected anchor (`ErrorCode() == "previous_response_not_found"`,
  `session_model_call.go:1027`) is handled by the session as today.

## 8. Protocol adapters

### 8.1 Interfaces and the client

```go
// package llm
type Protocol interface {
    ID() string
    PrunablePaths() []string   // must equal registry.PrunablePaths(ID()); a test asserts it (§8.2)
    BuildBody(req Request, res registry.Resolved) (map[string]any, error)
    Complete(ctx, req Request, res registry.Resolved) (Response, error)
    Stream(ctx, req Request, res registry.Resolved) (Stream, error)
    ListModels(ctx, res registry.Resolved) ([]registry.Model, error)   // ErrUnsupported when ModelsEndpoint is "-"
    CountTokens(ctx, req Request, res registry.Resolved) (int, error)  // ErrUnsupported when CountTokensEndpoint is "-"
}

type Authenticator interface {
    Apply(ctx, *http.Request, res registry.Resolved) error // sets auth headers from res.Credential, res.Transport.AuthHeader, and per-instance token state keyed by res.Instance
}

// Optional, implemented only by the Codex transport (§9.5).
type RequestPreparer interface {
    PrepareRequest(ctx, *http.Request, body map[string]any, req Request, res registry.Resolved) error
    RequiresStreamingComplete() bool
}
```

One instance of each protocol is registered at init; adapters hold no
per-provider state. Base URL, headers, auth, and caps arrive in `Resolved`.
Today there are two Chat Completions builders
(`llm/providers/openai/chatcompletions.go`, used as the openai adapter's
fallback, and `llm/providers/openaicompat`, which owns the quirks); they
consolidate into the single `chatcompletions` protocol package, and
`llm/providers/openai` becomes `responses` with only the Responses
implementation. The shared helper package `llm/providers/internal/openaichat`
(in-band error and tool-argument helpers both builders import,
`responses.go:19,554,565,1234`) stays where it is so `responses` never
imports a sibling protocol package.

`llm.Client` is constructed with a registry (`NewClient(WithRegistry(r))`;
without the option it lazily loads the embedded snapshot offline) and
dispatches: resolve `req.Provider/req.Model` → look up the protocol by
`res.Protocol` → call it. It keeps today's `Register(adapter)` (keyed on
`adapter.Name()`, `llm/client.go:60-73`) as an override map consulted by
instance name **first**: when an override exists and the name resolves,
`ShapeRequest` runs and the override sees the shaped request; when the name
does not resolve (`fake`, `other`, `off`, `broken`, and the other test
doubles), the request passes through untouched and no error is raised;
either way there is no body prune, and the override's
`ResponsesContinuationPlanner` is honored (§7.6). The profile those tests
pair with the fake comes from `Resolve` against injected instances (§5.2).
The `nameToTag` map, `RegisterInstanceAdapterFactory`,
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

`bearer`, `optional-bearer`, `header`, and `none` are trivial
authenticators. `oauth-openai-codex` is the existing `auth/<instance>.json`
flow moved behind the interface, with its token-refresh state cached per
instance; the same transport implements `RequestPreparer` (§9.5). `gcp-adc`
sends a bearer token from application-default credentials
(`golang.org/x/oauth2/google`, `FindDefaultCredentials`, called at first
request, never at load; step 2 promotes `golang.org/x/oauth2` from an
indirect to a direct dependency), cached per instance and refreshed by the
token source; it ships with the other authenticators in §14 step 2 so the
implicit Vertex instances of §5.1 are never dead. Nothing else is needed for
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
   `json_object`), and `Sampling` (false omits the temperature and top-p
   paths before the prune ever sees them) all act here.
2. **Prune** by `Fields`, a denylist over an enumerated set. The registry
   owns the authoritative per-protocol path table (data, so it can seed
   `Caps.Fields` and validate config without importing protocol packages);
   each protocol's `PrunablePaths()` returns the same list and a test
   asserts equality. `registry.Prune(body, res.Caps.Fields)` deletes each
   prunable path whose flag is false and records the deleted paths on the
   API attempt log entry as `pruned_fields`. A `fields` key in the overlay
   or `providers.toml` that is not in the row's resolved-protocol set is a
   load error (typo guard); keys inherited from a `base` on another protocol
   are ignored (§4.2).
3. **Body constants** from `Transport.Body` are set, creating parent
   objects as needed, so they survive the prune and never depend on build
   state; a constant overrides a caller-supplied `ProviderOptions` value of
   the same path.
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
overlay or a derivation, set on the rows it applies to:

| Today | Cap |
|---|---|
| `responsesLiteModel` (`gpt-5.6`) on the platform API | `thinking_always_on` + `image_detail = "omit"` + `prompt_cache_retention = false` on the `openai` `gpt-5.6*` glob row (§6.2) |
| `codexLite` (`gpt-5.6` on the Codex backend) | `responses_lite` + `body` constants on the `openai-codex` `gpt-5.6*` glob row (§6.2, §9.5) |
| `defaultImageDetail` (`gpt-5.4/5.5/gpt-6`) | `ImageDetail = "original"` on those rows; baseline `"high"` |
| `reasoningSummaryLevel` (`gpt-5/gpt-6`) | `ReasoningSummary = "detailed"` on those rows, `"auto"` on the rest of `openai`; baseline `none` |
| `isClaude5OrNewer` (temperature, adaptive, display) | `Sampling = false` (from models.dev, inherited by alias rows such as the Mythos rows and the Codex rows); `ThinkingShape = "adaptive"`, `ThinkingAlwaysOn`, and `ThinkingDisplay = "summarized"` derived (§7.4) on every instance serving those rows |
| `adaptiveThinking` for Opus/Sonnet 4.6 | derived (§7.4): adaptive, always on, no display (they still carry `budget_tokens`) |
| the Opus 4.5 hybrid | top-level `"*claude-opus-4-5*"` glob row, which also reaches aliases of those rows |
| `geminiSupportsMultimodalFunctionResponse` (`gemini-3`) | top-level `"*gemini-3*"` glob row |
| `[1m]` synthesis for `claude-opus-4-`/`claude-sonnet-4-` | curated `[1m]` alias rows (§6.2) |
| `minimax/` under openrouter | the `minimax/*` glob row (§6.2), which also reaches live-only MiniMax ids (§4.1) |
| `codexModelVariants` | `wire_id` + rows under `openai-codex` (§6.2) |
| `openAIModelSupports24hPromptCache` | `prompt_cache_retention` on the `gpt-5*`/`gpt-4.1*` glob rows, which also reach uncataloged `gpt-5.x` ids (§4.1) |

A new model generation means adding rows to the overlay, not editing an
adapter.

### 8.4 Reasoning

`Reasoning` gates everything: `false` empties `ReasoningControls` and
`EffortValues` (so the hub shows no effort chip), sends no reasoning field
on any protocol, drops replayed thinking from history, and strips reasoning
keys from `ProviderOptions` (today's `ReasoningOff` transforms,
`openaicompat/compat.go:189`, `request.go:113,230,353,374`). When `true`,
`ReasoningControls` (after §7.4's derivation) says what the model accepts,
`ThinkingShape` says how the anthropic protocol spells it, and
`ThinkingFormat` says how the openai-chat protocol spells it. `Fields` plays
no part in reasoning.

Two words used below: a row is *effort-capable* when `effort ∈
ReasoningControls`, which after §7.4 is every row except one that explicitly
lists controls without `effort`; it is *enable-capable* when `toggle ∈
ReasoningControls`.

**openai-chat.** The dialect table is today's `applyThinkingFormat`
(`request.go:230-291`, documented in `docs/llm-providers.md` "thinking_format:
exact wire JSON per dialect") kept verbatim; the only change is the gate on
the dialects that had one.

| `ThinkingFormat` | when an effort is set | with `ThinkingAlwaysOn` and no effort |
|---|---|---|
| `openai` (default) | `reasoning_effort: <wire>` if effort-capable, else nothing (Chat Completions has no toggle) | `reasoning_effort: medium` clamped to `EffortValues` (today's default, `request.go:238-247`; the adapter-side backstop, reached only by a request §7.4's rule left without an effort) |
| `openrouter` | `reasoning: {effort: <wire>}` **unconditionally** (today's `request.go:265-266`; OpenRouter normalizes effort for every reasoning model, translating it to a budget for Anthropic-routed ones, and six of its `anthropic/*` rows are toggle-only in models.dev while its live listing reports no `supported_efforts` for them, so a gate here would silently downgrade `high` to medium) | `reasoning: {enabled: true}` |
| `zai` | always `thinking: {type: enabled, clear_thinking: false}`; plus `reasoning_effort: <wire>` if effort-capable | `thinking: {type: enabled, clear_thinking: false}` |
| `deepseek` | always `thinking: {type: enabled}`; plus `reasoning_effort: <wire>` if effort-capable | `thinking: {type: enabled}` |
| `together` | always `reasoning: {enabled: true}`; plus `reasoning_effort: <wire>` if effort-capable | `reasoning: {enabled: true}` |
| `qwen` | `enable_thinking: true` | `enable_thinking: true` |
| `qwen-chat-template` | `chat_template_kwargs: {enable_thinking: true, preserve_thinking: true}` | same |
| `chat-template` | `chat_template_kwargs: <ChatTemplateKwargs>` (omitted when empty) | same |
| `string-thinking` | `thinking: <wire>` | `thinking: "medium"` clamped to `EffortValues` (today's default) |

An explicit `none` is the user turning thinking off. On the `openai` (and
default) dialect it sends `reasoning_effort: none` and on `openrouter`
`reasoning: {effort: none}`, in both cases only when the row's `EffortValues`
lists an off level (gpt-5.1 and later; the `openai` dialect also needs the
row to be effort-capable). No other dialect has a value that says off, so the
control is omitted. In every case the off returns before the
`ThinkingAlwaysOn` column above: each shape there switches thinking ON, which
would invert the user's stated intent.

**openai-responses.** `reasoning: {effort: <wire>}` when an effort is set and
the row is effort-capable; `reasoning.summary` from `ReasoningSummary`; with
`ThinkingAlwaysOn` and no effort, `reasoning: {summary: …}` alone, as today's
lite handling (`responses.go:145-151`). An explicit `none` sends
`{effort: none}` on a row that lists the off level and is effort-capable, and
otherwise drops the reasoning object whole — summary included, so a
mandatory-thinking row does not keep reasoning on.
`include: [reasoning.encrypted_content]` accompanies every `reasoning`
object when `Fields["include"]` is on.

**anthropic.** `ThinkingShape` picks one of three bodies the builder already
knows (`anthropic/request.go:131-176`): `adaptive` → `thinking: {type:
adaptive}` plus `display` from `ThinkingDisplay`, sent whenever
`ThinkingAlwaysOn` (derived for adaptive rows) or an effort is set, plus
`output_config.effort` only when the caller set an effort; `budget` →
`thinking: {type: enabled, budget_tokens}` from the existing effort→budget
table, only when an effort is set; `budget+effort` (Opus 4.5, Kimi K3) →
both. Unset shape → no thinking object, and so does an explicit `none`:
no Claude row states an off effort level, so there is no value that says off
here, and keeping the always-on adaptive body would switch thinking on
against the user's intent.

**google.** Effort → `thinkingConfig` as today; `none` sends no
`thinkingConfig`.

The off therefore reaches the wire on exactly the rows that state they
accept one, and is omitted everywhere else — never inverted into thinking-on
(amended 2026-08-30, Jesse: it must be possible to explicitly send a "turn
reasoning off"). A `thinking_levels` map (today's per-model level →
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
`VarsEnv` (a user-layer `vars_env` merges key-wise like `vars`), then `Vars`
from the curated and upstream layers (defaults), then the variable is left
unresolved with a warning and the error naming the variable and the
instance fires at the first request (§4.2). That order is what makes
`OPENAI_BASE_URL`
and the other `*_BASE_URL` variables of §6.2 work: the curated URL is the
default, the environment overrides it, and an explicit instance `base_url`
wins over both. A variable's value is substituted verbatim; there is no
path normalization beyond the host rules below, which is why every
`*_BASE_URL` value carries its version segment. `HostRule` names one of two
normalizers, the only host-aware code in the system: `vertex-location`
derives the Vertex host from the location variable (§9.4); `ollama-host` is
today's `envvars.ResolveOllamaBaseURL` + `NormalizeOllamaHost`
(`envvars/ollama_host.go`), applied to the **variable value** before
substitution and producing the full base URL: `OLLAMA_BASE_URL` wins when
set, else the `OLLAMA_HOST` value is normalized (`localhost` →
`http://localhost:11434/v1`, `::1` → `http://[::1]:11434/v1`, `ollama.com` →
`https://ollama.com:443/v1`, `http://proxy/ollama/v1` kept), which is why
the Ollama template is `{OLLAMA_HOST}` with nothing appended. An explicit
instance `base_url` with no placeholder bypasses the host rule and the
environment entirely, as today (`ollama/adapter.go:136-141`). If the final
completion path contains `{model}`, `model` is not sent in the body.

### 9.2 Azure OpenAI and Azure Foundry (verified 2026-08-28, Microsoft Learn "v1 API")

- `base_url = https://{AZURE_RESOURCE_NAME}.openai.azure.com/openai/v1`
  (`services.ai.azure.com/openai/v1` also accepted upstream).
- `auth = header`, `auth_header = api-key`; Entra bearer tokens work through
  `auth = bearer` with the token in `api_key`.
- No `api-version` parameter on v1.
- Both `/responses` and `/chat/completions` exist; `model` in the body is
  the **deployment name**. A deployment row's own id is its wire id, and
  `alias_of` pulls the catalog facts and, when the row sets no protocol or
  transport of its own, the target's protocol and endpoint (§4.2):

```toml
[providers.azure]
api_key = "$AZURE_API_KEY"
[providers.azure.vars]
AZURE_RESOURCE_NAME = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"          # facts from the catalog row; wire id stays gpt55-prod; Responses endpoint
[providers.azure.models."claude-prod"]
alias_of = "claude-opus-4-5"  # facts, the anthropic protocol, the Foundry /anthropic/v1 endpoint, and the Opus 4.5 glob pin follow the target
```

- Claude on Azure Foundry: models.dev already marks those rows `npm:
  @ai-sdk/anthropic` with `api:
  https://${AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1`, so
  they resolve to the anthropic protocol at that base URL with Azure's
  `api-key` header (per-model overrides never change the provider's auth,
  §4.3; Anthropic's Foundry page accepts `api-key` or `x-api-key` plus
  `anthropic-version`). One instance, two protocols.

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
  `StructuredOutput = false` and `WebSearch = false` on the `"*anthropic.*"`
  provider glob row (both listed as unsupported on that page; models.dev
  marks the Sonnet 5, Haiku 4.5, Sonnet 4.6, Opus 4.5, and Opus 4.6 rows
  `structured_output: true` at the row level, which the layer-3 glob beats
  under §4.1's merge order; the Mantle OpenAI rows keep their own values).
- **Inference-profile ids are not served on this endpoint** (corrected
  2026-08-31 from live evidence). `bedrock-mantle` hosts are regional
  (`bedrock-mantle.<region>.api.aws`), and the endpoint serves exactly the
  ids its own catalog lists: `GET
  https://bedrock-mantle.{region}.api.aws/v1/models` answers 200 with six
  Claude ids, all unprefixed —
  `anthropic.claude-{fable-5,haiku-4-5,opus-4-7,opus-4-8,opus-5,sonnet-5}`.
  A request naming a `global.`, `us.`, `eu.`, `jp.`, `au.`, or `apac.`
  inference-profile id answers `404 not_found_error` ("The model
  'global.anthropic.claude-sonnet-5' does not exist"), even though `aws
  bedrock list-inference-profiles` reports that same profile ACTIVE and
  SYSTEM_DEFINED in the account and region the request was made from, and
  `get-foundation-model-availability` reports it AUTHORIZED and AVAILABLE.
  The namespaces are simply different: inference-profile ids address
  bedrock-runtime's `InvokeModel`/`Converse` path, which §1 puts out of
  scope. models.dev lists the profile rows regardless, so the §6.1 converter
  marks every region-prefixed `amazon-bedrock` row `Hidden`: the row stays in
  the catalog for metadata and still resolves when a reference names one
  explicitly — carrying a `hidden: this provider does not serve this row`
  warning, so the endpoint's own 404 remains the truth — but it is out of
  `evener models list`. The provider's `default_model` and `cheap_model` are
  unprefixed for the same reason. §7.2's prefix strip only serves ids the
  catalog lacks.
  *Historical note:* Jesse verified the global profile live on 2026-08-28,
  three days before the readings above, and that note predates this finding.
  Which path that verification took is an open question flagged for the human
  partner; nothing in this design now depends on the answer.
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
  variable those templates introduce comes from `VarsEnv` like any other
  (§6.1). The two `openai/gpt-oss-*-maas` rows that lack the template are
  hidden (§6.1).

### 9.5 The Codex transport (`oauth-openai-codex`)

This is the one transport with behavior beyond auth, all of it existing code
in `llm/providers/openai/adapter.go` and `responses.go` that moves behind the
`Authenticator` + `RequestPreparer` pair (§8.1). Listed so nothing is lost:

- the OAuth record is per instance at `auth/<instance>.json` under the
  state root (`auth/openai/storage.go:72-99`), so the implicit
  `openai-codex` instance reads `auth/openai-codex.json`. Today's record is
  `auth/openai.json` (`adapter.go:114-121`, written by `evener openai login`
  whose `--instance` defaults to `openai`); nothing renames it: a record
  `auth/<name>.json` whose instance `<name>` is not on the Codex transport
  (or does not exist) produces a one-line startup notice naming the file
  and `evener openai logout --instance <name>` (flag day, §14.1);
- the authenticator's `Apply` sets `Authorization`, `ChatGPT-Account-ID`
  (from the token claims), `originator`, and `User-Agent` on **every**
  request, including `ListModels` (today `ListModels` goes through
  `setHeaders`, `openai/models.go:25`); `PrepareRequest` owns only the
  per-request work: `session-id`, `thread-id`, `x-client-request-id` from
  the request (`setRequestHeaders`); the inherited
  `OpenAI-Organization`/`OpenAI-Project` headers are removed by the overlay
  (§6.2), matching today's Codex adapter;
- `x-openai-internal-codex-responses-lite: true` when `Caps.ResponsesLite`
  is set (without it the backend hangs);
- `client_metadata`: `PrepareRequest` reads `res.Caps.Fields["metadata"]`
  directly. When true it sends `client_metadata = merge(body.metadata,
  req.ClientMetadata)` when that merge is non-empty (today omits an empty
  object, `responses.go:133-136`) and deletes `metadata` (the session puts
  the Codex installation id in `req.ClientMetadata` and never sets
  `req.Metadata`, `session.go:1354-1358`); when false it deletes both and
  sends neither;
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
surface  = "generic"                     # the gateway serves non-OpenAI models
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
`family`, `default_model`, `cheap_model` → `Provider` (the curated overlay
alone may also set `implicit`, `name`, `doc`, and a top-level
`default_order` array, which TOML tables could not order; those keys are a
load error in `providers.toml`); `transport` (a preset name), `base_url`,
`host_rule`, `auth`, `auth_header`, `endpoint`,
`stream_endpoint`, `models_endpoint`, `count_tokens_endpoint`, `vars`,
`vars_env`, `body` → `Provider.Transport`; `protocol` → `Provider.Protocol`;
every `Caps` field
by its snake_case name (`context_window`, `effort_values`,
`reasoning_controls`, `thinking_format`, `fields`, …) at the instance level
→ `Provider.Caps`, and inside `[providers.X.models."<id or glob>"]` →
`Model.Caps`, where `alias_of`, `wire_id`, `family`, `protocol`, `surface`,
`headers`, and the transport keys are also accepted. A top-level
`[models."<glob>"]` table is accepted in the curated overlay and in
`providers.toml` alike.

Rules, enforced at load with errors that name the instance and key:

- names are lowercase, no slash, unique; `base` must name a registry id;
  `alias_of` must name an existing non-alias row; `transport` must name a
  preset; `default` follows §5.1 (unknown name = error, credential-less
  implicit id = warning)
- an unknown key anywhere in the file is a load error naming it
  (`toml.MetaData.Undecoded()`), so a leftover `thinking_levels` or
  `compat` cannot be silently ignored
- `protocol` must be a registered protocol; `surface` one of the four values
- `auth` ∈ `bearer | optional-bearer | header | none | gcp-adc | oauth-openai-codex`
- `fields` keys in a provider entry or an exact row must be in that
  record's resolved-protocol prunable set (typo guard); a glob row's keys
  are checked against each row it matches and silently skipped for rows on
  another protocol
- `thinking_format`, `thinking_shape`, `max_tokens_field`, `cache_control`,
  `reasoning_field`, `host_rule`, `image_detail` are validated against their
  vocabularies; `reasoning_controls` entries must be `effort`,
  `budget_tokens`, or `toggle`
- `effort_values` entries non-empty; `"off"` rejected. `default_effort` is
  checked against the effort vocabulary (the six tiers plus `none`), which
  `registry` restates rather than importing from `llm` — `llm` imports
  `registry`, and the clamp passes an unrankable level through untouched, so
  an unchecked typo would reach the provider
- `$ENV` expansion in `api_key`, `credential_headers`, and `vars` uses
  today's `$NAME` / `${NAME}` / `$$` rules and happens at resolve time, so one
  instance's missing variable never blocks another. An unset variable in
  `api_key` or `credential_headers` yields an empty `Credential` with
  `Warnings: no credential (<NAME> unset)` and the first-request error
  (§5.2, so `inspect` keeps working); an unset variable in `vars` or a
  template yields `Warnings: unresolved variable <NAME>` and the
  first-request error (§4.2). In `headers` an unset variable **drops the header**
  (that is how the optional `OpenAI-Organization`/`OpenAI-Project` headers
  work; today it is an error, `apikey.go:261-276`); an empty-string value
  removes an inherited header of that name.
- **credential inheritance stops at the endpoint**: an instance that sets
  a literal `base_url` different from its base's `base_url` (compared after
  substituting the curated defaults, so copying the default URL verbatim
  is not "different") does not inherit the base's `APIKeyEnv`; a
  credentials-store entry under the instance's own name always satisfies
  it (that is the store's first layer, and the only way the hub's pane
  enters a key); otherwise, unless its `auth` is `none`,
  `optional-bearer`, `gcp-adc`, or `oauth-openai-codex`, it must set
  `api_key`, `api_key_env`, or `credential_headers`, else resolving it fails
  at first request with "no credential for <instance>". Today's
  `CredentialTag` applies this only to the openai-compatible shape
  (`providercfg.go:177-186`); the rule here generalizes it to every base,
  so a gateway never receives a vendor key by accident. An instance that
  keeps the template and supplies `vars` (the `bedrock` and `vertex`
  examples) or overrides the URL through its `*_BASE_URL` variable (§9.1)
  inherits normally. Credentials-store entries are looked up by instance
  name only, as today, never through the base.
- `WriteFile` keeps today's scrub-and-restore so hub rewrites never persist a
  credential the user did not author
- when both `auth = bearer` and a `credential_headers.Authorization` are
  present, the header wins and no bearer is derived from the key

`type`, `api_style`, `quirks`, `[instances.*]`, and `compat` are gone. A file
that uses any of them fails to load with a message pointing at this
document and §14.1. The CLI exits with it. The hub starts with implicit
instances only, shows it as a diagnostic, and refuses every instance write
with the same pointer (today's CRUD reloads the file before each write,
`app_instances.go:291-299`, and there is no raw-file editor in the web UI).
So that sessions still launch, `EVENER_PROVIDERS_CONFIG` gains a third
state: **present and empty means "no user layer"** (`os.LookupEnv`, not
`Getenv`; unset still means the default path, `load_client.go:38-41`). A
hub whose file failed to load sets it empty in every child environment,
replacing any inherited value (`launchconfig.ToEnv` starts from
`os.Environ()`, `env.go:41`), and passes `EVENER_CREDENTIALS_CONFIG` naming
its own `credentials.toml` (new variable; when unset, the store is the
sibling of the providers path, else `<config-root>/credentials.toml`, as
today, `main.go:236-239`). Children then compute the same implicit set from
the environment and the store; the hub no longer injects the launched
instance's key into the child (`env.go:56-60`, deleted with the roster).
The remedy is by hand: edit the file or move it aside; the hub never
rewrites or deletes it.

Credential resolution order, for every scheme that takes a key: the
instance's own `api_key` (a literal or `$VAR`, today's
`load_client.go:74-77` rule that the file wins); else a
`credential_headers.Authorization` (which also suppresses any bearer); else
the credentials-store file entry under the instance name; else the
environment: the instance's resolved `APIKeyEnv` (which the endpoint stop
above empties), plus `<NAME>_API_KEY` under the §6.2 uppercase rule **only
for instance names that are not registry ids** (so `[providers.anthropic]
base_url = gateway` cannot pick up `ANTHROPIC_API_KEY` through the name
layer, and `TOGETHERAI_API_KEY` is not an undocumented alias of
`TOGETHER_API_KEY`); `oauth-openai-codex` and `gcp-adc` ignore all of these
and use their record. `Credential.Source` records which step won. The
store keeps its file semantics but no longer owns a provider roster: it is
constructed with the registry's `(id → APIKeyEnv)` table, so
`envvars.Providers()`, `envvars.APIKeyVars`, and `envvars.AuthModes` are
deleted along with the seven-provider list; the generic env helpers and the
Ollama host helpers in `envvars` stay.

## 11. Commands, hub, and wire types

### 11.1 `evener models`

- `list [--provider X] [--all]` — resolved rows with protocol, surface,
  context, output cap, cost, effort ladder, warnings. Hidden providers,
  hidden rows, and rows without `tool_call` only with `--all`.
- `inspect <ref>` — the full `Resolved` record with provenance per field,
  the pruned-field list the protocol would apply, and the request skeleton
  (endpoint, auth scheme, headers with secrets masked). Works without a
  credential (§5.2).
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
  the report says both work) into `providers.toml`; discovered models are
  printed, never written. The runtime never probes on its own.
- `add <name> --base X [--base-url …] [--protocol …] [--var K=V]
  [--api-key-env NAME] [--credential-header K=V] [--surface S]` — writes
  the entry, then runs `probe --write` unless `--no-probe`. When the entry
  would resolve no credential under §10's rule (a gateway `--base-url` with
  no store entry under `<name>`, no `--api-key-env` whose variable is set,
  no `<NAME>_API_KEY` for a custom name, and no `--credential-header`),
  `add` writes it, skips the probe, and prints what to set. Secrets never
  appear on the command line: a `--credential-header` value must contain a
  `$VAR` reference and no literal secret (`Authorization=Bearer
  $PORTKEY_KEY`), and keys are entered through the hub's credentials pane
  or `credentials.toml`.

### 11.3 Hub and appwire

The hub's instance CRUD (`cmd/evener-hub/app_instances.go`) calls the same
functions, with the implicit-instance semantics of §5.1 (edit and
set-default write a shadowing entry that carries only the fields the user
changed, never a literal `base_url` the form merely displayed, so §10's
credential-inheritance stop does not fire on an untouched URL; remove is
refused). The appwire types
change shape (`appwire/types.go:2488-2523`): `InstanceEntry` drops `Type` and
`APIStyle` and gains `Base`, `Protocol`, `Surface`, `Vars`, `Auth`,
`Implicit`; its existing `BaseURL`, `IsDefault`, `HasStoredOAuth`,
`HasStoredFile`, `StoredEmail`, `CredentialRequired`, `ActiveSource`,
`AuthModes`, and `EnvVar` stay, with `AuthModes` derived from
`Transport.Auth` and `EnvVar` carrying the variable that actually resolved
(as `InstanceLayers` reports today, `store.go:137-155`), else the first
`APIKeyEnv` entry. `InstanceCreateParams` and `InstanceEditParams` follow;
`InstanceListResponse.AvailableTypes` becomes `AvailableProviders` (registry
ids with display names and `VarsEnv`, so the add form can render the right
variable inputs). The credentials pane lists every curated implicit
provider whether or not it currently has a credential (§5.2 resolves them
regardless), which is where a fresh install signs in to `openai-codex` or
enters its first key. That pane is fed by the `evener/auth/*` RPCs, which
change with it: `auth/list` returns one `AuthStatusResponse` per curated
implicit provider plus every explicit instance (today it iterates the
`envvars` roster, `app_auth.go:336-358`, `store.go:175-177`);
`AuthStatusResponse.AuthModes` derives from `Transport.Auth`; the OAuth
`status`/`login`/`logout` default name and `normalizeAuthProvider`'s empty
default (`app_auth.go:512-518`) become `openai-codex`, matching §9.5;
`Store.List()` is fed the implicit list, not the full 207-id table.
`AuthListResponse`/`AuthStatusResponse` (`appwire/types.go:1897,2439`) join
the regenerated-TS list. `protocol/types.gen.ts` is regenerated and the frontend
instance dialogs and row (`panes/settings/sections/credentials/`,
`InstanceRow.tsx`, `credentialLabels.ts`) are updated; `make test-web`
covers them. The spawn credential gate (`spawn.go:649-684`) keys on
`Transport.Auth`: `none` and `optional-bearer` need nothing;
`oauth-openai-codex` is satisfied by the instance's OAuth record; `gcp-adc`
by the ADC variable or file; everything else by a resolved key or
credential header. A `providers.toml` load error (with the child-spawn and
write-refusal behavior of §10) and the stray OAuth-record notice (§9.5) are
surfaced as hub diagnostics so web-UI users see them.

`model/list` returns `Resolved`-derived rows straight from the registry, so
`enrichModelDescriptors` and `applyInstanceModelOverride` are deleted.

`cmd/llmcall` uses `cmdutil.LoadClient` like everything else.

## 12. Errors

`llm.ClassifyHTTPError(status, headers, body)` extends today's
`errorFromHTTPStatus`/`classifyByMessage` (`llm/errors.go:262-375`), which
already match context-length, content-filter, quota, and not-found messages,
carry `RetryAfter`, mark `cyber_policy_violation` retryable, and treat 413 as
context length. Evaluation order: **413 first** (Groq's TPM ceiling arrives
as HTTP 413 with `code: "rate_limit_exceeded"`, and it is a per-request size
limit that recurs on retry, so status must beat that code), then **specific
codes** from the structured body, then **status** (where 400 and 422 are
not terminal: they defer to the message rows, as `errors.go:274-282` does
today), then **message patterns**, then the **generic type**; a generic
type such as `invalid_request_error` never short-circuits the message rows
(today's order is status → code → message, `errors.go:274-282,344-365`, and
the message rows must stay live so compaction-on-context-length keeps
firing). The 429
branch keeps today's message check for the "usage limit" phrase
(`usagelimit.go:67,90-92`) so no current classification is lost. The code
table:

| Signal | Kind |
|---|---|
| 413 (any wording or code, including Groq's per-request TPM ceiling); codes `context_length_exceeded`, `request_too_large`; 400 matching `context length\|maximum context\|too many tokens\|reduce the length`; Anthropic `prompt is too long` (new) | `KindContextLength`, non-retryable, message verbatim |
| codes `usage_limit_reached`, `insufficient_quota`, Kimi's quota 403 body (`llm/usagelimit.go`), or the "usage limit" phrase on 429 | `KindQuotaExceeded`, non-retryable, carries the reset time (unchanged from today; listed so it is not lost) |
| code `rate_limit_exceeded` on 429; other 429 | `KindRateLimit` (retryable, honors `retry-after` and `x-ratelimit-reset-*`) |
| codes `unknown_parameter`, `unsupported_parameter` (name from `error.param`); 400 messages `Unrecognized request argument supplied: <name>` (bare token; OpenAI Chat sends `param: null` here), `Unknown parameter: '<name>'` (Responses), `Unsupported parameter: '<name>'`, `unknown field <name>` | `KindInvalidRequest`. Hint selection: `<name>` equal to the row's current max-tokens spelling (`max_tokens` or `max_completion_tokens`; OpenAI's "Unsupported parameter: 'max_tokens' … Use 'max_completion_tokens' instead", or an older compatible server's "Unrecognized request argument supplied: max_completion_tokens" behind a `base = "openai"` gateway) → `Hint: set max_tokens_field = "<the other spelling>"`; `<name>` in the row's prunable set (the max-tokens path is always keyed `max_tokens` there, whatever spelling is in effect) → `Hint: run evener models inspect <ref> and set fields.<name> = false`; otherwise the generic hint below (a cap-governed or nested path such as `reasoning.summary` is not a valid `fields` key) |
| 400 `invalid JSON body` or another generic `invalid_request_error` with no parameter token (including Anthropic's "not supported with thinking" family, which names no parameter) | `KindInvalidRequest` with `Hint: run evener models inspect <ref>; this endpoint rejected a field the registry sends — compare the pruned-field list against the provider's documentation` |
| 401/403 otherwise, 404 with `model` in the message | as today |

The provider's message is always included verbatim. `ErrorCode()` survives
(the session keys continuation-anchor rejection on it). `BehaviorTag()` on
error types is removed; `Provider()` returns the instance name and a new
`Protocol()` returns the protocol id.

## 13. Testing

- **Converter**: a checked-in 40-provider excerpt of models.dev
  (`llm/registry/testdata/models.dev.sample.json`) covering every `npm`
  in the table, per-model `provider` overrides (including a cross-protocol
  row with no per-model `api`, a Mantle row, and a per-model `api` with a
  placeholder), both `interleaved` shapes, every `reasoning_options`
  combination including `reasoning: true` with none and toggle-only,
  `limit.input`, an output cap ≥ the window, `@default` ids, tiers, a hidden
  provider, a provider with no `api`, non-Claude Bedrock rows, a
  `google-vertex` `openai/*-maas` row, mixed-case ids, and rows with mapped,
  unmapped, and absent `family`. Table tests assert the converted records;
  a fuzz target feeds mutated JSON.
- **Merge and provenance**: property tests that a later layer's set field
  always wins regardless of level, that the effective order is 1 → 2 → 3 →
  live → 4 (live overrides a curated fact and never a user fact), that
  within a layer the order is provider → top-level glob → provider glob →
  row with longer patterns winning among globs and lexical order breaking
  ties, that glob rows are tested against the reference and the matched or
  alias-target id and reach live-only and synthesized ids, that `nil`
  always inherits, that map layers merge key-wise, that `Base` chains
  resolve models (and stop with `inherit_models = false`), that alias
  seeding imports facts, family, and surface, imports protocol and
  transport only for same-provider targets, runs over layers 1–4, and
  rejects chains and cycles, that cross-protocol records do not inherit
  protocol-specific transport fields or `Fields` but do inherit the host
  rule, that `Hidden` is recomputed at both levels against the environment
  and cleared by a later-layer or same-provider-alias protocol or
  transport, that a user-layer dangling `alias_of`, an unknown `transport`,
  or an unknown `default` fails load while a curated dangling `alias_of`
  degrades to a hidden row with the warning (and §6.4's cache validation
  accepts a snapshot that dangles one) and a credential-less implicit
  `default` only warns, and that every provenance entry names a real layer.
- **Instances**: implicit-instance derivation from env and store fixtures
  (only `implicit = true` providers; gcp-adc from the env var and the
  well-known file, never the metadata server: the test asserts no HTTP
  client is constructed); default selection in all four branches including
  `XAI_API_KEY` alone, `OPENAI_API_KEY` alone (instances `{openai, ollama}`,
  no `openai-codex`), `OPENAI_COMPATIBLE_BASE_URL` alone (an instance, never
  the default) and unset (no pseudo-provider instance at all), the ADC file
  alone (no Vertex instance: `Hidden` for the unset project and location)
  and with `GOOGLE_VERTEX_PROJECT` + `GOOGLE_VERTEX_LOCATION`
  (`google-vertex-anthropic` by `default_order`), `ANTHROPIC_API_KEY` +
  `GROQ_API_KEY` with a shadowing `[providers.groq] protocol = …` entry
  (anthropic still wins) and with a custom `[providers.work] base =
  "openai"` entry (anthropic still wins), an explicit `[providers.ollama]`
  next to `ANTHROPIC_API_KEY` (anthropic wins), and the same with
  `default_model` set on the Ollama entry (anthropic still wins by rank;
  ollama wins only when it is the sole candidate); an unset `$VAR` in
  `api_key` resolving with the warning; shadowing by explicit entries; the
  credential-inheritance stop for gateways and its non-application to
  `vars`-only instances and to `OPENAI_BASE_URL` overrides; `WithInstances`
  injection; resolution without a credential carrying the warning, and no
  warning for `optional-bearer`.
- **Flag day** (§14.1): an old-schema hub-materialized `providers.toml` at
  the default path fails to load with the pointer; the CLI exits with it;
  the hub starts with implicit instances only, surfaces it as a
  diagnostic, spawns a child with `EVENER_PROVIDERS_CONFIG=` (empty) and
  `EVENER_CREDENTIALS_CONFIG` set, and that child launches against the
  implicit set with the hub's store (asserted at the default path and at a
  custom path); the hub refuses an instance write with the pointer; a stray
  `auth/openai.json` produces the notice and nothing else; a
  `credentials.toml` entry under an unknown instance name is reported by
  `evener providers list` and ignored; the old `evener_model_catalog_overrides.json`
  and LiteLLM data are gone from the build.
- **Resolution**: golden `Resolved` records (JSON) for a fixed set of
  references: `groq/openai/gpt-oss-120b` (chat and responses),
  `openai/gpt-5.5`, `openai/gpt-5.6` (asserting `thinking_always_on`,
  `image_detail = omit`, no `prompt_cache_retention`), `openai/gpt-5.5` with
  `OPENAI_BASE_URL` set (asserting the override and inherited credentials),
  `openai-codex/gpt-5.6` (asserting `WireID = gpt-5.6-sol`, the lite body
  constants, inherited facts, Codex's off-list intact, the Codex transport,
  and the exact header set without org/project),
  `anthropic/claude-sonnet-4-5` and `anthropic/claude-sonnet-4-5[1m]`
  (asserting `budget` shape, no always-on; the `[1m]` row also `WireID`,
  window 1000000 reached through the alias fold, and no beta header),
  `anthropic/claude-opus-4-6[1m]`
  (asserting it is unknown: synthesized row, wire id verbatim, `model not
  in catalog`), `anthropic/claude-haiku-4-5` (`budget`),
  `anthropic/claude-opus-4-6` with
  no effort (asserting the adaptive object, no `display`, and no
  `output_config`), `anthropic/claude-opus-4-7` (adaptive with `display =
  summarized`), `anthropic/claude-opus-4-5` (asserting `budget+effort` and
  no always-on), `anthropic/claude-opus-5`, `anthropic/claude-mythos-5`
  (facts from the Azure alias, the `anthropic` transport, adaptive with
  display), `azure/gpt55-prod` (asserting `WireID = gpt55-prod`),
  `azure/claude-prod` (asserting the anthropic protocol, the Foundry
  endpoint, and `budget+effort` via the target-matching glob),
  `azure/claude-opus-4-5` (asserting `budget+effort` via the top-level glob
  and the `api-key` header), `azure/llama-…` (asserting `Surface =
  generic`), `bedrock/anthropic.claude-sonnet-5` (asserting
  `StructuredOutput = false` from the glob over the row's `true`, and
  `display = summarized`), `bedrock/openai.gpt-5.5` (asserting the Mantle
  preset and `StructuredOutput` intact), `bedrock/global.anthropic.claude-opus-5`
  (asserting the verbatim wire id), `bedrock/anthropic.claude-new-model`
  (synthesized, asserting the `*anthropic.*` glob pins reached it and the
  adaptive shape from the provider family), `vertex/claude-opus-5` and
  `google-vertex/claude-opus-5` (asserting the `vertex-anthropic` endpoints,
  not Gemini's, and the resolved host), `openrouter/anthropic/claude-opus-5`
  (asserting `Surface = anthropic`), `openrouter/anthropic/claude-opus-4.6`
  with no effort (asserting no `reasoning` object in the built body),
  `openrouter/anthropic/claude-sonnet-4.5 --reasoning-effort high`
  (asserting `reasoning.effort == "high"` on the wire),
  `orclaude/minimax/minimax-m2.7` under the §14.1 recipe (asserting
  `/messages`, OpenRouter's key through the cross-protocol rule, and
  `Surface = anthropic` from the glob row), `anthropic/claude-opus-4-5[1m]`
  (asserting `Sampling` unset: Opus 4.5 accepts temperature upstream, and
  the alias inherits the target's value), `anthropic/claude-mythos-5` and
  `openai-codex/gpt-5.6` (asserting `Sampling = false` inherited through the
  alias),
  `openrouter/minimax/minimax-m2.7` (asserting the enable
  object with no effort), `openrouter/deepseek/deepseek-r1` and
  `kimi-for-coding/kimi-for-coding` with `--reasoning-effort high`
  (asserting the effort reaches the wire; the Kimi row also `budget` and the
  anthropic surface), `kimi-for-coding/k3` (asserting `budget+effort` and
  the anthropic surface), `minimax/MiniMax-M3` (asserting `budget` from the
  toggle-only controls and the anthropic surface),
  `anthropic/some-new-model` (unknown, asserting `Surface = anthropic`,
  adaptive shape with display, `max_tokens = 32000`), `local/whatever-claude`
  (unknown on the pseudo-provider, asserting no thinking shape and `generic`),
  `work/glm-5.2-nvfp4` (asserting the effort control from `effort_values`
  and `Surface = generic`), `moonshotai/kimi-k2.5` (asserting
  `MaxOutputTokens` cleared), `xai/<default>` with only `XAI_API_KEY`
  (asserting it is the default instance), `local/whatever` (unknown model),
  `ollama/llama3:8b` (live-only, `OLLAMA_HOST=localhost`, `::1`, and
  `OLLAMA_BASE_URL` set, with and without `OLLAMA_API_KEY`), and
  `ollama/qwen3:8b` with a user `[providers.ollama.models."qwen3*"]` row
  (asserting the glob reached the live id).
- **Pruner**: every prunable path per protocol, nested paths, array-element
  paths, the `PrunablePaths()` = registry table contract, unknown-key
  rejection at load, and build → prune → constants → prepare ordering (a
  constant on a pruned parent survives; the Codex `client_metadata` rule in
  both flag states and the empty-merge case runs last).
- **Wire captures**: the existing per-protocol golden bodies
  (`wire_capture_test.go`) are regenerated from `Resolved` inputs; new cases
  per cloud transport assert endpoint, stream endpoint, auth header name,
  body constants, model omission, and the Codex header set; an
  `openrouter/anthropic/claude-opus-5` multi-turn tool-use case asserts the
  signed `reasoning_details` round trip.
- **Client dispatch**: an override adapter registered under a resolvable
  name receives the shaped request; one registered under an unresolvable
  name receives the request untouched with no error; an override's
  continuation planner is honored; `ShapeRequest` never adds an effort.
- **Continuation**: plan derivation from `Resolved` for OpenAI proper (on),
  Groq Responses (off), the `work` gateway (off, chat protocol), Azure (on),
  and Codex (family `codex`); request-fingerprint stability across two
  builds of the same request and across `Complete`/`Stream`.
- **Refresh**: injected fetcher; asserts cache write, ETag round-trip,
  offline short-circuit, both sanity floors, and that no test path
  constructs a real HTTP client.
- **Error classifier**: table of real captured bodies (Groq 400 and 413,
  OpenAI Chat `Unrecognized request argument supplied: …` with `param:
  null`, OpenAI Responses `unknown_parameter` with `param`, OpenAI
  `context_length_exceeded`, Anthropic prompt-too-long as
  `invalid_request_error`, Anthropic "not supported with thinking" (no
  parameter hint), OpenAI `insufficient_quota`, ChatGPT
  `usage_limit_reached` by code and by phrase).
- **Config**: every load-error rule in §10 has a failing fixture.
- **Cross-adapter differential** (`llm/providers/difftest`) is adapted: it
  constructs adapters by struct literal and calls the two-argument interface
  today (`differential_fuzz_test.go:89-92`), so it moves to `Resolved`
  inputs; the openai-compat leg becomes the `chatcompletions` leg.
- **Live** (`EVENER_LIVE_TESTS=1`): the Sonnet 4.5 `[1m]` row, whose 1M
  window is GA and whose request therefore carries no beta header
  (`TestLiveOneMegaContextRowAccepted`); the `[1m]` beta on Opus
  4.5, Groq Responses, Bedrock `global.` routing, Kimi K3's thinking shape,
  MiniMax M3's budget object, and one request per cloud transport once
  step 4 lands.

## 14. Implementation order

The repo is a `go.work` workspace, so a deleted `llm` symbol breaks every
module at once. The order below keeps the tree green at every step by
building the new packages beside the old ones and cutting over last.

1. **`llm/registry`** (additive): types, converter, overlay with transports
   and glob rows, config loader, merge with provenance and alias seeding,
   instances and `WithInstances`, `Resolve`, `FindModel`, derived caps, the
   prunable-path table and `Prune`, embedded snapshot, cache/refresh with
   injected fetcher; `llm.ShapeRequest` and the interfaces of §8.1 in `llm`.
   `evener models list|inspect|refresh` lands here and reads the new-schema
   `providers.toml` only; during steps 1–2 an old-schema file is treated as
   absent with a one-line note ("ignored until the cut-over"), never the
   §14.1 pointer, because every other command still needs that file and the
   hub re-materializes it at startup until step 3. After step 3 the command
   behaves like the rest of the CLI (§10). Nothing else consumes the
   registry yet. The §13 golden `Resolved` records for
   `azure/…`, `bedrock/…`, and `vertex/…` are written here, so the data model
   is proven for the cloud providers before any cloud call is made. (~2,600
   lines + ~2,400 lines of tests + data.)
2. **New protocol packages** (additive): `chatcompletions` (consolidating
   `openaicompat` and `openai/chatcompletions.go`), `responses`, and
   `Resolved`-driven types added inside the existing `anthropic` and
   `google` packages next to the old adapters, each with `BuildBody` and
   `PrunablePaths`; every authenticator including `gcp-adc` (adding
   `golang.org/x/oauth2` as a direct dependency), the Codex transport, the
   error classifier, wire captures and the adapted difftest. The old
   adapters keep running. (~1,600 new lines, mostly moved; ~3,000 lines of
   tests moved or regenerated.)
3. **Cut-over** (one commit series, tree green at its end): `llm.Client`
   routes by protocol with the override map and plans continuation from
   `Resolved` + `BuildBody`; `agent/provider.Profile` wraps `Resolved` and
   the test helpers resolve against injected instances; the thirteen tag
   and catalog branches move per §7.5; hub `model/list`, instance CRUD,
   appwire types, generated TS, and frontend dialogs; `providers
   probe|add`; `llmcall` on `LoadClient`; credentials store takes the
   registry table; the old-schema load error with its §14.1 pointer and the
   stray-record notice wired into the CLI and the hub diagnostics; the
   `EVENER_PROVIDERS_CONFIG` tri-state at every reader (`load_client.go:38-41,
   120-124`, `main.go:233-235`, `agent/internal/liveeval/paths.go:37-44`,
   `launchconfig.ToEnv`, and the `t.Setenv(…, "")` idiom in
   `cmdutil/load_client_test.go:185-189`, which now means "no user layer")
   and `EVENER_CREDENTIALS_CONFIG` as an `envvars.Var` so the test mains
   scrub it; `envvars`
   roster, `cmdutil/seed.go`, `materialize.go`, `providercfg`,
   `model_catalog*.go`, the LiteLLM data, the wrapper packages,
   `openaicompat`, the old `openai` adapter, and the old
   `anthropic`/`google` adapter types deleted, along with the fuzz targets
   that exercise them (`llm/client_capabilities_fuzz_test.go`,
   `lcfg_config_surface_fuzz_test.go`, `client_config_edges_fuzz_test.go`,
   `core_contracts_fuzz_test.go`, `cmdutil/coverage_program_fuzz_test.go`,
   rewritten against the registry where they still have a subject);
   `docs/llm-providers.md`, `docs/llm-provider-config-and-launch.md`,
   `docs/ollama.md`, and the variable and sibling-file mentions in
   `README.md`, `docs/getting-started.md`, `docs/evener-hub.md`, and
   `docs/developing-evener/environment.md` rewritten around §3–§10. (~net
   −4,000 lines including tests.)
4. **Cloud providers** (later phase): the live-verified coverage of §13
   for Azure, Bedrock, and Vertex against the overlay entries of §9.
   Nothing in steps 1–3 is provisional for them: the transport fields
   (`Auth`, `AuthHeader`, `BaseURL`/`Endpoint`/`StreamEndpoint` templates
   with `{VAR}`, `HostRule`, presets, the `-` endpoint sentinel, `Body`
   constants, model-level `Transport` overlays, the cross-protocol
   inheritance rule, `WireID`), the authenticators, and the `Fields`
   baselines are designed for these three from the start.

### 14.1 Flag day

There is no migration code. After upgrading, a user does the following
once, and the release notes and the load-error pointer say so:

- **`providers.toml`**: an old-schema file (`[instances.*]`, `type`,
  `api_style`, `quirks`, `compat`) fails to load. The CLI exits with the
  pointer; the hub starts with implicit instances only, shows the error as
  a diagnostic, launches sessions against the implicit set, and refuses
  instance writes until the file is fixed (§10, §11.3). The user edits,
  deletes, or moves the file aside by hand. Most users need no file at all
  afterwards: every provider on the implicit list (§6.2) exists from its
  key, and `*_BASE_URL` variables cover proxies. A gateway or a custom-named
  instance is re-created with `evener providers add … --api-key-env NAME`
  or `--credential-header K=V` (§11.2); an instance with its own
  `base_url` never inherits the vendor key (§10), which today's
  `[instances.anthropic] base_url = …` shape did.
- **Default instance**: with more than one instance and no `default`, the
  default follows the ranking of §5.1 (`default_order`, then custom-named
  entries by name), not today's alphabetical registration order or the
  file's sorted-name rule; `GEMINI_API_KEY` + `OPENAI_API_KEY` now defaults
  to `openai`, not `google`, and a custom gateway entry never outranks an
  implicit vendor. Set `default` to keep the old pick.
- **Instance names**: `kimi`, `glm`, `kimi-anthropic`, `openrouter-anthropic`,
  and `openai-compatible`-as-a-vendor-name are gone; the registry ids are
  `moonshotai`, `zai`, `kimi-for-coding`, and (for the anthropic-protocol
  route to OpenRouter, which exists for MiniMax's Anthropic-style tool
  calls) the recipe

  ```toml
  [providers.orclaude]
  base     = "openrouter"
  protocol = "anthropic"
  [providers.orclaude.models."minimax/*"]
  surface  = "anthropic"
  ```

  A saved session, `launch.toml`, `EVENER_MODEL`, or plugin `model:`
  declaration that names an old instance fails with the unknown instance
  error naming the available instances.
- **Environment variables**: `KIMI_API_KEY` now means the Kimi coding plan
  (models.dev's convention); Moonshot's platform key is
  `MOONSHOT_API_KEY`; `GLM_API_KEY` is `ZHIPU_API_KEY`;
  `KIMI_CODING_API_KEY`, `KIMI_BASE_URL`, `KIMI_CODING_BASE_URL`,
  `GLM_BASE_URL`, `GEMINI_BASE_URL` (now `GOOGLE_BASE_URL`),
  `OPENAI_CHATGPT_BASE_URL` (now `OPENAI_CODEX_BASE_URL`), and
  `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` are not read. Every `*_BASE_URL` value
  now includes the version segment (§6.2).
- **Codex OAuth**: records are per instance, so `auth/openai.json` belongs
  to an instance named `openai`, which by default is the platform API and
  never reads it; `evener openai login` writes `auth/openai-codex.json` and
  the Codex instance is `openai-codex` (§9.5). `openai/…` means the
  platform API unless the user writes `[providers.openai] base =
  "openai-codex"`, in which case the old record is read as that instance's.
  A stray record (`auth/<name>.json` for any instance not on the Codex
  transport, including `auth/work.json` from `evener openai login
  --instance work`) produces a startup notice until it is removed with
  `evener openai logout --instance <name>` or deleted.
- **Credentials store**: entries keyed by an old instance name are ignored
  and reported by `evener providers list`; the hub's credentials pane
  re-enters them under the new names.
- **`[1m]` references**: only the Sonnet 4.5 and Opus 4.5 rows keep the
  suffix (§6.2); `claude-opus-4-6[1m]` and later are unknown ids.
- **Sessions with no `--reasoning-effort`** carry the row's `DefaultEffort`,
  else `medium`, clamped to its ladder (§7.4). Fable 5 and the rest of
  adaptive Claude run at `high`, their stated default; the budget-shaped
  Claude 4.5 generation, Gemini 2.5, and the zai/qwen toggles move from
  their provider's dynamic default to `medium`.
- **Context windows for Ollama and local models**: the LiteLLM `ollama/*`
  rows (8192 for `llama3.1`, `:tag` stripping) are gone; every live-only
  model on `ollama` or a pseudo-provider now budgets against the
  provider-level 131072 default (§6.2). Set the real window on a row
  (`[providers.ollama.models."llama3.1*"] context_window = 8192`) or
  compaction fires late.
- **Tri-state `EVENER_PROVIDERS_CONFIG`**: `export EVENER_PROVIDERS_CONFIG=`
  (present, empty) now means "no user layer" (§10); today it meant the
  default path. `evener providers list` and the hub diagnostics print
  "user layer: none (EVENER_PROVIDERS_CONFIG is empty)" so the state is
  visible. `gemini` is no longer accepted as an alias of `google` in model
  references.

None of this is detected or translated at runtime, and none of the old
files are renamed or deleted.

## 15. Decisions taken

- models.dev is the only upstream. LiteLLM's extra breadth is regional
  Bedrock/Azure/Vertex keys and non-chat modes we do not use; models.dev has
  those providers with per-model transport hints LiteLLM lacks.
- The embedded snapshot is raw upstream JSON. One converter, no generator.
- Protocol selection is explicit. The probe is a tool that writes config.
- One flat `Caps` plus `Fields` as a denylist over an enumerated set of
  paths no cap governs. Not four typed compat shapes.
- Merge precedence: effective layer order 1 → 2 → 3 → live → 4; within a
  layer provider → top-level glob → provider glob → row; globs tested
  against the reference and the matched or alias-target id, so they reach
  live-only, synthesized, and aliased ids; later layer wins regardless of
  level; alias seeds facts and, for same-provider targets, protocol and
  transport.
- Wire-shape derivation on the anthropic protocol is keyed on the row's
  family (or the provider's curated family for synthesized rows), never on
  its surface; the pins are the Opus 4.5 hybrid, Kimi, and MiniMax.
- `ThinkingAlwaysOn` is a builder concern, not a branch in the effort rule.
  No registry layer injects an effort; `agent` applies one rule for the
  effort a request carries, reading the row's `DefaultEffort` (§7.4,
  Jesse, 2026-08-30).
- The `openrouter` dialect sends `reasoning.effort` unconditionally, as
  today.
- Implicit instances only for the curated list; everything else is opt-in
  through `providers.toml`. Resolution never requires a credential;
  requests do.
- Flag day (Jesse, 2026-08-29): no migration, no runtime compatibility
  code. Old config, old instance names, old variables, and the old OAuth
  record path stop working; §14.1 is the checklist. `*_BASE_URL` overrides
  survive as a feature with models.dev's URL convention.
- Bedrock via Anthropic's Messages endpoint on `bedrock-mantle`, bearer
  token only. No SigV4, no event-stream framing, no AWS SDK dependency.
  Inference-profile ids (`global.`/`us.`/…) resolve for metadata but are
  hidden from listings — the Mantle endpoint serves only unprefixed Claude ids
  (§9.3, verified live 2026-08-31; the 2026-08-28 global verification predates
  this and its path is an open question).
- Surface is a model attribute derived from models.dev `family`; the
  provider fallback applies only to family-less and synthesized rows.
- 413 stays a context-length error; the classifier's new work is the
  structured body and the unrecognized-parameter hint.
- Ollama is never the default unless the user gives it a `default_model`
  and nothing outranks it; the `NonDefaultEligible` interface goes away.
- `EVENER_PROVIDERS_CONFIG=` (present, empty) means "no user layer";
  `EVENER_CREDENTIALS_CONFIG` names the store. The hub uses both to keep
  launching sessions while its own file fails to load.
- Vertex and Bedrock token counting is estimate-only; exact counting is
  [#565](https://github.com/prime-radiant-inc/evener/issues/565).
- Azure, Bedrock, and Vertex may land as a later phase (§14 step 4), but the
  data model, transport axis, and authenticators support them from steps
  1–2.
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
hybrid across every provider, `gemini-3`, `gpt-5*`, `minimax/*`,
`anthropic/*` (§4.1, §6.2); derived caps computed on the final merged row
(§7.4); `@default` rows re-keyed by the converter (§6.1); verbatim wire ids
for prefix/dated/live hits (§7.2); split prompt-cache gates so Codex keeps
`prompt_cache_key` (§7.5, §6.2); the credential-inheritance rule keyed on a
`base_url` override with the keyless auth schemes exempt (§10); the curated
`implicit` list and current-instance preference in `FindModel` (§5.1, §6.2,
§7.5); pseudo-providers as `generic` (§3.1, §6.2); the variable resolution
order with curated `Vars` as defaults (§9.1); the cross-protocol rule
extended to instances and the `work` example corrected (§4.2, §10);
non-Claude Bedrock rows hidden by rule (§6.1, §9.3); the OpenRouter
`reasoning_details` round-trip rule (§8.4); effort implied by
`EffortValues` and pass-through for unknown models (§7.4, §8.4); the
platform-side half of `responsesLiteModel` on `openai`'s `gpt-5.6*` rows
and the full Codex lite description (§6.2, §8.3, §9.5); the `llm` ↔
`registry` import direction with `ShapeRequest` and the interfaces in `llm`
(§4, §8.1); the `Client.Register` override map for test doubles and the
fuzz-target list (§8.1, §14); the spawn gate keyed on every auth scheme
(§11.3); `ollama-host` applied to the variable value (§6.2, §9.1); the
concrete per-dialect reasoning table including `clear_thinking: false`
(§8.4); 413 kept as context length and the §2 premise corrected (§2, §12);
the `RequestPreparer` hook for the Codex transport and its place in the
assembly order (§8.1, §8.2, §9.5); `stop` removed from the Responses set
(§8.2); the `anthropic-version` header left in place on Vertex (§9.4);
Vertex MaaS rows keeping `gcp-adc` (§4.3, §9.4); Anthropic `metadata` off at
baseline (§8.2); `status`/`logout` defaults (§9.5); `WebSearch` on
`google-vertex-anthropic` (§6.2); the storage-scope components (§7.6); the
Bedrock golden on a row that contests the pin and the `default_order`
fallback (§5.1, §13); the Azure Claude auth header (§9.2); the family-rule
wording (§6.1).

Revision 6 incorporated the fourth adversarial review (two reviewers, 16
scored findings, all accepted): the provider-level `thinking_shape` pin on
`anthropic` removed, with per-row derivation gated on the anthropic
protocol and a synthesized-row rule for uncataloged Claude ids (§6.2,
§7.4); `ThinkingDisplay` derived from the absence of `budget_tokens` instead
of a glob (§7.4, §8.3); `ThinkingAlwaysOn` derivation limited to the
anthropic protocol (§7.4); the effort control derived for cataloged
`reasoning: true` rows with no controls and for live/synthesized rows (§7.4,
§8.4); the provider Surface fallback limited to family-less and synthesized
rows (§3, §6.1, §6.2); `default_model` for every implicit provider and the
named-instance error (§5.1, §6.2); the Codex OAuth record rename with notice
(§9.5); the Azure Claude `api-key` header (§9.2); the definition of "serves"
for `FindModel` with `default_order` tie-break (§7, §7.5); resolution
without a credential and `WithInstances` for the test seam,
`Register(adapter)` kept keyed on `Name()`, override planners honored (§5.2,
§7.6, §8.1); the Codex `client_metadata` rule reading the `metadata` flag
directly (§9.5); Codex org/project headers removed (§6.2, §9.5); Ollama
`optional-bearer`, `OLLAMA_API_KEY`, and `OLLAMA_BASE_URL` (§4, §6.2, §9.1,
§11.3); the output cap ≥ window guard (§4.1, §6.1, §6.4, §7.4); classifier
order with specific codes first and generic types last, plus the
no-field-name hint (§12); `internal/openaichat` kept as shared helpers
(§8.1, §14); the registry as the authoritative prunable-path table (§8.1,
§8.2); the `gpt-5.6*` explicit `prompt_cache_retention = false` (§6.2); the
cross-protocol rule's inherited list including `HostRule` (§4.2); glob
tie-break (§4.1); `Hidden` not inherited (§4, §4.2); explicit instances also
needing a `DefaultModel` (§5.1); the `work` example's `surface = generic`
(§10); `ThinkingAlwaysOn` named as the one structural cap live may set (§5);
the Bedrock `StructuredOutput` pin scoped to the `*anthropic.*` glob (§6.2,
§9.3); the `google-vertex` `openai/*-maas` rows hidden (§6.1, §9.4); alias
seeding over layers 1–4 (§4.2); body constants overriding `ProviderOptions`
(§8.2); the `openai`/`string-thinking` always-on default `medium` (§8.4);
`probe --write` recording the protocol only (§11.2); the corrected appwire
field list (§11.3).

Revision 7 incorporated the fifth adversarial review (two reviewers, 10
scored findings, all accepted): `Model.Family` as the key for wire-shape
derivation so surface pins never change the body, with Kimi and MiniMax
pinned to the anthropic surface at row level and `budget` shapes (§3.2, §4,
§6.1, §6.2, §7.4, §15); toggle-only rows on the anthropic protocol deriving
`budget` (§7.4); the `anthropic` Mythos rows and the corrected coverage
sentence (§6.2); glob rows reaching live-only and synthesized ids (§4.1,
§7.2, §7.3, §8.3, §13); `Model.Hidden` with its recompute and clearing
rules, and dropped-vs-hidden made consistent (§4, §6.1, §11.1); alias rows
taking the target's protocol and transport when they set neither (§4.2,
§9.2); the `openrouter` dialect sending `reasoning.effort` unconditionally
(§8.4, §15); the one-time pre-load migration and the hub starting instead
of exiting (§1, §6.4, §9.5, §10, §14.1, §15); the override-adapter rule for
unresolvable names and `NewClient(WithRegistry)` (§8.1, §13); plus the
smaller items: the output-cap guard limited to catalog/live values; the
1237-row count; unknown effort values passing through; alias chains and
cycles as load errors; goal 1 reworded; the full implicit list as
`default_order`; `default` naming a missing instance; per-model `api`
placeholders feeding `VarsEnv`; case-sensitive ids; the always-on default
`medium` clamped; an explicit Ollama `base_url` bypassing the host rule;
`EnvVar` as the variable that resolved; the "usage limit" phrase kept on
429 and the parameter-token rule for the hint; `gcp-adc` shipped in step 2;
`client_metadata` omitted when empty; no credential warning for keyless
schemes.

Revision 8 incorporated the sixth adversarial review (two reviewers, 14
scored findings, all accepted): `providers.toml` and `credentials.toml`
placed under the config root, with only the catalog cache under the state
root (§5, §14.1); the migration rewriting the old file into the new schema
with `default` preserved and `base =` entries for the renamed instance
names, renaming credential-store entries, and defined failure/concurrency
rules, moved to step 1 (§1, §10, §13, §14, §14.1); `api_key_env` pins for
`moonshotai`, `kimi-for-coding`, and `zai` so today's `KIMI_API_KEY`,
`KIMI_CODING_API_KEY`, and `GLM_API_KEY` keep working without a Kimi
coding-plan default that 401s (§6.1, §6.2, §13); `*_BASE_URL` overrides as
templated base URLs with curated defaults (§6.2, §9.1, §10, §15); `[1m]`
alias rows for every 4.x Claude row so stored refs keep resolving, with the
beta header only on the 4.5 rows (§6.2, §13); the alias protocol/transport
import limited to same-provider targets so `anthropic/claude-mythos-5` stays
on the `anthropic` transport (§4.2); glob rows tested against the alias
target and the matched id so `azure/claude-prod` gets the Opus 4.5 hybrid
(§4.1, §4.2, §8.3, §9.2, §13); the merge defined per layer with the live
layer between 3 and 4 instead of a post-pass, so "later layer wins" and
"live never overrides the user" both hold (§4.1, §5, §7.2, §13, §15); the
session's `ThinkingAlwaysOn` → `medium` injection deleted and `ShapeRequest`
never adding an effort (§7.4, §7.5, §8.4, §13, §15); `unknown_parameter`,
`unsupported_parameter`, and the bare-token and `Unknown parameter:` shapes
in the hint rule (§12, §13); `Provider.Family` for synthesized rows instead
of keying the *claude* rule on the provider surface (§4, §6.2, §7.3, §7.4);
`Resolve` on hidden rows succeeding with a warning (§4.2, §4.4); the
`openrouter-anthropic` rationale corrected and its replacement named (§3.2,
§4.2); `credential-less default` as a warning (§5.1, §10); the Mythos
preview facts sourced from the LiteLLM snapshot with `reasoning` and
`effort_values` (§6.2); live `ThinkingAlwaysOn` only when `mandatory` is
`true` (§5); `golang.org/x/oauth2` as a direct dependency (§8.1, §14); the
OpenRouter MiniMax effort note (§6.2); load errors as hub diagnostics
(§11.3); the `default` credential-less warning instead of a load error
(§5.1).

Revision 9 applied Jesse's ruling that the cut-over is a flag day with no
runtime compatibility code, which removed the migration of revisions 7–8
and with it 14 of the 22 findings of the seventh review (the OAuth-record
rename versus the `openai` entry, the OAuth-plus-API-key precedence, the
credential-store renames, the `api_style` dimension of the id table, the
`compat`/`thinking_levels` translation, the hub-captured `base_url`,
`default` mapping, the no-file installs, the mixed-file trigger, the
step-1 rewrite that broke old readers, the `.pre-registry` naming under
`EVENER_PROVIDERS_CONFIG`, the scrub bypass, the store load order, and the
legacy `[1m]` refs); §14.1 is now the user-facing checklist. The remaining
findings were accepted: `Sampling` as an alias-inheritable fact replacing
the converter's `Fields["temperature"]` (§4.1, §6.1, §8.2, §8.3, §13);
`*_BASE_URL` templates redefined with models.dev's version-segment
convention, `vars_env` as a config key, and the pseudo-providers implicit
from `OPENAI_COMPATIBLE_BASE_URL` with `OPENAI_COMPATIBLE_API_KEY` (§6.2,
§9.1, §10, §14.1); `base` winning over a name match, `base` resolving
against the curated registry only, and `ProviderID` defined (§4.2); a
dangling curated alias degrading to a hidden row with cache validation by
full layered load (§4.2, §6.4); the credential-inheritance stop limited to
a literal `base_url` with the `CredentialTag` citation corrected (§10);
413 evaluated before the code table and `rate_limit_exceeded` scoped to 429
(§12); the parameter hint gated on the prunable set (§12); the glob typo
guard per matched row (§10); hub shadowing entries never carrying an
untouched `base_url` (§11.3); the OAuth record per instance with the stray
`auth/openai.json` notice (§9.5, §11.3); the `[1m]` rows limited to the
beta-gated 4.5 rows (§6.2); the unset-`$VAR` header drop noted as a change
(§10); and the Fable 5 `medium` → `high` note (§14.1).

Revision 10 incorporated the eighth adversarial review (two reviewers, 14
scored findings, all accepted): the implicit-instance credential test
made per auth scheme, `openai-codex` pinning `api_key_env = []`, and
pseudo-providers instances only when their base URL resolves and never in
`default_order` (§5.1, §6.2, §13); `Hidden` evaluated against the
environment and no longer marking pseudo-providers unconditionally (§4,
§6.2); the hub withholding `EVENER_PROVIDERS_CONFIG` from children and
refusing instance writes while its file fails to load, with the remedy by
hand (§10, §11.3, §13, §14.1); `evener providers add` credential flags and
probe skipping (§11.2, §14.1); the `openrouter-anthropic` recipe pinning
the anthropic surface for MiniMax (§3.2, §13, §14.1); `Sampling` assertions
corrected (Opus 4.5 accepts temperature) and the stale
`claude-opus-4-6[1m]` golden replaced (§8.3, §13); the curated dangling
alias degrade and cache-validation tests (§13); the `max_tokens_field` hint
and 400/422 deferring to the message rows (§12); the `default_order`
change, custom-named OAuth records, the credential-inheritance
generalization, and the generic stray-record notice added to the flag-day
checklist (§9.5, §14.1); `evener models` treating an old-schema file as
absent during steps 1–2 (§14); the pre-cut-over turn fallback for the
replay scope (§7.5); the `default` rules for non-implicit ids and for an
Ollama entry with `default_model` (§5.1, §13); the credential stop comparing
against substituted defaults (§10); the version-less variable list
completed (§6.2); the `<ID>` uppercase rule (§6.2); and the sandbox path
citation (§7.5).

Revision 11 incorporated the ninth adversarial review (two reviewers, 9
scored findings, all accepted), which reported §1–§3, §6–§9, §12, and §15
as converged: the hub keeps sessions launching through an explicitly empty
`EVENER_PROVIDERS_CONFIG` ("no user layer") and a new
`EVENER_CREDENTIALS_CONFIG`, instead of withholding a variable the child
would recompute (§10, §13, §15); `Hidden` governs listings and implicitness
only, with an unresolved template variable a warning at resolve time and an
error at first request, so `inspect` works for the cloud providers (§4.2,
§5.1, §9.1, §10); the cloud providers need their template variables to be
implicit, and the §13 fixture says so (§5.1, §13); an unset `$VAR` in
`api_key` is a warning, not a resolve error (§10); a credentials-store
entry under the instance name satisfies the credential stop and `add`
consults it, with `<NAME>_API_KEY` defined for custom names (§10, §11.2);
the `max_tokens_field` hint suggests the spelling not in effect (§12); the
default ranking keeps a shadowed curated id at its `default_order`
position and ranks custom entries after the list, disclosed in §14.1 with
§15 corrected (§5.1, §13, §14.1, §15); plus the minor items: `--credential-header`
values as `$VAR` references (§11.2); §7.4 steps 4–6 gated on `Reasoning`;
`Provider.APIKey` and the `Credential` type (§4); unknown TOML keys as load
errors (§10); `ProviderID` defined through `base` (§4.2); `FindModel`
ranking for mixed explicit and implicit servers (§7.5); the sorted-name
attribution corrected (§5.1); `registry.Load` offline under
`testing.Testing()` (§6.4); and the credentials pane listing every curated
implicit provider (§11.3).

Revision 12 incorporated the tenth adversarial review (two reviewers, 6
scored findings, all accepted; both reported the data model, resolution,
protocols, transports, errors, and implementation order converged): the
credential order defined end to end, with `<NAME>_API_KEY` only for
non-registry names so the endpoint stop cannot be bypassed through the
store's name layer (§4, §10, §11.2); the Ollama and pseudo-provider
provider-level context-window default with the §14.1 disclosure (§6.2,
§14.1); `Authenticator.Apply` taking `Resolved`, with the Codex
authenticator owning the constant headers on every request including
listing (§8.1, §9.5); the `testing.Testing()` offline default with the
`WithFetcher`/`Offline: false` opt-out and a named `registry.Refresh`
(§6.4); the `evener/auth/*` RPC family added to the hub change list
(§11.3); plus the minor items: `Credential` without a registry-side
fingerprint and with a `credential_headers` source, the client HMAC in the
continuation scope (§4, §7.6); the `ProviderID` comment (§4.4); the
`{openai, ollama}` fixture (§13); curated-only overlay keys and the
`default_order` array (§10); goal 1 qualified for the cloud providers
(§1); the tri-state variable's reader sites, the test idiom, and
`EVENER_CREDENTIALS_CONFIG` as an `envvars.Var` (§14); the `gemini` alias
and the tri-state in the checklist (§14.1); the wider docs list (§14); and
the `--credential-header` wording (§11.2).
