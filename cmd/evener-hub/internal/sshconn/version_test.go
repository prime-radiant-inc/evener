package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func TestDetectSupervisorTable(t *testing.T) {
	launchdYes := []byte("PID\tStatus\tLabel\n1234\t0\tcom.example.evener-hub\n")
	launchdNo := []byte("PID\tStatus\tLabel\n1234\t0\tcom.apple.something\n")
	sysYes := []byte("evener-hub.service loaded active running Evener Hub\n")
	sysNo := []byte("sshd.service loaded active running OpenSSH server\n")
	userYes := []byte("evener-hub.service loaded active running Evener Hub\n")

	cases := []struct {
		name    string
		goos    string
		l, s, u []byte
		want    supervisor
	}{
		{"launchd present", "darwin", launchdYes, nil, nil, supervisor{supervisorLaunchd, "com.example.evener-hub"}},
		{"launchd absent", "darwin", launchdNo, nil, nil, supervisor{}},
		{"systemd present", "linux", nil, sysYes, nil, supervisor{supervisorSystemd, "evener-hub.service"}},
		{"systemd-user present", "linux", nil, sysNo, userYes, supervisor{supervisorSystemdUser, "evener-hub.service"}},
		{"bare", "linux", nil, sysNo, sysNo, supervisor{}},
		{"unknown os", "windows", nil, sysYes, userYes, supervisor{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectSupervisorFrom(tc.goos, tc.l, tc.s, tc.u); got != tc.want {
				t.Fatalf("detectSupervisorFrom = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSupervisorRestartRemote(t *testing.T) {
	cases := []struct {
		sup  supervisor
		want string
	}{
		{supervisor{supervisorLaunchd, "com.example.evener-hub"}, "launchctl kickstart -k gui/$(id -u)/com.example.evener-hub"},
		{supervisor{supervisorSystemd, "evener-hub.service"}, "systemctl restart evener-hub.service"},
		{supervisor{supervisorSystemdUser, "evener-hub.service"}, "systemctl --user restart evener-hub.service"},
		{supervisor{}, ""},
	}
	for _, tc := range cases {
		if got := tc.sup.restartRemote(); got != tc.want {
			t.Errorf("restartRemote() = %q, want %q", got, tc.want)
		}
	}
}

func TestParseLogPath(t *testing.T) {
	out := []byte("COMMAND  PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
		"evener  4242 dev     1w   REG    1,2      123  456 /home/dev/evener-hub.log\n" +
		"evener  4242 dev     2w   REG    1,2      123  456 /home/dev/evener-hub.log\n")
	got, ok := parseLogPath(out)
	if !ok || got != "/home/dev/evener-hub.log" {
		t.Fatalf("parseLogPath = (%q,%v), want (/home/dev/evener-hub.log,true)", got, ok)
	}

	// A tty destination (nothing redirected) yields no path.
	if _, ok := parseLogPath([]byte("evener 4242 dev 1u CHR 16,1 0t0 9 /dev/ttys003\n")); ok {
		t.Fatal("tty destination parsed as a log path")
	}
	// Header only.
	if _, ok := parseLogPath([]byte("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n")); ok {
		t.Fatal("header parsed as a log path")
	}
}

func TestRelaunchCommand(t *testing.T) {
	cmd := "/opt/evener/bin/evener hub -addr 0.0.0.0:9180 -evener /opt/evener/bin/evener"
	if got, want := relaunchCommand(cmd, "/home/dev/evener-hub.log"), "nohup "+cmd+" >>/home/dev/evener-hub.log 2>&1 </dev/null &"; got != want {
		t.Fatalf("relaunchCommand with log:\n got %q\nwant %q", got, want)
	}
	if got, want := relaunchCommand(cmd, ""), "nohup "+cmd+" </dev/null >/dev/null 2>&1 &"; got != want {
		t.Fatalf("relaunchCommand without log:\n got %q\nwant %q", got, want)
	}
}

func TestHubPort(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:9180": "9180",
		"0.0.0.0:8080":   "8080",
		"":               "9180",
		"bogus":          "9180",
	}
	for addr, want := range cases {
		if got := hubPort(addr); got != want {
			t.Errorf("hubPort(%q) = %q, want %q", addr, got, want)
		}
	}
}

// TestEnsureVersionMatchesAttachesWithoutDeploy covers acceptance criterion 3's
// first half: a matching version attaches directly, with no build or deploy or
// restart command.
func TestEnsureVersionMatchesAttachesWithoutDeploy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	buildCalled := false
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev", // cannedRun reports host version "dev"
		BuildBinary:               func(context.Context, string, string, string) error { buildCalled = true; return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if buildCalled {
		t.Fatal("build ran for a matching version")
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"cat >", "systemctl", "launchctl", "lsof", "kill ", "nohup", "command -v"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("matching version issued deploy/restart command: %v", argv)
			}
		}
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
}

// TestEnsureVersionDiffersDeploysRestartsThenAttaches covers acceptance
// criterion 3's second half: a differing version deploys, restarts, then
// attaches, in that order.
func TestEnsureVersionDiffersDeploysRestartsThenAttaches(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}

	var mu sync.Mutex
	var events []string
	var states []State
	record := func(s string) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
	}

	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case strings.Contains(joined, "XDG_STATE_HOME"):
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			case strings.Contains(joined, "test -d /opt/evener/bin"):
				return nil, nil
			case strings.Contains(joined, "list-units"):
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case strings.Contains(joined, "systemctl restart"):
				record("restart")
				return nil, nil
			case strings.Contains(joined, "api/health"):
				return []byte(`{"ok":true}`), nil
			case strings.Contains(joined, "cat >"):
				record("push")
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			record("start")
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		OnEvent: func(ev Event) {
			if ev.Kind == EventState {
				mu.Lock()
				states = append(states, ev.State)
				mu.Unlock()
			}
		},
		BuildBinary: func(_ context.Context, goos, goarch, out string) error {
			record("build")
			if goos != "linux" || goarch != "amd64" {
				return fmt.Errorf("build target %s/%s", goos, goarch)
			}
			return os.WriteFile(out, []byte("new-binary"), 0o755)
		},
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	idx := func(name string) int {
		for i, e := range events {
			if e == name {
				return i
			}
		}
		return -1
	}
	bi, pi, ri, si := idx("build"), idx("push"), idx("restart"), idx("start")
	if bi < 0 || pi < 0 || ri < 0 || si < 0 {
		t.Fatalf("missing phase(s) in %v", events)
	}
	if !(bi < pi && pi < ri && ri < si) {
		t.Fatalf("phases out of order (build<push<restart<start): %v", events)
	}

	wantStates := []State{StatePreflighting, StateDeploying, StateRestarting, StateAttaching}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("state sequence = %v, want %v", states, wantStates)
	}
}

// TestEnsureUnsupportedTargetDoesNotDeploy covers acceptance criterion 6: no
// build and no bridge for a target with no shipped build.
func TestEnsureUnsupportedTargetDoesNotDeploy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	override := map[string][]byte{"uname -s": []byte("Darwin\n"), "uname -m": []byte("x86_64\n")}
	fr := &fakeRunner{runFn: cannedRun(override), startFn: goodStartFn(t)}
	buildCalled := false
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               func(context.Context, string, string, string) error { buildCalled = true; return nil },
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrUnsupportedHost) {
		t.Fatalf("err = %v, want ErrUnsupportedHost", err)
	}
	if buildCalled {
		t.Fatal("deploy ran for an unsupported target")
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
}

// TestRestartBareRecoversPidArgvAndLog covers the bare-process fallback's pid
// discovery and relaunch construction, including log preservation.
func TestRestartBareRecoversPidArgvAndLog(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	var relaunchArgv []string

	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return nil, errors.New("exit status 1") // no listener: port clear
		case strings.Contains(joined, "ps -p 4242 -ww -o command"):
			return []byte("/opt/evener/bin/evener hub -addr 0.0.0.0:9180 -evener /opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "lsof -p 4242 -a -d 1,2"):
			return []byte("COMMAND  PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"evener  4242 dev     1w   REG    1,2      1  2 /home/dev/evener-hub.log\n" +
				"evener  4242 dev     2w   REG    1,2      1  2 /home/dev/evener-hub.log\n"), nil
		case strings.Contains(joined, "kill 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host); err != nil {
		t.Fatalf("restartBare: %v", err)
	}
	wantRemote := "nohup /opt/evener/bin/evener hub -addr 0.0.0.0:9180 -evener /opt/evener/bin/evener >>/home/dev/evener-hub.log 2>&1 </dev/null &"
	want := rawCommandArgv(m.opts, host, wantRemote)
	if !equalArgv(relaunchArgv, want) {
		t.Fatalf("relaunch argv:\n got %v\nwant %v", relaunchArgv, want)
	}
}

// TestRestartBareNoHubIsErrRestart surfaces "no running hub to restart" instead
// of starting a second hub.
func TestRestartBareNoHubIsErrRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
		return nil, errors.New("exit status 1")
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if err := m.restartBare(context.Background(), host); !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart", err)
	}
	if got := len(fr.recordedRuns()); got != 1 {
		t.Fatalf("Run calls = %d, want 1 (pid discovery only)", got)
	}
}
