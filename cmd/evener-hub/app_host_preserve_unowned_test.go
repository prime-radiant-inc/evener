package hub

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// preserveContract pins the rule every record table shares when a rewrite
// preserves the file's records: a record for a name the write does not own
// rides through, a record for an owned name is not resurrected from the file, a
// dropped record is not resurrected, and the carried record wins over the
// file's older copy of the same key. derive runs one table's derivation over a
// file section (the config's copy), a carried section, and a dropped key set,
// with ownedHost as the only name the write owns.
func preserveContract[V any](t *testing.T, keyFor func(host string) string, mk func(host, tag string) V, derive func(file, carried map[string]V, dropped map[string]struct{}) map[string]V) {
	t.Helper()
	const ownedHost, otherHost, droppedHost, carriedHost = "owned-host", "other-host", "dropped-host", "carried-host"
	file := map[string]V{}
	for _, host := range []string{ownedHost, otherHost, droppedHost, carriedHost} {
		file[keyFor(host)] = mk(host, "file")
	}
	carried := map[string]V{keyFor(carriedHost): mk(carriedHost, "carried")}
	dropped := map[string]struct{}{keyFor(droppedHost): {}}

	got := derive(file, carried, dropped)

	want := map[string]V{
		keyFor(otherHost):   mk(otherHost, "file"),
		keyFor(carriedHost): mk(carriedHost, "carried"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preserved set = %+v, want %+v", got, want)
	}
}

func TestRecordTablesPreserveUnownedFileRecords(t *testing.T) {
	known := []hostreg.Host{{Name: "owned-host"}}
	receiptKey := func(host string) string {
		return hostReceiptScopedKey(hostReceiptScope{MutationID: "m", Name: host, Kind: hostMutationAdd, Generation: 1, IncarnationID: "i"})
	}

	t.Run("receipts", func(t *testing.T) {
		preserveContract(t, receiptKey,
			func(host, tag string) HostMutationReceipt { return HostMutationReceipt{Outcome: tag + host} },
			func(file, carried map[string]HostMutationReceipt, dropped map[string]struct{}) map[string]HostMutationReceipt {
				return hubTOMLReceiptTables(Config{MutationReceipts: file}, nil, known, carried, dropped)
			})
	})
	t.Run("tombstones", func(t *testing.T) {
		preserveContract(t, func(host string) string { return host },
			func(host, tag string) HostTombstone { return HostTombstone{Name: host, Origin: tag} },
			func(file, carried map[string]HostTombstone, dropped map[string]struct{}) map[string]HostTombstone {
				return hubTOMLTombstoneTables(Config{Tombstones: file}, nil, known, carried, dropped)
			})
	})
	t.Run("pruned receipts", func(t *testing.T) {
		preserveContract(t, receiptKey,
			func(host, tag string) PrunedReceiptMarker { return PrunedReceiptMarker{PrunedAt: tag + host} },
			func(file, carried map[string]PrunedReceiptMarker, dropped map[string]struct{}) map[string]PrunedReceiptMarker {
				return hubTOMLPrunedReceiptTables(Config{PrunedReceipts: file}, nil, known, carried, dropped)
			})
	})
	t.Run("staged receipts", func(t *testing.T) {
		preserveContract(t, func(host string) string { return host },
			func(host, tag string) HostStagedReceipt { return HostStagedReceipt{Key: tag + host} },
			func(file, carried map[string]HostStagedReceipt, dropped map[string]struct{}) map[string]HostStagedReceipt {
				return hubTOMLStagedReceiptTables(Config{StagedReceipts: file}, nil, known, carried, dropped)
			})
	})
	t.Run("teardown remnants", func(t *testing.T) {
		preserveContract(t, func(host string) string { return "remnant-" + host },
			func(host, tag string) HostTeardownRemnant { return HostTeardownRemnant{Host: host, Seam: tag} },
			func(file, carried map[string]HostTeardownRemnant, dropped map[string]struct{}) map[string]HostTeardownRemnant {
				return hubTOMLTeardownRemnantTables(Config{TeardownRemnants: file}, nil, known, carried, dropped)
			})
	})
	t.Run("teardown attempts", func(t *testing.T) {
		remnants := map[string]HostTeardownRemnant{}
		for _, host := range []string{"owned-host", "other-host", "dropped-host", "carried-host"} {
			remnants["remnant-"+host] = HostTeardownRemnant{Host: host}
		}
		preserveContract(t, func(host string) string { return "attempt-" + host },
			func(host, tag string) HostTeardownAttempt {
				return HostTeardownAttempt{RemnantID: "remnant-" + host, State: tag}
			},
			func(file, carried map[string]HostTeardownAttempt, dropped map[string]struct{}) map[string]HostTeardownAttempt {
				return hubTOMLTeardownAttemptTables(Config{TeardownAttempts: file, TeardownRemnants: remnants}, nil, known, carried, nil, dropped)
			})
	})
	t.Run("store sync intents", func(t *testing.T) {
		preserveContract(t, func(host string) string { return host },
			func(host, tag string) HostStoreSyncIntent {
				return HostStoreSyncIntent{Generation: 1, TokenValues: []string{tag + host}}
			},
			func(file, carried map[string]HostStoreSyncIntent, dropped map[string]struct{}) map[string]HostStoreSyncIntent {
				return hubTOMLStoreSyncTables(Config{PendingStoreSync: file}, nil, known, carried, dropped)
			})
	})
}
