package hub

// Exit characterization for the host operation worker. runOperationWorker
// carries one deploy flow and one restart flow, each with several early exits,
// and every exit has to leave the same things behind: the record in the right
// terminal state, the seams called in the right order, the supervisor handoff
// of any channel the operation published run exactly once, and the host gate
// released. Each scenario drives one path through the real Deploy or Restart
// handler and reads those back.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

// exitTrace records the seam calls of one scenario in order.
type exitTrace struct {
	mu     sync.Mutex
	events []string
}

func (e *exitTrace) add(event string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, event)
}

func (e *exitTrace) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.events)
}

// exitWant is what a scenario must leave behind. progress is the number of
// progress lines the record carries.
type exitWant struct {
	state    hostops.State
	ok       bool
	trace    []string
	progress int
}

// exitSeams builds the seams of one scenario over trace. attachFailures maps an
// attach call number to the error that call returns (with no handoff, as a
// failing attach that published nothing does);
// handoffFails names the attach call numbers whose handoff cannot start the
// supervisor.
type exitSeams struct {
	trace          *exitTrace
	attachFailures map[int]error
	handoffFails   map[int]bool
	deployErr      error
	restartErr     error
	// afterDeploy and afterRestart run after the seam's trace event.
	afterDeploy  func()
	afterRestart func()
	attachedAt   func() bool
}

func (s exitSeams) build(t *testing.T) deploySeams {
	attachCalls := 0
	var mu sync.Mutex
	return deploySeams{
		deploy: func(_ context.Context, host hostreg.Host, f sshconn.Preflight) (string, sshconn.Preflight, error) {
			s.trace.add("deploy")
			if s.afterDeploy != nil {
				s.afterDeploy()
			}
			return deployTestTarget(t, host), f, s.deployErr
		},
		restart: func(context.Context, hostreg.Host, sshconn.Preflight) error {
			s.trace.add("restart")
			if s.afterRestart != nil {
				s.afterRestart()
			}
			return s.restartErr
		},
		clientAttached: func(string) (*appwire.Client, bool) {
			return &appwire.Client{}, s.attachedAt == nil || s.attachedAt()
		},
		attached: func(string) (sshconn.Preflight, bool) {
			return deployTestFacts(deployTestHost()), s.attachedAt == nil || s.attachedAt()
		},
		attachUnderGate: func(_ context.Context, _ hostreg.Host, _ hostops.Holder, explicit bool) (func() bool, error) {
			mu.Lock()
			attachCalls++
			n := attachCalls
			mu.Unlock()
			s.trace.add(fmt.Sprintf("attach%d(explicit=%t)", n, explicit))
			handoff := func() bool {
				s.trace.add(fmt.Sprintf("handoff%d", n))
				return !s.handoffFails[n]
			}
			if err := s.attachFailures[n]; err != nil {
				return nil, err
			}
			return handoff, nil
		},
	}
}

// exitHub is one scenario's manager plus how to start its operation.
type exitHub struct {
	m     *hubHostManager
	store *hostops.Store
	start func() (string, error)
}

// deployExitHub builds a hub whose Deploy runs the planned-restart flow (the
// token's running build differs from the controller's) unless current is set,
// in which case the host already runs the controller's build and no restart
// follows.
func deployExitHub(t *testing.T, seams deploySeams, current bool) exitHub {
	t.Helper()
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	seams.probe = restartProbeScript(t, "dev", "dev", before, before.Add(time.Minute))
	running := "dev"
	if current {
		running = "v1.2.3"
		seams.probe = restartProbeScript(t, "v1.2.3", "v1.2.3", before, before)
	}
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, seams)
	revision := "dev"
	if current {
		revision = running
	}
	token := mintDeployToken(t, store, m, entry, running, true, revision)
	return exitHub{m: m, store: store, start: func() (string, error) {
		response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
			Name: entry.Name, Token: token.Value, OperationID: "op-1",
		})
		return response.ID, err
	}}
}

// restartExitHub builds a hub whose Restart runs the restart flow.
func restartExitHub(t *testing.T, seams deploySeams) exitHub {
	t.Helper()
	entry := deployTestHost()
	entry.Generation = 2
	entry.IncarnationID = "inc-new"
	configPath := deployTestConfigPath(t, entry)
	before := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	seams.restartVersion = "dev"
	if seams.probe == nil {
		seams.probe = restartProbeScript(t, "dev", "dev", before, before.Add(time.Minute))
	}
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, seams)
	return exitHub{m: m, store: store, start: func() (string, error) {
		response, err := m.Restart(context.Background(), appwire.HostRestartParams{
			Name: entry.Name, OperationID: "op-1", Generation: 2, IncarnationID: "inc-new",
		})
		return response.ID, err
	}}
}

func rewriteHubTOML(t *testing.T, m *hubHostManager) {
	t.Helper()
	live, ok := m.cfg.hosts.Get("m4")
	if !ok {
		t.Error("the registry carries no entry")
		return
	}
	edited := live
	edited.SSH = "m4-elsewhere.example"
	if err := os.WriteFile(m.cfg.configPath, []byte(planTestHubTOML([]hostreg.Host{edited})), 0o600); err != nil {
		t.Errorf("rewrite hub.toml: %v", err)
	}
}

func cancelOperations(m *hubHostManager) {
	m.cfg.opsMu.Lock()
	cancel := m.cfg.opsCancel
	m.cfg.opsMu.Unlock()
	cancel()
}

func checkExit(t *testing.T, hub exitHub, tr *exitTrace, want exitWant) {
	t.Helper()
	id, err := hub.start()
	if err != nil {
		t.Fatalf("start the operation: %v", err)
	}
	hub.m.cfg.opsWG.Wait()
	record, ok := hub.store.Record(id)
	if !ok {
		t.Fatalf("record %s disappeared", id)
	}
	if record.State != want.state {
		t.Errorf("state = %q, want %q (result %+v)", record.State, want.state, record.Result)
	}
	if record.Result == nil || record.Result.OK != want.ok {
		t.Errorf("result = %+v, want ok=%t", record.Result, want.ok)
	}
	if got := tr.snapshot(); !slices.Equal(got, want.trace) {
		t.Errorf("seam trace = %v, want %v", got, want.trace)
	}
	if len(record.Progress) != want.progress {
		t.Errorf("progress lines = %d, want %d", len(record.Progress), want.progress)
	}
	if gateHeldBy(hub.m, "m4") {
		t.Error("the worker left the host gate held")
	}
}

func TestOperationWorkerDeployExits(t *testing.T) {
	failed := errors.New("injected failure")
	// terminal is an attach cause the reattach does not retry.
	terminal := fmt.Errorf("%w: fixture", sshconn.ErrProtocolIncompatible)
	tests := []struct {
		name  string
		build func(t *testing.T, tr *exitTrace) exitHub
		want  exitWant
	}{
		{"deploy then planned restart completes", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr}.build(t), false)
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 9,
			trace: []string{"deploy", "restart", "attach1(explicit=false)", "handoff1"}}},
		{"current host deploys without restarting", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr}.build(t), true)
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 6,
			trace: []string{"deploy"}}},
		{"fingerprint drift before the deploy", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr}.build(t)
			hub = deployExitHub(t, seams, false)
			hub.m.cfg.gate = &editingGate{inner: hub.m.cfg.gate, onAs: func() { rewriteHubTOML(t, hub.m) }}
			return hub
		}, exitWant{state: hostops.StateFailed, progress: 2}},
		{"no deploy step wired", func(t *testing.T, tr *exitTrace) exitHub {
			hub := deployExitHub(t, exitSeams{trace: tr}.build(t), false)
			hub.m.cfg.deployHost = nil
			return hub
		}, exitWant{state: hostops.StateFailed, progress: 2}},
		{"host detached before the deploy", func(t *testing.T, tr *exitTrace) exitHub {
			attachedChecks := 0
			seams := exitSeams{trace: tr}.build(t)
			attached := seams.attached
			seams.attached = func(name string) (sshconn.Preflight, bool) {
				attachedChecks++
				facts, ok := attached(name)
				return facts, ok && attachedChecks < 2
			}
			return deployExitHub(t, seams, false)
		}, exitWant{state: hostops.StateFailed, progress: 2}},
		{"deploy fails", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr, deployErr: failed}.build(t), false)
		}, exitWant{state: hostops.StateFailed, progress: 3, trace: []string{"deploy"}}},
		{"fingerprint drift between the deploy and the restart", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, afterDeploy: func() { rewriteHubTOML(t, hub.m) }}.build(t)
			hub = deployExitHub(t, seams, false)
			return hub
		}, exitWant{state: hostops.StateFailed, progress: 4, trace: []string{"deploy"}}},
		{"restart fails after the deploy", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr, restartErr: failed}.build(t), false)
		}, exitWant{state: hostops.StateFailed, progress: 5, trace: []string{"deploy", "restart"}}},
		{"reattach fails", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr, attachFailures: map[int]error{1: terminal}}.build(t), false)
		}, exitWant{state: hostops.StateFailed, progress: 7,
			trace: []string{"deploy", "restart", "attach1(explicit=false)"}}},
		{"supervisor cannot start after the verification", func(t *testing.T, tr *exitTrace) exitHub {
			return deployExitHub(t, exitSeams{trace: tr, handoffFails: map[int]bool{1: true}}.build(t), false)
		}, exitWant{state: hostops.StateFailed, progress: 9,
			trace: []string{"deploy", "restart", "attach1(explicit=false)", "handoff1"}}},
		{"shutdown during the deploy", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, deployErr: context.Canceled, afterDeploy: func() { cancelOperations(hub.m) }}.build(t)
			hub = deployExitHub(t, seams, false)
			return hub
		}, exitWant{state: hostops.StateInterrupted, progress: 3, trace: []string{"deploy"}}},
		{"shutdown after the deploy lands", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, afterDeploy: func() { cancelOperations(hub.m) }}.build(t)
			hub = deployExitHub(t, seams, true)
			return hub
		}, exitWant{state: hostops.StateInterrupted, progress: 4, trace: []string{"deploy"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &exitTrace{}
			checkExit(t, tc.build(t, tr), tr, tc.want)
		})
	}
}

func TestOperationWorkerRestartExits(t *testing.T) {
	failed := errors.New("injected failure")
	terminal := fmt.Errorf("%w: fixture", sshconn.ErrProtocolIncompatible)
	// detachedUntilAttach reports the host unattached until the first attach
	// call, the state an attach-first restart starts from.
	detachedUntilAttach := func(tr *exitTrace) func() bool {
		return func() bool {
			return slices.Contains(tr.snapshot(), "attach1(explicit=true)")
		}
	}
	tests := []struct {
		name  string
		build func(t *testing.T, tr *exitTrace) exitHub
		want  exitWant
	}{
		{"attached restart completes", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr}.build(t))
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 7,
			trace: []string{"restart", "attach1(explicit=false)", "handoff1"}}},
		{"attach-first restart completes", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr, attachedAt: detachedUntilAttach(tr)}.build(t))
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 8,
			trace: []string{"attach1(explicit=true)", "restart", "attach2(explicit=false)", "handoff2"}}},
		{"attach-first fails after publishing a channel", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr, attachedAt: detachedUntilAttach(tr), attachFailures: map[int]error{1: failed}}.build(t))
		}, exitWant{state: hostops.StateFailed, progress: 3,
			trace: []string{"attach1(explicit=true)"}}},
		{"pre-restart probe fails", func(t *testing.T, tr *exitTrace) exitHub {
			seams := exitSeams{trace: tr, attachedAt: detachedUntilAttach(tr)}.build(t)
			seams.probe = func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
				return hubcore.HostRuntimeProbe{}, failed
			}
			return restartExitHub(t, seams)
		}, exitWant{state: hostops.StateFailed, progress: 3,
			trace: []string{"attach1(explicit=true)", "handoff1"}}},
		{"host detaches after the pre-restart probe", func(t *testing.T, tr *exitTrace) exitHub {
			attachedChecks := 0
			seams := exitSeams{trace: tr}.build(t)
			attached := seams.attached
			seams.attached = func(name string) (sshconn.Preflight, bool) {
				attachedChecks++
				facts, ok := attached(name)
				return facts, ok && attachedChecks < 1
			}
			return restartExitHub(t, seams)
		}, exitWant{state: hostops.StateFailed, progress: 2}},
		{"restart fails after attach-first", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr, attachedAt: detachedUntilAttach(tr), restartErr: failed}.build(t))
		}, exitWant{state: hostops.StateFailed, progress: 4,
			trace: []string{"attach1(explicit=true)", "restart", "handoff1"}}},
		{"fingerprint drift before the restart", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr}.build(t)
			hub = restartExitHub(t, seams)
			hub.m.cfg.gate = &editingGate{inner: hub.m.cfg.gate, onAs: func() { rewriteHubTOML(t, hub.m) }}
			return hub
		}, exitWant{state: hostops.StateFailed, progress: 2}},
		{"recovery fails", func(t *testing.T, tr *exitTrace) exitHub {
			hub := restartExitHub(t, exitSeams{trace: tr}.build(t))
			hub.m.cfg.recoverDeployment = func(context.Context, hostreg.Host, hostops.Holder, func() error) (*sshconn.RestartResult, error) {
				tr.add("recover")
				return nil, failed
			}
			return hub
		}, exitWant{state: hostops.StateFailed, progress: 2, trace: []string{"recover"}}},
		{"recovery finds nothing pending", func(t *testing.T, tr *exitTrace) exitHub {
			hub := restartExitHub(t, exitSeams{trace: tr}.build(t))
			hub.m.cfg.recoverDeployment = func(context.Context, hostreg.Host, hostops.Holder, func() error) (*sshconn.RestartResult, error) {
				tr.add("recover")
				return nil, nil
			}
			return hub
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 7,
			trace: []string{"recover", "restart", "attach1(explicit=false)", "handoff1"}}},
		{"recovery replaces the process so no restart follows", func(t *testing.T, tr *exitTrace) exitHub {
			started := time.Date(2026, 9, 27, 12, 0, 30, 0, time.UTC)
			seams := exitSeams{trace: tr}.build(t)
			seams.probe = probeScript(t, hubcore.HostRuntimeProbe{Version: "dev", RunningHealthy: true, ProcessStartTime: &started})
			hub := restartExitHub(t, seams)
			hub.m.cfg.recoverDeployment = func(context.Context, hostreg.Host, hostops.Holder, func() error) (*sshconn.RestartResult, error) {
				tr.add("recover")
				return &sshconn.RestartResult{TargetVersion: "dev", StartedAt: started, ReplacedStartedAt: started.Add(-time.Hour)}, nil
			}
			return hub
		}, exitWant{state: hostops.StateComplete, ok: true, progress: 6,
			trace: []string{"recover", "attach1(explicit=false)", "handoff1"}}},
		{"reattach fails", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr, attachFailures: map[int]error{1: terminal}}.build(t))
		}, exitWant{state: hostops.StateFailed, progress: 5,
			trace: []string{"restart", "attach1(explicit=false)"}}},
		{"supervisor cannot start after the verification", func(t *testing.T, tr *exitTrace) exitHub {
			return restartExitHub(t, exitSeams{trace: tr, handoffFails: map[int]bool{1: true}}.build(t))
		}, exitWant{state: hostops.StateFailed, progress: 7,
			trace: []string{"restart", "attach1(explicit=false)", "handoff1"}}},
		{"shutdown during the restart", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, restartErr: context.Canceled, afterRestart: func() { cancelOperations(hub.m) }}.build(t)
			hub = restartExitHub(t, seams)
			return hub
		}, exitWant{state: hostops.StateInterrupted, progress: 3, trace: []string{"restart"}}},
		{"shutdown after the restart lands", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, afterRestart: func() { cancelOperations(hub.m) }}.build(t)
			hub = restartExitHub(t, seams)
			return hub
		}, exitWant{state: hostops.StateInterrupted, progress: 4, trace: []string{"restart"}}},
		{"shutdown after attach-first", func(t *testing.T, tr *exitTrace) exitHub {
			var hub exitHub
			seams := exitSeams{trace: tr, attachedAt: detachedUntilAttach(tr), restartErr: context.Canceled,
				afterRestart: func() { cancelOperations(hub.m) }}.build(t)
			hub = restartExitHub(t, seams)
			return hub
		}, exitWant{state: hostops.StateInterrupted, progress: 4, trace: []string{"attach1(explicit=true)", "restart", "handoff1"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &exitTrace{}
			checkExit(t, tc.build(t, tr), tr, tc.want)
		})
	}
}

// TestStartOperationRefusalsLeaveNothingBehind drives the refusals of the
// shared start path that sit behind the gate and after the probe epoch is
// persisted. Each one must answer a typed refusal, create no record, start no
// worker, leave no probe epoch behind and release the gate; a deploy's token
// survives unless the refusal is the token itself.
func TestStartOperationRefusalsLeaveNothingBehind(t *testing.T) {
	entry := deployTestHost()
	type refusal struct {
		name string
		// seams adjusts the scripted seams before the hub is built; hub adjusts
		// the built hub.
		seams func(seams *deploySeams)
		hub   func(t *testing.T, m *hubHostManager, store *hostops.Store, registry *hostreg.Registry)
		// info is the typed refusal the caller must see.
		info appwire.ErrorInfo
		// tokenSuperseded is set when the refusal's cause is the token itself
		// being replaced, so it is not expected to survive.
		tokenSuperseded bool
	}
	tests := []refusal{
		{name: "probe epoch cannot be persisted", info: appwire.ErrorProbeFailed,
			hub: func(_ *testing.T, m *hubHostManager, _ *hostops.Store, _ *hostreg.Registry) { m.cfg.bootID = "" }},
		{name: "probe fails", info: appwire.ErrorProbeFailed,
			seams: func(seams *deploySeams) {
				seams.probe = func(context.Context, hostreg.Host, *appwire.Client, appwire.FencingEpoch) (hubcore.HostRuntimeProbe, error) {
					return hubcore.HostRuntimeProbe{}, errors.New("injected probe failure")
				}
			}},
		{name: "host detaches during the probe", info: appwire.ErrorHostDetached,
			seams: func(seams *deploySeams) {
				attachedChecks := 0
				seams.attached = func(string) (sshconn.Preflight, bool) {
					attachedChecks++
					return deployTestFacts(entry), attachedChecks < 1
				}
			}},
		{name: "registration moves while the gate is acquired", info: appwire.ErrorStaleEntry,
			hub: func(t *testing.T, m *hubHostManager, _ *hostops.Store, registry *hostreg.Registry) {
				m.cfg.gate = &editingGate{inner: m.cfg.gate, onTry: func() {
					live, _ := registry.Get(entry.Name)
					if err := registry.Update(live); err != nil {
						t.Errorf("move the registration: %v", err)
					}
				}}
			}},
		{name: "token is superseded before the write", info: appwire.ErrorTokenSuperseded, tokenSuperseded: true,
			hub: func(t *testing.T, m *hubHostManager, store *hostops.Store, _ *hostreg.Registry) {
				m.cfg.gate = &editingGate{inner: m.cfg.gate, onTry: func() {
					mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
				}}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := deployTestConfigPath(t, entry)
			seams := deploySeams{probe: deployProbeScript(t)}
			if tc.seams != nil {
				tc.seams(&seams)
			}
			m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, seams)
			if tc.hub != nil {
				tc.hub(t, m, store, registry)
			}
			token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
			_, err := m.Deploy(context.Background(), appwire.HostDeployParams{
				Name: entry.Name, Token: token.Value, OperationID: "op-1",
			})
			info, _, _ := deployWireInfo(t, err)
			if info != tc.info {
				t.Fatalf("refusal = %q, want %q", info, tc.info)
			}
			m.cfg.opsWG.Wait()
			if got := len(store.Records()); got != 0 {
				t.Errorf("the refusal created %d records", got)
			}
			if _, ok := store.ProbeEpoch(entry.Name); ok {
				t.Error("the refusal left its probe epoch behind")
			}
			if gateHeldBy(m, entry.Name) {
				t.Error("the refusal left the host gate held")
			}
			if _, ok := store.OutstandingToken(entry.Name); !ok && !tc.tokenSuperseded {
				t.Error("the refusal consumed the token")
			}
		})
	}
}

// TestStartOperationReplayedAtTheWriteDropsItsProbeEpoch pins the deploy side
// of the in-write dedup: a call that wins the gate after another call with the
// same operation id created its record is answered with that record, starts no
// worker, leaves the token alone and drops the probe epoch it persisted.
func TestStartOperationReplayedAtTheWriteDropsItsProbeEpoch(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	counters := &deployCounters{}
	m, store, registry := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{counters: counters, probe: deployProbeScript(t)})
	live, ok := registry.Get(entry.Name)
	if !ok {
		t.Fatal("the registry carries no entry")
	}
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	var racer hostops.Record
	m.cfg.gate = &editingGate{inner: m.cfg.gate, onTry: func() {
		racer = createPairRecordForTest(t, store, live, hostops.KindDeploy, "op-1")
	}}
	response, err := m.Deploy(context.Background(), appwire.HostDeployParams{
		Name: entry.Name, Token: token.Value, OperationID: "op-1",
	})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	m.cfg.opsWG.Wait()
	if racer.ID == "" || response.ID != racer.ID {
		t.Fatalf("response id = %q, want the racing record %q", response.ID, racer.ID)
	}
	if got := len(store.Records()); got != 1 {
		t.Errorf("records = %d, want only the racing record", got)
	}
	if got := counters.deploys.Load(); got != 0 {
		t.Errorf("deploy calls = %d, want 0: a replayed dedup hit launches no worker", got)
	}
	if _, ok := store.OutstandingToken(entry.Name); !ok {
		t.Error("the replay consumed the token")
	}
	if _, ok := store.ProbeEpoch(entry.Name); ok {
		t.Error("the replay left its probe epoch behind")
	}
	if gateHeldBy(m, entry.Name) {
		t.Error("the replay left the host gate held")
	}
}

// TestStartOperationRefusesBeforeTheGate pins the refusals that precede any
// lock: a name the registry does not hold, and a hub with no operation store.
func TestStartOperationRefusesBeforeTheGate(t *testing.T) {
	entry := deployTestHost()
	configPath := deployTestConfigPath(t, entry)
	m, store, _ := deployTestHub(t, configPath, []hostreg.Host{entry}, deploySeams{})
	token := mintDeployToken(t, store, m, entry, "v0.9.0", true, "v1.2.3")
	_, err := m.Deploy(context.Background(), appwire.HostDeployParams{Name: "ghost", Token: token.Value, OperationID: "op-1"})
	if info, _, code := deployWireInfo(t, err); code != appwire.CodeInvalidParams {
		t.Errorf("unknown host refusal = (%q, %d), want invalid params", info, code)
	}
	m.cfg.ops = nil
	_, err = m.Deploy(context.Background(), appwire.HostDeployParams{Name: entry.Name, Token: token.Value, OperationID: "op-1"})
	if info, _, code := deployWireInfo(t, err); code != appwire.CodeInternalError {
		t.Errorf("missing store refusal = (%q, %d), want internal error", info, code)
	}
	if gateHeldBy(m, entry.Name) {
		t.Error("a refusal before the gate left it held")
	}
}
