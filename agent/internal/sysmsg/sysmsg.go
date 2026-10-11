// Package sysmsg formats the system direction the session writes into the
// model's context, so the agent package and the compaction it delegates to
// write it the same way.
package sysmsg

import "strings"

// ReminderBlock wraps inner content in a multi-line <SYSTEM-REMINDER>
// faux-XML block, for direction the model treats as coming from the system
// rather than the conversation: task reminders, the interrupt marker, and
// compaction's note on partner messages it summarized.
func ReminderBlock(inner string) string {
	var b strings.Builder
	b.WriteString(ReminderOpen)
	b.WriteString(inner)
	if !strings.HasSuffix(inner, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(ReminderClose)
	return b.String()
}

// The tags around a ReminderBlock, the opening one with its newline.
const (
	ReminderOpen  = "<SYSTEM-REMINDER>\n"
	ReminderClose = "</SYSTEM-REMINDER>"
)

// IsReminderBlock reports whether text is exactly one ReminderBlock.
func IsReminderBlock(text string) bool {
	return strings.HasPrefix(text, ReminderOpen) && strings.HasSuffix(text, ReminderClose) &&
		strings.Count(text, ReminderClose) == 1
}
