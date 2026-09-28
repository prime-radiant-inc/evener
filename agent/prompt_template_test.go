package agent

import "testing"

func TestCollapseBlankLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"\n\n\n", "\n\n"},
		{"\n\n\n\n", "\n\n"},
		{"\n\n", "\n\n"},
		{"a\n\n\nb", "a\n\nb"},
		{"a\n\nb", "a\n\nb"},
		{"no newlines", "no newlines"},
	}
	for _, tt := range tests {
		got := collapseBlankLines(tt.in)
		if got != tt.want {
			t.Errorf("collapseBlankLines(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
