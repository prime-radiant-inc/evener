package transcriptindex

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/transcript"
)

// The resource budget for one tiny append-and-catch-up. Sixteen of these must
// together stay under appends*maxBytesPerAppend; the unchanged 1 MiB scan
// reader alone spends 16 MiB on its buffers, so the guard fails before the fix
// and passes after it.
const (
	appends           = 16
	maxBytesPerAppend = 512 << 10
)

// scanBufferHeader is the header shared by this file's fixtures.
func scanBufferHeader() transcript.Header {
	return transcript.Header{SessionID: "th_scan_buffer", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
}

// scanBufferFixture is sixteen tiny valid entry lines, enough for one
// append-and-catch-up per line without any large-record noise.
func scanBufferFixture() fixture {
	lines := make([]fixtureLine, 0, appends)
	for i := range appends {
		if i%2 == 0 {
			lines = append(lines, entryLine(user(fmt.Sprintf("question %d", i))))
		} else {
			lines = append(lines, entryLine(assistant(text(fmt.Sprintf("answer %d", i)))))
		}
	}
	return fixture{header: scanBufferHeader(), lines: lines}
}

// TestSmallAppendAllocationBudget is a resource regression for the
// append-and-catch-up path: a tiny append must not allocate a megabyte of
// scratch. It warms the index over the header so every measured catch-up is an
// extension scan, then sums runtime.MemStats.TotalAlloc across sixteen
// append-and-catch-up pairs. The measurement spans the whole path — the
// aggregate of the scan reader and every other per-catch-up allocation — not
// the reader alone; the reader is the dominant term the budget targets.
//
// The measurement runs before the preservation assertions, and the allocation
// budget is checked last, so a PRESERVATION failure on the unoptimized reader
// is reportable without the budget Fatalf aborting first.
func TestSmallAppendAllocationBudget(t *testing.T) {
	path, lines := writeHeaderOnly(t, scanBufferFixture())
	if len(lines) != appends {
		t.Fatalf("fixture has %d lines, want %d", len(lines), appends)
	}
	x := openIndex(t, path, t.TempDir())
	catchUp(t, x) // warm: the header alone is already committed

	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, line := range lines {
		appendBytes(t, path, line)
		catchUp(t, x)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocatedBytes := after.TotalAlloc - before.TotalAlloc

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if x.meta.Length != info.Size() {
		t.Fatalf("committed length = %d, want the whole file %d", x.meta.Length, info.Size())
	}
	if x.meta.Entries != uint64(len(lines)) {
		t.Fatalf("entry count = %d, want %d", x.meta.Entries, len(lines))
	}
	assertSampledWindows(t, x, path)

	if allocatedBytes >= appends*maxBytesPerAppend {
		t.Fatalf("allocated %d bytes for %d tiny appends", allocatedBytes, appends)
	}
}

// BenchmarkCatchUpSmallAppends times one append-and-catch-up. Encoding and
// appending happen with the timer stopped, so the reported cost and bytes are
// the catch-up scan's own: a buffer sized to the unread section must not charge
// a full megabyte for a tiny line. Payloads cover the 4 KiB floor and a
// mid-size record, plus a 128 KiB record that stays below the 1 MiB ceiling;
// the 2 MiB TestScanReaderSpanningRecord covers spanning the ceiling.
// -benchtime=100x bounds each run to 100 iterations and validates the final
// content.
func BenchmarkCatchUpSmallAppends(b *testing.B) {
	for _, size := range []int{64, 4 << 10, 128 << 10} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			path, _ := writeHeaderOnly(b, fixture{header: scanBufferHeader()})
			x := openIndex(b, path, filepath.Join(b.TempDir(), "index"))
			payload := strings.Repeat("x", size)
			b.ResetTimer()
			for i := range b.N {
				b.StopTimer()
				appendBytes(b, path, encodeEntry(b, i+1, assistant(text(payload))))
				b.StartTimer()
				catchUp(b, x)
			}
			b.StopTimer()
			info, err := os.Stat(path)
			if err != nil {
				b.Fatal(err)
			}
			if x.meta.Length != info.Size() {
				b.Fatalf("committed length = %d, want the whole file %d", x.meta.Length, info.Size())
			}
			if x.meta.Entries != uint64(b.N) {
				b.Fatalf("entry count = %d, want %d", x.meta.Entries, b.N)
			}
			assertSampledWindows(b, x, path)
		})
	}
}

// TestScanSmallReaderPreservesPartialTail: a catch-up over an incomplete final
// record commits only the complete lines before it; completing the record and
// catching up again yields the fixture exactly. The unread section is a partial
// line, so the scan reader's floor is what reads it.
func TestScanSmallReaderPreservesPartialTail(t *testing.T) {
	fx := namedResults()
	path, lines := writeHeaderOnly(t, fx)
	appendBytes(t, path, joinLines(lines[:len(lines)-1]))
	last := lines[len(lines)-1]
	appendBytes(t, path, last[:len(last)/2])
	x := openIndex(t, path, t.TempDir())
	withoutTail := referenceCandidates(t, path)
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "partial tail", window, withoutTail, len(withoutTail), 40)

	appendBytes(t, path, last[len(last)/2:])
	catchUp(t, x)
	if x.rebuilds != 1 {
		t.Fatalf("finishing the line rebuilt the index (%d builds)", x.rebuilds)
	}
	assertAllWindows(t, x, path)
}

// TestScanReaderSpanningRecord: a valid record larger than the reader's 1 MiB
// ceiling is assembled from fragments across buffer boundaries, and a small
// record appended after it is read too. Both survive a catch-up and a reopen
// that resumes from the sidecar.
func TestScanReaderSpanningRecord(t *testing.T) {
	fx := fixture{
		header: scanBufferHeader(),
		lines: []fixtureLine{
			entryLine(user("before the big record")),
			entryLine(assistant(text(strings.Repeat("y", 2<<20)))),
			entryLine(assistant(text("after the big record"))),
		},
	}
	path, lines := writeHeaderOnly(t, fx)
	if len(lines) != 3 {
		t.Fatalf("fixture has %d lines, want 3", len(lines))
	}
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines[:2]))
	catchUp(t, x)
	appendBytes(t, path, lines[2])
	catchUp(t, x)
	assertAllWindows(t, x, path)

	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openIndex(t, path, dir)
	if reopened.rebuilds != 0 {
		t.Fatalf("reopening rebuilt the index (%d builds)", reopened.rebuilds)
	}
	assertAllWindows(t, reopened, path)
}

// TestScanCatchUpToDoesNotConsumeFollowingEntry: a bounded catch-up covers only
// the complete lines up to its length, not the entry that starts at or after
// that bound. A full catch-up then covers the rest, and the whole transcript
// reads back with the fixture.
func TestScanCatchUpToDoesNotConsumeFollowingEntry(t *testing.T) {
	fx := scanBufferFixture()
	path, lines := writeHeaderOnly(t, fx)
	if len(lines) < 2 {
		t.Fatalf("fixture has %d lines, want at least 2", len(lines))
	}
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, joinLines(lines))
	firstEnd := int64(len(encodeHeader(t, fx.header)) + len(lines[0]))
	if err := x.CatchUpTo(firstEnd); err != nil {
		t.Fatal(err)
	}
	if x.meta.Entries != 1 {
		t.Fatalf("bounded catch-up covered %d entries, want 1", x.meta.Entries)
	}
	if x.meta.Length != firstEnd {
		t.Fatalf("bounded catch-up length = %d, want %d", x.meta.Length, firstEnd)
	}
	catchUp(t, x)
	if x.meta.Entries != uint64(len(lines)) {
		t.Fatalf("full catch-up covered %d entries, want %d", x.meta.Entries, len(lines))
	}
	assertAllWindows(t, x, path)
}
