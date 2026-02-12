# Audit: Sections 1-2 (Overview and Architecture)

Auditor: Claude Opus 4.6
Date: 2026-02-11
Spec: `/Users/jesse/prime-radiant/serf/unified-llm-spec.md` lines 1-345

## Summary

- Total requirements checked: 42
- Implemented: 28
- Partial: 9
- Missing: 4
- N/A: 1

## Findings

### 1.1 Problem Statement
- [x] IMPLEMENTED: Unified interface across OpenAI, Anthropic, Gemini. All three providers have native adapters in `internal/llm/providers/{openai,anthropic,google}/adapter.go`.
- [x] IMPLEMENTED: Switching providers by changing model/provider string. `Request.Provider` field routes to the correct adapter; `Request.Model` uses native model strings.

### 1.2 Design Principles
- [x] IMPLEMENTED: Provider-agnostic application code. The `Request`/`Response`/`Stream` types in `types.go` are provider-neutral. Provider-specific features pass through `ProviderOptions`.
- [x] IMPLEMENTED: Minimal surface area. The public API is compact: `Client`, `Generate`, `StreamGenerate`, `GenerateObject`, `StreamGenerateObject`, plus types.
- [x] IMPLEMENTED: Streaming-first. `Complete()` and `Stream()` are separate methods with distinct return types (`Response` vs `Stream`).
- [x] IMPLEMENTED: Composable middleware. `middleware.go` defines the `Middleware` interface; `Client.Use()` registers middleware.
- [x] IMPLEMENTED: Escape hatches via `ProviderOptions`. All three adapters read `req.ProviderOptions[providerName]` and merge keys into the request body.

### 2.1 Four-Layer Architecture
- [x] IMPLEMENTED: **Layer 1 -- Provider Specification.** `client.go` defines `ProviderAdapter` interface (Name/Complete/Stream) and shared types in `types.go`. These are pure interface + type definitions.
- [x] IMPLEMENTED: **Layer 2 -- Provider Utilities.** `sse.go` (SSE parsing), `retry_util.go` (retry logic), `ratelimit.go` (rate limit header parsing), `media_utils.go` (image helpers), `errors.go` (error normalization), `adapter_timeout.go` (timeout helpers) all serve as shared utilities for adapter authors.
- [x] IMPLEMENTED: **Layer 3 -- Core Client.** `client.go` `Client` struct: holds registered adapters, routes by provider, applies middleware.
- [x] IMPLEMENTED: **Layer 4 -- High-Level API.** `generate.go` (`Generate`), `stream_generate.go` (`StreamGenerate`), `generate_object.go` (`GenerateObject`, `StreamGenerateObject`) wrap Client with tool loops, prompt standardization, retries, structured output validation.

### 2.2 Client Configuration

#### Environment-Based Setup
- [x] IMPLEMENTED: `NewFromEnv()` in `env_registry.go` constructs a Client from env vars. Matches spec's `Client.from_env()`.
- [x] IMPLEMENTED: OpenAI reads `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID`.
- [x] IMPLEMENTED: Anthropic reads `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`.
- [x] IMPLEMENTED: Gemini reads `GEMINI_API_KEY` with `GOOGLE_API_KEY` fallback, and `GEMINI_BASE_URL`.
- [x] IMPLEMENTED: Only providers with keys present are registered. First registered becomes default.

#### Programmatic Setup
- [x] IMPLEMENTED: Adapters can be constructed explicitly with `&openai.Adapter{APIKey: ..., BaseURL: ...}` and registered via `Client.Register()`. All adapters accept `DefaultHeaders` and `Client` (*http.Client) fields.
- [ ] PARTIAL: **Timeout field on adapter.** Spec shows `timeout = 30.0` in programmatic setup. Adapters use `http.Client{Timeout: 0}` (no timeout) and rely on context deadlines instead. Per-request `AdapterTimeout` struct exists but there is no adapter-level default timeout field. This is a design choice but deviates from spec's programmatic example.

#### Provider Resolution
- [x] IMPLEMENTED: `Request.Provider` routes to named adapter. Falls back to `defaultProvider`. Raises `ConfigurationError` when neither is set.

#### Model String Convention
- [x] IMPLEMENTED: Native model strings used directly (no custom namespace). `Request.Model` passed through as-is.

### 2.3 Middleware / Interceptor Pattern
- [x] IMPLEMENTED: `Middleware` interface in `middleware.go` with `WrapComplete` and `WrapStream`. `MiddlewareFunc` convenience type. `Client.Use()` registers middleware.
- [x] IMPLEMENTED: **Execution order.** `applyMiddlewareComplete`/`applyMiddlewareStream` iterate in reverse registration order when wrapping, producing the correct onion pattern (first-registered = outermost = first to execute on request, last on response).
- [x] IMPLEMENTED: **Streaming middleware.** `WrapStream` wraps the `StreamFunc`, allowing middleware to intercept streaming requests. Middleware receives the `Stream` return value and can wrap it.
- [ ] PARTIAL: **Streaming event-level middleware.** Spec says middleware "wraps the event iterator and can observe or transform individual stream events" (the `YIELD event` pattern). Current `WrapStream` wraps at the `Stream` return level but does not provide a built-in mechanism to intercept individual events from the channel. Middleware would need to manually create a new `ChanStream` and forward/transform events, which is possible but not ergonomically supported. No example or helper exists for event-level middleware.

### 2.4 Provider Adapter Interface
- [x] IMPLEMENTED: `ProviderAdapter` interface: `Name() string`, `Complete(ctx, Request) (Response, error)`, `Stream(ctx, Request) (Stream, error)`.
- [x] IMPLEMENTED: **Two separate methods** (Complete vs Stream) with distinct return types, matching spec rationale.
- [x] IMPLEMENTED: **No separate send_tool_outputs method.** Tool results are sent as messages in conversation history.

#### Optional Adapter Methods
- [x] IMPLEMENTED: `Closer` interface with `Close() error`. `Client.Close()` calls it on all adapters.
- [ ] PARTIAL: `Initializer` interface with `Initialize(ctx) error` exists. `Client.Initialize()` calls it. **However, spec says "Called by Client on registration"** -- the actual `Register()` method does NOT call `Initialize`. It must be called separately by the application. This deviates from the spec's stated behavior.
- [x] IMPLEMENTED: `ToolChoiceSupporter` interface with `SupportsToolChoice(mode) bool`. `Client.SupportsToolChoice()` delegates.
- [ ] MISSING: **No adapter currently implements `Initializer` or `ToolChoiceSupporter`.** These interfaces exist on the Client side but none of the three built-in adapters (`openai`, `anthropic`, `google`) actually implement them. They are dead code paths at present.

### 2.5 Module-Level Default Client
- [x] IMPLEMENTED: `DefaultClient()` in `env_registry.go` lazily initializes from env vars on first use. `SetDefaultClient()` overrides it.
- [x] IMPLEMENTED: `Generate()`, `StreamGenerate()`, etc. use `DefaultClient()` when `opts.Client` is nil.
- [ ] PARTIAL: **Per-call client override.** Spec shows `generate(... client = my_client)`. Implementation uses `opts.Client` field, which works, but it's a struct field rather than the more ergonomic pattern shown in spec. This is a Go idiom difference and is acceptable -- marking as partial only because the spec says "Or pass explicitly per call" and the Go implementation does support it via the options struct.

### 2.6 Concurrency Model
- [ ] PARTIAL: **Async-first.** Go does not have async/await; it uses goroutines. `Complete()` and `Stream()` are blocking calls. This is the idiomatic Go approach and correct for the language, but the spec says "All provider calls are non-blocking. The complete() and stream() methods are asynchronous." The Go implementation relies on goroutine-based concurrency instead. Callers use `go` keyword for parallelism. N/A for the "provides both async and sync wrappers" part since Go has a single concurrency model.
- [x] IMPLEMENTED: **Multiple concurrent requests are safe.** Client holds no mutable state between requests (providers map is set at registration time). Adapters use independent `http.Client` instances with no mutable state.
- [ ] PARTIAL: **Provider adapters must be safe for concurrent use.** The adapters have a subtle race: `Complete()` and `Stream()` both check `if a.Client == nil` and assign a new `http.Client`. If called concurrently for the first time, this is a data race. In practice it's harmless (both would create identical clients) but it is technically unsafe. The `NewFromEnv()` constructors do initialize `Client`, so the race only happens with manually constructed zero-value adapters.

### 2.7 Native API Usage
- [x] IMPLEMENTED: **OpenAI uses Responses API** (`/v1/responses`). Confirmed in `openai/adapter.go` line 151: `a.BaseURL+"/v1/responses"`.
- [x] IMPLEMENTED: **Anthropic uses Messages API** (`/v1/messages`). Confirmed in `anthropic/adapter.go` line 169: `a.BaseURL+"/v1/messages"`.
- [x] IMPLEMENTED: **Gemini uses native API** (`/v1beta/models/*/generateContent`). Confirmed in `google/adapter.go` line 180.
- [x] IMPLEMENTED: **OpenAI-compatible adapter** exists separately (`openaicompat/`) using Chat Completions API for non-native providers (Ollama, vLLM, etc.). This correctly keeps it separate from the native OpenAI adapter.

### 2.8 Provider Beta Headers and Feature Flags

#### Anthropic beta headers
- [x] IMPLEMENTED: `betaHeaderFromProviderOptions()` reads `provider_options.anthropic.beta_headers` (string, []string, or []any) and joins into comma-separated `anthropic-beta` header. Applied in both `Complete()` and `Stream()`.
- [x] IMPLEMENTED: Prompt caching beta header (`prompt-caching-2024-07-31`) is automatically appended when auto-cache is enabled.

#### OpenAI feature flags
- [x] IMPLEMENTED: `provider_options.openai` keys are merged directly into the request body, allowing any feature flags. Web search is supported as `{"type": "web_search"}` tool.

#### Gemini configuration
- [x] IMPLEMENTED: `provider_options.google` AND `provider_options.gemini` are both checked and merged into the request body, allowing safety settings and other Gemini-specific configuration.

#### Provider options escape hatch
- [x] IMPLEMENTED: All four adapters (openai, anthropic, google, openai-compatible) support the `provider_options` escape hatch.

### 2.9 Model Catalog

#### ModelInfo Record
- [x] IMPLEMENTED: `ModelInfo` struct in `model_catalog.go` has all spec fields: `ID`, `Provider`, `DisplayName`, `ContextWindow`, `MaxOutputTokens`, `SupportsTools`, `SupportsVision`, `SupportsReasoning`, `InputCostPerMillion`, `OutputCostPerMillion`, `Aliases`.

#### Lookup Functions
- [x] IMPLEMENTED: `GetModelInfo(modelID)` returns catalog entry or nil.
- [x] IMPLEMENTED: `ListModels(provider)` returns all models, optionally filtered.
- [x] IMPLEMENTED: `GetLatestModel(provider, capability)` returns the best model by context window size, filtered by capability ("tools", "vision", "reasoning").

#### Catalog Data
- [x] IMPLEMENTED: Embedded LiteLLM catalog in `data/litellm_model_catalog.json` loaded via `model_catalog_embedded.go`. Catalog is a data file that can be updated independently.
- [ ] MISSING: **Catalog is not shipped as a standalone updateable data file.** Spec says "should be shipped as a data file (JSON or similar) that can be updated independently of the library code." The file IS embedded via Go's `embed` directive, but there's no mechanism to load an external/updated catalog file at runtime as a replacement. `LoadModelCatalogFromLiteLLMJSON(path)` exists for explicit loading but isn't wired into `DefaultClient()` or `EmbeddedModelCatalog()` as an override. An `XDG_DATA_HOME` based override or similar is absent.
- [ ] MISSING: **Unknown model strings are not explicitly documented as pass-through.** Spec says "The catalog is advisory, not restrictive -- unknown model strings are still passed through to the provider." This IS the actual behavior (the catalog is metadata-only, never consulted during request routing), but it's implicit rather than enforced/documented. Models not in the catalog work fine because the adapter just sends whatever model string it receives. This is correct behavior but the advisory nature could be more explicit in code comments.

### 2.10 Prompt Caching

#### OpenAI (Automatic)
- [x] IMPLEMENTED: Uses Responses API (which has automatic server-side caching). Reports `cache_read_tokens` from `usage.input_tokens_details.cached_tokens`.
- [ ] MISSING: **OpenAI `cache_write_tokens` not reported.** Only `CacheReadTokens` is populated from `input_tokens_details.cached_tokens`. The `CacheWriteTokens` field is never set for OpenAI. The Responses API may not expose this, but the spec says "report cache_read_tokens from usage" which is done. However, for parity with the other providers, checking if OpenAI exposes write tokens would be thorough.

#### Gemini (Automatic)
- [x] IMPLEMENTED: Automatic prefix caching; reports `cache_read_tokens` from `usageMetadata.cachedContentTokenCount`.
- [ ] PARTIAL: **Gemini `cache_write_tokens` not reported.** Only `CacheReadTokens` is populated. The `CacheWriteTokens` field is never set for Gemini. The Gemini API may not expose write counts, but this represents incomplete cache statistics mapping relative to the spec requirement "map these to `Usage.cache_read_tokens` and `Usage.cache_write_tokens`."
- [ ] MISSING: **Gemini explicit `cachedContent` API not exposed via `provider_options`.** Spec says "Expose explicit caching via `provider_options`." While `provider_options.google` can pass arbitrary keys into the request body, there's no documentation, example, or explicit support for the `cachedContent` field. A caller would need to know the Gemini API structure to pass it manually.

#### Anthropic (Auto cache_control injection)
- [x] IMPLEMENTED: **Automatic cache_control injection is enabled by default.** `anthropicAutoCacheEnabled()` returns `true` unless `provider_options.anthropic.auto_cache` is explicitly set to `false`. System prompt gets `cache_control: {type: "ephemeral"}`, last tool gets it, and `addCacheControlBreakpoint()` adds it to the message just before the last user message.
- [x] IMPLEMENTED: **Cache statistics reported.** `cache_read_input_tokens` -> `CacheReadTokens`, `cache_creation_input_tokens` -> `CacheWriteTokens`.
- [x] IMPLEMENTED: **Prompt caching beta header auto-appended** when auto-cache is active (`prompt-caching-2024-07-31`).

---

## Gap Summary (Actionable Items)

| # | Section | Severity | Gap |
|---|---------|----------|-----|
| 1 | 2.3 | Low | No ergonomic helper for event-level streaming middleware (wrapping individual stream events requires manual ChanStream construction) |
| 2 | 2.4 | Medium | `Register()` does not call `Initialize()` as spec states ("Called by Client on registration"). Must be called separately. |
| 3 | 2.4 | Low | No built-in adapter implements `Initializer` or `ToolChoiceSupporter` -- the optional interfaces are dead code |
| 4 | 2.6 | Low | Minor data race in adapters when `a.Client` is nil and `Complete()`/`Stream()` called concurrently on a zero-value adapter (not reachable via `NewFromEnv()`) |
| 5 | 2.9 | Low | No runtime catalog override mechanism (catalog is embed-only; `LoadModelCatalogFromLiteLLMJSON` exists but isn't wired as an override path) |
| 6 | 2.9 | Informational | Pass-through of unknown model strings is correct behavior but not explicitly documented/tested as a guarantee |
| 7 | 2.10 | Low | OpenAI adapter does not populate `CacheWriteTokens` (may not be available from API) |
| 8 | 2.10 | Low | Gemini adapter does not populate `CacheWriteTokens` (may not be available from API) |
| 9 | 2.10 | Low | Gemini explicit `cachedContent` API not documented or explicitly supported via provider_options |
