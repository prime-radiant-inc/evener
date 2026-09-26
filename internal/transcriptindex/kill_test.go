package transcriptindex

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// killWriter is a real transcript this test grows one turn at a time, so an
// index opened partway through has real, later content to extend into.
type killWriter struct {
	path string
	w    *transcript.Writer
}

func newKillWriter(t *testing.T) *killWriter {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kill.transcript.jsonl")
	w, err := transcript.NewWriterNoSync(path, transcript.Header{SessionID: "kill"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return &killWriter{path: path, w: w}
}

// record appends one turn and returns the length the file covers through it.
func (kw *killWriter) record(t *testing.T, text string) int64 {
	t.Helper()
	rec, err := kw.w.Record(schema.NewTurn(schema.TurnUserInput, llm.User(text)), transcript.RecordOptions{})
	if err != nil || !rec.Recorded {
		t.Fatalf("record %q: %+v, %v", text, rec, err)
	}
	return rec.Offset + rec.Length
}

// simulateKill installs testKillAfterRecordWrites for the caller's scope,
// guaranteed to be cleared even if the caller fails before reaching its own
// cleanup: the hook is a package-level seam shared across subtests.
func simulateKill(t *testing.T) {
	t.Helper()
	testKillAfterRecordWrites = func() error { return errors.New("killed before the header commit") }
	t.Cleanup(func() { testKillAfterRecordWrites = nil })
}

// changedIgnoringIncarnation is x's ChangedSince(0), with Incarnation zeroed
// so two builds of the same content compare equal regardless of which
// random incarnation each minted.
func changedIgnoringIncarnation(t *testing.T, x *Index) Changes {
	t.Helper()
	c, err := x.ChangedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	c.Incarnation = ""
	return c
}

// TestIndexExtensionKilledBeforeHeaderCommit simulates a process killed
// between an extension's or a rebuild's record writes and the meta commit
// that would make them readable (testKillAfterRecordWrites, this package's
// own test-only fault seam: the records are already on disk, but the build's
// header never learns to count them). Either way, the next open must redo
// consistently: an interrupted extension's leftover records are truncated
// away and rewritten, and an interrupted rebuild's orphaned half-built
// directory is harmless and eventually swept, so the recovered index equals
// a reference that never saw the interruption -- even with another handle
// (a process stand-in) holding the sidecar's shared lock throughout.
func TestIndexExtensionKilledBeforeHeaderCommit(t *testing.T) {
	t.Run("extension", func(t *testing.T) {
		kw := newKillWriter(t)
		first := kw.record(t, "one")
		dir := t.TempDir()
		x := openIndex(t, kw.path, dir) // Open catches up to "one": a real, uninterrupted build.
		if x.meta.Length != first {
			t.Fatalf("initial build covers %d, want %d", x.meta.Length, first)
		}

		// Another handle (a process stand-in) opens the same build and holds
		// it across the kill and the recovery: a shared-lock reader present
		// throughout must see the pre-kill state, then the recovered one.
		other := openIndex(t, kw.path, dir)
		beforeKill := changedIgnoringIncarnation(t, other)

		kw.record(t, "two")
		last := kw.record(t, "three")
		simulateKill(t)
		if err := x.CatchUpTo(last); err == nil {
			t.Fatal("the killed extension returned success")
		}
		testKillAfterRecordWrites = nil

		// other, opened before the kill, still reads the pre-kill state
		// correctly: the leftover records the killed extension wrote are
		// not yet counted by anything.
		if got := changedIgnoringIncarnation(t, other); !reflect.DeepEqual(got, beforeKill) {
			t.Fatalf("another handle's read across the kill = %s, want unchanged %s", dump(got), dump(beforeKill))
		}

		// The next open (a fresh handle, the way a restart's would be)
		// redoes the extension: the leftover records past the header's
		// counts are truncated before it re-scans and commits.
		recovered := openIndex(t, kw.path, dir)
		if err := recovered.CatchUpTo(last); err != nil {
			t.Fatal(err)
		}
		got := changedIgnoringIncarnation(t, recovered)

		reference := openIndex(t, kw.path, t.TempDir())
		if err := reference.CatchUpTo(last); err != nil {
			t.Fatal(err)
		}
		want := changedIgnoringIncarnation(t, reference)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("recovered index = %s, want the reference %s", dump(got), dump(want))
		}

		// other, still open throughout, now sees the recovered state too.
		if got := changedIgnoringIncarnation(t, other); !reflect.DeepEqual(got, want) {
			t.Fatalf("another handle's read after the recovery = %s, want %s", dump(got), dump(want))
		}
	})

	t.Run("rebuild", func(t *testing.T) {
		kw := newKillWriter(t)
		kw.record(t, "one")
		kw.record(t, "two")
		last := kw.record(t, "three")
		dir := t.TempDir()
		x := openIndex(t, kw.path, dir)
		before := changedIgnoringIncarnation(t, x)

		other := openIndex(t, kw.path, dir)

		// A killed rebuild: buildNew's own records land in a brand new build
		// directory that never becomes CURRENT (the kill lands before that
		// commit, and buildNew's own body has no cleanup -- only rebuild()'s
		// wrapper does, which this bypasses by calling buildNew directly, the
		// way a real kill would never run it either), so the orphan is left
		// exactly as a kill would leave it.
		simulateKill(t)
		if err := x.buildNew(last, ""); err == nil {
			t.Fatal("the killed rebuild returned success")
		}
		testKillAfterRecordWrites = nil
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) < 3 { // lock, CURRENT, the live build, and now the orphan
			t.Fatalf("sidecar dir = %v, want the orphaned build left behind", entries)
		}

		// The old build is still CURRENT and unaffected: a fresh open (and
		// the other handle open throughout) still reads the pre-kill state.
		unaffected := openIndex(t, kw.path, dir)
		if got := changedIgnoringIncarnation(t, unaffected); !reflect.DeepEqual(got, before) {
			t.Fatalf("read after the killed rebuild = %s, want the pre-kill state %s", dump(got), dump(before))
		}
		if got := changedIgnoringIncarnation(t, other); !reflect.DeepEqual(got, before) {
			t.Fatalf("another handle's read after the killed rebuild = %s, want %s", dump(got), dump(before))
		}

		// A later, real rebuild sweeps the orphan and leaves exactly one
		// build, matching a reference that was never interrupted.
		if err := unaffected.Rebuild(last); err != nil {
			t.Fatal(err)
		}
		entries, err = os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 3 { // lock, CURRENT, the one live build
			t.Fatalf("sidecar dir after a real rebuild = %v, want the orphan swept", entries)
		}
		got := changedIgnoringIncarnation(t, unaffected)
		reference := openIndex(t, kw.path, t.TempDir())
		if err := reference.CatchUpTo(last); err != nil {
			t.Fatal(err)
		}
		want := changedIgnoringIncarnation(t, reference)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("index after the sweep = %s, want the reference %s", dump(got), dump(want))
		}
	})
}
