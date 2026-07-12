package kimicoding

import (
	"strings"
	"testing"
)

func FuzzUserAgent(f *testing.F) {
	f.Add("Claude Code")
	f.Add("")
	f.Fuzz(func(t *testing.T, product string) {
		if strings.TrimSpace(UserAgent) == "" {
			t.Fatal("UserAgent is empty")
		}
		if strings.ContainsAny(UserAgent, "\r\n") {
			t.Fatalf("UserAgent contains a header delimiter: %q", UserAgent)
		}
		_ = product + UserAgent
	})
}
