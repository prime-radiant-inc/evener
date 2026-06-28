package strutil_test

import (
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
)

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		expected string
	}{
		{
			name:     "all empty",
			values:   []string{"", "", ""},
			expected: "",
		},
		{
			name:     "first non-empty",
			values:   []string{"first", "second", "third"},
			expected: "first",
		},
		{
			name:     "middle non-empty",
			values:   []string{"", "middle", ""},
			expected: "middle",
		},
		{
			name:     "last non-empty",
			values:   []string{"", "", "last"},
			expected: "last",
		},
		{
			name:     "single empty",
			values:   []string{""},
			expected: "",
		},
		{
			name:     "single non-empty",
			values:   []string{"only"},
			expected: "only",
		},
		{
			name:     "no args",
			values:   []string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strutil.FirstNonEmpty(tt.values...)
			if got != tt.expected {
				t.Errorf("FirstNonEmpty(%q) = %q; want %q", tt.values, got, tt.expected)
			}
		})
	}
}
