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
	return escapeModelAuthoredTurnFramesFor(msg, protocol, false)
}

// escapeModelAuthoredTurnFramesFor is escapeModelAuthoredTurnFrames with the
// target's reasoning policy. When the target renders replayed reasoning as
// ordinary assistant text, its adapter prepends that reasoning to the message
// body, so the reasoning is part of what the model reads: escaping only the
// text parts would leave a frame the model wrote in its reasoning unescaped in
// the body it receives (#6493 review). The stored reasoning is untouched; only
// the projected copy is rewritten, exactly as for text parts.
func escapeModelAuthoredTurnFramesFor(msg llm.Message, protocol string, promotesReasoning bool) llm.Message {
	if msg.Role != llm.RoleAssistant {
		return msg
	}
	// A machinery part is content this session manufactured, and escaping it
	// would rewrite the harness's own note. Every genuine one is user-role: the
	// constructors that set the flag (llm.UserMachinery, llm.MachineryText) are
	// used on user-role messages only, so the early return above already keeps
	// them out of this function.
	//
	// On an assistant part the flag cannot be genuine, because nothing in the
	// tree builds one. It can only come from inferPreFlagMachinery's decode-time
	// guess for a transcript written before the flag existed, which classifies
	// any text part shaped like one whole <system-notification> block — the
	// exact shape a model invents when it imitates one. Skipping that part would
	// replay the model's imitation verbatim, so the flag is not consulted here
	// (#6493 review).
	//
	// The parts this function reads are the parts the adapter sends, by one
	// shared rule: a whitespace-only unphased part the adapter drops must not
	// split the scan's phase run, or a fence one part opens and the next closes
	// reads as two blocks and the frame after the close is spared (#6493
	// review).
	indexes, texts, textPhases := sysmsg.WireTextParts(msg)
	var reasoningIndexes []int
	var reasoningTexts []string
	if promotesReasoning {
		for i, p := range msg.Content {
			if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
				reasoningIndexes = append(reasoningIndexes, i)
				reasoningTexts = append(reasoningTexts, p.Thinking.Text)
			}
		}
	}
	// A promoting target puts its reasoning in front of the message body, so
	// fence state runs from one into the other and the two are one body to the
	// reader. Escaping them separately is what let a fence opened in reasoning
	// hide a frame in the text (#6493 review).
	if len(reasoningIndexes) > 0 {
		reasoningBody := strings.Join(reasoningTexts, "\n")
		body := reasoningBody + "\n\n" + strings.Join(texts, "\n")
		escaped := sysmsg.EscapeModelAuthoredFrames(body)
		if escaped == body {
			return msg
		}
		lines := strings.Split(escaped, "\n")
		at := strings.Count(reasoningBody, "\n") + 1
		// The escape never adds or removes a line break, so the seam is where
		// it was: the reasoning's lines, then the blank line the adapter
		// inserts, then the text.
		if at+1 > len(lines) || lines[at] != "" {
			return msg
		}
		parts := slices.Clone(msg.Content)
		reasoningPiece := strings.Join(lines[:at], "\n")
		if reasoningPiece != reasoningTexts[0] {
			thinking := *msg.Content[reasoningIndexes[0]].Thinking
			thinking.Text = reasoningPiece
			parts[reasoningIndexes[0]].Thinking = &thinking
		}
		textLines := lines[at+1:]
		cursor := 0
		for k, text := range texts {
			n := strings.Count(text, "\n") + 1
			if cursor+n > len(textLines) {
				return msg
			}
			piece := strings.Join(textLines[cursor:cursor+n], "\n")
			cursor += n
			if piece != text {
				parts[indexes[k]].Text = piece
			}
		}
		msg.Content = parts
		return msg
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
	phases := textPhases
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
