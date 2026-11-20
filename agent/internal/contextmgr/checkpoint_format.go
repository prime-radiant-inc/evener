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
	// BodyStart and BodyEnd bound the fenced body Text was read from, in the
	// coordinates of the section that was parsed: BodyStart is just past the
	// opening delimiter's line, and BodyEnd is where the closing delimiter's
	// line begins, or the section's end when the block is never closed. Both
	// are -1 for a block that holds no fenced body.
	BodyStart int
	BodyEnd   int
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
			// A partner-role entry another model wrote is headed as the model's,
			// so a later fold reconstructs its authorship instead of reading it as
			// the partner's words. The heading is what carries the fact past a
			// fold, where the entry is read back from Markdown alone.
			b.WriteString(modelAuthoredConversationHeading(entry) + "\n\n")
		}
		text := entry.Text
		if entry.Role == "agent" || entry.modelAuthored {
			// This is the copy the reader gets, after the fitting the entry's
			// text went through (fitCheckpointConversation): the escape runs
			// here, once, on the entry as collected, so a middle trim that
			// dropped an opening delimiter exposes no frame in the tail it kept.
			text = sysmsg.EscapeModelAuthoredFrames(text)
		}
		writeMarkdownFence(&b, text)
	}
	return b.String()
}

// modelAuthoredConversationHeading is the block heading a partner-role entry
// another model wrote is rendered under. The partner's own entries keep the
// partner heading, so the two stay distinct across a fold.
func modelAuthoredConversationHeading(entry checkpointConversationEntry) string {
	return conversationHeading(entry.modelAuthored)
}

// conversationHeading is the block heading a partner-role entry is rendered
// under: the one naming another model as its author, or the partner's own.
func conversationHeading(modelAuthored bool) string {
	if modelAuthored {
		return "### Parent Agent"
	}
	return "### User"
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
		// A working note is the model's own text, so a frame it opens at line
		// start is defused on the copy the reader gets. The escape runs here,
		// once, on the note as collected: the collection cut the note before
		// this point, so no cut splits an escaped opener.
		writeMarkdownFence(&b, sysmsg.EscapeModelAuthoredFrames(note))
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

// carriesPartnerMessages reports whether a summary turn has a layout
// summarizeFold writes: the model's text fenced under summaryModelTextHeading,
// with the partner-role entries after it in their own conversation section. A
// summary written before that layout is the model's text throughout, whatever
// headings it used, and is carried whole.
//
// Both released layouts of that section are valid: the current one heads a
// model-authored entry as the parent agent's, and the one before it headed
// every partner-role entry as the partner's, a parent steer among them. The
// comparison is against the turn's own headings and bytes, because authorship
// normalization would move a legacy steer to the other heading and the escape
// would rewrite a body the turn stores as written.
//
// A partner-role block is the renderer's only when its text is already what the
// renderer's normalization would leave: the renderer trims a partner-role
// entry, so a block whose edges trim differently is model text that happens to
// use the layout's headings, and it is carried whole rather than promoted to
// the partner's authority.
func carriesPartnerMessages(text string) bool {
	start, body, end, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok {
		return false
	}
	modelText, ok := soleFencedText(text[body:end])
	if !ok {
		return false
	}
	blocks := parseMarkdownBlocks(markdownSection(text, checkpointConversationHeading))
	for _, block := range blocks {
		if role, ok := conversationRoleForHeading(block.Heading); !ok || role != "user" {
			return false
		}
		if block.Text != strings.TrimSpace(block.Text) {
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
	return text[start:] == renderCompactionModelText(summaryModelTextHeading, modelText+"\n")+renderPartnerConversationAsRead(blocks)+summaryMarkerEnd
}

// renderPartnerConversationAsRead renders a carried conversation section as the
// turn holds it: each block under the canonical heading its authorship names and
// with its text unchanged.
func renderPartnerConversationAsRead(blocks []checkpointMarkdownBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(checkpointConversationHeading + "\n\n")
	for _, block := range blocks {
		b.WriteString(conversationHeading(conversationHeadingIsModelAuthored(block.Heading)) + "\n\n")
		writeMarkdownFence(&b, block.Text)
	}
	return b.String()
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
			entries = append(entries, checkpointConversationEntry{
				Role:          role,
				Text:          block.Text,
				modelAuthored: conversationHeadingIsModelAuthored(block.Heading),
			})
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
				notes = append(notes, note)
			}
		}
		return notes
	}
	return extractCheckpointJSON(withoutFencedBlocks(text), "working_notes")
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
	escaped := escapeSpansInSlice(text, 0, spans)
	return escaped, escaped != text
}

// escapeSpansInSlice returns slice with the parts of spans that fall inside it
// escaped as model-authored prose. sliceStart is where slice begins in the
// coordinates spans are given in, so a caller that kept only part of a rendered
// turn escapes the part it kept, once, without touching text the model did not
// write or re-deriving coordinates in escaped text.
func escapeSpansInSlice(slice string, sliceStart int, spans []textSpan) string {
	end := sliceStart + len(slice)
	sorted := make([]textSpan, 0, len(spans))
	for _, span := range spans {
		start, stop := max(span.start, sliceStart), min(span.end, end)
		if start >= stop {
			continue
		}
		sorted = append(sorted, textSpan{start: start, end: stop})
	}
	if len(sorted) == 0 {
		return slice
	}
	slices.SortFunc(sorted, func(a, b textSpan) int { return a.start - b.start })
	var b strings.Builder
	b.Grow(len(slice))
	pos := sliceStart
	for _, span := range sorted {
		if span.start < pos {
			continue
		}
		b.WriteString(slice[pos-sliceStart : span.start-sliceStart])
		b.WriteString(sysmsg.EscapeModelAuthoredFrames(slice[span.start-sliceStart : span.end-sliceStart]))
		pos = span.end
	}
	b.WriteString(slice[pos-sliceStart:])
	return b.String()
}

// conversationRoleForHeading reports which channel a conversation block's
// modelAuthoredConversationRole is the heading name a partner-role entry another
// conversationRoleForHeading reports which channel a conversation block's
// heading puts it in: the partner's, or the model's own reply.
func conversationRoleForHeading(heading string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(heading)) {
	case "user", modelAuthoredConversationRole:
		return "user", true
	case "agent", "assistant":
		return "agent", true
	}
	return "", false
}

// modelAuthoredConversationRole is the heading name a partner-role entry another
// model wrote is rendered under, in the lower-cased form the heading reader
// compares against.
const modelAuthoredConversationRole = "parent agent"

// conversationHeadingIsModelAuthored reports whether a conversation block's
// heading names another model as its author rather than the human partner.
func conversationHeadingIsModelAuthored(heading string) bool {
	return strings.ToLower(strings.TrimSpace(heading)) == modelAuthoredConversationRole
}

// modelAuthoredConversationBlock reports whether a conversation block's heading
// names a model as its author: the model's own reply, or a partner-role entry
// another model wrote.
func modelAuthoredConversationBlock(heading string) bool {
	if conversationHeadingIsModelAuthored(heading) {
		return true
	}
	role, ok := conversationRoleForHeading(heading)
	return ok && role == "agent"
}

// sectionBlocks returns where heading's section body begins in text, and the
// "### " blocks that body holds. A text with no such section answers 0 and no
// blocks.
func sectionBlocks(text, heading string) (int, []checkpointMarkdownBlock) {
	if !strings.Contains(text, heading+"\n") {
		return 0, nil
	}
	_, body, end, ok := markdownSectionSpan(text, heading)
	if !ok {
		return 0, nil
	}
	return body, parseMarkdownBlocks(text[body:end])
}

// blockBodySpans returns the fenced-body range of every block include accepts,
// in text's coordinates. base is where the parsed section begins in text.
func blockBodySpans(base int, blocks []checkpointMarkdownBlock, include func(string) bool) []textSpan {
	var spans []textSpan
	for _, block := range blocks {
		if block.BodyStart < 0 || (include != nil && !include(block.Heading)) {
			continue
		}
		spans = append(spans, textSpan{start: base + block.BodyStart, end: base + block.BodyEnd})
	}
	return spans
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
// the author is per block: an agent or assistant block is the model's reply, and
// a parent-agent block is a partner-role entry another model wrote, so both are
// escaped; a user block is the partner's own message and keeps its bytes even
// when it contains a heading-shaped line or a frame the partner typed. A
// session-log checkpoint's log body is a rendering of the model's own summarized
// actions and is escaped too.
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
	base, notes := sectionBlocks(text, "## Working Notes")
	spans = append(spans, blockBodySpans(base, notes, nil)...)
	base, conversation := sectionBlocks(text, checkpointConversationHeading)
	spans = append(spans, blockBodySpans(base, conversation, modelAuthoredConversationBlock)...)
	return append(spans, sessionLogBodySpans(text)...)
}

// earlierSummarySpans returns the ranges that defuse a checkpoint's folded
// summaries: the blocks under "## Earlier Summaries".
//
// A summary the renderer wrote is carried whole, so its own layout still names
// the bodies the model wrote. Only those bodies are escaped: the block's harness
// markers, a fenced example the model quoted and the partner's messages the
// summary carries keep every byte. A summary in an older layout, or one whose
// layout cannot be read, is the model's text throughout and is escaped whole,
// which is where every block under the heading stood before the carried layout
// was recognized.
func earlierSummarySpans(text string) []textSpan {
	base, blocks := sectionBlocks(text, "## Earlier Summaries")
	var spans []textSpan
	for _, block := range blocks {
		start, stop := base+block.BodyStart, base+block.BodyEnd
		// writeMarkdownFence ends a body with a newline it does not own, and the
		// carried layout is compared byte for byte, so the block is read without
		// that line break and the escape is spliced back under it.
		carried := strings.TrimSuffix(text[start:stop], "\n")
		inner := summaryModelSpans(carried)
		if len(inner) == 0 {
			spans = append(spans, textSpan{start: start, end: stop})
			continue
		}
		for _, inner := range inner {
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
		blocks := parseMarkdownBlocks(region)
		// The log's delimiter opens on the line right after the label. A region
		// whose first block opens later has no log body here — an empty log
		// writes "(no entries)" — and the block below it belongs to some other
		// section.
		first := strings.IndexByte(region, '\n')
		if len(blocks) == 0 || first < 0 || blocks[0].BodyStart != first+1 {
			return nil
		}
		start, stop := offset+blocks[0].BodyStart, offset+blocks[0].BodyEnd
		return []textSpan{{start: start, end: stop}}
	}
	return nil
}

// EscapeRetainedSummary returns a summary turn's text with the frames the model
// authored defused, and reports whether any byte changed.
//
// A summary names the bodies the model wrote, and which they are depends on the
// layout it was written in. The layout the renderer writes now fences the
// model's text under the summary heading and follows it with the partner's
// messages and any parent steer in their own conversation section. A summary in
// the older layout is the model's reply from its opening marker to its end
// marker, with an optional pointer line after the opening. In both, only those
// bodies are escaped, and a turn that carries none keeps every byte.
//
// The stored turn keeps its bytes; this is the copy handed back to the model.
// The turn's markers, the pointer line, the partner-carry reminder, the section
// and block headings, the fence delimiters and every partner-headed block keep
// every byte, and each model-authored body is escaped as one block so an intact
// fenced example inside it is spared.
func EscapeRetainedSummary(text string) (string, bool) {
	return escapeModelSpans(text, summaryModelSpans(text))
}

// summaryModelSpans returns the ranges of a summary turn the model wrote: the
// text fenced under the summary heading and the carried blocks a heading names a
// model as the author of, and the reply between the markers when the turn
// carries the older layout. A turn with no model-authored body answers no spans.
func summaryModelSpans(text string) []textSpan {
	if !strings.ContainsAny(text, "<[") {
		return nil
	}
	spans := summaryHeadingModelSpans(text)
	if len(spans) == 0 {
		return legacySummaryModelSpans(text)
	}
	// The heading names the model's own body whether or not the rest of the
	// turn is intact, so a copy whose carried conversation a budget cut away
	// still answers it. The blocks under the carried conversation heading are
	// the model's only when the whole turn recomposes as the layout the
	// renderer writes, because that layout is what says whose block each
	// partner-role heading heads.
	if !carriesPartnerMessages(text) {
		return spans
	}
	base, conversation := sectionBlocks(text, checkpointConversationHeading)
	return append(spans, blockBodySpans(base, conversation, modelAuthoredConversationBlock)...)
}

// summaryHeadingModelSpans returns the range of the one body a summary turn puts
// under its summary heading: the model's own text, fenced, and the heading's
// only content.
//
// The heading is read only behind the opening this package writes, its fence
// must open the section, and the section must hold nothing below that fence but
// blank lines. A reply that merely writes the heading, or the heading and a
// fenced example below its own prose, is not this layout. The section ends at
// the conversation heading the renderer writes below it, so a turn whose carried
// conversation a budget cut away still answers its model body here.
//
// A budget can also cut the summary inside the fenced body itself, leaving the
// fence open to the end of the text. That body is the model's text as much as a
// closed one is, and the fence that opened it still says where it begins, so its
// range is answered from the block the open fence starts rather than refusing the
// whole layout.
func summaryHeadingModelSpans(text string) []textSpan {
	start, body, end, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok || !isSummaryTurnOpening(text[:start]) {
		return nil
	}
	section := text[body:end]
	if _, ok := soleFencedText(section); !ok {
		// Only the clipped shape is read here: the fence opens the section and
		// nothing closes it, which is what a cut inside the body leaves. Prose
		// before a fence, or prose after a closed one, is a reply writing under
		// this heading rather than the layout, and it answers no body.
		if end != len(text) || !opensUnclosedFence(section) {
			return nil
		}
	}
	blocks := parseMarkdownBlocks(section)
	if len(blocks) != 1 || blocks[0].BodyStart < 0 {
		return nil
	}
	return []textSpan{{start: body + blocks[0].BodyStart, end: body + blocks[0].BodyEnd}}
}

// opensUnclosedFence reports whether section's first non-blank line opens a
// fenced block that no later line closes, which is the shape a budget leaves
// when it cuts a summary inside the body fenced under its heading.
func opensUnclosedFence(section string) bool {
	lines := strings.Split(section, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	var fence mdfence.Tracker
	if len(lines) == 0 || !fence.Step(lines[0]) {
		return false
	}
	return !slices.ContainsFunc(lines[1:], fence.Step)
}

// legacySummaryModelSpans returns the reply a summary in the older layout
// holds: everything between the opening marker and the end marker, less the
// pointer line the turn may open with. That layout fences nothing and carries
// no conversation section, so the whole span is the model's text and is escaped
// as one block.
//
// The markers are what say where the reply begins and ends, so a reply that
// names the summary heading in its own text is still the model's throughout. A
// text with no end marker may be a prefix of either layout and answers no spans.
func legacySummaryModelSpans(text string) []textSpan {
	start, ok := legacySummaryBodyStart(text)
	if !ok {
		return nil
	}
	end := strings.LastIndex(text, summaryMarkerEnd)
	if end < start || strings.TrimSpace(text[end+len(summaryMarkerEnd):]) != "" {
		return nil
	}
	if strings.TrimSpace(text[start:end]) == "" {
		return nil
	}
	return []textSpan{{start: start, end: end}}
}

// legacySummaryBodyStart returns where a summary turn's own text begins: just
// past the opening marker, and past the one pointer line when the turn carries
// it.
func legacySummaryBodyStart(text string) (int, bool) {
	rest, ok := strings.CutPrefix(text, summaryTurnStart)
	if !ok {
		return 0, false
	}
	at := len(summaryTurnStart)
	if line, after, found := strings.Cut(rest, "\n"); found && strings.HasPrefix(line, summaryPointerLead) {
		at += len(rest) - len(after)
	}
	return at, true
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
// line begins, the body's first line included. Both the heading and the end are
// looked for only OUTSIDE fenced code blocks. Rendered entries are fenced and
// legitimately contain lines like "## 0" (a user message), a pasted
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
		case strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "[END CHECKPOINT]") || strings.HasPrefix(content, summaryMarkerEnd):
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

// parseMarkdownBlocks reads section's "### " blocks, each under the nearest
// heading above it that is outside every fenced block, and the one block of a
// fenced body written before the first heading, which answers the empty heading.
//
// A block's Text is its body's bytes with their edges trimmed, and BodyStart
// and BodyEnd are that body's untrimmed range, so the whitespace the trimming
// drops stays outside the range. A body that is never closed runs to the end of
// section, which is where its content ends for a reader too. Both offsets are
// -1 for a block that holds no fenced body.
func parseMarkdownBlocks(section string) []checkpointMarkdownBlock {
	var blocks []checkpointMarkdownBlock
	var heading string
	var lines []string
	bodyStart, bodyEnd := -1, -1
	var fence mdfence.Tracker
	sawFence := false
	offset := 0

	flush := func() {
		if heading == "" && bodyStart < 0 {
			// Only a fenced body is a block before the first heading: prose
			// written there belongs to the section heading, not to a block.
			return
		}
		if bodyStart >= 0 && bodyEnd < 0 {
			bodyEnd = len(section)
		}
		if text := trimBlockEdges(strings.Join(lines, "\n")); text != "" {
			blocks = append(blocks, checkpointMarkdownBlock{
				Heading:   heading,
				Text:      text,
				BodyStart: bodyStart,
				BodyEnd:   bodyEnd,
			})
		}
	}

	for _, line := range strings.SplitAfter(section, "\n") {
		if line == "" {
			break
		}
		content := strings.TrimSuffix(line, "\n")
		// The fence is stepped before a heading is read: a "### " line indented
		// less than a list item's content ends the item's fence, and is read as
		// a heading rather than as fenced text. A fence opened before the first
		// heading is tracked the same way, so a heading inside it is not read as
		// one.
		wasInside := fence.Inside()
		boundary := fence.Step(content)
		// A line ends the block it is in either by closing its delimiter, which
		// leaves no block open, or by leaving the list item that holds it, which
		// the tracker reports as a step that also left a block open. Its own line
		// is not part of the body, which is why the body ends where the line
		// begins.
		ended := wasInside && (boundary || !fence.Inside())
		if ended {
			bodyEnd = offset
		}
		if !fence.Inside() && strings.HasPrefix(content, "### ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(content, "### "))
			lines = nil
			sawFence = false
			bodyStart, bodyEnd = -1, -1
			offset += len(line)
			continue
		}
		if fence.Inside() && (!wasInside || ended) {
			bodyStart, bodyEnd = offset+len(line), -1
		}
		switch {
		case boundary:
			sawFence = true
		case fence.Inside():
			lines = append(lines, content)
		case !sawFence && strings.TrimSpace(content) != "":
			lines = append(lines, content)
		}
		offset += len(line)
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
		// The text stays as written here and in the collected copy; the escape
		// runs once, at render time (renderCheckpointConversation), on the copy
		// the reader finally gets. Escaping earlier would escape a text the
		// fitting can still cut, and a cut can drop the delimiter that made a
		// frame a quotation.
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
