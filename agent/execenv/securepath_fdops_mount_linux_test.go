//go:build linux

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// A browse base anchored through a symlink the open policy tolerated leaves
// the base fd on a pseudo mount while every spelled path under it looks
// non-pseudo. Open must classify the descriptor it opened — not the anchor's
// spelling — or a regular procfs file loses O_NONBLOCK at admission and a
// blocked read on it sits past both the deadline and cancellation.
func TestSecureDirFSOpenKeepsNonblockingForAPseudoRootReachedThroughAnAlias(t *testing.T) {
	t.Parallel()
	alias := filepath.Join(t.TempDir(), "proc-alias")
	if err := os.Symlink("/proc", alias); err != nil {
		t.Fatal(err)
	}
	baseFd, err := unix.Open(alias, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(baseFd) }()
	fsys := &secureDirFS{baseFd: baseFd, basePath: alias, ctx: t.Context()}
	f, err := fsys.Open("version")
	if err != nil {
		t.Fatalf("open version beneath an aliased /proc anchor: %v", err)
	}
	defer func() { _ = f.Close() }()
	gcf, ok := f.(*grepCtxFile)
	if !ok {
		t.Fatalf("secureDirFS.Open returned %T, want *grepCtxFile", f)
	}
	if !fdNonblocking(t, gcf.File) {
		t.Errorf("version opened beneath an aliased /proc anchor lost O_NONBLOCK: a blocked read on it would sit past the deadline machinery")
	}
}

// fdStreamed must answer from the descriptor's mount, because the spelled
// path an aliased anchor produces lies about where the file lives.
func TestFdStreamedClassifiesTheDescriptorsMountNotTheSpelling(t *testing.T) {
	t.Parallel()
	alias := filepath.Join(t.TempDir(), "proc-alias")
	if err := os.Symlink("/proc", alias); err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(alias, "version")
	if isPseudoFsPath(spelled) {
		t.Fatalf("fixture: %s must spell non-pseudo", spelled)
	}
	f, err := os.Open(spelled)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if !fdStreamed(int(f.Fd()), isPseudoFsPath(spelled)) {
		t.Errorf("fdStreamed on an aliased /proc/version = false, want true: the descriptor lives on procfs whatever the spelling says")
	}
	plain := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cf, err := os.Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cf.Close() }()
	if fdStreamed(int(cf.Fd()), isPseudoFsPath(plain)) {
		t.Errorf("fdStreamed on an ordinary %s-rooted file = true, want false", filepath.Base(filepath.Dir(plain)))
	}
	// The spelled check stays authoritative where the descriptor's mount
	// cannot be faked (stub-backed test roots), so a spelled pseudo path
	// classifies streamed regardless of the mount.
	if !fdStreamed(int(cf.Fd()), true) {
		t.Errorf("fdStreamed with a spelled pseudo path = false, want true: the spelling must keep its authority")
	}
}

// The same aliased anchor must not stop a readable pseudo file from being
// searched at all — classification picks the read discipline, never the skip.
func TestGrepNamedFileStillSearchesAnAliasedReadablePseudoFile(t *testing.T) {
	t.Parallel()
	alias := filepath.Join(t.TempDir(), "proc-alias")
	if err := os.Symlink("/proc", alias); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(alias, "version")
	fd, err := unix.Open(target, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newGrepAccum("Linux", false, 0, "content", 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := grepNamedFile(t.Context(), fd, target, a, nil, func(string) bool { return false })
	if err != nil {
		t.Fatalf("grepNamedFile on an aliased /proc/version: %v", err)
	}
	if !strings.Contains(out, "Linux") {
		t.Errorf("grepNamedFile on an aliased /proc/version = %q, want the version line", out)
	}
}
