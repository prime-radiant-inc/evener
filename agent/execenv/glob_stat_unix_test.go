//go:build unix

package execenv

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGlobMatchesAStatableUnreadableFile proves boundedDirFS does not hide the
// fs.StatFS fast path os.DirFS provides underneath it. boundedDirFS embeds
// fs.FS as a bare interface value, which exposes only Open, so fs.Stat on a
// boundedDirFS can never find a Stat method and falls back to Open plus
// File.Stat instead of reaching os.DirFS's own Stat the way it would if
// boundedDirFS forwarded it. A file that can be stat'ed but not opened (mode
// 0000) fails that fallback, so it is missed where it used to be found —
// globWalkFS.Stat and cancelFS.Stat both reach the walk's underlying
// filesystem through fs.Stat, so the loss shows up in an ordinary literal-name
// glob. File permissions are meaningless outside Unix, hence the build tag.
func TestGlobMatchesAStatableUnreadableFile(t *testing.T) {
	root := t.TempDir()
	const name = "secret.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("os.Stat(%q) = %v, want the file to remain stat-able at mode 0000 (else this test proves nothing)", path, err)
	}
	if _, err := os.Open(path); err == nil {
		t.Skip("this user can open a 0000 file; cannot stage a stat-able-but-unreadable file")
	}

	matches, err := NewLocalExecutionEnvironment(root).Glob(t.Context(), name, root, true)
	if err != nil {
		t.Fatalf("Glob(%q): %v", name, err)
	}
	want := []string{path}
	if len(matches) != 1 || matches[0] != want[0] {
		t.Fatalf("Glob(%q) = %v, want %v; the stat-able file was not matched because boundedDirFS hides fs.StatFS, so fs.Stat falls back to Open, which a mode-0000 file fails", name, matches, want)
	}
}
