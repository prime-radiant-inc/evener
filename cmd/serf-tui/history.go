package main

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	historyFile       = "input_history.txt"
	maxHistoryEntries = 1000
)

// loadHistory reads the input history file and returns up to maxHistoryEntries
// lines. Returns nil (not an error) if the file doesn't exist.
func loadHistory(stateDir string) []string {
	if stateDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(stateDir, historyFile))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	// Filter out empty lines.
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	if len(out) > maxHistoryEntries {
		out = out[len(out)-maxHistoryEntries:]
	}
	return out
}

// appendHistory appends a single entry to the history file (creating the
// directory if needed). Multi-line input is escaped: newlines become \n
// literals so each history entry occupies one line.
func appendHistory(stateDir, text string) {
	if stateDir == "" || text == "" {
		return
	}
	escaped := strings.ReplaceAll(text, "\n", `\n`)
	path := filepath.Join(stateDir, historyFile)
	os.MkdirAll(stateDir, 0o755) //nolint:errcheck
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(escaped + "\n") //nolint:errcheck
}

// unescapeHistory reverses the newline escaping done by appendHistory.
func unescapeHistory(s string) string {
	return strings.ReplaceAll(s, `\n`, "\n")
}
