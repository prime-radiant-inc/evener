package skill

import "testing"

// The kernel separates mountinfo fields with single ASCII spaces and escapes
// ASCII whitespace in path fields, but not U+00A0. A Unicode-aware field split
// would shift every later field and read this writable mount as read-only at the
// truncated prefix "/tmp/an".
func TestParseMountPoints_LiteralFieldSeparator(t *testing.T) {
	mounts := parseMountPoints("36 25 0:32 / /tmp/an\u00a0ro rw,relatime - tmpfs tmpfs rw\n")
	if len(mounts) != 1 {
		t.Fatalf("parsed %d mounts, want 1", len(mounts))
	}
	got := mounts[0]
	if got.path != "/tmp/an\u00a0ro" || got.readOnly {
		t.Fatalf("parsed mount = %+v, want a writable mount at /tmp/an\u00a0ro", got)
	}
}

// Octal escapes in the path field are decoded, so a mount point containing a
// space is read by its real name rather than the escaped spelling.
func TestParseMountPoints_DecodesEscapedPath(t *testing.T) {
	mounts := parseMountPoints("5425 5423 0:27 / /tmp/with\\040space ro,relatime - tmpfs tmpfs rw\n")
	if len(mounts) != 1 || mounts[0].path != "/tmp/with space" || !mounts[0].readOnly {
		t.Fatalf("parsed mounts = %+v, want one read-only mount at /tmp/with space", mounts)
	}
}
