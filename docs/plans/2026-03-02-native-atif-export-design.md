# Native ATIF Export — Design

## Problem

Harbor viewer reads serf's job data (rewards, timing, files) but the Trajectory tab is
empty because serf doesn't emit ATIF. Every other harbor agent (Claude Code, Codex) converts
its native format to ATIF in the Python adapter's `populate_context_post_run()`. We want
serf to export ATIF natively from the Go binary, making the trajectory available regardless
of how serf is invoked.

## Decision Summary

- **Where:** Go binary writes `trajectory.json` at session close
- **How:** Post-hoc conversion — read transcript JSONL back, convert to ATIF, write JSON
- **Flag:** `--export-atif <path>` — enables export and specifies output location
- **Scope:** Root session only (no subagent trajectory refs — matches Claude Code and Codex)
- **Fidelity:** Lossless — all internal data preserved via ATIF `extra` fields

## Architecture

```
Session runs → transcript.Append() writes JSONL (unchanged)
                    ↓
Session.Close() → ReadTranscript(path) → (header, entries)
                    ↓
              ConvertToATIF(header, entries) → ATIFTrajectory
                    ↓
              json.Marshal → write to --export-atif path
```

The transcript remains the operational format (append-only JSONL, crash recovery, session
resume). ATIF is a derived observability format written once at close.

### Converter Function

```go
func ConvertToATIF(header TranscriptHeader, entries []TranscriptEntry) ATIFTrajectory
```

Pure function. Takes transcript data, returns ATIF struct. Testable independently of Session.

### Flag

`--export-atif <path>` on the CLI. Maps to `SessionConfig.ExportATIFPath string`.
When empty (default), no trajectory is written. The adapter sets this to
`/logs/agent/trajectory.json` (harbor's bind-mounted logs dir), so serf writes directly
to where harbor viewer expects the file.

## Turn Mapping

| Serf Turn Kind | ATIF source | Notes |
|----------------|-------------|-------|
| USER_INPUT     | "user"      | Concatenate text content parts |
| ASSISTANT      | "agent"     | text→message, tool_call→tool_calls, thinking→reasoning_content |
| TOOL_RESULTS   | (merged)    | Merged into preceding agent step as observation.results |
| STEERING       | "system"    | Concatenate text content parts |
| CHECKPOINT     | "system"    | Preserved with extra.serf_kind="checkpoint" |
| SUMMARY        | "system"    | Preserved with extra.serf_kind="summary" |

### Content Part Mapping (ASSISTANT turns)

| ContentPart.Kind | ATIF field |
|------------------|------------|
| text             | step.message (concatenated) |
| tool_call        | step.tool_calls[] |
| thinking         | step.reasoning_content (concatenated) |
| redacted_thinking| step.extra.has_redacted_thinking = true |
| web_search       | step.extra.web_searches[] |

### TOOL_RESULTS → Observation

Each `tool_result` content part becomes an ObservationResult:
- `tool_result.tool_call_id` → `result.source_call_id`
- `tool_result.content` → `result.content` (stringified)
- `tool_result.is_error` → step.extra.tool_errors[call_id]
- `tool_result.duration_ms` → step.extra.tool_durations_ms[call_id]

### Usage → Metrics

| Serf Usage field | ATIF Metrics field |
|------------------|--------------------|
| input_tokens     | prompt_tokens |
| output_tokens    | completion_tokens |
| cache_read_tokens| cached_tokens |
| reasoning_tokens | metrics.extra.reasoning_tokens |
| cache_write_tokens| metrics.extra.cache_write_tokens |
| raw              | metrics.extra.raw_usage |

## Lossless Preservation via `extra`

ATIF allows `extra: dict` on trajectory root, agent, steps, and metrics. We use these to
preserve all internal data:

| Internal Data | ATIF extra location |
|---------------|---------------------|
| Parent session lineage | root extra: {parent_session_id, parent_tool_call_id, depth} |
| System prompt | root extra: {system_prompt} |
| Working directory | root extra: {working_dir} |
| Profile ID | agent extra: {profile_id} |
| Response IDs | step extra: {response_id} |
| Phase annotations | step extra: {phases: [...]} |
| Tool error flags | step extra: {tool_errors: {call_id: bool}} |
| Tool durations | step extra: {tool_durations_ms: {call_id: int}} |
| Thinking signatures | step extra: {thinking_signature} |
| Redacted thinking | step extra: {has_redacted_thinking: true} |
| Web searches | step extra: {web_searches: [{query, raw}]} |
| Checkpoint data | step extra: {serf_kind: "checkpoint", ...data} |
| Summary content | step extra: {serf_kind: "summary"} |
| Reasoning tokens | metrics extra: {reasoning_tokens} |
| Cache write tokens | metrics extra: {cache_write_tokens} |

## Go Types

New file: `agent/atif.go`

```go
type ATIFTrajectory struct {
    SchemaVersion string         `json:"schema_version"`
    SessionID     string         `json:"session_id"`
    Agent         ATIFAgent      `json:"agent"`
    Steps         []ATIFStep     `json:"steps"`
    FinalMetrics  *ATIFFinalMetrics `json:"final_metrics,omitempty"`
    Extra         map[string]any `json:"extra,omitempty"`
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
```

## Adapter Change

In `serf_agent.py`, add `--export-atif` flag to the command in `create_run_agent_commands()`:

```python
export_atif_path = f"{_CONTAINER_STATE_DIR}/trajectory.json"
# ... in command string:
f"--export-atif {export_atif_path} "
```

Then in `run()`, after downloading serf-state, copy the trajectory to logs_dir root:

```python
traj = local_state_dir / "trajectory.json"
if traj.exists():
    shutil.copy2(traj, self.logs_dir / "trajectory.json")
```

Alternatively, `--export-atif /logs/agent/trajectory.json` writes directly to the
bind-mounted dir, needing no copy. Either approach works.

## Testing

- `agent/atif_test.go` — unit tests for `ConvertToATIF()` with synthetic transcript data
- Test cases: simple conversation, tool use + observation merge, thinking→reasoning_content,
  checkpoint/summary preservation, metrics accumulation, empty transcript, lossless extra fields
- Integration: single-task harbor run, verify trajectory.json appears and harbor viewer renders it
