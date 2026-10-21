package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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

func TestHostRestartRecoveryChecksFingerprintBetweenPhases(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, path := range []string{"attached", "disconnected", "disconnected-stopped"} {
		for _, edit := range []string{"entry", "entry-renew", "entry-reconnect", "entry-reconnect-renew", "invalid", "invalid-reconnect", "unreadable", "unreadable-reconnect", "unrelated", "unchanged", "before-write"} {
			reconnect := strings.Contains(edit, "reconnect")
			if reconnect && path != "attached" {
				continue // Only this path still owns the preserved predecessor channel.
			}
			t.Run(path+"/"+edit, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
					runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA,
						acknowledgeFirstWrite: true, firstWriteContract: []byte(`{`), noSupervisor: path == "disconnected-stopped"}
					m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
					ch, err := manager.Ensure(t.Context(), host.Name)
					if err != nil {
						t.Fatal(err)
					}
					plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
					if err != nil {
						t.Fatal(err)
					}
					deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "unfinished-install"})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
					assertPublicRecoveryEffects(t, runner, 1, 1, 0, restartBuildA, restartStartA)
					initialVersion, initialStart := restartBuildA, restartStartA
					if path != "attached" {
						if path == "disconnected-stopped" {
							runner.mu.Lock()
							runner.runningVersion, runner.startedAt = "", ""
							runner.mu.Unlock()
							initialVersion, initialStart = "", ""
						}
						if err := ch.Close(); err != nil {
							t.Fatal(err)
						}
						waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
					}
					original, err := os.ReadFile(m.cfg.configPath)
					if err != nil {
						t.Fatal(err)
					}
					editConfig := func() {
						entries := []hostreg.Host{host}
						switch edit {
						case "entry", "entry-renew", "entry-reconnect", "entry-reconnect-renew", "before-write":
							entries[0].SSH = "changed.example"
						case "unrelated":
							entries = append(entries, hostreg.Host{Name: "other", SSH: "other.example"})
						case "unreadable", "unreadable-reconnect":
							if err := os.Remove(m.cfg.configPath); err != nil {
								t.Error(err)
							}
							if err := os.Mkdir(m.cfg.configPath, 0o700); err != nil {
								t.Error(err)
							}
							return
						}
						contents := []byte(planTestHubTOML(entries))
						if edit == "invalid" || edit == "invalid-reconnect" {
							contents = []byte("[[hosts]\n")
						}
						if err := os.WriteFile(m.cfg.configPath, contents, 0o600); err != nil {
							t.Error(err)
						}
					}
					observeMutation := runner.observeMutation
					runner.observeMutation = func() {
						observeMutation()
						// Run executes this under runner.mu immediately before the repair
						// write. The write still lands and its launch contract is verified.
						if runner.writes == 1 && edit != "before-write" {
							editConfig()
						}
					}
					if edit == "before-write" {
						m.cfg.gate = &editingGate{inner: manager, onAs: editConfig}
					}
					var workerErr error
					restartHost, recoverDeployment := m.cfg.restartHost, m.cfg.recoverDeployment
					m.cfg.restartHost = func(ctx context.Context, entry hostreg.Host, facts sshconn.Preflight, guard func() error) (string, error) {
						result, err := restartHost(ctx, entry, facts, guard)
						workerErr = err
						return result, err
					}
					m.cfg.recoverDeployment = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, guard func() error) (*sshconn.RestartResult, error) {
						result, err := recoverDeployment(ctx, entry, holder, guard)
						workerErr = err
						return result, err
					}
					params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "repair-with-config-edit"}
					restart, err := m.Restart(t.Context(), params)
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					refused := edit != "unchanged" && edit != "unrelated"
					if refused {
						assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
						wantWrites := 2
						switch edit {
						case "before-write":
							wantWrites = 1
							if workerErr != nil {
								t.Fatalf("pre-write refusal reached recovery: %v", workerErr)
							}
						case "entry", "entry-renew", "entry-reconnect", "entry-reconnect-renew":
							info, data, code := deployWireInfo(t, workerErr)
							var binding appwire.StaleEntryBinding
							if err := json.Unmarshal(data["binding"], &binding); err != nil || info != appwire.ErrorStaleEntry || code != appwire.CodeConflict || binding != appwire.StaleEntryBindingHubTOMLFingerprint {
								t.Fatalf("post-install refusal: info=%s code=%d binding=%s err=%v", info, code, binding, err)
							}
						default:
							var wire appwire.WireError
							if !errors.As(workerErr, &wire) || wire.Code != appwire.CodeInternalError {
								t.Fatalf("invalid selected config was not refused: %v", workerErr)
							}
						}
						assertPublicRecoveryEffects(t, runner, wantWrites, wantWrites, 0, initialVersion, initialStart)
						runner.mu.Lock()
						starts, disk := runner.starts, runner.diskVersion
						runner.mu.Unlock()
						if starts != 0 || disk != restartBuildB {
							t.Fatalf("post-install refusal: starts=%d disk=%s", starts, disk)
						}
						if reconnect {
							// Drain the initial attachment, then lose the still-preserved
							// bridge while the selected config entry remains changed.
							waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
							if err := ch.Close(); err != nil {
								t.Fatal(err)
							}
							select {
							case kind := <-events:
								synctest.Wait() // The real reconnect producer releases its gate.
								if kind != sshconn.EventFailed {
									t.Errorf("stale-entry reconnect outcome=%s, want %s", kind, sshconn.EventFailed)
								}
							case <-time.After(5 * time.Second):
								t.Fatal("post-refusal supervisor attempt did not finish")
							}
							runner.mu.Lock()
							starts = runner.starts
							runner.mu.Unlock()
							if starts != 0 {
								t.Errorf("stale-entry reconnect started hub %d times", starts)
							}
							assertPublicRecoveryEffects(t, runner, 2, 2, 0, restartBuildA, restartStartA)
						}
						if edit == "unreadable" || edit == "unreadable-reconnect" {
							if err := os.Remove(m.cfg.configPath); err != nil {
								t.Fatal(err)
							}
						}
						// A fresh explicit Restart may validate the still-changed entry.
						// Keep the restored-entry case as an independent recovery control.
						if !strings.HasSuffix(edit, "-renew") {
							if err := os.WriteFile(m.cfg.configPath, original, 0o600); err != nil {
								t.Fatal(err)
							}
						}
						runner.observeMutation, m.cfg.gate = observeMutation, manager
						// A later controller must not replace the original B intent. A
						// verified acknowledged repair must not write a third time.
						buildinfo.GitSHA = restartBuildA
						params.OperationID = "retry-restored-config"
						restart, err = m.Restart(t.Context(), params)
						if err != nil {
							t.Fatal(err)
						}
						m.cfg.opsWG.Wait()
					}
					assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
					wantRestarts, wantStarts := 1, 0
					if path == "disconnected-stopped" {
						wantRestarts, wantStarts = 0, 1
					}
					assertPublicRecoveryEffects(t, runner, 2, 2, wantRestarts, restartBuildB, restartStartB)
					runner.mu.Lock()
					starts, epochs := runner.starts, append([]appwire.FencingEpoch(nil), runner.probeEpochs...)
					runner.mu.Unlock()
					record, _ := store.Record(restart.ID)
					if starts != wantStarts || len(epochs) == 0 || epochs[len(epochs)-1] != epochOf(record) {
						t.Fatalf("recovered process proof: starts=%d epochs=%v record=%+v", starts, epochs, record)
					}
				})
			})
		}
	}
}

func TestHostDeployEntryBindingSupersession(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	const nextBuild = "cccccccccccccccccccccccccccccccccccccccc"
	for _, sourceFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("source-fails=%t", sourceFails), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
				runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA,
					acknowledgeFirstWrite: true, firstWriteContract: []byte(`{`)}
				m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
				ch, err := manager.Ensure(t.Context(), host.Name)
				if err != nil {
					t.Fatal(err)
				}
				waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
				deploy := func(id string) appwire.HostDeployResponse {
					plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
					if err != nil {
						t.Fatal(err)
					}
					record, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: id})
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					return record
				}
				first := deploy("unfinished-original")
				assertRestartRecoveryRecord(t, store, first.ID, hostops.StateFailed)
				entry := host
				entry.SSH = "new-confirmed-entry.example"
				if err := os.WriteFile(m.cfg.configPath, []byte(planTestHubTOML([]hostreg.Host{entry})), 0o600); err != nil {
					t.Fatal(err)
				}
				buildinfo.GitSHA = nextBuild
				runner.mu.Lock()
				runner.launchResponse, runner.buildVersion, runner.failRestart = nil, nextBuild, true
				if sourceFails {
					runner.buildError = errors.New("compiler source unavailable")
				}
				runner.mu.Unlock()
				second := deploy("new-confirmed-deploy")
				assertRestartRecoveryRecord(t, store, second.ID, hostops.StateFailed)
				wantWrites, wantRestarts := 2, 1
				if sourceFails {
					wantWrites, wantRestarts = 1, 0
				}
				assertPublicRecoveryEffects(t, runner, 2, wantWrites, wantRestarts, restartBuildA, restartStartA)
				runner.mu.Lock()
				runner.failRestart, runner.buildError = false, nil
				runner.mu.Unlock()
				if err := ch.Close(); err != nil {
					t.Fatal(err)
				}
				if !sourceFails {
					waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
					assertPublicRecoveryEffects(t, runner, 2, 2, 2, nextBuild, "2026-01-03T00:00:00Z")
					return
				}
				// No authorized write happened. The old B intent and binding must
				// still refuse automatic recovery under the edited entry.
				waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
				assertPublicRecoveryEffects(t, runner, 2, 1, 0, restartBuildA, restartStartA)
				restart, err := m.Restart(t.Context(), appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "renew-original-build"})
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
				assertPublicRecoveryEffects(t, runner, 2, 1, 1, restartBuildB, restartStartB)
			})
		})
	}
}

func TestHostRestartEntryBindingAdmissionAndRenewal(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	for _, scenario := range []string{"dedup", "generation", "busy", "worker-refused", "renew-attached", "renew-disconnected"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildA, "", "snapshot"
				runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA, failRestart: true}
				m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
				ch, err := manager.Ensure(t.Context(), host.Name)
				if err != nil {
					t.Fatal(err)
				}
				waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
				params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "original-restart"}
				first, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, first.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 0, 0, 1, restartBuildA, restartStartA)
				setEntry := func(ssh string) {
					entry := host
					entry.SSH = ssh
					if err := os.WriteFile(m.cfg.configPath, []byte(planTestHubTOML([]hostreg.Host{entry})), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				setEntry("renewed.example")
				buildinfo.GitSHA = restartBuildB // Recovery must keep A and its original pin.
				fresh := params
				fresh.OperationID = "fresh-restart"
				switch scenario {
				case "dedup":
					replay, err := m.Restart(t.Context(), params)
					if err != nil || replay.ID != first.ID {
						t.Fatalf("dedup: record=%+v err=%v", replay, err)
					}
				case "generation":
					stale := fresh
					stale.Generation++
					_, err := m.Restart(t.Context(), stale)
					info, _, code := deployWireInfo(t, err)
					if info != appwire.ErrorStaleEntry || code != appwire.CodeConflict {
						t.Fatalf("stale generation: info=%s code=%d", info, code)
					}
				case "busy":
					release, err := manager.TryAcquire(host.Name, hostops.Holder{Kind: hostops.HolderPlan})
					if err != nil {
						t.Fatal(err)
					}
					_, err = m.Restart(t.Context(), fresh)
					release()
					info, _, code := deployWireInfo(t, err)
					if info != appwire.ErrorHostBusyTransient || code != appwire.CodeConflict {
						t.Fatalf("busy admission: info=%s code=%d", info, code)
					}
				case "worker-refused":
					m.cfg.gate = &editingGate{inner: manager, onAs: func() { setEntry("changed-after-admission.example") }}
					record, err := m.Restart(t.Context(), fresh)
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, record.ID, hostops.StateFailed)
					m.cfg.gate = manager
					setEntry("renewed.example")
				}
				renew := strings.HasPrefix(scenario, "renew-")
				if scenario != "renew-attached" {
					if err := ch.Close(); err != nil {
						t.Fatal(err)
					}
					waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
					assertPublicRecoveryEffects(t, runner, 0, 0, 1, restartBuildA, restartStartA)
				}
				runner.mu.Lock()
				runner.failRestart = renew
				runner.mu.Unlock()
				fresh.OperationID = "validated-renewal"
				record, err := m.Restart(t.Context(), fresh)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				if !renew {
					assertRestartRecoveryRecord(t, store, record.ID, hostops.StateComplete)
					assertPublicRecoveryEffects(t, runner, 0, 0, 2, restartBuildA, "2026-01-03T00:00:00Z")
					return
				}
				assertRestartRecoveryRecord(t, store, record.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 0, 0, 2, restartBuildA, restartStartA)
				setEntry("changed-again.example")
				runner.mu.Lock()
				runner.failRestart = false
				runner.mu.Unlock()
				if scenario == "renew-attached" {
					if err := ch.Close(); err != nil {
						t.Fatal(err)
					}
					waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
				}
				_, err = manager.Ensure(t.Context(), host.Name)
				info, data, code := deployWireInfo(t, err)
				var binding appwire.StaleEntryBinding
				if err := json.Unmarshal(data["binding"], &binding); err != nil || info != appwire.ErrorStaleEntry || code != appwire.CodeConflict || binding != appwire.StaleEntryBindingHubTOMLFingerprint {
					t.Fatalf("second drift refusal: info=%s code=%d binding=%s err=%v", info, code, binding, err)
				}
				assertPublicRecoveryEffects(t, runner, 0, 0, 2, restartBuildA, restartStartA)
				// Restore only the renewed entry, not the original. An automatic
				// Ensure must now recover A without a new explicit authorization.
				setEntry("renewed.example")
				if _, err := manager.Ensure(t.Context(), host.Name); err != nil {
					t.Fatal(err)
				}
				assertPublicRecoveryEffects(t, runner, 0, 0, 3, restartBuildA, "2026-01-04T00:00:00Z")
			})
		})
	}
}

func TestHostRestartRepairsDisconnectedDeployment(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
	for _, scenario := range []string{"protocol", "missing-flag", "malformed", "approved-replacement", "repair-retry", "fingerprint", "wrong-build", "wrong-process", "gate-handoff"} {
		run := func(t *testing.T) {
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
			releaseReconnect := make(chan struct{})
			var releaseOnce sync.Once
			unblockReconnect := func() { releaseOnce.Do(func() { close(releaseReconnect) }) }
			defer unblockReconnect()
			if scenario == "gate-handoff" {
				runner.failedEvent = func() { <-releaseReconnect }
			}
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
			if scenario == "gate-handoff" {
				_, err := m.Restart(t.Context(), params)
				if info, _, code := deployWireInfo(t, err); info != appwire.ErrorHostBusyTransient || code != appwire.CodeConflict {
					t.Fatalf("held reconnect admitted Restart: %v", err)
				}
				unblockReconnect()
				synctest.Wait()
			}
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
				m.cfg.recoverDeployment = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, guard func() error) (*sshconn.RestartResult, error) {
					recoveryCalled = true
					return recoverDeployment(ctx, entry, holder, guard)
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
		}
		t.Run(scenario, func(t *testing.T) { synctest.Test(t, run) })
	}
}

func TestHostRestartStartsStoppedDeployOnlyInstallation(t *testing.T) {
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildB, "", "snapshot"
	scenarios := []string{
		"matching", "controller-drift", "repair-needed", "unacknowledged", "occupied-port", "unknown-port", "start-retry",
		"wrong-build", "wrong-pin", "wrong-probe-process", "missing-probe-process", "wrong-probe-build",
		"known-predecessor", "known-predecessor-start-retry", "known-unchanged-process", "reconnect-only",
		"identity-failed", "identity-missing", "retry-identity-failed", "retry-identity-missing",
		"start-retry-occupied-port", "start-retry-unknown-port",
	}
	for _, supervisor := range []string{"systemd", "systemd-user", "launchd"} {
		for _, scenario := range []string{"matching", "retry-identity-failed", "known-predecessor", "known-unchanged-process", "occupied-port", "unknown-port", "wrong-probe-process", "missing-probe-process", "wrong-probe-build"} {
			scenarios = append(scenarios, supervisor+"/"+scenario)
		}
		scenarios = append(scenarios, supervisor+"-dev/matching")
	}
	for _, name := range scenarios {
		run := func(t *testing.T) {
			buildinfo.GitSHA, buildinfo.Channel = restartBuildB, "snapshot"
			target, initialStart := restartBuildB, restartStartA
			supervisor, scenario, supervised := strings.Cut(name, "/")
			if !supervised {
				supervisor, scenario = "", name
			}
			if base, dev := strings.CutSuffix(supervisor, "-dev"); dev {
				supervisor = base
				buildinfo.GitSHA, buildinfo.Channel = "", ""
				target = "dev"
			}
			retryStart := strings.HasPrefix(scenario, "start-retry") || scenario == "known-predecessor-start-retry" || strings.HasPrefix(scenario, "retry-identity-")
			runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: target, startedAt: initialStart,
				acknowledgeFirstWrite: scenario != "unacknowledged", firstWriteContract: []byte(`{`), noSupervisor: !supervised, supervisor: supervisor}
			initialVersion := target
			if strings.HasPrefix(scenario, "known-") {
				// Unlike deploy-only, this plan authorizes replacement while the
				// original process can still be identified before the failed write.
				initialVersion = restartBuildA
				runner.runningVersion = initialVersion
			}
			m, manager, store, host, events := publicRecoveryHub(t, runner, target)
			ch, err := manager.Ensure(t.Context(), host.Name)
			if err != nil {
				t.Fatal(err)
			}
			if target == "dev" {
				runner.mu.Lock()
				runner.startedAt, initialStart = "", ""
				runner.mu.Unlock()
			}
			plan, err := m.Plan(t.Context(), appwire.HostPlanParams{Name: host.Name})
			if err != nil {
				t.Fatal(err)
			}
			deploy, err := m.Deploy(t.Context(), appwire.HostDeployParams{Name: host.Name, Token: plan.Token, OperationID: "stopped-deploy-only"})
			if err != nil {
				t.Fatal(err)
			}
			m.cfg.opsWG.Wait()
			assertRestartRecoveryRecord(t, store, deploy.ID, hostops.StateFailed)
			assertPublicRecoveryEffects(t, runner, 1, 1, 0, initialVersion, initialStart)
			runner.mu.Lock()
			runner.runningVersion, runner.startedAt = "", ""
			runner.launchResponse = []byte(`{`)
			if scenario == "reconnect-only" {
				runner.launchResponse = nil
				runner.bridgeAttempts = make(chan struct{}, 1)
			}
			runner.mu.Unlock()
			if err := ch.Close(); err != nil {
				t.Fatal(err)
			}
			if scenario == "reconnect-only" {
				select {
				case <-runner.bridgeAttempts:
				case <-time.After(5 * time.Second):
					t.Fatal("normal reconnect did not attempt its bridge")
				}
				assertPublicRecoveryEffects(t, runner, 1, 1, 0, "", "")
				runner.mu.Lock()
				starts := runner.starts
				runner.mu.Unlock()
				if starts != 0 {
					t.Fatalf("normal reconnect promoted deploy-only into %d starts", starts)
				}
				return
			}
			waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
			// Matching bytes alone do not acknowledge an interrupted write. A
			// valid, acknowledged installation must not need another write.
			runner.mu.Lock()
			runner.diskVersion = target
			if scenario != "repair-needed" {
				runner.launchResponse = nil
			}
			switch scenario {
			case "occupied-port", "unknown-port":
				runner.portState = scenario
			case "start-retry", "known-predecessor-start-retry", "start-retry-occupied-port", "start-retry-unknown-port":
				runner.failStart = true
			case "identity-failed", "identity-missing", "retry-identity-failed", "retry-identity-missing":
				runner.startupIdentityFault = strings.TrimPrefix(scenario, "retry-")
				runner.failStart = strings.HasPrefix(scenario, "retry-")
			case "known-unchanged-process":
				runner.startTime = restartStartA
			case "wrong-build":
				runner.startVersion = restartBuildA
			case "wrong-pin":
				runner.healthPin = restartBuildA
			case "wrong-probe-process":
				runner.probeStart = restartStartA
			case "missing-probe-process":
				runner.omitProbeStart = true
			case "wrong-probe-build":
				runner.probeVersion = restartBuildA
			}
			runner.mu.Unlock()
			if scenario == "controller-drift" {
				buildinfo.GitSHA = restartBuildA
			}
			var recoveryResult *sshconn.RestartResult
			if strings.Contains(scenario, "identity-") {
				recoverDeployment := m.cfg.recoverDeployment
				m.cfg.recoverDeployment = func(ctx context.Context, entry hostreg.Host, holder hostops.Holder, guard func() error) (*sshconn.RestartResult, error) {
					result, err := recoverDeployment(ctx, entry, holder, guard)
					recoveryResult = result
					return result, err
				}
			}
			params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "start-deployed-installation"}
			restart, err := m.Restart(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			m.cfg.opsWG.Wait()
			wantStarts, wantWrites := 1, 1
			if scenario == "repair-needed" || scenario == "unacknowledged" {
				wantWrites = 2
			}
			switch scenario {
			case "occupied-port", "unknown-port", "start-retry", "known-predecessor-start-retry", "wrong-build", "wrong-pin", "retry-identity-failed", "retry-identity-missing", "start-retry-occupied-port", "start-retry-unknown-port":
				assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
				runner.mu.Lock()
				starts, writes := runner.starts, runner.writes
				runner.portState, runner.failStart = "", false
				if scenario == "wrong-build" || scenario == "wrong-pin" {
					// The owned start is already running. Repair its response and
					// settle it without launching or replacing another process.
					runner.runningVersion, runner.healthPin = target, ""
				}
				runner.mu.Unlock()
				failedStarts := 1
				if scenario == "occupied-port" || scenario == "unknown-port" {
					failedStarts = 0
				}
				if starts != failedStarts || writes != 1 {
					t.Fatalf("failed recovery effects: starts=%d writes=%d, want %d/1", starts, writes, failedStarts)
				}
				if retryStart {
					buildinfo.GitSHA = restartBuildA
				}
				if strings.HasPrefix(scenario, "start-retry-") {
					runner.mu.Lock()
					runner.portState = strings.TrimPrefix(scenario, "start-retry-")
					runner.mu.Unlock()
					params.OperationID = "retry-start-blocked-port"
					blocked, err := m.Restart(t.Context(), params)
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
					assertRestartRecoveryRecord(t, store, blocked.ID, hostops.StateFailed)
					assertPublicRecoveryEffects(t, runner, 1, 1, 0, "", "")
					runner.mu.Lock()
					starts = runner.starts
					runner.portState = ""
					runner.mu.Unlock()
					if starts != 1 {
						t.Fatalf("blocked recorded startup ran again: starts=%d", starts)
					}
				}
				params.OperationID = "retry-start-deployed-installation"
				restart, err = m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				if retryStart {
					wantStarts = 2
				}
			}
			if strings.Contains(scenario, "identity-") {
				assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 1, 1, 0, target, restartStartB)
				runner.mu.Lock()
				starts, observations := runner.starts, runner.healthAfterStart
				runner.startupIdentityFault = ""
				runner.healthPin = restartBuildA
				runner.mu.Unlock()
				if starts != wantStarts || observations != 2 {
					t.Fatalf("identity refusal: starts=%d health observations=%d, want %d/2", starts, observations, wantStarts)
				}
				// Health was accepted once before identity observation failed. The
				// original B intent must survive, even after the controller changes.
				buildinfo.GitSHA = restartBuildA
				recoveryResult = nil
				params.OperationID = "retry-start-wrong-pin"
				wrongPin, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, wrongPin.ID, hostops.StateFailed)
				assertPublicRecoveryEffects(t, runner, 1, 1, 0, target, restartStartB)
				if recoveryResult == nil || recoveryResult.TargetVersion != target || !recoveryResult.StartedAt.IsZero() {
					t.Fatalf("startup accepted a different pin after identity refusal: recovery=%+v", recoveryResult)
				}
				runner.mu.Lock()
				runner.healthPin = ""
				runner.mu.Unlock()
				recoveryResult = nil
				params.OperationID = "retry-start-identity-observation"
				restart, err = m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				if recoveryResult == nil || recoveryResult.TargetVersion != target || recoveryResult.StartedAt.Format(time.RFC3339) != restartStartB {
					t.Fatalf("owned startup did not survive identity refusal: recovery=%+v", recoveryResult)
				}
			}
			wantState := hostops.StateComplete
			if scenario == "wrong-probe-process" || scenario == "missing-probe-process" || scenario == "wrong-probe-build" || scenario == "known-unchanged-process" {
				wantState = hostops.StateFailed
			}
			wantStart := restartStartB
			if scenario == "known-unchanged-process" {
				wantStart = restartStartA
			}
			assertRestartRecoveryRecord(t, store, restart.ID, wantState)
			assertPublicRecoveryEffects(t, runner, wantWrites, wantWrites, 0, target, wantStart)
			runner.mu.Lock()
			starts, epochs := runner.starts, append([]appwire.FencingEpoch(nil), runner.probeEpochs...)
			runner.mu.Unlock()
			record, _ := store.Record(restart.ID)
			if starts != wantStarts || len(epochs) == 0 || epochs[len(epochs)-1] != epochOf(record) {
				t.Fatalf("startup/epoch: starts=%d epochs=%v record=%+v", starts, epochs, record)
			}
			waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
			repaired, ok := manager.ChannelIfAttached(host.Name)
			if !ok {
				t.Fatal("verified startup has no live channel")
			}
			if err := repaired.Close(); err != nil {
				t.Fatal(err)
			}
			waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
			assertPublicRecoveryEffects(t, runner, wantWrites, wantWrites, 0, target, wantStart)
			runner.mu.Lock()
			starts = runner.starts
			runner.mu.Unlock()
			if starts != wantStarts {
				t.Fatalf("supervisor repeated startup: starts=%d want=%d", starts, wantStarts)
			}
		}
		t.Run(name, func(t *testing.T) { synctest.Test(t, run) })
	}
}

func TestHostRestartRenewsStoppedReplacementCommand(t *testing.T) {
	// Not parallel: the controller build stamp is process-wide.
	sha, dirty, channel := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel
	t.Cleanup(func() { buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = sha, dirty, channel })
	var scenarios []string
	for _, mode := range []string{"bare", "systemd"} {
		for _, check := range []string{"matching", "occupied-port", "unknown-port", "wrong-pin", "old-process", "wrong-probe-process"} {
			scenarios = append(scenarios, mode+"/"+check)
		}
	}
	for _, scenario := range scenarios {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				mode, check, _ := strings.Cut(scenario, "/")
				buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.Channel = restartBuildA, "", "snapshot"
				runner := &restartRecoveryRunner{diskVersion: restartBuildA, runningVersion: restartBuildA, startedAt: restartStartA,
					noSupervisor: mode == "bare", bareProcess: mode == "bare", supervisor: mode, failRestart: mode == "systemd"}
				m, manager, store, host, events := publicRecoveryHub(t, runner, restartBuildB)
				ch, err := manager.Ensure(t.Context(), host.Name)
				if err != nil {
					t.Fatal(err)
				}
				waitPublicRecoveryEvent(t, events, sshconn.EventAttached)
				entry := host
				entry.SSH = "renewed.example"
				changed := []byte(planTestHubTOML([]hostreg.Host{entry}))
				runner.afterStop = func() {
					if err := os.WriteFile(m.cfg.configPath, changed, 0o600); err != nil {
						t.Error(err)
					}
				}
				params := appwire.HostRestartParams{Name: host.Name, Generation: host.Generation, IncarnationID: host.IncarnationID, OperationID: "stop-before-relaunch"}
				first, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				assertRestartRecoveryRecord(t, store, first.ID, hostops.StateFailed)
				wantKills, wantRestarts := 1, 0
				if mode == "systemd" {
					wantKills, wantRestarts = 0, 1
				}
				assertStopped := func() {
					t.Helper()
					assertPublicRecoveryEffects(t, runner, 0, 0, wantRestarts, "", "")
					runner.mu.Lock()
					defer runner.mu.Unlock()
					if runner.kills != wantKills || runner.starts != 0 {
						t.Fatalf("stopped effect: kills=%d starts=%d", runner.kills, runner.starts)
					}
				}
				assertStopped()
				if err := ch.Close(); err != nil {
					t.Fatal(err)
				}
				waitPublicRecoveryEvent(t, events, sshconn.EventFailed)
				assertStopped() // Automatic recovery must retain the original entry binding.
				buildinfo.GitSHA = restartBuildB
				runner.mu.Lock()
				runner.afterStop, runner.failRestart = nil, false
				switch check {
				case "occupied-port", "unknown-port":
					runner.portState = check
				case "wrong-pin":
					runner.healthPin = restartBuildB
				case "old-process":
					runner.startTime = restartStartA
				case "wrong-probe-process":
					runner.probeStart = restartStartA
				}
				runner.mu.Unlock()
				params.OperationID = "renew-saved-replacement"
				restart, err := m.Restart(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				m.cfg.opsWG.Wait()
				if check != "matching" {
					assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateFailed)
				}
				if check == "occupied-port" || check == "unknown-port" {
					assertStopped()
				}
				if check != "matching" && check != "wrong-probe-process" {
					// Clear only the injected observation. Recovery must retain the
					// saved command, pin and predecessor, and never stop another hub.
					runner.mu.Lock()
					runner.portState, runner.healthPin = "", ""
					if check == "old-process" {
						runner.startedAt = restartStartB
					}
					runner.mu.Unlock()
					params.OperationID = "retry-saved-replacement"
					restart, err = m.Restart(t.Context(), params)
					if err != nil {
						t.Fatal(err)
					}
					m.cfg.opsWG.Wait()
				}
				if check != "wrong-probe-process" {
					assertRestartRecoveryRecord(t, store, restart.ID, hostops.StateComplete)
				}
				assertPublicRecoveryEffects(t, runner, 0, 0, wantRestarts, restartBuildA, restartStartB)
				contents, err := os.ReadFile(m.cfg.configPath)
				if err != nil || !bytes.Equal(contents, changed) {
					t.Fatalf("selected entry was not retained: read error=%v contents=%q", err, contents)
				}
				record, _ := store.Record(restart.ID)
				runner.mu.Lock()
				defer runner.mu.Unlock()
				wantCommand := "systemctl restart evener-hub.service"
				if mode == "bare" {
					wantCommand = "nohup /opt/evener/bin/evener hub -addr 127.0.0.1:9180 -config /srv/recovered.toml >>/srv/recovered.log 2>&1 </dev/null &"
				}
				if runner.kills != wantKills || runner.starts != 1 || len(runner.launchCommands) != 1 || runner.launchCommands[0] != wantCommand {
					t.Fatalf("saved replacement replay: kills=%d starts=%d commands=%q", runner.kills, runner.starts, runner.launchCommands)
				}
				if len(runner.probeEpochs) == 0 || runner.probeEpochs[len(runner.probeEpochs)-1] != epochOf(record) || runner.afterProbes != 1 {
					t.Fatalf("replacement process proof: epochs=%v probes=%d record=%+v", runner.probeEpochs, runner.afterProbes, record)
				}
			})
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
	return publicRecoveryHubForHost(t, runner, runner, artifact, deployTestHost())
}

func publicRecoveryHubForHost(t *testing.T, runner *restartRecoveryRunner, remote sshconn.Runner, artifact string, host hostreg.Host) (*hubHostManager, *sshconn.Manager, *hostops.Store, hostreg.Host, <-chan sshconn.EventKind) {
	t.Helper()
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
	manager := sshconn.New(registry, sshconn.Options{Runner: remote, Stderr: io.Discard, BackoffBase: 10 * time.Millisecond,
		OnEvent: func(event sshconn.Event) {
			if event.Kind == sshconn.EventAttached || event.Kind == sshconn.EventFailed {
				events <- event.Kind
			}
			if event.Kind == sshconn.EventFailed && runner.failedEvent != nil {
				runner.failedEvent()
			}
		},
		BuildBinary: func(_ context.Context, _, _, out string) error {
			runner.mu.Lock()
			runner.builds++
			version, err := runner.buildVersion, runner.buildError
			runner.mu.Unlock()
			if err != nil {
				return err
			}
			if version != "" {
				return os.WriteFile(out, []byte(version), 0o700)
			}
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
				// Events are emitted while reconnect still holds the gate. Let the
				// producer finish before submitting another fail-fast operation.
				synctest.Wait()
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
	launchError                   error
	keepLaunchError               bool
	delayBridgeExit               bool
	writes, restarts, afterProbes int
	builds, starts                int
	buildVersion                  string
	buildError                    error
	acknowledgeFirstWrite         bool
	firstWriteContract            []byte
	failRepair                    bool
	failRestart                   bool
	interruptRestartVerification  bool
	missing                       bool
	missingError                  error
	noSupervisor                  bool
	bareProcess                   bool
	kills                         int
	afterStop                     func()
	launchCommands                []string
	supervisor                    string
	portState, startVersion       string
	startTime                     string
	healthPin                     string
	failStart, omitProbeStart     bool
	startupIdentityFault          string
	healthAfterStart              int
	bridgeAttempts                chan struct{}
	observeMutation               func()
	failedEvent                   func()
	probeEpochs                   []appwire.FencingEpoch
	streams                       []*probeFakeStdio
}

func (r *restartRecoveryRunner) Run(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := strings.Join(argv, " ")
	switch {
	case strings.HasSuffix(command, "uname -s"):
		if r.supervisor == "launchd" {
			return []byte("Darwin\n"), nil
		}
		return []byte("Linux\n"), nil
	case strings.HasSuffix(command, "uname -m"):
		if r.supervisor == "launchd" {
			return []byte("arm64\n"), nil
		}
		return []byte("x86_64\n"), nil
	case strings.Contains(command, "XDG_STATE_HOME"):
		return []byte("HOME=/home/ops\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
	case strings.HasSuffix(command, "id -u"):
		return []byte("1000\n"), nil
	case strings.Contains(command, "launch-check"):
		if r.launchError != nil {
			return nil, r.launchError
		}
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
		if r.noSupervisor || r.supervisor == "systemd-user" && !strings.Contains(command, "--user") {
			return nil, nil
		}
		if r.runningVersion == "" {
			if strings.Contains(command, "--user") && r.supervisor != "systemd-user" {
				return nil, nil
			}
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		}
		return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
	case strings.Contains(command, "launchctl list"):
		pid := "-"
		if r.runningVersion != "" {
			pid = "123"
		}
		return []byte(pid + " 0 com.example.evener-hub\n"), nil
	case strings.Contains(command, "lsof -ti :9180"):
		if r.portState == "unknown-port" {
			return nil, errors.New("port probe interrupted")
		}
		if r.bareProcess && r.runningVersion != "" {
			return []byte("4242\n"), nil
		}
		if r.portState == "occupied-port" || r.noSupervisor && r.runningVersion != "" {
			return []byte(sshconn.ListenerPresentMarker), nil
		}
		return []byte(sshconn.NoListenerMarker), nil
	case r.bareProcess && strings.Contains(command, "/proc/4242/cmdline"):
		return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00-config\x00/srv/recovered.toml\x00"), nil
	case r.bareProcess && strings.Contains(command, "lsof -nP -p 4242"):
		return []byte("n127.0.0.1:9180\n"), nil
	case r.bareProcess && strings.Contains(command, "ps -o user= -p 4242"):
		return []byte("ops\n"), nil
	case r.bareProcess && strings.Contains(command, "lsof -p 4242 -a -d 1,2"):
		return []byte("evener 4242 ops 1w REG 8,1 123 456 /srv/recovered.log\nevener 4242 ops 2w REG 8,1 123 456 /srv/recovered.log\n"), nil
	case r.bareProcess && strings.Contains(command, "kill -s 0 -- 4242"):
		return []byte("__sshconn_pid_gone__\n"), nil
	case r.bareProcess && strings.Contains(command, "kill -- 4242"):
		if r.observeMutation != nil {
			r.observeMutation()
		}
		r.kills++
		r.runningVersion, r.startedAt = "", ""
		if r.afterStop != nil {
			r.afterStop()
		}
		return nil, nil
	case strings.Contains(command, "nohup"),
		r.supervisor != "" && r.runningVersion == "" && (strings.Contains(command, "systemctl start") || strings.Contains(command, "systemctl --user start") || strings.Contains(command, "systemctl restart") || strings.Contains(command, "systemctl --user restart") || strings.Contains(command, "launchctl kickstart")):
		if r.observeMutation != nil {
			r.observeMutation()
		}
		r.starts++
		r.launchCommands = append(r.launchCommands, argv[len(argv)-1])
		if r.failStart {
			return nil, errors.New("startup interrupted")
		}
		r.runningVersion, r.startedAt = r.diskVersion, restartStartB
		if r.startVersion != "" {
			r.runningVersion = r.startVersion
		}
		if r.startTime != "" {
			r.startedAt = r.startTime
		}
		r.healthAfterStart = 0
		return nil, nil
	case strings.Contains(command, "systemctl start"):
		r.starts++
		r.runningVersion, r.startedAt = r.diskVersion, restartStartA
		return nil, nil
	case strings.Contains(command, "systemctl restart"), strings.Contains(command, "systemctl --user restart"), strings.Contains(command, "launchctl kickstart"):
		if r.observeMutation != nil {
			r.observeMutation()
		}
		r.restarts++
		if r.failRestart {
			if r.afterStop != nil {
				r.runningVersion, r.startedAt = "", ""
				r.afterStop()
			}
			return nil, errors.New("restart command interrupted")
		}
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
		if r.interruptRestartVerification && r.restarts > 0 {
			return nil, errors.New("replacement health observation interrupted")
		}
		if r.runningVersion == "" {
			return nil, errors.New("no listener")
		}
		pin := r.runningVersion
		if r.healthPin != "" {
			pin = r.healthPin
		}
		startedAt := r.startedAt
		if r.starts > 0 {
			r.healthAfterStart++
			if r.healthAfterStart >= 2 {
				switch r.startupIdentityFault {
				case "identity-failed":
					return nil, errors.New("startup identity observation interrupted")
				case "identity-missing":
					startedAt = ""
				}
			}
		}
		return fmt.Appendf(nil, `{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, r.runningVersion, pin, startedAt), nil
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
		if !r.keepLaunchError {
			r.launchError = nil
		}
		if r.writes == 1 {
			r.launchResponse = r.firstWriteContract
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}
}

func (r *restartRecoveryRunner) Start(context.Context, []string, io.Writer) (sshconn.Stdio, error) {
	r.mu.Lock()
	present := r.runningVersion != ""
	if r.bridgeAttempts != nil {
		select {
		case r.bridgeAttempts <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
	if !present {
		return nil, errors.New("no listener")
	}
	stream := probeBridge(func(params appwire.HostRunningParams) (appwire.HostRunningResponse, appwire.WireError) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.probeEpochs = append(r.probeEpochs, params.FencingEpoch)
		version, startedAt := r.runningVersion, r.startedAt
		if r.restarts > 0 || r.starts > 0 {
			r.afterProbes++
			if r.probeVersion != "" {
				version = r.probeVersion
			}
			if r.probeStart != "" {
				startedAt = r.probeStart
			}
			if r.omitProbeStart {
				startedAt = ""
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
