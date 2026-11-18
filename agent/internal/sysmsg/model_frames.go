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

// SerializesPartsSeparately reports whether the target emits each text part as
// its own block on the wire, so that a fence opened in one part does not run
// into the next.
//
// It is a wider question than PhaseSeparatesMessages: Responses splits by
// Phase, while the Anthropic and Google adapters emit one block per text part
// and ignore Phase entirely. Both shapes mean the same thing for this scan — a
// part is a block of its own, and an opener at the start of a part is not
// sheltered by an unclosed fence in the part before it. Only the Chat
// Completions adapter joins a turn's text into one body, and there the joined
// reading is the only one the provider sees (#6493 review).
func SerializesPartsSeparately(protocol string) bool {
	switch protocol {
	case "openai-responses", "anthropic", "google":
		return true
	}
	return false
}

// EscapePartsIndividually escapes each text part as a block of its own.
//
// A reader that emits one block per part has a second reading of fence state
// that the joined scan cannot reach: a fence left open in one part shelters the
// next part's opener when the run is read as one block, and a delimiter that
// closes a fence in the second part reads as a fresh opener when that part is
// read alone. Each reading spares a frame the other escapes, so a caller runs
// the joined scan and then this one, and neither can shelter an opener the
// other sees. Escaping is idempotent and never adds or removes a delimiter, so
// the second pass cannot re-open what the first closed; an example fenced
// inside a single part stays fenced under both readings and keeps its bytes
// (#6493 review).
func EscapePartsIndividually(texts []string) []string {
	out := make([]string, len(texts))
	for i, text := range texts {
		out[i] = EscapeModelAuthoredFrames(text)
	}
	return out
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

// SplitJoinedEscape splits escaped, the escape of strings.Join(originals, "\n"),
// back into one piece per original, by each original's line count.
//
// The escape rewrites a frame's opening character and never adds or removes a
// line or a line break, so a piece occupies exactly the lines its original did
// and the split is the join read backwards. ok is false when the escaped text
// does not have that shape, and the caller then leaves the text as stored
// rather than hand back text split somewhere it was not.
//
// Two readers need it, for different reasons: the replay projection writes each
// piece back into the part it came from, and checkpoint capture concatenates
// the pieces so the note keeps the boundaries the model's own text had. Sharing
// the splitter keeps them splitting the same scan the same way.
func SplitJoinedEscape(escaped string, originals []string) (pieces []string, ok bool) {
	lines := strings.Split(escaped, "\n")
	pieces = make([]string, 0, len(originals))
	at := 0
	for _, original := range originals {
		n := strings.Count(original, "\n") + 1
		if at+n > len(lines) {
			return nil, false
		}
		pieces = append(pieces, strings.Join(lines[at:at+n], "\n"))
		at += n
	}
	if at != len(lines) {
		return nil, false
	}
	return pieces, true
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
	var lists listContainer
	pos := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		out := line
		// One list scope for the line, read from the state BEFORE it: it answers
		// both questions the line asks — does a fence opened here belong to a
		// list item, and is this line indented code — and the two must agree, or
		// a fence the item holds is read as a document-level block that outlives
		// it (#6493 review).
		//
		// It is read only outside a block: a fence's own content may look like a
		// list, and what it holds is quoted text, not structure. "Outside" is
		// decided before the line, so the line that opens a fence still gets the
		// item's scope.
		container := 0
		if !fence.Inside() {
			container = lists.step(content)
		}
		inBlock := fence.StepInBlockIn(content, container)
		if !inBlock && !indentedCode(content, container) && !isQuotedLine(content) {
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
//
// The four columns are measured from the enclosing list item's content
// indentation, not from the page. A list nested far enough in reaches four
// columns with its own prose: an ordered list's ten-plus item is `   11. ` at
// the marker's width, and a frame opener aligned with that item's text sits at
// four columns while being the item's ordinary continuation text. Measuring
// from the page exempted that prose and left its opener raw in both replay and
// checkpoint excerpts, while real code inside the item — four columns past the
// item's content, which is where Markdown puts it — keeps its bytes (#6493
// review). container is the item's content indentation, 0 outside a list.
func indentedCode(content string, container int) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	return mdfence.LeadingColumns(content) >= container+4
}

// listContainer tracks the content indentation of the Markdown list item the
// scan is inside, so an item's own text is not mistaken for page-indented code.
//
// It models what the escape needs and nothing more: the current item's content
// indentation and, when the item is nested, the one that encloses it. A line
// indented less than the current item's content leaves that item, and the scope
// that applies to it is its parent's rather than the top level: a nested list
// resuming at its parent's indentation is the parent's continuation prose, and
// clearing to zero there made that prose read as page-indented code, so a frame
// in it survived (#6493 review). A blank line stays inside the current item,
// since a blank line neither ends an item nor opens code.
type listContainer struct {
	content int
	parent  int
}

// step returns the content indentation that applies to line: the item's, taken
// from a marker line, or 0 when the line sits outside any item.
func (l *listContainer) step(content string) int {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return l.content
	}
	indent := mdfence.LeadingColumns(content)
	if l.content > 0 && indent < l.content {
		// Left the item: the scope that applies is the enclosing one. A line that
		// is also indented less than the parent leaves that too, and the walk
		// continues to the top level.
		l.content, l.parent = l.parent, 0
		for l.content > 0 && indent < l.content {
			l.content, l.parent = l.parent, 0
		}
	}
	rest := content[mdfence.LeadingSpaces(content):]
	if marker, ok := mdfence.ListMarker(rest); ok {
		after := rest[marker:]
		spaces := len(after) - len(strings.TrimLeft(after, " "))
		if spaces >= 1 && spaces <= 4 {
			// The content starts after the marker and its following spaces, at
			// this line's own indentation. The item it nests inside is the one
			// whose content it is indented past, and a marker at the top level
			// nests inside nothing.
			if l.content > 0 && indent >= l.content {
				l.parent = l.content
			} else {
				l.parent = 0
			}
			l.content = indent + marker + spaces
			return l.content
		}
	}
	return l.content
}
