package execenv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/searchresult"
)

// rgStreamFixture writes an executable that prints exactly stdout, the fake
// ripgrep the capture tests drive.
func rgStreamFixture(t *testing.T, dir, name string, stdout []byte) string {
	t.Helper()
	rec := filepath.Join(dir, name+".stdout")
	if err := os.WriteFile(rec, stdout, 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, name)
	writeExecFixture(t, exe, "#!/bin/sh\nexec cat "+shellQuotePath(rec)+"\n")
	return exe
}

// rgFakeRgEnv is an environment whose rg is the given fixture executable.
func rgFakeRgEnv(t *testing.T, root, rg string) *LocalExecutionEnvironment {
	t.Helper()
	env := NewLocalExecutionEnvironment(root)
	t.Cleanup(env.Cleanup)
	env.lookPath = func(name string) (string, error) {
		if name != "rg" {
			return "", errors.New("unexpected lookup " + name)
		}
		return rg, nil
	}
	return env
}

// TestGrepRgCaptureBoundsASingleFileSearchAndKeepsTheTail is the round-8 HIGH's
// shape: a single-file content search whose output is far past the retention
// budget. The capture must bound the stream by construction, disclose the
// omission, and still show how the match stream ends — the model reads the
// first lines and the last records, never megabytes.
func TestGrepRgCaptureBoundsASingleFileSearchAndKeepsTheTail(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	stubGrepRetainedBytes(t, 512)
	root := t.TempDir()
	file := filepath.Join(root, "big.txt")
	if err := os.WriteFile(file, []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A single named file's content records carry no path from ripgrep.
	var stream bytes.Buffer
	const records = 400
	for i := 1; i <= records; i++ {
		fmt.Fprintf(&stream, "%d:%s\n", i, strings.Repeat("n", 40))
	}
	rg := rgStreamFixture(t, t.TempDir(), "rg", stream.Bytes())
	env := rgFakeRgEnv(t, root, rg)

	got, err := env.Grep(context.Background(), "n", file, "", false, 1_000_000, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	entries, notes := searchresult.Split(got)
	if len(entries) > 8*grepRetainedBytes {
		t.Fatalf("a single-file search retained %d bytes of entries under a %d-byte budget", len(entries), grepRetainedBytes)
	}
	if !strings.Contains(notes, "retained-output cap") {
		t.Fatalf("the bounded result did not disclose the byte truncation: %q", notes)
	}
	// The stream's last five records are the capture's tail: the model sees how
	// the match stream ends instead of reading a head-only cut.
	for i := records - 4; i <= records; i++ {
		if !strings.Contains(got, fmt.Sprintf("%d:%s", i, strings.Repeat("n", 40))) {
			t.Fatalf("the tail is missing record %d of the stream: %q", i, notes)
		}
	}
	if strings.Contains(got, fmt.Sprintf("%d:", records/2)) {
		t.Fatalf("a middle record survived the bound: %q", got)
	}
}

// TestGrepRgTruncatedSearchKeepsMaxResultsRows is the tool-contract pin: the
// grep tool promises at most max_results rows, so neither the omission marker
// nor the five tail records may become rows of their own — they ride in the
// result's note, after the blank-line separator, where the clients' entry count
// cannot see them.
func TestGrepRgTruncatedSearchKeepsMaxResultsRows(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	stubGrepRetainedBytes(t, 512)
	root := t.TempDir()
	const files = 200
	var stream bytes.Buffer
	for i := 1; i <= files; i++ {
		path := filepath.Join(root, fmt.Sprintf("f%03d.txt", i))
		stream.WriteString(path + "\x00" + "1:NEEDLE" + strings.Repeat("n", 40) + "\n")
	}
	rg := rgStreamFixture(t, t.TempDir(), "rg", stream.Bytes())
	env := rgFakeRgEnv(t, root, rg)

	const maxResults = 3
	got, err := env.Grep(context.Background(), "NEEDLE", root, "", false, maxResults, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if n := searchresult.EntryCount(got); n > maxResults {
		t.Fatalf("a truncated search returned %d rows, past its max_results of %d: %q", n, maxResults, got)
	}
	_, notes := searchresult.Split(got)
	for i := files - 4; i <= files; i++ {
		name := fmt.Sprintf("f%03d.txt", i)
		if !strings.Contains(notes, name) {
			t.Fatalf("the tail is missing record %d of the stream: %q", i, notes)
		}
	}
}

// TestRgCaptureIsChunkBoundaryInvariant: the capture is fed a stream, and the
// retained bytes must not depend on how the writes were chunked — a boundary
// inside a record must not change what is kept or what is counted.
func TestRgCaptureIsChunkBoundaryInvariant(t *testing.T) {
	t.Parallel()
	dir := "/base"
	var stream bytes.Buffer
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&stream, "%s/f%02d.txt\x001:needle%s\n", dir, i, strings.Repeat("x", 30))
	}
	// A pathname holding a newline, so a boundary inside it is in play too.
	stream.WriteString(dir + "/we\nird.txt\x002:needle\n")
	raw := stream.Bytes()

	whole := newRgCapture("/base", false, 300)
	if _, err := whole.Write(raw); err != nil {
		t.Fatal(err)
	}
	want := whole.view(false)

	for _, chunk := range []int{1, 2, 3, 7, 17, 64, 333} {
		c := newRgCapture("/base", false, 300)
		for i := 0; i < len(raw); i += chunk {
			if _, err := c.Write(raw[i:min(i+chunk, len(raw))]); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.view(false); !viewsEqual(got, want) {
			t.Fatalf("chunking by %d changed the retained bytes:\n got  = %+v\n want = %+v", chunk, got, want)
		}
	}
}

// viewsEqual compares two capture views, whose tail makes them incomparable
// with ==.
func viewsEqual(a, b rgCaptureView) bool {
	return a.head == b.head && a.retained == b.retained && a.omitted == b.omitted && slices.Equal(a.tail, b.tail)
}

// TestRgCaptureHoldsAtMostItsBound is the capture's accounting ceiling: the
// object never holds more than its head budget plus its tail window, whatever
// the stream does.
func TestRgCaptureHoldsAtMostItsBound(t *testing.T) {
	t.Parallel()
	const capBytes = 1024
	c := newRgCapture("/base", false, capBytes)
	bound := capBytes + rgTailWindowBytes
	chunk := bytes.Repeat([]byte("/base/f.txt\x001:needle\n"), 64)
	for i := range 4000 {
		if _, err := c.Write(chunk); err != nil {
			t.Fatal(err)
		}
		if held := c.heldBytes(); held > bound {
			t.Fatalf("after write %d the capture held %d bytes, past its %d-byte bound", i, held, bound)
		}
	}
	if !c.over {
		t.Fatal("a stream far past the cap did not trip it")
	}
	view := c.view(false)
	if view.omitted != c.total-view.retained {
		t.Fatalf("the omitted count %d does not account for the %d stream bytes less %d retained",
			view.omitted, c.total, view.retained)
	}
	if view.omitted <= 0 {
		t.Fatalf("a stream past the cap reported no omission: %+v", view)
	}
}

// TestRgCaptureDoesNotMarkAShortStream: a stream that fits whole is retained
// whole with no marker and no tail, so an ordinary search is unchanged.
func TestRgCaptureDoesNotMarkAShortStream(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	raw := []byte(dir + "/a.txt\x001:needle\n" + dir + "/b.txt\x002:needle\n")
	c := newRgCapture("/base", false, 1<<20)
	if _, err := c.Write(raw); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if view.head != string(raw) {
		t.Fatalf("a stream inside the cap was not retained whole: %q", view.head)
	}
	if len(view.tail) != 0 || view.omitted != 0 {
		t.Fatalf("a stream inside the cap grew a tail or a marker: %+v", view)
	}
}

// TestRgCaptureNeverPresentsACutRecordFragment: the cap falls wherever the
// stream happens to be when it trips, so the head almost always ends mid-record.
// A fragment must be dropped, never presented as a result — neither a
// half-written payload line nor a partial pathname, in either framing.
func TestRgCaptureNeverPresentsACutRecordFragment(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	first := dir + "/a.txt\x001:needle\n"
	// The cap falls inside the second record's payload line.
	c := newRgCapture("/base", false, len(first)+5)
	if _, err := c.Write([]byte(first + dir + "/b.txt\x001:need")); err != nil {
		t.Fatal(err)
	}
	if got := c.view(false); got.head != first {
		t.Fatalf("the head kept a fragment of the record the cap cut: %q", got.head)
	}
	// A cut inside a pathname, in the files-only framing, whose records end at
	// their NUL rather than at a newline.
	files := []byte(dir + "/a.txt\x00" + dir + "/b.tx")
	cf := newRgCapture("/base", true, len(dir)+len("/a.txt\x00")+3)
	if _, err := cf.Write(files); err != nil {
		t.Fatal(err)
	}
	if got := cf.view(true); got.head != dir+"/a.txt\x00" {
		t.Fatalf("a files-only head kept a pathname fragment: %q", got.head)
	}
	if strings.Contains(cf.view(true).head, "b.tx") {
		t.Fatalf("a cut pathname fragment survived into the head: %q", cf.view(true).head)
	}
}

// TestGrepRgFailedSearchDiagnosticIsBounded: a failed search shows its stdout
// beside its stderr, and that diagnostic is a bounded prefix — a firehose of
// bytes on a failing rg cannot reach the model whole.
func TestGrepRgFailedSearchDiagnosticIsBounded(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var flood bytes.Buffer
	for i := range 40_000 {
		fmt.Fprintf(&flood, "%d:%s\n", i, strings.Repeat("n", 40))
	}
	rec := filepath.Join(t.TempDir(), "flood.out")
	if err := os.WriteFile(rec, flood.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "rg")
	writeExecFixture(t, exe, "#!/bin/sh\ncat "+shellQuotePath(rec)+"\necho oops >&2\nexit 2\n")
	env := rgFakeRgEnv(t, root, exe)

	got, err := env.Grep(context.Background(), "NEEDLE", root, "", false, 100, "content")
	if err == nil {
		t.Fatalf("a failing rg reported success: %q", got)
	}
	if !strings.Contains(got, "oops") {
		t.Fatalf("the diagnostic dropped rg's stderr: %q", got)
	}
	if len(got) > rgCaptureDiagnosticBytes+len("\noops\n") {
		t.Fatalf("the diagnostic kept %d bytes of a failing rg's stdout, past its %d-byte bound",
			len(got), rgCaptureDiagnosticBytes)
	}
}

// TestGrepRgCancellationIsAnErrorNotAnEmptySuccess: a cancelled search reports
// its cancellation rather than returning an empty result that reads as "no
// matches".
func TestGrepRgCancellationIsAnErrorNotAnEmptySuccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	exe := filepath.Join(t.TempDir(), "rg")
	writeExecFixture(t, exe, "#!/bin/sh\nsleep 10\n")
	env := rgFakeRgEnv(t, root, exe)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	defer cancel()
	got, err := env.Grep(ctx, "NEEDLE", root, "", false, 100, "content")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled search = %q, %v, want context.Canceled", got, err)
	}
	if got != "" {
		t.Fatalf("a cancelled search returned a result: %q", got)
	}
}

// TestRgCaptureAccountsRetainedSourceBytes is MEDIUM 1: a capped slot carries
// the synthetic omission text, whose bytes are not stream bytes. Counting it as
// retained understates the omission, and with a tail record past the slot cap
// the count goes NEGATIVE, which suppresses the disclosure entirely.
func TestRgCaptureAccountsRetainedSourceBytes(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	short := func(name string) string { return dir + "/" + name + "\x001:n\n" }
	// Each record is one byte past the tail slot cap, so its slot is a whole
	// slot plus the omission text — more bytes than the record it stands for.
	capped := func(name string) string {
		head := dir + "/" + name + "\x001:needle"
		return head + strings.Repeat("n", rgTailSlotBytes+1-len(head)-1) + "\n"
	}
	first := short("a.txt")
	stream := []byte(first + short("b.txt"))
	for i := 1; i <= rgTailRecords; i++ {
		stream = append(stream, capped(fmt.Sprintf("c%d.txt", i))...)
	}
	// The cap holds exactly the first record, so the only stream bytes the view
	// does not retain are the short record the window's own fragment takes with
	// it: twenty-odd bytes, against five slots' worth of omission text.
	c := newRgCapture("/base", false, len(first))
	if _, err := c.Write(stream); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if view.omitted != c.total-view.retained {
		t.Fatalf("the omitted count %d does not account for the %d stream bytes less %d retained",
			view.omitted, c.total, view.retained)
	}
	if view.omitted <= 0 {
		t.Fatalf("a stream that dropped records reported %d omitted bytes (%d slots retained of source records)",
			view.omitted, len(view.tail))
	}
	if !strings.Contains(rgTailNote(view), "bytes omitted") {
		t.Fatalf("the disclosure note states no omission: %q", rgTailNote(view))
	}
}

// TestGrepRgDisclosesWhenTheTailRecordIsCapped is MEDIUM 1's user-visible half:
// GrepSkipping discloses only when the omitted count is positive, so the
// negative count above made a search that dropped records report itself as
// complete — the exact defect class this PR exists to close.
func TestGrepRgDisclosesWhenTheTailRecordIsCapped(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	root := t.TempDir()
	file := filepath.Join(root, "big.txt")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A single named file's records carry no path, so a record can be tiny. The
	// cap holds two bytes of the first one, and what it really drops — six bytes
	// of that record — is far less than the omission text the five capped slots
	// of the tail add.
	stubGrepRetainedBytes(t, 2)
	record := "1:x\n"
	huge := "1:" + strings.Repeat("n", rgTailSlotBytes-2) + "\n"
	stream := record + strings.Repeat(huge, rgTailRecords)
	env := rgFakeRgEnv(t, root, rgStreamFixture(t, t.TempDir(), "rg", []byte(stream)))

	got, err := env.Grep(context.Background(), "x", file, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(got, "retained-output cap") {
		t.Fatalf("a truncated search disclosed nothing: %q", got)
	}
	if !strings.Contains(got, "the stream's last 5 records") {
		t.Fatalf("the tail lost the capped records: %q", got)
	}
}

// TestRgCaptureTailResynchronizesInFilesOnlyMode is MEDIUM 2's first case: the
// window opens wherever the cap tripped, and a files-only record is a path plus
// its NUL with no newline. Walking from the window's first byte finds no
// delimiter at all, so every tail record the design promises was dropped.
func TestRgCaptureTailResynchronizesInFilesOnlyMode(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	var stream bytes.Buffer
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&stream, "%s/f%02d.txt\x00", dir, i)
	}
	// The cap lands inside the second pathname, so the window opens mid-path.
	c := newRgCapture("/base", true, len(dir)+len("/f01.txt\x00")+3)
	if _, err := c.Write(stream.Bytes()); err != nil {
		t.Fatal(err)
	}
	view := c.view(true)
	if len(view.tail) != rgTailRecords {
		t.Fatalf("a files-only window kept %d tail records, want %d: %q", len(view.tail), rgTailRecords, view.tail)
	}
	for _, slot := range view.tail {
		if !strings.HasPrefix(slot, dir+"/") || !strings.HasSuffix(slot, "\x00") {
			t.Fatalf("a pathname fragment was presented as a complete record: %q", slot)
		}
	}
	if last := view.tail[len(view.tail)-1]; last != dir+"/f40.txt\x00" {
		t.Fatalf("the tail does not end at the stream's last record: %q", last)
	}
}

// TestRgCaptureTailDropsANewlineBearingPathFragment is MEDIUM 2's second case: a
// window that opens inside a pathname holding a newline, where the embedded
// newline is not a record boundary. The fragment must not be presented as a
// record, and the complete trailing records must survive.
func TestRgCaptureTailDropsANewlineBearingPathFragment(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	var stream bytes.Buffer
	stream.WriteString(dir + "/we\nird.txt\x002:second\n")
	last := ""
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&stream, "%s/f%02d.txt\x001:first\n", dir, i)
		last = fmt.Sprintf("%s/f%02d.txt\x001:first\n", dir, i)
	}
	// The cap lands inside the newline-bearing pathname, BEFORE its newline, so
	// the window opens with bytes that do not start a path and the embedded
	// newline is the first one the walk sees.
	c := newRgCapture("/base", false, len(dir)+len("/we"))
	if _, err := c.Write(stream.Bytes()); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	for _, slot := range view.tail {
		if !strings.HasPrefix(slot, dir+"/") {
			t.Fatalf("a pathname fragment was presented as a complete record: %q", slot)
		}
	}
	if len(view.tail) == 0 || view.tail[len(view.tail)-1] != last {
		t.Fatalf("the complete trailing records did not survive: %q", view.tail)
	}
}

// TestRgCaptureKeepsTheRecordAfterACaptureEndingOnABoundary is round 11's
// finding: the resync dropped the window's first record unconditionally, so a cap
// that fell exactly at a record end lost a record the capture holds whole. The
// framing state now proves the window opens at a record start, so nothing is
// dropped.
func TestRgCaptureKeepsTheRecordAfterACaptureEndingOnABoundary(t *testing.T) {
	t.Parallel()
	c := newRgCapture("/base", false, len("1:a\n")) // the cap ends exactly at the first record's end
	if _, err := c.Write([]byte("1:a\n2:b\n")); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if len(view.tail) != 1 || view.tail[0] != "2:b\n" {
		t.Fatalf("a cap that ended at a record boundary lost the record after it: %q", view.tail)
	}
	if view.omitted != 0 {
		t.Fatalf("nothing was left out, but the view reports %d omitted bytes", view.omitted)
	}
	if whole := view.wholeStream(); whole != "1:a\n2:b\n" {
		t.Fatalf("the head and the tail are not the stream: %q", whole)
	}
}

// TestRgCaptureKeepsEveryRecordAfterACaptureEndingOnANulBoundary is the same
// property in the files_with_matches framing, whose records end at their NUL.
func TestRgCaptureKeepsEveryRecordAfterACaptureEndingOnANulBoundary(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	stream := []byte(dir + "/a.txt\x00" + dir + "/b.txt\x00" + dir + "/c.txt\x00")
	c := newRgCapture("/base", true, len(dir)+len("/a.txt\x00"))
	if _, err := c.Write(stream); err != nil {
		t.Fatal(err)
	}
	view := c.view(true)
	want := []string{dir + "/b.txt\x00", dir + "/c.txt\x00"}
	if !slices.Equal(view.tail, want) {
		t.Fatalf("a cap that ended at a NUL boundary lost the records after it:\n got  = %q\n want = %q", view.tail, want)
	}
	if view.omitted != 0 {
		t.Fatalf("nothing was left out, but the view reports %d omitted bytes", view.omitted)
	}
}

// TestGrepRgShowsTheRecordAfterACaptureEndingOnABoundary is that finding as the
// model sees it: the record after the cap is retained whole, so the result must
// carry it and must not disclose a truncation that did not happen.
func TestGrepRgShowsTheRecordAfterACaptureEndingOnABoundary(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	stubGrepRetainedBytes(t, len("1:a\n"))
	root := t.TempDir()
	file := filepath.Join(root, "big.txt")
	if err := os.WriteFile(file, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := rgFakeRgEnv(t, root, rgStreamFixture(t, t.TempDir(), "rg", []byte("1:a\n2:b\n")))

	got, err := env.Grep(context.Background(), "a", file, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if !strings.Contains(got, "2:b") {
		t.Fatalf("the record after a cap that ended on a boundary was lost: %q", got)
	}
	if strings.Contains(got, "retained-output cap") {
		t.Fatalf("a capture that left nothing out disclosed truncation: %q", got)
	}
}

// TestRgCaptureWholeStreamRefusesACappedSlot pins the other half of wholeStream:
// a slot cut to its bound is not the record's own bytes, so head and tail are NOT
// the stream and the caller must keep the tail in the note, where the cut is
// stated. Without the guard a capture that left nothing out would present the
// slot's synthetic omission text as part of a complete result.
func TestRgCaptureWholeStreamRefusesACappedSlot(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	first := dir + "/a.txt\x001:n\n"
	huge := dir + "/b.txt\x001:" + strings.Repeat("n", rgTailSlotBytes) + "\n"
	c := newRgCapture("/base", false, len(first)) // the cap ends exactly at the first record's end
	if _, err := c.Write([]byte(first + huge)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if len(view.tail) != 1 {
		t.Fatalf("tail = %q, want the one record after the cap", view.tail)
	}
	if view.omitted != 0 {
		t.Fatalf("the capture holds every byte, but reports %d omitted", view.omitted)
	}
	if whole := view.wholeStream(); whole != "" {
		t.Fatalf("a slot cut to its bound was presented as the stream: %q", whole)
	}
}

// TestRgCaptureDoesNotFabricateARecordFromANewlineBearingPath is round 17's
// finding: a pathname may hold a newline, and what follows that newline can look
// exactly like a record start, so content alone cannot tell a pathname tail from
// a payload tail. The legal pathname "/base/we\n/base/fake.txt" with the cap just
// before its embedded newline must never present "/base/fake.txt" as a record.
func TestRgCaptureDoesNotFabricateARecordFromANewlineBearingPath(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	// One record whose pathname holds a newline, then a real record after it.
	record := dir + "/we\n" + dir + "/fake.txt\x001:match\n"
	after := dir + "/real.txt\x002:match\n"
	c := newRgCapture("/base", false, len(dir)+len("/we")) // the cap ends just before the newline
	if _, err := c.Write([]byte(record + after)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	for _, slot := range view.tail {
		if strings.Contains(slot, "/fake.txt") {
			t.Fatalf("a pathname fragment was presented as a record: %q", slot)
		}
	}
	if len(view.tail) != 1 || view.tail[0] != after {
		t.Fatalf("the record after the cut pathname was lost:\n got  = %q\n want = %q", view.tail, []string{after})
	}
}

// TestRgCaptureResynchronizesAPayloadTailToTheNextRecord is MEDIUM 1 (round 15):
// a window that opens inside a match PAYLOAD ends at its own newline, which comes
// BEFORE the window's first NUL — that NUL belongs to the next record. The
// pathname-tail skip therefore runs past a complete record the capture is holding,
// and a payload cut followed by one record yields an empty tail.
func TestRgCaptureResynchronizesAPayloadTailToTheNextRecord(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	first := dir + "/a.txt\x001:" + strings.Repeat("n", 64) + "\n"
	second := dir + "/b.txt\x002:match\n"
	// The cap lands just inside the first record's payload line.
	c := newRgCapture("/base", false, len(dir)+len("/a.txt\x001:"))
	if _, err := c.Write([]byte(first + second)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if len(view.tail) != 1 || view.tail[0] != second {
		t.Fatalf("the record after a payload cut was lost:\n got  = %q\n want = %q", view.tail, []string{second})
	}
	if view.omitted <= 0 {
		t.Fatalf("a capture that dropped bytes reported %d omitted", view.omitted)
	}
}

// TestRgCaptureReportsACutSlotWithNothingOmitted is MEDIUM 1 (round 13): a
// boundary-ending cap whose tail holds an oversized record omits no bytes and
// cuts one slot to its bound. The view must say so, because the caller's
// disclosure guard reads exactly this state.
func TestRgCaptureReportsACutSlotWithNothingOmitted(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	short := dir + "/a.txt\x001:n\n"
	huge := dir + "/big.txt\x001:" + strings.Repeat("n", rgTailSlotBytes) + "\n"
	c := newRgCapture("/base", false, len(short)) // the cap ends exactly at the first record's end
	if _, err := c.Write([]byte(short + huge)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if view.omitted != 0 {
		t.Fatalf("the capture holds every byte, but reports %d omitted", view.omitted)
	}
	if view.cut != 1 {
		t.Fatalf("cut = %d, want the one record longer than its slot", view.cut)
	}
	if whole := view.wholeStream(); whole != "" {
		t.Fatalf("a cut slot was presented as the stream: %q", whole)
	}
}

// TestGrepRgDisclosesACutSlotWhenNothingWasOmitted is MEDIUM 1 as the model sees
// it: gating both notes on the omission alone lost the tail of a capture that cut
// a record at its slot bound and omitted nothing — silent truncation.
func TestGrepRgDisclosesACutSlotWhenNothingWasOmitted(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	root := t.TempDir()
	file := filepath.Join(root, "big.txt")
	if err := os.WriteFile(file, []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	short := "1:n\n"
	huge := "1:" + strings.Repeat("n", rgTailSlotBytes) + "\n"
	stubGrepRetainedBytes(t, len(short))
	env := rgFakeRgEnv(t, root, rgStreamFixture(t, t.TempDir(), "rg", []byte(short+huge)))

	got, err := env.Grep(context.Background(), "n", file, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	entries, notes := searchresult.Split(got)
	if !strings.Contains(notes, fmt.Sprintf("%d-byte slot", rgTailSlotBytes)) {
		t.Fatalf("a cut tail slot was not disclosed: entries=%q notes=%q", entries, notes)
	}
	if strings.Contains(notes, "later matches omitted") {
		t.Fatalf("nothing was omitted, but the note claims otherwise: %q", notes)
	}
	// The slot's own marker states the record it cut, which is honest; what must
	// not appear is a stream-level omission marker claiming zero bytes.
	if strings.Contains(notes, fmt.Sprintf(outputOmissionMarker, 0)) {
		t.Fatalf("nothing was omitted, but the note carries a zero-omission marker: %q", notes)
	}
}

// TestRgCaptureIgnoresABoundaryProofTheWindowSlideInvalidated is MEDIUM 2: the
// head proves the cap ended on a record boundary, but the rolling trim then moved
// the window's start INSIDE a record. Trusting the stale proof presents that
// record's payload suffix as a complete one, losing its path and line number.
func TestRgCaptureIgnoresABoundaryProofTheWindowSlideInvalidated(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	short := dir + "/a.txt\x001:n\n"
	// A final record larger than the whole tail window, so the window slides into
	// its payload and holds no record start at all.
	huge := dir + "/big.txt\x001:" + strings.Repeat("n", 2*rgTailWindowBytes) + "\n"
	c := newRgCapture("/base", false, len(short))
	if _, err := c.Write([]byte(short + huge)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	for _, slot := range view.tail {
		if !strings.HasPrefix(slot, dir+"/") {
			t.Fatalf("a payload suffix was presented as a complete record: %q", slot[:min(len(slot), 80)])
		}
	}
}

// TestRgCaptureCapsARecordWhoseMarkerMatchesItsDroppedBytes is MEDIUM 3: a record
// whose dropped part is exactly as wide as the marker that states it leaves the
// slot the same length as the record, so a length comparison cannot tell a cut
// slot from an intact one.
func TestRgCaptureCapsARecordWhoseMarkerMatchesItsDroppedBytes(t *testing.T) {
	t.Parallel()
	const dir = "/base"
	short := dir + "/a.txt\x001:n\n"
	head := dir + "/big.txt\x001:"
	// rgTailSlotBytes + 28: 28 is the width of "[... 28 bytes omitted ...]".
	exact := head + strings.Repeat("n", rgTailSlotBytes+28-len(head)-1) + "\n"
	if len(exact) != rgTailSlotBytes+28 {
		t.Fatalf("fixture record is %d bytes, want %d", len(exact), rgTailSlotBytes+28)
	}
	c := newRgCapture("/base", false, len(short))
	if _, err := c.Write([]byte(short + exact)); err != nil {
		t.Fatal(err)
	}
	view := c.view(false)
	if view.cut != 1 {
		t.Fatalf("cut = %d, want the record that is longer than its slot", view.cut)
	}
	if whole := view.wholeStream(); whole != "" {
		t.Fatalf("a cut slot was presented as the stream: %q", whole)
	}
}

// TestGrepRgKeepsACappedSlotMarkerOutOfTheRows is MEDIUM 3 as the model sees it:
// the slot's synthetic marker must never be parsed as a result row.
func TestGrepRgKeepsACappedSlotMarkerOutOfTheRows(t *testing.T) {
	// Not parallel: shrinks the package-wide byte budget.
	root := t.TempDir()
	file := filepath.Join(root, "big.txt")
	if err := os.WriteFile(file, []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	short := "1:n\n"
	head := "1:"
	exact := head + strings.Repeat("n", rgTailSlotBytes+28-len(head)-1) + "\n"
	stubGrepRetainedBytes(t, len(short))
	env := rgFakeRgEnv(t, root, rgStreamFixture(t, t.TempDir(), "rg", []byte(short+exact)))

	got, err := env.Grep(context.Background(), "n", file, "", false, 100, "content")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	entries, notes := searchresult.Split(got)
	if strings.Contains(entries, "bytes omitted") {
		t.Fatalf("a slot's marker text became a result row: %q", entries)
	}
	if !strings.Contains(notes, "bytes omitted") {
		t.Fatalf("the cut slot's own marker is missing from the note: %q", notes)
	}
}

// FuzzRgCapture drives the capture with arbitrary bytes: whatever the stream and
// the cap, the object stays inside its bound, its view accounts for every byte
// it did not keep, and chunking the writes cannot change the view.
func FuzzRgCapture(f *testing.F) {
	f.Add([]byte("/base/a.txt\x001:needle\n"), uint16(0))
	f.Add([]byte("/base/we\nird.txt\x002:needle\n/base/z.txt\x003:x\n"), uint16(7))
	f.Add([]byte("pathless\nlines\n"), uint16(3))
	f.Add(bytes.Repeat([]byte("/base/a.txt\x001:needle\n"), 200), uint16(64))

	f.Fuzz(func(t *testing.T, stream []byte, capSel uint16) {
		capBytes := 1 + int(capSel)%4096
		whole := newRgCapture("/base", false, capBytes)
		if _, err := whole.Write(stream); err != nil {
			t.Fatal(err)
		}
		view := whole.view(false)
		if held := whole.heldBytes(); held > capBytes+rgTailWindowBytes {
			t.Fatalf("capture held %d bytes, past its %d-byte bound", held, capBytes+rgTailWindowBytes)
		}
		if view.omitted != whole.total-view.retained {
			t.Fatalf("omitted %d does not account for %d stream bytes less %d retained",
				view.omitted, whole.total, view.retained)
		}
		if view.omitted < 0 {
			t.Fatalf("negative omission %d", view.omitted)
		}
		// Chunking the same stream must not change what is kept.
		for _, chunk := range []int{1, 5, 64} {
			c := newRgCapture("/base", false, capBytes)
			for i := 0; i < len(stream); i += chunk {
				if _, err := c.Write(stream[i:min(i+chunk, len(stream))]); err != nil {
					t.Fatal(err)
				}
			}
			if got := c.view(false); !viewsEqual(got, view) {
				t.Fatalf("chunking by %d changed the view:\n got  = %+v\n want = %+v", chunk, got, view)
			}
		}
	})
}
