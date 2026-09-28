package hostops

import (
	"testing"
)

// multiFenceStoreJSON is a corrupt store carrying two open fences for one host:
// the newer generation is the host's synthesized current pair, while the older
// fence is a row a detail lookup must still be able to address directly.
const multiFenceStoreJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":3,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1-old","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-old"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h1-new","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":4,"incarnationId":"inc-h1b","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-new"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000003","clientOperationId":"op-h9","host":"h9","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h9","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`

// TestOperationsDetailResolvesANonNewestFenceOnAQuarantinedHost is the round-four
// L1 test: a host-pinned detail lookup of a mirror-less host addresses the named
// row directly, even when the host carries a newer open fence that owns the
// synthesized current pair.
func TestOperationsDetailResolvesANonNewestFenceOnAQuarantinedHost(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, multiFenceStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Both fences import under their original ids.
	if _, ok := store.Record("00000000000000000001"); !ok {
		t.Fatalf("the older fence was not imported: %+v", store.Records())
	}

	page, err := store.ReadOperations(OperationsQuery{Host: "h1", ID: "00000000000000000001"})
	if err != nil {
		t.Fatalf("host-pinned detail lookup of the older fence: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].ID != "00000000000000000001" {
		t.Fatalf("host-pinned detail lookup = %+v, want the older fence row", page.Records)
	}
	// The row's own pair is what the response carries for a detail lookup.
	if page.Generation == nil || *page.Generation != 3 || page.IncarnationID != "inc-h1" {
		t.Fatalf("the detail page pair = %v/%q, want the addressed row's own 3/inc-h1", page.Generation, page.IncarnationID)
	}

	// An unfiltered detail lookup agrees.
	unfiltered, err := store.ReadOperations(OperationsQuery{ID: "00000000000000000001"})
	if err != nil {
		t.Fatalf("unfiltered detail lookup: %v", err)
	}
	if len(unfiltered.Records) != 1 || unfiltered.Records[0].ID != "00000000000000000001" {
		t.Fatalf("unfiltered detail lookup = %+v, want the older fence row", unfiltered.Records)
	}

	// A host-pinned list (no id filter) still lists both rows under their own
	// pairs: the newest fence's pair is the current one, and its older sibling
	// stays addressable but is not listed under a pair it never ran under.
	list, err := store.ReadOperations(OperationsQuery{Host: "h1"})
	if err != nil {
		t.Fatalf("host-pinned list: %v", err)
	}
	if len(list.Records) != 1 || list.Records[0].ID != "00000000000000000002" {
		t.Fatalf("host-pinned list = %+v, want the newest fence listed under the current pair", list.Records)
	}
}
