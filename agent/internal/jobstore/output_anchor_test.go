package jobstore

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// A store that crashes before its first barrier has a genuine zero-length
// anchor, and the empty-file checksum vouches for no byte. Recovery must report
// an empty log rather than refuse: refusing aborts session restore, turning a
// tail the ruling allows us to lose into a dead session. Reporting the surviving
// bytes instead would present bytes nothing verified as output.
func TestOutputReportsAnEmptyLogForAZeroAnchor(t *testing.T) {
	base := afero.NewMemMapFs()
	const log = "/job.log"
	const meta = "/job.log.meta.json"
	if err := afero.WriteFile(base, log, bytes.Repeat([]byte("b"), 18), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputMetaFileFsSync(base, meta, outputMeta{
		TotalBytes:      40,
		RetainedSHA256:  outputBytesSHA256(bytes.Repeat([]byte("a"), 40)),
		DurableFileSize: new(int64(0)),
		DurableSHA256:   outputBytesSHA256(nil),
	}, false); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readValidOutputMetaFs(base, meta, log, 18)
	if err != nil || !ok {
		t.Fatalf("a zero anchor must not refuse the log (it aborts session restore): ok=%v err=%v", ok, err)
	}
	if got.TotalBytes != 0 {
		t.Fatalf("total = %d, want 0: nothing vouched for the 18 surviving bytes", got.TotalBytes)
	}
}

// A lost tail is reported up to the anchor, never past it: bytes beyond the
// anchor were never vouched for, and a torn page can leave arbitrary content
// there.
func TestOutputReportsOnlyTheAnchoredPrefixOfALostTail(t *testing.T) {
	base := afero.NewMemMapFs()
	const log = "/job.log"
	const meta = "/job.log.meta.json"
	vouched := bytes.Repeat([]byte("a"), 40)
	// The anchored 10 bytes survive, followed by 8 bytes nothing verified.
	surviving := append(append([]byte{}, vouched[:10]...), bytes.Repeat([]byte("\x00"), 8)...)
	if err := afero.WriteFile(base, log, surviving, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputMetaFileFsSync(base, meta, outputMeta{
		TotalBytes:      40,
		RetainedSHA256:  outputBytesSHA256(vouched),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(vouched[:10]),
	}, false); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readValidOutputMetaFs(base, meta, log, int64(len(surviving)))
	if err != nil || !ok {
		t.Fatalf("lost tail with a matching anchor: ok=%v err=%v", ok, err)
	}
	if got.TotalBytes != 10 {
		t.Fatalf("total = %d, want the anchored 10: the 8 surviving bytes were never vouched for", got.TotalBytes)
	}
}

// The tolerance still refuses a log that contradicts its anchor, which is what
// separates a lost tail from replaced output.
func TestOutputRejectsALostTailWhosePrefixDisagreesWithTheAnchor(t *testing.T) {
	base := afero.NewMemMapFs()
	const log = "/job.log"
	const meta = "/job.log.meta.json"
	vouched := bytes.Repeat([]byte("a"), 40)
	if err := afero.WriteFile(base, log, append(bytes.Repeat([]byte("z"), 10), bytes.Repeat([]byte("b"), 8)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOutputMetaFileFsSync(base, meta, outputMeta{
		TotalBytes:      40,
		RetainedSHA256:  outputBytesSHA256(vouched),
		DurableFileSize: new(int64(10)),
		DurableSHA256:   outputBytesSHA256(vouched[:10]),
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readValidOutputMetaFs(base, meta, log, 18); err == nil {
		t.Fatal("a log whose first 10 bytes contradict the anchor was accepted as a lost tail")
	}
}

var _ = filepath.Join
