package jobstore

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// ReadOutputSnapshotFromFile reads a stable head or tail window from an
// already-open output file. The caller owns f and must close it. path is used
// to locate the output metadata sidecars; output bytes and file observations
// are read through f. A concurrent change returns ErrOutputChangedDuringRead;
// callers that retry must reopen the output first.
func ReadOutputSnapshotFromFile(path string, f *os.File, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	if f == nil {
		return OutputSnapshot{}, errors.New("jobstore: output file is nil")
	}
	if maxBytes < 0 {
		return OutputSnapshot{}, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}
	fs := afero.NewOsFs()
	snapshot, err := readOutputSnapshotFromFileOnce(fs, path, f, maxBytes, fromHead)
	if errors.Is(err, errOutputChanged) {
		return OutputSnapshot{}, ErrOutputChangedDuringRead
	}
	return snapshot, err
}

// ReadOutputWindowSnapshotFromFile reads a stable raw forward range from an
// already-open output file. The caller owns f and must close it. path is used
// to locate the output metadata sidecars; output bytes and file observations
// are read through f. A concurrent change returns ErrOutputChangedDuringRead;
// callers that retry must reopen the output first.
func ReadOutputWindowSnapshotFromFile(path string, f *os.File, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	if f == nil {
		return OutputWindowSnapshot{}, errors.New("jobstore: output file is nil")
	}
	if maxBytes < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: maxBytes=%d", ErrInvalidLimit, maxBytes)
	}
	if offset < 0 {
		return OutputWindowSnapshot{}, fmt.Errorf("%w: offset=%d", ErrInvalidOffset, offset)
	}
	fs := afero.NewOsFs()
	snapshot, err := readOutputWindowSnapshotFromFileOnce(fs, path, f, offset, maxBytes)
	if errors.Is(err, errOutputChanged) {
		return OutputWindowSnapshot{}, ErrOutputChangedDuringRead
	}
	return snapshot, err
}

// ReadOutputPageSnapshotFromFile selects and reads a raw page from f. The
// caller owns f, and must reopen it before retrying a concurrent change.
func ReadOutputPageSnapshotFromFile(path string, f *os.File, beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	if f == nil {
		return OutputWindowSnapshot{}, errors.New("jobstore: output file is nil")
	}
	snapshot, err := readOutputPageSnapshotFromFileOnce(afero.NewOsFs(), path, f, beforeBytes, maxBytes)
	if errors.Is(err, errOutputChanged) {
		return OutputWindowSnapshot{}, ErrOutputChangedDuringRead
	}
	return snapshot, err
}

func readOutputPageSnapshotFromFileOnce(fs afero.Fs, path string, f *os.File, beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputRangeSnapshotFromFileOnce(fs, path, f, func(totalBytes, retainedStart int64) (int64, int64, error) {
		return outputPageBounds(beforeBytes, maxBytes, totalBytes, retainedStart)
	})
}

// KEEP IN SYNC with the path-backed attempt protocol in output_snapshot.go.
// These implementations intentionally remain separate: frozen path-reader
// seams require afero path access, while descriptor reads must fence path/file
// generations and give observation errors precedence over partial changes.
func readOutputSnapshotFromFileOnce(fs afero.Fs, path string, f *os.File, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	before, err := observeOutputSnapshotFromFile(fs, path, f)
	if err != nil {
		return OutputSnapshot{}, err
	}

	snapshot, readErr := readOutputSnapshotFromFileAttempt(fs, path, f, before.retainedBytes, maxBytes, fromHead)
	after, observeErr := observeOutputSnapshotFromFile(fs, path, f)
	if observeErr != nil {
		return OutputSnapshot{}, observeErr
	}
	if errors.Is(readErr, errOutputChanged) {
		return OutputSnapshot{}, errOutputChanged
	}
	if after.changedFrom(before) {
		return OutputSnapshot{}, errOutputChanged
	}
	if readErr != nil {
		return OutputSnapshot{}, readErr
	}
	return snapshot, nil
}

func readOutputWindowSnapshotFromFileOnce(fs afero.Fs, path string, f *os.File, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputRangeSnapshotFromFileOnce(fs, path, f, func(totalBytes, retainedStart int64) (int64, int64, error) {
		return outputWindowBounds(offset, maxBytes, totalBytes, retainedStart)
	})
}

func readOutputRangeSnapshotFromFileOnce(fs afero.Fs, path string, f *os.File, selectRange outputRangeSelector) (OutputWindowSnapshot, error) {
	before, err := observeOutputSnapshotFromFile(fs, path, f)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}

	snapshot, readErr := readOutputRangeSnapshotFromFileAttempt(fs, path, f, before.retainedBytes, selectRange)
	after, observeErr := observeOutputSnapshotFromFile(fs, path, f)
	if observeErr != nil {
		return OutputWindowSnapshot{}, observeErr
	}
	if errors.Is(readErr, errOutputChanged) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if after.changedFrom(before) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if err := checkOutputFileGeneration(path, f); err != nil {
		return OutputWindowSnapshot{}, err
	}
	if readErr != nil {
		return snapshot, readErr
	}
	return snapshot, nil
}

func readOutputSnapshotFromFileAttempt(fs afero.Fs, path string, f *os.File, retainedBytes int64, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	if err := checkOutputFileGeneration(path, f); err != nil {
		return OutputSnapshot{}, err
	}
	fileFS := newOutputSnapshotFileFS(fs, path, f)
	totalBytes, retainedStart, retainedStartPartial, err := readOutputMetaForSnapshot(fileFS, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputSnapshot{}, err
	}

	content, err := readOutputSnapshotWindowFromFile(f, retainedBytes, maxBytes, fromHead)
	if err != nil {
		return OutputSnapshot{}, err
	}

	afterInfo, err := f.Stat()
	if err != nil {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	afterTotal, afterRetainedStart, afterRetainedStartPartial, err := readOutputMetaForSnapshot(fileFS, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || afterTotal != totalBytes || afterRetainedStart != retainedStart || afterRetainedStartPartial != retainedStartPartial {
		return OutputSnapshot{}, errOutputChanged
	}
	if err := checkOutputFileGeneration(path, f); err != nil {
		return OutputSnapshot{}, err
	}
	return OutputSnapshot{
		Content:              content,
		TotalBytes:           totalBytes,
		RetainedStart:        retainedStart,
		RetainedStartPartial: retainedStartPartial,
		Truncated:            retainedStart > 0 || int64(maxBytes) < retainedBytes,
	}, nil
}

func readOutputRangeSnapshotFromFileAttempt(fs afero.Fs, path string, f *os.File, retainedBytes int64, selectRange outputRangeSelector) (OutputWindowSnapshot, error) {
	if err := checkOutputFileGeneration(path, f); err != nil {
		return OutputWindowSnapshot{}, err
	}
	fileFS := newOutputSnapshotFileFS(fs, path, f)
	totalBytes, retainedStart, retainedStartPartial, err := readOutputMetaForSnapshot(fileFS, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	offset, end, rangeErr := selectRange(totalBytes, retainedStart)
	snapshot := OutputWindowSnapshot{
		Start:                offset,
		End:                  end,
		TotalBytes:           totalBytes,
		RetainedStart:        retainedStart,
		RetainedStartPartial: retainedStartPartial,
	}
	if rangeErr != nil {
		return snapshot, rangeErr
	}
	if totalBytes-retainedStart != retainedBytes {
		return OutputWindowSnapshot{}, errOutputChanged
	}

	content, err := readOutputRawSnapshotWindowFromFile(f, offset-retainedStart, end-offset)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	snapshot.Content = content
	snapshot.End = end
	snapshot.Truncated = retainedStart > 0 || offset > retainedStart || end < totalBytes

	afterInfo, err := f.Stat()
	if err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output window snapshot: %w", err)
	}
	afterTotal, afterRetainedStart, afterRetainedStartPartial, err := readOutputMetaForSnapshot(fileFS, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || afterTotal != totalBytes || afterRetainedStart != retainedStart || afterRetainedStartPartial != retainedStartPartial {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if err := checkOutputFileGeneration(path, f); err != nil {
		return OutputWindowSnapshot{}, err
	}
	return snapshot, nil
}

func checkOutputFileGeneration(path string, f *os.File) error {
	pathInfo, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errOutputChanged
	}
	if err != nil {
		return fmt.Errorf("jobstore: stat output snapshot path: %w", err)
	}
	fileInfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("jobstore: stat output snapshot descriptor: %w", err)
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return errOutputChanged
	}
	return nil
}

func observeOutputSnapshotFromFile(fs afero.Fs, path string, f *os.File) (outputSnapshotObservation, error) {
	return observeOutputSnapshot(newOutputSnapshotFileFS(fs, path, f), path)
}

func readOutputSnapshotWindowFromFile(f *os.File, retainedBytes int64, maxBytes int, fromHead bool) ([]byte, error) {
	windowBytes := min(retainedBytes, int64(maxBytes))
	start := int64(0)
	if !fromHead {
		start = retainedBytes - windowBytes
	}
	content, err := readOutputRawSnapshotWindowFromFile(f, start, windowBytes)
	if err != nil {
		return nil, fmt.Errorf("jobstore: read output snapshot: %w", err)
	}
	if fromHead && windowBytes < retainedBytes {
		content = runetrim.TrimTrailingPartial(content)
	}
	if !fromHead && start > 0 {
		content = runetrim.TrimLeadingPartial(content)
	}
	return content, nil
}

func readOutputRawSnapshotWindowFromFile(f *os.File, fileOffset int64, size int64) ([]byte, error) {
	if _, err := f.Seek(fileOffset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("jobstore: seek output window snapshot: %w", err)
	}
	content := make([]byte, int(size))
	if len(content) > 0 {
		if _, err := io.ReadFull(f, content); err != nil {
			return nil, fmt.Errorf("jobstore: read output window snapshot: %w", err)
		}
	}
	return content, nil
}

// outputSnapshotFileFS lets the unchanged metadata validation helpers inspect
// the already-open output descriptor while continuing to read sidecars from the
// underlying filesystem. Its borrowed output handle deliberately ignores Close;
// ownership remains with the caller of the exported FromFile API.
type outputSnapshotFileFS struct {
	afero.Fs
	path string
	file *os.File
}

func newOutputSnapshotFileFS(fs afero.Fs, path string, f *os.File) afero.Fs {
	return &outputSnapshotFileFS{Fs: fs, path: path, file: f}
}

func (fs *outputSnapshotFileFS) Open(name string) (afero.File, error) {
	if name != fs.path {
		return fs.Fs.Open(name)
	}
	if _, err := fs.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return borrowedOutputSnapshotFile{File: fs.file}, nil
}

func (fs *outputSnapshotFileFS) Stat(name string) (os.FileInfo, error) {
	if name == fs.path {
		return fs.file.Stat()
	}
	return fs.Fs.Stat(name)
}

type borrowedOutputSnapshotFile struct {
	*os.File
}

func (borrowedOutputSnapshotFile) Close() error { return nil }
