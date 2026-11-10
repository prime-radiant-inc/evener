package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func TestTranscriptCanonicalStoreEvictsLeastRecentlyUsedBytes(t *testing.T) {
	store := newTranscriptCanonicalStore(6)
	store.put("first", []byte("aa"))
	store.put("second", []byte("bb"))
	if _, ok := store.get("first"); !ok {
		t.Fatal("first entry missing before capacity is exceeded")
	}

	store.put("third", []byte("ccc"))

	if _, ok := store.get("second"); ok {
		t.Fatal("least-recently-used entry survived over capacity")
	}
	if _, ok := store.get("first"); !ok {
		t.Fatal("recently-read entry was evicted")
	}
	if _, ok := store.get("third"); !ok {
		t.Fatal("new entry was evicted")
	}
	if store.bytes > store.maxBytes {
		t.Fatalf("retained bytes = %d, max = %d", store.bytes, store.maxBytes)
	}
}

func TestTranscriptCanonicalStoreCopiesValues(t *testing.T) {
	store := newTranscriptCanonicalStore(16)
	input := []byte("abc")
	store.put("item", input)
	input[0] = 'x'

	first, ok := store.get("item")
	if !ok {
		t.Fatal("stored entry missing")
	}
	first[1] = 'y'
	second, ok := store.get("item")
	if !ok {
		t.Fatal("stored entry missing after read")
	}
	if string(second) != "abc" {
		t.Fatalf("stored entry = %q, want immutable copy", second)
	}
}

func TestTranscriptCanonicalStoreDuplicateKeyKeepsFirstBytes(t *testing.T) {
	store := newTranscriptCanonicalStore(16)
	store.put("item", []byte("first"))
	store.put("item", []byte("second"))

	got, ok := store.get("item")
	if !ok {
		t.Fatal("stored entry missing")
	}
	if string(got) != "first" {
		t.Fatalf("stored entry = %q, want first canonical bytes", got)
	}
}

func TestTranscriptCanonicalStoreRejectsOversizeItem(t *testing.T) {
	store := newTranscriptCanonicalStore(1)
	if err := store.putItem("item", "triple", appwire.ThreadItem{ID: "item"}); err == nil {
		t.Fatal("oversize canonical item was accepted")
	}
	if _, ok := store.get("item"); ok {
		t.Fatal("oversize canonical item was retained")
	}
}

func TestEvictedFetchTokenIsNeverReissued(t *testing.T) {
	store := newTranscriptCanonicalStore(256)
	triple := "local:thread\x00view\x001\x000\x001"
	first := store.tokenFor(triple)
	if err := store.putItem(first, triple, appwire.ThreadItem{
		ID: "item", Type: "agentMessage", TurnID: "turn-1", Version: 1, Text: "visible",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.borrow(first); !ok {
		t.Fatal("retained entry missing")
	}
	store.put("other", make([]byte, 256))
	if _, ok := store.borrow(first); ok {
		t.Fatal("entry was not evicted")
	}
	if second := store.tokenFor(triple); second == first {
		t.Fatalf("evicted token %q was reissued, so an outstanding cursor could revive", first)
	}
}

func TestRetiredFetchTokenIsNotRevivedByADelayedRetention(t *testing.T) {
	store := newTranscriptCanonicalStore(64)
	triple := "local:thread\x00view\x002\x000\x002"
	token := store.tokenFor(triple)
	// Retaining bytes past the budget evicts the entry immediately, retiring the
	// token exactly as an eviction between a concurrent read's mint and its
	// retained insert does.
	store.putOwnedTriple(token, triple, make([]byte, 80))
	if _, ok := store.borrow(token); ok {
		t.Fatal("retained entry survived over the byte budget")
	}
	if err := store.putItem(token, triple, appwire.ThreadItem{ID: "delayed", Type: "agentMessage"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.borrow(token); ok {
		t.Fatal("a delayed retention revived an evicted token, so an outstanding cursor could resume a different encoding")
	}
}

func TestCanonicalizingCandidatesRegistersOnlyRetainedEntries(t *testing.T) {
	operations := &sessionOperations{}
	options := transcriptWindowOptionsFor(transcriptItemCandidateResult{}, appwire.ThreadWindowParams{}, 1,
		transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon)
	operations.retainCanonicalWindowOptions(&options)
	candidate := appitempaging.TranscriptItemCandidate{
		Item:     appwire.ThreadItem{ID: "item", Type: "agentMessage", Version: 1},
		Position: appwire.ThreadItemPosition{Entry: 1},
	}
	canonical, err := options.CanonicalizeCandidates([]appitempaging.TranscriptItemCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 {
		t.Fatalf("canonical candidates = %d, want the untouched candidate", len(canonical))
	}
	store := operations.canonicalItemStore()
	store.mu.Lock()
	registered := len(store.live)
	store.mu.Unlock()
	if registered != 0 {
		t.Fatalf("canonicalizing a never-retained candidate registered %d live entries", registered)
	}
}

func TestRetainedCursorMemoFollowsATokenChange(t *testing.T) {
	operations := &sessionOperations{}
	result := transcriptItemCandidateResult{Identity: appitempaging.CursorIdentity{ThreadRef: "local:thread", Incarnation: "memo-view", ProjectionVersion: 1}}
	options := transcriptWindowOptionsFor(result, appwire.ThreadWindowParams{}, 1,
		transcriptWindowAppliedByServingDaemon, appitempaging.FetchCursorOriginServingDaemon)
	operations.retainCanonicalWindowOptions(&options)
	candidate := appitempaging.TranscriptItemCandidate{
		TurnID:   "turn-1",
		Item:     appwire.ThreadItem{ID: "item", Type: "agentMessage", Version: 1},
		Position: appwire.ThreadItemPosition{Entry: 1},
	}
	first, err := options.MintFetchCursor(candidate, "0123456789abcdef-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := options.MintFetchCursor(candidate, "0123456789abcdef-2")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("a retried pack after eviction received the cursor for the retired token")
	}
}
