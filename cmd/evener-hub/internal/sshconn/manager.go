package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"slices"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// State is a channel lifecycle state. 04a's chain is
// disconnected -> preflighting -> attaching -> attached -> reconnecting; Close
// is terminal. The deploying/restarting states belong to 04b.
type State string

const (
	StateDisconnected State = "disconnected"
	StatePreflighting State = "preflighting"
	StateAttaching    State = "attaching"
	StateAttached     State = "attached"
	StateReconnecting State = "reconnecting"
)

// EventKind classifies lifecycle events a manager emits.
type EventKind string

const (
	// EventState reports an intermediate transition (preflighting, attaching,
	// reconnecting, disconnected).
	EventState EventKind = "state"
	// EventAttached reports a usable channel for Host.
	EventAttached EventKind = "attached"
	// EventDetached reports that Host's channel went away; the manager is
	// reconnecting (or has given up, signaled by a following EventFailed plus
	// the disconnected state).
	EventDetached EventKind = "detached"
	// EventFailed reports a terminal attach failure for Host.
	EventFailed EventKind = "failed"
)

// Event is one lifecycle notification. Component 05 listens for EventAttached
// and EventDetached to add/remove the corresponding appsource.Source.
type Event struct {
	Host  string
	Kind  EventKind
	State State
	Err   error
}

// Options configures a Manager. The zero value is usable: exec runner, ssh to
// stderr, default timeouts, and a default backoff.
type Options struct {
	// Runner is the process seam; nil uses the production execRunner.
	Runner Runner
	// Stderr is the ssh diagnostic sink; nil uses os.Stderr.
	Stderr io.Writer
	// Logger, when set, receives lifecycle log lines.
	Logger func(format string, args ...any)

	ConnectTimeout      time.Duration
	ServerAliveInterval time.Duration
	ServerAliveCountMax int

	// ClientName/ClientVersion are reported in the AppWire initialize
	// handshake. Defaults: "evener-hub" and buildinfo.Version().
	ClientName    string
	ClientVersion string

	// BackoffBase/BackoffMax bound the reconnect delay. Defaults 500ms / 30s.
	BackoffBase time.Duration
	BackoffMax  time.Duration

	// OnEvent, when set, is called synchronously for every lifecycle event with
	// the per-host lock held. Holding it is deliberate: it is what orders a
	// Detached before any Attached that follows it for the same host. It also
	// means a callback must not call back into Manager — Ensure takes the same
	// non-reentrant lock and would deadlock. Record state, or hand the work to
	// another goroutine and call Ensure from there.
	OnEvent func(Event)

	// Test seams.
	sleep             func(context.Context, time.Duration) error
	jitter            func(time.Duration) time.Duration
	initializeTimeout time.Duration
	attemptTimeout    time.Duration
}

func (o Options) runner() Runner {
	if o.Runner != nil {
		return o.Runner
	}
	return execRunner{}
}

func (o Options) stderr() io.Writer {
	if o.Stderr != nil {
		return o.Stderr
	}
	return os.Stderr
}

func (o Options) clientName() string {
	if o.ClientName != "" {
		return o.ClientName
	}
	return "evener-hub"
}

func (o Options) clientVersion() string {
	if o.ClientVersion != "" {
		return o.ClientVersion
	}
	return buildinfo.Version()
}

func (o Options) backoffBase() time.Duration {
	if o.BackoffBase > 0 {
		return o.BackoffBase
	}
	return 500 * time.Millisecond
}

func (o Options) backoffMax() time.Duration {
	if o.BackoffMax > 0 {
		return o.BackoffMax
	}
	return 30 * time.Second
}

func (o Options) initTimeout() time.Duration {
	if o.initializeTimeout > 0 {
		return o.initializeTimeout
	}
	return 30 * time.Second
}

// attemptTimeout bounds one reconnect attempt (preflight plus attach). Without
// it an attempt inherits baseCtx, which lives until Close, so a single remote
// command that never returns would hold the host's lock indefinitely and stall
// every later reconnect and Ensure for that host. The default covers four
// preflight round trips plus the attach handshake.
func (o Options) attemptLimit() time.Duration {
	if o.attemptTimeout > 0 {
		return o.attemptTimeout
	}
	return 4*o.connectTimeout() + o.initTimeout()
}

func (o Options) waitSleep(ctx context.Context, d time.Duration) error {
	if o.sleep != nil {
		return o.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// defaultJitter spreads a delay over [d/2, d) so a fleet reconnecting at once
// does not stampede a host.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)))
}

// Manager owns the SSH channels for the configured hosts. It is safe for
// concurrent use.
type Manager struct {
	reg    *hostreg.Registry
	opts   Options
	runner Runner

	baseCtx context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	closed bool
	locks  map[string]*sync.Mutex
	chans  map[string]*Channel
}

// New builds a Manager over reg's validated hosts. opts.Runner defaults to the
// production execRunner, so a test must inject a fake to stay off ssh.
func New(reg *hostreg.Registry, opts Options) *Manager {
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		reg:     reg,
		opts:    opts,
		runner:  opts.runner(),
		baseCtx: baseCtx,
		cancel:  cancel,
		locks:   map[string]*sync.Mutex{},
		chans:   map[string]*Channel{},
	}
}

// Ensure returns a connected, initialized channel for host, preflighting and
// attaching as needed. It is idempotent while attached: repeated calls return
// the same *Channel.
//
// After a successful attach, Ensure starts a supervisor that watches the link
// and reconnects (fresh ssh bridge + client, never a hub start) with bounded
// exponential backoff and jitter until Manager.Close. ctx bounds this call and
// the handshake, not the channel's lifetime.
func (m *Manager) Ensure(ctx context.Context, name string) (*Channel, error) {
	if m.isClosed() {
		return nil, ErrManagerClosed
	}
	if m.reg == nil {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	host, ok := m.reg.Get(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	// The registry is the authority on the name's spelling, and every lookup
	// below (and in the supervisor) is keyed off host.Name: normalizing here
	// keeps a caller's stray whitespace from creating a second channel entry and
	// a second per-host lock.
	name = host.Name

	lock := m.hostLock(name)
	lock.Lock()

	if ch := m.liveChannel(name); ch != nil {
		lock.Unlock()
		return ch, nil
	}
	// A channel whose link has dropped is no longer usable, but it keeps
	// ownership until a replacement is attached: were this attach to fail, its
	// supervisor must still find the channel mapped so it can run the reconnect
	// loop. Only a successful publish retires it.
	stale := m.currentChannel(name)
	ch, err := m.ensureOnce(ctx, host)
	if err != nil {
		m.stateEvent(name, StateDisconnected)
		lock.Unlock()
		return nil, err
	}
	if !m.publishChannel(name, ch) {
		// Close landed while this attach was in flight. Handing back a channel
		// that nothing will ever supervise would be a lie, so reap it.
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	lock.Unlock()

	if m.isClosed() {
		// Close landed between publishing and announcing. The channel is already
		// unusable, so report the terminal state instead of announcing it.
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	// Retire the replaced channel outside the lock: Channel.Close blocks on the
	// ssh child's exit, and the supervisor does the same for its own channel.
	if stale != nil {
		_ = stale.Close()
	}
	m.attachEvent(name, StateAttached)
	go m.supervise(host, ch, lock)
	return ch, nil
}

// Close cancels every supervisor and closes every live channel. It is terminal
// for the Manager: the base context stays canceled, and a later Ensure reports
// ErrManagerClosed rather than attaching a channel nothing would supervise.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()
	chans := make([]*Channel, 0, len(m.chans))
	for _, ch := range m.chans {
		chans = append(chans, ch)
	}
	m.mu.Unlock()

	var first error
	for _, ch := range chans {
		if err := ch.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ensureOnce runs one full preflight-then-attach sequence with the state
// transitions around it.
func (m *Manager) ensureOnce(ctx context.Context, host hostreg.Host) (*Channel, error) {
	m.stateEvent(host.Name, StatePreflighting)
	facts, err := m.preflight(ctx, host)
	if err != nil {
		return nil, err
	}
	m.stateEvent(host.Name, StateAttaching)
	return m.attach(ctx, host, facts)
}

// attach spawns the bridge, wraps its stdio in a StreamTransport, and
// initializes the AppWire client.
func (m *Manager) attach(ctx context.Context, host hostreg.Host, facts Preflight) (*Channel, error) {
	argv := channelArgv(m.opts, host)
	sink := newDiagSink(m.opts.stderr())
	stdio, err := m.runner.Start(ctx, argv, sink)
	if err != nil {
		return nil, sshRunFailure(host.Name, "start bridge", err, sink.tail())
	}

	stream := &stdioReadWriter{in: stdio.Stdin(), out: stdio.Stdout()}
	monitor := newLinkMonitor(stream)
	transport := appwire.NewStreamTransport(monitor)
	client := appwire.NewClient(transport)
	ch := &Channel{
		host:      host,
		facts:     facts,
		stdio:     stdio,
		transport: transport,
		client:    client,
		lost:      make(chan struct{}),
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	// A short read (EOF/error) is the link-down edge. Wire it before the read
	// loop starts so no early drop is missed.
	monitor.onErr = ch.markLost
	// The client read loop must outlive this call's ctx; it ends when the
	// transport is closed (Channel.Close or the link dying).
	client.Start(m.baseCtx)

	initCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		initCtx, cancel = context.WithTimeout(ctx, m.opts.initTimeout())
	}
	defer cancel()

	if _, err := client.Initialize(initCtx, appwire.InitializeParams{
		ProtocolVersion: appwire.ProtocolVersion,
		ClientInfo:      appwire.ClientInfo{Name: m.opts.clientName(), Version: m.opts.clientVersion()},
	}); err != nil {
		reapBridge(transport, stdio)
		if m.baseCtx.Err() != nil {
			// Close landed while the handshake was in flight. The canceled base
			// context, not a transport fault, is why this channel is unusable.
			return nil, ErrManagerClosed
		}
		if _, isMismatch := errors.AsType[appwire.ProtocolVersionMismatchError](err); isMismatch {
			return nil, fmt.Errorf("%w: host %q: %w", ErrProtocolIncompatible, host.Name, err)
		}
		if isAuthFailure(sink.tail()) {
			return nil, fmt.Errorf("%w: host %q: %w: %s", ErrSSHAuth, host.Name, err, sink.tail())
		}
		return nil, fmt.Errorf("%w: host %q initialize: %w: %s", ErrSSHStart, host.Name, err, sink.tail())
	}

	if m.baseCtx.Err() != nil {
		reapBridge(transport, stdio)
		return nil, ErrManagerClosed
	}

	go func() {
		_ = stdio.Wait()
		close(ch.stopped)
		ch.markLost()
	}()
	return ch, nil
}

// reapBridge tears down a bridge that never became a channel. The synchronous
// Wait is load-bearing: os/exec copies the child's stderr into the diagnostic
// sink from its own goroutine and returns from Wait only once that copy has
// finished, so a failure cannot be classified off the sink before the child is
// reaped. It also closes the stream, which releases the pipes the child held.
func reapBridge(transport *appwire.StreamTransport, stdio Stdio) {
	if transport != nil {
		_ = transport.Close()
	}
	_ = stdio.Kill()
	_ = stdio.Wait()
}

// supervise waits for host's channel to drop, then reconnects with bounded
// exponential backoff. It never issues a hub-start command: re-attach is a
// fresh bridge/client against the already-running host hub.
//
// It holds the host lock only around state inspection, channel teardown, and
// one attach attempt. The backoff sleep happens with the lock released, so a
// concurrent Ensure never waits behind a delay that can reach BackoffMax.
func (m *Manager) supervise(host hostreg.Host, ch *Channel, lock *sync.Mutex) {
	select {
	case <-ch.lost:
	case <-m.baseCtx.Done():
		return
	}

	lock.Lock()
	if m.baseCtx.Err() != nil || ch.isClosed() || m.currentChannel(host.Name) != ch {
		lock.Unlock()
		return
	}
	m.clearChannel(host.Name)
	m.stateEvent(host.Name, StateReconnecting)
	m.detachEvent(host.Name, StateReconnecting)
	// Tearing the dead channel down can block on the ssh child's exit, which
	// needs no lock; a concurrent Ensure only has to be serialized against the
	// attach below.
	lock.Unlock()
	_ = ch.Close()

	delay := m.opts.backoffBase()
	for {
		if err := m.opts.waitSleep(m.baseCtx, m.jitterFor(delay)); err != nil {
			m.stateEvent(host.Name, StateDisconnected)
			return
		}
		if !m.reconnectOnce(host, lock) {
			return
		}
		m.stateEvent(host.Name, StateReconnecting)
		delay = nextBackoff(delay, m.opts.backoffMax())
	}
}

// reconnectOnce runs one re-attach attempt with the host lock held, and reports
// whether another attempt is worth making. Every outcome that ends the
// supervisor emits its own state event first.
func (m *Manager) reconnectOnce(host hostreg.Host, lock *sync.Mutex) bool {
	lock.Lock()
	defer lock.Unlock()

	if m.baseCtx.Err() != nil {
		m.stateEvent(host.Name, StateDisconnected)
		return false
	}
	if m.liveChannel(host.Name) != nil {
		// Another Ensure attached while we slept; its supervisor owns the host.
		return false
	}
	attemptCtx, cancel := context.WithTimeout(m.baseCtx, m.opts.attemptLimit())
	defer cancel()
	nch, err := m.ensureOnce(attemptCtx, host)
	if err == nil {
		if !m.publishChannel(host.Name, nch) {
			// Close landed mid-attempt; reap the channel it would have orphaned.
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		m.attachEvent(host.Name, StateAttached)
		go m.supervise(host, nch, lock)
		return false
	}
	if isTerminal(err) {
		m.failedEvent(host.Name, err)
		m.stateEvent(host.Name, StateDisconnected)
		return false
	}
	return true
}

func (m *Manager) jitterFor(d time.Duration) time.Duration {
	if m.opts.jitter != nil {
		return m.opts.jitter(d)
	}
	return defaultJitter(d)
}

// nextBackoff doubles delay, capped at limit.
func nextBackoff(delay, limit time.Duration) time.Duration {
	delay *= 2
	if limit > 0 && delay > limit {
		return limit
	}
	return delay
}

// isTerminal reports whether err ends the reconnect loop instead of being
// retried. Auth failures and every contract violation are terminal, matching
// spec 04's "Error handling".
func isTerminal(err error) bool {
	switch {
	case errors.Is(err, ErrProtocolIncompatible),
		errors.Is(err, ErrUnsupportedHost),
		errors.Is(err, ErrLaunchContract),
		errors.Is(err, ErrSSHAuth),
		errors.Is(err, ErrHostNotFound),
		errors.Is(err, ErrPreflightDecode):
		return true
	default:
		return false
	}
}

func (m *Manager) hostLock(name string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock := m.locks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[name] = lock
	}
	return lock
}

func (m *Manager) currentChannel(name string) *Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chans[name]
}

// liveChannel returns name's channel when it is present and still usable. A
// channel whose link has dropped is not usable: Ensure must attach a fresh one
// rather than hand back a handle that fails on first use.
func (m *Manager) liveChannel(name string) *Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := m.chans[name]
	if ch == nil || ch.isClosed() || ch.isLost() {
		return nil
	}
	return ch
}

// publishChannel records ch as name's channel unless Close already ran, in which
// case nothing would ever supervise it: the caller reaps ch and reports
// ErrManagerClosed instead.
func (m *Manager) publishChannel(name string, ch *Channel) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.chans[name] = ch
	return true
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

func (m *Manager) clearChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.chans, name)
}

func (m *Manager) logf(format string, args ...any) {
	if m.opts.Logger != nil {
		m.opts.Logger(format, args...)
	}
}

func (m *Manager) emit(ev Event) {
	m.logf("sshconn: host %s state=%s kind=%s err=%v", ev.Host, ev.State, ev.Kind, ev.Err)
	if m.opts.OnEvent != nil {
		m.opts.OnEvent(ev)
	}
}

func (m *Manager) stateEvent(name string, s State) {
	m.emit(Event{Host: name, Kind: EventState, State: s})
}

func (m *Manager) attachEvent(name string, s State) {
	m.emit(Event{Host: name, Kind: EventAttached, State: s})
}

func (m *Manager) detachEvent(name string, s State) {
	m.emit(Event{Host: name, Kind: EventDetached, State: s})
}

func (m *Manager) failedEvent(name string, err error) {
	m.emit(Event{Host: name, Kind: EventFailed, State: StateDisconnected, Err: err})
}

// Channel is one owned SSH channel plus the AppWire client over it.
type Channel struct {
	host      hostreg.Host
	facts     Preflight
	stdio     Stdio
	transport *appwire.StreamTransport
	client    *appwire.Client

	lost      chan struct{}
	lostOnce  sync.Once
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	stopped   chan struct{}
}

// Client returns the initialized AppWire client over this channel.
func (c *Channel) Client() *appwire.Client { return c.client }

// Transport returns the StreamTransport carrying AppWire over ssh stdio.
func (c *Channel) Transport() appwire.Transport { return c.transport }

// Preflight returns the host facts learned before attaching: GOOS/GOARCH,
// HOME, and the resolved config/state roots. The value owns its slice, so a
// caller cannot reach back into the channel's state through it.
func (c *Channel) Preflight() Preflight {
	pf := c.facts
	pf.LaunchFlags = slices.Clone(c.facts.LaunchFlags)
	return pf
}

// Host returns the registry entry this channel was built from.
func (c *Channel) Host() hostreg.Host { return c.host }

// Close kills the ssh child and closes the stream. It is idempotent and
// terminal.
func (c *Channel) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.transport != nil {
			if err := c.transport.Close(); err != nil {
				c.closeErr = err
			}
		}
		if c.stdio != nil {
			if err := c.stdio.Kill(); err != nil && c.closeErr == nil {
				c.closeErr = err
			}
			if c.stopped != nil {
				<-c.stopped
			}
		}
		c.markLost()
	})
	return c.closeErr
}

func (c *Channel) markLost() {
	c.lostOnce.Do(func() { close(c.lost) })
}

func (c *Channel) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// isLost reports whether the link has dropped. The channel is unusable from that
// moment on, even though Close may not have run yet.
func (c *Channel) isLost() bool {
	select {
	case <-c.lost:
		return true
	default:
		return false
	}
}

// stdioReadWriter presents a child's stdin/stdout as the single
// io.ReadWriteCloser StreamTransport needs. Nothing else may touch these pipes:
// the bridge owns stdout for frames (component 02's hard rule).
type stdioReadWriter struct {
	in  io.WriteCloser
	out io.ReadCloser
}

func (s *stdioReadWriter) Read(p []byte) (int, error)  { return s.out.Read(p) }
func (s *stdioReadWriter) Write(p []byte) (int, error) { return s.in.Write(p) }

func (s *stdioReadWriter) Close() error {
	errIn := s.in.Close()
	errOut := s.out.Close()
	if errIn != nil {
		return errIn
	}
	return errOut
}

// linkMonitor signals the first read error/EOF on the stream, which is the
// channel's link-down edge. Only the AppWire client's read loop reads this
// stream, so nothing else observes or steals the signal.
type linkMonitor struct {
	rw    io.ReadWriteCloser
	onErr func()
	once  sync.Once
}

func newLinkMonitor(rw io.ReadWriteCloser) *linkMonitor {
	return &linkMonitor{rw: rw, onErr: func() {}}
}

func (l *linkMonitor) Read(p []byte) (int, error) {
	n, err := l.rw.Read(p)
	if err != nil {
		l.once.Do(l.onErr)
	}
	return n, err
}

func (l *linkMonitor) Write(p []byte) (int, error) { return l.rw.Write(p) }
func (l *linkMonitor) Close() error                { return l.rw.Close() }
