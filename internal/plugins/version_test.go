package plugins

import "testing"

func TestComputeVersion(t *testing.T) {
	cases := []struct{ pj, decl, sha, want string }{
		{"1.2.3", "9.9", "abcdef0123456789", "1.2.3"},
		{"", "2.0", "abcdef0123456789", "2.0"},
		{"", "", "abcdef0123456789ff", "abcdef012345"},
		{"", "", "", "unknown"},
	}
	for _, c := range cases {
		if got := computeVersion(c.pj, c.decl, c.sha); got != c.want {
			t.Errorf("computeVersion(%q,%q,%q) = %q, want %q", c.pj, c.decl, c.sha, got, c.want)
		}
	}
}
