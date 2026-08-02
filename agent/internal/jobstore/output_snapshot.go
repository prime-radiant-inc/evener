package jobstore

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/internal/runetrim"
)

// ErrOutputChangedDuringRead is returned when both immediate attempts to read
// a consistent output snapshot race with an append or retention prune.
var ErrOutputChangedDuringRead = errors.New("jobstore: output changed during read")

var errOutputChanged = errors.New("jobstore: output snapshot changed")

// OutputSnapshot is a point-in-time window over a job's retained output.
type OutputSnapshot struct {
	Content       []byte
	TotalBytes    int64
	RetainedStart int64
	Truncated     bool
}

// outputSnapshotObservation is the comparable state changed by OutputStore's
// writer protocol. An append changes retainedBytes; a capped prune changes the
// pending or final metadata even when the retained length returns to the cap.
// Keeping raw metadata bytes also distinguishes stable malformed metadata from
// a concurrent change without classifying errors by their text or position.
type outputSnapshotObservation struct {
	outputExists  bool
	retainedBytes int64
	metaExists    bool
	meta          string
	pendingExists bool
	pending       string
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
	if observeErr != nil {
		return OutputSnapshot{}, observeErr
	}
	if before != after {
		return OutputSnapshot{}, errOutputChanged
	}
	if readErr != nil {
		return OutputSnapshot{}, readErr
	}
	return snapshot, nil
}

func readOutputSnapshotAttempt(fs afero.Fs, path string, retainedBytes int64, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	totalBytes, retainedStart, err := readOutputMetaForFile(fs, outputMetaPath(path), path, retainedBytes)
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
	afterTotal, afterRetainedStart, err := readOutputMetaForFile(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || afterTotal != totalBytes || afterRetainedStart != retainedStart {
		return OutputSnapshot{}, errOutputChanged
	}

	return OutputSnapshot{
		Content:       content,
		TotalBytes:    totalBytes,
		RetainedStart: retainedStart,
		Truncated:     retainedStart > 0 || int64(maxBytes) < retainedBytes,
	}, nil
}

func observeOutputSnapshot(fs afero.Fs, path string) (outputSnapshotObservation, error) {
	var observation outputSnapshotObservation
	info, err := fs.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return observation, nil
	}
	if err != nil {
		return outputSnapshotObservation{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	observation.outputExists = true
	observation.retainedBytes = info.Size()

	observation.meta, observation.metaExists, err = readOutputSnapshotMetadata(fs, outputMetaPath(path))
	if err != nil {
		return outputSnapshotObservation{}, err
	}
	observation.pending, observation.pendingExists, err = readOutputSnapshotMetadata(fs, outputPendingMetaPath(outputMetaPath(path)))
	if err != nil {
		return outputSnapshotObservation{}, err
	}
	return observation, nil
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
