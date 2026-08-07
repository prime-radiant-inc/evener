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

func TestOutputMatcherCarriesProvenanceAcrossChunks(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "job_1")
	if got := m.FeedAtWithProvenance([]byte("re"), 2, p); len(got) != 0 {
		t.Fatalf("partial feed matches = %#v, want none", got)
	}

	got := m.FeedAtWithProvenance([]byte("ady\n"), 6, nil)
	if len(got) != 1 || got[0].Text != "ready" {
		t.Fatalf("matches = %+v, want ready", got)
	}
	if !provenance.ContainsWatch(got[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("match provenance = %+v, want carried watch_A/wg_1", got[0].Provenance)
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

// The window scanner does not wait for a terminator: an unterminated tail
// matches as soon as its bytes arrive, so there is nothing left to flush when
// the job ends.
func TestOutputMatcherMatchesUnterminatedTail(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	if got := m.Feed([]byte("all done")); len(got) != 1 || got[0] != "all done" {
		t.Fatalf("unterminated tail must match on Feed: %#v", got)
	}
	if got := m.Feed([]byte("\n")); len(got) != 0 {
		t.Fatalf("terminating the same tail must not re-fire: %#v", got)
	}
}

func TestOutputMatcherCarriesProvenanceOnUnterminatedTail(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	p := provenance.WithWatch(nil, "watch_A", "wg_1", "wd_1", "session_1", "job_1")
	got := m.FeedAtWithProvenance([]byte("ready"), 5, p)
	if len(got) != 1 || got[0].Text != "ready" {
		t.Fatalf("unterminated tail matches = %+v, want ready", got)
	}
	if !provenance.ContainsWatch(got[0].Provenance, "watch_A", "wg_1") {
		t.Fatalf("match provenance = %+v, want watch_A/wg_1", got[0].Provenance)
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

// An empty pattern matches at every position in the window, so it reports the
// line each of those positions falls on — including the unterminated tail.
func TestOutputMatcherEmptyRegexpReportsEveryLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(``))
	got := m.Feed([]byte("one\n\npartial"))
	if len(got) == 0 {
		t.Fatal("empty regexp must match")
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, want := range []string{"one", "", "partial"} {
		if !seen[want] {
			t.Fatalf("empty regexp excerpts = %#v, want one containing %q", got, want)
		}
	}
}

func TestOutputMatcherEmptyChunkAfterMatchDoesNotRefire(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`done`))
	if got := m.Feed([]byte("done")); len(got) != 1 {
		t.Fatalf("first feed must match: %#v", got)
	}
	if got := m.Feed(nil); len(got) != 0 {
		t.Fatalf("empty chunk must not re-match: %#v", got)
	}
}

// A CRLF producer's lines end where $ expects them to, and the reported
// excerpt does not carry the '\r'.
func TestOutputMatcherStripsCRLFBeforeMatching(t *testing.T) {
	m := NewOutputMatcher(mustCompileOutputMatch(t, `ready$`))
	got := m.Feed([]byte("server ready\r\n"))
	if len(got) != 1 || got[0] != "server ready" {
		t.Fatalf("CRLF line matches = %#v, want [\"server ready\"]", got)
	}
}

// However long a line grows, the retained window stays bounded — that bound is
// the matcher's whole memory footprint.
func TestOutputMatcherBoundsRetainedWindow(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte(strings.Repeat("x", 4*outputMatchWindowBytes))); len(got) != 0 {
		t.Fatalf("non-matching output must not match: %#v", got)
	}
	if len(m.carry) > outputMatchWindowBytes {
		t.Fatalf("window length = %d, want <= %d", len(m.carry), outputMatchWindowBytes)
	}
	// A match inside the same never-terminated line still fires.
	if got := m.Feed([]byte("ready")); len(got) != 1 {
		t.Fatalf("match inside a long line must fire: %#v", got)
	}
}

// The old line matcher dropped this whole line on the floor. The window scanner
// finds the token and reports a bounded excerpt of the line around it.
func TestOutputMatcherMatchesInsideAnUnterminatedOverlongLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	got := m.Feed([]byte(strings.Repeat("x", outputMatchWindowBytes+1) + "ready"))
	if len(got) != 1 {
		t.Fatalf("token in an unterminated overlong line fired %d times, want 1", len(got))
	}
	if len(got[0]) > outputMatchWindowBytes {
		t.Fatalf("excerpt is %d bytes, want <= %d", len(got[0]), outputMatchWindowBytes)
	}
	if !strings.HasSuffix(got[0], "ready") {
		t.Fatalf("excerpt %q does not end at the match", got[0])
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
	for i := range 2 {
		if got := m.FeedAt([]byte("ready\n"), 6); len(got) != 0 {
			t.Fatalf("feed %d: scan-covered line must never match: %#v", i, got)
		}
	}
	if got := m.FeedAt([]byte("ready again\n"), 18); len(got) != 1 || got[0] != "ready again" {
		t.Fatalf("line beyond the scan offset must still match: %#v", got)
	}
}

// SeedCarry replaces the window wholesale and accounts for everything already
// in it: the seeded bytes never fire, bytes appended after them do.
func TestOutputMatcherSeedCarryReplacesTheWindow(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	if got := m.Feed([]byte("stale partial")); len(got) != 0 {
		t.Fatalf("non-matching output must not match: %#v", got)
	}
	m.SeedCarry([]byte("already ready"))
	if got := m.Feed([]byte("\n")); len(got) != 0 {
		t.Fatalf("seeded (already-scanned) bytes must not fire: %#v", got)
	}
	if got := m.Feed([]byte("ready again\n")); len(got) != 1 || got[0] != "ready again" {
		t.Fatalf("output after the seed must fire: %#v", got)
	}
}

func TestOutputMatcherSeedCarryBoundsTheWindow(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	m.SeedCarry([]byte(strings.Repeat("x", 4*outputMatchWindowBytes)))
	if len(m.carry) > outputMatchWindowBytes {
		t.Fatalf("window length = %d, want <= %d", len(m.carry), outputMatchWindowBytes)
	}
	// The seeded line has no newline in it, so the match still sits on a line
	// longer than the window: the excerpt falls back to the match onward.
	if got := m.Feed([]byte("server ready\n")); len(got) != 1 || got[0] != "ready" {
		t.Fatalf("output after an oversized seed must still match: %#v", got)
	}
}

func TestScanRetainedReturnsLastMatchingLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`(?i)ready`))
	last, matched := m.ScanRetained([]byte("ready one\nnoise\nready two\nready three\n"))
	if !matched || last != "ready three" {
		t.Fatalf("ScanRetained = (%q, %v), want last matching line \"ready three\"", last, matched)
	}
}

// The level scan does not require a terminator: a job whose output ends without
// a newline is exactly the case this scanner exists to serve.
func TestScanRetainedMatchesUnterminatedFinalLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	last, matched := m.ScanRetained([]byte("starting\nserver ready"))
	if !matched || last != "server ready" {
		t.Fatalf("ScanRetained = (%q, %v), want \"server ready\"", last, matched)
	}
}

func TestScanRetainedFindsMatchInAnOverlongLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`ready`))
	// A line longer than the window is no longer skipped: the level scan reports
	// a bounded excerpt ending at the match.
	data := []byte(strings.Repeat("x", outputMatchWindowBytes+1) + "ready\n")
	last, matched := m.ScanRetained(data)
	if !matched {
		t.Fatal("match buried in an overlong line must be reported")
	}
	if len(last) > outputMatchWindowBytes || !strings.HasSuffix(last, "ready") {
		t.Fatalf("excerpt = %q (%d bytes), want a bounded excerpt ending at the match", last, len(last))
	}
	// A normal matching line after the overlong one wins as the LAST match.
	data = append(data, []byte("server ready\n")...)
	if last, matched = m.ScanRetained(data); !matched || last != "server ready" {
		t.Fatalf("ScanRetained = (%q, %v), want \"server ready\"", last, matched)
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
	p.WatchKeys[0].WatchID = "watch_mutated"
	if provenance.ContainsWatch(pending.Provenance, "watch_mutated", "wg_1") {
		t.Fatalf("pending provenance aliases source provenance: %+v", pending.Provenance)
	}
	if !provenance.ContainsWatch(pending.Provenance, "watch_A", "wg_1") {
		t.Fatalf("pending provenance changed after source mutation: %+v", pending.Provenance)
	}
}

// ---------------------------------------------------------------------------
// Byte-window scanner (WS3 T2). The matcher scans a rolling byte window over
// the raw stream instead of assembling lines, so output that never emits a
// newline — a progress bar, a JSON blob, a no-newline build log — can still
// fire a watch.
// ---------------------------------------------------------------------------

// (a) The incident: a job whose output is one enormous line. The line-assembly
// matcher dropped any line over the cap, so the token was silently never seen.
func TestOutputMatcherMatchesInsideOneEnormousLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	blob := strings.Repeat("x", 5000) + "READY" + strings.Repeat("y", 5000)
	var got []string
	for off := 0; off < len(blob); off += 1024 {
		end := min(off+1024, len(blob))
		got = append(got, m.Feed([]byte(blob[off:end]))...)
	}
	if len(got) != 1 {
		t.Fatalf("token inside a 10KB single line fired %d times, want 1: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "READY") {
		t.Fatalf("reported excerpt %q does not contain the match", got[0])
	}
}

// (b) A match straddling two chunks fires exactly once: the window carries the
// tail of the previous chunk, and the seam is not a match boundary.
func TestOutputMatcherMatchSpanningWindowSeamFiresOnce(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	if got := m.Feed([]byte(strings.Repeat("x", 5000) + "REA")); len(got) != 0 {
		t.Fatalf("half a token must not fire: %#v", got)
	}
	if got := m.Feed([]byte("DY" + strings.Repeat("y", 10))); len(got) != 1 {
		t.Fatalf("token spanning the seam fired %d times, want 1: %#v", len(got), got)
	}
	if got := m.Feed([]byte(strings.Repeat("z", 10))); len(got) != 0 {
		t.Fatalf("re-scanning the window must not re-fire: %#v", got)
	}
}

// (c) Offset dedup: a re-fed chunk, and a chunk overlapping one already fed,
// never fire a match the scanner has already reported.
func TestOutputMatcherRefedOverlappingWindowsDoNotDoubleFire(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	if got := m.FeedAt([]byte("a READY b"), 9); len(got) != 1 {
		t.Fatalf("first feed = %#v, want one match", got)
	}
	if got := m.FeedAt([]byte("a READY b"), 9); len(got) != 0 {
		t.Fatalf("an identical re-fed chunk must not fire: %#v", got)
	}
	// A chunk whose first bytes repeat already-scanned output contributes only
	// its suffix, and the repeated match does not fire again.
	if got := m.FeedAt([]byte("READY b c"), 14); len(got) != 0 {
		t.Fatalf("overlapping re-feed must not double-fire: %#v", got)
	}
	if got := m.FeedAt([]byte(" READY d"), 22); len(got) != 1 {
		t.Fatalf("genuinely new match must still fire: %#v", got)
	}
}

// (d) Anchor semantics. Patterns are compiled multiline, so ^/$ anchor at
// newlines; the window edge also counts as a text boundary, so ^/$ can anchor
// there even mid-line. Both halves are pinned here.
func TestOutputMatcherAnchorsAreMultiline(t *testing.T) {
	re, err := CompileOutputMatch(`^READY$`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m := NewOutputMatcher(re)
	if got := m.Feed([]byte("noise\nREADY\nmore\n")); len(got) != 1 || got[0] != "READY" {
		t.Fatalf("multiline anchors = %#v, want [\"READY\"]", got)
	}
}

func TestOutputMatcherDollarAnchorsAtTheGrowingWindowEnd(t *testing.T) {
	re, err := CompileOutputMatch(`^done$`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m := NewOutputMatcher(re)
	// The stream has produced "done" with no newline yet. The end of the scan
	// window counts as end-of-text, so $ anchors there and the watch fires now
	// rather than waiting for a terminator that may never come.
	if got := m.Feed([]byte("done")); len(got) != 1 || got[0] != "done" {
		t.Fatalf("$ at the growing window end = %#v, want [\"done\"]", got)
	}
	// More output arrives on the same line: the earlier fire is not repeated,
	// and the now-longer line does not match.
	if got := m.Feed([]byte("zo\n")); len(got) != 0 {
		t.Fatalf("extending the line must not re-fire: %#v", got)
	}
}

func TestOutputMatcherCaretAnchorsAtTheWindowStart(t *testing.T) {
	re, err := CompileOutputMatch(`^x+READY$`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	m := NewOutputMatcher(re)
	// The line is longer than the window, so the window start lands mid-line.
	// ^ anchors at the window start: the fire is deliberate, documented, and the
	// price of never dropping a long line on the floor.
	if got := m.Feed([]byte(strings.Repeat("x", outputMatchWindowBytes) + "READY")); len(got) != 0 {
		t.Fatalf("unterminated line must not match ^x+READY$ yet: %#v", got)
	}
	if got := m.Feed([]byte("\n")); len(got) != 1 {
		t.Fatalf("^ at the window start = %#v, want one match", got)
	}
}

// (e) Match-length bound: the window is the stated limit on how long a single
// match may be. A longer match is not reported.
func TestOutputMatcherMatchLongerThanWindowDoesNotFire(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`A[^B]*B`))
	long := "A" + strings.Repeat("-", outputMatchWindowBytes) + "B"
	if got := m.Feed([]byte(long)); len(got) != 0 {
		t.Fatalf("match longer than the window must not fire: %#v", got)
	}
	short := "A" + strings.Repeat("-", 10) + "B"
	if got := m.Feed([]byte(short)); len(got) != 1 || got[0] != short {
		t.Fatalf("match within the window bound = %#v, want %q", got, short)
	}
}

// (f) The attach-time level scan uses the same windowing and the same bound, so
// attach and stream agree on what can match.
func TestScanRetainedFindsMatchInsideOneEnormousLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	data := []byte(strings.Repeat("x", 5000) + "READY" + strings.Repeat("y", 5000))
	last, matched := m.ScanRetained(data)
	if !matched {
		t.Fatal("attach scan must find a token inside a 10KB single line")
	}
	if !strings.Contains(last, "READY") {
		t.Fatalf("attach excerpt %q does not contain the match", last)
	}
}

func TestScanRetainedSkipsMatchLongerThanWindow(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`A[^B]*B`))
	data := []byte("A" + strings.Repeat("-", outputMatchWindowBytes) + "B\n")
	if last, matched := m.ScanRetained(data); matched {
		t.Fatalf("attach scan must honour the same match-length bound, got %q", last)
	}
}

// Attach then stream: a long-line match already present at attach fires once
// through the level scan and is not re-fired by the live path that re-scans the
// same bytes inside its window.
func TestAttachScanThenStreamDoesNotDoubleFireLongLine(t *testing.T) {
	m := NewOutputMatcher(regexp.MustCompile(`READY`))
	retained := []byte(strings.Repeat("x", 5000) + "READY" + strings.Repeat("y", 100))
	m.SetScanOffset(int64(len(retained)))
	m.SeedCarry(retained)
	if _, matched := m.ScanRetained(retained); !matched {
		t.Fatal("attach scan must fire on the retained long line")
	}
	if got := m.FeedAt([]byte("zzz"), int64(len(retained))+3); len(got) != 0 {
		t.Fatalf("live path must not re-fire the attach-scanned match: %#v", got)
	}
	if got := m.FeedAt([]byte(" READY again"), int64(len(retained))+15); len(got) != 1 {
		t.Fatalf("a new match after attach must fire: %#v", got)
	}
}

func mustCompileOutputMatch(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := CompileOutputMatch(pattern)
	if err != nil {
		t.Fatalf("CompileOutputMatch(%q): %v", pattern, err)
	}
	return re
}
