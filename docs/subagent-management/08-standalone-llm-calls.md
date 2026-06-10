# Standalone LLM Calls and Lightweight Helpers

Status: Proposed evergreen spec. This doc defines the lightweight-helper path as standalone LLM calls. It is not a new subagent layer, not a job registry, and not a hidden child-session runtime.

## Purpose

Give Serf and SDK embedders a clear, small pattern for one-off model calls used by bounded helper work: summaries, titles, lint-style checks, classification, object extraction, hook prompt handlers, and prompt transformations.

Subagents remain the right abstraction for isolated multi-turn work with tools, progress, lifecycle control, and child transcripts. Lightweight helpers are ordinary LLM calls owned by the current operation.

## Goals

- Reuse the existing `llm.Client`, `llm.Request`, `llm.Generate`, `llm.GenerateObject`, and `llm.StreamGenerate` paths instead of inventing a second provider abstraction.
- Keep helper calls standalone: no subagent job, no child transcript, no task-store mutation, no tool loop unless the caller deliberately uses a lower-level API outside the helper contract.
- Provide a precise helper API pattern that is thin enough to implement incrementally and test with fake adapters.
- Preserve existing middleware, provider selection, request validation, usage, adapter timeout/error stamping, and error classification behavior. Helpers built on `llm.Generate`/`StreamGenerate` also use the generate retry path; helpers that call `llm.Client.Complete` directly preserve validation/middleware/provider stamping/error typing only and must wrap retry explicitly if they need it. Complete-based helper calls preserve API logging when the provided client has logging middleware attached; streaming helpers preserve stream middleware, but current API logging middleware does not log streams until `APILogger.WrapStream` is implemented. Active rate limiting should not be implied beyond current rate-limit header parsing/metadata.
- Make helper calls observable through their owning operation, hook, or session event rather than through `job_list` or delegate/job lifecycle tools.
- Keep helper prompts bounded, deterministic where possible, and easy to unit test.

## Non-goals

- No new subagent layer for helpers.
- No hidden child sessions for web fetch, session naming, hook prompt handlers, or classifiers.
- No new provider registry, middleware abstraction, streaming protocol, transcript format, task system, or prompt-template engine.
- No automatic tool, MCP, web, project-doc, skill, or session-history access.
- No autonomous agent loop, retry loop outside the existing LLM path, or helper-specific lifecycle-control tools.
- No broad framework for all possible LLM utilities; add helpers only when two or more call sites need the same request construction or result handling.

## Current implementation anchors

LLM package anchors:

- `llm/client.go`: `ProviderAdapter`, `Client`, `Client.Complete`, `Client.Stream`, and `Client.Use`. The client already validates requests, resolves providers, applies middleware, stamps provider metadata, and wraps errors.
- `llm/types.go`: `Request`, `Response`, `Message`, `Usage`, `ResponseFormat`, and provider/model request fields.
- `llm/generate.go`: `GenerateOptions`, `GenerateResult`, `Generate`, tool-loop options, retry policy, timeouts, and request construction.
- `llm/generate_object.go`: `GenerateObjectOptions`, `GenerateObject`, `StreamGenerateObject`, schema response format, JSON parsing, and schema validation.
- `llm/stream_generate.go`: `StreamGenerate`, `StreamResult`, `TextStream`, `Response`, `PartialResponse`, `TotalUsage`, and `Steps`.
- `llm/middleware.go`: `Middleware`, `CompleteFunc`, `StreamFunc`, and shared middleware application.
- `llm/retry.go` and `llm/retry_util.go`: existing retry machinery used by high-level generate paths.
- `llm/ratelimit.go`: current rate-limit header parsing/metadata helpers; do not treat this as an active limiter unless one is added.
- `llm/classify.go`: error classification (`Classify`) for retry/fallback/permanent behavior. Do not confuse this with label-classification helpers; name any label helper to avoid ambiguity if needed.
- `llm/apilog.go`: middleware-based API logging support that Complete-based helper calls preserve when the provided client has the middleware attached. Current `APILogger.WrapStream` is a passthrough, so streaming helpers are not API-logged until stream logging is implemented.

Session and helper-call anchors:

- `agent/session_model_call.go`: full session model request preparation, context management, tool definitions, warnings, and provider error handling. Standalone helpers must not implicitly take on this session runtime behavior.
- `agent/tool_web_fetch.go`: current side call example. `webFetch` fetches content, builds a cheap-model `llm.Request`, then calls `s.client.Complete` for Q&A.
- `agent/session_namer.go`: current bounded helper example. `nameSession` uses one cheap-model `llm.GenerateObject` call with a schema, timeout, max tokens, and usage returned in `sessionNameResult`.
- `agent/internal/hooks/hooks.go`: prompt hooks currently adapt `*llm.Client` to a small `promptHookClient` and execute one `llm.Request` via `Complete`.
- `agent/internal/contextmgr/fork_summarize.go`, `context_manager.go` / `summarizeWithLLM`, `strategy_recursive_distill.go`, `strategy_checkpoint_pred.go`, and `strategy_memory_crystals.go`: current context-management side calls for summaries/checkpoints/crystals.
- `agent/session_tools.go` / `describeImage`: current helper-like side call owned by tool execution diagnostics.
- `cmd/llmcall/main.go`: dedicated standalone LLM-call command built with `llm.NewFromEnv()` today; include it in inventory or explicitly keep it out of helper extraction if CLI behavior should remain separate.
- `cmdutil/load_client.go`: config-driven `LoadClient` and provider-config loading. SDK helper construction should reuse or factor this behavior where appropriate, not duplicate credential/config logic in helper code. Do not assume `cmd/llmcall` already uses this path unless it is migrated deliberately.

## Definition

A lightweight helper is a bounded standalone LLM call with these properties:

- invoked by an owning operation such as a tool, hook, session namer, SDK utility, or diagnostic path;
- represented as `llm.Client.Complete`, `llm.Client.Stream`, `llm.Generate`, `llm.GenerateObject`, or `llm.StreamGenerate` work;
- has no subagent ID;
- is not registered with the subagent manager;
- cannot be resumed, waited on, steered, or closed through subagent lifecycle tools;
- does not write a child transcript or mutate the task store;
- does not execute tools under the helper API pattern;
- returns result text/object/stream plus usage/provider/model metadata when available;
- surfaces errors through the owning operation.

## Use a helper when

- The task should complete in one model request, or in one existing high-level `GenerateObject`/stream call without agentic follow-up.
- The task does not need tools or filesystem/network access beyond data the caller explicitly provides.
- The output is small and immediately consumed by the caller.
- Failure should be reported as part of the current operation, not as a child-agent failure.
- A persistent child identity, transcript, lifecycle hooks, or progress status would be noise.

Examples:

- answer a question over already-fetched page text in `web_fetch`;
- generate a session title;
- run the LLM-call transport for a prompt hook; hook-owned parsing currently uses the shared hook-output contract documented in [`hooks.md`](../hooks.md) (`continue`, `systemMessage`, `hookSpecificOutput`, and top-level `decision`/`reason`), while `{ok, reason}` remains a future prompt/agent compatibility target (see [`07-lifecycle-hooks-claude-compat.md`](07-lifecycle-hooks-claude-compat.md)) until implemented;
- classify a short diagnostic into a fixed label set;
- extract a JSON object from caller-provided text;
- summarize a bounded chunk of already-selected history.

## Use a subagent when

- The task needs tools.
- The task may require multiple turns, investigation, retries with changed strategy, or user steering.
- The parent needs `job_send_message`, `job_read_output`, `job_stop`, progress, or child status.
- The work should be isolated from the parent context.
- A child transcript/audit trail is required.

## Exact helper API pattern

Do not introduce a second runtime. Implement helpers as thin wrappers over the existing `llm` package.

### Shared options and result shape

The evergreen helper shape should be this small, tool-free subset:

```go
type HelperCallOptions struct {
    Client *llm.Client

    Provider string
    Model    string

    System   string
    Messages []llm.Message
    Prompt   *string

    Temperature     *float64
    TopP            *float64
    MaxTokens       *int
    StopSequences   []string
    ReasoningEffort *string
    ResponseFormat  *llm.ResponseFormat
    Metadata        map[string]string
    ClientMetadata  map[string]string

    TimeoutTotal   time.Duration
    TimeoutPerStep time.Duration
}

type HelperTextResult struct {
    Text         string
    Message      llm.Message
    Response     llm.Response
    Usage        llm.Usage
    TotalUsage   llm.Usage
    Provider     string
    Model        string
    FinishReason llm.FinishReason
}
```

Rules:

- `context.Context` is mandatory on every helper function.
- `Client` is required for SDK/public helper calls. Internal call sites may use existing `llm.Generate` default-client behavior only when that is already the local convention.
- Accept either non-nil `Prompt` or non-empty `Messages`, not both. If `System` is set, prepend it as a system message using existing `llm.System` semantics.
- Helper options intentionally omit `Tools`, `ToolChoice`, `MaxToolRounds`, `WebSearch`, `SessionID`, `ThreadID`, and transcript/task fields. `TopP`, `StopSequences`, and `ResponseFormat` may remain in the helper shape because existing standalone command paths already expose them and they do not grant tools or session access.
- If a caller needs those omitted fields, it is no longer using the lightweight helper contract; use the lower-level `llm` API explicitly or a subagent.

### Text helper

```go
func GenerateHelperText(ctx context.Context, opts HelperCallOptions) (HelperTextResult, error) {
    // Validate opts.Client, prompt/messages, and any helper-specific bounds.
    // Build llm.GenerateOptions with no Tools, ToolChoice none where supported, and MaxToolRounds fixed to 0.
    // Call llm.Generate(ctx, genOpts).
    // Copy Text, Response, Usage, TotalUsage, FinishReason, and Provider/Model from res.Response.
}
```

Required construction:

```go
zeroToolRounds := 0
genOpts := llm.GenerateOptions{
    Client:          opts.Client,
    Provider:        opts.Provider,
    Model:           opts.Model,
    System:          stringPtrOrNil(opts.System),
    Messages:        opts.Messages,
    Temperature:     opts.Temperature,
    TopP:            opts.TopP,
    MaxTokens:       opts.MaxTokens,
    StopSequences:   append([]string(nil), opts.StopSequences...),
    ReasoningEffort: opts.ReasoningEffort,
    ResponseFormat:  opts.ResponseFormat,
    Metadata:        opts.Metadata,
    ClientMetadata:  opts.ClientMetadata,
    MaxToolRounds:   &zeroToolRounds,
    TimeoutTotal:    opts.TimeoutTotal,
    TimeoutPerStep:  opts.TimeoutPerStep,
}
if opts.Client.SupportsToolChoice(opts.Provider, "none") {
    genOpts.ToolChoice = &llm.ToolChoice{Mode: "none"}
}
```

If `Prompt` is used, set `genOpts.Prompt = opts.Prompt`; if `Messages` is used, set `genOpts.Messages`. Never set `Tools` in the helper. Set `ToolChoice{Mode:"none"}` only when the resolved client/provider supports it; otherwise rely on `Tools:nil`, `MaxToolRounds=0`, and post-call `ToolCalls` rejection. `MaxToolRounds=0` is a safety belt against execution, not sufficient isolation if tools are accidentally populated; helper validation must keep `GenerateOptions.Tools == nil` and reject any returned passive `ToolCalls` unless a caller explicitly drops to the lower-level API. Populate helper result `Provider` from `res.Response.Provider`; populate `Model` from `res.Response.Model` and fall back to `opts.Model` when the adapter leaves the response model empty.

### Object helper

```go
type HelperObjectOptions struct {
    HelperCallOptions
    Schema map[string]any
}

func GenerateHelperObject(ctx context.Context, opts HelperObjectOptions) (*llm.GenerateResult, error) {
    // Validate schema is non-nil.
    // Build the same tool-free GenerateOptions.
    // Call llm.GenerateObject(ctx, llm.GenerateObjectOptions{...}).
}
```

The result may initially be `*llm.GenerateResult` to avoid needless DTO churn. Add a typed wrapper only when callers need it. `llm.GenerateObject` already stores parsed output in `GenerateResult.Output`.

### Stream helper

```go
func StreamHelperText(ctx context.Context, opts HelperCallOptions) (*llm.StreamResult, error) {
    // Build the same tool-free GenerateOptions.
    // Call llm.StreamGenerate(ctx, genOpts).
}
```

Streaming helpers must return the existing `*llm.StreamResult`/`llm.Stream` pattern. They must not invent another event protocol and must not buffer the full stream unless the caller asks by calling `Response` or an accumulator.

### Label classification helper

Because `llm.Classify` already classifies provider errors, avoid exporting another `llm.Classify` with a different meaning. If a label classifier is added, name it explicitly:

```go
type LabelClassificationResult struct {
    Label      string
    Confidence *float64
    Rationale  string
    Raw        *llm.GenerateResult
}

func ClassifyLabel(ctx context.Context, opts HelperCallOptions, labels []string) (LabelClassificationResult, error) {
    // Validate non-empty unique labels.
    // Use GenerateHelperObject with a small schema: label enum, optional confidence/rationale.
}
```

## Client construction

Keep one documented lightweight client-construction path for SDK users, but do not duplicate CLI-only logic unnecessarily.

Candidate API:

```go
type LLMClientLoadOptions struct {
    StateDir       string
    ProvidersPath  string
    Provider       string
    Model          string
    APILog         bool
    Middleware     []llm.Middleware
    TestClient     *llm.Client
}

type LLMClientInfo struct {
    ProviderNames   []string
    DefaultProvider string
    ConfigPath      string
}

func LoadLLMClient(ctx context.Context, opts LLMClientLoadOptions) (*llm.Client, LLMClientInfo, error)
```

Implementation guidance:

- If this is only needed by commands, keep using `cmdutil.LoadClient`.
- If SDK embedders need it, factor provider config and credential loading out of `cmdutil` instead of importing command utilities into libraries.
- Do not make helper construction write provider config, session metadata, transcripts, task files, or cache files as a side effect. Existing provider-config behavior in `cmdutil/load_client.go` intentionally seeds absent config in memory without writing.

## Runtime boundaries

Standalone helpers must not:

- call `delegate` or create a `Session`;
- appear in `job_list` or child job status;
- run subagent lifecycle hooks;
- write transcripts or session meta;
- mutate task state;
- automatically read project docs, skills, MCP resources, filesystem paths, URLs, or prior history;
- execute tools;
- bypass provider/model policy implemented in the client/request layer;
- bypass middleware, the intended retry path for the chosen underlying API, rate-limit metadata propagation, Complete-based API logging middleware when attached, stream middleware for streaming helpers, or adapter timeouts.

Helpers may:

- receive explicit text/messages/data from the caller;
- use caller-provided `*llm.Client`;
- use caller-provided provider/model choices;
- return usage/model/provider metadata;
- attach diagnostics to the owning operation's event/result/log.

## Observability and diagnostics

Helper calls should be visible, but not as agents.

Recommended event/result metadata shape:

```json
{
  "helper_name": "session_namer",
  "owner": "session_init",
  "provider": "openai",
  "model": "gpt-...",
  "usage": {"input_tokens": 1000, "output_tokens": 80},
  "duration_ms": 450,
  "cached": false,
  "error_kind": ""
}
```

Rules:

- Attach diagnostics to the owner: tool result metadata, hook events, session warning/debug events, or SDK callbacks.
- Do not expose helper internals to the model unless the owning operation intentionally includes them.
- Redact secrets in metadata. Do not log API keys, headers, raw credentials, MCP secrets, or unbounded prompt contents.
- Preserve `llm` error types/kinds so retry/fallback/permanent behavior remains inspectable.

## YAGNI/DRY implementation plan

1. **Inventory current side calls only.** Start with `agent/tool_web_fetch.go`, `agent/session_namer.go`, `agent/session_tools.go` / `describeImage`, prompt hook calls in `agent/internal/hooks/hooks.go`, context-manager side calls in `agent/internal/contextmgr/`, and the standalone `cmd/llmcall` command. Do not search for hypothetical future abstractions beyond current call sites.
2. **Extract the smallest shared request builder.** Add a private helper if one package owns the duplication; add a public helper only when multiple packages or SDK users need it.
3. **Prefer existing `llm.GenerateOptions`.** Do not create a parallel option tree unless it prevents accidental tool/session fields. If a wrapper type is used, map directly to `llm.GenerateOptions`.
4. **Force tool-free behavior in helpers.** Set `MaxToolRounds` to `0`, set `ToolChoice{Mode: "none"}` only where supported, never populate `Tools` in helper wrappers, and reject returned passive tool calls.
5. **Reuse existing result types first.** Return `*llm.GenerateResult`, `*llm.StreamResult`, or a tiny DTO only where it removes repeated extraction code.
6. **Add observation at owner boundaries.** Do not create a helper event bus. Add concise helper metadata where the owning tool/hook/session already emits events or results.
7. **Do not move client loading prematurely.** Keep `cmdutil.LoadClient` as-is unless SDK helpers require a library construction API. If they do, factor shared provider-config loading once.
8. **Keep caches explicit.** Do not add default helper-output caching. A helper may cache only with a key that includes input content hash, prompt version, model/provider, and relevant options.
9. **Delete duplication after extraction.** Once a shared helper exists, migrate current call sites instead of leaving both paths active.
10. **Stop after the helper contract is covered.** Do not add subagent commands, lifecycle controls, or UI surfaces for helpers.

## Acceptance criteria

- Existing direct use of `llm.Client.Complete`, `llm.Client.Stream`, `llm.Generate`, `llm.GenerateObject`, and `llm.StreamGenerate` remains supported.
- Helper APIs are thin wrappers over existing `llm` APIs and do not create a new provider, middleware, retry, stream, or subagent abstraction.
- Helper calls require `context.Context` and respect cancellation/timeouts.
- Helper calls use existing request validation, provider resolution, middleware, the intended retry path for the chosen underlying API, rate-limit metadata handling, adapter timeout defaults, Complete-based API logging middleware when attached, stream middleware for streaming helpers, and error classification.
- Helper calls do not create sessions, child transcripts, subagent jobs, task records, or lineage metadata.
- Tool-free helpers never populate `Tools`, set `ToolChoice` to `none` only where supported, set `MaxToolRounds` to `0` when using `llm.Generate`/stream helpers, and reject any returned passive tool calls.
- Text/object helpers return usage/model/provider metadata when the adapter provides it, falling back to requested model metadata when response model is empty.
- Object helpers use the existing schema response/validation path in `llm.GenerateObject`.
- Streaming helpers return existing stream/result types and do not buffer unless the caller requests accumulation.
- Current examples (`web_fetch`, session naming, prompt hooks) either use the shared helper path or are documented as intentionally local wrappers with equivalent behavior.
- Diagnostics identify the helper owner/name without exposing secrets or registering the helper as an agent.

## Tests

Minimum tests for the helper contract:

- Fake adapter test: `GenerateHelperText` builds the expected `llm.Request` with provider, model, system/user message ordering, max tokens, temperature, metadata, and no tools.
- Tool isolation test: helper wrapper sets `MaxToolRounds` to `0`, passes no `Tools`, requests `ToolChoice` `none` only where supported, omits it for a fake adapter that does not support `none`, rejects returned passive tool calls, and does not execute a tool even if a caller tries to smuggle tool-like metadata.
- Fake adapter test: `GenerateHelperObject` calls `llm.GenerateObject`, accepts valid JSON, rejects invalid JSON, and returns schema validation errors without wrapping away `llm` error classification.
- Label classifier test, if added: rejects empty labels, duplicate labels, and labels not returned from the enum schema.
- Middleware/retry test: helper calls invoke existing `llm.Middleware` and retry behavior exactly as the chosen underlying path would (`llm.Generate` gets generate retry; direct `Client.Complete` does not unless wrapped).
- Streaming test: `StreamHelperText` returns `*llm.StreamResult`, forwards text deltas, and does not force buffering before the caller consumes events.
- Cancellation/timeout test: canceled context and helper timeouts stop the request and return the existing context-wrapped error behavior.
- No session-state writes test: helper calls with a fake client do not create transcript, meta, task, or subagent files.
- No subagent registration test: helper calls do not appear in subagent manager/list output.
- Current-call-site regression tests:
  - `web_fetch` cheap-model helper uses the selected cheap model/provider and reports owner-level errors.
  - session namer remains advisory; helper failure does not fail the session unless existing policy says it should.
  - prompt hook helper preserves hook timeout/error behavior and surfaces prompt LLM errors with hook context; hook output parsing remains owned by the hook runner and follows doc 07's current shared output contract until the future `{ok, reason}` target is implemented.

## Compatibility notes

- This spec is compatible with current direct `llm.Client` and high-level `llm.Generate*` usage.
- The exact exported helper names may differ, but the pattern is normative: standalone LLM call, tool-free helper wrapper, existing client/middleware path plus the retry semantics of the chosen underlying API, no subagent runtime.
- Existing call sites may remain local when extraction would add indirection without reducing duplication. That is acceptable under YAGNI if tests cover the boundary.
