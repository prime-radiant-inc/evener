package hub

// evener/host/plan's per-host gate tests (deploy pipeline 08b §5, §6 step 2,
// §12's "Busy-holder classes", "Durable probe epoch", and "Post-acquisition
// entry re-read" rows): the plan try-acquires THE gate after its ungated
// refresh and holds it across the epoch persist, the probe, the re-checks and
// the mint; a held gate fails fast with the typed busy error naming its
// holder; and no refusal leaves a token or a probe epoch behind.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// planTestGate returns the test hub's per-host gate, failing when the fixture
// wired something other than the standalone gate.
func planTestGate(t *testing.T, m *hubHostManager) *hostops.ProcessGate {
	t.Helper()
	gate, ok := m.cfg.gate.(*hostops.ProcessGate)
	if !ok {
		t.Fatalf("the test hub's gate is %T, want *hostops.ProcessGate", m.cfg.gate)
	}
	return gate
}

// wantBusy fails the test unless err is a busy refusal of the expected holder
// class, and returns its wire data.
func wantBusy(t *testing.T, err error, kind hostops.HolderKind, operationID string) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T (%v), want an appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeConflict {
		t.Fatalf("busy code = %d, want the conflict class %d", wire.Code, appwire.CodeConflict)
	}
	switch kind {
	case hostops.HolderOperation:
		data, ok := wire.Data.(appwire.HostBusyOperationErrorData)
		if !ok {
			t.Fatalf("busy data = %T, want HostBusyOperationErrorData", wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorHostBusyOperation || data.OperationID != operationID {
			t.Fatalf("busy data = %+v, want %q naming %q", data, appwire.ErrorHostBusyOperation, operationID)
		}
	default:
		data, ok := wire.Data.(appwire.ErrorData)
		if !ok {
			t.Fatalf("busy data = %T, want ErrorData", wire.Data)
		}
		if data.EvenerErrorInfo != appwire.ErrorHostBusyTransient {
			t.Fatalf("busy discriminator = %q, want %q", data.EvenerErrorInfo, appwire.ErrorHostBusyTransient)
		}
	}
}

// TestHostPlanRefusesAHeldGate pins §5's busy classes through the handler: a
// deploy operation's held gate names the operation and its record id; a plan's
// own window and the manager's attach work render the typed transient form
// with no operation reference. In every case the plan probes nothing, mints
// nothing, and leaves no probe epoch behind.
func TestHostPlanRefusesAHeldGate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	cases := []struct {
		name        string
		holder      hostops.Holder
		kind        hostops.HolderKind
		operationID string
	}{
		{
			name:        "an operation-held gate names the operation",
			holder:      hostops.Holder{Kind: hostops.HolderOperation, OperationID: "00000000000000000042"},
			kind:        hostops.HolderOperation,
			operationID: "00000000000000000042",
		},
		{
			name:   "a plan-held gate renders the transient form",
			holder: hostops.Holder{Kind: hostops.HolderPlan},
			kind:   hostops.HolderPlan,
		},
		{
			name:   "an attach-held gate renders the manager's transient form",
			holder: hostops.Holder{Kind: hostops.HolderManager, Activity: "attach"},
			kind:   hostops.HolderManager,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			m, store, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{
				probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
					probes++
					return planTestProbe(), nil
				},
			})
			release, err := planTestGate(t, m).TryAcquire("m4", tc.holder)
			if err != nil {
				t.Fatalf("holding the gate: %v", err)
			}
			defer release()
			err = errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"}))
			wantBusy(t, err, tc.kind, tc.operationID)
			if probes != 0 {
				t.Fatal("a plan against a held gate probed the host")
			}
			if _, ok := store.OutstandingToken("m4"); ok {
				t.Fatal("a plan against a held gate minted a token")
			}
			if _, ok := store.ProbeEpoch("m4"); ok {
				t.Fatal("a plan against a held gate persisted a probe epoch")
			}
		})
	}
}

// TestHostPlanHoldsTheGateAcrossProbeAndMint pins the hold's span: while the
// probe runs, the gate is held (a second try-acquire refuses with the plan
// holder), and by the time the plan returns the gate is free again — the hold
// covers the probe window, the mint, and the return, and nothing longer.
func TestHostPlanHoldsTheGateAcrossProbeAndMint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	var m *hubHostManager
	heldDuringProbe := false
	seams := planSeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			_, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan})
			var busy *hostops.BusyError
			if !errors.As(err, &busy) {
				t.Fatalf("the gate was free during the probe: try-acquire = %v", err)
			}
			if busy.Holder.Kind != hostops.HolderPlan {
				t.Fatalf("the gate's holder during the probe = %+v, want the plan", busy.Holder)
			}
			heldDuringProbe = true
			return planTestProbe(), nil
		},
	}
	m, _, _ = planTestManager(t, configPath, []hostreg.Host{planTestHost()}, seams)
	result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil || result.HostPlanPlanned == nil {
		t.Fatalf("Plan = %+v/%v, want the planned arm", result, err)
	}
	if !heldDuringProbe {
		t.Fatal("the probe never ran")
	}
	release, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("the gate was still held after the plan returned: %v", err)
	}
	release()
}

// TestHostPlanPersistsTheProbeEpochBeforeProbing pins §6 step 2's durable
// epoch: the row exists before the probe call, it is bound to the host's
// current (generation, incarnation id) pair, the wire presents exactly that
// epoch, and the planned arm's mint supersedes the row.
func TestHostPlanPersistsTheProbeEpochBeforeProbing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	var m *hubHostManager
	var store *hostops.Store
	var registry *hostreg.Registry
	seams := planSeams{
		probe: func(_ context.Context, host hostreg.Host, _ *appwire.Client, presented appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			live, ok := registry.Get(host.Name)
			if !ok {
				t.Fatal("the host vanished from the registry")
			}
			row, ok := store.ProbeEpoch(host.Name)
			if !ok {
				t.Fatal("the probe ran before any durable probe epoch was persisted")
			}
			if row.Generation != live.Generation || row.IncarnationID != live.IncarnationID {
				t.Fatalf("the epoch is bound to %d/%q, want the registry's %d/%q", row.Generation, row.IncarnationID, live.Generation, live.IncarnationID)
			}
			if presented.BootID != row.BootID || presented.OpSeq != row.OpSeq {
				t.Fatalf("the probe presented %+v, want the persisted epoch %+v", presented, row)
			}
			if presented.BootID == "" || presented.OpSeq == 0 {
				t.Fatal("the probe presented a defaulted epoch")
			}
			return planTestProbe(), nil
		},
	}
	m, store, registry = planTestManager(t, configPath, []hostreg.Host{planTestHost()}, seams)
	result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil || result.HostPlanPlanned == nil {
		t.Fatalf("Plan = %+v/%v, want the planned arm", result, err)
	}
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("the mint did not supersede the plan's probe epoch")
	}
}

// TestHostPlanDropsTheProbeEpochOnRefusal pins §6 step 2's failure path: a
// plan that does not mint deletes its epoch, so no epoch-only row outlives its
// call.
func TestHostPlanDropsTheProbeEpochOnRefusal(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m, store, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			return hubcore.HostRuntimeProbe{}, errors.New("probe read timed out")
		},
	})
	result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	planNoTokenReasonOf(t, result, appwire.HostPlanReasonProbeFailed, false)
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("a refused plan left its probe epoch behind")
	}
}

// TestHostPlanRefusesAMoveBetweenRefreshAndGate pins §5's post-acquisition
// re-read through the handler: a mutation landing in the ungated refresh window
// refuses with typed stale-entry and mints nothing, because the under-gate
// re-read sees the moved registration before any epoch or probe.
func TestHostPlanRefusesAMoveBetweenRefreshAndGate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	probes := 0
	m, store, registry := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			probes++
			return planTestProbe(), nil
		},
	})
	m.cfg.planFacts = func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
		edited := host
		edited.SSH = "m4.moved.example"
		if err := registry.Update(edited); err != nil {
			return hubcore.HostPlanFacts{}, fmt.Errorf("registry.Update: %w", err)
		}
		return planTestFacts(host), nil
	}
	err := errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"}))
	if data := wantStaleEntry(t, err); data.Binding != appwire.StaleEntryBindingGeneration {
		t.Fatalf("binding = %q, want %q", data.Binding, appwire.StaleEntryBindingGeneration)
	}
	if probes != 0 {
		t.Fatal("a plan whose entry moved under it probed the host")
	}
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("a plan whose entry moved under it persisted a probe epoch")
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a plan whose entry moved under it minted a token")
	}
	if !strings.Contains(err.Error(), "registration moved") {
		t.Fatalf("refusal %q does not name the moved registration", err)
	}
}

// TestHostPlanRefreshesUngatedThenAcquiresTheGate pins §6 step 2's order from
// the plan's own side: the facts refresh runs with the gate free, and the probe
// runs with the gate held — the ungated refresh is what makes the later
// acquisition meaningful, and holding the gate across the SSH refresh would
// reintroduce the slow-probe busy-refusal bug §10 names.
func TestHostPlanRefreshesUngatedThenAcquiresTheGate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	var m *hubHostManager
	refreshedUngated := false
	seams := planSeams{
		facts: func(_ context.Context, host hostreg.Host) (hubcore.HostPlanFacts, error) {
			release, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan})
			if err != nil {
				t.Fatalf("the gate was held during the ungated refresh: %v", err)
			}
			release()
			refreshedUngated = true
			return planTestFacts(host), nil
		},
		probe: func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			if _, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan}); err == nil {
				t.Fatal("the gate was free during the probe; the plan must hold it across the probe window")
			}
			return planTestProbe(), nil
		},
	}
	m, _, _ = planTestManager(t, configPath, []hostreg.Host{planTestHost()}, seams)
	result, err := m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"})
	if err != nil || result.HostPlanPlanned == nil {
		t.Fatalf("Plan = %+v/%v, want the planned arm", result, err)
	}
	if !refreshedUngated {
		t.Fatal("the facts refresh never ran")
	}
}

// TestHostPlanProbeTimeoutLeavesNothingHeld pins §6's timeout arm: a probe
// that never answers past the plan's deadline returns the no-token
// `probe-failed` refusal, and by then nothing is left held — the gate is free
// and the probe epoch is gone.
func TestHostPlanProbeTimeoutLeavesNothingHeld(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	var m *hubHostManager
	probeEntered := make(chan struct{})
	seams := planSeams{
		probe: func(ctx context.Context, _ hostreg.Host, _ *appwire.Client, _ appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
			close(probeEntered)
			// A hung remote: the probe returns only when the plan's own context
			// ends, exactly as the production primitive's deadline does.
			<-ctx.Done()
			return hubcore.HostRuntimeProbe{}, ctx.Err()
		},
	}
	var store *hostops.Store
	m, store, _ = planTestManager(t, configPath, []hostreg.Host{planTestHost()}, seams)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	done := make(chan appwire.HostPlanResult, 1)
	go func() {
		result, _ := m.Plan(ctx, appwire.HostPlanParams{Name: "m4"})
		done <- result
	}()
	<-probeEntered
	// While the probe hangs, the plan holds the gate: a contender fails fast.
	if _, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan}); err == nil {
		t.Fatal("the gate was free while the probe hung")
	}
	result := <-done
	planNoTokenReasonOf(t, result, appwire.HostPlanReasonProbeFailed, false)
	// Nothing is left held past the probe window.
	release, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderPlan})
	if err != nil {
		t.Fatalf("the gate was still held after the timed-out plan returned: %v", err)
	}
	release()
	if _, ok := store.ProbeEpoch("m4"); ok {
		t.Fatal("a timed-out plan left its probe epoch behind")
	}
	if _, ok := store.OutstandingToken("m4"); ok {
		t.Fatal("a timed-out plan minted a token")
	}
}

// TestHostPlanRefusesAnOperationHolderWithoutARecordID pins §11's operation
// class's fallback: an operation holder that carries no record id cannot name
// an open/wait-able reference, so the refusal is the typed transient form —
// never a `host-busy-operation` with an empty operationId a client could not
// resolve.
func TestHostPlanRefusesAnOperationHolderWithoutARecordID(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(configPath, []byte(planTestHubTOML([]hostreg.Host{planTestHost()})), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	m, _, _ := planTestManager(t, configPath, []hostreg.Host{planTestHost()}, planSeams{})
	release, err := planTestGate(t, m).TryAcquire("m4", hostops.Holder{Kind: hostops.HolderOperation})
	if err != nil {
		t.Fatalf("holding the gate: %v", err)
	}
	defer release()
	err = errOf(m.Plan(context.Background(), appwire.HostPlanParams{Name: "m4"}))
	wantBusy(t, err, hostops.HolderPlan, "")
}
