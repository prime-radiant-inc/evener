package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema/schematest"
)

// TestMultiHandleCatchUpAdoptsPendingCommunicate: a handle that never itself
// scanned the pending communicate call must still project it correctly after
// calling CatchUp directly (not just a window read), whether the call is
// still pending or has since been paired by the writer (roborev finding on
// PR #2545, "stale builder state after adoption can lose an adopted pending
// communicate"). This is the CatchUp-path counterpart to
// TestAdoptedHandleRepairsStaleBuilderBeforeFlush, which only exercises the
// Latest/Before read path.
func TestMultiHandleCatchUpAdoptsPendingCommunicate(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(user("talk")),
		entryLine(call1("m1")),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	a := openIndex(t, path, dir)
	b := openIndex(t, path, dir)

	appendBytes(t, path, joinLines(lines))
	catchUp(t, a)

	// b adopts a's pending communicate through its own CatchUp, not through
	// a window read.
	catchUp(t, b)
	want := referenceCandidates(t, path)
	window, err := b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "b's CatchUp adopts a's pending flush", window, want, len(want), 40)

	// a pairs the call; b's next CatchUp must adopt the resolved state too.
	appendBytes(t, path, encodeEntry(t, 3, results(result("m1", "communicate", "delivered"))))
	catchUp(t, a)
	catchUp(t, b)
	want = referenceCandidates(t, path)
	window, err = b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "b's CatchUp adopts a's pairing", window, want, len(want), 40)
}

// TestAlternatingHandlesDoNotRebuildOverAPendingCommunicateTail: two handles
// (the production shape in phase 3: hub + daemon) reading a transcript whose
// tail is an unpaired communicate call must not rebuild on every read.
// Before the fix, adopting a build with meta.PendingCommunicate set always
// forced repairBuilder to rebuild, which mints a new build directory and
// rewrites CURRENT — so the other handle's next read adopts *that* build and
// rebuilds again, and so on for every alternating read (roborev finding on
// PR #2545, "rebuild-on-every-read across handles while a communicate is
// pending").
func TestAlternatingHandlesDoNotRebuildOverAPendingCommunicateTail(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(user("talk")),
		entryLine(call1("p1")),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	a := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines))
	catchUp(t, a)

	b := openIndex(t, path, dir)
	baseA, baseB := a.rebuilds, b.rebuilds
	want := referenceCandidates(t, path)

	for i := range 5 {
		wa, err := a.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "a's read", wa, want, len(want), 40)
		wb, err := b.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "b's read", wb, want, len(want), 40)
		if a.rebuilds != baseA || b.rebuilds != baseB {
			t.Fatalf("round %d: alternating reads over a pending-communicate tail rebuilt: a %d->%d, b %d->%d", i, baseA, a.rebuilds, baseB, b.rebuilds)
		}
	}
}

// A covered prefix of transcript-only entries holds entries but no turn
// record, so restoring the builder must not read one.
func TestAlternatingHandlesDoNotRebuildOverATranscriptOnlyPrefix(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(schematest.TranscriptOnlySamples()[0]),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	a := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines))
	catchUp(t, a)

	b := openIndex(t, path, dir)
	baseA, baseB := a.rebuilds, b.rebuilds
	want := referenceCandidates(t, path)

	for i := range 3 {
		wa, err := a.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "a's read", wa, want, len(want), 40)
		wb, err := b.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		assertWindow(t, "b's read", wb, want, len(want), 40)
		if a.rebuilds != baseA || b.rebuilds != baseB {
			t.Fatalf("round %d: alternating reads over a transcript-only prefix rebuilt: a %d->%d, b %d->%d", i, baseA, a.rebuilds, baseB, b.rebuilds)
		}
	}
}
