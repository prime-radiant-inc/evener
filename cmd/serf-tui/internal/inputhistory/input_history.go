// Package inputhistory holds the in-memory input-history limits and escaping
// helpers for the TUI's composer. Hub-mode does not persist input history to
// disk; entries live only for the current session.
package inputhistory

import "strings"

// MaxHistoryEntries caps how many input-history entries are retained in memory.
const MaxHistoryEntries = 1000

// UnescapeHistory reverses the newline escaping the in-memory history uses
// (so multi-line entries occupy a single slice element). Hub-mode does not
// persist input history to disk; entries live only for the current session.
func UnescapeHistory(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}
