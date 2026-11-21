package google

import "slices"

// supportsGenerateContent reports whether a models.list row advertises the
// generateContent capability. Retained after the /interactions cutover as the
// models.list capability filter separating chat/generation models from
// non-chat rows (such as embedding models).
func supportsGenerateContent(methods []string) bool {
	return slices.Contains(methods, "generateContent")
}
