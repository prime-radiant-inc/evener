package contextmgr

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/agent/internal/sysmsg"
)

// checkpointConversationHeading heads the conversation a compaction turn
// carries verbatim.
const checkpointConversationHeading = "## Conversation"

type checkpointConversationEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
	// modelAuthored reports that Text a model wrote, even though Role may put it
	// in the partner's channel: a delegate's task from its parent, a parent's
	// steer, or the model's own reply. It rides beside the text so the escape can
	// run on the copy a reader finally gets — after any fitting that cut the
	// text, since a cut can drop the delimiter that made a frame a quotation.
	// A genuine partner entry answers false and keeps every byte.
	modelAuthored bool `json:"-"`
}

type checkpointMarkdownBlock struct {
	Heading string
	Text    string
}

func renderCheckpointConversation(entries []checkpointConversationEntry) string {
	entries = cleanCheckpointConversation(entries)
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(checkpointConversationHeading + "\n\n")
	for _, entry := range entries {
		switch entry.Role {
		case "agent":
			b.WriteString("### Agent\n\n")
		default:
			b.WriteString("### User\n\n")
		}
		text := entry.Text
		if entry.modelAuthored {
			// This is the copy the reader gets, after the fitting the entry's
			// text went through (fitCheckpointConversation): the escape runs
			// here so a middle trim that dropped an opening delimiter exposes
			// no frame in the tail it kept.
			text = sysmsg.EscapeModelAuthoredFrames(text)
		}
		writeMarkdownFence(&b, text)
	}
	return b.String()
}

// trimBlockEdges drops a block's leading and trailing blank lines and reports
// "" for a block that holds nothing else, keeping the first content line's
// indentation. TrimSpace would strip that indentation, and indentation is
// load-bearing for model-authored text: a line indented four or more columns is
// Markdown code, which the frame escape deliberately spares as a quoted
// example. Trimming it would turn a spared code line into a bare frame opener
// in a user-role message (#6493).
//
// Partner messages do not use this: their normalization is TrimSpace, which the
// summary-layout check depends on.
func trimBlockEdges(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func renderCheckpointWorkingNotes(notes []string) string {
	clean := make([]string, 0, len(notes))
	for _, note := range notes {
		note = trimBlockEdges(note)
		if note != "" {
			clean = append(clean, note)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Working Notes\n\n")
	for _, note := range clean {
		b.WriteString("### Note\n\n")
		writeMarkdownFence(&b, note)
	}
	return b.String()
}

// renderCheckpointEarlierSummaries renders the LLM summaries a checkpoint folds,
// each whole and fenced, so their own "## " headings stay inside the section
// and a later checkpoint can extract them again.
func renderCheckpointEarlierSummaries(summaries []string) string {
	if len(summaries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Earlier Summaries\n\n")
	for _, summary := range summaries {
		b.WriteString("### Summary\n\n")
		writeMarkdownFence(&b, summary)
	}
	return b.String()
}

func extractCheckpointEarlierSummaries(text string) []string {
	var summaries []string
	for _, block := range parseMarkdownBlocks(markdownSection(text, "## Earlier Summaries")) {
		summaries = append(summaries, block.Text)
	}
	return summaries
}

// summaryMarkerStart and summaryMarkerEnd frame a summary turn. A model
// sometimes echoes them back around its own text, so everything that writes,
// looks for, or strips the framing compares against these two, and the turn's
// opening adds the newline the format wants after the start marker.
const (
	summaryMarkerStart = "[CONTEXT SUMMARY]"
	summaryMarkerEnd   = "[END SUMMARY]"
)

// summaryTurnStart opens a summary turn.
const summaryTurnStart = summaryMarkerStart + "\n"

// withoutEdgeFraming returns line without any framing markers at its edges, in
// however many copies they sit there, so a section heading a model wrapped in
// the framing it was shown is still read as one. It reads through the markers
// and nothing else: the reply is stored as the model wrote it, and a marker
// inside a line's own text stays part of that text.
func withoutEdgeFraming(line string) string {
	for {
		trimmed := strings.TrimSpace(line)
		rest, found := strings.CutPrefix(trimmed, summaryMarkerStart)
		if !found {
			rest, found = strings.CutPrefix(trimmed, summaryMarkerEnd)
		}
		if !found {
			rest, found = strings.CutSuffix(trimmed, summaryMarkerEnd)
		}
		if !found {
			rest, found = strings.CutSuffix(trimmed, summaryMarkerStart)
		}
		if !found {
			return line
		}
		line = strings.TrimSpace(rest)
	}
}

// markerOnly reports whether a trimmed line holds at least one framing marker
// and nothing else.
func markerOnly(line string) bool {
	if !strings.Contains(line, summaryMarkerStart) && !strings.Contains(line, summaryMarkerEnd) {
		return false
	}
	rest := strings.ReplaceAll(strings.ReplaceAll(line, summaryMarkerStart, ""), summaryMarkerEnd, "")
	return strings.TrimSpace(rest) == ""
}

// indentedCode reports whether a line is Markdown-indented code (four columns of
// whitespace, a tab reaching the next four-column stop), whose text is the
// model's content even when it names the framing.
func indentedCode(line string) bool {
	return strings.TrimSpace(line) != "" && mdfence.LeadingColumns(line) >= 4
}

// summaryPointerLead begins the compaction pointer line a summary turn opens
// with (CompactionMeta.compactionPointer("summary")).
const summaryPointerLead = "Earlier history was compacted into this summary "

// summaryModelTextHeading heads the model's own text in a summary turn.
const summaryModelTextHeading = "## Summary"

// carriesPartnerMessages reports whether a summary turn has the layout
// summarizeFold writes: the model's text fenced under summaryModelTextHeading,
// with the partner's messages after it in their own conversation section. A
// summary written before that layout is the model's text throughout, whatever
// headings it used, and is carried whole.
func carriesPartnerMessages(text string) bool {
	start, body, end, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok {
		return false
	}
	modelText, ok := soleFencedText(text[body:end])
	if !ok {
		return false
	}
	entries := extractMarkdownConversation(text)
	for _, entry := range entries {
		if entry.Role != "user" {
			return false
		}
	}
	// Before the heading the renderer writes only the turn's opening marker
	// and, for a persistent session, the transcript pointer line.
	if !isSummaryTurnOpening(text[:start]) {
		return false
	}
	// Everything from the heading on must be exactly what the renderer writes
	// for that model text and those entries. The block holds the model text
	// with a trailing newline ensured, which soleFencedText strips once, so
	// re-rendering it with that newline restored reproduces any model text,
	// trailing blank lines included.
	return text[start:] == renderCompactionModelText(summaryModelTextHeading, modelText+"\n")+renderCheckpointConversation(entries)+summaryMarkerEnd
}

// isSummaryTurnOpening reports whether prefix is what the renderer writes
// before a summary turn's model text: the opening marker, optionally followed
// by the one compaction pointer line and the partner-carry reminder.
func isSummaryTurnOpening(prefix string) bool {
	rest, ok := strings.CutPrefix(prefix, summaryTurnStart)
	if !ok {
		return false
	}
	if line, after, found := strings.Cut(rest, "\n"); found && strings.HasPrefix(line, summaryPointerLead) {
		rest = after
	}
	// Then, when some partner messages were summarized, the one reminder.
	reminder, ok := strings.CutSuffix(rest, "\n")
	return rest == "" || (ok && sysmsg.IsReminderBlock(reminder))
}

// soleFencedText returns the content of the one fenced block section holds,
// blank lines aside, or false when it holds anything else.
func soleFencedText(section string) (string, bool) {
	lines := strings.Split(strings.TrimSpace(section), "\n")
	var fence mdfence.Tracker
	if len(lines) < 2 || !fence.Step(lines[0]) {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if fence.Step(lines[i]) {
			if i != len(lines)-1 {
				return "", false
			}
			return strings.Join(lines[1:i], "\n"), true
		}
	}
	return "", false
}

// trimmedMessageNote stands in for the middle of a human partner's message
// that was too long to carry whole: it says how much was trimmed and where the
// full text is.
func trimmedMessageNote(trimmed int) string {
	return fmt.Sprintf("\n\n[... %d characters of this message trimmed here; the full message is in this session's transcript ...]\n\n", trimmed)
}

// partnerCarryReminder tells the model that summarized partner messages
// are in the summary, not quoted, and how to read them verbatim.
func partnerCarryReminder(summarized int, meta *CompactionMeta) string {
	return sysmsg.ReminderBlock(fmt.Sprintf("Your human partner's %d earliest messages folded here are summarized above, not quoted.%s", summarized, meta.TranscriptRecoverySentence())) + "\n"
}

// renderCompactionModelText renders a model-written compaction turn's own text
// under heading, fenced. It comes first in the turn: a summarizer reads a
// previous compaction from its head, so the model's text must not sit behind
// the partner's messages the turn carries. Fencing it keeps its own headings
// and code fences from being read as the sections that follow.
func renderCompactionModelText(heading, text string) string {
	var b strings.Builder
	b.WriteString(heading + "\n\n")
	writeMarkdownFence(&b, text)
	return b.String()
}

func writeMarkdownFence(b *strings.Builder, text string) {
	fence := mdfence.Pick(text)
	b.WriteString(fence)
	b.WriteString("text\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(fence)
	b.WriteString("\n\n")
}

func extractCheckpointConversation(text string) []checkpointConversationEntry {
	if entries := extractMarkdownConversation(text); len(entries) > 0 {
		return entries
	}

	// The legacy formats predate fenced sections, so they are read outside
	// fences: markup quoted inside a fenced earlier summary is its text.
	text = withoutFencedBlocks(text)
	openTag := "<conversation>"
	closeTag := "</conversation>"
	if _, rest, found := strings.Cut(text, openTag); found {
		if body, _, found := strings.Cut(rest, closeTag); found {
			var entries []checkpointConversationEntry
			if err := json.Unmarshal([]byte(body), &entries); err == nil {
				return cleanCheckpointConversation(entries)
			}
		}
	}

	// Backward compatibility for checkpoints that bundled user and agent text
	// separately. The original relative ordering is unrecoverable, so preserve
	// the old bundle order while migrating into the new conversation shape.
	var entries []checkpointConversationEntry
	for _, msg := range extractCheckpointJSON(text, "user_messages") {
		entries = append(entries, checkpointConversationEntry{Role: "user", Text: msg})
	}
	for _, msg := range extractCheckpointJSON(text, "agent_responses") {
		entries = append(entries, checkpointConversationEntry{Role: "agent", Text: msg})
	}
	return entries
}

func extractMarkdownConversation(text string) []checkpointConversationEntry {
	section := markdownSection(text, checkpointConversationHeading)
	if section == "" {
		return nil
	}
	blocks := parseMarkdownBlocks(section)
	entries := make([]checkpointConversationEntry, 0, len(blocks))
	for _, block := range blocks {
		if role, ok := conversationRoleForHeading(block.Heading); ok {
			entries = append(entries, checkpointConversationEntry{Role: role, Text: block.Text})
		}
	}
	return cleanCheckpointConversation(entries)
}

func extractCheckpointWorkingNotes(text string) []string {
	section := markdownSection(text, "## Working Notes")
	if section != "" {
		blocks := parseMarkdownBlocks(section)
		notes := make([]string, 0, len(blocks))
		for _, block := range blocks {
			note := trimBlockEdges(block.Text)
			if note != "" {
				// A working note is the model's own analytical text. This
				// function is one of the two seams a fold re-reads an older
				// checkpoint through, so a raw frame captured by a build that
				// predates the capture escape would otherwise be carried
				// forward and sent again in a user-role message (#6493
				// review). Escaping here is idempotent, so a note this build
				// already escaped is unchanged.
				notes = append(notes, sysmsg.EscapeModelAuthoredFrames(note))
			}
		}
		return notes
	}
	carried := extractCheckpointJSON(withoutFencedBlocks(text), "working_notes")
	for i, note := range carried {
		carried[i] = sysmsg.EscapeModelAuthoredFrames(note)
	}
	return carried
}

func markdownSection(text, heading string) string {
	_, body, end, ok := markdownSectionSpan(text, heading)
	if !ok {
		return ""
	}
	return text[body:end]
}

// textSpan is a byte range of a rendered turn.
type textSpan struct {
	start, end int
}

// escapeModelSpans returns text with each span escaped as model-authored prose,
// and reports whether any byte changed. The spans are the ranges a turn's own
// layout names as the model's; every byte outside them keeps its own.
func escapeModelSpans(text string, spans []textSpan) (string, bool) {
	escaped, _ := escapeModelRanges(text, spans)
	return escaped, escaped != text
}

// escapeModelRanges returns text with each span escaped as model-authored prose,
// and returns the same ranges in the escaped text's coordinates. A caller that
// keeps only part of the result can then tell which ranges survived and escape
// an exposed remainder without touching text the model did not write.
func escapeModelRanges(text string, spans []textSpan) (string, []textSpan) {
	if len(spans) == 0 {
		return text, nil
	}
	sorted := make([]textSpan, 0, len(spans))
	for _, span := range spans {
		if span.start < 0 || span.end < span.start || span.end > len(text) {
			continue
		}
		sorted = append(sorted, span)
	}
	slices.SortFunc(sorted, func(a, b textSpan) int { return a.start - b.start })
	var b strings.Builder
	b.Grow(len(text))
	moved := make([]textSpan, 0, len(sorted))
	pos := 0
	for _, span := range sorted {
		if span.start < pos {
			continue
		}
		escaped := sysmsg.EscapeModelAuthoredFrames(text[span.start:span.end])
		b.WriteString(text[pos:span.start])
		start := b.Len()
		b.WriteString(escaped)
		moved = append(moved, textSpan{start: start, end: b.Len()})
		pos = span.end
	}
	b.WriteString(text[pos:])
	return b.String(), moved
}

// fencedBlockSpan is one fenced code block's interior body: BodyStart is where
// the body begins, just past the opening delimiter's line, and BodyEnd where the
// closing delimiter's line begins, or the end of the scanned text when the block
// is never closed.
type fencedBlockSpan struct {
	// Heading is the nearest "### " heading outside a fence above the block, or
	// "" when no heading precedes it.
	Heading   string
	BodyStart int
	BodyEnd   int
}

// fencedBlockSpans returns the interior body span of every fenced code block in
// section, in document order, tracking fences exactly as parseMarkdownBlocks
// does: the tracker steps a line before a heading is read, so an indented
// closing delimiter ends a list item's block and the heading below it is read as
// a heading, while a heading inside an open block is content.
//
// Offsets are relative to section. A block that is never closed runs to the end
// of section, which is where its content ends for a reader too.
func fencedBlockSpans(section string) []fencedBlockSpan {
	var spans []fencedBlockSpan
	var fence mdfence.Tracker
	var heading string
	var open *fencedBlockSpan
	offset := 0
	for _, line := range strings.SplitAfter(section, "\n") {
		if line == "" {
			break
		}
		content := strings.TrimSuffix(line, "\n")
		wasInside := fence.Inside()
		// A line ends the block it is in either by closing its delimiter, which
		// leaves no block open, or by leaving the list item that holds it, which
		// the tracker reports as a step that also left a block open. Its own line
		// is not part of the body, which is why the body ends where the line
		// begins.
		boundary := fence.Step(content)
		ended := wasInside && (boundary || !fence.Inside())
		if ended {
			open.BodyEnd = offset
			spans = append(spans, *open)
			open = nil
		}
		if fence.Inside() && (!wasInside || ended) {
			open = &fencedBlockSpan{Heading: heading, BodyStart: offset + len(line)}
		}
		// Only a line outside every block can name the one that follows.
		if !fence.Inside() && strings.HasPrefix(content, "### ") {
			heading = strings.TrimSpace(strings.TrimPrefix(content, "### "))
		}
		offset += len(line)
	}
	if open != nil {
		open.BodyEnd = len(section)
		spans = append(spans, *open)
	}
	return spans
}

// agentConversationHeading reports whether a conversation block's heading names
// the model as its author, in either spelling the renderer writes.
func conversationRoleForHeading(heading string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(heading)) {
	case "user":
		return "user", true
	case "agent", "assistant":
		return "agent", true
	}
	return "", false
}

func agentConversationHeading(heading string) bool {
	role, ok := conversationRoleForHeading(heading)
	return ok && role == "agent"
}

func collectFencedSpans(text, heading string, include func(string) bool) []textSpan {
	if !strings.Contains(text, heading+"\n") {
		return nil
	}
	_, body, end, ok := markdownSectionSpan(text, heading)
	if !ok {
		return nil
	}
	end = ownedSectionEnd(text, body, end)
	var spans []textSpan
	for _, span := range fencedBlockSpans(text[body:end]) {
		if include != nil && !include(span.Heading) {
			continue
		}
		spans = append(spans, textSpan{start: body + span.BodyStart, end: body + span.BodyEnd})
	}
	return spans
}

// ownedSectionEnd returns where heading's section body stops being this
// section's: the next top-level heading line outside a fence, or end.
//
// markdownSectionSpan's end is wider by one line, because a body whose FIRST
// line is the next heading never ends the section there. That reading is right
// for extracting a section's text, which is why it is kept, and wrong for
// deciding which section owns a block: a heading written with no body under it
// would hand every block of the section below to this one, including the
// partner's messages.
func ownedSectionEnd(text string, body, end int) int {
	var fence mdfence.Tracker
	offset := body
	for _, line := range strings.SplitAfter(text[body:end], "\n") {
		if line == "" {
			break
		}
		content := strings.TrimSuffix(line, "\n")
		if !fence.StepInBlock(content) && strings.HasPrefix(content, "## ") {
			return offset
		}
		offset += len(line)
	}
	return end
}

// EscapeRetainedCheckpoint returns a checkpoint turn's text with the frames the
// model authored defused, and reports whether any byte changed.
//
// A checkpoint is a user-role message, so model-authored text placed in it
// speaks in the partner's channel. This is the copy handed back to the model, not
// the stored turn: the checkpoint this session wrote, or one it is about to
// send again, keeps its own bytes.
//
// A folded summary is the model's text throughout, and so is a working note, so
// every block under those headings is escaped. Under the conversation heading
// the author is per block: an agent or assistant block is the model's reply and
// is escaped, while a user block is the partner's own message and keeps its
// bytes even when it contains a heading-shaped line or a frame the partner
// typed. A session-log checkpoint's log body is a rendering of the model's own
// summarized actions and is escaped too.
//
// Each complete block body is escaped as one string rather than line by line, so
// fence state is read on the text the block actually holds: a frame whose
// numbered answer sits on a later line is still escaped, and a fenced example
// the model quoted keeps every byte. The section headings, the block headings
// and the fence delimiters are written by this package and are not touched.
func EscapeRetainedCheckpoint(text string) (string, bool) {
	return escapeModelSpans(text, retainedCheckpointModelSpans(text))
}

// retainedCheckpointModelSpans returns the ranges of a checkpoint turn the
// model wrote, as its layout names them: the folded summaries, the working
// notes, the agent's own conversation blocks and the session log's body.
func retainedCheckpointModelSpans(text string) []textSpan {
	spans := earlierSummarySpans(text)
	spans = append(spans, collectFencedSpans(text, "## Working Notes", nil)...)
	spans = append(spans, collectFencedSpans(text, checkpointConversationHeading, agentConversationHeading)...)
	return append(spans, sessionLogBodySpans(text)...)
}

// earlierSummarySpans returns the ranges that defuse a checkpoint's folded
// summaries: the blocks under "## Earlier Summaries".
//
// A summary the renderer wrote is carried whole, so its own layout still names
// one model-authored body — its fenced text under the summary heading. Only
// that body is escaped: the block's harness markers, a fenced example the model
// quoted and the partner's messages the summary carries keep every byte.
// Escaping such a block as plain Markdown instead would rewrite the harness
// markers and leave the model's body raw inside its own fence. A summary in an
// older layout is the model's text throughout, and one whose layout cannot be
// read is escaped whole, which is where every block under the heading stood
// before the carried layout was recognized.
func earlierSummarySpans(text string) []textSpan {
	if !strings.Contains(text, "## Earlier Summaries\n") {
		return nil
	}
	_, body, end, ok := markdownSectionSpan(text, "## Earlier Summaries")
	if !ok {
		return nil
	}
	end = ownedSectionEnd(text, body, end)
	var spans []textSpan
	for _, span := range fencedBlockSpans(text[body:end]) {
		start, stop := body+span.BodyStart, body+span.BodyEnd
		block := text[start:stop]
		// writeMarkdownFence ends a body with a newline it does not own, and the
		// carried layout is compared byte for byte, so the block is read without
		// that line break and the escape is spliced back under it.
		carried := strings.TrimSuffix(block, "\n")
		if !carriesPartnerMessages(carried) {
			spans = append(spans, textSpan{start: start, end: stop})
			continue
		}
		for _, inner := range summaryModelSpans(carried) {
			spans = append(spans, textSpan{start: start + inner.start, end: start + inner.end})
		}
	}
	return spans
}

// sessionLogCheckpointStart heads a checkpoint the session-log strategy writes.
const sessionLogCheckpointStart = "[CONTEXT CHECKPOINT - SESSION LOG]\n"

// sessionLogLabel heads the fenced block holding the session log.
const sessionLogLabel = "Session log:"

// sessionLogBodySpans returns the range that escapes a session-log checkpoint's
// log body: the interior of the fenced block written directly after the
// checkpoint's own "Session log:" line, or none when the text is not that shape.
//
// The log is a rendering of the model's own summarized actions, so a frame its
// entries hold reads in the partner's channel. The label is looked for only
// outside every fenced block, and only a fence that opens on the very next line
// is its block: the partner's prompt sits in its own fence above, so a prompt
// carrying a "Session log:" line of its own cannot name the block the escape
// rewrites, and an empty log — written as "(no entries)" with no fence at all —
// cannot fall through to the conversation section below it. The rest of the
// checkpoint — the markers, the fenced prompt, and the partner's messages —
// keeps every byte.
func sessionLogBodySpans(text string) []textSpan {
	if !strings.HasPrefix(text, sessionLogCheckpointStart) {
		return nil
	}
	var fence mdfence.Tracker
	offset := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(line, "\n")
		inBlock := fence.StepInBlock(content)
		offset += len(line)
		if inBlock || content != sessionLogLabel {
			continue
		}
		region := text[offset:]
		spans := fencedBlockSpans(region)
		// The log's delimiter opens on the line right after the label. A region
		// whose first block opens later has no log body here — an empty log
		// writes "(no entries)" — and the block below it belongs to some other
		// section.
		first := strings.IndexByte(region, '\n')
		if len(spans) == 0 || first < 0 || spans[0].BodyStart != first+1 {
			return nil
		}
		start, stop := offset+spans[0].BodyStart, offset+spans[0].BodyEnd
		return []textSpan{{start: start, end: stop}}
	}
	return nil
}

// EscapeRetainedSummary returns a summary turn's text with the frames the model
// authored defused, and reports whether any byte changed.
//
// Only the summary layout the renderer writes has a model-authored body to
// escape: the model's text fenced under the summary heading, followed by the
// partner's messages in their own conversation section. A summary in the older
// layout is the model's text throughout and is returned unchanged, as are turns
// that carry no summary section.
//
// The stored turn keeps its bytes; this is the copy handed back to the model.
// The turn's markers, the pointer line, the partner-carry reminder, the section
// and block headings, the fence delimiters and the partner's own messages all
// keep every byte, and the model's body is escaped as one block so an intact
// fenced example inside it is spared.
func EscapeRetainedSummary(text string) (string, bool) {
	return escapeModelSpans(text, summaryModelSpans(text))
}

// summaryModelSpans returns the ranges of a summary turn the model wrote: the
// text fenced under the summary heading, when the turn carries the layout
// summarizeFold writes. A summary in the older layout is the model's text
// throughout and answers no spans, as does a turn with no summary section.
func summaryModelSpans(text string) []textSpan {
	_, body, end, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok {
		return nil
	}
	spans := fencedBlockSpans(text[body:end])
	if len(spans) != 1 {
		return nil
	}
	start, stop := body+spans[0].BodyStart, body+spans[0].BodyEnd
	if !strings.ContainsAny(text[start:stop], "<[") {
		return nil
	}
	if !carriesPartnerMessages(text) {
		return nil
	}
	return []textSpan{{start: start, end: stop}}
}

// withoutMarkdownSection returns text with heading's section removed, or text
// unchanged when it has no such section.
func withoutMarkdownSection(text, heading string) string {
	start, _, end, ok := markdownSectionSpan(text, heading)
	if !ok {
		return text
	}
	return text[:start] + text[end:]
}

// markdownSectionSpan locates heading's section in text: start is where the
// heading line begins, body where the section body begins, and end where the
// next top-level "## " (or the turn's closing [END CHECKPOINT] or [END SUMMARY])
// line begins. Both the heading and the end are looked for only OUTSIDE fenced
// code blocks. Rendered entries are
// fenced and legitimately contain lines like "## 0" (a user message), a pasted
// "## Earlier Summaries", or an earlier summary's own "## Working Notes"; a
// fence-blind scan would take those for section boundaries and misread the
// checkpoint on a render/extract round-trip. This mirrors the fence tracking
// parseMarkdownBlocks applies to the section body.
//
// A summary turn whose carry is empty writes its closing marker directly after
// the fenced model text. Recognizing that marker is what keeps it out of the
// section body, so the body still holds exactly the one fenced block the
// renderer wrote.
func markdownSectionSpan(text, heading string) (start, body, end int, ok bool) {
	var fence mdfence.Tracker
	start = -1
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			break
		}
		content := strings.TrimSuffix(line, "\n")
		switch {
		// Fence lines and fenced content are never boundaries.
		case fence.StepInBlock(content):
		case start < 0:
			if line == heading+"\n" {
				start, body = offset, offset+len(line)
			}
		// The body's first line never ends the section.
		case offset > body && (strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "[END CHECKPOINT]") || strings.HasPrefix(content, summaryMarkerEnd)):
			return start, body, offset, true
		}
		offset += len(line)
	}
	if start < 0 {
		return 0, 0, 0, false
	}
	return start, body, len(text), true
}

// withoutFencedBlocks returns text with every fenced code block, fences
// included, removed. The legacy checkpoint formats are read from what is left.
func withoutFencedBlocks(text string) string {
	var b strings.Builder
	var fence mdfence.Tracker
	for _, line := range strings.SplitAfter(text, "\n") {
		if !fence.StepInBlock(strings.TrimSuffix(line, "\n")) {
			b.WriteString(line)
		}
	}
	return b.String()
}

func parseMarkdownBlocks(section string) []checkpointMarkdownBlock {
	var blocks []checkpointMarkdownBlock
	var heading string
	var lines []string
	var fence mdfence.Tracker
	sawFence := false

	flush := func() {
		if heading == "" {
			return
		}
		text := trimBlockEdges(strings.Join(lines, "\n"))
		if text != "" {
			blocks = append(blocks, checkpointMarkdownBlock{Heading: heading, Text: text})
		}
	}

	for line := range strings.SplitSeq(section, "\n") {
		// The fence is stepped before a heading is read: a "### " line indented
		// less than a list item's content ends the item's fence, and is read as
		// a heading rather than as fenced text. A fence opened before the first
		// heading is tracked the same way, so a heading inside it is not read as
		// one.
		isFence := fence.Step(line)
		if !fence.Inside() && strings.HasPrefix(line, "### ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			lines = nil
			sawFence = false
			continue
		}
		if heading == "" {
			continue
		}
		if isFence {
			sawFence = true
			continue
		}
		if fence.Inside() {
			lines = append(lines, line)
			continue
		}
		if !sawFence && strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return blocks
}

func cleanCheckpointConversation(entries []checkpointConversationEntry) []checkpointConversationEntry {
	out := make([]checkpointConversationEntry, 0, len(entries))
	for _, entry := range entries {
		role := strings.ToLower(strings.TrimSpace(entry.Role))
		switch role {
		case "user", "agent":
		case "assistant":
			role = "agent"
		default:
			continue
		}
		// A partner entry keeps its TrimSpace normalization: it is carried text,
		// and that normalization is the discriminator carriesPartnerMessages
		// compares against. An agent entry is the model's own reply, and its
		// first line's indentation is what makes a quoted code example read as
		// code rather than as a fence opener, so it keeps its edges (#6493
		// review). Dropping it would also change the model's bytes in the
		// checkpoint, which the escape is not licensed to do.
		text := strings.TrimSpace(entry.Text)
		if role == "agent" {
			text = trimBlockEdges(entry.Text)
		}
		if text == "" {
			continue
		}
		// An agent entry is the model's own reply, and this function is also
		// the seam a fold re-reads an older checkpoint through. A checkpoint
		// written before the capture escape existed carries the raw frame, so
		// escaping only at capture would re-inject it in a user-role message on
		// the next fold (#6493 review). The partner's entry is the authority
		// and keeps its bytes; the transcript's own record is untouched
		// because this returns the carried copy.
		if role == "agent" {
			text = sysmsg.EscapeModelAuthoredFrames(text)
		}
		out = append(out, checkpointConversationEntry{Role: role, Text: text, modelAuthored: entry.modelAuthored})
	}
	return out
}

// extractCheckpointJSON extracts a JSON string array from a checkpoint text
// stored in XML-style tags like <user_messages>[...]</user_messages>.
// Falls back to legacy "Original task:" format for backward compatibility.
func extractCheckpointJSON(text, tag string) []string {
	// New format: <tag>[...]</tag>
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	if _, rest, found := strings.Cut(text, openTag); found {
		if body, _, found := strings.Cut(rest, closeTag); found {
			var msgs []string
			if err := json.Unmarshal([]byte(body), &msgs); err == nil {
				return msgs
			}
		}
	}

	// Legacy format: "Original task: ..." line (only for user_messages tag).
	if tag == "user_messages" {
		if task := extractOriginalPromptLine(text, "Original task: "); task != "" {
			return []string{task}
		}
	}

	return nil
}
