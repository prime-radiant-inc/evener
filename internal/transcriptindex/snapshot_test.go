package transcriptindex

import (
	"errors"
	"fmt"
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
