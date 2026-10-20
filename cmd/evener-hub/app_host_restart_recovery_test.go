package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
)

const (
	restartBuildA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	restartBuildB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	restartStartA = "2026-01-01T00:00:00Z"
	restartStartB = "2026-01-02T00:00:00Z"
)

// Exercise the public operation worker and the real SSH manager together. Only
// SSH and the compiler process are faked: the first deployment loses its write
// response, and Restart must finish the captured B deployment before replacing A.
func TestHostRestartReconcilesDistinctBuildDeployment(t *testing.T) {
	// Not parallel: the controller build stamp is process-wide.
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel
	})
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", ""

	for _, scenario := range []struct {
		name            string
		probeVersion    string
		probeStart      string
		delayBridgeExit bool
		emptyPath       bool
		want            hostops.State
	}{
		{name: "requested-build-replaces-predecessor", want: hostops.StateComplete},
		{name: "replacement-before-bridge-exit", delayBridgeExit: true, want: hostops.StateComplete},
		{name: "resolved-path-before-bridge-exit", emptyPath: true, delayBridgeExit: true, want: hostops.StateComplete},
		{name: "wrong-build", probeVersion: restartBuildA, want: hostops.StateFailed},
		{name: "wrong-process", probeStart: restartStartA, want: hostops.StateFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			host := deployTestHost()
			if scenario.emptyPath {
				host.EvenerPath = ""
			}
			registry, err := hostreg.New([]hostreg.Host{host})
			if err != nil {
				t.Fatal(err)
			}
			store, err := hostops.Open(hostops.StorePath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			runner := &restartRecoveryRunner{
				diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA,
				probeVersion: scenario.probeVersion, probeStart: scenario.probeStart,
				delayBridgeExit: scenario.delayBridgeExit,
			}
			manager := sshconn.New(registry, sshconn.Options{
				Runner: runner, Stderr: io.Discard,
				BuildBinary: func(_ context.Context, _, _, out string) error {
					return os.WriteFile(out, []byte(restartBuildB), 0o700)
				},
			})
			t.Cleanup(func() { _ = manager.Close() })
			configPath := deployTestConfigPath(t, host)
			m := newHubHostManager(nil, manager, hubcore.WebConfig{
				RemoteHostOpsStore: store, HubBootID: "restart-test-boot",
			}, configPath, registry, t.Logf)
			t.Cleanup(func() { m.ShutdownHostOperations(operationsShutdownTimeout) })
			if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
				t.Fatalf("initial attach: %v", err)
			}
			plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
			if err != nil {
				t.Fatalf("plan: %v", err)
			}
			deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{
				Name: host.Name, Token: plan.Token, OperationID: "interrupted-deploy",
			})
			if err != nil {
				t.Fatalf("deploy admission: %v", err)
			}
			m.cfg.opsWG.Wait()
			assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
			runner.mu.Lock()
			initialWrites := runner.writes
			runner.mu.Unlock()
			if initialWrites != 1 {
				record, _ := store.Record(deploy.ID)
				t.Fatalf("deployment did not reach its interrupted write: writes=%d result=%+v", initialWrites, record.Result)
			}
			host, _ = registry.Get(host.Name)
			restart, err := m.Restart(t.Context(), appwire.HostRestartParams{
				Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID,
				OperationID: "recover-restart",
			})
			if err != nil {
				t.Fatalf("restart admission: %v", err)
			}
			m.cfg.opsWG.Wait()
			runner.mu.Lock()
			writes, restarts, probes := runner.writes, runner.restarts, runner.afterProbes
			diskVersion, runningVersion := runner.diskVersion, runner.runningVersion
			bridges := len(runner.streams)
			runner.mu.Unlock()
			if bridges != 2 {
				t.Fatalf("restart reused predecessor bridge: opened=%d, want 2", bridges)
			}
			if writes != 2 || restarts != 1 || probes != 1 || diskVersion != restartBuildB || runningVersion != restartBuildB {
				t.Fatalf("recovery: writes=%d restarts=%d post-restart probes=%d disk=%q running=%q", writes, restarts, probes, diskVersion, runningVersion)
			}
			assertRestartRecoveryRecord(t, store, restart.ID, scenario.want)
		})
	}
}

func TestHostRestartPreservesCompatibleHubWithUnusableExecutable(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		launch       []byte
		initiallyBad bool
	}{
		{name: "incompatible-at-connect", launch: []byte(`{"protocol":"other-protocol","version":"disk-build","launch_flags":["api-log"]}`), initiallyBad: true},
		{name: "incompatible-after-connect", launch: []byte(`{"protocol":"other-protocol","version":"disk-build","launch_flags":["api-log"]}`)},
		{name: "missing-startup-flag", launch: fmt.Appendf(nil, `{"protocol":%q,"version":"disk-build","launch_flags":[]}`, appwire.ProtocolVersion)},
		{name: "unreadable-contract", launch: []byte(`{`)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			host := deployTestHost()
			registry, err := hostreg.New([]hostreg.Host{host})
			if err != nil {
				t.Fatal(err)
			}
			store, err := hostops.Open(hostops.StorePath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA}
			if scenario.initiallyBad {
				runner.launchResponse = scenario.launch
			}
			manager := sshconn.New(registry, sshconn.Options{Runner: runner, Stderr: io.Discard, DeployDisabled: true})
			t.Cleanup(func() { _ = manager.Close() })
			m := newHubHostManager(nil, manager, hubcore.WebConfig{RemoteHostOpsStore: store, HubBootID: "restart-preservation"}, deployTestConfigPath(t, host), registry, t.Logf)
			t.Cleanup(func() { m.ShutdownHostOperations(operationsShutdownTimeout) })
			if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
				t.Fatalf("compatible Connect: %v", err)
			}
			runner.mu.Lock()
			runner.launchResponse = scenario.launch
			runner.mu.Unlock()
			host, _ = registry.Get(host.Name)
			restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "preserve-live-hub"})
			if err != nil {
				t.Fatalf("restart admission: %v", err)
			}
			m.cfg.opsWG.Wait()
			runner.mu.Lock()
			writes, restarts, started := runner.writes, runner.restarts, runner.startedAt
			runner.mu.Unlock()
			if writes != 0 || restarts != 0 || started != restartStartA {
				t.Fatalf("usable predecessor was changed: writes=%d restarts=%d start=%s", writes, restarts, started)
			}
			assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
			if ch, err := manager.Ensure(t.Context(), host.Name); err != nil || ch == nil {
				t.Fatalf("compatible hub no longer usable: channel=%v err=%v", ch, err)
			}
			runner.mu.Lock()
			runner.launchResponse = nil
			runner.mu.Unlock()
			retry, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "restart-repaired-executable"})
			if err != nil {
				t.Fatalf("restart repaired executable: %v", err)
			}
			m.cfg.opsWG.Wait()
			assertRestartRecoveryRecord(t, store, retry.ID, hostops.StateComplete)
			runner.mu.Lock()
			writes, restarts, started = runner.writes, runner.restarts, runner.startedAt
			runner.mu.Unlock()
			if writes != 0 || restarts != 1 || started != restartStartB {
				t.Fatalf("repaired restart: writes=%d restarts=%d start=%s", writes, restarts, started)
			}
		})
	}
}

func assertRestartRecoveryRecord(t *testing.T, store *hostops.Store, id string, want hostops.State) {
	t.Helper()
	record, ok := store.Record(id)
	if !ok || record.State != want || record.Result == nil || record.Result.OK != (want == hostops.StateComplete) {
		t.Fatalf("operation %q: state=%s result=%+v, want %s", id, record.State, record.Result, want)
	}
}

func TestHostRestartRepairsDisconnectedDeployment(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
	for _, scenario := range []string{"protocol", "missing-flag", "malformed", "approved-replacement", "repair-retry", "fingerprint", "wrong-build", "wrong-process"} {
		t.Run(scenario, func(t *testing.T) {
			contract := fmt.Appendf(nil, `{"protocol":"incompatible","version":%q,"launch_flags":["api-log"]}`, restartBuildB)
			if scenario == "missing-flag" {
				contract = fmt.Appendf(nil, `{"protocol":%q,"version":%q,"launch_flags":[]}`, appwire.ProtocolVersion, restartBuildB)
			}
			if scenario == "malformed" || scenario == "approved-replacement" {
				contract = []byte(`{`)
			}
			// A healthy B process makes this public plan deploy-only. Its failed
			// installation must stay pending when the bridge subsequently drops.
			runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildB, startedAt: restartStartA,
				acknowledgeFirstWrite: true, firstWriteContract: contract, failRepair: scenario == "repair-retry"}
			initialVersion := restartBuildB
			if scenario == "approved-replacement" {
				initialVersion = restartBuildA
				runner.runningVersion = initialVersion
			}
			if scenario == "wrong-build" {
				runner.probeVersion = restartBuildA
			}
			if scenario == "wrong-process" {
				runner.probeStart = restartStartA
			}
			m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
			ch, err := manager.Ensure(t.Context(), host.Name)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
			if err != nil {
				t.Fatal(err)
			}
			deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "invalid-install"})
			if err != nil {
				t.Fatal(err)
			}
			m.cfg.opsWG.Wait()
			assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
			assertPublicRecoveryEffects(t, runner, 1, 1, 0, initialVersion, restartStartA)
			if err := ch.Close(); err != nil {
				t.Fatal(err)
			}
			// Let actual reconnect reach its terminal contract refusal. It must not
			// erase installation ownership or manufacture a new deployment.
			waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
			if manager.Attached(host.Name) {
				t.Fatal("failed deployment unexpectedly reattached")
			}
			params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "repair-disconnected"}
			stale := params
			stale.Generation++
			if _, err := m.Restart(t.Context(), stale); err == nil {
				t.Fatal("Restart accepted a stale generation")
			}
			assertPublicRecoveryEffects(t, runner, 1, 1, 0, initialVersion, restartStartA)
			if scenario == "fingerprint" {
				original, err := os.ReadFile(m.cfg.configPath)
				if err != nil {
					t.Fatal(err)
				}
				edited := host
				edited.SSH = "elsewhere.example"
				recoverDeployment := m.cfg.recoverDeployment
				recoveryCalled := false
				m.cfg.recoverDeployment = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder) (*sshconn.RestartResult, error) {
					recoveryCalled = true
					return recoverDeployment(ctx, entry, holder)
				}
				m.cfg.gate = &editingGate{inner: manager, onAs: func() {
					if err := os.WriteFile(m.cfg.configPath, []byte(planTestHubTOML([]hostreg.Host{edited})), 0o600); err != nil {
						t.Error(err)
					}
				}}
				refused, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, refused.ID, hostops.StateFailed)
				if recoveryCalled {
					t.Fatal("worker entered recovery after its host configuration changed")
				}
				assertPublicRecoveryEffects(t, runner, 1, 1, 0, initialVersion, restartStartA)
				m.cfg.recoverDeployment = recoverDeployment
				m.cfg.gate = manager
				if err := os.WriteFile(m.cfg.configPath, original, 0o600); err != nil {
					t.Fatal(err)
				}
				params.OperationID = "after-fingerprint-refusal"
			}
			restart, err := m.Restart(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			m.cfg.opsWG.Wait()
			wantWrites := 2
			if scenario == "repair-retry" {
				assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 2, 2, 0, restartBuildB, restartStartA)
				runner.mu.Lock()
				runner.failRepair = false
				runner.mu.Unlock()
				params.OperationID = "repair-disconnected-retry"
				restart, err = m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				wantWrites = 3
			}
			wantState := hostops.StateComplete
			if scenario == "wrong-build" || scenario == "wrong-process" {
				wantState = hostops.StateFailed
			}
			assertRestartRecoveryRecord(t, store, restart.ID, wantState)
			assertPublicRecoveryEffects(t, runner, wantWrites, wantWrites, 1, restartBuildB, restartStartB)
			record, _ := store.Record(restart.ID)
			runner.mu.Lock()
			epochs, bridges := append([]appwire.FencingEpoch(nil), runner.probeEpochs...), len(runner.streams)
			runner.mu.Unlock()
			if bridges != 2 || len(epochs) == 0 || epochs[len(epochs)-1] != epochOf(record) {
				t.Fatalf("post-repair bridge/epoch: bridges=%d epochs=%v record=%+v", bridges, epochs, record)
			}
			// A second transport drop must reconnect without another mutation:
			// this observes the worker's real supervisor handoff, not its callback.
			waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
			repaired, ok := manager.ChannelIfAttached(host.Name)
			if !ok {
				t.Fatal("completed Restart has no live channel")
			}
			if err := repaired.Close(); err != nil {
				t.Fatal(err)
			}
			waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
			assertPublicRecoveryEffects(t, runner, wantWrites, wantWrites, 1, restartBuildB, restartStartB)
		})
	}
}

func TestHostRestartDisconnectedWithoutDeploymentDoesNotInstall(t *testing.T) {
	runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA}
	m, manager, store, host, _ := publicRecoveryHub(t, runner, restartBuildB)
	ch, err := manager.Ensure(t.Context(), host.Name)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.launchResponse = fmt.Appendf(nil, `{"protocol":"incompatible","version":%q,"launch_flags":["api-log"]}`, restartBuildA)
	runner.mu.Unlock()
	// Drop under the actual operation hold so reconnect cannot win the race.
	m.cfg.gate = &editingGate{inner: manager, onAs: func() { _ = ch.Close() }}
	params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "ordinary-disconnected"}
	restart, err := m.Restart(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.opsWG.Wait()
	assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
	assertPublicRecoveryEffects(t, runner, 0, 0, 0, restartBuildA, restartStartA)
	runner.mu.Lock()
	bridges := len(runner.streams)
	runner.launchResponse = nil
	runner.mu.Unlock()
	if bridges != 2 || !manager.Attached(host.Name) {
		t.Fatalf("ordinary Restart did not attach first: bridges=%d attached=%t", bridges, manager.Attached(host.Name))
	}
	m.cfg.gate = manager
	params.OperationID = "ordinary-restored"
	restart, err = m.Restart(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.opsWG.Wait()
	assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
	assertPublicRecoveryEffects(t, runner, 0, 0, 1, restartBuildA, restartStartB)
}

func TestHostDeployRestartAfterFreshProvisioningDoesNotRepush(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = "", "", ""
	absent := exec.Command("sh", "-c", "exit 1").Run()
	var exit *exec.ExitError
	if !errors.As(absent, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("missing-executable fixture: %v", absent)
	}
	runner := &restartRecoveryRunner{missing: true, missingError: absent, acknowledgeFirstWrite: true}
	m, manager, store, host, _ := publicRecoveryHub(t, runner, "dev")
	if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
		t.Fatalf("fresh provisioning: %v", err)
	}
	assertPublicRecoveryEffects(t, runner, 1, 1, 0, "dev", restartStartA)
	plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "same-label-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.opsWG.Wait()
	assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateComplete)
	assertPublicRecoveryEffects(t, runner, 2, 2, 1, "dev", restartStartB)
	restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "standalone-after-provisioning"})
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.opsWG.Wait()
	assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
	assertPublicRecoveryEffects(t, runner, 2, 2, 2, "dev", "2026-01-03T00:00:00Z")
}

func publicRecoveryHub(t *testing.T, runner *restartRecoveryRunner, artifact string) (*hubHostManager, *sshconn.Manager, *hostops.Store, hostreg.Host, <-chan sshconn.EventKind) {
	t.Helper()
	host := deployTestHost()
	registry, err := hostreg.New([]hostreg.Host{host})
	if err != nil {
		t.Fatal(err)
	}
	host, _ = registry.Get(host.Name)
	store, err := hostops.Open(hostops.StorePath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	events := make(chan sshconn.EventKind, 32)
	manager := sshconn.New(registry, sshconn.Options{Runner: runner, Stderr: io.Discard, BackoffBase: 10 * time.Millisecond,
		OnEvent: func(event sshconn.Event) {
			if event.Kind == sshconn.EventAttached || event.Kind == sshconn.EventFailed {
				events <- event.Kind
			}
		},
		BuildBinary: func(_ context.Context, _, _, out string) error {
			runner.mu.Lock()
			runner.builds++
			runner.mu.Unlock()
			return os.WriteFile(out, []byte(artifact), 0o700)
		},
	})
	t.Cleanup(func() { _ = manager.Close() })
	m := newHubHostManager(nil, manager, hubcore.WebConfig{RemoteHostOpsStore: store, HubBootID: "public-recovery"}, deployTestConfigPath(t, host), registry, t.Logf)
	t.Cleanup(func() { m.ShutdownHostOperations(operationsShutdownTimeout) })
	runner.observeMutation = func() {
		release, err := manager.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderPlan})
		if release != nil {
			release()
		}
		var busy *hostops.BusyError
		if !errors.As(err, &busy) || busy.Holder.Kind != hostops.HolderOperation {
			t.Errorf("mutation without operation gate: %v", err)
			return
		}
		record, ok := store.Record(busy.Holder.OperationID)
		ensurePending := strings.HasPrefix(record.ClientOperationID, "ensure-") && record.State == hostops.StatePending
		if !ok || record.State != hostops.StateRunning && !ensurePending || epochOf(record).BootID == "" {
			t.Errorf("mutation without durable running record/epoch: %+v", record)
		}
	}
	return m, manager, store, host, events
}

func waitPublicRecoveryEvent(t *testing.T, events <-chan sshconn.EventKind, want sshconn.EventKind) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case kind := <-events:
			if kind == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("no %s event", want)
		}
	}
}

func assertPublicRecoveryEffects(t *testing.T, runner *restartRecoveryRunner, builds, writes, restarts int, version, started string) {
	t.Helper()
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.builds != builds || runner.writes != writes || runner.restarts != restarts || runner.runningVersion != version || runner.startedAt != started {
		t.Fatalf("effects: builds=%d writes=%d restarts=%d version=%s start=%s, want %d/%d/%d/%s/%s", runner.builds, runner.writes, runner.restarts, runner.runningVersion, runner.startedAt, builds, writes, restarts, version, started)
	}
}

// restartRecoveryRunner models a remote machine at the process/network boundary.
// Health reports the actual replacement; the AppWire overrides exercise the
// worker's independent verification after sshconn has accepted that replacement.
type restartRecoveryRunner struct {
	mu                            sync.Mutex
	diskVersion, runningVersion   string
	startedAt                     string
	probeVersion, probeStart      string
	launchResponse                []byte
	delayBridgeExit               bool
	writes, restarts, afterProbes int
	builds, starts                int
	acknowledgeFirstWrite         bool
	firstWriteContract            []byte
	failRepair                    bool
	missing                       bool
	missingError                  error
	observeMutation               func()
	probeEpochs                   []appwire.FencingEpoch
	streams                       []*probeFakeStdio
}

func (r *restartRecoveryRunner) Run(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := strings.Join(argv, " ")
	switch {
	case strings.HasSuffix(command, "uname -s"):
		return []byte("Linux\n"), nil
	case strings.HasSuffix(command, "uname -m"):
		return []byte("x86_64\n"), nil
	case strings.Contains(command, "XDG_STATE_HOME"):
		return []byte("HOME=/home/ops\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
	case strings.HasSuffix(command, "id -u"):
		return []byte("1000\n"), nil
	case strings.Contains(command, "launch-check"):
		if r.missing {
			return nil, r.missingError
		}
		if r.launchResponse != nil {
			return r.launchResponse, nil
		}
		return fmt.Appendf(nil, `{"protocol":%q,"version":%q,"launch_flags":["api-log"]}`, appwire.ProtocolVersion, r.diskVersion), nil
	case strings.Contains(command, "if test -x"):
		if r.missing {
			return nil, r.missingError
		}
		return nil, nil
	case strings.Contains(command, "test -d /opt/evener/bin"):
		return nil, nil
	case strings.Contains(command, "command -v evener"):
		return []byte("/opt/evener/bin/evener\n"), nil
	case strings.Contains(command, "evener_resolve"):
		return []byte("/opt/evener/bin/evener\n"), nil
	case strings.Contains(command, "list-units"):
		if r.runningVersion == "" {
			if strings.Contains(command, "--user") {
				return nil, nil
			}
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		}
		return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
	case strings.Contains(command, "lsof -ti :9180"):
		return []byte(sshconn.NoListenerMarker), nil
	case strings.Contains(command, "systemctl start"):
		r.starts++
		r.runningVersion, r.startedAt = r.diskVersion, restartStartA
		return nil, nil
	case strings.Contains(command, "systemctl restart"):
		if r.observeMutation != nil {
			r.observeMutation()
		}
		r.restarts++
		r.runningVersion, r.startedAt = r.diskVersion, fmt.Sprintf("2026-01-%02dT00:00:00Z", r.restarts+1)
		// A remote replacement can be confirmed before the local SSH reader
		// observes EOF. Keep that boundary open to exercise the ordering.
		if !r.delayBridgeExit {
			for _, stream := range r.streams {
				_ = stream.Kill()
			}
		}
		return nil, nil
	case strings.Contains(command, "api/health"):
		if r.runningVersion == "" {
			return nil, errors.New("no listener")
		}
		return fmt.Appendf(nil, `{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, r.runningVersion, r.runningVersion, r.startedAt), nil
	case strings.Contains(command, "cat >"):
		if r.observeMutation != nil {
			r.observeMutation()
		}
		r.writes++
		if r.writes == 1 && !r.acknowledgeFirstWrite || r.writes > 1 && r.failRepair {
			return nil, errors.New("interrupted transport")
		}
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		r.diskVersion = string(data)
		r.missing, r.launchResponse = false, nil
		if r.writes == 1 {
			r.launchResponse = r.firstWriteContract
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}
}

func (r *restartRecoveryRunner) Start(context.Context, []string, io.Writer) (sshconn.Stdio, error) {
	stream := probeBridge(func(params appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.probeEpochs = append(r.probeEpochs, params.FencingEpoch)
		version, startedAt := r.runningVersion, r.startedAt
		if r.restarts > 0 {
			r.afterProbes++
			if r.probeVersion != "" {
				version = r.probeVersion
			}
			if r.probeStart != "" {
				startedAt = r.probeStart
			}
		}
		return appwire.HostRunningResponse{
			BuildRevision: version, Healthy: true, ProcessStartTime: startedAt,
		}, appwire.WireError{}
	})
	r.mu.Lock()
	r.streams = append(r.streams, stream)
	r.mu.Unlock()
	return stream, nil
}
