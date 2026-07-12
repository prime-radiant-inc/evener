package strutil_test

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
)

func FuzzFirstNonEmpty(f *testing.F) {
	for _, input := range []string{"", "a", "\x00a", "\x00\x00last", "first\x00second"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 4096 {
			t.Skip()
		}
		values := strings.Split(input, "\x00")
		got := strutil.FirstNonEmpty(values...)
		for _, value := range values {
			if value != "" {
				if got != value {
					t.Fatalf("FirstNonEmpty(%q) = %q, want %q", values, got, value)
				}
				return
			}
		}
		if got != "" {
			t.Fatalf("FirstNonEmpty(%q) = %q, want empty", values, got)
		}
	})
}
