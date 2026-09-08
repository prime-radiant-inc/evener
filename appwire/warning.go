package appwire

import (
	"encoding/json"
	"strings"
)

// EffectiveMessage returns the message a warning frame carries, total over
// every shape Warning (typed `any` on the wire) can take: Message wins when
// non-blank; otherwise Warning carries the message when it is itself a
// non-blank string, or an object whose own "message" entry is a non-blank
// string. Every other shape — a number, a bool, an array, null, an absent
// field, an object with no "message", an object whose "message" is not a
// string, or a blank string standing in for either — carries no message.
// Those shapes are not stringified into a message: a number or an array is
// not text a producer meant a human to read, and manufacturing one would
// dress up debug noise as a diagnosis. DecodeWarningParams falls back to the
// frame itself for exactly this reason.
func (p WarningParams) EffectiveMessage() string {
	if strings.TrimSpace(p.Message) != "" {
		return p.Message
	}
	switch warning := p.Warning.(type) {
	case string:
		if strings.TrimSpace(warning) != "" {
			return warning
		}
	case map[string]any:
		if message, ok := warning["message"].(string); ok && strings.TrimSpace(message) != "" {
			return message
		}
	}
	return ""
}

// DecodeWarningParams reads a warning frame into its declared type and
// returns the message a reader should render. It returns no error: a field
// the contract cannot type — a Cause that is not an object, say — still
// leaves every field json did decode populated, and rejecting the whole
// frame over that one field would take Message, Title, Source and Hint down
// with it. A frame that yields no message at all under EffectiveMessage is
// surfaced as the frame itself, so a malformed warning is visible instead of
// silent.
func DecodeWarningParams(raw json.RawMessage) (WarningParams, string) {
	var params WarningParams
	_ = json.Unmarshal(raw, &params)
	if message := params.EffectiveMessage(); message != "" {
		return params, message
	}
	return params, string(raw)
}
