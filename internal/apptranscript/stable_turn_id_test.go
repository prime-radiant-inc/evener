package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// TestBoundedReadsHonorPersistedStableTurnIDs pins the one rule both persisted
// projection paths follow: a persisted StableTurnID is the turn's id, and entry
// -index numbering is only the fallback for entries that carry none.
//
// The full reader (TurnsFromFile) has always honored it. The bounded reader
// numbered purely by entry index, so the SAME dormant session answered with
// different turn ids depending on whether the read was windowed — and since
// reserved client-mutation ids live in their own "turn_mN" namespace (kata
// rk09), a windowed read renamed a turn_mN entry to an unrelated turn_N.
func TestBoundedReadsHonorPersistedStableTurnIDs(t *testing.T) {
	older := appwire.ClientMutationTurnID(2)
	newer := appwire.ClientMutationTurnID(5)
	path := writeEntries(t,
		userEntry(1, "in-0"),
		assistantTextEntry(2, "out-0"),
		reservedUserEntry(3, "in-1", older),
		assistantTextEntry(4, "out-1"),
		userEntry(5, "in-2"),
		assistantTextEntry(6, "out-2"),
		reservedUserEntry(7, "in-3", newer),
		assistantTextEntry(8, "out-3"),
	)

	full := requireTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	wantIDs := []string{"turn_1", "turn_2", older, "turn_4", "turn_5", "turn_6", newer, "turn_8"}
	if gotIDs := turnIDs(full); !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("full read turn IDs = %v, want %v", gotIDs, wantIDs)
	}

	cache := NewTurnCache()
	latest, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, 3, boundedTestProjector)
	wantLatest, wantCursor := appwire.WindowTurns(full, 3)
	if gotIDs := turnIDs(latest); !reflect.DeepEqual(gotIDs, turnIDs(wantLatest)) {
		t.Fatalf("windowed read turn IDs = %v, want the full read's %v", gotIDs, turnIDs(wantLatest))
	}
	if !reflect.DeepEqual(latest, wantLatest) || cursor != wantCursor {
		t.Fatalf("windowed read = (%#v, %q), want the full read's (%#v, %q)", latest, cursor, wantLatest, wantCursor)
	}

	page := requirePageFromFile(t, cache, path, testMaxLineBytes, cursor, 3, boundedTestProjector)
	wantPage := appwire.PageTurns(full, cursor, 3)
	if gotIDs := turnIDs(page.Turns); !reflect.DeepEqual(gotIDs, turnIDs(wantPage.Data)) {
		t.Fatalf("paged read turn IDs = %v, want the full read's %v", gotIDs, turnIDs(wantPage.Data))
	}
	if !reflect.DeepEqual(page.Turns, wantPage.Data) || page.NextCursor != wantPage.NextCursor {
		t.Fatalf("paged read = (%#v, %q), want the full read's (%#v, %q)", page.Turns, page.NextCursor, wantPage.Data, wantPage.NextCursor)
	}
}

// TestBoundedReadsMatchTheFullReadOnDuplicatePersistedTurnIDs covers the legacy
// data shape from before reserved ids were namespaced: a persisted id minted
// INSIDE the entry-index namespace, which collides with the entry that owns
// that number by position (kata 21r6).
//
// Whatever the two paths render for it — today both keep the persisted id and
// so publish "turn_11" twice — they must render it the SAME way. A session
// whose ids depend on how it was read is a worse failure than a session with a
// duplicate id: the duplicate is at least stable across a scroll.
func TestBoundedReadsMatchTheFullReadOnDuplicatePersistedTurnIDs(t *testing.T) {
	entries := make([]transcript.Entry, 0, 12)
	for seq := 1; seq <= 12; seq++ {
		if seq == 3 {
			entries = append(entries, reservedUserEntry(seq, "in-legacy", "turn_11"))
			continue
		}
		entries = append(entries, userEntry(seq, "in"))
	}
	path := writeEntries(t, entries...)

	full := requireTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	occurrences := 0
	for _, id := range turnIDs(full) {
		if id == "turn_11" {
			occurrences++
		}
	}
	if occurrences != 2 {
		t.Fatalf("full read turn IDs = %v, want the persisted turn_11 to collide with entry 11's own number", turnIDs(full))
	}

	cache := NewTurnCache()
	latest, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, 4, boundedTestProjector)
	wantLatest, wantCursor := appwire.WindowTurns(full, 4)
	if !reflect.DeepEqual(latest, wantLatest) || cursor != wantCursor {
		t.Fatalf("windowed read IDs = %v (cursor %q), want the full read's %v (cursor %q)", turnIDs(latest), cursor, turnIDs(wantLatest), wantCursor)
	}

	for cursor != "" {
		page := requirePageFromFile(t, cache, path, testMaxLineBytes, cursor, 4, boundedTestProjector)
		wantPage := appwire.PageTurns(full, cursor, 4)
		if !reflect.DeepEqual(page.Turns, wantPage.Data) || page.NextCursor != wantPage.NextCursor {
			t.Fatalf("paged read at cursor %q = %v (next %q), want the full read's %v (next %q)",
				cursor, turnIDs(page.Turns), page.NextCursor, turnIDs(wantPage.Data), wantPage.NextCursor)
		}
		cursor = page.NextCursor
	}
}

// TestBoundedReadNamesAnEntryTheSameWayIndexingAndProjecting closes the bounded
// reader's own seam. It calls the projector twice for an entry — once while
// indexing, to decide whether the entry is visible at all, and again when the
// entry falls inside a requested range — and both calls have to name the turn
// the same way. Naming it one way for the visibility decision and another for
// the rendering leaves the two halves of one read disagreeing about which turn
// they are talking about.
func TestBoundedReadNamesAnEntryTheSameWayIndexingAndProjecting(t *testing.T) {
	reserved := appwire.ClientMutationTurnID(2)
	path := writeEntries(t,
		userEntry(1, "in-0"),
		assistantTextEntry(2, "out-0"),
		reservedUserEntry(3, "the answer", reserved),
		assistantTextEntry(4, "out-1"),
	)

	seen := map[int][]string{}
	recording := func(turn schema.Turn, turnID string, turnIndex int, toolNames map[string]string) []appwire.ThreadItem {
		seen[turnIndex] = append(seen[turnIndex], turnID)
		return boundedTestProjector(turn, turnID, turnIndex, toolNames)
	}
	requireLatestFromFile(t, NewTurnCache(), path, testMaxLineBytes, 4, recording)

	for index, ids := range seen {
		for _, id := range ids {
			if id != ids[0] {
				t.Fatalf("entry %d was projected under %v; the index scan and the range reader must name it the same turn", index, ids)
			}
		}
	}
	if ids := seen[3]; len(ids) == 0 || ids[0] != reserved {
		t.Fatalf("entry 3 was projected under %v, want its persisted %q", ids, reserved)
	}
}

func assistantTextEntry(seq int, text string) transcript.Entry {
	return transcript.Entry{Kind: "entry", Seq: seq, Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(text)}}
}

func reservedUserEntry(seq int, text, stableTurnID string) transcript.Entry {
	entry := userEntry(seq, text)
	entry.Turn.StableTurnID = stableTurnID
	return entry
}
