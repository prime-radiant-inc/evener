package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// The remote process, supervisor and endpoint reports are modeled independently.
// Only the actual restart command changes predecessor health in the happy path.
func TestSelectedRestartWithoutPredecessorHealth(t *testing.T) {
	for _, name := range []string{"unavailable-predecessor", "retry-unavailable-predecessor", "unhealthy-successor", "different-pid", "different-birth", "missing-capability", "healthy-predecessor"} {
		t.Run(name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fr, process := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				return launchContract("same-sha"), nil
			}, func(int) ([]byte, error) { return nil, errors.New("no legacy health probe") })
			process.observe, process.before, process.after = nil, "same-sha", "same-sha"
			process.initialUnavailable = name != "healthy-predecessor"
			base := fr.runFn
			var m *Manager
			retryReady := false
			effects, writes, imageProbes := 0, 0, 0
			var selected pendingRestartState
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if name == "retry-unavailable-predecessor" && remote == processAliveRemote("4242") {
					return []byte(pidAliveMarker + "\n"), nil
				}
				pending := m.pendingRestart(host.Name)
				if pending.selected != nil && selected.selected == nil {
					selected = pending
					if name == "different-pid" {
						process.mutated = true
					}
				}
				if name == "different-birth" && pending.selected != nil && remote == ownerGenerationRemote("4242") {
					return []byte("kernel-boot\n12501\n"), nil
				}
				if name == "retry-unavailable-predecessor" && !retryReady && pending.selected != nil && remote == ownerGenerationRemote("4242") {
					return nil, errOwnerEvidence
				}
				if remote == "/proc/4242/exe launch-check --json" {
					imageProbes++
					if name == "missing-capability" {
						return nil, errOwnerEvidence
					}
				}
				if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") {
					writes++
				}
				if remote == "systemctl start evener-hub.service" || strings.HasPrefix(remote, "kill -- ") {
					t.Fatalf("live predecessor used unexpected mutation: %s", remote)
				}
				if remote == "systemctl restart evener-hub.service" {
					effects++
					if pending.selected == nil || pending.nextEnv == nil || (pending.selected.report.Running == nil) != (name != "healthy-predecessor") {
						t.Fatalf("restart lost predecessor or validated next environment: %+v", pending)
					}
					out, err := base(ctx, argv, in)
					if name != "unhealthy-successor" {
						process.initialUnavailable = false
					}
					return out, err
				}
				return base(ctx, argv, in)
			}
			m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "same-sha", DeployDisabled: true, sleep: func(context.Context, time.Duration) error { return nil }})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			if name == "retry-unavailable-predecessor" {
				pending := m.pendingRestart(host.Name)
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || pending.selected == nil || pending.selected.report.Running != nil || pending.nextEnv != nil || !process.initialUnavailable {
					t.Fatalf("independent-evidence failure: err=%v effects=%d pending=%+v", err, effects, pending)
				}
				retryReady = true
				_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			}
			pending := m.pendingRestart(host.Name)
			switch name {
			case "different-pid", "different-birth":
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || selected.selected == nil || !reflect.DeepEqual(selected, pending) {
					t.Fatalf("changed predecessor: err=%v effects=%d pending preserved=%v", err, effects, reflect.DeepEqual(selected, pending))
				}
			case "missing-capability":
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || pending.active() {
					t.Fatalf("unproven image: err=%v effects=%d pending=%+v", err, effects, pending)
				}
			case "unhealthy-successor":
				if !errors.Is(err, ErrRestart) || effects != 1 || !pending.active() || pending.nextEnv == nil || pending.selected.report.Running != nil || !process.mutated {
					t.Fatalf("unhealthy successor: err=%v effects=%d pending=%+v", err, effects, pending)
				}
			default:
				if err != nil || effects != 1 || pending.active() || !process.mutated || process.initialUnavailable {
					t.Fatalf("verified successor: err=%v effects=%d pending=%+v", err, effects, pending)
				}
			}
			if name != "healthy-predecessor" && imageProbes == 0 {
				t.Fatal("unavailable predecessor had no independent image capability proof")
			}
			if writes != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatalf("unexpected write/attach: writes=%d attaches=%d", writes, len(fr.recordedStarts()))
			}
		})
	}
}
