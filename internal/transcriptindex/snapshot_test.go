package transcriptindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestSnapshotCandidateAtReturnsExactVersionedItem(t *testing.T) {
	path, lines := writeHeaderOnly(t, fixture{lines: []fixtureLine{
		entryLine(user("fetch-source")),
		entryLine(assistant(text("fetch-target"))),
	}})
	for _, line := range lines {
		appendBytes(t, path, line)
	}
	x := openIndex(t, path, t.TempDir())
	defer x.Close()
	latest, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	want := latest.Candidates[0]
	if err := x.ReadAt(latest.Length, func(snapshot *Snapshot) error {
		got, err := snapshot.CandidateAt(want.Position)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("candidate = %+v, want %+v", got, want)
		}
		_, err = snapshot.CandidateAt(appwire.ThreadItemPosition{Entry: want.Position.Entry + 100})
		var wireErr appwire.WireError
		if !errors.As(err, &wireErr) || wireErr.Data == nil {
			t.Fatalf("missing candidate error = %T %v, want typed stale cursor", err, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadAtKeepsInterruptedExecutionOnOlderPage(t *testing.T) {
	t.Parallel()
	fx := fixture{lines: []fixtureLine{
		entryLine(opens("old-execution", schema.TurnSpanExecution, user("old-source-201"))),
		entryLine(inTurn("old-execution", assistant(text("old-reply-202")))),
		entryLine(opens("new-execution", schema.TurnSpanExecution, user("new-source-203"))),
		entryLine(inTurn("new-execution", assistant(text("new-reply-204")))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	for _, line := range lines {
		appendBytes(t, path, line)
	}
	x, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	base, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, encodeEntry(t, 5, completion("old-execution", schema.TurnInterrupted, 10, 200)))
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}
	expected, err := x.ChangedSince(base.Length)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Turns) != 1 || expected.Turns[0].ID != "old-execution" || expected.Turns[0].Status != appwire.TurnStatusInterrupted {
		t.Fatalf("fixture did not interrupt the older execution: %+v", expected.Turns)
	}
	appendBytes(t, path, encodeEntry(t, 6, completion("old-execution", schema.TurnCompleted, 11, 300)))
	other, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.CatchUp(); err != nil {
		t.Fatal(err)
	}
	if err := x.ReadAt(expected.Length, func(s *Snapshot) error {
		latest, err := s.Latest(1)
		if err != nil {
			return err
		}
		if len(latest.Candidates) != 1 || latest.Candidates[0].Turn.ID != "new-execution" {
			t.Fatal("older source unexpectedly on latest page")
		}
		got, err := s.ChangedSince(base.Length)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("lost older-page interruption at cut: got %+v want %+v", got, expected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadAtReusesOneHistoricalView(t *testing.T) {
	t.Parallel()
	path, _ := writeHeaderOnly(t, fixture{})
	appendBytes(t, path, encodeEntry(t, 1, user("first-cut-221")))
	x, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	first, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, encodeEntry(t, 2, user("second-cut-222")))
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}
	var view *Index
	for i := range 2 {
		if err := x.ReadAt(first.Length, func(s *Snapshot) error {
			if i == 0 {
				view = s.index
			} else if s.index != view {
				t.Fatal("same published boundary rebuilt its private records")
			}
			_, err := s.Latest(1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.RebuildAtLeast(first.Length); err != nil {
		t.Fatal(err)
	}
	if err := x.ReadAt(first.Length, func(s *Snapshot) error {
		if s.index == view || s.index.meta.Incarnation == view.meta.Incarnation {
			t.Fatal("new incarnation reused stale private records")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := x.ReadAt(x.meta.Length, func(*Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if x.cutView != nil {
		t.Fatal("ordinary read retained historical prefix")
	}
}

// readRecorder wraps a handle's transcript so a test can see every region a
// read actually touches — the whole reconstruction path, whatever reads it.
type readRecorder struct {
	real transcriptFile
	note func(off, n int64)
}

func (r readRecorder) ReadAt(p []byte, off int64) (int, error) {
	r.note(off, int64(len(p)))
	return r.real.ReadAt(p, off)
}

func (r readRecorder) Stat() (os.FileInfo, error) { return r.real.Stat() }

func (r readRecorder) Close() error { return r.real.Close() }

// assertReadsAbove fails when a recorded region starts below floor, allowing
// the documented byte-sized boundary probe at probe: a reconstruction reads
// only from the recorded contributing regions, and one that replayed the
// covered prefix would read from the transcript's beginning.
func assertReadsAbove(t *testing.T, floor, probe int64, what string, reads []int64) {
	t.Helper()
	if len(reads) == 0 {
		t.Fatal("the reconstruction read no transcript bytes")
	}
	for i := 0; i+1 < len(reads); i += 2 {
		off, n := reads[i], reads[i+1]
		if off >= floor || (off == probe && n == 1) {
			continue
		}
		t.Fatalf("reconstruction read a region starting at %d, before %s %d (regions %v)", off, what, floor, reads)
	}
}

// A historical read reconstructs from the recorded contributing regions — the
// nearest committed cut and the spans of the turns the suffix stamped — and
// never from the covered prefix: the recorder fails the read at the first
// transcript region it sees below the cut.
func TestReadAtReconstructionScansNoCoveredPrefix(t *testing.T) {
	path, _ := writeHeaderOnly(t, fixture{})
	for i := range 400 {
		appendBytes(t, path, encodeEntry(t, i+1, user(fmt.Sprintf("prefix-entry-%d-%s", i, strings.Repeat("x", 64)))))
	}
	x := openIndex(t, path, DirFor(path))
	expected, err := x.Latest(3)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := x.ChangedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, encodeEntry(t, 401, user("unpublished-tail-301")))
	other, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.CatchUp(); err != nil {
		t.Fatal(err)
	}
	// Take up the other handle's build first, so the recorder below sees
	// only what the cut read itself reads.
	if _, err := x.Latest(1); err != nil {
		t.Fatal(err)
	}
	var reads []int64
	raw := x.transcript
	x.transcript = readRecorder{raw, func(off, n int64) { reads = append(reads, off, n) }}
	defer func() { x.transcript = raw }()
	var served int
	if err := x.ReadAt(expected.Length, func(s *Snapshot) error {
		served = len(reads) // the view is built; every later read serves it
		latest, err := s.Latest(3)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, expected) {
			t.Fatalf("cut latest mismatch:\ngot  %+v\nwant %+v", latest, expected)
		}
		changed, err := s.ChangedSince(0)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(changed, changes) {
			t.Fatalf("cut changes mismatch:\ngot  %+v\nwant %+v", changed, changes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertReadsAbove(t, expected.Length, expected.Length-1, "the published cut", reads[:served])
}

// A suffix-stamped turn is folded again from its own span, across the turns
// that interleave with it, so the fold starts at the turn's recorded first
// entry: the recorder fails the read at the first transcript region it sees
// below that entry.
func TestReadAtReconstructionFoldsOnlyTheStampedTurnsSpan(t *testing.T) {
	fx := fixture{lines: []fixtureLine{
		entryLine(opens("old-execution", schema.TurnSpanExecution, user("old-source-311"))),
		entryLine(inTurn("old-execution", assistant(call("old-call-312", "read_file", "{}")))),
		entryLine(opens("new-execution", schema.TurnSpanExecution, user("new-source-313"))),
		entryLine(inTurn("new-execution", assistant(text("new-reply-314")))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	for _, line := range lines {
		appendBytes(t, path, line)
	}
	x, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	base, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	slot, found, err := x.findTurn(context.Background(), "old-execution")
	if err != nil || !found {
		t.Fatalf("fixture did not open the older execution: %v", err)
	}
	buf, err := x.turns.read(slot, 1)
	if err != nil {
		t.Fatal(err)
	}
	firstOffset := decodeTurn(buf).FirstOffset
	if firstOffset == 0 {
		t.Fatal("fixture did not open the older execution")
	}
	appendBytes(t, path, encodeEntry(t, 5, completion("old-execution", schema.TurnInterrupted, 10, 200)))
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}
	expected, err := x.ChangedSince(base.Length)
	if err != nil {
		t.Fatal(err)
	}
	if len(expected.Turns) != 1 || expected.Turns[0].ID != "old-execution" || expected.Turns[0].Status != appwire.TurnStatusInterrupted {
		t.Fatalf("fixture did not interrupt the older execution: %+v", expected.Turns)
	}
	appendBytes(t, path, encodeEntry(t, 6, completion("old-execution", schema.TurnCompleted, 11, 300)))
	other, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.CatchUp(); err != nil {
		t.Fatal(err)
	}
	// Take up the other handle's build first, so the recorder below sees
	// only what the cut read itself reads.
	if _, err := x.Latest(1); err != nil {
		t.Fatal(err)
	}
	var reads []int64
	raw := x.transcript
	x.transcript = readRecorder{raw, func(off, n int64) { reads = append(reads, off, n) }}
	defer func() { x.transcript = raw }()
	var served int
	if err := x.ReadAt(expected.Length, func(s *Snapshot) error {
		served = len(reads) // the view is built; every later read serves it
		latest, err := s.Latest(1)
		if err != nil {
			return err
		}
		if len(latest.Candidates) != 1 || latest.Candidates[0].Turn.ID != "new-execution" {
			t.Fatal("older source unexpectedly on latest page")
		}
		changed, err := s.ChangedSince(base.Length)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(changed, expected) {
			t.Fatalf("lost older-page interruption at cut:\ngot  %+v\nwant %+v", changed, expected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertReadsAbove(t, firstOffset, expected.Length-1, "the stamped turn's first entry", reads[:served])
}

// A cached cut view borrows the strings handle of the build it was derived
// from, so dropping that build must drop the view too: a later read at the
// same length would otherwise serve strings reads through a closed handle.
func TestCloseBuildDropsTheCachedCutView(t *testing.T) {
	path, _ := writeHeaderOnly(t, fixture{})
	for i := range 10 {
		appendBytes(t, path, encodeEntry(t, i+1, user(fmt.Sprintf("entry-%d", i))))
	}
	x, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	published, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, encodeEntry(t, 11, user("unpublished-tail")))
	other, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.CatchUp(); err != nil {
		t.Fatal(err)
	}
	if err := x.ReadAt(published.Length, func(*Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if x.cutView == nil {
		t.Fatal("the cut read did not cache a view")
	}
	if err := x.closeBuild(); err != nil {
		t.Fatal(err)
	}
	if x.cutView != nil {
		t.Fatal("closeBuild kept the cached cut view")
	}
}

func BenchmarkReadAtHistorical5000(b *testing.B) {
	path, _ := writeHeaderOnly(b, fixture{})
	for i := range 5000 {
		appendBytes(b, path, encodeEntry(b, i+1, user(fmt.Sprintf("entry-%d %s", i, strings.Repeat("x", 1024)))))
	}
	x, err := Open(path, DirFor(path))
	if err != nil {
		b.Fatal(err)
	}
	defer x.Close()
	window, err := x.Latest(40)
	if err != nil {
		b.Fatal(err)
	}
	appendBytes(b, path, encodeEntry(b, 5001, user("unpublished-raw-record")))
	other, err := Open(path, DirFor(path))
	if err != nil {
		b.Fatal(err)
	}
	defer other.Close()
	b.Logf("5000 entries, %d transcript bytes, latest window 40 items", window.Length)
	read := func() {
		if err := x.ReadAt(window.Length, func(s *Snapshot) error {
			_, err := s.Latest(40)
			return err
		}); err != nil {
			b.Fatal(err)
		}
	}
	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			x.cutView = nil
			read()
		}
	})
	read()
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			read()
		}
	})
}

// Each expected cut is observed before the suffix exists. Another independently
// opened handle then overwrites summaries and contributors in the same sidecar.
// Re-reading each cut must recover the exact earlier result without rewinding
// that handle or changing its incarnation.
func TestReadAtSeparateHandlePreservesEveryCut(t *testing.T) {
	t.Parallel()
	path, lines := writeHeaderOnly(t, everything())
	x, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	type cut struct {
		window  Window
		changes Changes
		before  Window
	}
	var cuts []cut
	for _, line := range lines {
		appendBytes(t, path, line)
		if err := x.CatchUp(); err != nil {
			t.Fatal(err)
		}
		w, err := x.Latest(3)
		if err != nil {
			t.Fatal(err)
		}
		changes, err := x.ChangedSince(0)
		if err != nil {
			t.Fatal(err)
		}
		c := cut{window: w, changes: changes}
		if len(w.Candidates) > 0 {
			c.before, err = x.Before(w.Candidates[0].Position, 3)
			if err != nil {
				t.Fatal(err)
			}
		}
		cuts = append(cuts, c)
	}
	other, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// A terminal update through the other handle makes even the final saved cut
	// historical, and exercises the separate handle's metadata adoption.
	appendBytes(t, path, encodeEntry(t, len(lines)+1, user("after-all-cuts")))
	if err := other.CatchUp(); err != nil {
		t.Fatal(err)
	}
	ahead, err := other.Latest(3)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cuts {
		err := x.ReadAt(c.window.Length, func(s *Snapshot) error {
			got, err := s.Latest(3)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(got, c.window) {
				t.Errorf("cut %d latest mismatch\ngot %+v\nwant %+v", i, got, c.window)
			}
			changed, err := s.ChangedSince(0)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(changed, c.changes) {
				t.Errorf("cut %d changes mismatch", i)
			}
			if len(got.Candidates) > 0 {
				before, err := s.Before(got.Candidates[0].Position, 3)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(before, c.before) {
					t.Errorf("cut %d before mismatch", i)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("cut %d: %v", i, err)
		}
	}
	after, err := other.Latest(3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, ahead) {
		t.Fatal("historical reads changed the shared sidecar")
	}
	w, changes, err := other.LatestSince(3, appwire.SnapshotIdentity{Incarnation: ahead.Incarnation, Length: cuts[len(cuts)-1].window.Length})
	if err != nil || changes == nil || w.Length != ahead.Length || changes.Length != w.Length {
		t.Fatalf("saved-history LatestSince: %+v %+v %v", w, changes, err)
	}
}
