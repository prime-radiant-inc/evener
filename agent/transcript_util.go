package agent

import "strings"

// firstLineClamp returns the first non-empty line of s with internal whitespace
// flattened (all runs of whitespace collapsed to a single space, leading/trailing
// trimmed), then clamped to limit runes with a trailing ellipsis when truncated. It
// is the shared helper used by the markdown/outline renderers and the find catalog to
// stop un-named sessions from dumping a full multi-paragraph prompt.
func firstLineClamp(s string, limit int) string {
	firstLine := ""
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			firstLine = strings.Join(fields, " ")
			break
		}
	}
	if firstLine == "" {
		return ""
	}
	runes := []rune(firstLine)
	if len(runes) <= limit {
		return firstLine
	}
	return string(runes[:limit]) + "…"
}
