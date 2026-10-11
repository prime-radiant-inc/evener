// Package lineend normalizes text line endings, so every reader in the agent
// agrees on what a line is.
package lineend

import "strings"

// Normalize turns each CRLF and lone CR line ending in text into LF. Text with
// no carriage return comes back unchanged, without a copy.
func Normalize(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}
