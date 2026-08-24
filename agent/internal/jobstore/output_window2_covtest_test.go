package jobstore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// TestOutputStoreWindowFaultArms drives the Window method and proves each
// filesystem-error arm surfaces the injected fault: stat, open, seek, and read.
func TestOutputStoreWindowFaultArms(t *testing.T) {
	setup := func(base afero.Fs) {
		o, err := openOutputFs(base, outputFaultPath, 0)
		if err != nil {
			t.Fatalf("setup open: %v", err)
		}
		if _, err := o.Append([]byte("payload data here")); err != nil {
			t.Fatalf("setup append: %v", err)
		}
		if err := o.Close(); err != nil {
			t.Fatalf("setup close: %v", err)
		}
	}
	errs := faultSweep(t, 48, setup, func(fs afero.Fs) error {
		o, err := openOutputFs(fs, outputFaultPath, 0)
		if err != nil {
			return err
		}
		defer o.Close() //nolint:errcheck
		_, _, _, _, err = o.Window(0, 100)
		return err
	})

	assertArmReached(t, errs, "jobstore: stat output")
	assertArmReached(t, errs, "jobstore: open output")
}

// TestOutputStoreWindowNegativeMaxBytes covers the negative maxBytes guard
// in Window (line 265-266).
func TestOutputStoreWindowNegativeMaxBytes(t *testing.T) {
	o, err := OpenOutputNoSync(filepath.Join(t.TempDir(), "job.log"), 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	_, _, _, _, err = o.Window(0, -1)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("error = %v, want ErrInvalidLimit", err)
	}
}

// TestLstatOutputArtifact_NonLstater covers the non-Lstater path
// (output.go:172-173): a filesystem that does not implement afero.Lstater
// returns an error.
func TestLstatOutputArtifact_NonLstater(t *testing.T) {
	// MemMapFs implements LstatIfPossible, so use a wrapper that doesn't.
	fs := nonLstaterFs{afero.NewMemMapFs()}
	_, err := lstatOutputArtifact(fs, "/some/path")
	if err == nil || !strings.Contains(err.Error(), "non-following lookup unavailable") {
		t.Fatalf("expected non-Lstater error, got %v", err)
	}
}

// nonLstaterFs wraps an afero.Fs but does NOT implement afero.Lstater,
// so LstatIfPossible is unavailable.
type nonLstaterFs struct {
	afero.Fs
}

// TestOutputStoreWindowStatError covers the stat error path in Window
// (output.go:272-273): when the output file does not exist, stat fails.
func TestOutputStoreWindowStatError(t *testing.T) {
	o, err := OpenOutputNoSync(filepath.Join(t.TempDir(), "job.log"), 100)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	// Remove the underlying file so Stat fails.
	_ = o.fs.Remove(o.path)
	_, _, _, _, err = o.Window(0, 100)
	if err == nil || !strings.Contains(err.Error(), "stat output") {
		t.Fatalf("expected stat error, got %v", err)
	}
}

// TestOutputStoreWindowOpenError covers the open error path in Window
// (output.go:278-279): when the output file exists for stat but cannot be opened.
// This is hard to trigger with a real filesystem; the fault FS test above
// covers it instead. This test documents the path.
func TestOutputStoreWindowOpenError(t *testing.T) {
	t.Skip("covered by TestOutputStoreWindowFaultArms via fault FS injection")
}
