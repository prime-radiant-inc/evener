package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/shellquote"
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
		uid  string
		want string
		ok   bool
	}{
		{supervisor{supervisorLaunchd, "com.example.evener-hub"}, "1000", "launchctl kickstart -k gui/1000/com.example.evener-hub", true},
		// The domain argument must be one bare-safe word: a missing or non-numeric
		// uid (the preflight probe records no numeric id) refuses the supervised
		// path. A `$(id -u)` spelling would be single-quoted into a literal by the
		// quoting rule and never reach launchctl.
		{supervisor{supervisorLaunchd, "com.example.evener-hub"}, "", "", false},
		{supervisor{supervisorLaunchd, "com.example.evener-hub"}, "$(id -u)", "", false},
		// A label outside [A-Za-z0-9_.-] is host-derived data and refuses the
		// supervised path rather than being interpolated into the remote shell.
		{supervisor{supervisorLaunchd, "com.example/evener hub"}, "1000", "", false},
		{supervisor{supervisorSystemd, "evener-hub.service"}, "", "systemctl restart evener-hub.service", true},
		{supervisor{supervisorSystemdUser, "evener-hub.service"}, "", "systemctl --user restart evener-hub.service", true},
		{supervisor{}, "", "", false},
	}
	for _, tc := range cases {
		if got, ok := tc.sup.restartRemote(tc.uid); got != tc.want || ok != tc.ok {
			t.Errorf("restartRemote(%q) = (%q,%v), want (%q,%v)", tc.uid, got, ok, tc.want, tc.ok)
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
	// A stamped controller and host carry a comparable identity; the identity-less
	// "dev" case is covered by TestEnsureDevBuildDeploysOncePerManager.
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`),
			"api/health":   []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`),
		}),
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
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
	launchChecks := 0
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
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, "XDG_STATE_HOME"):
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				// The preflight reads the stale on-disk binary; the re-probe after
				// the deploy reads the controller's freshly installed build.
				launchChecks++
				if launchChecks == 1 {
					return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
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
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			case strings.Contains(joined, "cat >"):
				record("push")
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			case strings.Contains(strings.Join(argv, " "), "command -v evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
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
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
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
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			case strings.Contains(joined, "cat >"):
				if stdin != nil {
					_, _ = io.ReadAll(stdin)
				}
				return nil, nil
			case strings.Contains(strings.Join(argv, " "), "command -v evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
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
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
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
	if err := m.restartBare(context.Background(), host, hubIdentity{}); !errors.Is(err, ErrRestart) {
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
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
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
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
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
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
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
	if !strings.Contains(remote, ">>"+shellquote.RemoteWord(logPath)) {
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
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
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
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
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
		return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
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
		return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
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
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		}
		return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{}); err != nil {
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
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			healthRemote = joined
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9999"}`), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
		t.Fatalf("restartBare: %v", err)
	}
	if err := m.waitHealthy(context.Background(), host, "newsha", "", hubIdentity{}); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if !strings.Contains(healthRemote, "127.0.0.1:9999/api/health") {
		t.Fatalf("health probe did not use the host's configured address: %q", healthRemote)
	}
}

// TestWaitHealthyQuotesPort proves a malformed Options.HubAddr cannot inject a
// command into the remote login shell through the health URL: the port is quoted
// like every other host-derived value in the package.
func TestWaitHealthyQuotesPort(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1:9180;id"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "/api/health") {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180;id"}`), nil
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.waitHealthy(context.Background(), host, "newsha", "", hubIdentity{}); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	remote := strings.Join(fr.recordedRuns()[0], " ")
	if !strings.Contains(remote, "'http://127.0.0.1:9180;id/api/health'") {
		t.Fatalf("health URL does not quote the address: %q", remote)
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
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a foreign listener", err)
	}
	if !strings.Contains(err.Error(), "nginx") {
		t.Fatalf("error does not name the refused process: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
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
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an untokenizable command line", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
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
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an --addr mismatch", err)
	}
	if !strings.Contains(err.Error(), "9999") || !strings.Contains(err.Error(), "9180") {
		t.Fatalf("error does not name both addresses: %v", err)
	}
}

// TestRestartBareRefusesSamePortDifferentHost proves restart validates the FULL
// normalized bind address, not just the port: a hub on 127.0.0.1:9180 does not
// own a host configured for 127.0.0.2:9180, so it must be refused rather than
// killed.
func TestRestartBareRefusesSamePortDifferentHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.2:9180"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a hub bound to a different loopback address", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:9180") || !strings.Contains(err.Error(), "127.0.0.2:9180") {
		t.Fatalf("error does not name both endpoints: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
			t.Fatalf("a hub on a different address was killed: %v", argv)
		}
	}
}

// TestRestartBareRefusesDefaultAddrAtNonDefaultEndpoint proves an invocation with
// no --addr is refused when the configured endpoint is not the hub default: the
// controller cannot read the host's hub.toml, so it cannot prove such a process
// owns 127.0.0.2:9180 rather than the default 127.0.0.1:9180.
func TestRestartBareRefusesDefaultAddrAtNonDefaultEndpoint(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.2:9180"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a no---addr hub at a non-default endpoint", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:9180") || !strings.Contains(err.Error(), "127.0.0.2:9180") {
		t.Fatalf("error does not name both the hub default and the configured endpoint: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
			t.Fatalf("a hub at the default address was killed for a non-default endpoint: %v", argv)
		}
	}
}

// TestRestartBareInspectsListenerAddress proves the restart inspects the
// listener's ACTUAL bound address rather than trusting the recovered argv alone:
// a hub whose invocation carries no --addr (its bind came from hub.toml, which
// the controller cannot read) is restarted when its socket owns the configured
// endpoint.
func TestRestartBareInspectsListenerAddress(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.2:9180"}
	killed := false
	var relaunchArgv []string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			if killed {
				return []byte(noListenerMarker + "\n"), nil
			}
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub\n"), nil // no --addr
		case strings.Contains(joined, "-F n"):
			return []byte("n127.0.0.2:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242 -a -d 1,2"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
		t.Fatalf("restartBare: %v (a listener owning the configured endpoint must restart)", err)
	}
	if !killed {
		t.Fatal("the hub owning the configured endpoint was not stopped")
	}
	if len(relaunchArgv) == 0 {
		t.Fatal("the hub was not relaunched")
	}
}

// TestRestartBareListenerAddressBeatsRecoveredAddr proves the inspected socket
// address is authoritative: a recovered --addr that agrees with the configured
// endpoint is not enough when the socket shows the process is bound elsewhere.
func TestRestartBareListenerAddressBeatsRecoveredAddr(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.2:9180"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.2:9180\n"), nil
		case strings.Contains(joined, "-F n"):
			return []byte("n127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart when the socket does not own the configured endpoint", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:9180") || !strings.Contains(err.Error(), "127.0.0.2:9180") {
		t.Fatalf("error does not name the listener and the configured endpoint: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
			t.Fatalf("a listener bound elsewhere was killed: %v", argv)
		}
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
		{"another subcommand merely naming hub", "/opt/evener/bin/evener serve --model hub", true},
		{"the hub attach client is not the daemon", "/opt/evener/bin/evener hub attach --stdio", true},
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

// TestHubExecutableMatchesCanonicalTarget proves the restart identifies the hub
// by the canonical recovered executable compared to the configured target. A
// hardcoded `path.Base(argv[0]) == "evener"` refused every valid custom
// evener_path (an arbitrary executable path); canonicalizing both sides is the
// identity the rule needs.
func TestHubExecutableMatchesCanonicalTarget(t *testing.T) {
	t.Run("custom evener_path basename restarts", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/current/evener-hub"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			if strings.Contains(joined, "evener_resolve /opt/evener/current/evener-hub") {
				return []byte("/opt/evener/current/evener-hub\n"), nil
			}
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "/opt/evener/current/evener-hub"); err != nil {
			t.Fatalf("hubExecutableMatches: %v (a valid custom evener_path basename must restart)", err)
		}
	})

	t.Run("a symlink resolves to the configured target", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/usr/local/bin/evener"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "evener_resolve /usr/local/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "evener_resolve /proc/self/exe"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "/proc/self/exe"); err != nil {
			t.Fatalf("hubExecutableMatches: %v (a symlinked path must canonicalize to the same target)", err)
		}
	})

	t.Run("a non-hub executable refuses", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "evener_resolve /usr/sbin/nginx"):
				return []byte("/usr/sbin/nginx\n"), nil
			case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "/usr/sbin/nginx"); !errors.Is(err, ErrRestart) {
			t.Fatalf("hubExecutableMatches(nginx) err = %v, want ErrRestart", err)
		}
	})

	t.Run("empty evener_path compares command -v evener", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "command -v evener"):
				return []byte("/usr/local/bin/evener\n"), nil
			case strings.Contains(joined, "evener_resolve /usr/local/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "evener_resolve /proc/1234/exe"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "/proc/1234/exe"); err != nil {
			t.Fatalf("hubExecutableMatches: %v (empty evener_path resolves the target with command -v evener)", err)
		}
	})

	t.Run("bare argv[0] resolves through command -v", func(t *testing.T) {
		// An ad hoc hub is commonly launched as `evener hub`, so the recovered
		// argv[0] is a bare name. The on-host resolver's `test -f` checks the SSH
		// login cwd and never searches PATH, so the name must be resolved with
		// `command -v` first, exactly as expectedHubExecutable and deployTarget do.
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "command -v evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "evener_resolve evener"):
				return nil, errors.New("resolver: no such file or directory")
			case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "evener"); err != nil {
			t.Fatalf("hubExecutableMatches(bare argv[0]) err = %v, want nil (a PATH-launched hub must be restartable)", err)
		}
	})

	t.Run("bare argv[0] absent from PATH refuses", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			if strings.Contains(strings.Join(argv, " "), "command -v evener") {
				return []byte("\n"), nil
			}
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		if err := m.hubExecutableMatches(context.Background(), host, "evener"); !errors.Is(err, ErrRestart) {
			t.Fatalf("hubExecutableMatches(bare argv[0] not on PATH) err = %v, want ErrRestart", err)
		}
	})
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
		{"systemd exited", "linux", nil, []byte("evener-hub.service loaded active exited Evener Hub\n"), nil},
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
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000"}, hubIdentity{}); err != nil {
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
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000"}, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart", err)
	}
	if !errors.Is(err, kickErr) || !strings.Contains(err.Error(), "kickstart") {
		t.Fatalf("error does not surface the failed kickstart: %v", err)
	}
}

// TestWaitHealthyUsesTheConfiguredHostAddr proves the health probe addresses the
// host's configured listen address instead of discarding its host part for
// "localhost". A valid loopback such as 127.0.0.2 (or an IPv6-only ::1) is not
// reachable as "localhost", so the old probe failed verification after a
// successful restart. Before the fix the recorded remote was
// `curl -fsS localhost:9180/api/health` and this test failed with
// "health probe discarded the configured host".
func TestWaitHealthyUsesTheConfiguredHostAddr(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.2:9180"}
	var remote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "/api/health") {
			remote = joined
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.2:9180"}`), nil
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.waitHealthy(context.Background(), host, "newsha", "", hubIdentity{}); err != nil {
		t.Fatalf("waitHealthy: %v", err)
	}
	if !strings.Contains(remote, "127.0.0.2:9180/api/health") {
		t.Fatalf("health probe discarded the configured host: %q", remote)
	}
	if strings.Contains(remote, "localhost") {
		t.Fatalf("health probe still used localhost: %q", remote)
	}
}

// TestHubHealthRemoteNormalizesWildcardBinds proves the health URL is built from
// the configured address with the attach path's wildcard-to-loopback
// normalization: a wildcard bind is reached over loopback, the IPv6 wildcard
// maps to ::1 rather than forcing the IPv4 family, and every non-wildcard
// address (including a valid loopback like 127.0.0.2) is probed verbatim. The
// invocation is also pinned to carry -q/--noproxy (so a host-side .curlrc or
// proxy cannot answer in place of the loopback hub), an explicit http:// scheme
// (curl only guesses one from the host part, unreliably for a bracketed IPv6
// literal), and the per-request timeouts (so a listener that never answers cannot
// stall the health loop).
func TestHubHealthRemoteNormalizesWildcardBinds(t *testing.T) {
	const prefix = "curl -q --noproxy '*' -fsS --connect-timeout 5 --max-time 10 "
	cases := map[string]string{
		"127.0.0.2:9180": "http://127.0.0.2:9180/api/health",
		"127.0.0.1:9999": "http://127.0.0.1:9999/api/health",
		"0.0.0.0:9180":   "http://127.0.0.1:9180/api/health",
		"localhost:9180": "http://127.0.0.1:9180/api/health",
		"[::]:9180":      "'http://[::1]:9180/api/health'",
		"[::1]:9180":     "'http://[::1]:9180/api/health'",
	}
	for addr, want := range cases {
		if got := hubHealthRemote(addr); got != prefix+want {
			t.Errorf("hubHealthRemote(%q) = %q, want %q", addr, got, prefix+want)
		}
	}
}

// TestEnsureRestartsWhenTheRunningHubVersionDiffers proves version auto-match
// keys off the RUNNING hub, not the on-disk binary. A prior deploy can leave the
// new binary installed while the old process keeps serving (a restart that failed
// without polkit, an ambiguous unit listing, a bare relaunch failure), so the
// on-disk launch-check now reports the controller's version. Keying the gate off
// disk then skips deploy/restart entirely and attaches to the stale hub, silently
// losing the feature's core guarantee that the attached runtime matches the
// controller. Before the fix the on-disk version matched and no restart was ever
// issued; after it the differing /api/health version forces the restart path.
func TestEnsureRestartsWhenTheRunningHubVersionDiffers(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restarts := 0
	healthProbes := 0
	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.HasSuffix(joined, "uname -s"):
				return []byte("Linux\n"), nil
			case strings.HasSuffix(joined, "uname -m"):
				return []byte("x86_64\n"), nil
			case strings.HasSuffix(joined, "id -u"):
				return []byte("1000\n"), nil
			case strings.Contains(joined, "XDG_STATE_HOME"):
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				// The on-disk binary already matches the controller.
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			case strings.Contains(joined, "list-units"):
				return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
			case strings.Contains(joined, "systemctl restart"):
				restarts++
				return nil, nil
			case strings.Contains(joined, "api/health"):
				healthProbes++
				if healthProbes == 1 {
					// The stale hub a previously failed restart left serving.
					return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
				}
				return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			case strings.Contains(strings.Join(argv, " "), "command -v evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               func(context.Context, string, string, string) error { return nil },
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if restarts == 0 {
		t.Fatal("no restart: a running hub whose /api/health version differs from the controller must enter the restart path even when the on-disk binary matches")
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel preflight version = %q, want %q", got, "newsha")
	}
}

// TestReconnectNeverStartsAStoppedHub proves the bootstrap start is gated to an
// explicit attach request: a reconnect (explicit=false) to a host whose hub does
// not answer starts nothing, even though an explicit Ensure on the same host
// would bootstrap it (TestFirstAttachBootstrapsStoppedHost).
func TestReconnectNeverStartsAStoppedHub(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: cannedRun(map[string][]byte{
		"api/health": []byte(`not a health body`),
	}), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev", // cannedRun reports the on-disk version "dev"
	})

	if _, err := m.ensureOnce(context.Background(), host, false); err != nil {
		t.Fatalf("ensureOnce(reconnect): %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"systemctl", "launchctl", "kill ", "nohup", "cat >"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("a reconnect started a hub: %v", argv)
			}
		}
	}
}

// TestFirstAttachBootstrapsStoppedHost covers acceptance criterion 15: an
// explicit attach to a host whose hub is not running starts the identified
// supervisor's unit and attaches only after /api/health reports the expected
// build.
func TestFirstAttachBootstrapsStoppedHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	started := false
	var startedCmd string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !started {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "systemctl start"):
			started = true
			startedCmd = joined
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v (first attach must start a stopped hub)", err)
	}
	if !started {
		t.Fatal("the identified supervisor's unit was never started")
	}
	if !strings.Contains(startedCmd, "systemctl start evener-hub.service") {
		t.Fatalf("start command = %q, want systemctl start evener-hub.service", startedCmd)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge attaches only after the hub matched)", got)
	}
}

// TestExpectedServedBuildPinsOnlyTheControllersBuild pins the three branches of
// the rule every wait depends on. A host carrying this controller's build is
// waited for by version AND by the snapshot pin that proves which commit it is; a
// host that kept its own build is waited for by its own version and needs no pin —
// the controller's pin names a commit that host was never given; and a host whose
// contract could not be read carries no build to differ from, so the controller's
// build stays the expectation (the launch-contract gates handle that host).
func TestExpectedServedBuildPinsOnlyTheControllersBuild(t *testing.T) {
	origChannel, origSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = origChannel, origSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "controllersha"

	for _, tc := range []struct {
		name        string
		facts       Preflight
		wantVersion string
		wantGitSHA  string
	}{
		{"the controller's build", Preflight{LaunchCheckKnown: true, Version: "newsha"}, "newsha", "controllersha"},
		{"the host's own build", Preflight{LaunchCheckKnown: true, Version: "oldsha"}, "oldsha", ""},
		{"an unreadable contract", Preflight{}, "newsha", "controllersha"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotVersion, gotSHA := expectedServedBuild(tc.facts, "newsha")
			if gotVersion != tc.wantVersion || gotSHA != tc.wantGitSHA {
				t.Fatalf("expectedServedBuild = (%q, %q), want (%q, %q)", gotVersion, gotSHA, tc.wantVersion, tc.wantGitSHA)
			}
		})
	}
}

// TestFirstAttachBootstrapsStoppedHostOnAnotherBuild is the stopped-host twin of
// TestEnsureAttachesToAnotherBuildWhenProtocolMatches: the first-attach bootstrap
// starts the host's hub and waits for it to answer, and the build it will answer
// with is the one the host runs. A binary reporting "oldsha" can never report the
// controller's "newsha", so the start is judged against the host's own build.
func TestFirstAttachBootstrapsStoppedHostOnAnotherBuild(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	started := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !started {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			// The binary that was started is the one on disk: it comes up
			// reporting its own build, never the controller's.
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "systemctl start"):
			started = true
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: the stopped host's own build must be accepted once it answers", err)
	}
	if !started {
		t.Fatal("the identified supervisor's unit was never started")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge attaches to the host it just started)", got)
	}
}

// TestPendingStartRecoveryAcceptsTheHostsOwnBuild covers the recovery half of the
// same rule as TestFirstAttachBootstrapsStoppedHostOnAnotherBuild. A first attach
// records the command it ran (setPendingStart) so an interrupted bootstrap can be
// completed by the next Ensure; recoverRestart then re-runs that command and waits
// for the hub to answer. The build it must answer with is the host's own: the one
// the bootstrap that recorded the start was judged against.
func TestPendingStartRecoveryAcceptsTheHostsOwnBuild(t *testing.T) {
	const relaunch = "relaunch-the-host-hub"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	started := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, relaunch):
			started = true
			return nil, nil
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !started {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	// The recorded start an interrupted first attach leaves behind.
	m.setPendingStart(host.Name, relaunch)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: recovering a recorded start must accept the host's own build", err)
	}
	if !started {
		t.Fatal("the recorded start command was never re-run")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge attaches once the host's build answers)", got)
	}
}

// TestRestartOnlyMismatchOnAnotherBuildAttaches covers the same rule where a
// recorded start is replayed while a hub IS answering. ensureOnce then takes the
// restartHub branch rather than the recovery branch, and restartHub re-derived the
// controller's own version to wait for — so the same pending record was judged by
// two different rules depending on whether anything answered /api/health, and a
// host that kept its own build failed its restart with a permanent ErrRestart.
//
// The hub answers a DIFFERENT build before the restart, so the healthy-start
// settlement (a start whose served build already answers is settled without a
// restart) does not fire and the restartHub branch is still exercised.
func TestRestartOnlyMismatchOnAnotherBuildAttaches(t *testing.T) {
	const relaunch = "systemctl restart evener-hub.service"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restarted := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "systemctl restart"):
			restarted = true
			return nil, nil
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "api/health"):
			// Before the restart a stale process answers; the restart must converge
			// it on the host's own build (never the controller's version).
			if !restarted {
				return []byte(`{"version":"stale","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
			}
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	m.setPendingStart(host.Name, relaunch)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a recorded start must be settled against the host's own build", err)
	}
	if !restarted {
		t.Fatal("the recorded start was never replayed")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge attaches once the host's build answers)", got)
	}
}

// TestPendingStartOnHealthySupervisorlessHostAttaches pins the regression the
// supervisorless restart refusal introduced: a recorded START leaves a zero
// `replaced`, so the "different process" test can never clear it. When the
// started hub later came up healthy on a supervisorless host, the next Ensure
// saw a pending restart, took the restartHub branch (runningKnown is true, so
// recoverRestart is not used), and refused with ErrRestart forever — a host
// already serving the expected build could never attach. A recorded start has no
// predecessor to exclude, so a hub answering the expected build settles it,
// exactly as recoverRestart judges a recovered start.
func TestPendingStartOnHealthySupervisorlessHostAttaches(t *testing.T) {
	const relaunch = "nohup /opt/evener/bin/evener hub"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: a supervisorless host
		case strings.Contains(joined, "api/health"):
			// The started hub is healthy and serves the expected build.
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	// An interrupted first attach on a supervisorless host leaves a pending start
	// behind; the launched hub came up healthy afterwards.
	m.setPendingStart(host.Name, relaunch)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a healthy hub must settle a recorded start, not be refused as a supervisorless restart", err)
	}
	if got := m.pendingRestart(host.Name); got != (pendingRestartState{}) {
		t.Fatalf("pending start survived a healthy hub: %+v", got)
	}
}

// TestPendingStartOnHealthyHostKeepingItsOwnBuildAttaches is the companion for a
// host that keeps its own build (hostBuildDiffers, no deploy configured). The
// recorded start was judged against expectedServedBuild — the host's own build —
// so settlement must use that same expectation, not the controller's version;
// otherwise the marker survives, ensureDecision takes the restart branch because
// runningKnown sends it to restartHub rather than recoverRestart, and a
// supervisorless host refuses forever.
func TestPendingStartOnHealthyHostKeepingItsOwnBuildAttaches(t *testing.T) {
	const relaunch = "nohup /opt/evener/bin/evener hub"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor
		case strings.Contains(joined, "api/health"):
			// The host serves its own build; the attach path accepts it.
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	m.setPendingStart(host.Name, relaunch)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a recorded start must be settled against the host's own build", err)
	}
	if got := m.pendingRestart(host.Name); got != (pendingRestartState{}) {
		t.Fatalf("pending start survived a healthy host keeping its own build: %+v", got)
	}
}

// TestPendingStartNotSettledForWrongSnapshotCommit pins the snapshot pin on the
// settlement path: a health body reporting the expected version but a different
// backend_git_sha must not settle a recorded start, or a supervisorless
// controller would attach to a commit it never installed. The waits enforce the
// pin (waitStartedHealthy/waitHealthy via hubBuildSHAMatches); settlement must
// apply the same rule rather than version equality alone.
func TestPendingStartNotSettledForWrongSnapshotCommit(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "expectedsha"

	const relaunch = "nohup /opt/evener/bin/evener hub"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor
		case strings.Contains(joined, "api/health"):
			// The expected version, but a commit this controller never installed.
			return []byte(`{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	m.setPendingStart(host.Name, relaunch)

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure = %v, want ErrRestart (a wrong-commit hub must not settle a recorded start)", err)
	}
	if got := m.pendingRestart(host.Name); got == (pendingRestartState{}) {
		t.Fatal("a recorded start was settled against the expected version but the wrong snapshot commit")
	}
}

// TestFirstAttachBootstrapAdHocLaunch proves the bootstrap falls back to the
// detached ad hoc launch of the resolved executable when no supervisor unit is
// identified.
func TestFirstAttachBootstrapAdHocLaunch(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launched := false
	var launchCmd string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !launched {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no evener hub unit; the ad hoc path
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "nohup"):
			launched = true
			launchCmd = joined
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !launched {
		t.Fatal("the ad hoc launch was never issued")
	}
	if !strings.Contains(launchCmd, "/opt/evener/bin/evener hub") {
		t.Fatalf("launch command = %q, want the resolved evener hub invocation", launchCmd)
	}
}

// TestBootstrapRefusesWhenTheAddressIsOwned proves the bootstrap never starts a
// second hub: a process already listening on the configured address (even one
// that did not answer the health probe) suppresses the start.
func TestBootstrapRefusesWhenTheAddressIsOwned(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			return nil, errors.New("curl: (7) Failed to connect")
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte("4242\n"), nil // a process owns the address
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"systemctl start", "launchctl kickstart", "nohup"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("bootstrap started a second hub over an owned address: %v", argv)
			}
		}
	}
}

// TestBootstrapUnhealthyIsErrRestart proves a start that never becomes healthy
// attaches nothing and fails with ErrRestart.
func TestBootstrapUnhealthyIsErrRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "systemctl start"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (a hub that never becomes healthy attaches nothing)", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
}

// TestFirstAttachAfterDeployBootstrapsStoppedHost pins the round-five High that a
// deploy on a stopped ad-hoc host aborted before the first-attach bootstrap:
// ensureDecision set restart on every deploy, so ensureOnce ran restartHub, and
// restartBare found no listener to replace and returned ErrRestart instead of
// reaching bootstrapHub. A deploy with no hub present is a fresh install, not a
// restart: the explicit attach must start the stopped hub, and the channel must
// report the freshly installed build rather than the pre-deploy contract.
func TestFirstAttachAfterDeployBootstrapsStoppedHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls, launches := 0, 0
	var launchCmd string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: the ad hoc host
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil // nothing is listening
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(joined, "nohup"):
			launches++
			launchCmd = joined
			return nil, nil
		case strings.Contains(joined, "api/health"):
			if launches == 0 {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a deploy on a stopped host must reach the first-attach bootstrap)", err)
	}
	if launches == 0 {
		t.Fatal("no hub was started for a stopped host after its deploy")
	}
	if !strings.Contains(launchCmd, "/opt/evener/bin/evener hub") {
		t.Fatalf("bootstrap command = %q, want the resolved evener hub invocation", launchCmd)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
			t.Fatalf("a stopped host was restarted instead of bootstrapped: %v", argv)
		}
	}
	// The deploy changed the on-disk build, so the launch contract must have been
	// re-read: the attached channel reports the deployed build, not the stale
	// pre-deploy contract.
	if launchCalls < 2 {
		t.Fatalf("launch-check calls = %d, want >= 2 (re-probe after the deploy)", launchCalls)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel preflight version = %q, want %q (the deployed build, not the pre-deploy contract)", got, "newsha")
	}
}

// TestDeployThenExplicitAttachStartsDormantUnit pins the dormant-supervisor half
// of the same High: a stopped host whose evener hub unit is loaded but inactive
// must be started by the explicit-attach bootstrap (systemctl start), not routed
// through restartHub. It also pins that the re-probed contract reaches the
// channel when the deploy did not restart anything.
func TestDeployThenExplicitAttachStartsDormantUnit(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls := 0
	var startCmd, restartCmd string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(joined, "systemctl restart"):
			restartCmd = joined
			return nil, nil
		case strings.Contains(joined, "systemctl start"):
			startCmd = joined
			return nil, nil
		case strings.Contains(joined, "api/health"):
			if startCmd == "" {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (the inactive unit must be started by the bootstrap)", err)
	}
	if restartCmd != "" {
		t.Fatalf("restartHub restarted the unit instead of the bootstrap starting it: %q", restartCmd)
	}
	if !strings.Contains(startCmd, "systemctl start evener-hub.service") {
		t.Fatalf("start command = %q, want systemctl start evener-hub.service", startCmd)
	}
	if launchCalls < 2 {
		t.Fatalf("launch-check calls = %d, want >= 2 (re-probe after the deploy)", launchCalls)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel preflight version = %q, want %q", got, "newsha")
	}
}

// TestReconnectDeployDoesNotStartAStoppedHub pins the round-five High that a
// deploy during a reconnect started a stopped hub: with a dormant supervisor
// unit, the unconditional restart sent restartHub into `systemctl restart`, which
// starts the unit. A reconnect (explicit=false) must never start a hub; only an
// explicit attach may bootstrap one.
func TestReconnectDeployDoesNotStartAStoppedHub(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return nil, errors.New("curl: (7) Failed to connect")
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
		return nil, errors.New("ssh: connect to host alpha.example port 22: Connection refused")
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	// explicit=false is the reconnect/bridge path. The deploy may install the
	// matching build, but it must not start the stopped hub.
	if _, err := m.ensureOnce(context.Background(), host, false); err == nil {
		t.Fatal("ensureOnce(reconnect) reported success with nothing serving")
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		for _, forbidden := range []string{"systemctl start", "systemctl restart", "launchctl kickstart", "nohup"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("a reconnect deploy started a hub: %v", argv)
			}
		}
	}
}

// TestParseHubHealthRejectsBodyWithoutVersion pins the honest known/unknown
// rule: JSON that decodes but carries no version is not a hub with an empty
// version, and reading it as one would fire a restart against some unrelated
// listener that merely speaks JSON. Round twelve extends the rule to the fields
// that make a body hub identity rather than a version string: the hub REST
// contract version and the address the hub actually bound must both match what
// this controller expects, so an unrelated service — or a hub serving a
// different endpoint — answering with the expected version is not treated as
// authoritative. It also pins that the process start time is carried when
// present, since restart verification keys off it.
func TestParseHubHealthRejectsBodyWithoutVersion(t *testing.T) {
	const addr = "127.0.0.1:9180"
	const goodBody = `{"status":"ok","version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180","started_at":"2026-01-02T03:04:05.5Z"}`
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"hub body", goodBody, "newsha", true},
		{"empty object", `{}`, "", false},
		{"status only", `{"status":"ok"}`, "", false},
		{"null", `null`, "", false},
		{"not json", `<html>nope</html>`, "", false},
		{"version only", `{"version":"newsha"}`, "", false},
		{"no mobile api version", `{"version":"newsha","hub_addr":"127.0.0.1:9180"}`, "", false},
		{"wrong mobile api version", `{"version":"newsha","mobile_api_version":2,"hub_addr":"127.0.0.1:9180"}`, "", false},
		{"no hub addr", `{"version":"newsha","mobile_api_version":1}`, "", false},
		{"other endpoint", `{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.2:9180"}`, "", false},
		{"other port", `{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9999"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHubHealth([]byte(tc.in), addr)
			if got.version != tc.want || ok != tc.ok {
				t.Fatalf("parseHubHealth(%q) = (%q,%v), want (%q,%v)", tc.in, got.version, ok, tc.want, tc.ok)
			}
		})
	}

	got, ok := parseHubHealth([]byte(goodBody), addr)
	if !ok || got.startedAt.IsZero() {
		t.Fatalf("parseHubHealth dropped the process start time: (%v,%v)", got, ok)
	}
	// A body without started_at yields a zero time, which sameProcessAs never
	// reads as a match.
	got, ok = parseHubHealth([]byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), addr)
	if !ok || !got.startedAt.IsZero() {
		t.Fatalf("parseHubHealth invented a start time: (%v,%v)", got, ok)
	}
	if got.sameProcessAs(hubIdentity{version: "newsha"}) {
		t.Fatal("two zero start times were read as the same process")
	}
	// The address comparison uses the same wildcard-to-loopback normalization the
	// probe's URL does: a hub bound to the wildcard is the endpoint the controller
	// reaches on loopback.
	if _, ok := parseHubHealth([]byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"0.0.0.0:9180"}`), addr); !ok {
		t.Fatal("a wildcard-bound hub reporting loopback was rejected")
	}
}

// TestEnsureDevBuildDeploysOncePerManager pins the dev-identity rule: "dev" is not
// an identity, so an unstamped controller cannot prove an unstamped host matches.
// With a deploy configured it installs its own build once, then trusts the host
// for this process's lifetime instead of rebuilding on every reconnect.
func TestEnsureDevBuildDeploysOncePerManager(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	builds := 0
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
		},
		func(call int) ([]byte, error) {
			// A real hub always reports started_at (cmd/evener-hub/web_api.go
			// handleAPIHealth); the pre-restart probe (call 0) and the post-restart
			// answers carry different start times, so the "dev" restart below is
			// verified as a replacement rather than read as the old process
			// (round thirteen).
			return []byte(fmt.Sprintf(`{"version":"dev","started_at":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
				time.Date(2026, 1, 1, 0, call, 0, 0, time.UTC).Format(time.RFC3339))), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("bin"), 0o755)
		},
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if builds != 1 {
		t.Fatalf("builds after first Ensure = %d, want 1 (a dev host cannot prove it matches)", builds)
	}

	// The next Ensure (after a dropped link) must not rebuild or restart again.
	ch.markLost()
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if builds != 1 {
		t.Fatalf("builds after second Ensure = %d, want 1 (the dev build is already installed)", builds)
	}
}

// TestEnsureRefusesSupervisorlessRestart pins spec 04 criterion 20 at the Ensure
// level: a host with no supervisor needs an unguarded ad hoc signal to restart,
// and no atomic process handle (a pidfd or a host-side pin helper) is reachable
// through this component's `ssh <dest> <command>` interface. The ladder must
// refuse with ErrRestart and emit no signal rather than fall back to the removed
// `restartBare` kill — "the bare `restartBare` (`kill <pid>`, `sshconn/version.go`)
// is not acceptable … When neither form is available for the identified process
// the manager refuses with `ErrRestart` and emits no signal." This replaces
// TestEnsureRestartRecoveryRetriesRelaunch, whose bare-relaunch recovery is
// unreachable now that no supervisorless restart is ever signaled; the
// supervised recovery that path shared is covered by
// TestEnsureSupervisorRestartRecoveryRetriesRestart.
func TestEnsureRefusesSupervisorlessRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls, relaunches, killCalls := 0, 0, 0

	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: the bare-process path
		case strings.Contains(joined, "lsof -ti :9180"):
			if killCalls == 0 {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killCalls++
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunches++
			if relaunches == 1 {
				return []byte("nohup: failed"), errors.New("exit status 1")
			}
			return nil, nil
		case strings.Contains(joined, "api/health"):
			// Nothing answers until the recovered relaunch has actually started
			// the deployed hub.
			if relaunches < 2 {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}

	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure err = %v, want ErrRestart (a supervisorless restart must refuse)", err)
	}
	if killCalls != 0 {
		t.Fatalf("kill calls = %d, want 0 (a supervisorless restart must emit no signal)", killCalls)
	}
	if relaunches != 0 {
		t.Fatalf("relaunches = %d, want 0 (a supervisorless restart must not relaunch)", relaunches)
	}
}

// TestRestartBarePrefersNullDelimitedArgv proves restartBare recovers an exact
// argv from /proc/<pid>/cmdline when the host provides it, so an argument
// containing a space survives intact instead of being split by a space-joined ps
// line.
func TestRestartBarePrefersNullDelimitedArgv(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	var relaunchArgv []string
	exactArgv := []string{"/opt/evener/bin/evener", "hub", "-addr", "127.0.0.1:9180", "-config", "/opt/my hub.toml"}

	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte(strings.Join(exactArgv, "\x00") + "\x00"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunchArgv = append([]string(nil), argv...)
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
		t.Fatalf("restartBare: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "-ww -o ") {
			t.Fatalf("restartBare fell back to space-joined ps despite a readable /proc: %v", argv)
		}
	}
	want := relaunchCommand(exactArgv, "")
	found := false
	for _, a := range relaunchArgv {
		if a == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("relaunch did not preserve the exact argv %v: %v", exactArgv, relaunchArgv)
	}
	if !strings.Contains(want, shellquote.RemoteWord("/opt/my hub.toml")) {
		t.Fatalf("relaunch lost the argument boundary: %q", want)
	}
}

// TestRestartBareRefusesAmbiguousSplitValue proves the ps fallback refuses a
// command line whose word boundaries cannot be recovered (a value containing a
// space) instead of killing a process and relaunching it with a wrong argv.
func TestRestartBareRefusesAmbiguousSplitValue(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return nil, errors.New("exit status 1") // no /proc on this host
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -config /opt/my hub.toml\n"), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an ambiguous command line", err)
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error does not explain the ambiguity: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill -- 4242") {
			t.Fatalf("ambiguous process was still killed: %v", argv)
		}
	}
}

// TestChannelArgvAndProbesShareHostAddrResolution proves the bridge and the
// restart/health probes resolve the host's listen address the same way, so a
// manager-wide Options.HubAddr with no per-host Addr cannot make the probes
// address a different port than the bridge dials.
func TestChannelArgvAndProbesShareHostAddrResolution(t *testing.T) {
	opts := Options{HubAddr: "127.0.0.1:9999"}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	m := newTestManager(t, testRegistry(t, host), &fakeRunner{}, opts)

	if got := m.hostAddr(host); got != "127.0.0.1:9999" {
		t.Fatalf("hostAddr = %q, want the manager-wide HubAddr", got)
	}
	argv := channelArgv(opts, host)
	idx := slices.Index(argv, "--addr")
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != "127.0.0.1:9999" {
		t.Fatalf("bridge argv does not carry the resolved addr: %v", argv)
	}

	host.Addr = "127.0.0.1:9998"
	if got := m.hostAddr(host); got != "127.0.0.1:9998" {
		t.Fatalf("hostAddr = %q, want the per-host Addr", got)
	}
	argv = channelArgv(opts, host)
	if idx = slices.Index(argv, "--addr"); idx < 0 || argv[idx+1] != "127.0.0.1:9998" {
		t.Fatalf("bridge argv does not carry the per-host addr: %v", argv)
	}

	// With nothing configured the bridge must be left to resolve the host's own
	// hub.toml address: a default passed here would override it. The probes still
	// have to address something, so they fall back to the hub default.
	host.Addr = ""
	if got := hostAddrFor(Options{}, host); got != defaultHubAddr {
		t.Fatalf("hostAddrFor = %q, want the hub default with nothing configured", got)
	}
	if slices.Contains(channelArgv(Options{}, host), "--addr") {
		t.Fatalf("bridge argv forces a default addr, overriding the host's config: %v", channelArgv(Options{}, host))
	}
}

// TestEnsureDeployPhaseHasItsOwnBudget proves a cold cross-compile is not killed
// by attemptLimit, which bounds only preflight: the deploy phase runs under its
// own, longer deployLimit. Before the fix the whole ensure sequence shared
// attemptLimit (70s by default) and a slower build failed with a context-deadline
// error.
func TestEnsureDeployPhaseHasItsOwnBudget(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		attemptTimeout:            50 * time.Millisecond,
		deployTimeout:             2 * time.Second,
		BuildBinary: func(ctx context.Context, _, _, out string) error {
			// Longer than attemptLimit would have allowed, but within deployLimit.
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return os.WriteFile(out, []byte("bin"), 0o755)
		},
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v (a build longer than attemptLimit must still fit the deploy budget)", err)
	}
}

// TestEnsureSupervisorRestartRecoveryRetriesRestart pins the High finding that a
// failed supervisor restart was not recorded as pending. A `systemctl restart`
// that leaves no listener, with the new binary already on disk, used to make the
// next Ensure see a matching on-disk version and an unknown running version, skip
// the restart, and attach to a host with no hub. Every restart mode now records
// its command before running it, so the next Ensure completes it.
func TestEnsureSupervisorRestartRecoveryRetriesRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restarts, launchCalls := 0, 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			restarts++
			return nil, nil
		case strings.Contains(joined, "api/health"):
			// The hub only answers once the recovery restart has run.
			if restarts < 2 {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 1 err = %v, want ErrRestart (the supervisor restart never brought the hub up)", err)
	}
	if restarts != 1 {
		t.Fatalf("systemctl restarts after Ensure 1 = %d, want 1", restarts)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 2: %v (the pending supervisor restart must be retried)", err)
	}
	if restarts != 2 {
		t.Fatalf("systemctl restarts after recovery = %d, want 2 (the recorded supervisor restart was not retried)", restarts)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel version = %q, want newsha", got)
	}
}

// TestEnsureRefusesUnusableHubAddr pins the address-validation finding: the
// controller must not probe (or kill) through an address it cannot trust. A
// malformed or non-loopback address, and a config_path with no address at all
// (where the bridge resolves a port the controller cannot read), are refused
// before any ssh command runs rather than silently probed at the default port.
func TestEnsureRefusesUnusableHubAddr(t *testing.T) {
	cases := []struct {
		name string
		host hostreg.Host
	}{
		{"no port", hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1"}},
		{"non-loopback", hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "10.0.0.1:9180"}},
		{"bad port", hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "127.0.0.1:notaport"}},
		{"config without addr", hostreg.Host{Name: "alpha", SSH: "alpha.example", ConfigPath: "/etc/evener/hub.toml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			m := newTestManager(t, testRegistry(t, tc.host), fr, Options{})
			if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrHostAddr) {
				t.Fatalf("Ensure err = %v, want ErrHostAddr", err)
			}
			if runs := fr.recordedRuns(); len(runs) != 0 {
				t.Fatalf("ssh ran despite an unusable address: %v", runs)
			}
			if starts := fr.recordedStarts(); len(starts) != 0 {
				t.Fatalf("bridge started despite an unusable address: %v", starts)
			}
		})
	}
}

// TestValidateHubAddr pins the accepted shapes: a loopback host, localhost, or a
// wildcard bind, with a numeric port. Everything else is refused rather than
// probed.
func TestValidateHubAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9180", "127.0.0.2:1", "[::1]:9180", "localhost:9180", "0.0.0.0:9180", "[::]:9180", ":9180"} {
		if err := validateHubAddr("alpha", addr); err != nil {
			t.Errorf("validateHubAddr(%q) = %v, want nil", addr, err)
		}
	}
	for _, addr := range []string{"", "127.0.0.1", "127.0.0.1:", "127.0.0.1:0", "127.0.0.1:70000", "10.0.0.1:9180", "hub.example:9180", "127.0.0.1:abc"} {
		if err := validateHubAddr("alpha", addr); !errors.Is(err, ErrHostAddr) {
			t.Errorf("validateHubAddr(%q) = %v, want ErrHostAddr", addr, err)
		}
	}
}

// TestRefreshAfterRestartReportsTheReprobedVersion pins the Low finding that
// refreshLaunchContract overwrote facts.Version with the expected value. The
// re-probe reads the on-disk binary, which can still differ from what the
// running hub reported, so the channel must report what the probe actually saw.
func TestRefreshAfterRestartReportsTheReprobedVersion(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(map[string][]byte{
		"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"probedsha","launch_flags":["api-log"]}`),
	})}
	m := newTestManager(t, testRegistry(t, host), fr, Options{controllerVersionOverride: "expectedsha"})

	facts, err := m.refreshLaunchContract(context.Background(), host, Preflight{})
	if err != nil {
		t.Fatalf("refreshLaunchContract: %v", err)
	}
	if facts.Version != "probedsha" {
		t.Fatalf("facts.Version = %q, want the re-probed version %q", facts.Version, "probedsha")
	}
	if facts.Protocol != appwire.ProtocolVersion || !facts.LaunchCheckKnown {
		t.Fatalf("refreshLaunchContract did not record the refreshed contract: %+v", facts)
	}
}

// TestEnsureKeepsPendingRestartWhenOldProcessStillServes pins the High finding
// that a failed restart was cleared solely because the old hub reported the
// expected version. "dev" is not unique, so when `systemctl restart` refuses
// (no polkit) and the old process keeps serving, the next Ensure must retry the
// restart instead of clearing it and attaching to the process it was meant to
// replace. The start time from /api/health is what distinguishes the two.
func TestEnsureKeepsPendingRestartWhenOldProcessStillServes(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restarts := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			restarts++
			return []byte("Failed to restart evener-hub.service: Interactive authentication required.\n"), errors.New("exit status 1")
		case strings.Contains(joined, "api/health"):
			// The same old process keeps serving: same version, same start time.
			return []byte(`{"version":"dev","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary:               writeStageBinary,
	})

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 1 err = %v, want ErrRestart (the restart command failed)", err)
	}
	if restarts != 1 {
		t.Fatalf("systemctl restarts after Ensure 1 = %d, want 1", restarts)
	}

	// Second Ensure: the old process still answers with the expected, non-unique
	// "dev" version. That must not be read as a completed restart.
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 2 err = %v, want ErrRestart (the pending restart must be retried)", err)
	}
	if restarts != 2 {
		t.Fatalf("systemctl restarts after Ensure 2 = %d, want 2 (the pending restart must be retried)", restarts)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("bridge Start calls = %d, want 0 (never attach to the process the restart replaces)", got)
	}
}

// TestWaitHealthyRejectsThePreRestartProcess pins restart verification's use of
// the process start time: a hub answering with the expected version but the
// start time of the process the restart meant to replace has not been replaced.
func TestWaitHealthyRejectsThePreRestartProcess(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
		return []byte(`{"version":"dev","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	replaced, ok := parseHubHealth([]byte(`{"version":"dev","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), defaultHubAddr)
	if !ok {
		t.Fatal("parseHubHealth rejected a valid body")
	}
	err := m.waitHealthy(context.Background(), host, "dev", "", replaced)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (the pre-restart process must not satisfy verification)", err)
	}
	if !strings.Contains(err.Error(), "pre-restart process") {
		t.Fatalf("error does not name the un-replaced process: %v", err)
	}

	// A genuinely new process (same version, later start time) is accepted.
	replaced.startedAt = replaced.startedAt.Add(-time.Hour)
	if err := m.waitHealthy(context.Background(), host, "dev", "", replaced); err != nil {
		t.Fatalf("waitHealthy rejected a new process: %v", err)
	}
}

// TestRestartBareRecordsRelaunchBeforePortClearFails pins the High finding that
// restartBare recorded the relaunch only after waitPortClear succeeded. When the
// kill lands but the port never clears (a slow drain, a dropped transport), the
// old hub is dead with nothing recorded, and the next Ensure finds no listener
// and no command to recover.
func TestRestartBareRecordsRelaunchBeforePortClearFails(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	relaunches := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil // the port never clears
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunches++
			return nil, nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartBare(context.Background(), host, hubIdentity{}); !errors.Is(err, ErrRestart) {
		t.Fatalf("restartBare err = %v, want ErrRestart (the port stayed held)", err)
	}
	if relaunches != 0 {
		t.Fatalf("relaunches = %d, want 0 (the port never cleared)", relaunches)
	}
	if got := m.pendingRestart(host.Name).command; !strings.Contains(got, "nohup") {
		t.Fatalf("pending restart = %q, want the recorded relaunch (the kill landed, the port never cleared)", got)
	}
}

// TestRestartBareRefusesUnparsableRecoveredAddr pins the Medium finding that an
// unparsable recovered --addr fell back to the default port in hubPort, matched
// a default-port host, and passed the mismatch refusal that exists to prevent
// killing the wrong listener.
func TestRestartBareRefusesUnparsableRecoveredAddr(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(joined, "-ww -o "):
			return []byte("/opt/evener/bin/evener hub -addr garbage\n"), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an unparsable recovered --addr", err)
	}
	if !strings.Contains(err.Error(), "unparsable") {
		t.Fatalf("error does not explain the unparsable address: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "kill ") {
			t.Fatalf("an unparsable recovered --addr still killed a listener: %v", argv)
		}
	}
}

// TestDetectSupervisorSurfacesListingFailure pins the Medium finding that any
// supervisor-listing error was read as "supervisor absent", which fell back to
// killing and nohup-relaunching a hub the service manager still owns. Only a
// genuinely unavailable listing tool may fall back.
func TestDetectSupervisorSurfacesListingFailure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	surfaced := []struct {
		name string
		out  []byte
	}{
		{"permission refused", []byte("Failed to restart: Interactive authentication required.\n")},
		{"transport drop", []byte("ssh: connect to host alpha.example port 22: Connection refused\n")},
		{"broken bus", []byte("Failed to connect to bus: Permission denied\n")},
	}
	for _, tc := range surfaced {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.out
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				if strings.Contains(strings.Join(argv, " "), "systemctl") {
					return out, errors.New("exit status 1")
				}
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if _, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"}); !errors.Is(err, ErrRestart) {
				t.Fatalf("err = %v, want ErrRestart (a broken listing must not fall back to the bare path)", err)
			}
		})
	}

	t.Run("absent tool falls back", func(t *testing.T) {
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			if strings.Contains(strings.Join(argv, " "), "systemctl") {
				return []byte("/bin/sh: 1: systemctl: not found\n"), errors.New("exit status 127")
			}
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})
		sup, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"})
		if err != nil || sup.live.kind != supervisorNone {
			t.Fatalf("detectSupervisor = (%+v,%v), want no live supervisor and no error", sup, err)
		}
	})
}

// TestSplitNullArgvKeepsTrailingEmptyElement pins the Low finding that a
// trailing empty argument survived TrimRight as the previous element's value
// loss: `/proc/<pid>/cmdline` terminates every element with NUL, so trimming all
// trailing NULs turned `evener hub -config ""` into the valid-looking
// `evener hub -config` and relaunched with the value missing.
func TestSplitNullArgvKeepsTrailingEmptyElement(t *testing.T) {
	if argv, ok := splitNullArgv([]byte("/opt/evener/bin/evener\x00hub\x00-config\x00\x00")); ok || argv != nil {
		t.Fatalf("splitNullArgv = (%v,%v), want (nil,false) for a trailing empty argument", argv, ok)
	}
	got, ok := splitNullArgv([]byte("/opt/evener/bin/evener\x00hub\x00-config\x00/x\x00"))
	if !ok || len(got) != 4 || got[3] != "/x" {
		t.Fatalf("splitNullArgv = (%v,%v), want the four-element argv", got, ok)
	}
	if got, ok := splitNullArgv([]byte("evener\x00")); !ok || len(got) != 1 || got[0] != "evener" {
		t.Fatalf("splitNullArgv = (%v,%v), want [evener]", got, ok)
	}
	if _, ok := splitNullArgv(nil); ok {
		t.Fatal("splitNullArgv accepted an empty input")
	}
}

// TestEnsureCorruptLaunchCheckReachesTheDeployPath pins the Medium finding that a
// launch-check decode failure was terminal before the decision ladder, so a
// corrupt on-disk binary never got the deploy path even though ensureDecision
// treats an unknown contract as a deploy trigger. With a deploy configured the
// contract is recorded as unknown; with none it stays terminal (covered by
// TestEnsurePreflightDecodeFailure).
func TestEnsureCorruptLaunchCheckReachesTheDeployPath(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				return []byte("not json"), nil
			}
			return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a corrupt on-disk contract must reach the deploy path)", err)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel version = %q, want newsha", got)
	}
}

// TestDetectSupervisorResolvesFromTheSystemListingWithoutAUserBus pins the High
// finding that detectSupervisor ran `systemctl --user list-units` unconditionally
// and aborted with ErrRestart when it failed. A headless host reached over
// non-interactive ssh exports no XDG_RUNTIME_DIR/DBUS_SESSION_BUS_ADDRESS, so the
// user listing exits nonzero with "Failed to connect to bus: ...", which matches
// none of supervisorListingAbsent's markers — even though the system listing had
// already named the hub's unit. ErrRestart is non-terminal, so the supervisor
// retried forever and the host stayed on the old build, silently defeating
// version auto-match.
//
// Round thirteen narrowed "the system listing settled the host" to a LIVE system
// unit: an inactive system unit is only a candidate, because a running USER unit
// is the hub actually serving the host (detectSupervisor now consults the user
// listing in that case). An unavailable user bus still falls through to the
// system answer — the property this test has always pinned — so a headless host
// whose hub unit is merely stopped stays repairable.
func TestDetectSupervisorResolvesFromTheSystemListingWithoutAUserBus(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	busErr := errors.New("exit status 1")
	busOut := []byte("Failed to connect to bus: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined (consider using --machine=@.host --user to connect to the system bus of the host)\n")

	cases := []struct {
		name      string
		sys       []byte
		want      supervisorSet
		wantUserQ bool
	}{
		{
			name: "running system unit",
			sys:  []byte("evener-hub.service loaded active running Evener Hub\n"),
			want: supervisorSet{live: supervisor{supervisorSystemd, "evener-hub.service"}},
		},
		{
			name:      "inactive system unit",
			sys:       []byte("evener-hub.service loaded inactive dead Evener Hub\n"),
			want:      supervisorSet{dormant: supervisor{supervisorSystemd, "evener-hub.service"}},
			wantUserQ: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			userListed := false
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				joined := strings.Join(argv, " ")
				switch {
				case strings.Contains(joined, "systemctl --user"):
					userListed = true
					return busOut, busErr
				case strings.Contains(joined, "list-units"):
					return tc.sys, nil
				case strings.Contains(strings.Join(argv, " "), "command -v evener"):
					return []byte("/opt/evener/bin/evener\n"), nil
				case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
					return []byte("/opt/evener/bin/evener\n"), nil
				default:
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})

			got, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"})
			if err != nil {
				t.Fatalf("detectSupervisor: %v (an unavailable user bus must not defeat a system listing that named the hub's unit)", err)
			}
			if got != tc.want {
				t.Fatalf("detectSupervisor = %+v, want %+v", got, tc.want)
			}
			if userListed != tc.wantUserQ {
				if tc.wantUserQ {
					t.Fatal("an INACTIVE system unit settled the host: a running user unit would have been missed (round thirteen)")
				}
				t.Fatal("the user listing was consulted although the system listing named a LIVE evener hub unit")
			}
		})
	}
}

// TestRestartHubStartsAnInactiveSupervisorWhenThePortIsFree pins the Medium
// finding that load-but-inactive evener hub units were dropped entirely, so a
// host with the unit stopped and no ad-hoc hub was restarted by searching for a
// bare PID and failing with "no hub listening" — the unit that owns the hub was
// never started. Before the fix restartHub returned ErrRestart; now it starts the
// inactive unit (a plain `systemctl restart` starts it) and verifies health.
func TestRestartHubStartsAnInactiveSupervisorWhenThePortIsFree(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	restarted, killed := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "systemctl restart"):
			restarted = true
			return nil, nil
		case strings.Contains(joined, "kill "):
			killed = true
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v (a stopped hub unit with a free port must be started)", err)
	}
	if !restarted {
		t.Fatal("the inactive supervisor was never started")
	}
	if killed {
		t.Fatal("restartHub killed a bare process instead of starting the stopped unit")
	}
}

// TestRestartHubRefusesAdHocHubUnderAnInactiveSupervisor pins spec 04 criterion
// 20 for the case where a supervisor definition exists but is not the hub's
// owner: an inactive unit that does not own the configured address is not an
// accepted supervisor, so the branch is the supervisorless one — "the
// supervisorless branch, which refuses `ErrRestart` with no signal and no
// relaunch" — and an ad hoc hub holding the port must be refused rather than
// killed. The inactive unit must also not be started while the ad hoc hub holds
// the port, because starting it would race hub.lock. Before the fix restartBare
// killed the ad hoc hub and relaunched it; that is exactly the check-then-act
// signal criterion 20 removes ("the bare `restartBare` (`kill <pid>`,
// `sshconn/version.go`) is not acceptable").
func TestRestartHubRefusesAdHocHubUnderAnInactiveSupervisor(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed, restarted := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			if !killed {
				return []byte("4242\n"), nil
			}
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "systemctl restart"):
			restarted = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("restartHub err = %v, want ErrRestart (a supervisorless restart must refuse)", err)
	}
	if restarted {
		t.Fatal("restartHub started the inactive unit while an ad-hoc hub held the port")
	}
	if killed {
		t.Fatal("restartHub killed the ad-hoc hub instead of refusing the supervisorless restart")
	}
}

// assertNoRestartSignal fails when fr recorded a bare signal (`kill --`) or a
// relaunch (`nohup`): a refused restart must emit neither.
func assertNoRestartSignal(t *testing.T, fr *fakeRunner) {
	t.Helper()
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "kill --") || strings.Contains(joined, "nohup") {
			t.Fatalf("refused restart still signaled or relaunched: %v", argv)
		}
	}
}

// TestRestartHubRefusesUnsafeLaunchdLabel pins spec 04 criterion 19: a detected
// launchd supervisor whose label is outside the bare-safe set must refuse with
// ErrRestart (no signal, no relaunch) instead of falling through to an unmanaged
// ad hoc launch. The label comes from the host's `launchctl list`, so
// interpolating it into `launchctl kickstart gui/<uid>/<label>` would be the
// injection the bare-safe check exists to prevent, and the ad hoc fall-through
// is the unguarded check-then-act kill criterion 20 forbids. Before the fix
// restartHub fell through: it recovered pid 4242 by port, ran `kill -- 4242`,
// and relaunched the recovered argv with nohup.
func TestRestartHubRefusesUnsafeLaunchdLabel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "launchctl list"):
			return []byte("PID\tStatus\tLabel\n1234\t0\tcom.example/evener-hub\n"), nil
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			if killed {
				return []byte(noListenerMarker + "\n"), nil
			}
			return []byte("4242\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000"}, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a label outside the bare-safe set", err)
	}
	if !strings.Contains(err.Error(), "com.example/evener-hub") {
		t.Fatalf("error does not name the unsafe label: %v", err)
	}
	if killed {
		t.Fatal("restartHub killed a bare process instead of refusing the unsafe label")
	}
	assertNoRestartSignal(t, fr)
}

// TestRestartHubRefusesSupervisorlessRestart pins spec 04 criterion 20: when no
// supervisor owns the hub, the restart would have to signal an identified PID
// directly, and no atomic process handle (a pidfd or a host-side pin helper) is
// reachable through this component's `ssh <dest> <command>` interface. The
// manager must refuse with ErrRestart and emit no signal instead of the
// check-then-act `restartBare` kill. Before the fix restartBare killed 4242 by
// port and relaunched its recovered argv. The cold-bootstrap start path is
// unaffected: it is start-only and never reaches this branch.
func TestRestartHubRefusesSupervisorlessRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	killed := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return nil, nil
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			if killed {
				return []byte(noListenerMarker + "\n"), nil
			}
			return []byte("4242\n"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			return nil, nil
		case strings.Contains(joined, "api/health"):
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a supervisorless restart", err)
	}
	if !strings.Contains(err.Error(), "supervisor") {
		t.Fatalf("error does not explain the missing supervisor: %v", err)
	}
	if killed {
		t.Fatal("restartHub signaled a bare pid instead of refusing a supervisorless restart")
	}
	assertNoRestartSignal(t, fr)
}

// TestRestartBareFailsClosedOnUnusableProcArgv pins the Medium finding that a
// readable /proc/<pid>/cmdline containing an empty word (e.g. `evener hub -config
// ""`) was rejected by splitNullArgv and then silently recovered through the
// space-joined `ps` fallback, which drops the empty value and relaunches as
// `evener hub -config`. The old hub is already killed by then, so the broken
// relaunch bricks the host and recovery retries the same broken command. The
// probe must fail closed instead of falling back.
func TestRestartBareFailsClosedOnUnusableProcArgv(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	port := hubPort(defaultHubAddr)
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "/proc/4242/cmdline"):
			// Readable, but the trailing empty argument makes it ambiguous.
			return []byte("/opt/evener/bin/evener\x00hub\x00-config\x00\x00"), nil
		case strings.Contains(joined, "lsof -ti :"+port):
			return []byte("4242\n"), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartBare(context.Background(), host, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for a readable but unusable /proc argv", err)
	}
	if !strings.Contains(err.Error(), "not a usable argv") {
		t.Fatalf("error does not explain the unusable exact argv: %v", err)
	}
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "-ww -o ") {
			t.Fatalf("fell back to lossy ps despite a readable /proc: %v", argv)
		}
		if strings.Contains(joined, "kill -- 4242") {
			t.Fatalf("the process was killed before its argv could be recovered: %v", argv)
		}
	}
}

// TestEnsureAttachesToAnotherBuildWhenProtocolMatches pins the rule: a host that
// answers the controller's launch-check is protocol-compatible by construction
// (launch-check refuses any protocol but its own, launchcheck.go:72-74), so a
// build whose version LABEL differs — an unstamped "dev" host against a stamped
// controller, or a host built from another checkout — must attach rather than be
// refused. The build label is not a compatibility signal; the protocol version
// is, and the wire layer enforces it independently at Initialize
// (appwire.ProtocolVersionMismatchError), with the preflight protocol refusal
// covering the host that cannot answer at all. Attaching is reported, not silent:
// the notice this asserts is how an operator learns which build the host kept.
func TestEnsureAttachesToAnotherBuildWhenProtocolMatches(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`),
		}),
		startFn: goodStartFn(t),
	}
	var logged []string
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		Logger: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a protocol-compatible host must attach whatever build it runs", err)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge must attach to the running host)", got)
	}
	// Accepting another build must not be silent: the operator sees which build
	// the host kept and which one this controller runs.
	reported := slices.ContainsFunc(logged, func(line string) bool {
		return strings.Contains(line, `"oldsha"`) && strings.Contains(line, `"newsha"`)
	})
	if !reported {
		t.Fatalf("attaching to another build was not reported: logged %q", logged)
	}
	// No deploy path is configured, so nothing may be pushed or installed: the
	// host's own binary stays, and the difference is reported rather than repaired.
	for _, argv := range fr.recordedRuns() {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "evener-install") || strings.Contains(joined, "cat >") {
			t.Fatalf("a deploy ran on a host with no deploy path configured: %v", argv)
		}
	}
}

// TestDetectSupervisorConsultsTheUserListingWhenTheSystemListingHasNone covers
// the other half of the lazy user-listing change: when the system listing names
// no evener hub unit at all, the user listing IS consulted and a running
// systemd --user unit is still detected. It also pins the plumbing of that
// output back out of the conditional (an accidentally shadowed variable would
// discard it and report no supervisor).
func TestDetectSupervisorConsultsTheUserListingWhenTheSystemListingHasNone(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "systemctl --user"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("sshd.service loaded active running OpenSSH server\n"), nil
		case strings.Contains(strings.Join(argv, " "), "command -v evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(strings.Join(argv, " "), "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	got, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"})
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	want := supervisorSet{live: supervisor{supervisorSystemdUser, "evener-hub.service"}}
	if got != want {
		t.Fatalf("detectSupervisor = %+v, want %+v", got, want)
	}
}
