package main

import (
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
		if strings.TrimSpace(item.Text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, msgUser); ok {
				r.messages[idx].Text = item.Text
				r.messages[idx].TurnIndex = turnIndex
				return
			}
			if idx, ok := r.pendingUserEchoIndex(item.Text); ok {
				r.messages[idx].Text = item.Text
				r.messages[idx].TurnIndex = turnIndex
				r.messages[idx].ItemID = item.ID
				return
			}
			r.messages = append(r.messages, chatMessage{Kind: msgUser, Text: item.Text, TurnIndex: turnIndex, ItemID: item.ID})
		}
	case "agent_message":
		if strings.TrimSpace(item.Text) != "" {
			if idx, ok := r.messageIndexByItemID(item.ID, msgAssistant); ok {
				r.messages[idx].Text = item.Text
				if completed {
					r.clearActiveMessage(item)
				}
			} else if idx, ok := r.activeMessageIndex(item); ok {
				r.messages[idx].Text = item.Text
				if completed {
					r.clearActiveMessage(item)
				}
			} else if len(r.messages) > 0 && r.messages[len(r.messages)-1].Kind == msgAssistant {
				r.messages[len(r.messages)-1].Text = item.Text
				r.messages[len(r.messages)-1].ItemID = item.ID
			} else {
				idx := len(r.messages)
				r.messages = append(r.messages, chatMessage{Kind: msgAssistant, Text: item.Text, ItemID: item.ID})
				if !completed {
					r.rememberActiveMessage(item, idx)
				}
			}
		}
	case "tool_call":
		done := threadItemToolDone(item, completed)
		if idx, ok := r.toolIndex(item); ok {
			if item.ID != "" {
				r.messages[idx].ItemID = item.ID
			}
			if item.CallID != "" {
				r.messages[idx].ToolCallID = item.CallID
			}
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
		r.messages = append(r.messages, chatMessage{Kind: msgTool, ItemID: item.ID, ToolCallID: item.CallID, Tool: info})
		if !done {
			r.rememberActiveTool(item, idx)
		}
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

func (r *hubTranscriptReducer) messageIndexByItemID(itemID string, kind messageKind) (int, bool) {
	if itemID == "" {
		return 0, false
	}
	for i := range r.messages {
		if r.messages[i].Kind == kind && r.messages[i].ItemID == itemID {
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

func (r *hubTranscriptReducer) toolIndex(item appwire.ThreadItem) (int, bool) {
	if idx, ok := r.activeToolIndex(item); ok {
		return idx, true
	}
	for i := range r.messages {
		msg := r.messages[i]
		if msg.Kind != msgTool || msg.Tool == nil {
			continue
		}
		if item.ID != "" && msg.ItemID == item.ID {
			return i, true
		}
		if item.CallID != "" && msg.ToolCallID == item.CallID {
			return i, true
		}
	}
	return 0, false
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
