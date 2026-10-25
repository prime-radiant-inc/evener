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
)

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
			"launch-check": []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`),
			"api/health":   []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`),
		}),
		startFn: goodStartFn(t),
	}
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "newsha", "newsha")
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

// A positive live protocol mismatch requires deployment, verified restart, and
// a final compatible attachment, in that order.
func TestEnsureProtocolUpgradeDeploysRestartsThenAttaches(t *testing.T) {
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
			case argv[len(argv)-1] == envProbeScript:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				// The preflight reads the stale on-disk binary; the re-probe after
				// the deploy reads the controller's freshly installed build.
				launchChecks++
				if launchChecks == 1 {
					return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
				}
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	requireProtocolUpgrade(t, fr)
	modelLegacySelectedProcess(t, fr, host)
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
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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

	wantStates := []State{StatePreflighting, StateAttaching, StateDeploying, StateRestarting, StateAttaching}
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

// Protocol recovery judges startup capabilities on the deployed binary. A
// missing disk flag does not prevent a required upgrade over SSH.
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
			case argv[len(argv)-1] == envProbeScript:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				launchChecks++
				if launchChecks == 1 {
					// The on-disk 04a-era binary: old version, no api-log flag.
					return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":[]}`), nil
				}
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	requireProtocolUpgrade(t, fr)
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			built = true
			return os.WriteFile(out, []byte("new-binary"), 0o755)
		},
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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
	declared := fr.runFn
	modelLegacySelectedProcess(t, fr, host)
	selected := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		if remote == "systemctl show --property=Id,MainPID,User,ExecStart,WorkingDirectory -- other-evener-hub.service" {
			return []byte("Id=other-evener-hub.service\nMainPID=4242\nUser=dev\nExecStart={ path=/opt/evener/bin/evener ; argv[]=/opt/evener/bin/evener hub ; ignore_errors=no ; }\nWorkingDirectory=/home/dev\n"), nil
		}
		if strings.Contains(strings.Join(argv, " "), "list-units") {
			return declared(ctx, argv, in)
		}
		return selected(ctx, argv, in)
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart for an ambiguous supervisor", err)
	}
	if m.pendingRestart(host.Name).selected != nil {
		t.Fatal("ambiguous service gained mutation intent")
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
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr, _ := supervisorRestartRunner(nil, func(int) ([]byte, error) {
		return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "oldsha")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (stale version must fail the restart)", err)
	}
	if m.pendingRestart(host.Name).selected == nil {
		t.Fatal("failed verification discarded selected intent")
	}
	if !strings.Contains(err.Error(), "oldsha") || !strings.Contains(err.Error(), "newsha") {
		t.Fatalf("error does not name the stale and expected versions: %v", err)
	}
}

// TestRestartHubSurfacesFailedRestartCommand proves a failed restart command is
// surfaced even when the health endpoint reports the expected version: a
// healthy-looking hub must not mask the command that failed to restart it.
func TestRestartHubSurfacesFailedRestartCommand(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	restartErr := errors.New("exit status 1")
	fr, _ := supervisorRestartRunner(restartErr, func(int) ([]byte, error) {
		return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{})
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
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr, _ := supervisorRestartRunner(nil, func(probe int) ([]byte, error) {
		if probe < 2 {
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		}
		return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
	})
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	selected.delay = 2
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v", err)
	}
	if selected.successorPolls < 2 {
		t.Fatalf("selected successor probes = %d, want >= 2 (the stale response must not satisfy verification)", selected.successorPolls)
	}
}

// TestEffectiveUserName pins the spec's resolution chain: the destination
// composeDest dials decides the user, and a destination that names none reports
// unknown so the restart probes the login user instead of comparing the owner
// against an empty string (which would refuse every legitimate hub on an
// ambient-ssh_config host).
func TestEffectiveUserName(t *testing.T) {
	cases := []struct {
		name string
		host hostreg.Host
		want string
		ok   bool
	}{
		{"an explicit user composes the destination", hostreg.Host{SSH: "alpha.example", User: "dev"}, "dev", true},
		{"a user@host destination names its user", hostreg.Host{SSH: "bob@alpha.example"}, "bob", true},
		{"a bare hostname names none", hostreg.Host{SSH: "alpha.example"}, "", false},
		{"an empty destination names none", hostreg.Host{}, "", false},
		{"an empty user part names none", hostreg.Host{SSH: "@alpha.example"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := effectiveUserName(tc.host)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("effectiveUserName(%+v) = %q, %v; want %q, %v", tc.host, got, ok, tc.want, tc.ok)
			}
		})
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

// TestRestartHubLaunchdKickstartStatusIsAdvisory proves a nonzero `launchctl
// kickstart` status does not fail the restart when the hub afterwards reports the
// deployed version: docs/evener-hub-remote-operations.md:290-293 says an
// interrupted kickstart can report failure even when the restart succeeded, so
// the health probe is the real check. Before the fix restartHub returned
// ErrRestart without ever probing health.
func TestRestartHubLaunchdKickstartStatusIsAdvisory(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
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
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	selected.darwin, selected.advisory = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v (a failed kickstart status must not mask a successful restart)", err)
	}
	if selected.successorPolls == 0 {
		t.Fatal("health was never probed after a failed kickstart status")
	}
}

// TestRestartHubLaunchdKickstartFailureSurfacesWhenHealthFails proves the
// kickstart status is still reported as a diagnostic when the restart really did
// not take: the health probe is authoritative, but its failure names the command
// that also failed.
func TestRestartHubLaunchdKickstartFailureSurfacesWhenHealthFails(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
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
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "oldsha")
	selected.darwin, selected.advisory = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart", err)
	}
	if !errors.Is(err, kickErr) || !strings.Contains(err.Error(), "kickstart") {
		t.Fatalf("error does not surface the failed kickstart: %v", err)
	}
}

// A different live build is preserved even when the on-disk binary already
// matches the controller. Build skew alone grants no restart authority.
func TestEnsurePreservesRunningHubWhenBuildDiffers(t *testing.T) {
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
			case argv[len(argv)-1] == envProbeScript:
				return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
			case strings.Contains(joined, "launch-check"):
				// The on-disk binary already matches the controller.
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               func(context.Context, string, string, string) error { return nil },
	})
	testRestartRecorder(t, m)

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if restarts != 0 {
		t.Fatal("Connect restarted a compatible running hub")
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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "newsha", "newsha").stopped = true
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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
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
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "oldsha").stopped = true
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
	const relaunch = "systemctl start evener-hub.service"
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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !started {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	testRestartRecorder(t, m)
	// The recorded start an interrupted first attach leaves behind.
	m.setPendingStart(host.Name, relaunch, "oldsha", "", nil)

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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
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
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	testRestartRecorder(t, m)
	m.setPendingStart(host.Name, relaunch, "oldsha", "", nil)

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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: a supervisorless host
		case strings.Contains(joined, "api/health"):
			// The started hub is healthy and serves the expected build.
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	// An interrupted first attach on a supervisorless host leaves a pending start
	// behind; the launched hub came up healthy afterwards.
	m.setPendingStart(host.Name, relaunch, "newsha", "", nil)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a healthy hub must settle a recorded start, not be refused as a supervisorless restart", err)
	}
	if got := m.pendingRestart(host.Name); !reflect.DeepEqual(got, pendingRestartState{}) {
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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor
		case strings.Contains(joined, "api/health"):
			// The host serves its own build; the attach path accepts it.
			return []byte(`{"version":"oldsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	m.setPendingStart(host.Name, relaunch, "oldsha", "", nil)

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure = %v, want nil: a recorded start must be settled against the host's own build", err)
	}
	if got := m.pendingRestart(host.Name); !reflect.DeepEqual(got, pendingRestartState{}) {
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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor
		case strings.Contains(joined, "api/health"):
			// The expected version, but a commit this controller never installed.
			return []byte(`{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	testRestartRecorder(t, m)
	m.setPendingStart(host.Name, relaunch, "newsha", "expectedsha", nil)

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure = %v, want ErrRestart (a wrong-commit hub must not settle a recorded start)", err)
	}
	if got := m.pendingRestart(host.Name); reflect.DeepEqual(got, pendingRestartState{}) {
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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "newsha", "newsha")
	selected.stopped, selected.adhoc = true, true
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

// TestBootstrapUnhealthyIsErrRestart proves a start that never becomes healthy
// attaches nothing and fails with ErrRestart.
func TestBootstrapUnhealthyIsErrRestart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	healthProbes := 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			healthProbes++
			if healthProbes == 1 {
				return nil, errors.New("no listener")
			}
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
	fixture := modelSelectedProcess(t, fr, host.EvenerPath, "oldsha", "oldsha")
	fixture.stopped = true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})
	testRestartRecorder(t, m)

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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":[]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	selected.stopped, selected.adhoc = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":[]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha").stopped = true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":[]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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

// Compatible unstamped builds remain untouched on first Connect and reconnect.
func TestEnsureDevBuildSurvivesReconnectWithoutDeployment(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	builds := 0
	fr := deployRunner(t,
		func(int) ([]byte, error) {
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"dev","launch_flags":["api-log"]}`), nil
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
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "dev")
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("bin"), 0o755)
		},
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if builds != 0 {
		t.Fatalf("builds after first Ensure = %d, want 0 (compatible live hub)", builds)
	}

	// The next Ensure (after a dropped link) must not rebuild or restart again.
	ch.markLost()
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if builds != 0 {
		t.Fatalf("builds after second Ensure = %d, want 0 (reconnect preserves the installed build)", builds)
	}
}

// TestEnsureRestartRecoveryRetriesRelaunch pins High #2: a bare restart that kills
// the old hub and fails to relaunch leaves the host with no listener. The next
// Ensure must complete the recorded relaunch, which is what ErrRestart promises.
func TestEnsureRestartRecoveryRetriesRelaunch(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launchCalls, relaunches, killCalls := 0, 0, 0
	var relaunchCommands []string

	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "id -un"):
			return []byte("dev\n"), nil
		case strings.Contains(joined, pidOwnerRemote("4242")):
			return []byte("dev\n"), nil
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case argv[len(argv)-1] == envProbeScript && !strings.Contains(joined, "nohup"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
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
			relaunchCommands = append(relaunchCommands, joined)
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

	requireProtocolUpgrade(t, fr)
	fixture := modelLegacySelectedProcess(t, fr, host)
	fixture.adhoc = true
	fixture.observe = func(f *selectedProcessFixture) {
		f.stopped = killCalls > 0 && relaunches < 2
		f.before, f.after = "oldsha", "newsha"
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 1 err = %v, want ErrRestart (the relaunch failed)", err)
	}
	if relaunches != 1 {
		t.Fatalf("relaunches after failed restart = %d, want 1", relaunches)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 2: %v (the next Ensure must retry the recorded relaunch)", err)
	}
	if relaunches != 2 {
		t.Fatalf("relaunches after recovery = %d, want 2", relaunches)
	}
	if len(relaunchCommands) != 2 || relaunchCommands[0] != relaunchCommands[1] {
		t.Fatalf("recovery did not retry the recorded relaunch: %v", relaunchCommands)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel version = %q, want newsha", got)
	}
}

// Listener precedence belongs to bootstrap. Attachment never overrides the
// remote selected endpoint with a manager-wide or per-host listener address.
func TestChannelArgvAndBootstrapShareHostAddrResolution(t *testing.T) {
	opts := Options{HubListenAddr: "127.0.0.1:9999"}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}

	argv := hubBootstrapArgv(opts, host, "evener")
	idx := slices.Index(argv, "--addr")
	if idx < 0 || idx+1 >= len(argv) || argv[idx+1] != "127.0.0.1:9999" {
		t.Fatalf("bootstrap argv does not carry the resolved addr: %v", argv)
	}
	if slices.Contains(channelArgv(opts, host), "--addr") {
		t.Fatal("attachment overrides the selected endpoint with a manager listener")
	}

	host.Addr = "127.0.0.1:9998"
	argv = hubBootstrapArgv(opts, host, "evener")
	if idx = slices.Index(argv, "--addr"); idx < 0 || argv[idx+1] != "127.0.0.1:9998" {
		t.Fatalf("bootstrap argv does not carry the per-host addr: %v", argv)
	}
	if slices.Contains(channelArgv(opts, host), "--addr") {
		t.Fatal("attachment overrides the selected endpoint with a per-host listener")
	}

	// With nothing configured the bridge must be left to resolve the host's own
	// hub.toml address: a default passed here would override it.
	host.Addr = ""
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
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
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
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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
	restarts, starts, launchCalls := 0, 0, 0
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(joined, "uname -s"):
			return []byte("Linux\n"), nil
		case strings.HasSuffix(joined, "uname -m"):
			return []byte("x86_64\n"), nil
		case strings.HasSuffix(joined, "id -u"):
			return []byte("1000\n"), nil
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			launchCalls++
			if launchCalls == 1 {
				return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		case strings.Contains(joined, "systemctl start"):
			starts++
			return nil, nil
		case strings.Contains(joined, "systemctl restart"):
			restarts++
			return nil, nil
		case strings.Contains(joined, "api/health"):
			// The hub only answers once the recovery restart has run.
			if restarts+starts < 2 {
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
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha")
	selected.unavailable = true
	requireProtocolUpgrade(t, fr)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrRestart) {
		t.Fatalf("Ensure 1 err = %v, want ErrRestart (the supervisor restart never brought the hub up)", err)
	}
	if restarts != 1 {
		t.Fatalf("systemctl restarts after Ensure 1 = %d, want 1", restarts)
	}

	selected.unavailable, selected.stopped = false, true
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 2: %v (the pending supervisor restart must be retried)", err)
	}
	if restarts != 1 || starts != 1 {
		t.Fatalf("recovery: restarts=%d starts=%d, want 1/1", restarts, starts)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel version = %q, want newsha", got)
	}
}

// TestRefreshAfterRestartReportsTheReprobedVersion pins the Low finding that
// refreshLaunchContract overwrote facts.Version with the expected value. The
// re-probe reads the on-disk binary, which can still differ from what the
// running hub reported, so the channel must report what the probe actually saw.
func TestRefreshAfterRestartReportsTheReprobedVersion(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(map[string][]byte{
		"launch-check": []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"probedsha","launch_flags":["api-log"]}`),
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
		case argv[len(argv)-1] == envProbeScript:
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"dev","launch_flags":["api-log"]}`), nil
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
	requireProtocolUpgrade(t, fr)
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

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
	requireNoAttach(t, fr)
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

// A corrupt disk contract does not prevent recovery from a positive live
// protocol mismatch. The deployed artifact must still pass its launch-check.
func TestEnsureCorruptLaunchCheckReachesTheDeployPath(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := deployRunner(t,
		func(call int) ([]byte, error) {
			if call == 0 {
				return []byte("not json"), nil
			}
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
		},
		func(int) ([]byte, error) {
			return []byte(`{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		},
	)
	requireProtocolUpgrade(t, fr)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		BuildBinary:               writeStageBinary,
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a corrupt on-disk contract must reach the deploy path)", err)
	}
	if got := ch.Preflight().Version; got != "newsha" {
		t.Fatalf("channel version = %q, want newsha", got)
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
	restarted, killed := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
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
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v (a stopped hub unit with a free port must be started)", err)
	}
	if !restarted {
		t.Fatal("the inactive supervisor was never started")
	}
	if killed {
		t.Fatal("restartHub killed a bare process instead of starting the stopped unit")
	}
}

// TestRestartHubPrefersAnAdHocHubOverAnInactiveSupervisor guards the dormant
// path: an inactive unit must NOT be started while an ad-hoc hub holds the hub
// port, because starting it would race hub.lock and leave the ad-hoc hub (on the
// old build) still serving. The bare-process restart is the correct repair there.
func TestRestartHubPrefersAnAdHocHubOverAnInactiveSupervisor(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	killed, restarted := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "id -un"):
			return []byte("dev\n"), nil
		case strings.Contains(joined, pidOwnerRemote("4242")):
			return []byte("dev\n"), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded inactive dead Evener Hub\n"), nil
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
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
	fixture := modelLegacySelectedProcess(t, fr, host)
	fixture.adhoc, fixture.dormant = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v", err)
	}
	if restarted {
		t.Fatal("restartHub started the inactive unit while an ad-hoc hub held the port")
	}
	if !killed {
		t.Fatal("the ad-hoc hub was never restarted")
	}
}

// TestRestartHubFallsThroughOnUnsafeLaunchdLabel pins the restored rule: a
// detected launchd supervisor whose label is outside the bare-safe set has no
// safely-buildable restart command, so restartHub falls through to the guarded
// ad hoc path — the identified pid is killed and the recovered argv is
// relaunched — rather than refusing. The label comes from the host's
// `launchctl list`, so it must never be interpolated into the remote shell; the
// test asserts it appears in no command the runner is asked to execute. Before
// the restoration restartHub refused with ErrRestart and emitted no signal.
func TestRestartHubFallsThroughOnUnsafeLaunchdLabel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	killed, relaunched := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "id -un"):
			return []byte("dev\n"), nil
		case strings.Contains(joined, pidOwnerRemote("4242")):
			return []byte("dev\n"), nil
		case strings.Contains(joined, "launchctl list"):
			return []byte("PID\tStatus\tLabel\n1234\t0\tcom.example/evener-hub\n"), nil
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunched = true
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
	fixture := modelLegacySelectedProcess(t, fr, host)
	fixture.adhoc, fixture.darwin, fixture.unsafeLabel = true, true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "darwin", UID: "1000", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub = %v, want nil (an unbuildable launchd command must fall through to the ad hoc path)", err)
	}
	if !killed {
		t.Fatal("restartHub never killed the identified hub: the ad hoc fall-through did not run")
	}
	if !relaunched {
		t.Fatal("restartHub never relaunched the recovered argv")
	}
	const label = "com.example/evener-hub"
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), label) {
			t.Fatalf("the unsafe launchd label reached the remote shell: %v", argv)
		}
	}
}

// TestRestartHubRestartsAdHocOnSupervisorlessHost pins the restored rule for a
// hub with no supervisor: the restart runs the guarded ad hoc path — it recovers
// the listening pid, kills it, and relaunches the recovered argv — instead of
// refusing with ErrRestart. The running hub is expected to answer /api/health on
// the recovered build, exactly as the pre-refusal path required. Before the
// restoration restartHub refused with ErrRestart and emitted no signal.
func TestRestartHubRestartsAdHocOnSupervisorlessHost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	killed, relaunched := false, false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "id -un"):
			return []byte("dev\n"), nil
		case strings.Contains(joined, pidOwnerRemote("4242")):
			return []byte("dev\n"), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: the ad hoc path
		case strings.Contains(joined, "/proc/4242/cmdline"):
			return []byte("/opt/evener/bin/evener\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
		case strings.Contains(joined, "lsof -p 4242"):
			return nil, errors.New("exit status 1")
		case strings.Contains(joined, "kill -s 0 -- 4242"):
			return []byte(pidGoneMarker + "\n"), nil
		case strings.Contains(joined, "kill -- 4242"):
			killed = true
			return nil, nil
		case strings.Contains(joined, "nohup"):
			relaunched = true
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
	fixture := modelLegacySelectedProcess(t, fr, host)
	fixture.adhoc, fixture.noSystemctl = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	if err := m.restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1}, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}); err != nil {
		t.Fatalf("restartHub = %v, want nil (a supervisorless hub must restart through the ad hoc path)", err)
	}
	if !killed {
		t.Fatal("restartHub never killed the listening pid: the ad hoc path did not run")
	}
	if !relaunched {
		t.Fatal("restartHub never relaunched the recovered argv")
	}
}

// Live initialize is the compatibility authority. The channel keeps the
// separately observed disk build without synchronizing it to the controller.
func TestEnsureAttachesToAnotherBuildWhenProtocolMatches(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(map[string][]byte{
			"launch-check": []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"oldsha","launch_flags":["api-log"]}`),
		}),
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure = %v, want nil: a protocol-compatible host must attach whatever build it runs", err)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (the bridge must attach to the running host)", got)
	}
	if ch.Handshake().ProtocolVersion != appwire.ProtocolVersion || ch.Preflight().Version != "oldsha" {
		t.Fatal("the channel lost the live protocol or observed disk build")
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
