package hub

import (
	"bytes"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// validateHostRecordSet checks a record family against the shapes this build
// writes: every record complete, agreeing with its key and with the records it
// pairs with. A set that fails is one no writer of this build produces, so it
// is refused loudly before any write or serve rather than read as half of
// itself. hub.toml's reserved sections and the operation store's family share
// this one rule set.
func validateHostRecordSet(set hostRecordSet) error {
	if err := validateHostRecords(set.HostRecords, set.Generations); err != nil {
		return fmt.Errorf("validate host records: %w", err)
	}
	if err := validateHostMutationReceipts(set.MutationReceipts); err != nil {
		return fmt.Errorf("validate mutation receipts: %w", err)
	}
	if err := validateHostTombstones(set.Tombstones, set.Generations); err != nil {
		return fmt.Errorf("validate tombstones: %w", err)
	}
	if err := validatePrunedReceipts(set.PrunedReceipts); err != nil {
		return fmt.Errorf("validate pruned receipts: %w", err)
	}
	if err := validateHostStagedReceipts(set.StagedReceipts); err != nil {
		return fmt.Errorf("validate staged receipts: %w", err)
	}
	if err := validateHostTeardownRemnants(set.TeardownRemnants); err != nil {
		return fmt.Errorf("validate teardown remnants: %w", err)
	}
	if err := validateHostTeardownAttempts(set.TeardownAttempts, set.TeardownRemnants); err != nil {
		return fmt.Errorf("validate teardown attempts: %w", err)
	}
	if err := validateHostStoreSync(set.PendingStoreSync); err != nil {
		return fmt.Errorf("validate pending store sync: %w", err)
	}
	if err := validateHostMirrorCommits(set.MirrorCommits); err != nil {
		return fmt.Errorf("validate mirror commits: %w", err)
	}
	return nil
}

// encodeRecordMap renders each record as the JSON object the operation store
// holds.
func encodeRecordMap[V any](records map[string]V) (map[string]json.RawMessage, error) {
	encoded := make(map[string]json.RawMessage, len(records))
	for key, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			return nil, fmt.Errorf("encode record %q: %w", key, err)
		}
		encoded[key] = raw
	}
	return encoded, nil
}

// decodeRecordMap reads each stored record back, refusing a field this build
// does not know: a record from a newer build is never silently reinterpreted or
// rewritten without the field.
func decodeRecordMap[V any](what string, encoded map[string]json.RawMessage) (map[string]V, error) {
	records := make(map[string]V, len(encoded))
	for key, raw := range encoded {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var record V
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("decode %s record %q: %w", what, key, err)
		}
		records[key] = record
	}
	return records, nil
}

// encodeHostRecords renders a record set as the operation store's family, after
// the same validation a load applies, so the store never holds a record this
// build would refuse to read back. The legacy-only intent and mirror-marker
// records have no home in the family: the live protocol that wrote them is gone,
// and a set that still carries one is refused.
func encodeHostRecords(set hostRecordSet) (hostops.HostRecords, error) {
	if len(set.PendingStoreSync) != 0 || len(set.MirrorCommits) != 0 {
		return hostops.HostRecords{}, fmt.Errorf("the record set still carries %d cross-file intent(s) and %d mirror marker(s)", len(set.PendingStoreSync), len(set.MirrorCommits))
	}
	if err := validateHostRecordSet(set); err != nil {
		return hostops.HostRecords{}, err
	}
	var family hostops.HostRecords
	var err error
	if family.Hosts, err = encodeRecordMap(set.HostRecords); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.Generations, err = encodeRecordMap(set.Generations); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.Receipts, err = encodeRecordMap(set.MutationReceipts); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.Tombstones, err = encodeRecordMap(set.Tombstones); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.PrunedReceipts, err = encodeRecordMap(set.PrunedReceipts); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.StagedReceipts, err = encodeRecordMap(set.StagedReceipts); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.Remnants, err = encodeRecordMap(set.TeardownRemnants); err != nil {
		return hostops.HostRecords{}, err
	}
	if family.Attempts, err = encodeRecordMap(set.TeardownAttempts); err != nil {
		return hostops.HostRecords{}, err
	}
	return family, nil
}

// decodeHostRecords reads the operation store's family back into a record set
// and validates it. Every map in the result is non-nil.
func decodeHostRecords(family hostops.HostRecords) (hostRecordSet, error) {
	var set hostRecordSet
	var err error
	if set.HostRecords, err = decodeRecordMap[HostRecord]("host", family.Hosts); err != nil {
		return hostRecordSet{}, err
	}
	if set.Generations, err = decodeRecordMap[HostGeneration]("generation", family.Generations); err != nil {
		return hostRecordSet{}, err
	}
	if set.MutationReceipts, err = decodeRecordMap[HostMutationReceipt]("receipt", family.Receipts); err != nil {
		return hostRecordSet{}, err
	}
	if set.Tombstones, err = decodeRecordMap[HostTombstone]("tombstone", family.Tombstones); err != nil {
		return hostRecordSet{}, err
	}
	if set.PrunedReceipts, err = decodeRecordMap[PrunedReceiptMarker]("pruned receipt", family.PrunedReceipts); err != nil {
		return hostRecordSet{}, err
	}
	if set.StagedReceipts, err = decodeRecordMap[HostStagedReceipt]("staged receipt", family.StagedReceipts); err != nil {
		return hostRecordSet{}, err
	}
	if set.TeardownRemnants, err = decodeRecordMap[HostTeardownRemnant]("teardown remnant", family.Remnants); err != nil {
		return hostRecordSet{}, err
	}
	if set.TeardownAttempts, err = decodeRecordMap[HostTeardownAttempt]("teardown attempt", family.Attempts); err != nil {
		return hostRecordSet{}, err
	}
	set.PendingStoreSync = map[string]HostStoreSyncIntent{}
	set.MirrorCommits = map[string]HostMirrorCommit{}
	if err := validateHostRecordSet(set); err != nil {
		return hostRecordSet{}, err
	}
	return set, nil
}

// decodeTeardownRemnants reads only the teardown remnants of the family. They
// fence lifecycle actions on their own, so a record elsewhere in the family that
// this build cannot read, a teardown attempt included, must not hide them.
func decodeTeardownRemnants(family hostops.HostRecords) (map[string]HostTeardownRemnant, error) {
	return decodeRecordMap[HostTeardownRemnant]("teardown remnant", family.Remnants)
}

// decodeTeardownAttempts reads only the teardown attempts of the family.
func decodeTeardownAttempts(family hostops.HostRecords) (map[string]HostTeardownAttempt, error) {
	return decodeRecordMap[HostTeardownAttempt]("teardown attempt", family.Attempts)
}
