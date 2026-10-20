package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

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
