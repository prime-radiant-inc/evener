package contextmgr

import (
	"encoding/json"
	"fmt"
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
		case fence.Step(content):
		case fence.Inside():
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
	var fence mdfence.Tracker
	for _, line := range strings.SplitAfter(text, "\n") {
		if !fence.Step(strings.TrimSuffix(line, "\n")) && !fence.Inside() {
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
		if task := extractOriginalPromptLine(text, "Original task: "); task != "" {
			return []string{task}
		}
	}

	return nil
}
