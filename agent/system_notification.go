package agent

import "fmt"

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
