package sysmsg

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
)

// modelAuthoredFrameOpeners are the line-start spellings that open a harness
// input frame: a block the harness itself appends to the conversation, or the
// block a real reply from the human partner arrives in. Text the model wrote
// must never present as one of them, because the user-turn channel is the
// authorization channel (#6493): a fabricated [answers] block reads as a
// decision the partner made, and a fabricated <delegate-notification> reads as
// a runtime fact the harness never reported.
//
// Only the opening character is rewritten (see EscapeModelAuthoredFrames), so
// this list is about recognising the frame's first character run, not about
// parsing the frame.
var modelAuthoredFrameOpeners = []string{
	"<delegate-notification",
	"<job-notification",
	"<delegate-user-note",
	"<system-notification",
	"<SYSTEM-REMINDER",
	"<system-reminder",
	"<environment_context",
	"<environment-context",
	"<shared-notes",
	"<skill-context",
	"[answers]",
	"[CONTEXT SUMMARY]",
	"[CONTEXT CHECKPOINT]",
	"[END SUMMARY]",
	"[END CHECKPOINT]",
}

// EscapeModelAuthoredFrames returns text with every line-start harness input
// frame defused, so model-authored prose cannot pass to a later request as a
// frame the harness wrote. The frame's opening character is rewritten to its
// character reference ("&lt;" or "&#91;"), which leaves the text legible and
// the line otherwise byte-identical while denying the line the exact spelling
// a real frame opens with. The stored turn keeps its own bytes: callers escape
// the copy they hand the model or place in a compaction turn, never the turn
// they persist.
//
// Code is not prose and is left alone: a line inside a fenced block, indented
// four or more columns, or beginning with a block quote marker is the model
// quoting a frame rather than emitting one, and escaping it would corrupt a
// legitimate example.
//
// One pass, so the escape is idempotent: an already-escaped opener no longer
// matches, and its "&lt;" is not itself a frame. Text with no frame opener
// returns unchanged and unallocated.
func EscapeModelAuthoredFrames(text string) string {
	if !strings.ContainsAny(text, "<[") {
		return text
	}
	var b strings.Builder
	var fence mdfence.Tracker
	changed := false
	for _, line := range splitKeepingNewlines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if !fence.StepInBlock(content) && !indentedCode(content) && !isQuotedLine(content) {
			if escaped, ok := escapeFrameOpener(content); ok {
				line = escaped + line[len(content):]
				changed = true
			}
		}
		b.WriteString(line)
	}
	if !changed {
		return text
	}
	return b.String()
}

// escapeFrameOpener rewrites the opening character of content's frame prefix,
// reporting false when content starts with no frame opener.
func escapeFrameOpener(content string) (string, bool) {
	trimmed := strings.TrimLeft(content, " ")
	if trimmed == "" {
		return "", false
	}
	indent := len(content) - len(trimmed)
	for _, opener := range modelAuthoredFrameOpeners {
		if !strings.HasPrefix(trimmed, opener) {
			continue
		}
		switch trimmed[0] {
		case '<':
			return content[:indent] + "&lt;" + trimmed[1:], true
		case '[':
			return content[:indent] + "&#91;" + trimmed[1:], true
		default:
			return "", false
		}
	}
	return "", false
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

// splitKeepingNewlines splits text after each newline, so joining the result
// reproduces text exactly.
func splitKeepingNewlines(text string) []string {
	lines := make([]string, 0, strings.Count(text, "\n")+1)
	for text != "" {
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			lines = append(lines, text)
			break
		}
		lines = append(lines, text[:nl+1])
		text = text[nl+1:]
	}
	return lines
}
