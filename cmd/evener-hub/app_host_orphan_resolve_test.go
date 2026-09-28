//go:build linux || darwin

package hub

// Tests for evener/host/orphan-resolve's handler half (crash-fencing §5, §8,
// §10): the resolve over the wire, the refusal shapes, the attestation
// bindings and freshness, the lost-response replay, the origin guard's
// ordering (before any record lookup), and the `operations` detail's
// orphanBoundary rendering. No host is dialed; the boundary enumeration runs
// through the injected seam.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// orphanResolveBoundaryJSON is a one-member remote-fencing boundary with one
// lease entry, in §9's persisted shape.
const orphanResolveBoundaryJSON = `[{"kind":"remote-fencing","fencingEpoch":{"bootId":"boot-1","opSeq":2},"guardEpoch":3,` +
	`"leaseEntries":[{"command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z","ownership":{"pid":41,"pidStartTime":"777"}}]}]`

// newOrphanResolveFixture builds a manager over a fresh operation store and the
// caller's verification seam.
func newOrphanResolveFixture(t *testing.T, verify hostfence.VerifyOptions) (*hubHostManager, *hostops.Store) {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore: store,
		RemoteHostOrphanVerify: func(record hostops.Record) error {
			return hostfence.VerifyOrphanBoundary(record, verify)
		},
	}, "", nil, nil)
	return m, store
}

// quarantinedHubRecord persists one running deploy record, arms and matches a
// spawn intent on it, and lands the fencing quarantine.
func quarantinedHubRecord(t *testing.T, store *hostops.Store, host string) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-" + host, Host: host, Kind: hostops.KindDeploy,
		Generation: 7, IncarnationID: "inc-" + host,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(record.ID, hostops.StateRunning, nil); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	if _, err := store.ArmSpawnIntent(record.ID, hostops.SpawnIntent{
		Nonce: "n1", Platform: hostops.SpawnPlatformLinux, CgroupID: "/cg/" + host,
	}); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 41, "777"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	quarantined, err := store.QuarantineFencing(record.ID, json.RawMessage(orphanResolveBoundaryJSON))
	if err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	return quarantined
}

// orphanLocalHubRecord persists one running deploy record and marks it
// orphan-unverified with a local boundary and an open intent — no quarantine
// marker, so the local arm of the resolve is the one under test.
func orphanLocalHubRecord(t *testing.T, store *hostops.Store, host, boundary string) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-" + host, Host: host, Kind: hostops.KindDeploy,
		Generation: 7, IncarnationID: "inc-" + host,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.Transition(record.ID, hostops.StateRunning, nil); err != nil {
		t.Fatalf("Transition(running): %v", err)
	}
	if _, err := store.ArmSpawnIntent(record.ID, hostops.SpawnIntent{
		Nonce: "n1", Platform: hostops.SpawnPlatformLinux, CgroupID: "/cg/" + host,
	}); err != nil {
		t.Fatalf("ArmSpawnIntent: %v", err)
	}
	if _, err := store.MatchSpawnIntent(record.ID, "n1", 41, "777"); err != nil {
		t.Fatalf("MatchSpawnIntent: %v", err)
	}
	marked, err := store.SetOrphanBoundary(record.ID, json.RawMessage(boundary), nil)
	if err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}
	return marked
}

// cleanLeaseVerify is the scripted enumeration that confirms the entry gone.
func cleanLeaseVerify() hostfence.VerifyOptions {
	return hostfence.VerifyOptions{VerifyLeaseEntry: func(hostfence.LeaseRef) (bool, error) { return true, nil }}
}

// orphanErr returns the error half of an OrphanResolve call, failing when no
// error came back.
func orphanErr(result appwire.OperationRecord, err error) error {
	if err == nil {
		return fmt.Errorf("OrphanResolve returned %+v, want an error", result)
	}
	return err
}

func TestOrphanResolveClearsTheQuarantinedRecord(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")

	resolved, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil {
		t.Fatalf("OrphanResolve: %v", err)
	}
	if resolved.State != appwire.OperationStateInterrupted {
		t.Fatalf("state = %q, want interrupted", resolved.State)
	}
	if !resolved.OrphanResolved {
		t.Fatal("the response is not marked orphanResolved")
	}
	if resolved.OrphanBoundary != nil {
		t.Fatalf("the response still carries a boundary: %+v", resolved.OrphanBoundary)
	}
	if resolved.Attestation != nil {
		t.Fatalf("an unattested resolve persisted an attestation: %+v", resolved.Attestation)
	}
	if resolved.Result == nil || resolved.Result.OK || resolved.Result.Message != hostops.InterruptedNote {
		t.Fatalf("result = %+v, want the interrupted note", resolved.Result)
	}
	// The store's half: the marker cleared in the same write, the record
	// resolved, the boundary and the intent gone.
	if _, ok := store.FencingQuarantine("h1"); ok {
		t.Fatal("the quarantine marker survived the resolve")
	}
	stored, ok := store.Record(record.ID)
	if !ok || stored.State != hostops.StateInterrupted || !stored.OrphanResolved {
		t.Fatalf("stored record = %+v (ok %v), want a resolved record", stored, ok)
	}
	if len(stored.OrphanBoundary) != 0 || len(stored.PendingSpawns) != 0 {
		t.Fatalf("stored record still carries boundary/intents: %+v", stored)
	}
}

// scriptedBoundary is a minimal LocalBoundaryHandle for the local-arm plumbing
// test: fixed membership, no signaling.
type scriptedBoundary struct {
	members []execenv.BoundaryMember
}

func (s *scriptedBoundary) Enforcing() bool { return true }
func (s *scriptedBoundary) Members() ([]execenv.BoundaryMember, error) {
	return append([]execenv.BoundaryMember(nil), s.members...), nil
}
func (s *scriptedBoundary) SignalVerified(int, string) error { return nil }
func (s *scriptedBoundary) Await(time.Duration, func([]execenv.BoundaryMember) bool) error {
	return nil
}
func (s *scriptedBoundary) Close() error { return nil }

func TestOrphanResolveLocalArmUsesTheInjectedEnumeration(t *testing.T) {
	localBoundary := `[{"kind":"local-linux","cgroupId":"/cg/h1","nonce":"n1","pid":41,"startTime":"777"}]`
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	handle := &scriptedBoundary{}
	opts := hostfence.VerifyOptions{
		Open:    func(execenv.BoundaryIdentity) (hostfence.LocalBoundaryHandle, error) { return handle, nil },
		Observe: func(int) (string, error) { return "", execenv.ErrBoundaryMemberGone },
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore: store,
		RemoteHostOrphanVerify: func(record hostops.Record) error {
			return hostfence.VerifyOrphanBoundary(record, opts)
		},
	}, "", nil, nil)
	record := orphanLocalHubRecord(t, store, "h1", localBoundary)
	// An empty local boundary proves clean and resolves.
	if _, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID}); err != nil {
		t.Fatalf("OrphanResolve(clean local): %v", err)
	}
	// A member present refuses transient busy; nothing is signaled.
	record2 := orphanLocalHubRecord(t, store, "h2", strings.Replace(localBoundary, "/cg/h1", "/cg/h2", 1))
	handle.members = []execenv.BoundaryMember{{PID: 41, StartToken: "777"}}
	_, err = m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record2.ID})
	assertWireCode(t, err, appwire.CodeConflict)
	if !strings.Contains(err.Error(), "transient") && !strings.Contains(err.Error(), "still holds") {
		t.Fatalf("refusal %q does not name the held boundary", err)
	}
	if stored, ok := store.Record(record2.ID); !ok || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("a refused resolve moved the record: %+v (ok %v)", stored, ok)
	}
}

// TestOrphanResolveAcceptsAnUnneededAttestation pins §9's
// accepted-but-unneeded rule: on a record that does not carry the
// boundary-unavailable entry, a present attestation is accepted and persisted,
// and a boundaryRef that matches nothing is never a refusal.
func TestOrphanResolveAcceptsAnUnneededAttestation(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent",
		RecordID: record.ID, BoundaryRef: "/state/not-this-records-boundary.json",
		ObservedAt: m.nowTime().UTC().Format("2006-01-02T15:04:05Z"),
	}
	resolved, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
	if err != nil {
		t.Fatalf("OrphanResolve with an unneeded attestation = %v, want acceptance", err)
	}
	if resolved.Attestation == nil || resolved.Attestation.BoundaryRef != attestation.BoundaryRef {
		t.Fatalf("response attestation = %+v, want the presented one persisted beside the marker", resolved.Attestation)
	}
}

func TestOrphanResolveRefusals(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	// Unknown id: typed not-found, before anything is touched.
	err := orphanErr(m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: "00000000000000000099"}))
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("unknown id refusal = %#v, want a wire error", err)
	}
	if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorResourceNotFound {
		t.Fatalf("unknown id refusal data = %#v, want the typed not-found", wire.Data)
	}
	// An ordinary running record is a validation refusal.
	running, err := store.Create(hostops.NewRecord{ClientOperationID: "client-run", Host: "h1", Kind: hostops.KindDeploy, Generation: 7, IncarnationID: "inc-run"})
	if err != nil {
		t.Fatalf("Create(running): %v", err)
	}
	assertWireCode(t, orphanErr(m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: running.ID})), appwire.CodeInvalidParams)
	// Members still present (the seam refuses) and an unconfigured seam both
	// refuse transient busy, and the record stays open.
	busy, busyStore := newOrphanResolveFixture(t, hostfence.VerifyOptions{VerifyLeaseEntry: func(hostfence.LeaseRef) (bool, error) { return false, nil }})
	busyRecord := quarantinedHubRecord(t, busyStore, "h2")
	_, err = busy.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: busyRecord.ID})
	assertWireCode(t, err, appwire.CodeConflict)
	if !strings.Contains(err.Error(), string(appwire.ErrorHostBusyTransient)) && !strings.Contains(err.Error(), "still registered live") {
		t.Fatalf("busy refusal = %q, want the transient-busy form", err)
	}
	closed, closedStore := newOrphanResolveFixture(t, hostfence.VerifyOptions{})
	closedRecord := quarantinedHubRecord(t, closedStore, "h3")
	_, err = closed.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: closedRecord.ID})
	assertWireCode(t, err, appwire.CodeConflict)
	// The refused records and their markers stand.
	if _, ok := busyStore.FencingQuarantine("h2"); !ok {
		t.Fatal("a refused resolve dropped a marker")
	}
	if _, ok := closedStore.FencingQuarantine("h3"); !ok {
		t.Fatal("a refused resolve dropped a marker")
	}
}

func TestOrphanResolveAttestationBindings(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	// A boundary-unavailable record: a running record marked with the custody
	// sentinel, which no enumeration can ever clear.
	record, err := store.Create(hostops.NewRecord{ClientOperationID: "client-cust", Host: "h1", Kind: hostops.KindDeploy, Generation: 7, IncarnationID: "inc-cust"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	custodyRef := "/state/operations.json.custody-1"
	if _, err := store.SetOrphanBoundary(record.ID, json.RawMessage(
		`[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"`+custodyRef+`"}]`), nil); err != nil {
		t.Fatalf("SetOrphanBoundary(unavailable): %v", err)
	}
	good := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent",
		RecordID: record.ID, BoundaryRef: custodyRef, ObservedAt: m.nowTime().UTC().Format("2006-01-02T15:04:05Z"),
	}
	// An id-only call never clears the record.
	_, err = m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("id-only refusal = %q, want it to name the required attestation", err)
	}
	// Every mismatched binding refuses before any clearance.
	for name, mutate := range map[string]func(*appwire.HostOrphanResolveAttestation){
		"operator":    func(a *appwire.HostOrphanResolveAttestation) { a.Operator = "someone-else" },
		"recordId":    func(a *appwire.HostOrphanResolveAttestation) { a.RecordID = "00000000000000000042" },
		"boundaryRef": func(a *appwire.HostOrphanResolveAttestation) { a.BoundaryRef = "/state/other.json" },
		"stale":       func(a *appwire.HostOrphanResolveAttestation) { a.ObservedAt = "2026-01-01T00:00:00Z" },
		"statement":   func(a *appwire.HostOrphanResolveAttestation) { a.Statement = "trust me" },
	} {
		attestation := good
		mutate(&attestation)
		ctx := withSessionOperator(context.Background(), "operator-alpha")
		_, err := m.OrphanResolve(ctx, appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
		if err == nil {
			t.Fatalf("%s mismatch: resolve succeeded, want a validation refusal", name)
		}
		assertWireCode(t, err, appwire.CodeInvalidParams)
		if stored, ok := store.Record(record.ID); !ok || stored.State != hostops.StateOrphanUnverified {
			t.Fatalf("%s mismatch cleared the record: %+v (ok %v)", name, stored, ok)
		}
	}
	// The authenticated caller's own attestation resolves, and it persists.
	resolved, err := m.OrphanResolve(withSessionOperator(context.Background(), "operator-alpha"),
		appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &good})
	if err != nil {
		t.Fatalf("attested resolve: %v", err)
	}
	if resolved.Attestation == nil || *resolved.Attestation != good {
		t.Fatalf("response attestation = %+v, want the presented one", resolved.Attestation)
	}
	stored, ok := store.Record(record.ID)
	if !ok || stored.OrphanAttestation == nil || stored.OrphanAttestation.BoundaryRef != custodyRef {
		t.Fatalf("stored attestation = %+v (ok %v), want it persisted", stored.OrphanAttestation, ok)
	}
}

func TestOrphanResolveReplaysTheResolvedId(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")
	first, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil {
		t.Fatalf("OrphanResolve: %v", err)
	}
	// The replay returns the persisted resolution; the store file does not move.
	replayed, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil {
		t.Fatalf("OrphanResolve(replay): %v", err)
	}
	if replayed.OrphanResolved != first.OrphanResolved || replayed.State != first.State ||
		replayed.UpdatedAt != first.UpdatedAt || replayed.Result.Message != first.Result.Message {
		t.Fatalf("replay = %+v, want the persisted resolution %+v", replayed, first)
	}
	// An ordinary unmarked interrupted record is NOT replayable: it is rejected.
	// (The boot reap's resolve writes no marker.) Build one through the store's
	// reap path and prove the handler refuses it.
	other := orphanLocalHubRecord(t, store, "h2", `[{"kind":"local-linux","cgroupId":"/cg/h2","nonce":"n1","pid":41,"startTime":"777"}]`)
	if _, err := store.ResolveReapedSpawn(other.ID, []string{"n1"}); err != nil {
		t.Fatalf("ResolveReapedSpawn: %v", err)
	}
	assertWireCode(t, orphanErr(m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: other.ID})), appwire.CodeInvalidParams)
}

func TestOrphanResolveRefusesRemoteOriginBeforeAnyLookup(t *testing.T) {
	m, _ := newOrphanResolveFixture(t, cleanLeaseVerify())
	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	// The id is unknown, so a lookup-first handler would answer not-found; the
	// origin refusal proves the guard ran first (before dedup and any lookup).
	err := orphanErr(m.OrphanResolve(ctx, appwire.HostOrphanResolveParams{ID: "00000000000000000099"}))
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), hostRoutingOriginBridge) {
		t.Fatalf("refusal %q does not name the origin %q", err, hostRoutingOriginBridge)
	}
}

func TestOperationsDetailCarriesTheOrphanBoundary(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")
	page, err := m.Operations(context.Background(), appwire.HostOperationsParams{ID: record.ID})
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(page.Operations) != 1 {
		t.Fatalf("operations page = %+v, want the record by id", page.Operations)
	}
	wire := page.Operations[0]
	if wire.State != appwire.OperationStateOrphanUnverified || wire.OrphanBoundary == nil || len(*wire.OrphanBoundary) != 1 {
		t.Fatalf("wire record = %+v, want one orphanBoundary member", wire)
	}
	entry := (*wire.OrphanBoundary)[0]
	remote := entry.BoundaryEntryRemoteFencing
	if remote == nil || remote.Kind != "remote-fencing" || len(remote.LeaseEntries) != 1 {
		t.Fatalf("boundary entry = %+v, want the persisted remote-fencing arm", entry)
	}
	// The resolved record's boundary is gone; only the marker remains.
	if _, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID}); err != nil {
		t.Fatalf("OrphanResolve: %v", err)
	}
	page, err = m.Operations(context.Background(), appwire.HostOperationsParams{ID: record.ID})
	if err != nil {
		t.Fatalf("Operations(after): %v", err)
	}
	if page.Operations[0].OrphanBoundary != nil || !page.Operations[0].OrphanResolved {
		t.Fatalf("resolved wire record = %+v, want no boundary and the marker", page.Operations[0])
	}
}
