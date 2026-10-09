//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A stamp that cannot be written still leaves the page's description note
// in the result, after the note saying the stamp failed.
func TestMemoryStampFailureKeepsTheDescriptionNote(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file modes")
	}
	s, scope := memoryWritesSession(t)
	dir := filepath.Join(scope, "locked")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("# Heading only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Writes replace the page through a temp file in its directory, so a
	// read-only directory is what makes the stamp write fail.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	env, release, err := s.acquireMemoryEnvironment("personal")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	notes := s.stampMemoryPage(env, filepath.Join("locked", "p.md"))
	if !strings.HasPrefix(notes, memoryStampFailedNote) || !strings.HasSuffix(notes, memoryMissingDescriptionNote) {
		t.Fatalf("notes=%q", notes)
	}
}
