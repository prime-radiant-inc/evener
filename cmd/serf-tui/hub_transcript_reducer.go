package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

type hubTranscriptReducer struct {
	messages       []chatMessage
	activeTools    map[string]int
	activeMessages map[string]int
}

func newHubTranscriptReducer(messages []chatMessage, activeTools, activeMessages map[string]int) hubTranscriptReducer {
	if activeTools == nil {
		activeTools = make(map[string]int)
	}
	if activeMessages == nil {
		activeMessages = make(map[string]int)
	}
	return hubTranscriptReducer{
		messages:       messages,
		activeTools:    activeTools,
		activeMessages: activeMessages,
	}
}

func (r *hubTranscriptReducer) applyAgentMessageDelta(itemID, delta string) {
	if delta == "" {
		return
	}
	if idx, ok := r.activeMessageIndex(appwire.ThreadItem{ID: itemID}); ok {
		r.messages[idx].Text += delta
		return
	}
	if len(r.messages) > 0 && r.messages[len(r.messages)-1].Kind == msgAssistant {
		idx := len(r.messages) - 1
		r.messages[idx].Text += delta
		if itemID != "" {
			r.rememberActiveMessage(appwire.ThreadItem{ID: itemID}, idx)
		}
		return
	}
	idx := len(r.messages)
	r.messages = append(r.messages, chatMessage{Kind: msgAssistant, Text: delta, ItemID: itemID})
	if itemID != "" {
		r.rememberActiveMessage(appwire.ThreadItem{ID: itemID}, idx)
	}
}

func (r *hubTranscriptReducer) applyUserMessageEcho(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r.messages = append(r.messages, chatMessage{Kind: msgUser, Text: text})
}

func (r *hubTranscriptReducer) applyToolOutputDelta(itemID, delta string) {
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
	r.messages = append(r.messages, chatMessage{
		Kind:   msgTool,
		ItemID: itemID,
		Tool: &toolCallInfo{
			Output: delta,
		},
	})
	r.rememberActiveTool(item, idx)
}

func (r *hubTranscriptReducer) applyThreadItem(item appwire.ThreadItem, turnIndex int, completed bool) {
	switch item.Type {
	case "user_message":
		text := userMessageItemText(item)
		if strings.TrimSpace(text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, msgUser, item.TurnID, turnIndex); ok {
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
			r.messages = append(r.messages, chatMessage{Kind: msgUser, Text: text, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID})
		}
	case "agent_message":
		if strings.TrimSpace(item.Text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, msgAssistant, item.TurnID, turnIndex); ok {
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
			} else if len(r.messages) > 0 && r.messages[len(r.messages)-1].Kind == msgAssistant && turnScopeMatches(r.messages[len(r.messages)-1].TurnID, item.TurnID, r.messages[len(r.messages)-1].TurnIndex, turnIndex) {
				r.messages[len(r.messages)-1].Text = item.Text
				r.messages[len(r.messages)-1].ItemID = item.ID
				r.messages[len(r.messages)-1].TurnID = item.TurnID
				r.messages[len(r.messages)-1].TurnIndex = turnIndex
			} else {
				idx := len(r.messages)
				r.messages = append(r.messages, chatMessage{Kind: msgAssistant, Text: item.Text, TurnID: item.TurnID, ItemID: item.ID, TurnIndex: turnIndex})
				if !completed {
					r.rememberActiveMessage(item, idx)
				}
			}
		}
	case "tool_call":
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
		r.messages = append(r.messages, chatMessage{Kind: msgTool, TurnID: item.TurnID, TurnIndex: turnIndex, ItemID: item.ID, ToolCallID: item.CallID, Tool: info})
		if !done {
			r.rememberActiveTool(item, idx)
		}
	}
}

func userMessageItemText(item appwire.ThreadItem) string {
	if strings.TrimSpace(item.Text) != "" {
		return item.Text
	}
	switch len(item.Images) {
	case 0:
		return ""
	case 1:
		return imageItemsPlaceholder(item.Images)
	default:
		return imageItemsPlaceholder(item.Images)
	}
}

func imageItemsPlaceholder(images []appwire.InputItem) string {
	switch len(images) {
	case 0:
		return ""
	case 1:
		return "[image]"
	default:
		return fmt.Sprintf("[%d images]", len(images))
	}
}

func (r *hubTranscriptReducer) activeMessageIndex(item appwire.ThreadItem) (int, bool) {
	if item.ID == "" {
		return 0, false
	}
	idx, ok := r.activeMessages[item.ID]
	if !ok || idx < 0 || idx >= len(r.messages) || r.messages[idx].Kind != msgAssistant {
		return 0, false
	}
	return idx, true
}

func (r *hubTranscriptReducer) messageIndexByItemID(itemID string, kind messageKind, turnID string, turnIndex int) (int, bool) {
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

func (r *hubTranscriptReducer) pendingUserEchoIndex(text string) (int, bool) {
	for i := len(r.messages) - 1; i >= 0; i-- {
		msg := r.messages[i]
		if msg.Kind != msgUser {
			continue
		}
		if msg.Pending {
			continue
		}
		if msg.ItemID == "" && msg.Text == text {
			return i, true
		}
	}
	return 0, false
}

func (r *hubTranscriptReducer) rememberActiveMessage(item appwire.ThreadItem, idx int) {
	if item.ID != "" {
		r.activeMessages[item.ID] = idx
	}
}

func (r *hubTranscriptReducer) clearActiveMessage(item appwire.ThreadItem) {
	if item.ID != "" {
		delete(r.activeMessages, item.ID)
	}
}

func (r *hubTranscriptReducer) activeToolIndex(item appwire.ThreadItem) (int, bool) {
	if item.ID != "" {
		if idx, ok := r.activeTools[item.ID]; ok && idx < len(r.messages) {
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

func (r *hubTranscriptReducer) toolIndex(item appwire.ThreadItem, turnIndex int) (int, bool) {
	if idx, ok := r.activeToolIndex(item); ok {
		return idx, true
	}
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != msgTool || msg.Tool == nil {
			continue
		}
		if item.ID != "" && msg.ItemID == item.ID && turnScopeMatches(msg.TurnID, item.TurnID, msg.TurnIndex, turnIndex) {
			return i, true
		}
		if item.CallID != "" && msg.ToolCallID == item.CallID && turnScopeMatches(msg.TurnID, item.TurnID, msg.TurnIndex, turnIndex) {
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

func (r *hubTranscriptReducer) rememberActiveTool(item appwire.ThreadItem, idx int) {
	if item.ID != "" {
		r.activeTools[item.ID] = idx
	}
	if item.CallID != "" {
		r.activeTools[item.CallID] = idx
	}
}

func (r *hubTranscriptReducer) clearActiveTool(item appwire.ThreadItem) {
	if item.ID != "" {
		delete(r.activeTools, item.ID)
	}
	if item.CallID != "" {
		delete(r.activeTools, item.CallID)
	}
}

// appendPendingSteering renders an optimistic STEERING placeholder
// while the daemon's STEERING_INJECTED event is in flight. Returns
// the PendingID for later mark/remove operations.
func (r *hubTranscriptReducer) appendPendingSteering(text string) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgSteering,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// appendPendingUser renders an optimistic USER_INPUT placeholder.
// Today the renderer already does silent user-message echo via
// applyUserMessageEcho; this helper extends that to set Pending so
// the spinner prefix renders.
func (r *hubTranscriptReducer) appendPendingUser(text string) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgUser,
		Text:      text,
		Pending:   true,
		PendingID: id,
	})
	return id
}

// appendPendingDrain renders the single transient drain-as-steer chip
// that collapses queued entries while the daemon merges them into one
// STEERING_INJECTED event.
func (r *hubTranscriptReducer) appendPendingDrain(queuedCount int) int64 {
	id := nextPendingMessageID()
	r.messages = append(r.messages, chatMessage{
		Kind:      msgSteering,
		Text:      fmt.Sprintf("draining %d → steering", queuedCount),
		Pending:   true,
		PendingID: id,
	})
	return id
}

func (r *hubTranscriptReducer) markPendingFailed(id int64, reason string) {
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

func (r *hubTranscriptReducer) removePending(id int64) {
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
