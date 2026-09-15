package sshconn

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestManagerAttached(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	m := newTestManager(t, testRegistry(t, host), &fakeRunner{}, Options{})

	if m.Attached("alpha") {
		t.Fatal("never-ensured host reported attached")
	}
	ch := &Channel{done: make(chan struct{}), lost: make(chan struct{})}
	m.setChannel("alpha", ch)
	if !m.Attached("alpha") {
		t.Fatal("live channel not reported attached")
	}
	// A dropped link is unusable before the supervisor clears it: markLost closes
	// lost (child exit or monitor error) while done stays open.
	ch.markLost()
	if m.Attached("alpha") {
		t.Fatal("link-lost channel reported attached")
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("close channel: %v", err)
	}
	if m.Attached("alpha") {
		t.Fatal("closed channel reported attached")
	}
	if m.Attached("missing") {
		t.Fatal("unknown host reported attached")
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

func TestEnsureProtocolMismatchShortCircuitsBeforeStart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	override := map[string][]byte{
		"launch-check": []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`),
	}
	fr := &fakeRunner{runFn: cannedRun(override), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("err = %v, want ErrProtocolIncompatible", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0 (refused before bridge)", got)
	}
}

func TestEnsureProtocolRefusedByRunShortCircuits(t *testing.T) {
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
	if !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("err = %v, want ErrProtocolIncompatible", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
}

func TestEnsureMissingAPILogFlag(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	override := map[string][]byte{"launch-check": []byte(`{"protocol":"evener-appwire-v5","version":"dev","launch_flags":[]}`)}
	fr := &fakeRunner{runFn: cannedRun(override), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrLaunchContract) {
		t.Fatalf("err = %v, want ErrLaunchContract", err)
	}
	if got := len(fr.recordedStarts()); got != 0 {
		t.Fatalf("Start calls = %d, want 0", got)
	}
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

func TestEnsureStartFailureIsErrSSHStart(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
			// A BatchMode auth failure kills the child with no handshake.
			_, _ = stderr.Write([]byte("bob@alpha.example: Permission denied (publickey).\n"))
			return nil, errors.New("exit status 255")
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.Ensure(context.Background(), "alpha")
	if !errors.Is(err, ErrSSHStart) {
		t.Fatalf("err = %v, want ErrSSHStart", err)
	}
	// A start-level failure is ErrSSHStart; classification of an
	// initialize-time auth failure is covered by isAuthFailure's unit test.
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
