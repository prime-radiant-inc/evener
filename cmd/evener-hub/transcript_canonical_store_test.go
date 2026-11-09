package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
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
	if err := store.putItem("item", appwire.ThreadItem{ID: "item"}); err == nil {
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
	if err := store.putItem(first, appwire.ThreadItem{
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
