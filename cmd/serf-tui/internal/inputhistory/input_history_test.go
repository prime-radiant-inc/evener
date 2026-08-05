package inputhistory_test

import (
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/inputhistory"
)

func TestUnescapeHistory(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "no escaped newlines",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "escaped newlines",
			input:    "hello\\nworld",
			expected: "hello\nworld",
		},
		{
			name:     "mixed content",
			input:    "line1\\nline2\\nline3",
			expected: "line1\nline2\nline3",
		},
		{
			name:     "multiple escaped newlines",
			input:    "\\n\\n\\n",
			expected: "\n\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inputhistory.UnescapeHistory(tt.input)
			if got != tt.expected {
				t.Errorf("UnescapeHistory(%q) = %q; want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMaxHistoryEntries(t *testing.T) {
	if inputhistory.MaxHistoryEntries != 1000 {
		t.Errorf("MaxHistoryEntries = %d; want 1000", inputhistory.MaxHistoryEntries)
	}
}
