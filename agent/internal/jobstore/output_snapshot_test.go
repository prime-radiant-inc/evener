package jobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

func TestReadOutputSnapshotRetentionPruned(t *testing.T) {
	path, f := newOutputSnapshotFile(t, 5, "abcdefgh")

	got, err := ReadOutputSnapshotFromFile(path, f, 1024, false)
	if err != nil {
		t.Fatalf("ReadOutputSnapshotFromFile: %v", err)
	}
	if string(got.Content) != "defgh" || got.TotalBytes != 8 || got.RetainedStart != 3 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want retained tail defgh at lifetime offset 3", got)
	}
}

// requireMetadataCorruption pins that stable metadata the writer never
// produces is reported as corruption, never as a retryable concurrent change.
func requireMetadataCorruption(t *testing.T, err error) {
	t.Helper()
	if err == nil || errors.Is(err, ErrOutputChangedDuringRead) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want durable metadata corruption", err)
	}
}

func TestReadOutputSnapshotRejectsMalformedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write output: %v", err)
	}
	if err := os.WriteFile(outputMetaPath(path), []byte("not-json\n"), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	_, err := readOutputSnapshotAtPath(path, 3, false)
	requireMetadataCorruption(t, err)
}

// An output file shorter than the pending metadata's retained tail is a state
// the writer never produces, live or crashed, because the tail rewrite replaces
// the output atomically.
func TestReadOutputSnapshotTruncatedBelowPendingTailIsCorruption(t *testing.T) {
	for name, retained := range map[string][]byte{
		"empty-file":   nil,
		"partial-tail": []byte("fg"),
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewOsFs()
			path := filepath.Join(t.TempDir(), "job.log")
			if err := afero.WriteFile(fs, path, retained, 0o644); err != nil {
				t.Fatalf("write truncated output: %v", err)
			}
			metaPath := outputMetaPath(path)
			// The final metadata is the previous cycle's; the pending metadata
			// claims a 5-byte retained tail the file no longer holds.
			if err := writeSnapshotMetadataFile(fs, metaPath, []byte("abcde"), 10, 5); err != nil {
				t.Fatalf("write final metadata: %v", err)
			}
			if err := writeSnapshotMetadataFile(fs, outputPendingMetaPath(metaPath), []byte("fghij"), 15, 10); err != nil {
				t.Fatalf("write pending metadata: %v", err)
			}

			_, err := readOutputWindowSnapshotAtPath(path, 10, 5)
			requireMetadataCorruption(t, err)
			_, err = readOutputSnapshotAtPath(path, 5, false)
			requireMetadataCorruption(t, err)
		})
	}
}

func TestReadOutputWindowSnapshotPendingMetadataMismatchRemainsCorruption(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "job.log")
	const content = "AAAA"
	mustWriteSnapshotFixture(t, fs, path, []byte(content), 4, 0)
	if err := writeSnapshotMetadataFile(fs, outputPendingMetaPath(outputMetaPath(path)), []byte("BBBB"), 4, 0); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}

	_, err := readOutputWindowSnapshotAtPath(path, 0, len(content))
	requireMetadataCorruption(t, err)
}

func TestReadOutputSnapshotRejectsRollbackStaleAndRepeatedHashPendingMetadata(t *testing.T) {
	for name, tc := range map[string]struct {
		output       string
		finalContent string
		finalTotal   int64
		finalStart   int64
		pending      string
		pendingTotal int64
		pendingStart int64
	}{
		"rollback": {
			output: "BBBB", finalContent: "BBBB", finalTotal: 8, finalStart: 4,
			pending: "AAAA", pendingTotal: 4, pendingStart: 0,
		},
		"stale pending generation": {
			output: "BBBB", finalContent: "BBBB", finalTotal: 12, finalStart: 8,
			pending: "AAAA", pendingTotal: 8, pendingStart: 4,
		},
		"repeated hash does not match output": {
			output: "CCCC", finalContent: "AAAA", finalTotal: 4, finalStart: 0,
			pending: "AAAA", pendingTotal: 8, pendingStart: 4,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewOsFs()
			path := filepath.Join(t.TempDir(), "job.log")
			if err := afero.WriteFile(fs, path, []byte(tc.output), 0o644); err != nil {
				t.Fatalf("write output: %v", err)
			}
			if err := writeSnapshotMetadataFile(fs, outputMetaPath(path), []byte(tc.finalContent), tc.finalTotal, tc.finalStart); err != nil {
				t.Fatalf("write final metadata: %v", err)
			}
			if err := writeSnapshotMetadataFile(fs, outputPendingMetaPath(outputMetaPath(path)), []byte(tc.pending), tc.pendingTotal, tc.pendingStart); err != nil {
				t.Fatalf("write pending metadata: %v", err)
			}

			_, err := readOutputWindowSnapshotAtPath(path, 0, len(tc.output))
			requireMetadataCorruption(t, err)
			_, err = readOutputSnapshotAtPath(path, len(tc.output), false)
			requireMetadataCorruption(t, err)
		})
	}
}

func mustWriteSnapshotFixture(t *testing.T, fs afero.Fs, path string, content []byte, total, retainedStart int64) {
	t.Helper()
	if err := writeSnapshotFixture(fs, path, content, total, retainedStart); err != nil {
		t.Fatalf("write snapshot fixture: %v", err)
	}
}

func writeSnapshotFixture(fs afero.Fs, path string, content []byte, total, retainedStart int64) error {
	if err := afero.WriteFile(fs, path, content, 0o644); err != nil {
		return err
	}
	return writeSnapshotMetadata(fs, path, content, total, retainedStart)
}

func writeSnapshotMetadata(fs afero.Fs, path string, content []byte, total, retainedStart int64) error {
	return writeSnapshotMetadataFile(fs, outputMetaPath(path), content, total, retainedStart)
}

func writeSnapshotMetadataFile(fs afero.Fs, metaPath string, content []byte, total, retainedStart int64) error {
	sum := sha256.Sum256(content)
	meta, err := json.Marshal(outputMeta{
		TotalBytes:     total,
		RetainedStart:  retainedStart,
		RetainedSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return err
	}
	return afero.WriteFile(fs, metaPath, append(meta, '\n'), 0o644)
}
