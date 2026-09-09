package selfupdate

import (
	"path/filepath"
	"testing"
)

// TestInstallDirsFromExecutableDerivesSystemPrefix proves the hub upgrades
// the installation it actually runs from: a hub executing as
// /usr/local/share/evener/bin/evener must install into /usr/local, not the
// ~/.local default. Fails today because the hub passes no Prefix at all.
func TestInstallDirsFromExecutableDerivesSystemPrefix(t *testing.T) {
	prefix, binDir, shareBinDir := InstallDirsFromExecutable("/usr/local/share/evener/bin/evener")
	if prefix != "/usr/local" {
		t.Fatalf("prefix = %q, want %q", prefix, "/usr/local")
	}
	if shareBinDir != "/usr/local/share/evener/bin" {
		t.Fatalf("shareBinDir = %q, want %q", shareBinDir, "/usr/local/share/evener/bin")
	}
	if binDir != "/usr/local/bin" {
		t.Fatalf("binDir = %q, want %q", binDir, "/usr/local/bin")
	}
}

// TestInstallDirsFromExecutableDerivesHomePrefix covers the standard
// user-local install: ~/.local/share/evener/bin/evener maps back to
// ~/.local with matching bin and share dirs.
func TestInstallDirsFromExecutableDerivesHomePrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe := filepath.Join(home, ".local", "share", "evener", "bin", "evener")
	prefix, binDir, shareBinDir := InstallDirsFromExecutable(exe)
	if prefix != filepath.Join(home, ".local") {
		t.Fatalf("prefix = %q, want %q", prefix, filepath.Join(home, ".local"))
	}
	if shareBinDir != filepath.Join(home, ".local", "share", "evener", "bin") {
		t.Fatalf("shareBinDir = %q, want the running share dir", shareBinDir)
	}
	if binDir != filepath.Join(home, ".local", "bin") {
		t.Fatalf("binDir = %q, want %q", binDir, filepath.Join(home, ".local", "bin"))
	}
}

// TestInstallDirsFromExecutableFallsBackToDefaults covers binaries running
// from anywhere else (a worktree build, a temp dir): there is no install
// layout to infer, so empty strings select Upgrade's own defaults.
func TestInstallDirsFromExecutableFallsBackToDefaults(t *testing.T) {
	prefix, binDir, shareBinDir := InstallDirsFromExecutable(filepath.Join(t.TempDir(), "evener"))
	if prefix != "" || binDir != "" || shareBinDir != "" {
		t.Fatalf("got (%q, %q, %q), want all empty for an unknown layout", prefix, binDir, shareBinDir)
	}
}
