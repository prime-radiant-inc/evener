package apptranscript

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

// itemKey builds the canonical transcript key the way production does, so the
// assertions spell out the turn id and ordinals while still pinning the exact
// wire key format.
func itemKey(turnID string, entry uint64, item uint32) string {
	return appitempaging.TranscriptItemKey(turnID, appwire.ThreadItemPosition{Entry: entry, Item: item})
}

// The logical-turn grouping contract: one live turn spans several transcript
// entries (a user input followed by assistant, tool-call, and tool-result
// entries), and the live snapshot allocates that turn ONE entry ordinal and
// items numbered across the whole turn. The file projection must reproduce
// those keys, or a live-rendered item vanishes on reload and the frontend --
// which reconciles by transcriptKey -- duplicates it (design: "Transcript key:
// stable item identity reproduced by live event projection and later file
// projection").
//
// The fixtures mirror what a session actually persists for logical turns
// opened by client-mutation user inputs (StableTurnID set on the user entry
// only): entries 1..2 for the first turn and 3..6 for the second, so the
// ordinal drift compounds exactly the way the second probe observed
// (entries-per-turn minus one per turn).

func logicalTurnFixture() []transcript.Entry {
	return []transcript.Entry{
		// Logical turn 1: reserved user input + assistant continuation.
		reservedUserEntry(1, "first question", "turn_m1"),
		assistantTextEntry(2, "first answer"),
		// Logical turn 2: reserved user input + tool call + result + assistant.
		reservedUserEntry(3, "second question", "turn_m2"),
		assistantToolCallEntry(4, "call_a", "read_file", `{"path":"a"}`),
		toolResultEntry(5, "call_a", "read_file", "line 1"),
		assistantTextEntry(6, "second answer"),
	}
}

// TestLegacyReadersKeepOneTurnPerDecodedEntry pins the pre-item-paging API
// contract. Logical grouping is an additive item-mode projection; callers of
// TurnsFromFile and TurnsFromEntries still receive one projected turn for each
// visible decoded entry.
func TestLegacyReadersKeepOneTurnPerDecodedEntry(t *testing.T) {
	entries := []transcript.Entry{
		userEntry(1, "question"),
		assistantTextEntry(2, "answer"),
	}
	path := writeEntries(t, entries...)

	fromFile := requireTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())
	fromEntries, err := TurnsFromEntries(transcript.Header{}, entries, sequentialTestProjector())
	if err != nil {
		t.Fatalf("TurnsFromEntries: %v", err)
	}
	for _, read := range []struct {
		name  string
		turns []appwire.Turn
	}{
		{name: "file", turns: fromFile},
		{name: "entries", turns: fromEntries},
	} {
		if got, want := turnIDs(read.turns), []string{"turn_1", "turn_2"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s legacy turn ids = %v, want one turn per decoded entry %v", read.name, got, want)
		}
	}
}

// TestFileProjectionReproducesLiveLogicalTurnKeys is the differential oracle
// for F3: the file projection of a persisted logical turn must yield the same
// turn id, entry ordinal, and item ordinals the live snapshot would have
// allocated for the same items.
//
// Live allocation (appTurnSnapshot semantics, reproduced from the projector's
// notification stream): each logical turn consumes one entry ordinal; items
// are numbered 0..n-1 in arrival order. With no prelude, turn 1 opens at entry
// ordinal 0 (user item 0, assistant item 1); turn 2 opens at entry ordinal 1
// (user item 0, merged tool item 1, final assistant item 2).
func TestFileProjectionReproducesLiveLogicalTurnKeys(t *testing.T) {
	path := writeEntries(t, logicalTurnFixture()...)

	turns := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())

	// The persisted projection must group both logical turns under their
	// reserved openers' ids, not fall back to per-entry turn ids.
	gotIDs := turnIDs(turns)
	wantIDs := []string{"turn_m1", "turn_m2"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("file projection turn ids = %v, want the logical turns' ids %v", gotIDs, wantIDs)
	}

	if len(turns) != 2 {
		t.Fatalf("file projection produced %d turns, want 2 logical turns", len(turns))
	}

	// Turn 1: user at (0,0), assistant at (0,1).
	first := keysFor(turns[0])
	wantFirst := []string{itemKey("turn_m1", 0, 0), itemKey("turn_m1", 0, 1)}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("turn_m1 keys = %v, want %v", first, wantFirst)
	}

	// Turn 2: user at (1,0); the tool call and its result must merge into ONE
	// item at (1,1); the closing assistant message at (1,2).
	second := keysFor(turns[1])
	wantSecond := []string{itemKey("turn_m2", 1, 0), itemKey("turn_m2", 1, 1), itemKey("turn_m2", 1, 2)}
	if !reflect.DeepEqual(second, wantSecond) {
		t.Fatalf("turn_m2 keys = %v, want %v (tool call and result share one item)", second, wantSecond)
	}
}

// TestFileProjectionReproducesLiveLogicalTurnKeysWithoutStableIDs covers the
// daemon-minted namespace: a logical turn whose opener carries no StableTurnID
// still groups, and its turn id stays the opener's fallback id
// (turn_<opening entry index>), matching persistedTurnID's fallback rule.
func TestFileProjectionReproducesLiveLogicalTurnKeysWithoutStableIDs(t *testing.T) {
	path := writeEntries(t,
		userEntry(1, "first question"),
		assistantTextEntry(2, "first answer"),
		userEntry(3, "second question"),
		assistantTextEntry(4, "second answer"),
	)

	turns := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())

	gotIDs := turnIDs(turns)
	wantIDs := []string{"turn_1", "turn_3"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("file projection turn ids = %v, want openers' fallback ids %v", gotIDs, wantIDs)
	}

	// One entry ordinal per logical turn: turn_1 at 0, turn_3 at 1.
	first := keysFor(turns[0])
	wantFirst := []string{itemKey("turn_1", 0, 0), itemKey("turn_1", 0, 1)}
	if !reflect.DeepEqual(first, wantFirst) {
		t.Fatalf("turn_1 keys = %v, want %v", first, wantFirst)
	}
	second := keysFor(turns[1])
	wantSecond := []string{itemKey("turn_3", 1, 0), itemKey("turn_3", 1, 1)}
	if !reflect.DeepEqual(second, wantSecond) {
		t.Fatalf("turn_3 keys = %v, want %v", second, wantSecond)
	}
}

// TestBoundedReadsMatchGroupedFileProjection holds the bounded reader to the
// same grouping as the full read: whatever the full read renders for a
// transcript, windowed and paged reads must render exactly the same turns.
func TestBoundedReadsMatchGroupedFileProjection(t *testing.T) {
	path := writeEntries(t, logicalTurnFixture()...)
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())

	cache := NewTurnCache()
	latest, cursor := requireLatestFromFile(t, cache, path, testMaxLineBytes, 1, boundedTestProjector)
	wantLatest, wantCursor := appwire.WindowTurns(full, 1)
	if !reflect.DeepEqual(latest, wantLatest) || cursor != wantCursor {
		t.Fatalf("windowed read = (%#v, %q), want the full read's (%#v, %q)", latest, cursor, wantLatest, wantCursor)
	}

	for cursor != "" {
		page := requirePageFromFile(t, cache, path, testMaxLineBytes, cursor, 1, boundedTestProjector)
		wantPage := appwire.PageTurns(full, cursor, 1)
		if !reflect.DeepEqual(page.Turns, wantPage.Data) || page.NextCursor != wantPage.NextCursor {
			t.Fatalf("paged read at cursor %q = (%#v, next %q), want the full read's (%#v, next %q)",
				cursor, page.Turns, page.NextCursor, wantPage.Data, wantPage.NextCursor)
		}
		cursor = page.NextCursor
	}
}

// TestItemWindowKeysMatchGroupedFileProjection holds the item-paging path to
// the same logical-turn keys as the full read: the saved-index candidate
// window must answer with the exact TranscriptKeys the full projection yields
// for the same items, or a cursor minted by one path stales in the other.
func TestItemWindowKeysMatchGroupedFileProjection(t *testing.T) {
	path := writeEntries(t, logicalTurnFixture()...)
	full := requireItemTurnsFromFile(t, path, testMaxLineBytes, sequentialTestProjector())

	cache := NewTurnCache()
	window, _, err := cache.LatestItemWindowFromFile(path, testMaxLineBytes, ItemWindowOptions{
		ThreadRef: "local:th_1",
		Limit:     10,
	}, boundedTestProjector)
	if err != nil {
		t.Fatalf("latest item window: %v", err)
	}

	var wantKeys []string
	for _, turn := range full {
		for _, item := range turn.Items {
			wantKeys = append(wantKeys, item.TranscriptKey)
		}
	}
	var gotKeys []string
	for _, candidate := range window.Candidates {
		gotKeys = append(gotKeys, candidate.Item.TranscriptKey)
	}
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("item window keys = %v, want the full projection's %v", gotKeys, wantKeys)
	}
}

func keysFor(turn appwire.Turn) []string {
	keys := make([]string, 0, len(turn.Items))
	for _, item := range turn.Items {
		keys = append(keys, item.TranscriptKey)
	}
	return keys
}

func requireItemTurnsFromFile(t testing.TB, path string, maxLineBytes int, project EntryProjector) []appwire.Turn {
	t.Helper()
	turns, err := ItemTurnsFromFile(path, maxLineBytes, project)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	return turns
}
