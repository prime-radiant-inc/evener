package agent

import (
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/llm"
)

// escapeModelAuthoredTurnFramesReporting returns msg with line-start harness
// input frames defused in the text of an assistant-role turn, so a request the
// session serves cannot hand the model its own earlier fabrication back with
// the spelling a harness-written frame or a real reply opens with (#6493). It
// also reports the target's reasoning policy and whether it changed any part of
// the message.
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
//
// When the target renders replayed reasoning as ordinary assistant text, its
// adapter prepends that reasoning to the message body, so the reasoning is part
// of what the model reads: escaping only the text parts would leave a frame the
// model wrote in its reasoning unescaped in the body it receives (#6493
// review). The stored reasoning is untouched; only the projected copy is
// rewritten, exactly as for text parts.
//
// changed is the whole answer to "was this message rewritten", and a caller
// that decides whether to keep the result must use it. Comparing the escaped
// message's Text() with the original's cannot see a rewrite confined to
// reasoning — Message.Text() reads text parts only — so a fallback target that
// promotes reasoning would discard an escape that only touched a thinking
// block, and its adapter would prepend the original frame to the body (#6493
// review).
func escapeModelAuthoredTurnFramesReporting(msg llm.Message, protocol string, promotesReasoning bool) (llm.Message, bool) {
	if msg.Role != llm.RoleAssistant {
		return msg, false
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
		// A tool-call round can carry reasoning and no ordinary text at all.
		// Then the reasoning IS the body, with no adapter seam to step over:
		// taking the seam path below would split zero text parts and discard
		// the rewrite, and a promoting adapter would prepend the raw frame
		// (#6493 review).
		if len(texts) == 0 {
			escaped := sysmsg.EscapeModelAuthoredFrames(reasoningBody)
			if escaped == reasoningBody {
				return msg, false
			}
			reasoningPieces, ok := sysmsg.SplitJoinedEscape(escaped, reasoningTexts)
			if !ok {
				return msg, false
			}
			parts := slices.Clone(msg.Content)
			changed := false
			for k, piece := range reasoningPieces {
				if piece == reasoningTexts[k] {
					continue
				}
				thinking := *msg.Content[reasoningIndexes[k]].Thinking
				thinking.Text = piece
				parts[reasoningIndexes[k]].Thinking = &thinking
				changed = true
			}
			if !changed {
				return msg, false
			}
			msg.Content = parts
			return msg, changed
		}
		body := reasoningBody + "\n\n" + strings.Join(texts, "\n")
		escaped := sysmsg.EscapeModelAuthoredFrames(body)
		if escaped == body {
			return msg, false
		}
		lines := strings.Split(escaped, "\n")
		at := strings.Count(reasoningBody, "\n") + 1
		// The escape never adds or removes a line break, so the seam is where
		// it was: the reasoning's lines, then the blank line the adapter
		// inserts, then the text.
		if at+1 > len(lines) || lines[at] != "" {
			return msg, false
		}
		// Split the escaped body back the way it was joined: one piece per
		// reasoning block and one per text part, by each one's own line count.
		// Writing the whole escaped reasoning body into the first block leaves
		// the later blocks in place, and the promoting adapter joins every block
		// again — duplicating the later reasoning and replaying any raw frame it
		// held in its original spelling (#6493 review).
		reasoningPieces, ok := sysmsg.SplitJoinedEscape(strings.Join(lines[:at], "\n"), reasoningTexts)
		if !ok {
			return msg, false
		}
		textPieces, ok := sysmsg.SplitJoinedEscape(strings.Join(lines[at+1:], "\n"), texts)
		if !ok {
			return msg, false
		}
		parts := slices.Clone(msg.Content)
		changed := false
		for k, piece := range reasoningPieces {
			if piece == reasoningTexts[k] {
				continue
			}
			thinking := *msg.Content[reasoningIndexes[k]].Thinking
			thinking.Text = piece
			parts[reasoningIndexes[k]].Thinking = &thinking
			changed = true
		}
		for k, piece := range textPieces {
			if piece != texts[k] {
				parts[indexes[k]].Text = piece
				changed = true
			}
		}
		msg.Content = parts
		return msg, changed
	}
	if len(indexes) == 0 {
		return msg, false
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
		pieces, ok := sysmsg.SplitJoinedEscape(escaped, segmentTexts)
		if !ok {
			// The escape changed the line structure; leave the turn as
			// stored rather than hand back text split somewhere it was not.
			return msg, false
		}
		for k, piece := range pieces {
			if piece == segmentTexts[k] {
				continue
			}
			if parts == nil {
				parts = slices.Clone(msg.Content)
			}
			parts[indexes[span[0]+k]].Text = piece
		}
	}
	// An adapter that serializes each part as its own block gives a part a
	// reading of its own, so the run scan above is followed by one pass per
	// part. Which protocols those are is the shared predicate's answer, not this
	// function's: Responses splits by Phase while Anthropic and Google emit one
	// block per part and ignore Phase, and a scan that knew only about Responses
	// left the other two sending an opener its own reader would have escaped
	// (#6493 review).
	//
	// The allocation sits inside the loop because this pass can be the first to
	// change anything — the case it exists for. A fence the run reading treats as
	// open leaves the segment pass above with nothing to rewrite, and only the
	// per-part reading sees the frame in the next part.
	if sysmsg.SerializesPartsSeparately(protocol) {
		partTexts := make([]string, len(indexes))
		for k, at := range indexes {
			partTexts[k] = msg.Content[at].Text
			if parts != nil {
				partTexts[k] = parts[at].Text
			}
		}
		for k, defused := range sysmsg.EscapePartsIndividually(partTexts) {
			if defused == partTexts[k] {
				continue
			}
			if parts == nil {
				parts = slices.Clone(msg.Content)
			}
			parts[indexes[k]].Text = defused
		}
	}
	if parts == nil {
		return msg, false
	}
	msg.Content = parts
	return msg, true
}
