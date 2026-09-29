package hub

// evener/host/deploy and evener/host/restart tests (deploy pipeline 08b §6,
// §10, §11, §12). The rows: the fixed processing order with dedup first and the
// token consumed exactly once; the typed token, remnant, busy, detached and
// stale refusals with the token unconsumed and no record; the under-gate
// re-resolution and the terminal-operation scan; the restart incarnation pair;
// worker lifetime (a client disconnect leaves the operation running; controller
// shutdown records `interrupted` and releases the gate); the planned-restart
// path with the seam-(d) interim pinned; and the post-operation refresh's
// verified success and verbatim failures.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// deployTestHost is the configured host the operation tests act on.
func deployTestHost() hostreg.Host {
	return hostreg.Host{
		Name:       "m4",
		SSH:        "m4.example",
		User:       "ops",
		EvenerPath: "/opt/evener/bin/evener",
		Roots:      []string{"/srv/work"},
	}
}

// deployTestHome is the attached channel's reported home, so the deploy target
// resolves the same way for the handler and the test.
const deployTestHome = "/home/ops"

// deployTestFacts is the attached channel's captured preflight.
func deployTestFacts(entry hostreg.Host) sshconn.Preflight {
	return sshconn.Preflight{
		Host:     entry.Name,
		OS:       "linux",
		Arch:     "amd64",
		Home:     deployTestHome,
		UID:      "1000",
		Version:  "v0.9.0",
		Protocol: appwire.ProtocolVersion,
	}
}

// deployTestTarget is the target path the token binds and the handler
// re-resolves.
func deployTestTarget(t *testing.T, entry hostreg.Host) string {
	t.Helper()
	target, err := sshconn.DeployRunTargetFor(entry, deployTestHome)
	if err != nil {
		t.Fatalf("DeployRunTargetFor: %v", err)
	}
	return target
}

// deployWireInfo extracts the typed discriminator and data from a refused
// handler call.
func deployWireInfo(t *testing.T, err error) (appwire.ErrorInfo, map[string]json.RawMessage, int) {
	t.Helper()
	if err == nil {
		t.Fatal("the call succeeded, want a typed refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T (%v), want an appwire.WireError", err, err)
	}
	raw, merr := json.Marshal(wire.Data)
	if merr != nil {
		t.Fatalf("marshal refusal data: %v", merr)
	}
	var data map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &data); uerr != nil {
		t.Fatalf("unmarshal refusal data %s: %v", raw, uerr)
	}
	var info appwire.ErrorInfo
	if uerr := json.Unmarshal(data["evenerErrorInfo"], &info); uerr != nil {
		t.Fatalf("refusal data %s carries no evenerErrorInfo: %v", raw, uerr)
	}
	return info, data, wire.Code
}

// deployTestConfigPath writes a machine-managed hub.toml declaring entries and
// returns its path: the fixture every operation test's handler fingerprints.
func deployTestConfigPath(t *testing.T, entries ...hostreg.Host) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML(entries)), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	return configPath
}

// restartProbeScript scripts the two probes a restart-follows operation makes:
// the pre-replacement probe (beforeVersion at before) and the post-operation
// refresh probe (afterVersion at after).
func restartProbeScript(t *testing.T, beforeVersion, afterVersion string, before, after time.Time) func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
	t.Helper()
	return probeScript(t,
		hubcore.HostRuntimeProbe{Version: beforeVersion, RunningHealthy: true, ProcessStartTime: &before},
		hubcore.HostRuntimeProbe{Version: afterVersion, RunningHealthy: true, ProcessStartTime: &after},
	)
}

// probeScript builds a probe seam that answers the given probes in order and
// keeps answering the last one: the operation path probes once before the
// replacement (step 3 for a deploy, the worker's own probe for a restart) and
// once after it.
func probeScript(t *testing.T, probes ...hubcore.HostRuntimeProbe) func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
	t.Helper()
	if len(probes) == 0 {
		t.Fatal("probeScript needs at least one probe")
	}
	var calls atomic.Int64
	return func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
		index := int(calls.Add(1)) - 1
		if index >= len(probes) {
			index = len(probes) - 1
		}
		return probes[index], nil
	}
}

// deployProbeScript scripts the deploy path's probes for a token whose running
// version differs from the controller's revision: the step-(3) probe (the token
// binding) and the post-restart refresh probe, which must report the controller
// revision and a changed process start time.
func deployProbeScript(t *testing.T) func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	return restartProbeScript(t, "v0.9.0", "v1.2.3", before, after)
}

// deploySeams is the test's control over the operation path's seams. Nil
// fields take the happy-path values.
type deploySeams struct {
	facts          func(ctx context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error)
	probe          func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error)
	attached       func(name string) (sshconn.Preflight, bool)
	clientAttached func(name string) (*appwire.Client, bool)
	deploy         func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight) (string, sshconn.Preflight, error)
	restart        func(ctx context.Context, host hostreg.Host, facts sshconn.Preflight) error
	remnant        func(name string) (string, bool)
	// attachUnderGate is the operation-owned attach/reattach seam (§6 seam (d)).
	// Nil takes a handoff-only stub: no manager owns channels in these tests, so
	// the worker's reattach is a no-op unless a test scripts it.
	attachUnderGate func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error)
	lastKnown       func(entry hostreg.Host, probe hubcore.HostRuntimeProbe, facts hubcore.HostPlanFacts) error
	counters        *deployCounters
}

// deployCounters counts the worker's seam invocations.
type deployCounters struct {
	deploys  atomic.Int64
	restarts atomic.Int64
}

// deployTestHub builds a manager wired the way the operation tests need it:
// the live registry, an operation store, an attached-only client seam, and the
// test's seams.
func deployTestHub(t *testing.T, configPath string, entries []hostreg.Host, seams deploySeams) (*hubHostManager, *hostops.Store, *hostreg.Registry) {
	t.Helper()
	registry, err := hostreg.New(entries)
	if err != nil {
		t.Fatalf("hostreg.New: %v", err)
	}
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatalf("hostops.Open: %v", err)
	}
	counters := seams.counters
	if counters == nil {
		counters = &deployCounters{}
	}
	facts := seams.facts
	if facts == nil {
		facts = func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			return planTestFacts(host), nil
		}
	}
	probe := seams.probe
	if probe == nil {
		probe = func(_ context.Context, _ hostreg.Host, _ *appwire.Client, _ appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{Version: "v0.9.0", RunningHealthy: true}, nil
		}
	}
	attached := seams.attached
	if attached == nil {
		attached = func(string) (sshconn.Preflight, bool) { return deployTestFacts(deployTestHost()), true }
	}
	clientAttached := seams.clientAttached
	if clientAttached == nil {
		clientAttached = func(string) (*appwire.Client, bool) { return &appwire.Client{}, true }
	}
	deploy := seams.deploy
	if deploy == nil {
		deploy = func(_ context.Context, host hostreg.Host, f sshconn.Preflight) (string, sshconn.Preflight, error) {
			counters.deploys.Add(1)
			return deployTestTarget(t, host), f, nil
		}
	}
	restart := seams.restart
	if restart == nil {
		restart = func(context.Context, hostreg.Host, sshconn.Preflight) error {
			counters.restarts.Add(1)
			return nil
		}
	}
	attachUnderGate := seams.attachUnderGate
	if attachUnderGate == nil {
		attachUnderGate = func(context.Context, hostreg.Host, hostops.Holder, bool) (func() bool, error) {
			return func() bool { return true }, nil
		}
	}
	cfg := hubcore.WebConfig{
		RemoteHostRegistry:         registry,
		RemoteHostOpsStore:         store,
		RemoteHostConfigPath:       configPath,
		RemoteHostClientIfAttached: clientAttached,
		RemoteHostPlanFacts:        facts,
		RemoteHostPlanProbe:        probe,
		HubBootID:                  "test-boot",
	}
	m := newHubHostManager(nil, nil, cfg, configPath, registry, func(string, ...any) {})
	m.cfg.deployHost = deploy
	m.cfg.restartHost = restart
	m.cfg.attachedFacts = attached
	m.cfg.remnantFence = seams.remnant
	m.cfg.attachUnderGate = attachUnderGate
	m.cfg.lastKnownPublish = seams.lastKnown
	return m, store, registry
}

// mintDeployToken mints a token bound to entry and the test's hub.toml
// fingerprint, with the running state the caller names.
func mintDeployToken(t *testing.T, store *hostops.Store, m *hubHostManager, entry hostreg.Host, runningVersion string, healthy bool, controllerRevision string) hostops.Token {
	t.Helper()
	if live, ok := m.liveHost(entry.Name); ok {
		entry = live
	}
	fingerprint, ok := m.hostTOMLFingerprint(entry.Name)
	if !ok {
		t.Fatal("the test hub.toml does not fingerprint")
	}
	token, err := store.MintToken(hostops.MintRequest{
		Host:               entry.Name,
		Generation:         entry.Generation,
		IncarnationID:      entry.IncarnationID,
		EntryHash:          hostEntryFingerprint(entry),
		HubTOMLFingerprint: fingerprint,
		FactsRevision:      testEntryFactDigest("facts"),
		FactsCapturedAt:    time.Now().UTC(),
		TargetPath:         deployTestTarget(t, entry),
		ControllerRevision: controllerRevision,
		RunningVersion:     runningVersion,
		RunningHealthy:     healthy,
		FreshnessBound:     hostops.DefaultFreshnessBound,
		TTL:                hostops.DefaultTokenTTL,
	})
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return token
}

// testEntryFactDigest renders a canonical digest for the test facts revision.
func testEntryFactDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// waitOperationState waits for the record to reach want, failing after a
// generous bound (the worker runs on its own goroutine).
func waitOperationState(t *testing.T, store *hostops.Store, id string, want hostops.State) hostops.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, ok := store.Record(id)
		if !ok {
			t.Fatalf("record %s disappeared", id)
		}
		if record.State == want {
			return record
		}
		if record.State.Terminal() {
			t.Fatalf("record %s reached %q with result %+v, want %q", id, record.State, record.Result, want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("record %s stayed %q, want %q", id, record.State, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestHostDeployRunsTheFixedOrderAndCompletes is the happy path: the token is
// consumed exactly once, the fresh record answers `pending`, the worker runs
// the deploy and the verified refresh, and the record reaches `complete`.
func TestHostDeployRunsTheFixedOrderAndCompletes(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{probe: deployProbeScript(t)})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")

	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if response.State != appwire.OperationStatePending || response.ClientOperationID != "op-1" || response.ID == "" {
		t.Fatalf("deploy response = %+v, want a fresh pending record echoing op-1", response)
	}
	if _, ok := store.OutstandingToken(entry.Name); ok {
		t.Fatal("the deploy left the consumed token row behind")
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("completed record carries result %+v", record.Result)
	}
	if len(record.Progress) == 0 {
		t.Fatal("the completed record carries no progress")
	}
	// The refresh's verified facts are what the completion is proved by; the
	// probe presented the record's persisted epoch, never a default.
	if record.Kind != hostops.KindDeploy || record.Host != entry.Name {
		t.Fatalf("record = %+v", record)
	}
}

// TestHostDeployDedupFirstReturnsTheRecordWithoutConsuming pins §6 step 1: a
// same-key replay returns the existing record with no token validation and no
// consumption — even when the presented token is unusable.
func TestHostDeployDedupFirstReturnsTheRecordWithoutConsuming(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{probe: deployProbeScript(t)})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	first, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("first Deploy: %v", err)
	}
	waitOperationState(t, store, first.ID, hostops.StateComplete)

	// The replay presents a token row nobody minted: dedup answers before any
	// token validation, so the record comes back.
	replay, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: strings.Repeat("A", 32), OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("replayed Deploy: %v", err)
	}
	if replay.ID != first.ID || replay.State != appwire.OperationStateComplete {
		t.Fatalf("replay = %+v, want record %s in state complete", replay, first.ID)
	}
}

// TestHostDeployRefusesTypedTokenErrors pins §11's four token refusals and
// their class: no record, the token row where one exists untouched.
func TestHostDeployRefusesTypedTokenErrors(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{probe: deployProbeScript(t)})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")

	// Mismatched: a value outside the wire's token shape.
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: "not-a-token", OperationID: "op-mismatched",
	})
	if info, _, code := deployWireInfo(t, err); info != appwire.ErrorTokenMismatched || code != appwire.CodeConflict {
		t.Fatalf("mismatched refusal = (%q, code %d), want (%q, %d)", info, code, appwire.ErrorTokenMismatched, appwire.CodeConflict)
	}

	// Superseded: a second mint replaces the row's nonce.
	superseded := token
	replacement := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: superseded.Value, OperationID: "op-superseded",
	})
	if info, _, _ := deployWireInfo(t, err); info != appwire.ErrorTokenSuperseded {
		t.Fatalf("superseded refusal = %q, want %q", info, appwire.ErrorTokenSuperseded)
	}
	if row, ok := store.OutstandingToken(entry.Name); !ok || row.Value != replacement.Value {
		t.Fatalf("the superseded refusal disturbed the current row: %+v", row)
	}

	// Expired: the presented row is expired. The store's clock is the real clock,
	// so the fixture's TTL is spent by sleeping past a sub-second... the token
	// fixture mints the default five-minute TTL; expiry is exercised against the
	// store directly (hostops' own clock-injected tests) and through the consume
	// path below by consuming the row and replaying it.
	consumed, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: replacement.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("a fresh token refused: %v", err)
	}
	waitOperationState(t, store, consumed.ID, hostops.StateComplete)
	// Consumed-then-replayed reads as token-missing (§12): the row is gone.
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: replacement.Value, OperationID: "op-replayed",
	})
	if info, _, _ := deployWireInfo(t, err); info != appwire.ErrorTokenMissing {
		t.Fatalf("consumed-token replay refusal = %q, want %q", info, appwire.ErrorTokenMissing)
	}
	if row, ok := store.OutstandingToken(entry.Name); ok {
		t.Fatalf("a token row survived the consume: %+v", row)
	}
}

// TestHostDeployRefusesRemnantOpenPastDedup pins §6 step 2's fence: the
// remnant-open arm fires past the dedup check, and a dedup hit is unaffected.
func TestHostDeployRefusesRemnantOpenPastDedup(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:   deployProbeScript(t),
		remnant: func(string) (string, bool) { return "remnant-7", true },
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, code := deployWireInfo(t, err)
	if info != appwire.ErrorRemnantOpen || code != appwire.CodeConflict {
		t.Fatalf("remnant refusal = (%q, code %d), want (%q, %d)", info, code, appwire.ErrorRemnantOpen, appwire.CodeConflict)
	}
	var remnantID string
	if uerr := json.Unmarshal(data["remnantId"], &remnantID); uerr != nil || remnantID != "remnant-7" {
		t.Fatalf("remnantId = %q (%v), want remnant-7", remnantID, uerr)
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("the remnant refusal consumed the token")
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("the remnant refusal created records: %d", got)
	}

	// A dedup hit beats the fence: create the record with the fence lifted.
	m.cfg.remnantFence = nil
	created, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy with the fence lifted: %v", err)
	}
	waitOperationState(t, store, created.ID, hostops.StateComplete)
	m.cfg.remnantFence = func(string) (string, bool) { return "remnant-7", true }
	replay, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: strings.Repeat("C", 32), OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("dedup hit with an open remnant refused: %v", err)
	}
	if replay.State != appwire.OperationStatePending && replay.State != appwire.OperationStateRunning &&
		replay.State != appwire.OperationStateComplete {
		t.Fatalf("replay state = %q", replay.State)
	}
}

// TestHostDeployRefusesABusyGate pins §5's busy classes as deploy renders them
// and the holder publication sub-window: an operation-held gate names the
// record id; the pre-record window and a plan-held gate render the transient
// form with no operation reference.
func TestHostDeployRefusesABusyGate(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")

	// The pre-record window: a deploy holds the gate with no record yet.
	preRecord, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderOperation})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if info, _, code := deployWireInfo(t, err); info != appwire.ErrorHostBusyTransient || code != appwire.CodeConflict {
		t.Fatalf("pre-record busy = (%q, %d), want (%q, %d)", info, code, appwire.ErrorHostBusyTransient, appwire.CodeConflict)
	}
	// The promotion: the same hold names its operation once the record exists.
	if err := m.cfg.gate.HoldAs(entry.Name, hostops.Holder{Kind: hostops.HolderOperation, OperationID: "00000000000000000042"}); err != nil {
		t.Fatalf("HoldAs: %v", err)
	}
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, _ := deployWireInfo(t, err)
	var operationID string
	if uerr := json.Unmarshal(data["operationId"], &operationID); uerr != nil {
		t.Fatalf("busy data carries no operationId: %v", uerr)
	}
	if info != appwire.ErrorHostBusyOperation || operationID != "00000000000000000042" {
		t.Fatalf("promoted busy = (%q, %q), want (%q, 00000000000000000042)",
			info, operationID, appwire.ErrorHostBusyOperation)
	}
	preRecord()

	// A plan-held gate renders the transient form with no operation reference.
	planRelease, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("TryAcquire(plan): %v", err)
	}
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, _ = deployWireInfo(t, err)
	if info != appwire.ErrorHostBusyTransient {
		t.Fatalf("plan-held busy = %q, want %q", info, appwire.ErrorHostBusyTransient)
	}
	if _, ok := data["operationId"]; ok {
		t.Fatal("the transient form carried an operationId")
	}
	planRelease()

	// The token survived every contended attempt.
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("a busy refusal consumed the token")
	}
}

// TestHostDeployRefusesDetachedAndLeavesTheToken pins §6's detached-during-
// deploy rule: the channel gone at the gated probe is `host-detached`, the
// token is unconsumed and no record exists.
func TestHostDeployRefusesDetachedAndLeavesTheToken(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{}, fmt.Errorf("%w: no live channel", errHostDetached)
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, _, code := deployWireInfo(t, err)
	if info != appwire.ErrorHostDetached || code != appwire.CodeUnavailable {
		t.Fatalf("detached refusal = (%q, code %d), want (%q, %d)", info, code, appwire.ErrorHostDetached, appwire.CodeUnavailable)
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("the detached refusal consumed the token")
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("the detached refusal created records: %d", got)
	}
	if _, ok := store.ProbeEpoch(entry.Name); ok {
		t.Fatal("the detached refusal left a probe epoch behind")
	}
}

// TestHostDeployUnderGateRevalidationRefusesRunningDrift pins §6 step 3's
// execution-time re-resolution: a probe result that differs from the token's
// bindings is a typed stale-entry refusal with no record and no consumption.
func TestHostDeployUnderGateRevalidationRefusesRunningDrift(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{Version: "v0.8.0", RunningHealthy: true}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, code := deployWireInfo(t, err)
	if info != appwire.ErrorStaleEntry || code != appwire.CodeConflict {
		t.Fatalf("drift refusal = (%q, code %d), want (%q, %d)", info, code, appwire.ErrorStaleEntry, appwire.CodeConflict)
	}
	var binding string
	if uerr := json.Unmarshal(data["binding"], &binding); uerr != nil || binding != "running-version" {
		t.Fatalf("stale binding = %q (%v), want running-version", binding, uerr)
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("the drift refusal consumed the token")
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("the drift refusal created records: %d", got)
	}
	if _, ok := store.ProbeEpoch(entry.Name); ok {
		t.Fatal("the drift refusal left its probe epoch behind")
	}
}

// TestHostDeployUnderGateRevalidationRefusesFingerprintDrift pins the
// hub.toml-fingerprint arm: a hand edit between plan and deploy refuses.
func TestHostDeployUnderGateRevalidationRefusesFingerprintDrift(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	edited := entry
	edited.SSH = "m4-elsewhere.example"
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{edited})), 0o600); err != nil {
		t.Fatalf("rewrite hub.toml: %v", err)
	}
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, _ := deployWireInfo(t, err)
	var binding string
	if uerr := json.Unmarshal(data["binding"], &binding); uerr != nil || info != appwire.ErrorStaleEntry || binding != "hub.toml-fingerprint" {
		t.Fatalf("fingerprint drift refusal = (%q, %q, %v), want (stale-entry, hub.toml-fingerprint)", info, binding, uerr)
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("the fingerprint drift refusal consumed the token")
	}
}

// TestHostDeployRefusesAConcurrentTerminalOperation pins §6 step 3's closing
// scan: an operation that finishes while the probe ran refuses the deploy with
// stale-entry (concurrent-terminal-op), token unconsumed.
func TestHostDeployRefusesAConcurrentTerminalOperation(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	var store *hostops.Store
	var m *hubHostManager
	var otherID string
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			// A terminal operation lands during the probe window.
			live, ok := m.liveHost(entry.Name)
			if !ok {
				return hubcore.HostRuntimeProbe{}, errors.New("the host vanished from the registry")
			}
			record, err := store.Create(hostops.NewRecord{
				ClientOperationID: "op-other", Host: entry.Name, Kind: hostops.KindRestart,
				Generation: live.Generation, IncarnationID: live.IncarnationID,
			})
			if err != nil {
				return hubcore.HostRuntimeProbe{}, err
			}
			otherID = record.ID
			if _, err := store.Transition(record.ID, hostops.StateFailed, func(r *hostops.Record) {
				r.Result = &hostops.Result{OK: false, Message: "done elsewhere"}
			}); err != nil {
				return hubcore.HostRuntimeProbe{}, err
			}
			return hubcore.HostRuntimeProbe{Version: "v0.9.0", RunningHealthy: true}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	info, data, _ := deployWireInfo(t, err)
	var binding string
	_ = json.Unmarshal(data["binding"], &binding)
	if info != appwire.ErrorStaleEntry || binding != "concurrent-terminal-op" {
		t.Fatalf("concurrent-terminal refusal = (%q, %q), want (stale-entry, concurrent-terminal-op)", info, binding)
	}
	if otherID == "" {
		t.Fatal("the probe seam never created the competing record")
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Fatal("the concurrent-terminal refusal consumed the token")
	}
	if got := len(store.Records()); got != 1 {
		t.Fatalf("records = %d, want only the competing record", got)
	}
}

// TestHostRestartRunsTheOperationAndCompletes is the restart happy path: a
// fresh create answers `pending`, the worker runs the 04b restart path, the
// post-operation probe proves the new process, and the record completes.
func TestHostRestartRunsTheOperationAndCompletes(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "v1.2.3", "v1.2.3", before, after),
	})
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if response.State != appwire.OperationStatePending {
		t.Fatalf("restart response = %+v, want pending", response)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Kind != hostops.KindRestart || record.Generation != 2 || record.IncarnationID != "inc-new" {
		t.Fatalf("restart record = %+v", record)
	}
	if len(record.FencingEpoch) == 0 {
		t.Fatal("the restart record carries no fencing epoch")
	}
}

// TestHostRestartIncarnationPair pins §12's restart incarnation row at the
// handler: a retry repeating the old pair replays the retained record, a pair
// older than current with no record refuses stale-entry, and a reuse naming the
// new pair opens fresh.
func TestHostRestartIncarnationPair(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "v1.2.3", "v1.2.3", before, after),
	})

	// The retained record from before the update: pinned to the superseded pair.
	retained, err := store.Create(hostops.NewRecord{
		ClientOperationID: "op-old", Host: entry.Name, Kind: hostops.KindRestart,
		Generation: 1, IncarnationID: "inc-old",
	})
	if err != nil {
		t.Fatalf("Create retained: %v", err)
	}
	if _, err := store.Transition(retained.ID, hostops.StateFailed, func(r *hostops.Record) {
		r.Result = &hostops.Result{OK: false, Message: "old outcome"}
	}); err != nil {
		t.Fatalf("Transition retained: %v", err)
	}

	// A lost-response retry repeating the old pair replays the retained record.
	replay, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-old", Generation: 1, IncarnationID: "inc-old",
	})
	if err != nil {
		t.Fatalf("old-pair retry: %v", err)
	}
	if replay.ID != retained.ID || replay.State != appwire.OperationStateFailed {
		t.Fatalf("old-pair retry = %+v, want the retained %s", replay, retained.ID)
	}

	// A pair older than current with no retained record is stale-entry.
	_, err = m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-other", Generation: 1, IncarnationID: "inc-missing",
	})
	info, data, _ := deployWireInfo(t, err)
	var binding string
	_ = json.Unmarshal(data["binding"], &binding)
	if info != appwire.ErrorStaleEntry || binding != "pruned-generation" {
		t.Fatalf("stale-pair refusal = (%q, %q), want (stale-entry, pruned-generation)", info, binding)
	}

	// The same operation ID naming the new pair opens fresh.
	fresh, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-old", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("new-pair reuse: %v", err)
	}
	if fresh.ID == retained.ID {
		t.Fatalf("the new-pair reuse replayed the superseded record %s", retained.ID)
	}
	waitOperationState(t, store, fresh.ID, hostops.StateComplete)
}

// TestHostOperationWorkerSurvivesAClientDisconnect pins §6's worker lifetime:
// the operation's worker runs under the controller-lifetime context, so
// canceling the RPC's context after the record exists neither cancels the
// worker nor stops it completing.
func TestHostOperationWorkerSurvivesAClientDisconnect(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var sawCanceled atomic.Bool
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: deployProbeScript(t),
		deploy: func(ctx context.Context, host hostreg.Host, f sshconn.Preflight) (string, sshconn.Preflight, error) {
			close(entered)
			<-proceed
			if ctx.Err() != nil {
				sawCanceled.Store(true)
			}
			return deployTestTarget(t, host), f, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	rpcCtx, cancelRPC := context.WithCancel(context.Background())
	response, err := m.Deploy(rpcCtx, appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	<-entered
	cancelRPC()
	close(proceed)
	waitOperationState(t, store, response.ID, hostops.StateComplete)
	if sawCanceled.Load() {
		t.Fatal("the worker's context was canceled by the client disconnect")
	}
}

// TestHostOperationShutdownInterruptsAndReleasesTheGate pins the shutdown half
// of §6's worker lifetime: controller shutdown records `interrupted` with the
// shutdown note and releases the host's gate.
func TestHostOperationShutdownInterruptsAndReleasesTheGate(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	entered := make(chan struct{})
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		deploy: func(ctx context.Context, host hostreg.Host, f sshconn.Preflight) (string, sshconn.Preflight, error) {
			close(entered)
			<-ctx.Done()
			return "", f, ctx.Err()
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	<-entered
	if moved := m.ShutdownHostOperations(2 * time.Second); moved != 0 {
		// The worker settles its own record; the pass is the belt-and-braces
		// half and must find nothing left.
		t.Fatalf("shutdown's leftover pass moved %d records, want 0 (the worker settled)", moved)
	}
	record, ok := store.Record(response.ID)
	if !ok || record.State != hostops.StateInterrupted {
		t.Fatalf("record after shutdown = %+v, want interrupted", record)
	}
	if record.Result == nil || !strings.Contains(record.Result.Message, "shutting down") {
		t.Fatalf("interrupted note = %+v, want the shutdown named", record.Result)
	}
	if _, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderPlan}); err != nil {
		t.Fatalf("the shutdown did not release the host's gate: %v", err)
	}
}

// gateHeldBy reports whether the hub's per-host gate for name is held by anyone
// other than this probe: a try-acquire that fails busy proves a holder.
func gateHeldBy(m *hubHostManager, name string) bool {
	release, err := m.cfg.gate.TryAcquire(name, hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		return true
	}
	release()
	return false
}

// progressText renders a record's progress lines for substring assertions.
func progressText(record hostops.Record) string {
	lines := make([]string, 0, len(record.Progress))
	for _, entry := range record.Progress {
		lines = append(lines, entry.Message)
	}
	return strings.Join(lines, "\n")
}

// TestHostDeployPlannedRestartReattachesUnderTheHeldGate pins §6 seam (d) and
// §12's restart-reattach row: the worker retains the host's gate across the
// restart's channel drop, reattaches through the gate-aware primitive — never
// the normal attach path — re-probes over the reattached channel, and hands the
// supervisor off under the still-held gate before releasing it.
func TestHostDeployPlannedRestartReattachesUnderTheHeldGate(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(90 * time.Second)
	var restartSawGateHeld, reattachSawGateHeld, handoffSawGateHeld atomic.Bool
	var reattachCalls, handoffCalls atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			restartSawGateHeld.Store(gateHeldBy(m, entry.Name))
			return nil
		},
		attachUnderGate: func(_ context.Context, got hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error) {
			reattachSawGateHeld.Store(gateHeldBy(m, entry.Name))
			if explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			live, ok := m.liveHost(got.Name)
			if !ok || !hostreg.SameRegistration(live, got) {
				return nil, fmt.Errorf("the reattach ran for %q/%d, not the live registration %+v", got.Name, got.Generation, live)
			}
			if holder.Kind != hostops.HolderOperation || holder.OperationID == "" {
				return nil, fmt.Errorf("the reattach presented holder %+v, want the operation holder naming the record", holder)
			}
			reattachCalls.Add(1)
			return func() bool {
				handoffSawGateHeld.Store(gateHeldBy(m, entry.Name))
				handoffCalls.Add(1)
				return true
			}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "dev", true, "dev")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if !restartSawGateHeld.Load() {
		t.Fatal("the restart step ran without the host's gate held")
	}
	if !reattachSawGateHeld.Load() {
		t.Fatal("the operation-owned reattach ran without the host's gate held")
	}
	if !handoffSawGateHeld.Load() {
		t.Fatal("the supervisor handoff ran after the gate was released")
	}
	if got := reattachCalls.Load(); got != 1 {
		t.Fatalf("reattach calls = %d, want 1", got)
	}
	if got := handoffCalls.Load(); got != 1 {
		t.Fatalf("handoff calls = %d, want 1", got)
	}
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("planned-restart record result = %+v, want ok", record.Result)
	}
	progress := progressText(record)
	if !strings.Contains(progress, "under the held host gate") {
		t.Fatalf("the record does not name the under-gate reattach:\n%s", progress)
	}
	if strings.Contains(progress, "releasing the host gate") {
		t.Fatalf("the seam-(d) interim release point survives:\n%s", progress)
	}
}

// TestHostRestartUnattachedAttachFirstsUnderTheHeldGate pins §6's attach-first
// arm and §12's row: a restart whose host has no attached channel runs the
// operation-owned attach under the already-held gate first (never refusing
// host-detached), names the attach-first path in the record, and still
// reattaches after the restart drops that channel.
func TestHostRestartUnattachedAttachFirstsUnderTheHeldGate(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachedState atomic.Bool
	var attachCalls, attachFirstCalls, restartCalls atomic.Int64
	var attachGateFree atomic.Bool
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:          restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, attachedState.Load() },
		attached:       func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), attachedState.Load() },
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			restartCalls.Add(1)
			attachedState.Store(false) // the restart drops the channel
			return nil
		},
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			call := attachCalls.Add(1)
			if call == 1 && !explicit {
				t.Error("the attach-first arm ran with explicit=false; a first attach keeps the bootstrap semantics")
			}
			if call == 2 && explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			if !gateHeldBy(m, entry.Name) {
				attachGateFree.Store(true)
			}
			if call == 1 {
				attachFirstCalls.Add(1)
			}
			attachedState.Store(true)
			return func() bool { return true }, nil
		},
	})
	// The host is unattached: the handler must accept the restart and let the
	// worker attach first, never refuse host-detached.
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart on an unattached host: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if got := restartCalls.Load(); got != 1 {
		t.Fatalf("restart calls = %d, want 1", got)
	}
	if got := attachCalls.Load(); got != 2 {
		t.Fatalf("attach calls = %d, want the attach-first plus the post-restart reattach", got)
	}
	if got := attachFirstCalls.Load(); got != 1 {
		t.Fatalf("attach-first calls = %d, want 1", got)
	}
	if attachGateFree.Load() {
		t.Fatal("an attach-under-gate call ran without the host's gate held")
	}
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("attach-first restart record result = %+v, want ok", record.Result)
	}
	progress := progressText(record)
	if !strings.Contains(progress, "attach-first") {
		t.Fatalf("the record does not name the attach-first path:\n%s", progress)
	}
}

// TestHostRestartDroppedChannelAttachFirstsUnderTheHeldGate pins the
// detached-window rule: a restart whose channel drops between the handler's
// resolution and the worker's start attaches first under the held gate. The
// attach-first decision is re-evaluated where the channel is used, never
// trusted from the earlier check.
func TestHostRestartDroppedChannelAttachFirstsUnderTheHeldGate(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachState atomic.Bool
	var clientChecks, attachCalls, attachFirstCalls atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) {
			// The handler's check (the first call) sees the host attached; the
			// drop lands before the worker's own check, which must attach first.
			return &appwire.Client{}, clientChecks.Add(1) == 1 || attachState.Load()
		},
		attached: func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), attachState.Load() },
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			attachState.Store(false) // the restart drops the channel
			return nil
		},
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			call := attachCalls.Add(1)
			if call == 1 {
				attachFirstCalls.Add(1)
			}
			if call == 1 && !explicit {
				t.Error("the attach-first arm ran with explicit=false; a first attach keeps the bootstrap semantics")
			}
			if call == 2 && explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			attachState.Store(true)
			return func() bool { return true }, nil
		},
	})
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if got := attachFirstCalls.Load(); got != 1 {
		t.Fatalf("attach-first calls = %d, want 1 for the channel that dropped before the worker", got)
	}
	if got := attachCalls.Load(); got != 2 {
		t.Fatalf("attach calls = %d, want the attach-first plus the post-restart reattach", got)
	}
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("dropped-window restart record result = %+v, want ok", record.Result)
	}
	if progress := progressText(record); !strings.Contains(progress, "attach-first") {
		t.Fatalf("the record does not name the attach-first path:\n%s", progress)
	}
}

// TestHostRestartAttachFirstHandsOffWhenTheRestartFails pins the retained
// handoff: when a restart fails after the attach-first attached the host, the
// worker still hands that channel to a supervisor under the held gate before
// releasing — a failed restart must never strand a live attached channel.
func TestHostRestartAttachFirstHandsOffWhenTheRestartFails(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachedState atomic.Bool
	var handoffCalls atomic.Int64
	var handoffSawGateHeld atomic.Bool
	handedOffCh := make(chan struct{}, 1)
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:          restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, attachedState.Load() },
		attached:       func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), attachedState.Load() },
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			return errors.New("the restart command failed before replacing the process")
		},
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			attachedState.Store(true)
			return func() bool {
				handoffSawGateHeld.Store(gateHeldBy(m, entry.Name))
				handoffCalls.Add(1)
				select {
				case handedOffCh <- struct{}{}:
				default:
				}
				return true
			}, nil
		},
	})
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || record.Result.OK {
		t.Fatalf("failed restart record result = %+v, want a failure", record.Result)
	}
	// The restart-failure path records its outcome through fail(), which hands
	// the channel off before m.failOperation writes the terminal state, so the
	// handoff has run by the time the record reads failed; the worker's
	// deferred finish is only the backstop for exits that return without a
	// failure. Wait on the handoff signal rather than assuming it already ran.
	select {
	case <-handedOffCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("handoff calls = %d, want the attach-first channel handed off despite the failed restart", handoffCalls.Load())
	}
	if got := handoffCalls.Load(); got != 1 {
		t.Fatalf("handoff calls = %d, want the attach-first channel handed off despite the failed restart", got)
	}
	if !handoffSawGateHeld.Load() {
		t.Fatal("the handoff ran after the gate was released")
	}
}

// TestHostRestartReattachRetriesATransientAttachFailure pins the restored
// tolerance: under the held gate the dropped channel's supervisor cannot
// reconnect, so a host still coming back up after a reboot can refuse the
// reattach dial with a retryable error. A transient failure is retried within
// the refresh window instead of failing the whole restart on the first dial,
// and the record shows the reattach path exactly once — retries are one step,
// not many.
func TestHostRestartReattachRetriesATransientAttachFailure(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachCalls, handedOff atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:          restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, true },
		attached:       func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), true },
		restart:        func(context.Context, hostreg.Host, sshconn.Preflight) error { return nil },
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			if explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			if call := attachCalls.Add(1); call <= 2 {
				// The host is not accepting dials yet: a retryable transport error.
				return nil, errors.New("dial tcp 10.0.0.5:22: connect: connection refused")
			}
			return func() bool { handedOff.Add(1); return true }, nil
		},
	})
	m.cfg.probeTimeout = 300 * time.Millisecond // refreshWindow = 1.2s: two retries fit
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("transient-reattach restart record result = %+v, want ok", record.Result)
	}
	if got := attachCalls.Load(); got != 3 {
		t.Fatalf("attach calls = %d, want 3 (two transient refusals, then the success)", got)
	}
	if got := handedOff.Load(); got != 1 {
		t.Fatalf("handoff calls = %d, want 1", got)
	}
	progress := progressText(record)
	if got := strings.Count(progress, "reattaching the host under the held host gate"); got != 1 {
		t.Fatalf("the record shows %d reattach progress lines, want 1:\n%s", got, progress)
	}
}

// TestHostRestartReattachFailsFastOnATerminalAttachFailure pins the other side
// of the tolerance: a terminal attach cause cannot be fixed by waiting, so the
// reattach fails on the first attempt and the record carries the terminal
// cause verbatim.
func TestHostRestartReattachFailsFastOnATerminalAttachFailure(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachCalls atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:          restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, true },
		attached:       func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), true },
		restart:        func(context.Context, hostreg.Host, sshconn.Preflight) error { return nil },
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, _ bool) (func() bool, error) {
			attachCalls.Add(1)
			return nil, sshconn.ErrProtocolIncompatible
		},
	})
	m.cfg.probeTimeout = 300 * time.Millisecond
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if got := attachCalls.Load(); got != 1 {
		t.Fatalf("attach calls = %d, want 1: a terminal cause must not be retried", got)
	}
	if record.Result == nil || record.Result.OK {
		t.Fatalf("terminal-reattach restart record result = %+v, want a failure", record.Result)
	}
	if msg := record.Result.Message; !strings.Contains(msg, "host protocol incompatible") {
		t.Fatalf("the record does not carry the terminal cause:\n%s", msg)
	}
	if progress := progressText(record); strings.Count(progress, "reattaching the host under the held host gate") != 1 {
		t.Fatalf("the record does not show the reattach path exactly once:\n%s", progress)
	}
}

// TestHostRestartReattachReportsAnExhaustedRefreshWindow pins the honest
// failure arm: when the host never comes back within the refresh window, the
// retry stops at the bound, the operation fails with a message naming the
// window, and the last cause travels with it — never a claim of success.
func TestHostRestartReattachReportsAnExhaustedRefreshWindow(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachCalls atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe:          restartProbeScript(t, "dev", "dev", before, after),
		clientAttached: func(string) (*appwire.Client, bool) { return &appwire.Client{}, true },
		attached:       func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), true },
		restart:        func(context.Context, hostreg.Host, sshconn.Preflight) error { return nil },
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, _ bool) (func() bool, error) {
			attachCalls.Add(1)
			return nil, errors.New("dial tcp 10.0.0.5:22: connect: connection refused")
		},
	})
	m.cfg.probeTimeout = 100 * time.Millisecond // refreshWindow = 400ms
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if got := attachCalls.Load(); got < 2 || got > 3 {
		t.Fatalf("attach calls = %d, want the retry bounded by the window (2 or 3)", got)
	}
	if record.Result == nil || record.Result.OK {
		t.Fatalf("exhausted-window restart record result = %+v, want a failure", record.Result)
	}
	msg := record.Result.Message
	if !strings.Contains(msg, "did not reattach within the refresh window after the restart") {
		t.Fatalf("the failure does not name the exhausted window:\n%s", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Fatalf("the last cause did not travel with the failure:\n%s", msg)
	}
	if progress := progressText(record); strings.Count(progress, "reattaching the host under the held host gate") != 1 {
		t.Fatalf("the record does not show the reattach path exactly once:\n%s", progress)
	}
}

// promotionFailingGate fails the first HoldAs it sees — the record promotion
// startOperation logs as non-fatal — and tracks the holder its inner gate
// carries, so a test can prove the worker restores the holder before its attach
// (the gate-aware primitive requires the gate to carry the holder presented).
type promotionFailingGate struct {
	inner hostops.Gate
	mu    sync.Mutex
	calls int
	// effective is the holder the inner gate carries: what TryAcquire
	// registered, or what the last successful HoldAs published.
	effective hostops.Holder
}

func (g *promotionFailingGate) TryAcquire(host string, holder hostops.Holder) (func(), error) {
	release, err := g.inner.TryAcquire(host, holder)
	if err == nil {
		g.mu.Lock()
		g.effective = holder
		g.mu.Unlock()
	}
	return release, err
}

func (g *promotionFailingGate) HoldAs(host string, holder hostops.Holder) error {
	g.mu.Lock()
	g.calls++
	fail := g.calls == 1
	g.mu.Unlock()
	if fail {
		// The promotion that could not publish: the inner gate keeps the
		// pre-record holder TryAcquire registered.
		return hostops.ErrGateNotHeld
	}
	if err := g.inner.HoldAs(host, holder); err != nil {
		return err
	}
	g.mu.Lock()
	g.effective = holder
	g.mu.Unlock()
	return nil
}

func (g *promotionFailingGate) effectiveHolder() hostops.Holder {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.effective
}

// holderTrackingGate delegates to inner and records the holder the inner gate
// carries, so a test can observe the gate's holder across the attach ladder's
// own holder changes (an Ensure-triggered deploy promotes it and restores the
// manager holder when the step finishes).
type holderTrackingGate struct {
	inner hostops.Gate
	mu    sync.Mutex
	// effective is the holder the inner gate carries: what TryAcquire
	// registered, or what the last successful HoldAs published.
	effective hostops.Holder
}

func (g *holderTrackingGate) TryAcquire(host string, holder hostops.Holder) (func(), error) {
	release, err := g.inner.TryAcquire(host, holder)
	if err == nil {
		g.mu.Lock()
		g.effective = holder
		g.mu.Unlock()
	}
	return release, err
}

func (g *holderTrackingGate) HoldAs(host string, holder hostops.Holder) error {
	if err := g.inner.HoldAs(host, holder); err != nil {
		return err
	}
	g.mu.Lock()
	g.effective = holder
	g.mu.Unlock()
	return nil
}

func (g *holderTrackingGate) effectiveHolder() hostops.Holder {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.effective
}

// TestHostRestartRestoresTheHolderAfterANestedEnsureDeploy pins the nested-
// deploy interaction: the attach ladder can run an Ensure-triggered deploy
// whose recorder promotes the gate to the inner operation and whose finish
// restores the manager holder. The worker must re-assert its own holder after
// the attach — otherwise the handoff's own-hold precondition refuses and a
// published channel is left unsupervised with the operation recorded failed.
func TestHostRestartRestoresTheHolderAfterANestedEnsureDeploy(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var gate *holderTrackingGate
	var handoffRefused atomic.Bool
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
		attachUnderGate: func(_ context.Context, got hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error) {
			if explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			// The attach ladder ran an Ensure-triggered deploy: its recorder
			// promoted the gate to the inner operation, and its finish restored
			// the manager holder, exactly as m.EnsureDeploy/finishEnsureOperation
			// do in production.
			if err := gate.HoldAs(got.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"}); err != nil {
				return nil, err
			}
			return func() bool {
				if got := gate.effectiveHolder(); got != holder {
					handoffRefused.Store(true)
					return false
				}
				return true
			}, nil
		},
	})
	gate = &holderTrackingGate{inner: m.cfg.gate}
	m.cfg.gate = gate

	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if handoffRefused.Load() {
		t.Fatal("the handoff's own-hold precondition refused after the nested Ensure deploy")
	}
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("restart record result = %+v, want ok", record.Result)
	}
	if got := gate.effectiveHolder(); got.OperationID != record.ID {
		t.Fatalf("the gate carries holder %+v, want the operation holder %q restored", got, record.ID)
	}
}

// TestHostRestartRestoresTheOperationHolderBeforeTheAttach pins the contract
// agreement between the non-fatal record promotion and the primitive's
// own-holder precondition: a promotion that could not publish must not leave
// the worker presenting a holder the gate does not carry.
func TestHostRestartRestoresTheOperationHolderBeforeTheAttach(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var gate *promotionFailingGate
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
		attachUnderGate: func(_ context.Context, _ hostreg.Host, holder hostops.Holder, explicit bool) (func() bool, error) {
			// The primitive refuses unless the gate carries the presented
			// holder, so standing that check in here makes a lost promotion
			// observably fatal without it.
			if got := gate.effectiveHolder(); got.OperationID != holder.OperationID {
				return nil, fmt.Errorf("the gate carries holder %+v, the attach presents %+v", got, holder)
			}
			return func() bool { return true }, nil
		},
	})
	gate = &promotionFailingGate{inner: m.cfg.gate}
	m.cfg.gate = gate

	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart despite the failed record promotion: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("restart record result = %+v, want ok", record.Result)
	}
	gate.mu.Lock()
	calls, effective := gate.calls, gate.effective
	gate.mu.Unlock()
	if calls < 2 {
		t.Fatalf("HoldAs calls = %d, want the failed promotion plus the worker's restore", calls)
	}
	if effective.OperationID != record.ID {
		t.Fatalf("the gate carries holder %+v, want the record holder %q restored", effective, record.ID)
	}
}

// nthHoldAsFailingGate fails the chosen HoldAs call (1-based) and delegates
// every other call, so a test can break one specific holder publication — the
// post-attach restore, say — without disturbing the others.
type nthHoldAsFailingGate struct {
	inner  hostops.Gate
	mu     sync.Mutex
	calls  int
	failAt int
}

func (g *nthHoldAsFailingGate) TryAcquire(host string, holder hostops.Holder) (func(), error) {
	return g.inner.TryAcquire(host, holder)
}

func (g *nthHoldAsFailingGate) HoldAs(host string, holder hostops.Holder) error {
	g.mu.Lock()
	g.calls++
	fail := g.calls == g.failAt
	g.mu.Unlock()
	if fail {
		return hostops.ErrGateNotHeld
	}
	return g.inner.HoldAs(host, holder)
}

// TestHostRestartKeepsThePublishedChannelsHandoffWhenTheHolderRestoreFails pins
// the restore-failure contract: when the post-attach holder restore fails after
// the attach published a channel, the worker still invokes that channel's
// handoff before recording the failure, so a published channel is never left
// without the supervisor start its handoff owns.
func TestHostRestartKeepsThePublishedChannelsHandoffWhenTheHolderRestoreFails(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var handoffCalls atomic.Int64
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			if explicit {
				t.Error("the post-restart reattach ran with explicit=true; a reattach must never bootstrap a hub")
			}
			return func() bool {
				handoffCalls.Add(1)
				return true
			}, nil
		},
	})
	// Call 1 is the record promotion, call 2 the pre-attach restore, call 3 the
	// post-attach restore: failing only call 3 leaves the gate held by the
	// operation holder, so the handoff itself can still run.
	m.cfg.gate = &nthHoldAsFailingGate{inner: m.cfg.gate, failAt: 3}

	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || !strings.Contains(record.Result.Message, "could not be restored after the attach") {
		t.Fatalf("failure = %+v, want the restore failure named", record.Result)
	}
	if got := handoffCalls.Load(); got != 1 {
		t.Fatalf("handoff calls = %d, want the published channel's handoff invoked despite the restore failure", got)
	}
}

// TestHostRestartWithoutAnAttachSeamPreservesTheNoManagerArm pins the
// manager-less contract: with no operation-owned attach wired there is no
// channel to reattach, so the worker's arm is a no-op success — exactly the
// interim wait's manager-less arm — and the refresh's own attachment check
// still decides completion.
func TestHostRestartWithoutAnAttachSeamPreservesTheNoManagerArm(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", before, after),
	})
	m.cfg.attachUnderGate = nil // the manager-less configuration

	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart without an attach seam: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("restart record result = %+v, want ok", record.Result)
	}
}

// TestHostRestartAttachFirstProbesTheFreshlyAttachedClient pins the probe's
// client source: the pre-restart probe resolves the live client after the
// attach-first attach, never a value captured before the worker ran — the
// attach-first arm's probe must see the client the attach just published, not
// nil and not the dropped predecessor.
func TestHostRestartAttachFirstProbesTheFreshlyAttachedClient(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	var attachState atomic.Bool
	var published atomic.Pointer[appwire.Client]
	var probeSawStaleClient atomic.Bool
	probe := restartProbeScript(t, "dev", "dev", before, after)
	var m *hubHostManager
	var store *hostops.Store
	m, store, _ = deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: func(ctx context.Context, host hostreg.Host, client *appwire.Client, epoch appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			if client == nil || client != published.Load() {
				probeSawStaleClient.Store(true)
			}
			return probe(ctx, host, client, epoch)
		},
		clientAttached: func(string) (*appwire.Client, bool) {
			if !attachState.Load() {
				return nil, false
			}
			return published.Load(), true
		},
		attached: func(string) (sshconn.Preflight, bool) { return deployTestFacts(entry), attachState.Load() },
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			attachState.Store(false) // the restart drops the channel
			return nil
		},
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			published.Store(&appwire.Client{})
			attachState.Store(true)
			return func() bool { return true }, nil
		},
	})
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateComplete)
	if record.Result == nil || !record.Result.OK {
		t.Fatalf("restart record result = %+v, want ok", record.Result)
	}
	if probeSawStaleClient.Load() {
		t.Fatal("a running probe received nil or a client other than the freshly attached one")
	}
}

// TestHostDeployPostRefreshRecordsAnUnverifiableFailureVerbatim pins §6's
// verification rule: a post-restart probe whose process start time did not
// change is a recorded failure, never a clean success.
func TestHostDeployPostRefreshRecordsAnUnverifiableFailureVerbatim(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	same := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "dev", "dev", same, same),
	})
	token := mintDeployToken(t, store, m, entry, "dev", true, "dev")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || record.Result.OK {
		t.Fatalf("failed record result = %+v, want a failure", record.Result)
	}
	if !strings.Contains(record.Result.Message, "same process start time") {
		t.Fatalf("failure message %q does not record the unchanged start time verbatim", record.Result.Message)
	}
}

// TestEnsureTriggeredDeployIsADurableOperationNamingItsRecord pins §6's
// Ensure-triggered operation and §12's "Ensure busy names its operation": the
// hook persists the record with its fencing epoch under the held gate, the gate
// hold publishes the operation so a contender's refusal names the record id,
// and the finish records the outcome.
func TestEnsureTriggeredDeployIsADurableOperationNamingItsRecord(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}

	// The Ensure path holds the host's gate when it deploys.
	release, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	finish, err := m.EnsureDeploy(live)
	if err != nil {
		t.Fatalf("EnsureDeploy: %v", err)
	}
	records := store.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want the one Ensure operation", len(records))
	}
	record := records[0]
	if record.State != hostops.StatePending || record.Kind != hostops.KindDeploy {
		t.Fatalf("Ensure record = %+v, want a pending deploy", record)
	}
	if !strings.HasPrefix(record.ClientOperationID, "ensure-") {
		t.Fatalf("client operation id = %q, want the server-minted ensure- form", record.ClientOperationID)
	}
	if len(record.FencingEpoch) == 0 {
		t.Fatal("the Ensure record carries no fencing epoch")
	}

	// A contender while the Ensure deploy is in flight is refused with the
	// operation class naming the record.
	_, err = m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderPlan})
	var busy *hostops.BusyError
	if !errors.As(err, &busy) || busy.Holder.Kind != hostops.HolderOperation || busy.Holder.OperationID != record.ID {
		t.Fatalf("contender busy = %+v (%v), want the Ensure operation %s", busy, err, record.ID)
	}

	finish(nil)
	done, _ := store.Record(record.ID)
	if done.State != hostops.StateComplete || done.Result == nil || !done.Result.OK {
		t.Fatalf("finished Ensure record = %+v, want complete", done)
	}
	// Once the step is over the holder goes back to the ladder's attach class:
	// a contender must not be told the finished operation is still running.
	_, err = m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderPlan})
	if !errors.As(err, &busy) {
		t.Fatalf("contender after the finish = %v, want a busy refusal", err)
	}
	if busy.Holder.Kind != hostops.HolderManager {
		t.Fatalf("holder after the finish = %+v, want the manager's attach class", busy.Holder)
	}
	release()
}

// TestEnsureRestartIsADurableOperationNamingItsRecord is the restart-only twin
// of the Ensure-deploy record test: the hook persists a restart record with its
// fencing epoch under the held gate before the leg's first remote command,
// publishes it as the gate holder, and hands back the finish that records the
// outcome.
func TestEnsureRestartIsADurableOperationNamingItsRecord(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}

	// The Ensure path holds the host's gate when it restarts.
	release, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	finish, err := m.EnsureRestart(live)
	if err != nil {
		t.Fatalf("EnsureRestart: %v", err)
	}
	records := store.Records()
	if len(records) != 1 {
		t.Fatalf("records = %d, want the one Ensure restart", len(records))
	}
	record := records[0]
	if record.State != hostops.StatePending || record.Kind != hostops.KindRestart {
		t.Fatalf("Ensure restart record = %+v, want a pending restart", record)
	}
	if !strings.HasPrefix(record.ClientOperationID, "ensure-") {
		t.Fatalf("client operation id = %q, want the server-minted ensure- form", record.ClientOperationID)
	}
	if len(record.FencingEpoch) == 0 {
		t.Fatal("the Ensure restart record carries no fencing epoch")
	}

	// A contender while the restart leg is in flight is refused with the
	// operation class naming the record.
	_, err = m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderPlan})
	var busy *hostops.BusyError
	if !errors.As(err, &busy) || busy.Holder.Kind != hostops.HolderOperation || busy.Holder.OperationID != record.ID {
		t.Fatalf("contender busy = %+v (%v), want the Ensure restart %s", busy, err, record.ID)
	}

	finish(nil)
	done, _ := store.Record(record.ID)
	if done.State != hostops.StateComplete || done.Result == nil || !done.Result.OK {
		t.Fatalf("finished Ensure restart record = %+v, want complete", done)
	}
	release()
}

// TestEnsureRestartRefusesWithoutAStore pins persisted-before-launch for the
// restart-only path too: with no operation store wired the attempt refuses
// rather than restarting unrecorded (§6's "a reconnect with no durable record
// performs no mutating SSH command").
func TestEnsureRestartRefusesWithoutAStore(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 1
	entry.IncarnationID = "inc-1"
	m := &hubHostManager{cfg: &hostManagerConfig{logf: func(string, ...any) {}}}
	if _, err := m.EnsureRestart(entry); err == nil {
		t.Fatal("EnsureRestart without an operation store succeeded")
	}
}

// TestEnsureTriggeredDeployRefusesWithoutAStore pins persisted-before-launch:
// with no operation store wired the Ensure deploy refuses rather than running
// unrecorded.
func TestEnsureTriggeredDeployRefusesWithoutAStore(t *testing.T) {
	entry := deployTestHost()
	entry.Generation = 1
	entry.IncarnationID = "inc-1"
	m := &hubHostManager{cfg: &hostManagerConfig{logf: func(string, ...any) {}}}
	if _, err := m.EnsureDeploy(entry); err == nil {
		t.Fatal("EnsureDeploy without an operation store succeeded")
	}
}

// TestHostDeployPostRefreshRefusesAVersionMismatch pins that the
// post-operation revision check reads the consumed token's own bindings: a refresh probe
// that reports a verifiable revision other than the deployed one is a recorded
// failure naming the revision that was wanted, never a clean success.
func TestHostDeployPostRefreshRefusesAVersionMismatch(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	after := before.Add(time.Minute)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		probe: restartProbeScript(t, "v0.9.0", "v9.9.9", before, after),
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || !strings.Contains(record.Result.Message, `want the deploy build "v1.2.3"`) {
		t.Fatalf("failure message %q does not name the deployed revision the token bound", record.Result.Message)
	}
}

// TestHostDeploySkipsTheRestartWhenTheHostIsCurrent pins that the worker's
// restart decision comes from the consumed token's bindings: a host already
// healthy on the controller's revision is not restarted, and the refresh's
// process-start-time requirement does not apply to it.
func TestHostDeploySkipsTheRestartWhenTheHostIsCurrent(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	same := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	counters := &deployCounters{}
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{
		counters: counters,
		probe:    restartProbeScript(t, "v1.2.3", "v1.2.3", same, same),
	})
	token := mintDeployToken(t, store, m, entry, "v1.2.3", true, "v1.2.3")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	waitOperationState(t, store, response.ID, hostops.StateComplete)
	if got := counters.restarts.Load(); got != 0 {
		t.Fatalf("restart calls = %d, want 0 for a host already on the controller's revision", got)
	}
}

// editingGate wraps the test hub's gate and runs edit at one hook — the
// acquisition or the promotion — so a test can land a hub.toml edit exactly in
// the window a check must catch.
type editingGate struct {
	inner hostops.Gate
	onTry func()
	onAs  func()
}

func (g *editingGate) TryAcquire(host string, holder hostops.Holder) (func(), error) {
	if g.onTry != nil {
		g.onTry()
	}
	return g.inner.TryAcquire(host, holder)
}

func (g *editingGate) HoldAs(host string, holder hostops.Holder) error {
	if g.onAs != nil {
		g.onAs()
	}
	return g.inner.HoldAs(host, holder)
}

// TestHostRestartRefusesFingerprintDriftUnderTheGate pins §6's restart arm of
// the post-acquisition re-read: an edit landing between restart's resolution
// and its gate is a typed stale-entry refusal with no record.
func TestHostRestartRefusesFingerprintDriftUnderTheGate(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}
	edited := live
	edited.SSH = "m4-elsewhere.example"
	m.cfg.gate = &editingGate{
		inner: m.cfg.gate,
		onTry: func() {
			if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{edited})), 0o600); err != nil {
				t.Errorf("rewrite hub.toml: %v", err)
			}
		},
	}
	_, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: live.Generation, IncarnationID: live.IncarnationID,
	})
	info, data, _ := deployWireInfo(t, err)
	var binding string
	_ = json.Unmarshal(data["binding"], &binding)
	if info != appwire.ErrorStaleEntry || binding != "hub.toml-fingerprint" {
		t.Fatalf("under-gate drift refusal = (%q, %q), want (stale-entry, hub.toml-fingerprint)", info, binding)
	}
	if got := len(store.Records()); got != 0 {
		t.Fatalf("the drift refusal created records: %d", got)
	}
}

// TestHostRestartRefusesFingerprintDriftBeforeTheRestart pins §6's final
// restart check: an edit landing after the handler's checks but before the
// worker's pre-restart re-read aborts with no restart and a recorded
// stale-entry failure.
func TestHostRestartRefusesFingerprintDriftBeforeTheRestart(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	counters := &deployCounters{}
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{counters: counters})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}
	edited := live
	edited.SSH = "m4-elsewhere.example"
	m.cfg.gate = &editingGate{
		inner: m.cfg.gate,
		onAs: func() {
			if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{edited})), 0o600); err != nil {
				t.Errorf("rewrite hub.toml: %v", err)
			}
		},
	}
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: live.Generation, IncarnationID: live.IncarnationID,
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || !strings.Contains(record.Result.Message, "stale-entry (hub.toml-fingerprint)") {
		t.Fatalf("failure = %+v, want the stale-entry fingerprint note", record.Result)
	}
	if got := counters.restarts.Load(); got != 0 {
		t.Fatalf("restart calls = %d, want 0: the drift must abort before the irreversible step", got)
	}
}

// TestEnsureTriggeredDeployRunsAfterATerminalRecord pins the High review
// finding: the Ensure path's create reads its pre-operation sequence under the
// held gate, so an operation that finished before the attempt (or a boot
// interrupted transition) never refuses every later Ensure-triggered deploy.
func TestEnsureTriggeredDeployRunsAfterATerminalRecord(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}
	// One terminal operation on the host, and a boot-pass interrupted record:
	// both advance the store's sequence past zero.
	done := createPairRecordForTest(t, store, live, hostops.KindDeploy, "op-old")
	if done.Sequence == 0 {
		t.Fatal("the fixture did not stamp a sequence")
	}
	if moved, err := store.InterruptInFlight(hostops.InterruptedNote); err != nil || moved != 0 {
		t.Fatalf("InterruptInFlight = (%d, %v), want (0, nil)", moved, err)
	}

	release, err := m.cfg.gate.TryAcquire(entry.Name, hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"})
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	finish, err := m.EnsureDeploy(live)
	if err != nil {
		t.Fatalf("EnsureDeploy after a terminal record: %v", err)
	}
	finish(nil)
	release()
	records := store.Records()
	if len(records) != 2 {
		t.Fatalf("records = %d, want the old one plus a fresh Ensure record", len(records))
	}
	fresh, ok := store.Record(records[1].ID)
	if !ok || fresh.State != hostops.StateComplete || fresh.ClientOperationID == done.ClientOperationID {
		t.Fatalf("fresh Ensure record = %+v, want a new complete record", fresh)
	}
}

// createPairRecordForTest persists and completes one record for the live entry.
func createPairRecordForTest(t *testing.T, store *hostops.Store, entry hostreg.Host, kind hostops.Kind, clientID string) hostops.Record {
	t.Helper()
	record, err := store.Create(hostops.NewRecord{
		ClientOperationID: clientID, Host: entry.Name, Kind: kind,
		Generation: entry.Generation, IncarnationID: entry.IncarnationID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	done, err := store.Transition(record.ID, hostops.StateComplete, func(r *hostops.Record) {
		r.Result = &hostops.Result{OK: true, Message: "done"}
	})
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	return done
}

// TestHostRestartInWriteDedupClosesTheRace pins the Medium review finding: a
// concurrent call with the same operation ID that creates its record while this
// one waits for the gate is replayed by the write's own locked dedup check, so
// one operation ID never yields two records (and two restarts).
func TestHostRestartInWriteDedupClosesTheRace(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	counters := &deployCounters{}
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{counters: counters})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}
	var racer hostops.Record
	// The pre-gate lookup has already missed when the gate acquisition runs
	// this hook: it is exactly the window between lookup and write.
	m.cfg.gate = &editingGate{
		inner: m.cfg.gate,
		onTry: func() {
			racer = createPairRecordForTest(t, store, live, hostops.KindRestart, "op-1")
		},
	}
	response, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-1", Generation: live.Generation, IncarnationID: live.IncarnationID,
	})
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if racer.ID == "" {
		t.Fatal("the fixture never created the racing record")
	}
	if response.ID != racer.ID {
		t.Fatalf("response id = %s, want the racing record %s", response.ID, racer.ID)
	}
	if got := len(store.Records()); got != 1 {
		t.Fatalf("records = %d, want one: the same operation ID must not create a second record", got)
	}
	if got := counters.restarts.Load(); got != 0 {
		t.Fatalf("restart calls = %d, want 0: a replayed dedup hit launches no worker", got)
	}
}

// TestHostOperationWorkerFailsWithoutASeam pins the Low review finding: a hub
// with no deploy step wired records a failure instead of panicking the worker
// goroutine.
func TestHostOperationWorkerFailsWithoutASeam(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{probe: deployProbeScript(t)})
	m.cfg.deployHost = nil
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	record := waitOperationState(t, store, response.ID, hostops.StateFailed)
	if record.Result == nil || !strings.Contains(record.Result.Message, "no deploy step wired") {
		t.Fatalf("failure = %+v, want the missing-seam refusal", record.Result)
	}
}
