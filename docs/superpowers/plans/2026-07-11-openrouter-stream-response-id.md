# OpenAI-Compatible Stream Response ID Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve the top-level Chat Completions SSE response ID on Serf's final streaming `llm.Response`.

**Architecture:** Reuse the already-decoded `chatCompletionChunk.ID`, retain the latest non-empty value during `decodeStream`, and assign it to the final response on `[DONE]`. Prove the behavior with the existing deterministic captured OpenRouter SSE fixture.

**Tech Stack:** Go, `httptest`, Server-Sent Events, Serf `llm` response types.

## Global Constraints

- Default tests perform no live provider request.
- Empty chunk IDs never erase an observed ID and never cause Serf to invent one.
- Provider-handle export remains redacted by default.
- No prompt, response body, tool argument, credential, or raw HTTP body is newly persisted.

---

### Task 1: Propagate streamed response IDs

**Files:**
- Modify: `llm/providers/openaicompat/adapter.go`
- Test: `llm/providers/openaicompat/openrouter_stream_capture_test.go`

**Interfaces:**
- Consumes: `chatCompletionChunk.ID` from each decoded SSE event.
- Produces: `llm.Response.ID` on the final `StreamEventFinish` response.

- [ ] **Step 1: Write the failing regression assertion**

In `TestReplay_CapturedOpenRouterStream`, after asserting the final response is
non-nil, require the fixture's exact generation ID:

```go
const wantID = "gen-1783017913-0zugW1oNmQbUgo0VdDsu"
if final == nil {
	t.Fatal("captured real OpenRouter stream produced no final response")
}
if final.ID != wantID {
	t.Fatalf("final response ID = %q, want %q", final.ID, wantID)
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./llm/providers/openaicompat -run TestReplay_CapturedOpenRouterStream -count=1
```

Expected: FAIL with `final response ID = "", want "gen-..."`.

- [ ] **Step 3: Implement the minimal decoder propagation**

In `decodeStream`, declare response-ID state next to `model`:

```go
var responseID string
var model string
```

After decoding each non-`[DONE]` chunk:

```go
if chunk.ID != "" {
	responseID = chunk.ID
}
```

Set the final response field:

```go
finalResp := &llm.Response{
	ID:        responseID,
	Provider:  "openai-compatible",
	Model:     model,
	Message:   msg,
	Finish:    finish,
	RateLimit: rl,
}
```

- [ ] **Step 4: Format and verify GREEN**

Run:

```bash
gofmt -w llm/providers/openaicompat/adapter.go llm/providers/openaicompat/openrouter_stream_capture_test.go
go test ./llm/providers/openaicompat -run TestReplay_CapturedOpenRouterStream -count=1
go test ./llm/providers/openaicompat
```

Expected: PASS with clean output.

- [ ] **Step 5: Run repository verification**

Run:

```bash
go test ./...
go vet ./...
git diff --check
```

Expected: all commands exit zero.

- [ ] **Step 6: Commit and push**

```bash
git add llm/providers/openaicompat/adapter.go llm/providers/openaicompat/openrouter_stream_capture_test.go
git commit -m "fix(llm): retain streamed response IDs"
git push origin main
```

- [ ] **Step 7: Provide the immutable pin**

Run: `git rev-parse HEAD`

Expected: the exact pushed commit used by the eval appliance plan. Do not use a
branch name or floating reference.
