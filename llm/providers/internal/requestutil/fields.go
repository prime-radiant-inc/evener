package requestutil

import "primeradiant.com/evener/llm/registry"

// WireFieldEnabled reports whether a wire path is explicitly disabled by the
// resolved field table. FieldMaxTokens follows MaxTokensField to its selected
// provider spelling on the protocols that spell the output limit that way —
// the same rule registry.Prune applies, so a disabled field cannot be restored
// by a finalizer after the prune removed it.
func WireFieldEnabled(caps registry.Caps, path, protocol string) bool {
	for field, enabled := range caps.Fields {
		wirePath := field
		if field == registry.FieldMaxTokens && protocol == registry.ProtocolOpenAIChat && caps.MaxTokensField != nil && *caps.MaxTokensField != "" {
			wirePath = *caps.MaxTokensField
		}
		if wirePath == path && !enabled {
			return false
		}
	}
	return true
}
