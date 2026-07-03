package worktree

import "testing"

// FuzzParseGitVersion drives parseGitVersion with arbitrary strings. It
// must never panic, and whenever it reports ok=true, both returned numbers
// must be non-negative (the regex only ever matches digit runs, but Atoi
// overflow handling is worth pinning down here too).
func FuzzParseGitVersion(f *testing.F) {
	seeds := []string{
		"git version 2.43.0",
		"git version 2.32.9",
		"git version 2.33.0.windows.1",
		"git version 2.33.0",
		"git version 3.0.0",
		"git version 2.39.3 (Apple Git-146)",
		"",
		"not a version string",
		"version",
		"version 2",
		"version 2.",
		"version .2",
		"git version 999999999999999999999999.0",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		major, minor, ok := parseGitVersion(s) // must not panic regardless of input
		if !ok {
			return
		}
		if major < 0 || minor < 0 {
			t.Fatalf("parseGitVersion(%q) = %d.%d, ok=true but a version number is negative", s, major, minor)
		}
	})
}
