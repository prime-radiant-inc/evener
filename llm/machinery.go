package llm

import "strings"

// The faux-XML tags wrapping one-way machinery notifications to the model
// (e.g. a stored-attachment path note). This package is the single source of
// the spelling: the producer (agent's systemNotification helper) builds blocks
// from these constants, and the consumers — transcript's decode-time
// inference for entries written before the Machinery flag existed — match the
// same tags, so they cannot drift apart.
const (
	SystemNotificationOpenTag  = "<system-notification>"
	SystemNotificationCloseTag = "</system-notification>"
)

// IsMachineryNotificationText reports whether text is exactly one machinery
// notification block: the opening tag, the inner message, the closing tag,
// and nothing else on either end. It backs the decode-time inference that
// classifies entries written before the Machinery flag existed; display-time
// filtering must use the flag, not this predicate — a user pasting this
// exact shape is still the user's own words.
func IsMachineryNotificationText(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, SystemNotificationOpenTag) &&
		strings.HasSuffix(trimmed, SystemNotificationCloseTag)
}

// MachineryText returns a text part flagged as machinery — content the
// session manufactured for the model, not user prose.
func MachineryText(text string) ContentPart {
	return ContentPart{Kind: ContentText, Text: text, Machinery: true}
}

// UserMachinery returns a user-role message whose single text part is
// flagged as machinery.
func UserMachinery(text string) Message {
	return Message{Role: RoleUser, Content: []ContentPart{MachineryText(text)}}
}
