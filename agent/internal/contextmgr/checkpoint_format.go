package contextmgr

import (
	"encoding/json"
	"strings"
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

// summaryModelTextHeading heads the model's own text in a summary turn.
const summaryModelTextHeading = "## Summary"

// carriesPartnerMessages reports whether a summary turn has the layout
// summarizeFold writes: the model's text fenced under summaryModelTextHeading,
// with the partner's messages after it in their own conversation section. A
// summary written before that layout is the model's text throughout, whatever
// headings it used, and is carried whole.
func carriesPartnerMessages(text string) bool {
	_, body, _, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimLeft(text[body:], "\n"), "\n")
	_, n := fenceRun(first)
	return n >= 3
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
		if fence.step(line) {
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
type fenceTracker struct {
	char   byte
	length int
}

// inside reports whether the lines stepped so far leave a block open.
func (f *fenceTracker) inside() bool { return f.length > 0 }

// step advances over line and reports whether it opened or closed a block.
func (f *fenceTracker) step(line string) bool {
	char, n := fenceRun(line)
	if n < 3 {
		return false
	}
	if f.length == 0 {
		f.char, f.length = char, n
		return true
	}
	if char == f.char && n >= f.length && strings.TrimSpace(line[n:]) == "" {
		f.length = 0
		return true
	}
	return false
}

// fenceRun returns the fence character a line starts with and how many of it
// lead the line, or 0, 0 when it starts with neither a backtick nor a tilde.
func fenceRun(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 0
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
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
