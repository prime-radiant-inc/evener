# LLM Input Token Counting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move local input-token estimation into `llm/` and add exact preflight token counters for Anthropic and Google/Gemini.

**Architecture:** Add focused token-count types and pure local estimation functions in `llm`, plus an optional `InputTokenCounter` adapter interface. `llm.Client` remains the only provider router; individual adapters own exact provider API calls.

**Tech Stack:** Go, existing `llm.Request`/`llm.Client`/provider adapters, `net/http`, `image.DecodeConfig`, package tests with fake HTTP servers.

---

### Task 1: Core `llm` Token Count API

**Files:**
- Create: `llm/token_count.go`
- Create: `llm/token_count_test.go`
- Modify: `llm/client.go`

- [ ] **Step 1: Write failing tests**

Add tests for:
- `EstimateInputTokens` does not scale with image byte length.
- Google local image estimates use 258-token tiling.
- Anthropic local image estimates use 28px patches.
- `Client.CountInputTokens` routes to an adapter implementing `InputTokenCounter`.
- `Client.CountInputTokens` falls back to local estimates when an adapter lacks the interface.

- [ ] **Step 2: Run red tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm -run 'InputTokens|TokenCount' -count=1
```

Expected: compile/test failures because the API does not exist yet.

- [ ] **Step 3: Implement minimal API**

Create `llm/token_count.go` with:
- `InputTokenCount`
- `InputTokenCounter`
- `EstimateInputTokens`
- `EstimateMessagesInputTokens`
- media dimension decoding helpers
- provider/model-aware local image estimates

Modify `llm/client.go` with `CountInputTokens`.

- [ ] **Step 4: Run green tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm -run 'InputTokens|TokenCount' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add llm/token_count.go llm/token_count_test.go llm/client.go
git commit -m "Add llm input token counting API"
```

### Task 2: Exact Anthropic Counter

**Files:**
- Modify: `llm/providers/anthropic/adapter.go`
- Modify: `llm/providers/anthropic/adapter_test.go`

- [ ] **Step 1: Write failing tests**

Add tests that an Anthropic adapter:
- posts to `/v1/messages/count_tokens`
- reuses message/request body conversion
- parses `{"input_tokens": 123}`
- returns provider HTTP errors through existing classification helpers

- [ ] **Step 2: Run red tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm/providers/anthropic -run 'CountInputTokens' -count=1
```

Expected: fail because `CountInputTokens` is missing.

- [ ] **Step 3: Implement counter**

Add `CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error)` to the Anthropic adapter. Use `buildRequestBody`, marshal it, POST to `BaseURL + "/v1/messages/count_tokens"`, parse `input_tokens`, stamp source/provider/model/raw.

- [ ] **Step 4: Run green tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm/providers/anthropic -run 'CountInputTokens' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add llm/providers/anthropic/adapter.go llm/providers/anthropic/adapter_test.go
git commit -m "Add Anthropic exact input token counter"
```

### Task 3: Exact Google Counter

**Files:**
- Modify: `llm/providers/google/adapter.go`
- Modify: `llm/providers/google/adapter_test.go`

- [ ] **Step 1: Write failing tests**

Add tests that a Google adapter:
- posts to `/v1beta/models/<model>:countTokens`
- sends `generateContentRequest`
- parses `{"totalTokens": 456}`
- returns classified HTTP errors

- [ ] **Step 2: Run red tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm/providers/google -run 'CountInputTokens' -count=1
```

Expected: fail because `CountInputTokens` is missing.

- [ ] **Step 3: Implement counter**

Add `CountInputTokens(ctx context.Context, req llm.Request) (llm.InputTokenCount, error)` to the Google adapter. Reuse `toGeminiContents` and `buildRequestBody`, POST `{"generateContentRequest": body}` to `:countTokens`, parse `totalTokens`, stamp source/provider/model/raw.

- [ ] **Step 4: Run green tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm/providers/google -run 'CountInputTokens' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add llm/providers/google/adapter.go llm/providers/google/adapter_test.go
git commit -m "Add Google exact input token counter"
```

### Task 4: Agent Integration

**Files:**
- Modify: `agent/session_model_call.go`
- Modify: `agent/internal/contextmgr/context_manager.go`
- Modify: `agent/internal/contextmgr/context_manager_test.go`
- Modify: `agent/session_dod_test.go`
- Delete: `agent/internal/contextestimate/contextestimate.go`

- [ ] **Step 1: Write or update failing tests**

Update existing image-context tests to assert the estimator comes from `llm` behavior, not an agent-local copy.

- [ ] **Step 2: Run red tests if compile fails**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/contextmgr ./agent -run 'EstimateTokens|ContextWindowAwareness' -count=1
```

Expected: fail until imports and call sites are updated.

- [ ] **Step 3: Replace agent-local estimator**

Use `llm.EstimateMessagesInputTokens` for history/message-only estimates and `llm.EstimateInputTokens` for full session warning requests. Remove the agent-local context estimator package.

- [ ] **Step 4: Run green tests**

```bash
GOCACHE=/tmp/serf-gocache go test ./agent/internal/contextmgr ./agent -run 'EstimateTokens|ContextWindowAwareness' -count=1
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add agent/session_model_call.go agent/internal/contextmgr/context_manager.go agent/internal/contextmgr/context_manager_test.go agent/session_dod_test.go
git add agent/internal/contextestimate/contextestimate.go
git commit -m "Use llm token estimator in agent context pressure"
```

### Task 5: Verification

**Files:**
- All touched Go files.

- [ ] **Step 1: Run focused verification**

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./llm/providers/anthropic ./llm/providers/google ./agent/internal/contextmgr -count=1
GOCACHE=/tmp/serf-gocache go test ./agent -run 'ContextWindowAwareness' -count=1
```

Expected: pass.

- [ ] **Step 2: Check worktree**

```bash
git status --short
```

Expected: clean after commits.
