package execenv

import (
	"path/filepath"
	"testing"
)

// TestSplitLeaf covers splitLeaf for various inputs.
func TestSplitLeaf(t *testing.T) {
	cases := []struct {
		rel      string
		wantDir  string
		wantLeaf string
	}{
		{".", "", ""},
		{"", "", ""},
		{"file.txt", "", "file.txt"},
		{"nested/file.txt", "nested", "file.txt"},
		{"a/b/c", "a/b", "c"},
	}
	for _, tc := range cases {
		dir, leaf := splitLeaf(tc.rel)
		if dir != tc.wantDir || leaf != tc.wantLeaf {
			t.Errorf("splitLeaf(%q) = (%q, %q), want (%q, %q)", tc.rel, dir, leaf, tc.wantDir, tc.wantLeaf)
		}
	}
}

// TestDirOrDot covers dirOrDot for empty and non-empty inputs.
func TestDirOrDot(t *testing.T) {
	if got := dirOrDot(""); got != "." {
		t.Errorf("dirOrDot(\"\") = %q, want %q", got, ".")
	}
	if got := dirOrDot("/foo"); got != "/foo" {
		t.Errorf("dirOrDot(\"/foo\") = %q, want %q", got, "/foo")
	}
}

// TestRelComponents covers relComponents for various inputs.
func TestRelComponents(t *testing.T) {
	if got := relComponents("."); len(got) != 0 {
		t.Errorf("relComponents(\".\") = %v, want nil", got)
	}
	if got := relComponents(""); len(got) != 0 {
		t.Errorf("relComponents(\"\") = %v, want nil", got)
	}
	got := relComponents("a/b/c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("relComponents(\"a/b/c\") = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("relComponents(\"a/b/c\")[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestContainingRoot covers containingRoot for direct and missed matches.
func TestContainingRoot(t *testing.T) {
	roots := []string{"/foo/bar", "/baz"}
	// Direct match.
	root, rel, ok := containingRoot(roots, "/foo/bar")
	if !ok || root != "/foo/bar" || rel != "." {
		t.Errorf("containingRoot exact: root=%q rel=%q ok=%v", root, rel, ok)
	}
	// Child match.
	root, rel, ok = containingRoot(roots, "/foo/bar/child")
	if !ok || root != "/foo/bar" || rel != "child" {
		t.Errorf("containingRoot child: root=%q rel=%q ok=%v", root, rel, ok)
	}
	// No match.
	_, _, ok = containingRoot(roots, "/other")
	if ok {
		t.Error("containingRoot should not match /other")
	}
}

// TestContainingRoot_AbsClean covers the filepath.Clean path in
// containingRoot: a path with redundant separators should be cleaned before
// matching.
func TestContainingRoot_AbsClean(t *testing.T) {
	roots := []string{"/foo/bar"}
	root, _, ok := containingRoot(roots, filepath.Clean("/foo/bar//child"))
	if !ok || root != "/foo/bar" {
		t.Errorf("containingRoot(clean): root=%q ok=%v", root, ok)
	}
}
