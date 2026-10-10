package hostops

// comp08 2b's retire-and-drop contract for the removed crash-fencing store
// keys. A prior-build store file carries an orphan boundary, the resolved
// marker, an operator attestation, open pending-spawn intents and a per-host
// fencing-quarantine marker. Every strict decoder must still LOAD that file —
// the normal open and the corrupt-store custody path alike — while the domain
// model never carries the retired payload and every rewrite drops it.

import (
	"path/filepath"
	"strings"
	"testing"
)

// priorShapeStoreJSON is a healthy prior-build store: one pending record
// carrying an orphan boundary, a resolved marker and an open pending-spawn
// intent set, one resolved interrupted record carrying the attestation, and a
// per-host fencing-quarantine marker at the top level.
const priorShapeStoreJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"client-h1","host":"h1","kind":"deploy","state":"pending",` +
	`"generation":7,"incarnationId":"inc-h1","fencingEpoch":{"bootId":"boot-1","opSeq":3},` +
	`"orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
	`"pendingSpawns":[{"nonce":"nonce-h1","platform":"linux","cgroupId":"/cg/h1"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"client-h2","host":"h2","kind":"deploy","state":"interrupted",` +
	`"generation":7,"incarnationId":"inc-h2","orphanResolved":true,` +
	`"attestation":{"operator":"operator-alpha","statement":"orphan-verified-absent",` +
	`"recordId":"00000000000000000002","observedAt":"2026-09-26T00:00:00Z"},` +
	`"result":{"ok":false,"message":"interrupted"},"sequence":1,` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false}],` +
	`"fencingQuarantines":{"h1":{"recordId":"00000000000000000001","quarantinedAt":"2026-09-26T00:00:00Z"}}}`

// TestPriorBuildStoreLoadsAndDropsRetiredKeys pins the normal open: the retired
// keys decode (never a refusal), the served records carry none of the retired
// payload, and the boot pass's rewrite leaves none of the keys in the file.
func TestPriorBuildStoreLoadsAndDropsRetiredKeys(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, priorShapeStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a prior-build store: %v", err)
	}
	record, ok := store.Record("00000000000000000001")
	if !ok || record.State != StatePending {
		t.Fatalf("the prior-build record = %+v/%t, want the pending record served", record, ok)
	}

	// The boot pass is this file's rewrite trigger: it moves the pending record
	// to interrupted and writes the store back.
	moved, err := store.RecoverInterrupted()
	if err != nil || moved != 1 {
		t.Fatalf("RecoverInterrupted = %d/%v, want the one pending record moved", moved, err)
	}
	body := string(mustReadFile(t, path))
	for _, key := range []string{"orphanBoundary", "orphanResolved", "attestation", "pendingSpawns", "fencingQuarantines"} {
		if strings.Contains(body, key) {
			t.Fatalf("the rewrite still carries the retired key %q:\n%s", key, body)
		}
	}
	// The rewritten file is a store this build can hold, and its records carry
	// the retained fields the prior file held.
	reopened := reopenFresh(t, path)
	if got, ok := reopened.Record("00000000000000000001"); !ok || got.State != StateInterrupted {
		t.Fatalf("the rewritten record = %+v/%t, want the interrupted state", got, ok)
	}
	if got, ok := reopened.Record("00000000000000000002"); !ok || got.State != StateInterrupted {
		t.Fatalf("the resolved record = %+v/%t, want it served as interrupted", got, ok)
	}
}

// priorShapeCorruptStoreJSON is the same prior shape, made corrupt for §4's
// custody path the way quarantineFixtureJSON is: a retained record's sequence
// stamp sits above the store-level sequence, so the load refuses and the
// corrupt-store quarantine takes over.
const priorShapeCorruptStoreJSON = `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
	`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
	`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-markerless","platform":"linux","nonce":"nonce-h1"}],` +
	`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
	`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
	`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
	`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}],` +
	`"fencingQuarantines":{"h1":{"recordId":"00000000000000000001","quarantinedAt":"2026-09-26T00:00:00Z"}}}`

// TestPriorBuildCorruptStoreStillQuarantinesAndWritesNoRetiredKeys pins the
// custody path's tolerance: the §9 boundary schema is gone, so the corrupt
// prior-build file must still quarantine — the custody snapshot keeps the
// imported identities, and neither the custody file nor the replacement store
// re-emits the retired keys.
func TestPriorBuildCorruptStoreStillQuarantinesAndWritesNoRetiredKeys(t *testing.T) {
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, priorShapeCorruptStoreJSON)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a corrupt prior-build store: %v", err)
	}
	if store.Quarantine() == nil {
		t.Fatal("the corrupt prior-build store did not quarantine")
	}
	// The custody snapshot keeps the imported identities and re-emits none of
	// the retired boundary or marker payload.
	_, custodyBytes := quarantineArtifact(t, filepath.Dir(path), ".custody-")
	for _, key := range []string{`"boundary":`, `"quarantine":`, `"fencingQuarantines"`} {
		if strings.Contains(string(custodyBytes), key) {
			t.Fatalf("the custody file still carries the retired key %q:\n%s", key, custodyBytes)
		}
	}
	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("the replacement store serves %d records, want the two custody imports: %+v", len(records), records)
	}
	for _, record := range records {
		if record.State != StateOrphanUnverified {
			t.Fatalf("the replacement store serves %+v, want only the closed-name imports", record)
		}
	}
	body := string(mustReadFile(t, path))
	for _, key := range []string{"orphanBoundary", "orphanResolved", "attestation", "pendingSpawns", "fencingQuarantines"} {
		if strings.Contains(body, key) {
			t.Fatalf("the replacement store still carries the retired key %q:\n%s", key, body)
		}
	}
	reopenFresh(t, path)
}

// TestPriorBuildMalformedBoundaryStillLoadsAndQuarantines pins the same
// tolerance at the malformed end: the §9 boundary schema is gone, so a prior
// file carrying a boundary no variant could describe is still a file this build
// loads — and a corrupt one still quarantines — rather than a startup refusal.
func TestPriorBuildMalformedBoundaryStillLoadsAndQuarantines(t *testing.T) {
	malformed := `{"version":1,"sequence":1,"allocatorHighWaterMark":2,"records":[` +
		`{"id":"00000000000000000001","clientOperationId":"op-h1","host":"h1","kind":"restart","state":"orphan-unverified",` +
		`"generation":3,"incarnationId":"inc-h1","orphanBoundary":[{"kind":"local-linux"}],` +
		`"createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z","hostRemoved":false},` +
		`{"id":"00000000000000000002","clientOperationId":"op-h2","host":"h2","kind":"deploy","state":"complete",` +
		`"generation":7,"incarnationId":"inc-h2","createdAt":"2026-09-26T00:00:00Z","updatedAt":"2026-09-26T00:00:00Z",` +
		`"hostRemoved":false,"result":{"ok":true,"message":"done"},"sequence":2}]}`
	path := StorePath(t.TempDir())
	writeRawStore(t, path, 0o600, malformed)
	// Corrupt only at the store level (the record stamp sits above the store
	// sequence), so the tolerance under test is the boundary shape.
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open on a prior-build store with a malformed boundary: %v", err)
	}
	if store.Quarantine() == nil {
		t.Fatal("the corrupt prior-build store with a malformed boundary did not quarantine")
	}
	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("the replacement store serves %d records, want the two custody imports", len(records))
	}
	for _, record := range records {
		if record.State != StateOrphanUnverified {
			t.Fatalf("the replacement store serves %+v, want only the closed-name imports", record)
		}
	}
	reopenFresh(t, path)
}
