package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRestartRecoveryValidatesExecutableBeforeMutation(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "requested-sha"
	for _, ingress := range []string{"ensure", "reconnect", "restart"} {
		for _, disabled := range []bool{true, false} {
			for _, operation := range []string{"restart-alive", "restart-stopped", "start-stopped"} {
				for _, drift := range []string{"protocol", "protocol-pinned", "startup-flag", "absent", "unknown", "matching-build", "different-build", "different-build-wrong-live", "different-build-unknown-live", "completed-build"} {
					t.Run(fmt.Sprintf("%s/disabled=%t/%s/%s", ingress, disabled, operation, drift), func(t *testing.T) {
						buildinfo.GitSHA = "requested-sha"
						target, controller, expectedPin := "host-A", "controller-B", ""
						if drift == "protocol-pinned" {
							target, controller = "snapshot", "snapshot"
							// Bootstrap of a retained installation does not impose a controller pin.
							if operation != "start-stopped" {
								expectedPin = "requested-sha"
							}
						}
						protocol, flags, disk := appwire.ProtocolVersion, []string{"api-log"}, target
						started, served := "2026-01-01T00:00:00Z", disk
						running, effects, writes := operation != "start-stopped", 0, 0
						absent, unknown := false, false
						absentExit := exitStatus(t, 1)
						fr := deployRunner(t, func(int) ([]byte, error) {
							if absent {
								return nil, absentExit
							}
							if unknown {
								return []byte("unreadable launch contract"), nil
							}
							return json.Marshal(map[string]any{"protocol": protocol, "version": disk, "launch_flags": flags})
						}, func(int) ([]byte, error) {
							if !running {
								return nil, errors.New("no listener")
							}
							return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":"requested-sha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, served, started)), nil
						})
						run := fr.runFn
						fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
							command := strings.Join(argv, " ")
							switch {
							case strings.Contains(command, "systemctl restart"), strings.Contains(command, "systemctl start"):
								effects++
								if effects == 1 {
									running = operation == "restart-alive"
									return nil, errors.New("interrupted launch")
								}
								running, served, started = true, disk, fmt.Sprintf("2026-01-%02dT00:00:00Z", effects)
								return nil, nil
							case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
								writes++
							case strings.Contains(command, "if test -x"):
								if absent {
									return nil, absentExit
								}
								return nil, nil
							case strings.Contains(command, "lsof -ti :9180"):
								return []byte(noListenerMarker), nil
							}
							return run(ctx, argv, stdin)
						}
						host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
						m := newTestManager(t, testRegistry(t, host), fr, Options{DeployDisabled: disabled, controllerVersionOverride: controller, BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
						host, _ = m.reg.Get(host.Name)
						recordConnectionOperations(t, m)
						facts, preflightErr := m.preflight(t.Context(), host)
						if preflightErr != nil {
							t.Fatal(preflightErr)
						}
						var err error
						if operation == "start-stopped" {
							_, err = m.Ensure(t.Context(), host.Name)
						} else {
							_, err = m.RestartForOperation(t.Context(), host, facts, nil)
						}
						if !errors.Is(err, ErrRestart) {
							t.Fatalf("initial interruption: %v", err)
						}
						pending := m.pendingRestart(host.Name)
						if pending.command == "" || pending.deployment || pending.start != (operation == "start-stopped") || pending.expectedVersion != target || pending.expectedGitSHA != expectedPin || effects != 1 || writes != 0 {
							t.Fatalf("launch intent: %+v, effects=%d writes=%d", pending, effects, writes)
						}
						m.opts.controllerVersionOverride, buildinfo.GitSHA = "later-controller", "later-pin"
						var wantErr error
						switch drift {
						case "protocol", "protocol-pinned":
							protocol, wantErr = "incompatible-protocol", ErrProtocolIncompatible
						case "startup-flag":
							flags, wantErr = nil, ErrLaunchContract
						case "absent":
							absent, wantErr = true, ErrExecutableMissing
						case "unknown":
							unknown, wantErr = true, ErrPreflightDecode
						case "different-build":
							disk, wantErr = "other-C", ErrVersionMismatch
						case "different-build-wrong-live":
							disk, served, wantErr = "other-C", "unrelated-D", ErrVersionMismatch
						case "different-build-unknown-live":
							disk, started, wantErr = "other-C", "", ErrVersionMismatch
						case "completed-build":
							disk, running, started = "other-C", true, "2026-01-02T00:00:00Z"
						}
						beforeRunning, beforeStarted, beforeServed := running, started, served
						connect := func() (*Channel, error) {
							if ingress == "reconnect" {
								return m.ensureOnce(t.Context(), host, false)
							}
							if ingress == "restart" {
								if _, err := m.RestartForOperation(t.Context(), host, facts, nil); err != nil {
									return nil, err
								}
							}
							return m.Ensure(t.Context(), host.Name)
						}
						ch, err := connect()
						if ch != nil {
							defer func() { _ = ch.Close() }()
						}
						wantEffects := 2
						if drift == "completed-build" {
							wantEffects = 1
						}
						if wantErr != nil {
							refusedEffects := 1
							if running != beforeRunning || started != beforeStarted || served != beforeServed {
								t.Fatalf("invalid target mutated process: running=%t started=%s served=%s", running, started, served)
							}
							if !errors.Is(err, wantErr) || ch != nil || effects != refusedEffects || writes != 0 || m.pendingRestart(host.Name) != pending {
								t.Fatalf("unsafe recovery: err=%v channel=%v effects=%d writes=%d pending=%+v, want %+v", err, ch, effects, writes, m.pendingRestart(host.Name), pending)
							}
							protocol, flags, disk = appwire.ProtocolVersion, []string{"api-log"}, target
							absent, unknown = false, false
							running, started, served = operation == "restart-alive", "2026-01-01T00:00:00Z", target
							ch, err = connect()
							if ch != nil {
								defer func() { _ = ch.Close() }()
							}
						}
						if err != nil || ch == nil || effects != wantEffects || writes != 0 || !running || served != target || started != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name).active() {
							t.Fatalf("repaired recovery: err=%v channel=%v effects=%d writes=%d running=%t started=%s served=%s pending=%+v", err, ch, effects, writes, running, started, served, m.pendingRestart(host.Name))
						}
					})
				}
			}
		}
	}
}

func TestRestartRecoverySupervisedProcessProof(t *testing.T) {
	for _, label := range []string{"dev", "host-dirty"} {
		for _, mode := range []string{"dormant", "live", "occupied"} {
			for _, supervisor := range []string{"systemd", "systemd-user", "launchd"} {
				t.Run(label+"/"+mode+"/"+supervisor, func(t *testing.T) {
					effects, running := 0, false
					fr := deployRunner(t, func(int) ([]byte, error) { return launchContract(label), nil }, func(int) ([]byte, error) {
						if !running {
							return nil, errors.New("no health answer")
						}
						return fmt.Appendf(nil, `{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-02T00:00:00Z"}`, label), nil
					})
					run := fr.runFn
					fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
						command := strings.Join(argv, " ")
						switch {
						case strings.Contains(command, "list-units"):
							if supervisor == "systemd-user" != strings.Contains(command, "--user") {
								return nil, nil
							}
							state := "inactive dead"
							if mode == "live" {
								state = "active running"
							}
							return []byte("evener-hub.service loaded " + state + "\n"), nil
						case strings.Contains(command, "launchctl list"):
							pid := "-"
							if mode == "live" {
								pid = "123"
							}
							return []byte(pid + " 0 com.example.evener-hub\n"), nil
						case strings.Contains(command, "lsof -ti :9180"):
							if mode == "occupied" {
								return []byte(listenerPresentMarker), nil
							}
							return []byte(noListenerMarker), nil
						case strings.Contains(command, " restart "), strings.Contains(command, "launchctl kickstart"):
							effects++
							running = true
							return nil, nil
						}
						return run(ctx, argv, stdin)
					}
					host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
					m := newTestManager(t, testRegistry(t, host), fr, Options{DeployDisabled: true, sleep: func(context.Context, time.Duration) error { return nil }})
					facts, err := m.preflight(t.Context(), host)
					if err != nil {
						t.Fatal(err)
					}
					if supervisor == "launchd" {
						facts.OS = "darwin"
					}
					result, err := m.restartForOperation(t.Context(), host, facts, nil)
					switch mode {
					case "dormant":
						if err != nil || effects != 1 || result.TargetVersion != label || !result.ReplacedStartedAt.IsZero() || result.StartedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name).active() {
							t.Fatalf("dormant startup: result=%+v effects=%d err=%v pending=%+v", result, effects, err, m.pendingRestart(host.Name))
						}
					case "live":
						if !errors.Is(err, ErrRestart) || effects != 1 || !result.StartedAt.IsZero() || m.pendingRestart(host.Name).start {
							t.Fatalf("live unit invented startup proof: result=%+v effects=%d err=%v", result, effects, err)
						}
					case "occupied":
						if !errors.Is(err, ErrRestart) || effects != 0 || !result.StartedAt.IsZero() || m.pendingRestart(host.Name).active() {
							t.Fatalf("occupied unit started: result=%+v effects=%d err=%v", result, effects, err)
						}
					}
				})
			}
		}
	}
}
