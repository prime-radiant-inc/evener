package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/rendezvous"
)

func TestUnixTime(t *testing.T) {
	if got := UnixTime(0); !got.IsZero() {
		t.Fatalf("UnixTime(0)=%v, want zero", got)
	}
	if got := UnixTime(-5); !got.IsZero() {
		t.Fatalf("UnixTime(-5)=%v, want zero", got)
	}
	got := UnixTime(1_700_000_000)
	if got.IsZero() || got.Unix() != 1_700_000_000 {
		t.Fatalf("UnixTime(1700000000)=%v", got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("UnixTime should be UTC, got %v", got.Location())
	}
}

func TestUnixSeconds(t *testing.T) {
	if got := UnixSeconds(time.Time{}); got != 0 {
		t.Fatalf("UnixSeconds(zero)=%d, want 0", got)
	}
	ts := time.Unix(1_700_000_000, 0)
	if got := UnixSeconds(ts); got != 1_700_000_000 {
		t.Fatalf("UnixSeconds=%d", got)
	}
}

func TestAppwireThreadLess(t *testing.T) {
	newer := appwire.Thread{ID: "a", Name: "Alpha", UpdatedAt: 200, CreatedAt: 100}
	older := appwire.Thread{ID: "b", Name: "Beta", UpdatedAt: 100, CreatedAt: 100}
	// More recently updated threads sort first (Less == true).
	if !AppwireThreadLess(newer, older) {
		t.Fatal("newer thread should sort before older")
	}
	if AppwireThreadLess(older, newer) {
		t.Fatal("older thread must not sort before newer")
	}

	// Equal timestamps fall back to case-insensitive title order.
	x := appwire.Thread{ID: "x", Name: "apple", UpdatedAt: 100, CreatedAt: 100}
	y := appwire.Thread{ID: "y", Name: "banana", UpdatedAt: 100, CreatedAt: 100}
	if !AppwireThreadLess(x, y) {
		t.Fatal("apple should sort before banana at equal timestamps")
	}
}

func TestAppwireThreadOrderKeyTitleFallback(t *testing.T) {
	// Name wins when present.
	if got := appwireThreadOrderKey(appwire.Thread{Name: "N", Preview: "P", SessionID: "S"}); got.title != "N" {
		t.Fatalf("title=%q, want Name", got.title)
	}
	// Preview is the fallback when Name is empty.
	if got := appwireThreadOrderKey(appwire.Thread{Preview: "P", SessionID: "S"}); got.title != "P" {
		t.Fatalf("title=%q, want Preview", got.title)
	}
	// SessionID is the last resort.
	if got := appwireThreadOrderKey(appwire.Thread{SessionID: "S"}); got.title != "S" {
		t.Fatalf("title=%q, want SessionID", got.title)
	}
	// ID wins for the id field, falling back to SessionID.
	if got := appwireThreadOrderKey(appwire.Thread{SessionID: "S"}); got.id != "S" {
		t.Fatalf("id=%q, want SessionID fallback", got.id)
	}
}

func liveEntry(sessionID, address string, started time.Time) LiveEntry {
	return LiveEntry{
		Entry:     rendezvous.Entry{Address: address, StartedAt: started},
		SessionID: sessionID,
	}
}

func TestLiveEntryWithPastLessNilIndex(t *testing.T) {
	newer := liveEntry("s1", "addr1", time.Unix(200, 0))
	older := liveEntry("s2", "addr2", time.Unix(100, 0))
	// With no past index, ordering uses the live entry's own StartedAt.
	if !LiveEntryWithPastLess(newer, older, nil) {
		t.Fatal("more recently started entry should sort first")
	}
}

func TestLiveEntryWithPastLessUsesPastMeta(t *testing.T) {
	// A past index whose meta for s1 is much newer must reorder s1 ahead of s2
	// even though s1's live StartedAt is older — the past meta is authoritative.
	past := &PastIndex{
		byID: map[string]PastEntry{
			"s1": {ID: "s1", Meta: schema.SessionMeta{ID: "s1", UpdatedAt: time.Unix(9000, 0), CreatedAt: time.Unix(9000, 0)}},
		},
	}
	s1 := liveEntry("s1", "addr1", time.Unix(1, 0))    // live start is ancient
	s2 := liveEntry("s2", "addr2", time.Unix(5000, 0)) // no past entry, falls back to StartedAt

	if !LiveEntryWithPastLess(s1, s2, past) {
		t.Fatal("s1 with a newer past meta should sort before s2")
	}
	// Confirm the fallback path is what makes the difference: without the past
	// index, s2 (newer live start) sorts first instead.
	if LiveEntryWithPastLess(s1, s2, nil) {
		t.Fatal("without past index, ancient-start s1 must not sort before s2")
	}
}

func TestLiveEntryOrderKeyFallbackID(t *testing.T) {
	// Fallback id preference: SessionID, then ThreadID, then Address.
	le := LiveEntry{Entry: rendezvous.Entry{Address: "addr", ThreadID: "th"}}
	if got := liveEntryFallbackOrderKey(le); got.id != "th" {
		t.Fatalf("id=%q, want ThreadID when SessionID empty", got.id)
	}
	le = LiveEntry{Entry: rendezvous.Entry{Address: "addr"}}
	if got := liveEntryFallbackOrderKey(le); got.id != "addr" {
		t.Fatalf("id=%q, want Address as last resort", got.id)
	}
}
