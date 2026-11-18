package sysmsg

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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

// harnessTagPrefix is one line-start tag and how its name compares. The
// spellings the harness writes differ in case where the writing code does:
// <SYSTEM-REMINDER> is uppercase, <system-reminder> and
// <system-notification> are lowercase. CaseFoldASCII marks the tag the
// assistant-fabrication detector matches case-insensitively, so a model
// writing <System-Reminder> cannot be flagged by that detector and replayed
// unescaped by the escape — the drift #6695's review found.
//
// This package keeps its own list and its own comparison. agent/doctor's
// fabrication detector keeps a separate list; the two are not shared, so a
// change here reaches the escape only.
type harnessTagPrefix struct {
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
// added there for detection need not be added here. The detector's list is its
// own; this package compares tag names through harnessTagMatches and the
// detector through its own rule, and neither reaches the other's list.
var modelAuthoredTagOpeners = []harnessTagPrefix{
	{Prefix: "<delegate-notification"},
	{Prefix: "<job-notification"},
	{Prefix: "<delegate-user-note"},
	{Prefix: "<system-notification"},
	// Case-folded here exactly as the doctor's detector folds it, so any
	// spelling that detector would flag is also escaped (see harnessTagPrefix).
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

// harnessTagMatches reports whether line begins tag, returning the text after
// it. This is this package's tag comparison, used by the escape alone; the
// doctor's fabrication detector keeps its own list and its own rule.
func harnessTagMatches(line string, tag harnessTagPrefix) (rest string, ok bool) {
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
	// The last numbered answer line's offset, found once. A marker carrying a
	// note is a frame only when an answer line follows it, and "an answer
	// follows" is the same question as "the LAST answer is after it" — so one
	// scan answers it for every marker in the text. Scanning the remainder per
	// marker was quadratic, and a long reply full of note-bearing markers ran
	// that scan again on every request while it stayed in history (#6493).
	lastAnswer := lastAnswerOffset(text)
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		out := line
		// One list scope for the line, read from the state BEFORE it: it answers
		// both questions the line asks — does a fence opened here belong to a
		// list item, and is this line indented code — and the two must agree, or
		// a fence the item holds is read as a document-level block that outlives
		// it (#6493 review).
		//
		// A block's own content may look like a list, and what it holds is quoted
		// text, not structure, so the list is not stepped while one is open. The
		// exception is the line that ENDS the block: it belongs to the enclosing
		// item, so its scope is resolved here and the fence is told so below.
		// Without that, an unclosed nested fence dedenting onto the parent's
		// four-column prose would hand this line scope zero, the prose would read
		// as page-indented code, and a model-authored frame in it would survive
		// in both replay and checkpoint capture.
		container, enclosing := 0, 0
		if !fence.Inside() || fence.LeavesItem(content) {
			container, enclosing = lists.step(content)
		}
		inBlock := fence.StepInBlockIn(content, container, enclosing)
		if !inBlock && !indentedCode(content, container) && !isQuotedLine(content) {
			if escaped, ok := escapeFrameOpener(content, pos+len(line) <= lastAnswer); ok {
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
//
// The indentation is skipped as bytes and written back unchanged, because it is
// the line's own Markdown indentation: a tab-indented continuation line under a
// two-digit ordered item reaches the item's content column, so it is prose to
// the code check and only a tab is left ahead of the frame. Skipping spaces
// alone left the tab in place and passed the frame through unescaped (#6493).
//
// answerFollows is true when a numbered answer line comes after this one. It is
// read only for the answer marker, which the client reads out of a reply with a
// note appended to the marker line as long as numbered answer lines follow:
// parseAskAnswerLines takes the answers from that text and
// linkedReplyAnswersThisCall admits it, while the stricter
// composedReplyCoversCall declines it and keeps the raw row. A marker alone on
// its line opens a block whether or not answers follow; a marker carrying a note
// is a frame only in that shape, so a sentence that merely mentions the marker
// stays prose.
//
// The caller computes answerFollows once for the whole text rather than scanning
// per line, which is what keeps a long reply linear.
func escapeFrameOpener(content string, answerFollows bool) (string, bool) {
	trimmed := strings.TrimLeft(content, " \t")
	if trimmed == "" {
		return "", false
	}
	indent := len(content) - len(trimmed)
	if isModelAuthoredMarkerLine(strings.TrimRight(trimmed, " \t")) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	if isAnswerMarkerWithNote(trimmed, answerFollows) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	for _, tag := range modelAuthoredTagOpeners {
		after, ok := harnessTagMatches(trimmed, tag)
		if !ok || !hasTagBoundary(after) {
			continue
		}
		return content[:indent] + "&lt;" + trimmed[1:], true
	}
	return "", false
}

// answerMarker is the bracket the harness writes ahead of a composed answer
// reply. It is also the whole-line marker in modelAuthoredMarkerLines, and it is
// named here because this shape checks a prefix of the line rather than the
// whole of it.
const answerMarker = "[answers]"

// isAnswerMarkerWithNote reports whether line starts with the answer marker and
// carries a note after it, and answerFollows says a numbered answer line comes
// later in the reply.
//
// The client reads an edited reply as `[answers] <note>` followed by answer
// lines, and treats that as the composed reply's own content. A bare mention of
// the marker in a sentence does not qualify: the shape needs the marker at the
// line's start, a note after it, and at least one answer line under it.
//
// The answer line may be anywhere later in the reply, not only on the next
// non-blank line, because the client's parseAskAnswerLines reads every line of
// the reply: an edited draft routinely carries a commentary paragraph between
// the note and the answers, and stopping at the first line left that supported
// shape unescaped (#6493). parseAskAnswerLines is TypeScript
// (appwire-client/typescript/askShared.ts) and cannot be called from here, so
// the shape is matched in Go; the two must be kept in step.
func isAnswerMarkerWithNote(line string, answerFollows bool) bool {
	note, ok := strings.CutPrefix(line, answerMarker)
	if !ok || strings.TrimSpace(note) == "" {
		return false
	}
	return answerFollows
}

// lastAnswerOffset returns the byte offset at which the LAST numbered answer line
// in text starts, or a negative value when there is none.
//
// One scan answers "does an answer line follow this marker" for every marker in
// the text: an answer follows a marker exactly when the last answer does. Doing
// it per marker was quadratic in the length of the reply, and ran again on every
// request for as long as the reply stayed in history (#6493).
func lastAnswerOffset(text string) int {
	last := -1
	pos := 0
	for line := range strings.Lines(text) {
		if isAnswerLine(strings.TrimSpace(line)) {
			last = pos
		}
		pos += len(line)
	}
	return last
}

// isAnswerLine reports whether line is one numbered answer line in the client's
// composed form: an ordinal, a full stop, a separator, a bracketed header and the
// arrow the client writes before the resolution text.
//
// The accepted forms are the client's, character for character: its grammar is
// /^(\d+)\.\s(?:\[([^\]\r\n]*)\]|\[("(?:\\.|[^"\\])*")\])\s→\s/ in
// appwire-client/typescript/askShared.ts. Two narrowings here cost a review
// round each, so they are stated rather than left to the code:
//
//   - The separator after the ordinal is \s, matched as the CLIENT matches it
//     rather than as a space-or-tab test would: a nonbreaking space after the
//     ordinal is a separator to the client and not to either, so a reply whose
//     only answer was spaced that way went unrecognized and the marker it
//     answers stayed unescaped (#6493 review).
//   - The header's body is [^\]\r\n]*, which admits an EMPTY header.
//
// The header may also be a JSON string form the client accepts; only its
// brackets and arrow are checked here, because this decides whether a frame
// opener is escaped rather than parsing the reply.
func isAnswerLine(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) || line[i] != '.' {
		return false
	}
	sep, sepLen := utf8.DecodeRuneInString(line[i+1:])
	if !isClientSpace(sep) {
		return false
	}
	rest, ok := strings.CutPrefix(line[i+1+sepLen:], "[")
	if !ok {
		return false
	}
	// The header body is anything but the delimiters, including nothing at all.
	header, after, ok := strings.Cut(rest, "]")
	if !ok || strings.ContainsAny(header, "\r\n") {
		return false
	}
	return strings.Contains(after, "→")
}

// isClientSpace reports whether r is one of the characters the client's \s
// matches.
//
// ECMAScript's \s is WhiteSpace plus LineTerminator: the tab, the vertical tab,
// the form feed, the byte-order mark, the line terminators, and every Unicode
// space separator — Zs, which includes both the ordinary space and the
// nonbreaking one. Go's \s is a different set and a space-or-tab test is
// narrower still, so the client's set is spelled out here: the scanner models
// the input the client accepts (#6493 review).
func isClientSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\ufeff', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
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
// item's content, which is where Markdown puts it — keeps its bytes.
//
// container is the item's content indentation, 0 outside a list.
func indentedCode(content string, container int) bool {
	if strings.TrimSpace(content) == "" {
		return false
	}
	return mdfence.LeadingColumns(content) >= container+4
}

// listContainer tracks the content indentation of every Markdown list item the
// scan is inside, so an item's own text is not mistaken for page-indented code.
//
// It is a stack because the structure is one: an item may contain an item that
// contains an item, and a line dedenting out of the innermost one belongs to
// whichever enclosing item it still fits inside. Holding only the current item
// and the one above it lost the rest, so a line dedenting two levels at once
// fell through to column zero, and prose in an outer item whose content starts
// at column four read as page-indented code with the frame in it surviving.
//
// A blank line stays inside the current item, since a blank line neither ends
// an item nor opens code.
type listContainer struct {
	// items is the content indentation of each enclosing item, outermost
	// first. The last one is the current item; an empty stack is the top level.
	items []int
}

// current returns the content indentation of the item the scan is inside, or 0
// at the top level.
func (l *listContainer) current() int {
	if len(l.items) == 0 {
		return 0
	}
	return l.items[len(l.items)-1]
}

// enclosing returns the content indentation of the item above the current one,
// or 0 when the current item is the outermost.
func (l *listContainer) enclosing() int {
	if len(l.items) < 2 {
		return 0
	}
	return l.items[len(l.items)-2]
}

// step returns the content indentation that applies to line — the innermost item
// whose content the line still reaches, taken from a marker line, or 0 when the
// line sits outside any item — and the content indentation of the item that
// ENCLOSES it, resolved after the dedent and before the line's own item opens.
//
// The enclosing scope is what a fence opened on the line belongs to, and it is
// not the scope the scan has just moved to: a sibling item's marker line has
// already left the previous sibling, so its enclosing item is the one above both
// of them. Only this walk knows that, which is why it is returned rather than
// left for the tracker to infer from the column it recorded last.
func (l *listContainer) step(content string) (scope, enclosing int) {
	if strings.TrimSpace(content) == "" {
		return l.current(), l.enclosing()
	}
	indent := mdfence.LeadingColumns(content)
	// Leave every item the line is indented out of. Popping one level at a time
	// is what keeps the enclosing item that the line DOES fit inside, which a
	// two-slot walk could not: it had nowhere to put the level above.
	for len(l.items) > 0 && indent < l.items[len(l.items)-1] {
		l.items = l.items[:len(l.items)-1]
	}
	enclosing = l.current()
	// Where the item's text begins comes from mdfence.ListItemContent, the same
	// parser the fence tracker reads, so the two cannot disagree about the
	// content column. The marker's separator is Markdown whitespace — a space or
	// a tab — and a tab after a two-digit marker puts the content at column four,
	// which counting literal spaces alone had put at three (#6493).
	//
	// A marker opens an item only within three columns of the enclosing item's
	// content. Further in it is indented code, and a marker-shaped line inside
	// code is example text rather than structure: treating it as an item
	// invented a scope whose content column then classified the code's own frame
	// lines as prose and rewrote the quoted example.
	//
	// The marker is read at the byte offset where the indentation ends, not at
	// the leading SPACE count: indentation is columns, so a tab-indented nested
	// marker has no leading spaces and a marker that is not at byte zero. Read
	// from the space count, the nested item went unrecorded, and its
	// continuation prose — four columns past the parent's content, one column
	// past the item's own — measured as indented code and kept its frame
	// (#6493 review).
	if itemContent, ok := mdfence.ListItemContent(content, mdfence.LeadingIndentEnd(content)); ok && indent-enclosing <= 3 {
		l.items = append(l.items, itemContent)
		return l.current(), enclosing
	}
	return l.current(), enclosing
}
