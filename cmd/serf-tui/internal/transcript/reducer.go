package transcript

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/internal/appwire"
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
	case "agentMessage":
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
