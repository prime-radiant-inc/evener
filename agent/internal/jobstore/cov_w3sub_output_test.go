package jobstore

import (
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
)

// readValidPendingOutputMeta reconciles a crash mid-prune. The prefix-recovery
// branch (metaRetained < retained) has a chain of consistency checks against a
// crafted output file, a pending journal, and the older final metadata. These
// tests drive each rejection arm with deliberately inconsistent sidecars, and
// drive the filesystem-read error arms with the fault seam.

// w3subSeedPendingScenario writes the canonical valid prefix-recovery triple
// used by TestW2Tail_readValidPendingOutputMeta_DeepReconcile:
//
//	output   = "0123456789" (retained 10)
//	pending  = {Total 20, RetainedStart 14}  -> metaRetained 6, prefixLen 4
//	final    = {Total 14, RetainedStart 10}  -> finalRetained 4
func w3subSeedPendingScenario(t *testing.T, fs afero.Fs) (outPath, pendingPath, finalPath string) {
	t.Helper()
	outPath = "job.out"
	if err := afero.WriteFile(fs, outPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	pendingPath = outputPendingMetaPath(outputMetaPath(outPath))
	finalPath = outputMetaPath(outPath)
	if err := writeOutputMetaFileFs(fs, pendingPath, outputMeta{
		TotalBytes: 20, RetainedStart: 14, RetainedSHA256: outputBytesSHA256([]byte("456789")),
	}); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if err := writeOutputMetaFileFs(fs, finalPath, outputMeta{
		TotalBytes: 14, RetainedStart: 10, RetainedSHA256: outputBytesSHA256([]byte("0123")),
	}); err != nil {
		t.Fatalf("seed final: %v", err)
	}
	return outPath, pendingPath, finalPath
}

// The consistency-rejection arms: each mutates one sidecar of the otherwise
// valid prefix-recovery triple and asserts the read is rejected with an error.
func TestW3Sub_ReadValidPendingMeta_RejectionArms(t *testing.T) {
	t.Run("pending retained bytes invalid", func(t *testing.T) {
		// TotalBytes < RetainedStart -> outputMetaRetainedBytes error (output.go:507).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := writeOutputMetaFileFs(fs, pendingPath, outputMeta{TotalBytes: 5, RetainedStart: 20}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected invalid-retained error")
		}
	})

	t.Run("suffix sha mismatch", func(t *testing.T) {
		// metaRetained<retained but the retained tail hash is wrong (output.go:513).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := writeOutputMetaFileFs(fs, pendingPath, outputMeta{
			TotalBytes: 20, RetainedStart: 14, RetainedSHA256: "deadbeef",
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected suffix mismatch error")
		}
	})

	t.Run("final meta corrupt", func(t *testing.T) {
		// Final metadata is unparseable JSON (output.go:520).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := afero.WriteFile(fs, finalPath, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected corrupt final-meta error")
		}
	})

	t.Run("final meta missing", func(t *testing.T) {
		// A valid pending prefix but no final journal to reconcile against (output.go:523).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := fs.Remove(finalPath); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected missing final-meta error")
		}
	})

	t.Run("final retained bytes invalid", func(t *testing.T) {
		// Final TotalBytes < RetainedStart -> outputMetaRetainedBytes error (output.go:528).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := writeOutputMetaFileFs(fs, finalPath, outputMeta{TotalBytes: 2, RetainedStart: 10}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected invalid final-retained error")
		}
	})

	t.Run("final invariant mismatch", func(t *testing.T) {
		// Final RetainedStart no longer lines up with pending's prefix (output.go:533).
		fs := afero.NewMemMapFs()
		outPath, pendingPath, finalPath := w3subSeedPendingScenario(t, fs)
		if err := writeOutputMetaFileFs(fs, finalPath, outputMeta{
			TotalBytes: 14, RetainedStart: 5, RetainedSHA256: outputBytesSHA256([]byte("0123")),
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10); err == nil {
			t.Fatalf("expected invariant-mismatch error")
		}
	})

	t.Run("metaRetained exceeds retained", func(t *testing.T) {
		// metaRetained > retained falls to the equal-check mismatch arm (output.go:549).
		fs := afero.NewMemMapFs()
		if err := afero.WriteFile(fs, "j.out", []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
		pendingPath := outputPendingMetaPath(outputMetaPath("j.out"))
		finalPath := outputMetaPath("j.out")
		if err := writeOutputMetaFileFs(fs, pendingPath, outputMeta{TotalBytes: 10, RetainedStart: 2}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, "j.out", 5); err == nil {
			t.Fatalf("expected retained-exceeds mismatch error")
		}
	})
}

// The filesystem-read error arms: sweep the fault seam over the valid
// prefix-recovery scenario so that each output-file open/seek/read faults once,
// proving the suffix/prefix/full-file hash error arms surface the injected
// fault (output.go:511, 536, 542).
func TestW3Sub_ReadValidPendingMeta_HashReadFaultArms(t *testing.T) {
	errs := faultSweep(t, 48,
		func(base afero.Fs) {
			w3subSeedPendingScenario(t, base)
		},
		func(fs afero.Fs) error {
			outPath := "job.out"
			_, _, err := readValidPendingOutputMeta(fs,
				outputPendingMetaPath(outputMetaPath(outPath)), outputMetaPath(outPath), outPath, 10)
			return err
		},
	)
	assertArmReached(t, errs, "jobstore: open output for metadata hash")
	assertArmReached(t, errs, "jobstore: seek output for metadata hash")
	assertArmReached(t, errs, "jobstore: hash output metadata")
}

// The equal-branch full-file hash open faults through the seam (output.go:553).
func TestW3Sub_ReadValidPendingMeta_ExactHashFaultArm(t *testing.T) {
	errs := faultSweep(t, 24,
		func(base afero.Fs) {
			if err := afero.WriteFile(base, "j.out", []byte("hello"), 0o644); err != nil {
				t.Fatalf("seed output: %v", err)
			}
			if err := writeOutputMetaFileFs(base, outputPendingMetaPath(outputMetaPath("j.out")),
				outputMeta{TotalBytes: 5, RetainedStart: 0, RetainedSHA256: outputBytesSHA256([]byte("hello"))}); err != nil {
				t.Fatalf("seed pending: %v", err)
			}
		},
		func(fs afero.Fs) error {
			_, _, err := readValidPendingOutputMeta(fs,
				outputPendingMetaPath(outputMetaPath("j.out")), outputMetaPath("j.out"), "j.out", 5)
			return err
		},
	)
	assertArmReached(t, errs, "jobstore: open output for metadata hash")
}

// writeOutputMetaFileFs guards its tmp-file Write against a short write and its
// Close against an error; the fault seam (fuzz/fault) never short-writes and
// does not intercept Close, so those two durable-write arms need a purpose-built
// filesystem double that fails exactly those File operations.
type badWriteFs struct {
	afero.Fs
	shortWrite bool
	closeErr   bool
}

func (b badWriteFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := b.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &badWriteFile{File: f, shortWrite: b.shortWrite, closeErr: b.closeErr}, nil
}

type badWriteFile struct {
	afero.File
	shortWrite bool
	closeErr   bool
}

func (b *badWriteFile) Write(p []byte) (int, error) {
	if b.shortWrite && len(p) > 0 {
		// Write one fewer byte and report the short count without an error, so
		// writeOutputMetaFileFs takes its n != len(b) arm rather than its err arm.
		n, err := b.File.Write(p[:len(p)-1])
		if err != nil {
			return n, err
		}
		return n, nil
	}
	return b.File.Write(p)
}

func (b *badWriteFile) Close() error {
	_ = b.File.Close()
	if b.closeErr {
		return errors.New("injected close error")
	}
	return nil
}

func TestW3Sub_WriteOutputMetaFileFs_ShortWriteAndCloseArms(t *testing.T) {
	t.Run("short write", func(t *testing.T) {
		fs := badWriteFs{Fs: afero.NewMemMapFs(), shortWrite: true}
		if err := writeOutputMetaFileFs(fs, "meta.json", outputMeta{TotalBytes: 1}); err == nil {
			t.Fatalf("expected short-write error")
		}
	})
	t.Run("close error", func(t *testing.T) {
		fs := badWriteFs{Fs: afero.NewMemMapFs(), closeErr: true}
		if err := writeOutputMetaFileFs(fs, "meta.json", outputMeta{TotalBytes: 1}); err == nil {
			t.Fatalf("expected close error")
		}
	})
}
