package jobstore

import (
	"regexp"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/provenance"
)

func TestFoldWatchSendPendingLatestWinsAndTerminalRemoves(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", Message: "first"}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2", Message: "latest", CoalescedCount: 1}},
	}
	rec := FoldWatchSends(events)
	if got := rec.Pending[key].Message; got != "latest" {
		t.Fatalf("message = %q, want latest", got)
	}
	events = append(events, Event{Kind: EventWatchSendDelivered, Seq: 3, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2"}})
	if got := FoldWatchSends(events).Pending; len(got) != 0 {
		t.Fatalf("pending after delivered = %+v", got)
	}
}

func TestFoldWatchSendTerminalKindsRemovePending(t *testing.T) {
	for _, kind := range []EventKind{EventWatchSendDelivered, EventWatchSendDropped, EventWatchSendEvicted} {
		t.Run(string(kind), func(t *testing.T) {
			key := WatchSendKey{
				VisibleSessionID:        "root",
				WatchTarget:             "job_A",
				ResolvedWatchedIdentity: "job_A",
				ResolvedSendTo:          "job_sidecar",
				WatchGeneration:         "wg_1",
			}
			events := []Event{
				{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", Message: "pending"}},
				{Kind: kind, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1"}},
			}
			if got := FoldWatchSends(events).Pending; len(got) != 0 {
				t.Fatalf("pending after %s = %+v", kind, got)
			}
		})
	}
}

func TestFoldWatchSendOlderTerminalDoesNotRemoveNewerPending(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 1, Message: "first"}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2", UpdateSeq: 2, Message: "newer"}},
		{Kind: EventWatchSendDelivered, Seq: 3, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 1}},
	}
	rec := FoldWatchSends(events)
	got := rec.Pending[key]
	if got == nil {
		t.Fatal("newer pending was removed by older delivered event")
	}
	if got.DeliveryID != "d2" || got.Message != "newer" {
		t.Fatalf("pending = %+v, want newer delivery", got)
	}
}

func TestFoldWatchSendNewerTerminalTombstoneRejectsOlderPending(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendDelivered, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2", UpdateSeq: 2}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 1, Message: "stale"}},
	}
	if got := FoldWatchSends(events).Pending; len(got) != 0 {
		t.Fatalf("pending after newer delivered tombstone = %+v, want none", got)
	}
}

func TestFoldWatchSendZeroSeqTerminalTombstoneRejectsZeroSeqPending(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendDelivered, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1"}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", Message: "stale"}},
	}
	if got := FoldWatchSends(events).Pending; len(got) != 0 {
		t.Fatalf("pending after zero-seq delivered tombstone = %+v, want none", got)
	}
}

func TestFoldWatchSendOlderPendingDoesNotOverwriteNewerPending(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "root",
		WatchTarget:             "job_A",
		ResolvedWatchedIdentity: "job_A",
		ResolvedSendTo:          "job_sidecar",
		WatchGeneration:         "wg_1",
	}
	events := []Event{
		{Kind: EventWatchSendPending, Seq: 1, WatchSend: &WatchSendState{Key: key, DeliveryID: "d2", UpdateSeq: 2, Message: "newer"}},
		{Kind: EventWatchSendPending, Seq: 2, WatchSend: &WatchSendState{Key: key, DeliveryID: "d1", UpdateSeq: 1, Message: "stale"}},
	}
	rec := FoldWatchSends(events)
	got := rec.Pending[key]
	if got == nil {
		t.Fatal("newer pending was removed")
	}
	if got.DeliveryID != "d2" || got.Message != "newer" {
		t.Fatalf("pending = %+v, want newer delivery", got)
	}
}

func TestOutputMatcherMatchesCompletedLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`(?i)ready`))
	got := m.Feed([]byte("starting\nserver ready\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matches = %#v, want [\"server ready\"]", got)
	}
}

func TestOutputMatcherNoSilentMissAcrossChunks(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	if got := m.Feed([]byte("server REA")); len(got) != 0 {
		t.Fatalf("partial line must not match yet: %#v", got)
	}
	if got := m.Feed([]byte("DY now\n")); len(got) != 1 || got[0] != "server READY now" {
		t.Fatalf("split-across-chunks line must match once joined: %#v", got)
	}
}

func TestOutputMatcherDoesNotRematchOldBytes(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	_ = m.Feed([]byte("ready\n"))
	got := m.Feed([]byte("still going\n"))
	if len(got) != 0 {
		t.Errorf("already-consumed line must not re-match: %#v", got)
	}
}

func TestOutputMatcherFlushReturnsFinalPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	if got := m.Feed([]byte("all done")); len(got) != 0 {
		t.Fatalf("unterminated line must not match on Feed: %#v", got)
	}
	if got := m.Flush(); len(got) != 1 || got[0] != "all done" {
		t.Errorf("Flush must match the buffered final line: %#v", got)
	}
}

func TestOutputMatcherEmptyChunksAreNoop(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`partial`))
	if got := m.Feed(nil); len(got) != 0 {
		t.Fatalf("nil chunk must not match: %#v", got)
	}
	if got := m.Feed([]byte("part")); len(got) != 0 {
		t.Fatalf("partial line must not match yet: %#v", got)
	}
	if got := m.Feed([]byte{}); len(got) != 0 {
		t.Fatalf("empty chunk must not match: %#v", got)
	}
	if got := m.Feed([]byte("ial\n")); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("line after empty chunk must match once completed: %#v", got)
	}
}

func TestOutputMatcherEmptyRegexpMatchesCompletedLines(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(``))
	got := m.Feed([]byte("one\n\npartial"))
	if len(got) != 2 || got[0] != "one" || got[1] != "" {
		t.Fatalf("empty regexp matches completed lines = %#v, want [\"one\", \"\"]", got)
	}
	if got := m.Flush(); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("empty regexp must match flushed partial line: %#v", got)
	}
}

func TestOutputMatcherFlushRepeatedlyClearsCarry(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	_ = m.Feed([]byte("done"))
	if got := m.Flush(); len(got) != 1 || got[0] != "done" {
		t.Fatalf("first Flush must match the buffered line: %#v", got)
	}
	if got := m.Flush(); len(got) != 0 {
		t.Fatalf("second Flush must not re-match: %#v", got)
	}
}

func TestOutputMatcherStripsCRLFBeforeMatching(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready$`))
	got := m.Feed([]byte("server ready\r\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("CRLF line matches = %#v, want [\"server ready\"]", got)
	}
}

func TestOutputMatcherBoundsOverlongCarry(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte(strings.Repeat("x", maxOutputMatcherLineBytes+1))); len(got) != 0 {
		t.Fatalf("overlong unterminated line must not match: %#v", got)
	}
	if len(m.carry) > maxOutputMatcherLineBytes {
		t.Fatalf("carry length = %d, want <= %d", len(m.carry), maxOutputMatcherLineBytes)
	}
	if got := m.Feed([]byte("ready\n")); len(got) != 0 {
		t.Fatalf("overlong line must be skipped through newline: %#v", got)
	}
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matcher did not recover after overlong line: %#v", got)
	}
}

func TestOutputMatcherFlushSkipsOverlongPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte(strings.Repeat("x", maxOutputMatcherLineBytes+1) + "ready")); len(got) != 0 {
		t.Fatalf("overlong unterminated line must not match on Feed: %#v", got)
	}
	if got := m.Flush(); len(got) != 0 {
		t.Fatalf("overlong final partial line must not match on Flush: %#v", got)
	}
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matcher did not recover after overlong flush: %#v", got)
	}
}

func TestOutputMatcherFeedAtAtOrBelowScanOffsetMatchesNothing(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	m.SetScanOffset(20)
	if got := m.FeedAt([]byte("ready\n"), 10); len(got) != 0 {
		t.Fatalf("chunk ending below the scan offset must not match: %#v", got)
	}
	if got := m.FeedAt([]byte("ready\n"), 20); len(got) != 0 {
		t.Fatalf("chunk ending at the scan offset must not match: %#v", got)
	}
}

func TestOutputMatcherFeedAtStraddlingChunkMatchesOnlySuffix(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`tok\d`))
	m.SetScanOffset(8)
	// Chunk spans [3, 13): "tok1\n" lies below the scan offset, "tok2\n" beyond.
	got := m.FeedAt([]byte("tok1\ntok2\n"), 13)
	if len(got) != 1 || got[0] != "tok2" {
		t.Fatalf("straddling chunk matches = %#v, want [\"tok2\"]", got)
	}
}

func TestOutputMatcherSeedCarryMatchesTokenStraddlingScanBoundary(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`server READY`))
	// The attach-time scan covered [0, 12) whose retained tail after the last
	// newline was the partial line "ser"; the rest of the line arrives via FeedAt.
	m.SetScanOffset(12)
	m.SeedCarry([]byte("ser"))
	if got := m.FeedAt([]byte("ver READY\n"), 22); len(got) != 1 || got[0] != "server READY" {
		t.Fatalf("token straddling the scan boundary matches = %#v, want [\"server READY\"]", got)
	}
	if got := m.FeedAt([]byte("done\n"), 27); len(got) != 0 {
		t.Fatalf("straddling token must match exactly once: %#v", got)
	}
}

func TestOutputMatcherFeedCounterSkipsScanCoveredBytes(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	if got := m.Feed([]byte("starting\n")); len(got) != 0 {
		t.Fatalf("pre-scan feed must not match: %#v", got)
	}
	// An attach-time scan covered [0, 15): "starting\nREADY\n". The trailing
	// "READY\n" was appended before the scan but reaches Feed only after it.
	m.SetScanOffset(15)
	if got := m.Feed([]byte("READY\n")); len(got) != 0 {
		t.Fatalf("scan-covered bytes must not match again: %#v", got)
	}
	if got := m.Feed([]byte("more READY\n")); len(got) != 1 || got[0] != "more READY" {
		t.Fatalf("bytes beyond the scan offset must match: %#v", got)
	}
}

func TestOutputMatcherFeedAtNeverMatchesLineBelowScanOffset(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	m.SetScanOffset(6)
	for i := 0; i < 2; i++ {
		if got := m.FeedAt([]byte("ready\n"), 6); len(got) != 0 {
			t.Fatalf("feed %d: scan-covered line must never match: %#v", i, got)
		}
	}
	if got := m.FeedAt([]byte("ready again\n"), 18); len(got) != 1 || got[0] != "ready again" {
		t.Fatalf("line beyond the scan offset must still match: %#v", got)
	}
}

func TestOutputMatcherSeedCarryReplacesBufferedPartial(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`^fresh tail$`))
	if got := m.Feed([]byte("stale partial")); len(got) != 0 {
		t.Fatalf("partial line must not match: %#v", got)
	}
	m.SeedCarry([]byte("fresh tail"))
	if got := m.Feed([]byte("\n")); len(got) != 1 || got[0] != "fresh tail" {
		t.Fatalf("seeded carry must replace the buffered partial: %#v", got)
	}
}

func TestOutputMatcherSeedCarryBoundsOverlongTail(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	m.SeedCarry([]byte(strings.Repeat("x", maxOutputMatcherLineBytes+1)))
	if len(m.carry) > maxOutputMatcherLineBytes {
		t.Fatalf("carry length = %d, want <= %d", len(m.carry), maxOutputMatcherLineBytes)
	}
	if got := m.Feed([]byte("ready\n")); len(got) != 0 {
		t.Fatalf("line completing an overlong seeded tail must not match: %#v", got)
	}
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("matcher did not recover after overlong seeded tail: %#v", got)
	}
}

func TestScanRetainedReturnsLastMatchingLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`(?i)ready`))
	last, matched := m.ScanRetained([]byte("ready one\nnoise\nready two\nready three\n"))
	if !matched || last != "ready three" {
		t.Fatalf("ScanRetained = (%q, %v), want last matching line \"ready three\"", last, matched)
	}
}

func TestScanRetainedIgnoresUnterminatedFinalLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	// The only "ready" sits on the unterminated final line; it belongs to the
	// carry and must not be reported by the level scan.
	last, matched := m.ScanRetained([]byte("starting\nserver ready"))
	if matched {
		t.Fatalf("unterminated final line must be ignored, got match %q", last)
	}
}

func TestScanRetainedSkipsOverlongLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	// A complete line longer than the cap is skipped exactly like the stream path,
	// so a match buried in an overlong line is not reported.
	data := []byte(strings.Repeat("x", maxOutputMatcherLineBytes+1) + "ready\n")
	last, matched := m.ScanRetained(data)
	if matched {
		t.Fatalf("overlong complete line must be skipped, got match %q", last)
	}
	// A normal matching line after the overlong one is still found.
	data = append(data, []byte("server ready\n")...)
	last, matched = m.ScanRetained(data)
	if !matched || last != "server ready" {
		t.Fatalf("ScanRetained after overlong line = (%q, %v), want \"server ready\"", last, matched)
	}
}

func TestScanRetainedNoMatchReturnsFalse(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if last, matched := m.ScanRetained([]byte("starting\nworking\n")); matched {
		t.Fatalf("no matching complete line must return false, got %q", last)
	}
}

func TestScanRetainedEmptyDataReturnsFalse(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(``))
	if last, matched := m.ScanRetained(nil); matched {
		t.Fatalf("empty data must return false, got %q", last)
	}
	if last, matched := m.ScanRetained([]byte{}); matched {
		t.Fatalf("empty data must return false, got %q", last)
	}
}

func TestScanRetainedDoesNotTouchCarryOrScanOffset(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	// Prime the attach state the way configureWatch does: scan offset at 12 and a
	// seeded carry of the partial tail "ser".
	m.SetScanOffset(12)
	m.SeedCarry([]byte("ser"))
	// A level scan over retained data must not consume the seed or move the scan
	// offset: it is a pure read.
	if _, matched := m.ScanRetained([]byte("server READY\nserver READY\n")); !matched {
		t.Fatal("ScanRetained must still report a level match over retained data")
	}
	// The seed and scan offset survive: a discarded chunk at the boundary leaves
	// the seed intact, and the line completing through FeedAt fires exactly once.
	if got := m.FeedAt([]byte("under\n"), 12); len(got) != 0 {
		t.Fatalf("chunk ending at the scan offset must be discarded: %#v", got)
	}
	if got := m.FeedAt([]byte("ver READY\n"), 22); len(got) != 1 || got[0] != "server READY" {
		t.Fatalf("seeded carry must survive ScanRetained + discard and match once: %#v", got)
	}
}

// TestSeededCarryPreservedAcrossDiscard pins that a FeedAt chunk wholly at or
// below the scan offset is discarded WITHOUT corrupting a seeded carry: the
// straddling token then completes through a later FeedAt and matches exactly
// once. This is the no-double-fire-across-the-seam invariant the attach protocol
// relies on (spec §7.1).
func TestSeededCarryPreservedAcrossDiscard(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	m.SetScanOffset(12)
	m.SeedCarry([]byte("ser"))
	// A chunk ending exactly at the scan offset is discarded and must leave the
	// seeded "ser" carry untouched.
	if got := m.FeedAt([]byte("scan covered\n"), 12); got != nil {
		t.Fatalf("discarded chunk returned matches: %#v", got)
	}
	// "ver READY\n" is 10 bytes ending at 22, starting at 12 == scanOffset, so it
	// is not sliced; combined with the seeded carry "ser" the completed line is
	// "server READY", which matches exactly once.
	if got := m.FeedAt([]byte("ver READY\n"), 22); len(got) != 1 || got[0] != "server READY" {
		t.Fatalf("seeded carry must survive discard and match once: %#v", got)
	}
}

func TestFoldWatchSendsPreservesProvenance(t *testing.T) {
	key := WatchSendKey{
		VisibleSessionID:        "session_1",
		WatchID:                 "watch_A",
		WatchTarget:             "caller",
		ResolvedWatchedIdentity: "caller",
		ResolvedSendTo:          "dlg_1",
		WatchGeneration:         "wg_1",
	}
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "caller")
	rec := FoldWatchSends([]Event{{
		Kind: EventWatchSendPending,
		Seq:  1,
		WatchSend: &WatchSendState{
			Key:        key,
			DeliveryID: "wd_1",
			UpdateSeq:  1,
			Provenance: p,
		},
	}})

	pending := rec.Pending[key]
	if pending == nil {
		t.Fatal("pending watch send missing")
	}
	if !provenance.ContainsWatch(pending.Provenance, "watch_A", "wg_1") {
		t.Fatalf("pending provenance = %+v, want watch_A/wg_1", pending.Provenance)
	}
}
