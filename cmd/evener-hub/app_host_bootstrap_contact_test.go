package hub

// The first-contact caller's tests: the attach ladder's first-attach repair is
// the one trigger, and the caller drives crash-fencing §6's flow with the row's
// identity, the operation-store evidence, a durable attempt epoch, and the
// remote seam — or refuses typed. All seams are scripted: no ssh, no host.

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// bootstrapContactFixture boots a manager over a fresh hub.toml carrying one
// host, plus the real operation store and controller boot id the first-contact
// caller binds its attempt to.
type bootstrapContactFixture struct {
	t    *testing.T
	path string
	m    *hubHostManager
	ops  *hostops.Store
}

func newBootstrapContactFixture(t *testing.T) *bootstrapContactFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(path, []hostreg.Host{{Name: "alpha", SSH: "alpha.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	ops, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{
		RemoteHostOpsStore: ops,
		HubBootID:          "boot-1",
	}, path, hosts, nil)
	return &bootstrapContactFixture{t: t, path: path, m: m, ops: ops}
}

// hostRow returns the fixture host's live registry row (what the attach ladder
// hands the caller).
func (f *bootstrapContactFixture) hostRow() hostreg.Host {
	f.t.Helper()
	entry, ok := f.m.cfg.hosts.Get("alpha")
	if !ok {
		f.t.Fatal("alpha is not live")
	}
	return entry
}

// scriptBootstrapSeams wires a recording runner and a winning claim primitive.
func (f *bootstrapContactFixture) scriptBootstrapSeams(runner hostfence.Runner, quiesce hostfence.ClaimQuiesce) {
	f.m.cfg.bootstrapRunner = func(string) hostfence.Runner { return runner }
	if quiesce != nil {
		f.m.cfg.bootstrapQuiesce = func(hostreg.Host) hostfence.ClaimQuiesce { return quiesce }
	}
}

// TestBootstrapFirstContactDeliversThroughTheProductionCaller drives the full
// §6:131-137 sequence through the production caller: the attempt fence lands
// with the caller's minted epoch, the one exempt delivery step ships the
// embedded helper bytes over the wired runner, the self-test verifies the
// delivered helper, and the finalizing write converges helperInstalled.
func TestBootstrapFirstContactDeliversThroughTheProductionCaller(t *testing.T) {
	f := newBootstrapContactFixture(t)
	runner := &hubRunner{}
	quiesce := bareQuiesce()
	f.scriptBootstrapSeams(runner, quiesce)

	row := f.hostRow()
	if err := f.m.BootstrapFirstContact(context.Background(), row); err != nil {
		t.Fatalf("BootstrapFirstContact: %v", err)
	}

	// The converged provisioning record: attempt fence, installed flag, version.
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || !record.HelperInstalled || record.HelperVersion != hostfence.HelperVersion {
		t.Fatalf("host_records[alpha] = %+v, want the converged provisioning record", record)
	}
	// The attempt's epoch is the durable per-host epoch the caller minted, bound
	// to the row's registration; the fence names it for §6:139's recovery, and
	// the host-side claim received it.
	epochRow, ok := f.ops.ProbeEpoch("alpha")
	if !ok {
		t.Fatal("no durable epoch was persisted for the first-contact attempt")
	}
	if epochRow.Generation != row.Generation || epochRow.IncarnationID != row.IncarnationID {
		t.Fatalf("the attempt epoch is bound to %d/%q, want the row's %d/%q",
			epochRow.Generation, epochRow.IncarnationID, row.Generation, row.IncarnationID)
	}
	if quiesce.epoch.BootID != epochRow.BootID || quiesce.epoch.OpSeq != epochRow.OpSeq {
		t.Fatalf("the claim primitive received epoch %+v, want the persisted epoch %+v", quiesce.epoch, epochRow)
	}
	if record.BootstrapEpochBoot != epochRow.BootID || record.BootstrapEpochOpSeq != epochRow.OpSeq {
		t.Fatalf("the fence's epoch = (%q, %d), want the persisted epoch (%q, %d)",
			record.BootstrapEpochBoot, record.BootstrapEpochOpSeq, epochRow.BootID, epochRow.OpSeq)
	}

	// The delivery shipped the embedded helper bytes (§6:131), and the self-test
	// round trip ran through the same runner (§6:139's pre-fence verification).
	if len(runner.calls) != 2 {
		t.Fatalf("remote calls = %v, want the delivery then the self-test", runner.calls)
	}
	embedded := base64.StdEncoding.EncodeToString(hostfence.HelperScript())
	if !strings.Contains(runner.calls[0], embedded) {
		t.Fatal("the delivery command does not carry the embedded helper bytes")
	}
	if !strings.HasSuffix(runner.calls[1], " version") {
		t.Fatalf("second remote call = %q, want the helper's version self-test", runner.calls[1])
	}
}

// TestBootstrapFirstContactRefusesWithoutAClaimPrimitive pins the production
// posture: no pre-existing trusted host-side claim-plus-quiesce primitive
// exists yet, so delivery stays unavailable and refuses fail-closed with the
// typed conflict-class `fencing-helper-absent` — never `probe-failed`, never an
// unfenced push — after the attempt fence has already landed (§6:133/:135/:161).
func TestBootstrapFirstContactRefusesWithoutAClaimPrimitive(t *testing.T) {
	f := newBootstrapContactFixture(t)
	runner := &hubRunner{}
	f.scriptBootstrapSeams(runner, nil)

	err := f.m.BootstrapFirstContact(context.Background(), f.hostRow())
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %#v, want a typed wire refusal", err)
	}
	data, ok := wire.Data.(appwire.FencingHelperErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperAbsent || data.Host != "alpha" {
		t.Fatalf("refusal data = %#v, want the fencing-helper-absent form naming alpha", wire.Data)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("refusal code = %d, want the conflict class", wire.Code)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none without a claim primitive", runner.calls)
	}
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || record.HelperInstalled {
		t.Fatalf("host_records[alpha] = %+v, want attempt-fenced without helperInstalled", record)
	}
}

// TestBootstrapFirstContactSkipsAProvisionedHost pins the common case after the
// first attach: a converged record returns immediately, with no attempt fence,
// no epoch mint, and no remote step.
func TestBootstrapFirstContactSkipsAProvisionedHost(t *testing.T) {
	f := newBootstrapContactFixture(t)
	identity := hubBootstrapIdentity(t, f.m, "alpha")
	if _, won, err := f.m.bootstrapStore().PersistAttemptFence("alpha", identity, bootstrapEpoch()); err != nil || !won {
		t.Fatalf("PersistAttemptFence = (%v, %v), want a won write", won, err)
	}
	if _, err := f.m.bootstrapStore().FinalizeBootstrap("alpha", identity, hostfence.HelperVersion); err != nil {
		t.Fatalf("FinalizeBootstrap: %v", err)
	}
	runner := &hubRunner{}
	quiesce := bareQuiesce()
	f.scriptBootstrapSeams(runner, quiesce)

	if err := f.m.BootstrapFirstContact(context.Background(), f.hostRow()); err != nil {
		t.Fatalf("BootstrapFirstContact: %v", err)
	}
	if len(runner.calls) != 0 || quiesce.calls != 0 {
		t.Fatalf("a provisioned host was touched: runner=%v quiesce=%d", runner.calls, quiesce.calls)
	}
	if _, ok := f.ops.ProbeEpoch("alpha"); ok {
		t.Fatal("a provisioned host minted an attempt epoch")
	}
}

// TestBootstrapFirstContactRefusesAStaleRow pins the identity binding end to
// end through the caller: a row the ladder resolved before a remove and re-add
// refuses as the typed `stale-entry` class without minting an epoch, claiming,
// probing, or delivering.
func TestBootstrapFirstContactRefusesAStaleRow(t *testing.T) {
	f := newBootstrapContactFixture(t)
	staleRow := f.hostRow()

	known := f.m.cfg.store.snapshot()
	removal := make([]hostreg.Host, 0, len(known))
	for _, entry := range known {
		if entry.Name != "alpha" {
			removal = append(removal, entry)
		}
	}
	f.m.cfg.mu.Lock()
	err := f.m.persistHosts(removal, known, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("removal write: %v", err)
	}
	fresh := known[0]
	fresh.Generation++
	fresh.IncarnationID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	fresh.PresenceEpoch++
	f.m.cfg.store.set([]hostreg.Host{fresh})
	f.m.cfg.mu.Lock()
	err = f.m.persistHosts([]hostreg.Host{fresh}, removal, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("re-add write: %v", err)
	}

	runner := &hubRunner{}
	quiesce := bareQuiesce()
	f.scriptBootstrapSeams(runner, quiesce)

	err = f.m.BootstrapFirstContact(context.Background(), staleRow)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("err = %#v, want a typed wire refusal", err)
	}
	data, ok := wire.Data.(appwire.StaleEntryErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorStaleEntry || data.Binding != appwire.StaleEntryBindingGeneration {
		t.Fatalf("refusal data = %#v, want the stale-entry class on its generation binding", wire.Data)
	}
	if len(runner.calls) != 0 || quiesce.calls != 0 {
		t.Fatalf("a stale row touched the current host: runner=%v quiesce=%d", runner.calls, quiesce.calls)
	}
	if _, ok := f.ops.ProbeEpoch("alpha"); ok {
		t.Fatal("a stale attempt minted an epoch")
	}
}

// TestBootstrapEvidenceReadsTheOperationStore pins §6:131's non-record evidence
// items against the real store: a prior fenced epoch and an interrupted record
// each close the exemption, and neither a fresh store nor an unrelated host's
// records does.
func TestBootstrapEvidenceReadsTheOperationStore(t *testing.T) {
	f := newBootstrapContactFixture(t)
	if got := (f.m.bootstrapEvidence("alpha")); got != (hostfence.BootstrapEvidence{}) {
		t.Fatalf("evidence on a fresh store = %+v, want the zero value", got)
	}

	// An unrelated host's fenced epoch must not close alpha's exemption.
	other, err := f.ops.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: "client-other", Host: "beta", Kind: hostops.KindDeploy,
		Pair:   hostops.OperationPair{Generation: 1, IncarnationID: "inc-beta"},
		BootID: "boot-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation(beta): %v", err)
	}
	if got := f.m.bootstrapEvidence("alpha"); got != (hostfence.BootstrapEvidence{}) {
		t.Fatalf("evidence = %+v, want the unrelated host to be ignored", got)
	}
	_ = other

	// A deploy record that persisted a fencing epoch is §6:131's prior fenced
	// epoch.
	fenced, err := f.ops.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: "client-alpha", Host: "alpha", Kind: hostops.KindDeploy,
		Pair:   hostops.OperationPair{Generation: 1, IncarnationID: "inc-alpha"},
		BootID: "boot-1",
	})
	if err != nil {
		t.Fatalf("CreateOperation(alpha): %v", err)
	}
	if got := f.m.bootstrapEvidence("alpha"); !got.FencedEpoch || got.Interrupted {
		t.Fatalf("evidence = %+v, want a prior fenced epoch", got)
	}

	// An interrupted record from a crashed incarnation closes it too.
	if _, err := f.ops.TransitionToState(fenced.Record.ID, hostops.StateInterrupted,
		&hostops.Result{OK: false, Message: hostops.InterruptedNote}, hostops.InterruptedNote); err != nil {
		t.Fatalf("TransitionToState(interrupted): %v", err)
	}
	if got := f.m.bootstrapEvidence("alpha"); !got.FencedEpoch || !got.Interrupted {
		t.Fatalf("evidence = %+v, want the interrupted record", got)
	}
}

// TestBootstrapFirstContactRefusesTheExemptionOnPriorWork pins the caller's
// evidence wiring end to end: a host carrying an interrupted record from a
// crashed incarnation takes the fenced path (the flow's decision), so nothing
// is delivered and no attempt fence opens a second exempt window — while an
// ordinary provisioned host still attaches.
func TestBootstrapFirstContactRefusesTheExemptionOnPriorWork(t *testing.T) {
	f := newBootstrapContactFixture(t)
	record, err := f.ops.Create(hostops.NewRecord{
		ClientOperationID: "client-alpha", Host: "alpha", Kind: hostops.KindDeploy,
		Generation: 1, IncarnationID: "inc-alpha",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.ops.TransitionToState(record.ID, hostops.StateInterrupted,
		&hostops.Result{OK: false, Message: hostops.InterruptedNote}, hostops.InterruptedNote); err != nil {
		t.Fatalf("TransitionToState(interrupted): %v", err)
	}
	runner := &hubRunner{}
	quiesce := bareQuiesce()
	f.scriptBootstrapSeams(runner, quiesce)

	if err := f.m.BootstrapFirstContact(context.Background(), f.hostRow()); err != nil {
		t.Fatalf("BootstrapFirstContact: %v", err)
	}
	if len(runner.calls) != 0 || quiesce.calls != 0 {
		t.Fatalf("an ineligible host was touched: runner=%v quiesce=%d", runner.calls, quiesce.calls)
	}
	file := liveRecord(t, f.path, "alpha")
	if file.BootstrapAttempted || file.HelperInstalled {
		t.Fatalf("host_records[alpha] = %+v, want no attempt fence for an ineligible host", file)
	}
}
