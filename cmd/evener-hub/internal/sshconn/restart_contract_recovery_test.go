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
	for _, explicit := range []bool{true, false} {
		for _, disabled := range []bool{true, false} {
			for _, operation := range []string{"restart-alive", "restart-stopped", "start-stopped"} {
				for _, drift := range []string{"protocol", "protocol-pinned", "startup-flag", "absent", "unknown", "matching-build", "different-build"} {
					t.Run(fmt.Sprintf("explicit=%t/disabled=%t/%s/%s", explicit, disabled, operation, drift), func(t *testing.T) {
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
						var err error
						if operation == "start-stopped" {
							_, err = m.Ensure(t.Context(), host.Name)
						} else {
							facts, preflightErr := m.preflight(t.Context(), host)
							if preflightErr != nil {
								t.Fatal(preflightErr)
							}
							_, err = m.RestartForOperation(t.Context(), host, facts)
						}
						if !errors.Is(err, ErrRestart) {
							t.Fatalf("initial interruption: %v", err)
						}
						pending := m.pendingRestart(host.Name)
						if pending.command == "" || pending.deployment || pending.start != (operation == "start-stopped") || pending.expectedVersion != target || pending.expectedGitSHA != expectedPin || effects != 1 || writes != 0 {
							t.Fatalf("launch intent: %+v, effects=%d writes=%d", pending, effects, writes)
						}
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
							disk, wantErr = "other-C", ErrRestart
						}
						connect := func() (*Channel, error) {
							if explicit {
								return m.Ensure(t.Context(), host.Name)
							}
							return m.ensureOnce(t.Context(), host, false)
						}
						ch, err := connect()
						if ch != nil {
							defer func() { _ = ch.Close() }()
						}
						wantEffects := 2
						if wantErr != nil {
							refusedEffects := 1
							if drift == "different-build" {
								// A usable different build can launch, but cannot satisfy the recorded target.
								refusedEffects, wantEffects = 2, 3
							} else if running != (operation == "restart-alive") || started != "2026-01-01T00:00:00Z" || served != target {
								t.Fatalf("invalid target mutated process: running=%t started=%s served=%s", running, started, served)
							}
							if !errors.Is(err, wantErr) || ch != nil || effects != refusedEffects || writes != 0 || m.pendingRestart(host.Name) != pending {
								t.Fatalf("unsafe recovery: err=%v channel=%v effects=%d writes=%d pending=%+v, want %+v", err, ch, effects, writes, m.pendingRestart(host.Name), pending)
							}
							protocol, flags, disk = appwire.ProtocolVersion, []string{"api-log"}, target
							absent, unknown = false, false
							ch, err = connect()
							if ch != nil {
								defer func() { _ = ch.Close() }()
							}
						}
						if err != nil || ch == nil || effects != wantEffects || writes != 0 || !running || served != target || started != fmt.Sprintf("2026-01-%02dT00:00:00Z", wantEffects) || m.pendingRestart(host.Name).active() {
							t.Fatalf("repaired recovery: err=%v channel=%v effects=%d writes=%d running=%t started=%s served=%s pending=%+v", err, ch, effects, writes, running, started, served, m.pendingRestart(host.Name))
						}
					})
				}
			}
		}
	}
}
