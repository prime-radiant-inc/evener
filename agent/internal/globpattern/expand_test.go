package globpattern

import (
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{name: "ordinary glob", pattern: "src/**/*.go", want: []string{"src/**/*.go"}},
		{name: "alternatives", pattern: "*.{ts,tsx,css}", want: []string{"*.ts", "*.tsx", "*.css"}},
		{name: "nested alternatives", pattern: "src/{a,{b,c}}/*.go", want: []string{"src/a/*.go", "src/b/*.go", "src/c/*.go"}},
		{name: "empty alternative", pattern: "report{,.md}", want: []string{"report", "report.md"}},
		{name: "escaped braces", pattern: `literal\{name\}.go`, want: []string{`literal\{name\}.go`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Expand(tt.pattern)
			if err != nil {
				t.Fatalf("Expand(%q): %v", tt.pattern, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Expand(%q) = %#v, want %#v", tt.pattern, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("Expand(%q)[%d] = %q, want %q", tt.pattern, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExpandRejectsMalformedBraces(t *testing.T) {
	for _, pattern := range []string{"a/{b,c", "a/b}", "a/{b,{c,d}"} {
		t.Run(pattern, func(t *testing.T) {
			if _, err := Expand(pattern); err == nil {
				t.Fatalf("Expand(%q) succeeded, want malformed-brace error", pattern)
			}
		})
	}
}

func TestExpandCapsAlternatives(t *testing.T) {
	pattern := strings.Repeat("{a,b}", 9)
	if _, err := Expand(pattern); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Expand(%q) error = %v, want expansion-limit error", pattern, err)
	}
}
