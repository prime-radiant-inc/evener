package jobstore

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func w2tailWriteMeta(t *testing.T, fs afero.Fs, path string, meta outputMeta) {
	t.Helper()
	if err := writeOutputMetaFileFs(fs, path, meta); err != nil {
		t.Fatalf("write meta %s: %v", path, err)
	}
}

// readValidOutputMetaFs validates the final (non-pending) metadata: the absent,
// mismatch, corrupt, retained-bytes-invalid, and both happy paths.
func TestW2Tail_readValidOutputMetaFs(t *testing.T) {
	const content = "0123456789"
	fullHash := outputBytesSHA256([]byte(content))

	setup := func() (afero.Fs, string, string) {
		fs := afero.NewMemMapFs()
		outPath := "job.out"
		if err := afero.WriteFile(fs, outPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return fs, outPath, outputMetaPath(outPath)
	}

	t.Run("absent", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		_, ok, err := readValidOutputMetaFs(fs, metaPath, outPath, int64(len(content)))
		if ok || err != nil {
			t.Fatalf("absent meta: ok=%v err=%v", ok, err)
		}
	})

	t.Run("happy exact", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		w2tailWriteMeta(t, fs, metaPath, outputMeta{TotalBytes: 10, RetainedStart: 0, RetainedSHA256: fullHash})
		meta, ok, err := readValidOutputMetaFs(fs, metaPath, outPath, 10)
		if !ok || err != nil {
			t.Fatalf("happy: ok=%v err=%v", ok, err)
		}
		if meta.TotalBytes != 10 {
			t.Errorf("TotalBytes=%d", meta.TotalBytes)
		}
	})

	t.Run("sha mismatch", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		w2tailWriteMeta(t, fs, metaPath, outputMeta{TotalBytes: 10, RetainedStart: 0, RetainedSHA256: "deadbeef"})
		if _, _, err := readValidOutputMetaFs(fs, metaPath, outPath, 10); err == nil {
			t.Fatalf("expected sha-mismatch error")
		}
	})

	t.Run("retained mismatch", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		// metaRetained (10) > retained (5) triggers the != branch.
		w2tailWriteMeta(t, fs, metaPath, outputMeta{TotalBytes: 10, RetainedStart: 0, RetainedSHA256: fullHash})
		if _, _, err := readValidOutputMetaFs(fs, metaPath, outPath, 5); err == nil {
			t.Fatalf("expected retained-mismatch error")
		}
	})

	t.Run("invalid retained bytes", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		w2tailWriteMeta(t, fs, metaPath, outputMeta{TotalBytes: 1, RetainedStart: 5, RetainedSHA256: fullHash})
		if _, _, err := readValidOutputMetaFs(fs, metaPath, outPath, 10); err == nil {
			t.Fatalf("expected invalid-retained error")
		}
	})

	t.Run("corrupt json", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		if err := afero.WriteFile(fs, metaPath, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readValidOutputMetaFs(fs, metaPath, outPath, 10); err == nil {
			t.Fatalf("expected parse error")
		}
	})

	// metaRetained < retained: the prefix-reconciliation happy path where the
	// stored meta accounted for fewer bytes than are now retained on disk.
	t.Run("prefix reconcile", func(t *testing.T) {
		fs, outPath, metaPath := setup()
		prefixHash := outputBytesSHA256([]byte("0123")) // first 4 bytes
		w2tailWriteMeta(t, fs, metaPath, outputMeta{TotalBytes: 4, RetainedStart: 0, RetainedSHA256: prefixHash})
		meta, ok, err := readValidOutputMetaFs(fs, metaPath, outPath, 10)
		if !ok || err != nil {
			t.Fatalf("prefix reconcile: ok=%v err=%v", ok, err)
		}
		if meta.TotalBytes != 10 || meta.RetainedSHA256 != fullHash {
			t.Errorf("reconciled meta = %+v", meta)
		}
	})
}

// readValidPendingOutputMeta reconciles a crash mid-prune between a newer
// pending meta and the older final meta.
func TestW2Tail_readValidPendingOutputMeta_DeepReconcile(t *testing.T) {
	const content = "0123456789" // R = 10
	fs := afero.NewMemMapFs()
	outPath := "job.out"
	if err := afero.WriteFile(fs, outPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pendingPath := outputPendingMetaPath(outputMetaPath(outPath))
	finalPath := outputMetaPath(outPath)

	// See package output.go: metaRetained=6, prefixLen=4.
	// pending: TotalBytes=20, RetainedStart=14, tail = output[4:10] "456789".
	w2tailWriteMeta(t, fs, pendingPath, outputMeta{
		TotalBytes: 20, RetainedStart: 14, RetainedSHA256: outputBytesSHA256([]byte("456789")),
	})
	// final (older): TotalBytes=14, RetainedStart=10, prefix = output[0:4] "0123".
	w2tailWriteMeta(t, fs, finalPath, outputMeta{
		TotalBytes: 14, RetainedStart: 10, RetainedSHA256: outputBytesSHA256([]byte("0123")),
	})

	meta, ok, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 10)
	if !ok || err != nil {
		t.Fatalf("deep reconcile: ok=%v err=%v", ok, err)
	}
	if meta.RetainedStart != 10 || meta.RetainedSHA256 != outputBytesSHA256([]byte(content)) {
		t.Errorf("reconciled pending meta = %+v", meta)
	}
}

// readValidPendingOutputMeta shares the exact/mismatch arms with the final path.
func TestW2Tail_readValidPendingOutputMeta_ExactAndErrors(t *testing.T) {
	const content = "hello"
	fs := afero.NewMemMapFs()
	outPath := "j.out"
	if err := afero.WriteFile(fs, outPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	pendingPath := outputPendingMetaPath(outputMetaPath(outPath))
	finalPath := outputMetaPath(outPath)
	hash := outputBytesSHA256([]byte(content))

	// Exact happy path.
	w2tailWriteMeta(t, fs, pendingPath, outputMeta{TotalBytes: 5, RetainedStart: 0, RetainedSHA256: hash})
	if _, ok, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 5); !ok || err != nil {
		t.Fatalf("exact: ok=%v err=%v", ok, err)
	}

	// SHA mismatch.
	w2tailWriteMeta(t, fs, pendingPath, outputMeta{TotalBytes: 5, RetainedStart: 0, RetainedSHA256: "nope"})
	if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 5); err == nil {
		t.Fatalf("expected sha mismatch error")
	}

	// Suffix mismatch on the metaRetained<retained arm (final meta missing).
	w2tailWriteMeta(t, fs, pendingPath, outputMeta{TotalBytes: 3, RetainedStart: 0, RetainedSHA256: outputBytesSHA256([]byte("llo"))})
	_ = fs.Remove(finalPath)
	if _, _, err := readValidPendingOutputMeta(fs, pendingPath, finalPath, outPath, 5); err == nil {
		t.Fatalf("expected missing-final-meta error")
	}
}

func TestW2Tail_outputFileHasPrefixSuffixSHA256_OpenError(t *testing.T) {
	fs := afero.NewMemMapFs()
	if _, err := outputFileHasPrefixSHA256(fs, "missing", 4, "x"); err == nil {
		t.Errorf("prefix open error not surfaced")
	}
	if _, err := outputFileHasSuffixSHA256(fs, "missing", 0, 4, "x"); err == nil {
		t.Errorf("suffix open error not surfaced")
	}
}

func TestW2Tail_writeOutputMetaFileFs_EmptyPathAndError(t *testing.T) {
	if err := writeOutputMetaFileFs(afero.NewMemMapFs(), "", outputMeta{}); err != nil {
		t.Errorf("empty path should be no-op, got %v", err)
	}
	// A read-only fs surfaces the OpenFile error arm.
	ro := afero.NewReadOnlyFs(afero.NewMemMapFs())
	if err := writeOutputMetaFileFs(ro, "dir/meta.json", outputMeta{TotalBytes: 1}); err == nil {
		t.Errorf("read-only fs should surface a write error")
	} else if !strings.Contains(err.Error(), "jobstore") {
		t.Errorf("unexpected error: %v", err)
	}
	_ = errors.New
}
