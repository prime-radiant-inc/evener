package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/serf/appwire"
)

type TranscriptReducer struct {
	messages       []ChatMessage
	activeTools    map[string]int
	activeMessages map[string]int
}

func NewTranscriptReducer(messages []ChatMessage, activeTools, activeMessages map[string]int) TranscriptReducer {
	if activeTools == nil {
		activeTools = make(map[string]int)
	}
	if activeMessages == nil {
		activeMessages = make(map[string]int)
	}
	return TranscriptReducer{
		messages:       messages,
		activeTools:    activeTools,
		activeMessages: activeMessages,
	}
}

// Messages returns the current message list the reducer has folded.
func (r *TranscriptReducer) Messages() []ChatMessage { return r.messages }

// ActiveTools returns the item-id -> message-index map for in-flight tool calls.
func (r *TranscriptReducer) ActiveTools() map[string]int { return r.activeTools }

// ActiveMessages returns the item-id -> message-index map for in-flight messages.
func (r *TranscriptReducer) ActiveMessages() map[string]int { return r.activeMessages }

func (r *TranscriptReducer) ApplyAgentMessageDelta(turnID, itemID, delta string) {
	if delta == "" {
		return
	}
	// The agent answering means any live thought is no longer the current turn.
	r.finalizeLiveReasoning()
	turnIndex := TurnIndexFromID(turnID)
	item := appwire.ThreadItem{ID: itemID, TurnID: turnID}
	if idx, ok := r.activeMessageIndex(item); ok {
		r.messages[idx].Text += delta
		if turnID != "" {
			r.messages[idx].TurnID = turnID
			r.messages[idx].TurnIndex = turnIndex
		}
		return
	}
	if len(r.messages) > 0 && r.messages[len(r.messages)-1].Kind == MsgAssistant && turnScopeMatches(r.messages[len(r.messages)-1].TurnID, turnID, r.messages[len(r.messages)-1].TurnIndex, turnIndex) {
		idx := len(r.messages) - 1
		r.messages[idx].Text += delta
		if turnID != "" {
			r.messages[idx].TurnID = turnID
			r.messages[idx].TurnIndex = turnIndex
		}
		if itemID != "" {
			r.rememberActiveMessage(item, idx)
		}
		return
	}
	idx := len(r.messages)
	r.messages = append(r.messages, ChatMessage{Kind: MsgAssistant, Text: delta, TurnID: turnID, TurnIndex: turnIndex, ItemID: itemID})
	if itemID != "" {
		r.rememberActiveMessage(item, idx)
	}
}

// ResetAgentMessage discards the in-progress assistant message for itemID so a
// retried model call's output replaces, rather than appends to, the partial
// that was already streamed. No-op when the item is unknown.
func (r *TranscriptReducer) ResetAgentMessage(turnID, itemID string) {
	if itemID == "" {
		return
	}
	idx, ok := r.messageIndexByItemID(itemID, MsgAssistant, turnID, TurnIndexFromID(turnID))
	if !ok {
		return
	}
	r.messages = append(r.messages[:idx], r.messages[idx+1:]...)
	delete(r.activeMessages, itemID)
	r.shiftActiveIndicesAfterRemoval(idx)
}

// ApplyReasoningSummaryDelta streams an incremental chunk of the model's
// reasoning summary ("thinking") into the live thought for itemID, creating the
// thought on the first chunk. A chunk for a new reasoning item collapses the
// previous live thought, so at most one thought streams open at a time.
func (r *TranscriptReducer) ApplyReasoningSummaryDelta(turnID, itemID, delta string) {
	if delta == "" {
		return
	}
	turnIndex := TurnIndexFromID(turnID)
	if idx, ok := r.activeReasoningIndex(itemID); ok {
		r.messages[idx].Text += delta
		return
	}
	r.finalizeLiveReasoning()
	idx := len(r.messages)
	r.messages = append(r.messages, ChatMessage{Kind: MsgReasoning, Text: delta, TurnID: turnID, TurnIndex: turnIndex, ItemID: itemID})
	if itemID != "" {
		r.activeMessages[itemID] = idx
	}
}

// FinalizeReasoning collapses the live thought, if any, to its one-line gist.
// Called when the turn completes so a finished turn never leaves a thought
// streaming open.
func (r *TranscriptReducer) FinalizeReasoning() {
	r.finalizeLiveReasoning()
}

// finalizeLiveReasoning marks the single live (still-streaming) reasoning
// thought Done so the renderer collapses it, and drops it from the active map.
func (r *TranscriptReducer) finalizeLiveReasoning() {
	for i := range r.messages {
		if r.messages[i].Kind == MsgReasoning && !r.messages[i].Done {
			r.messages[i].Done = true
			if id := r.messages[i].ItemID; id != "" {
				delete(r.activeMessages, id)
			}
		}
	}
}

// activeReasoningIndex returns the index of the live reasoning thought for
// itemID, if it is still streaming.
func (r *TranscriptReducer) activeReasoningIndex(itemID string) (int, bool) {
	if itemID == "" {
		return 0, false
	}
	idx, ok := r.activeMessages[itemID]
	if !ok || idx < 0 || idx >= len(r.messages) {
		return 0, false
	}
	if r.messages[idx].Kind != MsgReasoning || r.messages[idx].Done {
		return 0, false
	}
	return idx, true
}

// shiftActiveIndicesAfterRemoval decrements the cached active-item indices that
// sat after a removed message so they keep pointing at the same messages.
func (r *TranscriptReducer) shiftActiveIndicesAfterRemoval(removed int) {
	for id, i := range r.activeMessages {
		if i > removed {
			r.activeMessages[id] = i - 1
		}
	}
	for id, i := range r.activeTools {
		if i > removed {
			r.activeTools[id] = i - 1
		}
	}
}

func (r *TranscriptReducer) ApplyUserMessageEcho(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.messages = append(r.messages, ChatMessage{Kind: MsgUser, Text: text})
}

func (r *TranscriptReducer) RemoveUserMessageEcho(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if idx, ok := r.pendingUserEchoIndex(text); ok {
		r.messages = append(r.messages[:idx], r.messages[idx+1:]...)
	}
}

func (r *TranscriptReducer) ApplyToolOutputDelta(itemID, delta string) {
	if delta == "" {
		return
	}
	item := appwire.ThreadItem{ID: itemID, Output: delta}
	if idx, ok := r.activeToolIndex(item); ok {
		if info := r.messages[idx].Tool; info != nil {
			info.Output += delta
		}
		return
	}
	idx := len(r.messages)
	r.messages = append(r.messages, ChatMessage{
		Kind:   MsgTool,
		ItemID: itemID,
		Tool: &ToolCallInfo{
			Output: delta,
		},
	})
	r.rememberActiveTool(item, idx)
}

func (r *TranscriptReducer) ApplyThreadItem(item appwire.ThreadItem, turnIndex int, completed bool) {
	switch item.Type {
	case "systemMessage":
		text := systemMessageItemText(item)
		if text == "" {
			return
		}
		r.messages = append(r.messages, ChatMessage{Kind: MsgSystem, Text: text, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID})
	case "userMessage":
		text := userMessageItemText(item)
		if strings.TrimSpace(text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, MsgUser, item.TurnID, turnIndex); ok {
				r.messages[idx].Text = text
				r.messages[idx].TurnID = item.TurnID
				r.messages[idx].TurnIndex = turnIndex
				return
			}
			if idx, ok := r.pendingUserEchoIndex(text); ok {
				r.messages[idx].Text = text
				r.messages[idx].TurnID = item.TurnID
				r.messages[idx].TurnIndex = turnIndex
				r.messages[idx].ItemID = item.ID
				return
			}
			r.messages = append(r.messages, ChatMessage{Kind: MsgUser, Text: text, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID})
		}
	case "reasoning":
		if idx, ok := r.activeReasoningIndex(item.ID); ok {
			if completed {
				r.messages[idx].Done = true
				r.clearActiveMessage(item)
			}
			return
		}
		r.finalizeLiveReasoning()
		idx := len(r.messages)
		r.messages = append(r.messages, ChatMessage{Kind: MsgReasoning, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID, Done: completed})
		if item.ID != "" && !completed {
			r.activeMessages[item.ID] = idx
		}
	case "agentMessage":
		// The agent answering means any live thought is no longer the current turn.
		r.finalizeLiveReasoning()
		if strings.TrimSpace(item.Text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, MsgAssistant, item.TurnID, turnIndex); ok {
				r.messages[idx].Text = item.Text
				r.messages[idx].TurnID = item.TurnID
				r.messages[idx].TurnIndex = turnIndex
				if completed {
					r.clearActiveMessage(item)
				}
			} else if idx, ok := r.activeMessageIndex(item); ok {
				r.messages[idx].Text = item.Text
				r.messages[idx].TurnID = item.TurnID
				r.messages[idx].TurnIndex = turnIndex
				if completed {
					r.clearActiveMessage(item)
				}
			} else if len(r.messages) > 0 && r.messages[len(r.messages)-1].Kind == MsgAssistant && turnScopeMatches(r.messages[len(r.messages)-1].TurnID, item.TurnID, r.messages[len(r.messages)-1].TurnIndex, turnIndex) {
				r.messages[len(r.messages)-1].Text = item.Text
				r.messages[len(r.messages)-1].ItemID = item.ID
				r.messages[len(r.messages)-1].TurnID = item.TurnID
				r.messages[len(r.messages)-1].TurnIndex = turnIndex
			} else {
				idx := len(r.messages)
				r.messages = append(r.messages, ChatMessage{Kind: MsgAssistant, Text: item.Text, TurnID: item.TurnID, ItemID: item.ID, TurnIndex: turnIndex})
				if !completed {
					r.rememberActiveMessage(item, idx)
				}
			}
		}
	case "commandExecution":
		// The agent calling a tool means any live thought is no longer current.
		r.finalizeLiveReasoning()
		done := threadItemToolDone(item, completed)
		if idx, ok := r.toolIndex(item, turnIndex); ok {
			if item.ID != "" {
				r.messages[idx].ItemID = item.ID
			}
			if item.CallID != "" {
				r.messages[idx].ToolCallID = item.CallID
			}
			r.messages[idx].TurnID = item.TurnID
			r.messages[idx].TurnIndex = turnIndex
			info := r.messages[idx].Tool
			if info == nil {
				return
			}
			mergeThreadItemIntoToolInfo(info, item, done)
			if done {
				r.clearActiveTool(item)
			} else {
				r.rememberActiveTool(item, idx)
			}
			return
		}
		info := toolInfoFromThreadItem(item, done)
		idx := len(r.messages)
		r.messages = append(r.messages, ChatMessage{Kind: MsgTool, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID, ToolCallID: item.CallID, Tool: info})
		if !done {
			r.rememberActiveTool(item, idx)
		}
	}
}

func (r *TranscriptReducer) ApplySerfJob(job appwire.SerfJobInfo) {
	run := subagentRunFromJob(job)
	if run.JobID == "" && run.DelegateID == "" && run.OriginToolCallID == "" && run.OriginItemID == "" {
		return
	}
	// Delegate subagents and long-lived (background) shell jobs are tracked as
	// subagent runs; a foreground shell stays an ordinary tool call.
	if run.JobType != "" && !isDelegateJobType(run.JobType) && !isBackgroundShellRun(run) {
		return
	}
	idx, matched := r.subagentMessageIndex(run)
	if !matched && !hasDelegateJobSignal(run) {
		return
	}
	if matched {
		info := r.messages[idx].Tool
		if info == nil {
			return
		}
		merged := mergeSubagentRun(info.Subagent, run)
		info.Subagent = &merged
		if merged.Task != "" && info.Description == "" {
			info.Description = merged.Task
		}
		if subagentTerminalStatus(merged.Status) {
			info.Done = true
		}
		return
	}
	name := "delegate"
	desc := run.Task
	if isBackgroundShellRun(run) {
		name = "shell"
		if desc == "" {
			desc = run.Command
		}
	}
	info := &ToolCallInfo{Name: name, Description: desc, Done: subagentTerminalStatus(run.Status)}
	info.Subagent = &run
	r.messages = append(r.messages, ChatMessage{Kind: MsgTool, ItemID: run.OriginItemID, ToolCallID: run.OriginToolCallID, Tool: info})
}

func isBackgroundShellRun(run SubagentRunInfo) bool {
	return run.Background && strings.EqualFold(strings.TrimSpace(run.JobType), "shell")
}

// ApplyTieHeadline pulls a job notification's result headline onto its rail run
// (matched by job id), so a finished subagent reads "tests passed · 4ad69c0"
// instead of a bare "done". Reports whether a run was matched.
func (r *TranscriptReducer) ApplyTieHeadline(jobID, headline string, isError bool) bool {
	jobID = strings.TrimSpace(jobID)
	headline = strings.TrimSpace(headline)
	if jobID == "" || headline == "" {
		return false
	}
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != MsgTool || msg.Tool == nil || msg.Tool.Subagent == nil {
			continue
		}
		if msg.Tool.Subagent.JobID != jobID {
			continue
		}
		msg.Tool.Subagent.Headline = headline
		msg.Tool.Subagent.HeadlineError = isError
		return true
	}
	return false
}

// ApplyChildActivity routes a watched child's latest live step to its running
// subagent row (matched by transcript ref). The step count advances only when
// the activity actually changes, so a stalled child's count visibly freezes
// (honest progress, no fake liveness). Reports whether a row was updated.
func (r *TranscriptReducer) ApplyChildActivity(ref, activity string) bool {
	ref = strings.TrimSpace(ref)
	activity = strings.TrimSpace(activity)
	if ref == "" || activity == "" {
		return false
	}
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != MsgTool || msg.Tool == nil || msg.Tool.Subagent == nil {
			continue
		}
		run := msg.Tool.Subagent
		if run.TranscriptRef != ref || subagentTerminalStatus(run.Status) {
			continue
		}
		if run.Activity != activity {
			run.Steps++
			run.Activity = activity
		}
		return true
	}
	return false
}

func (r *TranscriptReducer) subagentMessageIndex(run SubagentRunInfo) (int, bool) {
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != MsgTool || msg.Tool == nil {
			continue
		}
		// Match a delegate tool message, or any tool message that already
		// carries a subagent run (e.g. a tracked background shell).
		if !isDelegateToolName(msg.Tool.Name) && msg.Tool.Subagent == nil {
			continue
		}
		if msg.Tool.Subagent != nil {
			existing := msg.Tool.Subagent
			if run.JobID != "" && existing.JobID != "" && existing.JobID != run.JobID {
				continue
			}
			if run.JobID != "" && existing.JobID == run.JobID {
				return i, true
			}
			if run.DelegateID != "" && existing.DelegateID == run.DelegateID && existing.JobID == run.JobID {
				return i, true
			}
		}
		if run.OriginItemID != "" && msg.ItemID == run.OriginItemID {
			return i, true
		}
		if run.OriginToolCallID != "" && msg.ToolCallID == run.OriginToolCallID {
			return i, true
		}
	}
	return 0, false
}

func hasDelegateJobSignal(run SubagentRunInfo) bool {
	return isDelegateJobType(run.JobType) || run.DelegateID != "" || isBackgroundShellRun(run)
}

func isDelegateJobType(jobType string) bool {
	return strings.EqualFold(strings.TrimSpace(jobType), "delegate")
}

func isDelegateToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "delegate", "delegate_send":
		return true
	default:
		return false
	}
}

func subagentRunFromJob(job appwire.SerfJobInfo) SubagentRunInfo {
	return SubagentRunInfo{
		DelegateID:       strings.TrimSpace(job.DelegateID),
		JobID:            strings.TrimSpace(job.JobID),
		JobType:          strings.TrimSpace(job.JobType),
		Status:           strings.TrimSpace(job.Status),
		Reason:           strings.TrimSpace(job.Reason),
		Background:       job.Background,
		Command:          strings.TrimSpace(job.Command),
		Task:             strings.TrimSpace(job.Task),
		TranscriptRef:    strings.TrimSpace(job.TranscriptRef),
		OriginTurnID:     strings.TrimSpace(job.OriginTurnID),
		OriginToolCallID: strings.TrimSpace(job.OriginToolCallID),
		OriginItemID:     strings.TrimSpace(job.OriginItemID),
		OutputBytes:      job.OutputBytes,
	}
}

func subagentRunFromToolItem(item appwire.ThreadItem) SubagentRunInfo {
	raw := item.Raw
	if len(raw) == 0 && strings.TrimSpace(item.Output) != "" {
		raw = json.RawMessage(item.Output)
	}
	var payload struct {
		DelegateID       string `json:"delegate_id"`
		JobID            string `json:"job_id"`
		StartedJobID     string `json:"started_job_id"`
		CurrentJobID     string `json:"current_job_id"`
		LatestJobID      string `json:"latest_job_id"`
		Type             string `json:"type"`
		Status           string `json:"status"`
		Reason           string `json:"reason"`
		Task             string `json:"task"`
		TranscriptRef    string `json:"transcript_ref"`
		OriginTurnID     string `json:"origin_turn_id"`
		OriginToolCallID string `json:"origin_tool_call_id"`
		OriginItemID     string `json:"origin_item_id"`
		OutputBytes      int64  `json:"output_bytes"`
		TotalBytes       int64  `json:"total_bytes"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &payload) != nil {
		return SubagentRunInfo{}
	}
	jobID := firstNonEmptyString(payload.JobID, payload.StartedJobID, payload.CurrentJobID, payload.LatestJobID)
	outputBytes := payload.OutputBytes
	if outputBytes == 0 {
		outputBytes = payload.TotalBytes
	}
	return SubagentRunInfo{
		DelegateID:       strings.TrimSpace(payload.DelegateID),
		JobID:            strings.TrimSpace(jobID),
		JobType:          strings.TrimSpace(payload.Type),
		Status:           strings.TrimSpace(payload.Status),
		Reason:           strings.TrimSpace(payload.Reason),
		Task:             strings.TrimSpace(payload.Task),
		TranscriptRef:    strings.TrimSpace(payload.TranscriptRef),
		OriginTurnID:     strings.TrimSpace(payload.OriginTurnID),
		OriginToolCallID: strings.TrimSpace(payload.OriginToolCallID),
		OriginItemID:     strings.TrimSpace(payload.OriginItemID),
		OutputBytes:      outputBytes,
	}
}

func mergeSubagentRun(dst *SubagentRunInfo, src SubagentRunInfo) SubagentRunInfo {
	if dst == nil {
		return src
	}
	out := *dst
	if src.DelegateID != "" {
		out.DelegateID = src.DelegateID
	}
	if src.JobID != "" {
		out.JobID = src.JobID
	}
	if src.JobType != "" {
		out.JobType = src.JobType
	}
	if src.Status != "" {
		out.Status = src.Status
	}
	if src.Reason != "" {
		out.Reason = src.Reason
	}
	if src.Task != "" {
		out.Task = src.Task
	}
	if src.TranscriptRef != "" {
		out.TranscriptRef = src.TranscriptRef
	}
	if src.OriginTurnID != "" {
		out.OriginTurnID = src.OriginTurnID
	}
	if src.OriginToolCallID != "" {
		out.OriginToolCallID = src.OriginToolCallID
	}
	if src.OriginItemID != "" {
		out.OriginItemID = src.OriginItemID
	}
	if src.OutputBytes != 0 {
		out.OutputBytes = src.OutputBytes
	}
	return out
}

func subagentTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "done", "failed", "cancelled", "stopped", "succeeded":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func systemMessageItemText(item appwire.ThreadItem) string {
	text := strings.TrimSpace(item.Text)
	if text == "" {
		return ""
	}
	description := strings.TrimSpace(item.Description)
	if description == "" {
		return text
	}
	if description == "Hook" {
		return strings.Join(strings.Fields(text), " ")
	}
	return description + "\n" + text
}

func userMessageItemText(item appwire.ThreadItem) string {
	if strings.TrimSpace(item.Text) != "" {
		return item.Text
	}
	switch len(item.Images) {
	case 0:
		return ""
	case 1:
		return ImageItemsPlaceholder(item.Images)
	default:
		return ImageItemsPlaceholder(item.Images)
	}
}

func ImageItemsPlaceholder(images []appwire.InputItem) string {
	switch len(images) {
	case 0:
		return ""
	case 1:
		return "[image]"
	default:
		return fmt.Sprintf("[%d images]", len(images))
	}
}

func (r *TranscriptReducer) activeMessageIndex(item appwire.ThreadItem) (int, bool) {
	if item.ID == "" {
		return 0, false
	}
	idx, ok := r.activeMessages[item.ID]
	if !ok || idx < 0 || idx >= len(r.messages) || r.messages[idx].Kind != MsgAssistant {
		return 0, false
	}
	if !turnScopeMatches(r.messages[idx].TurnID, item.TurnID, r.messages[idx].TurnIndex, TurnIndexFromID(item.TurnID)) {
		return 0, false
	}
	return idx, true
}

func (r *TranscriptReducer) messageIndexByItemID(itemID string, kind MessageKind, turnID string, turnIndex int) (int, bool) {
	if itemID == "" {
		return 0, false
	}
	for i := range r.messages {
		if r.messages[i].Kind == kind && r.messages[i].ItemID == itemID && turnScopeMatches(r.messages[i].TurnID, turnID, r.messages[i].TurnIndex, turnIndex) {
			return i, true
		}
	}
	return 0, false
}

func (r *TranscriptReducer) pendingUserEchoIndex(text string) (int, bool) {
	for i := len(r.messages) - 1; i >= 0; i-- {
		msg := r.messages[i]
		if msg.Kind != MsgUser {
			continue
		}
		if msg.Pending {
			continue
		}
		if msg.Failed || msg.PendingID != 0 {
			continue
		}
		if msg.ItemID == "" && msg.Text == text {
			return i, true
		}
	}
	return 0, false
}

func (r *TranscriptReducer) rememberActiveMessage(item appwire.ThreadItem, idx int) {
	if item.ID != "" {
		r.activeMessages[item.ID] = idx
	}
}

func (r *TranscriptReducer) clearActiveMessage(item appwire.ThreadItem) {
	if item.ID != "" {
		delete(r.activeMessages, item.ID)
	}
}

func (r *TranscriptReducer) activeToolIndex(item appwire.ThreadItem) (int, bool) {
	if item.ID != "" {
		if idx, ok := r.activeTools[item.ID]; ok && idx < len(r.messages) {
			if !turnScopeMatches(r.messages[idx].TurnID, item.TurnID, r.messages[idx].TurnIndex, TurnIndexFromID(item.TurnID)) {
				return 0, false
			}
			return idx, true
		}
	}
	if item.CallID != "" {
		if idx, ok := r.activeTools[item.CallID]; ok && idx < len(r.messages) {
			return idx, true
		}
	}
	return 0, false
}

func (r *TranscriptReducer) toolIndex(item appwire.ThreadItem, turnIndex int) (int, bool) {
	if idx, ok := r.activeToolIndex(item); ok {
		return idx, true
	}
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != MsgTool || msg.Tool == nil {
			continue
		}
		if item.ID != "" && msg.ItemID == item.ID && turnScopeMatches(msg.TurnID, item.TurnID, msg.TurnIndex, turnIndex) {
			return i, true
		}
		if item.CallID != "" && msg.ToolCallID == item.CallID {
			return i, true
		}
	}
	return 0, false
}

func turnScopeMatches(existingID, incomingID string, existing, incoming int) bool {
	if existingID != "" && incomingID != "" {
		return existingID == incomingID
	}
	return existing == 0 || incoming == 0 || existing == incoming
}

func (r *TranscriptReducer) rememberActiveTool(item appwire.ThreadItem, idx int) {
	if item.ID != "" {
		r.activeTools[item.ID] = idx
	}
	if item.CallID != "" {
		r.activeTools[item.CallID] = idx
	}
}

func (r *TranscriptReducer) clearActiveTool(item appwire.ThreadItem) {
	if item.ID != "" {
		delete(r.activeTools, item.ID)
	}
	if item.CallID != "" {
		delete(r.activeTools, item.CallID)
	}
}

// AppendPendingSteering renders an optimistic STEERING placeholder
// while the daemon's STEERING_INJECTED event is in flight. Returns
// the PendingID for later mark/remove operations.
func (r *TranscriptReducer) AppendPendingSteering(text string) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, ChatMessage{
		Kind:      MsgSteering,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// AppendPendingUser renders an optimistic USER_INPUT placeholder.
// Today the renderer already does silent user-message echo via
// ApplyUserMessageEcho; this helper extends that to set Pending so
// the spinner prefix renders.
func (r *TranscriptReducer) AppendPendingUser(text string) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, ChatMessage{
		Kind:      MsgUser,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// AppendPendingDrain renders the single transient drain-as-steer chip
// that collapses queued entries while the daemon merges them into one
// STEERING_INJECTED event.
func (r *TranscriptReducer) AppendPendingDrain(queuedCount int) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, ChatMessage{
		Kind:      MsgSteering,
		Text:      fmt.Sprintf("draining %d → steering", queuedCount),
		Pending:   true,
		PendingID: id,
	})
	return id
}

func (r *TranscriptReducer) MarkPendingFailed(id int64, reason string) {
	for i := range r.messages {
		if r.messages[i].PendingID != id {
			continue
		}
		r.messages[i].Pending = false
		r.messages[i].Failed = true
		r.messages[i].Reason = reason
		return
	}
}

func (r *TranscriptReducer) RemovePending(id int64) {
	for i := range r.messages {
		if r.messages[i].PendingID != id {
			continue
		}
		r.messages = append(r.messages[:i], r.messages[i+1:]...)
		return
	}
}

var pendingMessageIDCounter int64

func nextPendingMessageID() int64 {
	pendingMessageIDCounter++
	return pendingMessageIDCounter
}
