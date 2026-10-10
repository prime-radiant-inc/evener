package hub

// evener/host/operations tests (deploy pipeline 08b §8, §10). The read is
// proved wired through the real server construction path, refused for a remote
// origin, and — the spec's no-lazy-attachment rule — proved to dial nothing.
// The record, cursor and bounds semantics live in hostops/cursor_test.go; this
// file pins the handler's translation: the page shapes (§10's
// hostBoundaries/generation presence rules), the §11 refusal envelopes, and
// the negative forward-allow-list assertion.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
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
// an unfiltered page with the authoritative hostBoundaries map and the
// controller-assigned records, and the cursor it returns resumes the page.
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
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

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
	bound, ok := page.HostBoundaries["m4"].(map[string]any)
	if !ok {
		t.Fatalf("hostBoundaries[m4] = %T %+v, want the object arm", page.HostBoundaries["m4"], page.HostBoundaries["m4"])
	}
	if bound["incarnationId"] != "inc-m4" || bound["generation"] != float64(2) || bound["presenceEpoch"] != float64(3) {
		t.Fatalf("hostBoundaries[m4] = %+v, want the mirrored triple", bound)
	}
	if page.NextCursor == "" {
		t.Fatal("page carried no nextCursor")
	}
	var rest appwire.HostOperationsResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerHostOperations, appwire.HostOperationsParams{Limit: 5, Cursor: page.NextCursor}, &rest); err != nil {
		t.Fatalf("evener/host/operations (continuation): %v", err)
	}
	if len(rest.Operations) != 1 || rest.Operations[0].ClientOperationID != "op-2" {
		t.Fatalf("continuation operations = %+v, want op-2", rest.Operations)
	}
}

// TestHostOperationsHostPinnedShapeAndHistoryReadability pins §10's presence
// rules on the handler's own translation — generation/incarnationId exactly on
// host-pinned pages, hostBoundaries exactly on unfiltered pages — and that the
// read never resolves the live registry: a removed host whose records and
// boundary remain in the store stays readable.
func TestHostOperationsHostPinnedShapeAndHistoryReadability(t *testing.T) {
	removed := hostreg.Host{Name: "gone", Generation: 4, IncarnationID: "inc-gone", PresenceEpoch: 2}
	store := operationsTestStore(t, []hostreg.Host{removed})
	seedOperationRecord(t, store, "gone", "op-gone", 4, "inc-gone")
	m, _, _ := planTestManager(t, "", nil, planSeams{})
	m.cfg.ops = store

	page, err := m.Operations(context.Background(), appwire.HostOperationsParams{Name: "gone"})
	if err != nil {
		t.Fatalf("Operations(gone): %v", err)
	}
	if page.Generation != 4 || page.IncarnationID != "inc-gone" {
		t.Fatalf("pinned page pair = %d/%q, want the stored historical pair", page.Generation, page.IncarnationID)
	}
	if page.HostBoundaries != nil {
		t.Fatalf("pinned page carried hostBoundaries %+v, want none", page.HostBoundaries)
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
	bound, ok := unfiltered.HostBoundaries["gone"].(appwire.HostBoundary)
	if !ok || bound.IncarnationID != "inc-gone" || bound.Generation != 4 || bound.PresenceEpoch != 2 {
		t.Fatalf("unfiltered hostBoundaries[gone] = %T %+v, want the stored triple object", unfiltered.HostBoundaries["gone"], unfiltered.HostBoundaries["gone"])
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
	m.cfg.ops = store
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

// TestHostOperationsRefusalEnvelopes pins §11's pairs on the handler's
// translation: a stale cursor is a conflict-class `stale-entry` naming the
// binding, an over-cap first page is the distinct `cursor-too-large` carrying
// {capBytes: 8192}, and a malformed cursor or unknown state is invalid params.
func TestHostOperationsRefusalEnvelopes(t *testing.T) {
	entry := planTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-m4"
	entry.PresenceEpoch = 3
	store := operationsTestStore(t, []hostreg.Host{entry})
	seedOperationRecord(t, store, "m4", "op-1", 2, "inc-m4")
	m, _, _ := planTestManager(t, "", []hostreg.Host{entry}, planSeams{})
	m.cfg.ops = store

	staleCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"pos":"00000000000000000001","compactSeq":0,"bounds":{},"quarantineEpoch":0}`))
	stale := wantStaleEntry(t, operationsErr(t, m, appwire.HostOperationsParams{Cursor: staleCursor}))
	if stale.Binding != appwire.StaleEntryBindingGeneration {
		t.Fatalf("stale binding = %q, want %q", stale.Binding, appwire.StaleEntryBindingGeneration)
	}

	var wire appwire.WireError
	if !errors.As(operationsErr(t, m, appwire.HostOperationsParams{Cursor: "%%%"}), &wire) {
		t.Fatal("malformed cursor did not refuse as a wire error")
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("malformed cursor code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
	wire = appwire.WireError{}
	if !errors.As(operationsErr(t, m, appwire.HostOperationsParams{State: "bogus"}), &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unknown state did not refuse invalid params: %+v", wire)
	}

	// The over-cap refusal, driven through the same store the read uses: 400
	// boundary hosts make the unfiltered bounds map exceed the 8 KiB cap.
	big := operationsTestStore(t, nil)
	boundaries := make(map[string]hostops.Boundary, 400)
	for i := range 400 {
		name := fmt.Sprintf("host-%03d", i)
		boundaries[name] = hostops.Boundary{Generation: 1, IncarnationID: "incarnation-" + name, PresenceEpoch: 1}
	}
	if err := big.MirrorBoundaries(boundaries, nil); err != nil {
		t.Fatalf("MirrorBoundaries: %v", err)
	}
	seedOperationRecord(t, big, "host-000", "op-1", 1, "incarnation-host-000")
	m.cfg.ops = big
	wire = appwire.WireError{}
	if !errors.As(operationsErr(t, m, appwire.HostOperationsParams{}), &wire) {
		t.Fatal("over-cap page did not refuse as a wire error")
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("over-cap code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	data, ok := wire.Data.(appwire.CursorTooLargeErrorData)
	if !ok {
		t.Fatalf("over-cap data = %T, want appwire.CursorTooLargeErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorCursorTooLarge || data.CapBytes != 8192 {
		t.Fatalf("over-cap data = %+v, want cursor-too-large with capBytes 8192", data)
	}
}

// TestHostOperationsCursorInvalidatedEnvelope pins §11's `cursor-invalidated`
// arm through the handler: a mid-pagination compaction surfaces the distinct
// discriminator (never stale-entry) with the compacting compactSeq and the
// affected host's bounds entry as stored at mint.
func TestHostOperationsCursorInvalidatedEnvelope(t *testing.T) {
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
	finishSeed := func(record hostops.Record) {
		t.Helper()
		if _, err := store.Transition(record.ID, hostops.StateComplete, func(r *hostops.Record) {
			r.Result = &hostops.Result{OK: true, Message: "done"}
		}); err != nil {
			t.Fatalf("Transition(%s): %v", record.ID, err)
		}
	}
	first := seedOperationRecord(t, store, "m4", "op-1", 2, "inc-m4")
	finishSeed(first)
	second := seedOperationRecord(t, store, "m4", "op-2", 2, "inc-m4")
	finishSeed(second) // per-host bound: the first record compacted, compactSeq 1.

	m, _, _ := planTestManager(t, "", []hostreg.Host{entry}, planSeams{})
	m.cfg.ops = store
	page, err := m.Operations(context.Background(), appwire.HostOperationsParams{Name: "m4", Limit: 1})
	if err != nil {
		t.Fatalf("Operations(page 1): %v", err)
	}
	if len(page.Operations) != 1 || page.Operations[0].ID != second.ID {
		t.Fatalf("page 1 = %+v, want the surviving record %s", page.Operations, second.ID)
	}

	third := seedOperationRecord(t, store, "m4", "op-3", 2, "inc-m4")
	finishSeed(third) // compacts the first page's record: the cursor is invalidated.

	var wire appwire.WireError
	if !errors.As(operationsErr(t, m, appwire.HostOperationsParams{Name: "m4", Cursor: page.NextCursor}), &wire) {
		t.Fatal("the invalidated continuation did not refuse as a wire error")
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	data, ok := wire.Data.(appwire.CursorInvalidatedErrorData)
	if !ok {
		t.Fatalf("data = %T, want appwire.CursorInvalidatedErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorCursorInvalidated {
		t.Fatalf("evenerErrorInfo = %q, want %q", data.EvenerErrorInfo, appwire.ErrorCursorInvalidated)
	}
	if data.CompactSeq != 2 || data.Host != "m4" {
		t.Fatalf("data = %+v, want compactSeq 2 on host m4", data)
	}
	if bound, ok := data.Bounds.(appwire.HostBoundary); !ok ||
		bound.Generation != 2 || bound.IncarnationID != "inc-m4" || bound.PresenceEpoch != 3 {
		t.Fatalf("bounds = %T %+v, want the minted triple", data.Bounds, data.Bounds)
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
	m.cfg.ops = store
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
