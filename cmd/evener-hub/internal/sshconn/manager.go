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
	// supervisors holds each host's live reconnect loop cancel func, so a terminal
	// failure can end that loop even while it waits out a backoff.
	supervisors map[string]context.CancelFunc
	// supervisorsWG counts the live supervise goroutines (each reconnect attempt
	// runs inside one), so Close can wait for them to quiesce before it returns.
	supervisorsWG sync.WaitGroup
	// announced records, per host, the channel whose Attached has no matching
	// Detached yet. Close reads it to pair the events for the channel it tears
	// down without emitting a Detached for one already paired.
	announced map[string]*Channel
}

// New builds a Manager over reg's validated hosts. opts.Runner defaults to the
// production execRunner, so a test must inject a fake to stay off ssh.
func New(reg *hostreg.Registry, opts Options) *Manager {
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		reg:         reg,
		opts:        opts,
		runner:      opts.runner(),
		baseCtx:     baseCtx,
		cancel:      cancel,
		locks:       map[string]*sync.Mutex{},
		chans:       map[string]*Channel{},
		supervisors: map[string]context.CancelFunc{},
		announced:   map[string]*Channel{},
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
		err := m.channelUsable(name, ch)
		lock.Unlock()
		if err == nil {
			return ch, nil
		}
		if errors.Is(err, ErrManagerClosed) {
			// Close tore this channel down; closing it again is a no-op.
			_ = ch.Close()
		}
		return nil, err
	}
	// A channel whose link has dropped is no longer usable, but it keeps
	// ownership until a replacement is attached: were this attach to fail, its
	// supervisor must still find the channel mapped so it can run the reconnect
	// loop. Only a successful publish retires it.
	stale := m.currentChannel(name)
	// Retire the replaced channel on every path out of here, early returns
	// included: a Close racing this attach must not leave the old ssh child, its
	// pipes, and its transport alive. It runs outside the host lock, since
	// Channel.Close blocks on that child's exit.
	defer func() {
		if stale != nil {
			_ = stale.Close()
		}
	}()
	// Cap the attempt at attemptLimit whether or not the caller brought a
	// deadline — a long caller deadline must not hold this host's lock that long —
	// and tie it to the manager's lifetime: without the tie an in-flight attempt
	// outlives Manager.Close until its own deadline, with its ssh child alive.
	attemptCtx, cancel := context.WithTimeout(ctx, m.opts.attemptLimit())
	defer cancel()
	stopClose := context.AfterFunc(m.baseCtx, cancel)
	defer stopClose()
	ch, err := m.ensureOnce(attemptCtx, host)
	if err != nil {
		// Close canceling the attempt surfaces a preflight that was in flight as a
		// retryable transport failure; the canceled base context, not the host, is
		// why. Match the handshake path and report the closed manager rather than a
		// retryable error a supervisor would keep chasing. A terminal cause found
		// concurrently is kept, because it names what actually went wrong.
		if m.baseCtx.Err() != nil && !isTerminal(err) {
			err = ErrManagerClosed
		}
		// A terminal failure is the documented EventFailed case, not just a state
		// transition: a consumer has to be able to tell it from a retryable one.
		if isTerminal(err) {
			// A retired channel was announced as attached, so its consumer needs the
			// matching Detached before the terminal Failed — the contract this file's
			// event docs state, and without it component 05 keeps the source.
			if stale != nil {
				m.detachEvent(name, StateDisconnected)
			}
			// Close canceling the attach is not a host failure: consumers shutting the
			// manager down must not see an EventFailed for it, exactly as the early
			// isClosed return above emits nothing.
			if !errors.Is(err, ErrManagerClosed) {
				m.failedEvent(name, err)
			}
			// The replacement cannot be built, so the dropped channel must not keep
			// ownership: its supervisor would treat the host as its own and repeat a
			// failure that cannot succeed. The deferred close reaps it.
			m.clearChannel(name)
			// A supervisor that retired its own channel and is waiting out a
			// backoff must stop too: otherwise it repeats the terminal outcome
			// (a second EventFailed for a host already announced as finished).
			m.stopSupervisor(name)
		}
		m.stateEvent(name, StateDisconnected)
		lock.Unlock()
		return nil, err
	}
	// Validate before publishing, as reconnectOnce does. Publishing first would
	// displace the dropped channel whose supervisor is waiting to recover, and a
	// replacement that died in the handshake would then leave the host with no
	// supervisor at all — the predecessor's gone with the map entry and none
	// started for the replacement.
	if ch.isClosed() || ch.isLost() {
		// A dropped predecessor stays mapped: its supervisor still owns the host and
		// will reconnect. With no predecessor there is nothing to supervise, so the
		// state is honestly disconnected.
		if stale == nil {
			m.stateEvent(name, StateDisconnected)
		}
		lock.Unlock()
		_ = ch.Close()
		return nil, errChannelDropped(name)
	}
	if !m.publishChannel(name, ch) {
		// Close landed while this attach was in flight. Handing back a channel
		// that nothing will ever supervise would be a lie, so reap it.
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if m.isClosed() || ch.isClosed() {
		// Close landed between publishing and announcing: nothing will supervise
		// this channel, so consumers must not hear about it either.
		// The channel it replaced was announced, so its consumer still needs the
		// matching Detached: Close's own pairing sees the replacement, not it.
		if stale != nil {
			m.detachEvent(name, StateDisconnected)
		}
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if ch.isLost() {
		// The link died between the pre-publish validation and here. There is
		// nothing usable left to announce: retire the replacement and report a
		// retryable drop, exactly as the pre-publish check does.
		m.clearChannel(name)
		if stale != nil {
			m.detachEvent(name, StateDisconnected)
		} else {
			m.stateEvent(name, StateDisconnected)
		}
		lock.Unlock()
		_ = ch.Close()
		return nil, errChannelDropped(name)
	}
	// Announce under the lock, as reconnectOnce does. That is what orders the
	// retired channel's Detached before this Attached, and it keeps a concurrent
	// Ensure or supervisor from interleaving its own transitions with them.
	if stale != nil {
		m.detachEvent(name, StateReconnecting)
	}
	m.attachEvent(name, ch, StateAttached)
	m.startSupervise(host, ch, lock)
	if m.isClosed() || ch.isClosed() {
		// Close landed between announcing and returning. Consumers must be able to
		// drop what they just installed, so pair the Attached with a Detached while
		// the lock still orders the two.
		m.detachEvent(name, StateDisconnected)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if ch.isLost() {
		// The link died between validating and returning. The supervisor this call
		// just started owns the reconnect and will pair the Attached with a
		// Detached; the caller must not be handed a dead channel as a success.
		lock.Unlock()
		return nil, errChannelDropped(name)
	}
	lock.Unlock()
	return ch, nil
}

// channelUsable classifies a channel's fitness to hand to a caller: nil when it
// is usable, ErrManagerClosed when the manager itself has closed (terminal), and
// a retryable error when the link died under us. A lost channel is not a closed
// manager: conflating the two reported a recoverable link drop as terminal.
func (m *Manager) channelUsable(name string, ch *Channel) error {
	if m.isClosed() {
		return ErrManagerClosed
	}
	if ch.isClosed() || ch.isLost() {
		return errChannelDropped(name)
	}
	return nil
}

// errChannelDropped reports a channel that died before it could be used. It is
// deliberately not ErrManagerClosed: the manager is healthy and the channel's
// supervisor is already reconnecting, so the caller should retry rather than
// treat the host as finished.
func errChannelDropped(name string) error {
	return fmt.Errorf("%w: host %q link dropped before the channel was usable", ErrSSHStart, name)
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
	type entry struct {
		name string
		ch   *Channel
	}
	chans := make([]entry, 0, len(m.chans))
	for name, ch := range m.chans {
		chans = append(chans, entry{name: name, ch: ch})
	}
	m.mu.Unlock()

	var first error
	for _, e := range chans {
		// Serialize with Ensure and this host's supervisor: both hold the host
		// lock while they inspect or publish its channel, so a channel cannot be
		// announced as usable after its teardown has begun.
		lock := m.hostLock(e.name)
		lock.Lock()
		// Pair the Attached a consumer saw with a Detached, under the same lock
		// that ordered them: the supervisor returns on the canceled base context
		// without emitting one, so shutdown is where that channel's source is
		// dropped. claimAnnounced makes this a no-op for a channel whose Detached
		// was already emitted (a replacement retired as Close raced it).
		if m.claimAnnounced(e.name, e.ch) {
			m.detachEvent(e.name, StateDisconnected)
		}
		err := e.ch.Close()
		lock.Unlock()
		if err != nil && first == nil {
			first = err
		}
	}
	// Wait for the supervisors to quiesce. baseCtx is already canceled, so each
	// one ends after at most one state transition; letting Close return first
	// would surface lifecycle events after Close, which consumers treat as
	// terminal. OnEvent runs synchronously, so any event a supervisor still emits
	// is delivered before this returns.
	m.supervisorsWG.Wait()
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
	ch := &Channel{
		host:      host,
		facts:     facts,
		stdio:     stdio,
		transport: transport,
		lost:      make(chan struct{}),
		done:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	// A short read (EOF/error) is the link-down edge. Wire it before the read
	// loop starts so no early drop is missed. The client reads through a wrapper
	// that reports codec-level receive errors too, because the byte-level monitor
	// cannot see a frame the codec rejects after a successful read.
	monitor.onErr = ch.markLost
	client := appwire.NewClient(&lossWatchingTransport{Transport: transport, onErr: ch.markLost})
	ch.client = client
	// The client read loop must outlive this call's ctx; it ends when the
	// transport is closed (Channel.Close or the link dying).
	client.Start(m.baseCtx)

	// Bound the handshake by initTimeout, never by the caller's or the attempt's
	// deadline: both Ensure and reconnectOnce hand attach a context that always
	// carries the attempt limit (default 70s), so the old deadline check meant a
	// hung Initialize ran to that limit instead of stopping at initTimeout
	// (default 30s). WithTimeout keeps whichever deadline is earlier.
	initCtx, cancel := context.WithTimeout(ctx, m.opts.initTimeout())
	defer cancel()

	if _, err := client.Initialize(initCtx, appwire.InitializeParams{
		ProtocolVersion: appwire.ProtocolVersion,
		ClientInfo:      appwire.ClientInfo{Name: m.opts.clientName(), Version: m.opts.clientVersion()},
	}); err != nil {
		waitErr := reapBridge(transport, stdio)
		if m.baseCtx.Err() != nil {
			// Close landed while the handshake was in flight. The canceled base
			// context, not a transport fault, is why this channel is unusable.
			return nil, ErrManagerClosed
		}
		if _, isMismatch := errors.AsType[appwire.ProtocolVersionMismatchError](err); isMismatch {
			return nil, fmt.Errorf("%w: host %q: %w", ErrProtocolIncompatible, host.Name, err)
		}
		// ssh forwards the remote command's stderr onto the same sink as its own
		// diagnostics, so the marker alone cannot separate "ssh refused the key"
		// from "the remote program printed these words". Like the one-shot
		// preflight path, require ssh's own exit status: it is in hand once the
		// bridge has been reaped.
		if isAuthFailure(sink.tail()) && sshOwnFailure(waitErr) {
			return nil, fmt.Errorf("%w: host %q: %w: %s", ErrSSHAuth, host.Name, err, sink.tail())
		}
		return nil, fmt.Errorf("%w: host %q initialize: %w: %s", ErrSSHStart, host.Name, err, sink.tail())
	}

	if m.baseCtx.Err() != nil {
		_ = reapBridge(transport, stdio)
		return nil, ErrManagerClosed
	}

	go func() {
		_ = stdio.Wait()
		close(ch.stopped)
		ch.markLost()
	}()
	return ch, nil
}

// reapBridge tears down a bridge that never became a channel, and returns the
// child's own wait error so the caller can read ssh's exit status. The
// synchronous Wait is load-bearing: os/exec copies the child's stderr into the
// diagnostic sink from its own goroutine and returns from Wait only once that
// copy has finished, so a failure cannot be classified off the sink before the
// child is reaped. It also closes the stream, which releases the pipes the child
// held.
func reapBridge(transport *appwire.StreamTransport, stdio Stdio) error {
	if transport != nil {
		_ = transport.Close()
	}
	_ = stdio.Kill()
	return stdio.Wait()
}

// lossWatchingTransport reports every receive error as the channel's link-down
// edge. linkMonitor only sees read-level failures, so a frame the codec rejects
// after a successful read — malformed JSON, a bad length, an overflow — would
// otherwise leave the manager holding a channel whose reader is gone. It does not
// forward appwire.Pinger, deliberately: keepalive stays ssh's own ServerAlive*.
type lossWatchingTransport struct {
	appwire.Transport
	onErr func()
}

func (t *lossWatchingTransport) Recv(ctx context.Context) (appwire.Message, error) {
	msg, err := t.Transport.Recv(ctx)
	if err != nil {
		t.onErr()
	}
	return msg, err
}

// supervise waits for host's channel to drop, then reconnects with bounded
// exponential backoff. It never issues a hub-start command: re-attach is a
// fresh bridge/client against the already-running host hub.
//
// It holds the host lock only around state inspection, channel teardown, and
// one attach attempt. The backoff sleep happens with the lock released, so a
// concurrent Ensure never waits behind a delay that can reach BackoffMax.
func (m *Manager) supervise(ctx context.Context, host hostreg.Host, ch *Channel, lock *sync.Mutex) {
	select {
	case <-ch.lost:
	case <-ctx.Done():
		return
	}

	lock.Lock()
	// Ownership, not liveness, decides whether this supervisor still has work: the
	// manager closing ends it, and a replaced channel means the replacement's
	// supervisor owns the host now. A channel that was closed while still mapped is
	// still ours to reconnect — Ensure may have retired a dropped one, and the host
	// must not be left unsupervised because of that.
	if ctx.Err() != nil || m.currentChannel(host.Name) != ch {
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
		if err := m.opts.waitSleep(ctx, m.jitterFor(delay)); err != nil {
			// Every transition is emitted under the lock, so a concurrent Ensure
			// cannot interleave its Preflighting with this Disconnected.
			lock.Lock()
			m.stateEvent(host.Name, StateDisconnected)
			lock.Unlock()
			return
		}
		if !m.reconnectOnce(ctx, host, lock) {
			return
		}
		delay = nextBackoff(delay, m.opts.backoffMax())
	}
}

// startSupervise launches the reconnect loop that owns host's channel. The
// supervisor gets its own context, derived from baseCtx, so a terminal failure
// can end it even while it waits out a backoff.
func (m *Manager) startSupervise(host hostreg.Host, ch *Channel, lock *sync.Mutex) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.mu.Lock()
	m.supervisors[host.Name] = cancel
	m.mu.Unlock()
	// WaitGroup.Go adds, runs, and marks the loop done, so Close waiting on the
	// group observes the fully-finished loop.
	m.supervisorsWG.Go(func() {
		// Release this loop's context child when it ends. startSupervise replaces
		// the map entry on every reconnect, so a replaced loop's WithCancel child
		// would otherwise stay registered with baseCtx until Close — a leak that
		// grows by one with each reconnect.
		defer cancel()
		m.supervise(ctx, host, ch, lock)
	})
}

// stopSupervisor ends host's reconnect loop, if one is running. Without it a
// terminal failure announced by Ensure would leave the supervisor that retired
// its own channel still looping, to repeat the same terminal outcome — a second
// EventFailed for a host consumers were already told was finished — or, for a
// terminal class the next attempt happens to pass, to announce an Attached after
// that Failed.
func (m *Manager) stopSupervisor(name string) {
	m.mu.Lock()
	cancel := m.supervisors[name]
	delete(m.supervisors, name)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// reconnectOnce runs one re-attach attempt with the host lock held, and reports
// whether another attempt is worth making. Every outcome that ends the
// supervisor emits its own state event first.
func (m *Manager) reconnectOnce(ctx context.Context, host hostreg.Host, lock *sync.Mutex) bool {
	lock.Lock()
	defer lock.Unlock()

	if ctx.Err() != nil {
		m.stateEvent(host.Name, StateDisconnected)
		return false
	}
	if m.currentChannel(host.Name) != nil {
		// Any mapped channel means another supervisor owns this host: one whose link
		// has dropped is still owned, because its own supervisor is reconnecting.
		// Keying on liveness here let two supervisors attach for one host, and the
		// loser's channel was clobbered without ever being detached.
		return false
	}
	attemptCtx, cancel := context.WithTimeout(ctx, m.opts.attemptLimit())
	defer cancel()
	nch, err := m.ensureOnce(attemptCtx, host)
	if err == nil {
		if nch.isLost() || nch.isClosed() {
			// The link died between the handshake and this publish. Announcing it
			// would install a dead channel and leak the old registration, so reap
			// it and take another turn through the backoff.
			_ = nch.Close()
			m.stateEvent(host.Name, StateReconnecting)
			return true
		}
		if !m.publishChannel(host.Name, nch) {
			// Close landed mid-attempt; reap the channel it would have orphaned.
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		m.attachEvent(host.Name, nch, StateAttached)
		m.startSupervise(host, nch, lock)
		return false
	}
	if isTerminal(err) {
		// Close canceling the attempt is not a host failure: during shutdown a
		// consumer must not receive an EventFailed for it.
		if !errors.Is(err, ErrManagerClosed) {
			m.failedEvent(host.Name, err)
		}
		m.stateEvent(host.Name, StateDisconnected)
		m.stopSupervisor(host.Name)
		return false
	}
	m.stateEvent(host.Name, StateReconnecting)
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
		errors.Is(err, ErrPreflightDecode),
		errors.Is(err, ErrManagerClosed):
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

func (m *Manager) attachEvent(name string, ch *Channel, s State) {
	// Record the outstanding Attached before the callback runs: Close has to see
	// it as soon as a consumer does, so it can pair the events.
	m.mu.Lock()
	m.announced[name] = ch
	m.mu.Unlock()
	m.emit(Event{Host: name, Kind: EventAttached, State: s})
}

func (m *Manager) detachEvent(name string, s State) {
	m.mu.Lock()
	delete(m.announced, name)
	m.mu.Unlock()
	m.emit(Event{Host: name, Kind: EventDetached, State: s})
}

// claimAnnounced clears and reports whether ch is the channel whose Attached has
// no matching Detached yet. Close uses it so a channel it tears down after its
// Detached was already emitted (a replacement retired as Close raced it) is not
// announced as detached twice.
func (m *Manager) claimAnnounced(name string, ch *Channel) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.announced[name] != ch {
		return false
	}
	delete(m.announced, name)
	return true
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

// Host returns the registry entry this channel was built from. The value owns
// its slice, mirroring Preflight and hostreg's own copies.
func (c *Channel) Host() hostreg.Host {
	h := c.host
	h.Roots = slices.Clone(c.host.Roots)
	return h
}

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
	// These pipes are closed from two directions — Channel.Close and the child
	// exiting on its own — so an already-closed pipe is expected, not a teardown
	// failure worth reporting to Manager.Close's caller.
	if errIn != nil && !errors.Is(errIn, os.ErrClosed) {
		return errIn
	}
	if errOut != nil && !errors.Is(errOut, os.ErrClosed) {
		return errOut
	}
	return nil
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
