# OpenAI-Compatible Tool-Result Image Handling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let OpenAI-compatible adapters continue after image-bearing tool results by sanitizing only Chat Completions dispatch copies while preserving native Responses image support.

**Architecture:** Keep the provider-agnostic `llm.Request` complete. Add a pure copy-on-write transformation in `llm/providers/openaicompat` that clears tool-result image fields. Apply it only inside the private Chat Completions transport path, so non-adaptive Chat calls and adaptive Chat fallback calls use text-only tool messages while adaptive Responses calls retain the original image-bearing request. Keep the exported strict Chat body builder unchanged.

**Tech Stack:** Go, `net/http/httptest`, existing OpenAI-compatible adapter and Responses/Chat serializers, deterministic package tests.

**Spec:** `docs/superpowers/specs/2026-08-20-openaicompat-tool-result-images-design.md`

## Global Constraints

- Do not mutate the request, message history, or transcript-owned tool-result data.
- Do not put image content into a generic Chat Completions `role: "tool"` message.
- Preserve the existing Responses `function_call_output` plus `input_image` wire format.
- Keep `ChatCompletionsBody`/`buildRequestBody` strict for direct image-bearing serialization.
- Tests must be deterministic and offline; use an `httptest.Server` as the provider boundary.
- Keep changes limited to the OpenAI-compatible adapter and its focused tests.

---

### Task 1: Add failing copy-and-dispatch regression tests

**Files:**
- Create: `llm/providers/openaicompat/tool_result_images_test.go`
- Read/retain: `llm/providers/openaicompat/adapter_test.go` (existing strict body and adaptive route tests)
- Read/retain: `llm/providers/openai/adapter_test.go` (existing first-party Responses image contract)

**Interfaces:**
- Consumes: the existing `llm.Request`, `llm.Message`, and `llm.ToolResultData` types; `Adapter.Complete`, `Adapter.Stream`, and `ChatCompletionsBody`.
- Produces: failing tests that define `requestWithoutToolResultImages(req llm.Request) llm.Request` and the adapter's Chat-only dispatch behavior.

- [ ] **Step 1: Write the failing pure-copy test.**

Create a request containing one `llm.RoleTool` message with text content, tool metadata, `ImageData`, and `ImageMediaType`. Call the planned helper and assert:

```go
func TestRequestWithoutToolResultImagesCopiesOnlyWireImageFields(t *testing.T) {
    image := []byte{0x89, 0x50, 0x4e, 0x47}
    req := llm.Request{Messages: []llm.Message{llm.ToolResultNamed("call_img", "read_file", "screenshot", false)}}
    req.Messages[0].Content[0].ToolResult.ImageData = image
    req.Messages[0].Content[0].ToolResult.ImageMediaType = "image/png"

    sanitized := requestWithoutToolResultImages(req)
    got := sanitized.Messages[0].Content[0].ToolResult
    if got.ImageData != nil || got.ImageMediaType != "" {
        t.Fatalf("sanitized image fields = %v/%q, want nil/empty", got.ImageData, got.ImageMediaType)
    }
    if got.Content != "screenshot" || got.ToolCallID != "call_img" || got.Name != "read_file" {
        t.Fatalf("sanitized metadata = %+v", got)
    }
    original := req.Messages[0].Content[0].ToolResult
    if string(original.ImageData) != string(image) || original.ImageMediaType != "image/png" {
        t.Fatalf("input was mutated: %+v", original)
    }
}
```

Use `bytes.Equal` for the image assertion in the actual test rather than converting bytes to strings.

- [ ] **Step 2: Add a failing non-adaptive Chat completion test.**

Use an `httptest.Server` that records `/chat/completions`, returns a minimal successful Chat Completions JSON response, and fails any unexpected path. Construct `&Adapter{BaseURL: server.URL, Client: server.Client()}` and call `Complete` with the image-bearing tool result. Assert that the call succeeds, the recorded tool message retains `content: "screenshot"`, and its JSON body contains no image data or media-type fields. The pre-fix behavior fails before dispatch with the existing configuration error.

- [ ] **Step 3: Add a failing non-adaptive streaming test.**

Use the existing SSE response shape from `TestStreamWireCaptureRecordsExactAttemptBeforeFinish` in `wire_capture_test.go`. Record the Chat request and consume the returned stream through `StreamEventFinish`. Assert success and the same text-only tool message shape. The pre-fix behavior returns the configuration error before creating the stream.

- [ ] **Step 4: Add the adaptive fallback regression test.**

Use an adaptive adapter and a server with two routes:

- `/responses` returns HTTP 404 with a provider error, forcing the existing fallback path.
- `/chat/completions` records the request and returns a successful Chat Completions response.

Call `Complete` with an image-bearing tool result. Assert that Responses was attempted, Chat fallback succeeded, Chat received the textual tool output without image fields, and the original in-memory request still contains the image bytes. The pre-fix behavior reaches Chat with the original request and fails the strict body guard.

- [ ] **Step 5: Add the adaptive Responses preservation assertion.**

In the adaptive test table, add a successful `/responses` mode that decodes the request and verifies the image remains in the `function_call_output.output` content as an `input_image` data URI. The test may share the existing successful-response fixture in `adapter_test.go`; its red phase is supplied by the fallback subtest, while this assertion guards the endpoint-aware behavior from regression.

- [ ] **Step 6: Run the focused tests and verify the expected red state.**

Run:

```bash
go test ./llm/providers/openaicompat -run 'Test(RequestWithoutToolResultImages|Adapter.*ToolResultImage|Adaptive.*ToolResultImage)' -count=1
```

Expected: compilation/test failure because `requestWithoutToolResultImages` is not defined and the existing Chat paths reject the image-bearing tool result. Do not change production code until this failure is observed.

- [ ] **Step 7: Commit the red tests.**

```bash
git add llm/providers/openaicompat/tool_result_images_test.go
git commit -m "test(openaicompat): cover tool-result image chat fallback"
```

### Task 2: Implement pure Chat-only request sanitization

**Files:**
- Modify: `llm/providers/openaicompat/request.go` (near `requestHasToolResultImages`)
- Test: `llm/providers/openaicompat/tool_result_images_test.go`

**Interfaces:**
- Consumes: `llm.Request` values with arbitrary messages and optional image-bearing tool results.
- Produces: `requestWithoutToolResultImages(req llm.Request) llm.Request`, a request copy suitable for Chat serialization.

- [ ] **Step 1: Add the minimal copy-on-write helper.**

Implement:

```go
func requestWithoutToolResultImages(req llm.Request) llm.Request
```

The function must return `req` unchanged when no tool-result part has non-empty `ImageData` or `ImageMediaType`. When a match exists, shallow-copy the request, clone the message slice, clone each message's content slice, copy each affected `ToolResultData` value, clear only `ImageData` and `ImageMediaType`, and point the cloned content part at the copied result. Preserve every other request, message, content-part, and tool-result field. Never mutate the input slices or pointers.

- [ ] **Step 2: Run the pure-copy test.**

Run:

```bash
go test ./llm/providers/openaicompat -run '^TestRequestWithoutToolResultImagesCopiesOnlyWireImageFields$' -count=1
```

Expected: PASS, including proof that the original image bytes and media type remain present.

- [ ] **Step 3: Run the direct body guard test.**

Run:

```bash
go test ./llm/providers/openaicompat -run '^TestBuildRequestBody_RejectsToolResultImage$' -count=1
```

Expected: PASS. The helper must not weaken `buildRequestBody` or `ChatCompletionsBody` validation.

- [ ] **Step 4: Commit the helper.**

```bash
git add llm/providers/openaicompat/request.go llm/providers/openaicompat/tool_result_images_test.go
git commit -m "fix(openaicompat): copy tool results for chat serialization"
```

### Task 3: Apply sanitization at both Chat dispatch paths

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go` at `completeViaChatCompletions` and `streamViaChatCompletions` call boundaries.
- Test: `llm/providers/openaicompat/tool_result_images_test.go`

**Interfaces:**
- Consumes: the pure `requestWithoutToolResultImages` helper.
- Produces: successful text-only Chat dispatch for non-adaptive adapters and adaptive fallbacks, with full image-bearing requests preserved for Responses attempts.

- [ ] **Step 1: Sanitize only inside the private Chat transport functions.**

At the beginning of `completeViaChatCompletions` and `streamViaChatCompletions`, replace the local request with:

```go
req = requestWithoutToolResultImages(req)
```

Do not add sanitization to `Complete` before `completeViaResponses`, and do not add it to `Stream` before `streamViaResponses`. Because the private Chat functions are used by both non-adaptive dispatch and adaptive fallback, this covers both cases without inspecting provider behavior tags.

- [ ] **Step 2: Run the non-adaptive tests.**

Run:

```bash
go test ./llm/providers/openaicompat -run 'TestAdapter(Complete|Stream).*ToolResultImage' -count=1
```

Expected: PASS. Both methods dispatch a text-only Chat tool message and preserve the request fixture's original image fields.

- [ ] **Step 3: Run the adaptive tests.**

Run:

```bash
go test ./llm/providers/openaicompat -run '^TestAdaptive.*ToolResultImage|^TestNewFromEnv_(Complete|Stream)Auto' -count=1
```

Expected: PASS. Successful Responses requests still contain `input_image`; fallback Chat requests omit image fields and complete successfully.

- [ ] **Step 4: Run the strict-body and existing provider tests.**

Run:

```bash
go test ./llm/providers/openaicompat -count=1
```

Expected: PASS with the existing direct `buildRequestBody` rejection tests unchanged. If an existing adapter test expects `Complete` or `Stream` to reject before dispatch, update that test to assert the new approved Chat dispatch contract while retaining the strict body-builder rejection assertion.

- [ ] **Step 5: Commit the endpoint integration.**

```bash
git add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/tool_result_images_test.go
git commit -m "fix(openaicompat): handle tool-result images on chat path"
```

### Task 4: Run package verification and inspect the final change

**Files:**
- Verify: all changed files under `llm/providers/openaicompat`
- Verify: `docs/superpowers/specs/2026-08-20-openaicompat-tool-result-images-design.md`

- [ ] **Step 1: Run focused provider tests.**

```bash
go test ./llm/providers/openaicompat ./llm/providers/openai -count=1
```

Expected: PASS. This proves the compatibility adapter's Chat fallback and the first-party Responses image contract together.

- [ ] **Step 2: Run the root LLM package tests.**

```bash
go test ./llm/... -count=1
```

Expected: PASS with no provider credentials or network access required.

- [ ] **Step 3: Run formatting and static checks for changed Go files.**

```bash
gofmt -w llm/providers/openaicompat/adapter.go llm/providers/openaicompat/request.go llm/providers/openaicompat/tool_result_images_test.go
git diff --check
go vet ./llm/providers/openaicompat
```

Expected: no formatting diff remains, `git diff --check` is clean, and `go vet` exits zero.

- [ ] **Step 4: Review the final diff and repository state.**

```bash
git diff main...HEAD --stat
git diff main...HEAD -- docs/superpowers/specs/2026-08-20-openaicompat-tool-result-images-design.md llm/providers/openaicompat/adapter.go llm/providers/openaicompat/request.go llm/providers/openaicompat/tool_result_images_test.go
git status --short --branch
```

Expected: only the revised spec, adapter/request changes, and focused tests are present; no unrelated files are modified and the worktree is clean.

- [ ] **Step 5: Record the verification result.**

Report the exact test commands and exit results, the endpoint behavior verified, the worktree path, and the final commit hashes. Do not claim the full repository gate passed unless it actually ran and exited zero.
