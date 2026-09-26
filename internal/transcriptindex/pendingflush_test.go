package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// commTestHeader is a plain header (no system prompt), for tests that build
// their own small transcripts rather than drawing from the shared fixture
// corpus.
func commTestHeader() transcript.Header {
	return transcript.Header{SessionID: "th_pendingflush", CreatedAt: fixtureClock, ProfileID: "openai", Model: "gpt-test"}
}

// TestAdoptedHandleRepairsStaleBuilderBeforeFlush: a handle that never itself
// scanned a communicate call must not read its own (stale, empty) builder
// state when another handle wrote the pending call — it must adopt the
// covered records and repair before pendingFlush runs (roborev finding on PR
// #2303, round 4: "stale builder after adopt"). Covers both directions: b
// picks up a's pending flush, and later stops showing it once a pairs the
// call.
func TestAdoptedHandleRepairsStaleBuilderBeforeFlush(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(user("talk")),
		entryLine(call1("a1")),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	a := openIndex(t, path, dir)
	b := openIndex(t, path, dir)

	appendBytes(t, path, joinLines(lines))
	catchUp(t, a)

	// b has never scanned the pending call itself; Latest must still surface
	// the flushed message using the records a wrote, not b's own (empty)
	// builder.
	want := referenceCandidates(t, path)
	window, err := b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "b sees a's pending flush", window, want, len(want), 40)

	// a pairs the call; b must stop showing the now-resolved flush (the
	// inverse direction: a stale builder that still holds a resolved
	// communicate must not re-emit it).
	appendBytes(t, path, encodeEntry(t, 3, results(result("a1", "communicate", "delivered"))))
	catchUp(t, a)
	want = referenceCandidates(t, path)
	window, err = b.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "b sees a's pairing", window, want, len(want), 40)
}

// call1 is an assistant entry issuing a single deferred communicate call
// with a well-formed message, and no text of its own.
func call1(id string) schema.Turn {
	return assistant(call(id, "communicate", `{"message":"pending"}`))
}

// TestReopenThenTextlessCommunicateRecoversEarlierAssistantText: restoreBuilder
// (run on a reopen) cannot recover lastAssistantText any more than it can
// commCalls — both are builder state a targeted restore does not attempt to
// reconstruct (see restoreBuilder). A communicate call that becomes pending
// right after a reopen, from an assistant entry with no text of its own,
// must still see the true earlier assistant text the whole-file projection
// carries, so the echo check suppresses a message that duplicates it
// (roborev finding on PR #2303, round 4, trigger (b)).
func TestReopenThenTextlessCommunicateRecoversEarlierAssistantText(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(user("start")),
		entryLine(assistant(text("echo me"))),
	}}
	path, lines := writeHeaderOnly(t, fx)
	dir := t.TempDir()
	x := openIndex(t, path, dir)
	appendBytes(t, path, joinLines(lines))
	catchUp(t, x)

	// Reopen: a fresh Index instance loads the sidecar and restores the
	// builder from records, rather than scanning from scratch.
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	x = openIndex(t, path, dir)

	// A text-less communicate call, in the same turn as the earlier "echo
	// me" text, whose message echoes it. The whole-file projection
	// suppresses this as a duplicate of the assistant text already shown;
	// the index must too.
	appendBytes(t, path, encodeEntry(t, 3, assistant(call("k20", "communicate", `{"message":"echo me"}`))))
	catchUp(t, x)

	want := referenceCandidates(t, path)
	window, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	assertWindow(t, "suppressed echo after reopen", window, want, len(want), 40)
}

// TestRepeatedCatchUpWithNoGrowthDoesNotRebuildWhilePending: extend used to
// rebuild the whole index on every catch-up while meta.PendingCommunicate was
// set, even with nothing new to cover and this handle's own builder already
// authoritative (roborev finding on PR #2303, round 4). A caller that
// re-checks the same length repeatedly — the common shape for a transcript
// that ends on an unpaired communicate call — must not pay for a rebuild each
// time.
func TestRepeatedCatchUpWithNoGrowthDoesNotRebuildWhilePending(t *testing.T) {
	fx := fixture{header: commTestHeader(), lines: []fixtureLine{
		entryLine(user("talk")),
		entryLine(call1("k30")),
	}}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	before := x.rebuilds
	for range 5 {
		if err := x.CatchUp(); err != nil {
			t.Fatal(err)
		}
	}
	if x.rebuilds != before {
		t.Fatalf("rebuilds = %d, want %d: repeated no-growth CatchUp rebuilt while pending", x.rebuilds, before)
	}
}
