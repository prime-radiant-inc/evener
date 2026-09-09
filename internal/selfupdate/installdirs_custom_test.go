package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallDirsPreservesCustomBinDir proves a hub launched through a
// custom BINDIR entrypoint keeps it: install.sh supports BINDIR (symlink
// dir) independently of the prefix, so deriving binDir as <prefix>/bin
// from the resolved share path would write symlinks to the wrong place
// (or fail on permissions). The unresolved entrypoint directory -- the
// dir holding the launched name -- is the binDir. Fails today: resolution
// drops the entrypoint and reconstructs <prefix>/bin.
func TestInstallDirsPreservesCustomBinDir(t *testing.T) {
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	customBin := filepath.Join(t.TempDir(), "custom-bin")
	if err := os.MkdirAll(customBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(customBin, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, binDir, gotShare := InstallDirsFromExecutable(link)
	if prefix != root {
		t.Fatalf("prefix = %q, want %q", prefix, root)
	}
	if binDir != customBin {
		t.Fatalf("binDir = %q, want the entrypoint dir %q", binDir, customBin)
	}
	if gotShare != shareBin {
		t.Fatalf("shareBinDir = %q, want %q", gotShare, shareBin)
	}
}

// TestInstallDirsFromPreresolvedShareBinary covers Linux, where
// os.Executable resolves /proc/self/exe to the TARGET: exe arrives
// already resolved as <prefix>/share/evener/bin/<name>, so the
// entrypoint dir IS the share dir. The sibling check must not treat
// that as a live custom BINDIR -- returning binDir==shareBinDir makes
// installExtractedBinaries Remove the just-installed binary and
// symlink it to itself. Fails today: sibling==candidate trivially.
func TestInstallDirsFromPreresolvedShareBinary(t *testing.T) {
	root := t.TempDir()
	shareBin := filepath.Join(root, "share", "evener", "bin")
	if err := os.MkdirAll(shareBin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(shareBin, "evener")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	prefix, binDir, gotShare := InstallDirsFromExecutable(exe)
	if prefix != root {
		t.Fatalf("prefix = %q, want %q", prefix, root)
	}
	if gotShare != shareBin {
		t.Fatalf("shareBinDir = %q, want %q", gotShare, shareBin)
	}
	if binDir == shareBin {
		t.Fatalf("binDir = shareBinDir (%q): install would remove the binary and self-symlink", binDir)
	}
	if want := filepath.Join(root, "bin"); binDir != want {
		t.Fatalf("binDir = %q, want standard %q", binDir, want)
	}
}

// TestInstallDirsFromArbitraryShareLayout proves a fully custom
// installation (BINDIR + EVENER_SHARE_BINDIR both non-standard) derives
// directly from the launch symlink: entry dir as binDir, resolved target
// dir as shareBinDir, with no ~/.local fallback. Fails today: neither
// suffix matches, so all-empty selects Upgrade's defaults and the hub
// restarts from an unrelated copy.
func TestInstallDirsFromArbitraryShareLayout(t *testing.T) {
	customShare := filepath.Join(t.TempDir(), "custom", "share")
	if err := os.MkdirAll(customShare, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(customShare, "evener")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	customBin := filepath.Join(t.TempDir(), "custom", "bin")
	if err := os.MkdirAll(customBin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(customBin, "evener")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	prefix, binDir, shareBinDir := InstallDirsFromExecutable(link)
	// The prefix is unknowable for a non-standard layout (best effort,
	// may be empty); what matters is both install dirs are exact, so no
	// ~/.local fallback and the restart binary is the managed one.
	_ = prefix
	if binDir != customBin {
		t.Fatalf("binDir = %q, want the entrypoint dir %q", binDir, customBin)
	}
	if shareBinDir != customShare {
		t.Fatalf("shareBinDir = %q, want the resolved target dir %q", shareBinDir, customShare)
	}
}

// NOTE: hardlink and copy entrypoints in a custom BINDIR are deliberately
// unsupported: with no symlink the derivation cannot tell a managed custom
// dir from an unrelated copy, and guessing wrong redirects symlinks (or
// refuses a valid update). install.sh always installs a symlink at BINDIR,
// so the symlink case above is the supported custom layout.

// TestInstallPairRemovesStagedOnCommitFailure proves a failed commit does
// not leak staged temp files: the pair commit removes the staged file
// whose rename failed. (Supersedes the old copyExecutable rename test;
// staging owns temp lifecycle now.)
func TestInstallPairRemovesStagedOnCommitFailure(t *testing.T) {
	extractDir := t.TempDir()
	shareBin := filepath.Join(t.TempDir(), "share")
	binDir := filepath.Join(t.TempDir(), "bin")
	for _, d := range []string{extractDir, shareBin, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, bin := range installBinaries {
		if err := os.WriteFile(filepath.Join(extractDir, bin), []byte("new"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	previous := renameFile
	renameFile = func(_, _ string) error { return errors.New("rename failed") }
	t.Cleanup(func() { renameFile = previous })

	if _, err := installExtractedBinaries(t.Context(), extractDir, shareBin, binDir); err == nil ||
		!strings.Contains(err.Error(), "rename failed") {
		t.Fatalf("err = %v, want the rename error", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(shareBin, "*.stage"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staged files left behind after commit failure: %v", leftovers)
	}
}

// TestInstallDirsAcceptsFilesystemRootPrefix proves PREFIX=/ is a valid
// installation root: /share/evener/bin/<name> CutSuffixes to an empty
// prefix, which the old empty-check rejected into the ~/.local fallback.
// Fails today: all-empty for the root layout.
func TestInstallDirsAcceptsFilesystemRootPrefix(t *testing.T) {
	sep := string(filepath.Separator)
	prefix, binDir, shareOut := InstallDirsFromExecutable(filepath.Join(sep, "share", "evener", "bin", "evener"))
	if prefix != sep {
		t.Fatalf("prefix = %q, want the filesystem root", prefix)
	}
	if binDir != filepath.Join(sep, "bin") {
		t.Fatalf("binDir = %q, want /bin", binDir)
	}
	if shareOut != filepath.Join(sep, "share", "evener", "bin") {
		t.Fatalf("shareBinDir = %q", shareOut)
	}
}
