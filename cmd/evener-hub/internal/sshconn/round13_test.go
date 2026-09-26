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

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestRound13DirtyControllerDeployRefusalIsTerminal pins the round-thirteen
// Medium that a dirty controller with a build source entered an infinite deploy
// loop. `deployRequired` forces a deploy for a dirty "<sha>-dirty" version (a
// dirty version is not an identity: another dirty checkout at the same commit
// reports it too), but the production build path refuses to compile a dirty
// controller's tree (verifyBuildRevision), and the installer fallback refuses
// dirty before it can pin an artifact. `markDevDeployed` was therefore never
// reached, the forced deploy stayed forced, and the refusal was returned as a
// retryable ErrDeploy — so the supervisor cross-compiled forever while the host
// was never attached.
//
// The fix keeps round twelve's property (a dirty version is never treated as an
// identity match, so the deploy is still forced) and makes the refusal terminal
// instead of ErrDeploy: the supervisor stops, the cause is reported, and no
// build, restart, or attach is attempted. The test drives the decision and the
// supervisor's own per-iteration path (reconnectOnce), not BuildBinary alone —
// which is how the round-twelve test missed the loop.
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
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v6","version":"` + dirty + `","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			// The host already reports the controller's identical dirty version.
			return []byte(`{"version":"` + dirty + `","started_at":"2026-01-01T00:00:00Z","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`), nil
		case strings.Contains(joined, "list-units"):
			return []byte("evener-hub.service loaded active running Evener Hub\n"), nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}, startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: dirty,
		BuildBinary: func(_ context.Context, _, _, out string) error {
			builds++
			return os.WriteFile(out, []byte("staged-binary"), 0o755)
		},
		sleep: func(context.Context, time.Duration) error { return nil },
	})

	// Round twelve's rule stands: the identical dirty version on both sides is not
	// a match, so the deploy is still required.
	if !m.deployRequired(host.Name, Preflight{
		Host:             host.Name,
		LaunchCheckKnown: true,
		Protocol:         appwire.ProtocolVersion,
		Version:          dirty,
		LaunchFlags:      []string{requiredLaunchFlag},
	}, dirty) {
		t.Fatal("a dirty controller stopped forcing the deploy, so it could attach to a host whose code equality cannot prove")
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
		t.Fatalf("build attempts = %d, want 0 (a dirty controller has no deployable build)", builds)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("bridge Start calls = %d, want 0 (never attach to a host the dirty version cannot verify)", got)
	}
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

// TestRound13DeployTargetPreservesTheRunningHubExecutable pins the round-thirteen
// Medium that deployTarget ignored the executable the running hub was launched
// from: with no evener_path it pushed to whatever `evener` resolved to on the
// PATH (or the installer default), while the restart path proves the recovered
// hub executable is the target it is about to relaunch (hubExecutableMatches)
// and refuses to restart a hub running a different binary. The pushed file then
// already matched, so the deploy/restart pair looped forever. The running hub's
// own executable now wins, in the same order the installer fallback preserves
// (existingInstallableEvener).
func TestRound13DeployTargetPreservesTheRunningHubExecutable(t *testing.T) {
	const hubExe = "/home/dev/.local/bin/evener"
	const pathExe = "/usr/local/bin/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"} // no evener_path

	t.Run("the running hub's executable wins over the PATH binary", func(t *testing.T) {
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "lsof -ti :9180"):
				return []byte("4242\n"), nil
			case strings.Contains(joined, "/proc/4242/cmdline"):
				return []byte(hubExe + "\x00hub\x00-addr\x00127.0.0.1:9180\x00"), nil
			case strings.Contains(joined, "command -v evener"):
				return []byte(pathExe + "\n"), nil
			case strings.Contains(joined, "evener_resolve"):
				return []byte(hubExe + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		got, err := m.deployTarget(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
		if err != nil {
			t.Fatalf("deployTarget: %v", err)
		}
		if got != hubExe {
			t.Fatalf("deployTarget = %q, want the running hub's executable %q (pushing the PATH binary leaves the upgrade inert)", got, hubExe)
		}
		for _, argv := range fr.recordedRuns() {
			if strings.Contains(strings.Join(argv, " "), "command -v evener") {
				t.Fatalf("deployTarget resolved the PATH binary although the running hub named its own executable: %v", argv)
			}
		}
	})

	t.Run("with no running hub the PATH binary is still the target", func(t *testing.T) {
		fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "lsof -ti :9180"):
				return []byte(noListenerMarker + "\n"), nil
			case strings.Contains(joined, "command -v evener"):
				return []byte(pathExe + "\n"), nil
			case strings.Contains(joined, "evener_resolve"):
				return []byte(pathExe + "\n"), nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		}}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		got, err := m.deployTarget(context.Background(), host, Preflight{OS: "linux", Arch: "amd64", Home: "/home/dev"})
		if err != nil {
			t.Fatalf("deployTarget: %v", err)
		}
		if got != pathExe {
			t.Fatalf("deployTarget = %q, want the PATH binary %q when no hub is running", got, pathExe)
		}
	})
}

// TestRound13WaitHealthyRequiresProofAUnverifiableRestartTook pins the
// round-thirteen Medium that waitHealthy accepted `got.version == expected &&
// !sameProcessAs(replaced)` for "dev" and dirty versions. sameProcessAs is false
// both when two processes differ AND when either start time is unknown, so an
// old process reporting the expected non-unique version with no started_at
// satisfied a restart that never replaced anything. The strict rule is now the
// same one the pending-restart settle path uses (differentProcessFrom): the
// answer must prove it is a different process, with both start times known.
func TestRound13WaitHealthyRequiresProofAUnverifiableRestartTook(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	identity := func(version string, started time.Time) hubIdentity {
		return hubIdentity{version: version, startedAt: started}
	}
	at := func(minutes int) time.Time { return time.Date(2026, 1, 1, 0, minutes, 0, 0, time.UTC) }
	answer := func(id hubIdentity) []byte {
		if id.startedAt.IsZero() {
			return []byte(fmt.Sprintf(`{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`, id.version))
		}
		return []byte(fmt.Sprintf(`{"version":%q,"started_at":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			id.version, id.startedAt.Format(time.RFC3339)))
	}

	cases := []struct {
		name     string
		expected string
		replaced hubIdentity
		answer   hubIdentity
		wantErr  bool
	}{
		{
			// The finding's case: the answer carries the expected "dev" version but
			// no start time, so nothing proves the pre-restart process is gone.
			name:     "an answer without a start time is not a replacement",
			expected: "dev",
			replaced: identity("dev", at(0)),
			answer:   identity("dev", time.Time{}),
			wantErr:  true,
		},
		{
			// Fail closed on the other side: an unknown pre-restart identity proves
			// nothing either, even when the answer carries a start time.
			name:     "an unknown pre-restart identity fails closed",
			expected: "dev",
			replaced: identity("dev", time.Time{}),
			answer:   identity("dev", at(1)),
			wantErr:  true,
		},
		{
			name:     "a proven different process is accepted",
			expected: "dev",
			replaced: identity("dev", at(0)),
			answer:   identity("dev", at(1)),
			wantErr:  false,
		},
		{
			name:     "the pre-restart process itself is refused",
			expected: "dev",
			replaced: identity("dev", at(0)),
			answer:   identity("dev", at(0)),
			wantErr:  true,
		},
		{
			// A stamped version is still a content identity: equality proves the
			// deployed build is serving, whether or not any start time is known.
			name:     "a stamped version still decides on its own",
			expected: "newsha",
			replaced: identity("newsha", time.Time{}),
			answer:   identity("newsha", time.Time{}),
			wantErr:  false,
		},
		{
			name:     "a dirty version needs the same proof",
			expected: "abc1234-dirty",
			replaced: identity("abc1234-dirty", time.Time{}),
			answer:   identity("abc1234-dirty", at(1)),
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{runFn: func(_ context.Context, _ []string, _ io.Reader) ([]byte, error) {
				return answer(tc.answer), nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{
				sleep: func(context.Context, time.Duration) error { return nil },
			})

			err := m.waitHealthy(context.Background(), host, tc.expected, "", tc.replaced)
			if (err != nil) != tc.wantErr {
				t.Fatalf("waitHealthy = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrRestart) {
				t.Fatalf("waitHealthy error = %v, want ErrRestart so the restart is retried", err)
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
		case strings.Contains(joined, "XDG_STATE_HOME"):
			return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
		case strings.Contains(joined, "launch-check"):
			return []byte(`{"protocol":"evener-appwire-v6","version":"dev","launch_flags":["api-log"]}`), nil
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

		err := m.restartBare(context.Background(), host, hubIdentity{})
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

		if err := m.restartBare(context.Background(), host, hubIdentity{}); err != nil {
			t.Fatalf("restartBare: %v (an IPv6 wildcard owns the IPv6 endpoint it was configured for)", err)
		}
		if !killed {
			t.Fatal("the hub owning the configured IPv6 endpoint was not stopped")
		}
	})
}
