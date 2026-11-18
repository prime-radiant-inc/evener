package sysmsg

import (
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/llm"
)

// PhaseSeparatesMessages reports whether the target protocol emits a separate
// wire message per text Phase rather than joining a turn's text into one.
//
// Only the OpenAI Responses adapter has phases: it groups consecutive text
// parts by Phase and emits one assistant message per group. Every other
// adapter joins a turn's text into a single message, and that includes the case
// a session switches to: stored parts keep the phase labels they were recorded
// with, but the adapter that replays them no longer honours them (#6493
// review). Escaping must use the boundaries of the adapter that will actually
// send the text, so this answers for the dispatch target, not for the history.
func PhaseSeparatesMessages(protocol string) bool {
	return protocol == "openai-responses"
}

// CarriesWireText reports whether a text part reaches the provider as message
// text. It is the adapters' inclusion rule, and the frame scan must use it
// too: a boundary the scan cannot see is a message the scan cannot escape, and
// the two would then disagree about where a fence ends (#6493 review).
//
// An empty part still counts when it carries a Phase, because the Responses
// adapter emits one message per phase and an empty phase-bearing part is a
// message of its own. A whitespace-only part with no phase opens no message,
// so it cannot hide one either. The test is TrimSpace, not emptiness: the
// Responses adapter drops a part whose text is only whitespace
// (responses/input.go's `strings.TrimSpace(p.Text) == "" && p.Phase == ""`),
// so a scan that kept such a part would split a phase run the adapter joins —
// resetting fence state where the adapter carries it across, and sparing the
// frame after the next delimiter (#6493 review).
func CarriesWireText(p llm.ContentPart) bool {
	return p.Kind == llm.ContentText && (strings.TrimSpace(p.Text) != "" || p.Phase != "")
}

// WireTextParts returns an assistant message's text parts as EscapePhasedText
// consumes them: the index, text and phase of every part CarriesWireText keeps,
// in stored order.
//
// It exists so a caller cannot filter with one rule and segment with another.
// Dropping a part is what makes the surrounding parts one message to the
// adapter, so the drop and the segmentation have to be the same decision; two
// callers each writing their own loop is how the scan and the wire drifted
// apart (#6493 review).
func WireTextParts(msg llm.Message) (indexes []int, texts, phases []string) {
	for i, p := range msg.Content {
		if !CarriesWireText(p) {
			continue
		}
		indexes = append(indexes, i)
		texts = append(texts, p.Text)
		phases = append(phases, p.Phase)
	}
	return indexes, texts, phases
}

// TextSegments splits text parts at the message boundaries the given protocol
// will use, returning each segment's part range. A protocol that joins the
// turn returns one segment covering every part; a phase-separating one returns
// a segment per run of equal Phase. Callers escape each segment as one block,
// which is what the model will read as one message.
func TextSegments(phases []string, protocol string) [][2]int {
	if len(phases) == 0 {
		return nil
	}
	if !PhaseSeparatesMessages(protocol) {
		return [][2]int{{0, len(phases)}}
	}
	var segments [][2]int
	start := 0
	for i := 1; i <= len(phases); i++ {
		if i == len(phases) || phases[i] != phases[start] {
			segments = append(segments, [2]int{start, i})
			start = i
		}
	}
	return segments
}

// EscapePhasedText is the one scan that decides where a model-authored frame
// may be escaped in text assembled from several parts. It returns, per
// target-boundary segment, that segment's text escaped as a single block.
//
// Both readers use it: the replay projection, which hands the text to a
// provider, and checkpoint capture, which flattens the model's text into a
// user-role working note. They must agree, because they answer the same
// question — is this line inside a fence the reader will put it in? — and two
// separate scans drifted exactly where a fence opened in one phase and closed
// in the next (#6493 review). protocol is the dispatch target's: the adapter
// that will send the text, not the one that recorded it.
func EscapePhasedText(texts, phases []string, protocol string) []string {
	if len(texts) == 0 {
		return nil
	}
	segments := TextSegments(phases, protocol)
	out := make([]string, 0, len(segments))
	for _, span := range segments {
		out = append(out, EscapeModelAuthoredFrames(strings.Join(texts[span[0]:span[1]], "\n")))
	}
	return out
}

// HarnessTagPrefix is one line-start tag and how its name compares. The
// spellings the harness writes differ in case where the writing code does:
// <SYSTEM-REMINDER> is uppercase, <system-reminder> and
// <system-notification> are lowercase. CaseFoldASCII marks the tag the
// assistant-fabrication detector matches case-insensitively, so a model
// writing <System-Reminder> cannot be flagged by that detector and replayed
// unescaped by the escape — the drift #6695's review found.
type HarnessTagPrefix struct {
	Prefix        string
	CaseFoldASCII bool
}

// modelAuthoredTagOpeners are the line-start tags EscapeModelAuthoredFrames
// defuses: every frame the harness appends to the model's context, plus the
// tags a real reply or notification arrives wrapped in. Text the model wrote
// must never present as one of them, because the user-turn channel is the
// authorization channel (#6493): a fabricated [answers] block reads as a
// decision the partner made, and a fabricated <delegate-notification> reads as
// a runtime fact the harness never reported.
//
// This is deliberately NOT the same set as agent/doctor/fabrication.go's
// harnessTagPrefixes, and the two are not meant to converge:
//
//   - An escape must cover a SUPERSET. Escaping a line that turns out to be
//     ordinary prose costs a reader nothing, so this list matches on prefix
//     and carries the bracketed block markers below.
//   - The doctor's detector must not produce a FALSE POSITIVE, so [answers] is
//     not one of its tag prefixes at all: it reports one only when a numbered
//     answer line follows (fabrication.go's hasFollowingNumberedAnswerLine),
//     and it matches [CONTEXT SUMMARY] by whole-line equality rather than by
//     prefix.
//
// So an entry added here for safety need not be added there, and an entry
// added there for detection need not be added here. What the two must agree on
// is how a tag NAME compares: HarnessTagMatches is this package's rule, and a
// tag that both readers know must fold case the same way in both. When you add
// a frame that both should know, add it to both lists and say why.
var modelAuthoredTagOpeners = []HarnessTagPrefix{
	{Prefix: "<delegate-notification"},
	{Prefix: "<job-notification"},
	{Prefix: "<delegate-user-note"},
	{Prefix: "<system-notification"},
	// Case-folded here exactly as the doctor's detector folds it, so any
	// spelling that detector would flag is also escaped (see HarnessTagPrefix).
	{Prefix: "<system-reminder", CaseFoldASCII: true},
	{Prefix: "<environment_context"},
	{Prefix: "<environment-context"},
	{Prefix: "<shared-notes"},
	{Prefix: "<skill-context"},
	{Prefix: "<skill-inventory"},
	// The task store's completion block, sent at line start in user-role
	// steering (agent/task_reminders.go's taskCompletionMachineBlockStart).
	// A model repeating it would present state the store never reported.
	{Prefix: "<evener-task-completion"},
}

// modelAuthoredMarkerLines are the bracketed line-start markers the harness
// writes into the model's context. They are matched whole-line: a sentence
// opening with the marker's spelling is prose quoting it, while a line that is
// only the marker opens a block.
var modelAuthoredMarkerLines = []string{
	"[answers]",
	"[CONTEXT SUMMARY]",
	"[CONTEXT CHECKPOINT]",
	// The session-log strategy's own header starts with "[CONTEXT CHECKPOINT"
	// and would otherwise match nothing: it is its own line, not a prefix.
	"[CONTEXT CHECKPOINT - SESSION LOG]",
	"[END SUMMARY]",
	"[END CHECKPOINT]",
	// The OODA strategy's orientation banner, appended as a user-role steering
	// turn (internal/contextmgr/strategy_ooda.go).
	"[SESSION ORIENTATION]",
}

// HarnessTagMatches reports whether line begins tag, returning the text after
// it. The one place tag names are compared: both this package's escape and the
// doctor's fabrication detector call it, so a case-folding rule cannot differ
// between the two lists.
func HarnessTagMatches(line string, tag HarnessTagPrefix) (rest string, ok bool) {
	if !tag.CaseFoldASCII {
		return strings.CutPrefix(line, tag.Prefix)
	}
	if len(line) < len(tag.Prefix) || !strings.EqualFold(line[:len(tag.Prefix)], tag.Prefix) {
		return "", false
	}
	return line[len(tag.Prefix):], true
}

// EscapeModelAuthoredFrames returns text with every line-start harness input
// frame defused, so model-authored prose cannot pass to a later request as a
// frame the harness wrote. A tag's opening character is rewritten to "&lt;"
// and a marker's opening bracket to "&#91;", which leaves the text legible and
// the line otherwise byte-identical while denying it the exact spelling a real
// frame opens with. The stored turn keeps its own bytes: callers escape the
// copy they hand the model or place in a compaction turn, never the turn they
// persist.
//
// Code is not prose and is left alone, as the doctor's detector leaves it: a
// line inside a fenced block (including one opened inside a list item),
// indented four or more columns, or beginning with a block quote marker is the
// model quoting a frame rather than emitting one, and escaping it would
// corrupt a legitimate example. A two-space list continuation line is prose to
// both readers and is escaped.
//
// One pass, so the escape is idempotent: an already-escaped opener no longer
// matches, and its "&lt;" is not itself a frame. Text with no frame opener
// returns unchanged and unallocated. Line endings are preserved byte for byte,
// so CRLF text keeps its CRLFs.
func EscapeModelAuthoredFrames(text string) string {
	if !strings.ContainsAny(text, "<[") {
		return text
	}
	var b strings.Builder
	created := false
	var fence mdfence.Tracker
	pos := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		out := line
		if !fence.StepInBlock(content) && !indentedCode(content) && !isQuotedLine(content) {
			if escaped, ok := escapeFrameOpener(content); ok {
				out = escaped + line[len(content):]
			}
		}
		// Nothing is allocated and nothing is copied until a line actually
		// changes: most replayed text carries no frame, and this runs on
		// every request.
		if out != line {
			if !created {
				b.Grow(len(text))
				b.WriteString(text[:pos])
				created = true
			}
			b.WriteString(out)
		} else if created {
			b.WriteString(line)
		}
		pos += len(line)
	}
	if !created {
		return text
	}
	return b.String()
}

// escapeFrameOpener rewrites the opening character of content's frame prefix,
// reporting false when content starts with no frame.
func escapeFrameOpener(content string) (string, bool) {
	trimmed := strings.TrimLeft(content, " ")
	if trimmed == "" {
		return "", false
	}
	indent := len(content) - len(trimmed)
	if isModelAuthoredMarkerLine(strings.TrimRight(trimmed, " \t")) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	for _, tag := range modelAuthoredTagOpeners {
		after, ok := HarnessTagMatches(trimmed, tag)
		if !ok || !hasTagBoundary(after) {
			continue
		}
		return content[:indent] + "&lt;" + trimmed[1:], true
	}
	return "", false
}

// isModelAuthoredMarkerLine reports whether line, trimmed, is exactly one of
// modelAuthoredMarkerLines.
func isModelAuthoredMarkerLine(line string) bool {
	return slices.Contains(modelAuthoredMarkerLines, line)
}

// hasTagBoundary reports whether rest, the text after a matched tag prefix, is
// where a tag name ends: end of line, whitespace, ">", "/", or "=". A longer
// word that merely starts with a tag name (<system-notifications-doc>) is an
// identifier, not a frame.
func hasTagBoundary(rest string) bool {
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ' ', '\t', '>', '/', '\r', '=':
		return true
	default:
		return false
	}
}

// isQuotedLine reports whether content begins a Markdown block quote, whose
// body is quoted material rather than the author speaking.
func isQuotedLine(content string) bool {
	return strings.HasPrefix(strings.TrimLeft(content, " "), ">")
}

// indentedCode reports whether content is Markdown-indented code, whose text is
// literal content rather than prose.
func indentedCode(content string) bool {
	return strings.TrimSpace(content) != "" && mdfence.LeadingColumns(content) >= 4
}
