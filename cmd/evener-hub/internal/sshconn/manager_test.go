package sshconn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

func newTestManager(t *testing.T, reg *hostreg.Registry, fr *fakeRunner, opts Options) *Manager {
	t.Helper()
	opts.Runner = fr
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	m := New(reg, opts)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func goodStartFn(t *testing.T) func(context.Context, []string, io.Writer) (Stdio, error) {
	return func(context.Context, []string, io.Writer) (Stdio, error) {
		return newFakeBridge(appwire.ProtocolVersion).stdio, nil
	}
}

func TestEnsurePreflightAndAttach(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if ch.Client() == nil || ch.Transport() == nil {
		t.Fatal("channel missing client/transport")
	}
	pf := ch.Preflight()
	if pf.OS != "linux" || pf.Arch != "amd64" {
		t.Fatalf("preflight os/arch = %s/%s, want linux/amd64", pf.OS, pf.Arch)
	}
	if pf.StateRoot != "/home/dev/.local/state/evener" {
		t.Fatalf("stateRoot = %q", pf.StateRoot)
	}
	if pf.ConfigRoot != "/home/dev/.config/evener" {
		t.Fatalf("configRoot = %q", pf.ConfigRoot)
	}
	if pf.Protocol != appwire.ProtocolVersion || pf.Version != "dev" {
		t.Fatalf("preflight version/protocol = %q/%q", pf.Version, pf.Protocol)
	}

	starts := fr.recordedStarts()
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	if want := channelArgv(m.opts, host); !equalArgv(starts[0], want) {
		t.Fatalf("Start argv:\n got %v\nwant %v", starts[0], want)
	}
	// Preflight must pass --protocol appwire.ProtocolVersion.
	for _, argv := range fr.recordedRuns() {
		if containsToken(argv, "launch-check") && !containsToken(argv, appwire.ProtocolVersion) {
			t.Fatalf("launch-check argv missing protocol: %v", argv)
		}
	}
}

func TestEnsureWithUserComposesDest(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", User: "bob"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	argv := fr.recordedStarts()[0]
	if !containsToken(argv, "bob@alpha.example") {
		t.Fatalf("Start argv missing composed dest: %v", argv)
	}
}

func TestEnsureIdempotentWhileAttached(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	ch2, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if ch1 != ch2 {
		t.Fatal("Ensure returned a different channel while attached")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
}

func TestEnsureUnknownHost(t *testing.T) {
	fr := &fakeRunner{}
	m := newTestManager(t, testRegistry(t), fr, Options{})
	_, err := m.Ensure(context.Background(), "nope")
	if !errors.Is(err, ErrHostNotFound) {
		t.Fatalf("err = %v, want ErrHostNotFound", err)
	}
	if len(fr.recordedRuns()) != 0 || len(fr.recordedStarts()) != 0 {
		t.Fatal("unknown host triggered runner calls")
	}
}

func TestEnsureStderrWiredToSink(t *testing.T) {
	var buf bytes.Buffer
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
			_, _ = stderr.Write([]byte("ssh: some diagnostic\n"))
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{Stderr: &buf})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !strings.Contains(buf.String(), "some diagnostic") {
		t.Fatalf("stderr sink = %q", buf.String())
	}
}

// TestEnsureProtocolMismatchReachesTheDeployPath proves the High fix: a host
// whose on-disk binary speaks a different appwire protocol is not refused
// terminally before the deploy path has run (deploy is over ssh, not appwire).
// With no deploy configured the outcome is the deploy failure, not a protocol
// refusal; with one configured the host is upgraded and attaches.
func TestEnsureProtocolMismatchReachesTheDeployPath(t *testing.T) {
	t.Run("no deploy configured refuses the protocol terminally", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
		override := map[string][]byte{
			"launch-check": []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`),
		}
		fr := &fakeRunner{runFn: cannedRun(override), startFn: goodStartFn(t)}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		_, err := m.Ensure(context.Background(), "alpha")
		// With no BuildSource/BuildBinary there is no deploy to offer, so the
		// incompatible protocol is refused terminally instead of retrying a deploy
		// that can never run (the infinite ErrDeploy loop the review named).
		if !errors.Is(err, ErrProtocolIncompatible) {
			t.Fatalf("err = %v, want ErrProtocolIncompatible (no deploy can upgrade the host)", err)
		}
		if got := len(fr.recordedStarts()); got != 0 {
			t.Fatalf("Start calls = %d, want 0 (no bridge before the upgrade)", got)
		}
	})

	t.Run("deploy upgrades a protocol-incompatible host", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := deployRunner(t,
			func(call int) ([]byte, error) {
				if call == 0 {
					return []byte(`{"protocol":"evener-appwire-v4","version":"oldsha","launch_flags":["api-log"]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			},
			func(int) ([]byte, error) { return []byte(`{"version":"newsha"}`), nil },
		)
		m := newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "newsha",
			BuildBinary:               writeStageBinary,
		})

		ch, err := m.Ensure(context.Background(), "alpha")
		if err != nil {
			t.Fatalf("Ensure: %v (a protocol-incompatible host must be upgradable over ssh)", err)
		}
		if got := ch.Preflight().Version; got != "newsha" {
			t.Fatalf("channel version = %q, want newsha", got)
		}
	})
}

// TestEnsureProtocolRefusedByRunReachesTheDeployPath covers the sibling case:
// the on-disk binary refuses `launch-check --protocol` outright, so there is no
// parsed contract at all. That is still a fact about a host reachable over ssh,
// so when a deploy is configured the deploy path runs before any refusal; with no
// deploy configured there is nothing to install, so the refusal is terminal.
func TestEnsureProtocolRefusedByRunReachesTheDeployPath(t *testing.T) {
	t.Run("no deploy configured refuses the refused protocol terminally", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
		fr := &fakeRunner{
			runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				if containsToken(argv, "launch-check") {
					return []byte(`unsupported appwire protocol "evener-appwire-v5" (supported "evener-appwire-v4")`), errors.New("exit status 1")
				}
				return cannedRun(nil)(context.Background(), argv, nil)
			},
			startFn: goodStartFn(t),
		}
		m := newTestManager(t, testRegistry(t, host), fr, Options{})

		_, err := m.Ensure(context.Background(), "alpha")
		// The refusal is a fact about the binary, but with no deploy configured
		// there is no build to install, so it is terminal here rather than a
		// retryable deploy failure.
		if !errors.Is(err, ErrProtocolIncompatible) {
			t.Fatalf("err = %v, want ErrProtocolIncompatible (no deploy can upgrade the host)", err)
		}
		if got := len(fr.recordedStarts()); got != 0 {
			t.Fatalf("Start calls = %d, want 0", got)
		}
	})

	t.Run("deploy upgrades a host whose binary refused the protocol", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := deployRunner(t,
			func(call int) ([]byte, error) {
				if call == 0 {
					return []byte(`unsupported appwire protocol "evener-appwire-v5" (supported "evener-appwire-v4")`), errors.New("exit status 1")
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"newsha","launch_flags":["api-log"]}`), nil
			},
			func(int) ([]byte, error) { return []byte(`{"version":"newsha"}`), nil },
		)
		m := newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "newsha",
			BuildBinary:               writeStageBinary,
		})

		ch, err := m.Ensure(context.Background(), "alpha")
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if got := ch.Preflight().Version; got != "newsha" {
			t.Fatalf("channel version = %q, want newsha", got)
		}
	})
}

// TestEnsureMissingAPILogFlagIsDeployable covers the equal-version case the High
// finding named: two unstamped dev builds report "dev", so the version match says
// nothing and the flag check must itself be part of the deploy trigger. Only a
// deploy that still leaves the flag missing is a terminal refusal.
func TestEnsureMissingAPILogFlagIsDeployable(t *testing.T) {
	t.Run("still missing after the deploy is a terminal refusal", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		built := false
		fr := deployRunner(t,
			func(int) ([]byte, error) {
				return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":[]}`), nil
			},
			func(int) ([]byte, error) { return []byte(`{"version":"dev"}`), nil },
		)
		m := newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "dev",
			BuildBinary: func(_ context.Context, _, _, out string) error {
				built = true
				return os.WriteFile(out, []byte("bin"), 0o755)
			},
		})

		_, err := m.Ensure(context.Background(), "alpha")
		if !errors.Is(err, ErrLaunchContract) {
			t.Fatalf("err = %v, want ErrLaunchContract", err)
		}
		if !built {
			t.Fatal("the deploy path was never offered before the refusal")
		}
		if got := len(fr.recordedStarts()); got != 0 {
			t.Fatalf("Start calls = %d, want 0", got)
		}
	})

	t.Run("deploy that advertises the flag lets the host attach", func(t *testing.T) {
		host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
		fr := deployRunner(t,
			func(call int) ([]byte, error) {
				if call == 0 {
					return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":[]}`), nil
				}
				return []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":["api-log"]}`), nil
			},
			func(int) ([]byte, error) { return []byte(`{"version":"dev"}`), nil },
		)
		m := newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "dev",
			BuildBinary:               writeStageBinary,
		})

		if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	})
}

func TestEnsureInitializeProtocolMismatch(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			return newFakeBridge("evener-appwire-v4").stdio, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("err = %v, want ErrProtocolIncompatible", err)
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
}

func TestEnsureUnsupportedHost(t *testing.T) {
	cases := []struct {
		name     string
		override map[string][]byte
	}{
		{"unknown os", map[string][]byte{"uname -s": []byte("FreeBSD\n")}},
		{"unknown arch", map[string][]byte{"uname -m": []byte("i686\n")}},
		{"unsupported target", map[string][]byte{"uname -s": []byte("Darwin\n"), "uname -m": []byte("x86_64\n")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
			fr := &fakeRunner{runFn: cannedRun(tc.override), startFn: goodStartFn(t)}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.Ensure(context.Background(), "alpha")
			if !errors.Is(err, ErrUnsupportedHost) {
				t.Fatalf("err = %v, want ErrUnsupportedHost", err)
			}
			if got := len(fr.recordedStarts()); got != 0 {
				t.Fatalf("Start calls = %d, want 0 (no deploy, no bridge)", got)
			}
		})
	}
}

func TestEnsurePreflightDecodeFailure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(map[string][]byte{"launch-check": []byte("not json")}), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("err = %v, want ErrPreflightDecode", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
}

func TestEnsureStartFailureClassification(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	cases := []struct {
		name    string
		stderr  string
		wantErr error
	}{
		{
			// Under BatchMode ssh never prompts, so a refusal must not be retried.
			name:    "auth refusal is terminal",
			stderr:  "bob@alpha.example: Permission denied (publickey).\n",
			wantErr: ErrSSHAuth,
		},
		{
			// A start-level failure with no auth marker stays retryable.
			name:    "transport failure stays retryable",
			stderr:  "ssh: connect to host alpha.example port 22: Connection refused\n",
			wantErr: ErrSSHStart,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{
				runFn: cannedRun(nil),
				startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
					_, _ = stderr.Write([]byte(tc.stderr))
					return nil, errors.New("exit status 255")
				},
			}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			_, err := m.Ensure(context.Background(), "alpha")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestEnsureAfterCloseFailsWithErrManagerClosed(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("err = %v, want ErrManagerClosed", err)
	}
	if len(fr.recordedRuns()) != 0 || len(fr.recordedStarts()) != 0 {
		t.Fatal("Ensure on a closed manager still ran commands")
	}
}

// A dropped link makes its channel unusable before the supervisor has replaced
// it. Ensure must never hand that dead handle back to a caller.
func TestEnsureReplacesLostChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	ch1.markLost()

	ch2, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if ch2 == ch1 {
		t.Fatal("Ensure returned the lost channel")
	}
	if ch2.isClosed() || ch2.isLost() {
		t.Fatal("replacement channel is not usable")
	}
	waitClosed(t, ch1)
}

// The registry is the authority on a name's spelling: a padded name must find
// the same host, channel entry, and per-host lock as the canonical one.
func TestEnsureNormalizesHostName(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	ch1, err := m.Ensure(context.Background(), "  alpha  ")
	if err != nil {
		t.Fatalf("Ensure(padded): %v", err)
	}
	ch2, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure(canonical): %v", err)
	}
	if ch1 != ch2 {
		t.Fatal("a padded name produced a second channel")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
}

// Preflight hands out host facts; a caller mutating them must not reach back
// into the channel's own state.
func TestPreflightIsIndependentCopy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	pf := ch.Preflight()
	if len(pf.LaunchFlags) == 0 {
		t.Fatal("no launch flags to mutate")
	}
	pf.LaunchFlags[0] = "tampered"
	if got := ch.Preflight().LaunchFlags[0]; got != "api-log" {
		t.Fatalf("Preflight() shares its LaunchFlags backing array: got %q", got)
	}
}

// A reconnect attempt inherits baseCtx, which lives until Close. Without a
// per-attempt bound a single remote command that never returns would hold the
// host's lock forever, stalling every later reconnect and Ensure.
func TestReconnectAttemptIsBounded(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var mu sync.Mutex
	hang := false
	sleeps := 0
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			hung := hang
			mu.Unlock()
			if hung {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase:    time.Millisecond,
		BackoffMax:     time.Millisecond,
		attemptTimeout: 20 * time.Millisecond,
		sleep: func(context.Context, time.Duration) error {
			mu.Lock()
			sleeps++
			mu.Unlock()
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	mu.Lock()
	hang = true
	mu.Unlock()
	ch.markLost()

	// Two sleeps means the hung attempt ended at the bound and the loop went on
	// to another attempt.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		got := sleeps
		mu.Unlock()
		if got >= 2 {
			return
		}
		select {
		case <-deadline:
			t.Fatal("a hung reconnect attempt never timed out: the host lock is held for good")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// When Ensure wins the race against a dropped channel's supervisor and its own
// attach fails, the host must still recover through supervision rather than sit
// disconnected with nothing retrying it.
func TestFailedEnsureDuringLinkDropStillRecovers(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	failNext := false
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			mu.Lock()
			fail := failNext
			failNext = false
			mu.Unlock()
			if fail {
				return nil, errors.New("ssh: connect to host alpha.example port 22: Connection refused")
			}
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached) // drain the initial attach

	// Freeze the dropped channel's supervisor at its precondition check so
	// Ensure can win the race, then let both run.
	lock := m.hostLock("alpha")
	lock.Lock()
	mu.Lock()
	failNext = true
	mu.Unlock()
	ch1.markLost()
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	lock.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ensure never returned")
	}
	// Either goroutine may have consumed the failure; both orders must end with
	// the host attached again and supervised.
	waitForEvent(t, events, EventAttached)
}

// The supervisor's backoff sleep can reach BackoffMax. A concurrent Ensure must
// not be stuck behind it.
func TestReconnectSleepDoesNotHoldHostLock(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	starts := 0
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}

	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	sleepFn := func(context.Context, time.Duration) error {
		enterOnce.Do(func() { close(sleepEntered) })
		<-release
		return nil
	}
	defer releaseOnce.Do(func() { close(release) })

	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Second,
		sleep:       sleepFn,
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ch1.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor never entered the backoff sleep")
	}

	type result struct {
		ch  *Channel
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := m.Ensure(context.Background(), "alpha")
		done <- result{ch: ch, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Ensure during backoff: %v", r.err)
		}
		if r.ch == ch1 {
			t.Fatal("Ensure returned the lost channel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure blocked while the supervisor slept: the host lock is held across the backoff")
	}

	// Waking up, the supervisor must stand down instead of clobbering the fresh
	// channel with a second reconnect.
	releaseOnce.Do(func() { close(release) })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 2 {
		t.Fatalf("Start calls = %d, want 2 (initial + the Ensure attach)", got)
	}
}

// Close during an in-flight attach must not leave a channel behind that nothing
// will supervise.
func TestEnsureInFlightCloseIsRejected(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			startOnce.Do(func() { close(started) })
			<-release
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	defer releaseOnce.Do(func() { close(release) })

	m := newTestManager(t, reg, fr, Options{})
	errCh := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		errCh <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("attach never reached the runner")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("in-flight Ensure err = %v, want ErrManagerClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Ensure did not return")
	}
	if ch := m.currentChannel("alpha"); ch != nil {
		t.Fatal("closed manager kept a channel")
	}
}

// A preflight that cannot authenticate is terminal: the reconnect loop must not
// hammer a host that cannot let us in.
func TestReconnectPreflightAuthFailureIsTerminal(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	authFail := false
	starts := 0
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			fail := authFail
			mu.Unlock()
			if fail {
				auth := "bob@alpha.example: Permission denied (publickey).\n"
				return []byte(auth), &RunError{Stderr: []byte(auth), Err: errors.New("exit status 255")}
			}
			return canned(ctx, argv, stdin)
		},
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mu.Lock()
	authFail = true
	mu.Unlock()
	ch.markLost()

	ev := waitForEvent(t, events, EventFailed)
	if !errors.Is(ev.Err, ErrSSHAuth) {
		t.Fatalf("failed event err = %v, want ErrSSHAuth", ev.Err)
	}
	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("Start calls = %d, want 1 (an auth refusal must not re-attach)", got)
	}
}

// A replacement channel must be announced only after its predecessor's Detached:
// a consumer that installs and removes sources by these events would otherwise
// leak the old registration.
func TestEnsureAnnouncesDetachForRetiredChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 256)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Freeze the dropped channel's supervisor behind the host lock and queue
	// Ensure first, so Ensure is the one that retires the stale channel.
	lock := m.hostLock("alpha")
	lock.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Ensure(context.Background(), "alpha")
	}()
	time.Sleep(10 * time.Millisecond)
	ch1.markLost()
	time.Sleep(10 * time.Millisecond)
	lock.Unlock()
	<-done

	var kinds []EventKind
	for {
		select {
		case ev := <-events:
			kinds = append(kinds, ev.Kind)
			continue
		default:
		}
		break
	}
	detaches, firstDetach, lastAttach := 0, -1, -1
	for i, k := range kinds {
		switch k {
		case EventDetached:
			detaches++
			if firstDetach < 0 {
				firstDetach = i
			}
		case EventAttached:
			lastAttach = i
		}
	}
	if detaches != 1 {
		t.Fatalf("detach events for the retired channel = %d, want exactly 1: %v", detaches, kinds)
	}
	if lastAttach < firstDetach {
		t.Fatalf("replacement announced before the retired channel was detached: %v", kinds)
	}
}

// OnEvent documents that callbacks run with the per-host lock held; that is what
// orders a Detached before the Attached that follows it. A callback that finds
// the lock free means the guarantee is gone.
func TestAttachEventIsEmittedUnderTheHostLock(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}

	var mu sync.Mutex
	checked, unlocked := 0, 0
	var m *Manager
	m = newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) {
			if ev.Kind != EventAttached {
				return
			}
			lock := m.hostLock(ev.Host)
			acquired := lock.TryLock()
			if acquired {
				lock.Unlock()
			}
			mu.Lock()
			checked++
			if acquired {
				unlocked++
			}
			mu.Unlock()
		},
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if checked == 0 {
		t.Fatal("no attach event observed")
	}
	if unlocked != 0 {
		t.Fatalf("%d of %d attach events were emitted without the host lock", unlocked, checked)
	}
}

// A remote program's own "Permission denied" must not read as an ssh auth
// refusal: that would end the reconnect loop for good over a problem that is not
// authentication.
func TestRemoteProgramPermissionDeniedIsNotAuthFailure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if strings.HasSuffix(strings.Join(argv, " "), "uname -s") {
				denied := "Permission denied\n"
				return []byte(denied), &RunError{Stdout: []byte(denied), Err: errors.New("exit status 1")}
			}
			return cannedRun(nil)(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.Ensure(context.Background(), "alpha")
	if errors.Is(err, ErrSSHAuth) {
		t.Fatalf("a remote program's stdout was read as an ssh auth refusal: %v", err)
	}
	if !errors.Is(err, ErrSSHStart) {
		t.Fatalf("err = %v, want retryable ErrSSHStart", err)
	}
}

// The first attach is bounded too: a hung remote command must not hold the host
// lock only because the caller passed a deadline-less context.
func TestInitialPreflightIsBounded(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: func(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{attemptTimeout: 20 * time.Millisecond})
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ensure succeeded against a hung host")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure never gave up: the initial preflight is unbounded")
	}
}

// Host hands out the registry entry; a caller mutating it must not reach back
// into the channel's own state.
func TestHostIsIndependentCopy(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", Roots: []string{"/srv/evener"}}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := ch.Host()
	if len(got.Roots) == 0 {
		t.Fatal("no roots to mutate")
	}
	got.Roots[0] = "tampered"
	if ch.Host().Roots[0] != "/srv/evener" {
		t.Fatal("Host() shares its Roots backing array")
	}
}

// waitClosed fails unless ch reaches the closed state within the deadline. A
// supervisor may close a channel just after releasing the host lock, so a
// concurrent Ensure cannot assume it already happened.
func waitClosed(t *testing.T, ch *Channel) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ch.isClosed() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("channel was never closed")
}

func TestReconnectBackoffAndFreshStartWithoutHubStart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	var bridges []*fakeBridge
	var sleeps []time.Duration
	startCalls := 0

	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, _ io.Writer) (Stdio, error) {
			mu.Lock()
			defer mu.Unlock()
			startCalls++
			switch startCalls {
			case 1:
				b := newFakeBridge(appwire.ProtocolVersion)
				bridges = append(bridges, b)
				return b.stdio, nil
			case 2:
				return nil, errors.New("ssh: connect to host alpha.example port 22: Connection refused")
			default:
				b := newFakeBridge(appwire.ProtocolVersion)
				bridges = append(bridges, b)
				return b.stdio, nil
			}
		},
	}

	events := make(chan Event, 128)
	base := 10 * time.Millisecond
	recordSleep := func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return nil
	}
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: base,
		BackoffMax:  1 * time.Second,
		sleep:       recordSleep,
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	mu.Lock()
	if len(bridges) != 1 {
		mu.Unlock()
		t.Fatalf("bridges after initial attach = %d, want 1", len(bridges))
	}
	first := bridges[0]
	mu.Unlock()

	// Drop the link: the server side stops writing, the controller reads EOF.
	first.stdio.drop()

	waitForEvent(t, events, EventDetached)
	waitForEvent(t, events, EventAttached)

	mu.Lock()
	gotSleeps := append([]time.Duration(nil), sleeps...)
	gotStarts := startCalls
	mu.Unlock()

	wantSleeps := []time.Duration{base, 2 * base}
	if len(gotSleeps) != len(wantSleeps) {
		t.Fatalf("backoff sleeps = %v, want %v", gotSleeps, wantSleeps)
	}
	for i := range wantSleeps {
		if gotSleeps[i] != wantSleeps[i] {
			t.Fatalf("backoff sleep[%d] = %v, want %v", i, gotSleeps[i], wantSleeps[i])
		}
	}
	if gotStarts != 3 {
		t.Fatalf("Start calls = %d, want 3 (initial + failed retry + successful re-attach)", gotStarts)
	}

	// Re-attach must be a fresh bridge, never a hub-start command.
	for _, argv := range fr.recordedStarts() {
		if !containsToken(argv, "attach") {
			t.Fatalf("re-attach argv is not the attach form: %v", argv)
		}
	}
	assertNoHubStart(t, fr.recordedRuns(), fr.recordedStarts())
}

func TestReconnectAuthFailureDoesNotRetry(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	startCalls := 0
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
			mu.Lock()
			defer mu.Unlock()
			startCalls++
			if startCalls == 1 {
				return newFakeBridge(appwire.ProtocolVersion).stdio, nil
			}
			// Reconnect: ssh exits before the handshake with a BatchMode auth
			// failure on stderr, which the attach path classifies as ErrSSHAuth.
			_, _ = stderr.Write([]byte("bob@alpha.example: Permission denied (publickey).\n"))
			b := newFakeBridge(appwire.ProtocolVersion)
			b.stdio.drop()
			return b.stdio, nil
		},
	}
	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Force the channel's link down.
	ch.markLost()
	waitForEvent(t, events, EventFailed)

	mu.Lock()
	got := startCalls
	mu.Unlock()
	// Initial attach + exactly one reconnect attempt; the terminal error stops
	// the loop instead of spamming ssh.
	if got != 2 {
		t.Fatalf("Start calls = %d, want 2 (one terminal retry then stop)", got)
	}
}

func waitForEvent(t *testing.T, ch <-chan Event, kind EventKind) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for event %s", kind)
		}
	}
}

func assertNoHubStart(t *testing.T, runs, starts [][]string) {
	t.Helper()
	all := append(append([][]string(nil), runs...), starts...)
	for _, argv := range all {
		if containsToken(argv, "serve") {
			t.Fatalf("hub-start command issued: %v", argv)
		}
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "hub" && (argv[i+1] == "start" || argv[i+1] == "serve" || argv[i+1] == "run") {
				t.Fatalf("hub-start command issued: %v", argv)
			}
		}
	}
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsToken(argv []string, token string) bool {
	return slices.Contains(argv, token)
}
