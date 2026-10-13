//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The rg routing must not depend on reported size or on #4568's capture bound:
// ripgrep's --max-filesize is metadata-based, so a stream that stats small —
// a FIFO, /proc/kmsg — is searched to its end, and a named target bypasses
// the flag outright. The targets that must never reach rg are a named
// non-regular file, a search rooted inside a pseudo-filesystem root, and a
// file named inside one (the /proc/kmsg class: a deliberate request, bounded
// by the native arm's per-file read cap instead of rg's unbounded read).
// Every case here keeps rg "installed" through the lookPath seam and proves
// the routing with the argv recorder: rg is invoked zero times.
func TestGrepNeverHandsStreamsToRipgrep(t *testing.T) {
	// Not parallel: stubPseudoFsRoots swaps the package's root list.
	root := t.TempDir()
	stream := filepath.Join(root, "stream")
	if err := syscall.Mkfifo(stream, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pseudo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pseudo", "keep.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubPseudoFsRoots(t, filepath.Join(root, "pseudo"))
	rec := &grepArgvRecorder{}
	env := NewLocalExecutionEnvironment(root)
	defer env.Cleanup()
	env.EnvPolicy = EnvPolicyNone
	env.commandFactory = rec
	env.lookPath = func(string) (string, error) { return "/fixture/rg", nil }

	// A named FIFO routes native and is refused by the type check without a
	// writer ever appearing.
	got, err := env.Grep(t.Context(), "needle", stream, "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("named FIFO = %q, want no matches from a stream entry", got)
	}
	if rec.argvCalls != 0 {
		t.Fatalf("named FIFO went to ripgrep: argv calls = %d, want 0", rec.argvCalls)
	}

	// A search rooted inside a pseudo root routes native and still searches.
	got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pseudo"), "", false, 100, "files_with_matches")
	if err != nil {
		t.Fatal(err)
	}
	if want := "keep.txt"; got != want {
		t.Fatalf("pseudo-rooted search = %q, want %q", got, want)
	}
	if rec.argvCalls != 0 {
		t.Fatalf("pseudo-rooted search went to ripgrep: argv calls = %d, want 0", rec.argvCalls)
	}

	// A file named inside a pseudo root — the /proc/kmsg class — routes
	// native and is still read, bounded by the per-file cap.
	got, err = env.Grep(t.Context(), "needle", filepath.Join(root, "pseudo", "keep.txt"), "", false, 100, "content")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1:needle"; got != want {
		t.Fatalf("file named inside a pseudo root = %q, want %q", got, want)
	}
	if rec.argvCalls != 0 {
		t.Fatalf("file named inside a pseudo root went to ripgrep: argv calls = %d, want 0", rec.argvCalls)
	}
}
