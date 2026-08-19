// Package tuitext holds generic, layout-agnostic text helpers for the TUI:
// width-aware truncation and line-list utilities with no dependency on any
// hub or session types.
package tuitext

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// NonEmptyStrings returns the input slice with blank (whitespace-only) entries removed.
func NonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

// TruncateText shortens text to the given display width, appending "..." when
// it must cut (or hard-slicing when the width is too small for an ellipsis).
// Truncation is display-width aware: runes are accumulated by their rendered
// width (so wide CJK runes count as 2 columns), guaranteeing the result never
// exceeds the requested width.
func TruncateText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	if width <= 3 {
		return fitByWidth(runes, width)
	}
	return fitByWidth(runes, width-3) + "..."
}

// fitByWidth returns the longest prefix of runes whose rendered width does not
// exceed limit.
func fitByWidth(runes []rune, limit int) string {
	var b strings.Builder
	used := 0
	for _, r := range runes {
		rw := lipgloss.Width(string(r))
		if used+rw > limit {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

// TruncateMultilineText applies TruncateText to every line of text independently.
func TruncateMultilineText(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = TruncateText(line, width)
	}
	return strings.Join(lines, "\n")
}

// ShellSectionLineCount returns the number of rendered lines in section,
// ignoring a single trailing newline; an empty section counts as zero.
func ShellSectionLineCount(section string) int {
	section = strings.TrimRight(section, "\n")
	if section == "" {
		return 0
	}
	return strings.Count(section, "\n") + 1
}

// LimitFirstLines returns at most the first maxLines lines of text.
func LimitFirstLines(text string, maxLines int) string {
	if maxLines <= 0 {
		return text
	}
	lines := MultilineLines(text)
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:maxLines], "\n")
}

// MultilineLines splits text into lines, dropping a single trailing newline;
// empty text yields nil.
func MultilineLines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
