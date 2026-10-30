package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestSelectedRestartUnavailablePredecessorRecovers(t *testing.T) {
	for _, name := range []string{"restart/same-predecessor", "restart/different-process", "ensure/same-predecessor", "ensure/different-process", "restart/stopped-after-unavailable", "restart/different-process-missing-flag", "ensure/different-process-missing-flag", "restart/stopped-after-unavailable-missing-flag", "ensure/stopped-after-unavailable-missing-flag"} {
		differentProcess, viaEnsure := strings.Contains(name, "/different-process"), strings.HasPrefix(name, "ensure/")
		cold, badContract := strings.Contains(name, "/stopped-after-unavailable"), strings.HasSuffix(name, "-missing-flag")
		t.Run(name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			unusable := false
			fr, process := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				out := launchContract("same-sha")
				if unusable {
					out = []byte(strings.ReplaceAll(string(out), `"api-log"`, `"not-api-log"`))
				}
				return out, nil
			}, func(int) ([]byte, error) { return nil, errors.New("no legacy health probe") })
			process.observe, process.before, process.after = nil, "same-sha", "same-sha"
			process.initialUnavailable = true
			base := fr.runFn
			var m *Manager
			allowReplacement := false
			effects, writes := 0, 0
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				// Retain an unexecuted intent by withholding independent birth
				// evidence at the stop boundary, not by withholding health.
				if !allowReplacement && m.pendingRestart(host.Name).active() && remote == ownerGenerationRemote("4242") {
					return nil, errOwnerEvidence
				}
				if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") {
					writes++
				}
				if remote == "systemctl restart evener-hub.service" {
					effects++
					owner := m.pendingRestart(host.Name).selected
					if owner == nil || owner.pid != "4242" || owner.report.Running == nil || owner.report.Running.PID != 4242 || owner.report.Running.BootID != "boot-one" {
						t.Fatalf("restart did not retain verified predecessor report: %+v", owner)
					}
				}
				if remote == "systemctl start evener-hub.service" {
					if !cold {
						t.Fatal("live predecessor retry attempted a cold start")
					}
					effects++
					saved := m.pendingRestart(host.Name)
					if saved.selected == nil || saved.selected.report.Running != nil || saved.nextEnv == nil {
						t.Fatalf("cold start lost its validated expected environment: %+v", saved)
					}
					out, err := base(ctx, argv, in)
					if err != nil {
						t.Fatal(err)
					}
					return out, errReplacementFixture
				}
				return base(ctx, argv, in)
			}
			reads := completedRestartAuthenticatedBridge(t, fr)
			m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "same-sha", DeployDisabled: true})
			if viaEnsure {
				testRestartRecorder(t, m)
			}
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			// Model a previously retained unexecuted intent explicitly. A fresh
			// read-only refusal now restores its prior state instead of retaining one.
			owner, err := m.resolveOwnedEndpoint(t.Context(), host, facts)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := m.prepareSelectedRestart(t.Context(), host, facts, owner, "same-sha", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := m.restartSelectedPending(t.Context(), host, facts, &retained, true); !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("unavailable independent-evidence refusal: %v", err)
			}
			pending := m.pendingRestart(host.Name)
			if pending.selected == nil || pending.selected.report.Running != nil || effects != 0 || writes != 0 || process.mutated {
				t.Fatalf("unavailable predecessor: pending=%+v effects=%d writes=%d mutated=%v", pending, effects, writes, process.mutated)
			}
			allowReplacement = true
			process.initialUnavailable = false
			process.mutated = differentProcess
			if cold {
				process.stopped = true
				if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, errReplacementFixture) {
					t.Fatalf("authorized cold start lost reply: %v", err)
				}
				if !m.pendingRestart(host.Name).active() || effects != 1 || process.stopped || !process.mutated {
					t.Fatalf("cold start not retained: effects=%d process=%+v pending=%+v", effects, process, m.pendingRestart(host.Name))
				}
			}
			unusable = badContract
			if viaEnsure {
				ch, ensureErr := m.ensureOnce(t.Context(), host, false)
				err = ensureErr
				if differentProcess {
					if ch != nil {
						t.Fatal("different process was attached")
					}
				} else if err == nil {
					if ch == nil {
						t.Fatal("recovered Ensure did not attach")
					}
					result, readErr := ch.Client().ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:read-proof"})
					if readErr != nil || result.Thread.SessionID != "read-proof" || reads.Load() != 1 {
						t.Fatalf("authenticated successor read: result=%+v err=%v reads=%d", result, readErr, reads.Load())
					}
				}
			} else {
				_, err = m.RestartForOperation(t.Context(), host, facts, nil)
			}
			if differentProcess {
				want := errOwnerEvidence
				if badContract {
					want = ErrLaunchContract
				}
				if !errors.Is(err, want) || effects != 0 || !reflect.DeepEqual(pending, m.pendingRestart(host.Name)) {
					t.Fatalf("different process: err=%v effects=%d retained=%v", err, effects, reflect.DeepEqual(pending, m.pendingRestart(host.Name)))
				}
			} else if err != nil || effects != 1 || !process.mutated || m.pendingRestart(host.Name).active() {
				t.Fatalf("healthy predecessor retry: err=%v effects=%d successor=%v pending=%v", err, effects, process.mutated, m.pendingRestart(host.Name).active())
			}
			wantAttach := 0
			if viaEnsure && !differentProcess {
				wantAttach = 1
			}
			if writes != 0 || len(fr.recordedStarts()) != wantAttach {
				t.Fatalf("unexpected write/attach: writes=%d attaches=%d", writes, len(fr.recordedStarts()))
			}
		})
	}
}
