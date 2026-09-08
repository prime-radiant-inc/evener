package jobstore

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// ErrOutputChangedDuringRead is returned when both immediate attempts to read
// a consistent output snapshot race with an append or retention prune.
var ErrOutputChangedDuringRead = errors.New("jobstore: output changed during read")

var errOutputChanged = errors.New("jobstore: output snapshot changed")

// OutputSnapshot is a point-in-time window over a job's retained output.
type OutputSnapshot struct {
	Content              []byte
	TotalBytes           int64
	RetainedStart        int64
	RetainedStartPartial bool
	Truncated            bool
}

// OutputWindowSnapshot is a stable raw forward window over retained job
// output. Start, End, TotalBytes, and RetainedStart all use lifetime offsets.
type OutputWindowSnapshot struct {
	Content              []byte
	Start                int64
	End                  int64
	TotalBytes           int64
	RetainedStart        int64
	RetainedStartPartial bool
	Truncated            bool
}

// outputSnapshotObservation is the comparable state changed by OutputStore's
// writer protocol. An append changes retainedBytes; a capped prune changes the
// pending or final metadata even when the retained length returns to the cap.
// Keeping raw metadata bytes also distinguishes stable malformed metadata from
// a concurrent change without classifying errors by their text or position.
type outputSnapshotObservation struct {
	outputObserved  bool
	outputExists    bool
	retainedBytes   int64
	outputInfo      os.FileInfo
	pendingObserved bool
	pendingExists   bool
	pending         string
	metaObserved    bool
	metaExists      bool
	meta            string
}

// ReadOutputSnapshot reads a stable head or tail window without opening the
// output or its metadata for writing. A concurrent change is retried once
// immediately; the read never waits for more output or job completion.
func ReadOutputSnapshot(path string, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	return readOutputSnapshotFs(afero.NewOsFs(), path, maxBytes, fromHead)
}

func readOutputSnapshotFs(fs afero.Fs, path string, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	if maxBytes < 0 {
		return OutputSnapshot{}, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}
	return readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		return readOutputSnapshotOnce(fs, path, maxBytes, fromHead)
	})
}

func readOutputSnapshotWithRetry(read func() (OutputSnapshot, error)) (OutputSnapshot, error) {
	snapshot, err := read()
	if !errors.Is(err, errOutputChanged) {
		return snapshot, err
	}
	snapshot, err = read()
	if errors.Is(err, errOutputChanged) {
		return OutputSnapshot{}, ErrOutputChangedDuringRead
	}
	return snapshot, err
}

// ReadOutputWindowSnapshot reads a stable raw forward range without opening
// the output or its metadata for writing. A concurrent append or retention
// prune is retried once immediately.
func ReadOutputWindowSnapshot(path string, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputWindowSnapshotFs(afero.NewOsFs(), path, offset, maxBytes)
}

func readOutputWindowSnapshotFs(fs afero.Fs, path string, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	if maxBytes < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}
	if offset < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: offset=%d", ErrInvalidOffset, offset)
	}
	return readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
		return readOutputWindowSnapshotOnce(fs, path, offset, maxBytes)
	})
}

func readOutputWindowSnapshotWithRetry(read func() (OutputWindowSnapshot, error)) (OutputWindowSnapshot, error) {
	snapshot, err := read()
	if !errors.Is(err, errOutputChanged) {
		return snapshot, err
	}
	snapshot, err = read()
	if errors.Is(err, errOutputChanged) {
		return OutputWindowSnapshot{}, ErrOutputChangedDuringRead
	}
	return snapshot, err
}

func readOutputWindowSnapshotOnce(fs afero.Fs, path string, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	before, err := observeOutputSnapshot(fs, path)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	if !before.outputExists {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output window snapshot %s: %w", path, os.ErrNotExist)
	}

	snapshot, readErr := readOutputWindowSnapshotAttempt(fs, path, before.retainedBytes, offset, maxBytes)
	after, observeErr := observeOutputSnapshot(fs, path)
	if errors.Is(readErr, errOutputChanged) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if after.changedFrom(before) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if observeErr != nil {
		return OutputWindowSnapshot{}, observeErr
	}
	if readErr != nil {
		return snapshot, readErr
	}
	return snapshot, nil
}

func readOutputMetaForSnapshot(fs afero.Fs, path string, outputPath string, retained int64) (total int64, retainedStart int64, retainedStartPartial bool, err error) {
	total, retainedStart, retainedStartPartial, err = readOutputMetaForFile(fs, path, outputPath, retained)
	if errors.Is(err, errOutputPendingHandoff) {
		err = errOutputChanged
	}
	return total, retainedStart, retainedStartPartial, err
}

func readOutputWindowSnapshotAttempt(fs afero.Fs, path string, retainedBytes int64, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	totalBytes, retainedStart, retainedStartPartial, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	snapshot := OutputWindowSnapshot{
		Start:                offset,
		End:                  offset,
		TotalBytes:           totalBytes,
		RetainedStart:        retainedStart,
		RetainedStartPartial: retainedStartPartial,
	}
	if offset < retainedStart {
		return snapshot, fmt.Errorf("%w: offset=%d first_available=%d", ErrOutputPruned, offset, retainedStart)
	}
	if offset > totalBytes {
		return snapshot, fmt.Errorf("%w: offset=%d total=%d", ErrInvalidOffset, offset, totalBytes)
	}
	if totalBytes-retainedStart != retainedBytes {
		return OutputWindowSnapshot{}, errOutputChanged
	}

	end := addWindowLimit(offset, maxBytes, totalBytes)
	content, err := readOutputRawSnapshotWindow(fs, path, offset-retainedStart, end-offset)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	snapshot.Content = content
	snapshot.End = end
	snapshot.Truncated = retainedStart > 0 || offset > retainedStart || end < totalBytes

	afterInfo, err := fs.Stat(path)
	if err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output window snapshot: %w", err)
	}
	afterTotal, afterRetainedStart, afterRetainedStartPartial, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || afterTotal != totalBytes || afterRetainedStart != retainedStart || afterRetainedStartPartial != retainedStartPartial {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	return snapshot, nil
}

func readOutputRawSnapshotWindow(fs afero.Fs, path string, fileOffset int64, size int64) (content []byte, err error) {
	f, err := fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output window snapshot: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output window snapshot: %w", closeErr)
		}
	}()
	if fileOffset > 0 {
		if _, err := f.Seek(fileOffset, io.SeekStart); err != nil {
			return nil, fmt.Errorf("jobstore: seek output window snapshot: %w", err)
		}
	}
	content = make([]byte, int(size))
	if len(content) > 0 {
		if _, err := io.ReadFull(f, content); err != nil {
			return nil, fmt.Errorf("jobstore: read output window snapshot: %w", err)
		}
	}
	return content, nil
}

func readOutputSnapshotOnce(fs afero.Fs, path string, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	before, err := observeOutputSnapshot(fs, path)
	if err != nil {
		return OutputSnapshot{}, err
	}
	if !before.outputExists {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot %s: %w", path, os.ErrNotExist)
	}

	snapshot, readErr := readOutputSnapshotAttempt(fs, path, before.retainedBytes, maxBytes, fromHead)
	after, observeErr := observeOutputSnapshot(fs, path)
	if errors.Is(readErr, errOutputChanged) {
		return OutputSnapshot{}, errOutputChanged
	}
	if after.changedFrom(before) {
		return OutputSnapshot{}, errOutputChanged
	}
	if observeErr != nil {
		return OutputSnapshot{}, observeErr
	}
	if readErr != nil {
		return OutputSnapshot{}, readErr
	}
	return snapshot, nil
}

func readOutputSnapshotAttempt(fs afero.Fs, path string, retainedBytes int64, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	totalBytes, retainedStart, retainedStartPartial, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputSnapshot{}, err
	}

	content, err := readOutputSnapshotWindow(fs, path, retainedBytes, maxBytes, fromHead)
	if err != nil {
		return OutputSnapshot{}, err
	}

	afterInfo, err := fs.Stat(path)
	if err != nil {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	afterTotal, afterRetainedStart, afterRetainedStartPartial, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || afterTotal != totalBytes || afterRetainedStart != retainedStart || afterRetainedStartPartial != retainedStartPartial {
		return OutputSnapshot{}, errOutputChanged
	}
	return OutputSnapshot{
		Content:              content,
		TotalBytes:           totalBytes,
		RetainedStart:        retainedStart,
		RetainedStartPartial: retainedStartPartial,
		Truncated:            retainedStart > 0 || int64(maxBytes) < retainedBytes,
	}, nil
}

func observeOutputSnapshot(fs afero.Fs, path string) (outputSnapshotObservation, error) {
	var observation outputSnapshotObservation
	info, err := fs.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		observation.outputObserved = true
		return observation, nil
	}
	if err != nil {
		return observation, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	observation.outputObserved = true
	observation.outputExists = true
	observation.retainedBytes = info.Size()
	observation.outputInfo = info

	metaPath := outputMetaPath(path)
	// OutputStore publishes pending metadata before rewriting a capped file and
	// removes it only after publishing final metadata. Reading pending first
	// prevents one observation from combining the old final bytes with a
	// post-handoff missing pending file and aliasing the old stable state.
	observation.pending, observation.pendingExists, err = readOutputSnapshotMetadata(fs, outputPendingMetaPath(metaPath))
	if err != nil {
		return observation, err
	}
	observation.pendingObserved = true
	observation.meta, observation.metaExists, err = readOutputSnapshotMetadata(fs, metaPath)
	if err != nil {
		return observation, err
	}
	observation.metaObserved = true
	return observation, nil
}

func (after outputSnapshotObservation) changedFrom(before outputSnapshotObservation) bool {
	// A prune can return to the same retained size while the reader still holds
	// a hash of the expanded file. Metadata publication need not advance during
	// that interval, so also fence the file generation and in-place appends.
	if before.outputInfo != nil && after.outputInfo != nil {
		if !before.outputInfo.ModTime().Equal(after.outputInfo.ModTime()) {
			return true
		}
		if before.outputInfo.Sys() != nil && after.outputInfo.Sys() != nil && !os.SameFile(before.outputInfo, after.outputInfo) {
			return true
		}
	}

	if after.outputObserved && (after.outputExists != before.outputExists || after.retainedBytes != before.retainedBytes) {
		return true
	}
	if after.pendingObserved && (after.pendingExists != before.pendingExists || after.pending != before.pending) {
		return true
	}
	return after.metaObserved && (after.metaExists != before.metaExists || after.meta != before.meta)
}

func readOutputSnapshotMetadata(fs afero.Fs, path string) (string, bool, error) {
	b, err := afero.ReadFile(fs, path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("jobstore: observe output metadata: %w", err)
	}
	return string(b), true, nil
}

func readOutputSnapshotWindow(fs afero.Fs, path string, retainedBytes int64, maxBytes int, fromHead bool) (content []byte, err error) {
	windowBytes := min(retainedBytes, int64(maxBytes))
	start := int64(0)
	if !fromHead {
		start = retainedBytes - windowBytes
	}

	f, err := fs.Open(path)
	if err != nil {
		return nil, fmt.Errorf("jobstore: open output snapshot: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("jobstore: close output snapshot: %w", closeErr)
		}
	}()
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, fmt.Errorf("jobstore: seek output snapshot: %w", err)
		}
	}
	content = make([]byte, int(windowBytes))
	if len(content) > 0 {
		if _, err := io.ReadFull(f, content); err != nil {
			return nil, fmt.Errorf("jobstore: read output snapshot: %w", err)
		}
	}
	if fromHead && windowBytes < retainedBytes {
		content = runetrim.TrimTrailingPartial(content)
	}
	if !fromHead && start > 0 {
		content = runetrim.TrimLeadingPartial(content)
	}
	return content, nil
}
