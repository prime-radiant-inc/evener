package transcriptindex

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/internal/appitempaging"
)

// openRefusing opens a refusing handle the way openIndex opens an ordinary one.
func openRefusing(t testing.TB, path, dir string) *Index {
	t.Helper()
	x, err := OpenRefusing(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	return x
}

// authorForeignSidecar writes a sidecar whose live build claims a foreign
// projection identity: the shape a week-old daemon leaves behind when a
// schema-fingerprint change lands. Hand-authored on purpose — seeding through
// any open path would repair the sidecar and make the test vacuous.
func authorForeignSidecar(t testing.TB, dir string, entries uint64) {
	t.Helper()
	build := "build_foreign"
	if err := os.MkdirAll(filepath.Join(dir, build), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{itemsFile, turnsFile, updatesFile, stringsFile} {
		if err := os.WriteFile(filepath.Join(dir, build, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := meta{
		Format:     formatVersion,
		Projection: projectionID + ":" + strings.Repeat("0", 64), // no binary here computes this fingerprint
		Entries:    entries,
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, build, metaFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, currentFileName), []byte(build+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// lastRecordedEntry reads the live build's committed entry count off the disk
// and returns the ordinal of the last entry it recorded: the value a refusal
// must carry, derived the same way the refusal itself does.
func lastRecordedEntry(t testing.TB, dir string) uint64 {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(dir, string(bytes.TrimSpace(current)), metaFile))
	if err != nil {
		return 0
	}
	var m meta
	if json.Unmarshal(data, &m) != nil || m.Entries == 0 {
		return 0
	}
	return m.Entries - 1
}

// snapshotSidecar records every file in the sidecar directory, keyed by
// relative path, so a test can prove a refusal wrote nothing. skipUpdates
// leaves the update log out: its unbuffered rows are crash evidence an
// interrupted scan may legitimately leave behind, and the update log is the
// one file a mid-scan refusal is allowed to have grown.
func snapshotSidecar(t testing.TB, dir string, skipUpdates bool) map[string][]byte {
	t.Helper()
	snap := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if skipUpdates && (d.Name() == updatesFile || strings.HasPrefix(d.Name(), updatesFile+".")) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		snap[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func assertSameSidecar(t testing.TB, label string, before, after map[string][]byte) {
	t.Helper()
	for _, key := range slices.Sorted(maps.Keys(before)) {
		afterData, ok := after[key]
		if !ok {
			t.Fatalf("%s: the sidecar lost %s", label, key)
		}
		if !bytes.Equal(before[key], afterData) {
			t.Fatalf("%s: the sidecar's %s changed", label, key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(after)) {
		if _, ok := before[key]; !ok {
			t.Fatalf("%s: the refusal left a new %s behind", label, key)
		}
	}
}

func requireRefusal(t testing.TB, err error, wantLast uint64) {
	t.Helper()
	if !errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("err = %v, want ErrRebuildRequired", err)
	}
	var refusal *RebuildRequiredError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a refusal carrying the last recorded entry", err)
	}
	if refusal.LastEntry != wantLast {
		t.Fatalf("refusal names entry %d, want %d", refusal.LastEntry, wantLast)
	}
}

func requireLockBusy(t testing.TB, err error) {
	t.Helper()
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("err = %v, want ErrLockBusy", err)
	}
}

// requireSoon turns a would-be hang into a fast failure: an operation that
// blocks on the sidecar's lock instead of refusing must not stall the suite.
func requireSoon(t testing.TB, op func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- op() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the operation blocked instead of returning promptly")
		return nil
	}
}

// corruptFirstEntry breaks the first entry's JSON in place: the text value's
// opening quote becomes a digit, so every decoder rejects the line — the
// lenient one the window read re-decodes with included. The rewrite is
// length-preserving, so the committed opener length still spans the whole
// line, and it sits far outside the covered tail the grown-by-appends check
// sums, so catch-up stays healthy.
func corruptFirstEntry(t testing.TB, path string, headerLen int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if headerLen >= len(data) {
		t.Fatal("transcript too short to corrupt")
	}
	lineEnd := len(data)
	if i := bytes.IndexByte(data[headerLen:], '\n'); i >= 0 {
		lineEnd = headerLen + i
	}
	quote := []byte(`"text":"`)
	at := bytes.Index(data[headerLen:lineEnd], quote)
	if at < 0 {
		t.Fatal("the first entry line carries no text value to corrupt")
	}
	// The pattern's last byte is the value's opening quote.
	data[headerLen+at+len(quote)-1] = '1'
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// requireQuarantinedFirstItem pins a read over the corrupted transcript: the
// corrupted entry projects as its quarantine item, and every later item is
// exactly what the healthy reference held. The reference is taken before the
// corruption, since the reference reader rejects lines it did not author.
func requireQuarantinedFirstItem(t testing.TB, window Window, want []appitempaging.TranscriptItemCandidate) {
	t.Helper()
	if len(window.Candidates) != len(want) {
		t.Fatalf("window holds %d candidates, want the healthy reference's %d", len(window.Candidates), len(want))
	}
	first := window.Candidates[0]
	// The quarantine's identity, not its copy: the stub item's machine ID is
	// the API's own contract value (Copy Is Not a Test Oracle).
	if first.Item.ID != "item_unreadable_0" {
		t.Fatalf("first item = %+v, want the corrupted entry's quarantine", first.Item)
	}
	if !reflect.DeepEqual(window.Candidates[1:], want[1:]) {
		t.Fatal("the items after the corrupted entry diverge from the healthy reference")
	}
}

// bigFixture is a transcript whose first entry sits far outside the covered
// tail, so corrupting it leaves the grown-by-appends check intact.
func bigFixture(t testing.TB) (fixture, int) {
	t.Helper()
	header := transcript.Header{SessionID: "th_refusing", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
	var lines []fixtureLine
	for range 12 {
		lines = append(lines, entryLine(user(strings.Repeat("word ", 200))))
	}
	fx := fixture{name: "big", header: header, lines: lines}
	return fx, len(encodeHeader(t, header))
}

// preludeFixture is a transcript holding an execution left open, whose
// summary record restoreBuilder reads by id from the strings file: truncating
// that file makes the restore fail.
func preludeFixture() fixture {
	header := transcript.Header{SessionID: "th_refusing", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test", SystemPrompt: "You are Evener."}
	return fixture{name: "prelude", header: header, lines: []fixtureLine{
		entryLine(user("one")),
		entryLine(opens("turn_r1", schema.TurnSpanExecution, user("inside an open execution"))),
	}}
}

// openCommittedSidecar builds a healthy sidecar over fx with an ordinary
// handle and leaves it open, standing for the writer a hub reads beside.
func openCommittedSidecar(t *testing.T, fx fixture) (path, dir string) {
	t.Helper()
	path = writeFixture(t, fx)
	dir = t.TempDir()
	openIndex(t, path, dir)
	return path, dir
}

func TestOpenRefusingOpensWithoutCatchingUp(t *testing.T) {
	fx := fixture{name: "fresh", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}}
	path := writeFixture(t, fx)
	dir := t.TempDir()
	x := openRefusing(t, path, dir)

	// The open created the directory and the lock, and nothing else: no
	// catch-up ran, so no build exists.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != lockFileName {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("sidecar after a refusing open = %v, want only the lock file", names)
	}

	// Catching up finds no live build and refuses to rebuild inline.
	before := snapshotSidecar(t, dir, false)
	requireRefusal(t, x.CatchUp(), 0)
	assertSameSidecar(t, "no-live-build refusal", before, snapshotSidecar(t, dir, false))
}

func TestRefusingCatchUpRefusesEachRebuildTrigger(t *testing.T) {
	t.Run("identity-stale sidecar", func(t *testing.T) {
		path := writeFixture(t, fixture{name: "stale", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
		dir := t.TempDir()
		authorForeignSidecar(t, dir, 38)
		x := openRefusing(t, path, dir)
		before := snapshotSidecar(t, dir, false)
		// The ordinal survives the failed adoption: the refusal reads it off
		// the disk, the only place it still lives.
		requireRefusal(t, x.CatchUp(), 37)
		assertSameSidecar(t, "identity-stale refusal", before, snapshotSidecar(t, dir, false))
	})

	t.Run("replaced transcript file", func(t *testing.T) {
		fx := fixture{name: "replaced", header: everything().header, lines: []fixtureLine{
			entryLine(user("one")),
			entryLine(user("two")),
			entryLine(user("three")),
		}}
		path := writeFixture(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		want := lastRecordedEntry(t, dir)
		if err := os.WriteFile(path, []byte("a completely different transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		x := openRefusing(t, path, dir)
		before := snapshotSidecar(t, dir, false)
		requireRefusal(t, x.CatchUp(), want)
		assertSameSidecar(t, "replaced-file refusal", before, snapshotSidecar(t, dir, false))
		_ = h
	})

	t.Run("mid-scan errRebuild fallback", func(t *testing.T) {
		fx := fixture{name: "orphans", header: everything().header, lines: []fixtureLine{
			entryLine(user("orphans")),
			entryLine(assistant(call("o1", "read_file", `{}`), call("o2", "grep", `{}`))),
			entryLine(results(result("o1", "read_file", "named"), result("o2", "", "nameless orphan"))),
			// The result for a call nothing ever named: only a full build
			// knows the name, so the scan falls back to errRebuild.
			entryLine(results(result("o3", "", "never called"))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		appendBytes(t, path, joinLines(lines[:3]))
		catchUp(t, h)
		appendBytes(t, path, lines[3])
		want := lastRecordedEntry(t, dir)

		x := openRefusing(t, path, dir)
		before := snapshotSidecar(t, dir, true)
		requireRefusal(t, x.CatchUp(), want)
		after := snapshotSidecar(t, dir, true)
		assertSameSidecar(t, "mid-scan refusal", before, after)
		if !bytes.Equal(before[currentFileName], after[currentFileName]) {
			t.Fatal("the mid-scan refusal swapped CURRENT")
		}
	})

	t.Run("leftover update rows", func(t *testing.T) {
		fx := fixture{name: "leftover", header: everything().header, lines: []fixtureLine{
			entryLine(user("hi")),
			entryLine(assistant(call("c1", "read_file", `{}`))),
			entryLine(results(result("c1", "read_file", "body"))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		appendBytes(t, path, joinLines(lines[:2]))
		catchUp(t, h)
		appendBytes(t, path, lines[2])
		// The interrupted extension leaves a leftover update-log row naming a
		// committed slot: exactly the row that would force a rebuild.
		simulateItemKill(t)
		if err := h.CatchUp(); err == nil {
			t.Fatal("the killed extension returned success")
		}
		testKillAfterItemUpdateLog = nil

		want := lastRecordedEntry(t, dir)
		before := snapshotSidecar(t, dir, false)
		x := openRefusing(t, path, dir)
		requireRefusal(t, x.CatchUp(), want)
		assertSameSidecar(t, "leftover-rows refusal", before, snapshotSidecar(t, dir, false))
	})

	t.Run("refused fallback keeps the partial scan untrusted", func(t *testing.T) {
		fx := fixture{name: "partial", header: everything().header, lines: []fixtureLine{
			entryLine(user("hello")),
			entryLine(assistant(call("c1", "read_file", `{}`))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		appendBytes(t, path, lines[0])
		catchUp(t, h)

		// The refusing handle's catch-up scans the appended assistant entry:
		// the item lands — past the committed counts — and the turn's
		// re-stamp faults at its update-log write, so no row ever reaches the
		// disk to name the half-applied line. The fallback rebuild refuses
		// with the scan's partial application still in the handle's memory,
		// and on a retry the leftover-rows trigger has nothing to collect:
		// only the refusal's stale mark keeps that state untrusted.
		x := openRefusing(t, path, dir)
		want := lastRecordedEntry(t, dir)
		appendBytes(t, path, lines[1])
		simulateTurnKill(t)
		err := x.CatchUp()
		requireRefusal(t, err, want)
		var refusal *RebuildRequiredError
		if !errors.As(err, &refusal) {
			t.Fatalf("the refusal carried no typed identity: %v", err)
		}
		testKillAtTurnUpdateLog = nil

		// The fault cleared, and the same handle retries before any repair
		// commits: the refusal must answer again. The half-applied entry may
		// never be replayed as an append — its item sits past the committed
		// counts, and a replay would commit duplicate positions.
		before := snapshotSidecar(t, dir, true)
		requireRefusal(t, x.CatchUp(), want)
		assertSameSidecar(t, "retry after the cleared fault", before, snapshotSidecar(t, dir, true))

		// The window and change reads must refuse too, with the same typed
		// refusal: the half-applied item sits past the committed counts in
		// this handle's memory, and serving it — or changes built on it —
		// under the old snapshot length would publish an entry no committed
		// build covers.
		_, err = x.Latest(40)
		requireRefusal(t, err, want)
		_, err = x.ChangedSince(x.meta.Length)
		requireRefusal(t, err, want)

		// The repair's own recipe — an ordinary handle's revalidating
		// rebuild against the build the refusal named — commits a fresh
		// build, and the refusing handle's next refresh adopts it: the
		// reads recover, agreeing with an ordinary handle's.
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.RebuildIfStillRefused(info.Size(), refusal.Build); err != nil {
			t.Fatalf("the repair recipe: %v", err)
		}
		window, err := x.Latest(40)
		if err != nil {
			t.Fatalf("window after the repair: %v", err)
		}
		reference, err := h.Latest(40)
		if err != nil {
			t.Fatalf("the ordinary handle's window: %v", err)
		}
		if len(window.Candidates) != len(reference.Candidates) || len(window.Candidates) == 0 {
			t.Fatalf("window after the repair = %d candidates, want the ordinary handle's %d", len(window.Candidates), len(reference.Candidates))
		}
		if _, err := x.ChangedSince(x.meta.Length); err != nil {
			t.Fatalf("changes after the repair: %v", err)
		}
	})

	t.Run("pending communicate", func(t *testing.T) {
		fx := fixture{name: "pending", header: everything().header, lines: []fixtureLine{
			entryLine(user("never resolved")),
			entryLine(assistant(callRaw("k5", "communicate", `{message: "still going"}`))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		appendBytes(t, path, joinLines(lines))
		catchUp(t, h)
		// An entry that keeps the call pending and sets no assistant text: the
		// defensive pending-communicate rebuild must fire.
		appendBytes(t, path, encodeEntry(t, 3, user("more")))
		want := lastRecordedEntry(t, dir)

		x := openRefusing(t, path, dir)
		before := snapshotSidecar(t, dir, true)
		requireRefusal(t, x.CatchUp(), want)
		after := snapshotSidecar(t, dir, true)
		assertSameSidecar(t, "pending-communicate refusal", before, after)
		if !bytes.Equal(before[currentFileName], after[currentFileName]) {
			t.Fatal("the pending-communicate refusal swapped CURRENT")
		}
	})

	t.Run("restoreBuilder failure in catch-up", func(t *testing.T) {
		path, dir := openCommittedSidecar(t, preludeFixture())
		// Every open turn's id string lives in the strings file; removing it
		// makes restoreBuilder fail, which repairBuilder answers with a rebuild.
		if err := os.Truncate(filepath.Join(liveBuild(t, dir), stringsFile), 0); err != nil {
			t.Fatal(err)
		}
		want := lastRecordedEntry(t, dir)
		before := snapshotSidecar(t, dir, false)
		x := openRefusing(t, path, dir)
		requireRefusal(t, x.CatchUp(), want)
		assertSameSidecar(t, "restoreBuilder refusal", before, snapshotSidecar(t, dir, false))
	})

	t.Run("restoreBuilder failure on the window path", func(t *testing.T) {
		path, dir := openCommittedSidecar(t, preludeFixture())
		x := openRefusing(t, path, dir)
		if err := x.CatchUp(); err != nil {
			t.Fatal(err)
		}
		// A foreign writer extends the same build, then the strings file goes
		// away: the next window read adopts the changed meta (builder stale),
		// and the exclusive repair step's restoreBuilder fails.
		h := openIndex(t, path, dir)
		appendBytes(t, path, encodeEntry(t, 3, user("three")))
		catchUp(t, h)
		if err := os.Truncate(filepath.Join(liveBuild(t, dir), stringsFile), 0); err != nil {
			t.Fatal(err)
		}
		want := lastRecordedEntry(t, dir)
		before := snapshotSidecar(t, dir, false)
		_, err := x.Latest(5)
		requireRefusal(t, err, want)
		assertSameSidecar(t, "window-path restoreBuilder refusal", before, snapshotSidecar(t, dir, false))
	})
}

// A crash between an extension's record flushes and its meta publish leaves
// the committed build on disk already carrying the overwritten turn — its
// completion and newer version under the committed snapshot — with the
// leftover update-log row announcing it. The refusing CatchUp's leftover-rows
// refusal must mark the build stale the way the refused fallback rebuild
// does, so no read — window or changes — serves the half-applied slot until
// a refresh adopts the repair's committed build (#4343 round-32 medium 1).
func TestALeftoverUpdateRefusalKeepsReadsRefusingUntilTheRepair(t *testing.T) {
	fx := fixture{name: "leftover-turn", header: everything().header, lines: []fixtureLine{
		entryLine(user("hi")),
		entryLine(assistant(call("c1", "read_file", `{}`))),
		entryLine(results(result("c1", "read_file", "body"))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	h := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines[:2]))
	catchUp(t, h)
	floor := h.meta.Length
	appendBytes(t, path, lines[2])
	// The killed extension stamps the assistant turn with its completion,
	// logs the turn's update row, flushes the overwritten record, and dies
	// before meta publishes the covered state.
	simulateKill(t)
	if err := h.CatchUp(); err == nil {
		t.Fatal("the killed extension returned success")
	}
	testKillAfterRecordWrites = nil

	want := lastRecordedEntry(t, dir)
	x := openRefusing(t, path, dir)
	requireRefusal(t, x.CatchUp(), want)

	// The loaded build's committed turn slot carries the overwritten turn:
	// serving it — or changes announcing it — publishes state no committed
	// snapshot covers. Both reads must refuse with the same typed refusal.
	if _, err := x.Latest(40); err == nil {
		t.Fatal("the window read served the overwritten turn the leftover refusal refused to trust")
	} else {
		requireRefusal(t, err, want)
	}
	if _, err := x.ChangedSince(floor); err == nil {
		t.Fatal("the change read announced the overwritten turn the leftover refusal refused to trust")
	} else {
		requireRefusal(t, err, want)
	}

	// The registry's repair commits a fresh build and rotates CURRENT to
	// it; the handle's reads adopt it, the stale mark clearing with the
	// load, and the window serves the repaired derivation.
	h2 := openIndex(t, path, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Latest(40); err != nil {
		t.Fatalf("the window read still refused after the repair committed: %v", err)
	}
	assertAllWindows(t, x, path)
}

// A sidecar whose CURRENT pointer cannot be read is an access problem, not a
// missing build and not a rebuild trigger: a refusing handle answers the
// ordinary error, so the hub treats the read as transient instead of arming
// a repair that cannot fix permissions.
func TestARefusingWindowReadOverAnUnreadableCurrentAnswersTheOrdinaryError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix file permission to make CURRENT unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file permission this test relies on")
	}
	path, dir := openCommittedSidecar(t, preludeFixture())
	if err := os.Chmod(filepath.Join(dir, currentFileName), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, currentFileName), 0o644) })

	x := openRefusing(t, path, dir)
	_, err := x.Latest(5)
	if err == nil {
		t.Fatal("a read whose CURRENT is unreadable should fail")
	}
	if errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("window read over an unreadable CURRENT = %v, want the ordinary access error, not a rebuild refusal", err)
	}
	if errors.Is(err, errNoLiveBuild) {
		t.Fatalf("window read over an unreadable CURRENT = %v, want the access error kept ordinary, not a missing build", err)
	}
}

// The exclusive paths classify too: a refusing CatchUp whose refresh cannot
// read CURRENT must answer the same ordinary error — its extend step would
// only refuse, so without the classification the access failure masquerades
// as a rebuild refusal and arms a doomed repair.
func TestARefusingCatchUpOverAnUnreadableCurrentAnswersTheOrdinaryError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix file permission to make CURRENT unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file permission this test relies on")
	}
	path, dir := openCommittedSidecar(t, preludeFixture())
	if err := os.Chmod(filepath.Join(dir, currentFileName), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, currentFileName), 0o644) })

	x := openRefusing(t, path, dir)
	err := x.CatchUp()
	if err == nil {
		t.Fatal("a catch-up whose CURRENT is unreadable should fail")
	}
	if errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("catch-up over an unreadable CURRENT = %v, want the ordinary access error, not a rebuild refusal", err)
	}
}

// A CURRENT that names a build whose files are gone is no live build either —
// a rebuild recreates them from the transcript — so a refusing read refuses
// and the hub's repair heals, instead of the reads failing as ordinary
// errors that no repair ever answers.
func TestARefusingReadOverAVanishedBuildRefuses(t *testing.T) {
	t.Run("build directory gone", func(t *testing.T) {
		path, dir := openCommittedSidecar(t, preludeFixture())
		name, err := os.ReadFile(filepath.Join(dir, currentFileName))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, strings.TrimSpace(string(name)))); err != nil {
			t.Fatal(err)
		}
		x := openRefusing(t, path, dir)
		requireRefusal(t, x.CatchUp(), 0)
	})
	t.Run("meta.json gone under the loaded build", func(t *testing.T) {
		path, dir := openCommittedSidecar(t, preludeFixture())
		x := openRefusing(t, path, dir)
		if err := x.CatchUp(); err != nil {
			t.Fatalf("catch-up over the committed sidecar: %v", err)
		}
		if err := os.Remove(filepath.Join(liveBuild(t, dir), metaFile)); err != nil {
			t.Fatal(err)
		}
		_, err := x.Latest(5)
		requireRefusal(t, err, 0)
	})
}

func TestRefusingHandleExtendsIncrementallyWithoutRebuilding(t *testing.T) {
	fx := fixture{name: "incremental", header: everything().header, lines: []fixtureLine{
		entryLine(user("read the image")),
		entryLine(assistant(call("c_img", "read_file", `{"path":"a.png"}`))),
		entryLine(results(result("c_img", "read_file", "file body"))),
		entryLine(assistant(text("done"))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	h := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines[:2]))
	catchUp(t, h)

	x := openRefusing(t, path, dir)
	appendBytes(t, path, joinLines(lines[2:]))
	if err := x.CatchUp(); err != nil {
		t.Fatalf("incremental extension on a refusing handle: %v", err)
	}
	if x.rebuilds != 0 {
		t.Fatalf("refusing handle rebuilt %d times on an incremental extension", x.rebuilds)
	}
	assertAllWindows(t, x, path)
}

func TestCorruptRecordOnARefusingWindowReadRefusesAndMarksStale(t *testing.T) {
	t.Run("refusing handle", func(t *testing.T) {
		fx, headerLen := bigFixture(t)
		path := writeFixture(t, fx)
		dir := t.TempDir()
		openIndex(t, path, dir)
		want := referenceCandidates(t, path)
		corruptFirstEntry(t, path, headerLen)

		x := openRefusing(t, path, dir)
		if err := x.CatchUp(); err != nil {
			t.Fatalf("catch-up over an out-of-tail corruption: %v", err)
		}
		_, err := x.Before(want[1].Position, 5)
		requireRefusal(t, err, lastRecordedEntry(t, dir))
		if errors.Is(err, errCorrupt) {
			t.Fatal("the refusal leaked the raw corrupt error")
		}
		x.mu.Lock()
		stale := x.stale
		x.mu.Unlock()
		if !stale {
			t.Fatal("the refusal did not mark the index stale")
		}
	})

	t.Run("ordinary handle keeps the raw error", func(t *testing.T) {
		fx, headerLen := bigFixture(t)
		path := writeFixture(t, fx)
		dir := t.TempDir()
		y := openIndex(t, path, dir)
		want := referenceCandidates(t, path)
		corruptFirstEntry(t, path, headerLen)

		_, err := y.Before(want[1].Position, 5)
		if !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want the raw corrupt error", err)
		}
		if errors.Is(err, ErrRebuildRequired) {
			t.Fatal("an ordinary handle refused a rebuild")
		}
	})
}

// TestACorruptTurnIDReferenceRefusesAndRepairs pins the reader-crash shape
// end to end: a stored turn record whose leading ID reference points past
// the strings file — MaxUint64 is the value that overflows an additive
// bounds check — must refuse as corrupt and heal through a repair, never
// panic the reader.
func TestACorruptTurnIDReferenceRefusesAndRepairs(t *testing.T) {
	path, dir := openCommittedSidecar(t, preludeFixture())
	turnsPath := filepath.Join(liveBuild(t, dir), turnsFile)
	f, err := os.OpenFile(turnsPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	rec := make([]byte, turnRecordSize)
	if _, err := f.ReadAt(rec, 0); err != nil {
		t.Fatal(err)
	}
	for i := range rec[:8] {
		rec[i] = 0xff
	}
	if _, err := f.WriteAt(rec, 0); err != nil {
		t.Fatal(err)
	}

	x := openRefusing(t, path, dir)
	_, err = x.Latest(5)
	requireRefusal(t, err, lastRecordedEntry(t, dir))

	// A foreign handle's rebuild re-encodes the reference from the
	// transcript, and the refusing handle converges onto the fresh build.
	repair := openIndex(t, path, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repair.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUp(); err != nil {
		t.Fatalf("the refusing handle did not converge after the repair: %v", err)
	}
	if _, err := x.Latest(5); err != nil {
		t.Fatal(err)
	}
}

// TestASpanReadOverAnOverUpdateSlotRefusesInsteadOfPanicking pins the
// reader-crash chain a corrupt update row drives through ChangedSince, at
// the span step it reaches: the row's Slot — MaxUint64, the value that
// wraps an additive bounds check — enters the changed-slot maps,
// changedSince calls span(slot, slot+1), and span's neighbour-inclusive
// read wraps to a negative slice offset. The corrupt classification must
// answer instead of panicking the reader.
func TestASpanReadOverAnOverUpdateSlotRefusesInsteadOfPanicking(t *testing.T) {
	path, dir := openCommittedSidecar(t, preludeFixture())
	x := openRefusing(t, path, dir)
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	r := newReader(x)
	// The +1 wraps at runtime, exactly as changedSince's span(slot, slot+1)
	// does when the slot itself is MaxUint64.
	var overSlot uint64 = math.MaxUint64
	_, err := x.span(r, overSlot, overSlot+1)
	if err == nil {
		t.Fatal("a span read over an overflowing slot should fail")
	}
	if !errors.Is(err, errCorrupt) && !errors.Is(err, ErrRebuildRequired) {
		t.Fatalf("span over an overflowing slot = %v, want the corrupt refusal, not a panic", err)
	}
}

// TestAStoredHeaderLengthThatCannotDecodeRefusesAndRepairs pins the
// corruption class every bounds check passes but no rebuild trigger named:
// meta.json's header_length can name bytes that do not decode as a header —
// the read must refuse as corrupt and heal through a repair, never fail as
// an ordinary error no repair answers.
func TestAStoredHeaderLengthThatCannotDecodeRefusesAndRepairs(t *testing.T) {
	path, dir := openCommittedSidecar(t, preludeFixture())
	metaPath := filepath.Join(liveBuild(t, dir), metaFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var edited map[string]any
	if err := json.Unmarshal(data, &edited); err != nil {
		t.Fatal(err)
	}
	edited["header_length"] = 1
	rewritten, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}

	x := openRefusing(t, path, dir)
	_, err = x.Latest(5)
	requireRefusal(t, err, lastRecordedEntry(t, dir))

	// A foreign handle's rebuild recomputes the stored header offsets from
	// the transcript, and the refusing handle converges onto the fresh build.
	repair := openIndex(t, path, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repair.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUp(); err != nil {
		t.Fatalf("the refusing handle did not converge after the repair: %v", err)
	}
	if _, err := x.Latest(5); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptRecordConvergesThroughAForeignRepair(t *testing.T) {
	fx, headerLen := bigFixture(t)
	path := writeFixture(t, fx)
	dir := t.TempDir()
	openIndex(t, path, dir)
	want := referenceCandidates(t, path)
	corruptFirstEntry(t, path, headerLen)

	x := openRefusing(t, path, dir)
	if err := x.CatchUp(); err != nil {
		t.Fatal(err)
	}
	_, err := x.Before(want[1].Position, 5)
	requireRefusal(t, err, lastRecordedEntry(t, dir))

	// The stale mark keeps refusing catch-ups until a repair commits.
	requireRefusal(t, x.CatchUp(), lastRecordedEntry(t, dir))

	// A foreign handle's rebuild commits a fresh build over the corrupt one
	// (quarantining the corrupted entry, as a scan of it must).
	repair := openIndex(t, path, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := repair.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}

	// The refusing handle adopts the new build through refresh, its stale
	// marks clear with the adoption, and the read that refused now succeeds —
	// where keeping the marks would refuse forever.
	if err := x.CatchUp(); err != nil {
		t.Fatalf("the refusing handle did not converge after the repair: %v", err)
	}
	x.mu.Lock()
	stale, rebuilds := x.stale, x.rebuilds
	x.mu.Unlock()
	if stale {
		t.Fatal("the adoption kept the stale marks")
	}
	if rebuilds != 0 {
		t.Fatalf("the refusing handle rebuilt %d times", rebuilds)
	}
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	requireQuarantinedFirstItem(t, window, want)
}

func TestRefusingHandlesReturnErrLockBusyWithoutBlocking(t *testing.T) {
	path, dir := openCommittedSidecar(t, fixture{name: "locked", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	x := openRefusing(t, path, dir)

	// A foreign writer holds the sidecar's exclusive lock.
	lock, err := os.OpenFile(filepath.Join(dir, lockFileName), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockFile(lock, true); err != nil {
		t.Fatal(err)
	}

	requireLockBusy(t, requireSoon(t, x.CatchUp))
	_, err = x.Latest(5)
	requireLockBusy(t, err)

	if err := unlockFile(lock); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := x.CatchUp(); err != nil {
		t.Fatalf("catch-up after the contention cleared: %v", err)
	}
}

// The repair path's revalidation (#4497 r9): a registration that lands after
// another repair already committed a fresh build must adopt that outcome
// instead of rebuilding the healthy sidecar again — a second full rebuild
// re-pays the whole cost and rotates an incarnation readers hold. The
// refusal names the build it refused; RebuildIfStillRefused rebuilds only
// while that build is still the live one.
func TestRebuildIfStillRefusedSkipsABuildTheRefusalDidNotName(t *testing.T) {
	path, dir := openCommittedSidecar(t, fixture{name: "committed", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	before := snapshotSidecar(t, dir, false)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	x, err := OpenWithoutCatchUp(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // the test's own handle; hygiene only

	// The delayed registration: the refusal named a build another reader's
	// repair has already replaced.
	if err := x.RebuildIfStillRefused(info.Size(), "build_the_refusal_named"); err != nil {
		t.Fatal(err)
	}
	assertSameSidecar(t, "the no-op repair", before, snapshotSidecar(t, dir, false))

	// The no-live-build refusal is answered by any committed build: an
	// empty name skips over a live sidecar too.
	if err := x.RebuildIfStillRefused(info.Size(), ""); err != nil {
		t.Fatal(err)
	}
	assertSameSidecar(t, "the answered no-build refusal", before, snapshotSidecar(t, dir, false))
}

func TestRebuildIfStillRefusedRebuildsTheBuildTheRefusalStillNames(t *testing.T) {
	path := writeFixture(t, fixture{name: "stale", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	dir := t.TempDir()
	authorForeignSidecar(t, dir, 1)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	x, err := OpenWithoutCatchUp(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // the test's own handle; hygiene only

	if err := x.RebuildIfStillRefused(info.Size(), "build_foreign"); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, currentFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(bytes.TrimSpace(current)); got == "build_foreign" {
		t.Fatal("CURRENT still names the refused build: the repair did not rebuild")
	}
	y := openIndex(t, path, dir)
	assertAllWindows(t, y, path)
}

func TestRebuildIfStillRefusedRebuildsWhenNoBuildAnswersTheRefusal(t *testing.T) {
	path := writeFixture(t, fixture{name: "absent", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	dir := t.TempDir()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	x, err := OpenWithoutCatchUp(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // the test's own handle; hygiene only

	// The refusal carried no build name and nothing has committed one since.
	if err := x.RebuildIfStillRefused(info.Size(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, currentFileName)); err != nil {
		t.Fatalf("the repair left no live build: %v", err)
	}
	y := openIndex(t, path, dir)
	assertAllWindows(t, y, path)
}

// The revalidation's own failure class: a CURRENT it cannot read must fail
// the repair ordinarily — the registry counts it against the budget and
// retries — instead of guessing a skip, which would record success over a
// sidecar still broken, or guessing a rebuild, whose committed CURRENT would
// bury an access fault a permission change cannot fix.
func TestRebuildIfStillRefusedAnswersAnUnreadableCurrentOrdinarily(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on a Unix file permission to make CURRENT unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores the file permission this test relies on")
	}
	path, dir := openCommittedSidecar(t, preludeFixture())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, currentFileName), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, currentFileName), 0o644) })
	x, err := OpenWithoutCatchUp(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close() //nolint:errcheck // the test's own handle; hygiene only

	err = x.RebuildIfStillRefused(info.Size(), "")
	if err == nil {
		t.Fatal("revalidation over an unreadable CURRENT must not answer success")
	}
	if errors.Is(err, ErrRebuildRequired) || errors.Is(err, ErrLockBusy) {
		t.Fatalf("revalidation over an unreadable CURRENT = %v, want the ordinary access error", err)
	}
}

func TestOrdinaryHandlesStillRebuildWhereRefusingHandlesRefuse(t *testing.T) {
	t.Run("Open rebuilds an identity-stale sidecar", func(t *testing.T) {
		path := writeFixture(t, fixture{name: "stale", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
		dir := t.TempDir()
		authorForeignSidecar(t, dir, 1)
		y := openIndex(t, path, dir)
		if y.rebuilds == 0 {
			t.Fatal("Open did not rebuild the identity-stale sidecar")
		}
		assertAllWindows(t, y, path)
	})

	t.Run("catch-up rebuilds a stale-marked index", func(t *testing.T) {
		fx, headerLen := bigFixture(t)
		path := writeFixture(t, fx)
		dir := t.TempDir()
		y := openIndex(t, path, dir)
		want := referenceCandidates(t, path)
		corruptFirstEntry(t, path, headerLen)
		if _, err := y.Before(want[1].Position, 5); !errors.Is(err, errCorrupt) {
			t.Fatalf("err = %v, want the raw corrupt error", err)
		}
		before := y.rebuilds
		catchUp(t, y)
		if y.rebuilds != before+1 {
			t.Fatalf("rebuilds = %d, want %d: the stale mark must rebuild at the next catch-up", y.rebuilds, before+1)
		}
		window, err := y.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		requireQuarantinedFirstItem(t, window, want)
	})

	t.Run("mid-scan errRebuild still rebuilds", func(t *testing.T) {
		fx := fixture{name: "orphans", header: everything().header, lines: []fixtureLine{
			entryLine(user("orphans")),
			entryLine(assistant(call("o1", "read_file", `{}`), call("o2", "grep", `{}`))),
			entryLine(results(result("o1", "read_file", "named"), result("o2", "", "nameless orphan"))),
			entryLine(results(result("o3", "", "never called"))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		y := openIndex(t, path, dir)
		appendBytes(t, path, joinLines(lines[:3]))
		catchUp(t, y)
		first, err := y.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		appendBytes(t, path, lines[3])
		before := y.rebuilds
		catchUp(t, y)
		if y.rebuilds != before+1 {
			t.Fatalf("rebuilds = %d, want %d: the mid-scan fallback must rebuild on an ordinary handle", y.rebuilds, before+1)
		}
		grown, err := y.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		if grown.Incarnation != first.Incarnation {
			t.Fatal("the mid-scan rebuild minted a new incarnation")
		}
		assertAllWindows(t, y, path)
	})

	t.Run("leftover update rows heal", func(t *testing.T) {
		fx := fixture{name: "leftover", header: everything().header, lines: []fixtureLine{
			entryLine(user("hi")),
			entryLine(assistant(call("c1", "read_file", `{}`))),
			entryLine(results(result("c1", "read_file", "body"))),
		}}
		path, lines := writeHeaderOnly(t, fx)
		dir := t.TempDir()
		h := openIndex(t, path, dir)
		appendBytes(t, path, joinLines(lines[:2]))
		catchUp(t, h)
		appendBytes(t, path, lines[2])
		simulateItemKill(t)
		if err := h.CatchUp(); err == nil {
			t.Fatal("the killed extension returned success")
		}
		testKillAfterItemUpdateLog = nil

		y := openIndex(t, path, dir)
		catchUp(t, y)
		assertAllWindows(t, y, path)
	})
}

// TestForeignBuildAdoptionClearsStaleMarks pins the one behavior change an
// ordinary handle sees: stale marks belong to the build they were set on, so
// adopting a different committed build clears them instead of rebuilding
// redundantly.
func TestForeignBuildAdoptionClearsStaleMarks(t *testing.T) {
	fx, headerLen := bigFixture(t)
	path := writeFixture(t, fx)
	dir := t.TempDir()
	y := openIndex(t, path, dir)
	want := referenceCandidates(t, path)
	corruptFirstEntry(t, path, headerLen)
	if _, err := y.Before(want[1].Position, 5); !errors.Is(err, errCorrupt) {
		t.Fatalf("err = %v, want the raw corrupt error", err)
	}

	// A foreign handle rebuilds: the corrupted entry quarantines, the build
	// mints a new directory, and CURRENT repoints at it.
	foreign := openIndex(t, path, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.Rebuild(info.Size()); err != nil {
		t.Fatal(err)
	}

	before := y.rebuilds
	if err := y.CatchUp(); err != nil {
		t.Fatal(err)
	}
	if y.rebuilds != before {
		t.Fatalf("rebuilds = %d, want %d: adopting a foreign build must clear the stale marks, not rebuild", y.rebuilds, before)
	}
	window, err := y.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	requireQuarantinedFirstItem(t, window, want)
}

// TestChangedMetaAdoptionKeepsStaleMarks pins the other side of the rule: a
// stale-marked handle that adopts a changed meta for the same build keeps its
// marks and still rebuilds at its next extension, exactly as today.
func TestChangedMetaAdoptionKeepsStaleMarks(t *testing.T) {
	fx, headerLen := bigFixture(t)
	path := writeFixture(t, fx)
	dir := t.TempDir()
	y := openIndex(t, path, dir)
	want := referenceCandidates(t, path)
	corruptFirstEntry(t, path, headerLen)
	if _, err := y.Before(want[1].Position, 5); !errors.Is(err, errCorrupt) {
		t.Fatalf("err = %v, want the raw corrupt error", err)
	}

	// A foreign writer extends the same build: the meta changes but the
	// committed build does not.
	foreign := openIndex(t, path, dir)
	appendBytes(t, path, encodeEntry(t, 13, user("appended after the corruption")))
	catchUp(t, foreign)

	before := y.rebuilds
	if err := y.CatchUp(); err != nil {
		t.Fatal(err)
	}
	if y.rebuilds != before+1 {
		t.Fatalf("rebuilds = %d, want %d: same-build marks must survive a changed-meta adoption", y.rebuilds, before+1)
	}
	window, err := y.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	// The rebuild quarantined the corrupted entry and indexed the appended
	// one: one more item than the healthy reference, still led by the
	// quarantine.
	if len(window.Candidates) != len(want)+1 {
		t.Fatalf("window holds %d candidates, want %d", len(window.Candidates), len(want)+1)
	}
	if window.Candidates[0].Item.ID != "item_unreadable_0" {
		t.Fatalf("first item = %+v, want the corrupted entry's quarantine", window.Candidates[0].Item)
	}
	if last := window.Candidates[len(window.Candidates)-1].Item.Text; last != "appended after the corruption" {
		t.Fatalf("last item text = %q, want the appended entry", last)
	}
}

func TestNewCacheWithOpenRefusingOpensWithoutCatchUp(t *testing.T) {
	path := writeFixture(t, fixture{name: "cache", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	c := NewCacheWithOpen(4, OpenRefusing)
	defer c.Close() //nolint:errcheck // test cleanup

	x, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	// The miss opened without catching up: no build directory, no CURRENT.
	entries, err := os.ReadDir(DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != lockFileName {
		t.Fatalf("sidecar after a refusing cache miss = %d entries, want only the lock file", len(entries))
	}
	// The handle the cache handed out carries the refusing policy.
	requireRefusal(t, x.CatchUp(), 0)
	c.Release(x)
}

func TestNewCacheKeepsOpensCatchUpAndRebuildBehavior(t *testing.T) {
	path := writeFixture(t, fixture{name: "cache", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	c := NewCache(2)
	defer c.Close() //nolint:errcheck // test cleanup

	x, err := c.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	// Open caught up and rebuilt: a live build exists and reads succeed.
	if x.rebuilds == 0 {
		t.Fatal("NewCache's miss did not rebuild a never-indexed transcript")
	}
	if _, err := x.Latest(5); err != nil {
		t.Fatal(err)
	}
	c.Release(x)
}

func TestCacheConcurrentAcquireWithAnInjectedOpenSharesOneOpen(t *testing.T) {
	path := writeFixture(t, fixture{name: "cache", header: everything().header, lines: []fixtureLine{entryLine(user("hello"))}})
	var opens atomic.Int32
	c := NewCacheWithOpen(4, func(path, dir string) (*Index, error) {
		opens.Add(1)
		return OpenRefusing(path, dir)
	})
	defer c.Close() //nolint:errcheck // test cleanup

	var wg sync.WaitGroup
	handles := make([]*Index, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			handles[i], errs[i] = c.Acquire(path)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if handles[0] != handles[1] {
		t.Fatal("concurrent Acquires of one path returned different handles")
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("the injected open ran %d times for one path acquired concurrently, want 1", got)
	}
	c.Release(handles[0])
	c.Release(handles[1])
}
