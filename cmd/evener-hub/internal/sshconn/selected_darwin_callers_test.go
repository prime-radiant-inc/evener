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

// This is a declared remote process model, not Darwin execution. Every accepted
// command is enumerated, and only the launch command can change process state.
type darwinBootstrapFixture struct {
	running   bool
	launches  int
	lostReply bool
	candidate string
	boundary  string
	failure   string
	inspected bool
	cancel    context.CancelFunc
}

var errDarwinInspection = errors.New("fixture inspection transport failure")
var errDarwinPermission = errors.New("fixture inspection permission failure")

func (f *darwinBootstrapFixture) runner(t *testing.T, host hostreg.Host) *fakeRunner {
	t.Helper()
	report := func(candidate bool) []byte {
		r := remoteEndpointFixture(host.ConfigPath, "127.0.0.1:24519", "darwin-bootstrap-boot")
		r.Running.PID = 4343
		r.Running.Version = "release-selected"
		if !f.running {
			r.State, r.Running = "stopped", nil
		}
		if candidate && f.candidate == "different config" {
			r.ConfigPath, r.HubStateRoot = "/srv/other.toml", "/srv/other-state"
		}
		return []byte(mustOwnerJSON(t, r))
	}
	inspect := func() ([]byte, error) {
		f.inspected = true
		switch f.failure {
		case "transport":
			return nil, errDarwinInspection
		case "permission":
			return nil, errDarwinPermission
		case "malformed":
			return []byte("not-a-contract"), nil
		case "cancel":
			f.cancel()
			return nil, context.Canceled
		}
		panic("invalid fixture failure")
	}
	fr := &fakeRunner{startFn: goodStartFn(t)}
	fr.runFn = func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		switch remote {
		case "uname -s":
			return []byte("Darwin\n"), nil
		case "uname -m":
			return []byte("arm64\n"), nil
		case "id -u":
			return []byte("1000\n"), nil
		case envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case "/opt/evener launch-check --json", "/opt/evener launch-check --protocol evener-appwire-v7 --json":
			return []byte(strings.ReplaceAll(goodLaunchCheck, `"version":"dev"`, `"version":"release-selected"`)), nil
		case "/opt/evener hub endpoint --json --config /srv/selected.toml":
			return report(false), nil
		case "launchctl list":
			if f.candidate != "" {
				return []byte("-\t0\tcom.example.evener-hub-candidate\n"), nil
			}
			return []byte("PID\tStatus\tLabel\n"), nil
		case launchdDefinitionRemote("1000", "com.example.evener-hub-candidate"):
			program := "/opt/evener"
			if f.candidate == "different executable" {
				program = "/other/evener"
			}
			return []byte("program = " + program + "\nworking directory = /candidate\narguments = {\n" + program + "\nhub\n--config\n/srv/selected.toml\n}\nHOME => /home/dev\n"), nil
		case "pwd", "CDPATH='' cd -P -- /srv && pwd -P":
			return []byte("/srv\n"), nil
		case freshOwnerEnvironmentRemote, darwinEnvironmentRemote("4343"):
			return []byte("HOME=/home/dev\x00"), nil
		case resolveDeployCommand("/opt/evener"):
			if f.candidate != "" && f.boundary == "executable" {
				return inspect()
			}
			return []byte("/opt/evener\n"), nil
		case resolveDeployCommand("/other/evener"):
			return []byte("/other/evener\n"), nil
		case ownerContext("/candidate", []string{"HOME=/home/dev"}) + resolveDeployCommand("/srv/other.toml"):
			return []byte("/srv/other.toml\n"), nil
		case ownerContext("/candidate", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/other.toml 2>/dev/null || stat -L -f '%d %i' /srv/other.toml":
			return []byte("1 200\n"), nil
		case ownerContext("/candidate", []string{"HOME=/home/dev"}) + "stat -Lc '%d %i' /srv/selected.toml 2>/dev/null || stat -L -f '%d %i' /srv/selected.toml":
			return []byte("1 100\n"), nil
		case "CDPATH='' cd -P -- /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; /opt/evener hub endpoint --json --config /srv/selected.toml":
			return report(false), nil
		case "CDPATH='' cd -P -- /candidate || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; /opt/evener hub endpoint --json --config /srv/selected.toml":
			if f.boundary == "config" {
				return inspect()
			}
			return report(true), nil
		case "ps -p 4343 -o lstart=":
			return []byte("Fri Oct 16 10:00:01 2026\n"), nil
		case "ps -o user= -p 4343":
			return []byte("dev\n"), nil
		case "if [ -r /proc/4343/cmdline ]; then cat /proc/4343/cmdline; fi":
			return nil, nil
		case "ps -p 4343 -ww -o command=":
			return []byte("/opt/evener hub --config /srv/selected.toml\n"), nil
		case "lsof -a -p 4343 -d cwd -Fn":
			return []byte("p4343\nfcwd\nn/srv\n"), nil
		case "CDPATH='' cd -P -- /srv || exit; unset HOME XDG_CONFIG_HOME XDG_STATE_HOME EVENER_HUB_LISTEN_ADDR; export HOME=/home/dev; nohup /opt/evener hub --config /srv/selected.toml </dev/null >/dev/null 2>&1 &":
			f.launches++
			if f.launches == 1 && f.lostReply {
				f.running = true
				return nil, errReplacementFixture
			}
			if f.launches == 1 && f.failure != "" {
				return nil, errReplacementFixture
			}
			// A retry that ignored candidate inspection must be observable without
			// entering readiness polling or disguising the inspection failure.
			if f.candidate != "" && f.failure != "" {
				return nil, errReplacementFixture
			}
			f.running = true
			return nil, nil
		default:
			t.Fatalf("undeclared Darwin bootstrap command %q", remote)
			return nil, errors.New("undeclared command")
		}
	}
	return fr
}

func TestEnsureDarwinFreshAdHocBootstrap(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-first-reply=%v", lost), func(t *testing.T) {
			host := hostreg.Host{Name: "selected", SSH: "dev@remote", EvenerPath: "/opt/evener", ConfigPath: "/srv/selected.toml"}
			f := &darwinBootstrapFixture{lostReply: lost}
			fr := f.runner(t, host)
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected", sleep: func(context.Context, time.Duration) error { return nil }})
			testRestartRecorder(t, m)
			_, err := m.Ensure(t.Context(), host.Name)
			if lost {
				if !errors.Is(err, errReplacementFixture) || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
					t.Fatalf("lost start err=%v pending=%+v", err, m.pendingRestart(host.Name))
				}
				_, err = m.Ensure(t.Context(), host.Name)
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.launches != 1 || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
				t.Fatalf("launches=%d pending=%+v attaches=%d", f.launches, m.pendingRestart(host.Name), len(fr.recordedStarts()))
			}
			want := []string{"/opt/evener", "hub", "attach", "--stdio", "--config", "/srv/selected.toml"}
			if got := fr.recordedStarts()[0][12:]; !equalStrings(got, want) {
				t.Fatalf("attach=%v want=%v", got, want)
			}
		})
	}
}

func TestEnsureDarwinStoppedRetryCandidateInspection(t *testing.T) {
	for _, boundary := range []string{"executable", "config"} {
		for _, failure := range []string{"transport", "permission", "malformed", "cancel"} {
			t.Run(boundary+"/"+failure, func(t *testing.T) {
				host := hostreg.Host{Name: "selected", SSH: "dev@remote", EvenerPath: "/opt/evener", ConfigPath: "/srv/selected.toml"}
				f := &darwinBootstrapFixture{failure: failure}
				fr := f.runner(t, host)
				m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected", sleep: func(context.Context, time.Duration) error { return nil }})
				testRestartRecorder(t, m)
				if _, err := m.Ensure(t.Context(), host.Name); !errors.Is(err, errReplacementFixture) {
					t.Fatalf("first start=%v", err)
				}
				f.candidate, f.boundary = "matching", boundary
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				f.cancel = cancel
				_, err := m.Ensure(ctx, host.Name)
				if err == nil || !f.inspected || f.launches != 1 || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
					t.Fatalf("inspection err=%v inspected=%v launches=%d pending=%+v attaches=%d", err, f.inspected, f.launches, m.pendingRestart(host.Name), len(fr.recordedStarts()))
				}
				if failure == "transport" && !errors.Is(err, errDarwinInspection) {
					t.Fatalf("transport lost: %v", err)
				}
				if failure == "permission" && !errors.Is(err, errDarwinPermission) {
					t.Fatalf("permission lost: %v", err)
				}
				if failure == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			})
		}
	}
}

func TestEnsureDarwinStoppedRetryCandidateSelection(t *testing.T) {
	for _, candidate := range []string{"different executable", "different config", "matching"} {
		t.Run(candidate, func(t *testing.T) {
			host := hostreg.Host{Name: "selected", SSH: "dev@remote", EvenerPath: "/opt/evener", ConfigPath: "/srv/selected.toml"}
			f := &darwinBootstrapFixture{failure: "transport"}
			fr := f.runner(t, host)
			m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "release-selected", sleep: func(context.Context, time.Duration) error { return nil }})
			testRestartRecorder(t, m)
			if _, err := m.Ensure(t.Context(), host.Name); !errors.Is(err, errReplacementFixture) {
				t.Fatalf("first start=%v", err)
			}
			f.failure, f.candidate = "", candidate
			_, err := m.Ensure(t.Context(), host.Name)
			if candidate == "matching" {
				if !errors.Is(err, errOwnerEvidence) || f.launches != 1 || m.pendingRestart(host.Name).selected == nil || len(fr.recordedStarts()) != 0 {
					t.Fatalf("selected service err=%v launches=%d pending=%+v", err, f.launches, m.pendingRestart(host.Name))
				}
			} else if err != nil || f.launches != 2 || m.pendingRestart(host.Name).selected != nil || len(fr.recordedStarts()) != 1 {
				t.Fatalf("unrelated service err=%v launches=%d pending=%+v attaches=%d", err, f.launches, m.pendingRestart(host.Name), len(fr.recordedStarts()))
			}
		})
	}
}
