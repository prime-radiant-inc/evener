package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/hubendpoint"
)

// Darwin process, lsof and launchd observations are modeled here. This does not
// establish that these commands work on a running macOS supervisor.
func TestRetainedDarwinReinstallOwnership(t *testing.T) {
	for _, mode := range []string{"live", "stopped-free", "launchd-live", "launchd-stopped", "generation-changed", "generation-after-read", "generation-unavailable", "absence-unavailable", "absence-malformed", "user-changed", "argv-changed", "cwd-changed", "environment-changed", "boot-changed", "record-unavailable", "launchd-unavailable", "now-supervised", "generation-at-write", "boot-at-write"} {
		t.Run(mode, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			repair, boundary := false, false
			writes, starts, systemd, lockReads, generations := 0, 0, 0, 0, 0
			probeErr := errors.New("Darwin evidence unavailable")
			fr, selected := deployRunnerForFixture(t, "darwin", "arm64", func(int) ([]byte, error) {
				return launchContract("newsha"), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.adhoc, selected.noSystemctl, selected.observe = true, true, nil
			if mode == "launchd-live" || mode == "launchd-stopped" {
				selected.adhoc = false
			}
			selected.before, selected.after = "oldsha", "oldsha"
			var recorded *hubendpoint.Record
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.HasPrefix(remote, "systemctl ") {
					systemd++
				}
				if repair {
					switch remote {
					case "id -u dev":
						return []byte("1000\n"), nil
					case "id -u other":
						return []byte("1001\n"), nil
					case "ps -p 4242 -o lstart=":
						generations++
						if mode == "stopped-free" || mode == "launchd-stopped" || mode == "absence-unavailable" || mode == "absence-malformed" || mode == "generation-unavailable" {
							return nil, probeErr
						}
						if mode == "generation-changed" || (mode == "generation-after-read" && generations == 2) || (mode == "generation-at-write" && boundary) {
							return []byte("another process birth\n"), nil
						}
					case processAliveRemote("4242"):
						if mode == "absence-unavailable" {
							return nil, probeErr
						}
						if mode == "absence-malformed" {
							return []byte("unknown\n"), nil
						}
						if mode == "generation-unavailable" {
							return []byte(pidAliveMarker + "\n"), nil
						}
						return []byte(pidGoneMarker + "\n"), nil
					case "ps -o user= -p 4242":
						if mode == "user-changed" {
							return []byte("other\n"), nil
						}
					case hubArgvRemote("4242"):
						if mode == "argv-changed" {
							return []byte("/opt/evener/bin/evener\x00hub\x00--config\x00/other.toml\x00"), nil
						}
					case "lsof -a -p 4242 -d cwd -Fn":
						if mode == "cwd-changed" {
							return []byte("p4242\nfcwd\nn/other\n"), nil
						}
					case darwinEnvironmentRemote("4242"):
						if mode == "environment-changed" {
							return []byte("HOME=/other\x00"), nil
						}
					case "launchctl list":
						if mode == "launchd-unavailable" {
							return nil, probeErr
						}
						if mode == "now-supervised" {
							return []byte("4242\t0\tcom.example.evener-hub\n"), nil
						}
					case "if test -f /home/dev/.local/state/evener/hub-endpoint.json && test ! -L /home/dev/.local/state/evener/hub-endpoint.json; then head -c 65537 < /home/dev/.local/state/evener/hub-endpoint.json; else exit 1; fi":
						if mode == "record-unavailable" {
							return nil, probeErr
						}
						record := *recorded
						if mode == "boot-changed" || (mode == "boot-at-write" && boundary) {
							record.BootID = "another-boot"
						}
						return json.Marshal(record)
					}
					if strings.HasPrefix(remote, "if test -x ") {
						if selected.executableAbsent {
							return nil, exitStatus(t, 1)
						}
						return nil, nil
					}
				}
				if strings.Contains(remote, "cat >") {
					writes++
					if !repair {
						return nil, errors.New("interrupted recorded write")
					}
					selected.executableAbsent = false
				}
				if strings.Contains(remote, "nohup ") || strings.HasPrefix(remote, "launchctl kickstart ") {
					starts++
				}
				return base(ctx, argv, in)
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, file string) error {
				if repair {
					boundary = true
				}
				return writeStageBinary(ctx, goos, goarch, file)
			}})
			host, _ = m.reg.Get(host.Name)
			facts, err := m.preflight(t.Context(), host)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.deployForOperation(t.Context(), host, facts, m.deploymentIntent(false, hubIdentity{}), nil); err == nil {
				t.Fatal("initial write did not retain a failed deployment")
			}
			retained := m.pendingRestart(host.Name)
			if retained.selected == nil || !retained.deployment || writes != 1 || retained.selected.report.Running == nil || (retained.selected.supervisor.kind == supervisorNone) != selected.adhoc {
				t.Fatalf("missing ad-hoc retained intent: writes=%d pending=%+v", writes, retained)
			}
			recorded = retained.selected.report.Running
			repair, selected.executableAbsent = true, true
			selected.stopped = mode == "stopped-free" || mode == "launchd-stopped"
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "darwin", Arch: "arm64", UID: "1000", ExecutableMissing: true}, retained, nil)
			if mode == "live" || mode == "stopped-free" || mode == "launchd-live" || mode == "launchd-stopped" {
				if err != nil || writes != 2 || m.pendingRestart(host.Name).active() {
					t.Fatalf("authorized Darwin repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
				}
				if lockReads != 0 {
					t.Fatalf("pinned write inspected a remote lock: %d", lockReads)
				}
			} else if err == nil || writes != 1 || !reflect.DeepEqual(m.pendingRestart(host.Name), retained) {
				t.Fatalf("unproven Darwin repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
			}
			if err != nil && strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("refused because of a fixture gap: %v", err)
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 || systemd != 0 {
				t.Fatalf("Darwin repair started/attached or queried systemd: starts=%d systemd=%d", starts, systemd)
			}
			if strings.HasSuffix(mode, "-at-write") && !boundary {
				t.Fatal("write-boundary replacement was not exercised")
			}
			t.Logf("writes=%d starts=%d lock proofs=%d birth reads=%d boundary=%t", writes, starts, lockReads, generations, boundary)
		})
	}
}
