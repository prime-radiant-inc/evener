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
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestOperationRestartReconcilesFreshDeploymentDrift(t *testing.T) {
	for _, restart := range []bool{false, true} {
		for _, acknowledged := range []bool{false, true} {
			for _, drift := range []string{"unchanged", "cached-different", "compatible-build", "protocol", "protocol-refusal", "startup-flag", "unreadable", "repair-write-failure", "repair-wrong-build"} {
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
					recordConnectionOperations(t, m)
					facts, err := m.preflight(t.Context(), host)
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := m.DeployForOperation(t.Context(), host, facts, restart); err == nil {
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
					version, err := m.RestartForOperation(t.Context(), host, facts)
					if drift == "repair-write-failure" || drift == "repair-wrong-build" {
						wantErr := ErrDeploy
						if drift == "repair-wrong-build" {
							wantErr = errDeployUnstamped
						}
						pending = m.pendingRestart(host.Name)
						if !errors.Is(err, wantErr) || writes != 2 || restarts != 0 || running != "predecessor-build" || !pending.deployment || !pending.restart || pending.expectedVersion != requested || pending.replaced.startedAt.Format(time.RFC3339) != "2026-01-01T00:00:00Z" || pending.writeAcknowledged != (drift == "repair-wrong-build") {
							t.Fatalf("failed repair lost intent or replaced predecessor: err=%v writes=%d restarts=%d running=%q pending=%+v", err, writes, restarts, running, pending)
						}
						version, err = m.RestartForOperation(t.Context(), host, facts)
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
			_, after, err := m.DeployForOperation(t.Context(), host, facts, scenario != "uncertain-deploy-only")
			if (err != nil) != (scenario != "predecessor-probe") {
				t.Fatalf("deploy: err=%v writes=%d", err, writes)
			}
			if _, err := m.RestartForOperation(t.Context(), host, after); err != nil {
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
