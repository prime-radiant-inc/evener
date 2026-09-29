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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
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
		RemoteHostOrphanVerify: func(_ context.Context, record hostops.Record) error {
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
		RemoteHostOrphanVerify: func(_ context.Context, record hostops.Record) error {
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
// accepted-but-unneeded rule for an *attributed* caller: on a record that does
// not carry the boundary-unavailable entry, a present attestation whose operator
// matches the session identity is accepted and persisted, and a boundaryRef that
// matches nothing is never a refusal. (H1: an unattributed attestation is
// refused outright, pinned in TestOrphanResolveAttestationBindings.)
func TestOrphanResolveAcceptsAnUnneededAttestation(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent",
		RecordID: record.ID, BoundaryRef: "/state/not-this-records-boundary.json",
		ObservedAt: m.nowTime().UTC().Format("2006-01-02T15:04:05Z"),
	}
	resolved, err := m.OrphanResolve(withSessionOperator(context.Background(), "operator-alpha"),
		appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
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
	// Every mismatched binding refuses before any clearance. (The unattributed
	// posture is pinned separately: recorded as given, never refused.)
	for name, mutate := range map[string]func(*appwire.HostOrphanResolveAttestation){
		"operator":    func(a *appwire.HostOrphanResolveAttestation) { a.Operator = "someone-else" },
		"no operator": func(a *appwire.HostOrphanResolveAttestation) { a.Operator = "" },
		"recordId":    func(a *appwire.HostOrphanResolveAttestation) { a.RecordID = "00000000000000000042" },
		"boundaryRef": func(a *appwire.HostOrphanResolveAttestation) { a.BoundaryRef = "/state/other.json" },
		"stale":       func(a *appwire.HostOrphanResolveAttestation) { a.ObservedAt = "2026-01-01T00:00:00Z" },
		"future": func(a *appwire.HostOrphanResolveAttestation) {
			a.ObservedAt = m.nowTime().Add(time.Hour).UTC().Format(time.RFC3339)
		},
		"statement": func(a *appwire.HostOrphanResolveAttestation) { a.Statement = "trust me" },
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

// TestOrphanResolveRecordsAnUnattributedAttestation pins the fail-audited
// posture required for §8/§11's path back: with no transport principal the
// attestation clears a boundary-unavailable record, recorded as given and
// marked unattributed — never verified — and the persisted replay carries the
// same marker.
func TestOrphanResolveRecordsAnUnattributedAttestation(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record, err := store.Create(hostops.NewRecord{ClientOperationID: "client-unattributed", Host: "h1", Kind: hostops.KindDeploy, Generation: 7, IncarnationID: "inc-unattributed"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	custodyRef := "/state/operations.json.custody-unattributed"
	if _, err := store.SetOrphanBoundary(record.ID, json.RawMessage(
		`[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"`+custodyRef+`"}]`), nil); err != nil {
		t.Fatalf("SetOrphanBoundary(unavailable): %v", err)
	}
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent",
		RecordID: record.ID, BoundaryRef: custodyRef,
		ObservedAt: m.nowTime().UTC().Format(time.RFC3339),
	}
	resolved, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
	if err != nil {
		t.Fatalf("unattributed resolve = %v, want the fail-audited clearance", err)
	}
	if !resolved.OrphanResolved || resolved.State != appwire.OperationStateInterrupted {
		t.Fatalf("response = %+v, want the resolved record", resolved)
	}
	stored, ok := store.Record(record.ID)
	if !ok || stored.OrphanAttestation == nil {
		t.Fatalf("stored record = %+v (ok %v), want the persisted attestation", stored, ok)
	}
	if !stored.OrphanAttestation.Unattributed || stored.OrphanAttestation.Operator != "operator-alpha" {
		t.Fatalf("stored attestation = %+v, want it recorded as given and marked unattributed", stored.OrphanAttestation)
	}
	// The API replay answers from the persisted resolution, marker included.
	replayed, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil || !replayed.OrphanResolved {
		t.Fatalf("handler replay = (%+v, %v), want the persisted resolution", replayed, err)
	}
	storeReplay, err := store.ResolveOrphan(record.ID, nil)
	if err != nil || storeReplay.OrphanAttestation == nil || !storeReplay.OrphanAttestation.Unattributed {
		t.Fatalf("store replay = (%+v, %v), want the unattributed marker preserved", storeReplay.OrphanAttestation, err)
	}
}

// TestOrphanResolveRefusesAFutureObservedAtBeyondSkew pins M3 directly: an
// observation beyond the bounded clock-skew allowance refuses like a stale one.
func TestOrphanResolveRefusesAFutureObservedAtBeyondSkew(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record := quarantinedHubRecord(t, store, "h1")
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent", RecordID: record.ID,
		ObservedAt: m.nowTime().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	_, err := m.OrphanResolve(withSessionOperator(context.Background(), "operator-alpha"),
		appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), "future") {
		t.Fatalf("future refusal = %q, want it to name the future observation", err)
	}
	if stored, ok := store.Record(record.ID); !ok || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("a future attestation cleared the record: %+v (ok %v)", stored, ok)
	}
}

func TestOrphanResolveMixedUnavailableBoundaryRefuses(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record, err := store.Create(hostops.NewRecord{ClientOperationID: "client-mixed", Host: "h1", Kind: hostops.KindDeploy, Generation: 7, IncarnationID: "inc-mixed"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	mixed := `[{"kind":"local-linux","cgroupId":"/cg/h1","nonce":"n1","pid":41,"startTime":"777"},` +
		`{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/state/custody.json"}]`
	if _, err := store.SetOrphanBoundary(record.ID, json.RawMessage(mixed), nil); err != nil {
		t.Fatalf("SetOrphanBoundary(mixed): %v", err)
	}
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent", RecordID: record.ID,
		BoundaryRef: "/state/custody.json", ObservedAt: m.nowTime().UTC().Format(time.RFC3339),
	}
	_, err = m.OrphanResolve(withSessionOperator(context.Background(), "operator-alpha"),
		appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation})
	assertWireCode(t, err, appwire.CodeInvalidParams)
	if !strings.Contains(err.Error(), "mixes") {
		t.Fatalf("mixed-boundary refusal = %q, want it to name the mixed shape", err)
	}
	if stored, ok := store.Record(record.ID); !ok || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("a mixed boundary cleared the record: %+v (ok %v)", stored, ok)
	}
}

// TestOrphanResolveAcceptsASmallFutureClockSkew pins the bounded skew: an
// attestation within the allowance resolves, one beyond it refuses (the
// "future" case in the bindings table).
func TestOrphanResolveAcceptsASmallFutureClockSkew(t *testing.T) {
	m, store := newOrphanResolveFixture(t, cleanLeaseVerify())
	record, err := store.Create(hostops.NewRecord{ClientOperationID: "client-skew", Host: "h1", Kind: hostops.KindDeploy, Generation: 7, IncarnationID: "inc-skew"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetOrphanBoundary(record.ID, json.RawMessage(
		`[{"kind":"boundary-unavailable","reason":"corrupt-store-custody","custodyRef":"/state/custody-skew.json"}]`), nil); err != nil {
		t.Fatalf("SetOrphanBoundary: %v", err)
	}
	attestation := appwire.HostOrphanResolveAttestation{
		Operator: "operator-alpha", Statement: "orphan-verified-absent", RecordID: record.ID,
		BoundaryRef: "/state/custody-skew.json", ObservedAt: m.nowTime().Add(time.Minute).UTC().Format(time.RFC3339),
	}
	if _, err := m.OrphanResolve(withSessionOperator(context.Background(), "operator-alpha"),
		appwire.HostOrphanResolveParams{ID: record.ID, Attestation: &attestation}); err != nil {
		t.Fatalf("resolve with a one-minute future skew = %v, want acceptance", err)
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

// scriptedFenceRunner serves the helper protocol's read-only answers — `version`
// and `entries` — over a scripted remote, so the resolve's remote arm runs
// without ssh or a host.
type scriptedFenceRunner struct {
	version     string
	versionExit int
	entries     string
	commands    []string
}

func (r *scriptedFenceRunner) Run(_ context.Context, command string) (string, string, int, error) {
	r.commands = append(r.commands, command)
	switch {
	case strings.HasSuffix(command, " version"):
		if r.versionExit != 0 {
			return "", "no helper here", r.versionExit, nil
		}
		return r.version, "", 0, nil
	case strings.HasSuffix(command, " entries"):
		return r.entries, "", 0, nil
	}
	return "", "unexpected command", 2, nil
}

// fenceEntries wraps lease-entry JSON in the helper's enumeration envelope.
func fenceEntries(entries string) string {
	return `{"version":1,"entries":[` + entries + `]}`
}

// livePidEntry is one running lease entry whose stored identity is the pid pair
// the quarantined record's boundary carries (41/777).
const livePidEntry = `{"id":"n1","command":"deploy --now","registeredAt":"2026-09-28T10:00:00Z",` +
	`"ownership":{"pid":41,"pidStartTime":"777"},"state":"running","descendants":[]}`

// newOrphanResolveRunnerFixture builds a manager whose production routing
// (verifyOrphanRecord) is exercised: no injected verify seam, only the remote
// runner.
func newOrphanResolveRunnerFixture(t *testing.T, runner hostfence.Runner) (*hubHostManager, *hostops.Store) {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore: store,
		RemoteHostOrphanFenceRunner: func(string) hostfence.Runner {
			return runner
		},
	}, "", nil, nil)
	return m, store
}

// TestOrphanResolveRemoteFencingThroughTheHelper pins M1's real path: a
// remote-fencing record resolves once the helper's lease enumeration confirms
// every persisted entry gone, and refuses while one is still live.
func TestOrphanResolveRemoteFencingThroughTheHelper(t *testing.T) {
	runner := &scriptedFenceRunner{version: "1\n", entries: fenceEntries("")}
	m, store := newOrphanResolveRunnerFixture(t, runner)
	record := quarantinedHubRecord(t, store, "h1")
	resolved, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil {
		t.Fatalf("OrphanResolve(remote, all gone) = %v, want the clean resolution", err)
	}
	if !resolved.OrphanResolved || resolved.State != appwire.OperationStateInterrupted {
		t.Fatalf("response = %+v, want the resolved record", resolved)
	}
	if len(runner.commands) < 2 || !strings.HasSuffix(runner.commands[0], " version") || !strings.HasSuffix(runner.commands[1], " entries") {
		t.Fatalf("helper commands = %v, want the read-only version self-test then the enumeration", runner.commands)
	}
	if _, ok := store.FencingQuarantine("h1"); ok {
		t.Fatal("the quarantine marker survived the resolved remote boundary")
	}

	// A persisted entry still live under its stored identity refuses busy.
	live := &scriptedFenceRunner{version: "1\n", entries: fenceEntries(livePidEntry)}
	liveManager, liveStore := newOrphanResolveRunnerFixture(t, live)
	liveRecord := quarantinedHubRecord(t, liveStore, "h2")
	_, err = liveManager.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: liveRecord.ID})
	assertWireCode(t, err, appwire.CodeConflict)
	var liveWire appwire.WireError
	if !errors.As(err, &liveWire) {
		t.Fatalf("live-entry refusal = %#v, want a wire error", err)
	}
	if data, ok := liveWire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorHostBusyTransient {
		t.Fatalf("live-entry refusal data = %#v, want the transient-busy form", liveWire.Data)
	}
	if !strings.Contains(err.Error(), "boundary members") {
		t.Fatalf("live-entry refusal = %q, want it to name the held members", err)
	}
	// A live entry whose identity differs is unrelated work: already clean.
	other := &scriptedFenceRunner{version: "1\n", entries: fenceEntries(strings.Replace(livePidEntry, `"pid":41`, `"pid":99`, 1))}
	otherManager, otherStore := newOrphanResolveRunnerFixture(t, other)
	otherRecord := quarantinedHubRecord(t, otherStore, "h3")
	if _, err := otherManager.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: otherRecord.ID}); err != nil {
		t.Fatalf("OrphanResolve(unrelated live entry) = %v, want clean", err)
	}
}

// TestOrphanResolveHelperGateRidesItsDiscriminator pins §8's helper gate on the
// resolve path: an absent or untrusted helper is its own typed refusal, not a
// generic busy, so the operator installs the pinned helper out-of-band.
func TestOrphanResolveHelperGateRidesItsDiscriminator(t *testing.T) {
	absent := &scriptedFenceRunner{versionExit: 1}
	absentManager, absentStore := newOrphanResolveRunnerFixture(t, absent)
	absentRecord := quarantinedHubRecord(t, absentStore, "h1")
	_, err := absentManager.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: absentRecord.ID})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("absent-helper refusal = %#v, want a wire error", err)
	}
	data, ok := wire.Data.(appwire.FencingHelperErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperAbsent || data.Host != "h1" {
		t.Fatalf("absent-helper data = %#v, want the fencing-helper-absent form naming h1", wire.Data)
	}

	untrusted := &scriptedFenceRunner{version: "2\n"}
	untrustedManager, untrustedStore := newOrphanResolveRunnerFixture(t, untrusted)
	untrustedRecord := quarantinedHubRecord(t, untrustedStore, "h2")
	_, err = untrustedManager.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: untrustedRecord.ID})
	if !errors.As(err, &wire) {
		t.Fatalf("untrusted-helper refusal = %#v, want a wire error", err)
	}
	if data, ok := wire.Data.(appwire.FencingHelperErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperUntrusted || data.Version != "2" {
		t.Fatalf("untrusted-helper data = %#v, want the fencing-helper-untrusted form naming version 2", wire.Data)
	}
}

// TestOrphanResolveWithoutARemoteRunnerFailsClosedHonestly pins the message
// half of M1: with no runner the boundary cannot be enumerated and the refusal
// says so, instead of telling the operator to retry a call that fails
// identically.
func TestOrphanResolveWithoutARemoteRunnerFailsClosedHonestly(t *testing.T) {
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{RemoteHostOpsStore: store}, "", nil, nil)
	record := quarantinedHubRecord(t, store, "h1")
	_, err = m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	assertWireCode(t, err, appwire.CodeConflict)
	if !strings.Contains(err.Error(), "cannot be enumerated") {
		t.Fatalf("no-runner refusal = %q, want it to say the boundary cannot be enumerated", err)
	}
	if strings.Contains(err.Error(), "then retry") {
		t.Fatalf("no-runner refusal = %q, want no retry advice for an enumeration that cannot appear", err)
	}
}

// TestRecoveryMutationsCarryTheOrphanFence pins §8's discriminator assignment
// on the recovery mutations: teardown-retry and teardown-recover refuse with
// orphan-fenced-busy naming the blocking record while an orphan-unverified
// record is open, and with the quarantine fencing-failure form when the marker
// is present.
func TestRecoveryMutationsCarryTheOrphanFence(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts := newFlakyRegistry(t, hostRegistryEntries(cfg))
	store, err := hostops.Open(hostops.StorePath(dir))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{RemoteHostOpsStore: store}, configPath, hosts.Registry, nil)
	host, ok := m.cfg.hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	remnantID := mintRemnantID()
	remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, "", m.nowTime())
	entries := m.cfg.store.snapshot()
	if err := m.persistHosts(entries, entries, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}}); err != nil {
		t.Fatalf("stage remnant: %v", err)
	}
	orphan := orphanLocalHubRecord(t, store, "side", `[{"kind":"local-linux","cgroupId":"/cg/side","nonce":"n1","pid":41,"startTime":"777"}]`)

	assertFence := func(t *testing.T, err error, want appwire.ErrorInfo, recordID string) {
		t.Helper()
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("refusal = %#v, want a wire error", err)
		}
		switch want {
		case appwire.ErrorOrphanFencedBusy:
			data, ok := wire.Data.(appwire.OrphanFencedBusyErrorData)
			if !ok || data.EvenerErrorInfo != want || data.RecordID != recordID {
				t.Fatalf("refusal data = %#v, want orphan-fenced-busy naming %s", wire.Data, recordID)
			}
		default:
			data, ok := wire.Data.(appwire.FencingFailureErrorData)
			if !ok || data.EvenerErrorInfo != want || data.Host != "side" {
				t.Fatalf("refusal data = %#v, want the quarantine form naming side", wire.Data)
			}
		}
	}
	_, err = m.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID})
	assertFence(t, err, appwire.ErrorOrphanFencedBusy, orphan.ID)
	_, err = m.TeardownRecover(context.Background(), appwire.HostTeardownRecoverParams{RemnantID: remnantID})
	assertFence(t, err, appwire.ErrorOrphanFencedBusy, orphan.ID)
	// The fence held: the remnant is still open, and no attempt was claimed.
	if _, open := m.cfg.store.markedRemnantFor("side"); !open {
		t.Fatal("a fenced repair call closed the remnant")
	}

	// A quarantined host refuses with the fencing-failure form (precedence).
	quarantined := quarantinedHubRecord(t, store, "side2")
	_ = quarantined
	dir2 := t.TempDir()
	configPath2 := filepath.Join(dir2, "hub.toml")
	if err := writeHubTOMLHosts(configPath2, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml 2: %v", err)
	}
	cfg2, err := LoadConfig(configPath2)
	if err != nil {
		t.Fatalf("LoadConfig 2: %v", err)
	}
	hosts2 := newFlakyRegistry(t, hostRegistryEntries(cfg2))
	store2, err := hostops.Open(hostops.StorePath(dir2))
	if err != nil {
		t.Fatalf("hostops.Open 2: %v", err)
	}
	m2 := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{RemoteHostOpsStore: store2}, configPath2, hosts2.Registry, nil)
	host2, _ := m2.cfg.hosts.Get("side")
	remnantID2 := mintRemnantID()
	remnant2 := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host2, "", m2.nowTime())
	entries2 := m2.cfg.store.snapshot()
	if err := m2.persistHosts(entries2, entries2, hostPersistChange{remnant: &pendingHostRemnant{RemnantID: remnantID2, Remnant: remnant2}}); err != nil {
		t.Fatalf("stage remnant 2: %v", err)
	}
	quarantinedRecord := quarantinedHubRecord(t, store2, "side")
	_, err = m2.TeardownRetry(context.Background(), appwire.HostTeardownRetryParams{RemnantID: remnantID2})
	assertFence(t, err, appwire.ErrorFencingFailure, quarantinedRecord.ID)
}

// TestOperationRecordWireNormalizesAnEmptyBoundary pins L1: an orphan-unverified
// record whose raw boundary bytes are absent decodes to a nil slice, and the
// wire must still render the explicit `[]` §9's presence rule requires — never
// `orphanBoundary: null`.
func TestOperationRecordWireNormalizesAnEmptyBoundary(t *testing.T) {
	wire, err := operationRecordWire(hostops.Record{
		ID:                "00000000000000000001",
		ClientOperationID: "client-empty",
		Host:              "h1",
		Kind:              hostops.KindDeploy,
		State:             hostops.StateOrphanUnverified,
		Generation:        7,
		IncarnationID:     "inc-h1",
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("operationRecordWire: %v", err)
	}
	if wire.OrphanBoundary == nil {
		t.Fatal("the wire carries no orphanBoundary pointer for an orphan-unverified record")
	}
	if len(*wire.OrphanBoundary) != 0 {
		t.Fatalf("wire orphanBoundary = %+v, want the empty array", *wire.OrphanBoundary)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"orphanBoundary":[]`) {
		t.Fatalf("wire bytes = %s, want an explicit []", raw)
	}
}

// TestOrphanResolveReplaysACompactedResolution pins §5's replay horizon across
// compaction at the handler: a resolved record that has compacted still answers
// its persisted resolution by id, and an unmarked compacted record refuses as a
// validation refusal (never not-found).
func TestOrphanResolveReplaysACompactedResolution(t *testing.T) {
	store, err := hostops.OpenWithRetention(hostops.StorePath(t.TempDir()),
		hostops.RetentionPolicy{TerminalPerHost: 1, TerminalStoreWide: 10})
	if err != nil {
		t.Fatalf("OpenWithRetention: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{
		RemoteHostOpsStore: store,
		RemoteHostOrphanVerify: func(_ context.Context, record hostops.Record) error {
			return hostfence.VerifyOrphanBoundary(record, cleanLeaseVerify())
		},
	}, "", nil, nil)
	record := quarantinedHubRecord(t, store, "h1")
	if _, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID}); err != nil {
		t.Fatalf("OrphanResolve: %v", err)
	}
	// A second terminal record for the same host exceeds TerminalPerHost, so the
	// resolved record compacts into a tombstone.
	sibling := orphanLocalHubRecord(t, store, "h1", `[{"kind":"local-linux","cgroupId":"/cg/h1","nonce":"n1","pid":41,"startTime":"777"}]`)
	if _, err := store.ResolveReapedSpawn(sibling.ID, []string{"n1"}); err != nil {
		t.Fatalf("ResolveReapedSpawn: %v", err)
	}
	if _, ok := store.Record(record.ID); ok {
		t.Fatal("the resolved record did not compact, so the replay path is untested")
	}
	replayed, err := m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: record.ID})
	if err != nil || !replayed.OrphanResolved || !replayed.Compacted {
		t.Fatalf("handler replay of the compacted id = (%+v, %v), want the persisted resolution", replayed, err)
	}
	if replayed.OrphanBoundary != nil {
		t.Fatalf("replay = %+v, want no boundary", replayed)
	}
	// A later unmarked terminal record compacts the sibling in turn; the handler
	// refuses it as a validation refusal, never not-found.
	third := orphanLocalHubRecord(t, store, "h1", `[{"kind":"local-linux","cgroupId":"/cg/h1","nonce":"n1","pid":41,"startTime":"777"}]`)
	if _, err := store.ResolveReapedSpawn(third.ID, []string{"n1"}); err != nil {
		t.Fatalf("ResolveReapedSpawn(third): %v", err)
	}
	if _, ok := store.Record(sibling.ID); ok {
		t.Fatal("the unmarked sibling did not compact")
	}
	assertWireCode(t, orphanErr(m.OrphanResolve(context.Background(), appwire.HostOrphanResolveParams{ID: sibling.ID})), appwire.CodeInvalidParams)
}

// TestAdmissionFenceBlocksPlanDeployRestartRemoveAndAttach pins M2's wiring:
// §8's fence is consulted by the shared admission paths — plan, deploy, restart,
// the mutations, and attach — with the quarantine form winning wherever the
// marker is present and the transient form for a local-reap orphan record.
func TestAdmissionFenceBlocksPlanDeployRestartRemoveAndAttach(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(dir))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{RemoteHostOpsStore: store}, configPath, hosts, nil)
	entry, ok := hosts.Get("side")
	if !ok {
		t.Fatal("fixture lost the host")
	}
	assertAdmission := func(t *testing.T, err error, want appwire.ErrorInfo) {
		t.Helper()
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("refusal = %#v, want a wire error", err)
		}
		switch want {
		case appwire.ErrorHostBusyTransient:
			if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != want {
				t.Fatalf("refusal data = %#v, want %q", wire.Data, want)
			}
		default:
			if data, ok := wire.Data.(appwire.FencingFailureErrorData); !ok || data.EvenerErrorInfo != want || data.Host != "side" {
				t.Fatalf("refusal data = %#v, want %q naming side", wire.Data, want)
			}
		}
	}

	// A local-reap orphan record fences the name: every admission path refuses
	// transient busy before doing anything for it.
	orphan := orphanLocalHubRecord(t, store, "side", `[{"kind":"local-linux","cgroupId":"/cg/side","nonce":"n1","pid":41,"startTime":"777"}]`)
	assertAdmission(t, errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "side"})), appwire.ErrorHostBusyTransient)
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{Name: "side", Token: "tok", OperationID: "op-deploy-1"})
	assertAdmission(t, err, appwire.ErrorHostBusyTransient)
	_, err = m.Restart(context.Background(), appwire.HostRestartParams{
		Name: "side", OperationID: "op-restart-1",
		Generation: entry.Generation, IncarnationID: entry.IncarnationID,
	})
	assertAdmission(t, err, appwire.ErrorHostBusyTransient)
	_, err = m.Remove(context.Background(), appwire.HostRemoveParams{
		Name: "side", MutationID: "mut-1",
		ExpectedGeneration: entry.Generation, ExpectedIncarnationID: entry.IncarnationID,
	})
	assertAdmission(t, err, appwire.ErrorHostBusyTransient)
	attachCfg := hubcore.WebConfig{HostOrphanFence: m.orphanAdmissionRefusal}
	_, err = hubHostAttach(context.Background(), attachCfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "side"})
	assertAdmission(t, err, appwire.ErrorHostBusyTransient)
	if stored, ok := store.Record(orphan.ID); !ok || stored.State != hostops.StateOrphanUnverified {
		t.Fatalf("a refused admission moved the orphan record: %+v (ok %v)", stored, ok)
	}

	// The quarantine marker's form wins wherever it is present.
	quarantined, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-side-q", Host: "side", Kind: hostops.KindDeploy,
		Generation: 7, IncarnationID: "inc-side-q",
	})
	if err != nil {
		t.Fatalf("Create(quarantined): %v", err)
	}
	if _, err := store.Transition(quarantined.ID, hostops.StateRunning, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if _, err := store.QuarantineFencing(quarantined.ID, json.RawMessage(orphanResolveBoundaryJSON)); err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	assertAdmission(t, errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "side"})), appwire.ErrorFencingFailure)
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{Name: "side", Token: "tok", OperationID: "op-deploy-2"})
	assertAdmission(t, err, appwire.ErrorFencingFailure)
	_, err = hubHostAttach(context.Background(), attachCfg, appsource.NewRegistry(), hosts, appwire.HostAttachParams{Host: "side"})
	assertAdmission(t, err, appwire.ErrorFencingFailure)
	// A different name is untouched by the fence.
	assertWireCode(t, errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "other"})), appwire.CodeInvalidParams)
}

// TestExplicitSourceIDsAttachIsFenced pins the second attach trigger: an
// explicit host-targeted thread/list dials through dialRemoteHost with its own
// cfg copy, and §8's fence refuses there — quarantined hosts with the
// fencing-failure form, orphan-unverified hosts with the transient form —
// before any remote command runs and before any operation record is minted.
func TestExplicitSourceIDsAttachIsFenced(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "side.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(dir))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	dials := 0
	webCfg := hubcore.WebConfig{
		RemoteHostRegistry: hosts,
		RemoteHostOpsStore: store,
		RemoteHostClient: func(context.Context, string) (*appwire.Client, error) {
			dials++
			return nil, errors.New("the fenced dial must not run")
		},
		HostOrphanFence: func(name string) error { return orphanAdmissionRefusalFor(store, name) },
	}
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewRemoteHubSource("side", nil, func(context.Context, string) (*appwire.Client, error) {
		dials++
		return nil, errors.New("the fenced dial must not run")
	}))
	assertFenced := func(t *testing.T, want appwire.ErrorInfo) {
		t.Helper()
		_, err := hubThreadList(context.Background(), webCfg, sources, appwire.ThreadListParams{SourceIDs: []string{"side"}})
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("explicit thread/list attach = %#v, want the fenced refusal", err)
		}
		switch want {
		case appwire.ErrorHostBusyTransient:
			if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != want {
				t.Fatalf("attach refusal data = %#v, want %q", wire.Data, want)
			}
		default:
			if data, ok := wire.Data.(appwire.FencingFailureErrorData); !ok || data.EvenerErrorInfo != want {
				t.Fatalf("attach refusal data = %#v, want %q", wire.Data, want)
			}
		}
	}

	// A local-reap orphan record refuses the transient form.
	orphanLocalHubRecord(t, store, "side", `[{"kind":"local-linux","cgroupId":"/cg/side","nonce":"n1","pid":41,"startTime":"777"}]`)
	assertFenced(t, appwire.ErrorHostBusyTransient)
	// The quarantine marker's form wins where it is present.
	quarantined, err := store.Create(hostops.NewRecord{
		ClientOperationID: "client-side-q2", Host: "side", Kind: hostops.KindDeploy,
		Generation: 7, IncarnationID: "inc-side-q2",
	})
	if err != nil {
		t.Fatalf("Create(quarantined): %v", err)
	}
	if _, err := store.Transition(quarantined.ID, hostops.StateRunning, nil); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if _, err := store.QuarantineFencing(quarantined.ID, json.RawMessage(orphanResolveBoundaryJSON)); err != nil {
		t.Fatalf("QuarantineFencing: %v", err)
	}
	assertFenced(t, appwire.ErrorFencingFailure)

	if dials != 0 {
		t.Fatalf("the fenced explicit attach dialed %d time(s), want zero", dials)
	}
	for _, record := range store.Records() {
		if record.Kind == hostops.KindDeploy && record.ClientOperationID != "client-side" && record.ClientOperationID != "client-side-q2" {
			t.Fatalf("a fenced attach minted operation record %+v", record)
		}
	}
	if len(store.Records()) != 2 {
		t.Fatalf("records = %d, want only the two fencing fixtures (no Ensure-minted record)", len(store.Records()))
	}
}

// TestEnsureHooksRefuseWhileFenced pins the reviewer's second half: the
// Ensure-triggered deploy/restart hooks refuse before minting a record, so any
// Ensure path (the reconnect ladder included) is fenced at the hook.
func TestEnsureHooksRefuseWhileFenced(t *testing.T) {
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(nil, nil, hubcore.WebConfig{RemoteHostOpsStore: store}, "", nil, nil)
	host := hostreg.Host{Name: "side", SSH: "side.example", Generation: 3, IncarnationID: "inc-side"}
	orphanLocalHubRecord(t, store, "side", `[{"kind":"local-linux","cgroupId":"/cg/side","nonce":"n1","pid":41,"startTime":"777"}]`)
	before := len(store.Records())
	if _, _, err := m.EnsureDeploy(host); err == nil {
		t.Fatal("EnsureDeploy(fenced) succeeded, want the fence refusal")
	} else {
		var wire appwire.WireError
		if !errors.As(err, &wire) {
			t.Fatalf("EnsureDeploy refusal = %#v, want a wire error", err)
		}
		if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorHostBusyTransient {
			t.Fatalf("EnsureDeploy refusal data = %#v, want %q", wire.Data, appwire.ErrorHostBusyTransient)
		}
	}
	if _, _, err := m.EnsureRestart(host); err == nil {
		t.Fatal("EnsureRestart(fenced) succeeded, want the fence refusal")
	}
	if got := len(store.Records()); got != before {
		t.Fatalf("a fenced Ensure minted %d record(s)", got-before)
	}
}
