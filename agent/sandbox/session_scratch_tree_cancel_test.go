//go:build unix

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// cancellingScratchDirectory reads the real fixture directory, then cancels
// while enumeration is in progress. It does not choose or fake any entries.
type cancellingScratchDirectory struct {
	scratchTreeDirectory
	cancel   context.CancelFunc
	consumed int
}

func (d *cancellingScratchDirectory) ReadDir(n int) ([]os.DirEntry, error) {
	entries, err := d.scratchTreeDirectory.ReadDir(n)
	d.consumed += len(entries)
	d.cancel()
	return entries, err
}

// Dropping the ctx check or restoring an unbounded ReadDir consumes the whole
// base and returns candidates from a pass whose cancellation is already known.
func TestSessionScratchTreeRootIDsStopsDuringRealDirectoryEnumeration(t *testing.T) {
	tempBase, _ := isolateSessionScratchTreeBases(t) // package-wide bases and reader seam: serial
	canonicalBase, err := filepath.EvalSymlinks(tempBase)
	if err != nil {
		t.Fatal(err)
	}
	const trees = 90
	for i := range trees {
		if err := os.Mkdir(filepath.Join(tempBase, fmt.Sprintf("evener-scratch-ROOT%03d", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var opened *cancellingScratchDirectory
	previous := openScratchTreeDirectory
	openScratchTreeDirectory = func(base string) (scratchTreeDirectory, error) {
		dir, err := previous(base)
		if err != nil || base != canonicalBase {
			return dir, err
		}
		opened = &cancellingScratchDirectory{scratchTreeDirectory: dir, cancel: cancel}
		return opened, nil
	}
	t.Cleanup(func() { openScratchTreeDirectory = previous })

	if got := SessionScratchTreeRootIDs(ctx); len(got) != 0 {
		t.Errorf("cancelled listing returned %d candidates, want none from an incomplete pass", len(got))
	}
	if opened == nil {
		t.Fatal("the real fixture directory was not opened")
	}
	if opened.consumed == 0 || opened.consumed >= trees {
		t.Errorf("cancelled enumeration consumed %d of %d entries, want a nonempty bounded batch", opened.consumed, trees)
	}
	if _, err := opened.ReadDir(1); err == nil || errors.Is(err, io.EOF) {
		t.Errorf("directory after cancellation ReadDir error = %v, want closed", err)
	}

	openScratchTreeDirectory = previous
	if got := SessionScratchTreeRootIDs(t.Context()); len(got) != trees {
		t.Errorf("later uninterrupted listing returned %d of %d trees", len(got), trees)
	}
}
