package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRefreshLaunchContractUpdatesAbsenceOnlyOnSuccessfulProbe(t *testing.T) {
	absent := exitStatus(t, 1)
	for _, missing := range []bool{false, true} {
		for _, probe := range []string{"valid", "malformed", "transport", "absent"} {
			t.Run(fmt.Sprintf("missing=%t/%s", missing, probe), func(t *testing.T) {
				host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
				fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
					command := strings.Join(argv, " ")
					if strings.Contains(command, "if test -x") {
						if probe == "absent" {
							return nil, absent
						}
						return nil, nil
					}
					if !strings.Contains(command, "launch-check") {
						t.Fatalf("unexpected command: %s", command)
					}
					switch probe {
					case "malformed":
						return []byte(`{`), nil
					case "transport", "absent":
						return nil, errors.New("interrupted probe")
					default:
						return launchContract("dev"), nil
					}
				}}
				m := newTestManager(t, testRegistry(t, host), fr, Options{})
				facts, err := m.refreshLaunchContract(t.Context(), host, Preflight{ExecutableMissing: missing})
				if probe == "valid" {
					if err != nil || facts.ExecutableMissing || !facts.LaunchCheckKnown || facts.Version != "dev" {
						t.Fatalf("successful probe: facts=%+v err=%v", facts, err)
					}
				} else if err == nil || facts.ExecutableMissing != missing || facts.LaunchCheckKnown {
					t.Fatalf("failed probe changed absence/unknown facts: facts=%+v err=%v", facts, err)
				}
				if probe == "absent" && !errors.Is(err, ErrExecutableMissing) {
					t.Fatalf("missing executable lost its classification: %v", err)
				}
			})
		}
	}
}

func TestRecoverDeploymentForRestartRequiresOwnGateAndRegistration(t *testing.T) {
	for _, scenario := range []string{"no-hold", "other-holder", "stale-registration"} {
		t.Run(scenario, func(t *testing.T) {
			writes := 0
			fr := deployRunner(t, func(int) ([]byte, error) {
				if writes > 0 {
					return []byte(`{`), nil
				}
				return launchContract("dev"), nil
			}, nil)
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				if strings.Contains(strings.Join(argv, " "), "cat >") {
					writes++
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			registry := testRegistry(t, host)
			host, _ = registry.Get(host.Name)
			m := newTestManager(t, registry, fr, Options{controllerVersionOverride: "dev", BuildBinary: writeStageBinary})
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, false, nil); err == nil || writes != 1 {
				t.Fatalf("expected failed acknowledged deployment: writes=%d err=%v", writes, err)
			}
			holder := attachUnderGateCaller()
			if scenario != "no-hold" {
				release, err := m.TryAcquire(host.Name, holder)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			wantErr := hostops.ErrGateNotHeld
			if scenario == "other-holder" {
				holder.OperationID = "another-operation"
			}
			if scenario == "stale-registration" {
				host.Generation++
				wantErr = ErrHostNotFound
			}
			runs := len(fr.recordedRuns())
			result, err := m.RecoverDeploymentForRestart(t.Context(), host, holder, nil)
			if !errors.Is(err, wantErr) || result != nil || len(fr.recordedRuns()) != runs || !m.pendingRestart(host.Name).deployment {
				t.Fatalf("recovery crossed guard or lost intent: result=%+v err=%v commands=%d→%d", result, err, runs, len(fr.recordedRuns()))
			}
		})
	}
}

func TestOperationRestartReconcilesFreshDeploymentDrift(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, acknowledged := range []bool{false, true} {
			for _, drift := range []string{"unchanged", "cached-different", "compatible-build", "protocol", "protocol-refusal", "startup-flag", "unreadable", "repair-write-failure", "repair-wrong-build", "guard-before-repair", "guard-after-repair"} {
				t.Run(fmt.Sprintf("restart=%t/acknowledged=%t/%s", restart, acknowledged, drift), func(t *testing.T) {
					const requested = "requested-build"
					disk, running, started := requested, "predecessor-build", "2026-01-01T00:00:00Z"
					if drift == "cached-different" {
						disk = "cached-build"
					}
					writes, restarts := 0, 0
					verificationFailed := false
					protocolRefused := false
					var badContract []byte
					fr := deployRunner(t, func(int) ([]byte, error) {
						if writes == 1 && acknowledged && !verificationFailed {
							// The write succeeded, but its verification did not. Even a
							// deploy-only request must retain its installation intent.
							verificationFailed = true
							return []byte(`{`), nil
						}
						if protocolRefused {
							return []byte("unsupported appwire protocol"), errors.New("launch-check refused")
						}
						if badContract != nil {
							return badContract, nil
						}
						return launchContract(disk), nil
					}, func(int) ([]byte, error) {
						return fmt.Appendf(nil, `{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, running, started), nil
					})
					run := fr.runFn
					fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
						switch command := strings.Join(argv, " "); {
						case strings.Contains(command, "cat >"):
							writes++
							if (writes == 1 && !acknowledged) || (writes == 2 && drift == "repair-write-failure") {
								return nil, errors.New("interrupted write")
							}
							disk, badContract, protocolRefused = requested, nil, false
							if writes == 2 && drift == "repair-wrong-build" {
								disk = "incorrect-install"
							}
						case strings.Contains(command, "systemctl restart"):
							restarts++
							running, started = disk, "2026-01-02T00:00:00Z"
						}
						return run(ctx, argv, stdin)
					}
					host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
					m := newTestManager(t, testRegistry(t, host), fr, Options{
						controllerVersionOverride: requested, BuildBinary: writeStageBinary,
						sleep: func(context.Context, time.Duration) error { return nil },
					})
					host, _ = m.reg.Get(host.Name)
					recordConnectionOperations(t, m)
					facts, err := m.preflight(t.Context(), host)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := m.DeployForOperation(t.Context(), host, facts, restart, nil); err == nil {
						t.Fatal("initial deployment unexpectedly verified")
					}
					pending := m.pendingRestart(host.Name)
					if !pending.deployment || pending.restart != restart || pending.writeAcknowledged != acknowledged || writes != 1 || restarts != 0 {
						t.Fatalf("initial deployment: pending=%+v writes=%d restarts=%d", pending, writes, restarts)
					}
					disk = "drifted-build"
					wantWrites := 2
					switch drift {
					case "unchanged", "cached-different":
						disk = requested
						if acknowledged {
							wantWrites = 1
						}
					case "protocol":
						badContract = []byte(`{"protocol":"other-protocol","version":"drifted-build","launch_flags":["api-log"]}`)
					case "protocol-refusal":
						protocolRefused = true
					case "startup-flag":
						badContract = fmt.Appendf(nil, `{"protocol":%q,"version":"drifted-build","launch_flags":[]}`, appwire.ProtocolVersion)
					case "unreadable":
						badContract = []byte(`{`)
					}
					refusal := errors.New("operation entry changed")
					restored := false
					guard := func() error {
						if !restored && (drift == "guard-before-repair" || drift == "guard-after-repair" && writes == 2) {
							return refusal
						}
						return nil
					}
					version, err := m.RestartForOperation(t.Context(), host, facts, guard)
					if strings.HasPrefix(drift, "guard-") {
						after := m.pendingRestart(host.Name)
						wantWrites, wantAcknowledged := 1, acknowledged
						wantRestart := pending.restart
						if drift == "guard-after-repair" {
							wantWrites, wantAcknowledged = 2, true
							wantRestart = true
							if after.replaced.version != "predecessor-build" || after.replaced.startedAt.Format(time.RFC3339) != "2026-01-01T00:00:00Z" {
								t.Fatalf("guard lost original predecessor: %+v", after)
							}
						} else if after.replaced != pending.replaced {
							t.Fatalf("pre-write guard changed predecessor: before=%+v after=%+v", pending, after)
						}
						if !errors.Is(err, refusal) || writes != wantWrites || restarts != 0 || running != "predecessor-build" || started != "2026-01-01T00:00:00Z" || !after.deployment || after.restart != wantRestart || after.start || after.command != "" || after.writeAcknowledged != wantAcknowledged || after.expectedVersion != pending.expectedVersion || after.expectedGitSHA != pending.expectedGitSHA {
							t.Fatalf("guard changed owned intent or process: err=%v writes=%d restarts=%d pending=%+v", err, writes, restarts, after)
						}
						if drift == "guard-before-repair" && after != pending {
							t.Fatalf("refused renewal changed pending effect: before=%+v after=%+v", pending, after)
						}
						m.opts.controllerVersionOverride = "later-controller"
						restored = true
						version, err = m.RestartForOperation(t.Context(), host, facts, nil)
					}
					if drift == "repair-write-failure" || drift == "repair-wrong-build" {
						wantErr := ErrDeploy
						if drift == "repair-wrong-build" {
							wantErr = errDeployUnstamped
						}
						pending = m.pendingRestart(host.Name)
						if !errors.Is(err, wantErr) || writes != 2 || restarts != 0 || running != "predecessor-build" || !pending.deployment || !pending.restart || pending.expectedVersion != requested || pending.replaced.startedAt.Format(time.RFC3339) != "2026-01-01T00:00:00Z" || pending.writeAcknowledged != (drift == "repair-wrong-build") {
							t.Fatalf("failed repair lost intent or replaced predecessor: err=%v writes=%d restarts=%d running=%q pending=%+v", err, writes, restarts, running, pending)
						}
						version, err = m.RestartForOperation(t.Context(), host, facts, nil)
						wantWrites = 3
					}
					if err != nil || version != requested || writes != wantWrites || restarts != 1 || disk != requested || running != requested || m.pendingRestart(host.Name).active() {
						t.Fatalf("fresh recovery: err=%v version=%q writes=%d restarts=%d disk=%q running=%q pending=%+v", err, version, writes, restarts, disk, running, m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}

func TestOperationRestartReconcilesDeployment(t *testing.T) {
	for _, scenario := range []string{"predecessor-probe", "uncertain-write", "uncertain-deploy-only"} {
		t.Run(scenario, func(t *testing.T) {
			requestedOnDisk, requestedRunning := false, false
			started := "2026-01-01T00:00:00Z"
			writes, restarts := 0, 0
			interrupted := errors.New("transport interrupted")
			fr := deployRunner(t, func(int) ([]byte, error) {
				return launchContract("dev"), nil
			}, func(call int) ([]byte, error) {
				if scenario == "predecessor-probe" && call == 0 {
					return nil, interrupted
				}
				return fmt.Appendf(nil, `{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, started), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				switch {
				case strings.Contains(command, "cat >"):
					writes++
					if scenario != "predecessor-probe" && writes == 1 {
						return nil, interrupted
					}
					requestedOnDisk = true
				case strings.Contains(command, "systemctl restart"):
					restarts++
					requestedRunning = requestedOnDisk
					started = "2026-01-02T00:00:00Z"
				}
				return run(ctx, argv, stdin)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{
				controllerVersionOverride: "dev", BuildBinary: writeStageBinary,
				sleep: func(context.Context, time.Duration) error { return nil },
			})
			recordConnectionOperations(t, m)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			_, after, err := m.DeployForOperation(t.Context(), host, facts, scenario != "uncertain-deploy-only", nil)
			if (err != nil) != (scenario != "predecessor-probe") {
				t.Fatalf("deploy: err=%v writes=%d", err, writes)
			}
			if _, err := m.RestartForOperation(t.Context(), host, after, nil); err != nil {
				t.Fatalf("restart: %v", err)
			}
			ch, err := m.Ensure(t.Context(), host.Name)
			wantWrites := 2
			if scenario == "predecessor-probe" {
				wantWrites = 1
			}
			if err != nil || ch == nil || writes != wantWrites || restarts != 1 || !requestedRunning || m.pendingRestart(host.Name).active() {
				t.Fatalf("recovery: err=%v channel=%v writes=%d restarts=%d requestedRunning=%v pending=%+v", err, ch, writes, restarts, requestedRunning, m.pendingRestart(host.Name))
			}
		})
	}
}
