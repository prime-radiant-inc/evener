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
	b.WriteString("<SYSTEM-REMINDER>\n")
	b.WriteString(inner)
	if !strings.HasSuffix(inner, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}
