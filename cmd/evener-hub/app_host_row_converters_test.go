package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func fullyPopulatedHost() hostreg.Host {
	return hostreg.Host{
		Name:          "side",
		SSH:           "side.example",
		User:          "ops",
		KeyPath:       "/keys/side",
		EvenerPath:    "/opt/evener",
		ConfigPath:    "/etc/evener.toml",
		Addr:          "127.0.0.1:9",
		Roots:         []string{"/srv/a", "/srv/b"},
		Generation:    7,
		IncarnationID: "inc-7",
	}
}

// TestHostRowConvertersAgreeOnOneEntry pins that every site rendering a host
// entry as a wire row carries the same field set: the live entry, the
// receipt's recorded row, the tombstone, and the removed-row narrowing of
// each.
func TestHostRowConvertersAgreeOnOneEntry(t *testing.T) {
	entry := fullyPopulatedHost()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	receipt := newHostMutationReceipt("mutation", hostMutationUpdate, entry, now)
	tombstone := newHostTombstone(entry, 0, nil, now, defaultHostRecordPolicy())

	live := hostEntryRow(entry)
	if got := hostReceiptRow(receipt, hostMutationUpdate); !reflect.DeepEqual(got, live) {
		t.Fatalf("receipt row = %+v, want the live entry row %+v", got, live)
	}
	// The receipt's bare row carries the configuration but not the pinned pair.
	unpinned := live
	unpinned.Generation, unpinned.IncarnationID = 0, ""
	if got := hostRowForReceiptRow(receipt.Row); !reflect.DeepEqual(got, unpinned) {
		t.Fatalf("bare receipt row = %+v, want %+v", got, unpinned)
	}

	removedLive := live
	removedLive.Removed = true
	retained := 0
	want := removedRowForRow(removedLive)
	want.RetainedRows = &retained
	fromTombstone := removedRowForRow(tombstoneRow(tombstone))
	if !reflect.DeepEqual(fromTombstone, want) {
		t.Fatalf("tombstone removed row = %+v, want %+v", fromTombstone, want)
	}
	fromReceipt := removedRowForRow(hostReceiptRow(receipt, hostMutationRemove))
	want.RetainedRows = nil
	if !reflect.DeepEqual(fromReceipt, want) {
		t.Fatalf("receipt removed row = %+v, want %+v", fromReceipt, want)
	}
	if want.Name == "" || want.Address == "" || want.User == "" || want.KeyPath == "" ||
		want.EvenerPath == "" || want.ConfigPath == "" || want.Addr == "" || len(want.Roots) != 2 ||
		want.Origin == "" || want.Generation == 0 || want.IncarnationID == "" || !want.Removed {
		t.Fatalf("removed row %+v dropped a field the fixture populated", want)
	}
}

// TestHostConfigFieldsRoundTripThroughRecords pins that the records persisting
// an entry's configuration (receipt row, tombstone) hand back the same entry.
func TestHostConfigFieldsRoundTripThroughRecords(t *testing.T) {
	entry := fullyPopulatedHost()
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if got := hostFromReceiptRow(hostReceiptRowFor(entry)); !sameEffectiveHostEntryFields(got, entry) {
		t.Fatalf("receipt row round trip = %+v, want %+v", got, entry)
	}
	tombstone := newHostTombstone(entry, 3, nil, now, defaultHostRecordPolicy())
	back := tombstoneEntry(tombstone)
	if !sameEffectiveHostEntryFields(back, entry) || back.Generation != entry.Generation || back.IncarnationID != entry.IncarnationID {
		t.Fatalf("tombstone round trip = %+v, want %+v", back, entry)
	}
}
