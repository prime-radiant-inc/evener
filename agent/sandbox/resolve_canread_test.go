package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestSpawnedCanReadUnenforcedAllowsAnyPath(t *testing.T) {
	t.Parallel()
	for _, rp := range []ResolvedPolicy{
		{}, // plain off
		{WriteBlocked: true, Spawned: AccessScope{Read: ReadWorktreeOnly}},
	} {
		if !rp.SpawnedCanRead("/anything/at/all") {
			t.Fatalf("policy %+v must spawn with plain OS access", rp)
		}
	}
	masked := ResolvedPolicy{
		WriteBlocked: true,
		MaskedPaths:  []string{"/home/me/.ssh"},
		Spawned:      AccessScope{Read: ReadWorktreeOnly},
	}
	if masked.SpawnedCanRead("/home/me/.ssh/id_rsa") {
		t.Fatal("the write-blocked degrade must keep the file tools' mask floor")
	}
	if !masked.SpawnedCanRead("/var/tmp/worktree") {
		t.Fatal("an unmasked path on the write-blocked degrade must stay readable")
	}
}

func TestSpawnedCanReadReadAnywhere(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode:        ModeReadOnly,
		Spawned:     AccessScope{Read: ReadAnywhere},
		MaskedPaths: []string{"/home/me/.ssh"},
	}
	for _, path := range []string{"/var/tmp/worktree", "/home/me/project/file"} {
		if !rp.SpawnedCanRead(path) {
			t.Errorf("SpawnedCanRead(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"/home/me/.ssh/id_rsa", "relative/path"} {
		if rp.SpawnedCanRead(path) {
			t.Errorf("SpawnedCanRead(%q) = true, want false", path)
		}
	}
}

func TestSpawnedCanReadRestrictedRoots(t *testing.T) {
	t.Parallel()
	rp := ResolvedPolicy{
		Mode: ModeRestricted,
		Spawned: AccessScope{
			Read:       ReadWorktreeOnly,
			ReadRoots:  []string{"/work"},
			WriteRoots: []string{"/write-only"},
		},
		UnmaskedRoots: []string{"/home/me/.config/evener/skills"},
		MaskedPaths:   []string{"/home/me/.config/evener"},
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/work", true},
		{"/work/sub/dir", true},
		{"/home/me/.config/evener/skills/foo", true},
		{"/home/me/.config/evener/skills/.git/config", false},
		{"/home/me/.config/evener/other", false},
		{"/write-only", false},
		{"/outside", false},
		{"work", false},
	}
	for _, tc := range cases {
		if got := rp.SpawnedCanRead(tc.path); got != tc.want {
			t.Errorf("SpawnedCanRead(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestSpawnedCanReadSymlinkedReadRoot(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	rp := ResolvedPolicy{
		Mode:    ModeRestricted,
		Spawned: AccessScope{Read: ReadWorktreeOnly, ReadRoots: []string{link}},
	}
	literal := filepath.Join(link, "sub")
	resolved := filepath.Join(realDir, "sub")
	if !rp.SpawnedCanRead(literal) {
		t.Fatalf("SpawnedCanRead(%q) = false, want the literal grant to admit it", literal)
	}
	if !rp.SpawnedCanReadResolved(resolved) {
		t.Fatalf("SpawnedCanReadResolved(%q) = false, want the resolved grant to admit it", resolved)
	}
	if rp.SpawnedCanRead(resolved) {
		t.Errorf("SpawnedCanRead(%q) = true, want the resolved spelling refused against the literal grant", resolved)
	}
}
