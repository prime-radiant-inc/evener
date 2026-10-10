package jobstore

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// ErrOutputChangedDuringRead is returned when an append or retention prune
// prevents a consistent output snapshot. Path readers retry once internally;
// descriptor readers return it after one attempt so their caller can reopen.
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

// OutputWindowSnapshot is a stable raw range over retained job
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

// ReadOutputPageSnapshot selects and reads a stable raw latest or backward
// page. A concurrent append or retention prune is retried once immediately.
func ReadOutputPageSnapshot(path string, beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputPageSnapshotFs(afero.NewOsFs(), path, beforeBytes, maxBytes)
}

func readOutputPageSnapshotFs(fs afero.Fs, path string, beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputWindowSnapshotWithRetry(func() (OutputWindowSnapshot, error) {
		return readOutputPageSnapshotOnce(fs, path, beforeBytes, maxBytes)
	})
}

type outputRangeSelector func(totalBytes, retainedStart int64) (start, end int64, err error)

func readOutputPageSnapshotOnce(fs afero.Fs, path string, beforeBytes *int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputRangeSnapshotOnce(fs, path, func(totalBytes, retainedStart int64) (int64, int64, error) {
		return outputPageBounds(beforeBytes, maxBytes, totalBytes, retainedStart)
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

// KEEP IN SYNC with the descriptor-backed attempt protocol in
// output_snapshot_fd.go. These implementations intentionally remain separate:
// frozen path-reader seams require afero path access, while descriptor reads
// additionally fence path/file generations and give observation errors
// precedence over a partially observed change.
func readOutputWindowSnapshotOnce(fs afero.Fs, path string, offset int64, maxBytes int) (OutputWindowSnapshot, error) {
	return readOutputRangeSnapshotOnce(fs, path, func(totalBytes, retainedStart int64) (int64, int64, error) {
		return outputWindowBounds(offset, maxBytes, totalBytes, retainedStart)
	})
}

func readOutputRangeSnapshotOnce(fs afero.Fs, path string, selectRange outputRangeSelector) (OutputWindowSnapshot, error) {
	before, err := observeOutputSnapshot(fs, path)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	if !before.outputExists {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output window snapshot %s: %w", path, os.ErrNotExist)
	}

	snapshot, readErr := readOutputRangeSnapshotAttempt(fs, path, before.retainedBytes, selectRange)
	after, observeErr := observeOutputSnapshot(fs, path)
	if errors.Is(readErr, errOutputChanged) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if observeErr != nil {
		return OutputWindowSnapshot{}, observeErr
	}
	if after.changedFrom(before) {
		return OutputWindowSnapshot{}, errOutputChanged
	}
	if readErr != nil {
		return snapshot, readErr
	}
	return snapshot, nil
}

func readOutputMetaForSnapshot(fs afero.Fs, path string, outputPath string, retained int64) (outputView, error) {
	view, err := readOutputViewForFile(fs, path, outputPath, retained)
	if errors.Is(err, errOutputPendingHandoff) {
		err = errOutputChanged
	}
	return view, err
}

// outputSnapshotWindow places a head or tail snapshot of at most maxBytes over
// the visible bytes [visibleAt, retainedBytes) of the file.
func outputSnapshotWindow(retainedBytes, visibleAt int64, maxBytes int, fromHead bool) (start, size int64) {
	// A visible start the file does not reach yet (metadata moved on between a
	// reader's stat and its metadata read) leaves an empty window; the reader's
	// after-read check then sees the change.
	visibleAt = min(max(visibleAt, 0), retainedBytes)
	size = min(retainedBytes-visibleAt, int64(maxBytes))
	start = visibleAt
	if !fromHead {
		start = retainedBytes - size
	}
	return start, size
}

// trimOutputSnapshotWindow realigns only the window's own cuts: a head cut
// short of the end can end mid-rune, and a tail cut past the visible start can
// begin mid-rune. The output's own first and last bytes are kept as they are.
func trimOutputSnapshotWindow(content []byte, retainedBytes, visibleAt, start int64, fromHead bool) []byte {
	if fromHead && start+int64(len(content)) < retainedBytes {
		return runetrim.TrimTrailingPartial(content)
	}
	if !fromHead && start > visibleAt {
		return runetrim.TrimLeadingPartial(content)
	}
	return content
}

func readOutputRangeSnapshotAttempt(fs afero.Fs, path string, retainedBytes int64, selectRange outputRangeSelector) (OutputWindowSnapshot, error) {
	view, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	offset, end, rangeErr := selectRange(view.total, view.visibleStart)
	snapshot := OutputWindowSnapshot{
		Start:                offset,
		End:                  end,
		TotalBytes:           view.total,
		RetainedStart:        view.visibleStart,
		RetainedStartPartial: view.visiblePartial,
	}
	if rangeErr != nil {
		return snapshot, rangeErr
	}
	if view.total-view.fileStart != retainedBytes {
		return OutputWindowSnapshot{}, errOutputChanged
	}

	content, err := readOutputRawSnapshotWindow(fs, path, offset-view.fileStart, end-offset)
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	snapshot.Content = content
	snapshot.End = end
	snapshot.Truncated = view.visibleStart > 0 || offset > view.visibleStart || end < view.total

	afterInfo, err := fs.Stat(path)
	if err != nil {
		return OutputWindowSnapshot{}, fmt.Errorf("jobstore: stat output window snapshot: %w", err)
	}
	after, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputWindowSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || after != view {
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

// KEEP IN SYNC with the descriptor-backed attempt protocol in
// output_snapshot_fd.go. See the window-reader cross-reference above for why
// the frozen path and descriptor implementations cannot share an accessor.
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
	if observeErr != nil {
		return OutputSnapshot{}, observeErr
	}
	if after.changedFrom(before) {
		return OutputSnapshot{}, errOutputChanged
	}
	if readErr != nil {
		return OutputSnapshot{}, readErr
	}
	return snapshot, nil
}

func readOutputSnapshotAttempt(fs afero.Fs, path string, retainedBytes int64, maxBytes int, fromHead bool) (OutputSnapshot, error) {
	view, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, retainedBytes)
	if err != nil {
		return OutputSnapshot{}, err
	}
	content, err := readOutputSnapshotWindow(fs, path, retainedBytes, view.visibleOffset(), maxBytes, fromHead)
	if err != nil {
		return OutputSnapshot{}, err
	}

	afterInfo, err := fs.Stat(path)
	if err != nil {
		return OutputSnapshot{}, fmt.Errorf("jobstore: stat output snapshot: %w", err)
	}
	after, err := readOutputMetaForSnapshot(fs, outputMetaPath(path), path, afterInfo.Size())
	if err != nil {
		return OutputSnapshot{}, err
	}
	if afterInfo.Size() != retainedBytes || after != view {
		return OutputSnapshot{}, errOutputChanged
	}
	return OutputSnapshot{
		Content:              content,
		TotalBytes:           view.total,
		RetainedStart:        view.visibleStart,
		RetainedStartPartial: view.visiblePartial,
		Truncated:            view.visibleStart > 0 || int64(maxBytes) < retainedBytes-view.visibleOffset(),
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

func readOutputSnapshotWindow(fs afero.Fs, path string, retainedBytes, visibleAt int64, maxBytes int, fromHead bool) (content []byte, err error) {
	start, windowBytes := outputSnapshotWindow(retainedBytes, visibleAt, maxBytes, fromHead)

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
	return trimOutputSnapshotWindow(content, retainedBytes, visibleAt, start, fromHead), nil
}
