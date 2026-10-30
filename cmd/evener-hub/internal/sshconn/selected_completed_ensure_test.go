package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestSelectedRestartRegistration(t *testing.T) {
	prior := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener", Generation: 1, IncarnationID: "original", PresenceEpoch: 1}
	for _, field := range []string{"unchanged", "host", "generation", "incarnation", "presence"} {
		t.Run(field, func(t *testing.T) {
			host := prior
			switch field {
			case "host":
				host.ConfigPath = "/different/hub.toml"
			case "generation":
				host.Generation++
			case "incarnation":
				host.IncarnationID = "replacement"
			case "presence":
				host.PresenceEpoch++
			}
			err := checkSelectedRestartRegistration(host, prior)
			if field == "unchanged" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("registration refusal: %v", err)
			}
		})
	}
}

func TestEnsureReconcilesSelectedRestartBeforeStartupContract(t *testing.T) {
	for _, contract := range []string{"missing-flag", "incompatible"} {
		for _, mode := range []string{"completed", "generation-changed", "old-process", "same-boot", "wrong-build", "unavailable"} {
			t.Run(contract+"/"+mode, func(t *testing.T) {
				host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
				recovery := false
				restarts, writes := 0, 0
				lost := errors.New("restart succeeded, reply lost")
				fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
					out := launchContract("same-sha")
					if recovery {
						if contract == "missing-flag" {
							out = []byte(strings.ReplaceAll(string(out), `"api-log"`, `"not-api-log"`))
						} else {
							var disk map[string]any
							if err := json.Unmarshal(out, &disk); err != nil {
								t.Fatal(err)
							}
							disk["protocol"] = "incompatible"
							return json.Marshal(disk)
						}
					}
					return out, nil
				}, func(int) ([]byte, error) { return nil, errors.New("no legacy health probe") })
				selected.observe, selected.before, selected.after = nil, "same-sha", "same-sha"
				base := fr.runFn
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") {
						writes++
					}
					out, err := base(ctx, argv, in)
					if remote == "systemctl restart evener-hub.service" {
						restarts++
						if err == nil {
							return out, lost
						}
					}
					if recovery && mode == "same-boot" && strings.Contains(remote, "hub endpoint --json") && err == nil {
						var report map[string]any
						if err := json.Unmarshal(out, &report); err != nil {
							t.Fatal(err)
						}
						if running, ok := report["running"].(map[string]any); ok {
							running["boot_id"] = "boot-one"
						}
						return json.Marshal(report)
					}
					return out, err
				}
				reads := completedRestartAuthenticatedBridge(t, fr)
				m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "same-sha", DeployDisabled: true})
				host, _ = m.reg.Get(host.Name)
				facts, err := m.preflight(t.Context(), host)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := m.RestartForOperation(t.Context(), host, facts, nil); !errors.Is(err, lost) {
					t.Fatalf("lost reply: %v", err)
				}
				pending := m.pendingRestart(host.Name)
				if pending.selected == nil || !selected.mutated || restarts != 1 {
					t.Fatalf("missing completed selected restart: pending=%+v restarts=%d", pending, restarts)
				}
				if pending.binding != nil {
					t.Fatal("restart without callback retained an effect binding")
				}
				retained := pending.selected.clone()
				pending.selected = &retained
				recovery = true
				switch mode {
				case "generation-changed":
					if err := m.reg.Update(host); err != nil {
						t.Fatal(err)
					}
					updated, _ := m.reg.Get(host.Name)
					before := host
					before.Generation = updated.Generation
					if updated.Generation <= host.Generation || !reflect.DeepEqual(before, updated) {
						t.Fatalf("update must change only generation: before=%+v after=%+v", host, updated)
					}
					host = updated
				case "old-process":
					selected.mutated = false
				case "wrong-build":
					selected.after = "other-sha"
				case "unavailable":
					selected.unavailable = true
				}
				ch, err := m.ensureOnce(t.Context(), host, false)
				if mode == "completed" {
					if err != nil || ch == nil || m.pendingRestart(host.Name).active() {
						t.Fatalf("completed reconnect: channel=%v err=%v pending=%+v", ch, err, m.pendingRestart(host.Name))
					}
					result, err := ch.Client().ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:read-proof"})
					if err != nil || result.Thread.SessionID != "read-proof" || result.Thread.Name != "completed-successor" || reads.Load() != 1 {
						t.Fatalf("authenticated successor read: result=%+v err=%v reads=%d", result, err, reads.Load())
					}
				} else {
					want := ErrLaunchContract
					if contract == "incompatible" {
						want = ErrProtocolIncompatible
					}
					if mode == "generation-changed" {
						want = errOwnerEvidence
					}
					if !errors.Is(err, want) || ch != nil || !reflect.DeepEqual(pending, m.pendingRestart(host.Name)) {
						t.Fatalf("unproven reconnect: channel=%v err=%v want=%v pending=%+v", ch, err, want, m.pendingRestart(host.Name))
					}
					if reads.Load() != 0 {
						t.Fatalf("refused reconnect performed %d authenticated reads", reads.Load())
					}
				}
				if restarts != 1 || writes != 0 {
					t.Fatalf("reconciliation mutated remote: restarts=%d writes=%d", restarts, writes)
				}
				wantAttach := 0
				if mode == "completed" {
					wantAttach = 1
				}
				if len(fr.recordedStarts()) != wantAttach {
					t.Fatalf("attach commands=%d want=%d", len(fr.recordedStarts()), wantAttach)
				}
			})
		}
	}
}
