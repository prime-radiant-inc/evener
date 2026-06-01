package agent

import (
	"encoding/json"
	"strings"
)

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
	b.WriteString("## Conversation\n\n")
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

	openTag := "<conversation>"
	closeTag := "</conversation>"
	if idx := strings.Index(text, openTag); idx >= 0 {
		rest := text[idx+len(openTag):]
		if end := strings.Index(rest, closeTag); end >= 0 {
			var entries []checkpointConversationEntry
			if err := json.Unmarshal([]byte(rest[:end]), &entries); err == nil {
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
	section := markdownSection(text, "## Conversation")
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
	return extractCheckpointJSON(text, "working_notes")
}

func markdownSection(text, heading string) string {
	bodyStart := -1
	prefix := heading + "\n"
	if strings.HasPrefix(text, prefix) {
		bodyStart = len(prefix)
	} else if idx := strings.Index(text, "\n"+prefix); idx >= 0 {
		bodyStart = idx + 1 + len(prefix)
	}
	if bodyStart < 0 {
		return ""
	}
	rest := text[bodyStart:]
	end := len(rest)
	for _, marker := range []string{"\n## ", "\n[END CHECKPOINT]"} {
		if idx := strings.Index(rest, marker); idx >= 0 && idx < end {
			end = idx
		}
	}
	return rest[:end]
}

func parseMarkdownBlocks(section string) []checkpointMarkdownBlock {
	var blocks []checkpointMarkdownBlock
	var heading string
	var lines []string
	inFence := false
	fenceMarker := ""
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

	for _, line := range strings.Split(section, "\n") {
		if !inFence && strings.HasPrefix(line, "### ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			lines = nil
			sawFence = false
			continue
		}
		if heading == "" {
			continue
		}
		if marker := markdownFenceMarker(line); marker != "" {
			if !inFence {
				inFence = true
				fenceMarker = marker
				sawFence = true
				continue
			}
			if strings.TrimSpace(line) == fenceMarker {
				inFence = false
				fenceMarker = ""
				continue
			}
		}
		if inFence {
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

func markdownFenceMarker(line string) string {
	count := 0
	for _, r := range line {
		if r != '`' {
			break
		}
		count++
	}
	if count < 3 {
		return ""
	}
	return strings.Repeat("`", count)
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
	if idx := strings.Index(text, openTag); idx >= 0 {
		rest := text[idx+len(openTag):]
		if end := strings.Index(rest, closeTag); end >= 0 {
			var msgs []string
			if err := json.Unmarshal([]byte(rest[:end]), &msgs); err == nil {
				return msgs
			}
		}
	}

	// Legacy format: "Original task: ..." line (only for user_messages tag).
	if tag == "user_messages" {
		if idx := strings.Index(text, "Original task: "); idx >= 0 {
			rest := text[idx+len("Original task: "):]
			if nl := strings.Index(rest, "\n"); nl >= 0 {
				rest = rest[:nl]
			}
			if rest != "" {
				return []string{rest}
			}
		}
	}

	return nil
}
