package transcriptindex

import (
	"errors"
	"fmt"
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

// simulateItemKill installs testKillAfterItemUpdateLog the same way
// simulateKill installs testKillAfterRecordWrites.
func simulateItemKill(t *testing.T) {
	t.Helper()
	testKillAfterItemUpdateLog = func() error {
		return errors.New("killed between the update-log row and the item overwrite")
	}
	t.Cleanup(func() { testKillAfterItemUpdateLog = nil })
}

// simulateTurnKill installs testKillAtTurnUpdateLog the same way
// simulateItemKill installs testKillAfterItemUpdateLog.
func simulateTurnKill(t *testing.T) {
	t.Helper()
	testKillAtTurnUpdateLog = func() error {
		return errors.New("killed at the turn's update-log write")
	}
	t.Cleanup(func() { testKillAtTurnUpdateLog = nil })
}

// errKilledBetweenFlushes is the death the between-flushes seam simulates,
var errKilledBetweenFlushes = errors.New("killed between the string and record flushes")

// simulateBetweenFlushesKill installs testKillBetweenFlushes the same way
// simulateKill installs testKillAfterRecordWrites.
func simulateBetweenFlushesKill(t *testing.T) {
	t.Helper()
	testKillBetweenFlushes = func() error { return errKilledBetweenFlushes }
	t.Cleanup(func() { testKillBetweenFlushes = nil })
}

// TestIndexKilledBetweenFlushesRecoversReadably: a kill between writeMeta's
// two flush groups leaves exactly one of them durable. The recovery replays
// the interrupted entry only while the record on disk is older than the
// entry, so whichever group the killed extension flushed first decides
// whether the recovered index reads back what a never-interrupted reference
// holds -- the string a re-stamped turn record names must be durable before
// the record -- or commits a record the strings file cannot answer and
// serves corruption for the rest of the build's life.
func TestIndexKilledBetweenFlushesRecoversReadably(t *testing.T) {
	header := transcript.Header{SessionID: "kill_between_flushes", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	opening := schema.Turn{
		Kind: schema.TurnUserInput, Message: llm.User("question"),
		Format: schema.TurnFormatIdentity, TurnID: "turn_flush", TurnKind: schema.TurnSpanExecution,
	}
	modeling := schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{text("answer")}},
		Model:   "gpt-kill-model",
		Format:  schema.TurnFormatIdentity, TurnID: "turn_flush",
	}
	openingLine := encodeEntry(t, 1, opening)
	modelingLine := encodeEntry(t, 2, modeling)

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, encodeHeader(t, header), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	appendBytes(t, path, openingLine)
	x := openIndex(t, path, dir) // Open catches up to the opening: a committed turn with no model yet.

	appendBytes(t, path, modelingLine)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	full := info.Size()
	simulateBetweenFlushesKill(t)
	err = x.CatchUpTo(full)
	if !errors.Is(err, errKilledBetweenFlushes) {
		t.Fatalf("the extension failed early, before the flushes: %v", err)
	}
	testKillBetweenFlushes = nil

	// The next open (a fresh handle, the way a restart's would be) replays
	// the interrupted entry and must read back the reference.
	recovered := openIndex(t, path, dir)
	got := changedIgnoringIncarnation(t, recovered)
	reference := openIndex(t, path, filepath.Join(t.TempDir(), "reference"))
	want := changedIgnoringIncarnation(t, reference)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered index = %s, want the reference %s", dump(got), dump(want))
	}
}

// TestAdoptedWriteThroughTableWritesStringsThrough: a handle that adopts a
// build whose turn records outgrew the buffer limit writes its records
// straight to the file from then on, so the strings those records name must
// not sit in the handle's buffer: a record could land naming a string only
// the buffer holds, and a kill before any flush commits exactly that. The
// adopting handle must carry the strings blob into write-through with the
// records it adopts.
func TestAdoptedWriteThroughTableWritesStringsThrough(t *testing.T) {
	header := transcript.Header{SessionID: "kill_write_through", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	newTurn := func(i int) schema.Turn {
		return schema.Turn{
			Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("question %d", i)),
			Format: schema.TurnFormatIdentity, TurnID: fmt.Sprintf("turn_mix_%d", i), TurnKind: schema.TurnSpanExecution,
		}
	}
	modeling := schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{text("answer")}},
		Model:   "gpt-mix-model",
		Format:  schema.TurnFormatIdentity, TurnID: "turn_mix_1",
	}

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, encodeHeader(t, header), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := 1; i <= 12; i++ {
		appendBytes(t, path, encodeEntry(t, i, newTurn(i)))
	}
	openIndex(t, path, dir) // Builds twelve turns buffered, under the default limit.

	// A handle that opens now adopts a turn table past the shrunk limit: its
	// records write through, so the strings they name must not buffer.
	previous := recordBufferLimit
	recordBufferLimit = 12*turnRecordSize - 1
	t.Cleanup(func() { recordBufferLimit = previous })

	mixed := openIndex(t, path, dir)
	if mixed.turns.buf != nil {
		t.Fatal("the adopted turn table still buffers past the limit")
	}
	if mixed.strings.buf != nil {
		t.Fatal("the adopted handle still buffers strings while its records write through")
	}

	// The extension that follows re-stamps a committed turn in place with a
	// model: its record lands on the file the moment it is written, so the
	// string it names must be durable by then. A kill before any flush
	// commits whatever is durable, and the recovered index must read.
	appendBytes(t, path, encodeEntry(t, 13, modeling))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	simulateBetweenFlushesKill(t)
	err = mixed.CatchUpTo(info.Size())
	if !errors.Is(err, errKilledBetweenFlushes) {
		t.Fatalf("the extension failed early, before the flushes: %v", err)
	}
	testKillBetweenFlushes = nil

	recovered := openIndex(t, path, dir)
	got := changedIgnoringIncarnation(t, recovered)
	reference := openIndex(t, path, filepath.Join(t.TempDir(), "reference"))
	want := changedIgnoringIncarnation(t, reference)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered index = %s, want the reference %s", dump(got), dump(want))
	}
}

// TestTableLimitFlipStopsBufferingStrings: a table that outgrows the buffer
// limit mid-extension starts writing its records straight to the file, so
// the strings those records name must stop buffering the moment it flips,
// flushed first: the records already in its buffer would otherwise land
// naming strings the file never held. After the flip every string the
// records name is durable, and the strings blob writes through for the
// rest of the extension.
func TestTableLimitFlipStopsBufferingStrings(t *testing.T) {
	header := transcript.Header{SessionID: "kill_flip", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	opening := schema.Turn{
		Kind: schema.TurnUserInput, Message: llm.User("question"),
		Format: schema.TurnFormatIdentity, TurnID: "turn_flip", TurnKind: schema.TurnSpanExecution,
	}
	modeling := schema.Turn{
		Kind:    schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{text("answer")}},
		Model:   "gpt-flip-model",
		Format:  schema.TurnFormatIdentity, TurnID: "turn_flip",
	}
	newTurn := func(i int) schema.Turn {
		return schema.Turn{
			Kind: schema.TurnUserInput, Message: llm.User(fmt.Sprintf("question %d", i)),
			Format: schema.TurnFormatIdentity, TurnID: fmt.Sprintf("turn_flip_%d", i), TurnKind: schema.TurnSpanExecution,
		}
	}

	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, encodeHeader(t, header), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	appendBytes(t, path, encodeEntry(t, 1, opening))
	x := openIndex(t, path, dir) // One committed turn, buffered under the default limit.

	// The extension first re-stamps the committed turn with a model -- the
	// string goes to the strings buffer -- and then appends enough turns to
	// push the turn table past the shrunk limit, flipping it mid-extension.
	previous := recordBufferLimit
	recordBufferLimit = 3 * turnRecordSize
	t.Cleanup(func() { recordBufferLimit = previous })
	appendBytes(t, path, encodeEntry(t, 2, modeling))
	for i := 2; i <= 5; i++ {
		appendBytes(t, path, encodeEntry(t, i+1, newTurn(i)))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUpTo(info.Size()); err != nil {
		t.Fatal(err)
	}

	if x.strings.buf != nil {
		t.Fatal("the strings blob still buffers after a record table flipped past the limit")
	}
	got := changedIgnoringIncarnation(t, x)
	reference := openIndex(t, path, filepath.Join(t.TempDir(), "reference"))
	want := changedIgnoringIncarnation(t, reference)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index after the flip = %s, want the reference %s", dump(got), dump(want))
	}
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
