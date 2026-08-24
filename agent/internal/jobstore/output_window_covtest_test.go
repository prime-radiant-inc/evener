package jobstore

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestReadWindow_NegativeMaxBytes covers the negative maxBytes guard in
// ReadWindow (line 316-317).
func TestReadWindow_NegativeMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutputNoSync(path, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	_, err = o.ReadWindow(0, -1)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("error = %v, want ErrInvalidLimit", err)
	}
}

// TestReadWindow_NegativeOffset covers the negative offset guard in ReadWindow
// (line 319-320).
func TestReadWindow_NegativeOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutputNoSync(path, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	_, err = o.ReadWindow(-1, 10)
	if !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("error = %v, want ErrInvalidOffset", err)
	}
}

// TestReadWindow_OffsetBeyondTotal covers the offset-beyond-total error path
// in ReadWindow (line 333-334).
func TestReadWindow_OffsetBeyondTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutputNoSync(path, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	appendOutput(t, o, "hello") // total = 5
	_, err = o.ReadWindow(100, 10)
	if !errors.Is(err, ErrInvalidOffset) {
		t.Fatalf("error = %v, want ErrInvalidOffset", err)
	}
}

// TestReadWindow_PrunedOffset covers the pruned-offset error path in ReadWindow
// (line 330-331): requesting a start before retainedStart returns
// ErrOutputPruned.
func TestReadWindow_PrunedOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutputNoSync(path, 4) // small cap to trigger prune
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	// Write enough to trigger pruning.
	appendOutput(t, o, "abcdefgh") // 8 bytes, cap 4 → retainedStart > 0
	if o.RetainedStart() == 0 {
		t.Fatalf("expected retainedStart > 0 after prune, got %d", o.RetainedStart())
	}
	_, err = o.ReadWindow(0, 10) // offset 0 is before retainedStart
	if !errors.Is(err, ErrOutputPruned) {
		t.Fatalf("error = %v, want ErrOutputPruned", err)
	}
}

// TestReadWindow_EmptyWindow covers the zero-length window path (line 340-341):
// when offset == end (offset is at total), the function returns an empty
// snapshot without reading the file.
func TestReadWindow_EmptyWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := OpenOutputNoSync(path, 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	appendOutput(t, o, "hello")      // total = 5
	snap, err := o.ReadWindow(5, 10) // offset == total → empty window
	if err != nil {
		t.Fatalf("ReadWindow at EOF: %v", err)
	}
	if len(snap.Content) != 0 {
		t.Errorf("expected empty content, got %d bytes", len(snap.Content))
	}
	if snap.Start != 5 || snap.End != 5 {
		t.Errorf("Start=%d End=%d, want 5/5", snap.Start, snap.End)
	}
}
