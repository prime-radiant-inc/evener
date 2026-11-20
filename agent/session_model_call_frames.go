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
// user-role, so the escape the serializers apply to assistant text cannot reach
// it, yet its body is text the model wrote: automatic note elicitation and
// compact_context both pin a note this session authored and publish it as
// steering reading `noteHandoffPrefix + "\n" + note`. A frame
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
// A notification carries two kinds of text, and the escaper separates them by
// what the harness itself writes around them:
//
//   - A <delegate-notification> or <job-notification> envelope is framing. Its
//     tags keep every byte, and the body between them is escaped, which is where
//     a delegate's words and a watch's note ride.
//   - A <system-notification> or <SYSTEM-REMINDER> block is the harness speaking
//     in its own voice — the causal link a fire recorded, the hourly backstop,
//     the self-influence breaker. The whole block is framing and keeps every
//     byte. Defusing its tags would hand the model a mangled record of why it
//     was woken.
//
// A text carrying none of them is wholly prose — the bare frame a stable
// receiver is handed — and is escaped whole.
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
		open, closeTag, prose := nextNotificationEnvelope(text[at:])
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
		if prose {
			b.WriteString(text[start:bodyStart])
			writeProse(text[bodyStart:bodyEnd])
			b.WriteString(closeTag)
		} else {
			// The harness's own block is framing end to end.
			b.WriteString(text[start : bodyEnd+len(closeTag)])
		}
		at = bodyEnd + len(closeTag)
	}
	if !changed {
		return text, false
	}
	return b.String(), true
}

// notificationEnvelope is one block a rendered notification carries, and whether
// the text between its tags is model prose.
type notificationEnvelope struct {
	open     string
	closeTag string
	prose    bool
}

// notificationEnvelopes are the blocks a rendered notification carries. Only the
// two delivery envelopes hold model prose; the rest are the harness's own voice.
// The reminder spellings are the two the harness writes, and no other casing: a
// model's own <System-Reminder> is prose, and the general escape defuses it.
var notificationEnvelopes = []notificationEnvelope{
	{open: "<delegate-notification", closeTag: "</delegate-notification>", prose: true},
	{open: "<job-notification", closeTag: "</job-notification>", prose: true},
	{open: "<system-notification", closeTag: llm.SystemNotificationCloseTag},
	{open: "<system-reminder", closeTag: "</system-reminder>"},
	{open: "<SYSTEM-REMINDER>", closeTag: sysmsg.ReminderClose},
}

// nextNotificationEnvelope returns where text's next notification block opens,
// the tag that closes it, and whether its body is model prose, or -1 when text
// carries none.
//
// An opener counts only at the start of a line. The harness writes every one of
// these blocks on a line of its own, while the model's prose reaches a
// notification either entity-escaped (a delegate's update, a watch frame's body,
// a note appended to a rendered block) or as a watch frame's field values, whose
// continuations the producer indents. A frame's own message can still begin a
// line with one of these tags, which is the one spelling this rule cannot tell
// from the harness's; the alternative — treating an opener anywhere as framing —
// would spare a model's prose inside the envelope bodies it is meant to escape.
//
// The earliest opener wins, so a block a model nested inside another is read as
// the outer one's body and escaped with it.
func nextNotificationEnvelope(text string) (int, string, bool) {
	open, closeTag, prose := -1, "", false
	for _, env := range notificationEnvelopes {
		at := strings.Index(text, env.open)
		for at > 0 && text[at-1] != '\n' {
			next := strings.Index(text[at+len(env.open):], env.open)
			if next < 0 {
				at = -1
				break
			}
			at += len(env.open) + next
		}
		if at >= 0 && (open < 0 || at < open) {
			open, closeTag, prose = at, env.closeTag, env.prose
		}
	}
	return open, closeTag, prose
}
