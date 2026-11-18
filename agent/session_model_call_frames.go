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
// A checkpoint or summary turn is user-role and so is not reached here, even
// though part of its body is the model's own text. Escaping it would mean
// deciding authorship by scanning for structural headings, and a model can
// write those headings itself: the scanner and the compaction reader then
// disagree about the same bytes. That is tracked separately as #6734, whose
// direction is provenance carried on the content part rather than re-derived
// from text.
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
