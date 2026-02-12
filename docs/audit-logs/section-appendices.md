# Audit: Unified LLM Spec Appendices A, B, C

**Spec reference:** `unified-llm-spec.md` lines 1780-1965
**Codebase:** `internal/llm/` package
**Date:** 2026-02-11

---

## Appendix A: Conversation Examples

### A.1 Simple Text Conversation

**Spec pattern:**
```
messages = [
    Message(role = SYSTEM, content = [ContentPart(kind = TEXT, text = "...")]),
    Message(role = USER,   content = [ContentPart(kind = TEXT, text = "...")])
]
```

**Codebase mapping:**
```go
msgs := []llm.Message{
    llm.System("You are a helpful assistant."),
    llm.User("What is 2 + 2?"),
}
```

**Verdict: PASS.** The `System()` and `User()` helper functions in `types.go:51-59` produce exactly the structure the spec describes. The `Message` struct has `Role` (type `Role` with `RoleSystem`, `RoleUser` constants) and `Content` (type `[]ContentPart`). `ContentPart` has `Kind` (type `ContentKind` with `ContentText` constant) and `Text` field. All fields match the spec.

---

### A.2 Multimodal Conversation

**Spec pattern:**
```
messages = [
    Message(role = USER, content = [
        ContentPart(kind = TEXT, text = "What do you see in this image?"),
        ContentPart(kind = IMAGE, image = ImageData(url = "https://example.com/photo.jpg"))
    ])
]
```

**Codebase mapping:**
```go
msgs := []llm.Message{{
    Role: llm.RoleUser,
    Content: []llm.ContentPart{
        {Kind: llm.ContentText, Text: "What do you see in this image?"},
        {Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/photo.jpg"}},
    },
}}
```

**Verdict: PASS.** `ContentImage` kind exists (`types.go:24`), `ImageData` struct has `URL`, `Data`, `MediaType`, and `Detail` fields (`types.go:102-107`). The spec example uses `url` and the struct has `URL`. There is no convenience helper like `UserWithImage(...)`, but the struct-literal approach works. Audio and Document content kinds are also present (`ContentAudio`, `ContentDocument` with `AudioData` and `DocumentData` structs), going beyond the spec example.

---

### A.3 Tool Use Conversation

**Spec pattern:**
```
messages = [
    Message(role = USER, content = [...]),
    Message(role = ASSISTANT, content = [
        ContentPart(kind = TOOL_CALL, tool_call = ToolCallData(id, name, arguments))
    ]),
    Message(role = TOOL, content = [
        ContentPart(kind = TOOL_RESULT, tool_result = ToolResultData(tool_call_id, content, is_error))
    ], tool_call_id = "call_123"),
    Message(role = ASSISTANT, content = [
        ContentPart(kind = TEXT, text = "...")
    ])
]
```

**Codebase mapping:**
```go
msgs := []llm.Message{
    llm.User("What is the weather in San Francisco?"),
    {Role: llm.RoleAssistant, Content: []llm.ContentPart{{
        Kind: llm.ContentToolCall,
        ToolCall: &llm.ToolCallData{
            ID: "call_123", Name: "get_weather",
            Arguments: json.RawMessage(`{"city":"San Francisco"}`),
        },
    }}},
    llm.ToolResult("call_123", "72F, sunny", false),
    llm.Assistant("The weather in San Francisco is 72F and sunny."),
}
```

**Verdict: PASS.** All structural elements match:
- `ContentToolCall` kind with `ToolCallData` containing `ID`, `Name`, `Arguments` (`types.go:122-131`)
- `ContentToolResult` kind with `ToolResultData` containing `ToolCallID`, `Content`, `IsError` (`types.go:148-156`)
- `ToolResult()` helper sets `Role: RoleTool` and `ToolCallID` on the message (`types.go:64-74`)
- The message-level `ToolCallID` field matches the spec's `tool_call_id` parameter (`types.go:38`)

---

### A.4 Thinking Blocks

**Spec pattern:**
```
messages = [
    Message(role = USER, content = [...]),
    Message(role = ASSISTANT, content = [
        ContentPart(kind = THINKING, thinking = ThinkingData(text = "...", signature = "...")),
        ContentPart(kind = TEXT, text = "The answer is 42.")
    ])
]
```

**Codebase mapping:**
```go
msgs := []llm.Message{
    llm.User("Solve this complex math problem..."),
    {Role: llm.RoleAssistant, Content: []llm.ContentPart{
        {Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
            Text: "Let me work through this step by step...",
            Signature: "sig_abc123",
        }},
        {Kind: llm.ContentText, Text: "The answer is 42."},
    }},
}
```

**Verdict: PASS.** `ContentThinking` kind exists (`types.go:29`), `ThinkingData` has `Text`, `Signature`, and `Redacted` fields (`types.go:158-162`). The `ContentRedThinking` kind (`types.go:30`) also supports redacted thinking blocks from Anthropic. The `Response.ReasoningText()` helper (`types.go:360-372`) extracts thinking text. The spec note about including thinking blocks in history for integrity verification is supported by the conversation structure.

---

## Appendix B: High-Level API Usage Examples

### B.1 Simple Generation

**Spec pattern:**
```
result = generate(model = "claude-opus-4-6", prompt = "Explain quantum computing")
PRINT(result.text)
PRINT(result.usage.total_tokens)
```

**Codebase mapping:**
```go
prompt := "Explain quantum computing"
result, err := llm.Generate(ctx, llm.GenerateOptions{
    Model:  "claude-opus-4-6",
    Prompt: &prompt,
})
fmt.Println(result.Text)
fmt.Println(result.Usage.TotalTokens)
```

**Verdict: PASS.** `Generate()` in `generate.go:108` accepts `GenerateOptions` with `Model` and `Prompt` fields. `GenerateResult` has `Text` (string) and `Usage` (type `Usage` with `TotalTokens` field). The `Prompt` field is `*string` rather than a bare string, which is slightly more verbose but necessary to distinguish "no prompt" from "empty prompt". The `Client` field on `GenerateOptions` is optional; when nil, `DefaultClient()` is used (`generate.go:110-116`), matching the spec's module-level default client pattern.

---

### B.2 Generation with Tools

**Spec pattern:**
```
result = generate(
    model = "claude-opus-4-6",
    system = "You are a helpful assistant with access to weather data.",
    prompt = "What is the weather in San Francisco?",
    tools = [weather_tool],
    max_tool_rounds = 5
)
PRINT(result.text)
PRINT(LENGTH(result.steps))
PRINT(result.total_usage.total_tokens)
```

**Codebase mapping:**
```go
sys := "You are a helpful assistant with access to weather data."
prompt := "What is the weather in San Francisco?"
maxRounds := 5
result, err := llm.Generate(ctx, llm.GenerateOptions{
    Model:         "claude-opus-4-6",
    System:        &sys,
    Prompt:        &prompt,
    Tools:         []llm.Tool{weatherTool},
    MaxToolRounds: &maxRounds,
})
fmt.Println(result.Text)
fmt.Println(len(result.Steps))
fmt.Println(result.TotalUsage.TotalTokens)
```

**Verdict: PASS.** All fields present:
- `System` (`*string`) in `GenerateOptions` (`generate.go:43`)
- `Tools` (`[]Tool`) with `Tool` having `Definition` + `Execute` func (`generate.go:15-18`)
- `MaxToolRounds` (`*int`) in `GenerateOptions` (`generate.go:51`), defaults to 1 per spec (`generate.go:125-126`)
- `GenerateResult.Steps` (`[]StepResult`) tracks each step (`generate.go:103`)
- `GenerateResult.TotalUsage` (`Usage`) aggregates tokens across all steps (`generate.go:102`)
- Tool execution is parallel via goroutines (`generate.go:319-371`)

---

### B.3 Streaming

**Spec pattern:**
```
result = stream(model = "claude-opus-4-6", prompt = "Write a poem")
FOR EACH event IN result:
    IF event.type == TEXT_DELTA:
        PRINT(event.delta)
response = result.response()
PRINT(response.usage)
```

**Codebase mapping:**
```go
prompt := "Write a poem"
result, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
    Model:  "claude-opus-4-6",
    Prompt: &prompt,
})
for ev := range result.Events() {
    if ev.Type == llm.StreamEventTextDelta {
        fmt.Print(ev.Delta)
    }
}
resp, err := result.Response()
fmt.Println(resp.Usage)
```

**Verdict: PASS.** `StreamGenerate()` in `stream_generate.go:72` returns `*StreamResult`. `StreamResult.Events()` returns `<-chan StreamEvent` (`stream_generate.go:24`). `StreamEventTextDelta` type exists (`stream.go:14`). `StreamEvent.Delta` field holds the text delta (`stream.go:34`). `StreamResult.Response()` blocks until stream completes and returns the final `*Response` with `Usage` (`stream_generate.go:42-54`).

The spec shows `result.response()` (lowercase); the codebase uses `result.Response()` (Go convention). This is a language-appropriate adaptation, not a gap.

**Minor note:** The spec's `stream()` function is named `StreamGenerate()` in the codebase. The low-level `Client.Stream()` also exists but operates at the adapter level (single turn, no tool loop). This is a reasonable naming choice -- `StreamGenerate` clarifies it's the high-level streaming equivalent of `Generate`.

---

### B.4 Structured Output

**Spec pattern:**
```
result = generate_object(
    model = "gpt-5.2",
    prompt = "Extract the person's name and age from: '...'",
    schema = { "type": "object", ... }
)
PRINT(result.output)
```

**Codebase mapping:**
```go
prompt := "Extract the person's name and age from: 'Alice is 30 years old'"
result, err := llm.GenerateObject(ctx, llm.GenerateObjectOptions{
    GenerateOptions: llm.GenerateOptions{
        Model:  "gpt-5.2",
        Prompt: &prompt,
    },
    Schema: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "name": map[string]any{"type": "string"},
            "age":  map[string]any{"type": "integer"},
        },
        "required": []any{"name", "age"},
    },
})
fmt.Println(result.Output)
```

**Verdict: PASS.** `GenerateObject()` in `generate_object.go:20` accepts `GenerateObjectOptions` which embeds `GenerateOptions` and adds `Schema` (`map[string]any`) and `Strict` (`bool`). The output is validated against the schema using `jsonschema/v5` (`generate_object.go:49-55`). `GenerateResult.Output` (`any`) holds the parsed object (`generate.go:106`). A `NoObjectGeneratedError` is returned if parsing or validation fails.

Streaming counterpart `StreamGenerateObject()` also exists (`generate_object.go:122`), with `StreamObjectResult` providing `Output()`, `Events()`, `Close()`, and `Response()` methods.

---

### B.5 Provider Fallback Pattern

**Spec pattern:**
```
TRY:
    result = generate(model = "claude-opus-4-6", prompt = "...")
CATCH ProviderError:
    result = generate(model = "gpt-5.2", provider = "openai", prompt = "...")
```

**Codebase mapping:**
```go
prompt := "..."
result, err := llm.Generate(ctx, llm.GenerateOptions{
    Model:  "claude-opus-4-6",
    Prompt: &prompt,
})
if err != nil {
    var provErr llm.Error
    if errors.As(err, &provErr) {
        result, err = llm.Generate(ctx, llm.GenerateOptions{
            Model:    "gpt-5.2",
            Provider: "openai",
            Prompt:   &prompt,
        })
    }
}
```

**Verdict: PASS.** The `Provider` field on `GenerateOptions` (`generate.go:39`) allows explicit provider selection. The unified error interface `Error` in `errors.go:13-21` allows checking for provider-specific errors. The `Provider` field on `Request` (`types.go:188`) routes to the correct adapter. When omitted, `defaultProvider` is used (`client.go:54-55`).

The error hierarchy supports fine-grained fallback decisions: `AuthenticationError`, `RateLimitError`, `ServerError`, etc. all implement the `Error` interface with `Retryable()`, `Provider()`, and `StatusCode()` methods.

---

### B.6 Middleware for Logging

**Spec pattern:**
```
FUNCTION logging_middleware(request, next):
    start_time = NOW()
    LOG_INFO("LLM request: provider=" + request.provider + " model=" + request.model)
    response = next(request)
    elapsed = NOW() - start_time
    LOG_INFO("LLM response: tokens=" + response.usage.total_tokens + " latency=" + elapsed)
    RETURN response

client = Client(
    providers = { "anthropic": AnthropicAdapter(...) },
    middleware = [logging_middleware]
)
```

**Codebase mapping:**
```go
loggingMW := llm.MiddlewareFunc{
    Complete: func(ctx context.Context, req llm.Request, next llm.CompleteFunc) (llm.Response, error) {
        start := time.Now()
        log.Printf("LLM request: provider=%s model=%s", req.Provider, req.Model)
        resp, err := next(ctx, req)
        elapsed := time.Since(start)
        if err == nil {
            log.Printf("LLM response: tokens=%d latency=%s", resp.Usage.TotalTokens, elapsed)
        }
        return resp, err
    },
    Stream: func(ctx context.Context, req llm.Request, next llm.StreamFunc) (llm.Stream, error) {
        // Similar logging for streaming...
        return next(ctx, req)
    },
}

client := llm.NewClient()
client.Register(anthropicAdapter)
client.Use(loggingMW)
```

**Verdict: PASS.** The `Middleware` interface (`middleware.go:10-13`) with `WrapComplete` and `WrapStream` methods supports wrapping. The convenience `MiddlewareFunc` struct (`middleware.go:15-18`) allows function-based middleware without defining a full type. `Client.Use()` appends middleware (`client.go:101-106`). Middleware is applied in registration order for requests, reverse order for responses (`client.go:99`, `middleware.go:38-47`).

**Minor observation:** The spec shows constructing a `Client` with middleware at creation time. The codebase uses `NewClient()` followed by `Register()` and `Use()` calls. This is a builder pattern vs. constructor pattern difference, not a functional gap -- the same capabilities are available.

---

## Appendix C: Design Decision Rationale

Each design decision and whether the implementation reflects it:

### C.1: "Single Request type instead of per-method parameter lists"

**Implementation:** `Request` struct (`types.go:186-207`) is a single unified type used by `Client.Complete()` and `Client.Stream()`. The high-level `GenerateOptions` (`generate.go:35-82`) is a separate struct for the `Generate()` convenience function, which constructs a `Request` internally.

**Verdict: PASS.** Both patterns exist: `Request` for the core adapter API, `GenerateOptions` as the ergonomic shorthand the spec calls for. The middleware sees `Request` uniformly.

---

### C.2: "Ship a model catalog if model strings work as-is"

**Implementation:** `ModelCatalog` in `model_catalog.go:28-31` with `GetModelInfo()`, `ListModels()`, `GetLatestModel()` methods. Loaded from an embedded LiteLLM JSON catalog (`model_catalog_embedded.go`). Unknown model strings pass through to providers -- the catalog is advisory only.

**Verdict: PASS.** The catalog provides `ModelInfo` with `ID`, `Provider`, `ContextWindow`, `MaxOutputTokens`, `SupportsTools`, `SupportsVision`, `SupportsReasoning`, and cost fields. The `GetLatestModel()` method can filter by capability. Unknown model strings are not rejected.

---

### C.3: "Explicit provider on Request instead of model-based routing"

**Implementation:** `Request.Provider` field (`types.go:188`). If empty, `Client.defaultProvider` is used (`client.go:54-55`). The `normalizeProviderName()` function (`client.go:175-183`) handles aliases like "gemini" -> "google".

**Verdict: PASS.** Explicit routing avoids ambiguity. Default provider removes boilerplate.

---

### C.4: "Separate generate() and stream()"

**Implementation:** `Generate()` returns `*GenerateResult` (`generate.go:108`). `StreamGenerate()` returns `*StreamResult` (`stream_generate.go:72`). At the adapter level: `Complete()` returns `(Response, error)`, `Stream()` returns `(Stream, error)` (`client.go:8-12`).

**Verdict: PASS.** Different return types, no boolean flag.

---

### C.5: "start/delta/end events instead of flat deltas"

**Implementation:** Full event lifecycle:
- `TEXT_START` / `TEXT_DELTA` / `TEXT_END` (`stream.go:13-15`)
- `REASONING_START` / `REASONING_DELTA` / `REASONING_END` (`stream.go:16-18`)
- `TOOL_CALL_START` / `TOOL_CALL_DELTA` / `TOOL_CALL_END` (`stream.go:19-21`)
- `STREAM_START` / `STEP_FINISH` / `FINISH` (`stream.go:12,22,25`)

**Verdict: PASS.** The start/delta/end pattern is implemented for text, reasoning, and tool calls.

---

### C.6: "max_tool_rounds instead of unlimited looping"

**Implementation:** `MaxToolRounds` in `GenerateOptions` (`generate.go:51`). Defaults to 1 (`generate.go:125-126`). Value of 0 disables automatic tool execution ("passive tool mode"). The loop checks `toolRoundsUsed >= maxToolRounds` before continuing (`generate.go:213`).

**Verdict: PASS.** Bounded by default, explicit opt-in for higher values.

---

### C.7: "JSON Schema for tool parameters instead of language-native types"

**Implementation:** `ToolDefinition.Parameters` is `map[string]any` representing a JSON Schema (`types.go:172`). Validated to have `type=object` at root (`tool_validation.go:39-53`). Arguments are validated against compiled `jsonschema/v5` schemas at runtime (`generate.go:374-387`).

**Verdict: PASS.** JSON Schema is the canonical format. The `jsonschema/v5` library provides standards-compliant validation.

---

### C.8: "Send error results to the model instead of raising exceptions"

**Implementation:** In `executeToolCalls()` (`generate.go:317-371`):
- Unknown tool: sends `ToolResultData{IsError: true, Content: "unknown tool: ..."}` (`generate.go:333-335`)
- Invalid arguments: sends `ToolResultData{IsError: true, Content: "invalid tool arguments: ..."}` (`generate.go:348-350`)
- Execute error: sends `ToolResultData{IsError: true, Content: err.Error()}` (`generate.go:361-363`)

The error result is appended to the conversation and the loop continues, giving the model a chance to recover.

**Verdict: PASS.** Tool errors are sent to the model, not raised as exceptions.

---

### C.9: "Default to retrying unknown errors"

**Implementation:** In `retryableError()` (`retry_util.go:27-40`): if `errors.As(err, &e)` succeeds (error implements `Error` interface), uses `e.Retryable()`. Otherwise, returns `true` (unknown errors are retryable). In `ErrorFromHTTPStatus()` (`errors.go:133-136`): unknown HTTP status codes get `retryable: true`.

**Verdict: PASS.** Unknown errors default to retryable.

---

### C.10: "Not retry timed-out requests by default"

**Implementation:** `RequestTimeoutError` created by `NewRequestTimeoutError()` (`errors.go:162-170`) has `retryable: false`. HTTP 408 timeout gets `retryable: true` (server-initiated timeout is different from client-side timeout). Context deadline exceeded is wrapped to `RequestTimeoutError` with `retryable: false` (`context_errors.go:12-15`).

**Verdict: PASS.** Client-side timeouts (context deadline exceeded) are not retried by default. HTTP 408 (server-initiated timeout) is retried, which is the correct semantic distinction.

---

### C.11: "Use each provider's native API instead of Chat Completions everywhere"

**Implementation:** Three separate adapter packages:
- `providers/openai/` -- uses Responses API (`/v1/responses`)
- `providers/anthropic/` -- uses Messages API
- `providers/google/` -- uses Gemini native API
- `providers/openaicompat/` -- compatibility adapter for OpenAI-compatible endpoints

Each adapter translates between the unified `Request`/`Response` types and its provider's native wire format.

**Verdict: PASS.** Native APIs are used for each provider. The `openaicompat` adapter exists as a convenience for third-party providers but is not the primary path.

---

### C.12: "Handle parallel tool execution in the SDK"

**Implementation:** In `executeToolCalls()` (`generate.go:317-371`): a `sync.WaitGroup` is used to execute all tool calls concurrently. Results are collected in a pre-allocated slice indexed by position. The function blocks until all calls complete, then returns all results to be appended to the conversation in one batch.

**Verdict: PASS.** Parallel tool execution with proper synchronization.

---

## Summary

### Gaps Found: NONE

All 4 conversation patterns (A.1-A.4) and all 6 high-level API patterns (B.1-B.6) can be expressed with the current Go API. All 12 design decisions from Appendix C are reflected in the implementation.

### Observations (not gaps)

1. **Pointer-based optional fields:** `Prompt`, `System`, `MaxToolRounds`, and `MaxTokens` are `*string` / `*int` in `GenerateOptions`. This is Go-idiomatic (distinguishing "not set" from "zero value") but more verbose than the spec's pseudocode. Not a gap -- it's a language adaptation.

2. **Function naming:** `StreamGenerate()` vs spec's `stream()`, `GenerateObject()` vs spec's `generate_object()`. These follow Go naming conventions. Not a gap.

3. **Builder vs constructor for Client:** The spec shows constructing a Client with providers and middleware inline. The codebase uses `NewClient()` + `Register()` + `Use()`. Functionally equivalent, just a different pattern.

4. **RepairToolCall callback:** The codebase adds a `RepairToolCall` callback in `GenerateOptions` (`generate.go:65`) that is not mentioned in the spec appendices. This is an extension, not a gap -- it provides additional functionality for handling malformed tool arguments.

5. **StopWhen predicate:** The `StopWhen` callback in `GenerateOptions` (`generate.go:72-73`) is an extension allowing custom termination of the tool loop, referenced as "spec 4.3" in the code comment.

6. **TextStream convenience:** `StreamResult.TextStream()` (`stream_generate.go:29-40`) provides a text-only channel as a convenience over filtering `Events()`. This is a useful addition beyond the spec.
