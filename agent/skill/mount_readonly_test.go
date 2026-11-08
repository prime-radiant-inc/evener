package skill

import "testing"

func TestReadOnlyMountAt(t *testing.T) {
	// The shape a bwrap sandbox presents: the host root bound read-only at /, a
	// fresh writable tmpfs at /tmp, and the session temp bound inside it.
	mounts := parseMountPoints("" +
		"5421 5426 252:0 / / ro,nosuid,nodev,relatime master:1 - ext4 /dev/sda rw\n" +
		"5422 5421 0:5 / /dev ro,nosuid,nodev,relatime - devtmpfs udev rw\n" +
		"5423 5421 0:26 / /tmp rw,nosuid,nodev,relatime - tmpfs tmpfs rw\n" +
		"5424 5423 252:1 /some/where /tmp/evener-session-1 ro,relatime - ext4 /dev/sdb rw\n" +
		"5425 5423 0:27 / /tmp/with\\040space ro,relatime - tmpfs tmpfs rw\n")
	for _, tc := range []struct {
		name string
		dir  string
		want bool
	}{
		{"read-only root", "/", true},
		{"read-only root child", "/home/jesse", true},
		{"writable tmpfs", "/tmp", false},
		{"writable tmpfs child", "/tmp/anything", false},
		{"read-only nested mount", "/tmp/evener-session-1/tmp", true},
		// Reachable only when the octal escape is decoded: the raw name would
		// fall through to the writable /tmp.
		{"escaped mount point", "/tmp/with space/x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readOnlyMountAt(mounts, tc.dir); got != tc.want {
				t.Fatalf("readOnlyMountAt(%q) = %v, want %v", tc.dir, got, tc.want)
			}
		})
	}
}

func TestReadOnlyMountAt_LongestPrefixWins(t *testing.T) {
	// A writable mount nested under a read-only one is writable.
	mounts := parseMountPoints("" +
		"1 0 0:1 / / ro - tmpfs tmpfs rw\n" +
		"2 1 0:2 / /scratch rw - tmpfs tmpfs rw\n")
	if readOnlyMountAt(mounts, "/scratch/x") {
		t.Fatal("read a writable nested mount as read-only")
	}
	if !readOnlyMountAt(mounts, "/other/x") {
		t.Fatal("did not read an unshadowed path on the read-only mount as read-only")
	}
}

func TestReadOnlyMountAt_WholeComponentsOnly(t *testing.T) {
	// /tmpfoo is a sibling of /tmp, not a child: the writable /tmp must not
	// shadow it, and a rw root is the only mount that covers it.
	mounts := parseMountPoints("" +
		"1 0 0:1 / / rw - tmpfs tmpfs rw\n" +
		"2 1 0:2 / /tmp rw - tmpfs tmpfs rw\n")
	for _, dir := range []string{"/tmp", "/tmp/x", "/tmpfoo", "/tmpfoo/x"} {
		if readOnlyMountAt(mounts, dir) {
			t.Fatalf("read %q on a writable mount table as read-only", dir)
		}
	}
}

func TestReadOnlyMountAt_LiteralFieldSeparator(t *testing.T) {
	// The kernel separates fields with single ASCII spaces and escapes ASCII
	// whitespace in mount points, but not U+00A0. A Unicode-aware field split
	// would shift every later field and read this writable mount as read-only at
	// the truncated prefix "/tmp/an".
	mounts := parseMountPoints("36 25 0:32 / /tmp/an\u00a0ro rw,relatime - tmpfs tmpfs rw\n")
	if readOnlyMountAt(mounts, "/tmp/an\u00a0ro") {
		t.Fatal("read a writable mount whose name contains U+00A0 as read-only")
	}
}
