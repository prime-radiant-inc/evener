package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/serf/llm"
)

// ContextManager tracks cumulative token usage and applies progressive
// compaction layers to conversation history as context fills up.
type ContextManager struct {
	profile  ProviderProfile
	client   *llm.Client
	cumUsage llm.Usage
	mu       sync.Mutex

	// Token measurement from the last API response. When available, used instead
	// of char/4 for the bulk of history. Reset to 0 after compaction.
	lastInputTokens     int // exact input token count from last API response
	historyLenAtMeasure int // number of turns when lastInputTokens was recorded

	// Thresholds are fractions (0.0–1.0) of the context window.
	ObservationMaskThreshold float64
	ThinkingClearThreshold   float64
	CheckpointThreshold      float64
	SummarizeThreshold       float64

	PreserveRecentTurns int

	// OnCompactionTurn is called for each compaction turn created (CHECKPOINT or SUMMARY).
	// Set by the session before ManageContext to record compaction turns in the transcript.
	OnCompactionTurn func(Turn)
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

// RecordInputTokens stores the exact input token count from an API response,
// along with the history length at that point. This enables accurate pressure
// calculation for subsequent turns without relying on the char/4 heuristic.
func (cm *ContextManager) RecordInputTokens(tokens int, historyLen int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
}

// estimatePressure calculates what fraction of the context window is in use.
// Uses actual API-reported token counts when available, falling back to char/4.
func (cm *ContextManager) estimatePressure(history []Turn, sysPromptChars int) float64 {
	cw := cm.profile.ContextWindowSize()
	if cw <= 0 {
		return 0
	}

	cm.mu.Lock()
	lastTokens := cm.lastInputTokens
	measuredLen := cm.historyLenAtMeasure
	cm.mu.Unlock()

	var totalTokens int
	if lastTokens > 0 && measuredLen <= len(history) {
		// Use the known token count as baseline, then estimate only new turns.
		newTurns := history[measuredLen:]
		totalTokens = lastTokens + EstimateTokens(newTurns)
	} else {
		// Fall back to char/4 for everything.
		totalTokens = EstimateTokens(history) + sysPromptChars/4
	}

	return float64(totalTokens) / float64(cw)
}

// EstimatePressure returns the estimated fraction of context window in use.
func (cm *ContextManager) EstimatePressure(history []Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
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
	emitFn func(EventKind, any),
) {
	cw := cm.profile.ContextWindowSize()
	if cw <= 0 {
		return
	}

	pressure := func() float64 {
		return cm.estimatePressure(*history, sysPromptChars)
	}

	p := pressure()
	compacted := false

	// Invalidate API token measurement before running any layer so that
	// between-layer pressure() calls use char/4 (which reflects in-place
	// mutations). Without this, stale lastInputTokens causes all layers
	// to cascade in a single pass.
	if p >= cm.ObservationMaskThreshold {
		cm.mu.Lock()
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		cm.mu.Unlock()
	}

	// Layer 1: Observation masking at ≥60%.
	if p >= cm.ObservationMaskThreshold {
		before := EstimateTokens(*history)
		maskObservations(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "observation_mask",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = pressure()
	}

	// Layer 2: Thinking clearing at ≥70%.
	if p >= cm.ThinkingClearThreshold {
		before := EstimateTokens(*history)
		clearThinking(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "thinking_clear",
			TurnsBefore:     len(*history),
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		compacted = true
		p = pressure()
	}

	// Layer 3: Deterministic checkpoint at ≥80%.
	if p >= cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		*history = checkpoint(*history, cm.PreserveRecentTurns)
		after := EstimateTokens(*history)
		emitFn(EventContextCompaction, ContextCompactionData{
			Layer:           "checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == TurnCheckpoint {
			cm.OnCompactionTurn((*history)[0])
		}
		compacted = true
		p = pressure()
	}

	// Layer 4: LLM summarization at ≥90%.
	if p >= cm.SummarizeThreshold && cm.client != nil {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		result, err := cm.summarizeWithLLM(ctx, *history, cm.PreserveRecentTurns)
		if err != nil {
			// On error, emit warning but continue with current history.
			emitFn(EventWarning, WarningData{
				Message: "LLM summarization failed: " + err.Error(),
			})
		} else {
			*history = result
			after := EstimateTokens(*history)
			emitFn(EventContextCompaction, ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == TurnSummary {
				cm.OnCompactionTurn((*history)[0])
			}
			compacted = true
		}
	}

	// Reset token measurement after compaction since history content changed.
	if compacted {
		cm.mu.Lock()
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		cm.mu.Unlock()
	}
}

// --- Observation masking (Layer 1) ---

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate results, already-masked results, recent turns,
// and results where the summary would be longer than the original.
func maskObservations(history []Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return
	}
	for i := 0; i < cutoff; i++ {
		t := &history[i]
		if t.Kind != TurnTool && t.Kind != TurnToolResults {
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

			summary := summarizeToolResult(tr.Name, content, args)
			// Skip masking if the summary is not shorter than the original.
			if len(summary) >= len(content) {
				continue
			}
			tr.Content = summary
		}
	}
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
func summarizeToolResult(toolName string, content any, args json.RawMessage) string {
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
		return fmt.Sprintf("[edit_file: %s → OK]", path)

	case "apply_patch":
		return "[apply_patch → OK]"

	case "write_file":
		path := getArg("file_path")
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
func clearThinking(history []Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return
	}

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
			p.Thinking.Text = fmt.Sprintf("[thinking: %d chars]", oldLen)
		}
	}
}

// --- Deterministic checkpoint (Layer 3) ---

// checkpoint replaces old history with a structured state snapshot.
// Returns a new history slice: [checkpoint_message, ...preserved_recent_turns].
func checkpoint(history []Turn, preserveRecent int) []Turn {
	if len(history) <= preserveRecent {
		return history
	}

	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history
	}

	// Extract original task (first user input that isn't a previous checkpoint/summary).
	// If all user inputs are checkpoints, extract the task from the checkpoint text.
	originalTask := ""
	for _, t := range history[:cutoff] {
		// Handle typed compaction turns (new TurnKinds).
		if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
			text := t.Message.Text()
			if idx := strings.Index(text, "Original task: "); idx >= 0 {
				rest := text[idx+len("Original task: "):]
				if nl := strings.Index(rest, "\n"); nl >= 0 {
					originalTask = rest[:nl]
				} else {
					originalTask = rest
				}
			}
			continue
		}
		if t.Kind != TurnUserInput {
			continue
		}
		// Backward compat: handle old TurnUserInput checkpoints/summaries by text prefix.
		text := t.Message.Text()
		if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") {
			if idx := strings.Index(text, "Original task: "); idx >= 0 {
				rest := text[idx+len("Original task: "):]
				if nl := strings.Index(rest, "\n"); nl >= 0 {
					originalTask = rest[:nl]
				} else {
					originalTask = rest
				}
			}
			continue
		}
		if strings.HasPrefix(text, "[CONTEXT SUMMARY]") {
			continue
		}
		originalTask = text
		break
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
			if p.Kind == llm.ContentWebSearch {
				toolCounts["web_search"]++
				continue
			}
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
					// Find the matching tool result by ToolCallID, within cutoff.
					exitCode := "?"
					for j := i + 1; j < cutoff; j++ {
						if history[j].Kind != TurnTool && history[j].Kind != TurnToolResults {
							continue
						}
						content := findToolResultByCallID(history[j], p.ToolCall.ID)
						if content != "" {
							exitCode = parseExitCode(content)
							break
						}
					}
					lastShellResults = append(lastShellResults, fmt.Sprintf("  %q → exit %s", cmdStr, exitCode))
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
		toolNames := make([]string, 0, len(toolCounts))
		for name := range toolCounts {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for i, name := range toolNames {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("%d %s", toolCounts[name], name))
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

	checkpointTurn := NewTurn(TurnCheckpoint, llm.User(b.String()))

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result
}

// findToolResultByCallID finds a tool result in a TurnTool by its ToolCallID
// and returns the string content, or "" if not found.
func findToolResultByCallID(t Turn, toolCallID string) string {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.ToolCallID == toolCallID {
			if s, ok := p.ToolResult.Content.(string); ok {
				return s
			}
		}
	}
	return ""
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
	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history, nil
	}

	// Cap the history text to avoid exceeding the cheap model's context window.
	// ~50k chars ≈ 12.5k tokens, leaving room for the instruction prefix and response.
	const maxHistoryChars = 50_000

	truncText := func(s string, max int) string {
		if len(s) > max {
			return s[:max] + "..."
		}
		return s
	}

	var b strings.Builder
	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case TurnUserInput:
			b.WriteString("User: " + truncText(t.Message.Text(), 500) + "\n")
		case TurnCheckpoint, TurnSummary:
			b.WriteString("Previous compaction: " + truncText(t.Message.Text(), 500) + "\n")
		case TurnAssistant:
			b.WriteString("Assistant: " + truncText(t.Message.Text(), 500) + "\n")
		case TurnTool, TurnToolResults:
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
		if b.Len() > maxHistoryChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
	}

	prefix := "You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary for another LLM that will resume this task.\n\n" +
		"Include:\n" +
		"- The original task/goal\n" +
		"- Current progress: what has been accomplished so far (specific files modified, specific changes made)\n" +
		"- Key decisions made and why\n" +
		"- What remains to be done (clear, actionable next steps)\n" +
		"- Any critical data, file paths, variable names, or error messages needed to continue\n" +
		"- Important constraints or user preferences discovered\n\n" +
		"Be concise and structured. Focus on helping the next LLM seamlessly continue WITHOUT re-reading files or re-discovering what was already done.\n\n"
	prompt := prefix + b.String()

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
	summaryTurn := NewTurn(TurnSummary, llm.User(summaryText))

	result := make([]Turn, 0, 1+preserveRecent)
	result = append(result, summaryTurn)
	result = append(result, history[cutoff:]...)
	return result, nil
}

// safeCutoff adjusts a cutoff index so the preserved turns don't start with a
// TurnTool or TurnSteering, which would produce invalid message ordering.
// TurnTool without a preceding assistant tool_call is invalid. TurnSteering
// after a checkpoint/summary (both user-role) produces consecutive user messages
// that some APIs reject.
// Returns -1 if no safe position exists; callers should skip compaction.
func safeCutoff(history []Turn, cutoff int) int {
	for cutoff > 0 && cutoff < len(history) {
		k := history[cutoff].Kind
		if k == TurnTool || k == TurnToolResults || k == TurnSteering {
			cutoff--
			continue
		}
		break
	}
	if cutoff <= 0 {
		return -1
	}
	return cutoff
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
	// Look for "exit_code=N" (raw shell output) or "exit N" (masked summary format).
	for _, line := range strings.Split(shellOutput, "\n") {
		// Raw format: exit_code=0
		if idx := strings.Index(line, "exit_code="); idx >= 0 {
			rest := line[idx+len("exit_code="):]
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
		// Masked format: → exit 0] or "exit 0"
		if idx := strings.Index(line, "exit "); idx >= 0 {
			rest := line[idx+len("exit "):]
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
