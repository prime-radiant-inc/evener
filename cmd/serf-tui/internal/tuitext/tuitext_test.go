package tuitext_test

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitext"
)

func TestNonEmptyStrings(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		expected []string
	}{
		{
			name:     "nil",
			values:   nil,
			expected: []string{},
		},
		{
			name:     "empty",
			values:   []string{},
			expected: []string{},
		},
		{
			name:     "all whitespace",
			values:   []string{" ", "  ", "\t", "\n"},
			expected: []string{},
		},
		{
			name:     "mixed",
			values:   []string{"", "hello", "  ", "world", "\t"},
			expected: []string{"hello", "world"},
		},
		{
			name:     "preserves order",
			values:   []string{"a", "", "b", "", "c"},
			expected: []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.NonEmptyStrings(tt.values)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("NonEmptyStrings(%q) = %q; want %q", tt.values, got, tt.expected)
			}
		})
	}
}

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		width    int
		expected string
	}{
		{
			name:     "width <= 0",
			text:     "hello",
			width:    0,
			expected: "",
		},
		{
			name:     "text shorter than width",
			text:     "hi",
			width:    10,
			expected: "hi",
		},
		{
			name:     "text longer than width",
			text:     "hello world",
			width:    8,
			expected: "hello...",
		},
		{
			name:     "text exactly width",
			text:     "hello",
			width:    5,
			expected: "hello",
		},
		{
			name:     "width <= 3 hard slice",
			text:     "abcdef",
			width:    3,
			expected: "abc",
		},
		{
			// "あいう" has display width 6; at width 3 only the first wide
			// rune (width 2) fits — adding the next would reach width 4 > 3.
			name:     "unicode text wide characters no ellipsis room",
			text:     "あいう",
			width:    3,
			expected: "あ",
		},
		{
			// Wide runes with room for an ellipsis: reserve 3 columns for
			// "...", then fit content by display width. "あい" is width 4,
			// plus "..." is width 7 <= 8.
			name:     "unicode text wide characters with ellipsis",
			text:     "あいうえお",
			width:    8,
			expected: "あい...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.TruncateText(tt.text, tt.width)
			if got != tt.expected {
				t.Errorf("TruncateText(%q, %d) = %q; want %q", tt.text, tt.width, got, tt.expected)
			}
			if tt.width > 0 && lipgloss.Width(got) > tt.width {
				t.Errorf("TruncateText(%q, %d) = %q has display width %d; want <= %d",
					tt.text, tt.width, got, lipgloss.Width(got), tt.width)
			}
		})
	}
}

func TestTruncateMultilineText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		width    int
		expected string
	}{
		{
			name:     "empty",
			text:     "",
			width:    10,
			expected: "",
		},
		{
			name:     "single line",
			text:     "hello world",
			width:    8,
			expected: "hello...",
		},
		{
			name:     "multiple lines",
			text:     "hello world\ngoodbye world",
			width:    8,
			expected: "hello...\ngoodb...",
		},
		{
			name:     "long lines",
			text:     "a very long line here\nanother very long line",
			width:    5,
			expected: "a ...\nan...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.TruncateMultilineText(tt.text, tt.width)
			if got != tt.expected {
				t.Errorf("TruncateMultilineText(%q, %d) = %q; want %q", tt.text, tt.width, got, tt.expected)
			}
		})
	}
}

func TestShellSectionLineCount(t *testing.T) {
	tests := []struct {
		name     string
		section  string
		expected int
	}{
		{
			name:     "empty",
			section:  "",
			expected: 0,
		},
		{
			name:     "no newlines",
			section:  "hello world",
			expected: 1,
		},
		{
			name:     "trailing newline",
			section:  "hello\n",
			expected: 1,
		},
		{
			name:     "multiple lines",
			section:  "line1\nline2\nline3\n",
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.ShellSectionLineCount(tt.section)
			if got != tt.expected {
				t.Errorf("ShellSectionLineCount(%q) = %d; want %d", tt.section, got, tt.expected)
			}
		})
	}
}

func TestLimitFirstLines(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		maxLines int
		expected string
	}{
		{
			name:     "maxLines <= 0",
			text:     "line1\nline2\nline3",
			maxLines: 0,
			expected: "line1\nline2\nline3",
		},
		{
			name:     "fewer lines than max",
			text:     "line1\nline2",
			maxLines: 5,
			expected: "line1\nline2",
		},
		{
			name:     "more lines than max",
			text:     "line1\nline2\nline3\nline4",
			maxLines: 2,
			expected: "line1\nline2",
		},
		{
			name:     "exactly max",
			text:     "line1\nline2\nline3",
			maxLines: 3,
			expected: "line1\nline2\nline3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.LimitFirstLines(tt.text, tt.maxLines)
			if got != tt.expected {
				t.Errorf("LimitFirstLines(%q, %d) = %q; want %q", tt.text, tt.maxLines, got, tt.expected)
			}
		})
	}
}

func TestMultilineLines(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected []string
	}{
		{
			name:     "empty",
			text:     "",
			expected: nil,
		},
		{
			name:     "no trailing newline",
			text:     "line1\nline2",
			expected: []string{"line1", "line2"},
		},
		{
			name:     "trailing newline",
			text:     "line1\nline2\n",
			expected: []string{"line1", "line2"},
		},
		{
			name:     "multiple lines",
			text:     "a\nb\nc\n",
			expected: []string{"a", "b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tuitext.MultilineLines(tt.text)
			if !reflect.DeepEqual(got, tt.expected) {
				t.Errorf("MultilineLines(%q) = %q; want %q", tt.text, got, tt.expected)
			}
		})
	}
}
