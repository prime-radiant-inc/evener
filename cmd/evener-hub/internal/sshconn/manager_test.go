package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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

// gateHook replaces the fixed sleeps that used to order a test goroutine against
// a host gate the test holds. Its hook signals arrival at the gate and parks the
// caller until open, so the test decides which contender runs first rather than
// hoping a sleep was long enough. A test must arm it before triggering the race;
// hook panics on an earlier call, so arming too late fails loudly.
type gateHook struct {
	arrived  chan struct{}
	release  chan struct{}
	armed    atomic.Bool
	once     sync.Once
	openOnce sync.Once
}

func newGateHook() *gateHook {
	return &gateHook{arrived: make(chan struct{}), release: make(chan struct{})}
}

// arm begins the interleaving the gate holds: from here hook calls signal and
// park, and a hook call before it panics.
func (g *gateHook) arm() { g.armed.Store(true) }

// hook reports the first armed caller's arrival and parks it; a later contender
// arriving before open proceeds without parking, since the arrival it would
// report has already been observed. A call before arm is a test ordering bug —
// the hook is not holding anything yet — so it panics rather than letting the
// manager run past the gate unnoticed.
func (g *gateHook) hook() {
	if !g.armed.Load() {
		panic("gateHook.hook called before arm: the test armed too late, so the hook is not holding the contender it means to")
	}
	var first bool
	g.once.Do(func() {
		first = true
		close(g.arrived)
	})
	if first {
		<-g.release
	}
}

func (g *gateHook) wait(t *testing.T, what string) {
	t.Helper()
	select {
	case <-g.arrived:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s to reach the host gate", what)
	}
}

// open releases a parked caller. Idempotent, so a test can defer it and still
// release explicitly once the interleaving is established.
func (g *gateHook) open() { g.openOnce.Do(func() { close(g.release) }) }

// A hook call before arm means the test armed too late: the hook would return
// silently, the manager would run straight through the gate the test believes it
// holds, and the mistake would surface only as a later timeout. Reproduce that
// mistake directly and require it to be loud.
func TestGateHookHookBeforeArmFailsLoudly(t *testing.T) {
	g := newGateHook()
	defer func() {
		if recover() == nil {
			t.Fatal("gateHook.hook before arm returned quietly; the late-arm ordering bug would stay silent")
		}
	}()
	g.hook()
}

// exitSignal turns "the supervisor stood down" into an observation. Tests that
// used to sleep long enough to hope a supervisor had finished wait on it
// instead, so the assertion is ordered against the supervisor's own exit.
type exitSignal struct {
	mu     sync.Mutex
	n      int
	want   int
	closed bool
	done   chan struct{}
}

func newExitSignal(want int) *exitSignal {
	return &exitSignal{want: want, done: make(chan struct{})}
}

func (s *exitSignal) hook(string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if !s.closed && s.n >= s.want {
		s.closed = true
		close(s.done)
	}
}

func (s *exitSignal) wait(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %d supervisor(s) to return", s.want)
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

// A link-lost channel is unusable in the window after markLost closes lost but
// before the supervisor clears and replaces it. Ensure must reattach rather than
// hand back the dead channel that Attached already reports as detached.
func TestEnsureReplacesLinkLostChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	dead := &Channel{done: make(chan struct{}), lost: make(chan struct{})}
	dead.markLost()
	m.publishChannel("alpha", dead, host)
	if m.Attached("alpha") {
		t.Fatal("link-lost channel reported attached")
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if ch == dead {
		t.Fatal("Ensure reused a link-lost channel")
	}
	if ch.isLost() {
		t.Fatal("Ensure returned a channel that had already lost its link")
	}
	if got := len(fr.recordedStarts()); got != 1 {
		t.Fatalf("Start calls = %d, want 1 (reattach)", got)
	}
}

// Round nine: Ensure owns the cleanup of the channel it supersedes. A link-lost
// channel is exactly the state Ensure replaces, and its supervisor cannot do the
// closing: it wakes only after Ensure releases the host lock, by which time the
// map holds the replacement, so supervise's stale-channel check returns before
// the close. Repeated connection losses would otherwise leak one ssh child and
// transport per loss — the loop below replaces three in a row.
func TestEnsureClosesEveryChannelItReplaces(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	superseded := make([]*Channel, 0, 3)
	for round := range 3 {
		dead := &Channel{done: make(chan struct{}), lost: make(chan struct{})}
		dead.markLost()
		m.publishChannel(host.Name, dead, host)

		live, err := m.Ensure(context.Background(), host.Name)
		if err != nil {
			t.Fatalf("Ensure round %d: %v", round, err)
		}
		if live == dead {
			t.Fatalf("round %d: Ensure reused the link-lost channel", round)
		}
		if live.isClosed() {
			t.Fatalf("round %d: Ensure closed the channel it had just installed", round)
		}
		superseded = append(superseded, dead)
	}
	if got := len(fr.recordedStarts()); got != 3 {
		t.Fatalf("Start calls = %d, want 3", got)
	}
	for round, dead := range superseded {
		if !dead.isClosed() {
			t.Fatalf("round %d: replaced channel left open; its ssh child and transport leak", round)
		}
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
	m.publishChannel("alpha", ch, host)
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

// publishChannel must stamp a channel with the registration the caller
// validated, not with whatever entry the registry holds when it runs: a re-read
// there is a TOCTOU. An update landing between the caller's
// hostreg.Registry.SameRegistration recheck and the publish would otherwise
// stamp a channel built from the pre-update entry with the post-update one,
// which MatchesRegistration then accepts for a row rendering the new
// configuration — the same "a row adopts another generation's channel and
// facts" defect the registration field exists to prevent, now on the publisher
// side. The registry here already holds the re-added entry (identical content,
// fresh generation) when the channel is published for the entry it was dialed
// for, so the channel must still pair with the dialed entry and refuse the
// visible one. Deterministic, no sleeps.
func TestPublishChannelStampsTheValidatedRegistrationNotTheVisibleOne(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)
	dialed, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("reg.Get: not registered")
	}
	// The remove/re-add the in-flight attach raced: same content, advanced
	// generation. This is what the registry holds when the publish runs, and
	// what a re-read there would have stamped onto the channel.
	if err := reg.Remove("alpha"); err != nil {
		t.Fatalf("reg.Remove: %v", err)
	}
	if err := reg.Add(host); err != nil {
		t.Fatalf("reg.Add after the remove: %v", err)
	}
	visible, ok := reg.Get("alpha")
	if !ok {
		t.Fatal("reg.Get after the re-add: not registered")
	}
	if hostreg.SameRegistration(dialed, visible) {
		t.Fatal("the re-add did not advance the registration; the test would exercise nothing")
	}

	m := newTestManager(t, reg, &fakeRunner{}, Options{})
	ch := &Channel{lost: make(chan struct{}), done: make(chan struct{})}
	if !m.publishChannel("alpha", ch, dialed) {
		t.Fatal("publishChannel refused a live manager")
	}
	if !ch.MatchesRegistration(dialed) {
		t.Fatal("the channel does not pair with the registration it was dialed for")
	}
	if ch.MatchesRegistration(visible) {
		t.Fatal("the channel adopted the entry the registry holds now, not the one it was dialed for")
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
			func(call int) ([]byte, error) {
				// A real hub always reports started_at (cmd/evener-hub/web_api.go
				// handleAPIHealth). The pre-restart probe (call 0) and the answers
				// after the restart carry different start times, which is what lets
				// a "dev" restart be verified as a replacement (round thirteen).
				return []byte(fmt.Sprintf(`{"version":"dev","started_at":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
					time.Date(2026, 1, 1, 0, call, 0, 0, time.UTC).Format(time.RFC3339))), nil
			},
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
			func(call int) ([]byte, error) {
				// As above: the hub reports a start time, and the restarted process
				// reports a different one.
				return []byte(fmt.Sprintf(`{"version":"dev","started_at":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
					time.Date(2026, 1, 1, 0, call, 0, 0, time.UTC).Format(time.RFC3339))), nil
			},
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
			// A failed Start never spawned a remote command, so the marker is
			// necessarily ssh's own — the one case classified ErrSSHAuth. It is still
			// retryable: a real spawn failure carries no diagnostic, so an
			// authentication refusal ordinarily reaches the manager as ErrSSHStart,
			// and retrying is the safe side of that ambiguity (see ErrSSHAuth).
			name:    "auth-shaped start failure is classified but stays retryable",
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
			if isTerminal(tc.wantErr) {
				t.Fatalf("%v must stay retryable", tc.wantErr)
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

// A canceled Ensure must not park behind another caller's (or a supervisor's)
// long preflight/attach on the same host. The per-host gate is context-aware, so
// the caller gets its own error back instead of waiting out the holder.
func TestEnsureHonorsContextWhileWaitingForHostLock(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	arrival := newGateHook()
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		beforeHostGate: func(string) { arrival.hook() },
	})

	// Hold the host gate, the way a long preflight or attach does. The
	// acquisition is paired so the gate entry comes back down with the test.
	lock := m.hostLock("alpha")
	defer m.releaseHostLock("alpha")
	lock.Lock()
	defer lock.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	arrival.arm()
	go func() {
		_, err := m.Ensure(ctx, "alpha")
		done <- err
	}()
	// Wait for the caller to reach the gate — an explicit handoff, so the waiting
	// path is the one under test instead of a race against its arrival — then let
	// it through the hook. The gate is still held, so it parks there, and the
	// cancel lands on the waiting path.
	arrival.wait(t, "the caller")
	arrival.open()
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure with a canceled context = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure stayed parked on the host gate despite its canceled context")
	}
}

// An already-canceled Ensure must report the context, not hand back an attached
// channel the caller can no longer use.
func TestEnsureCanceledContextDoesNotReturnALiveChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := m.Ensure(ctx, "alpha")
	if err == nil {
		t.Fatal("an already-canceled Ensure returned a channel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if ch != nil {
		t.Fatalf("Ensure returned a channel with a canceled context: %+v", ch)
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

// When Ensure and a dropped channel's supervisor both run for the host and
// Ensure's own attach fails, the host must still recover through supervision
// rather than sit disconnected with nothing retrying it. Both orders are
// constructed exactly: one parks the supervisor so Ensure consumes the injected
// failure, the other parks Ensure so the supervisor consumes it. Either way the
// host must end attached again and supervised.
func TestFailedEnsureDuringLinkDropStillRecovers(t *testing.T) {
	cases := []struct {
		name       string
		parkEnsure bool
	}{
		{"Ensure runs first and consumes the failure", false},
		{"the supervisor runs first and consumes the failure", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			supGate := newGateHook()
			ensureGate := newGateHook()
			// beforeHostGate runs at the top of every Ensure, the initial attach
			// below included. Only the contender may reach the gate: the initial
			// attach has to run free, and gateHook.hook panics on a pre-arm call.
			var attached atomic.Bool
			opts := Options{
				OnEvent:     func(ev Event) { events <- ev },
				BackoffBase: time.Millisecond,
				BackoffMax:  time.Millisecond,
				sleep:       func(context.Context, time.Duration) error { return nil },
				jitter:      func(d time.Duration) time.Duration { return d },
			}
			if tc.parkEnsure {
				opts.beforeHostGate = func(string) {
					if attached.Load() {
						ensureGate.hook()
					}
				}
			} else {
				opts.beforeSuperviseGate = func(string, *Channel) { supGate.hook() }
			}
			m := newTestManager(t, reg, fr, opts)
			ch1, err := m.Ensure(context.Background(), "alpha")
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			waitForEvent(t, events, EventAttached) // drain the initial attach

			// Park the chosen contender at the host gate, then let the other one
			// run to completion, so which goroutine consumes the failure is exact.
			gate := supGate
			if tc.parkEnsure {
				gate = ensureGate
			}
			gate.arm()
			attached.Store(true)
			defer gate.open()

			// Armed before the drop: the supervisor reaches beforeSuperviseGate the
			// moment it wakes on markLost, and an unarmed hook would let it through.
			mu.Lock()
			failNext = true
			mu.Unlock()
			ch1.markLost()

			done := make(chan error, 1)
			go func() {
				_, err := m.Ensure(context.Background(), "alpha")
				done <- err
			}()
			gate.wait(t, "the contender under test")

			if tc.parkEnsure {
				// The supervisor runs freely and eats the injected failure; its
				// recovery Attached is the proof, so Ensure is released only after it.
				waitForEvent(t, events, EventAttached)
				gate.open()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("Ensure after the supervisor recovered = %v, want the live channel", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("Ensure never returned")
				}
				return
			}
			// Ensure runs first and eats the failure; the parked supervisor must
			// still find the host its own and recover it.
			select {
			case err := <-done:
				if !errors.Is(err, ErrSSHStart) {
					t.Fatalf("Ensure = %v, want the retryable failure it consumed", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Ensure never returned")
			}
			gate.open()
			waitForEvent(t, events, EventAttached)
		})
	}
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

	// The retired supervisor is the only loop that ends here (the fresh channel's
	// stays waiting on its link), so its exit is the observation that replaces the
	// sleep which used to stand in for "it had time to clobber the host by now".
	exited := newExitSignal(1)
	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:         func(ev Event) { events <- ev },
		BackoffBase:     time.Millisecond,
		BackoffMax:      time.Second,
		sleep:           sleepFn,
		jitter:          func(d time.Duration) time.Duration { return d },
		superviseExited: exited.hook,
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
	exited.wait(t)
	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 2 {
		t.Fatalf("Start calls = %d, want 2 (initial + the Ensure attach)", got)
	}
}

// Close during an in-flight attach must not leave a channel behind that nothing
// will supervise. The blocked Start models a spawn in flight and honours ctx the
// way execRunner.Start's spawn decision does, so Close's cancellation ends the
// attach and the barrier Close now enforces can complete.
func TestEnsureInFlightCloseIsRejected(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	started := make(chan struct{})
	var startOnce sync.Once
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(ctx context.Context, _ []string, _ io.Writer) (Stdio, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	// A long attempt bound on purpose: only Close can end this attach.
	m := newTestManager(t, reg, fr, Options{attemptTimeout: 30 * time.Second})
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

// A preflight whose completed ssh run exits 255 with an auth marker is
// ambiguous: ssh forwards the remote command's own status unchanged, so 255 does
// not prove the refusal is ssh's. It must stay retryable and keep the reconnect
// loop alive rather than stop it for good on a remote command's own exit.
func TestReconnectPreflightAuthMarkerIsRetryable(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	authFail := false
	canned := cannedRun(nil)
	sshExit := exitStatus(t, 255)
	auth := "bob@alpha.example: Permission denied (publickey).\n"
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			fail := authFail
			mu.Unlock()
			if fail {
				return []byte(auth), &RunError{Stderr: []byte(auth), Err: sshExit}
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 128)
	// The loop sleeps before each reconnect attempt, so the *second* sleep is the
	// proof that the first attempt failed and the loop took another turn: a
	// terminal classification would have returned after that attempt instead.
	// Park that second sleep until Close cancels, so the test does not spin.
	var sleeps atomic.Int32
	retried := make(chan struct{})
	var retriedOnce sync.Once
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep: func(ctx context.Context, _ time.Duration) error {
			if sleeps.Add(1) >= 2 {
				retriedOnce.Do(func() { close(retried) })
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mu.Lock()
	authFail = true
	mu.Unlock()
	ch.markLost()

	select {
	case <-retried:
	case <-time.After(5 * time.Second):
		t.Fatal("the ambiguous auth marker stopped the reconnect loop instead of retrying")
	}
	assertNoFailedEvent(t, events)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A replacement channel must be announced only after its predecessor's Detached:
// a consumer that installs and removes sources by these events would otherwise
// leak the old registration.
func TestEnsureAnnouncesDetachForRetiredChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	supGate := newGateHook()
	exited := newExitSignal(1)
	events := make(chan Event, 256)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:             func(ev Event) { events <- ev },
		BackoffBase:         time.Millisecond,
		BackoffMax:          time.Millisecond,
		sleep:               func(context.Context, time.Duration) error { return nil },
		jitter:              func(d time.Duration) time.Duration { return d },
		beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		superviseExited:     exited.hook,
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Park the dropped channel's supervisor just before the host gate, so Ensure —
	// not a race — is the one that retires the stale channel.
	supGate.arm()
	defer supGate.open()
	ch1.markLost()
	supGate.wait(t, "the dropped channel's supervisor")
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Let the parked supervisor wake and stand down for the replacement, so the
	// event stream is complete before it is read.
	supGate.open()
	exited.wait(t)

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
			defer m.releaseHostLock(ev.Host)
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

// A lost channel is not a closed manager. Conflating the two reported a
// recoverable link drop as terminal, and could hand a dead handle to a caller.
func TestChannelUsableClassification(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	m := newTestManager(t, testRegistry(t, host), &fakeRunner{}, Options{})

	newChannel := func() *Channel {
		return &Channel{lost: make(chan struct{}), done: make(chan struct{})}
	}
	lost := func() *Channel { c := newChannel(); c.markLost(); return c }
	closed := func() *Channel { c := newChannel(); close(c.done); return c }

	if err := m.channelUsable("alpha", newChannel()); err != nil {
		t.Fatalf("a live channel was classified as %v", err)
	}
	for name, ch := range map[string]*Channel{"lost": lost(), "closed": closed()} {
		err := m.channelUsable("alpha", ch)
		if err == nil {
			t.Fatalf("%s channel accepted as usable", name)
		}
		if errors.Is(err, ErrManagerClosed) {
			t.Fatalf("%s channel reported as a closed manager: %v", name, err)
		}
		if !errors.Is(err, ErrSSHStart) {
			t.Fatalf("%s channel error = %v, want the retryable ErrSSHStart class", name, err)
		}
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.channelUsable("alpha", newChannel()); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("after Close, classification = %v, want ErrManagerClosed", err)
	}
}

// Close can land between the replacement's publish and Ensure's validation, at
// which point the map holds the replacement and the replaced channel is in
// nobody's hands: Ensure still has to reap it.
func TestEnsureRetiresReplacedChannelWhenCloseRaces(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	attaches := 0
	var kinds []EventKind
	var m *Manager
	closed := make(chan error, 1)
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m = newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) {
			mu.Lock()
			kinds = append(kinds, ev.Kind)
			mu.Unlock()
			if ev.Kind != EventAttached {
				return
			}
			mu.Lock()
			attaches++
			second := attaches == 2
			mu.Unlock()
			if second {
				// Close publishes its closed flag before it takes any host lock, so
				// this lands while Ensure still holds the lock and before Ensure
				// validates the channel it just published. Waiting for the flag
				// keeps that interleaving deterministic; Close itself blocks on the
				// lock Ensure is holding until Ensure releases it.
				go func() { closed <- m.Close() }()
				waitUntil(t, "Close to publish its closed flag", m.isClosed)
			}
		},
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ch1.markLost()

	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("Ensure racing Close = %v, want ErrManagerClosed", err)
	}
	waitClosed(t, ch1)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned")
	}
	// The replacement Close discarded must not keep the map slot: Close is
	// terminal, so no channel may remain mapped once it returns.
	if ch := m.currentChannel("alpha"); ch != nil {
		t.Fatalf("a closed manager left a channel mapped: %v", ch)
	}

	// Consumers installed a source on the Attached; the Detached that follows is
	// what lets them drop it, so the pair has to be announced even on this path.
	mu.Lock()
	got := append([]EventKind(nil), kinds...)
	mu.Unlock()
	if len(got) < 2 || got[len(got)-2] != EventAttached || got[len(got)-1] != EventDetached {
		t.Fatalf("Close racing the announce did not pair Attached with Detached: %v", got)
	}
}

// Manager.Close must end an in-flight initial attach: otherwise its ssh child
// outlives the manager until the caller's deadline or the attempt bound. It must
// also be an event-quiescence barrier for that caller goroutine: the canceled
// attach emits no Disconnected and no Failed, and nothing lands after Close
// returns.
func TestCloseCancelsInFlightInitialEnsure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	entered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	var kinds []EventKind
	fr := &fakeRunner{
		runFn: func(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
		startFn: goodStartFn(t),
	}
	// A long attempt bound on purpose: only Close can end this attempt, so the
	// assertion below is about Close rather than about attemptLimit.
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		attemptTimeout: 30 * time.Second,
		OnEvent: func(ev Event) {
			mu.Lock()
			kinds = append(kinds, ev.Kind)
			mu.Unlock()
		},
	})

	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the attach never reached the runner")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	atClose := len(kinds)
	mu.Unlock()
	select {
	case err := <-done:
		// Close canceling the preflight is the manager being finished, not a
		// retryable transport failure: Ensure must report it as such.
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("Ensure after Close = %v, want ErrManagerClosed", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not cancel the in-flight initial attach")
	}
	// Close already accounted for the in-flight Ensure, so the return above cannot
	// be followed by one of its terminal/error events. The wait above is the
	// observation that replaces the sleep: it returns only once that Ensure
	// goroutine — the only emitter left — has run to completion.
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != atClose {
		t.Fatalf("events after Close returned: at Close %d, grew to %d (%v)", atClose, len(kinds), kinds)
	}
	for _, k := range kinds {
		if k == EventFailed {
			t.Fatalf("shutdown surfaced an EventFailed for a cancelled attach: %v", kinds)
		}
	}
}

// A terminal failure that a canceled in-flight Ensure finds must not be
// announced either: consumers shutting the manager down must see neither a Failed
// nor a Disconnected for a host that was never attached.
func TestCloseSuppressesInFlightEnsureTerminalEmissions(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	entered := make(chan struct{})
	var once sync.Once
	fr := &fakeRunner{
		runFn: func(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			// A terminal cause found concurrently with shutdown: Ensure keeps it
			// (it names what went wrong) but must not announce it.
			return nil, fmt.Errorf("%w: refused", ErrProtocolIncompatible)
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		attemptTimeout: 30 * time.Second,
		OnEvent:        func(ev Event) { events <- ev },
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the attach never reached the runner")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrProtocolIncompatible) {
			t.Fatalf("Ensure after Close = %v, want the terminal ErrProtocolIncompatible", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not cancel the in-flight initial attach")
	}
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventFailed {
				t.Fatalf("shutdown surfaced an EventFailed for a cancelled attach: %+v", ev)
			}
			if ev.Kind == EventState && ev.State == StateDisconnected {
				t.Fatalf("shutdown surfaced a Disconnected for an attach that never completed: %+v", ev)
			}
			continue
		default:
		}
		break
	}
}

// A replacement that dies after it is published must not orphan the host. The
// channel it displaced (stale) still has a supervisor blocked on the host lock,
// and clearing the map would leave that supervisor owning nothing and stand down.
// The lost replacement has to give the slot back so supervision survives.
func TestLostPublishedReplacementPreservesSupervision(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var drop atomic.Bool
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
	supGate := newGateHook()
	exited := newExitSignal(1)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		// Drop exactly the second Ensure's just-published channel, between the
		// publish and the post-publish validation.
		afterPublish: func(_ string, ch *Channel) {
			if drop.CompareAndSwap(true, false) {
				ch.markLost()
			}
		},
		BackoffBase:         time.Millisecond,
		BackoffMax:          time.Millisecond,
		sleep:               func(context.Context, time.Duration) error { return nil },
		jitter:              func(d time.Duration) time.Duration { return d },
		beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		superviseExited:     exited.hook,
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Park ch1's supervisor at the host gate, then drop ch1, so Ensure is the
	// goroutine that retires the stale channel exactly rather than by a margin.
	supGate.arm()
	defer supGate.open()
	drop.Store(true)
	ch1.markLost()
	supGate.wait(t, "ch1's supervisor")
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrSSHStart) {
		t.Fatalf("Ensure = %v, want the retryable drop", err)
	}
	supGate.open()

	// The discarded replacement was Ensure's own attach (Start #2). ch1's
	// supervisor can only produce Start #3 if the slot was restored to it: the
	// pre-fix clearChannel left it nothing to own. Its exit follows that
	// re-attach, so waiting on the exit orders the count without a poll.
	exited.wait(t)
	mu.Lock()
	got := starts
	mu.Unlock()
	if got < 3 {
		t.Fatalf("Start calls = %d, want at least 3: the lost replacement orphaned the host", got)
	}
}

// A reconnect that publishes a channel whose link drops before the announcement
// must not announce it: the initial Ensure path revalidates after publishing and
// the reconnect path must too, or a consumer installs a source for a channel
// that is already gone.
func TestReconnectLostPublishedChannelIsNotAnnounced(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var drop atomic.Bool
	var armed atomic.Bool
	var announcedLost atomic.Int32
	liveAttached := make(chan struct{})
	var liveOnce sync.Once

	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	var m *Manager
	m = newTestManager(t, testRegistry(t, host), fr, Options{
		// Drop exactly the first reconnect's just-published channel, between the
		// publish and the announcement.
		afterPublish: func(_ string, ch *Channel) {
			if drop.CompareAndSwap(true, false) {
				ch.markLost()
			}
		},
		OnEvent: func(ev Event) {
			if ev.Kind != EventAttached || !armed.Load() {
				return
			}
			if ch := m.currentChannel(ev.Host); ch != nil && ch.isLost() {
				announcedLost.Add(1)
				return
			}
			liveOnce.Do(func() { close(liveAttached) })
		},
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	armed.Store(true)
	drop.Store(true)
	ch.markLost()

	select {
	case <-liveAttached:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never announced a usable replacement channel")
	}
	if got := announcedLost.Load(); got != 0 {
		t.Fatalf("%d already-lost channels were announced as attached", got)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A frame the codec rejects is not a read error, so the byte-level monitor never
// sees it: without the transport wrapper the manager would keep the channel and
// hand it out with its reader gone.
func TestCodecErrorMarksTheLinkLost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	bridge := newFakeBridge(appwire.ProtocolVersion)
	fr := &fakeRunner{
		runFn:   cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) { return bridge.stdio, nil },
	}
	events := make(chan Event, 128)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
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

	// Garbage on the stream is a frame the codec cannot decode, delivered after a
	// successful read.
	if _, err := bridge.stdio.outW.Write([]byte("not-a-frame\n")); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	waitForEvent(t, events, EventDetached)
	if !ch.isLost() {
		t.Fatal("a codec-level receive error did not mark the link lost")
	}
}

// A terminal replacement failure must release the host: otherwise the dropped
// channel's supervisor still treats it as owned and repeats a failure that cannot
// succeed.
func TestEnsureTerminalFailureReleasesHostOwnership(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	mismatch := false
	starts := 0
	failures := 0
	var kinds []EventKind
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			refuse := mismatch
			mu.Unlock()
			if refuse && strings.Contains(strings.Join(argv, " "), "launch-check") {
				return []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`), nil
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
	supGate := newGateHook()
	exited := newExitSignal(1)
	m := newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) {
			events <- ev
			mu.Lock()
			kinds = append(kinds, ev.Kind)
			mu.Unlock()
			if ev.Kind == EventFailed {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		},
		BackoffBase:         time.Millisecond,
		BackoffMax:          time.Millisecond,
		sleep:               func(context.Context, time.Duration) error { return nil },
		jitter:              func(d time.Duration) time.Duration { return d },
		beforeSuperviseGate: func(string, *Channel) { supGate.hook() },
		superviseExited:     exited.hook,
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Park the dropped channel's supervisor before the host gate, so the terminal
	// failure is this Ensure call's exactly rather than by a margin.
	supGate.arm()
	defer supGate.open()
	mu.Lock()
	mismatch = true
	mu.Unlock()
	ch1.markLost()
	supGate.wait(t, "the dropped channel's supervisor")
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("Ensure = %v, want the terminal ErrProtocolIncompatible", err)
	}

	// The failure has to be announced by the call that found it: a consumer that
	// only learns about it from the supervisor has already been told the host is
	// merely reconnecting.
	mu.Lock()
	atReturn := failures
	seq := append([]EventKind(nil), kinds...)
	mu.Unlock()
	if atReturn < 1 {
		t.Fatalf("terminal failures announced by the time Ensure returned = %d, want at least 1", atReturn)
	}
	// The retired channel was announced as attached, so its consumer must see the
	// matching Detached before the terminal Failed, not after and not never.
	detachAt, failAt := -1, -1
	for i, k := range seq {
		if k == EventDetached && detachAt < 0 {
			detachAt = i
		}
		if k == EventFailed && failAt < 0 {
			failAt = i
		}
	}
	if detachAt < 0 || detachAt > failAt {
		t.Fatalf("terminal failure did not detach the retired channel first: %v", seq)
	}

	// The terminal failure releases the host too: whichever goroutine won the lock,
	// the host must not be left holding a dropped channel and the terminal failure
	// must not be retried. Releasing the parked supervisor makes its stand-down an
	// observed exit, so the count below is ordered without a sleep.
	supGate.open()
	exited.wait(t)
	mu.Lock()
	got := starts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("Start calls = %d, want 1: a terminal failure must not be retried", got)
	}
	if ch := m.currentChannel("alpha"); ch != nil {
		t.Fatalf("a terminal failure left the dropped channel mapped: %v", ch)
	}
	waitClosed(t, ch1)
}

// A caller may bring a long deadline; it must not hold the host's lock for that
// long, because the host's supervisor needs the lock to recover.
func TestAttemptIsCappedEvenWithCallerDeadline(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: func(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{attemptTimeout: 20 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(ctx, "alpha")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ensure succeeded against a hung host")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a long caller deadline held the attempt: the cap is not applied")
	}
}

// ErrManagerClosed means the manager is finished, so the reconnect loop must not
// sleep and announce one more round before giving up. ErrSSHAuth stays retryable:
// ssh forwards a remote command's stderr and exit status, so no failure the
// manager can observe proves an ssh-level refusal (see ErrSSHAuth).
func TestIsTerminalClassifiesManagerClosed(t *testing.T) {
	if !isTerminal(ErrManagerClosed) {
		t.Fatal("ErrManagerClosed is not terminal")
	}
	if isTerminal(ErrSSHStart) {
		t.Fatal("a transport failure must stay retryable")
	}
	if isTerminal(ErrSSHAuth) {
		t.Fatal("ErrSSHAuth must not be terminal: the refusal cannot be attributed to ssh")
	}
	if !isTerminal(errExecutableMissing) {
		t.Fatal("errExecutableMissing is not terminal: with no deploy configured nothing can ever install the missing binary, so retrying forever cannot succeed")
	}
}

// TestEnsureBoundsHubIsPresent proves the hub-presence probe is bounded like
// every other phase of ensureOnce. hubIsPresent issues real ssh commands
// (systemctl list-units, then lsof); before the fix it inherited the attempt
// context, which for a supervisor reconnect has no deadline, so a hung remote
// command held the per-host lock forever. The fake hangs the supervisor probe and
// the test asserts ensureOnce still returns.
func TestEnsureBoundsHubIsPresent(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	probing := make(chan struct{}, 4)
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
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
			// A mismatching on-disk build puts a deploy on the table, which is the
			// only path that runs the hub-presence probe.
			return []byte(`{"protocol":"evener-appwire-v5","version":"oldsha","launch_flags":["api-log"]}`), nil
		case strings.Contains(joined, "api/health"):
			return nil, errors.New("curl: (7) Failed to connect")
		case strings.Contains(joined, "list-units"):
			select {
			case probing <- struct{}{}:
			default:
			}
			// Block until the probe's own context expires: a real command stuck on
			// a dead dbus behaves this way.
			<-ctx.Done()
			return nil, ctx.Err()
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		controllerVersionOverride: "newsha",
		attemptTimeout:            100 * time.Millisecond,
		BuildBinary:               writeStageBinary,
	})

	errCh := make(chan error, 1)
	go func() {
		_, err := m.ensureOnce(context.Background(), host, false)
		errCh <- err
	}()
	select {
	case <-probing:
	case <-time.After(10 * time.Second):
		t.Fatal("ensureOnce never reached the hub-presence supervisor probe")
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("ensureOnce returned nil; the hung hub-presence probe must surface a failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensureOnce did not return: hubIsPresent ran on an unbounded context and held the host lock")
	}
}

// Manager.Close tears every channel down, and a consumer installed a source on
// each Attached it saw. Shutdown must therefore emit the matching Detached under
// the host lock, or that source outlives the channel it was built for.
func TestCloseDetachesAnnouncedChannels(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Close is synchronous: every event it emitted is buffered by the time it
	// returns, and a supervisor on a canceled manager emits nothing more.
	var kinds []EventKind
	for {
		select {
		case ev := <-events:
			if ev.Host != "alpha" {
				t.Fatalf("event for the wrong host: %+v", ev)
			}
			kinds = append(kinds, ev.Kind)
			continue
		default:
		}
		break
	}
	detaches := 0
	for _, k := range kinds {
		if k == EventDetached {
			detaches++
		}
	}
	if detaches != 1 {
		t.Fatalf("detach events on Close = %d, want exactly 1: %v", detaches, kinds)
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != EventDetached {
		t.Fatalf("the Close Detached is not the last event: %v", kinds)
	}
}

// A terminal failure announced by a concurrent Ensure must also end the
// supervisor that retired its own channel and is waiting out a backoff.
// Otherwise that supervisor repeats the terminal outcome and announces a second
// EventFailed for a host consumers were already told was finished.
func TestTerminalFailureStopsTheReconnectLoop(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	mismatch := false
	failures := 0
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			refuse := mismatch
			mu.Unlock()
			if refuse && strings.Contains(strings.Join(argv, " "), "launch-check") {
				return []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`), nil
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 256)
	// Park the supervisor inside its backoff sleep, so it has already retired the
	// dropped channel and released the host lock when Ensure runs.
	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	exited := newExitSignal(1)
	m := newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) {
			events <- ev
			if ev.Kind == EventFailed {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		},
		BackoffBase:     time.Millisecond,
		BackoffMax:      time.Millisecond,
		superviseExited: exited.hook,
		sleep: func(context.Context, time.Duration) error {
			enterOnce.Do(func() { close(sleepEntered) })
			<-release
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)

	defer releaseOnce.Do(func() { close(release) })

	mu.Lock()
	mismatch = true
	mu.Unlock()
	ch1.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never entered its backoff sleep")
	}

	// No channel is mapped now, so this Ensure is a fresh attempt: it finds the
	// terminal protocol mismatch and must stop the parked supervisor with it.
	if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrProtocolIncompatible) {
		t.Fatalf("Ensure = %v, want the terminal ErrProtocolIncompatible", err)
	}
	releaseOnce.Do(func() { close(release) })

	// The stopped supervisor's exit is the observation: waiting on it replaces the
	// sleep that used to hope it had stopped by now.
	exited.wait(t)
	mu.Lock()
	got := failures
	mu.Unlock()
	if got != 1 {
		t.Fatalf("EventFailed announcements = %d, want exactly 1: the terminal failure did not stop the supervisor", got)
	}
}

// Attaching a replacement overwrites the host's supervisor registration, so a
// terminal failure of the replacement must still stop the *older* supervisor
// parked in its backoff. Otherwise that loop wakes, re-attaches against a host
// consumers were already told was finished, and announces a second EventFailed
// (or an Attached after that Failed).
func TestReplacementAttachDoesNotLeakTheOlderSupervisor(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	var mu sync.Mutex
	mismatch := false
	failures := 0
	sleeps := 0
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			refuse := mismatch
			mu.Unlock()
			if refuse && strings.Contains(strings.Join(argv, " "), "launch-check") {
				return []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`), nil
			}
			return canned(ctx, argv, stdin)
		},
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	events := make(chan Event, 256)
	// Only the first backoff sleep blocks: that parks the original supervisor
	// while a replacement attaches. Every later loop — the replacement's and the
	// older one once released — runs straight through.
	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	// Both loops end: the replacement's at its terminal failure, the parked older
	// one when the release lets it observe the cancellation. Waiting for both makes
	// "the older supervisor stood down" an observation rather than a sleep.
	exited := newExitSignal(2)
	m := newTestManager(t, reg, fr, Options{
		OnEvent: func(ev Event) {
			events <- ev
			if ev.Kind == EventFailed {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		},
		BackoffBase:     time.Millisecond,
		BackoffMax:      time.Millisecond,
		superviseExited: exited.hook,
		sleep: func(context.Context, time.Duration) error {
			mu.Lock()
			sleeps++
			n := sleeps
			mu.Unlock()
			if n == 1 {
				enterOnce.Do(func() { close(sleepEntered) })
				<-release
			}
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	defer releaseOnce.Do(func() { close(release) })

	ch1, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)

	// Drop the first channel: its supervisor clears the slot, releases the host
	// lock, and parks in the blocking backoff sleep.
	ch1.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the original supervisor never entered its backoff sleep")
	}

	// Attach a replacement while the older supervisor is parked. This replaces the
	// host's supervisor registration.
	ch2, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("replacement Ensure: %v", err)
	}
	if ch2 == ch1 {
		t.Fatal("Ensure handed back the dropped channel")
	}
	waitForEvent(t, events, EventAttached)

	// The replacement now fails terminally: its own supervisor retires the
	// dropped replacement and finds the protocol mismatch on the next attempt.
	mu.Lock()
	mismatch = true
	mu.Unlock()
	ch2.markLost()
	waitUntil(t, "the replacement's terminal failure", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failures == 1
	})

	// Let the parked older supervisor wake. Cancelled by the replacement's
	// terminal failure, it stands down; with a leaked cancellation it re-attaches
	// and announces a second EventFailed.
	releaseOnce.Do(func() { close(release) })
	exited.wait(t)
	mu.Lock()
	got := failures
	mu.Unlock()
	if got != 1 {
		t.Fatalf("EventFailed announcements = %d, want exactly 1: a replaced supervisor outlived its replacement", got)
	}
}

// A supervisor stopped by stopSupervisor is superseded: the terminal Ensure that
// stopped it already announced the host's Disconnected, and a replacement Ensure
// may attach before the stopped loop reaches the host lock. The stopped loop must
// then stay silent rather than emit its own Disconnected after the replacement's
// Attached, which would leave the host's lifecycle state inconsistent.
//
// The test constructs exactly that interleaving: it parks the dropped channel's
// supervisor inside its backoff sleep, drives a terminal Ensure (which cancels the
// loop through stopSupervisor), attaches a replacement, and only then releases the
// stale loop so it contends for the host lock after the replacement owns the host.
func TestSupersededSupervisorDoesNotEmitDisconnectedAfterReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		// honorCancel decides which of the two cancellation sites the parked
		// supervisor reports through: waitSleep returning an error (the primary
		// path) or a success return followed by reconnectOnce observing the
		// canceled context (the second site).
		honorCancel bool
	}{
		{name: "waitSleepObservesCancellation", honorCancel: true},
		{name: "reconnectOnceObservesCancellation", honorCancel: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
			reg := testRegistry(t, host)

			var mu sync.Mutex
			mismatch := false
			canned := cannedRun(nil)
			fr := &fakeRunner{
				runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
					mu.Lock()
					refuse := mismatch
					mu.Unlock()
					if refuse && strings.Contains(strings.Join(argv, " "), "launch-check") {
						return []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`), nil
					}
					return canned(ctx, argv, stdin)
				},
				startFn: goodStartFn(t),
			}

			var evMu sync.Mutex
			var events []Event
			sleepEntered := make(chan struct{})
			release := make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			exited := newExitSignal(1)
			m := newTestManager(t, reg, fr, Options{
				OnEvent: func(ev Event) {
					evMu.Lock()
					events = append(events, ev)
					evMu.Unlock()
				},
				BackoffBase:     time.Millisecond,
				BackoffMax:      time.Millisecond,
				superviseExited: exited.hook,
				sleep: func(ctx context.Context, _ time.Duration) error {
					enterOnce.Do(func() { close(sleepEntered) })
					// Park until the interleaving is established; only the dropped
					// channel's supervisor ever sleeps here.
					<-release
					if tc.honorCancel {
						return ctx.Err()
					}
					return nil
				},
				jitter: func(d time.Duration) time.Duration { return d },
			})
			defer releaseOnce.Do(func() { close(release) })

			ch1, err := m.Ensure(context.Background(), "alpha")
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}

			// Drop the first link so its supervisor retires the channel and parks in
			// the backoff sleep.
			ch1.markLost()
			select {
			case <-sleepEntered:
			case <-time.After(5 * time.Second):
				t.Fatal("the dropped channel's supervisor never entered its backoff sleep")
			}

			// Terminal Ensure: no channel is mapped, so it fails terminally and
			// cancels the parked supervisor through stopSupervisor.
			mu.Lock()
			mismatch = true
			mu.Unlock()
			if _, err := m.Ensure(context.Background(), "alpha"); !errors.Is(err, ErrProtocolIncompatible) {
				t.Fatalf("terminal Ensure = %v, want ErrProtocolIncompatible", err)
			}

			// Replacement Ensure: mismatches are off, so it attaches a fresh channel
			// and announces Attached while the stale supervisor is still parked.
			mu.Lock()
			mismatch = false
			mu.Unlock()
			ch2, err := m.Ensure(context.Background(), "alpha")
			if err != nil {
				t.Fatalf("replacement Ensure: %v", err)
			}
			if ch2 == ch1 {
				t.Fatal("replacement Ensure handed back the dropped channel")
			}

			// Everything up to here is the established interleaving; the stale
			// loop's events must all land after this boundary.
			evMu.Lock()
			boundary := len(events)
			evMu.Unlock()

			// Release the parked stale supervisor and wait for its own exit, so the
			// assertion is ordered against the loop standing down.
			releaseOnce.Do(func() { close(release) })
			exited.wait(t)

			evMu.Lock()
			after := append([]Event(nil), events[boundary:]...)
			evMu.Unlock()
			for _, ev := range after {
				if ev.Kind == EventState && ev.State == StateDisconnected {
					t.Fatalf("a superseded supervisor announced Disconnected after the replacement attached: %+v", ev)
				}
			}
		})
	}
}

// A supervisor parked in its backoff is canceled by Close rather than by a
// terminal outcome, and it no longer owns the host either way. It must therefore
// stand down without announcing a Disconnected: Close's own Detached is the
// terminal event, and a trailing state event after it violates the "Close is
// last" pairing that TestEnsureRetiresReplacedChannelWhenCloseRaces relies on.
// The sleeping supervisor deliberately honours cancellation only once released,
// so Close is guaranteed to observe it still parked rather than by a margin.
func TestCloseCanceledBackoffSupervisorEmitsNoDisconnected(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}

	var evMu sync.Mutex
	var events []Event
	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) {
			evMu.Lock()
			events = append(events, ev)
			evMu.Unlock()
		},
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep: func(ctx context.Context, _ time.Duration) error {
			enterOnce.Do(func() { close(sleepEntered) })
			<-release
			return ctx.Err()
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	defer releaseOnce.Do(func() { close(release) })

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ch.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never entered its backoff sleep")
	}

	// Close cancels the base context and then waits for the parked supervisor, so
	// it cannot return until the release lets the loop stand down.
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned while the supervisor was still parked: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the supervisor stood down")
	}

	evMu.Lock()
	got := append([]Event(nil), events...)
	evMu.Unlock()
	for _, ev := range got {
		if ev.Kind == EventState && ev.State == StateDisconnected {
			t.Fatalf("a Close-canceled supervisor announced Disconnected: %+v", ev)
		}
	}
}

// ssh forwards the remote command's stderr onto the same stream as its own
// diagnostics, so a remote program's error text must not be read as ssh
// refusing the key: only ssh's own exit status makes an auth refusal terminal.
func TestRemoteCommandStderrPermissionDeniedIsNotAuthFailure(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	remoteExit := exitStatus(t, 1)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if strings.Contains(strings.Join(argv, " "), "launch-check") {
				return nil, &RunError{
					Stderr: []byte("evener: Permission denied (publickey).\n"),
					Err:    remoteExit,
				}
			}
			return cannedRun(nil)(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	_, err := m.Ensure(context.Background(), "alpha")
	if errors.Is(err, ErrSSHAuth) {
		t.Fatalf("a remote program's stderr was read as an ssh auth refusal: %v", err)
	}
	if !errors.Is(err, ErrSSHStart) {
		t.Fatalf("err = %v, want the retryable ErrSSHStart class", err)
	}
}

// A hung Initialize handshake must end at initTimeout, not run to the attempt
// limit: Ensure and reconnectOnce always hand attach a context that already
// carries the attempt deadline, so a deadline check there never applies it.
func TestInitializeHandshakeIsBoundedByInitTimeout(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			return newSilentBridge().stdio, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		initializeTimeout: 50 * time.Millisecond,
		attemptTimeout:    10 * time.Second,
	})
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Ensure succeeded against a silent Initialize handshake")
		}
		if !errors.Is(err, ErrSSHStart) {
			t.Fatalf("err = %v, want ErrSSHStart", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("handshake took %v; initTimeout was not applied", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ensure never returned: the Initialize handshake is not bounded by initTimeout")
	}
}

// A mapped channel is owned even when its link has dropped: its own supervisor is
// reconnecting, so a second one must stand down rather than attach alongside it
// and clobber the entry without ever detaching it.
func TestReconnectStandsDownForAMappedDroppedChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	ch := &Channel{lost: make(chan struct{}), done: make(chan struct{})}
	ch.markLost()
	if !m.publishChannel("alpha", ch, host) {
		t.Fatal("publishChannel refused a live manager")
	}
	hostGate := m.hostLock("alpha")
	defer m.releaseHostLock("alpha")
	if got := m.reconnectOnce(context.Background(), host, hostGate); got {
		t.Fatal("reconnectOnce reported work to do for a host another channel owns")
	}
	if starts := len(fr.recordedStarts()); starts != 0 {
		t.Fatalf("Start calls = %d, want 0: the mapped channel still owns the host", starts)
	}
	if cur := m.currentChannel("alpha"); cur != ch {
		t.Fatalf("currentChannel = %v, want the mapped channel left alone", cur)
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

// A configured BackoffMax bounds every reconnect delay, the first one included:
// a maximum below BackoffBase must cap the initial retry, not only the later
// ones nextBackoff has already doubled into it.
func TestInitialReconnectDelayIsClampedToBackoffMax(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	reg := testRegistry(t, host)

	const (
		baseDelay = 100 * time.Millisecond
		maxDelay  = 10 * time.Millisecond
	)
	var mu sync.Mutex
	var bridges []*fakeBridge
	var sleeps []time.Duration
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, _ io.Writer) (Stdio, error) {
			mu.Lock()
			defer mu.Unlock()
			b := newFakeBridge(appwire.ProtocolVersion)
			bridges = append(bridges, b)
			return b.stdio, nil
		},
	}
	events := make(chan Event, 128)
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: baseDelay,
		BackoffMax:  maxDelay,
		sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
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

	// Drop the link so the supervisor takes its first backoff sleep, the delay the
	// clamp governs.
	first.stdio.drop()
	waitForEvent(t, events, EventDetached)
	waitForEvent(t, events, EventAttached)

	mu.Lock()
	gotSleeps := append([]time.Duration(nil), sleeps...)
	mu.Unlock()
	if len(gotSleeps) == 0 {
		t.Fatal("the supervisor never backed off")
	}
	if gotSleeps[0] != maxDelay {
		t.Fatalf("first reconnect delay = %v, want %v: BackoffMax must clamp the initial delay", gotSleeps[0], maxDelay)
	}
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

// An attach handshake whose ssh child exits 255 with an auth marker is ambiguous
// for the same reason as the one-shot path: ssh forwards a remote command's own
// 255 unchanged, so the failure stays retryable and the loop takes another turn
// instead of standing down for good.
func TestReconnectAttachAuthMarkerIsRetryable(t *testing.T) {
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
			// Reconnect: ssh exits before the handshake with a BatchMode auth marker
			// on stderr and a 255 status the remote command could also have produced.
			_, _ = stderr.Write([]byte("bob@alpha.example: Permission denied (publickey).\n"))
			b := newFakeBridge(appwire.ProtocolVersion)
			b.stdio.waitErr = exitStatus(t, 255)
			b.stdio.drop()
			return b.stdio, nil
		},
	}
	events := make(chan Event, 128)
	var sleeps atomic.Int32
	retried := make(chan struct{})
	var retriedOnce sync.Once
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep: func(ctx context.Context, _ time.Duration) error {
			if sleeps.Add(1) >= 2 {
				retriedOnce.Do(func() { close(retried) })
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Force the channel's link down.
	ch.markLost()

	select {
	case <-retried:
	case <-time.After(5 * time.Second):
		t.Fatal("the ambiguous auth marker stopped the reconnect loop instead of retrying")
	}
	assertNoFailedEvent(t, events)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// A failed Start is the one case the classifier attributes to ssh itself (no
// remote command ran, so the marker is ssh's), but it must not be terminal
// either: a real spawn failure carries no diagnostic, so an authentication
// refusal ordinarily reaches the manager as the ambiguous ErrSSHStart. Ending
// the loop on ErrSSHAuth would still stop supervision for an unauthenticable
// host only by accident, so the contract is that it retries under backoff.
func TestReconnectFailedStartAuthMarkerIsRetryable(t *testing.T) {
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
			// Reconnect: ssh never spawns, and the sink carries a BatchMode marker.
			_, _ = stderr.Write([]byte("bob@alpha.example: Permission denied (publickey).\n"))
			return nil, errors.New("fork/exec ssh: no such file or directory")
		},
	}
	events := make(chan Event, 128)
	var sleeps atomic.Int32
	retried := make(chan struct{})
	var retriedOnce sync.Once
	m := newTestManager(t, reg, fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		// The loop sleeps before each attempt, so the second sleep proves the first
		// attempt failed and another turn was taken; park it until Close cancels.
		sleep: func(ctx context.Context, _ time.Duration) error {
			if sleeps.Add(1) >= 2 {
				retriedOnce.Do(func() { close(retried) })
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ch.markLost()

	select {
	case <-retried:
	case <-time.After(5 * time.Second):
		t.Fatal("an ErrSSHAuth-classified failure stopped the reconnect loop instead of retrying")
	}
	assertNoFailedEvent(t, events)
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// stdioReadWriter's pipes are closed from two directions — Channel.Close and the
// child exiting on its own — so an already-closed pipe must not surface as a
// Manager.Close error. os.File reports a repeat close as os.ErrClosed; a pipe
// reports io.ErrClosedPipe. A real teardown failure must still surface.
func TestStdioReadWriterCloseIgnoresAlreadyClosedPipes(t *testing.T) {
	for _, err := range []error{os.ErrClosed, io.ErrClosedPipe} {
		s := &stdioReadWriter{in: closedPipeRW{err}, out: closedPipeRW{err}}
		if got := s.Close(); got != nil {
			t.Fatalf("Close with an already-closed pipe (%v) = %v, want nil", err, got)
		}
	}
	boom := errors.New("boom")
	s := &stdioReadWriter{in: closedPipeRW{boom}, out: closedPipeRW{boom}}
	if got := s.Close(); !errors.Is(got, boom) {
		t.Fatalf("Close with a real teardown failure = %v, want boom", got)
	}
}

type closedPipeRW struct{ err error }

func (c closedPipeRW) Read([]byte) (int, error)  { return 0, c.err }
func (c closedPipeRW) Write([]byte) (int, error) { return 0, c.err }
func (c closedPipeRW) Close() error              { return c.err }

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

// waitUntil polls cond until it holds, failing the test at the deadline.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
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

// The attach handshake gets ssh's diagnostics and the remote command's stderr on
// the same sink, so the text alone cannot separate them, and ssh forwards the
// remote command's exit status unchanged — including 255. A started bridge is
// therefore ambiguous and stays retryable, matching the one-shot preflight rule
// (isSSHAuthFailure); only a failure to spawn ssh can be attributed to ssh at
// all, because no remote command could then have produced the marker — and even
// that class is not terminal (see ErrSSHAuth).
func TestAttachAuthClassificationRequiresSSHExitStatus(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	const marker = "bob@alpha.example: Permission denied (publickey).\n"
	cases := []struct {
		name     string
		waitErr  error
		startErr error
		wantErr  error
	}{
		{"a started ssh exiting 255 with the marker stays retryable", exitStatus(t, 255), nil, ErrSSHStart},
		{"a remote program's exit with the marker stays retryable", exitStatus(t, 1), nil, ErrSSHStart},
		{
			"an ssh that never started ran no remote command, so the marker is ssh's",
			nil, errors.New("fork/exec ssh: no such file or directory"), ErrSSHAuth,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{
				runFn: cannedRun(nil),
				startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
					_, _ = stderr.Write([]byte(marker))
					if tc.startErr != nil {
						// ssh itself never spawned: no remote command ran.
						return nil, tc.startErr
					}
					// ssh spawns, forwards the marker, then exits before the handshake
					// can complete, so the bridge's stdout closes under Initialize.
					b := newFakeBridge(appwire.ProtocolVersion)
					b.stdio.waitErr = tc.waitErr
					b.stdio.drop()
					return b.stdio, nil
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

// Manager.Close owns every supervisor it started: it must not return while one
// is still running, or a consumer that treats Close as terminal could observe a
// lifecycle event (here, the supervisor's Disconnected) after Close returned.
func TestCloseWaitsForSupervisors(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}

	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		// Deliberately ignores ctx: models a supervisor still running when Close is
		// called, so Close has to wait for it rather than return ahead of it.
		sleep: func(context.Context, time.Duration) error {
			enterOnce.Do(func() { close(sleepEntered) })
			<-release
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer releaseOnce.Do(func() { close(release) })

	ch.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never entered its backoff sleep")
	}

	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while a supervisor was still running")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close never returned after the supervisor finished")
	}
}

// A second concurrent Close must not return while the first is still tearing
// channels down: a consumer that treats its own Close return as "no further
// lifecycle events" would otherwise observe the first teardown's events.
func TestSecondCloseWaitsForTheFirst(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}

	sleepEntered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	exited := newExitSignal(1)
	var events atomic.Int32
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:         func(Event) { events.Add(1) },
		BackoffBase:     time.Millisecond,
		BackoffMax:      time.Millisecond,
		superviseExited: exited.hook,
		// Deliberately ignores ctx: the supervisor is still running when the first
		// Close is called, so Close has to wait for it rather than return ahead.
		sleep: func(context.Context, time.Duration) error {
			enterOnce.Do(func() { close(sleepEntered) })
			<-release
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	defer releaseOnce.Do(func() { close(release) })

	ch.markLost()
	select {
	case <-sleepEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never entered its backoff sleep")
	}

	first := make(chan error, 1)
	go func() { first <- m.Close() }()
	// Once closed is set the first caller is committed to its waits, so a second
	// caller necessarily takes the concurrent branch.
	waitUntil(t, "the first Close to commit", m.isClosed)

	second := make(chan error, 1)
	go func() { second <- m.Close() }()
	select {
	case <-second:
		t.Fatal("a second Close returned while the first was still tearing down")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })
	for i, waits := range []chan error{first, second} {
		select {
		case err := <-waits:
			if err != nil {
				t.Fatalf("Close #%d: %v", i+1, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Close #%d never returned", i+1)
		}
	}

	// Every caller can treat its return as quiescent: no event may follow. The
	// supervisor's exit precedes Close's return (Close joins it); waiting on that
	// exit bounds the window with a real event instead of a sleep, and a Close
	// that returned ahead of its supervisor would let the late emission show up in
	// this count.
	before := events.Load()
	exited.wait(t)
	if after := events.Load(); after != before {
		t.Fatalf("lifecycle events (%d -> %d) were emitted after Close returned", before, after)
	}
}

// Close canceling an in-flight handshake is a closed manager, not a host
// failure: consumers shutting down must not receive an EventFailed for it, the
// same way the early isClosed return of Ensure emits nothing.
func TestCloseDuringHandshakeEmitsNoFailedEvent(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	entered := make(chan struct{})
	var once sync.Once
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			once.Do(func() { close(entered) })
			return newSilentBridge().stdio, nil
		},
	}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:           func(ev Event) { events <- ev },
		initializeTimeout: 30 * time.Second,
		attemptTimeout:    30 * time.Second,
	})
	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(context.Background(), "alpha")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the attach never reached the runner")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("Ensure = %v, want ErrManagerClosed", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not end the in-flight handshake")
	}
	assertNoFailedEvent(t, events)
}

// Same for the supervisor's own reconnect attempt: Close canceling its handshake
// is not a terminal host failure, so shutdown must not announce an EventFailed.
func TestCloseDuringReconnectHandshakeEmitsNoFailedEvent(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}

	var mu sync.Mutex
	startCalls := 0
	reconnectEntered := make(chan struct{})
	var enteredOnce sync.Once
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, _ io.Writer) (Stdio, error) {
			mu.Lock()
			startCalls++
			call := startCalls
			mu.Unlock()
			if call == 1 {
				return newFakeBridge(appwire.ProtocolVersion).stdio, nil
			}
			enteredOnce.Do(func() { close(reconnectEntered) })
			return newSilentBridge().stdio, nil
		},
	}
	events := make(chan Event, 128)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:           func(ev Event) { events <- ev },
		BackoffBase:       time.Millisecond,
		BackoffMax:        time.Millisecond,
		sleep:             func(context.Context, time.Duration) error { return nil },
		jitter:            func(d time.Duration) time.Duration { return d },
		initializeTimeout: 30 * time.Second,
		attemptTimeout:    30 * time.Second,
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ch.markLost()
	select {
	case <-reconnectEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never started a reconnect attach")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The supervisor announces its terminal Disconnected after the failed attach,
	// so read up to that event: whether or not Close waited for it, a spurious
	// EventFailed would necessarily precede it and be caught here.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventFailed {
				t.Fatalf("shutdown surfaced a spurious EventFailed: %+v", ev)
			}
			if ev.Kind == EventState && ev.State == StateDisconnected {
				deadline = nil
			}
		case <-deadline:
			t.Fatal("the supervisor never announced its terminal state")
		}
		if deadline == nil {
			break
		}
	}
	if cur := m.currentChannel("alpha"); cur != nil {
		t.Fatalf("closed manager kept a channel: %v", cur)
	}
}

// Close canceling a reconnect preflight that was in flight must not be reported
// as a retryable transport failure: that emitted a spurious StateReconnecting
// and sent the supervisor around another loop iteration during shutdown. The
// canceled attempt maps to ErrManagerClosed, so the link-drop's own
// StateReconnecting is the only one and the loop ends.
func TestCloseDuringReconnectPreflightEmitsNoExtraReconnecting(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}

	var mu sync.Mutex
	reconnectPreflight := false
	entered := make(chan struct{})
	var enteredOnce sync.Once
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			blocking := reconnectPreflight
			mu.Unlock()
			if blocking {
				enteredOnce.Do(func() { close(entered) })
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 128)
	var reconnectings atomic.Int32
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) {
			if ev.Kind == EventState && ev.State == StateReconnecting {
				reconnectings.Add(1)
			}
			events <- ev
		},
		BackoffBase:    time.Millisecond,
		BackoffMax:     time.Millisecond,
		sleep:          func(context.Context, time.Duration) error { return nil },
		jitter:         func(d time.Duration) time.Duration { return d },
		attemptTimeout: 30 * time.Second,
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mu.Lock()
	reconnectPreflight = true
	mu.Unlock()
	ch.markLost()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never reached a reconnect preflight")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close waited for the supervisor, so every event it produced is in hand.
	// Exactly one StateReconnecting is correct: the link-drop transition in
	// supervise. The canceled attempt must not add a second.
	if got := reconnectings.Load(); got != 1 {
		t.Fatalf("StateReconnecting events = %d, want 1 (the canceled attempt must not retry)", got)
	}
	assertNoFailedEvent(t, events)
	if cur := m.currentChannel("alpha"); cur != nil {
		t.Fatalf("closed manager kept a channel: %v", cur)
	}
}

// A terminal error found while Close is racing the supervisor's reconnect must
// not surface as an EventFailed: shutdown reports no host failure, even when the
// attempt also happened to find a terminal fault (here a protocol mismatch). The
// mapping to ErrManagerClosed covers only the retryable transport shape, so the
// manager's own teardown must suppress the terminal announcement directly.
func TestCloseRaceWithTerminalReconnectEmitsNoFailedEvent(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}

	var mu sync.Mutex
	block := false
	preflightEntered := make(chan struct{})
	releasePreflight := make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			mu.Lock()
			blocking := block
			mu.Unlock()
			if blocking && strings.Contains(strings.Join(argv, " "), "launch-check") {
				enteredOnce.Do(func() { close(preflightEntered) })
				<-releasePreflight
				return []byte(`{"protocol":"evener-appwire-v4","version":"dev","launch_flags":["api-log"]}`), nil
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 128)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent:     func(ev Event) { events <- ev },
		BackoffBase: time.Millisecond,
		BackoffMax:  time.Millisecond,
		sleep:       func(context.Context, time.Duration) error { return nil },
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	defer releaseOnce.Do(func() { close(releasePreflight) })

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	mu.Lock()
	block = true
	mu.Unlock()
	ch.markLost()
	select {
	case <-preflightEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor never reached a reconnect preflight")
	}

	// Begin shutdown while that preflight is still in flight: Close cancels the
	// base context and then waits for the supervisor.
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case <-m.baseCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close never canceled the base context")
	}

	// Release the preflight: it answers with a terminal protocol mismatch even
	// though the manager is closing. Shutdown must not read that as a host
	// failure.
	releaseOnce.Do(func() { close(releasePreflight) })
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not finish after the preflight returned")
	}
	assertNoFailedEvent(t, events)
}

func assertNoFailedEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventFailed {
				t.Fatalf("shutdown surfaced a spurious EventFailed: %+v", ev)
			}
			continue
		default:
		}
		return
	}
}

// ClientIfAttached is the non-dialing lookup components 05/06 use: it must answer
// from the installed channel alone — never preflighting or attaching a dormant
// host — and must stop answering the moment that channel drops or the manager
// closes. It is the client half of the Attached signal, so it reads the same
// installed-channel state.
func TestClientIfAttachedReturnsOnlyALiveChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	// No reconnect may replace the dropped channel while this test watches, so the
	// supervisor sleeps far longer than the test runs.
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Hour,
		BackoffMax:  time.Hour,
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("ClientIfAttached(unattached) = %v, %v; want nil, false", client, ok)
	}
	if client, ok := m.ClientIfAttached("ghost"); ok || client != nil {
		t.Fatalf("ClientIfAttached(unknown host) = %v, %v; want nil, false", client, ok)
	}
	if runs, starts := len(fr.recordedRuns()), len(fr.recordedStarts()); runs != 0 || starts != 0 {
		t.Fatalf("ClientIfAttached attached a dormant host: %d runs, %d starts", runs, starts)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	client, ok := m.ClientIfAttached("alpha")
	if !ok || client == nil {
		t.Fatalf("ClientIfAttached(attached) = %v, %v; want the live client, true", client, ok)
	}
	if client != ch.Client() {
		t.Fatal("ClientIfAttached returned a client other than the installed channel's")
	}
	// The registry is the authority on a name's spelling here too: a padded name
	// must resolve to the same host Ensure attached (TestEnsureNormalizesHostName).
	if client, ok := m.ClientIfAttached("  alpha  "); !ok || client != ch.Client() {
		t.Fatalf("ClientIfAttached did not normalize the name: %v, %v", client, ok)
	}

	ch.markLost()
	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("ClientIfAttached(a dropped channel) = %v, %v; want nil, false", client, ok)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("ClientIfAttached(a closed manager) = %v, %v; want nil, false", client, ok)
	}
}

// PreflightIfAttached is a test-only/convenience attached-only accessor: it
// answers from the installed channel's captured preflight alone, never
// preflighting or attaching a dormant host, and stops answering the moment
// that channel drops or the manager closes.
func TestPreflightIfAttachedReturnsOnlyALiveChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Hour,
		BackoffMax:  time.Hour,
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	if pf, ok := m.PreflightIfAttached("alpha"); ok {
		t.Fatalf("PreflightIfAttached(unattached) = %+v, true; want zero, false", pf)
	}
	if pf, ok := m.PreflightIfAttached("ghost"); ok {
		t.Fatalf("PreflightIfAttached(unknown host) = %+v, true; want zero, false", pf)
	}
	if runs, starts := len(fr.recordedRuns()), len(fr.recordedStarts()); runs != 0 || starts != 0 {
		t.Fatalf("PreflightIfAttached attached a dormant host: %d runs, %d starts", runs, starts)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	pf, ok := m.PreflightIfAttached("alpha")
	if !ok {
		t.Fatalf("PreflightIfAttached(attached) = %+v, false; want the live preflight, true", pf)
	}
	if !reflect.DeepEqual(pf, ch.Preflight()) {
		t.Fatalf("PreflightIfAttached = %+v; want the installed channel's %+v", pf, ch.Preflight())
	}
	if pf, ok := m.PreflightIfAttached("  alpha  "); !ok || !reflect.DeepEqual(pf, ch.Preflight()) {
		t.Fatalf("PreflightIfAttached did not normalize the name: %+v, %v", pf, ok)
	}

	ch.markLost()
	if pf, ok := m.PreflightIfAttached("alpha"); ok {
		t.Fatalf("PreflightIfAttached(a dropped channel) = %+v, true; want zero, false", pf)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if pf, ok := m.PreflightIfAttached("alpha"); ok {
		t.Fatalf("PreflightIfAttached(a closed manager) = %+v, true; want zero, false", pf)
	}
}

// HandshakeIfAttached is a test-only/convenience attached-only accessor: it
// offers the InitializeResponse captured at attach for a live channel and
// never dials.
func TestHandshakeIfAttachedReturnsOnlyALiveChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Hour,
		BackoffMax:  time.Hour,
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	if hs, ok := m.HandshakeIfAttached("alpha"); ok {
		t.Fatalf("HandshakeIfAttached(unattached) = %+v, true; want zero, false", hs)
	}
	if hs, ok := m.HandshakeIfAttached("ghost"); ok {
		t.Fatalf("HandshakeIfAttached(unknown host) = %+v, true; want zero, false", hs)
	}
	if runs, starts := len(fr.recordedRuns()), len(fr.recordedStarts()); runs != 0 || starts != 0 {
		t.Fatalf("HandshakeIfAttached attached a dormant host: %d runs, %d starts", runs, starts)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	hs, ok := m.HandshakeIfAttached("alpha")
	if !ok {
		t.Fatalf("HandshakeIfAttached(attached) = %+v, false; want the captured handshake, true", hs)
	}
	if hs.ProtocolVersion != appwire.ProtocolVersion {
		t.Fatalf("HandshakeIfAttached ProtocolVersion = %q, want %q", hs.ProtocolVersion, appwire.ProtocolVersion)
	}
	if !reflect.DeepEqual(hs, ch.Handshake()) {
		t.Fatalf("HandshakeIfAttached = %+v; want the installed channel's %+v", hs, ch.Handshake())
	}
	if hs, ok := m.HandshakeIfAttached("  alpha  "); !ok || !reflect.DeepEqual(hs, ch.Handshake()) {
		t.Fatalf("HandshakeIfAttached did not normalize the name: %+v, %v", hs, ok)
	}

	ch.markLost()
	if hs, ok := m.HandshakeIfAttached("alpha"); ok {
		t.Fatalf("HandshakeIfAttached(a dropped channel) = %+v, true; want zero, false", hs)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if hs, ok := m.HandshakeIfAttached("alpha"); ok {
		t.Fatalf("HandshakeIfAttached(a closed manager) = %+v, true; want zero, false", hs)
	}
}

// ChannelIfAttached is the atomic primitive the generation-guarded facts seams
// read: one lookup yields one installed channel, so a caller can take the
// client, preflight, and handshake from the same connection generation. It must
// answer from the installed channel alone and stop the moment that channel
// drops or the manager closes.
func TestChannelIfAttachedReturnsOnlyALiveChannel(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BackoffBase: time.Hour,
		BackoffMax:  time.Hour,
		jitter:      func(d time.Duration) time.Duration { return d },
	})

	if ch, ok := m.ChannelIfAttached("alpha"); ok || ch != nil {
		t.Fatalf("ChannelIfAttached(unattached) = %v, %v; want nil, false", ch, ok)
	}
	if ch, ok := m.ChannelIfAttached("ghost"); ok || ch != nil {
		t.Fatalf("ChannelIfAttached(unknown host) = %v, %v; want nil, false", ch, ok)
	}
	if runs, starts := len(fr.recordedRuns()), len(fr.recordedStarts()); runs != 0 || starts != 0 {
		t.Fatalf("ChannelIfAttached attached a dormant host: %d runs, %d starts", runs, starts)
	}

	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got, ok := m.ChannelIfAttached("alpha")
	if !ok || got != ch {
		t.Fatalf("ChannelIfAttached(attached) = %v, %v; want the installed channel", got, ok)
	}
	// The single value carries one generation: its client, preflight, and
	// handshake all belong to the channel Ensure just installed.
	if got.Client() != ch.Client() {
		t.Fatal("ChannelIfAttached returned a channel whose client is not the installed one")
	}
	if client, ok := m.ClientIfAttached("alpha"); !ok || client != got.Client() {
		t.Fatalf("ClientIfAttached disagrees with ChannelIfAttached: %v, %v", client, ok)
	}
	if pf, ok := m.PreflightIfAttached("alpha"); !ok || !reflect.DeepEqual(pf, got.Preflight()) {
		t.Fatalf("PreflightIfAttached disagrees with ChannelIfAttached: %+v, %v", pf, ok)
	}
	if hs, ok := m.HandshakeIfAttached("alpha"); !ok || !reflect.DeepEqual(hs, got.Handshake()) {
		t.Fatalf("HandshakeIfAttached disagrees with ChannelIfAttached: %+v, %v", hs, ok)
	}

	ch.markLost()
	if got, ok := m.ChannelIfAttached("alpha"); ok || got != nil {
		t.Fatalf("ChannelIfAttached(a dropped channel) = %v, %v; want nil, false", got, ok)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got, ok := m.ChannelIfAttached("alpha"); ok || got != nil {
		t.Fatalf("ChannelIfAttached(a closed manager) = %v, %v; want nil, false", got, ok)
	}
}

// exitGatedStdio is a bridge Stdio whose child exit the test releases directly,
// rather than through Kill: the stdio pipes stay open, so the manager's read
// loop keeps waiting and the child-wait goroutine's own liveness transition is
// the only one in play.
type exitGatedStdio struct {
	*fakeStdio
	exited   chan struct{}
	exitOnce sync.Once
}

func newExitGatedStdio(b *fakeBridge) *exitGatedStdio {
	return &exitGatedStdio{fakeStdio: b.stdio, exited: make(chan struct{})}
}

// exitChild releases the child, standing in for the ssh process ending. It is
// deliberately not Kill: nothing about the stdio link changes.
func (s *exitGatedStdio) exitChild() { s.exitOnce.Do(func() { close(s.exited) }) }

func (s *exitGatedStdio) Wait() error {
	<-s.exited
	return nil
}

// Round sixteen found the child-wait goroutine closing ch.stopped before calling
// ch.markLost(): a consumer that could tell the ssh child had exited (the
// stopped signal) could still be handed the dead channel by ClientIfAttached or
// Ensure. Liveness must be retired no later than the reaping is announced, so
// this parks the goroutine at the publication point and asserts the invariant
// deterministically instead of racing it.
func TestExitedChildIsNeverReportedLive(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	gated := newExitGatedStdio(newFakeBridge(appwire.ProtocolVersion))
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			return gated, nil
		},
	}
	exitGate := newGateHook()
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		afterChildExit: func(string, *Channel) { exitGate.hook() },
	})

	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if client, ok := m.ClientIfAttached("alpha"); !ok || client == nil {
		t.Fatal("ClientIfAttached did not offer the freshly attached channel")
	}

	exitGate.arm()
	gated.exitChild() // the ssh child exits; the stdio pipes stay open
	exitGate.wait(t, "the child-exit edge")
	defer exitGate.open()

	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("ClientIfAttached reported an exited child's channel as live: %v, %v", client, ok)
	}
}

// A caller that gives up while its attach is finishing must not be handed a
// channel. The channel is manager-owned and already supervised, so it is not torn
// down; the caller gets its own error instead.
func TestEnsureCanceledDuringAttachReturnsContextError(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		// EventAttached is delivered under the host lock, after the channel is
		// published and before Ensure's final validation: canceling here lands in
		// exactly that window.
		OnEvent: func(ev Event) {
			if ev.Kind == EventAttached {
				cancel()
			}
		},
	})

	ch, err := m.Ensure(ctx, "alpha")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure = %v, want context.Canceled", err)
	}
	if ch != nil {
		t.Fatalf("Ensure returned %+v to a caller that had given up", ch)
	}
	if client, ok := m.ClientIfAttached("alpha"); !ok || client == nil {
		t.Fatal("a canceled caller tore down the manager-owned channel")
	}
}

// A caller cancellation is not a host failure. It must come back as the caller's
// own context error — not the transport-shaped ErrSSHStart its cancellation
// produced — and must not announce a lifecycle transition the host never made.
func TestCallerCancellationReturnsItsOwnErrorWithoutStateEvent(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	started := make(chan struct{})
	var once sync.Once
	fr := &fakeRunner{
		runFn: func(ctx context.Context, _ []string, _ io.Reader) ([]byte, error) {
			once.Do(func() { close(started) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
		startFn: goodStartFn(t),
	}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) { events <- ev },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := m.Ensure(ctx, "alpha")
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("preflight never ran")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ensure = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrSSHStart) {
			t.Fatalf("Ensure = %v: a caller cancellation is not a transport failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return after its context was canceled")
	}
	drained := false
	for !drained {
		select {
		case ev := <-events:
			if ev.Kind == EventState && ev.State == StateDisconnected {
				t.Fatalf("a canceled caller announced %+v; the host never made that transition", ev)
			}
		default:
			drained = true
		}
	}
}

// toggleWrites forwards writes to a pipe until fail is set, then reports a broken
// pipe. It models an ssh child whose stdin pipe died while its stdout is still
// open: the send-side link-down edge, with no read-side EOF to observe.
type toggleWrites struct {
	fail *atomic.Bool
	w    io.WriteCloser
}

func (t *toggleWrites) Write(p []byte) (int, error) {
	if t.fail.Load() {
		return 0, io.ErrClosedPipe
	}
	return t.w.Write(p)
}

func (t *toggleWrites) Close() error { return t.w.Close() }

// toggledStdio is a fakeBridge's stdio with a switchable Stdin.
type toggledStdio struct {
	*fakeStdio
	stdin io.WriteCloser
}

func (s *toggledStdio) Stdin() io.WriteCloser { return s.stdin }

// A send whose write fails is as much a link-down edge as a read EOF. Without it
// isLost stays false, and the manager keeps offering a channel whose every write
// fails — including through the non-dialing lookup.
func TestFailedSendMarksTheLinkLost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var failSend atomic.Bool
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			b := newFakeBridge(appwire.ProtocolVersion)
			return &toggledStdio{
				fakeStdio: b.stdio,
				stdin:     &toggleWrites{fail: &failSend, w: b.stdio.inW},
			}, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if client, ok := m.ClientIfAttached("alpha"); !ok || client == nil {
		t.Fatal("the attached channel is not available through the lookup")
	}

	failSend.Store(true)
	if err := ch.Transport().Send(context.Background(), appwire.NotificationMessage("noop", nil)); err == nil {
		t.Fatal("a send over a broken stdin pipe reported success")
	}
	if !ch.isLost() {
		t.Fatal("a failed send left the channel marked live")
	}
	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("the manager kept offering a channel whose writes fail: %v, %v", client, ok)
	}
}

// Close can land between the two post-publish liveness checks, after the manager
// was last seen open. That outcome is the terminal ErrManagerClosed, not the
// retryable drop a caller would loop on, and Close — not the host — owns the
// events for the teardown so the helper emits none.
func TestLostAfterPublishClassifiesAManagerClosedByTheRace(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	m := newTestManager(t, testRegistry(t, host), &fakeRunner{}, Options{})
	dropped := &Channel{lost: make(chan struct{}), done: make(chan struct{})}
	dropped.markLost()
	if err := m.lostAfterPublish("alpha", dropped); !errors.Is(err, ErrSSHStart) {
		t.Fatalf("lostAfterPublish(open manager) = %v, want the retryable drop", err)
	}
	if got := m.currentChannel("alpha"); got != dropped {
		t.Fatal("a dropped predecessor lost its slot: its supervisor can no longer own the host")
	}

	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	// Put the flag back even if the assertion below fails: the cleanup Close would
	// otherwise wait forever for a teardown that never ran, turning a failed check
	// into a ten-minute hang.
	defer func() {
		m.mu.Lock()
		m.closed = false
		m.mu.Unlock()
	}()
	if err := m.lostAfterPublish("alpha", nil); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("lostAfterPublish(closed manager) = %v, want ErrManagerClosed", err)
	}
}

// Every attach builds its own diagSink, and os/exec copies each ssh child's
// stderr on its own goroutine. Those copies share the manager's one diagnostic
// writer, so that writer must serialize them: a caller-supplied sink is not
// required to be safe for concurrent use, and the tests pass bytes.Buffer.
func TestAttachDiagnosticsShareOneSerializedWriter(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var buf bytes.Buffer
	sinkCh := make(chan io.Writer, 1)
	fr := &fakeRunner{
		runFn: cannedRun(nil),
		startFn: func(_ context.Context, _ []string, stderr io.Writer) (Stdio, error) {
			sinkCh <- stderr
			return newFakeBridge(appwire.ProtocolVersion).stdio, nil
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{Stderr: &buf})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	sink := <-sinkCh
	if got := sink.(*diagSink).w; got != io.Writer(m.diagWriter) {
		t.Fatalf("attach wired its diagnostics to %T, want the manager's serialized writer", got)
	}

	const (
		writers = 8
		writes  = 32
		chunk   = 16
	)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			block := bytes.Repeat([]byte{byte('a' + i)}, chunk)
			for range writes {
				if _, err := m.diagWriter.Write(block); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	data := buf.Bytes()
	if want := writers * writes * chunk; len(data) != want {
		t.Fatalf("diagnostic sink holds %d bytes, want %d", len(data), want)
	}
	for off := 0; off < len(data); off += chunk {
		for _, b := range data[off : off+chunk] {
			if b != data[off] {
				t.Fatalf("chunk at byte %d interleaved: %q", off, data[off:off+chunk])
			}
		}
	}
}

// When Close wins the race after a replacement was published and then proved
// lost, lostAfterPublish runs with the map holding the replacement and Close's
// snapshot never seeing the predecessor. Close's own pairing therefore claims a
// channel that is not the predecessor, and the predecessor's supervisor stands
// down on the canceled base context without emitting — so the Detached that drops
// the source installed on the predecessor's Attached would leak for the life of
// the process unless lostAfterPublish emits it itself.
func TestLostAfterPublishDetachesThePredecessorOnClose(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	var mu sync.Mutex
	var kinds []EventKind
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) {
			mu.Lock()
			kinds = append(kinds, ev.Kind)
			mu.Unlock()
		},
	})
	// A real predecessor, announced through the real Ensure path.
	predecessor, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// Model the race exactly as it happens: Ensure's replacement has taken the map
	// slot, while announced still records the predecessor as the channel whose
	// Attached has no match.
	replacement := &Channel{lost: make(chan struct{}), done: make(chan struct{})}
	if !m.publishChannel("alpha", replacement, host) {
		t.Fatal("publishChannel refused while the manager was open")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close's snapshot held the replacement, so it paired nothing for the
	// predecessor.
	if err := m.lostAfterPublish("alpha", predecessor); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("lostAfterPublish after Close = %v, want ErrManagerClosed", err)
	}
	mu.Lock()
	got := append([]EventKind(nil), kinds...)
	mu.Unlock()
	detaches := 0
	for _, k := range got {
		if k == EventDetached {
			detaches++
		}
	}
	if detaches != 1 {
		t.Fatalf("lostAfterPublish after Close emitted %d Detached, want exactly 1: the predecessor's Attached leaked (%v)", detaches, got)
	}
	// The Detached is a claim: the predecessor must not stay announced, or a later
	// pairing emits a second one for the same Attached.
	m.mu.Lock()
	_, stillAnnounced := m.announced["alpha"]
	m.mu.Unlock()
	if stillAnnounced {
		t.Fatal("the predecessor stayed announced after its Detached")
	}
}

// ClientIfAttached is the rebind path a Detached callback uses. Close emits its
// Detached while the channel is still mapped and not yet closed, so a lookup that
// does not read m.closed under the same mutex acquisition would serve that
// callback a client for the channel Close is tearing down.
func TestClientIfAttachedRefusesDuringClose(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	var mu sync.Mutex
	var served []bool
	var m *Manager
	m = newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) {
			if ev.Kind != EventDetached {
				return
			}
			client, ok := m.ClientIfAttached(ev.Host)
			mu.Lock()
			served = append(served, ok || client != nil)
			mu.Unlock()
		},
	})
	if _, err := m.Ensure(context.Background(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if client, ok := m.ClientIfAttached("alpha"); !ok || client == nil {
		t.Fatal("precondition: the attached channel is not visible through the lookup")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	got := append([]bool(nil), served...)
	mu.Unlock()
	if len(got) == 0 {
		t.Fatal("Close emitted no Detached callback to observe")
	}
	for i, ok := range got {
		if ok {
			t.Fatalf("ClientIfAttached served a client during Close (Detached callback %d)", i)
		}
	}
}

// An AppWire notification overflow tears the connection down inside the client,
// not through a Recv error, so the transport wrapper must mark the channel lost
// on Close too: otherwise the manager keeps offering a channel whose reader is
// gone, and the supervisor that owns the host never reconnects.
func TestNotificationOverflowMarksTheLinkLost(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	bridge := newFakeBridge(appwire.ProtocolVersion)
	fr := &fakeRunner{
		runFn:   cannedRun(nil),
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) { return bridge.stdio, nil },
	}
	events := make(chan Event, 64)
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		OnEvent: func(ev Event) { events <- ev },
		// The supervisor emits its Reconnecting and Detached, then parks well past
		// the test; the reconnect itself is not what this pins.
		BackoffBase: time.Hour,
		BackoffMax:  time.Hour,
		jitter:      func(d time.Duration) time.Duration { return d },
	})
	ch, err := m.Ensure(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitForEvent(t, events, EventAttached)

	// One frame past the buffer cap with no consumer draining overflows it.
	_ = bridge.floodNotifications(appwire.NotificationBufferCap + 1)

	waitForEvent(t, events, EventDetached)
	if !ch.isLost() {
		t.Fatal("the overflow-triggered transport close did not mark the link lost")
	}
	if client, ok := m.ClientIfAttached("alpha"); ok || client != nil {
		t.Fatalf("the manager kept offering a channel whose reader is gone: %v, %v", client, ok)
	}
}

// ConnectTimeout is exported and can be set near the representable maximum. The
// attempt limit multiplies it by four and adds the initialize timeout, so a
// large value must saturate rather than overflow: a negative attempt limit
// becomes an already-expired context, so the attempt would fail instantly
// instead of bounding one reconnect attempt.
func TestAttemptLimitSaturatesOnHugeConnectTimeout(t *testing.T) {
	for _, ct := range []time.Duration{
		time.Duration(math.MaxInt64),
		time.Duration(math.MaxInt64) / 4,
		time.Duration(math.MaxInt64)/4 + 1,
		time.Duration(math.MaxInt64) / 8,
	} {
		got := Options{ConnectTimeout: ct}.attemptLimit()
		if got <= 0 {
			t.Fatalf("attemptLimit with ConnectTimeout=%v = %v, want a positive duration", ct, got)
		}
		if got < ct {
			t.Fatalf("attemptLimit with ConnectTimeout=%v = %v, want at least the connect timeout", ct, got)
		}
	}
}

// nextBackoff doubles the reconnect delay. A caller can seed it near the
// representable maximum through BackoffBase/BackoffMax, where the doubling
// overflows; the result must stay positive and bounded by limit, never wrap to
// a negative duration that fires the sleep timer immediately and spins the
// reconnect loop.
func TestNextBackoffSaturatesInsteadOfOverflowing(t *testing.T) {
	const maxDur = time.Duration(math.MaxInt64)
	for _, tc := range []struct {
		delay, limit time.Duration
	}{
		{maxDur, maxDur},
		{maxDur / 2, maxDur},
		{maxDur/2 + 1, maxDur},
		{maxDur/2 + 1, time.Second},
		{time.Second, maxDur},
	} {
		got := nextBackoff(tc.delay, tc.limit)
		if got <= 0 {
			t.Fatalf("nextBackoff(%v, %v) = %v, want a positive duration", tc.delay, tc.limit, got)
		}
		if tc.limit > 0 && got > tc.limit {
			t.Fatalf("nextBackoff(%v, %v) = %v, want at most the limit", tc.delay, tc.limit, got)
		}
	}
}
