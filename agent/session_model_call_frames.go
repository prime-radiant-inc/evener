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
func escapeModelAuthoredTurnFrames(msg llm.Message, protocol string) llm.Message {
	if msg.Role != llm.RoleAssistant {
		return msg
	}
	var indexes []int
	var texts []string
	for i, p := range msg.Content {
		// A machinery part is content this session manufactured, and escaping
		// it would rewrite the harness's own note. Every genuine one is
		// user-role: the constructors that set the flag (llm.UserMachinery,
		// llm.MachineryText) are used on user-role messages only, so the
		// early return above already keeps them out of this function.
		//
		// On an assistant part the flag cannot be genuine, because nothing in
		// the tree builds one. It can only come from inferPreFlagMachinery's
		// decode-time guess for a transcript written before the flag existed,
		// which classifies any text part shaped like one whole
		// <system-notification> block — the exact shape a model invents when
		// it imitates one. Skipping that part would replay the model's
		// imitation verbatim, so the flag is not consulted here (#6493
		// review).
		if p.Kind != llm.ContentText || p.Text == "" {
			continue
		}
		indexes = append(indexes, i)
		texts = append(texts, p.Text)
	}
	if len(indexes) == 0 {
		return msg
	}

	// Scan the text as the target adapter will assemble it. The boundaries are
	// the adapter's, not the history's: Responses emits one message per text
	// phase, while every other protocol joins the turn's text into one message
	// even when the stored parts still carry phase labels from a session that
	// ran against Responses. Escaping against the wrong boundaries either reads
	// a closing fence as a fresh opener or lets an example's delimiter close a
	// fence the adapter does not put in the same message (#6493 review).
	var parts []llm.ContentPart
	phases := make([]string, len(texts))
	for k := range texts {
		phases[k] = msg.Content[indexes[k]].Phase
	}
	// The scan itself is shared with checkpoint capture, so the two readers
	// cannot disagree about where a fence is (#6493 review).
	segments := sysmsg.TextSegments(phases, protocol)
	escapedSegments := sysmsg.EscapePhasedText(texts, phases, protocol)
	for s, span := range segments {
		segmentTexts := texts[span[0]:span[1]]
		joined := strings.Join(segmentTexts, "\n")
		escaped := escapedSegments[s]
		if escaped == joined {
			continue
		}
		// Escaping rewrites a frame's opening character and never adds or
		// removes a line break, so the escaped text splits back into the same
		// per-part line counts it was joined from.
		lines := strings.Split(escaped, "\n")
		at := 0
		for k, text := range segmentTexts {
			n := strings.Count(text, "\n") + 1
			if at+n > len(lines) {
				// The escape changed the line structure; leave the turn as
				// stored rather than hand back text split somewhere it was not.
				return msg
			}
			piece := strings.Join(lines[at:at+n], "\n")
			at += n
			if piece == text {
				continue
			}
			if parts == nil {
				parts = slices.Clone(msg.Content)
			}
			parts[indexes[span[0]+k]].Text = piece
		}
	}
	if parts == nil {
		return msg
	}
	msg.Content = parts
	return msg
}
