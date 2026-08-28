package envvars

import "strings"

// FirstNonEmpty returns the first argument whose trimmed value is non-empty.
// If all arguments are empty or whitespace-only, it returns "".
//
// This is the canonical implementation shared across all evener modules.
// The TrimSpace semantics ensure whitespace-only strings are treated as
// empty, matching the behavior used by the majority of former call sites.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
