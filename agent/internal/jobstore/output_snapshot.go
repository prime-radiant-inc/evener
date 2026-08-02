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
	beforeInfo, err := fs.Stat(path)
	if err != nil {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	retainedBytes := beforeInfo.Size()
	totalBytes, retainedStart, err := readOutputMetaForFile(fs, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputSnapshot{}, err
	}

	content, err := readOutputSnapshotWindow(fs, path, retainedBytes, maxBytes, fromHead)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return OutputSnapshot{}, errOutputChanged
	}
	if err != nil {
		return OutputSnapshot{}, err
	}

	afterInfo, err := fs.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return OutputSnapshot{}, errOutputChanged
	}
	if err != nil {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	afterTotal, afterRetainedStart, err := readOutputMetaForFile(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputSnapshot{}, errOutputChanged
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
