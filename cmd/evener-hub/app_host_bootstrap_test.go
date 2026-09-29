package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
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

// hubClaim is a held claim that records its release.
type hubClaim struct{ released int }

func (c *hubClaim) Release(context.Context) error {
	c.released++
	return nil
}

// hubClaimQuiesce is a scripted claim-plus-quiesce primitive.
type hubClaimQuiesce struct {
	report hostfence.QuiesceReport
	err    error
	calls  int
	claim  *hubClaim
	epoch  hostfence.Epoch
}

func (q *hubClaimQuiesce) ClaimAndQuiesce(_ context.Context, epoch hostfence.Epoch) (hostfence.QuiesceReport, hostfence.BootstrapClaim, error) {
	q.calls++
	q.epoch = epoch
	if q.err != nil {
		return hostfence.QuiesceReport{}, nil, q.err
	}
	if q.claim == nil {
		q.claim = &hubClaim{}
	}
	return q.report, q.claim, nil
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
		out := r.versionOut
		if out == "" {
			out = strconv.Itoa(hostfence.HelperVersion) + "\n"
		}
		return out, "", 0, nil
	}
	return "", "", 0, nil
}

// hubProbe is a scripted recovery re-probe.
type hubProbe struct {
	live  bool
	calls int
	seen  hostfence.Epoch
}

func (p *hubProbe) BootstrappedProcessLive(_ context.Context, epoch hostfence.Epoch) (bool, error) {
	p.calls++
	p.seen = epoch
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

	fenced, won, err := store.PersistAttemptFence("alpha", bootstrapEpoch())
	if err != nil {
		t.Fatalf("PersistAttemptFence: %v", err)
	}
	if !won {
		t.Fatal("the first fence write did not win the conditional")
	}
	if fenced.AttemptToken == "" {
		t.Fatal("the fence write minted no ownership token")
	}
	if !fenced.AttemptFenced || fenced.HelperInstalled {
		t.Fatalf("after the attempt fence = %+v, want attempt-fenced without helperInstalled", fenced)
	}
	record := liveRecord(t, f.path, "alpha")
	if !record.BootstrapAttempted || record.HelperInstalled || record.HelperVersion != 0 {
		t.Fatalf("host_records[alpha] = %+v, want only the attempt fence", record)
	}
	if record.BootstrapEpochBoot != bootstrapEpoch().BootID || record.BootstrapEpochOpSeq != bootstrapEpoch().OpSeq {
		t.Fatalf("host_records[alpha] epoch = (%q, %d), want the attempt's epoch (%q, %d)",
			record.BootstrapEpochBoot, record.BootstrapEpochOpSeq, bootstrapEpoch().BootID, bootstrapEpoch().OpSeq)
	}
	raw := string(readHostFileBytes(t, f.path))
	if !strings.Contains(raw, "bootstrap_attempted = true") {
		t.Fatalf("the attempt fence is not durable in hub.toml:\n%s", raw)
	}
	if !strings.Contains(raw, `bootstrap_epoch_boot = "boot-1"`) || !strings.Contains(raw, "bootstrap_epoch_op_seq = 1") {
		t.Fatalf("the attempt's fencing epoch is not durable in hub.toml:\n%s", raw)
	}
	if record.BootstrapAttemptToken == "" || !strings.Contains(raw, "bootstrap_attempt_token = ") {
		t.Fatalf("the attempt's ownership token is not durable in hub.toml:\n%s", raw)
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
	if !strings.Contains(raw, "helper_installed = true") || !strings.Contains(raw, fmt.Sprintf("helper_version = %d", hostfence.HelperVersion)) {
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
	runner := &hubRunner{}
	quiesce := &hubClaimQuiesce{err: errors.New("the controller crashed")}
	_, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: f.m.bootstrapStore(),
		Runner: runner, Quiesce: quiesce,
	})
	var gate *hostfence.HelperGateError
	if !errors.As(err, &gate) || gate.Discriminator != hostfence.DiscriminatorHelperAbsent {
		t.Fatalf("err = %v, want a typed %s refusal", err, hostfence.DiscriminatorHelperAbsent)
	}
	if quiesce.epoch != bootstrapEpoch() {
		t.Fatalf("the claim primitive received epoch %+v, want the request epoch %+v", quiesce.epoch, bootstrapEpoch())
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
	retryRunner := &hubRunner{}
	outcome, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: f.m.bootstrapStore(),
		Runner: retryRunner, Quiesce: retryQuiesce, Probe: probe,
	})
	if err != nil || outcome.Kind != hostfence.BootstrapFenced {
		t.Fatalf("recovery = (%v, %v), want the fenced path", outcome.Kind, err)
	}
	if probe.calls != 1 || retryQuiesce.calls != 1 || len(retryRunner.calls) != 0 {
		t.Fatalf("recovery claims once and never delivers: probe=%d quiesce=%d runner=%v", probe.calls, retryQuiesce.calls, retryRunner.calls)
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
	runner := &hubRunner{}
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
		Runner: &hubRunner{}, Quiesce: bareQuiesce(), Probe: &hubProbe{live: true},
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
	first := &hubRunner{}
	outcome, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: bootstrapEpoch(), Store: store,
		Runner: first, Quiesce: bareQuiesce(),
	})
	if err != nil || outcome.Kind != hostfence.BootstrapDelivered {
		t.Fatalf("first Bootstrap = (%v, %v), want delivered", outcome.Kind, err)
	}
	retryRunner := &hubRunner{}
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
		{name: "attempt fence with its epoch", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, BootstrapEpochBoot: "boot-1", BootstrapEpochOpSeq: 1}},
		{name: "version without installed", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, HelperVersion: 1}, wantErr: "without helper_installed"},
		{name: "installed without version", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, HelperInstalled: true}, wantErr: "without a helper_version record"},
		{name: "installed without the attempt fence", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, HelperInstalled: true, HelperVersion: 1}, wantErr: "without the bootstrap-attempt fence"},
		{name: "fence without its epoch", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true}, wantErr: "without the attempt's fencing epoch"},
		{name: "epoch without the fence", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapEpochBoot: "boot-1", BootstrapEpochOpSeq: 1}, wantErr: "without the attempt fence"},
		{name: "invalid epoch", record: HostRecord{IncarnationID: identity.IncarnationID, PresenceEpoch: 1, BootstrapAttempted: true, BootstrapEpochBoot: "-", BootstrapEpochOpSeq: 1}, wantErr: "invalid bootstrap-attempt epoch"},
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
	data, ok := wire.Data.(appwire.FencingHelperErrorData)
	if !ok {
		t.Fatalf("absent gate data = %T, want FencingHelperErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorFencingHelperAbsent {
		t.Fatalf("absent discriminator = %q, want %q", data.EvenerErrorInfo, appwire.ErrorFencingHelperAbsent)
	}
	if data.Host != "alpha" || data.Version != strconv.Itoa(hostfence.HelperVersion) {
		t.Fatalf("absent data = %+v, want the host and the pinned version string", data)
	}

	// The wire message carries the refusal's own detail — helperAbsentRefusal
	// wraps the gate with the reason (a lost claim race, a live foreign
	// process) — not just the gate's generic text.
	wrapped := fmt.Errorf("%w (a lost claim race)", &hostfence.HelperGateError{
		Host: "alpha", Discriminator: hostfence.DiscriminatorHelperAbsent, PinnedVersion: hostfence.HelperVersion,
	})
	err = m.operationProbeRefusal("alpha", wrapped)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok || !strings.Contains(wire.Message, "a lost claim race") {
		t.Fatalf("absent wire message = %q, want the wrapped refusal detail", wire.Message)
	}

	untrusted := hostfence.VerifyHelper("alpha", hostfence.HelperVersion, hostfence.HelperProbe{Present: true, Reported: true, Version: 99})
	err = m.operationProbeRefusal("alpha", untrusted)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("untrusted gate refusal classified as %T (%v), want an appwire.WireError", err, err)
	}
	data, ok = wire.Data.(appwire.FencingHelperErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorFencingHelperUntrusted {
		t.Fatalf("untrusted data = %#v, want the %s arm", wire.Data, appwire.ErrorFencingHelperUntrusted)
	}
	// An unknown discriminator is refused as an internal error naming the class,
	// never misclassified as absent (and never as probe-failed).
	unknown := &hostfence.HelperGateError{Host: "alpha", Discriminator: "fencing-helper-future", PinnedVersion: hostfence.HelperVersion}
	err = m.operationProbeRefusal("alpha", unknown)
	wire, ok = errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeInternalError {
		t.Fatalf("unknown discriminator = (%v, %v), want an internal-class refusal", wire, err)
	}
	if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorInternal {
		t.Fatalf("unknown discriminator data = %#v, want internal", wire.Data)
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

// TestHostBootstrapProvisioningDoesNotSurviveRemoval pins the derivation's
// purge: a removed name's bootstrap flags leave the store with its file record,
// so an in-process re-add never inherits a stale attempt fence or a converged
// helperInstalled flag — an inherited flag would read as provisioned forever,
// making the outcome depend on process lifetime rather than persisted state.
func TestHostBootstrapProvisioningDoesNotSurviveRemoval(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	if _, _, err := store.PersistAttemptFence("alpha", bootstrapEpoch()); err != nil {
		t.Fatalf("PersistAttemptFence: %v", err)
	}
	if _, err := store.FinalizeBootstrap("alpha", hostfence.HelperVersion); err != nil {
		t.Fatalf("FinalizeBootstrap: %v", err)
	}
	if p := f.m.cfg.store.provisioningFor("alpha"); !p.Provisioned() {
		t.Fatalf("provisioning after the finalize = %+v, want the converged record", p)
	}

	// The removal write: the live set no longer carries the name.
	entries := f.m.cfg.store.snapshot()
	removal := make([]hostreg.Host, 0, len(entries))
	for _, entry := range entries {
		if entry.Name != "alpha" {
			removal = append(removal, entry)
		}
	}
	f.m.cfg.mu.Lock()
	err := f.m.persistHosts(removal, entries, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("removal write: %v", err)
	}
	records, _ := readHostRecords(t, f.path)
	if _, carried := records["alpha"]; carried {
		t.Fatal("the removal left a host_records entry for the removed name")
	}
	if p := f.m.cfg.store.provisioningFor("alpha"); p != (hostfence.Provisioning{}) {
		t.Fatalf("the store kept %+v after the removal; an in-process re-add would inherit it", p)
	}

	// The re-add: a fresh incarnation for the same name mints no flags.
	fresh := entries[0]
	fresh.Generation = entries[0].Generation + 1
	fresh.IncarnationID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	fresh.PresenceEpoch = entries[0].PresenceEpoch + 1
	f.m.cfg.mu.Lock()
	err = f.m.persistHosts([]hostreg.Host{fresh}, removal, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("re-add write: %v", err)
	}
	record := liveRecord(t, f.path, "alpha")
	if record.BootstrapAttempted || record.HelperInstalled || record.HelperVersion != 0 {
		t.Fatalf("the re-added host inherited bootstrap flags: %+v", record)
	}
	if p := f.m.cfg.store.provisioningFor("alpha"); p != (hostfence.Provisioning{}) {
		t.Fatalf("the re-added host's store record = %+v, want the zero record", p)
	}
}

// TestHostBootstrapFinalizeRequiresTheAttemptFence pins the fence-before-install
// invariant at the real store: a direct finalize on a never-fenced host refuses
// instead of writing installed-without-fence, a shape the loader rejects.
func TestHostBootstrapFinalizeRequiresTheAttemptFence(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	if _, err := store.FinalizeBootstrap("alpha", hostfence.HelperVersion); err == nil {
		t.Fatal("FinalizeBootstrap on a never-fenced host = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), "has no bootstrap-attempt fence") {
		t.Fatalf("FinalizeBootstrap error = %v, want the explicit fence-before-install refusal", err)
	}
	if record := liveRecord(t, f.path, "alpha"); record.HelperInstalled || record.BootstrapAttempted {
		t.Fatalf("the refused finalize wrote flags: %+v", record)
	}
	if _, err := store.FinalizeBootstrap("alpha", 0); err == nil {
		t.Fatal("FinalizeBootstrap with version 0 = nil error, want a refusal")
	} else if !strings.Contains(err.Error(), "needs the delivered helper version") {
		t.Fatalf("FinalizeBootstrap(version 0) error = %v, want the explicit version refusal", err)
	}
}

// TestHostBootstrapPersistsTheAttemptEpoch pins §6:139's identity at the record
// layer: the fence's own atomic write carries the attempt's fencing epoch, a
// read-back returns it (so recovery can name the crashed attempt after a
// restart), and an unrelated rewrite preserves it with the flags.
func TestHostBootstrapPersistsTheAttemptEpoch(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	epoch := hostfence.Epoch{BootID: "boot-9", OpSeq: 42}
	if _, _, err := store.PersistAttemptFence("alpha", epoch); err != nil {
		t.Fatalf("PersistAttemptFence: %v", err)
	}
	record := liveRecord(t, f.path, "alpha")
	if record.BootstrapEpochBoot != epoch.BootID || record.BootstrapEpochOpSeq != epoch.OpSeq {
		t.Fatalf("host_records[alpha] epoch = (%q, %d), want (%q, %d)",
			record.BootstrapEpochBoot, record.BootstrapEpochOpSeq, epoch.BootID, epoch.OpSeq)
	}
	readBack, err := store.Provisioning("alpha")
	if err != nil {
		t.Fatalf("Provisioning: %v", err)
	}
	if readBack.AttemptEpoch != epoch {
		t.Fatalf("read-back epoch = %+v, want %+v", readBack.AttemptEpoch, epoch)
	}

	entries := f.m.cfg.store.snapshot()
	f.m.cfg.mu.Lock()
	err = f.m.persistHosts(entries, entries, hostPersistChange{})
	f.m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("unrelated rewrite: %v", err)
	}
	record = liveRecord(t, f.path, "alpha")
	if record.BootstrapEpochBoot != epoch.BootID || record.BootstrapEpochOpSeq != epoch.OpSeq {
		t.Fatalf("the epoch after an unrelated rewrite = (%q, %d), want (%q, %d)",
			record.BootstrapEpochBoot, record.BootstrapEpochOpSeq, epoch.BootID, epoch.OpSeq)
	}
}

// TestHostBootstrapFenceWriteIsConditional pins the compare-and-set at the real
// record: a second attempt's fence write returns the first owner's record
// unchanged and leaves hub.toml byte-identical, so two concurrent first-contacts
// cannot both deliver and the fence owner's epoch survives.
func TestHostBootstrapFenceWriteIsConditional(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	owner := hostfence.Epoch{BootID: "boot-a", OpSeq: 5}
	first, won, err := store.PersistAttemptFence("alpha", owner)
	if err != nil {
		t.Fatalf("first PersistAttemptFence: %v", err)
	}
	if !won {
		t.Fatal("the first fence write did not win the conditional")
	}
	if first.AttemptToken == "" {
		t.Fatal("the fence write minted no ownership token")
	}
	before := readHostFileBytes(t, f.path)

	delayed, delayedWon, err := store.PersistAttemptFence("alpha", hostfence.Epoch{BootID: "boot-b", OpSeq: 9})
	if err != nil {
		t.Fatalf("second PersistAttemptFence: %v", err)
	}
	if delayedWon {
		t.Fatal("the delayed fence write won the conditional, want the first owner to keep it")
	}
	if delayed.AttemptEpoch != owner || delayed.AttemptToken != first.AttemptToken {
		t.Fatalf("the delayed fence write returned %+v, want the first owner's epoch %+v and token %q",
			delayed, owner, first.AttemptToken)
	}
	if after := readHostFileBytes(t, f.path); !bytes.Equal(before, after) {
		t.Fatalf("the delayed fence write rewrote hub.toml on a lost race:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	record := liveRecord(t, f.path, "alpha")
	if record.BootstrapEpochBoot != owner.BootID || record.BootstrapEpochOpSeq != owner.OpSeq {
		t.Fatalf("host_records[alpha] epoch = (%q, %d), want the owner's (%q, %d)",
			record.BootstrapEpochBoot, record.BootstrapEpochOpSeq, owner.BootID, owner.OpSeq)
	}
}

// TestHubBootstrapRecoveryProbesThePersistedEpoch pins the epoch identity at the
// hub seam: after a crash and a restart with a different request epoch, recovery
// probes the fence's persisted crashed-attempt epoch.
func TestHubBootstrapRecoveryProbesThePersistedEpoch(t *testing.T) {
	f := newBootstrapFixture(t)
	crashed := hostfence.Epoch{BootID: "boot-old", OpSeq: 7}
	if _, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: crashed, Store: f.m.bootstrapStore(),
		Runner: &hubRunner{}, Quiesce: &hubClaimQuiesce{err: errors.New("the controller crashed")},
	}); err == nil {
		t.Fatal("Bootstrap = nil error, want the crashed claim")
	}
	probe := &hubProbe{}
	if _, err := hostfence.Bootstrap(context.Background(), hostfence.BootstrapRequest{
		Host: "alpha", Epoch: hostfence.Epoch{BootID: "boot-new", OpSeq: 3},
		Store: f.m.bootstrapStore(), Runner: &hubRunner{}, Quiesce: bareQuiesce(), Probe: probe,
	}); err != nil {
		t.Fatalf("recovery = %v", err)
	}
	if probe.seen != crashed {
		t.Fatalf("recovery probed %+v, want the persisted crashed attempt %+v", probe.seen, crashed)
	}
}

// TestOrphanAttemptRefusalIsTransientBusy pins §8:158's class for a crashed
// bootstrap attempt whose process is still live: the transient busy refusal,
// never probe-failed, with the diagnostic naming the crashed epoch.
func TestOrphanAttemptRefusalIsTransientBusy(t *testing.T) {
	m := testHostManager(nil, nil)
	orphan := &hostfence.AttemptOrphanError{
		Host: "alpha", Epoch: hostfence.Epoch{BootID: "boot-1", OpSeq: 7}, Detail: "live",
	}
	err := m.operationProbeRefusal("alpha", orphan)
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok || wire.Code != appwire.CodeConflict {
		t.Fatalf("orphan refusal = (%v, %v), want a conflict-class wire error", wire, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorHostBusyTransient {
		t.Fatalf("orphan refusal data = %#v, want the %s arm", wire.Data, appwire.ErrorHostBusyTransient)
	}
	if !strings.Contains(wire.Message, "boot-1/7") {
		t.Fatalf("orphan refusal message = %q, want the crashed epoch named", wire.Message)
	}
}

// TestHostBootstrapTokenLifecycle pins the durable ownership token at the record
// layer: the fence write mints it, revalidation accepts it while the attempt
// stands, invalidation retires it atomically, and after helperInstalled converges
// invalidation writes nothing.
func TestHostBootstrapTokenLifecycle(t *testing.T) {
	f := newBootstrapFixture(t)
	store := f.m.bootstrapStore()
	epoch := hostfence.Epoch{BootID: "boot-3", OpSeq: 11}
	fenced, won, err := store.PersistAttemptFence("alpha", epoch)
	if err != nil || !won {
		t.Fatalf("PersistAttemptFence = (%+v, %v, %v), want a won write", fenced, won, err)
	}
	if fenced.AttemptToken == "" {
		t.Fatal("the fence write minted no token")
	}
	if _, ok, err := store.RevalidateAttemptFence("alpha", epoch, fenced.AttemptToken); err != nil || !ok {
		t.Fatalf("RevalidateAttemptFence = (%v, %v), want the token to stand", ok, err)
	}
	if _, ok, _ := store.RevalidateAttemptFence("alpha", epoch, "another-token"); ok {
		t.Fatal("a foreign token revalidated")
	}
	retired, err := store.InvalidateAttemptFence("alpha", epoch, fenced.AttemptToken)
	if err != nil {
		t.Fatalf("InvalidateAttemptFence: %v", err)
	}
	if retired.AttemptToken != "" || !retired.AttemptFenced {
		t.Fatalf("after invalidation = %+v, want the fence kept with no token", retired)
	}
	if _, ok, _ := store.RevalidateAttemptFence("alpha", epoch, fenced.AttemptToken); ok {
		t.Fatal("the retired token still revalidates")
	}
	if record := liveRecord(t, f.path, "alpha"); record.BootstrapAttemptToken != "" || !record.BootstrapAttempted {
		t.Fatalf("host_records[alpha] = %+v, want the fence kept and the token gone", record)
	}

	// Once helperInstalled converges, invalidation writes nothing.
	if _, err := store.FinalizeBootstrap("alpha", hostfence.HelperVersion); err != nil {
		t.Fatalf("FinalizeBootstrap: %v", err)
	}
	before := readHostFileBytes(t, f.path)
	after, err := store.InvalidateAttemptFence("alpha", epoch, fenced.AttemptToken)
	if err != nil {
		t.Fatalf("InvalidateAttemptFence after finalize: %v", err)
	}
	if !after.HelperInstalled {
		t.Fatalf("after finalize invalidation = %+v, want the converged record", after)
	}
	if bytes.Equal(before, readHostFileBytes(t, f.path)) == false {
		t.Fatal("the post-finalize invalidation rewrote hub.toml")
	}
}

// TestHostBootstrapProvisioningSurvivesACompensatedRemoval pins the round-7
// medium: a removal's staged write prunes the live host's bootstrap flags from
// the store and the file, but the pre-commit compensation rolls the entry back
// with the flags restored (the plan captured them before the staged write), so a
// failed remove never leaves a live host reading as never-provisioned
// (§6:131's exactly-once exemption). It drives the staged-write shape and the
// real compensationChange through the record machinery, so it fails without the
// provisioning carry.
func TestHostBootstrapProvisioningSurvivesACompensatedRemoval(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := writeHubTOMLHosts(configPath, []hostreg.Host{{Name: "side", SSH: "s.example"}}); err != nil {
		t.Fatalf("seed hub.toml: %v", err)
	}
	m := bootHostManager(t, configPath)
	store := m.bootstrapStore()
	if _, _, err := store.PersistAttemptFence("side", bootstrapEpoch()); err != nil {
		t.Fatalf("PersistAttemptFence: %v", err)
	}
	if _, err := store.FinalizeBootstrap("side", hostfence.HelperVersion); err != nil {
		t.Fatalf("FinalizeBootstrap: %v", err)
	}

	known := m.cfg.store.snapshot()
	removal := make([]hostreg.Host, 0, len(known))
	for _, entry := range known {
		if entry.Name != "side" {
			removal = append(removal, entry)
		}
	}
	plan := &hostCommitPlan{Kind: hostMutationRemove, Name: "side", Key: "k", Entries: removal, Known: known}
	// What stageCommit captures before its write.
	plan.PriorProvisioning = m.cfg.store.provisioningFor(plan.Name)

	// The staging write: the removal's post-mutation live set. It prunes the flags.
	m.cfg.mu.Lock()
	err := m.persistHosts(removal, known, hostPersistChange{})
	m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("staged write: %v", err)
	}
	if p := m.cfg.store.provisioningFor("side"); p != (hostfence.Provisioning{}) {
		t.Fatalf("the staged write left %+v; the prune is what makes the compensation's restore the point", p)
	}

	// The pre-commit compensation: the rollback re-adds the pre-mutation entry.
	m.cfg.mu.Lock()
	err = m.persistHosts(known, known, compensationChange(plan))
	m.cfg.mu.Unlock()
	if err != nil {
		t.Fatalf("compensation write: %v", err)
	}

	// The live host kept its flags in the store and in the file.
	if p := m.cfg.store.provisioningFor("side"); !p.AttemptFenced || !p.HelperInstalled || p.HelperVersion != hostfence.HelperVersion {
		t.Fatalf("store provisioning after the compensation = %+v, want the flags restored", p)
	}
	record := liveRecord(t, configPath, "side")
	if !record.BootstrapAttempted || !record.HelperInstalled || record.HelperVersion != hostfence.HelperVersion {
		t.Fatalf("host_records[side] after the compensation = %+v, want the flags restored", record)
	}

	// A reopen reads the same flags back.
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig after the compensation: %v", err)
	}
	hosts, err := hostreg.New(hostRegistryEntries(cfg))
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	reopened := newHubHostManager(appsource.NewRegistry(), nil, hubcore.WebConfig{}, configPath, hosts, func(string, ...any) {})
	if p := reopened.cfg.store.provisioningFor("side"); !p.AttemptFenced || !p.HelperInstalled {
		t.Fatalf("reopened store provisioning = %+v, want the flags restored", p)
	}
}
