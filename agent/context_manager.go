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

// CompactionMeta holds session-level metadata injected into compaction summaries.
// The session populates this before each ManageContext call so that checkpoint
// and summarize have access to data outside the turn history.
type CompactionMeta struct {
	TranscriptPath  string   // path to the full session transcript JSONL
	TaskSnapshot    []Task   // current task list state (nil = no tasks)
	ActivatedSkills []string // skill names activated during this session
}

// ContextManager tracks cumulative token usage and applies progressive
// compaction layers to conversation history as context fills up.
type ContextManager struct {
	profile  ProviderProfile
	client   *llm.Client
	cumUsage llm.Usage
	mu       sync.Mutex

	// ResultToolName is the name of the user communication tool (default "communicate").
	// Used to identify communicate tool calls during compaction.
	ResultToolName string

	// Token measurement from the last API response. When available, used instead
	// of char/4 for the bulk of history. Reset to 0 after compaction.
	lastInputTokens     int // exact input token count from last API response
	historyLenAtMeasure int // number of turns when lastInputTokens was recorded

	// Thresholds are fractions (0.0–1.0) of the context window.
	// ObservationMaskThreshold and ThinkingClearThreshold are not used by the
	// default compaction path (MaybeCompact/ForceCompact) because in-place
	// modifications bust the prompt cache for all providers. They remain here
	// for experimental ContextStrategy implementations that call
	// maskObservations/clearThinking directly.
	ObservationMaskThreshold float64
	ThinkingClearThreshold   float64
	CheckpointThreshold      float64
	SummarizeThreshold       float64

	PreserveRecentTurns int

	// OnCompactionTurn is called for each compaction turn created (CHECKPOINT or SUMMARY).
	// Set by the session before ManageContext to record compaction turns in the transcript.
	OnCompactionTurn func(Turn)

	// Meta holds session-level metadata for enriching compaction summaries.
	// Set by the session before each ManageContext call.
	Meta CompactionMeta
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

func (cm *ContextManager) resultToolName() string {
	if cm.ResultToolName != "" {
		return cm.ResultToolName
	}
	return "communicate"
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

// SetProfile replaces the provider profile so that ContextWindowSize() and
// other profile-derived values stay current after a model change.
func (cm *ContextManager) SetProfile(profile ProviderProfile) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.profile = profile
}

// LastInputTokens returns the most recently recorded input token count.
func (cm *ContextManager) LastInputTokens() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.lastInputTokens
}

// Pressure returns the current context pressure as a fraction (0.0–1.0).
func (cm *ContextManager) Pressure(history []Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
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
	// mutations). Without this, stale lastInputTokens causes both layers
	// to cascade in a single pass.
	if p >= cm.CheckpointThreshold {
		cm.mu.Lock()
		cm.lastInputTokens = 0
		cm.historyLenAtMeasure = 0
		cm.mu.Unlock()
	}

	// Layer 1: Deterministic checkpoint at ≥80%.
	if p >= cm.CheckpointThreshold {
		turnsBefore := len(*history)
		before := EstimateTokens(*history)
		*history = checkpoint(*history, cm.PreserveRecentTurns, &cm.Meta, cm.resultToolName())
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

	// Layer 2: LLM summarization at ≥90%.
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

// ForceCompact runs all compaction layers unconditionally, regardless of context pressure.
// Used for user-initiated compaction (e.g. /compact command).
func (cm *ContextManager) ForceCompact(
	ctx context.Context,
	history *[]Turn,
	emitFn func(EventKind, any),
) {
	// Reset token measurement so inter-layer estimates use char/4.
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()

	// Layer 1: Deterministic checkpoint.
	turnsBefore := len(*history)
	before := EstimateTokens(*history)
	*history = checkpoint(*history, cm.PreserveRecentTurns, &cm.Meta, cm.resultToolName())
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

	// Layer 2: LLM summarization (only if client is available).
	if cm.client != nil {
		turnsBefore = len(*history)
		before = EstimateTokens(*history)
		result, err := cm.summarizeWithLLM(ctx, *history, cm.PreserveRecentTurns)
		if err != nil {
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
		}
	}

	// Reset token measurement after compaction.
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()
}

// --- Observation masking (Layer 1) ---

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate tool results, already-masked results,
// recent turns, and results where the summary would be longer than the original.
func maskObservations(history []Turn, preserveRecent int, resultToolName string) {
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
			// Never mask result tool results.
			if tr.Name == resultToolName {
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

func parseCommunicateArgs(args json.RawMessage) (kind string, message string) {
	var payload struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Output  *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if len(args) == 0 || json.Unmarshal(args, &payload) != nil {
		return "", ""
	}
	kind = strings.TrimSpace(payload.Kind)
	message = strings.TrimSpace(payload.Message)
	if message == "" && payload.Output != nil {
		message = strings.TrimSpace(payload.Output.Message)
	}
	if kind == "" && message != "" {
		// Legacy communicate calls without a kind, plus old submit_result calls,
		// were implicitly final.
		kind = "final"
	}
	return kind, message
}

func communicateKindAndMessageFromHistory(history []Turn, toolCallID string) (kind string, message string) {
	return parseCommunicateArgs(findToolCallArgs(history, toolCallID))
}

func isFinalCommunicateKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "final")
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
//
// The checkpoint is a deterministic extraction — no LLM call. It captures user
// messages verbatim, agent responses, file/tool metadata, task list, and skills.
// User messages are stored as JSON arrays for lossless round-tripping across
// repeated compactions.
func checkpoint(history []Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []Turn {
	if len(history) <= preserveRecent {
		return history
	}

	cutoff := safeCutoff(history, len(history)-preserveRecent)
	if cutoff < 0 {
		return history
	}

	// Budget: cap the checkpoint to avoid ballooning the context. The checkpoint
	// either feeds into the LLM summarizer (which replaces it) or — if the
	// summarizer is unavailable — becomes the sole history the main model sees.
	// 60k chars ≈ 15k tokens: fits comfortably in the cheap model's context
	// alongside the instruction prefix, and leaves room for preserved recent
	// turns in the main model's context.
	const maxCheckpointChars = 60_000

	// Collect: modified files, tool counts, shell results, user messages,
	// final agent responses, working notes, and activated skills.
	modifiedFiles := map[string]bool{}
	activatedSkills := map[string]bool{}
	toolCounts := map[string]int{}
	var lastShellResults []string
	var userMessages []string
	var agentResponses []string
	var workingNotes []string

	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case TurnCheckpoint, TurnSummary:
			// Extract user messages and working notes from previous compaction turns
			// so they survive across repeated compactions.
			userMessages = append(userMessages, extractCheckpointJSON(t.Message.Text(), "user_messages")...)
			workingNotes = append(workingNotes, extractCheckpointJSON(t.Message.Text(), "working_notes")...)

		case TurnUserInput:
			text := t.Message.Text()
			if text == "" {
				continue
			}
			// Old-format checkpoint/summary stored as TurnUserInput — extract
			// user messages from them just like typed compaction turns.
			if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, "[CONTEXT SUMMARY]") {
				userMessages = append(userMessages, extractCheckpointJSON(text, "user_messages")...)
				continue
			}
			userMessages = append(userMessages, text)

		case TurnTool, TurnToolResults:
			// Extract communicate(kind="final") results (agent's final responses to user).
			for _, p := range t.Message.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if p.ToolResult.Name == resultToolName {
					kind, msg := communicateKindAndMessageFromHistory(history[:i+1], p.ToolResult.ToolCallID)
					if isFinalCommunicateKind(kind) && msg != "" {
						agentResponses = append(agentResponses, msg)
					}
					continue
				}
			}

		case TurnAssistant:
			// Capture assistant analytical text as working notes.
			if text := t.Message.Text(); len(text) > 50 {
				note := text
				if len(note) > 500 {
					note = note[:500] + "..."
				}
				workingNotes = append(workingNotes, note)
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
					if cmd, ok := args["command"]; ok {
						cmdStr := fmt.Sprint(cmd)
						if len(cmdStr) > 60 {
							cmdStr = cmdStr[:60] + "..."
						}
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
				case "use_skill":
					if sn, ok := args["skill_name"]; ok {
						activatedSkills[fmt.Sprint(sn)] = true
					}
				}
			}
		}
	}

	// Build the fixed-size sections first (metadata), then fill remaining
	// budget with user messages and agent responses.
	var fixed strings.Builder

	// Transcript pointer — tells the model how to access full history.
	if meta != nil && meta.TranscriptPath != "" {
		fixed.WriteString(fmt.Sprintf("Full transcript: %s (use read_file to review earlier context if needed)\n", meta.TranscriptPath))
	}

	// Files modified.
	if len(modifiedFiles) > 0 {
		files := make([]string, 0, len(modifiedFiles))
		for f := range modifiedFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		fixed.WriteString(fmt.Sprintf("Files modified: %s\n", strings.Join(files, ", ")))
	}

	// Tool call counts.
	if total := sumCounts(toolCounts); total > 0 {
		fixed.WriteString(fmt.Sprintf("Actions taken: %d tool calls (", total))
		toolNames := make([]string, 0, len(toolCounts))
		for name := range toolCounts {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for i, name := range toolNames {
			if i > 0 {
				fixed.WriteString(", ")
			}
			fixed.WriteString(fmt.Sprintf("%d %s", toolCounts[name], name))
		}
		fixed.WriteString(")\n")
	}

	// Last 3 shell results.
	if len(lastShellResults) > 0 {
		start := 0
		if len(lastShellResults) > 3 {
			start = len(lastShellResults) - 3
		}
		fixed.WriteString("Last shell results:\n")
		for _, r := range lastShellResults[start:] {
			fixed.WriteString(r + "\n")
		}
	}

	// Task list status.
	if meta != nil && len(meta.TaskSnapshot) > 0 {
		fixed.WriteString("\nTask list:\n")
		for _, t := range meta.TaskSnapshot {
			line := fmt.Sprintf("  [%s] #%d: %s", string(t.Status), t.ID, t.Description)
			if len(t.DependsOn) > 0 {
				line += fmt.Sprintf(" (depends_on: %v)", t.DependsOn)
			}
			fixed.WriteString(line + "\n")
		}
	}

	// Activated skills.
	allSkills := map[string]bool{}
	for s := range activatedSkills {
		allSkills[s] = true
	}
	if meta != nil {
		for _, s := range meta.ActivatedSkills {
			allSkills[s] = true
		}
	}
	if len(allSkills) > 0 {
		skillNames := make([]string, 0, len(allSkills))
		for s := range allSkills {
			skillNames = append(skillNames, s)
		}
		sort.Strings(skillNames)
		fixed.WriteString(fmt.Sprintf("Activated skills: %s\n", strings.Join(skillNames, ", ")))
	}

	fixedStr := fixed.String()

	// Budget for variable content (user messages + agent responses as JSON).
	// Reserve space for [CONTEXT CHECKPOINT], [END CHECKPOINT], fixed sections,
	// and the JSON key names.
	overhead := len("[CONTEXT CHECKPOINT]\n") + len(fixedStr) + len("[END CHECKPOINT]\n") + 200
	variableBudget := maxCheckpointChars - overhead
	if variableBudget < 1000 {
		variableBudget = 1000
	}

	// Encode user messages, agent responses, and working notes as JSON for
	// lossless round-tripping. Shed oldest notes first, then oldest user
	// messages if needed to fit budget.
	userJSON := marshalJSONCompact(userMessages)
	respJSON := marshalJSONCompact(agentResponses)
	notesJSON := marshalJSONCompact(workingNotes)

	for len(userJSON)+len(respJSON)+len(notesJSON) > variableBudget {
		if len(workingNotes) > 1 {
			workingNotes = workingNotes[1:] // drop oldest note first
			notesJSON = marshalJSONCompact(workingNotes)
			continue
		}
		if len(userMessages) > 1 {
			userMessages = userMessages[1:] // then drop oldest user message
			userJSON = marshalJSONCompact(userMessages)
			continue
		}
		break
	}

	// Assemble final checkpoint.
	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT]\n")
	b.WriteString(fixedStr)
	if len(userMessages) > 0 {
		b.WriteString(fmt.Sprintf("\n<user_messages>%s</user_messages>\n", userJSON))
	}
	if len(agentResponses) > 0 {
		b.WriteString(fmt.Sprintf("\n<agent_responses>%s</agent_responses>\n", respJSON))
	}
	if len(workingNotes) > 0 {
		b.WriteString(fmt.Sprintf("\n<working_notes>%s</working_notes>\n", notesJSON))
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
	// ~80k chars ≈ 20k tokens, leaving room for the instruction prefix and response.
	const maxHistoryChars = 80_000

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
			// Preserve user messages verbatim. Cap at 5k to protect the cheap
			// model's context — normal messages are well under this.
			text := t.Message.Text()
			if len(text) > 5000 {
				text = text[:5000] + "..."
			}
			b.WriteString("User: " + text + "\n")
		case TurnCheckpoint, TurnSummary:
			b.WriteString("Previous compaction: " + truncText(t.Message.Text(), 1000) + "\n")
		case TurnAssistant:
			// Extract communicate calls so the summarizer sees how the agent
			// talked to the user, while only treating kind="final" as a submit.
			text := t.Message.Text()
			if text != "" {
				b.WriteString("Assistant: " + truncText(text, 500) + "\n")
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == cm.resultToolName() {
					kind, msg := parseCommunicateArgs(p.ToolCall.Arguments)
					if msg == "" {
						continue
					}
					switch {
					case isFinalCommunicateKind(kind):
						b.WriteString(fmt.Sprintf("Agent Final: %s\n", msg))
					case strings.EqualFold(kind, "ask"):
						b.WriteString(fmt.Sprintf("Agent Question: %s\n", msg))
					default:
						b.WriteString(fmt.Sprintf("Agent Message: %s\n", msg))
					}
				}
			}
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

	prefix := `You are performing a CONTEXT CHECKPOINT COMPACTION. This session is being continued from a previous conversation that ran out of context. Create a detailed handoff summary that another instance of yourself will use to seamlessly continue the work.

Your summary MUST include ALL of the following sections:

## User Messages
Reproduce ALL user messages verbatim. The next instance needs the full conversation context to understand the user's evolving intent across the session.

## Progress
What has been accomplished so far. Be specific about:
- Files created or modified (full paths)
- Specific changes made to each file
- Tests written or run and their results
- Commands executed and their outcomes

## Key Decisions
Important decisions made during the session and why. Include:
- Architecture or design choices
- Trade-offs considered
- User preferences or constraints discovered

## Agent Responses
Summarize key agent responses to the user, especially final answers, status updates, and any commitments made.

## Current State
Precisely what was being worked on when context ran out:
- What file was being edited
- What problem was being debugged
- What test was failing

## Pending Work
Clear, actionable next steps that remain. Be specific enough that the next instance can immediately start working.

## Analytical Findings
Specific technical discoveries made during the session:
- What algorithms, approaches, or parameter values were found to work
- What debugging insights were gained (root causes, validated hypotheses)
- What code patterns, data structures, or API behaviors were discovered
- Include specific values, numbers, and names — not vague summaries

## Critical Context
Any data, file paths, variable names, error messages, API details, or other specific information needed to continue. Focus on information that CANNOT be re-derived from reading the codebase.

Be thorough and structured. Err on the side of including too much rather than too little — lost context is expensive, extra tokens are cheap.

`
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

// extractCheckpointJSON extracts a JSON string array from a checkpoint text
// stored in XML-style tags like <user_messages>[...]</user_messages>.
// Falls back to legacy "Original task:" format for backward compatibility.
func extractCheckpointJSON(text, tag string) []string {
	// New format: <tag>[...]</tag>
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	if idx := strings.Index(text, open); idx >= 0 {
		rest := text[idx+len(open):]
		if end := strings.Index(rest, close); end >= 0 {
			var msgs []string
			if err := json.Unmarshal([]byte(rest[:end]), &msgs); err == nil {
				return msgs
			}
		}
	}

	// Legacy format: "Original task: ..." line (only for user_messages tag).
	if tag == "user_messages" {
		if idx := strings.Index(text, "Original task: "); idx >= 0 {
			rest := text[idx+len("Original task: "):]
			if nl := strings.Index(rest, "\n"); nl >= 0 {
				rest = rest[:nl]
			}
			if rest != "" {
				return []string{rest}
			}
		}
	}

	return nil
}

// marshalJSONCompact marshals a string slice as compact JSON.
func marshalJSONCompact(ss []string) string {
	b, _ := json.Marshal(ss)
	return string(b)
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
