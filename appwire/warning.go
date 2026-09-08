package appwire

import "strings"

// EffectiveMessage returns the human-facing message of a warning frame:
// Message when set, otherwise the message carried by Warning, which the
// contract types as `any` and producers send either as the raw event
// object or as a bare string. Empty when the frame carries neither.
func (p WarningParams) EffectiveMessage() string {
	if strings.TrimSpace(p.Message) != "" {
		return p.Message
	}
	switch warning := p.Warning.(type) {
	case string:
		return warning
	case map[string]any:
		if message, ok := warning["message"].(string); ok {
			return message
		}
	}
	return ""
}
