package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// --- MaybeCompact orchestrator ---

// MaybeCompact checks context pressure and applies compaction layers progressively.
// It modifies history in place and emits events for each layer applied.
// Called before each LLM request.
func (cm *ContextManager) MaybeCompact(
	ctx context.Context,
	history *[]Turn,
	sysPromptChars int,
	emitFn func(EventKind, map[string]any),
) error {
	cw := cm.profile.ContextWindowSize()
	if cw <= 0 {
		return nil
	}

	pressure := func() float64 {
		histTokens := EstimateTokens(*history)
		sysTokens := sysPromptChars / 4
		return float64(histTokens+sysTokens) / float64(cw)
	}

	p := pressure()

	// Layer 1: Observation masking at ≥60%.
	if p >= cm.ObservationMaskThreshold {
		before := EstimateTokens(*history)
		maskObservations(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, map[string]any{
			"layer":            "observation_mask",
			"turns_before":     len(*history),
			"turns_after":      len(*history),
			"est_tokens_before": before,
			"est_tokens_after":  after,
		})
		p = pressure()
	}

	// Layer 2: Thinking clearing at ≥70%.
	if p >= cm.ThinkingClearThreshold {
		before := EstimateTokens(*history)
		clearThinking(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, map[string]any{
			"layer":            "thinking_clear",
			"turns_before":     len(*history),
			"turns_after":      len(*history),
			"est_tokens_before": before,
			"est_tokens_after":  after,
		})
		p = pressure()
	}

	// Layer 3: Deterministic checkpoint at ≥80%.
	if p >= cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		*history = checkpoint(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, map[string]any{
			"layer":            "checkpoint",
			"turns_before":     turnsBefore,
			"turns_after":      len(*history),
			"est_tokens_before": before,
			"est_tokens_after":  after,
		})
		p = pressure()
	}

	// Layer 4: LLM summarization at ≥90%.
	if p >= cm.SummarizeThreshold && cm.client != nil {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		result, err := cm.summarizeWithLLM(ctx, *history, cm.PreserveRecentTurns)
		if err != nil {
			// On error, emit warning but continue with current history.
			emitFn(EventWarning, map[string]any{
				"message": "LLM summarization failed: " + err.Error(),
			})
		} else {
			*history = result
			after := EstimateTokens(*history)
			emitFn(EventContextCompaction, map[string]any{
				"layer":            "summarize",
				"turns_before":     turnsBefore,
				"turns_after":      len(*history),
				"est_tokens_before": before,
				"est_tokens_after":  after,
			})
		}
	}

	return nil
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

// --- Thinking clearing (Layer 2) ---

// clearThinking removes thinking text from old assistant turns, replacing it
// with a placeholder. Redacted thinking blocks are left untouched.
// Returns estimated tokens freed.
func clearThinking(history []Turn, preserveRecent int) int {
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
		if t.Kind != TurnAssistant {
			continue
		}
		for j := range t.Message.Content {
			p := &t.Message.Content[j]
			if p.Kind != llm.ContentThinking || p.Thinking == nil {
				continue
			}
			// Skip redacted thinking blocks.
			if p.Thinking.Redacted {
				continue
			}
			// Skip already-cleared thinking.
			if strings.HasPrefix(p.Thinking.Text, "[thinking:") {
				continue
			}
			oldLen := len(p.Thinking.Text)
			if oldLen == 0 {
				continue
			}
			placeholder := fmt.Sprintf("[thinking: %d chars]", oldLen)
			freed += (oldLen - len(placeholder)) / 4
			p.Thinking.Text = placeholder
		}
	}
	return freed
}

// --- Deterministic checkpoint (Layer 3) ---

// checkpoint replaces old history with a structured state snapshot.
// Returns a new history slice: [checkpoint_message, ...preserved_recent_turns].
func checkpoint(history []Turn, preserveRecent int) []Turn {
	if len(history) <= preserveRecent {
		return history
	}

	cutoff := len(history) - preserveRecent

	// Extract original task (first user input).
	originalTask := ""
	for _, t := range history {
		if t.Kind == TurnUserInput {
			originalTask = t.Message.Text()
			break
		}
	}
	if len(originalTask) > 500 {
		originalTask = originalTask[:500] + "..."
	}

	// Collect modified files from edit_file/write_file/apply_patch tool calls.
	modifiedFiles := map[string]bool{}
	toolCounts := map[string]int{}
	var lastShellResults []string

	for i := 0; i < cutoff; i++ {
		t := history[i]
		if t.Kind != TurnAssistant {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind != llm.ContentToolCall || p.ToolCall == nil {
				continue
			}
			name := p.ToolCall.Name
			toolCounts[name]++

			var args map[string]any
			if len(p.ToolCall.Arguments) > 0 {
				_ = json.Unmarshal(p.ToolCall.Arguments, &args)
			}

			switch name {
			case "edit_file", "write_file":
				if path, ok := args["file_path"]; ok {
					modifiedFiles[fmt.Sprint(path)] = true
				}
			case "apply_patch":
				// Extract file paths from patch content (lines with "*** Update File:" etc).
				if patch, ok := args["patch"]; ok {
					for _, line := range strings.Split(fmt.Sprint(patch), "\n") {
						line = strings.TrimSpace(line)
						for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
							if strings.HasPrefix(line, prefix) {
								modifiedFiles[strings.TrimPrefix(line, prefix)] = true
							}
						}
					}
				}
			case "shell":
				// Track last few shell commands and their exit codes.
				if cmd, ok := args["command"]; ok {
					cmdStr := fmt.Sprint(cmd)
					if len(cmdStr) > 60 {
						cmdStr = cmdStr[:60] + "..."
					}
					// Find the matching tool result to get exit code.
					for j := i + 1; j < len(history) && j < i+3; j++ {
						if history[j].Kind == TurnTool {
							content := toolResultContent(history[j])
							exitCode := parseExitCode(content)
							lastShellResults = append(lastShellResults, fmt.Sprintf("  %q → exit %s", cmdStr, exitCode))
							break
						}
					}
				}
			}
		}
	}

	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT]\n")
	b.WriteString(fmt.Sprintf("Original task: %s\n", originalTask))

	if len(modifiedFiles) > 0 {
		files := make([]string, 0, len(modifiedFiles))
		for f := range modifiedFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		b.WriteString(fmt.Sprintf("Files modified: %s\n", strings.Join(files, ", ")))
	}

	if total := sumCounts(toolCounts); total > 0 {
		b.WriteString(fmt.Sprintf("Actions taken: %d tool calls (", total))
		first := true
		for name, count := range toolCounts {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%d %s", count, name))
			first = false
		}
		b.WriteString(")\n")
	}

	// Include only the last 3 shell results.
	if len(lastShellResults) > 0 {
		start := 0
		if len(lastShellResults) > 3 {
			start = len(lastShellResults) - 3
		}
		b.WriteString("Last shell results:\n")
		for _, r := range lastShellResults[start:] {
			b.WriteString(r + "\n")
		}
	}

	b.WriteString("[END CHECKPOINT]\n")

	checkpointTurn := Turn{
		Kind:    TurnUserInput,
		Message: llm.User(b.String()),
	}

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result
}

// toolResultContent extracts string content from a TurnTool.
func toolResultContent(t Turn) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
}

func sumCounts(m map[string]int) int {
	total := 0
	for _, v := range m {
		total += v
	}
	return total
}

// --- LLM summarization (Layer 4) ---

// summarizeWithLLM calls the cheap model to generate a narrative summary of
// old history. Replaces old history with a single user message containing the
// summary, preserving the most recent turns.
func (cm *ContextManager) summarizeWithLLM(ctx context.Context, history []Turn, preserveRecent int) ([]Turn, error) {
	if len(history) <= preserveRecent {
		return history, nil
	}
	cutoff := len(history) - preserveRecent

	var b strings.Builder
	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case TurnUserInput:
			b.WriteString("User: " + t.Message.Text() + "\n")
		case TurnAssistant:
			b.WriteString("Assistant: " + t.Message.Text() + "\n")
		case TurnTool:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					if len(content) > 200 {
						content = content[:200] + "..."
					}
					b.WriteString(fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, content))
				}
			}
		case TurnSteering:
			b.WriteString("System: " + t.Message.Text() + "\n")
		}
	}

	prompt := "Summarize this coding agent conversation history concisely. " +
		"Focus on: what task was requested, what actions were taken, what files were modified, " +
		"what the current state is, and any errors encountered. Be brief but complete.\n\n" +
		b.String()

	req := llm.Request{
		Model:    cm.profile.CheapModel(),
		Provider: cm.profile.ID(),
		Messages: []llm.Message{llm.User(prompt)},
	}

	resp, err := cm.client.Complete(ctx, req)
	if err != nil {
		return nil, err
	}

	summaryText := "[CONTEXT SUMMARY]\n" + resp.Text() + "\n[END SUMMARY]"
	summaryTurn := Turn{
		Kind:    TurnUserInput,
		Message: llm.User(summaryText),
	}

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, summaryTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}

// --- Helper functions ---

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
