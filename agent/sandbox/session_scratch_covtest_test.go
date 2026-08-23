package sandbox

import (
	"path/filepath"
	"testing"
)

// TestSessionScratchRetain_Nil covers the nil-receiver path in Retain
// (line 75-76).
func TestSessionScratchRetain_Nil(t *testing.T) {
	var s *SessionScratch
	if err := s.Retain(); err != nil {
		t.Errorf("Retain on nil SessionScratch = %v, want nil", err)
	}
}

// TestSessionScratchRetain_NilLease covers the nil-lease path in Retain
// (line 75-76): a non-nil SessionScratch with a nil lease returns nil.
func TestSessionScratchRetain_NilLease(t *testing.T) {
	s := &SessionScratch{lease: nil}
	if err := s.Retain(); err != nil {
		t.Errorf("Retain with nil lease = %v, want nil", err)
	}
}

// TestCanonicalScratchRoot_EmptyRoot covers the empty-root path in
// canonicalScratchRoot (line 84-85): an empty root returns "", nil.
func TestCanonicalScratchRoot_EmptyRoot(t *testing.T) {
	got, err := canonicalScratchRoot("")
	if err != nil || got != "" {
		t.Errorf("canonicalScratchRoot(\"\") = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestCanonicalScratchRoot_Valid covers the happy path for a valid root.
func TestCanonicalScratchRoot_Valid(t *testing.T) {
	root := t.TempDir()
	got, err := canonicalScratchRoot(root)
	if err != nil {
		t.Fatalf("canonicalScratchRoot: %v", err)
	}
	// The result should be the canonical (EvalSymlinks) form of root.
	if got == "" {
		t.Error("canonicalScratchRoot returned empty for a valid root")
	}
}

// TestPathWithin covers all branches of pathWithin.
func TestPathWithin(t *testing.T) {
	root := "/foo/bar"
	cases := []struct {
		name string
		path string
		root string
		want bool
	}{
		{"same path", root, root, true},
		{"child", filepath.Join(root, "child"), root, true},
		{"sibling", "/foo/baz", root, false},
		{"parent", "/foo", root, false},
		{"empty root", root, "", false},
		{"relative", "foo/bar", root, false},
	}
	for _, tc := range cases {
		got := pathWithin(tc.path, tc.root)
		if got != tc.want {
			t.Errorf("%s: pathWithin(%q, %q) = %v, want %v", tc.name, tc.path, tc.root, got, tc.want)
		}
	}
}

// TestSessionScratchBase_NoValidBase covers the error path in
// sessionScratchBase where no candidate base is outside the workspace root
// (line 122): returns an error.
func TestSessionScratchBase_NoValidBase(t *testing.T) {
	root := t.TempDir()
	// Mock the cache dir fallback to return the root too — so both candidates
	// are within the workspace root and all get rejected.
	oldCache := sessionScratchUserCacheDir
	t.Cleanup(func() { sessionScratchUserCacheDir = oldCache })
	sessionScratchUserCacheDir = func() (string, error) { return root, nil }
	_, err := sessionScratchBase(root, root)
	if err == nil {
		t.Fatal("expected error when base is within workspace root")
	}
}
