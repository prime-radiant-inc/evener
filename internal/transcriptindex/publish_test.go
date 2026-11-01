package transcriptindex

import (
	"os"
	"path/filepath"
	"testing"
)

// The CURRENT pointer is published beside the builds in the sidecar
// directory; a symlink planted at a predictable temp name there must not
// redirect the write.
func TestOpenDoesNotFollowSymlinkPlantedAtPointerTempName(t *testing.T) {
	path := writeFixture(t, fixture{name: "fresh", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, currentFileName+".tmp")); err != nil {
		t.Fatal(err)
	}

	x := openIndex(t, path, dir)
	catchUp(t, x)

	if got, err := os.ReadFile(victim); err != nil || string(got) != "untouched" {
		t.Fatalf("symlink target = %q, %v; want untouched", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, currentFileName)); err != nil {
		t.Fatalf("CURRENT not published: %v", err)
	}
}
