package hostops

// Tests for §5's orphan-resolve store half: the dedicated write that clears the
// record's persisted boundary, every open pending-spawn intent, the per-host
// fencing-quarantine marker, and the state in ONE atomic write, persisting the
// `orphanResolved` marker and the operator attestation, with the lost-response
// replay arm and the boundary-unavailable refusal. The store never routes
// through Transition (store.go refuses the quarantined exit by type).

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// orphanResolveTestAttestation builds a well-formed attestation for recordID.
func orphanResolveTestAttestation(recordID string) *OrphanResolveAttestation {
	return &OrphanResolveAttestation{
		Operator:   "operator-alpha",
		Statement:  OrphanResolveStatement,
		RecordID:   recordID,
		ObservedAt: "2026-09-28T10:00:00Z",
	}
}

// quarantinedTestRecord persists one running deploy record with an armed+matched
// spawn intent and lands the fencing quarantine on it, so the resolve's one
// write has a marker, a boundary, and an intent to clear.
func quarantinedTestRecord(t *testing.T, store *Store, host, clientID string) Record {
	t.Helper()
	record := runningTestRecord(t, store, host, clientID)
	if _, err := store.ArmSpawnIntent(record.ID, SpawnIntent{
		Nonce: "nonce-" + clientID, Platform: SpawnPlatformLinux, CgroupID: "/cg/" + clientID,
	}); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "nonce-"+clientID, 41, "777"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	quarantined, err := store.QuarantineFencing(record.ID, json.RawMessage(remoteFencingBoundaryJSON))
	if err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	return quarantined
}

func TestResolveOrphanClearsEverythingInOneWrite(t *testing.T) {
	store, path := openTestStore(t)
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	before := store.Sequence()

	resolved, err := store.ResolveOrphan(quarantined.ID, orphanResolveTestAttestation(quarantined.ID))
	if err != nil {
		t.Fatalf("ResolveOrphan: %v", err)
	}
	if resolved.State != StateInterrupted {
		t.Fatalf("state = %q, want %q", resolved.State, StateInterrupted)
	}
	if !resolved.OrphanResolved {
		t.Fatal("the resolved record carries no orphanResolved marker")
	}
	if len(resolved.OrphanBoundary) > 0 {
		t.Fatalf("the resolved record still carries a boundary: %s", resolved.OrphanBoundary)
	}
	if len(resolved.PendingSpawns) > 0 {
		t.Fatalf("the resolved record still carries %d open intent(s)", len(resolved.PendingSpawns))
	}
	if resolved.Result == nil || resolved.Result.OK || resolved.Result.Message != InterruptedNote {
		t.Fatalf("result = %+v, want the interrupted note", resolved.Result)
	}
	if resolved.Sequence != before+1 {
		t.Fatalf("sequence = %d, want %d (one transition)", resolved.Sequence, before+1)
	}
	if got := resolved.OrphanAttestation; got == nil || *got != *orphanResolveTestAttestation(quarantined.ID) {
		t.Fatalf("attestation = %+v, want the presented one persisted beside the marker", got)
	}
	if _, ok := store.FencingQuarantine("h1"); ok {
		t.Fatal("the quarantine marker survived the resolve")
	}
	// The write is durable and COMPLETE: a fresh process reads the resolved
	// record, with no half-cleared boundary, intent, or marker anywhere.
	fresh := reopenFresh(t, path)
	stored, ok := fresh.Record(quarantined.ID)
	if !ok {
		t.Fatal("the resolved record did not reopen")
	}
	if stored.State != StateInterrupted || !stored.OrphanResolved || len(stored.OrphanBoundary) != 0 || len(stored.PendingSpawns) != 0 {
		t.Fatalf("reopened record = %+v, want a fully resolved record", stored)
	}
	if _, ok := fresh.FencingQuarantine("h1"); ok {
		t.Fatal("the reopen read a surviving quarantine marker")
	}
	// The file itself carries no stale half: neither the boundary bytes nor the
	// intent's nonce appear anywhere in the rewritten store.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if bytes.Contains(raw, []byte("nonce-client-h1")) {
		t.Fatalf("the store file still carries the dropped intent's nonce:\n%s", raw)
	}
	if bytes.Contains(raw, []byte(remoteFencingBoundaryJSON)) {
		t.Fatalf("the store file still carries the cleared boundary:\n%s", raw)
	}
}

func TestResolveOrphanWriteIsAtomic(t *testing.T) {
	path := StorePath(t.TempDir())
	failing := false
	store, err := openFS(afero.NewOsFs(), path, storeFaults{beforeRename: func() error {
		if failing {
			return errors.New("before-rename fault")
		}
		return nil
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	failing = true
	if _, err := store.ResolveOrphan(quarantined.ID, nil); err == nil {
		t.Fatal("ResolveOrphan whose rename never landed reported success")
	}
	// Neither half landed: the record is still orphan-unverified with its
	// boundary and intent, and the marker still stands, in memory and on disk.
	stored, ok := store.Record(quarantined.ID)
	if !ok || stored.State != StateOrphanUnverified || len(stored.OrphanBoundary) == 0 || len(stored.PendingSpawns) == 0 {
		t.Fatalf("record after the failed write = %+v (ok %v), want orphan-unverified with its boundary and intent", stored, ok)
	}
	if _, ok := store.FencingQuarantine("h1"); !ok {
		t.Fatal("a failed resolve dropped the marker in memory")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("a failed write changed the store file:\nbefore %s\nafter  %s", before, after)
	}
	if temps := leftoverTemps(t, filepath.Dir(path)); len(temps) > 0 {
		t.Fatalf("a failed write left temp files behind: %v", temps)
	}
}

func TestResolveOrphanReplaysThePersistedResolution(t *testing.T) {
	store, _ := openTestStore(t)
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	resolved, err := store.ResolveOrphan(quarantined.ID, orphanResolveTestAttestation(quarantined.ID))
	if err != nil {
		t.Fatalf("ResolveOrphan: %v", err)
	}
	// A lost-response retry of the same id replays the persisted resolution:
	// same record, no second transition, no rewritten marker or attestation.
	replayed, err := store.ResolveOrphan(quarantined.ID, orphanResolveTestAttestation(quarantined.ID))
	if err != nil {
		t.Fatalf("ResolveOrphan(replay): %v", err)
	}
	if !replayed.OrphanResolved || replayed.State != StateInterrupted {
		t.Fatalf("replay = %+v, want the persisted resolution", replayed)
	}
	if replayed.Sequence != resolved.Sequence || !replayed.UpdatedAt.Equal(resolved.UpdatedAt) {
		t.Fatalf("replay moved the record: sequence %d -> %d, updatedAt %s -> %s",
			resolved.Sequence, replayed.Sequence, resolved.UpdatedAt, replayed.UpdatedAt)
	}
	if got := replayed.OrphanAttestation; got == nil || *got != *resolved.OrphanAttestation {
		t.Fatalf("replay rewrote the attestation: %+v -> %+v", resolved.OrphanAttestation, got)
	}
}

func TestResolveOrphanRefusals(t *testing.T) {
	store, _ := openTestStore(t)
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	sibling := runningTestRecord(t, store, "h2", "client-h2")
	// An unknown id is not-found.
	if _, err := store.ResolveOrphan("00000000000000000099", nil); !errors.Is(err, ErrRecordNotFound) {
		t.Fatalf("unknown id = %v, want ErrRecordNotFound", err)
	}
	// An ordinary running record is not the fencing paths' to resolve.
	if _, err := store.ResolveOrphan(sibling.ID, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("non-unverified record = %v, want ErrInvalidTransition", err)
	}
	// The refusals wrote nothing: the quarantined record and its marker stand.
	if stored, ok := store.Record(quarantined.ID); !ok || stored.State != StateOrphanUnverified {
		t.Fatalf("refusals moved the quarantined record: %+v (ok %v)", stored, ok)
	}
	// A boundary-unavailable record never clears on an id alone.
	unavailable := runningTestRecord(t, store, "h3", "client-h3")
	entry := boundaryUnavailableEntry("/state/operations.json.custody-1")
	if _, err := store.SetOrphanBoundary(unavailable.ID, entry, nil); err != nil {
		t.Fatalf("SetOrphanBoundary(unavailable): %v", err)
	}
	if _, err := store.ResolveOrphan(unavailable.ID, nil); !errors.Is(err, ErrOrphanAttestationRequired) {
		t.Fatalf("id-only resolve of a boundary-unavailable record = %v, want ErrOrphanAttestationRequired", err)
	}
	// The attestation's own bindings are schema: a mismatched recordId is no
	// attribution this store persists.
	if _, err := store.ResolveOrphan(unavailable.ID, orphanResolveTestAttestation("00000000000000000042")); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("mismatched recordId = %v, want ErrInvalidRecord", err)
	}
	// With the attestation present the unavailable record resolves.
	attested := orphanResolveTestAttestation(unavailable.ID)
	attested.BoundaryRef = "/state/operations.json.custody-1"
	resolved, err := store.ResolveOrphan(unavailable.ID, attested)
	if err != nil {
		t.Fatalf("attested resolve: %v", err)
	}
	if !resolved.OrphanResolved || resolved.State != StateInterrupted {
		t.Fatalf("attested resolve = %+v, want a resolved record", resolved)
	}
}

func TestResolveOrphanAttestationSchema(t *testing.T) {
	store, _ := openTestStore(t)
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	for name, mutate := range map[string]func(*OrphanResolveAttestation){
		"wrong statement": func(a *OrphanResolveAttestation) { a.Statement = "teardown-verified-absent" },
		"no operator":     func(a *OrphanResolveAttestation) { a.Operator = "" },
		"no record":       func(a *OrphanResolveAttestation) { a.RecordID = "" },
		"not a timestamp": func(a *OrphanResolveAttestation) { a.ObservedAt = "yesterday" },
	} {
		attestation := orphanResolveTestAttestation(quarantined.ID)
		mutate(attestation)
		if _, err := store.ResolveOrphan(quarantined.ID, attestation); err == nil {
			t.Fatalf("%s: ResolveOrphan accepted a malformed attestation", name)
		}
	}
	if stored, ok := store.Record(quarantined.ID); !ok || stored.State != StateOrphanUnverified {
		t.Fatalf("a malformed attestation cleared the record: %+v (ok %v)", stored, ok)
	}
}

// TestResolveOrphanLandedWriteReturnsTheRecord pins the durable-write contract
// the handler reconciles with: a resolve whose rename landed but whose directory
// sync failed returns the committed record alongside the error, and
// RenameLanded reports it.
func TestResolveOrphanLandedWriteReturnsTheRecord(t *testing.T) {
	path := StorePath(t.TempDir())
	count, failAt := 0, 0
	store, err := openFS(afero.NewOsFs(), path, storeFaults{syncDir: func(fs afero.Fs, dir string) error {
		count++
		if failAt > 0 && count == failAt {
			return errors.New("injected directory-sync fault")
		}
		return syncDirFS(fs, dir)
	}})
	if err != nil {
		t.Fatalf("openFS: %v", err)
	}
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	// Learn the per-write directory-sync call count on a write that succeeds, so
	// the next write's LAST sync (the one behind the rename) can be failed
	// deterministically.
	before := count
	if _, err := store.ArmSpawnIntent(quarantined.ID, SpawnIntent{
		Nonce: "probe-nonce", Platform: SpawnPlatformLinux, CgroupID: "/cg/probe",
	}); err != nil {
		t.Fatalf("ArmSpawnIntent(probe): %v", err)
	}
	perWrite := count - before
	if perWrite <= 0 {
		t.Fatalf("a store write performed no directory sync (count %d -> %d)", before, count)
	}
	failAt = count + perWrite
	resolved, err := store.ResolveOrphan(quarantined.ID, orphanResolveTestAttestation(quarantined.ID))
	if err == nil {
		t.Fatal("ResolveOrphan reported success despite the sync fault")
	}
	if !RenameLanded(err) {
		t.Fatalf("ResolveOrphan error = %v, want a RenameLanded failure", err)
	}
	if resolved.ID != quarantined.ID || resolved.State != StateInterrupted || !resolved.OrphanResolved {
		t.Fatalf("landed resolve returned %+v, want the committed resolved record", resolved)
	}
	if marker, marked := store.FencingQuarantine("h1"); marked {
		t.Fatalf("landed resolve left the marker: %+v", marker)
	}
	// The file holds the resolution, and memory adopted it.
	if stored, ok := store.Record(quarantined.ID); !ok || !stored.OrphanResolved {
		t.Fatalf("memory after the landed write = %+v (ok %v)", stored, ok)
	}
	if fresh, ok := reopenFresh(t, path).Record(quarantined.ID); !ok || !fresh.OrphanResolved {
		t.Fatalf("file after the landed write = %+v (ok %v)", fresh, ok)
	}
}

// TestStoreRefusesCaseVariantAttestationKeys pins the owned-key rule for the
// newly persisted attestation object: a case variant of a canonical key is
// refused by the store's byte/key check — the rule the write path runs before
// every commit, so the variant can never be silently rewritten on the next save.
func TestStoreRefusesCaseVariantAttestationKeys(t *testing.T) {
	dir := t.TempDir()
	path := StorePath(dir)
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	if _, err := store.ResolveOrphan(quarantined.ID, orphanResolveTestAttestation(quarantined.ID)); err != nil {
		t.Fatalf("ResolveOrphan: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if err := checkStoreBytes(raw); err != nil {
		t.Fatalf("the canonical store bytes were refused: %v", err)
	}
	mutated := bytes.Replace(raw, []byte(`"operator"`), []byte(`"Operator"`), 1)
	if bytes.Equal(mutated, raw) {
		t.Fatal("the persisted attestation carries no operator key to mutate")
	}
	err = checkStoreBytes(mutated)
	if err == nil {
		t.Fatal("checkStoreBytes accepted a case-variant attestation key")
	}
	if !strings.Contains(err.Error(), "records[].attestation") {
		t.Fatalf("refusal = %v, want it to name the attestation object", err)
	}
}

// TestCompactionRetainsTheResolvedReplay pins §5/§10's replay horizon across
// compaction: a resolved record's tombstone replay still carries the
// orphanResolved marker and the attestation, so the lost-response retry answers
// as the resolved record it stands for.
func TestCompactionRetainsTheResolvedReplay(t *testing.T) {
	path := StorePath(t.TempDir())
	store, err := OpenWithRetention(path, RetentionPolicy{TerminalPerHost: 1, TerminalStoreWide: 10})
	if err != nil {
		t.Fatalf("OpenWithRetention: %v", err)
	}
	attestation := orphanResolveTestAttestation("00000000000000000001")
	quarantined := quarantinedTestRecord(t, store, "h1", "client-h1")
	attestation.RecordID = quarantined.ID
	if _, err := store.ResolveOrphan(quarantined.ID, attestation); err != nil {
		t.Fatalf("ResolveOrphan: %v", err)
	}
	// A second terminal record for the same host exceeds TerminalPerHost: the
	// resolved record compacts into a tombstone.
	sibling := runningTestRecord(t, store, "h1", "client-h1-b")
	if _, err := store.Transition(sibling.ID, StateComplete, func(r *Record) { r.Result = &Result{OK: true, Message: "done"} }); err != nil {
		t.Fatalf("Transition(complete): %v", err)
	}
	if len(store.Tombstones()) == 0 {
		t.Fatal("no tombstone was left, so nothing was compacted")
	}
	replayed, hit, err := store.LookupOperation(OperationDedupQuery{
		ClientOperationID: quarantined.ClientOperationID,
		Host:              quarantined.Host,
		Kind:              quarantined.Kind,
		Current:           OperationPair{Generation: quarantined.Generation, IncarnationID: quarantined.IncarnationID},
	})
	if err != nil || !hit {
		t.Fatalf("LookupOperation(resolved) = hit %v, err %v; want the retained replay", hit, err)
	}
	if !replayed.Compacted || !replayed.OrphanResolved || replayed.State != StateInterrupted {
		t.Fatalf("compacted replay = %+v, want the resolved marker retained", replayed)
	}
	if replayed.OrphanAttestation == nil || replayed.OrphanAttestation.BoundaryRef != "" ||
		replayed.OrphanAttestation.Operator != attestation.Operator {
		t.Fatalf("compacted replay attestation = %+v, want the persisted one", replayed.OrphanAttestation)
	}
	// It survives a reload: the tombstone carries the fields.
	fresh := reopenFresh(t, path)
	replayed, hit, err = fresh.LookupOperation(OperationDedupQuery{
		ClientOperationID: quarantined.ClientOperationID,
		Host:              quarantined.Host,
		Kind:              quarantined.Kind,
		Current:           OperationPair{Generation: quarantined.Generation, IncarnationID: quarantined.IncarnationID},
	})
	if err != nil || !hit || !replayed.OrphanResolved || replayed.OrphanAttestation == nil {
		t.Fatalf("reloaded replay = hit %v err %v record %+v; want the marker and attestation", hit, err, replayed)
	}
}
