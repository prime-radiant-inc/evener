package agent

import (
	"fmt"
	"strings"
)

// systemNotification wraps msg in a <system-notification> faux-XML block.
// These blocks are used for one-way notifications to the model that are not
// steering messages (which use <SYSTEM-REMINDER>) — e.g. tool-output context
// like a skill's directory path, or a callback-cancellation notice.
func systemNotification(msg string) string {
	return "<system-notification>" + msg + "</system-notification>"
}

// systemNotificationf is the format variant of systemNotification.
func systemNotificationf(format string, args ...any) string {
	return systemNotification(fmt.Sprintf(format, args...))
}

// systemReminder wraps msg in a <system-reminder> faux-XML block. These
// blocks are used for terse, single-line notices delivered as tool output —
// e.g. the self-influence breaker's disengagement nudge. This is distinct from
// systemReminderBlock, which wraps multi-line steering content in uppercase
// <SYSTEM-REMINDER> tags.
func systemReminder(msg string) string {
	return "<system-reminder>" + msg + "</system-reminder>"
}

// systemReminderf is the format variant of systemReminder.
func systemReminderf(format string, args ...any) string {
	return systemReminder(fmt.Sprintf(format, args...))
}

// systemReminderBlock wraps inner content in a multi-line <SYSTEM-REMINDER>
// faux-XML block. Used for steering messages the model treats as system
// direction rather than conversational content — e.g. task reminders and the
// interrupt marker. The content is written between the opening and closing
// tags with newline separators.
func systemReminderBlock(inner string) string {
	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	b.WriteString(inner)
	if !strings.HasSuffix(inner, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}

// systemReminderBlockBuilder returns a strings.Builder pre-seeded with the
// opening <SYSTEM-REMINDER> tag. The caller writes inner content and then
// calls finishSystemReminderBlock to close the block.
func systemReminderBlockBuilder() *strings.Builder {
	var b strings.Builder
	b.WriteString("<SYSTEM-REMINDER>\n")
	return &b
}

// finishSystemReminderBlock closes a builder started by systemReminderBlockBuilder.
func finishSystemReminderBlock(b *strings.Builder) string {
	b.WriteString("</SYSTEM-REMINDER>")
	return b.String()
}
