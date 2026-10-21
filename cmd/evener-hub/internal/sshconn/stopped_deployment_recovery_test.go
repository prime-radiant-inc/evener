package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestStoppedDeploymentRecovery(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "requested-sha"
	for _, scenario := range []string{"connect", "reconnect", "interrupted-start", "wrong-pin", "occupied-port", "unknown-port"} {
		t.Run(scenario, func(t *testing.T) {
			disk, running, sha := "old-build", "old-build", "old-sha"
			present := true
			writes, starts := 0, 0
			interrupted := errors.New("injected transport interruption")
			fr := deployRunner(t, func(int) ([]byte, error) { return launchContract(disk), nil }, func(int) ([]byte, error) {
				if !present {
					return nil, interrupted
				}
				return []byte(fmt.Sprintf(`{"version":%q,"backend_git_sha":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`, running, sha)), nil
			})
			run := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				command := strings.Join(argv, " ")
				switch {
				case strings.Contains(command, "cat >"):
					writes++
					disk = "snapshot"
					if writes == 1 {
						return nil, interrupted
					}
				case strings.Contains(command, "list-units"):
					return nil, nil
				case strings.Contains(command, "lsof -ti :9180"):
					switch scenario {
					case "occupied-port":
						return []byte(listenerPresentMarker), nil
					case "unknown-port":
						return nil, interrupted
					default:
						return []byte(noListenerMarker), nil
					}
				case strings.Contains(command, "nohup"):
					starts++
					if scenario == "interrupted-start" && starts == 1 {
						return nil, interrupted
					}
					present, running, sha = true, disk, "requested-sha"
					if scenario == "wrong-pin" {
						sha = "foreign-sha"
					}
					return nil, nil
				}
				return run(ctx, argv, stdin)
			}
			fr.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
				if !present {
					return nil, interrupted
				}
				return goodStartFn(t)(ctx, argv, stderr)
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "snapshot", BuildBinary: writeStageBinary, sleep: func(context.Context, time.Duration) error { return nil }})
			recordConnectionOperations(t, m)
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.DeployForOperation(t.Context(), host, facts, true, nil); !errors.Is(err, interrupted) {
				t.Fatalf("deployment interruption: %v", err)
			}
			if pending := m.pendingRestart(host.Name); !pending.deployment || !pending.restart || pending.command != "" {
				t.Fatalf("deployment intent: %+v", pending)
			}
			present = false // The predecessor exits before recovery records a restart command.
			connect := func() (*Channel, error) {
				if scenario == "reconnect" {
					return m.ensureOnce(t.Context(), host, false)
				}
				return m.Ensure(t.Context(), host.Name)
			}
			ch, err := connect()
			switch scenario {
			case "occupied-port", "unknown-port", "wrong-pin", "interrupted-start":
				if !errors.Is(err, ErrRestart) || ch != nil || !m.pendingRestart(host.Name).active() {
					t.Fatalf("unverified recovery: channel=%v err=%v pending=%+v", ch, err, m.pendingRestart(host.Name))
				}
				if scenario == "occupied-port" || scenario == "unknown-port" {
					if starts != 0 {
						t.Fatalf("unproved free port launched %d hubs", starts)
					}
					return
				}
				if starts != 1 || !m.pendingRestart(host.Name).start {
					t.Fatalf("start recovery was not retained: starts=%d pending=%+v", starts, m.pendingRestart(host.Name))
				}
				if scenario == "wrong-pin" {
					sha = "requested-sha"
				}
				ch, err = connect()
			}
			wantStarts := 1
			if scenario == "interrupted-start" {
				wantStarts = 2
			}
			if err != nil || ch == nil || writes != 2 || starts != wantStarts || running != "snapshot" || sha != "requested-sha" || m.pendingRestart(host.Name).active() {
				t.Fatalf("recovery: channel=%v err=%v writes=%d starts=%d running=%s sha=%s pending=%+v", ch, err, writes, starts, running, sha, m.pendingRestart(host.Name))
			}
		})
	}
}
