package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// A required protocol upgrade cannot compile an unrepeatable dirty controller
// tree. That source refusal is terminal, so reconnect does not retry forever.
// The own-executable exception is covered by TestDirtyControllerDeploysItsOwnExecutable.
func TestRound13DirtyControllerDeployRefusalIsTerminal(t *testing.T) {
	const dirty = "abc1234-dirty"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	builds := 0
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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"` + dirty + `","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			// The host already reports the controller's identical dirty version.
			return []byte(`{"version":"` + dirty + `","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	requireProtocolUpgrade(t, fr)
	modelLegacySelectedProcess(t, fr, host)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirty,
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("staged-binary"), 0o755)
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	})
	testDeployRecorder(t, m)
	testRestartRecorder(t, m)

	// An incompatible protocol still requires deployment, regardless of build labels.
	if !m.deployRequired(Preflight{
		Host:             host.Name,
		LaunchCheckKnown: true,
		Protocol:         "evener-appwire-v5",
		Version:          dirty,
		LaunchFlags:      []string{requiredLaunchFlag},
	}) {
		t.Fatal("an incompatible protocol did not require deployment")
	}

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, errControllerDirty) {
		t.Fatalf("Ensure err = %v, want errControllerDirty", err)
	}
	if !isTerminal(err) {
		t.Fatalf("Ensure err = %v, want a terminal refusal (a retryable one loops the supervisor forever)", err)
	}
	if errors.Is(err, ErrDeploy) {
		t.Fatalf("Ensure err = %v still wraps ErrDeploy, so the supervisor would retry the same refusal forever", err)
	}
	if !strings.Contains(err.Error(), "dirty tree") {
		t.Fatalf("error does not name the dirty-tree cause: %v", err)
	}
	if builds != 0 {
		t.Fatalf("build attempts = %d, want 0 (a dirty controller has no deployable source build)", builds)
	}
	requireNoAttach(t, fr)
	for _, argv := range fr.recordedRuns() {
		if strings.Contains(strings.Join(argv, " "), "systemctl restart") {
			t.Fatalf("the refused deploy still restarted the host: %v", argv)
		}
	}

	// The supervisor's own iteration must stand down. If this returned true the
	// reconnect loop would repeat the terminal refusal forever.
	hostGate := m.hostLock(host.Name)
	defer m.releaseHostLock(host.Name)
	if more := m.reconnectOnce(context.Background(), host, hostGate); more {
		t.Fatal("reconnectOnce asked for another attempt, so the supervisor would loop on the terminal refusal")
	}
	if builds != 0 {
		t.Fatalf("build attempts after the supervisor iteration = %d, want 0", builds)
	}
}

// Deployment follows the selected executable, never a listener's argv.
func TestDeployTargetUsesSelectedExecutable(t *testing.T) {
	for _, target := range []string{"", "/opt/selected/bin/evener"} {
		t.Run(target, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}
			want := target
			commands := []ownerCommand{}
			if target == "" {
				want = "/usr/local/bin/evener"
				commands = append(commands, ownerCommand{"command -v evener || echo __sshconn_no_evener__", want + "\n"})
			} else {
				commands = append(commands, ownerCommand{"test -d /opt/selected/bin", ""})
			}
			commands = append(commands, ownerCommand{resolveDeployCommand(want), want + "\n"})
			fr, index := ownerRunner(t, commands)
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			got, err := m.deployTarget(t.Context(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
			if err != nil || got != want || *index != len(commands) {
				t.Fatalf("target=%q want=%q err=%v commands=%d/%d", got, want, err, *index, len(commands))
			}
		})
	}
}

// TestRound13BootstrapStartStillVerifiesAnUnverifiableBuild pins the deliberate
// exception the strict rule needs: a first-ever start has no predecessor process
// to exclude (bootstrapHub refuses to start while a listener holds the address),
// so an unstamped "dev" hub that answers with the expected version verifies even
// when its health body carries no started_at. Without the exception no headless
// host could ever be bootstrapped by an unstamped controller.
func TestRound13BootstrapStartStillVerifiesAnUnverifiableBuild(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	launched := false
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
			return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"dev","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			if !launched {
				return nil, errors.New("curl: (7) Failed to connect")
			}
			// No started_at: a start has no predecessor, so the expected version
			// alone is the proof it can offer.
			return []byte(`{"version":"dev","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return nil, nil // no supervisor: the ad hoc host
		case strings.Contains(joined, "lsof -ti :9180"):
			return []byte(noListenerMarker + "\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "nohup"):
			launched = true
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	selected := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "dev", "dev")
	selected.stopped, selected.adhoc = true, true
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "dev",
		sleep:                     func(context.Context, time.Duration) error { return nil },
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v (a first start has no predecessor to exclude)", err)
	}
	if ch == nil {
		t.Fatal("Ensure returned no channel for a bootstrapped host")
	}
	if !launched {
		t.Fatal("the hub was never started")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("bridge Start calls = %d, want 1 (the host attached after the start verified)", got)
	}
}

// TestRound13RunningUserUnitBeatsAnInactiveSystemUnit pins the round-thirteen
// Medium that the user listing was consulted only when the system listing named
// no unit at all. A loaded-but-INACTIVE system unit is only a candidate: a
// running user unit is the hub actually serving the host, and it was skipped
// whenever any system unit existed. The user listing is now consulted whenever
// no system unit is LIVE, and precedence stays live-before-dormant (a dormant
// system unit still wins over a dormant user one).
func TestRound13RunningUserUnitBeatsAnInactiveSystemUnit(t *testing.T) {
	const sysDormant = "evener-hub.service loaded inactive dead Evener Hub\n"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	cases := []struct {
		name    string
		userOut []byte
		want    supervisorSet
	}{
		{
			name:    "a running user unit is the hub",
			userOut: []byte("evener-hub.service loaded active running Evener Hub\n"),
			want:    supervisorSet{live: supervisor{supervisorSystemdUser, "evener-hub.service"}},
		},
		{
			name:    "an inactive user unit leaves the system unit as the target",
			userOut: []byte("evener-hub.service loaded inactive dead Evener Hub\n"),
			want:    supervisorSet{dormant: supervisor{supervisorSystemd, "evener-hub.service"}},
		},
		{
			name:    "no user unit leaves the system unit as the target",
			userOut: nil,
			want:    supervisorSet{dormant: supervisor{supervisorSystemd, "evener-hub.service"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				joined := strings.Join(argv, " ")
				switch {
				case strings.Contains(joined, "systemctl --user"):
					return tc.userOut, nil
				case strings.Contains(joined, "list-units"):
					return []byte(sysDormant), nil
				default:
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})

			got, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"})
			if err != nil {
				t.Fatalf("detectSupervisor: %v", err)
			}
			if got != tc.want {
				t.Fatalf("detectSupervisor = %+v, want %+v", got, tc.want)
			}
		})
	}

	// A live SYSTEM unit still settles the host without the user query, which is
	// the round-eleven property: a headless user bus can never defeat it.
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "systemctl --user"):
			t.Fatalf("the user listing was consulted although a system unit is live: %v", argv)
			return nil, nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	got, err := m.detectSupervisor(context.Background(), host, Preflight{OS: "linux"})
	if err != nil {
		t.Fatalf("detectSupervisor: %v", err)
	}
	want := supervisorSet{live: supervisor{supervisorSystemd, "evener-hub.service"}}
	if got != want {
		t.Fatalf("detectSupervisor = %+v, want %+v", got, want)
	}
}

// TestRound13WildcardOwnershipIsAddressFamilyAware pins the round-thirteen
// Medium that the IPv6 wildcard listener (::) was read as owning IPv4 endpoints.
// Whether an IPv6 wildcard also accepts IPv4-mapped traffic depends on the
// socket's IPV6_V6ONLY setting, which neither lsof nor ss reports, so restart
// logic must not treat it as the process serving an IPv4 endpoint. The IPv4
// wildcard is the mirror case, and lsof's family-less "*" spelling keeps its
// meaning because the tool does not say which family it is.
func TestRound13WildcardOwnershipIsAddressFamilyAware(t *testing.T) {
	cases := []struct {
		local, configured string
		want              bool
	}{
		{"0.0.0.0:9180", "127.0.0.1:9180", true},
		{"0.0.0.0:9180", "[::1]:9180", false},
		{"[::]:9180", "[::1]:9180", true},
		{"[::]:9180", "127.0.0.1:9180", false},
		{"*:9180", "127.0.0.1:9180", true},
		{"*:9180", "[::1]:9180", true},
		{"127.0.0.1:9180", "127.0.0.1:9180", true},
		{"127.0.0.1:9180", "127.0.0.2:9180", false},
		{"[::1]:9180", "[::1]:9180", true},
		{"[::1]:9180", "127.0.0.1:9180", false},
		{"127.0.0.1:9181", "127.0.0.1:9180", false},
	}
	for _, tc := range cases {
		if got := listenerOwnsAddr(tc.local, tc.configured); got != tc.want {
			t.Errorf("listenerOwnsAddr(%q, %q) = %v, want %v", tc.local, tc.configured, got, tc.want)
		}
	}
}

// TestRound13RestartBareRefusesAnIPv6WildcardListenerForAnIPv4Endpoint pins the
// restart half of the same finding: a process bound only to the IPv6 wildcard is
// not the hub this host's IPv4 endpoint is configured for, so the bare restart
// must refuse it instead of killing whatever it is. The mirror case — an IPv6
// wildcard listener for an IPv6-configured host — is still restarted.
func TestRound13RestartBareRefusesAnIPv6WildcardListenerForAnIPv4Endpoint(t *testing.T) {
	newRunner := func(killed *bool) *fakeRunner {
		return &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "id -un"):
				return []byte("dev\n"), nil
			case strings.Contains(joined, pidOwnerRemote("4242")):
				return []byte("dev\n"), nil
			case strings.Contains(joined, "lsof -ti :9180"):
				if *killed {
					return []byte(noListenerMarker + "\n"), nil
				}
				return []byte("4242\n"), nil
			case strings.Contains(joined, "-ww -o "):
				return []byte("/opt/evener/bin/evener hub -addr 127.0.0.1:9180\n"), nil
			case strings.Contains(joined, "-F n"):
				return []byte("n[::]:9180\n"), nil
			case strings.Contains(joined, "lsof -p 4242 -a -d 1,2"):
				return nil, errors.New("exit status 1")
			case strings.Contains(joined, "kill -s 0 -- 4242"):
				return []byte(pidGoneMarker + "\n"), nil
			case strings.Contains(joined, "kill -- 4242"):
				*killed = true
				return nil, nil
			case strings.Contains(joined, "nohup"):
				return nil, nil
			case strings.Contains(joined, "command -v evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "evener_resolve"):
				return []byte("/opt/evener/bin/evener\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
	}

	t.Run("IPv4 endpoint refuses the IPv6 wildcard listener", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // default 127.0.0.1:9180
		killed := false
		m := newTestManager(t, testRegistry(t, host), newRunner(&killed), Options{
			sleep: func(context.Context, time.Duration) error { return nil },
		})

		err := m.restartBare(context.Background(), host, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}, nil)
		if !errors.Is(err, ErrRestart) {
			t.Fatalf("err = %v, want ErrRestart for an IPv6-wildcard listener on an IPv4 endpoint", err)
		}
		if killed {
			t.Fatal("a listener that may not serve the IPv4 endpoint was killed")
		}
	})

	t.Run("IPv6 endpoint accepts the IPv6 wildcard listener", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Addr: "[::]:9180"}
		killed := false
		m := newTestManager(t, testRegistry(t, host), newRunner(&killed), Options{
			sleep: func(context.Context, time.Duration) error { return nil },
		})

		if err := m.restartBare(context.Background(), host, m.opts.controllerVersion(), snapshotPinGitSHA(), hubIdentity{}, nil); err != nil {
			t.Fatalf("restartBare: %v (an IPv6 wildcard owns the IPv6 endpoint it was configured for)", err)
		}
		if !killed {
			t.Fatal("the hub owning the configured IPv6 endpoint was not stopped")
		}
	})
}
