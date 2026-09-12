package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// TestInstallDirsFromExecutableResolvesBinSymlink proves the normal
// install.sh entrypoint works: a hub launched as <prefix>/bin/evener (a
// symlink to ../share/evener/bin/evener) upgrades <prefix>, not ~/.local.
// os.Executable returns the symlink path unresolved (proven with a probe
// binary), so the derivation must resolve symlinks itself and accept the
// bin layout as well as the share layout. Fails today: raw path matches
// neither layout... and even EvalSymlinks alone would land in the share
// dir only if the link target is the share binary -- the bin dir itself
// must map back to the prefix.
func TestInstallDirsFromExecutableResolvesBinSymlink(t *testing.T) {
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(binDir, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, gotBin, gotShare := InstallDirsFromExecutable(link)
	if prefix != root {
		t.Fatalf("prefix = %q, want %q", prefix, root)
	}
	if gotBin != binDir {
		t.Fatalf("binDir = %q, want %q", gotBin, binDir)
	}
	if gotShare != shareBin {
		t.Fatalf("shareBinDir = %q, want %q", gotShare, shareBin)
	}
}

// TestInstallDirsFromExecutableAcceptsBinDirDirectly covers a hub launched
// via a hardlink or copy sitting straight in <prefix>/bin: no symlink to
// resolve, but the bin layout must still map back to the prefix.
func TestInstallDirsFromExecutableAcceptsBinDirDirectly(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(filepath.Join(root, "share", "evener", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "evener")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	prefix, gotBin, gotShare := InstallDirsFromExecutable(exe)
	if prefix != root {
		t.Fatalf("prefix = %q, want %q", prefix, root)
	}
	if gotBin != binDir {
		t.Fatalf("binDir = %q, want %q", gotBin, binDir)
	}
	if gotShare != filepath.Join(root, "share", "evener", "bin") {
		t.Fatalf("shareBinDir = %q, want the prefix share dir", gotShare)
	}
}

// TestInstallDirsFromExecutableResolvesShareSymlink covers the share binary
// itself reached through a symlink (e.g. /usr/local/bin/evener -> the
// share path on systems where both exist): resolution must land on the
// share layout, not fail on the link's own directory.
func TestInstallDirsFromExecutableResolvesShareSymlink(t *testing.T) {
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	link := filepath.Join(elsewhere, "evener-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, _, gotShare := InstallDirsFromExecutable(link)
	if prefix != root {
		t.Fatalf("prefix = %q, want %q", prefix, root)
	}
	if gotShare != shareBin {
		t.Fatalf("shareBinDir = %q, want %q", gotShare, shareBin)
	}
}
