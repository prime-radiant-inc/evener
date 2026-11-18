package agent

import (
	"slices"
	"strings"

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
	var indexes []int
	var texts []string
	for i, p := range msg.Content {
		if p.Kind != llm.ContentText || p.Machinery || p.Text == "" {
			continue
		}
		indexes = append(indexes, i)
		texts = append(texts, p.Text)
	}
	if len(indexes) == 0 {
		return msg
	}

	// Scan the text as the adapter will assemble it, not part by part. The
	// Chat Completions adapter joins an assistant's text parts with a newline
	// before the model reads them, so a fence one part opens and the next
	// closes is a single fenced block on the wire. Escaping each part on its
	// own would read the closing fence as a fresh opener and then treat the
	// frame after it as fenced, leaving it unescaped in the text the model
	// receives (#6493 review).
	joined := strings.Join(texts, "\n")
	escaped := sysmsg.EscapeModelAuthoredFrames(joined)
	if escaped == joined {
		return msg
	}

	// Escaping rewrites a frame's opening character and never adds or removes
	// a line break, so the escaped text splits back into the same per-part
	// line counts it was joined from.
	lines := strings.Split(escaped, "\n")
	var parts []llm.ContentPart
	at := 0
	for k, text := range texts {
		n := strings.Count(text, "\n") + 1
		if at+n > len(lines) {
			// The escape changed the line structure; leave the turn as stored
			// rather than hand back text split somewhere it was not.
			return msg
		}
		segment := strings.Join(lines[at:at+n], "\n")
		at += n
		if segment == text {
			continue
		}
		if parts == nil {
			parts = slices.Clone(msg.Content)
		}
		parts[indexes[k]].Text = segment
	}
	if parts == nil {
		return msg
	}
	msg.Content = parts
	return msg
}
