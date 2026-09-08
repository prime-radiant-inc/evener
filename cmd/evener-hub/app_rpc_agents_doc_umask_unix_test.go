//go:build unix

package hub

import (
	"os"
	"syscall"
	"testing"
)

// writeAgentsDoc says the file it lands is mode 0644, and that has to hold
// whatever umask the hub was started under - a mode argument alone is masked,
// so the same save would produce 0644 for one user and 0600 for another.
// Umask is process-wide, so this test must never run in parallel.
func TestWriteAgentsDocModeIsExplicitUnderARestrictiveUmask(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	root := t.TempDir()
	path := agentsDocPath(root)
	if err := writeAgentsDoc(path, "mine\n"); err != nil {
		t.Fatalf("writeAgentsDoc: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 0644", info.Mode().Perm())
	}
}
