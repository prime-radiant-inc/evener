package main

import "strings"

const maxHistoryEntries = 1000

// unescapeHistory reverses the newline escaping the in-memory history uses
// (so multi-line entries occupy a single slice element). Hub-mode does not
// persist input history to disk; entries live only for the current session.
func unescapeHistory(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}
