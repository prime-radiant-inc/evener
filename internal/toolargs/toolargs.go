// Package toolargs holds shared helpers for reading common tool-argument
// fields from decoded JSON args.
//
// The intent reader family (toolStartDescription, toolIntent,
// ToolIntentFromArguments, and the TUI toolsummary/msgrender renderers) all
// resolve a tool call's short description the same way: walk a fixed list of
// candidate keys in priority order and return the first one whose extracted
// value is non-empty. The per-site behavior that differs — which keys to
// consider, how a value is extracted from the decoded map, and whether
// surrounding whitespace is trimmed — is captured entirely in each caller's
// get closure and key list, so sharing the selection loop changes no
// behavior. FirstNonEmpty is that loop.
package toolargs

// FirstNonEmpty returns the first non-empty result of get for the given keys,
// in order. It is the shared key-selection loop duplicated by the intent
// readers across agent/, internal/apptranscript, and cmd/evener-tui/internal.
// get decides how a key maps to a string (string-only, scalar coercion, with
// or without strings.TrimSpace); the caller passes the key set. An empty
// string from get is treated as "absent" and the next key is tried.
func FirstNonEmpty(get func(string) string, keys ...string) string {
	for _, key := range keys {
		if v := get(key); v != "" {
			return v
		}
	}
	return ""
}
