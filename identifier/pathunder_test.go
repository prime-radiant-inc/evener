package identifier

import "testing"

func TestPathUnder(t *testing.T) {
	cases := []struct {
		name      string
		path, dir string
		want      bool
	}{
		{"equal", "/a/b", "/a/b", true},
		{"child", "/a/b/c", "/a/b", true},
		{"sibling sharing a name prefix", "/a/bc", "/a/b", false},
		{"parent", "/a", "/a/b", false},
		{"dot-dot escape", "/a/b/../c", "/a/b", false},
		{"unclean path staying inside", "/a/b/./c/../d", "/a/b", true},
		{"trailing slash on dir", "/a/b/c", "/a/b/", true},
		{"filesystem root contains a child", "/x", "/", true},
		{"filesystem root contains itself", "/", "/", true},
		{"empty dir with absolute path", "/a", "", false},
		{"empty dir with relative path", "a", "", false},
		{"blank dir", "/a", "  ", false},
		{"relative path under absolute dir", "a/b", "/a", false},
		{"absolute path under relative dir", "/a/b", "a", false},
		{"relative path under relative dir", "a/b", "a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PathUnder(tc.path, tc.dir); got != tc.want {
				t.Fatalf("PathUnder(%q, %q) = %v, want %v", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}
