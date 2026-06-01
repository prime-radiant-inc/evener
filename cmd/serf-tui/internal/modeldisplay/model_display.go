// Package modeldisplay provides helpers for compactly rendering model IDs and
// filesystem paths in the TUI's chip strips and headers.
package modeldisplay

import "strings"

// AbbreviateModel strips the first slash-segment (the instance name) and
// any trailing date suffix from a model ID, so it fits compactly in a
// display context where the instance is already shown in a group header or
// a separate label.
//
//	"openai/gpt-5"                       → "gpt-5"
//	"work/gpt-5"                         → "gpt-5"
//	"openrouter/anthropic/claude-opus-4" → "anthropic/claude-opus-4"
//	"openai/gpt-5-20260101"              → "gpt-5"
//	"bare-model"                         → "bare-model"
func AbbreviateModel(id string) string {
	if id == "" {
		return ""
	}
	// Strip first slash-segment (the instance name), whatever it is.
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	// Strip trailing -YYYYMMDD date suffix
	if len(id) >= 9 && id[len(id)-9] == '-' {
		tail := id[len(id)-8:]
		allDigits := true
		for _, r := range tail {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			id = id[:len(id)-9]
		}
	}
	return id
}

// AbbreviatePath shortens a filesystem path to at most maxLen characters,
// replacing $HOME prefix with ~ and middle-truncating if needed.
func AbbreviatePath(p string, maxLen int) string {
	if len(p) <= maxLen {
		return p
	}
	// Replace /home/<user> prefix with ~
	if strings.HasPrefix(p, "/home/") {
		if i := strings.IndexByte(p[len("/home/"):], '/'); i >= 0 {
			p = "~" + p[len("/home/")+i:]
		}
	}
	if len(p) <= maxLen {
		return p
	}
	// Middle-truncate
	keep := maxLen - 1
	head := keep / 2
	tail := keep - head
	return p[:head] + "…" + p[len(p)-tail:]
}
