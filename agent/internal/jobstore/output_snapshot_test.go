package jobstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestReadOutputSnapshotFullTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "full output\n")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 1024, false)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "full output\n" || got.TotalBytes != 12 || got.RetainedStart != 0 || got.Truncated {
		t.Fatalf("snapshot = %+v, want complete 12-byte output", got)
	}
}

func TestReadOutputSnapshotTruncatedHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdefgh")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 3, true)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "abc" || got.TotalBytes != 8 || got.RetainedStart != 0 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want truncated head abc", got)
	}
}

func TestReadOutputSnapshotRetentionPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 5)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdefgh")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputSnapshot(path, 1024, false)
	if err != nil {
		t.Fatalf("ReadOutputSnapshot: %v", err)
	}
	if string(got.Content) != "defgh" || got.TotalBytes != 8 || got.RetainedStart != 3 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want retained tail defgh at lifetime offset 3", got)
	}
}

func TestReadOutputSnapshotAlignsRuneWindowEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 1024)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "😀😀")
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	for _, tc := range []struct {
		name     string
		fromHead bool
	}{
		{name: "head", fromHead: true},
		{name: "tail", fromHead: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadOutputSnapshot(path, 6, tc.fromHead)
			if err != nil {
				t.Fatalf("ReadOutputSnapshot: %v", err)
			}
			if string(got.Content) != "😀" || got.TotalBytes != 8 || !got.Truncated {
				t.Fatalf("snapshot = %+v, want one whole rune from an 8-byte output", got)
			}
		})
	}
}

func TestReadOutputSnapshotRejectsInvalidLimit(t *testing.T) {
	_, err := ReadOutputSnapshot(filepath.Join(t.TempDir(), "unused.log"), -1, false)
	if !errors.Is(err, ErrInvalidLimit) {
		t.Fatalf("ReadOutputSnapshot error = %v, want ErrInvalidLimit", err)
	}
}

func TestReadOutputSnapshotDistinguishesMalformedMetadataAndMissingArtifact(t *testing.T) {
	t.Run("malformed metadata", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "job.log")
		if err := os.WriteFile(path, []byte("abc"), 0o644); err != nil {
			t.Fatalf("write output: %v", err)
		}
		if err := os.WriteFile(outputMetaPath(path), []byte("not-json\n"), 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}

		attempts := 0
		_, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
			attempts++
			return readOutputSnapshotOnce(afero.NewOsFs(), path, 3, false)
		})
		if err == nil || !strings.Contains(err.Error(), "parse output metadata") || errors.Is(err, os.ErrNotExist) || attempts != 1 {
			t.Fatalf("snapshot error = %v attempts=%d, want one malformed-metadata attempt", err, attempts)
		}
	})

	t.Run("missing artifact", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing.log")
		_, err := ReadOutputSnapshot(path, 3, false)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ReadOutputSnapshot error = %v, want missing-artifact error", err)
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatalf("read temp directory: %v", readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("missing read created artifacts: %v", entries)
		}
	})
}

func TestReadOutputSnapshotUsesOnlyReadOnlyFilesystemOperations(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("stable\n"), 7, 0)
	fs := &snapshotReadOnlyAuditFS{Fs: base}

	got, err := readOutputSnapshotFs(fs, path, 1024, false)
	if err != nil {
		t.Fatalf("readOutputSnapshotFs: %v", err)
	}
	if string(got.Content) != "stable\n" {
		t.Fatalf("content = %q, want stable newline", got.Content)
	}
	if fs.mutations != 0 {
		t.Fatalf("snapshot attempted %d mutating filesystem operations", fs.mutations)
	}
}

func TestReadOutputSnapshotRetriesOneChangedAttempt(t *testing.T) {
	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		if attempts == 1 {
			return OutputSnapshot{}, errOutputChanged
		}
		return OutputSnapshot{Content: []byte("stable\n"), TotalBytes: 7}, nil
	})
	if err != nil || attempts != 2 || string(got.Content) != "stable\n" {
		t.Fatalf("snapshot=%+v attempts=%d err=%v", got, attempts, err)
	}
}

func TestReadOutputSnapshotDetectsPostReadMetadataChange(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("first\n"), 6, 0)
	fs := &snapshotChangingFS{
		Fs:   base,
		path: path,
		replacements: []snapshotReplacement{
			{content: []byte("later\n"), total: 12, retainedStart: 6},
		},
	}

	got, err := readOutputSnapshotFs(fs, path, 1024, false)
	if err != nil {
		t.Fatalf("readOutputSnapshotFs: %v", err)
	}
	if string(got.Content) != "later\n" || got.TotalBytes != 12 || got.RetainedStart != 6 {
		t.Fatalf("snapshot = %+v, want stable second attempt after retention advanced", got)
	}
}

func TestReadOutputSnapshotReturnsChangedErrorAfterTwoRaces(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("first\n"), 6, 0)
	fs := &snapshotChangingFS{
		Fs:   base,
		path: path,
		replacements: []snapshotReplacement{
			{content: []byte("later\n"), total: 12, retainedStart: 6},
			{content: []byte("third\n"), total: 18, retainedStart: 12},
		},
	}

	_, err := readOutputSnapshotFs(fs, path, 1024, false)
	if !errors.Is(err, ErrOutputChangedDuringRead) {
		t.Fatalf("readOutputSnapshotFs error = %v, want ErrOutputChangedDuringRead", err)
	}
}

func TestReadOutputSnapshotRetriesChangeBeforeInitialMetadataValidation(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("first\n"), 6, 0)
	fs := &snapshotInitialValidationChangeFS{
		Fs:       base,
		path:     path,
		appended: []byte("second\n"),
	}

	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		return readOutputSnapshotOnce(fs, path, 1024, false)
	})
	if err != nil {
		t.Fatalf("snapshot after initial-validation change: %v", err)
	}
	if attempts != 2 || string(got.Content) != "first\nsecond\n" || got.TotalBytes != 13 {
		t.Fatalf("snapshot=%+v attempts=%d, want stable second attempt", got, attempts)
	}
}

func TestReadOutputSnapshotPreservesStablePostReadMetadataError(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("stable\n"), 7, 0)
	fs := &snapshotPostReadMetadataFaultFS{Fs: base, path: path}

	attempts := 0
	_, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		return readOutputSnapshotOnce(fs, path, 1024, false)
	})
	if !errors.Is(err, errSnapshotPostReadMetadata) || errors.Is(err, ErrOutputChangedDuringRead) || attempts != 1 {
		t.Fatalf("snapshot error=%v attempts=%d, want original post-read metadata error without retry", err, attempts)
	}
}

func TestReadOutputSnapshotRetriesCappedPrunePublicationHandoff(t *testing.T) {
	fs := newSnapshotPruneProtocolFS(t, snapshotPruneDuringInitialHash)

	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		return readOutputSnapshotOnce(fs, fs.path, 1024, false)
	})
	if err != nil {
		t.Fatalf("snapshot across capped prune publication: %v", err)
	}
	if attempts != 2 || string(got.Content) != "BBBB" || got.TotalBytes != 8 || got.RetainedStart != 4 {
		t.Fatalf("snapshot=%+v attempts=%d, want stable post-prune retry", got, attempts)
	}
}

func TestReadOutputSnapshotKeepsPartialChangeWhenMetadataObservationFails(t *testing.T) {
	fs := newSnapshotPruneProtocolFS(t, snapshotPruneAfterWindow)
	fs.afterObservationMetaErr = errSnapshotAfterObservedChange

	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		return readOutputSnapshotOnce(fs, fs.path, 1024, false)
	})
	if err != nil {
		t.Fatalf("snapshot after partial changed observation: %v", err)
	}
	if attempts != 2 || string(got.Content) != "BBBB" || got.TotalBytes != 8 || got.RetainedStart != 4 {
		t.Fatalf("snapshot=%+v attempts=%d, want stable retry after observed pending change", got, attempts)
	}
}

func TestReadOutputSnapshotKeepsInnerCoordinateChangeWhenObservationFails(t *testing.T) {
	fs := newSnapshotPruneProtocolFS(t, snapshotPruneAfterWindow)
	fs.afterObservationStatErr = errSnapshotAfterObservedChange

	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		return readOutputSnapshotOnce(fs, fs.path, 1024, false)
	})
	if err != nil {
		t.Fatalf("snapshot after changed coordinates and observation fault: %v", err)
	}
	if attempts != 2 || string(got.Content) != "BBBB" || got.TotalBytes != 8 || got.RetainedStart != 4 {
		t.Fatalf("snapshot=%+v attempts=%d, want retry from inner coordinate change", got, attempts)
	}
}

func TestReadOutputWindowSnapshotReadsRawLifetimeRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 5)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdefgh") // retained "defgh" at lifetime offset 3
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	got, err := ReadOutputWindowSnapshot(path, 4, 3)
	if err != nil {
		t.Fatalf("ReadOutputWindowSnapshot: %v", err)
	}
	if string(got.Content) != "efg" || got.Start != 4 || got.End != 7 || got.TotalBytes != 8 || got.RetainedStart != 3 || !got.Truncated {
		t.Fatalf("snapshot = %+v, want efg at lifetime [4,7) of retained [3,8)", got)
	}
}

func TestReadOutputWindowSnapshotPreservesExactInvalidUTF8Bytes(t *testing.T) {
	base := afero.NewMemMapFs()
	const path = "/job.log"
	want := []byte{0xf0, 0x9f, 0x98}
	mustWriteSnapshotFixture(t, base, path, want, 3, 0)

	got, err := readOutputWindowSnapshotFs(base, path, 0, len(want))
	if err != nil {
		t.Fatalf("readOutputWindowSnapshotFs: %v", err)
	}
	if !bytes.Equal(got.Content, want) || got.Start != 0 || got.End != 3 {
		t.Fatalf("snapshot = %+v content=%x, want exact invalid UTF-8 bytes %x", got, got.Content, want)
	}
}

func TestReadOutputWindowSnapshotValidatesOffsetAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 3)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	appendOutput(t, o, "abcdef") // retained "def" at lifetime offset 3
	if err := o.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	for _, tc := range []struct {
		name      string
		offset    int64
		maxBytes  int
		wantError error
	}{
		{name: "negative offset", offset: -1, maxBytes: 1, wantError: ErrInvalidOffset},
		{name: "pruned offset", offset: 2, maxBytes: 1, wantError: ErrOutputPruned},
		{name: "beyond EOF", offset: 7, maxBytes: 1, wantError: ErrInvalidOffset},
		{name: "negative limit", offset: 3, maxBytes: -1, wantError: ErrInvalidLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadOutputWindowSnapshot(path, tc.offset, tc.maxBytes)
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("error = %v, want %v", err, tc.wantError)
			}
		})
	}

	eof, err := ReadOutputWindowSnapshot(path, 6, 16)
	if err != nil {
		t.Fatalf("EOF window: %v", err)
	}
	if len(eof.Content) != 0 || eof.Start != 6 || eof.End != 6 || eof.TotalBytes != 6 || eof.RetainedStart != 3 {
		t.Fatalf("EOF snapshot = %+v", eof)
	}
}

func TestReadOutputWindowSnapshotRetriesOnceAndReturnsChangedError(t *testing.T) {
	t.Run("one change", func(t *testing.T) {
		attempts := 0
		got, err := readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
			attempts++
			if attempts == 1 {
				return OutputWindowSnapshot{}, errOutputChanged
			}
			return OutputWindowSnapshot{Content: []byte("stable"), Start: 4, End: 10, TotalBytes: 10, RetainedStart: 4}, nil
		})
		if err != nil || attempts != 2 || string(got.Content) != "stable" || got.Start != 4 || got.End != 10 {
			t.Fatalf("snapshot=%+v attempts=%d err=%v", got, attempts, err)
		}
	})

	t.Run("two changes", func(t *testing.T) {
		attempts := 0
		_, err := readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
			attempts++
			return OutputWindowSnapshot{}, errOutputChanged
		})
		if !errors.Is(err, ErrOutputChangedDuringRead) || attempts != 2 {
			t.Fatalf("attempts=%d error=%v, want two attempts and ErrOutputChangedDuringRead", attempts, err)
		}
	})
}

func TestReadOutputWindowSnapshotConcurrentAppendAndPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	o, err := CreateOutputNoSync(path, 128)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	t.Cleanup(func() { _ = o.Close() })
	appendOutput(t, o, "seed\n")

	var finished atomic.Bool
	done := make(chan error, 1)
	go func() {
		defer finished.Store(true)
		for range 150 {
			if _, err := o.Append([]byte("append-line\n")); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	successes := 0
	for !finished.Load() || successes == 0 {
		retainedStart := o.RetainedStart()
		got, err := ReadOutputWindowSnapshot(path, retainedStart, 31)
		if errors.Is(err, ErrOutputChangedDuringRead) || errors.Is(err, ErrOutputPruned) {
			continue
		}
		if err != nil {
			t.Fatalf("ReadOutputWindowSnapshot: %v", err)
		}
		successes++
		if got.Start < got.RetainedStart || got.End < got.Start || got.End > got.TotalBytes || int64(len(got.Content)) != got.End-got.Start {
			t.Fatalf("inconsistent file snapshot = %+v", got)
		}
	}
	if err := <-done; err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestReadOutputSnapshotTruncatedBelowPendingTailIsCorruption pins the reason
// the tail rewrite replaces the output atomically: an output file shorter than
// the pending metadata's retained tail is a state the writer never produces —
// live or crashed — so snapshot readers must report it as metadata corruption,
// never dress it up as a retryable concurrent change.
func TestReadOutputSnapshotTruncatedBelowPendingTailIsCorruption(t *testing.T) {
	for name, retained := range map[string][]byte{
		"empty-file":   nil,
		"partial-tail": []byte("fg"),
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			const path = "/job.log"
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

			wantErr := "jobstore: output metadata does not match retained output"
			if _, err := readOutputWindowSnapshotFs(fs, path, 10, 5); err == nil || err.Error() != wantErr {
				t.Fatalf("window snapshot err = %v, want %q", err, wantErr)
			}
			if _, err := readOutputSnapshotFs(fs, path, 5, false); err == nil || err.Error() != wantErr {
				t.Fatalf("tail snapshot err = %v, want %q", err, wantErr)
			}
		})
	}
}

func TestReadOutputWindowSnapshotUsesProductionPendingPublicationOrder(t *testing.T) {
	// Capture the capped retained size before the append starts. The append then
	// pauses after pruneLocked publishes .pending and before it opens the
	// replacement file, matching the production writer order.
	path := filepath.Join(t.TempDir(), "job.log")
	fs := newSnapshotAppendPruneFS(path)
	store, err := createOutputFsWithSync(fs, path, 4, true)
	if err != nil {
		t.Fatalf("create output: %v", err)
	}
	var (
		appendDone    chan error
		appendStarted bool
		appendJoined  bool
	)
	t.Cleanup(func() {
		// The append holds OutputStore.mu while waiting for the replacement gate;
		// release it before joining the append or closing the store.
		fs.releaseInitialMetadataValidation()
		fs.releaseOutputReplacement()
		if appendStarted && !appendJoined {
			<-appendDone
		}
		_ = store.Close()
	})
	appendOutput(t, store, "AAAA")
	fs.armInitialRetainedSize()

	type snapshotResult struct {
		got OutputWindowSnapshot
		err error
	}
	snapshotDone := make(chan snapshotResult, 1)
	go func() {
		got, err := readOutputWindowSnapshotFs(fs, path, 0, len("BBBB"))
		snapshotDone <- snapshotResult{got: got, err: err}
	}()
	<-fs.initialRetainedSizeCaptured

	appendDone = make(chan error, 1)
	appendStarted = true
	go func() {
		_, appendErrValue := store.Append([]byte("BBBB"))
		appendDone <- appendErrValue
	}()
	<-fs.pendingPublished
	fs.releaseInitialMetadataValidation()
	result := <-snapshotDone
	if result.err != nil {
		t.Fatalf("snapshot during pending publication: %v", result.err)
	}
	got := result.got
	if string(got.Content) != "AAAA" || got.TotalBytes != 8 || got.RetainedStart != 0 {
		t.Fatalf("snapshot during pending publication = %+v, want old prefix with successor coordinates", got)
	}
	// Keep the real writer paused and take a second observation at the captured
	// Stat boundary. This drives the handoff predicate through the public
	// snapshot path without calling either production validation helper.
	fs.forceCappedObservation.Store(true)
	_, err = readOutputWindowSnapshotFs(fs, path, 0, len("BBBB"))
	fs.forceCappedObservation.Store(false)
	if err == nil || err.Error() != "jobstore: output metadata does not match retained output" {
		t.Fatalf("snapshot with capped observation error = %v, want metadata corruption", err)
	}
	fs.releaseOutputReplacement()
	appendErr := <-appendDone
	appendJoined = true
	if appendErr != nil {
		t.Fatalf("append: %v", appendErr)
	}
}

func TestReadOutputWindowSnapshotPendingMetadataMismatchRemainsCorruption(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/job.log"
	const content = "AAAA"
	mustWriteSnapshotFixture(t, fs, path, []byte(content), 4, 0)
	if err := writeSnapshotMetadataFile(fs, outputPendingMetaPath(outputMetaPath(path)), []byte("BBBB"), 4, 0); err != nil {
		t.Fatalf("write pending metadata: %v", err)
	}

	_, err := readOutputWindowSnapshotFs(fs, path, 0, len(content))
	if err == nil || err.Error() != "jobstore: output metadata does not match retained output" {
		t.Fatalf("snapshot error = %v, want metadata corruption", err)
	}
}

func TestReadOutputSnapshotRejectsRollbackStaleAndRepeatedHashPendingMetadata(t *testing.T) {
	const corruption = "jobstore: output metadata does not match retained output"
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
			fs := afero.NewMemMapFs()
			const path = "/job.log"
			if err := afero.WriteFile(fs, path, []byte(tc.output), 0o644); err != nil {
				t.Fatalf("write output: %v", err)
			}
			if err := writeSnapshotMetadataFile(fs, outputMetaPath(path), []byte(tc.finalContent), tc.finalTotal, tc.finalStart); err != nil {
				t.Fatalf("write final metadata: %v", err)
			}
			if err := writeSnapshotMetadataFile(fs, outputPendingMetaPath(outputMetaPath(path)), []byte(tc.pending), tc.pendingTotal, tc.pendingStart); err != nil {
				t.Fatalf("write pending metadata: %v", err)
			}

			if _, err := readOutputWindowSnapshotFs(fs, path, 0, len(tc.output)); err == nil || err.Error() != corruption {
				t.Fatalf("window snapshot error = %v, want durable metadata corruption", err)
			}
			if _, err := readOutputSnapshotFs(fs, path, len(tc.output), false); err == nil || err.Error() != corruption {
				t.Fatalf("tail snapshot error = %v, want durable metadata corruption", err)
			}
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

var errSnapshotMutation = errors.New("snapshot test: mutating filesystem operation")

type snapshotReadOnlyAuditFS struct {
	afero.Fs
	mutations int
}

func (fs *snapshotReadOnlyAuditFS) rejectMutation() error {
	fs.mutations++
	return errSnapshotMutation
}

func (fs *snapshotReadOnlyAuditFS) Create(string) (afero.File, error) {
	return nil, fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Mkdir(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) MkdirAll(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	const mutating = os.O_WRONLY | os.O_RDWR | os.O_APPEND | os.O_CREATE | os.O_EXCL | os.O_TRUNC
	if flag&mutating != 0 {
		return nil, fs.rejectMutation()
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs *snapshotReadOnlyAuditFS) Remove(string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) RemoveAll(string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Rename(string, string) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Chmod(string, os.FileMode) error {
	return fs.rejectMutation()
}

func (fs *snapshotReadOnlyAuditFS) Chtimes(string, time.Time, time.Time) error {
	return fs.rejectMutation()
}

type snapshotChangingFS struct {
	afero.Fs
	path          string
	outputOpens   int
	mutatedWindow int
	replacements  []snapshotReplacement
}

type snapshotReplacement struct {
	content       []byte
	total         int64
	retainedStart int64
}

func (fs *snapshotChangingFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.outputOpens++
	}
	return fs.Fs.Open(name)
}

func (fs *snapshotChangingFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		windowRead := fs.outputOpens > 0 && fs.outputOpens%2 == 0
		if windowRead && fs.mutatedWindow != fs.outputOpens && len(fs.replacements) > 0 {
			replacement := fs.replacements[0]
			fs.replacements = fs.replacements[1:]
			if err := writeSnapshotFixture(fs.Fs, fs.path, replacement.content, replacement.total, replacement.retainedStart); err != nil {
				return nil, err
			}
			fs.mutatedWindow = fs.outputOpens
		}
	}
	return fs.Fs.Stat(name)
}

type snapshotInitialValidationChangeFS struct {
	afero.Fs
	path        string
	appended    []byte
	didAppend   bool
	didFinalize bool
	outputStats int
}

func (fs *snapshotInitialValidationChangeFS) Open(name string) (afero.File, error) {
	if name == outputPendingMetaPath(outputMetaPath(fs.path)) && !fs.didAppend {
		f, err := fs.OpenFile(fs.path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(fs.appended); err != nil {
			_ = f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		fs.didAppend = true
	}
	return fs.Fs.Open(name)
}

func (fs *snapshotInitialValidationChangeFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		fs.outputStats++
		if fs.outputStats >= 2 && fs.didAppend && !fs.didFinalize {
			content, err := afero.ReadFile(fs.Fs, fs.path)
			if err != nil {
				return nil, err
			}
			if err := writeSnapshotMetadata(fs.Fs, fs.path, content, int64(len(content)), 0); err != nil {
				return nil, err
			}
			fs.didFinalize = true
		}
	}
	return fs.Fs.Stat(name)
}

var errSnapshotPostReadMetadata = errors.New("snapshot test: post-read metadata fault")

type snapshotPostReadMetadataFaultFS struct {
	afero.Fs
	path             string
	outputOpens      int
	faultedForWindow bool
}

func (fs *snapshotPostReadMetadataFaultFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.outputOpens++
		if fs.outputOpens%2 == 0 {
			fs.faultedForWindow = false
		}
	}
	if name == outputMetaPath(fs.path) && fs.outputOpens > 0 && fs.outputOpens%2 == 0 && !fs.faultedForWindow {
		fs.faultedForWindow = true
		return nil, errSnapshotPostReadMetadata
	}
	return fs.Fs.Open(name)
}

type snapshotPruneStart uint8

const (
	snapshotPruneDuringInitialHash snapshotPruneStart = iota
	snapshotPruneAfterWindow
)

type snapshotPrunePhase uint8

const (
	snapshotPruneOld snapshotPrunePhase = iota
	snapshotPrunePending
	snapshotPruneFinal
)

var errSnapshotAfterObservedChange = errors.New("snapshot test: observation fault after change")

type snapshotPruneProtocolFS struct {
	afero.Fs
	path                    string
	start                   snapshotPruneStart
	phase                   snapshotPrunePhase
	outputOpens             int
	outputStats             int
	afterObservationMetaErr error
	afterObservationStatErr error
}

type snapshotAppendPruneFS struct {
	afero.Fs
	path                           string
	pendingPublished               chan struct{}
	allowOutputReplacement         chan struct{}
	initialRetainedSizeCaptured    chan struct{}
	allowInitialMetadataValidation chan struct{}
	pendingOnce                    sync.Once
	releaseOnce                    sync.Once
	initialOnce                    sync.Once
	initialReleaseOnce             sync.Once
	captureInitialStat             atomic.Bool
	statCalls                      atomic.Int32
	outputHashOpen                 atomic.Bool
	finalMetaAfterOutputHash       atomic.Bool
	forceCappedObservation         atomic.Bool
	initialInfo                    atomic.Value
}

func newSnapshotAppendPruneFS(path string) *snapshotAppendPruneFS {
	return &snapshotAppendPruneFS{
		Fs:                             afero.NewOsFs(),
		path:                           path,
		pendingPublished:               make(chan struct{}),
		allowOutputReplacement:         make(chan struct{}),
		initialRetainedSizeCaptured:    make(chan struct{}),
		allowInitialMetadataValidation: make(chan struct{}),
	}
}

func (fs *snapshotAppendPruneFS) armInitialRetainedSize() {
	fs.captureInitialStat.Store(true)
}

func (fs *snapshotAppendPruneFS) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(name)
	return info, true, err
}

func (fs *snapshotAppendPruneFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if name == fs.path+".tmp" {
		<-fs.allowOutputReplacement
	}
	return fs.Fs.OpenFile(name, flag, perm)
}

func (fs *snapshotAppendPruneFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.outputHashOpen.Store(true)
		fs.finalMetaAfterOutputHash.Store(false)
	}
	if name == outputMetaPath(fs.path) && fs.outputHashOpen.Load() {
		fs.finalMetaAfterOutputHash.Store(true)
	}
	return fs.Fs.Open(name)
}

func (fs *snapshotAppendPruneFS) Rename(oldpath, newpath string) error {
	err := fs.Fs.Rename(oldpath, newpath)
	if err == nil && newpath == outputPendingMetaPath(outputMetaPath(fs.path)) {
		fs.pendingOnce.Do(func() { close(fs.pendingPublished) })
	}
	return err
}

func (fs *snapshotAppendPruneFS) Stat(name string) (os.FileInfo, error) {
	info, err := fs.Fs.Stat(name)
	if name == fs.path && fs.captureInitialStat.Load() {
		call := fs.statCalls.Add(1)
		fs.initialOnce.Do(func() {
			fs.initialInfo.Store(info)
			close(fs.initialRetainedSizeCaptured)
			<-fs.allowInitialMetadataValidation
		})
		// The parent has no expanded-pending validation: its first post-hash
		// Stat is the outer observation. Keep that observation at the captured
		// size so the original metadata-corruption error is returned. On the
		// corrected path, that Stat is the expanded helper's entry boundary and
		// must report the actual expanded size.
		if fs.forceCappedObservation.Load() || (call == 2 && fs.finalMetaAfterOutputHash.Load()) {
			if captured := fs.initialInfo.Load(); captured != nil {
				return captured.(os.FileInfo), nil
			}
		}
	}
	return info, err
}

func (fs *snapshotAppendPruneFS) releaseInitialMetadataValidation() {
	fs.initialReleaseOnce.Do(func() { close(fs.allowInitialMetadataValidation) })
}

func (fs *snapshotAppendPruneFS) releaseOutputReplacement() {
	fs.releaseOnce.Do(func() { close(fs.allowOutputReplacement) })
}

func newSnapshotPruneProtocolFS(t *testing.T, start snapshotPruneStart) *snapshotPruneProtocolFS {
	t.Helper()
	base := afero.NewMemMapFs()
	const path = "/job.log"
	mustWriteSnapshotFixture(t, base, path, []byte("AAAA"), 4, 0)
	return &snapshotPruneProtocolFS{Fs: base, path: path, start: start}
}

func (fs *snapshotPruneProtocolFS) Open(name string) (afero.File, error) {
	if name == fs.path {
		fs.outputOpens++
		if fs.start == snapshotPruneDuringInitialHash && fs.phase == snapshotPruneOld && fs.outputOpens == 1 {
			if err := fs.publishPending(); err != nil {
				return nil, err
			}
		}
	}
	if name == outputMetaPath(fs.path) && fs.phase == snapshotPrunePending {
		if fs.afterObservationMetaErr != nil && fs.outputStats >= 3 {
			err := fs.afterObservationMetaErr
			fs.afterObservationMetaErr = nil
			if publishErr := fs.publishFinal(); publishErr != nil {
				return nil, publishErr
			}
			return nil, err
		}
		f, err := fs.Fs.Open(name)
		if err != nil {
			return nil, err
		}
		return &snapshotCloseHookFile{File: f, afterClose: fs.publishFinal}, nil
	}
	return fs.Fs.Open(name)
}

func (fs *snapshotPruneProtocolFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		fs.outputStats++
		if fs.start == snapshotPruneAfterWindow && fs.phase == snapshotPruneOld && fs.outputStats == 2 {
			if err := fs.publishPending(); err != nil {
				return nil, err
			}
		}
		if fs.outputStats >= 3 && fs.afterObservationStatErr != nil {
			err := fs.afterObservationStatErr
			fs.afterObservationStatErr = nil
			if publishErr := fs.publishFinal(); publishErr != nil {
				return nil, publishErr
			}
			return nil, err
		}
	}
	return fs.Fs.Stat(name)
}

func (fs *snapshotPruneProtocolFS) publishPending() error {
	const retained = "BBBB"
	if err := writeSnapshotMetadataFile(fs.Fs, outputPendingMetaPath(outputMetaPath(fs.path)), []byte(retained), 8, 4); err != nil {
		return err
	}
	if err := afero.WriteFile(fs.Fs, fs.path, []byte(retained), 0o644); err != nil {
		return err
	}
	fs.phase = snapshotPrunePending
	return nil
}

func (fs *snapshotPruneProtocolFS) publishFinal() error {
	if fs.phase != snapshotPrunePending {
		return nil
	}
	const retained = "BBBB"
	if err := writeSnapshotMetadata(fs.Fs, fs.path, []byte(retained), 8, 4); err != nil {
		return err
	}
	if err := fs.Remove(outputPendingMetaPath(outputMetaPath(fs.path))); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fs.phase = snapshotPruneFinal
	return nil
}

type snapshotCloseHookFile struct {
	afero.File
	afterClose func() error
}

func (f *snapshotCloseHookFile) Close() error {
	err := f.File.Close()
	if hookErr := f.afterClose(); err == nil {
		err = hookErr
	}
	return err
}

// The file size returns to its cap while pending/final metadata remain unchanged
// across both observations. The hash in between belongs to the expanded inode.
func TestReadOutputWindowSnapshotDetectsReplacementDuringHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	fs := &snapshotHashReplacementFS{snapshotAppendPruneFS: newSnapshotAppendPruneFS(path), finalPublication: make(chan struct{}), allowFinalPublication: make(chan struct{})}
	store, err := createOutputFsWithSync(fs, path, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	appendOutput(t, store, "AAAA")
	fs.armInitialRetainedSize()
	fs.fencePublication.Store(true)
	type result struct {
		snapshot OutputWindowSnapshot
		err      error
	}
	snapshotDone := make(chan result, 1)
	go func() {
		snapshot, err := readOutputWindowSnapshotFs(fs, path, 4, 4)
		snapshotDone <- result{snapshot, err}
	}()
	<-fs.initialRetainedSizeCaptured
	appendDone := make(chan error, 1)
	go func() { _, err := store.Append([]byte("BBBB")); appendDone <- err }()
	defer func() {
		fs.releaseInitialMetadataValidation()
		fs.releaseOutputReplacement()
		close(fs.allowFinalPublication)
		if err := <-appendDone; err != nil {
			t.Error(err)
		}
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	<-fs.pendingPublished
	fs.captureHash.Store(true)
	fs.releaseInitialMetadataValidation()
	got := <-snapshotDone
	if got.err != nil {
		t.Fatalf("snapshot across replacement: %v", got.err)
	}
	if string(got.snapshot.Content) != "BBBB" || got.snapshot.Start != 4 || got.snapshot.TotalBytes != 8 {
		t.Fatalf("snapshot=%+v", got.snapshot)
	}
}

type snapshotHashReplacementFS struct {
	*snapshotAppendPruneFS
	captureHash           atomic.Bool
	fencePublication      atomic.Bool
	finalPublication      chan struct{}
	allowFinalPublication chan struct{}
	finalOnce             sync.Once
}

func (fs *snapshotHashReplacementFS) Open(name string) (afero.File, error) {
	file, err := fs.snapshotAppendPruneFS.Open(name)
	if err == nil && name == fs.path && fs.captureHash.Swap(false) {
		return &snapshotCloseHookFile{File: file, afterClose: func() error {
			fs.releaseOutputReplacement()
			<-fs.finalPublication
			return nil
		}}, nil
	}
	return file, err
}

func (fs *snapshotHashReplacementFS) Rename(oldpath, newpath string) error {
	if newpath == outputMetaPath(fs.path) && fs.fencePublication.Load() {
		fs.finalOnce.Do(func() { close(fs.finalPublication) })
		<-fs.allowFinalPublication
	}
	return fs.snapshotAppendPruneFS.Rename(oldpath, newpath)
}
