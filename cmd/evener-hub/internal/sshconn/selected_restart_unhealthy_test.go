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
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || !reflect.DeepEqual(pending, pendingRestartState{}) || !process.initialUnavailable {
					t.Fatalf("independent-evidence failure: err=%v effects=%d pending=%+v", err, effects, pending)
				}
				retryReady = true
				_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			}
			pending := m.pendingRestart(host.Name)
			switch name {
			case "different-pid", "different-birth":
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || selected.selected == nil || !reflect.DeepEqual(pending, pendingRestartState{}) {
					t.Fatalf("changed predecessor: err=%v effects=%d pending restored=%v", err, effects, reflect.DeepEqual(pending, pendingRestartState{}))
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

func TestSelectedRestartPendingPredecessorHangs(t *testing.T) {
	for _, name := range []string{"healthy-successor", "report-error", "different-pid", "different-birth", "unhealthy-successor", "capability-unavailable", "config-unavailable"} {
		t.Run(name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			fr, process := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				return launchContract("same-sha"), nil
			}, func(int) ([]byte, error) { return nil, errors.New("no legacy health probe") })
			process.observe, process.before, process.after = nil, "same-sha", "same-sha"
			base := fr.runFn
			var m *Manager
			retry, gate := false, false
			attempts, effects, writes, reports, imageProbes := 0, 0, 0, 0, 0
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if retry && remote == process.exe+" hub endpoint --json" {
					reports++
					// Recovery reads selection and owner first, then the final gate.
					// Change evidence at that exact read, not at a readiness poll.
					if reports == 3 {
						gate = true
						switch name {
						case "report-error":
							return nil, errReplacementFixture
						case "different-pid":
							process.mutated = true
						case "capability-unavailable":
							m.mu.Lock()
							delete(m.ownerWitnesses, host.Name)
							m.mu.Unlock()
						}
					}
				}
				if remote == processAliveRemote("4242") {
					return []byte(pidAliveMarker + "\n"), nil
				}
				if gate && name == "different-birth" && remote == ownerGenerationRemote("4242") {
					return []byte("kernel-boot\n12501\n"), nil
				}
				if gate && name == "capability-unavailable" && remote == "/proc/4242/exe launch-check --json" {
					imageProbes++
					return nil, errOwnerEvidence
				}
				if gate && name == "config-unavailable" && strings.HasPrefix(remote, "CDPATH=") && strings.Contains(remote, " hub endpoint ") {
					return nil, errOwnerEvidence
				}
				if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") {
					writes++
				}
				if remote == "systemctl start evener-hub.service" || strings.HasPrefix(remote, "kill -- ") {
					t.Fatalf("live predecessor used unexpected mutation: %s", remote)
				}
				if remote == "systemctl restart evener-hub.service" {
					attempts++
					if !retry {
						return nil, errReplacementFixture // Interrupted before any effect.
					}
					pending := m.pendingRestart(host.Name)
					if pending.selected == nil || pending.selected.report.Running == nil || pending.selected.report.Running.BootID != "boot-one" || pending.nextEnv == nil || !process.initialUnavailable {
						t.Fatalf("hung predecessor identity or validated intent lost: %+v", pending)
					}
					effects++
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
			if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, errReplacementFixture) {
				t.Fatalf("interrupted healthy selection: %v", err)
			}
			before := m.pendingRestart(host.Name)
			if before.selected == nil || before.selected.report.Running == nil || before.selected.report.Running.BootID != "boot-one" || attempts != 1 || effects != 0 || process.mutated {
				t.Fatalf("healthy predecessor was not retained before interruption: %+v", before)
			}
			retry, process.initialUnavailable = true, true
			_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			pending := m.pendingRestart(host.Name)
			switch name {
			case "different-pid", "different-birth", "capability-unavailable", "config-unavailable":
				if !errors.Is(err, errOwnerEvidence) || effects != 0 || attempts != 1 || !reflect.DeepEqual(before, pending) {
					t.Fatalf("unproven hung predecessor: err=%v effects=%d attempts=%d pending preserved=%v", err, effects, attempts, reflect.DeepEqual(before, pending))
				}
			case "unhealthy-successor":
				if !errors.Is(err, ErrRestart) || effects != 1 || attempts != 2 || !reflect.DeepEqual(before, pending) || !process.mutated {
					t.Fatalf("unhealthy successor: err=%v effects=%d attempts=%d pending preserved=%v", err, effects, attempts, reflect.DeepEqual(before, pending))
				}
			default:
				if err != nil || effects != 1 || attempts != 2 || pending.active() || !process.mutated || process.initialUnavailable {
					t.Fatalf("hung predecessor retry: err=%v effects=%d attempts=%d pending=%+v", err, effects, attempts, pending)
				}
			}
			if !gate || name == "capability-unavailable" && imageProbes != 1 || writes != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatalf("gate/effect evidence: gate=%v image probes=%d writes=%d attaches=%d", gate, imageProbes, writes, len(fr.recordedStarts()))
			}
		})
	}
}
