//go:build linux

package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestRetainedAdhocReinstallStoppedEvidence(t *testing.T) {
	for _, mode := range []string{"stopped-free", "other-lock-owner", "transient-generation", "malformed-generation", "absence-probe-failed", "absence-probe-malformed", "lock-probe-failed", "lock-probe-malformed", "lock-owner-at-write"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			lock, err := os.OpenFile(filepath.Join(root, "hub.lock"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			hold := func() {
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
			repair, boundary := false, false
			writes, starts, absentReads, lockReads := 0, 0, 0, 0
			probeErr := errors.New("generation probe transport failed")
			fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
				return launchContract("newsha"), nil
			}, func(int) ([]byte, error) {
				return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-01T00:00:00Z"}`), nil
			})
			selected.adhoc = true
			base := fr.runFn
			fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				switch {
				case remote == selectedLockRemote(root):
					if !repair {
						return []byte("4242\n"), nil
					}
					lockReads++
					if mode == "lock-probe-failed" {
						return nil, probeErr
					}
					if mode == "lock-probe-malformed" {
						return []byte("not a lock owner\n"), nil
					}
					// Execute the production query against a real temporary lock.
					// Any occupied result is this test's PID, not retained PID 4242.
					return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
				case repair && remote == ownerGenerationRemote("4242"):
					if mode == "malformed-generation" {
						return []byte("malformed\n"), nil
					}
					return nil, probeErr
				case repair && remote == processAliveRemote("4242"):
					absentReads++
					switch mode {
					case "transient-generation", "malformed-generation":
						return []byte(pidAliveMarker + "\n"), nil
					case "absence-probe-failed":
						return nil, probeErr
					case "absence-probe-malformed":
						return []byte("unknown\n"), nil
					default:
						return []byte(pidGoneMarker + "\n"), nil
					}
				case repair && strings.HasPrefix(remote, "if test -x "):
					if selected.executableAbsent {
						return nil, exitStatus(t, 1)
					}
					return nil, nil
				case strings.Contains(remote, "cat >"):
					writes++
					if !repair {
						return nil, errors.New("interrupted recorded write")
					}
					selected.executableAbsent = false
				case strings.Contains(remote, "nohup "), strings.HasPrefix(remote, "systemctl start "), strings.HasPrefix(remote, "systemctl restart "):
					starts++
				}
				out, err := base(ctx, argv, in)
				return []byte(strings.ReplaceAll(string(out), "/home/dev/.local/state/evener", root)), err
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: func(ctx context.Context, goos, goarch, file string) error {
				if repair {
					boundary = true
					if mode == "lock-owner-at-write" {
						hold()
					}
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
			if retained.selected == nil || !retained.deployment || writes != 1 || retained.selected.supervisor.kind != supervisorNone || retained.selected.report.HubStateRoot != root {
				t.Fatalf("missing ad-hoc retained intent: writes=%d pending=%+v", writes, retained)
			}
			repair, selected.stopped, selected.executableAbsent = true, true, true
			if mode == "other-lock-owner" {
				hold()
			}
			_, _, err = m.deployForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", UID: "1000", ExecutableMissing: true}, retained, nil)
			lockIndependent := mode == "stopped-free" || mode == "other-lock-owner" || mode == "lock-probe-failed" || mode == "lock-probe-malformed" || mode == "lock-owner-at-write"
			if lockIndependent {
				if err != nil || writes != 2 || m.pendingRestart(host.Name).active() {
					t.Fatalf("pinned repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
				}
				if lockReads != 0 {
					t.Fatalf("pinned repair inspected remote lock %d times", lockReads)
				}
			} else {
				if err == nil || writes != 1 || !reflect.DeepEqual(m.pendingRestart(host.Name), retained) {
					t.Fatalf("unproven stopped-root repair: err=%v writes=%d pending=%+v", err, writes, m.pendingRestart(host.Name))
				}
				if mode == "transient-generation" && !errors.Is(err, probeErr) {
					t.Fatalf("generation failure not propagated: %v", err)
				}
			}
			if err != nil && strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("refused because of a fixture gap: %v", err)
			}
			if starts != 0 || len(fr.recordedStarts()) != 0 {
				t.Fatal("repair started or attached a hub")
			}
			if mode == "lock-owner-at-write" && !boundary {
				t.Fatal("write-boundary lock replacement was not exercised")
			}
			t.Logf("writes=%d starts=%d absence proofs=%d lock proofs=%d boundary=%t", writes, starts, absentReads, lockReads, boundary)
		})
	}
}

func TestCrashedRetainedRepairStartsWithoutHealth(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	repair := false
	writes, starts, stops, lockReads := 0, 0, 0, 0
	fr, selected := deployRunnerForFixture(t, "linux", "amd64", func(int) ([]byte, error) {
		return launchContract("newsha"), nil
	}, func(int) ([]byte, error) { return nil, errors.New("health unavailable") })
	selected.adhoc, selected.observe = true, nil
	base := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		switch {
		case repair && selected.executableAbsent && remote == host.EvenerPath+" launch-check --protocol evener-appwire-v7 --json":
			return nil, exitStatus(t, 127)
		case repair && remote == selectedLockRemote("/home/dev/.local/state/evener"):
			lockReads++
			return nil, errors.New("lock inspection unavailable")
		case repair && remote == ownerGenerationRemote("4242"):
			return nil, exitStatus(t, 1)
		case repair && remote == processAliveRemote("4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case repair && strings.HasPrefix(remote, "if test -x "):
			return nil, exitStatus(t, 1)
		case strings.Contains(remote, "cat >"):
			writes++
			if !repair {
				return nil, errors.New("interrupted write")
			}
			selected.executableAbsent = false
		case strings.Contains(remote, "nohup "):
			starts++
			return nil, errReplacementFixture
		case strings.HasPrefix(remote, "kill -- "), strings.Contains(remote, "systemctl restart "), strings.Contains(remote, "kickstart -k "):
			stops++
			return nil, errors.New("unexpected stop")
		}
		return base(ctx, argv, in)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "newsha", BuildBinary: writeStageBinary})
	host, _ = m.reg.Get(host.Name)
	facts, err := m.preflight(t.Context(), host)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.deployForOperation(t.Context(), host, facts, m.deploymentIntent(true, hubIdentity{}), nil); err == nil {
		t.Fatal("initial write did not fail")
	}
	retained := m.pendingRestart(host.Name)
	if retained.selected == nil || !retained.deployment || writes != 1 {
		t.Fatalf("pending=%+v writes=%d", retained, writes)
	}
	repair, selected.stopped, selected.initialUnavailable, selected.executableAbsent = true, true, true, true
	_, err = m.RestartForOperation(t.Context(), host, Preflight{Host: host.Name, OS: "linux", Arch: "amd64", UID: "1000", ExecutableMissing: true}, nil)
	if !errors.Is(err, errReplacementFixture) || writes != 2 || starts != 1 || stops != 0 || lockReads != 0 || !m.pendingRestart(host.Name).active() {
		t.Fatalf("crashed repair/start: err=%v writes=%d starts=%d stops=%d lock queries=%d pending=%+v", err, writes, starts, stops, lockReads, m.pendingRestart(host.Name))
	}
}
