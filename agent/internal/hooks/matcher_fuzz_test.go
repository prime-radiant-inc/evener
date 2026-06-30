package hooks

import (
	"regexp"
	"strings"
	"testing"
)

// FuzzMatchTarget drives matchTarget — the Claude-compatible hook matcher that
// classifies a matcher string into wildcard / exact-or-pipe-list / regexp modes
// and tests it against a target tool name. Input is an arbitrary matcher and an
// arbitrary target.
//
// Oracles (never bare no-panic):
//   - A trimmed "" or "*" matcher matches every target with no error.
//   - When the trimmed matcher is exact-class ([A-Za-z0-9_|] only) the result is
//     never an error and equals membership of target in the '|'-split segments —
//     pure set membership, independent of regexp.
//   - When matchTarget falls through to regexp mode and compiles, its verdict
//     equals regexp.MatchString; a compile error yields (false, err).
//   - matchTarget is deterministic.
func FuzzMatchTarget(f *testing.F) {
	f.Add("Bash", "Bash")
	f.Add("Edit|Write", "Write")
	f.Add("Bash", "BashOutput")
	f.Add(" Bash ", "Bash")
	f.Add("*", "anything")
	f.Add("", "anything")
	f.Add("^Bash.*", "BashOutput")
	f.Add("[", "x")
	f.Add("a|b|c", "")
	f.Add("Read", "read")

	f.Fuzz(func(t *testing.T, matcher, target string) {
		ok, err := matchTarget(matcher, target)

		// Determinism.
		if ok2, err2 := matchTarget(matcher, target); ok2 != ok || (err == nil) != (err2 == nil) {
			t.Fatalf("matchTarget not deterministic for (%q,%q)", matcher, target)
		}

		m := strings.TrimSpace(matcher)

		switch {
		case m == "" || m == "*":
			if !ok || err != nil {
				t.Fatalf("wildcard matcher %q: got (%v,%v), want (true,nil)", matcher, ok, err)
			}

		case isExactMatcher(m):
			if err != nil {
				t.Fatalf("exact matcher %q returned error %v", matcher, err)
			}
			want := false
			for _, seg := range strings.Split(m, "|") {
				if seg == target {
					want = true
					break
				}
			}
			if ok != want {
				t.Fatalf("exact matcher %q vs %q: got %v, want %v", matcher, target, ok, want)
			}

		default:
			// Regexp mode: verdict must equal a fresh RE2 match, or be a
			// (false, err) on compile failure.
			re, compileErr := regexp.Compile(m)
			if compileErr != nil {
				if ok || err == nil {
					t.Fatalf("uncompilable matcher %q: got (%v,%v), want (false, err)", matcher, ok, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("compilable regexp matcher %q returned error %v", matcher, err)
			}
			if want := re.MatchString(target); ok != want {
				t.Fatalf("regexp matcher %q vs %q: got %v, want %v", matcher, target, ok, want)
			}
		}
	})
}
