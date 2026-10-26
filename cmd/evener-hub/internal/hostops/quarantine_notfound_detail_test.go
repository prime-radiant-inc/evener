package hostops

import (
	"testing"
)

// TestOperationsNotFoundDetailMatchesMirroredAndUnmirroredHosts pins the final
// Low: a host-pinned detail lookup that matches no row reports the same pair
// (0/"") whether the host is mirrored or a custody import. Only a *found* detail
// row echoes its own pair.
func TestOperationsNotFoundDetailMatchesMirroredAndUnmirroredHosts(t *testing.T) {
	mirrored, _ := openTestStore(t)
	mirrorCursorBoundary(t, mirrored, "m1", 2, "inc-m1", 3)
	miss, err := mirrored.ReadOperations(OperationsQuery{Host: "m1", ID: "00000000000000000009"})
	if err != nil {
		t.Fatalf("mirrored not-found detail: %v", err)
	}
	if len(miss.Records) != 0 || miss.Generation == nil || *miss.Generation != 0 || miss.IncarnationID != "" {
		t.Fatalf("mirrored not-found detail = %+v (pair %v/%q), want no rows and 0/\"\"",
			miss.Records, miss.Generation, miss.IncarnationID)
	}

	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, quarantinedStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	unmirroredMiss, err := store.ReadOperations(OperationsQuery{Host: "h1", ID: "00000000000000000009"})
	if err != nil {
		t.Fatalf("unmirrored not-found detail: %v", err)
	}
	if len(unmirroredMiss.Records) != 0 || unmirroredMiss.Generation == nil || *unmirroredMiss.Generation != 0 ||
		unmirroredMiss.IncarnationID != "" {
		t.Fatalf("unmirrored not-found detail = %+v (pair %v/%q), want no rows and 0/\"\" like a mirrored host",
			unmirroredMiss.Records, unmirroredMiss.Generation, unmirroredMiss.IncarnationID)
	}

	found, err := store.ReadOperations(OperationsQuery{Host: "h1", ID: "00000000000000000001"})
	if err != nil {
		t.Fatalf("unmirrored found detail: %v", err)
	}
	if len(found.Records) != 1 || found.Records[0].ID != "00000000000000000001" {
		t.Fatalf("unmirrored found detail = %+v, want the h1 fence row", found.Records)
	}
	if found.Generation == nil || *found.Generation != 3 || found.IncarnationID != "inc-h1" {
		t.Fatalf("unmirrored found detail pair = %v/%q, want the row's own 3/inc-h1", found.Generation, found.IncarnationID)
	}
}
