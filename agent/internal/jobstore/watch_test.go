package jobstore

import (
	"regexp"
	"strings"
	"testing"
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
