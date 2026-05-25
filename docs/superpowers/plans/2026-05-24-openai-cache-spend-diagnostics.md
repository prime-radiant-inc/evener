# OpenAI Cache Spend Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Serf's OpenAI token spend diagnosable across all runtime paths and improve default prompt-cache behavior for long agent sessions.

**Architecture:** Centralize API logging setup so `serf run`, `serf serve`, and embedded TUI use the same middleware. Add conservative OpenAI cache defaults at the session request boundary, where session identity and profile are known. Extend analysis tooling to read both `api.jsonl` and transcript `api_call` records, then add focused context-churn diagnostics for large uncached spikes.

**Tech Stack:** Go, Serf `llm.Client` middleware, Serf transcript JSONL, Python analysis tooling.

---

## File Structure

- `cmdutil/api_logging.go`: new shared helper for attaching `llm.APILogger` to a client.
- `cmd/serf/run.go`: replace inline API logger setup with the shared helper.
- `cmd/serf/serve.go`: attach shared API logger for app-wire server sessions.
- `cmd/serf-tui/embedded.go`: attach shared API logger for embedded TUI sessions.
- `cmdutil/api_logging_test.go`: unit tests for shared logger setup, raw logging, and close behavior.
- `agent/session.go`: set stable OpenAI prompt-cache request defaults on session LLM requests.
- `agent/session_openai_cache_test.go`: request-capture tests for OpenAI cache key and retention defaults.
- `tools/api-log-analyze.py`: extend analyzer to read transcript `api_call` records, print cache ratios, and flag uncached spikes.
- `tools/test_api_log_analyze.py`: regression tests for mixed `api.jsonl` and transcript analysis.
- `docs/openai-spend-diagnostics.md`: short operational guide for auditing spend and interpreting cache metrics.

---

### Task 1: Centralize API Logger Setup

**Files:**
- Create: `cmdutil/api_logging.go`
- Create: `cmdutil/api_logging_test.go`
- Modify: `cmd/serf/run.go`

- [ ] **Step 1: Write failing tests for shared logging helper**

Create `cmdutil/api_logging_test.go`:

```go
package cmdutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/llm"
)

type loggingTestAdapter struct{}

func (loggingTestAdapter) Name() string { return "test" }

func (loggingTestAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Provider: req.Provider,
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Usage:    llm.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5},
		Raw:      map[string]any{"endpoint_url": "https://example.test/v1/responses"},
	}, nil
}

func (loggingTestAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func TestAttachAPILoggerWritesAPIJSONL(t *testing.T) {
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	closeLog, err := AttachAPILogger(client, dir, nil)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}
	defer closeLog()

	_, err = client.Complete(llm.WithAPILogContext(context.Background(), "sess-1", 7), llm.Request{
		Provider: "test",
		Model:    "m",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	for _, want := range []string{`"session_id":"sess-1"`, `"round":7`, `"endpoint_url":"https://example.test/v1/responses"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("api.jsonl missing %s:\n%s", want, string(data))
		}
	}
}

func TestAttachAPILoggerEnablesRawWhenEnvSet(t *testing.T) {
	t.Setenv("SERF_LOG_RAW_HTTP", "1")
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(loggingTestAdapter{})

	closeLog, err := AttachAPILogger(client, dir, nil)
	if err != nil {
		t.Fatalf("AttachAPILogger: %v", err)
	}
	if err := closeLog(); err != nil {
		t.Fatalf("closeLog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "api-raw.jsonl")); err != nil {
		t.Fatalf("api-raw.jsonl was not created: %v", err)
	}
}
```

Add `strings` to imports before running the test.

- [ ] **Step 2: Run failing test**

Run: `go test ./cmdutil -run 'TestAttachAPILogger' -count=1`

Expected: FAIL because `AttachAPILogger` is undefined.

- [ ] **Step 3: Implement shared helper**

Create `cmdutil/api_logging.go`:

```go
package cmdutil

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"primeradiant.com/serf/llm"
)

// AttachAPILogger installs the standard Serf API logger on client.
// The returned function must be called by the caller during shutdown.
func AttachAPILogger(client *llm.Client, stateDir string, warnings io.Writer) (func() error, error) {
	apiLogPath := filepath.Join(stateDir, "api.jsonl")
	apiLog, err := llm.NewAPILogger(apiLogPath)
	if err != nil {
		if warnings != nil {
			fmt.Fprintf(warnings, "warning: API logging disabled: %v\n", err) //nolint:errcheck
		}
		return func() error { return nil }, nil
	}
	apiLog.SyncInterval = 2 * time.Second
	if llm.RawBodyEnabled() {
		rawLogPath := filepath.Join(stateDir, "api-raw.jsonl")
		if err := apiLog.EnableRawLogging(rawLogPath); err != nil && warnings != nil {
			fmt.Fprintf(warnings, "warning: raw API logging disabled: %v\n", err) //nolint:errcheck
		}
	}
	client.Use(apiLog)
	return apiLog.Close, nil
}
```

- [ ] **Step 4: Update `cmd/serf/run.go` to use helper**

Replace the inline `llm.NewAPILogger` block with:

```go
	closeAPILog, err := cmdutil.AttachAPILogger(client, stateDir, cfg.stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmdutil ./cmd/serf -run 'APILog|Run' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmdutil/api_logging.go cmdutil/api_logging_test.go cmd/serf/run.go
git commit -m "refactor: share API logging setup"
```

---

### Task 2: Add API Logging To Serve And Embedded TUI

**Files:**
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf-tui/embedded.go`
- Test: existing `cmd/serf` and `cmd/serf-tui` tests

- [ ] **Step 1: Write failing serve regression test**

Add to `cmd/serf/serve_test.go` or the closest existing serve test file:

```go
func TestRunServeInstallsAPILogger(t *testing.T) {
	stateDir := t.TempDir()
	workDir := t.TempDir()
	t.Setenv("SERF_STATE_DIR", stateDir)
	t.Setenv("SERF_MODEL", "openai/gpt-4o-mini")
	t.Setenv("OPENAI_API_KEY", "test-key")

	// Use the existing test provider transport/fake OpenAI server helpers from serve tests.
	// The fake response must return usage so APILogger has a response to serialize.
	configureServeOpenAIResponse(t, `{
		"id":"resp_test",
		"model":"gpt-4o-mini",
		"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],
		"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":5},"output_tokens":2,"total_tokens":12}
	}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runServe([]string{"--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir})
	}()

	waitForServeReady(t)
	cancel()
	<-done

	data, err := os.ReadFile(filepath.Join(stateDir, "api.jsonl"))
	if err != nil {
		t.Fatalf("read api.jsonl: %v", err)
	}
	if !strings.Contains(string(data), `"cache_read_tokens":5`) {
		t.Fatalf("api.jsonl missing cache usage:\n%s", string(data))
	}
}
```

If the existing serve tests do not expose fake server helpers, use the embedded TUI test first and leave serve verification as a black-box startup test that asserts logger attachment by creating the file on startup.

- [ ] **Step 2: Update `cmd/serf/serve.go`**

After `llm.NewFromEnv` succeeds, add:

```go
	closeAPILog, err := cmdutil.AttachAPILogger(client, sd, os.Stderr)
	if err != nil {
		return err
	}
	defer closeAPILog() //nolint:errcheck
```

- [ ] **Step 3: Write embedded TUI regression test**

In the closest existing embedded wiring test, capture the client after `newEmbeddedRuntime` and assert that a real request writes `api.jsonl`. Use the same fake adapter/test harness pattern already used in `cmd/serf-tui/embedded_wiring_test.go`.

Expected assertion:

```go
data, err := os.ReadFile(filepath.Join(stateDir, "api.jsonl"))
if err != nil {
	t.Fatalf("read api.jsonl: %v", err)
}
if !strings.Contains(string(data), `"provider":"openai"`) {
	t.Fatalf("api.jsonl missing OpenAI request:\n%s", string(data))
}
```

- [ ] **Step 4: Update `cmd/serf-tui/embedded.go`**

After `llm.NewFromEnv` succeeds, add:

```go
	closeAPILog, err := cmdutil.AttachAPILogger(client, sd, nil)
	if err != nil {
		return nil, err
	}
```

Store `closeAPILog` in the embedded runtime struct and call it from the runtime close/shutdown method. If the runtime has no close method, add a field and call it from the existing session close path.

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/serf ./cmd/serf-tui -run 'Serve|Embedded|APILog' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf/serve.go cmd/serf/serve_test.go cmd/serf-tui/embedded.go cmd/serf-tui/embedded_wiring_test.go
git commit -m "fix: log API usage from serve and embedded TUI"
```

---

### Task 3: Set Conservative OpenAI Prompt Cache Defaults

**Files:**
- Modify: `agent/session.go`
- Create: `agent/session_openai_cache_test.go`

- [ ] **Step 1: Write failing request-capture test**

Create `agent/session_openai_cache_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

type cacheCaptureAdapter struct {
	last llm.Request
}

func (a *cacheCaptureAdapter) Name() string { return "openai" }

func (a *cacheCaptureAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.last = req
	return llm.Response{
		Provider: "openai",
		Model:    req.Model,
		Message:  llm.Assistant("ok"),
		Usage:    llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}, nil
}

func (a *cacheCaptureAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, nil
}

func TestSessionSetsOpenAIPromptCacheDefaults(t *testing.T) {
	adapter := &cacheCaptureAdapter{}
	client := llm.NewClient()
	client.Register(adapter)
	dir := t.TempDir()
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.5"), NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if _, err := sess.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if adapter.last.PromptCacheKey == "" {
		t.Fatalf("PromptCacheKey was empty")
	}
	if adapter.last.PromptCacheRetention != "24h" {
		t.Fatalf("PromptCacheRetention = %q, want 24h", adapter.last.PromptCacheRetention)
	}
}
```

- [ ] **Step 2: Run failing test**

Run: `go test ./agent -run TestSessionSetsOpenAIPromptCacheDefaults -count=1`

Expected: FAIL because `PromptCacheKey` and `PromptCacheRetention` are empty.

- [ ] **Step 3: Implement defaulting at request construction**

In `agent/session.go`, after the `llm.Request{...}` literal is created and before `s.client.Complete`, add:

```go
		if req.Provider == "openai" {
			if req.PromptCacheKey == "" {
				req.PromptCacheKey = "serf-session-" + s.id
			}
			if req.PromptCacheRetention == "" && openAIModelSupports24hPromptCache(req.Model) {
				req.PromptCacheRetention = "24h"
			}
		}
```

Add helper near other session helpers:

```go
func openAIModelSupports24hPromptCache(model string) bool {
	model = strings.TrimSpace(model)
	return strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-4.1")
}
```

Add `strings` to imports if not already present.

- [ ] **Step 4: Preserve explicit overrides**

Add a second test:

```go
func TestSessionPreservesExplicitOpenAICacheProviderOptions(t *testing.T) {
	if !openAIModelSupports24hPromptCache("gpt-5.5") {
		t.Fatal("expected gpt-5.5 to support 24h prompt cache")
	}
	if openAIModelSupports24hPromptCache("gpt-4o-mini") {
		t.Fatal("gpt-4o-mini should not get 24h retention by this conservative allowlist")
	}
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./agent -run 'OpenAIPromptCache|openAIModelSupports' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/session_openai_cache_test.go
git commit -m "feat: set OpenAI prompt cache defaults"
```

---

### Task 4: Make API Log Analyzer Read Transcripts And Flag Cache Problems

**Files:**
- Modify: `tools/api-log-analyze.py`
- Create: `tools/test_api_log_analyze.py`

- [ ] **Step 1: Write failing analyzer tests**

Create `tools/test_api_log_analyze.py`:

```python
import json
import subprocess
from pathlib import Path


def write_jsonl(path: Path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(row) + "\n" for row in rows))


def run_analyzer(path: Path, *args):
    return subprocess.run(
        ["python3", "tools/api-log-analyze.py", str(path), *args],
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=True,
    )


def test_analyzer_reads_transcript_api_call(tmp_path):
    transcript = tmp_path / "sessions" / "s.transcript.jsonl"
    write_jsonl(transcript, [
        {"kind": "header", "session_id": "s", "profile_id": "openai", "model": "gpt-5.5"},
        {
            "kind": "api_call",
            "ts": "2026-05-24T12:00:00Z",
            "round": 3,
            "request": {"provider": "openai", "model": "gpt-5.5", "message_count": 9, "tool_count": 14},
            "response": {
                "model": "gpt-5.5",
                "finish_reason": "stop",
                "text_length": 2,
                "tool_call_count": 0,
                "usage": {"input_tokens": 100, "cache_read_tokens": 900, "output_tokens": 10, "total_tokens": 1010},
            },
            "latency_ms": 25,
        },
    ])
    result = run_analyzer(tmp_path, "--summary")
    assert "gpt-5.5" in result.stdout
    assert "900" in result.stdout


def test_analyzer_flags_uncached_spike(tmp_path):
    api = tmp_path / "api.jsonl"
    write_jsonl(api, [
        {
            "ts": "2026-05-24T12:00:00Z",
            "session_id": "s",
            "round": 1,
            "request": {"provider": "openai", "model": "gpt-5.5"},
            "response": {
                "model": "gpt-5.5",
                "finish_reason": "stop",
                "text_length": 0,
                "tool_call_count": 1,
                "usage": {"input_tokens": 100000, "cache_read_tokens": 2304, "output_tokens": 10, "total_tokens": 102314},
            },
            "latency_ms": 10,
        },
    ])
    result = run_analyzer(tmp_path, "--cache-spikes", "--spike-threshold", "50000")
    assert "UNCACHED_SPIKE" in result.stdout
    assert "100000" in result.stdout
```

- [ ] **Step 2: Run failing tests**

Run: `pytest tools/test_api_log_analyze.py -q`

Expected: FAIL because transcript discovery and `--cache-spikes` do not exist.

- [ ] **Step 3: Extend file discovery**

In `tools/api-log-analyze.py`, update `find_log_files` to return `api.jsonl` and `*.transcript.jsonl`. Update `read_entries` to:

```python
if entry.get("kind") == "api_call":
    entry["_source_kind"] = "transcript"
    entry["session_id"] = entry.get("session_id") or entry.get("_header_session_id", "")
    entries.append(entry)
elif "request" in entry and ("response" in entry or "error" in entry):
    entry["_source_kind"] = "api_jsonl"
    entries.append(entry)
```

When reading transcript files, remember the header session ID from the first line and stamp it onto later `api_call` rows.

- [ ] **Step 4: Add cache summary columns**

In `print_summary`, add:

```python
"cache_read": 0,
"prompt_tokens": 0,
```

For each response usage:

```python
cache_read = usage.get("cache_read_tokens", 0) or 0
input_tokens = usage.get("input_tokens", 0) or 0
s["cache_read"] += cache_read
s["prompt_tokens"] += input_tokens + cache_read
```

Print `CacheRead` and `CacheHit%`.

- [ ] **Step 5: Add spike mode**

Add CLI arguments:

```python
parser.add_argument("--cache-spikes", action="store_true", help="Show high uncached-input calls")
parser.add_argument("--spike-threshold", type=int, default=50000, help="Uncached input token threshold")
```

If `--cache-spikes`, print entries whose `usage.input_tokens >= threshold` with prefix `UNCACHED_SPIKE`.

- [ ] **Step 6: Run tests**

Run: `pytest tools/test_api_log_analyze.py -q`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add tools/api-log-analyze.py tools/test_api_log_analyze.py
git commit -m "feat: analyze API usage from transcripts"
```

---

### Task 5: Add Context Churn Diagnostics To API Call Transcripts

**Files:**
- Modify: `agent/transcript.go`
- Modify: `agent/session.go`
- Modify: `agent/transcript_test.go`

- [ ] **Step 1: Add transcript fields test**

In `agent/transcript_test.go`, add:

```go
func TestTranscriptAPICallIncludesContextDiagnostics(t *testing.T) {
	dir := t.TempDir()
	w, err := NewTranscriptWriter(dir, SessionMeta{ID: "s", Model: "m"})
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	defer w.Close()

	err = w.AppendAPICall(TranscriptAPICall{
		Round:              2,
		Request:            llm.APILogRequest{Model: "m", Provider: "openai", MessageCount: 11, ToolCount: 14},
		ContextHistoryTurns: 9,
		SystemPromptBytes:  24649,
	})
	if err != nil {
		t.Fatalf("AppendAPICall: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "sessions", "s.transcript.jsonl"))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	for _, want := range []string{`"context_history_turns":9`, `"system_prompt_bytes":24649`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("transcript missing %s:\n%s", want, string(data))
		}
	}
}
```

- [ ] **Step 2: Run failing test**

Run: `go test ./agent -run TestTranscriptAPICallIncludesContextDiagnostics -count=1`

Expected: FAIL because fields do not exist.

- [ ] **Step 3: Add fields**

In `agent/transcript.go`, extend `TranscriptAPICall`:

```go
	ContextHistoryTurns int `json:"context_history_turns,omitempty"`
	SystemPromptBytes   int `json:"system_prompt_bytes,omitempty"`
```

- [ ] **Step 4: Populate fields**

In `agent/session.go`, where `apiCall := agent.TranscriptAPICall{...}` is built, set:

```go
ContextHistoryTurns: len(history),
SystemPromptBytes:   len(sys),
```

- [ ] **Step 5: Run tests**

Run: `go test ./agent -run 'TranscriptAPICall|APICall' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/transcript.go agent/session.go agent/transcript_test.go
git commit -m "feat: record context diagnostics on API calls"
```

---

### Task 6: Document The Operational Spend Audit

**Files:**
- Create: `docs/openai-spend-diagnostics.md`

- [ ] **Step 1: Create docs**

Create `docs/openai-spend-diagnostics.md`:

```markdown
# OpenAI Spend Diagnostics

Serf records OpenAI usage in two places:

- `<state-dir>/api.jsonl`: process-level API log, now written by `serf run`, `serf serve`, and embedded TUI.
- `<state-dir>/sessions/*.transcript.jsonl`: session transcript with interleaved `api_call` records.

Use:

```bash
tools/api-log-analyze.py ~/.local/state/serf --summary
tools/api-log-analyze.py ~/.local/state/serf --cache-spikes --spike-threshold 50000
```

Interpretation:

- `input_tokens` is uncached input after provider cache reads have been subtracted by the OpenAI adapter.
- `cache_read_tokens` is prompt input served from provider prompt cache.
- Effective prompt volume is `input_tokens + cache_read_tokens`.
- High cache hit with high spend usually means the session context is very large, not that caching is broken.
- Repeated `UNCACHED_SPIKE` rows mean either the prefix changed, the cache was cold/evicted, routing missed the prior prefix, or context management inserted a large new prefix before stable history.

OpenAI prompt-cache defaults:

- Serf sets a stable per-session `prompt_cache_key`.
- Serf sets `prompt_cache_retention=24h` for OpenAI models on the conservative allowlist.
- Explicit request/provider options override these defaults.
```

- [ ] **Step 2: Run analyzer help**

Run: `tools/api-log-analyze.py --help`

Expected: help includes `--cache-spikes` and `--spike-threshold`.

- [ ] **Step 3: Commit**

```bash
git add docs/openai-spend-diagnostics.md
git commit -m "docs: document OpenAI spend diagnostics"
```

---

## Final Verification

- [ ] Run focused Go tests:

```bash
go test ./cmdutil ./cmd/serf ./cmd/serf-tui ./agent -run 'APILog|OpenAIPromptCache|TranscriptAPICall|Serve|Embedded' -count=1
```

- [ ] Run analyzer tests:

```bash
pytest tools/test_api_log_analyze.py -q
```

- [ ] Run the analyzer against current local data:

```bash
tools/api-log-analyze.py ~/.local/state/serf --summary
tools/api-log-analyze.py ~/.local/state/serf --cache-spikes --spike-threshold 50000
```

- [ ] Confirm `git status --short` only shows intended files before final commit or PR.

## Self-Review Notes

This plan covers the observed infelicities:

- `api.jsonl` missing for `serve` and embedded TUI paths.
- Recent spend requiring transcript inspection instead of process API logs.
- OpenAI cache fields available but no default `prompt_cache_key` or 24h retention on long-session models.
- Large uncached spikes lacking context diagnostics.
- No operational doc explaining how to audit cache hit rates and interpret uncached input.

