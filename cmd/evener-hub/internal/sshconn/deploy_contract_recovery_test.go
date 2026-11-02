package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestDeploymentRecoveryRetainsUnverifiedContract(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "requested-sha"
	for _, approvedRestart := range []bool{false, true} {
		for _, predecessorAlive := range []bool{true, false} {
			for _, drift := range []string{"protocol", "startup-flag", "unknown", "absent", "restored-contract", "different-build"} {
				t.Run(fmt.Sprintf("restart=%t/alive=%t/%s", approvedRestart, predecessorAlive, drift), func(t *testing.T) {
					disk, served, pin := "host-A", "host-A", "old-sha"
					protocol, flags := appwire.ProtocolVersion, []string{"api-log"}
					started, present, unreadable := "2026-01-01T00:00:00Z", true, false
					absent, absentExit := false, exitStatus(t, 1)
					writes, restarts, bridges := 0, 0, 0
					fr := deployRunner(t, func(int) ([]byte, error) {
						if absent {
							return nil, absentExit
						}
						if unreadable {
							return []byte("unreadable launch contract"), nil
						}
						return json.Marshal(map[string]any{"hub_endpoint_schema": 1, "protocol": protocol, "version": disk, "launch_flags": flags})
					}, func(int) ([]byte, error) {
						if !present {
							return nil, errors.New("no listener")
						}
						return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, served, pin, started)), nil
					})
					run, start := fr.runFn, fr.startFn
					fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
						command := strings.Join(argv, " ")
						switch {
						case strings.Contains(command, "cat >"):
							writes++
							disk, protocol, flags, unreadable = "snapshot", appwire.ProtocolVersion, []string{"api-log"}, false
							absent = false
							if writes == 1 {
								switch drift {
								case "protocol":
									protocol = "incompatible-protocol"
								case "unknown":
									unreadable = true
								case "absent":
									absent = true
								default:
									flags = nil
								}
							}
						case strings.Contains(command, "systemctl restart"), strings.Contains(command, "systemctl start"):
							restarts++
							present, served, pin, started = true, disk, "requested-sha", "2026-01-02T00:00:00Z"
							return nil, nil
						case strings.Contains(command, "lsof -ti :9180"):
							return []byte(noListenerMarker), nil
						case strings.Contains(command, "if test -e"), strings.Contains(command, "if test -x"):
							if absent {
								return nil, absentExit
							}
							return nil, nil
						}
						return run(ctx, argv, stdin)
					}
					fr.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
						bridges++
						if !present {
							return nil, errors.New("no listener")
						}
						return start(ctx, argv, stderr)
					}
					host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
					m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "snapshot", BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
					host, _ = m.reg.Get(host.Name)
					recordConnectionOperations(t, m)
					facts, err := m.preflight(t.Context(), host)
					if err != nil {
						t.Fatal(err)
					}
					wantErr := ErrLaunchContract
					switch drift {
					case "protocol":
						wantErr = ErrProtocolIncompatible
					case "unknown":
						wantErr = ErrPreflightDecode
					case "absent":
						wantErr = ErrExecutableMissing
					}
					if _, _, err := m.DeployForOperation(t.Context(), registeredTestHost(t, m, host), facts, approvedRestart, nil, nil); !errors.Is(err, wantErr) {
						t.Fatalf("initial post-write validation: %v", err)
					}
					pending := m.pendingRestart(host.Name)
					if !pending.deployment || !pending.writeAcknowledged || pending.restart != approvedRestart || pending.expectedVersion != "snapshot" || pending.expectedGitSHA != "requested-sha" || writes != 1 || restarts != 0 {
						t.Fatalf("unverified deployment: pending=%+v writes=%d restarts=%d", pending, writes, restarts)
					}
					present = predecessorAlive
					settlesInstall := drift == "restored-contract" || drift == "different-build" || drift == "absent"
					wantWrites, wantBridges := 2, 1
					if drift == "restored-contract" || drift == "different-build" {
						flags = []string{"api-log"}
						if drift == "restored-contract" {
							wantWrites = 1
						} else {
							disk = "other-C"
						}
					}
					ch, err := m.ensureOnce(t.Context(), host, false)
					if ch != nil {
						defer func(ch *Channel) { _ = ch.Close() }(ch)
					}
					if !approvedRestart && settlesInstall {
						if (err == nil) != predecessorAlive || (ch != nil) != predecessorAlive || bridges != 1 || writes != wantWrites || restarts != 0 || present != predecessorAlive || served != "host-A" || started != "2026-01-01T00:00:00Z" || m.pendingRestart(host.Name).active() {
							t.Fatalf("deploy-only settlement replaced predecessor: err=%v channel=%v bridges=%d writes=%d restarts=%d present=%t served=%s started=%s pending=%+v", err, ch, bridges, writes, restarts, present, served, started, m.pendingRestart(host.Name))
						}
						wantBridges = 2
					} else if !approvedRestart || drift == "unknown" {
						if !errors.Is(err, wantErr) || ch != nil || bridges != 0 || writes != 1 || restarts != 0 || present != predecessorAlive || served != "host-A" || started != "2026-01-01T00:00:00Z" || !reflect.DeepEqual(m.pendingRestart(host.Name), pending) {
							t.Fatalf("lost installation ownership: err=%v channel=%v bridges=%d writes=%d restarts=%d present=%t served=%s started=%s pending=%+v, want %+v", err, ch, bridges, writes, restarts, present, served, started, m.pendingRestart(host.Name), pending)
						}
					}
					if !approvedRestart || drift == "unknown" {
						// Only this explicit Restart may promote deploy-only intent to replacement.
						version, restartErr := m.RestartForOperation(t.Context(), registeredTestHost(t, m, host), facts, nil)
						if restartErr != nil || version != "snapshot" {
							t.Fatalf("explicit repair: version=%s err=%v", version, restartErr)
						}
						ch, err = m.ensureOnce(t.Context(), host, false)
						if ch != nil {
							defer func(ch *Channel) { _ = ch.Close() }(ch)
						}
					}
					if err != nil || ch == nil || writes != wantWrites || restarts != 1 || bridges != wantBridges || !present || served != "snapshot" || pin != "requested-sha" || started != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name).active() {
						t.Fatalf("recovered deployment: err=%v channel=%v writes=%d restarts=%d bridges=%d present=%t served=%s pin=%s started=%s pending=%+v", err, ch, writes, restarts, bridges, present, served, pin, started, m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}
