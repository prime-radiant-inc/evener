package agent

import (
	"slices"

	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/llm"
)

// escapeModelAuthoredTurnFrames returns msg with line-start harness input
// frames defused in the text of an assistant-role turn, so a request the
// session serves cannot hand the model its own earlier fabrication back with
// the spelling a harness-written frame or a real reply opens with (#6493).
//
// Only the assistant role is escaped. RoleUser is the human partner speaking —
// their text is delivered as they typed it, and an escaped copy there would
// change what the model reads as their words — and a tool role is a result the
// harness itself recorded. Assistant text is the one role whose bytes the model
// invented, and the one that must not come back looking like input.
//
// A compaction turn is the exception to the role rule, and the reason this
// cannot be decided by role alone: a checkpoint or summary is user-role, yet
// part of its body is the model's own text (working notes, agent replies,
// earlier summaries, the summary's own prose). escapeCompactionTurnFrames
// escapes those sections and leaves the harness framing and the partner's
// blocks alone.
//
// The turn is never modified: the caller gets a message whose changed text
// parts are copies, so the transcript keeps exactly what the model wrote.
func escapeModelAuthoredTurnFrames(msg llm.Message) llm.Message {
	if msg.Role != llm.RoleAssistant {
		return msg
	}
	var parts []llm.ContentPart
	for i, p := range msg.Content {
		if p.Kind != llm.ContentText || p.Machinery || p.Text == "" {
			continue
		}
		escaped := sysmsg.EscapeModelAuthoredFrames(p.Text)
		if escaped == p.Text {
			continue
		}
		if parts == nil {
			parts = slices.Clone(msg.Content)
		}
		parts[i].Text = escaped
	}
	if parts == nil {
		return msg
	}
	msg.Content = parts
	return msg
}

// escapeCompactionTurnFrames returns a checkpoint or summary turn's message
// with frames defused in the model-authored sections of its body, leaving the
// harness framing and the human partner's blocks byte for byte. The stored turn
// keeps every byte.
func escapeCompactionTurnFrames(msg llm.Message) llm.Message {
	var parts []llm.ContentPart
	for i, p := range msg.Content {
		if p.Kind != llm.ContentText || p.Machinery || p.Text == "" {
			continue
		}
		escaped := sysmsg.EscapeCompactionModelSections(p.Text)
		if escaped == p.Text {
			continue
		}
		if parts == nil {
			parts = slices.Clone(msg.Content)
		}
		parts[i].Text = escaped
	}
	if parts == nil {
		return msg
	}
	msg.Content = parts
	return msg
}
