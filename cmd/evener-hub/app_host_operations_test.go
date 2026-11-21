package hub

// evener/host/operations tests (deploy pipeline 08b §8, §10). The read is
// proved wired through the real server construction path, refused for a remote
// origin, and — the spec's no-lazy-attachment rule — proved to dial nothing.
// The record and filter semantics live in hostops/operations_test.go; this file
// pins the handler's translation: the bounded listing shapes (§10's
// generation/incarnationId presence rule), the invalid-params refusal, and the
// negative forward-allow-list assertion.

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// operationsTestStore is a store seeded the way the registry's writes seed one:
// a boundary triple and one record per pair the test asks for.
func operationsTestStore(t *testing.T, hosts []hostreg.Host) *hostops.Store {
	t.Helper()
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	boundaries := make(map[string]hostops.Boundary, len(hosts))
	for _, host := range hosts {
		boundaries[host.Name] = hostops.Boundary{
			Generation:    host.Generation,
			IncarnationID: host.IncarnationID,
			PresenceEpoch: host.PresenceEpoch,
		}
	}
	if err := store.MirrorBoundaries(boundaries, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	return store
}

// seedOperationRecord persists one pending operation record for host.
func seedOperationRecord(t *testing.T, store *hostops.Store, host, operationID string, generation uint64, incarnationID string) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: operationID,
		Host:              host,
		Kind:              hostops.KindDeploy,
		Generation:        generation,
		IncarnationID:     incarnationID,
	})
	if err != nil {
		t.Fatalf("Create(%s): %v", host, err)
	}
	return record
}

// TestHostOperationsReadsThroughTheRealServer is the proof the read is wired:
// the real server construction path installs it, a real /rpc dispatch answers
// an unfiltered listing with the controller-assigned records, and the limit
// bounds the listing.
func TestHostOperationsReadsThroughTheRealServer(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/hub.toml"
	entry := planTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-m4"
	entry.PresenceEpoch = 3
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{entry}); err != nil {
		t.Fatalf("writeHubTOMLHosts: %v", err)
	}
	store := operationsTestStore(t, []hostreg.Host{entry})
	first := seedOperationRecord(t, store, "m4", "op-1", 2, "inc-m4")
	seedOperationRecord(t, store, "m4", "op-2", 2, "inc-m4")

	cfg, _, _ := hostManageWiringConfig(t, configPath, []hostreg.Host{entry}, detachRefusingRunner{})
	cfg.RemoteHostSSHManager = nil
	cfg.RemoteHostOpsStore = store
	cfg.RemoteHostClient = nil
	cfg.RemoteHostClientIfAttached = func(string) (*appwire.Client, bool) { return &appwire.Client{}, true }
	hub, web := newHubRPCTestServerWithWeb(t, cfg)
	defer hub.Close()
	if web.hostManage == nil {
		t.Fatal("the real server construction did not install the host-management surface")
	}
	client := dialHubRPCInitialized(t, hub)

	var page appwire.HostOperationsResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Limit: 1}, &page); err != nil {
		t.Fatalf("evener/host/operations: %v", err)
	}
	if len(page.Operations) != 1 || page.Operations[0].ID != first.ID {
		t.Fatalf("page operations = %+v, want the first record %q", page.Operations, first.ID)
	}
	// The seeded records were pending when the store was written; the manager's
	// §7 boot pass moves every in-flight record to `interrupted` before it
	// serves, so the page renders that terminal state (the read path serves what
	// the store holds).
	if page.Operations[0].ClientOperationID != "op-1" || page.Operations[0].State != appwire.OperationStateInterrupted {
		t.Fatalf("record = %+v, want op-1 interrupted", page.Operations[0])
	}
	if page.Generation != 0 || page.IncarnationID != "" {
		t.Fatalf("unfiltered page carried pair %d/%q, want none", page.Generation, page.IncarnationID)
	}

	var all appwire.HostOperationsResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{}, &all); err != nil {
		t.Fatalf("evener/host/operations (unbounded): %v", err)
	}
	if len(all.Operations) != 2 {
		t.Fatalf("unbounded listing = %+v, want both records", all.Operations)
	}
}

// TestHostOperationsHostPinnedShapeAndHistoryReadability pins §10's presence
// rule on the handler's own translation — generation/incarnationId exactly on
// host-pinned reads — and that the read never resolves the live registry: a
// removed host whose records and boundary remain in the store stays readable.
func TestHostOperationsHostPinnedShapeAndHistoryReadability(t *testing.T) {
	removed := hostreg.Host{Name: "gone", Generation: 4, IncarnationID: "inc-gone", PresenceEpoch: 2}
	store := operationsTestStore(t, []hostreg.Host{removed})
	seedOperationRecord(t, store, "gone", "op-gone", 4, "inc-gone")
	m, _, _ := planTestManager(t, "", nil, planSeams{})
	m.cfg.ops.Store(store)

	page, err := m.Operations(context.Background(), appwire.HostOperationsParams{Name: "gone"})
	if err != nil {
		t.Fatalf("Operations(gone): %v", err)
	}
	if page.Generation != 4 || page.IncarnationID != "inc-gone" {
		t.Fatalf("pinned page pair = %d/%q, want the stored historical pair", page.Generation, page.IncarnationID)
	}
	if len(page.Operations) != 1 || page.Operations[0].Host != "gone" {
		t.Fatalf("pinned page = %+v, want the removed host's record", page.Operations)
	}
	if page.Operations[0].HostRemoved {
		t.Fatal("record rendered hostRemoved though no removal tombstone marked it")
	}

	unfiltered, err := m.Operations(context.Background(), appwire.HostOperationsParams{})
	if err != nil {
		t.Fatalf("Operations(unfiltered): %v", err)
	}
	if unfiltered.Generation != 0 || unfiltered.IncarnationID != "" {
		t.Fatalf("unfiltered page carried pair %d/%q, want none", unfiltered.Generation, unfiltered.IncarnationID)
	}
	if len(unfiltered.Operations) != 1 || unfiltered.Operations[0].Host != "gone" {
		t.Fatalf("unfiltered page = %+v, want the stored record", unfiltered.Operations)
	}
}

// TestHostOperationsNeverDialsAndRefusesRemoteOrigin pins the spec's read
// rules: a read dials nothing (§13: "with no attach call, no explicit source,
// no read dials anything — list/status/operations never attach"), and a
// bridge-originated request is refused before it reads a byte.
func TestHostOperationsNeverDialsAndRefusesRemoteOrigin(t *testing.T) {
	entry := planTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-m4"
	entry.PresenceEpoch = 3
	store := operationsTestStore(t, []hostreg.Host{entry})
	seedOperationRecord(t, store, "m4", "op-1", 2, "inc-m4")
	m, _, _ := planTestManager(t, "", []hostreg.Host{entry}, planSeams{})
	m.cfg.ops.Store(store)
	dialed := false
	m.cfg.client = func(context.Context, string) (*appwire.Client, error) {
		dialed = true
		return nil, errors.New("a read must never dial")
	}
	m.cfg.clientIfAttached = func(string) (*appwire.Client, bool) {
		dialed = true
		return nil, false
	}
	m.cfg.manager = nil

	if _, err := m.Operations(context.Background(), appwire.HostOperationsParams{}); err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if dialed {
		t.Fatal("the read reached a dialing seam")
	}

	ctx := withHostRoutingOrigin(context.Background(), hostRoutingOriginBridge)
	if _, err := m.Operations(ctx, appwire.HostOperationsParams{}); err == nil {
		t.Fatal("bridge-originated read accepted, want refusal")
	} else {
		assertWireCode(t, err, appwire.CodeInvalidParams)
	}
}

// TestHostOperationsRefusalEnvelopes pins the handler's translation of a
// malformed query: an unknown state is invalid params.
func TestHostOperationsRefusalEnvelopes(t *testing.T) {
	entry := planTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-m4"
	entry.PresenceEpoch = 3
	store := operationsTestStore(t, []hostreg.Host{entry})
	seedOperationRecord(t, store, "m4", "op-1", 2, "inc-m4")
	m, _, _ := planTestManager(t, "", []hostreg.Host{entry}, planSeams{})
	m.cfg.ops.Store(store)

	var wire appwire.WireError
	if !errors.As(operationsErr(t, m, appwire.HostOperationsParams{State: "bogus"}), &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unknown state did not refuse invalid params: %+v", wire)
	}
}

// TestHostOperationsDetailCarriesTheCompactedMarker pins §12's "`compacted:
// true` exactly on tombstone replays" through the handler: the `id` detail
// filter resolves a compacted record out of its tombstone, with its retained
// terminal result, while a listed page never carries the marker.
func TestHostOperationsDetailCarriesTheCompactedMarker(t *testing.T) {
	entry := planTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-m4"
	entry.PresenceEpoch = 3
	store, err := hostops.OpenWithRetention(hostops.StorePath(t.TempDir()), hostops.RetentionPolicy{TerminalPerHost: 1})
	if err != nil {
		t.Fatalf("OpenWithRetention: %v", err)
	}
	if err := store.MirrorBoundaries(map[string]hostops.Boundary{
		"m4": {Generation: 2, IncarnationID: "inc-m4", PresenceEpoch: 3},
	}, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	finishSeedOperation(t, store, seedOperationRecord(t, store, "m4", "op-compacted", 2, "inc-m4"))
	finishSeedOperation(t, store, seedOperationRecord(t, store, "m4", "op-kept", 2, "inc-m4"))

	m, _, _ := planTestManager(t, "", []hostreg.Host{entry}, planSeams{})
	m.cfg.ops.Store(store)
	compactedID := "00000000000000000001"
	detail, err := m.Operations(context.Background(), appwire.HostOperationsParams{Name: "m4", ID: compactedID})
	if err != nil {
		t.Fatalf("Operations(id detail): %v", err)
	}
	if len(detail.Operations) != 1 || !detail.Operations[0].Compacted {
		t.Fatalf("detail page = %+v, want the compacted replay", detail.Operations)
	}
	if detail.Operations[0].ID != compactedID || detail.Operations[0].Result == nil || !detail.Operations[0].Result.OK {
		t.Fatalf("detail replay = %+v, want the retained outcome", detail.Operations[0])
	}
	// The listing page carries no compacted marker: the replay is a detail
	// resolution, not a listed row.
	list, err := m.Operations(context.Background(), appwire.HostOperationsParams{Name: "m4"})
	if err != nil {
		t.Fatalf("Operations(list): %v", err)
	}
	for _, record := range list.Operations {
		if record.Compacted {
			t.Fatalf("list page carried a compacted record: %+v", record)
		}
	}
}

// finishSeedOperation lands one seeded record as a complete operation.
func finishSeedOperation(t *testing.T, store *hostops.Store, record hostops.Record) {
	t.Helper()
	if _, err := store.Transition(record.ID, hostops.StateComplete, func(r *hostops.Record) {
		r.Result = &hostops.Result{OK: true, Message: "done"}
	}); err != nil {
		t.Fatalf("Transition(%s): %v", record.ID, err)
	}
}

// TestHostOperationsNotForwarded pins the negative allow-list assertion: the
// read acts on this controller's own operation store, so it is never forwarded
// to a remote hub.
func TestHostOperationsNotForwarded(t *testing.T) {
	if appwire.IsHostRequestMethod(appwire.MethodEvenerHostOperations) {
		t.Fatalf("controller-local %q is on the remote forward allow-list", appwire.MethodEvenerHostOperations)
	}
}

// operationsErr runs one read and returns its error, requiring the zero
// response.
func operationsErr(t *testing.T, m *hubHostManager, params appwire.HostOperationsParams) error {
	t.Helper()
	response, err := m.Operations(context.Background(), params)
	if err == nil {
		t.Fatalf("Operations(%+v) = %+v, want a refusal", params, response)
	}
	return err
}
