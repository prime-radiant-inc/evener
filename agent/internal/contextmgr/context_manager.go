// Package contextmgr manages a session's context pressure: it tracks cumulative
// token usage, applies progressive compaction layers (observation masking,
// thinking clearing, deterministic checkpoint, LLM summarization) to the
// conversation history, and hosts the pluggable context strategies. Strategies
// reach back to their owning session only through the narrow Host interface,
// which breaks the context⇄session cycle.
package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/contextestimate"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// CompactionMeta holds session-level metadata injected into compaction summaries.
// The session populates this before each ManageContext call so that checkpoint
// and summarize have access to data outside the turn history.
type CompactionMeta struct {
	SessionID       string   // session id; non-empty only when the session is persistent (has a stateDir)
	ActivatedSkills []string // skill names activated during this session
}

// Manager tracks cumulative token usage and applies progressive compaction
// layers to conversation history as context fills up.
type Manager struct {
	profile  *provider.Profile
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
	// for experimental Strategy implementations that call
	// maskObservations/clearThinking directly.
	ObservationMaskThreshold float64
	ThinkingClearThreshold   float64
	// WarnThreshold is the pressure fraction at which the Session nudges the
	// agent to self-compact at its next clean seam (best-effort; the checkpoint
	// and summary layers remain the guarantee).
	WarnThreshold       float64
	CheckpointThreshold float64
	SummarizeThreshold  float64

	PreserveRecentTurns int

	// OnCompactionTurn is called for each compaction turn created (CHECKPOINT or SUMMARY).
	// Set by the session before ManageContext to record compaction turns in the transcript.
	OnCompactionTurn func(schema.Turn)

	// Meta holds session-level metadata for enriching compaction summaries.
	// Set by the session before each ManageContext call.
	Meta CompactionMeta
}

// NewManager creates a Manager with default thresholds.
func NewManager(profile *provider.Profile, client *llm.Client) *Manager {
	return &Manager{
		profile:                  profile,
		client:                   client,
		ObservationMaskThreshold: 0.60,
		ThinkingClearThreshold:   0.70,
		WarnThreshold:            0.75,
		CheckpointThreshold:      0.80,
		SummarizeThreshold:       0.95,
		PreserveRecentTurns:      6,
	}
}

func (cm *Manager) resultToolName() string {
	if cm.ResultToolName != "" {
		return cm.ResultToolName
	}
	return "communicate"
}

// AddUsage records token usage from a completed LLM call.
func (cm *Manager) AddUsage(u llm.Usage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.cumUsage = cm.cumUsage.Add(u)
}

// CumulativeUsage returns accumulated session totals.
func (cm *Manager) CumulativeUsage() llm.Usage {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.cumUsage
}

// RecordInputTokens stores the exact input token count from an API response,
// along with the history length at that point. This enables accurate pressure
// calculation for subsequent turns without relying on the char/4 heuristic.
func (cm *Manager) RecordInputTokens(tokens int, historyLen int) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.lastInputTokens = tokens
	cm.historyLenAtMeasure = historyLen
}

// SetProfile replaces the provider profile so that ContextWindowSize() and
// other profile-derived values stay current after a model change.
func (cm *Manager) SetProfile(profile *provider.Profile) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.profile = profile
}

// currentProfile returns the active profile under cm.mu so reads do not race
// SetProfile (called from Session.SetModel). The profile pointer is swapped
// atomically; a caller uses the returned value for the duration of one operation.
func (cm *Manager) currentProfile() *provider.Profile {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.profile
}

// LastInputTokens returns the most recently recorded input token count.
func (cm *Manager) LastInputTokens() int {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.lastInputTokens
}

// Pressure returns the current context pressure as a fraction (0.0–1.0).
func (cm *Manager) Pressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// estimatePressure calculates what fraction of the context window is in use.
// Uses actual API-reported token counts when available, falling back to char/4.
func (cm *Manager) estimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	cw := cm.currentProfile().ContextWindowSize()
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
		totalTokens = lastTokens + estimateTokens(newTurns)
	} else {
		// Fall back to char/4 for everything.
		totalTokens = estimateTokens(history) + sysPromptChars/4
	}

	return float64(totalTokens) / float64(cw)
}

// EstimatePressure returns the estimated fraction of context window in use.
func (cm *Manager) EstimatePressure(history []schema.Turn, sysPromptChars int) float64 {
	return cm.estimatePressure(history, sysPromptChars)
}

// EstimateUsage returns the estimated used, total, and remaining context tokens.
func (cm *Manager) EstimateUsage(history []schema.Turn, sysPromptChars int) schema.ContextMetrics {
	cw := cm.currentProfile().ContextWindowSize()
	if cw <= 0 {
		return schema.ContextMetrics{}
	}

	cm.mu.Lock()
	lastTokens := cm.lastInputTokens
	measuredLen := cm.historyLenAtMeasure
	cm.mu.Unlock()

	var used int
	if lastTokens > 0 && measuredLen <= len(history) {
		used = lastTokens + estimateTokens(history[measuredLen:])
	} else {
		used = estimateTokens(history) + sysPromptChars/4
	}
	remaining := cw - used
	if remaining < 0 {
		remaining = 0
	}
	return schema.ContextMetrics{Used: used, Window: cw, Remaining: remaining}
}

// ApplyThresholdScale scales all compaction thresholds on cm by the given
// factor. A factor of 0 or 1 leaves defaults unchanged.
//
// Thresholds are clamped to a minimum of 0.20 so that aggressive scaling
// doesn't collapse all layers into a narrow band.
func ApplyThresholdScale(cm *Manager, scale float64) {
	if scale > 0 && scale != 1.0 {
		clamp := func(v float64) float64 {
			if v < 0.20 {
				return 0.20
			}
			return v
		}
		cm.ObservationMaskThreshold = clamp(cm.ObservationMaskThreshold * scale)
		cm.ThinkingClearThreshold = clamp(cm.ThinkingClearThreshold * scale)
		cm.WarnThreshold = clamp(cm.WarnThreshold * scale)
		cm.CheckpointThreshold = clamp(cm.CheckpointThreshold * scale)
		cm.SummarizeThreshold = clamp(cm.SummarizeThreshold * scale)
	}
}

// estimateTokens estimates token count for turns using the char/4 heuristic.
func estimateTokens(turns []schema.Turn) int {
	return contextestimate.EstimateTurnsTokens(turns)
}

// --- MaybeCompact orchestrator ---

// MaybeCompact checks context pressure and applies compaction layers progressively.
// It modifies history in place and emits events for each layer applied.
// Called before each LLM request.
func (cm *Manager) MaybeCompact(
	ctx context.Context,
	history *[]schema.Turn,
	sysPromptChars int,
	emitFn func(events.EventKind, events.EventData),
) {
	cw := cm.currentProfile().ContextWindowSize()
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
		before := estimateTokens(*history)
		*history = checkpoint(*history, cm.PreserveRecentTurns, &cm.Meta, cm.resultToolName())
		after := estimateTokens(*history)
		emitFn(events.EventContextCompaction, events.ContextCompactionData{
			Layer:           "checkpoint",
			TurnsBefore:     turnsBefore,
			TurnsAfter:      len(*history),
			EstTokensBefore: before,
			EstTokensAfter:  after,
		})
		if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
			cm.OnCompactionTurn((*history)[0])
		}
		compacted = true
		p = pressure()
	}

	// Layer 2: LLM summarization at ≥90%.
	if p >= cm.SummarizeThreshold && cm.client != nil {
		turnsBefore := len(*history)
		before := estimateTokens(*history)
		result, err := cm.summarizeWithLLM(ctx, *history, cm.PreserveRecentTurns)
		if err != nil {
			// On error, emit warning but continue with current history.
			emitFn(events.EventWarning, events.WarningData{
				Message: "LLM summarization failed: " + err.Error(),
			})
		} else {
			*history = result
			after := estimateTokens(*history)
			emitFn(events.EventContextCompaction, events.ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary {
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
// Used for user-initiated compaction (e.g. /compact command). instructions is forwarded
// to the LLM summarization layer; pass "" for default behavior. Returns true if a summary
// was actually generated (false when there is no client or the history is too short to summarize).
func (cm *Manager) ForceCompact(
	ctx context.Context,
	history *[]schema.Turn,
	instructions string,
	emitFn func(events.EventKind, events.EventData),
) (summarized bool) {
	// Reset token measurement so inter-layer estimates use char/4.
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()

	// Layer 1: Deterministic checkpoint.
	turnsBefore := len(*history)
	before := estimateTokens(*history)
	*history = checkpoint(*history, cm.PreserveRecentTurns, &cm.Meta, cm.resultToolName())
	after := estimateTokens(*history)
	emitFn(events.EventContextCompaction, events.ContextCompactionData{
		Layer:           "checkpoint",
		TurnsBefore:     turnsBefore,
		TurnsAfter:      len(*history),
		EstTokensBefore: before,
		EstTokensAfter:  after,
	})
	if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnCheckpoint {
		cm.OnCompactionTurn((*history)[0])
	}

	// Layer 2: LLM summarization (only if client is available).
	if cm.client != nil {
		turnsBefore = len(*history)
		before = estimateTokens(*history)
		lenBefore := len(*history)
		result, err := cm.summarizeWithLLMSteered(ctx, *history, cm.PreserveRecentTurns, instructions)
		if err != nil {
			emitFn(events.EventWarning, events.WarningData{
				Message: "LLM summarization failed: " + err.Error(),
			})
		} else {
			summarized = len(result) != lenBefore
			*history = result
			after := estimateTokens(*history)
			emitFn(events.EventContextCompaction, events.ContextCompactionData{
				Layer:           "summarize",
				TurnsBefore:     turnsBefore,
				TurnsAfter:      len(*history),
				EstTokensBefore: before,
				EstTokensAfter:  after,
			})
			if cm.OnCompactionTurn != nil && len(*history) > 0 && (*history)[0].Kind == schema.TurnSummary {
				cm.OnCompactionTurn((*history)[0])
			}
		}
	}

	// Reset token measurement after compaction.
	cm.mu.Lock()
	cm.lastInputTokens = 0
	cm.historyLenAtMeasure = 0
	cm.mu.Unlock()
	return summarized
}

// --- Observation masking (Layer 1) ---

// maskObservations replaces tool result content in old turns with one-line summaries.
// Preserves: error results, communicate tool results, already-masked results,
// recent turns, and results where the summary would be longer than the original.
func maskObservations(history []schema.Turn, preserveRecent int, resultToolName string) {
	if len(history) == 0 {
		return
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return
	}
	for i := 0; i < cutoff; i++ {
		t := &history[i]
		if t.Kind != schema.TurnTool && t.Kind != schema.TurnToolResults {
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
func findToolCallArgs(history []schema.Turn, toolCallID string) json.RawMessage {
	for i := len(history) - 1; i >= 0; i-- {
		t := history[i]
		if t.Kind != schema.TurnAssistant {
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

func parseCommunicateArgs(args json.RawMessage) (awaitReply bool, message string) {
	var payload struct {
		Message    string `json:"message"`
		AwaitReply *bool  `json:"await_reply"`
		Output     *struct {
			Message string `json:"message"`
		} `json:"output"`
	}
	if len(args) == 0 || json.Unmarshal(args, &payload) != nil {
		return false, ""
	}
	message = strings.TrimSpace(payload.Message)
	if message == "" && payload.Output != nil {
		message = strings.TrimSpace(payload.Output.Message)
	}
	if payload.AwaitReply == nil {
		return false, ""
	}
	return *payload.AwaitReply, message
}

func communicateArgsFromHistory(history []schema.Turn, toolCallID string) (awaitReply bool, message string) {
	return parseCommunicateArgs(findToolCallArgs(history, toolCallID))
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

	case "delegate":
		jobID := extractJSONField(contentStr, "job_id")
		if jobID != "" {
			return fmt.Sprintf("[delegate: %s]", jobID)
		}
		return fmt.Sprintf("[delegate: %d chars]", len(contentStr))

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
func clearThinking(history []schema.Turn, preserveRecent int) {
	if len(history) == 0 {
		return
	}

	cutoff := len(history) - preserveRecent
	if cutoff <= 0 {
		return
	}

	for i := 0; i < cutoff; i++ {
		t := &history[i]
		if t.Kind != schema.TurnAssistant {
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
// User messages and agent responses are stored as an interleaved Markdown
// conversation for readable round-tripping across repeated compactions.
func checkpoint(history []schema.Turn, preserveRecent int, meta *CompactionMeta, resultToolName string) []schema.Turn {
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

	data := collectCheckpointData(history, cutoff, resultToolName)
	checkpointTurn := schema.NewTurn(schema.TurnCheckpoint, llm.User(formatCheckpoint(data, meta, maxCheckpointChars)))

	result := make([]schema.Turn, 0, 1+preserveRecent)
	result = append(result, checkpointTurn)
	result = append(result, history[cutoff:]...)
	return result
}

// checkpointData is the raw material distilled from the to-be-compacted history
// prefix: which files were modified, which skills activated, per-tool call counts,
// the last few shell results, the conversation (user/agent turns), and the
// assistant's working notes.
type checkpointData struct {
	modifiedFiles    map[string]bool
	activatedSkills  map[string]bool
	toolCounts       map[string]int
	lastShellResults []string
	conversation     []checkpointConversationEntry
	workingNotes     []string
}

// collectCheckpointData walks history[:cutoff] and distills it into a
// checkpointData: modified files, tool counts, shell results, user messages,
// final agent responses, working notes, and activated skills.
func collectCheckpointData(history []schema.Turn, cutoff int, resultToolName string) checkpointData {
	data := checkpointData{
		modifiedFiles:   map[string]bool{},
		activatedSkills: map[string]bool{},
		toolCounts:      map[string]int{},
	}

	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case schema.TurnCheckpoint, schema.TurnSummary:
			// Extract user messages and working notes from previous compaction turns
			// so they survive across repeated compactions.
			data.conversation = append(data.conversation, extractCheckpointConversation(t.Message.Text())...)
			data.workingNotes = append(data.workingNotes, extractCheckpointWorkingNotes(t.Message.Text())...)

		case schema.TurnUserInput:
			text := t.Message.Text()
			if text == "" {
				continue
			}
			// Old-format checkpoint/summary stored as TurnUserInput — extract
			// user messages from them just like typed compaction turns.
			if strings.HasPrefix(text, "[CONTEXT CHECKPOINT]") || strings.HasPrefix(text, "[CONTEXT SUMMARY]") {
				data.conversation = append(data.conversation, extractCheckpointConversation(text)...)
				continue
			}
			data.conversation = append(data.conversation, checkpointConversationEntry{Role: "user", Text: text})

		case schema.TurnTool, schema.TurnToolResults:
			// Extract non-await communicate results (agent responses to the user).
			for _, p := range t.Message.Content {
				if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
					continue
				}
				if p.ToolResult.Name == resultToolName {
					awaitReply, msg := communicateArgsFromHistory(history[:i+1], p.ToolResult.ToolCallID)
					if !awaitReply && msg != "" {
						data.conversation = append(data.conversation, checkpointConversationEntry{Role: "agent", Text: msg})
					}
					continue
				}
			}

		case schema.TurnAssistant:
			// Capture assistant analytical text as working notes.
			if text := t.Message.Text(); len(text) > 50 {
				note := text
				if len(note) > 500 {
					note = note[:500] + "..."
				}
				data.workingNotes = append(data.workingNotes, note)
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentWebSearch {
					data.toolCounts["web_search"]++
					continue
				}
				if p.Kind != llm.ContentToolCall || p.ToolCall == nil {
					continue
				}
				name := p.ToolCall.Name
				data.toolCounts[name]++

				var args map[string]any
				if len(p.ToolCall.Arguments) > 0 {
					_ = json.Unmarshal(p.ToolCall.Arguments, &args)
				}

				switch name {
				case "edit_file", "write_file":
					if path, ok := args["file_path"]; ok {
						data.modifiedFiles[fmt.Sprint(path)] = true
					}
				case "apply_patch":
					if patch, ok := args["patch"]; ok {
						for _, line := range strings.Split(fmt.Sprint(patch), "\n") {
							line = strings.TrimSpace(line)
							for _, prefix := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
								if strings.HasPrefix(line, prefix) {
									data.modifiedFiles[strings.TrimPrefix(line, prefix)] = true
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
							if history[j].Kind != schema.TurnTool && history[j].Kind != schema.TurnToolResults {
								continue
							}
							content := findToolResultByCallID(history[j], p.ToolCall.ID)
							if content != "" {
								exitCode = parseExitCode(content)
								break
							}
						}
						data.lastShellResults = append(data.lastShellResults, fmt.Sprintf("  %q → exit %s", cmdStr, exitCode))
					}
				case "use_skill":
					if sn, ok := args["skill_name"]; ok {
						data.activatedSkills[fmt.Sprint(sn)] = true
					}
				}
			}
		}
	}

	return data
}

// formatCheckpoint renders collected checkpoint data into the [CONTEXT CHECKPOINT]
// … [END CHECKPOINT] block: the fixed metadata sections (transcript pointer,
// modified files, tool counts, recent shell results, activated skills), then the
// conversation and working notes fit into maxChars by shedding the oldest working
// notes first and then the oldest conversation entries.
func formatCheckpoint(data checkpointData, meta *CompactionMeta, maxChars int) string {
	// Build the fixed-size sections first (metadata), then fill remaining
	// budget with user messages and agent responses.
	var fixed strings.Builder

	// Transcript pointer — tells the model how to access full history via transcript tools.
	if meta != nil && meta.SessionID != "" {
		fixed.WriteString(fmt.Sprintf("Earlier history was compacted. This session's id is %s. Use read_session_transcript to recover earlier detail, or find_session_transcripts to search it.\n", meta.SessionID))
	}

	// Files modified.
	if len(data.modifiedFiles) > 0 {
		files := make([]string, 0, len(data.modifiedFiles))
		for f := range data.modifiedFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		fixed.WriteString(fmt.Sprintf("Files modified: %s\n", strings.Join(files, ", ")))
	}

	// Tool call counts.
	if total := sumCounts(data.toolCounts); total > 0 {
		fixed.WriteString(fmt.Sprintf("Actions taken: %d tool calls (", total))
		toolNames := make([]string, 0, len(data.toolCounts))
		for name := range data.toolCounts {
			toolNames = append(toolNames, name)
		}
		sort.Strings(toolNames)
		for i, name := range toolNames {
			if i > 0 {
				fixed.WriteString(", ")
			}
			fixed.WriteString(fmt.Sprintf("%d %s", data.toolCounts[name], name))
		}
		fixed.WriteString(")\n")
	}

	// Last 3 shell results.
	if len(data.lastShellResults) > 0 {
		start := 0
		if len(data.lastShellResults) > 3 {
			start = len(data.lastShellResults) - 3
		}
		fixed.WriteString("Last shell results:\n")
		for _, r := range data.lastShellResults[start:] {
			fixed.WriteString(r + "\n")
		}
	}

	// Activated skills.
	allSkills := map[string]bool{}
	for s := range data.activatedSkills {
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

	// Budget for variable content (conversation + working notes as Markdown).
	// Reserve space for [CONTEXT CHECKPOINT], [END CHECKPOINT], fixed sections,
	// and the Markdown section headings.
	overhead := len("[CONTEXT CHECKPOINT]\n") + len(fixedStr) + len("[END CHECKPOINT]\n") + 200
	variableBudget := maxChars - overhead
	if variableBudget < 1000 {
		variableBudget = 1000
	}

	// Encode conversation and working notes as Markdown. Shed oldest notes
	// first, then oldest user or agent messages if needed to fit budget.
	conversation := data.conversation
	workingNotes := data.workingNotes
	conversationMarkdown := renderCheckpointConversation(conversation)
	notesMarkdown := renderCheckpointWorkingNotes(workingNotes)

	for len(conversationMarkdown)+len(notesMarkdown) > variableBudget {
		if len(workingNotes) > 1 {
			workingNotes = workingNotes[1:] // drop oldest note first
			notesMarkdown = renderCheckpointWorkingNotes(workingNotes)
			continue
		}
		if len(conversation) > 1 {
			conversation = conversation[1:] // then drop oldest conversation entry
			conversationMarkdown = renderCheckpointConversation(conversation)
			continue
		}
		break
	}

	// Assemble final checkpoint.
	var b strings.Builder
	b.WriteString("[CONTEXT CHECKPOINT]\n")
	b.WriteString(fixedStr)
	if conversationMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(conversationMarkdown)
	}
	if notesMarkdown != "" {
		b.WriteString("\n")
		b.WriteString(notesMarkdown)
	}
	b.WriteString("[END CHECKPOINT]\n")
	return b.String()
}

// findToolResultByCallID finds a tool result in a TurnTool by its ToolCallID
// and returns the string content, or "" if not found.
func findToolResultByCallID(t schema.Turn, toolCallID string) string {
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

// summarizationRoute is one (provider, model) the summarizer tries in order: the
// configured cheap model on its (possibly cross-provider) cheap provider first,
// then the active model on the main provider as a fallback.
type summarizationRoute struct {
	provider string
	model    string
}

func summarizationModels(profile *provider.Profile) []summarizationRoute {
	if profile == nil {
		return nil
	}
	active := strings.TrimSpace(profile.Model())
	configured := strings.TrimSpace(profile.ConfiguredCheapModel())

	var routes []summarizationRoute
	if configured != "" {
		routes = append(routes, summarizationRoute{provider: profile.CheapProvider(), model: configured})
	}
	if active != "" {
		activeRoute := summarizationRoute{provider: profile.ID(), model: active}
		// Skip a fallback identical to the cheap route (same provider + model).
		if len(routes) == 0 || routes[0] != activeRoute {
			routes = append(routes, activeRoute)
		}
	}
	return routes
}

func shouldFallbackSummarizationModel(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if llm.Classify(err) == llm.ErrorClassFallback {
		return true
	}
	if llm.Classify(err) != llm.ErrorClassPermanent {
		return false
	}
	switch llm.Kind(err) {
	case llm.KindInvalidRequest, llm.KindNotFound, llm.KindAccessDenied:
		return true
	default:
		return false
	}
}

// defaultSummaryPrefix is the instruction block used when no caller instructions
// are provided. It mandates seven specific sections and directs the LLM to
// err on the side of verbosity.
var defaultSummaryPrefix = `You are performing a CONTEXT CHECKPOINT COMPACTION. This session is being continued from a previous conversation that ran out of context. Create a detailed handoff summary that another instance of yourself will use to seamlessly continue the work.

Your summary MUST include ALL of the following sections:

## Conversation Timeline
Reproduce user messages and agent replies in chronological, interleaved order. Preserve user messages verbatim. Summarize agent replies only when needed for brevity, but keep commitments, decisions, and final answers clear.

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

// buildSummaryPrompt constructs the full LLM prompt for context compaction.
// When instructions are non-empty the prompt is instruction-led: the
// mandatory-7-sections block is replaced by the caller's directive.
// When instructions are empty the default prompt is used unchanged.
func buildSummaryPrompt(historyText, instructions string) string {
	if instructions != "" {
		return `You are performing a CONTEXT CHECKPOINT COMPACTION for an agent continuing its own work.

## CALLER INSTRUCTIONS (these take precedence)
` + instructions + `

Follow the caller instructions above when deciding what to preserve verbatim and what to drop or condense. Where they conflict with the general guidance below, the caller instructions win. Still produce a coherent handoff: keep decisions, current state, and actionable next steps. Do not invent content.

` + historyText
	}
	return defaultSummaryPrefix + historyText
}

// noteElicitationPrompt asks the model, at compaction time, to enumerate the
// details that must survive verbatim — explicitly targeting the kinds a lossy
// summary erodes (opaque tokens, exact numbers, hashes, names). Used by the
// forced-note-at-compaction mechanism: the reply is pinned as the note before the
// compaction folds the history, so those details are re-stamped verbatim rather
// than decaying through successive summaries.
const noteElicitationPrompt = `Your conversation context is about to be COMPACTED — most of it will be replaced by a lossy summary. Before that happens, list everything that MUST survive VERBATIM for the work to continue.

Focus especially on details a summary tends to drop or paraphrase: exact tokens, IDs, hashes, version tags, exact numbers and thresholds, file/column/endpoint names, and specific decisions. Preserve exact strings — do not abbreviate or round.

Output a concise bullet list of the must-keep items, nothing else.`

// HasClient reports whether an LLM client is available (note elicitation and LLM
// summarization both require one).
func (cm *Manager) HasClient() bool { return cm.client != nil }

// noteElicitChars caps the history rendered into the elicitation prompt.
const noteElicitChars = 80_000

// ElicitNote asks the model to enumerate the must-survive-verbatim details from
// the given history, for pinning as a note before a compaction (Variant B of the
// forced-note-at-compaction mechanism). The caller passes the prefix the
// compaction is about to fold (the recent preserved turns survive verbatim and
// need no rescuing). Returns the model's bullet list. Falls back across the
// configured summarization models on transient errors, like the summarizer.
func (cm *Manager) ElicitNote(ctx context.Context, history []schema.Turn) (string, error) {
	if cm.client == nil {
		return "", errors.New("note elicitation requires an LLM client")
	}
	prof := cm.currentProfile()
	routes := summarizationModels(prof)
	if len(routes) == 0 {
		return "", errors.New("no model available for note elicitation")
	}
	prompt := noteElicitationPrompt + "\n\n--- CONVERSATION SO FAR ---\n" + renderHistoryForElicit(history, noteElicitChars)
	var resp llm.Response
	var lastErr error
	for _, route := range routes {
		req := llm.Request{
			Model:    route.model,
			Provider: route.provider,
			Messages: []llm.Message{llm.User(prompt)},
		}
		resp, lastErr = cm.client.Complete(ctx, req)
		if lastErr == nil {
			break
		}
		if route != routes[len(routes)-1] && !shouldFallbackSummarizationModel(ctx, lastErr) {
			break
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return strings.TrimSpace(resp.Text()), nil
}

// renderHistoryForElicit flattens turns into a plain-text transcript for the
// note-elicitation prompt, capped at maxChars and keeping the MOST RECENT content
// (it walks from the tail). Unlike the summarizer's rendering it includes
// tool-call arguments and FULL, untruncated tool-result content: the elicitor's
// whole job is to recover exact opaque values (tokens, IDs, hashes, paths) that
// in a coding agent live in tool output, so per-result truncation would defeat
// it — the maxChars budget is what bounds total size.
func renderHistoryForElicit(history []schema.Turn, maxChars int) string {
	var parts []string
	total := 0
	for i := len(history) - 1; i >= 0; i-- {
		s := renderTurnForElicit(history[i])
		if s == "" {
			continue
		}
		if total+len(s) > maxChars {
			if rem := maxChars - total; rem > 0 {
				parts = append(parts, "[... earlier history truncated ...]\n"+s[len(s)-rem:])
			}
			break
		}
		parts = append(parts, s)
		total += len(s)
	}
	// parts were collected tail-first; reverse to chronological order.
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "")
}

// renderTurnForElicit renders one turn as labeled plain text, including tool-call
// arguments and tool-result content — the parts that carry the exact values the
// elicitor must preserve. Returns "" for turns with no textual content.
func renderTurnForElicit(t schema.Turn) string {
	var b strings.Builder
	for _, p := range t.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				b.WriteString(p.Text + "\n")
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				b.WriteString(fmt.Sprintf("Tool call %s(%s)\n", p.ToolCall.Name, strings.TrimSpace(string(p.ToolCall.Arguments))))
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				b.WriteString(fmt.Sprintf("Tool result %s: %s\n", p.ToolResult.Name, fmt.Sprint(p.ToolResult.Content)))
			}
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return ""
	}
	label := ""
	switch t.Kind {
	case schema.TurnUserInput:
		label = "User: "
	case schema.TurnAssistant:
		label = "Assistant: "
	case schema.TurnSteering, schema.TurnCheckpoint, schema.TurnSummary:
		label = "Context: "
	}
	return label + b.String()
}

// summarizeWithLLM calls the cheap model to generate a narrative summary of
// old history. Replaces old history with a single user message containing the
// summary, preserving the most recent turns.
func (cm *Manager) summarizeWithLLM(ctx context.Context, history []schema.Turn, preserveRecent int) ([]schema.Turn, error) {
	return cm.summarizeWithLLMSteered(ctx, history, preserveRecent, "")
}

// summarizeWithLLMSteered is like summarizeWithLLM but accepts optional caller
// instructions that replace the default mandatory-sections prompt when non-empty.
func (cm *Manager) summarizeWithLLMSteered(ctx context.Context, history []schema.Turn, preserveRecent int, instructions string) ([]schema.Turn, error) {
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

	truncText := func(s string, limit int) string {
		if len(s) > limit {
			return s[:limit] + "..."
		}
		return s
	}

	var b strings.Builder
	for i := 0; i < cutoff; i++ {
		t := history[i]
		switch t.Kind {
		case schema.TurnUserInput:
			// Preserve user messages verbatim. Cap at 5k to protect the cheap
			// model's context — normal messages are well under this.
			text := t.Message.Text()
			if len(text) > 5000 {
				text = text[:5000] + "..."
			}
			b.WriteString("User: " + text + "\n")
		case schema.TurnCheckpoint, schema.TurnSummary:
			b.WriteString("Previous compaction: " + truncText(t.Message.Text(), 1000) + "\n")
		case schema.TurnAssistant:
			// Extract communicate calls so the summarizer sees how the agent
			// talked to the user, while still distinguishing questions from
			// non-await replies.
			text := t.Message.Text()
			if text != "" {
				b.WriteString("Assistant: " + truncText(text, 500) + "\n")
			}
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolCall && p.ToolCall != nil && p.ToolCall.Name == cm.resultToolName() {
					awaitReply, msg := parseCommunicateArgs(p.ToolCall.Arguments)
					if msg == "" {
						continue
					}
					switch {
					case awaitReply:
						b.WriteString(fmt.Sprintf("Agent Question: %s\n", msg))
					default:
						b.WriteString(fmt.Sprintf("Agent Message: %s\n", msg))
					}
				}
			}
		case schema.TurnTool, schema.TurnToolResults:
			for _, p := range t.Message.Content {
				if p.Kind == llm.ContentToolResult && p.ToolResult != nil {
					content := fmt.Sprint(p.ToolResult.Content)
					if len(content) > 200 {
						content = content[:200] + "..."
					}
					b.WriteString(fmt.Sprintf("Tool(%s): %s\n", p.ToolResult.Name, content))
				}
			}
		case schema.TurnSteering:
			b.WriteString("System: " + t.Message.Text() + "\n")
		}
		if b.Len() > maxHistoryChars {
			b.WriteString("\n[... truncated ...]\n")
			break
		}
	}

	prompt := buildSummaryPrompt(b.String(), instructions)

	sumProfile := cm.currentProfile()
	routes := summarizationModels(sumProfile)
	if len(routes) == 0 {
		return nil, errors.New("summarization model is empty")
	}
	var resp llm.Response
	var lastErr error
	for _, route := range routes {
		req := llm.Request{
			Model:    route.model,
			Provider: route.provider,
			Messages: []llm.Message{llm.User(prompt)},
		}
		resp, lastErr = cm.client.Complete(ctx, req)
		if lastErr == nil {
			break
		}
		if route != routes[len(routes)-1] && !shouldFallbackSummarizationModel(ctx, lastErr) {
			break
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	summaryText := "[CONTEXT SUMMARY]\n" + resp.Text() + "\n[END SUMMARY]"
	summaryTurn := schema.NewTurn(schema.TurnSummary, llm.User(summaryText))

	result := make([]schema.Turn, 0, 1+preserveRecent)
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
func safeCutoff(history []schema.Turn, cutoff int) int {
	for cutoff > 0 && cutoff < len(history) {
		k := history[cutoff].Kind
		if k == schema.TurnTool || k == schema.TurnToolResults || k == schema.TurnSteering {
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
