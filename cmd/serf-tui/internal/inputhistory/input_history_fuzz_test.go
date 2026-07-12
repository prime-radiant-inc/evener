package inputhistory_test

import (
	"strings"
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/inputhistory"
)

func FuzzUnescapeHistory(f *testing.F) {
	f.Add(`one\ntwo`)
	f.Add("")
	f.Fuzz(func(t *testing.T, input string) {
		got := inputhistory.UnescapeHistory(input)
		want := strings.ReplaceAll(input, `\n`, "\n")
		if got != want {
			t.Fatalf("UnescapeHistory(%q) = %q, want %q", input, got, want)
		}
	})
}
