package agent

import (
	"slices"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// escapeNoteHandoffBody escapes the model's own note in a note-handoff
// steering turn's model-facing copy.
//
// The escape is keyed on provenance, not on role. A note-handoff turn is
// user-role, so the assistant-only rule in
// escapeModelAuthoredTurnFramesReporting cannot reach it, yet its body is
// text the model wrote: automatic note elicitation and compact_context both
// pin a note this session authored and publish it after a compaction as
// steering reading `noteHandoffPrefix + "\n" + note` (#6493 review). A frame
// in that note therefore arrived with the spelling a harness frame or a real
// reply opens with, which is exactly what the user-turn channel must not
// carry.
//
// Only a turn whose SteeringKind says it carries the model's own note is
// touched. Genuine partner steering has no note-handoff kind, and the stored
// turn keeps the note's bytes; this returns a copy.
func escapeNoteHandoffBody(t schema.Turn, msg llm.Message) llm.Message {
	if t.Kind != schema.TurnSteering || t.SteeringKind != events.SteeringKindNoteHandoff {
		return msg
	}
	// The turn is built as one user text part holding the prefix and the note
	// (appendSteeringMessagesToHistory). Escaping the whole rendered text keeps
	// the attribution line as written — it opens no frame — and scans the note
	// under the same rule as any other model-authored text.
	rendered := msg.Text()
	if !strings.HasPrefix(rendered, noteHandoffPrefix) {
		return msg
	}
	escaped := sysmsg.EscapeModelAuthoredFrames(rendered)
	if escaped == rendered {
		return msg
	}
	return replaceMessageText(msg, escaped)
}

// replaceMessageText returns msg with its text parts replaced by text, split
// back onto the parts it came from.
//
// The escapes this file applies never add or remove a line, so text occupies
// exactly the lines the concatenation of the message's parts did and the split
// is that concatenation read backwards. A text whose line structure no longer
// matches leaves the message as stored, rather than handing back text split
// somewhere it was not.
func replaceMessageText(msg llm.Message, text string) llm.Message {
	texts := make([]string, 0, len(msg.Content))
	indexes := make([]int, 0, len(msg.Content))
	for i, p := range msg.Content {
		if p.Kind != llm.ContentText {
			continue
		}
		indexes = append(indexes, i)
		texts = append(texts, p.Text)
	}
	pieces, ok := sysmsg.SplitJoinedEscape(text, texts)
	if !ok {
		return msg
	}
	parts := slices.Clone(msg.Content)
	for k, at := range indexes {
		if pieces[k] == texts[k] {
			continue
		}
		parts[at].Text = pieces[k]
	}
	out := msg
	out.Content = parts
	return out
}

func escapeMessageText(msg llm.Message, escape func(string) (string, bool)) llm.Message {
	escaped, changed := escape(msg.Text())
	if !changed {
		return msg
	}
	return replaceMessageText(msg, escaped)
}

// escapeRetainedCheckpointBody escapes the model-authored bodies of a
// deterministic checkpoint in the model-facing copy.
//
// A checkpoint is replayed as stored rather than extracted, so one written
// before the capture escape existed would otherwise hand the model its own raw
// working notes and agent replies inside a user-role message. The bodies are
// read by the same reader that captures them, so the two cannot disagree about
// where a fence is or which block a line belongs to: a partner's entry keeps
// every byte even when it holds a heading-shaped line, and a fenced example the
// model quoted is spared. The stored turn keeps its bytes.
func escapeRetainedCheckpointBody(msg llm.Message) llm.Message {
	return escapeMessageText(msg, contextmgr.EscapeRetainedCheckpoint)
}

// escapeRetainedSummaryBody escapes the model-authored body of a retained
// summary in the model-facing copy.
//
// The model's text is fenced under the summary heading and so reads in the
// partner's channel: a frame it opens there is defused. The partner's messages,
// the harness markers and the stored turn keep every byte.
func escapeRetainedSummaryBody(msg llm.Message) llm.Message {
	return escapeMessageText(msg, contextmgr.EscapeRetainedSummary)
}

// escapeAgentAuthoredUserInputBody escapes a user-role input turn the model
// authored, in the model-facing copy.
//
// A delegate's start task and an idle resume arrive as user-role input stamped
// with the authoring agent's authorship. The role alone would read that text as
// the partner speaking, so a reserved frame in it would pass as a decision the
// partner made. Authorship is the discriminator: an input turn with no stamp or
// a human stamp is the partner's own words and keeps every byte, as does the
// stored turn.
func escapeAgentAuthoredUserInputBody(t schema.Turn, msg llm.Message) llm.Message {
	if t.Kind != schema.TurnUserInput || t.Authorship == nil || t.Authorship.Author.Kind != schema.TranscriptAuthorAgent {
		return msg
	}
	return escapeMessageText(msg, func(rendered string) (string, bool) {
		escaped := sysmsg.EscapeModelAuthoredFrames(rendered)
		return escaped, escaped != rendered
	})
}

// escapeParentSteeringBody escapes the model-authored body of parent steering in
// the model-facing copy.
//
// A parent model that calls delegate_send during a running delegate turn puts
// its own text into a user-role steering turn on the child, marked
// SteeringSourceParent. The assistant-only escape cannot reach a user-role
// turn, so a reserved frame in the parent's body arrived on the child's wire as
// a frame and was carried into the child's checkpoint (#6493 review).
//
// Only SteeringSourceParent is touched: the human partner's steering carries no
// parent source, is not model-authored, and must arrive as the partner typed
// it. That is the whole distinction this rule rests on.
func escapeParentSteeringBody(t schema.Turn, msg llm.Message) llm.Message {
	if t.Kind != schema.TurnSteering || t.SteeringSource != events.SteeringSourceParent {
		return msg
	}
	rendered := msg.Text()
	body, ok := strings.CutPrefix(rendered, parentMessageAttribution)
	if !ok {
		// A shape this rule does not recognize is left as stored rather than
		// escaped: the rule only rewrites text whose author it can establish.
		return msg
	}
	escaped := sysmsg.EscapeModelAuthoredFrames(body)
	if escaped == body {
		return msg
	}
	return replaceMessageText(msg, parentMessageAttribution+escaped)
}

// escapeNotificationCopyBody escapes the model-authored prose a rendered
// notification carries, in the model-facing copy.
//
// A delegate's report or progress update and a watch's note are the model's own
// words. They reach the receiving session as a user-role message with a genuine
// harness envelope around them: a delegate delivery is an attention turn with no
// steering kind, and a watch fire is a notification turn. That role is never
// escaped as model text, so a reserved answer marker in the prose arrived on the
// next request in its original spelling and read as a decision the partner
// made. The envelope, its attributes and the harness's own field lines keep
// every byte; the stored turn keeps its bytes too.
func escapeNotificationCopyBody(t schema.Turn, msg llm.Message) llm.Message {
	if t.Kind != schema.TurnSteering {
		return msg
	}
	// An attention entry is the harness's own delivery record, and a notification
	// kind is a watch fire or a stable receiver's frame. Both carry model prose
	// inside harness framing, and neither is the partner speaking.
	if t.AttentionID == "" && t.SteeringKind != events.SteeringKindNotification {
		return msg
	}
	return escapeMessageText(msg, escapeModelProseInNotificationCopy)
}

// escapeModelProseInNotificationCopy escapes the prose a rendered notification
// leaves to the model, reporting whether any byte changed.
//
// A <delegate-notification> or <job-notification> envelope is genuine framing
// and keeps every byte, including its attributes and the close tag that ends it.
// The body between them is escaped, which is where a delegate's words and a
// watch's note ride. A text carrying no envelope is wholly prose — the bare
// frame a stable receiver is handed — and is escaped whole.
func escapeModelProseInNotificationCopy(text string) (string, bool) {
	if !strings.ContainsAny(text, "<[") {
		return text, false
	}
	var b strings.Builder
	b.Grow(len(text))
	changed := false
	at := 0
	writeProse := func(span string) {
		escaped := sysmsg.EscapeModelAuthoredFrames(span)
		if escaped != span {
			changed = true
		}
		b.WriteString(escaped)
	}
	for at < len(text) {
		open, closeTag := nextNotificationEnvelope(text[at:])
		if open < 0 {
			writeProse(text[at:])
			break
		}
		start := at + open
		writeProse(text[at:start])
		headerEnd := strings.IndexByte(text[start:], '>')
		if headerEnd < 0 {
			b.WriteString(text[start:])
			break
		}
		headerEnd += start
		bodyStart := headerEnd + 1
		bodyEnd := strings.Index(text[bodyStart:], closeTag)
		if bodyEnd < 0 {
			// An unclosed block has no genuine framing past its header.
			b.WriteString(text[start:])
			break
		}
		bodyEnd += bodyStart
		b.WriteString(text[start:bodyStart])
		writeProse(text[bodyStart:bodyEnd])
		b.WriteString(closeTag)
		at = bodyEnd + len(closeTag)
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

// nextNotificationEnvelope returns where text's next harness notification
// envelope opens and the tag that closes it, or -1 when it carries none. The
// earliest opener wins, so an envelope a model nested inside another is read as
// the outer one's body and escaped with it.
func nextNotificationEnvelope(text string) (int, string) {
	open, closeTag := -1, ""
	for _, tag := range []string{"<delegate-notification", "<job-notification"} {
		if at := strings.Index(text, tag); at >= 0 && (open < 0 || at < open) {
			open, closeTag = at, "</"+tag[1:]+">"
		}
	}
	return open, closeTag
}

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
func escapeModelAuthoredTurnFramesReporting(msg llm.Message, protocol string, promotesReasoning, stripsEmptyContent bool) (llm.Message, bool) {
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
	// The parts this function reads are the parts the adapter sends, by that
	// adapter's own inclusion rule: a part it drops must not split the scan's
	// phase run, and a part it keeps must not be invisible to the scan. The
	// rules differ per adapter — a whitespace-only part is a blank line to Chat
	// Completions and nothing to Anthropic or Google — so the rule is answered
	// for the protocol that will send the text.
	//
	// stripsEmptyContent is that rule's other half on the one adapter that has
	// an empty-content policy: a target whose serializer drops empty text parts
	// drops the message an empty phase-bearing part would open, so the scan must
	// drop it too. Keeping the part where the serializer removes it reads a
	// paragraph as ended and treats the indented frame under it as code, which
	// is the one reading that spares a frame the wire exposes.
	indexes, texts, textPhases, textBreaks := sysmsg.WireTextPartsFlushedFor(protocol, msg, stripsEmptyContent)
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
	segments := sysmsg.TextSegmentsFlushed(phases, textBreaks, protocol)
	escapedSegments := sysmsg.EscapeFlushedText(texts, phases, textBreaks, protocol)
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
