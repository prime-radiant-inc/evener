// Package atif converts a serf session transcript into an Agent Trajectory
// Interchange Format (ATIF v1.7) document.
package atif

import (
	"encoding/json"
	"fmt"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// ATIF v1.7 types — Agent Trajectory Interchange Format.

// Trajectory is the root object of an ATIF v1.7 document.
type Trajectory struct {
	SchemaVersion string         `json:"schema_version"`
	SessionID     string         `json:"session_id"`
	Agent         Agent          `json:"agent"`
	Steps         []Step         `json:"steps"`
	FinalMetrics  *FinalMetrics  `json:"final_metrics,omitempty"`
	Extra         map[string]any `json:"extra"`
}

// Agent identifies the agent that produced the trajectory.
type Agent struct {
	Name      string         `json:"name"`
	Version   string         `json:"version"`
	ModelName string         `json:"model_name"`
	Extra     map[string]any `json:"extra"`
}

// Step is a single step in the trajectory.
type Step struct {
	StepID           int            `json:"step_id"`
	Source           string         `json:"source"`
	Message          string         `json:"message,omitempty"`
	Timestamp        string         `json:"timestamp,omitempty"`
	ModelName        string         `json:"model_name,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall     `json:"tool_calls,omitempty"`
	Observation      *Observation   `json:"observation,omitempty"`
	Metrics          *StepMetrics   `json:"metrics,omitempty"`
	Extra            map[string]any `json:"extra"`
}

// ToolCall records a single tool invocation.
type ToolCall struct {
	ToolCallID   string         `json:"tool_call_id"`
	FunctionName string         `json:"function_name"`
	Arguments    map[string]any `json:"arguments,omitempty"`
}

// Observation holds the tool results attached to a step.
type Observation struct {
	Results []ObservationResult `json:"results"`
}

// ObservationResult is a single tool result within an observation.
type ObservationResult struct {
	SourceCallID string `json:"source_call_id,omitempty"`
	Content      string `json:"content"`
}

// StepMetrics captures per-step token usage.
type StepMetrics struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	CachedTokens     int            `json:"cached_tokens"`
	Extra            map[string]any `json:"extra,omitempty"`
}

// FinalMetrics summarizes totals across all steps.
type FinalMetrics struct {
	TotalPromptTokens     int            `json:"total_prompt_tokens"`
	TotalCompletionTokens int            `json:"total_completion_tokens"`
	TotalCachedTokens     int            `json:"total_cached_tokens"`
	TotalSteps            int            `json:"total_steps"`
	Extra                 map[string]any `json:"extra,omitempty"`
}

// Convert converts a serf transcript (header + entries) into an ATIF v1.7 trajectory.
func Convert(header transcript.Header, entries []transcript.Entry) Trajectory {
	version := header.BuildVersion
	if version == "" {
		version = "unknown"
	}

	traj := Trajectory{
		SchemaVersion: "ATIF-v1.7",
		SessionID:     header.SessionID,
		Agent: Agent{
			Name:      "serf",
			Version:   version,
			ModelName: header.Model,
			Extra:     map[string]any{"profile_id": header.ProfileID},
		},
		Extra: buildRootExtra(header),
	}

	var steps []Step
	stepID := 1
	var totalPrompt, totalCompletion, totalCached int

	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		turn := entry.Turn

		switch turn.Kind {
		case schema.TurnUserInput:
			step := Step{
				StepID:    stepID,
				Source:    "user",
				Message:   turn.Message.Text(),
				Timestamp: formatTimestamp(turn),
				Extra:     map[string]any{},
			}
			steps = append(steps, step)
			stepID++

		case schema.TurnAssistant:
			step := convertAssistantTurn(turn, stepID)

			// Look ahead: if the next entry is TOOL_RESULTS, merge it as observation.
			if i+1 < len(entries) && entries[i+1].Turn.Kind == schema.TurnToolResults {
				i++
				obs, errMap, durMap := convertToolResults(entries[i].Turn)
				step.Observation = obs
				if len(errMap) > 0 {
					step.Extra["tool_errors"] = errMap
				}
				if len(durMap) > 0 {
					step.Extra["tool_durations_ms"] = durMap
				}
			}

			if step.Metrics != nil {
				totalPrompt += step.Metrics.PromptTokens
				totalCompletion += step.Metrics.CompletionTokens
				totalCached += step.Metrics.CachedTokens
			}

			steps = append(steps, step)
			stepID++

		case schema.TurnSteering, schema.TurnSystem:
			step := Step{
				StepID:    stepID,
				Source:    "system",
				Message:   turn.Message.Text(),
				Timestamp: formatTimestamp(turn),
				Extra:     map[string]any{},
			}
			steps = append(steps, step)
			stepID++

		case schema.TurnCheckpoint:
			step := Step{
				StepID:    stepID,
				Source:    "system",
				Message:   turn.Message.Text(),
				Timestamp: formatTimestamp(turn),
				Extra:     map[string]any{"serf_kind": "checkpoint"},
			}
			steps = append(steps, step)
			stepID++

		case schema.TurnSummary:
			step := Step{
				StepID:    stepID,
				Source:    "system",
				Message:   turn.Message.Text(),
				Timestamp: formatTimestamp(turn),
				Extra:     map[string]any{"serf_kind": "summary"},
			}
			steps = append(steps, step)
			stepID++

		case schema.TurnToolResults:
			// Orphaned TOOL_RESULTS (not preceded by ASSISTANT). ATIF forbids an
			// observation on a non-agent step and requires every observation
			// source_call_id to reference a tool_call in the same step. These
			// results have no originating tool_call in this trajectory, so emit
			// them on an agent step with the source_call_id nulled and preserve
			// the original ids in extra for traceability.
			obs, errMap, durMap := convertToolResults(turn)
			extra := map[string]any{"serf_kind": "orphaned_tool_results"}
			if obs != nil {
				var origIDs []string
				for i := range obs.Results {
					if obs.Results[i].SourceCallID != "" {
						origIDs = append(origIDs, obs.Results[i].SourceCallID)
						obs.Results[i].SourceCallID = ""
					}
				}
				if len(origIDs) > 0 {
					extra["orphaned_source_call_ids"] = origIDs
				}
			}
			if len(errMap) > 0 {
				extra["tool_errors"] = errMap
			}
			if len(durMap) > 0 {
				extra["tool_durations_ms"] = durMap
			}
			step := Step{
				StepID:      stepID,
				Source:      "agent",
				Timestamp:   formatTimestamp(turn),
				Observation: obs,
				Extra:       extra,
			}
			steps = append(steps, step)
			stepID++
		}
	}

	traj.Steps = steps
	traj.FinalMetrics = &FinalMetrics{
		TotalPromptTokens:     totalPrompt,
		TotalCompletionTokens: totalCompletion,
		TotalCachedTokens:     totalCached,
		TotalSteps:            len(steps),
	}

	return traj
}

// convertAssistantTurn extracts text, tool calls, thinking, and metadata from an assistant turn.
func convertAssistantTurn(turn schema.Turn, stepID int) Step {
	step := Step{
		StepID:    stepID,
		Source:    "agent",
		Timestamp: formatTimestamp(turn),
		Extra:     map[string]any{},
	}

	var textParts []byte
	var toolCalls []ToolCall
	var reasoningParts []byte
	var phases []string

	for _, part := range turn.Message.Content {
		switch part.Kind {
		case llm.ContentText:
			if len(textParts) > 0 {
				textParts = append(textParts, '\n')
			}
			textParts = append(textParts, part.Text...)
			if part.Phase != "" {
				phases = append(phases, part.Phase)
			}

		case llm.ContentToolCall:
			if part.ToolCall != nil {
				tc := ToolCall{
					ToolCallID:   part.ToolCall.ID,
					FunctionName: part.ToolCall.Name,
				}
				if len(part.ToolCall.Arguments) > 0 {
					var args map[string]any
					if json.Unmarshal(part.ToolCall.Arguments, &args) == nil {
						tc.Arguments = args
					}
				}
				toolCalls = append(toolCalls, tc)
			}

		case llm.ContentThinking:
			if part.Thinking != nil {
				if part.Thinking.Redacted {
					step.Extra["has_redacted_thinking"] = true
				} else {
					if len(reasoningParts) > 0 {
						reasoningParts = append(reasoningParts, '\n')
					}
					reasoningParts = append(reasoningParts, part.Thinking.Text...)
				}
				if part.Thinking.Signature != "" {
					step.Extra["thinking_signature"] = part.Thinking.Signature
				}
			}

		case llm.ContentRedThinking:
			step.Extra["has_redacted_thinking"] = true

		case llm.ContentWebSearch:
			ws := map[string]any{}
			if part.WebSearch != nil {
				if part.WebSearch.Query != "" {
					ws["query"] = part.WebSearch.Query
				}
				if len(part.WebSearch.Raw) > 0 {
					ws["raw"] = part.WebSearch.Raw
				}
			}
			existing, _ := step.Extra["web_searches"].([]any)
			step.Extra["web_searches"] = append(existing, ws)
		}
	}

	step.Message = string(textParts)
	step.ReasoningContent = string(reasoningParts)
	if len(toolCalls) > 0 {
		step.ToolCalls = toolCalls
	}
	if len(phases) > 0 {
		step.Extra["phases"] = phases
	}
	if turn.ResponseID != "" {
		step.Extra["response_id"] = turn.ResponseID
	}

	// Metrics from usage.
	if turn.Usage.InputTokens > 0 || turn.Usage.OutputTokens > 0 {
		m := &StepMetrics{
			PromptTokens:     turn.Usage.InputTokens,
			CompletionTokens: turn.Usage.OutputTokens,
		}
		if turn.Usage.CacheReadTokens != nil {
			m.CachedTokens = *turn.Usage.CacheReadTokens
		}
		extra := map[string]any{}
		if turn.Usage.ReasoningTokens != nil {
			extra["reasoning_tokens"] = *turn.Usage.ReasoningTokens
		}
		if turn.Usage.ReasoningTokensEstimated != nil {
			extra["reasoning_tokens_estimated"] = *turn.Usage.ReasoningTokensEstimated
		}
		if turn.Usage.CacheWriteTokens != nil {
			extra["cache_write_tokens"] = *turn.Usage.CacheWriteTokens
		}
		if turn.Usage.CacheWrite1hTokens != nil {
			extra["cache_write_1h_tokens"] = *turn.Usage.CacheWrite1hTokens
		}
		if len(turn.Usage.Raw) > 0 {
			extra["raw_usage"] = turn.Usage.Raw
		}
		if len(extra) > 0 {
			m.Extra = extra
		}
		step.Metrics = m
	}

	return step
}

// convertToolResults extracts observation results, error flags, and durations from a tool results turn.
func convertToolResults(turn schema.Turn) (*Observation, map[string]bool, map[string]int64) {
	var results []ObservationResult
	errMap := map[string]bool{}
	durMap := map[string]int64{}

	for _, part := range turn.Message.Content {
		if part.Kind != llm.ContentToolResult || part.ToolResult == nil {
			continue
		}
		tr := part.ToolResult
		results = append(results, ObservationResult{
			SourceCallID: tr.ToolCallID,
			Content:      fmt.Sprintf("%v", tr.Content),
		})
		if tr.IsError {
			errMap[tr.ToolCallID] = true
		}
		if tr.DurationMS > 0 {
			durMap[tr.ToolCallID] = tr.DurationMS
		}
	}

	if len(results) == 0 {
		return nil, nil, nil
	}
	return &Observation{Results: results}, errMap, durMap
}

// formatTimestamp formats a turn's timestamp as ISO 8601 UTC.
func formatTimestamp(turn schema.Turn) string {
	if turn.Timestamp.IsZero() {
		return ""
	}
	return turn.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
}

// buildRootExtra populates the trajectory-level extra map from the header.
func buildRootExtra(header transcript.Header) map[string]any {
	extra := map[string]any{}
	if header.WorkingDir != "" {
		extra["working_dir"] = header.WorkingDir
	}
	if header.ParentSessionID != "" {
		extra["parent_session_id"] = header.ParentSessionID
	}
	if header.ParentToolCallID != "" {
		extra["parent_tool_call_id"] = header.ParentToolCallID
	}
	if header.Depth > 0 {
		extra["depth"] = header.Depth
	}
	if header.Task != "" {
		extra["task"] = header.Task
	}
	if header.SystemPrompt != "" {
		extra["system_prompt"] = header.SystemPrompt
	}
	return extra
}
