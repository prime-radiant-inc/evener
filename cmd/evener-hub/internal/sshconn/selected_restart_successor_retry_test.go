package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Process, supervisor and image commands are strict fixtures, not host runtime.
func TestSelectedRestartRetriesUnhealthySuccessor(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, name := range []string{"retry", "pinned-build", "healthy-successor", "wrong-build", "missing-image", "wrong-image", "changed-launch", "unrelated-process", "image-birth-changed", "wrong-pin", "no-next-env", "unexecuted-intent", "automatic-recovery"} {
			t.Run(platform+"/"+name, func(t *testing.T) {
				host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
				arch := "amd64"
				if platform == "darwin" {
					arch = "arm64"
				}
				fr, process := deployRunnerForFixture(t, platform, arch, func(int) ([]byte, error) { return launchContract("same-sha"), nil }, func(int) ([]byte, error) { return nil, errReplacementFixture })
				process.observe, process.before, process.after, process.darwin = nil, "same-sha", "same-sha", platform == "darwin"
				base := fr.runFn
				effects, imageReads := 0, 0
				var m *Manager
				fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
					remote := strings.Join(argv[12:], " ")
					if strings.Contains(remote, "cat >") || strings.Contains(remote, "nohup ") || strings.HasPrefix(remote, "kill -- ") || remote == "systemctl start evener-hub.service" || remote == "launchctl kickstart gui/1000/com.example.evener-hub" {
						t.Fatalf("unexpected write, signal or cold start: %s", remote)
					}
					if platform == "darwin" && remote == "test ! -L /home/dev/.local/state/evener/hub.lock && test -f /home/dev/.local/state/evener/hub.lock && stat -f '%d %i' /home/dev/.local/state/evener/hub.lock" {
						return []byte("1234 890\n"), nil
					}
					if platform == "darwin" && remote == "lsof -a -p 4343 -F pftDi" {
						return []byte("p4343\nf7\ntREG\nD0x4d2\ni890\n"), nil
					}
					if remote == processAliveRemote("4242") {
						return []byte(pidGoneMarker + "\n"), nil
					}
					if effects == 1 && name == "unrelated-process" && remote == hubArgvRemote("4343") {
						return []byte(process.exe + "\x00hub\x00--config\x00/other.toml\x00"), nil
					}
					if effects == 1 && name == "wrong-image" && remote == "lsof -a -p 4343 -d txt -F Di" {
						return []byte("p4343\nD0x4d2\ni999\n"), nil
					}
					if effects == 1 && name == "image-birth-changed" && imageReads >= 2 {
						if remote == ownerGenerationRemote("4343") {
							return []byte("kernel-boot\n13501\n"), nil
						}
						if remote == "ps -p 4343 -o lstart=" {
							return []byte("different process birth\n"), nil
						}
					}
					if effects == 1 && name == "changed-launch" && remote == serviceDefinitionRemote(supervisor{kind: supervisorSystemd, label: "evener-hub.service"}) {
						return []byte("9000 999\n"), nil
					}
					if effects == 1 && name == "changed-launch" && remote == launchdDefinitionRemote("1000", "com.example.evener-hub") {
						out, err := base(ctx, argv, in)
						return []byte(strings.ReplaceAll(string(out), "working directory = /home/dev", "working directory = /different")), err
					}
					if remote == "/proc/4343/exe launch-check --json" || platform == "darwin" && effects == 1 && remote == process.exe+" launch-check --json" {
						imageReads++
						if name == "missing-image" || platform == "linux" && name == "wrong-image" {
							return nil, errOwnerEvidence
						}
						if name == "wrong-build" {
							return launchContract("wrong-sha"), nil
						}
						return launchContract("same-sha"), nil
					}
					if remote == "systemctl restart evener-hub.service" || remote == "launchctl kickstart -k gui/1000/com.example.evener-hub" {
						effects++
						if effects == 2 {
							pending := m.pendingRestart(host.Name)
							if pending.selected == nil || pending.selected.pid != "4343" || pending.selected.report.Running != nil || pending.nextEnv == nil {
								t.Fatalf("unhealthy B not retained before replacement: %+v", pending)
							}
						}
						var out []byte
						var err error
						if platform == "darwin" {
							process.mutated = true
						} else {
							out, err = base(ctx, argv, in)
						}
						process.initialUnavailable = effects == 1 && name != "healthy-successor"
						return out, err
					}
					translated := append([]string(nil), argv...)
					if effects >= 2 {
						for i := range translated {
							translated[i] = strings.ReplaceAll(translated[i], "4444", "4343")
						}
					}
					out, err := base(ctx, translated, in)
					if effects >= 2 {
						out = []byte(strings.NewReplacer("4343", "4444", "13500", "14500", "boot-two", "boot-three").Replace(string(out)))
					}
					if strings.HasSuffix(remote, " hub endpoint --json") && effects >= 2 && err == nil {
						var r hubendpoint.Report
						if json.Unmarshal(out, &r) == nil && r.Running != nil {
							r.Running.StartedAt = r.Running.StartedAt.Add(time.Minute)
							out, err = json.Marshal(r)
						}
					}
					return out, err
				}
				m = newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "same-sha", DeployDisabled: true, sleep: func(context.Context, time.Duration) error { return nil }})
				host, _ = m.reg.Get(host.Name)
				facts, err := m.preflight(t.Context(), host)
				if err != nil {
					t.Fatal(err)
				}
				_, err = m.RestartForOperation(t.Context(), host, facts, nil)
				if name == "healthy-successor" {
					if err != nil || effects != 1 || m.pendingRestart(host.Name).active() {
						t.Fatalf("healthy path: err=%v effects=%d", err, effects)
					}
					return
				}
				if !errors.Is(err, ErrRestart) || effects != 1 || !process.mutated {
					t.Fatalf("first restart: err=%v effects=%d", err, effects)
				}
				before := m.pendingRestart(host.Name)
				if before.selected == nil || before.selected.pid != "4242" || before.nextEnv == nil {
					t.Fatalf("A intent lost: %+v", before)
				}
				if name == "unexecuted-intent" || name == "no-next-env" {
					// A different process must not turn an unexecuted intent into authority.
					if name == "unexecuted-intent" {
						before.selected.report.Running = nil
					}
					before.nextEnv = nil
				}
				if name == "pinned-build" || name == "wrong-pin" {
					before.expectedGitSHA = "same-sha"
					process.sha = "same-sha"
					if name == "wrong-pin" {
						before.expectedGitSHA = "other-sha"
					}
				}
				if name == "unexecuted-intent" || name == "no-next-env" || name == "pinned-build" || name == "wrong-pin" {
					m.mu.Lock()
					m.pendingRestarts[host.Name] = before
					m.mu.Unlock()
				}
				if name == "automatic-recovery" {
					err = m.recoverSelectedRestart(t.Context(), host, facts, before)
				} else {
					_, err = m.RestartForOperation(t.Context(), host, facts, nil)
				}
				after := m.pendingRestart(host.Name)
				if name == "retry" || name == "pinned-build" {
					if err != nil || effects != 2 || after.active() || imageReads < 2 {
						t.Fatalf("B to C retry: err=%v effects=%d images=%d pending=%+v", err, effects, imageReads, after)
					}
				} else if !errors.Is(err, errOwnerEvidence) || effects != 1 || !reflect.DeepEqual(before, after) {
					t.Fatalf("unsafe adoption: err=%v effects=%d pending preserved=%v", err, effects, reflect.DeepEqual(before, after))
				}
				if len(fr.recordedStarts()) != 0 {
					t.Fatal("restart attached a client")
				}
			})
		}
	}
}
