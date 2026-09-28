package hub

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// The bootstrap tests drive crash-fencing §6's first-contact flow through the
// real hub.toml record machinery: a hub booted over a temporary config, the
// manager's own bootstrap record store, and scripted remote seams. The two
// crash windows §6:133-137 names are fault-injected by making the seam fail
// exactly where the crash would land, and the file's bytes are read back after
// every write.

// bootstrapFixture is one booted hub over a temporary hub.toml with one host.
type bootstrapFixture struct {
	t    *testing.T
	path string
	m    *hubHostManager
}

// newBootstrapFixture boots a manager over a fresh hub.toml carrying one host,
// the way the existing record tests do.
func newBootstrapFixture(t *testing.T) *bootstrapFixture {
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
	m := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, path, hosts, nil)
	return &bootstrapFixture{t: t, path: path, m: m}
}

// hubClaimQuiesce is a scripted claim-plus-quiesce primitive.
type hubClaimQuiesce struct {
	report hostfence.QuiesceReport
	err    error
	calls  int
}

func (q *hubClaimQuiesce) ClaimAndQuiesce(context.Context, hostfence.Epoch) (hostfence.QuiesceReport, error) {
	q.calls++
	return q.report, q.err
}

// hubRunner is a scripted remote runner that answers the helper version
// round trip with versionOut (the delivery command is answered with success).
type hubRunner struct {
	versionOut string
	calls      []string
}

func (r *hubRunner) Run(_ context.Context, command string) (string, string, int, error) {
	r.calls = append(r.calls, command)
	if strings.HasSuffix(command, " version") {
		return r.versionOut, "", 0, nil
	}
	return "", "", 0, nil
}

// hubProbe is a scripted recovery re-probe.
type hubProbe struct {
	live  bool
	calls int
}

func (p *hubProbe) BootstrappedProcessLive(context.Context, hostfence.Epoch) (bool, error) {
	p.calls++
	return p.live, nil
}

// bootstrapEpoch is one valid fencing epoch for these tests.
func bootstrapEpoch() hostfence.Epoch { return hostfence.Epoch{BootID: "boot-1", OpSeq: 1} }

// bareQuiesce is the winning claim with no foreign presence.
func bareQuiesce() *hubClaimQuiesce {
	return &hubClaimQuiesce{report: hostfence.QuiesceReport{Claimed: true}}
}

// failingFinalizeStore injects the second crash window: the attempt fence
// landed and the delivery ran, but the finalizing write never did.
type failingFinalizeStore struct{ hostfence.BootstrapStore }

func (failingFinalizeStore) FinalizeBootstrap(string, uint64) (hostfence.Provisioning, error) {
	return hostfence.Provisioning{}, errors.New("injected crash before the finalizing write")
}

// TestHostBootstrapFlagsPersistAndSurviveRewrites pins §6:133/:137's record
// machinery: the attempt fence lands in its own atomic write and is durable,
// the finalizing write converges helperInstalled with the version record, and
// an unrelated rewrite carries both forward instead of dropping them.
func TestHostBootstrapFlagsPersistAndSurviveRewrites(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()

	initial, err := store.Provisioning("alpha")
	if err != nil {
		t.Fatalf("Provisioning: %v", err)
	}
	if initial != (hostfence.Provisioning{}) {
		t.Fatalf("initial provisioning = %+v, want the zero record", initial)
	}

	fenced, err := store.PersistAttemptFence("alpha")
	if err != nil {
		t.Fatalf("PersistAttemptFence: %v", err)
	}
	if !fenced.AttemptFenced || fenced.HelperInstalled {
		t.Fatalf("after the attempt fence = %+v, want attempt-fenced without helperInstalled", fenced)
	}
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || record.HelperInstalled || record.HelperVersion != 0 {
		t.Fatalf("host_records[alpha] = %+v, want only the attempt fence", record)
	}
	raw := string(readHostFileBytes(t, f.path))
	if !strings.Contains(raw, "bootstrap_attempted = true") {
		t.Fatalf("the attempt fence is not durable in hub.toml:\n%s", raw)
	}
	if strings.Contains(raw, "helper_installed") {
		t.Fatalf("the attempt-fence write converged helperInstalled:\n%s", raw)
	}

	finalized, err := store.FinalizeBootstrap("alpha", hostfence.HelperVersion)
	if err != nil {
		t.Fatalf("FinalizeBootstrap: %v", err)
	}
	if !finalized.HelperInstalled || finalized.HelperVersion != hostfence.HelperVersion || !finalized.AttemptFenced {
		t.Fatalf("after the finalize = %+v, want the converged record", finalized)
	}
	raw = string(readHostFileBytes(t, f.path))
	if !strings.Contains(raw, "helper_installed = true") || !strings.Contains(raw, "helper_version = 1") {
		t.Fatalf("the finalizing write did not converge the flags:\n%s", raw)
	}

	// An unrelated rewrite (no staged records) must carry the flags through.
	entries := f.m.cfg.store.snapshot()
	f.m.cfg.mu.Lock()
	err = f.m.persistHosts(entries, entries, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("unrelated rewrite: %v", err)
	}
	record = liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || !record.HelperInstalled || record.HelperVersion != hostfence.HelperVersion {
		t.Fatalf("host_records[alpha] after an unrelated rewrite = %+v, want the flags preserved", record)
	}
}

// TestHostBootstrapCrashWindowBeforeSideEffect pins the first crash window
// through the real record machinery: the claim never answered (the process
// died after the fence write), the file carries the attempt fence, and a later
// attempt takes the fenced recovery path with no second delivery.
func TestHostBootstrapCrashWindowBeforeSideEffect(t *testing.T) {
	f := newBootstrapFixture(t)
	runner := &hubRunner{versionOut: "1\n"}
	_, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: f.m.bootstrapStore(),
		Runner: runner, Quiesce: &hubClaimQuiesce{err: errors.New("the controller crashed")},
	})
	var gate *hostfence.HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != hostfence.DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal", err, hostfence.DiscriminatorHelperAbsent)
	}
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || record.HelperInstalled {
		t.Fatalf("host_records[alpha] after the crash = %+v, want attempt-fenced without helperInstalled", record)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("remote calls = %v, want none before the crash", runner.calls)
	}

	// The retry: the attempt fence closes the exemption, recovery re-probes, and
	// the fenced path opens with the remote untouched.
	probe := &hubProbe{}
	retryQuiesce := bareQuiesce()
	retryRunner := &hubRunner{versionOut: "1\n"}
	outcome, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: f.m.bootstrapStore(),
		Runner: retryRunner, Quiesce: retryQuiesce, Probe: probe,
	})
	if err != nil || outcome.Kind != hostfence.BootstrapFenced {
		t.Fatalf("recovery = (%v, %v), want the fenced path", outcome.Kind, err)
	}
	if probe.calls != 1 || retryQuiesce.calls != 0 || len(retryRunner.calls) != 0 {
		t.Fatalf("recovery touched the remote: probe=%d quiesce=%d runner=%v", probe.calls, retryQuiesce.calls, retryRunner.calls)
	}
	if record := liveRecord(t, f.path, "alpha"); record.HelperInstalled {
		t.Fatalf("recovery converged helperInstalled: %+v", record)
	}
}

// TestHostBootstrapCrashWindowAfterDelivery pins the second crash window: the
// delivery ran but the finalizing write failed, the file stays attempt-fenced
// without helperInstalled, and recovery refuses while the crashed attempt's
// process is live.
func TestHostBootstrapCrashWindowAfterDelivery(t *testing.T) {
	f := newBootstrapFixture(t)
	runner := &hubRunner{versionOut: "1\n"}
	_, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: failingFinalizeStore{f.m.bootstrapStore()},
		Runner: runner, Quiesce: bareQuiesce(),
	})
	if err == nil {
		t.Fatal("Bootstrap = nil error, want the injected finalize failure")
	}
	if len(runner.calls) == 0 {
		t.Fatal("the delivery did not run before the injected crash")
	}
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || record.HelperInstalled {
		t.Fatalf("host_records[alpha] after the crash = %+v, want attempt-fenced without helperInstalled", record)
	}
	// Recovery with a live process refuses; the file is untouched.
	_, err = hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: f.m.bootstrapStore(),
		Runner: &hubRunner{}, Probe: &hubProbe{live: true},
	})
	if _, ok := errors.AsType[*hostfence.AttemptOrphanError](err); !ok {
		t.Fatalf("err = %v, want an AttemptOrphanError for the live crashed-attempt process", err)
	}
	if record := liveRecord(t, f.path, "alpha"); record.HelperInstalled {
		t.Fatalf("the refused recovery converged helperInstalled: %+v", record)
	}
}

// TestHostBootstrapRetryReplaysProvisioned pins §6:139's dedup replay through
// the real record: a retry after a finalized delivery observes the converged
// record and runs no second delivery.
func TestHostBootstrapRetryReplaysProvisioned(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	first := &hubRunner{versionOut: "1\n"}
	outcome, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: store,
		Runner: first, Quiesce: bareQuiesce(),
	})
	if err != nil || outcome.Kind != hostfence.BootstrapDelivered {
		t.Fatalf("first Bootstrap = (%v, %v), want delivered", outcome.Kind, err)
	}
	retryRunner := &hubRunner{versionOut: "1\n"}
	retryQuiesce := bareQuiesce()
	replay, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: store,
		Runner: retryRunner, Quiesce: retryQuiesce,
	})
	if err != nil || replay.Kind != hostfence.BootstrapProvisioned {
		t.Fatalf("retry = (%v, %v), want BootstrapProvisioned", replay.Kind, err)
	}
	if retryQuiesce.calls != 0 || len(retryRunner.calls) != 0 {
		t.Fatalf("the deduped retry delivered again: quiesce=%d runner=%v", retryQuiesce.calls, retryRunner.calls)
	}
}

// TestHostRecordProvisioningValidation pins the loud refusal of a bootstrap
// flag combination no writer of this build emits, at the decode boundary.
func TestHostRecordProvisioningValidation(t *testing.T) {
	identity := HostRecord{IncarnationID: strings.Repeat("a", 36), PresenceEpoch: 1}
	cases := []struct {
		name    string
		record  HostRecord
		wantErr string
	}{
		{name: "attempt fence alone", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true}},
		{name: "version without installed", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, HelperVersion: 1}, wantErr: "without helper_installed"},
		{name: "installed without version", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, HelperInstalled: true}, wantErr: "without a helper_version record"},
		{name: "installed without the attempt fence", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, HelperInstalled: true, HelperVersion: 1}, wantErr: "without the bootstrap-attempt fence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateHostRecords(map[string]HostRecord{"alpha": tc.record}, nil)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateHostRecords = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateHostRecords = %v, want a refusal naming %q", err, tc.wantErr)
			}
		})
	}
}

// TestHelperGateRefusalsRideTheConflictClass pins §8:161-162's classification:
// a helper-gate refusal is the typed conflict-class `fencing-helper-*`, never
// `probe-failed`, even on the probe classifier that every other read failure
// goes through.
func TestHelperGateRefusalsRideTheConflictClass(t *testing.T) {
	m := testHostManager(nil, nil)

	absent := hostfence.VerifyHelper("alpha", hostfence.HelperVersion, hostfence.HelperProbe{})
	err := m.operationProbeRefusal("alpha", absent)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("absent gate refusal classified as %T (%v), want an appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("absent gate code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	data, ok := wire.Data.(appwire.FencingHelperGateErrorData)
	if !ok {
		t.Fatalf("absent gate data = %T, want FencingHelperGateErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorFencingHelperAbsent {
		t.Fatalf("absent discriminator = %q, want %q", data.EvenerErrorInfo, appwire.ErrorFencingHelperAbsent)
	}
	if data.Host != "alpha" || data.PinnedVersion != hostfence.HelperVersion {
		t.Fatalf("absent data = %+v, want the host and pinned version", data)
	}

	untrusted := hostfence.VerifyHelper("alpha", hostfence.HelperVersion, hostfence.HelperProbe{Present: true, Reported: true, Version: 99})
	err = m.operationProbeRefusal("alpha", untrusted)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("untrusted gate refusal classified as %T (%v), want an appwire.WireError", err, err)
	}
	data, ok = wire.Data.(appwire.FencingHelperGateErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperUntrusted {
		t.Fatalf("untrusted data = %#v, want the %s arm", wire.Data, appwire.ErrorFencingHelperUntrusted)
	}
	if data.ObservedVersion != 99 {
		t.Fatalf("untrusted observed version = %d, want 99", data.ObservedVersion)
	}

	// An ordinary read failure still classifies as probe-failed.
	err = m.operationProbeRefusal("alpha", errors.New("read failed"))
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("ordinary probe failure classified as %T (%v), want an appwire.WireError", err, err)
	}
	if data, ok := wire.Data.(appwire.ProbeFailedErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorProbeFailed {
		t.Fatalf("ordinary probe failure data = %#v, want the %s arm", wire.Data, appwire.ErrorProbeFailed)
	}
}
