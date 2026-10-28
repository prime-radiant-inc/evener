package hub

// evener/host/deploy and evener/host/restart refusal arms: the typed §11
// envelopes each malformed request, drifted binding and missing seam answers
// with, asserted by their machine discriminators — never by prose. The happy
// paths and the worker lifetime live in app_host_deploy_test.go; the rows here
// drive the manager fixture's seams (deployTestHub) with real hostops state.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// refusalInfo extracts a refused handler call's typed discriminator.
func refusalInfo(t *testing.T, err error) appwire.ErrorInfo {
	t.Helper()
	info, _, _ := deployWireInfo(t, err)
	return info
}

// refusalBinding extracts a stale-entry refusal's binding half.
func refusalBinding(t *testing.T, err error) appwire.StaleEntryBinding {
	t.Helper()
	_, data, _ := deployWireInfo(t, err)
	raw, ok := data["binding"]
	if !ok {
		t.Fatalf("the refusal carries no binding: %v", data)
	}
	var binding appwire.StaleEntryBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatalf("the refusal's binding %s does not decode: %v", raw, err)
	}
	return binding
}

// deployRefusalFixture is one deploy refusal row's hub.
func deployRefusalFixture(t *testing.T, seams deploySeams) (*hubHostManager, *hostops.Store, hostreg.Host, string) {
	t.Helper()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, seams)
	return m, store, entry, configPath
}

func TestDeployRejectsMalformedParams(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, _, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	ctx := context.Background()

	if _, err := m.Deploy(ctx, appwire.HostDeployParams{Token: "tok", OperationID: "op-x"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a deploy with no host name = %v, want invalid params", err)
	}
	if _, err := m.Deploy(ctx, appwire.HostDeployParams{Name: entry.Name, OperationID: "op-x"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a deploy with no token = %v, want invalid params", err)
	}
	if _, err := m.Deploy(ctx, appwire.HostDeployParams{Name: entry.Name, Token: "tok"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a deploy with no client operation id = %v, want invalid params", err)
	}
	if _, err := m.Deploy(ctx, appwire.HostDeployParams{
		Name: entry.Name, Token: "tok", OperationID: strings.Repeat("x", hostops.MaxClientOperationIDBytes+1),
	}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("an oversized client operation id = %v, want invalid params", err)
	}
	if _, err := m.Deploy(ctx, appwire.HostDeployParams{
		Name: "ghost", Token: "tok", OperationID: "op-x",
	}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a deploy for an unknown host = %v, want invalid params", err)
	}
}

func TestRestartRejectsMalformedParams(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, _, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	ctx := context.Background()

	if _, err := m.Restart(ctx, appwire.HostRestartParams{OperationID: "op-x", Generation: 3, IncarnationID: "inc"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a restart with no host name = %v, want invalid params", err)
	}
	if _, err := m.Restart(ctx, appwire.HostRestartParams{Name: entry.Name, Generation: 3, IncarnationID: "inc"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a restart with no client operation id = %v, want invalid params", err)
	}
	if _, err := m.Restart(ctx, appwire.HostRestartParams{Name: entry.Name, OperationID: "op-x"}); refusalInfo(t, err) != appwire.ErrorInvalidParams {
		t.Fatalf("a restart with no intended pair = %v, want invalid params", err)
	}
}

func TestRestartRefusedWhenHubTOMLDoesNotFingerprint(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, _, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	if err := os.WriteFile(configPath, []byte("not a toml file [[["), 0o600); err != nil {
		t.Fatalf("break hub.toml: %v", err)
	}
	_, err := m.Restart(context.Background(), appwire.HostRestartParams{
		Name: entry.Name, OperationID: "op-x", Generation: 3, IncarnationID: "inc",
	})
	if refusalInfo(t, err) != appwire.ErrorInternal {
		t.Fatalf("a restart over a hub.toml that does not validate = %v, want an internal refusal", err)
	}
}

func TestDeployRefusedWithoutOperationStore(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, _, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	m.cfg.ops = nil
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: "tok", OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorInternal {
		t.Fatalf("a deploy with no operation store = %v, want an internal refusal", err)
	}
}

func TestDeployRefusedWhenChannelDetachedEarly(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{
		clientAttached: func(string) (*appwire.Client, bool) { return nil, false },
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorHostDetached {
		t.Fatalf("a deploy over a detached channel = %v, want host-detached", err)
	}
}

func TestDeployRefusedWhenAttachedFactsVanish(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{})
	m.cfg.attachedFacts = nil
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorHostDetached {
		t.Fatalf("a deploy whose attached facts vanished = %v, want host-detached", err)
	}
}

func TestDeployRefusedWhenNoProbePrimitive(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{})
	m.cfg.planProbe = nil
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorProbeFailed {
		t.Fatalf("a deploy with no running-probe primitive = %v, want probe-failed", err)
	}
}

func TestDeployRefusedWhenProbeFails(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{}, errors.New("the probe could not read the running state")
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorProbeFailed {
		t.Fatalf("a deploy whose probe failed = %v, want probe-failed", err)
	}
}

func TestDeployRefusedWhenHubTOMLDriftsUnderGate(t *testing.T) {
	t.Parallel()
	m, store, entry, configPath := deployRefusalFixture(t, deploySeams{})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	if err := os.WriteFile(configPath, []byte("not a toml file [[["), 0o600); err != nil {
		t.Fatalf("break hub.toml: %v", err)
	}
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorInternal {
		t.Fatalf("a deploy over a hub.toml that stopped validating = %v, want an internal refusal", err)
	}
}

func TestDeployRefusedWhenTokenSupersededUnderGate(t *testing.T) {
	t.Parallel()
	var store *hostops.Store
	var m *hubHostManager
	var entry hostreg.Host
	m, store, entry, _ = deployRefusalFixture(t, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			// A concurrent plan supersedes the token while the probe runs.
			mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
			return hubcore.HostRuntimeProbe{Version: "v0.9.0", RunningHealthy: true}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorTokenSuperseded {
		t.Fatalf("a deploy whose token was superseded under the gate = %v, want token-superseded", err)
	}
}

func TestDeployRefusedWhenTokensTargetNoLongerResolves(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{})
	token := mintDeployTokenWithTarget(t, store, m, entry, "/a/target/the/host/no/longer/resolves")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorStaleEntry {
		t.Fatalf("a deploy whose target resolved elsewhere = %v, want stale-entry", err)
	}
	if binding := refusalBinding(t, err); binding != appwire.StaleEntryBindingTarget {
		t.Fatalf("the stale-entry binding = %q, want the target half", binding)
	}
}

func TestDeployRefusedWhenRunningVersionDrifts(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{Version: "v9.9.9", RunningHealthy: true}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorStaleEntry {
		t.Fatalf("a deploy whose running version drifted = %v, want stale-entry", err)
	}
	if binding := refusalBinding(t, err); binding != appwire.StaleEntryBindingRunningVersion {
		t.Fatalf("the stale-entry binding = %q, want the running-version half", binding)
	}
}

func TestDeployRefusedWhenATerminalOperationLandsDuringTheProbe(t *testing.T) {
	t.Parallel()
	var store *hostops.Store
	var m *hubHostManager
	var entry hostreg.Host
	var terminalLanded atomic.Bool
	m, store, entry, _ = deployRefusalFixture(t, deploySeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			if terminalLanded.CompareAndSwap(false, true) {
				live, _ := m.liveHost(entry.Name)
				outcome, err := store.CreateOperation(hostops.OperationCreateRequest{
					ClientOperationID: "op-concurrent", Host: entry.Name, Kind: hostops.KindRestart,
					Pair:   hostops.OperationPair{Generation: live.Generation, IncarnationID: live.IncarnationID},
					BootID: "test-boot",
				})
				if err != nil {
					return hubcore.HostRuntimeProbe{}, err
				}
				if _, err := store.TransitionToState(outcome.Record.ID, hostops.StateRunning, nil, ""); err != nil {
					return hubcore.HostRuntimeProbe{}, err
				}
				if _, err := store.TransitionToState(outcome.Record.ID, hostops.StateComplete, &hostops.Result{OK: true, Message: "done"}, ""); err != nil {
					return hubcore.HostRuntimeProbe{}, err
				}
			}
			return hubcore.HostRuntimeProbe{Version: "v0.9.0", RunningHealthy: true}, nil
		},
	})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-x",
	})
	if refusalInfo(t, err) != appwire.ErrorStaleEntry {
		t.Fatalf("a deploy whose host finished another operation = %v, want stale-entry", err)
	}
	if binding := refusalBinding(t, err); binding != appwire.StaleEntryBindingConcurrentTerminalOp {
		t.Fatalf("the stale-entry binding = %q, want the concurrent-terminal-op half", binding)
	}
}

func TestDeployRefusedForConflictingOperationID(t *testing.T) {
	t.Parallel()
	m, store, entry, _ := deployRefusalFixture(t, deploySeams{})
	live, _ := m.liveHost(entry.Name)
	if _, err := store.CreateOperation(hostops.OperationCreateRequest{
		ClientOperationID: "op-dup", Host: entry.Name, Kind: hostops.KindRestart,
		Pair:   hostops.OperationPair{Generation: live.Generation, IncarnationID: live.IncarnationID},
		BootID: "test-boot",
	}); err != nil {
		t.Fatalf("seed the colliding restart record: %v", err)
	}
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-dup",
	})
	if refusalInfo(t, err) != appwire.ErrorConflictingOperationID {
		t.Fatalf("a deploy reusing a live operation id of another kind = %v, want conflicting-operation-id", err)
	}
}

func TestShutdownHostOperationsWithoutWorkers(t *testing.T) {
	t.Parallel()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, _, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	if moved := m.ShutdownHostOperations(time.Second); moved != 0 {
		t.Fatalf("a hub that never ran an operation interrupted %d records", moved)
	}
	_ = m.operationContext()
	if moved := m.ShutdownHostOperations(0); moved != 0 {
		t.Fatalf("a shutdown over an empty store interrupted %d records", moved)
	}
}

// mintDeployTokenWithTarget mints a token bound to the target path the caller
// names, so a row can pin the under-gate target re-resolution refusal.
func mintDeployTokenWithTarget(t *testing.T, store *hostops.Store, m *hubHostManager, entry hostreg.Host, target string) hostops.Token {
	t.Helper()
	if live, ok := m.liveHost(entry.Name); ok {
		entry = live
	}
	fingerprint, ok := m.hostTOMLFingerprint(entry.Name)
	if !ok {
		t.Fatal("the test hub.toml does not fingerprint")
	}
	token, err := store.MintTokenIfQuiescent(hostops.MintRequest{
		Host:               entry.Name,
		Generation:         entry.Generation,
		IncarnationID:      entry.IncarnationID,
		EntryHash:          hostEntryFingerprint(entry),
		HubTOMLFingerprint: fingerprint,
		FactsRevision:      testEntryFactDigest("facts"),
		FactsCapturedAt:    time.Now().UTC(),
		TargetPath:         target,
		ControllerRevision: "v1.2.3",
		RunningVersion:     "v0.9.0",
		RunningHealthy:     true,
		FreshnessBound:     hostops.DefaultFreshnessBound,
		TTL:                hostops.DefaultTokenTTL,
	}, store.Sequence())
	if err != nil {
		t.Fatalf("MintTokenIfQuiescent: %v", err)
	}
	return token
}

func TestOperationWireStateCoversTheClosedSet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state hostops.State
		want  appwire.OperationState
	}{
		{hostops.StatePending, appwire.OperationStatePending},
		{hostops.StateRunning, appwire.OperationStateRunning},
		{hostops.StateComplete, appwire.OperationStateComplete},
		{hostops.StateFailed, appwire.OperationStateFailed},
		{hostops.StateInterrupted, appwire.OperationStateInterrupted},
		{hostops.StateOrphanUnverified, appwire.OperationStateOrphanUnverified},
	}
	for _, tc := range cases {
		got, err := operationWireState(tc.state)
		if err != nil || got != tc.want {
			t.Fatalf("operationWireState(%q) = %q, %v; want %q", tc.state, got, err, tc.want)
		}
	}
	if _, err := operationWireState(hostops.State("nonsense")); err == nil {
		t.Fatal("a state outside the wire's set mapped onto the wire")
	}
}

func TestEpochOfReadsAMintedRecordAndRefusesAnUnboundOne(t *testing.T) {
	t.Parallel()
	unbound := hostops.Record{}
	if epoch := epochOf(unbound); epoch.BootID != "" || epoch.OpSeq != 0 {
		t.Fatalf("epochOf of an unbound record = %+v, want the zero epoch", epoch)
	}
	bound := hostops.Record{FencingEpoch: json.RawMessage(`{"bootId":"boot-1","opSeq":4}`)}
	epoch := epochOf(bound)
	if epoch.BootID != "boot-1" || epoch.OpSeq != 4 {
		t.Fatalf("epochOf of a minted record = %+v, want boot-1 at op sequence 4", epoch)
	}
}
