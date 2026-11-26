package sshconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/testwait"
)

// These tests pin the startup reconnect entry point: StartReconnect runs the
// explicit-Connect attach ladder immediately, retries transient failures with
// bounded backoff, hands the channel to mid-run supervision once attached, and
// stops on terminal failures and teardowns without stranding or double-starting
// a loop. They never dial: the fake runner scripts the remote hub boundary.

const (
	startupTestGeneration  uint64 = 7
	startupTestIncarnation        = "startup-incarnation-7"
)

// startupTestHost is the registration every startup test attaches: an explicit
// (generation, incarnation_id) pair distinguishes the EventAttached identity
// from one the registry minted.
func startupTestHost() hostreg.Host {
	return hostreg.Host{
		Name:          "alpha",
		SSH:           "alpha.example",
		EvenerPath:    "/opt/evener/bin/evener",
		Generation:    startupTestGeneration,
		IncarnationID: startupTestIncarnation,
	}
}

// startupEventLog records lifecycle events without consuming them, so a test
// can wait for one event and later assert another never arrived.
type startupEventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *startupEventLog) record(ev Event) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *startupEventLog) snapshot() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.events...)
}

// waitFor blocks until an event matching match has been recorded and returns
// the first such event.
func (l *startupEventLog) waitFor(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	var found Event
	testwait.Until(t, what, func() bool {
		for _, ev := range l.snapshot() {
			if match(ev) {
				found = ev
				return true
			}
		}
		return false
	})
	return found
}

func (l *startupEventLog) count(match func(Event) bool) int {
	n := 0
	for _, ev := range l.snapshot() {
		if match(ev) {
			n++
		}
	}
	return n
}

// requireNone fails when any recorded event matches match.
func (l *startupEventLog) requireNone(t *testing.T, what string, match func(Event) bool) {
	t.Helper()
	for _, ev := range l.snapshot() {
		if match(ev) {
			t.Fatalf("%s: %+v", what, ev)
		}
	}
}

func startupKind(kind EventKind) func(Event) bool {
	return func(ev Event) bool { return ev.Kind == kind }
}

func startupState(state State) func(Event) bool {
	return func(ev Event) bool { return ev.Kind == EventState && ev.State == state }
}

// startupSleepRecorder is the injected pacing sleep: it records every requested
// delay and, while block is set, parks the caller until the test releases it or
// its context ends. Parked loops can then be torn down exactly rather than after
// a guessed margin.
type startupSleepRecorder struct {
	mu      sync.Mutex
	delays  []time.Duration
	entered chan time.Duration
	release chan struct{}
	block   bool
	once    sync.Once
}

func newStartupSleepRecorder(block bool) *startupSleepRecorder {
	return &startupSleepRecorder{
		block:   block,
		entered: make(chan time.Duration, 1),
		release: make(chan struct{}),
	}
}

func (s *startupSleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	select {
	case s.entered <- d:
	default:
	}
	if !s.block {
		return nil
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitParked blocks until the loop reaches a pacing sleep.
func (s *startupSleepRecorder) waitParked(t *testing.T, what string) time.Duration {
	t.Helper()
	select {
	case d := <-s.entered:
		return d
	case <-time.After(testwait.Budget(t)):
		t.Fatalf("timed out waiting for %s to park in backoff", what)
		return 0
	}
}

// wake releases every parked and future sleep.
func (s *startupSleepRecorder) wake() { s.once.Do(func() { close(s.release) }) }

func (s *startupSleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

func startupIdentityJitter(d time.Duration) time.Duration { return d }

// startupLoopState is one registered supervisor as the test observes it.
type startupLoopState struct {
	generation uint64
	startup    bool
}

// startupLoopStates snapshots the loops registered for name.
func startupLoopStates(m *Manager, name string) []startupLoopState {
	m.mu.Lock()
	defer m.mu.Unlock()
	var states []startupLoopState
	for loop := range m.supervisors[name] {
		states = append(states, startupLoopState{generation: loop.host.Generation, startup: loop.startup})
	}
	return states
}

// startupHealthToggleRunner fails the preflight of every reconnect attempt
// until healthy is set, then answers the standard canned preflight. attempts
// counts preflight attempts, so a test can assert one loop — not two — tries.
func startupHealthToggleRunner(t *testing.T, healthy *atomic.Bool, attempts *atomic.Int32) *fakeRunner {
	t.Helper()
	canned := cannedRun(nil)
	return &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if classifyRemote(argv) == opUnameKernel {
				attempts.Add(1)
				if !healthy.Load() {
					return nil, errors.New("connection reset by peer")
				}
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
}

// startupFailingRunner fails every preflight attempt retryably, so a reconnect
// loop can only ever park in backoff.
func startupFailingRunner(t *testing.T) *fakeRunner {
	t.Helper()
	canned := cannedRun(nil)
	return &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if classifyRemote(argv) == opUnameKernel {
				return nil, errors.New("connection reset by peer")
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
}

// startupHostFixture scripts the remote hub boundary for a compatible installed
// executable whose hub is either serving or stopped: health refuses while
// stopped, the start command brings the hub up, and attempts counts preflights.
type startupHostFixture struct {
	version  string
	flags    []string
	running  atomic.Bool
	starts   atomic.Int32
	deploys  atomic.Int32
	attempts atomic.Int32
}

func newStartupHostFixture(running bool) *startupHostFixture {
	f := &startupHostFixture{version: "startup-host-build", flags: []string{"api-log"}}
	f.running.Store(running)
	return f
}

func (f *startupHostFixture) manager(t *testing.T, opts Options) *Manager {
	t.Helper()
	fr := &fakeRunner{}
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		command := strings.Join(argv, " ")
		switch {
		case strings.HasSuffix(command, "uname -s"):
			f.attempts.Add(1)
			return cannedRun(nil)(ctx, argv, stdin)
		case strings.Contains(command, "launch-check"):
			return json.Marshal(map[string]any{
				"hub_endpoint_schema": 1,
				"protocol":            appwire.ProtocolVersion,
				"version":             f.version,
				"launch_flags":        f.flags,
			})
		case strings.Contains(command, "api/health"):
			if !f.running.Load() {
				return nil, errors.New("connection refused")
			}
			return []byte(fmt.Sprintf(`{"version":%q,"mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`, f.version)), nil
		case strings.Contains(command, "lsof -ti :9180"):
			if f.running.Load() {
				return []byte("1234\n"), nil
			}
			return []byte(noListenerMarker), nil
		case strings.Contains(command, "list-units"):
			return []byte("evener-hub.service loaded inactive dead\n"), nil
		case strings.Contains(command, "systemctl start"), strings.Contains(command, "systemctl restart"):
			f.starts.Add(1)
			f.running.Store(true)
			return nil, nil
		case strings.Contains(command, "cat >"), strings.Contains(command, "evener-install.XXXXXX"):
			f.deploys.Add(1)
			return nil, errors.New("unexpected deploy")
		default:
			return cannedRun(nil)(ctx, argv, stdin)
		}
	}
	fr.startFn = func(ctx context.Context, argv []string, stderr io.Writer) (Stdio, error) {
		if !f.running.Load() {
			return nil, errors.New("bridge has no listener")
		}
		return goodStartFn(t)(ctx, argv, stderr)
	}
	model := modelSelectedProcess(t, fr, "/opt/evener/bin/evener", f.version, f.version)
	model.observe = func(model *selectedProcessFixture) { model.stopped = !f.running.Load() }
	opts.controllerVersionOverride = f.version
	if opts.BuildBinary == nil {
		opts.BuildBinary = writeStageBinary
	}
	return newTestManager(t, testRegistry(t, startupTestHost()), fr, opts)
}

// An attach announces the registration it was built from, so a consumer can
// record the exact (generation, incarnation_id) pair without reading the
// registry and racing a later edit.
func TestAttachEventCarriesRegistrationIdentity(t *testing.T) {
	log := &startupEventLog{}
	fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
	m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{OnEvent: log.record})
	registered, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("the test registry holds no alpha")
	}
	if registered.Generation != startupTestGeneration || registered.IncarnationID != startupTestIncarnation {
		t.Fatalf("registered identity = (%d, %q), want (%d, %q)", registered.Generation, registered.IncarnationID, startupTestGeneration, startupTestIncarnation)
	}
	if _, err := m.Ensure(t.Context(), "alpha"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ev := log.waitFor(t, "the attached event", startupKind(EventAttached))
	if ev.Generation != registered.Generation || ev.IncarnationID != registered.IncarnationID {
		t.Fatalf("Attached identity = (%d, %q), want (%d, %q)", ev.Generation, ev.IncarnationID, registered.Generation, registered.IncarnationID)
	}
}

func TestStartReconnectValidationAndIdempotency(t *testing.T) {
	t.Run("nil registry", func(t *testing.T) {
		m := New(nil, Options{Runner: &fakeRunner{}})
		t.Cleanup(func() { _ = m.Close() })
		if err := m.StartReconnect("alpha"); !errors.Is(err, ErrHostNotFound) {
			t.Fatalf("StartReconnect without a registry = %v, want ErrHostNotFound", err)
		}
	})

	t.Run("unknown host", func(t *testing.T) {
		fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{})
		if err := m.StartReconnect("nope"); !errors.Is(err, ErrHostNotFound) {
			t.Fatalf("StartReconnect(unknown) = %v, want ErrHostNotFound", err)
		}
	})

	t.Run("closed manager", func(t *testing.T) {
		fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{})
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if err := m.StartReconnect("alpha"); !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("StartReconnect after Close = %v, want ErrManagerClosed", err)
		}
	})

	t.Run("already attached", func(t *testing.T) {
		fr := &fakeRunner{runFn: cannedRun(nil), startFn: goodStartFn(t)}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{})
		if _, err := m.Ensure(t.Context(), "alpha"); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		runs, starts := len(fr.recordedRuns()), len(fr.recordedStarts())
		if err := m.StartReconnect("alpha"); err != nil {
			t.Fatalf("StartReconnect(attached) = %v, want nil", err)
		}
		if got := len(fr.recordedRuns()); got != runs {
			t.Fatalf("StartReconnect(attached) issued %d extra Run calls, want 0", got-runs)
		}
		if got := len(fr.recordedStarts()); got != starts {
			t.Fatalf("StartReconnect(attached) issued %d extra Start calls, want 0", got-starts)
		}
	})

	t.Run("concurrent calls while reconnecting", func(t *testing.T) {
		healthy := &atomic.Bool{}
		var attempts atomic.Int32
		fr := startupHealthToggleRunner(t, healthy, &attempts)
		sleep := newStartupSleepRecorder(true)
		var exited atomic.Int32
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
			sleep:           sleep.sleep,
			jitter:          startupIdentityJitter,
			superviseExited: func(string) { exited.Add(1) },
		})
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				errs <- m.StartReconnect("alpha")
			}()
		}
		close(start)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatalf("StartReconnect = %v, want nil", err)
			}
		}
		sleep.waitParked(t, "the startup loop")
		// A sequential repeat while parked must also dedupe against the live loop.
		if err := m.StartReconnect("alpha"); err != nil {
			t.Fatalf("sequential StartReconnect while parked = %v, want nil", err)
		}
		if got := attempts.Load(); got != 1 {
			t.Fatalf("preflight attempts = %d, want 1 for concurrent and sequential StartReconnect calls", got)
		}
		if states := startupLoopStates(m, "alpha"); len(states) != 1 || !states[0].startup {
			t.Fatalf("registered loops = %+v, want exactly one startup loop", states)
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if got := exited.Load(); got != 1 {
			t.Fatalf("supervisors exited = %d, want 1", got)
		}
		if states := startupLoopStates(m, "alpha"); len(states) != 0 {
			t.Fatalf("loops after Close = %+v, want none", states)
		}
	})
}

func TestStartReconnectAttachesImmediatelyAndHandsOffToMidRunSupervise(t *testing.T) {
	fixture := newStartupHostFixture(true)
	sleep := newStartupSleepRecorder(true)
	defer sleep.wake()
	log := &startupEventLog{}
	m := fixture.manager(t, Options{
		OnEvent: log.record,
		sleep:   sleep.sleep,
		jitter:  startupIdentityJitter,
	})
	registered, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("the test registry holds no alpha")
	}

	if err := m.StartReconnect("alpha"); err != nil {
		t.Fatalf("StartReconnect: %v", err)
	}
	ev := log.waitFor(t, "the startup attach", startupKind(EventAttached))
	if ev.Generation != registered.Generation || ev.IncarnationID != registered.IncarnationID {
		t.Fatalf("Attached identity = (%d, %q), want (%d, %q)", ev.Generation, ev.IncarnationID, registered.Generation, registered.IncarnationID)
	}
	if !m.Attached("alpha") {
		t.Fatal("Attached after the startup attach = false, want true")
	}
	if delays := sleep.recorded(); len(delays) != 0 {
		t.Fatalf("startup backing off before its first dial: %v", delays)
	}
	ch, ok := m.ChannelIfAttached("alpha")
	if !ok {
		t.Fatal("no live channel after the startup attach")
	}
	// The startup loop retires only once the channel's own supervisor owns it.
	testwait.Until(t, "the startup loop to hand off", func() bool {
		states := startupLoopStates(m, "alpha")
		return len(states) == 1 && !states[0].startup
	})

	// Drop the link with the remote hub now stopped. Mid-run supervision keeps
	// its rule of never starting a hub, proving the startup loop handed off
	// rather than being replaced by another explicit-Connect attempt.
	fixture.running.Store(false)
	ch.markLost()
	sleep.waitParked(t, "the mid-run supervisor")
	sleep.wake()
	testwait.Until(t, "the mid-run reconnect attempt", func() bool {
		return fixture.attempts.Load() >= 2
	})
	if got := fixture.starts.Load(); got != 0 {
		t.Fatalf("the mid-run supervisor issued %d hub-start commands, want 0", got)
	}
	assertNoHubStart(t, m.runner.(*fakeRunner).recordedRuns(), m.runner.(*fakeRunner).recordedStarts())
	log.requireNone(t, "a retryable mid-run failure announced a terminal failure", startupKind(EventFailed))
}

func TestStartReconnectBootstrapsStoppedCompatibleHub(t *testing.T) {
	fixture := newStartupHostFixture(false)
	log := &startupEventLog{}
	m := fixture.manager(t, Options{OnEvent: log.record})
	registered, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("the test registry holds no alpha")
	}

	if err := m.StartReconnect("alpha"); err != nil {
		t.Fatalf("StartReconnect: %v", err)
	}
	ev := log.waitFor(t, "the bootstrapped attach", startupKind(EventAttached))
	if ev.Generation != registered.Generation || ev.IncarnationID != registered.IncarnationID {
		t.Fatalf("Attached identity = (%d, %q), want (%d, %q)", ev.Generation, ev.IncarnationID, registered.Generation, registered.IncarnationID)
	}
	if !m.Attached("alpha") {
		t.Fatal("Attached after the bootstrapped attach = false, want true")
	}
	if got := fixture.starts.Load(); got != 1 {
		t.Fatalf("hub-start commands = %d, want 1", got)
	}
	if got := fixture.deploys.Load(); got != 0 {
		t.Fatalf("deploys = %d, want 0 for a compatible installed executable", got)
	}
}

func TestStartReconnectRetriesWithBoundedBackoff(t *testing.T) {
	const failures int32 = 4
	base, limit := 10*time.Millisecond, 30*time.Millisecond
	var attempts atomic.Int32
	canned := cannedRun(nil)
	fr := &fakeRunner{
		runFn: func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if classifyRemote(argv) == opUnameKernel && attempts.Add(1) <= failures {
				return nil, errors.New("connection reset by peer")
			}
			return canned(ctx, argv, stdin)
		},
		startFn: goodStartFn(t),
	}
	sleep := newStartupSleepRecorder(false)
	log := &startupEventLog{}
	m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
		OnEvent:     log.record,
		sleep:       sleep.sleep,
		jitter:      startupIdentityJitter,
		BackoffBase: base,
		BackoffMax:  limit,
	})
	registered, ok := m.reg.Get("alpha")
	if !ok {
		t.Fatal("the test registry holds no alpha")
	}

	if err := m.StartReconnect("alpha"); err != nil {
		t.Fatalf("StartReconnect: %v", err)
	}
	ev := log.waitFor(t, "the attach after retries", startupKind(EventAttached))
	if ev.Generation != registered.Generation || ev.IncarnationID != registered.IncarnationID {
		t.Fatalf("Attached identity = (%d, %q), want (%d, %q)", ev.Generation, ev.IncarnationID, registered.Generation, registered.IncarnationID)
	}
	if got := attempts.Load(); got != failures+1 {
		t.Fatalf("preflight attempts = %d, want %d", got, failures+1)
	}
	if got := log.count(startupState(StateReconnecting)); got != int(failures) {
		t.Fatalf("StateReconnecting events = %d, want %d", got, failures)
	}
	want := []time.Duration{base, 2 * base, limit, limit}
	if got := sleep.recorded(); !slices.Equal(got, want) {
		t.Fatalf("backoff delays = %v, want %v", got, want)
	}
}

func TestStartReconnectStopsOnTerminalError(t *testing.T) {
	current, ok := appwire.ProtocolVersionNumber(appwire.ProtocolVersion)
	if !ok {
		t.Fatalf("ProtocolVersion %q has no canonical number", appwire.ProtocolVersion)
	}
	newer := fmt.Sprintf("evener-appwire-v%d", current+1)
	canned := cannedRun(map[remoteOp][]byte{
		opLaunchCheck: []byte(fmt.Sprintf(`{"hub_endpoint_schema":1,"protocol":%q,"version":"dev","launch_flags":["api-log"]}`, newer)),
	})
	fr := &fakeRunner{
		runFn: canned,
		startFn: func(context.Context, []string, io.Writer) (Stdio, error) {
			bridge := newFakeBridge(newer)
			t.Cleanup(func() { _ = bridge.stdio.Kill() })
			return bridge.stdio, nil
		},
	}
	var attempts atomic.Int32
	run := fr.runFn
	fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		if classifyRemote(argv) == opUnameKernel {
			attempts.Add(1)
		}
		return run(ctx, argv, stdin)
	}
	sleep := newStartupSleepRecorder(false)
	var exited atomic.Int32
	log := &startupEventLog{}
	m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
		OnEvent:         log.record,
		sleep:           sleep.sleep,
		jitter:          startupIdentityJitter,
		superviseExited: func(string) { exited.Add(1) },
	})

	if err := m.StartReconnect("alpha"); err != nil {
		t.Fatalf("StartReconnect: %v", err)
	}
	failed := log.waitFor(t, "the terminal failure", startupKind(EventFailed))
	if !errors.Is(failed.Err, ErrHostProtocolNewer) {
		t.Fatalf("EventFailed error = %v, want ErrHostProtocolNewer", failed.Err)
	}
	log.waitFor(t, "the disconnected state after the terminal failure", startupState(StateDisconnected))
	testwait.Until(t, "the terminal startup loop to exit", func() bool { return exited.Load() == 1 })
	if got := attempts.Load(); got != 1 {
		t.Fatalf("preflight attempts = %d, want 1", got)
	}
	if got := log.count(startupState(StateReconnecting)); got != 0 {
		t.Fatalf("StateReconnecting events = %d, want 0", got)
	}
	if got := log.count(startupKind(EventAttached)); got != 0 {
		t.Fatalf("EventAttached events = %d, want 0", got)
	}
	if delays := sleep.recorded(); len(delays) != 0 {
		t.Fatalf("terminal failure slept between attempts: %v", delays)
	}
	if states := startupLoopStates(m, "alpha"); len(states) != 0 {
		t.Fatalf("loops after the terminal failure = %+v, want none", states)
	}
}

func TestStartReconnectStopsCleanlyOnCloseRemoveAndUpdate(t *testing.T) {
	// parkedStartupManager starts one startup reconnect over a runner that always
	// fails retryably, and returns once its loop is parked in backoff.
	parkedStartupManager := func(t *testing.T) (*Manager, *startupSleepRecorder, *startupEventLog) {
		t.Helper()
		fr := startupFailingRunner(t)
		sleep := newStartupSleepRecorder(true)
		log := &startupEventLog{}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
			OnEvent: log.record,
			sleep:   sleep.sleep,
			jitter:  startupIdentityJitter,
		})
		if err := m.StartReconnect("alpha"); err != nil {
			t.Fatalf("StartReconnect: %v", err)
		}
		sleep.waitParked(t, "the startup loop")
		log.waitFor(t, "the reconnecting state", startupState(StateReconnecting))
		return m, sleep, log
	}

	t.Run("Close", func(t *testing.T) {
		m, _, log := parkedStartupManager(t)
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// Close waits supervisorsWG, so the startup loop has exited and
		// deregistered by the time it returns.
		if states := startupLoopStates(m, "alpha"); len(states) != 0 {
			t.Fatalf("loops after Close = %+v, want none", states)
		}
		log.requireNone(t, "Close announced a terminal failure", startupKind(EventFailed))
	})

	t.Run("RemoveHost", func(t *testing.T) {
		m, _, log := parkedStartupManager(t)
		if err := m.RemoveHost("alpha"); err != nil {
			t.Fatalf("RemoveHost: %v", err)
		}
		// No channel was ever mapped, so the disconnected state is the only signal a
		// consumer has to clear midAttach.
		log.waitFor(t, "the disconnected state", startupState(StateDisconnected))
		testwait.Until(t, "the startup loop to exit", func() bool {
			return len(startupLoopStates(m, "alpha")) == 0
		})
		log.requireNone(t, "removal announced a terminal failure", startupKind(EventFailed))
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	t.Run("UpdateHost", func(t *testing.T) {
		m, _, log := parkedStartupManager(t)
		updated := startupTestHost()
		updated.SSH = "beta.example"
		if err := m.UpdateHost(updated, nil); err != nil {
			t.Fatalf("UpdateHost: %v", err)
		}
		// No channel was ever mapped, so the disconnected state is the only signal a
		// consumer has to clear midAttach.
		log.waitFor(t, "the disconnected state", startupState(StateDisconnected))
		testwait.Until(t, "the startup loop to exit", func() bool {
			return len(startupLoopStates(m, "alpha")) == 0
		})
		log.requireNone(t, "the update announced a terminal failure", startupKind(EventFailed))
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}

func TestEnsureRacingStartupReconnectPreservesOrRetiresLoop(t *testing.T) {
	t.Run("transient failure preserves the startup loop", func(t *testing.T) {
		healthy := &atomic.Bool{}
		var attempts atomic.Int32
		fr := startupHealthToggleRunner(t, healthy, &attempts)
		sleep := newStartupSleepRecorder(true)
		log := &startupEventLog{}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
			OnEvent: log.record,
			sleep:   sleep.sleep,
			jitter:  startupIdentityJitter,
		})
		if err := m.StartReconnect("alpha"); err != nil {
			t.Fatalf("StartReconnect: %v", err)
		}
		sleep.waitParked(t, "the startup loop")

		// The manual Ensure must not wait behind the parked backoff.
		done := make(chan error, 1)
		go func() {
			_, err := m.Ensure(t.Context(), "alpha")
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Ensure against a failing host succeeded")
			}
		case <-time.After(testwait.Budget(t)):
			t.Fatal("the manual Ensure waited for the startup loop's backoff")
		}
		log.requireNone(t, "a transient Ensure failure cleared midAttach while a supervisor was backing off", startupState(StateDisconnected))
		if states := startupLoopStates(m, "alpha"); len(states) != 1 || !states[0].startup {
			t.Fatalf("registered loops = %+v, want the startup loop still alive", states)
		}

		// The next wake attaches cleanly.
		healthy.Store(true)
		sleep.wake()
		registered, ok := m.reg.Get("alpha")
		if !ok {
			t.Fatal("the test registry holds no alpha")
		}
		ev := log.waitFor(t, "the attach after the startup loop's wake", startupKind(EventAttached))
		if ev.Generation != registered.Generation || ev.IncarnationID != registered.IncarnationID {
			t.Fatalf("Attached identity = (%d, %q), want (%d, %q)", ev.Generation, ev.IncarnationID, registered.Generation, registered.IncarnationID)
		}
		if !m.Attached("alpha") {
			t.Fatal("Attached after the startup loop's wake = false, want true")
		}
		testwait.Until(t, "the startup loop to hand off", func() bool {
			states := startupLoopStates(m, "alpha")
			return len(states) == 1 && !states[0].startup
		})
		log.requireNone(t, "the recovered host announced a terminal failure", startupKind(EventFailed))
	})

	t.Run("committed manual attach retires the startup loop", func(t *testing.T) {
		healthy := &atomic.Bool{}
		var attempts atomic.Int32
		fr := startupHealthToggleRunner(t, healthy, &attempts)
		sleep := newStartupSleepRecorder(true)
		defer sleep.wake()
		log := &startupEventLog{}
		m := newTestManager(t, testRegistry(t, startupTestHost()), fr, Options{
			OnEvent: log.record,
			sleep:   sleep.sleep,
			jitter:  startupIdentityJitter,
		})
		if err := m.StartReconnect("alpha"); err != nil {
			t.Fatalf("StartReconnect: %v", err)
		}
		sleep.waitParked(t, "the startup loop")

		// The manual attach commits supervision, which retires the startup loop.
		healthy.Store(true)
		ch, err := m.Ensure(t.Context(), "alpha")
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		log.waitFor(t, "the manual attach", startupKind(EventAttached))
		testwait.Until(t, "the startup loop to retire", func() bool {
			for _, state := range startupLoopStates(m, "alpha") {
				if state.startup {
					return false
				}
			}
			return true
		})

		// Drop the new channel immediately. Only the channel's mid-run
		// supervisor may reconnect; the retired startup loop must not race it
		// with a second attempt.
		before := attempts.Load()
		ch.markLost()
		sleep.waitParked(t, "the mid-run supervisor")
		sleep.wake()
		testwait.Until(t, "the mid-run supervisor to publish the replacement attach", func() bool {
			return log.count(startupKind(EventAttached)) == 2 && m.Attached("alpha")
		})
		testwait.Until(t, "only the replacement mid-run supervisor to remain registered", func() bool {
			states := startupLoopStates(m, "alpha")
			return len(states) == 1 && !states[0].startup
		})
		if got := attempts.Load(); got != before+1 {
			t.Fatalf("reconnect attempts after the drop = %d, want exactly %d", got, before+1)
		}
		log.requireNone(t, "the handoff announced a terminal failure", startupKind(EventFailed))
	})
}
