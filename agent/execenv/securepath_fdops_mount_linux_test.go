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

// admitOpenedFD must classify the descriptor's mount, because the spelled path
// an aliased anchor produces lies about where the file lives.
func TestAdmitOpenedFDClassifiesTheDescriptorsMountNotTheSpelling(t *testing.T) {
	t.Parallel()
	alias := filepath.Join(t.TempDir(), "proc-alias")
	if err := os.Symlink("/proc", alias); err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(alias, "version")
	if isPseudoFsPath(spelled) {
		t.Fatalf("fixture: %s must spell non-pseudo", spelled)
	}
	admit := func(path string, spelled bool) *os.File {
		t.Helper()
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		f, err := admitOpenedFD(fd, path, admitOptions{}, spelled)
		if err != nil {
			t.Fatalf("admitOpenedFD(%s): %v", path, err)
		}
		return f
	}
	// The descriptor lives on procfs whatever the spelling says.
	aliased := admit(spelled, false)
	if !fdNonblocking(t, aliased) {
		t.Errorf("admitOpenedFD on an aliased /proc/version cleared O_NONBLOCK: the descriptor lives on procfs whatever the spelling says")
	}
	_ = aliased.Close()
	plain := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ordinary := admit(plain, false)
	if fdNonblocking(t, ordinary) {
		t.Errorf("admitOpenedFD on an ordinary %s-rooted file kept O_NONBLOCK, want it cleared", filepath.Base(filepath.Dir(plain)))
	}
	_ = ordinary.Close()
	// The spelled check stays authoritative where the descriptor's mount
	// cannot be faked (stub-backed test roots).
	spelledPseudo := admit(plain, true)
	if !fdNonblocking(t, spelledPseudo) {
		t.Errorf("admitOpenedFD with a spelled pseudo path cleared O_NONBLOCK: the spelling must keep its authority")
	}
	_ = spelledPseudo.Close()
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
