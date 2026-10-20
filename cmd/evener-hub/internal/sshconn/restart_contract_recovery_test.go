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
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRestartRecoveryValidatesExecutableBeforeMutation(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		for _, predecessorAlive := range []bool{true, false} {
			for _, drift := range []string{"protocol", "startup-flag"} {
				t.Run(fmt.Sprintf("explicit=%t/alive=%t/%s", explicit, predecessorAlive, drift), func(t *testing.T) {
					protocol, flags := appwire.ProtocolVersion, []string{"api-log"}
					started := "2026-01-01T00:00:00Z"
					running, restarts, writes := true, 0, 0
					fr := deployRunner(t, func(int) ([]byte, error) {
						return json.Marshal(map[string]any{"protocol": protocol, "version": "dev", "launch_flags": flags})
					}, func(int) ([]byte, error) {
						if !running {
							return nil, errors.New("no listener")
						}
						return []byte(fmt.Sprintf(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":%q}`, started)), nil
					})
					run := fr.runFn
					fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
						command := strings.Join(argv, " ")
						switch {
						case strings.Contains(command, "systemctl restart"):
							restarts++
							if restarts == 1 {
								running = predecessorAlive
								return nil, errors.New("interrupted restart")
							}
							running, started = true, "2026-01-02T00:00:00Z"
							return nil, nil
						case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
							writes++
						}
						return run(ctx, argv, stdin)
					}
					host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
					m := newTestManager(t, testRegistry(t, host), fr, Options{DeployDisabled: true, controllerVersionOverride: "dev", sleep: func(context.Context, time.Duration) error { return nil }})
					host, _ = m.reg.Get(host.Name)
					recordConnectionOperations(t, m)
					facts, err := m.preflight(t.Context(), host)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := m.RestartForOperation(t.Context(), host, facts); !errors.Is(err, ErrRestart) {
						t.Fatalf("initial interruption: %v", err)
					}
					pending := m.pendingRestart(host.Name)
					if pending.command == "" || pending.start || restarts != 1 {
						t.Fatalf("restart intent: %+v, restarts=%d", pending, restarts)
					}
					wantErr := ErrProtocolIncompatible
					if drift == "protocol" {
						protocol = "incompatible-protocol"
					} else {
						flags, wantErr = nil, ErrLaunchContract
					}
					ch, err := m.ensureOnce(t.Context(), host, explicit)
					if ch != nil {
						defer func() { _ = ch.Close() }()
					}
					if !errors.Is(err, wantErr) || ch != nil || restarts != 1 || writes != 0 || running != predecessorAlive || started != "2026-01-01T00:00:00Z" || m.pendingRestart(host.Name) != pending {
						t.Fatalf("unsafe recovery: err=%v channel=%v restarts=%d writes=%d running=%t started=%s pending=%+v", err, ch, restarts, writes, running, started, m.pendingRestart(host.Name))
					}
					protocol, flags = appwire.ProtocolVersion, []string{"api-log"}
					ch, err = m.ensureOnce(t.Context(), host, explicit)
					if ch != nil {
						defer func() { _ = ch.Close() }()
					}
					if err != nil || ch == nil || restarts != 2 || writes != 0 || !running || started != "2026-01-02T00:00:00Z" || m.pendingRestart(host.Name).active() {
						t.Fatalf("repaired recovery: err=%v channel=%v restarts=%d writes=%d running=%t started=%s pending=%+v", err, ch, restarts, writes, running, started, m.pendingRestart(host.Name))
					}
				})
			}
		}
	}
}
