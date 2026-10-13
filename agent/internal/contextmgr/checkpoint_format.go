package contextmgr

import (
	"encoding/json"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/sysmsg"
)

// checkpointConversationHeading heads the conversation a compaction turn
// carries verbatim.
const checkpointConversationHeading = "## Conversation"

type checkpointConversationEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
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
		writeMarkdownFence(&b, entry.Text)
	}
	return b.String()
}

func renderCheckpointWorkingNotes(notes []string) string {
	clean := make([]string, 0, len(notes))
	for _, note := range notes {
		note = strings.TrimSpace(note)
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
	return strings.TrimSpace(line) != "" && leadingColumns(line) >= 4
}

// leadingColumns returns the columns of whitespace line starts with, a tab
// reaching the next four-column stop. A blank line is all its whitespace.
func leadingColumns(line string) int {
	columns := 0
	for _, r := range line {
		switch r {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return columns
		}
	}
	return columns
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
	var fence fenceTracker
	if len(lines) < 2 || !fence.step(lines[0]) {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if fence.step(lines[i]) {
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
	fence := markdownFence(text)
	b.WriteString(fence)
	b.WriteString("text\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(fence)
	b.WriteString("\n\n")
}

func markdownFence(text string) string {
	longest := 0
	current := 0
	for _, r := range text {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	if longest < 3 {
		longest = 3
	} else {
		longest++
	}
	return strings.Repeat("`", longest)
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
		role := strings.ToLower(strings.TrimSpace(block.Heading))
		switch role {
		case "user":
			entries = append(entries, checkpointConversationEntry{Role: "user", Text: block.Text})
		case "agent", "assistant":
			entries = append(entries, checkpointConversationEntry{Role: "agent", Text: block.Text})
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
			note := strings.TrimSpace(block.Text)
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
// next top-level "## " (or [END CHECKPOINT]) line begins. Both the heading and
// the end are looked for only OUTSIDE fenced code blocks. Rendered entries are
// fenced and legitimately contain lines like "## 0" (a user message), a pasted
// "## Earlier Summaries", or an earlier summary's own "## Working Notes"; a
// fence-blind scan would take those for section boundaries and misread the
// checkpoint on a render/extract round-trip. This mirrors the fence tracking
// parseMarkdownBlocks applies to the section body.
func markdownSectionSpan(text, heading string) (start, body, end int, ok bool) {
	var fence fenceTracker
	start = -1
	offset := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			break
		}
		content := strings.TrimSuffix(line, "\n")
		switch {
		// Fence lines and fenced content are never boundaries.
		case fence.step(content):
		case fence.inside():
		case start < 0:
			if line == heading+"\n" {
				start, body = offset, offset+len(line)
			}
		// The body's first line never ends the section.
		case offset > body && (strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "[END CHECKPOINT]")):
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
	var fence fenceTracker
	for _, line := range strings.SplitAfter(text, "\n") {
		if !fence.step(strings.TrimSuffix(line, "\n")) && !fence.inside() {
			b.WriteString(line)
		}
	}
	return b.String()
}

func parseMarkdownBlocks(section string) []checkpointMarkdownBlock {
	var blocks []checkpointMarkdownBlock
	var heading string
	var lines []string
	var fence fenceTracker
	sawFence := false

	flush := func() {
		if heading == "" {
			return
		}
		text := strings.TrimSpace(strings.Join(lines, "\n"))
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
		isFence := fence.step(line)
		if !fence.inside() && strings.HasPrefix(line, "### ") {
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
		if fence.inside() {
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

// fenceTracker follows Markdown fenced code blocks line by line. A block opens
// on a line starting with three or more backticks or tildes, and closes on a
// line of the same character, at least as long, with nothing after it.
//
// It also follows a fence opened INSIDE a single list item, the shape a model
// writing a bullet with a fenced example produces (#4435):
//
//   - ~~~text
//     quoted
//     ~~~
//
// Markdown opens the block on the item's first line. The item's content
// indentation is recorded when such a fence opens, and the matching closing
// fence is read at that indentation, so the item's own closing line no longer
// reads as a fresh, top-level opener.
//
// This stays a line-oriented subset, not a CommonMark block parser. Only one
// list container is modelled. A fence's own indentation is spaces; the item
// boundary counts columns, a tab reaching the next four-column stop. Only a
// closing fence at or within three columns past the content indentation closes
// a block; a non-blank line indented less than the content leaves the item, and
// the fence with it. Nested containers, block quotes and lazy continuation
// lines are not modelled. A numbered marker other than one cannot interrupt a
// paragraph in CommonMark; this tracker keeps no paragraph state, so it reads
// any list marker at the line's start the same way.
type fenceTracker struct {
	char   byte
	length int
	// container is the content indentation of the list item the tracker is
	// inside, which is the column a fence in the item opens at, or 0 outside a
	// list item. It outlives one block, so a second fence in the same item is
	// read at the item's indentation too, not as a new top-level fence.
	container int
}

// inside reports whether the lines stepped so far leave a block open.
func (f *fenceTracker) inside() bool { return f.length > 0 }

// step advances over line and reports whether it opened or closed a block.
func (f *fenceTracker) step(line string) bool {
	// A non-blank line indented less than the list item's content leaves the
	// item, and any fence the item holds ends with it: the line is read on its
	// own from here, as a new opener or as ordinary content. A blank line stays
	// inside the item.
	if f.container > 0 && strings.TrimSpace(line) != "" && leadingColumns(line) < f.container {
		f.length, f.container = 0, 0
	}
	if f.length == 0 {
		// A fence at the item's content indentation while the item holds (a
		// top-level fence is the container-0 case), else a fence on a new list
		// item's own line. A list fence is only looked for while no block is
		// open: a "- ~~~" line inside an open block is content.
		if char, n, _ := fenceRunAt(line, f.container); n != 0 {
			f.char, f.length = char, n
			return true
		}
		if char, n, container := fenceRunInList(line); n != 0 {
			f.char, f.length, f.container = char, n, container
			return true
		}
		return false
	}
	char, n, after := fenceRunAt(line, f.container)
	if n == 0 {
		return false
	}
	if char == f.char && n >= f.length && strings.TrimSpace(after) == "" {
		f.length = 0
		return true
	}
	return false
}

// fenceRun returns the fence a line starts with, after up to three spaces of
// indentation: its character, its length and the rest of the line. The length
// is 0 when the line does not start with a fence (three or more backticks or
// tildes).
func fenceRun(line string) (char byte, n int, after string) {
	return fenceRunAt(line, 0)
}

// fenceRunAt returns the fence a line starts with when it is indented by base
// columns of list-container indentation and then by up to three more spaces:
// its character, its length and the rest of the line. The length is 0 when the
// line is not indented that far or does not start with a fence.
func fenceRunAt(line string, base int) (char byte, n int, after string) {
	if len(line) < base || strings.TrimLeft(line[:base], " ") != "" {
		return 0, 0, ""
	}
	line = line[base:]
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 || indent == len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, ""
	}
	char = line[indent]
	rest := strings.TrimLeft(line[indent:], string(char))
	n = len(line) - indent - len(rest)
	if n < 3 {
		return 0, 0, ""
	}
	// A backtick fence's info string may not contain a backtick, so a line like
	// "- ```status=done```" is an inline code span, not a fence (#4435).
	if char == '`' && strings.Contains(rest, "`") {
		return 0, 0, ""
	}
	return char, n, rest
}

// fenceRunInList returns the fence a line opens inside a single list item: a
// list marker, then one to four spaces, then a fence. container is the item's
// content indentation: the marker and the spaces after it, which is the column
// the fence opens at, and the column the matching closing fence is read from.
// Its fence is 0 when the line is not a fence on a list item.
func fenceRunInList(line string) (char byte, n, container int) {
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent > 3 || indent == len(line) {
		return 0, 0, 0
	}
	marker, ok := listMarker(line[indent:])
	if !ok {
		return 0, 0, 0
	}
	after := line[indent+marker:]
	spaces := len(after) - len(strings.TrimLeft(after, " "))
	if spaces < 1 || spaces > 4 {
		return 0, 0, 0
	}
	char, n, _ = fenceRun(after[spaces:])
	if n == 0 {
		return 0, 0, 0
	}
	// The item's content indentation is the marker and the spaces that follow
	// it; the opening fence sits at its start. A closing fence may sit up to
	// three columns further (fenceRunAt allows that), but not less.
	return char, n, indent + marker + spaces
}

// listMarker returns the length in bytes of the list marker line starts with:
// "-", "+" or "*", or one to nine digits followed by "." or ")". The space that
// must follow the marker is not part of it.
func listMarker(line string) (int, bool) {
	switch line[0] {
	case '-', '+', '*':
		return 1, true
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 || i >= len(line) || (line[i] != '.' && line[i] != ')') {
		return 0, false
	}
	return i + 1, true
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
		text := strings.TrimSpace(entry.Text)
		if text == "" {
			continue
		}
		out = append(out, checkpointConversationEntry{Role: role, Text: text})
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
		if _, rest, found := strings.Cut(text, "Original task: "); found {
			rest, _, _ = strings.Cut(rest, "\n")
			if rest != "" {
				return []string{rest}
			}
		}
	}

	return nil
}
