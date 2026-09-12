package google

import "slices"

// supportsGenerateContent reports whether a models.list row serves the
// generateContent method, the only one this protocol speaks. Google lists the
// API methods a model accepts, so a row without it (an embedding model, say)
// is not a chat model at all.
func supportsGenerateContent(methods []string) bool {
	return slices.Contains(methods, "generateContent")
}
