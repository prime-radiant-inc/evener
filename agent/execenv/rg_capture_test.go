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

	whole := newRgCapture(300)
	if _, err := whole.Write(raw); err != nil {
		t.Fatal(err)
	}
	want := whole.view(dir, false)

	for _, chunk := range []int{1, 2, 3, 7, 17, 64, 333} {
		c := newRgCapture(300)
		for i := 0; i < len(raw); i += chunk {
			if _, err := c.Write(raw[i:min(i+chunk, len(raw))]); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.view(dir, false); !viewsEqual(got, want) {
			t.Fatalf("chunking by %d changed the retained bytes:\n got  = %+v\n want = %+v", chunk, got, want)
		}
	}
}

// viewsEqual compares two capture views, whose tail makes them incomparable
// with ==.
func viewsEqual(a, b rgCaptureView) bool {
	return a.head == b.head && a.omitted == b.omitted && slices.Equal(a.tail, b.tail)
}

// TestRgCaptureHoldsAtMostItsBound is the capture's accounting ceiling: the
// object never holds more than its head budget plus its tail window, whatever
// the stream does.
func TestRgCaptureHoldsAtMostItsBound(t *testing.T) {
	t.Parallel()
	const capBytes = 1024
	c := newRgCapture(capBytes)
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
	view := c.view("/base", false)
	retained := len(view.head)
	for _, slot := range view.tail {
		retained += len(slot)
	}
	if view.omitted != c.total-retained {
		t.Fatalf("the omitted count %d does not account for the %d stream bytes less %d retained", view.omitted, c.total, retained)
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
	c := newRgCapture(1 << 20)
	if _, err := c.Write(raw); err != nil {
		t.Fatal(err)
	}
	view := c.view(dir, false)
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
	c := newRgCapture(len(first) + 5)
	if _, err := c.Write([]byte(first + dir + "/b.txt\x001:need")); err != nil {
		t.Fatal(err)
	}
	if got := c.view(dir, false); got.head != first {
		t.Fatalf("the head kept a fragment of the record the cap cut: %q", got.head)
	}
	// A cut inside a pathname, in the files-only framing, whose records end at
	// their NUL rather than at a newline.
	files := []byte(dir + "/a.txt\x00" + dir + "/b.tx")
	cf := newRgCapture(len(dir) + len("/a.txt\x00") + 3)
	if _, err := cf.Write(files); err != nil {
		t.Fatal(err)
	}
	if got := cf.view(dir, true); got.head != dir+"/a.txt\x00" {
		t.Fatalf("a files-only head kept a pathname fragment: %q", got.head)
	}
	if strings.Contains(cf.view(dir, true).head, "b.tx") {
		t.Fatalf("a cut pathname fragment survived into the head: %q", cf.view(dir, true).head)
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
		whole := newRgCapture(capBytes)
		if _, err := whole.Write(stream); err != nil {
			t.Fatal(err)
		}
		view := whole.view("/base", false)
		if held := whole.heldBytes(); held > capBytes+rgTailWindowBytes {
			t.Fatalf("capture held %d bytes, past its %d-byte bound", held, capBytes+rgTailWindowBytes)
		}
		retained := len(view.head)
		for _, slot := range view.tail {
			retained += len(slot)
		}
		if view.omitted != whole.total-retained {
			t.Fatalf("omitted %d does not account for %d stream bytes less %d retained", view.omitted, whole.total, retained)
		}
		if view.omitted < 0 {
			t.Fatalf("negative omission %d", view.omitted)
		}
		// Chunking the same stream must not change what is kept.
		for _, chunk := range []int{1, 5, 64} {
			c := newRgCapture(capBytes)
			for i := 0; i < len(stream); i += chunk {
				if _, err := c.Write(stream[i:min(i+chunk, len(stream))]); err != nil {
					t.Fatal(err)
				}
			}
			if got := c.view("/base", false); !viewsEqual(got, view) {
				t.Fatalf("chunking by %d changed the view:\n got  = %+v\n want = %+v", chunk, got, view)
			}
		}
	})
}
