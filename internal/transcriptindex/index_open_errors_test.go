package transcriptindex

// Error-shape and open-path coverage for the index's public error surface:
// the rebuild refusal and entry error render their own facts, an unusable
// sidecar directory or lock path fails the open, and a vanished transcript
// fails catch-up with the stat error rather than a silent no-op.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestIndexErrorsCarryTheirFacts pins that the two error types render the
// data they carry: different facts must read differently, and the rebuild
// refusal must still match its sentinel through errors.Is.
func TestIndexErrorsCarryTheirFacts(t *testing.T) {
	t.Parallel()
	first := &RebuildRequiredError{LastEntry: 1}
	second := &RebuildRequiredError{LastEntry: 2}
	if first.Error() == "" || first.Error() == second.Error() {
		t.Fatalf("rebuild refusals for entries 1 and 2 render identically: %q", first.Error())
	}
	if !errors.Is(first, ErrRebuildRequired) {
		t.Fatal("a rebuild refusal no longer matches its sentinel")
	}
	inner := errors.New("inner")
	entryA := &EntryError{Ordinal: 1, Err: inner}
	entryB := &EntryError{Ordinal: 2, Err: inner}
	if entryA.Error() == "" || entryA.Error() == entryB.Error() {
		t.Fatalf("entry errors for ordinals 1 and 2 render identically: %q", entryA.Error())
	}
	if !errors.Is(entryA, inner) {
		t.Fatal("an entry error no longer unwraps to its cause")
	}
}

// TestOpenRefusesUnusableSidecarPaths pins the two open failures a caller
// cannot recover from by retrying: a sidecar directory that cannot be
// created, and a lock path that is not a file.
func TestOpenRefusesUnusableSidecarPaths(t *testing.T) {
	t.Parallel()
	fx := fixture{name: "open-errors", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}}
	path := writeFixture(t, fx)

	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, filepath.Join(blocked, "index")); err == nil {
		t.Fatal("a sidecar directory under a regular file was accepted")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, lockFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, dir); err == nil {
		t.Fatal("a sidecar lock path that is a directory was accepted")
	}
}

// TestCatchUpReportsAVanishedTranscript pins that catch-up on a transcript
// that disappeared fails with the stat error instead of answering as though
// nothing changed.
func TestCatchUpReportsAVanishedTranscript(t *testing.T) {
	t.Parallel()
	fx := fixture{name: "vanished", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUp(); err == nil {
		t.Fatal("catch-up on a vanished transcript reported success")
	}
}
