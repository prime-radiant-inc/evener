package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"primeradiant.com/serf/internal/llm"
)

// ContextManager tracks cumulative token usage and applies progressive
// compaction layers to conversation history as context fills up.
type ContextManager struct {
	profile  ProviderProfile
	client   *llm.Client
	cumUsage llm.Usage
	mu       sync.Mutex

	// Thresholds are fractions (0.0–1.0) of the context window.
	ObservationMaskThreshold float64
	ThinkingClearThreshold   float64
	CheckpointThreshold      float64
	SummarizeThreshold       float64

	PreserveRecentTurns int
}

// NewContextManager creates a ContextManager with default thresholds.
func NewContextManager(profile ProviderProfile, client *llm.Client) *ContextManager {
	return &ContextManager{
		profile:                  profile,
		client:                   client,
		ObservationMaskThreshold: 0.60,
		ThinkingClearThreshold:   0.70,
		CheckpointThreshold:      0.80,
		SummarizeThreshold:       0.90,
		PreserveRecentTurns:      6,
	}
}

// AddUsage records token usage from a completed LLM call.
func (cm *ContextManager) AddUsage(u llm.Usage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cumUsage = cm.cumUsage.Add(u)
}

// CumulativeUsage returns accumulated session totals.
func (cm *ContextManager) CumulativeUsage() llm.Usage {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.cumUsage
}

// EstimateTokens estimates token count for turns using the char/4 heuristic.
func EstimateTokens(turns []Turn) int {
	chars := 0
	for _, t := range turns {
		chars += messageCharCount(t.Message)
	}
	return chars / 4
}

// --- Observation masking (Layer 1) ---

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate results, already-masked results, and recent turns.
// Returns estimated tokens freed.
func maskObservations(history []Turn, preserveRecent int) int {
	if len(history) == 0 {
		return 0
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return 0
	}

	freed := 0
	for i := 0; i < cutoff; i++ {
		t := &history[i]
		if t.Kind != TurnTool {
			continue
		}
		for j := range t.Message.Content {
			p := &t.Message.Content[j]
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			tr := p.ToolResult

			// Never mask error results.
			if tr.IsError {
				continue
			}
			// Never mask communicate results.
			if tr.Name == "communicate" {
				continue
			}

			content, ok := tr.Content.(string)
			if !ok {
				continue
			}
			// Skip already-masked results (start with "[").
			if strings.HasPrefix(content, "[") && strings.HasSuffix(strings.TrimSpace(content), "]") {
				continue
			}

			// Find the tool call arguments from the preceding assistant turn.
			args := findToolCallArgs(history[:i], tr.ToolCallID)

			summary := summarizeToolResult(tr.Name, content, tr.IsError, args)
			oldLen := len(content)
			newLen := len(summary)
			freed += (oldLen - newLen) / 4 // may be negative for tiny results; net effect is still positive
			tr.Content = summary
		}
	}
	return freed
}

// findToolCallArgs looks backward from the tool result to find the matching
// assistant tool call and return its arguments.
func findToolCallArgs(history []Turn, toolCallID string) json.RawMessage {
	for i := len(history) - 1; i >= 0; i-- {
		t := history[i]
		if t.Kind != TurnAssistant {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.ID == toolCallID {
				return p.ToolCall.Arguments
			}
		}
	}
	return nil
}

// summarizeToolResult generates a one-line summary for a tool result.
func summarizeToolResult(toolName string, content any, isError bool, args json.RawMessage) string {
	contentStr := fmt.Sprint(content)
	var argsMap map[string]any
	if len(args) > 0 {
		_ = json.Unmarshal(args, &argsMap)
	}

	getArg := func(key string) string {
		if argsMap == nil {
			return ""
		}
		if v, ok := argsMap[key]; ok {
			return fmt.Sprint(v)
		}
		return ""
	}

	switch toolName {
	case "read_file":
		path := getArg("file_path")
		lines := countLines(contentStr)
		return fmt.Sprintf("[read_file: %s, %d lines]", path, lines)

	case "read_many_files":
		path := getArg("file_paths")
		return fmt.Sprintf("[read_many_files: %s → %d chars]", path, len(contentStr))

	case "shell":
		cmd := getArg("command")
		if len(cmd) > 60 {
			cmd = cmd[:60] + "..."
		}
		exitCode := parseExitCode(contentStr)
		return fmt.Sprintf("[shell: %q → exit %s]", cmd, exitCode)

	case "grep":
		pattern := getArg("pattern")
		matches := countLines(contentStr)
		return fmt.Sprintf("[grep: %q → %d matches]", pattern, matches)

	case "glob":
		pattern := getArg("pattern")
		files := countNonEmptyLines(contentStr)
		return fmt.Sprintf("[glob: %q → %d files]", pattern, files)

	case "edit_file":
		path := getArg("file_path")
		if isError {
			return fmt.Sprintf("[edit_file: %s → error]", path)
		}
		return fmt.Sprintf("[edit_file: %s → OK]", path)

	case "apply_patch":
		if isError {
			return "[apply_patch → error]"
		}
		return "[apply_patch → OK]"

	case "write_file":
		path := getArg("file_path")
		if isError {
			return fmt.Sprintf("[write_file: %s → error]", path)
		}
		return fmt.Sprintf("[write_file: %s → OK]", path)

	case "web_fetch":
		url := getArg("url")
		return fmt.Sprintf("[web_fetch: %s → %d chars]", url, len(contentStr))

	case "spawn_agent":
		// Try to extract agent_id from the JSON output.
		agentID := extractJSONField(contentStr, "agent_id")
		if agentID != "" {
			return fmt.Sprintf("[spawn_agent: %s]", agentID)
		}
		return fmt.Sprintf("[spawn_agent: %d chars]", len(contentStr))

	case "task_list":
		action := getArg("action")
		tasks := countJSONArrayElements(contentStr)
		return fmt.Sprintf("[task_list: %s → %d tasks]", action, tasks)

	case "use_skill":
		name := getArg("skill_name")
		return fmt.Sprintf("[use_skill: %s → %d chars]", name, len(contentStr))

	case "communicate":
		// Should never reach here (masked in caller), but be safe.
		return fmt.Sprintf("[communicate: %d chars]", len(contentStr))

	default:
		return fmt.Sprintf("[%s: %d chars]", toolName, len(contentStr))
	}
}

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func countNonEmptyLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func parseExitCode(shellOutput string) string {
	// Look for "exit_code=N" in the output.
	for _, line := range strings.Split(shellOutput, "\n") {
		if idx := strings.Index(line, "exit_code="); idx >= 0 {
			rest := line[idx+len("exit_code="):]
			// Take digits up to next space.
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return "?"
}

func extractJSONField(s, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return ""
	}
	if v, ok := m[field]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func countJSONArrayElements(s string) int {
	var arr []any
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return 0
	}
	return len(arr)
}
