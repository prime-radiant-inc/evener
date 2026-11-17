package jobstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// A sidecar written by an orderly Close names the retained window the writer
// finished with. Past that window the log holds bytes the writer never accounted
// for, and a crash that loses the metadata rename leaves the same sidecar beside
// a log it grew with a torn suffix. Nothing distinguishes those bytes from a torn
// page, so recovery reports the window the sidecar hashed and never the suffix.
//
// The sidecar is written as raw JSON rather than an outputMeta literal so this
// test states the on-disk shape itself: it is the format that carries the claim,
// and the shape has to be readable by a build that has no field for it.
func TestOutputCleanCloseMarkerRejectsASuffixPastIt(t *testing.T) {
	vouched := []byte("0123456789")
	logBytes := append(append([]byte{}, vouched...), []byte("tornb")...)
	sidecar := `{"total_bytes":10,"retained_start":0,"retained_sha256":"` +
		outputBytesSHA256(vouched) + `","clean_close_length":10}`
	path := rawOutputSidecarFile(t, logBytes, sidecar)

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		// A lost tail must never fail restore: the session has to open.
		t.Fatalf("OutputFileStats with a marked clean close and a torn suffix: %v", err)
	}
	if total != int64(len(vouched)) || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: the clean-close window, not the torn suffix",
			total, visibleStart, len(vouched))
	}

	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot: %v", err)
	}
	if !bytes.Equal(page.Content, vouched) {
		t.Fatalf("page content = %q, want %q: the torn suffix is not output", page.Content, vouched)
	}

	// A writable reopen must agree and must not carry the suffix forward.
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("writable reopen: %v", err)
	}
	if got := store.Len(); got != int64(len(vouched)) {
		t.Fatalf("reopened Len = %d, want %d", got, len(vouched))
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !bytes.Equal(got, vouched) {
		t.Fatalf("log = %q, want %q: the unaccounted suffix is not kept", got, vouched)
	}
}

// rawOutputSidecarFile writes a log and the exact sidecar bytes beside it.
func rawOutputSidecarFile(t *testing.T, logBytes []byte, sidecar string) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "job.log")
	if err := os.WriteFile(path, logBytes, 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.WriteFile(outputMetaPath(path), []byte(sidecar+"\n"), 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	return path
}

// A sidecar with neither a durable anchor nor a clean-close marker predates both
// contracts. It makes no claim about where the log ends, so a longer log keeps
// the bytes appended to it; dropping them here would lose output that was written
// before the upgrade.
func TestOutputUnmarkedSidecarStillAdoptsAWholeLog(t *testing.T) {
	vouched := []byte("0123456789")
	logBytes := append(append([]byte{}, vouched...), []byte("hello\n")...)
	path := tornSuffixFile(t, logBytes, outputMeta{
		TotalBytes:     int64(len(vouched)),
		RetainedStart:  0,
		RetainedSHA256: outputBytesSHA256(vouched),
	})

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats for an unmarked sidecar: %v", err)
	}
	if total != int64(len(logBytes)) || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: an unmarked sidecar adopts the whole log",
			total, visibleStart, len(logBytes))
	}
	page, err := readOutputPageSnapshotAtPath(path, nil, 1<<20)
	if err != nil {
		t.Fatalf("page snapshot: %v", err)
	}
	if !bytes.Equal(page.Content, logBytes) {
		t.Fatalf("page content = %q, want %q", page.Content, logBytes)
	}
}

// An orderly close stamps the marker, and only there: the sidecar an unclosed
// store writes makes no claim about where the log ends.
func TestOutputCleanCloseMarkerIsWrittenOnlyOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := OpenOutput(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Append([]byte("output\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if meta := readSidecarForTest(t, path); meta.CleanCloseLength != nil {
		t.Fatalf("a running store's sidecar claims a clean close at %d; only a finish may claim one",
			*meta.CleanCloseLength)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	meta := readSidecarForTest(t, path)
	if meta.CleanCloseLength == nil {
		t.Fatal("an orderly close left no clean-close marker")
	}
	if got := *meta.CleanCloseLength; got != meta.TotalBytes-meta.RetainedStart {
		t.Fatalf("clean-close marker = %d, want the retained window %d", got, meta.TotalBytes-meta.RetainedStart)
	}
}

// The claim a close makes has to outlive a reopen. A store that reopened and
// appended without restating the boundary would let a crash adopt everything the
// reopen wrote, so the marker is carried forward on every sidecar the store
// writes. The store here does not sync, so it writes no durable anchor and the
// marker is the only thing vouching for the boundary.
func TestOutputCleanCloseMarkerSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.log")
	store, err := openOutputFsNoSync(afero.NewOsFs(), path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Append([]byte("vouched\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	closeSidecar, err := os.ReadFile(outputMetaPath(path))
	if err != nil {
		t.Fatalf("read close sidecar: %v", err)
	}

	reopened, err := openOutputFsNoSync(afero.NewOsFs(), path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := reopened.Append([]byte("after reopen\n")); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if meta := readSidecarForTest(t, path); meta.CleanCloseLength == nil {
		t.Fatal("a reopened store's sidecar lost the clean-close claim")
	}

	// The crash: the close-time sidecar survives beside a log that carries torn
	// bytes past what the close vouched for.
	torn := append(append([]byte{}, []byte("vouched\nafter reopen\n")...), []byte("tornb")...)
	if err := os.WriteFile(path, torn, 0o644); err != nil {
		t.Fatalf("write torn log: %v", err)
	}
	if err := os.WriteFile(outputMetaPath(path), closeSidecar, 0o644); err != nil {
		t.Fatalf("restore close-time sidecar: %v", err)
	}

	total, visibleStart, err := OutputFileStats(path)
	if err != nil {
		t.Fatalf("OutputFileStats after a crash past a clean close: %v", err)
	}
	if total != int64(len("vouched\n")) || visibleStart != 0 {
		t.Fatalf("OutputFileStats = %d/%d, want %d/0: the window the clean close vouched for",
			total, visibleStart, len("vouched\n"))
	}
}

func readSidecarForTest(t *testing.T, path string) outputMeta {
	t.Helper()
	b, err := afero.ReadFile(afero.NewOsFs(), outputMetaPath(path))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	var meta outputMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatalf("parse sidecar: %v", err)
	}
	return meta
}
