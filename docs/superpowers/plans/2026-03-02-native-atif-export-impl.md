# Native ATIF Export Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Serf writes a `trajectory.json` (ATIF v1.6) alongside its transcript when `--export-atif <path>` is passed.

**Architecture:** Post-hoc conversion at `Session.Close()` — read the transcript JSONL back, convert to ATIF structs, marshal to JSON, write to the specified path. The converter is a pure function `ConvertToATIF(header, entries)` tested independently of Session.

**Tech Stack:** Go, standard library only (no new dependencies).

**Design doc:** `docs/plans/2026-03-02-native-atif-export-design.md`

---

### Task 1: ATIF types and simple converter test

**Files:**
- Create: `agent/atif.go`
- Create: `agent/atif_test.go`

**Step 1: Write the failing test for a simple 2-turn conversation**

In `agent/atif_test.go`:

```go
package agent

import (
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

func TestConvertToATIF_SimpleConversation(t *testing.T) {
	header := TranscriptHeader{
		SessionID:    "sess-001",
		Model:        "gpt-5.3-codex",
		BuildVersion: "v0.1.0-abc1234",
		ProfileID:    "openai",
		WorkingDir:   "/app",
		CreatedAt:    time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
	}

	ts1 := time.Date(2026, 3, 2, 12, 0, 1, 0, time.UTC)
	ts2 := time.Date(2026, 3, 2, 12, 0, 5, 0, time.UTC)

	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: Turn{
			Kind:      TurnUserInput,
			Message:   llm.User("Fix the bug in main.go"),
			Timestamp: ts1,
		}},
		{Kind: "entry", Seq: 1, Turn: Turn{
			Kind:      TurnAssistant,
			Message:   llm.Assistant("I'll fix the bug by changing line 42."),
			Timestamp: ts2,
			Usage:     llm.Usage{InputTokens: 100, OutputTokens: 50},
		}},
	}

	traj := ConvertToATIF(header, entries)

	if traj.SchemaVersion != "ATIF-v1.6" {
		t.Errorf("schema_version = %q, want ATIF-v1.6", traj.SchemaVersion)
	}
	if traj.SessionID != "sess-001" {
		t.Errorf("session_id = %q, want sess-001", traj.SessionID)
	}
	if traj.Agent.Name != "serf" {
		t.Errorf("agent.name = %q, want serf", traj.Agent.Name)
	}
	if traj.Agent.Version != "v0.1.0-abc1234" {
		t.Errorf("agent.version = %q, want v0.1.0-abc1234", traj.Agent.Version)
	}
	if traj.Agent.ModelName != "gpt-5.3-codex" {
		t.Errorf("agent.model_name = %q, want gpt-5.3-codex", traj.Agent.ModelName)
	}
	if len(traj.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(traj.Steps))
	}

	// Step 1: user
	s1 := traj.Steps[0]
	if s1.StepID != 1 {
		t.Errorf("step[0].step_id = %d, want 1", s1.StepID)
	}
	if s1.Source != "user" {
		t.Errorf("step[0].source = %q, want user", s1.Source)
	}
	if s1.Message != "Fix the bug in main.go" {
		t.Errorf("step[0].message = %q", s1.Message)
	}
	if s1.Timestamp != "2026-03-02T12:00:01Z" {
		t.Errorf("step[0].timestamp = %q", s1.Timestamp)
	}

	// Step 2: agent
	s2 := traj.Steps[1]
	if s2.StepID != 2 {
		t.Errorf("step[1].step_id = %d, want 2", s2.StepID)
	}
	if s2.Source != "agent" {
		t.Errorf("step[1].source = %q, want agent", s2.Source)
	}
	if s2.Message != "I'll fix the bug by changing line 42." {
		t.Errorf("step[1].message = %q", s2.Message)
	}
	if s2.Metrics == nil {
		t.Fatal("step[1].metrics is nil")
	}
	if s2.Metrics.PromptTokens != 100 {
		t.Errorf("prompt_tokens = %d, want 100", s2.Metrics.PromptTokens)
	}
	if s2.Metrics.CompletionTokens != 50 {
		t.Errorf("completion_tokens = %d, want 50", s2.Metrics.CompletionTokens)
	}

	// Final metrics
	if traj.FinalMetrics == nil {
		t.Fatal("final_metrics is nil")
	}
	if traj.FinalMetrics.TotalPromptTokens != 100 {
		t.Errorf("total_prompt_tokens = %d, want 100", traj.FinalMetrics.TotalPromptTokens)
	}
	if traj.FinalMetrics.TotalSteps != 2 {
		t.Errorf("total_steps = %d, want 2", traj.FinalMetrics.TotalSteps)
	}

	// Extra fields: root
	if traj.Extra["working_dir"] != "/app" {
		t.Errorf("extra.working_dir = %v", traj.Extra["working_dir"])
	}
	if traj.Agent.Extra["profile_id"] != "openai" {
		t.Errorf("agent.extra.profile_id = %v", traj.Agent.Extra["profile_id"])
	}
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./agent/ -run TestConvertToATIF_SimpleConversation -v`
Expected: compilation error — `ConvertToATIF` undefined

**Step 3: Write minimal ATIF types and converter stub**

Create `agent/atif.go`:

```go
package agent

import (
	"strings"

	"primeradiant.com/serf/llm"
)

// ATIF v1.6 types — see docs/plans/2026-03-02-native-atif-export-design.md

type ATIFTrajectory struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Agent         ATIFAgent         `json:"agent"`
	Steps         []ATIFStep        `json:"steps"`
	FinalMetrics  *ATIFFinalMetrics `json:"final_metrics,omitempty"`
	Extra         map[string]any    `json:"extra,omitempty"`
}

type ATIFAgent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	ModelName string         `json:"model_name,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

type ATIFStep struct {
	StepID           int                `json:"step_id"`
	Source           string             `json:"source"`
	Message          string             `json:"message"`
	Timestamp        string             `json:"timestamp,omitempty"`
	ModelName        string             `json:"model_name,omitempty"`
	ReasoningContent string             `json:"reasoning_content,omitempty"`
	ToolCalls        []ATIFToolCall     `json:"tool_calls,omitempty"`
	Observation      *ATIFObservation   `json:"observation,omitempty"`
	Metrics          *ATIFStepMetrics   `json:"metrics,omitempty"`
	Extra            map[string]any     `json:"extra,omitempty"`
}

type ATIFToolCall struct {
	ToolCallID   string         `json:"tool_call_id"`
	FunctionName string         `json:"function_name"`
	Arguments    map[string]any `json:"arguments"`
}

type ATIFObservation struct {
	Results []ATIFObservationResult `json:"results"`
}

type ATIFObservationResult struct {
	SourceCallID string `json:"source_call_id,omitempty"`
	Content      string `json:"content,omitempty"`
}

type ATIFStepMetrics struct {
	PromptTokens     int            `json:"prompt_tokens,omitempty"`
	CompletionTokens int            `json:"completion_tokens,omitempty"`
	CachedTokens     int            `json:"cached_tokens,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

type ATIFFinalMetrics struct {
	TotalPromptTokens     int            `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens int            `json:"total_completion_tokens,omitempty"`
	TotalCachedTokens     int            `json:"total_cached_tokens,omitempty"`
	TotalSteps            int            `json:"total_steps,omitempty"`
	Extra                 map[string]any `json:"extra,omitempty"`
}

// ConvertToATIF converts a serf transcript (header + entries) to an ATIF v1.6 trajectory.
// Lossless: all internal data is preserved in extra fields.
func ConvertToATIF(header TranscriptHeader, entries []TranscriptEntry) ATIFTrajectory {
	traj := ATIFTrajectory{
		SchemaVersion: "ATIF-v1.6",
		SessionID:     header.SessionID,
		Agent: ATIFAgent{
			Name:      "serf",
			Version:   header.BuildVersion,
			ModelName: header.Model,
			Extra:     map[string]any{},
		},
		Extra: map[string]any{},
	}
	if traj.Agent.Version == "" {
		traj.Agent.Version = "unknown"
	}
	if header.ProfileID != "" {
		traj.Agent.Extra["profile_id"] = header.ProfileID
	}
	if header.WorkingDir != "" {
		traj.Extra["working_dir"] = header.WorkingDir
	}
	if header.ParentSessionID != "" {
		traj.Extra["parent_session_id"] = header.ParentSessionID
	}
	if header.ParentToolCallID != "" {
		traj.Extra["parent_tool_call_id"] = header.ParentToolCallID
	}
	if header.Depth > 0 {
		traj.Extra["depth"] = header.Depth
	}
	if header.SystemPrompt != "" {
		traj.Extra["system_prompt"] = header.SystemPrompt
	}

	var totalPrompt, totalCompletion, totalCached int
	stepID := 1

	for i := 0; i < len(entries); i++ {
		e := entries[i]
		switch e.Turn.Kind {
		case TurnUserInput:
			step := ATIFStep{
				StepID:    stepID,
				Source:    "user",
				Message:   e.Turn.Message.Text(),
				Timestamp: e.Turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			}
			traj.Steps = append(traj.Steps, step)
			stepID++

		case TurnAssistant:
			step := convertAssistantTurn(e.Turn, stepID)
			// Merge following TOOL_RESULTS into this step's observation.
			if i+1 < len(entries) && entries[i+1].Turn.Kind == TurnToolResults {
				i++
				step.Observation = convertToolResults(entries[i].Turn)
			}
			if step.Metrics != nil {
				totalPrompt += step.Metrics.PromptTokens
				totalCompletion += step.Metrics.CompletionTokens
				totalCached += step.Metrics.CachedTokens
			}
			traj.Steps = append(traj.Steps, step)
			stepID++

		case TurnSteering:
			step := ATIFStep{
				StepID:    stepID,
				Source:    "system",
				Message:   e.Turn.Message.Text(),
				Timestamp: e.Turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
			}
			traj.Steps = append(traj.Steps, step)
			stepID++

		case TurnCheckpoint:
			step := ATIFStep{
				StepID:    stepID,
				Source:    "system",
				Message:   e.Turn.Message.Text(),
				Timestamp: e.Turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				Extra:     map[string]any{"serf_kind": "checkpoint"},
			}
			traj.Steps = append(traj.Steps, step)
			stepID++

		case TurnSummary:
			step := ATIFStep{
				StepID:    stepID,
				Source:    "system",
				Message:   e.Turn.Message.Text(),
				Timestamp: e.Turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				Extra:     map[string]any{"serf_kind": "summary"},
			}
			traj.Steps = append(traj.Steps, step)
			stepID++

		case TurnToolResults:
			// Orphaned TOOL_RESULTS (no preceding ASSISTANT) — include as system step.
			step := ATIFStep{
				StepID:    stepID,
				Source:    "system",
				Message:   "(orphaned tool results)",
				Timestamp: e.Turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
				Extra:     map[string]any{"serf_kind": "orphaned_tool_results"},
			}
			step.Observation = convertToolResults(e.Turn)
			traj.Steps = append(traj.Steps, step)
			stepID++
		}
	}

	traj.FinalMetrics = &ATIFFinalMetrics{
		TotalPromptTokens:     totalPrompt,
		TotalCompletionTokens: totalCompletion,
		TotalCachedTokens:     totalCached,
		TotalSteps:            len(traj.Steps),
	}

	return traj
}

func convertAssistantTurn(turn Turn, stepID int) ATIFStep {
	step := ATIFStep{
		StepID:    stepID,
		Source:    "agent",
		Timestamp: turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
		Extra:     map[string]any{},
	}

	var msgParts []string
	var reasoningParts []string
	var phases []string

	for _, p := range turn.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			msgParts = append(msgParts, p.Text)
			if p.Phase != "" {
				phases = append(phases, p.Phase)
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				tc := ATIFToolCall{
					ToolCallID:   p.ToolCall.ID,
					FunctionName: p.ToolCall.Name,
					Arguments:    map[string]any{},
				}
				if err := p.ToolCall.Parse(); err == nil && p.ToolCall.ParsedArguments != nil {
					tc.Arguments = p.ToolCall.ParsedArguments
				}
				step.ToolCalls = append(step.ToolCalls, tc)
			}
		case llm.ContentThinking:
			if p.Thinking != nil && p.Thinking.Text != "" {
				reasoningParts = append(reasoningParts, p.Thinking.Text)
			}
			if p.Thinking != nil && p.Thinking.Signature != "" {
				step.Extra["thinking_signature"] = p.Thinking.Signature
			}
		case llm.ContentRedThinking:
			step.Extra["has_redacted_thinking"] = true
		case llm.ContentWebSearch:
			ws := map[string]any{}
			if p.WebSearch != nil {
				ws["query"] = p.WebSearch.Query
			}
			existing, _ := step.Extra["web_searches"].([]any)
			step.Extra["web_searches"] = append(existing, ws)
		}
	}

	step.Message = strings.Join(msgParts, "")
	if len(reasoningParts) > 0 {
		step.ReasoningContent = strings.Join(reasoningParts, "\n")
	}
	if len(phases) > 0 {
		step.Extra["phases"] = phases
	}
	if turn.ResponseID != "" {
		step.Extra["response_id"] = turn.ResponseID
	}

	// Metrics from usage
	u := turn.Usage
	if u.InputTokens > 0 || u.OutputTokens > 0 {
		step.Metrics = &ATIFStepMetrics{
			PromptTokens:     u.InputTokens,
			CompletionTokens: u.OutputTokens,
			Extra:            map[string]any{},
		}
		if u.CacheReadTokens != nil {
			step.Metrics.CachedTokens = *u.CacheReadTokens
		}
		if u.ReasoningTokens != nil {
			step.Metrics.Extra["reasoning_tokens"] = *u.ReasoningTokens
		}
		if u.CacheWriteTokens != nil {
			step.Metrics.Extra["cache_write_tokens"] = *u.CacheWriteTokens
		}
	}

	return step
}

func convertToolResults(turn Turn) *ATIFObservation {
	obs := &ATIFObservation{}
	toolErrors := map[string]bool{}
	toolDurations := map[string]int64{}

	for _, p := range turn.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
			result := ATIFObservationResult{
				SourceCallID: p.ToolResult.ToolCallID,
				Content:      fmt.Sprintf("%v", p.ToolResult.Content),
			}
			obs.Results = append(obs.Results, result)
			if p.ToolResult.IsError {
				toolErrors[p.ToolResult.ToolCallID] = true
			}
			if p.ToolResult.DurationMS > 0 {
				toolDurations[p.ToolResult.ToolCallID] = p.ToolResult.DurationMS
			}
		}
	}

	return obs
}
```

Note: the `convertToolResults` function needs `"fmt"` imported. Also, the tool error/duration metadata needs to be attached to the *step*'s extra, not the observation (ATIF observations don't have extra fields). We'll handle this in Task 2 when we add the tool use test. For now the simple conversation test doesn't exercise tool results.

**Step 4: Run the test to verify it passes**

Run: `go test ./agent/ -run TestConvertToATIF_SimpleConversation -v`
Expected: PASS

**Step 5: Commit**

```bash
git add agent/atif.go agent/atif_test.go
git commit -m "feat: add ATIF v1.6 types and converter for simple conversations"
```

---

### Task 2: Tool use with observation merge

**Files:**
- Modify: `agent/atif_test.go`
- Modify: `agent/atif.go` (fix tool error/duration attachment)

**Step 1: Write the failing test for tool calls + observation**

Append to `agent/atif_test.go`:

```go
func TestConvertToATIF_ToolUse(t *testing.T) {
	header := TranscriptHeader{
		SessionID: "sess-002",
		Model:     "gpt-5.3-codex",
	}

	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: Turn{
			Kind:      TurnUserInput,
			Message:   llm.User("List files in /app"),
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
		{Kind: "entry", Seq: 1, Turn: Turn{
			Kind: TurnAssistant,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Let me list the files."},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID:        "call-1",
						Name:      "shell",
						Arguments: json.RawMessage(`{"command":"ls /app"}`),
					}},
				},
			},
			Timestamp: time.Date(2026, 3, 2, 12, 0, 1, 0, time.UTC),
			Usage:     llm.Usage{InputTokens: 200, OutputTokens: 30},
		}},
		{Kind: "entry", Seq: 2, Turn: Turn{
			Kind: TurnToolResults,
			Message: llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "call-1",
						Content:    "main.go\ngo.mod\n",
						IsError:    false,
						DurationMS: 150,
					}},
				},
			},
			Timestamp: time.Date(2026, 3, 2, 12, 0, 2, 0, time.UTC),
		}},
	}

	traj := ConvertToATIF(header, entries)

	if len(traj.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2 (user + agent with merged tool results)", len(traj.Steps))
	}

	agent := traj.Steps[1]
	if agent.Source != "agent" {
		t.Errorf("step[1].source = %q, want agent", agent.Source)
	}
	if agent.Message != "Let me list the files." {
		t.Errorf("step[1].message = %q", agent.Message)
	}
	if len(agent.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(agent.ToolCalls))
	}
	tc := agent.ToolCalls[0]
	if tc.ToolCallID != "call-1" {
		t.Errorf("tool_call_id = %q, want call-1", tc.ToolCallID)
	}
	if tc.FunctionName != "shell" {
		t.Errorf("function_name = %q, want shell", tc.FunctionName)
	}
	if tc.Arguments["command"] != "ls /app" {
		t.Errorf("arguments.command = %v", tc.Arguments["command"])
	}

	// Observation merged from TOOL_RESULTS
	if agent.Observation == nil {
		t.Fatal("observation is nil")
	}
	if len(agent.Observation.Results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(agent.Observation.Results))
	}
	r := agent.Observation.Results[0]
	if r.SourceCallID != "call-1" {
		t.Errorf("source_call_id = %q, want call-1", r.SourceCallID)
	}
	if r.Content != "main.go\ngo.mod\n" {
		t.Errorf("content = %q", r.Content)
	}

	// Tool durations in step extra
	durations, ok := agent.Extra["tool_durations_ms"].(map[string]int64)
	if !ok {
		t.Fatalf("tool_durations_ms missing or wrong type: %T", agent.Extra["tool_durations_ms"])
	}
	if durations["call-1"] != 150 {
		t.Errorf("tool_durations_ms[call-1] = %d, want 150", durations["call-1"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestConvertToATIF_ToolUse -v`
Expected: FAIL — `json` import needed in test, and tool error/duration metadata not attached to step extra yet.

**Step 3: Fix the converter to pass tool metadata to step extra**

Update `convertToolResults` to return the metadata maps, and update the caller in `ConvertToATIF` to attach them to the step's `Extra` field. Also update `convertToolResults` signature:

```go
// Returns observation + tool error map + tool duration map
func convertToolResults(turn Turn) (*ATIFObservation, map[string]bool, map[string]int64) {
```

The caller in the ASSISTANT case merges these maps into `step.Extra`:
```go
obs, toolErrors, toolDurations := convertToolResults(entries[i].Turn)
step.Observation = obs
if len(toolErrors) > 0 {
    step.Extra["tool_errors"] = toolErrors
}
if len(toolDurations) > 0 {
    step.Extra["tool_durations_ms"] = toolDurations
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestConvertToATIF -v`
Expected: both tests PASS

**Step 5: Commit**

```bash
git add agent/atif.go agent/atif_test.go
git commit -m "feat: ATIF converter handles tool calls and observation merge"
```

---

### Task 3: Thinking, checkpoint, summary, and edge cases

**Files:**
- Modify: `agent/atif_test.go`

**Step 1: Write tests for thinking content, checkpoint/summary preservation, and empty transcript**

Append to `agent/atif_test.go`:

```go
func TestConvertToATIF_ThinkingContent(t *testing.T) {
	header := TranscriptHeader{SessionID: "sess-003", Model: "claude-opus-4-6"}
	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: Turn{
			Kind: TurnAssistant,
			Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
						Text:      "Let me think about this...",
						Signature: "sig-abc",
					}},
					{Kind: llm.ContentText, Text: "Here's my answer."},
				},
			},
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
	}

	traj := ConvertToATIF(header, entries)
	if len(traj.Steps) != 1 {
		t.Fatalf("len(steps) = %d, want 1", len(traj.Steps))
	}
	s := traj.Steps[0]
	if s.ReasoningContent != "Let me think about this..." {
		t.Errorf("reasoning_content = %q", s.ReasoningContent)
	}
	if s.Message != "Here's my answer." {
		t.Errorf("message = %q", s.Message)
	}
	if s.Extra["thinking_signature"] != "sig-abc" {
		t.Errorf("thinking_signature = %v", s.Extra["thinking_signature"])
	}
}

func TestConvertToATIF_CheckpointAndSummary(t *testing.T) {
	header := TranscriptHeader{SessionID: "sess-004", Model: "gpt-5.3-codex"}
	entries := []TranscriptEntry{
		{Kind: "entry", Seq: 0, Turn: Turn{
			Kind:      TurnCheckpoint,
			Message:   llm.User("checkpoint data here"),
			Timestamp: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
		}},
		{Kind: "entry", Seq: 1, Turn: Turn{
			Kind:      TurnSummary,
			Message:   llm.User("summary of conversation"),
			Timestamp: time.Date(2026, 3, 2, 12, 1, 0, 0, time.UTC),
		}},
	}

	traj := ConvertToATIF(header, entries)
	if len(traj.Steps) != 2 {
		t.Fatalf("len(steps) = %d, want 2", len(traj.Steps))
	}
	if traj.Steps[0].Extra["serf_kind"] != "checkpoint" {
		t.Errorf("step[0].extra.serf_kind = %v", traj.Steps[0].Extra["serf_kind"])
	}
	if traj.Steps[0].Source != "system" {
		t.Errorf("step[0].source = %q, want system", traj.Steps[0].Source)
	}
	if traj.Steps[1].Extra["serf_kind"] != "summary" {
		t.Errorf("step[1].extra.serf_kind = %v", traj.Steps[1].Extra["serf_kind"])
	}
}

func TestConvertToATIF_EmptyTranscript(t *testing.T) {
	header := TranscriptHeader{SessionID: "sess-005", Model: "gpt-5.3-codex"}
	traj := ConvertToATIF(header, nil)

	if traj.SessionID != "sess-005" {
		t.Errorf("session_id = %q", traj.SessionID)
	}
	if len(traj.Steps) != 0 {
		t.Errorf("len(steps) = %d, want 0", len(traj.Steps))
	}
	if traj.FinalMetrics.TotalSteps != 0 {
		t.Errorf("total_steps = %d, want 0", traj.FinalMetrics.TotalSteps)
	}
}

func TestConvertToATIF_MissingBuildVersion(t *testing.T) {
	header := TranscriptHeader{SessionID: "sess-006", Model: "gpt-5.3-codex"}
	traj := ConvertToATIF(header, nil)
	if traj.Agent.Version != "unknown" {
		t.Errorf("agent.version = %q, want unknown", traj.Agent.Version)
	}
}
```

**Step 2: Run tests to verify they pass**

Run: `go test ./agent/ -run TestConvertToATIF -v`
Expected: all tests PASS (these exercise already-implemented code paths)

**Step 3: Commit**

```bash
git add agent/atif_test.go
git commit -m "test: add ATIF converter tests for thinking, checkpoint, summary, edge cases"
```

---

### Task 4: Wire `--export-atif` flag and Session.Close() integration

**Files:**
- Modify: `agent/session.go:59-160` (add ExportATIFPath to SessionConfig)
- Modify: `agent/session.go:680-744` (add ATIF export to Close)
- Modify: `cmd/serf/main.go:33-95` (add flag)
- Modify: `cmd/serf/run.go:20-49` (add to runConfig)
- Modify: `cmd/serf/run.go:158-178` (pass to SessionConfig)

**Step 1: Write a test that verifies ATIF export from transcript on disk**

Append to `agent/atif_test.go`:

```go
func TestExportATIF_WritesFile(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")

	// Write a transcript JSONL file.
	header := TranscriptHeader{
		SessionID: "sess-export",
		Model:     "gpt-5.3-codex",
		CreatedAt: time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC),
	}
	tpath := filepath.Join(sessionsDir, "sess-export.transcript.jsonl")
	tw, err := NewTranscriptWriter(tpath, header)
	if err != nil {
		t.Fatalf("NewTranscriptWriter: %v", err)
	}
	tw.Append(Turn{
		Kind:      TurnUserInput,
		Message:   llm.User("hello"),
		Timestamp: time.Date(2026, 3, 2, 12, 0, 1, 0, time.UTC),
	})
	tw.Close()

	// Export to a specific path.
	outPath := filepath.Join(dir, "trajectory.json")
	err = ExportATIF(tpath, outPath)
	if err != nil {
		t.Fatalf("ExportATIF: %v", err)
	}

	// Read and verify.
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read trajectory.json: %v", err)
	}
	var traj ATIFTrajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		t.Fatalf("unmarshal trajectory: %v", err)
	}
	if traj.SessionID != "sess-export" {
		t.Errorf("session_id = %q", traj.SessionID)
	}
	if len(traj.Steps) != 1 {
		t.Errorf("len(steps) = %d, want 1", len(traj.Steps))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExportATIF_WritesFile -v`
Expected: compilation error — `ExportATIF` undefined

**Step 3: Implement `ExportATIF` and wire into Session.Close**

Add to `agent/atif.go`:

```go
// ExportATIF reads a transcript JSONL file, converts to ATIF, and writes to outPath.
func ExportATIF(transcriptPath, outPath string) error {
	header, entries, _, err := ReadTranscript(transcriptPath)
	if err != nil {
		return fmt.Errorf("read transcript: %w", err)
	}
	traj := ConvertToATIF(header, entries)
	data, err := json.MarshalIndent(traj, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ATIF: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := os.WriteFile(outPath, data, 0o644); err != nil {
		return fmt.Errorf("write ATIF: %w", err)
	}
	return nil
}
```

Add imports `"encoding/json"`, `"fmt"`, `"os"`, `"path/filepath"` to atif.go.

Add `ExportATIFPath` to `SessionConfig` in `agent/session.go`:

```go
// ExportATIFPath, when non-empty, causes Session.Close to export an ATIF v1.6
// trajectory JSON file to this path. The path can be absolute or relative to
// the working directory.
ExportATIFPath string `json:"-"`
```

Add ATIF export to `Session.Close()` in `agent/session.go`, right after `s.transcript.Close()`:

```go
if s.transcript != nil {
    _ = s.transcript.Close()
}

// Export ATIF trajectory if configured (after transcript is closed/flushed).
if s.config.ExportATIFPath != "" && s.stateDir != "" && s.config.Depth == 0 {
    tpath := filepath.Join(s.stateDir, sessionsSubdir, s.id+".transcript.jsonl")
    if err := ExportATIF(tpath, s.config.ExportATIFPath); err != nil {
        s.emit(EventWarning, WarningData{Message: fmt.Sprintf("ATIF export failed: %v", err)})
    }
}
```

Note: `s.config.Depth == 0` ensures only root sessions export (matching the design decision).

**Step 4: Add the CLI flag**

In `cmd/serf/main.go`, add after `reasoningEffort` flag (around line 46):

```go
exportATIF := flag.String("export-atif", "", "export ATIF v1.6 trajectory to this path on session close")
```

In `cmd/serf/run.go`, add to `runConfig` struct:

```go
exportATIF string // --export-atif path
```

In `main.go`, add to the `run()` call:

```go
exportATIF: *exportATIF,
```

In `run.go`, add to the `SessionConfig` construction (around line 173):

```go
ExportATIFPath: cfg.exportATIF,
```

Add to `flag.Usage()` in main.go:

```go
fmt.Fprintf(os.Stderr, "  --export-atif <path>  Export ATIF v1.6 trajectory to this path on session close\n")
```

**Step 5: Run tests**

Run: `go test ./agent/ -run TestExportATIF -v && go test ./agent/ -run TestConvertToATIF -v`
Expected: all PASS

Run: `go test ./... -short` to verify nothing is broken.

**Step 6: Commit**

```bash
git add agent/atif.go agent/atif_test.go agent/session.go cmd/serf/main.go cmd/serf/run.go
git commit -m "feat: --export-atif flag writes ATIF v1.6 trajectory at session close"
```

---

### Task 5: Wire adapter and validate end-to-end

**Files:**
- Modify: `tools/serf_agent.py:109-124` (add --export-atif to command)
- Modify: `tools/serf_agent.py:126-158` (copy trajectory from serf-state)

**Step 1: Add `--export-atif` flag to the adapter command**

In `tools/serf_agent.py`, in `create_run_agent_commands()`, add the export flag.
Insert before the command string construction (around line 109):

```python
export_atif_flag = f"--export-atif {_CONTAINER_STATE_DIR}/trajectory.json "
```

And include it in the command string:

```python
return [
    ExecInput(
        command=(
            f"serf --provider {self._provider} "
            f"--model {self._model} "
            f"--max-rounds {self._max_rounds} "
            f"{min_result_flag}"
            f"{reviewer_gate_flag}"
            f"{result_tool_name_flag}"
            f"--state-dir {_CONTAINER_STATE_DIR} "
            f"{export_atif_flag}"
            f"{effort_flag}"
            f"-- {escaped}"
        ),
        env=env,
    ),
]
```

**Step 2: Copy trajectory to logs_dir in `run()`**

In `tools/serf_agent.py`, in the `run()` method's `finally` block, after downloading
serf-state and artifacts, add:

```python
# Copy ATIF trajectory to logs_dir root for harbor viewer.
traj_src = local_state_dir / "trajectory.json"
if traj_src.exists():
    shutil.copy2(traj_src, self.logs_dir / "trajectory.json")
    logger.info("Copied ATIF trajectory to %s", self.logs_dir / "trajectory.json")
```

**Step 3: Commit**

```bash
git add tools/serf_agent.py
git commit -m "feat: adapter passes --export-atif flag and copies trajectory for harbor viewer"
```

**Step 4: Build and deploy**

```bash
# Build
cd /Users/jesse/prime-radiant/serf
GOOS=linux GOARCH=amd64 go build -ldflags "$(go run ./cmd/serf/ --version 2>&1 | head -1 || echo '')" -o /tmp/serf-linux-amd64 ./cmd/serf/

# Deploy updated binary + adapter to magic-kingdom
./tools/run_eval.py launch --task build-cython-ext --reps 1 --dry-run
```

**Step 5: Run a single-task eval to validate**

```bash
./tools/run_eval.py launch --task build-cython-ext --reps 1
```

After completion, verify:

```bash
# Check trajectory exists
ssh jesse@magic-kingdom 'find /data/serf-evals/runs/ -name "trajectory.json" -newer /tmp/check-timestamp 2>/dev/null | head -5'

# Verify it's valid JSON with ATIF structure
ssh jesse@magic-kingdom 'cat $(find /data/serf-evals/runs/ -name "trajectory.json" | head -1) | python3 -m json.tool | head -20'
```

Open harbor viewer at `http://magic-kingdom:8081`, navigate to the trial, check the Trajectory tab.

**Step 6: Commit any fixes needed**

---

## Verification Checklist

1. `go test ./agent/ -run TestConvertToATIF -v` — all converter tests pass
2. `go test ./agent/ -run TestExportATIF -v` — file export test passes
3. `go test ./... -short` — all Go tests pass, no regressions
4. `go build ./cmd/serf/` — binary builds
5. `./serf --export-atif /tmp/test-traj.json --provider openai --model gpt-5-mini-2025-08-07 -- "echo hello"` — trajectory.json written
6. Single-task harbor run produces valid trajectory.json
7. Harbor viewer at magic-kingdom:8081 renders trajectory tab with steps
