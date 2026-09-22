package sandbox

import "testing"

// FileToolCanRead answers the policy question a caller must settle before it
// names a path to the model: may the in-process file tools read that path?
// The attachment-persistence note ("readable with read_file") is the caller
// that must not promise what the sandbox denies.

func TestFileToolCanReadUnconfinedPolicyAllowsAnyPath(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{} // off: file tools unconfined, plain os reads
	if !rp.FileToolCanRead("/anything/at/all.png") {
		t.Fatal("an unconfined policy must read any path (plain os)")
	}
}

func TestFileToolCanReadWriteBlockedOffPolicyAllowsReads(t *testing.T) {
	t.Parallel()
	// The write-blocked off degrade takes read-only's scope: reads anywhere.
	rp := ResolvedPolicy{WriteBlocked: true, FileTool: AccessScope{Read: ReadAnywhere}}
	if !rp.FileToolCanRead("/state/sessions/s1/attachments/a.png") {
		t.Fatal("a write-blocked off policy still reads anywhere")
	}
}

func TestFileToolCanReadRestrictedRoots(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:     ModeRestricted,
		FileTool: AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{"/work"}},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/work/file.png", true},
		{"/work/sub/dir/file.png", true},
		{"/work", true},
		{"/state/sessions/s1/attachments/file.png", false},
		{"/workbook/file.png", false}, // prefix boundary: /workbook is not /work
		{"relative.png", false},       // fail closed: a relative path is provably in no root
	}
	for _, tc := range cases {
		if got := rp.FileToolCanRead(tc.path); got != tc.want {
			t.Errorf("FileToolCanRead(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestFileToolCanReadRestrictedExtraReadRoots(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:     ModeRestricted,
		FileTool: AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{"/work", "/state"}},
	}
	if !rp.FileToolCanRead("/state/sessions/s1/attachments/a.png") {
		t.Error("an ExtraReadRoots grant must make the state dir readable to the file tools")
	}
}

func TestFileToolCanReadMaskedPathsDenyInEveryMode(t *testing.T) {
	t.Parallel()
	anywhere := ResolvedPolicy{
		Mode:        ModeReadOnly,
		FileTool:    AccessScope{Read: ReadAnywhere},
		MaskedPaths: []string{"/home/me/.ssh"},
	}
	if anywhere.FileToolCanRead("/home/me/.ssh/id_rsa") {
		t.Error("a masked path must deny reads even in read-anywhere mode")
	}
	if !anywhere.FileToolCanRead("/home/me/.sshx/not-under-the-mask") {
		t.Error("masking /home/me/.ssh must not mask the sibling /home/me/.sshx (prefix boundary)")
	}
	restricted := ResolvedPolicy{
		Mode:        ModeRestricted,
		FileTool:    AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{"/home/me"}},
		MaskedPaths: []string{"/home/me/.ssh"},
	}
	if restricted.FileToolCanRead("/home/me/.ssh/id_rsa") {
		t.Error("a masked path must deny reads even when a read root contains it")
	}
}

func TestFileToolCanReadRootSlashContainsEverything(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:     ModeRestricted,
		FileTool: AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{"/"}},
	}
	if !rp.FileToolCanRead("/x/y.png") {
		t.Fatal("root / must contain every absolute path")
	}
}
