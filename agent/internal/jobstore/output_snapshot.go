package jobstore

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/runetrim"
)

// ErrOutputChangedDuringRead is returned when an append or retention prune
// prevents a consistent output snapshot. Readers return it after one attempt
// so their caller can reopen the output and retry.
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
