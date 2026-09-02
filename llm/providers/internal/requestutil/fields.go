package requestutil

import "primeradiant.com/evener/llm/registry"

// WireFieldEnabled reports whether a wire path is explicitly disabled by the
// resolved field table. FieldMaxTokens follows MaxTokensField to its selected
// provider spelling.
func WireFieldEnabled(caps registry.Caps, path string) bool {
	for field, enabled := range caps.Fields {
		wirePath := field
		if field == registry.FieldMaxTokens && caps.MaxTokensField != nil && *caps.MaxTokensField != "" {
			wirePath = *caps.MaxTokensField
		}
		if wirePath == path && !enabled {
			return false
		}
	}
	return true
}
