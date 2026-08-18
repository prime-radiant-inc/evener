// Package strutil holds cross-cutting string helpers with no dependencies.
package strutil

// FirstNonEmpty returns the first non-empty string in values, or "" if all are empty.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
