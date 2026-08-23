package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/events"
)

// TestGlobalCommandsDir_HomeDirError covers the path where XDG_CONFIG_HOME is
// empty and os.UserHomeDir returns an error (lines 28-31).
func TestGlobalCommandsDir_HomeDirError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	orig := evenerwideUserHomeDir
	t.Cleanup(func() { evenerwideUserHomeDir = orig })
	evenerwideUserHomeDir = func() (string, error) {
		return "", os.ErrNotExist
	}
	if got := globalCommandsDir(); got != "" {
		t.Errorf("globalCommandsDir() = %q, want empty", got)
	}
}

// TestGlobalCommandsDir_HomeDirFallback covers the path where XDG_CONFIG_HOME
// is empty but os.UserHomeDir succeeds (lines 28-34).
func TestGlobalCommandsDir_HomeDirFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	orig := evenerwideUserHomeDir
	t.Cleanup(func() { evenerwideUserHomeDir = orig })
	evenerwideUserHomeDir = func() (string, error) {
		return home, nil
	}
	got := globalCommandsDir()
	want := filepath.Join(home, ".config", "evener", "commands")
	if got != want {
		t.Errorf("globalCommandsDir() = %q, want %q", got, want)
	}
}

// TestScanEvenerwideDir_UnreadableDir covers the unreadable-directory warning
// path (lines 79-81): a directory that exists but cannot be read produces a
// warning, not a silent skip.
func TestScanEvenerwideDir_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission-based tests are unreliable")
	}
	dir := t.TempDir()
	// Create a subdirectory and make it unreadable.
	unreadable := filepath.Join(dir, "unreadable")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	// Place a file inside, then remove read permission on the directory.
	if err := os.WriteFile(filepath.Join(unreadable, "x.md"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o755) })

	out := map[string]Command{}
	var warnings []events.WarningData
	scanEvenerwideDir(unreadable, "user", out, &warnings)
	if len(warnings) == 0 {
		t.Errorf("expected unreadable-dir warning, got none")
	}
}

// TestScanEvenerwideDir_UnreadableFile covers the unreadable-file warning path
// (lines 107-109): a readable directory with an unreadable .md file produces a
// warning.
func TestScanEvenerwideDir_UnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, permission-based tests are unreliable")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "secret.md")
	if err := os.WriteFile(file, []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })

	out := map[string]Command{}
	var warnings []events.WarningData
	scanEvenerwideDir(dir, "user", out, &warnings)
	if len(warnings) == 0 {
		t.Errorf("expected unreadable-file warning, got none")
	}
}
