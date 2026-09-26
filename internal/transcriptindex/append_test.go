package transcriptindex

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func appendBytes(t testing.TB, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // test write; Write's error is checked
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func openIndex(t testing.TB, path, dir string) *Index {
	t.Helper()
	x, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return x
}

func catchUp(t testing.TB, x *Index) {
	t.Helper()
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}
}

// hasNamelessResult reports whether the entry holds a tool result with no name
// of its own: the one entry an extending index may have to rebuild for.
func hasNamelessResult(turn schema.Turn) bool {
	for _, part := range turn.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.Name == "" {
			return true
		}
	}
	return false
}

// needsCommunicateHistory reports whether the entry is part of a deferred
// communicate flow: the call itself, or its (possibly nameless, already
// covered by hasNamelessResult) result. Like a nameless result, a restart
// between the call and its result can lose the CommRawArgs/LastAssistantText
// state the index needs, and force a rebuild.
func needsCommunicateHistory(turn schema.Turn) bool {
	for _, part := range turn.Message.Content {
		switch part.Kind {
		case llm.ContentToolCall:
			if part.ToolCall != nil && part.ToolCall.Name == "communicate" {
				return true
			}
		case llm.ContentToolResult:
			if part.ToolResult != nil && part.ToolResult.Name == "communicate" {
				return true
			}
		}
	}
	return false
}

// liveBuild is the sidecar's live build directory.
func liveBuild(t testing.TB, dir string) string {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, string(bytes.TrimSpace(current)))
}

// namedResults is the corpus minus the fixtures holding tool results with no
// name of their own, which an index that extends may rebuild for.
func namedResults() fixture {
	fx := fixture{name: "named results"}
	for _, set := range fixtures() {
		named := true
		for _, line := range set.lines {
			named = named && !hasNamelessResult(line.turn) && !needsCommunicateHistory(line.turn)
		}
		if named && set.name != "everything" {
			fx.header = set.header
			fx.lines = append(fx.lines, set.lines...)
		}
	}
	return fx
}

func everything() fixture {
	all := fixtures()
	return all[len(all)-1]
}

// writeHeaderOnly starts the fixture's transcript with its header alone and
// returns the path and the entry lines still to append.
func writeHeaderOnly(t testing.TB, fx fixture) (string, [][]byte) {
	t.Helper()
	header, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, lines
}

func TestAppendEntryByEntryMatchesTheReference(t *testing.T) {
	fx := everything()
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	for i, line := range lines {
		appendBytes(t, path, line)
		if i%7 == 6 {
			// A reopen resumes from the sidecar: no rebuild.
			if err := x.Close(); err != nil {
				t.Fatal(err)
			}
			x = openIndex(t, path, dir)
			if x.rebuilds != 0 && !fx.lines[i].blank && !hasNamelessResult(fx.lines[i].turn) && !needsCommunicateHistory(fx.lines[i].turn) {
				t.Fatalf("line %d: reopening rebuilt the index", i)
			}
		} else {
			before := x.rebuilds
			catchUp(t, x)
			if x.rebuilds != before && !hasNamelessResult(fx.lines[i].turn) && !needsCommunicateHistory(fx.lines[i].turn) {
				t.Fatalf("line %d: extending rebuilt the index", i)
			}
		}
		assertAllWindows(t, x, path)
	}
}

func TestUnterminatedTailIsPickedUpLater(t *testing.T) {
	fx := everything()
	header, lines := fx.encode(t)
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	if err := os.WriteFile(path, header, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, line := range lines[:len(lines)-1] {
		appendBytes(t, path, line)
	}
	last := lines[len(lines)-1]
	appendBytes(t, path, last[:len(last)/2])
	x := openIndex(t, path, t.TempDir())
	withoutTail := referenceCandidates(t, path)
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "half a line", window, withoutTail, len(withoutTail), 40)

	appendBytes(t, path, last[len(last)/2:])
	catchUp(t, x)
	if x.rebuilds != 1 {
		t.Fatalf("finishing the line rebuilt the index (%d builds)", x.rebuilds)
	}
	assertAllWindows(t, x, path)
}

func TestReplacedTruncatedOrRewrittenTranscriptRebuilds(t *testing.T) {
	fx := everything()
	header, lines := fx.encode(t)
	full := append(append([]byte(nil), header...), joinLines(lines)...)
	prefix := append(append([]byte(nil), header...), joinLines(lines[:len(lines)/2])...)
	cases := []struct {
		name   string
		change func(t *testing.T, path string)
	}{
		{"replaced by rename", func(t *testing.T, path string) {
			other := path + ".new"
			if err := os.WriteFile(other, prefix, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated", func(t *testing.T, path string) {
			if err := os.Truncate(path, int64(len(prefix))); err != nil {
				t.Fatal(err)
			}
		}},
		{"truncated and regrown", func(t *testing.T, path string) {
			if err := os.Truncate(path, int64(len(prefix))); err != nil {
				t.Fatal(err)
			}
			appendBytes(t, path, encodeEntry(t, 1, user("a different ending")))
			appendBytes(t, path, joinLines(lines[len(lines)/2:]))
		}},
		{"rewritten in place", func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Same length: a character of the last entry's text changes.
			if _, err := f.WriteAt([]byte("A"), int64(bytes.LastIndex(full, []byte("after duplicates")))); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
			if err := os.WriteFile(path, full, 0o600); err != nil {
				t.Fatal(err)
			}
			x := openIndex(t, path, t.TempDir())
			tc.change(t, path)
			catchUp(t, x)
			if x.rebuilds != 2 {
				t.Fatalf("builds = %d, want a rebuild after the change", x.rebuilds)
			}
			assertAllWindows(t, x, path)
		})
	}
}

func joinLines(lines [][]byte) []byte {
	var out []byte
	for _, line := range lines {
		out = append(out, line...)
	}
	return out
}

func TestReplayAfterCrashIsIdempotent(t *testing.T) {
	// The replayed entries continue the open turn: they add usage to its
	// summary and complete a call item, both in-place updates.
	fx := everything()
	fx.lines = append(fx.lines,
		entryLine(withUsage(at(user("replay"), 50), 1, 1, 0, 2)),
		entryLine(withUsage(assistant(call("rp1", "read_file", `{}`)), 10, 20, 5, 30)),
		entryLine(results(result("rp1", "read_file", "replayed result"))),
		entryLine(withUsage(assistant(text("replayed answer")), 3, 4, 0, 7)),
	)
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	cut := len(lines) - 2
	appendBytes(t, path, joinLines(lines[:cut]))
	x := openIndex(t, path, dir)
	held, err := x.Latest(1)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(liveBuild(t, dir), metaFile))
	if err != nil {
		t.Fatal(err)
	}
	// The next catch-up writes records, rewrites turn summaries and item
	// contributors in place, and then its meta. Restoring the old meta leaves
	// the sidecar as a crash before the meta write would: records and
	// in-place updates the meta does not count.
	appendBytes(t, path, joinLines(lines[cut:]))
	catchUp(t, x)
	changed, err := x.ChangedSince(held.Length)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed.Items) == 0 || len(changed.Turns) == 0 {
		t.Fatalf("the replayed entries changed nothing held: %+v", changed)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveBuild(t, dir), metaFile), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	replayed := openIndex(t, path, dir)
	if replayed.rebuilds != 0 {
		t.Fatal("replaying after a crash rebuilt the index")
	}
	assertAllWindows(t, replayed, path)
	// The replay redoes the update log, so a reader holding the old snapshot
	// still learns what the replayed entries changed.
	replayedChanges, err := replayed.ChangedSince(held.Length)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayedChanges, changed) {
		t.Fatalf("after replay ChangedSince = %s, want %s", dump(replayedChanges), dump(changed))
	}
	// Replaying the result entry must not add it as a second contributor.
	for slot := range replayed.items.n {
		buf, err := replayed.items.read(slot, 1)
		if err != nil {
			t.Fatal(err)
		}
		record := decodeItem(buf)
		if call, _ := replayed.strings.get(record.Call); string(call) == "rp1" && record.Middle.Len != 0 {
			t.Fatalf("replay recorded the result entry twice: middle contributors %d bytes", record.Middle.Len)
		}
	}
}

func TestNamelessResultForAnEarlierTurnRebuildsWhenExtending(t *testing.T) {
	header := everything().header
	path, _ := writeHeaderOnly(t, fixture{header: header})
	appendBytes(t, path, joinLines([][]byte{
		encodeEntry(t, 1, user("go")),
		encodeEntry(t, 2, assistant(call("e1", "grep", `{}`))),
		encodeEntry(t, 3, standalone(schema.TurnHookCompleted, "hook")),
	}))
	x := openIndex(t, path, t.TempDir())
	appendBytes(t, path, encodeEntry(t, 4, results(result("e1", "", "nameless, called in an earlier turn"))))
	catchUp(t, x)
	if x.rebuilds != 2 {
		t.Fatalf("builds = %d, want a rebuild for the nameless result", x.rebuilds)
	}
	assertAllWindows(t, x, path)

	// A nameless result for a call the open turn made extends in place.
	appendBytes(t, path, joinLines([][]byte{
		encodeEntry(t, 5, user("again")),
		encodeEntry(t, 6, assistant(call("e2", "read_file", `{}`))),
	}))
	catchUp(t, x)
	appendBytes(t, path, encodeEntry(t, 7, results(result("e2", "", "nameless, called in this turn"))))
	catchUp(t, x)
	if x.rebuilds != 2 {
		t.Fatalf("builds = %d, a nameless result for the open turn's call rebuilt", x.rebuilds)
	}
	assertAllWindows(t, x, path)
}

func TestCorruptSidecarRebuilds(t *testing.T) {
	fx := everything()
	cases := []struct {
		name    string
		corrupt func(t *testing.T, dir string)
	}{
		{"garbage meta", func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(liveBuild(t, dir), metaFile), []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"short items table", func(t *testing.T, dir string) {
			if err := os.Truncate(filepath.Join(liveBuild(t, dir), itemsFile), itemRecordSize); err != nil {
				t.Fatal(err)
			}
		}},
		{"other format", func(t *testing.T, dir string) {
			data, err := os.ReadFile(filepath.Join(liveBuild(t, dir), metaFile))
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(string(data[:len(data)-1]) + `,"format":99}`)
			if err := os.WriteFile(filepath.Join(liveBuild(t, dir), metaFile), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing strings", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(liveBuild(t, dir), stringsFile)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFixture(t, fx)
			dir := t.TempDir()
			x := openIndex(t, path, dir)
			if err := x.Close(); err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, dir)
			reopened := openIndex(t, path, dir)
			if reopened.rebuilds != 1 {
				t.Fatalf("builds = %d, want the corrupt sidecar rebuilt", reopened.rebuilds)
			}
			assertAllWindows(t, reopened, path)
		})
	}
}

// TestFailedRebuildsLeaveNoBuildBehind: a transcript with a line that does
// not decode fails every catch-up, and each failure rebuilds; the failed
// builds must not pile up on disk.
func TestFailedRebuildsLeaveNoBuildBehind(t *testing.T) {
	path := writeFixture(t, everything())
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	appendBytes(t, path, []byte("{not json\n"))
	for range 5 {
		if err := x.CatchUp(); err == nil {
			t.Fatal("catching up over a line that does not decode succeeded")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	builds := 0
	for _, entry := range entries {
		if entry.IsDir() {
			builds++
		}
	}
	if builds != 1 {
		t.Fatalf("%d build directories after failed rebuilds, want the one live build", builds)
	}
}
