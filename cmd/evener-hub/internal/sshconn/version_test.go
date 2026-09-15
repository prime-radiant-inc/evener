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
		wantErr bool
	}{
		{"launchd present", "darwin", launchdYes, nil, nil, supervisor{supervisorLaunchd, "com.example.evener-hub"}, false},
		{"launchd absent", "darwin", launchdNo, nil, nil, supervisor{}, false},
		{"systemd present", "linux", nil, sysYes, nil, supervisor{supervisorSystemd, "evener-hub.service"}, false},
		{"systemd-user present", "linux", nil, sysNo, userYes, supervisor{supervisorSystemdUser, "evener-hub.service"}, false},
		{"bare", "linux", nil, sysNo, sysNo, supervisor{}, false},
		{"unknown os", "windows", nil, sysYes, userYes, supervisor{}, false},
		{"launchd ambiguous", "darwin", []byte("PID\tStatus\tLabel\n1\t0\tcom.example.evener-hub\n2\t0\tcom.other.evener-hub\n"), nil, nil, supervisor{}, true},
		{"systemd ambiguous", "linux", nil, []byte("evener-hub.service loaded active running\nother-evener-hub.service loaded active running\n"), nil, supervisor{}, true},
		{"systemd-user ambiguous", "linux", nil, sysNo, []byte("evener-hub.service loaded active running\nother-evener-hub.service loaded active running\n"), supervisor{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectSupervisorFrom(tc.goos, tc.l, tc.s, tc.u)
			if (err != nil) != tc.wantErr {
				t.Fatalf("detectSupervisorFrom error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
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

	// Only stdout redirected: stderr would not be preserved, so no shared log.
	onlyStdout := []byte("evener 4242 dev 1w REG 1,2 123 456 /home/dev/evener-hub.log\n" +
		"evener 4242 dev 2u CHR 16,1 0t0 9 /dev/ttys003\n")
	if got, ok := parseLogPath(onlyStdout); ok {
		t.Fatalf("parseLogPath accepted a single descriptor: (%q,%v)", got, ok)
	}

	// Descriptors pointing at different files must not be collapsed onto the
	// first match.
	split := []byte("evener 4242 dev 1w REG 1,2 123 456 /home/dev/out.log\n" +
		"evener 4242 dev 2w REG 1,2 123 456 /home/dev/err.log\n")
	if got, ok := parseLogPath(split); ok {
		t.Fatalf("parseLogPath accepted mismatched descriptors: (%q,%v)", got, ok)
	}

	// A log path containing spaces must be recovered from the full NAME column,
	// not truncated to its last whitespace-delimited word: losing it redirects
	// the relaunched hub's output to /dev/null instead of preserving the log.
	spaced := []byte("evener 4242 dev 1w REG 1,2 123 456 /home/dev/my hub.log\n" +
		"evener 4242 dev 2w REG 1,2 123 456 /home/dev/my hub.log\n")
	if got, ok := parseLogPath(spaced); !ok || got != "/home/dev/my hub.log" {
		t.Fatalf("parseLogPath(spaced) = (%q,%v), want (/home/dev/my hub.log,true)", got, ok)
	}
}

func TestRelaunchCommand(t *testing.T) {
	argv := []string{"/opt/evener/bin/evener", "hub", "-addr", "0.0.0.0:9180", "-evener", "/opt/evener/bin/evener"}
	if got, want := relaunchCommand(argv, "/home/dev/evener-hub.log"), "nohup /opt/evener/bin/evener hub -addr 0.0.0.0:9180 -evener /opt/evener/bin/evener >>/home/dev/evener-hub.log 2>&1 </dev/null &"; got != want {
		t.Fatalf("relaunchCommand with log:\n got %q\nwant %q", got, want)
	}
	if got, want := relaunchCommand(argv, ""), "nohup /opt/evener/bin/evener hub -addr 0.0.0.0:9180 -evener /opt/evener/bin/evener </dev/null >/dev/null 2>&1 &"; got != want {
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
			case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "list-units"):
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case strings.Contains(joined, "systemctl restart"):
				record("restart")
				return nil, nil
			case strings.Contains(joined, "api/health"):
				return []byte(`{"version":"newsha"}`), nil
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

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// The attached channel must report the version now running, not the
	// pre-deploy version the preflight launch-check read from disk.
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel preflight version = %q, want %q after deploy+restart", got, "newsha")
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
	if bi >= pi || pi >= ri || ri >= si {
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

// TestEnsureDeploysBeforeEnforcingLaunchContract proves version auto-match runs
// before the launch-flag gate: a 04a-era host binary that predates a required
// flag (and whose version therefore differs) must be upgraded by the deploy, and
// the contract judged on the deployed build, not the one just replaced. Before
// the fix preflight returned ErrLaunchContract on the first launch-check and no
// deploy was ever attempted, permanently rejecting the host.
func TestEnsureDeploysBeforeEnforcingLaunchContract(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchChecks := 0
	built := false
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
				launchChecks++
				if launchChecks == 1 {
					// The on-disk 04a-era binary: old version, no api-log flag.
					return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":[]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			case strings.Contains(joined, "test -d /opt/evener/bin"):
				return nil, nil
			case strings.Contains(joined, "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "list-units"):
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case strings.Contains(joined, "systemctl restart"):
				return nil, nil
			case strings.Contains(joined, "api/health"):
				return []byte(`{"version":"newsha"}`), nil
			case strings.Contains(joined, "cat >"):
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			built = true
			return os.WriteFile(out, []byte("new-binary"), 0o755)
		},
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a host missing a required launch flag must be upgraded, not rejected)", err)
	}
	if !built {
		t.Fatal("no deploy happened for a host whose binary predates the required flag")
	}
	if launchChecks < 2 {
		t.Fatalf("launch-check calls = %d, want >= 2 (re-probe after the deploy)", launchChecks)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel preflight version = %q, want %q", got, "newsha")
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
			return []byte(noListenerMarker + "\n"), nil // lsof found no listener
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
		return []byte(noListenerMarker + "\n"), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if err := m.restartBare(context.Background(), host); !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart", err)
	}
	if got := len(fr.recordedRuns()); got != 1 {
		t.Fatalf("Run calls = %d, want 1 (pid discovery only)", got)
	}
}

// TestRestartBarePSInvocationSuppressesHeader proves the bare-process relaunch
// asks ps for the headerless form (`-o command=`). Without the trailing `=`,
// real GNU/BSD ps prepend a `COMMAND` line that becomes part of the relaunched
// argv, so the relaunch runs `nohup COMMAND` and discards the real command.
func TestRestartBarePSInvocationSuppressesHeader(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 0.0.0.0:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
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
	var psArgv []string
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "-ww -o ") {
			psArgv = argv
		}
	}
	if len(psArgv) == 0 {
		t.Fatal("no ps invocation recorded")
	}
	if !strings.Contains(strings.Join(psArgv, " "), "-o command=") {
		t.Fatalf("ps invocation %v does not request the headerless form (trailing =)", psArgv)
	}
}

// TestRestartBareStripsLeadingPSHeader proves the defensive half: even if ps
// emits a leading COMMAND line, it must not be folded into the relaunched argv.
func TestRestartBareStripsLeadingPSHeader(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	clean := "/opt/evener/bin/evener hub -addr 0.0.0.0:9180"
	killed := false
	var relaunchArgv []string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("COMMAND\n" + clean + "\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
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
	want := rawCommandArgv(m.opts, host, relaunchCommand([]string{"/opt/evener/bin/evener", "hub", "-addr", "0.0.0.0:9180"}, ""))
	if !equalArgv(relaunchArgv, want) {
		t.Fatalf("relaunch argv kept the ps header:\n got %v\nwant %v", relaunchArgv, want)
	}
}

// TestRestartBareQuotesRecoveredLogPath proves a log path carrying shell
// metacharacters is quoted in the relaunch, so it cannot inject a second
// command into the shell that starts the new hub.
func TestRestartBareQuotesRecoveredLogPath(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	const logPath = "/home/dev/evener-hub.log;rm"
	killed := false
	var relaunchArgv []string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 0.0.0.0:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return []byte("COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\n" +
				"evener 4242 dev 1w REG 1,2 1 2 " + logPath + "\n" +
				"evener 4242 dev 2w REG 1,2 1 2 " + logPath + "\n"), nil
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
	// The remote command is one ssh argument; join it so the assertion reads the
	// remote shell string the host will run.
	var remote string
	for _, a := range relaunchArgv {
		if strings.HasPrefix(a, "nohup ") {
			remote = a
		}
	}
	if remote == "" {
		t.Fatalf("no relaunch recorded in %v", relaunchArgv)
	}
	if !strings.Contains(remote, ">>"+shellQuote(logPath)) {
		t.Fatalf("relaunch does not quote the recovered log path: %q", remote)
	}
}

// TestFindHubPIDRejectsMultipleListeners proves more than one listener on the
// hub port is a loud failure, not a silent pick of the first pid: hub.lock
// guarantees one hub, so an ambiguous listing means something is wrong.
func TestFindHubPIDRejectsMultipleListeners(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "lsof -ti") {
			return []byte("4242\n4343\n"), nil
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	pid, err := m.findHubPID(context.Background(), host, port)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for multiple listeners", err)
	}
	if pid != "" {
		t.Fatalf("pid = %q, want empty on ambiguity", pid)
	}
	if !strings.Contains(err.Error(), "4242") || !strings.Contains(err.Error(), "4343") {
		t.Fatalf("error does not name both PIDs: %v", err)
	}
}

// TestWaitPortClearSurfacesProbeFailure proves a probe that could not run is
// not read as "port cleared": a transport failure must fail the wait instead of
// letting a relaunch race the old hub's lock.
func TestWaitPortClearSurfacesProbeFailure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "lsof -ti") {
			return []byte("ssh: connect to host alpha.example port 22: Connection refused\n"), errors.New("exit status 255")
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.waitPortClear(context.Background(), host, port); !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (a transport failure is not a clear port)", err)
	}
	if got := len(fr.recordedRuns()); got != 1 {
		t.Fatalf("Run calls = %d, want 1 (fail fast, no retry loop)", got)
	}
}

// TestRestartHubRejectsAmbiguousSupervisor proves an ambiguous service listing
// stops the restart before any service is touched, rather than restarting
// whichever unit happened to be listed first.
func TestRestartHubRejectsAmbiguousSupervisor(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running\nother-evener-hub.service loaded active running\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an ambiguous supervisor", err)
	}
	if !strings.Contains(err.Error(), "match") {
		t.Fatalf("error does not explain the ambiguity: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
			t.Fatalf("ambiguous supervisor still issued a restart: %v", argv)
		}
	}
}

// supervisorRestartRunner answers a linux systemd supervisor detection plus the
// restart command, and scripts the /api/health responses. health is consulted
// once per probe (0-based) so a test can model a stale hub that keeps answering
// before handing the port over to the deployed version.
func supervisorRestartRunner(restartErr error, health func(probe int) ([]byte, error)) (*fakeRunner, *int) {
	probe := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			return nil, restartErr
		case strings.Contains(joined, "api/health"):
			out, err := health(probe)
			probe++
			return out, err
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	return fr, &probe
}

// TestRestartHubRejectsStaleVersion proves the masked-restart failure mode: every
// health response is a valid hub body but reports the pre-deploy version, so
// restart verification must not accept it. Before the fix, waitHealthy returned
// on any non-empty body and restartHub reported success against the stale hub.
func TestRestartHubRejectsStaleVersion(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr, _ := supervisorRestartRunner(nil, func(int) ([]byte, error) {
		return []byte(`{"version":"oldsha"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (stale version must fail the restart)", err)
	}
	if !strings.Contains(err.Error(), "oldsha") || !strings.Contains(err.Error(), "newsha") {
		t.Fatalf("error does not name the stale and expected versions: %v", err)
	}
}

// TestRestartHubSurfacesFailedRestartCommand proves a failed restart command is
// surfaced even when the health endpoint reports the expected version: a
// healthy-looking hub must not mask the command that failed to restart it.
func TestRestartHubSurfacesFailedRestartCommand(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	restartErr := errors.New("exit status 1")
	fr, _ := supervisorRestartRunner(restartErr, func(int) ([]byte, error) {
		return []byte(`{"version":"newsha"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (failed restart command must surface)", err)
	}
	if !strings.Contains(err.Error(), "systemctl restart") || !errors.Is(err, restartErr) {
		t.Fatalf("error does not name the failed restart command: %v", err)
	}
}

// TestRestartHubWaitsForOldHubToHandOver proves the graceful-shutdown race is
// closed: the first health responses come from the old, still-draining hub and
// only a later probe reports the deployed version. Verification must ignore the
// stale responses and keep polling, not attach on the first answer.
func TestRestartHubWaitsForOldHubToHandOver(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr, probes := supervisorRestartRunner(nil, func(probe int) ([]byte, error) {
		if probe < 2 {
			return []byte(`{"version":"oldsha"}`), nil
		}
		return []byte(`{"version":"newsha"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}); err != nil {
		t.Fatalf("restartHub: %v", err)
	}
	if *probes < 2 {
		t.Fatalf("health probes = %d, want >= 2 (the stale response must not satisfy verification)", *probes)
	}
}

// TestHostAddrDrivesRestartAndHealthProbes proves the restart path uses the
// host's own listen address. channelArgv passes host.Addr as --addr, so the
// manager-wide Options.HubAddr default must not be used to find the listener or
// poll health: on a non-default port that kills the wrong process or polls the
// wrong service. Before the fix both probes read Options.HubAddr and this test
// failed with "unexpected remote command" for the :9999 probe.
func TestHostAddrDrivesRestartAndHealthProbes(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1:9999"}
	port := hubPort(host.Addr)
	killed := false
	var healthRemote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9999\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			healthRemote = joined
			return []byte(`{"version":"newsha"}`), nil
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
	if err := m.waitHealthy(context.Background(), host, "newsha"); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if !strings.Contains(healthRemote, "localhost:9999/api/health") {
		t.Fatalf("health probe used the wrong port: %q", healthRemote)
	}
}

// TestWaitHealthyQuotesPort proves a malformed Options.HubAddr cannot inject a
// command into the remote login shell through the health URL: the port is quoted
// like every other host-derived value in the package.
func TestWaitHealthyQuotesPort(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1:9180;id"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "/api/health") {
			return []byte(`{"version":"newsha"}`), nil
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.waitHealthy(context.Background(), host, "newsha"); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	remote := strings.Join(fr.recordedRuns()[0], " ")
	if !strings.Contains(remote, "localhost:'9180;id'/api/health") {
		t.Fatalf("health URL does not quote the port: %q", remote)
	}
}

// TestRestartBareRefusesForeignProcessOnHubPort proves the bare fallback no
// longer kills whatever holds the port: a process whose recovered argv is not an
// evener hub is refused with ErrRestart and left alone, so a port collision
// cannot terminate an unrelated service.
func TestRestartBareRefusesForeignProcessOnHubPort(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/usr/sbin/nginx -g daemon off\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a foreign listener", err)
	}
	if !strings.Contains(err.Error(), "nginx") {
		t.Fatalf("error does not name the refused process: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill 4242") {
			t.Fatalf("refused process was still killed: %v", argv)
		}
	}
}

// TestRestartBareRefusesCompoundCommandLine proves a recovered line carrying an
// unquoted metacharacter is refused rather than tokenized by guesswork: the line
// is not a simple exec, so restarting it could run the trailing command.
func TestRestartBareRefusesCompoundCommandLine(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 0.0.0.0:9180; rm -rf /\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an untokenizable command line", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill 4242") {
			t.Fatalf("untokenizable process was still killed: %v", argv)
		}
	}
}

// TestRestartBareRefusesAddrMismatch proves the recovered argv's --addr must
// agree with the port the listener was found on: a mismatch means the listener is
// not the hub this host is configured for, so it is refused rather than killed.
func TestRestartBareRefusesAddrMismatch(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1:9999"}
	port := hubPort(host.Addr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 0.0.0.0:9180\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an --addr mismatch", err)
	}
	if !strings.Contains(err.Error(), "9999") || !strings.Contains(err.Error(), "9180") {
		t.Fatalf("error does not name both addresses: %v", err)
	}
}

// TestTokenizeCommandLine covers the recovered-argv tokenizer: quotes and
// escapes are honored, and a line that is not a simple exec is refused.
func TestTokenizeCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"plain", "evener hub -addr 0.0.0.0:9180", []string{"evener", "hub", "-addr", "0.0.0.0:9180"}, false},
		{"single quoted space", "evener hub -config '/opt/my hub.toml'", []string{"evener", "hub", "-config", "/opt/my hub.toml"}, false},
		{"double quoted", `evener hub -config "/opt/my hub.toml"`, []string{"evener", "hub", "-config", "/opt/my hub.toml"}, false},
		{"escaped space", `evener hub -config /opt/my\ hub.toml`, []string{"evener", "hub", "-config", "/opt/my hub.toml"}, false},
		{"semicolon refused", "evener hub; rm -rf /", nil, true},
		{"pipe refused", "evener hub | tee /tmp/x", nil, true},
		{"redirect refused", "evener hub >/tmp/x", nil, true},
		{"substitution refused", "evener hub $(id)", nil, true},
		{"unterminated single quote", "evener hub 'oops", nil, true},
		{"unterminated double quote", `evener hub "oops`, nil, true},
		{"trailing backslash", `evener hub \`, nil, true},
		{"empty", "   ", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenizeCommandLine(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("tokenizeCommandLine(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokenizeCommandLine(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestHubArgvFromCommandLine proves only a genuine `evener hub` invocation is
// accepted as the restart target.
func TestHubArgvFromCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"evener hub", "/opt/evener/bin/evener hub -addr 0.0.0.0:9180", false},
		{"symlinked evener", "/usr/local/bin/evener hub", false},
		{"not evener", "/usr/sbin/nginx -g daemon off", true},
		{"evener serve is not the hub", "/opt/evener/bin/evener serve", true},
		{"compound refused", "/opt/evener/bin/evener hub; rm -rf /", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hubArgvFromCommandLine(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("hubArgvFromCommandLine(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// TestHubAddrFlag proves the --addr/-addr spellings are both recovered.
func TestHubAddrFlag(t *testing.T) {
	cases := []struct {
		argv []string
		want string
		ok   bool
	}{
		{[]string{"evener", "hub", "-addr", "0.0.0.0:9180"}, "0.0.0.0:9180", true},
		{[]string{"evener", "hub", "--addr", "0.0.0.0:9180"}, "0.0.0.0:9180", true},
		{[]string{"evener", "hub", "-addr=0.0.0.0:9180"}, "0.0.0.0:9180", true},
		{[]string{"evener", "hub", "--addr=0.0.0.0:9180"}, "0.0.0.0:9180", true},
		{[]string{"evener", "hub"}, "", false},
	}
	for _, tc := range cases {
		got, ok := hubAddrFlag(tc.argv)
		if got != tc.want || ok != tc.ok {
			t.Errorf("hubAddrFlag(%v) = (%q,%v), want (%q,%v)", tc.argv, got, ok, tc.want, tc.ok)
		}
	}
}

// TestDetectSupervisorIgnoresStoppedUnits proves a loaded-but-stopped service is
// not read as a live hub: treating it as supervised makes restartHub start a
// second hub that fails on hub.lock while an ad hoc hub keeps serving the old
// version. A stopped unit must fall through to the bare-process path.
func TestDetectSupervisorIgnoresStoppedUnits(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		l, s, u []byte
	}{
		{"systemd inactive", "linux", nil, []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil},
		{"systemd-user inactive", "linux", nil, []byte("sshd.service loaded active running\n"), []byte("evener-hub.service loaded inactive dead Evener Hub\n")},
		{"launchd not running", "darwin", []byte("PID\tStatus\tLabel\n-\t0\tcom.example.evener-hub\n"), nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := detectSupervisorFrom(tc.goos, tc.l, tc.s, tc.u)
			if err != nil {
				t.Fatalf("detectSupervisorFrom: %v", err)
			}
			if got.kind != supervisorNone {
				t.Fatalf("detectSupervisorFrom = %+v, want no supervisor for a stopped unit", got)
			}
		})
	}
}

// TestRestartHubLaunchdKickstartStatusIsAdvisory proves a nonzero `launchctl
// kickstart` status does not fail the restart when the hub afterwards reports the
// deployed version: docs/evener-hub-remote-operations.md:290-293 says an
// interrupted kickstart can report failure even when the restart succeeded, so
// the health probe is the real check. Before the fix restartHub returned
// ErrRestart without ever probing health.
func TestRestartHubLaunchdKickstartStatusIsAdvisory(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	kickErr := errors.New("exit status 1")
	probes := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "launchctl list"):
			return []byte("PID\tStatus\tLabel\n1234\t0\tcom.example.evener-hub\n"), nil
		case strings.Contains(joined, "launchctl kickstart"):
			return []byte("kickstart: job failed"), kickErr
		case strings.Contains(joined, "api/health"):
			probes++
			return []byte(`{"version":"newsha"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "darwin"}); err != nil {
		t.Fatalf("restartHub: %v (a failed kickstart status must not mask a successful restart)", err)
	}
	if probes == 0 {
		t.Fatal("health was never probed after a failed kickstart status")
	}
}

// TestRestartHubLaunchdKickstartFailureSurfacesWhenHealthFails proves the
// kickstart status is still reported as a diagnostic when the restart really did
// not take: the health probe is authoritative, but its failure names the command
// that also failed.
func TestRestartHubLaunchdKickstartFailureSurfacesWhenHealthFails(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	kickErr := errors.New("exit status 1")
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "launchctl list"):
			return []byte("PID\tStatus\tLabel\n1234\t0\tcom.example.evener-hub\n"), nil
		case strings.Contains(joined, "launchctl kickstart"):
			return []byte("kickstart: job failed"), kickErr
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"oldsha"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "darwin"})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart", err)
	}
	if !errors.Is(err, kickErr) || !strings.Contains(err.Error(), "kickstart") {
		t.Fatalf("error does not surface the failed kickstart: %v", err)
	}
}
