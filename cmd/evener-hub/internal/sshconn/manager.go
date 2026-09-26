package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"slices"
	"strings"
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
	StateDeploying    State = "deploying"
	StateRestarting   State = "restarting"
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
	// Stderr is the ssh diagnostic sink; nil uses os.Stderr. Every attach's ssh
	// child copies its stderr on its own goroutine, and each attach builds its own
	// diagSink over this one writer, so the manager serializes the copies: a
	// caller may pass a writer that is not safe for concurrent use.
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

	// BuildBinary cross-compiles the controller's own tree for goos/goarch into
	// the absolute path out, stamping the same buildinfo ldflags this process
	// carries. nil uses the production localBuild (a real `go build`); tests
	// inject a fake so no build runs.
	BuildBinary func(ctx context.Context, goos, goarch, out string) error

	// BuildSource is the filesystem path of the evener checkout the production
	// builder cross-compiles from. It must be explicit: an installed hub cannot
	// locate its own source reliably, and guessing — walking the working
	// directory or runtime.Caller frames — can silently build an unrelated or
	// ancestor checkout that declares the same module path. Empty means no
	// source is configured and the production builder fails with a clear error
	// instead of deploying whatever tree happens to be nearby. Ignored when
	// BuildBinary is set.
	BuildSource string

	// DeployHelp is the remedy clause the refusals that have nothing to install
	// append: the missing-executable preflight refusal and the installer fallbacks.
	// An embedder with its own CLI fills in the flags an operator must set; sshconn
	// is a library and must not learn those names, so they arrive through this field
	// rather than being written here. Empty falls back to the library's own sentence
	// ("set Options.BuildSource"), which is the right text for an embedder that has
	// no flags to name and keeps the refusal byte-for-byte what it was.
	DeployHelp string

	// HubAddr is the host hub's loopback listen address, used as this host's
	// --addr for the bridge and by the restart path to find the old pid and probe
	// /api/health. Both read the same resolution (hostAddrFor), so they cannot
	// address different ports. It must be a loopback or wildcard host:port. A
	// host entry may override it with its own Addr. When neither is set, this
	// Manager assumes the hub's documented default, 127.0.0.1:9180, and leaves
	// --addr off the bridge so the host resolves its own hub.toml; a host that
	// names its hub.toml via ConfigPath must therefore also carry an explicit
	// address (ErrHostAddr), because the controller cannot read the file to learn
	// the port the bridge will actually dial.
	HubAddr string

	// Test seams.
	sleep                     func(context.Context, time.Duration) error
	jitter                    func(time.Duration) time.Duration
	initializeTimeout         time.Duration
	attemptTimeout            time.Duration
	deployTimeout             time.Duration
	controllerVersionOverride string
	// afterPublish, when set, runs under the host lock immediately after a
	// successful publishChannel and before the post-publish validation, on both
	// the Ensure and the reconnect paths. Tests use it to drop the just-published
	// channel deterministically.
	afterPublish func(name string, ch *Channel)
	// beforeHostGate, when set, runs at the top of Ensure, before the caller waits
	// for the host gate. Tests use it to establish, without a sleep, that one
	// contender has reached a gate the test holds before another one does.
	beforeHostGate func(name string)
	// beforeUpdateHostSwap, when set, runs inside UpdateHost's gate hold after the
	// retired channel's teardown and before the registry Update. That is exactly
	// the window a gate-free reader would see: the retired channel is already
	// unmapped while the registry still holds the pre-edit entry. Tests use it to
	// drive a directly driven registry insert into the teardown/Update window, so
	// an update can be observed swapping an entry it did not capture, and to
	// assert the state a gate-free row build would observe in that window.
	beforeUpdateHostSwap func(name string)
	// AfterUpdateHostSwap, when set, runs inside UpdateHost's gate hold after a
	// successful registry swap and after the onRetire hook, immediately before the
	// gate is released. That is the window a caller's next read sits in once the
	// swap is committed: the registry already holds the new entry and the retired
	// identity is retired, but no concurrent attach can have started, because the
	// gate this call still holds is the one Ensure and a supervisor contend for.
	// Tests use it to interleave a directly driven registry change — the only
	// writer that can race the gate-free finish-phase reread — into the window
	// between a committed swap and the caller's next read, without a sleep. It is
	// never run on a refusal: there is no committed swap to observe then. Unlike
	// the seams above it is exported, because the hub package's own tests (which
	// drive this Manager through Options) must install it just as they install
	// Runner and OnEvent.
	AfterUpdateHostSwap func(name string)
	// beforeSuperviseGate, when set, runs in a host's supervisor after it observes
	// the link drop and before it contends for the host gate. Tests use it to park
	// a dropped channel's supervisor, so a concurrent Ensure's retire-and-attach
	// interleaving is exact rather than a race the sleep had to bias.
	beforeSuperviseGate func(name string, ch *Channel)
	// superviseExited, when set, runs after a supervisor goroutine has returned
	// and released any gate it held. Tests use it to observe "the supervisor
	// stood down" instead of sleeping long enough to hope it did.
	superviseExited func(name string)
	// afterChildExit, when set, runs in the child-wait goroutine once the exited
	// SSH child's exit edge has been published. Tests use it to inspect, without
	// a sleep or a scheduling race, what a consumer could observe at the moment
	// the reaping is announced: a channel whose process has exited must never be
	// reported live by ClientIfAttached/installedLiveChannel.
	afterChildExit func(name string, ch *Channel)
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

// controllerVersion is the build the controller expects the host to run: the
// in-process buildinfo.Version() unless a test overrides it. Version auto-match
// compares this with the host's launch-check version.
func (o Options) controllerVersion() string {
	if o.controllerVersionOverride != "" {
		return o.controllerVersionOverride
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

// attemptLimit bounds the preflight phase of one ensureOnce attempt. Without it
// an attempt inherits a context that lives until Close, so a single remote
// command that never returns would hold the host's lock indefinitely and stall
// every later reconnect and Ensure for that host. The default covers four
// preflight round trips. It deliberately does NOT bound the deploy/restart
// phase, which has its own, longer deployLimit: a cold cross-compile can take
// minutes and must not be killed by a budget tuned to preflight.
func (o Options) attemptLimit() time.Duration {
	if o.attemptTimeout > 0 {
		return o.attemptTimeout
	}
	return saturatingAdd(saturatingMul(o.connectTimeout(), 4), o.initTimeout())
}

// maxDuration is the largest representable time.Duration. The exported duration
// options can be set arbitrarily close to it, so the arithmetic that derives an
// attempt limit or a reconnect delay saturates instead of wrapping: a wrapped
// duration is negative, and a negative timeout or delay fires immediately —
// a tight reconnect loop rather than the bounded wait the option asked for.
const maxDuration = time.Duration(math.MaxInt64)

// saturatingAdd returns a+b, clamped to maxDuration when the sum would
// overflow. a is non-negative; b must be as well.
func saturatingAdd(a, b time.Duration) time.Duration {
	if b > 0 && a > maxDuration-b {
		return maxDuration
	}
	return a + b
}

// saturatingMul returns a*factor, clamped to maxDuration when the product would
// overflow. a and factor are non-negative.
func saturatingMul(a time.Duration, factor int64) time.Duration {
	if a <= 0 || factor <= 0 {
		return 0
	}
	if a > maxDuration/time.Duration(factor) {
		return maxDuration
	}
	return a * time.Duration(factor)
}

// deployLimit bounds the deploy and restart phases of one ensureOnce. They are
// slower than preflight by nature (a cold `go build -a` cross-compile, the
// restart stop/health waits), so they get their own budget rather than sharing
// attemptLimit.
func (o Options) deployLimit() time.Duration {
	if o.deployTimeout > 0 {
		return o.deployTimeout
	}
	return 10 * time.Minute
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
	// diagWriter serializes ssh diagnostics from every host onto one sink. Each
	// attach builds its own diagSink over Options.Stderr, and os/exec copies each
	// child's stderr on its own goroutine, so without a shared lock two hosts'
	// copies would write to a caller-supplied writer concurrently — a writer like
	// bytes.Buffer is not safe for that.
	diagWriter *syncWriter

	baseCtx context.Context
	cancel  context.CancelFunc

	mu     sync.Mutex
	closed bool
	// closeDone is closed by the first Close once every teardown wait is done. A
	// later concurrent Close blocks on it instead of returning while the first is
	// still closing channels and waiting on ensureWG/supervisorsWG, so "Close
	// returned" means no lifecycle event remains to be delivered for every caller,
	// not just the first.
	closeDone chan struct{}
	closeErr  error
	// supervisors holds, per host, the set of live reconnect loops. A replacement
	// attach registers a new loop without discarding the previous one, and a
	// terminal failure cancels every loop for the host — including one parked in a
	// backoff that a replacement's attach would otherwise have orphaned (a second
	// EventFailed, or an Attached after that Failed, for a host already announced
	// finished).
	supervisors map[string]map[*supervisorLoop]struct{}
	// supervisorsWG counts the live supervise goroutines (each reconnect attempt
	// runs inside one), so Close can wait for them to quiesce before it returns.
	supervisorsWG sync.WaitGroup
	// ensureWG counts in-flight Ensure callers. They are caller goroutines, not
	// supervisors, so Close cannot learn about them from the channel snapshot or
	// supervisorsWG; without this accounting an initial Ensure for a host with no
	// mapped channel would emit its terminal/error events after Close returned.
	ensureWG sync.WaitGroup
	// announced records, per host, the channel whose Attached has no matching
	// Detached yet. Close reads it to pair the events for the channel it tears
	// down without emitting a Detached for one already paired.
	announced map[string]*Channel
	locks     map[string]*hostLockEntry
	chans     map[string]*Channel
	// devDeployed records hosts this Manager has installed its own
	// identity-less build on — "dev", or a dirty "<sha>-dirty" build whose
	// version cannot prove the code matches (see isUnverifiableVersion) — so the
	// deploy happens at most once per process rather than on every reconnect.
	devDeployed map[string]bool
	// resolvedTargets records, per host, the executable path this Manager
	// resolved for the host — a deploy target, or a discovered install — so it
	// can be reused when the registry's evener_path is empty. Without it a host
	// whose binary is at the installer default ~/.local/bin/evener (off the
	// non-interactive PATH) was re-deployed on every reconnect, interrupting the
	// host's sessions each time.
	resolvedTargets map[string]string
	// pendingRestarts records, per host, the restart command (a supervisor's
	// restart, or a recorded bootstrap start) recorded before it ran, with the
	// identity of the hub it was meant to replace. A restart that left no
	// listener, or that left the old process serving, leaves this set, and the
	// next Ensure retries it.
	pendingRestarts map[string]pendingRestartState
}

// hubIdentity identifies a running hub from its /api/health body: the version it
// reports and, when the body carries one, its process start time. Version alone
// cannot tell two builds apart when both are "dev" — an unstamped controller and
// an unstamped host — so the start time is what proves a restart produced a new
// process rather than leaving the old one serving.
type hubIdentity struct {
	version   string
	startedAt time.Time
}

// sameProcessAs reports whether other is the same hub process as h, judged by
// start time. A zero start time on either side proves nothing and is never read
// as a match, so a health body without started_at falls back to the version
// comparison its caller already makes.
func (h hubIdentity) sameProcessAs(other hubIdentity) bool {
	return !h.startedAt.IsZero() && h.startedAt.Equal(other.startedAt)
}

// differentProcessFrom reports whether h is PROVABLY a different process from
// other: both start times must be known, and must differ. It is the strict
// counterpart of sameProcessAs for the one caller that must decide whether a
// recorded restart actually produced a replacement. sameProcessAs is false both
// when the processes differ and when either identity is unknown (a health body
// without started_at), so using it to settle a pending restart would let an old
// hub with the expected version and no start timestamp clear the marker and be
// attached to. An unknown identity on either side proves nothing and is treated
// as unresolved: the restart stays pending and is retried.
func (h hubIdentity) differentProcessFrom(other hubIdentity) bool {
	return !h.startedAt.IsZero() && !other.startedAt.IsZero() && !h.startedAt.Equal(other.startedAt)
}

// pendingRestartState is a restart command recorded before it ran, together with
// the identity of the hub process it replaces, so a later Ensure can tell whether
// the restart actually produced a new process instead of trusting a version that
// may not be unique.
type pendingRestartState struct {
	command  string
	replaced hubIdentity
	// start records that this command STARTS a hub where none was serving (the
	// bootstrap path, which refuses to run while a listener holds the address)
	// rather than replacing a process. A recovered start has no predecessor to
	// exclude, so its health verification accepts the expected version alone; a
	// recovered replacement must still prove it replaced the process it recorded.
	start bool
}

// New builds a Manager over reg's validated hosts. opts.Runner defaults to the
// production execRunner, so a test must inject a fake to stay off ssh.
func New(reg *hostreg.Registry, opts Options) *Manager {
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Manager{
		reg:             reg,
		opts:            opts,
		runner:          opts.runner(),
		diagWriter:      newSyncWriter(opts.stderr()),
		baseCtx:         baseCtx,
		cancel:          cancel,
		closeDone:       make(chan struct{}),
		locks:           map[string]*hostLockEntry{},
		chans:           map[string]*Channel{},
		devDeployed:     map[string]bool{},
		resolvedTargets: map[string]string{},
		pendingRestarts: map[string]pendingRestartState{},
		supervisors:     map[string]map[*supervisorLoop]struct{}{},
		announced:       map[string]*Channel{},
	}
}

// Registry returns the registry this manager dials and mutates through: the
// same instance Ensure validates against and AddHost and RemoveHost commit to.
// The hub's host surfaces share it — hostRegistryFromConfig prefers it when a
// caller threads the manager without a separate RemoteHostRegistry — so a
// runtime Add is visible to every reader and an added host is Ensure-able,
// instead of the surfaces splitting between the manager's registry and a
// fresh copy built from the configured entries.
func (m *Manager) Registry() *hostreg.Registry { return m.reg }

// Ensure returns a connected, initialized channel for host, preflighting and
// attaching as needed. It is idempotent while attached: repeated calls return
// the same *Channel.
//
// After a successful attach, Ensure starts a supervisor that watches the link
// and reconnects (fresh ssh bridge + client, never a hub start) with bounded
// exponential backoff and jitter until Manager.Close. ctx bounds this call and
// the handshake, not the channel's lifetime.
func (m *Manager) Ensure(ctx context.Context, name string) (*Channel, error) {
	if !m.beginEnsure() {
		return nil, ErrManagerClosed
	}
	defer m.ensureWG.Done()
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

	if m.opts.beforeHostGate != nil {
		m.opts.beforeHostGate(name)
	}
	lock := m.hostLock(name)
	// The acquisition is paired here, on the caller's own reference: every
	// return path below releases it, including the ones that hand the gate to
	// a supervisor — startSupervise takes that goroutine's own reference
	// before it exists, so this release never drops an entry a live supervisor
	// still gates on.
	defer m.releaseHostLock(name)
	// Honor the caller's context while waiting for the host gate: a canceled
	// caller must not park behind another Ensure's or a supervisor's long
	// preflight/attach, and must not be handed a channel afterwards.
	if err := lockHostCtx(ctx, lock); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		lock.Unlock()
		return nil, err
	}
	// The entry can have been deregistered — or replaced — between the lookup
	// above and this gate: RemoveHost drops the registry entry while holding
	// this same lock, and a remove/re-add of the name swaps in a new entry,
	// so rechecking the name alone would pass while the re-added name is
	// live. Compare the full entry — and its generation: content equality
	// cannot tell a remove/re-add of a byte-identical entry apart, while the
	// registry's insert generation always advances across the cycle. This
	// attach captured one identity, and a name that now resolves to a
	// different entry must be refused before it dials — a channel built from
	// the removed entry would be published under the re-added name. The
	// re-add's own caller attaches the fresh identity.
	if !m.reg.SameRegistration(name, host) {
		lock.Unlock()
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}

	// The mapped channel has to belong to the registration this attach just
	// validated, not merely be mapped under the name: a remove/re-add that
	// swapped the entry without a teardown (a caller writing the registry
	// directly, which is how the hub's own registry behaves with no manager
	// wired) leaves the removed identity's channel under the name, and handing
	// it back would give the re-added host a live channel to the removed
	// address. A channel from another registration falls through to the attach
	// below, which retires it on the way out.
	if ch := m.liveChannel(name); ch != nil && ch.host.Generation == host.Generation {
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
	// ensureOnce bounds each of its phases itself (preflight, deploy/restart, then
	// the attach handshake), so no outer attemptLimit is applied here; tie the
	// attempt to the manager's lifetime instead, so an in-flight attempt cannot
	// outlive Manager.Close with its ssh child alive.
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(m.baseCtx, cancel)
	defer stopClose()
	ch, err := m.ensureOnce(attemptCtx, host, true)
	if err != nil {
		// Close canceling the attempt surfaces a preflight that was in flight as a
		// retryable transport failure; the canceled base context, not the host, is
		// why. Match the handshake path and report the closed manager rather than a
		// retryable error a supervisor would keep chasing. A terminal cause found
		// concurrently is kept, because it names what actually went wrong.
		if m.baseCtx.Err() != nil && !isTerminal(err) {
			err = ErrManagerClosed
		}
		// Shutdown began while this attach was in flight. Close owns the last
		// events for the channels it tears down, and this caller goroutine is only
		// quiesced by ensureWG (see beginEnsure), so emitting here would surface a
		// Disconnected or a Failed for a host nothing will ever announce.
		if m.baseCtx.Err() != nil {
			if isTerminal(err) {
				m.clearChannel(name)
				m.stopSupervisor(name)
			}
			lock.Unlock()
			return nil, err
		}
		// The caller gave up while the attempt was in flight. The transport-shaped
		// failure its cancellation produced is not this host's problem, and the
		// host's state is not this caller's to announce: report the caller's own
		// error and emit nothing, exactly as the already-canceled path above does.
		// A terminal cause found concurrently is kept, because it names what
		// actually went wrong.
		if cerr := ctx.Err(); cerr != nil && !isTerminal(err) {
			lock.Unlock()
			return nil, cerr
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
	// Recheck the registry under the lock before publishing, on the full
	// entry and its generation: a deregistration that does not pass through
	// this gate (a caller dropping the registry entry directly, as the hub's
	// host manager does when no sshconn manager is wired) must not gain a
	// fresh channel for a host that is gone, and a remove/re-add that swapped
	// the entry mid-attach must not have the channel built from the removed
	// entry published under the re-added name — byte-identical re-adds
	// included, because the registry assigns the re-add a new generation. The
	// replacement is reaped and the dropped predecessor keeps ownership,
	// exactly as a replacement that died in its handshake does.
	if !m.reg.SameRegistration(name, host) {
		lock.Unlock()
		_ = ch.Close()
		return nil, fmt.Errorf("%w: %q", ErrHostNotFound, name)
	}
	if !m.publishChannel(name, ch, host) {
		// Close landed while this attach was in flight. Handing back a channel
		// that nothing will ever supervise would be a lie, so reap it.
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if m.opts.afterPublish != nil {
		m.opts.afterPublish(name, ch)
	}
	if m.isClosed() || ch.isClosed() {
		// Close landed between publishing and announcing: nothing will supervise
		// this channel, so consumers must not hear about it either.
		// The channel it replaced was announced, so its consumer still needs the
		// matching Detached: Close's own pairing sees the replacement, not it.
		if stale != nil {
			m.detachEvent(name, StateDisconnected)
		}
		// The discarded replacement must give the map slot back: leaving it mapped
		// would keep a closed channel where a consumer treats Close as terminal.
		m.clearChannel(name)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
	if ch.isLost() {
		// The link died between the pre-publish validation and here.
		err := m.lostAfterPublish(name, stale)
		lock.Unlock()
		_ = ch.Close()
		return nil, err
	}
	// Announce under the lock, as reconnectOnce does. That is what orders the
	// retired channel's Detached before this Attached, and it keeps a concurrent
	// Ensure or supervisor from interleaving its own transitions with them.
	if stale != nil {
		m.detachEvent(name, StateReconnecting)
	}
	m.attachEvent(name, ch, StateAttached)
	if !m.startSupervise(host, ch, lock) {
		// Close set its flag between the publish and here, so no supervisor will
		// ever own this channel. Pair the Attached just announced so a consumer can
		// drop the source it installed, and reap the channel rather than leave a
		// closed entry mapped.
		m.detachEvent(name, StateDisconnected)
		m.clearChannel(name)
		lock.Unlock()
		_ = ch.Close()
		return nil, ErrManagerClosed
	}
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
	if err := ctx.Err(); err != nil {
		// The caller gave up while the attach was finishing — during the
		// EventAttached callback or the final validation. The channel is
		// manager-owned and already supervised, so it is not torn down: report the
		// cancellation rather than hand a channel to a caller that cannot use it.
		lock.Unlock()
		return nil, err
	}
	lock.Unlock()
	return ch, nil
}

// ClientIfAttached returns name's current initialized client ONLY while a live,
// not-closed channel is installed, and reports false otherwise. It is the
// non-dialing lookup a background caller (component 06's fleet snapshot) and the
// notification broker's reconnect rebind (component 05) use in place of Ensure:
// it never spawns ssh, preflights, deploys, or attaches, so it can neither
// eagerly attach a dormant host nor re-dial one that dropped between a check and
// the call.
//
// Liveness is decided by the same predicate Ensure and Attached use
// (liveChannel: present, not closed, not lost), read together with the manager's
// open state so a client is never offered once shutdown has begun: the rebind
// runs from a lifecycle callback, including Close's own Detached, and a channel
// Close is tearing down must not be handed out. It deliberately does NOT take the
// per-host gate:
// EventAttached is delivered under that lock (see Options.OnEvent) and the
// rebind that consumes this lookup runs from that callback, so a re-entrant
// acquisition would deadlock. The installed channel is read under the manager
// mutex, which is the lock that publishes and clears it.
func (m *Manager) ClientIfAttached(name string) (*appwire.Client, bool) {
	// An unknown host is not attached by definition; attachedChannel reports it
	// as such instead of inventing the ErrHostNotFound Ensure would.
	ch, ok := m.attachedChannel(name)
	if !ok {
		return nil, false
	}
	return ch.Client(), true
}

// installedLiveChannel returns name's channel only while the manager is still
// open and the channel is usable, reading m.closed and the map under one mutex
// acquisition so the two can never be observed apart. ClientIfAttached is the
// rebind path a lifecycle callback uses, and Close emits its Detached while the
// channel is still mapped and not yet closed: a check that is not atomic with the
// lookup lets that callback rebind to a channel Close is tearing down.
func (m *Manager) installedLiveChannel(name string) (*Channel, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false
	}
	ch := m.chans[name]
	if ch == nil || ch.isClosed() || ch.isLost() {
		return nil, false
	}
	return ch, true
}

// ChannelIfAttached returns name's installed live channel ONLY while a live,
// not-closed channel is installed, and reports false otherwise. It is the
// atomic primitive behind the attached-only accessors, and the one production
// wiring actually uses: cmd/evener-hub/main.go backs both
// hubcore.WebConfig.RemoteHostFacts (remoteHostFactsForChannel) and
// RemoteHostHandshake (remoteHostHandshakeForChannel) with one
// ChannelIfAttached lookup each, rather than with PreflightIfAttached or
// HandshakeIfAttached. The generation guard lives at that call site —
// `ch.Client() == client` — so a caller that must not mix two connection
// generations reads the channel's client, preflight, and handshake from the
// single value this returns and refuses when its client differs, so a
// supervisor reconnect between two separate lookups cannot splice one
// generation's facts onto another's client. Like ClientIfAttached it takes the
// manager-wide mutex, not the per-host gate, and never spawns ssh, preflights,
// deploys, or attaches.
func (m *Manager) ChannelIfAttached(name string) (*Channel, bool) {
	return m.attachedChannel(name)
}

// PreflightIfAttached returns name's captured preflight facts ONLY while a
// live, not-closed channel is installed, and reports false otherwise. It is
// NOT the production backing for hubcore.WebConfig.RemoteHostFacts: that seam
// is remoteHostFactsForChannel, built on ChannelIfAttached with the channel
// generation guard (`ch.Client() == client`) at the call site. This accessor
// has no non-test caller; it is retained for tests and as a convenience for a
// caller that does not need the generation guard. Like ClientIfAttached it
// takes the manager-wide mutex, not the per-host gate, and never spawns ssh,
// preflights, deploys, or attaches.
func (m *Manager) PreflightIfAttached(name string) (Preflight, bool) {
	ch, ok := m.attachedChannel(name)
	if !ok {
		return Preflight{}, false
	}
	return ch.Preflight(), true
}

// HandshakeIfAttached returns the InitializeResponse captured when name's
// current channel attached, ONLY while a live, not-closed channel is installed,
// and reports false otherwise. It is NOT the production backing for
// hubcore.WebConfig.RemoteHostHandshake: that seam is
// remoteHostHandshakeForChannel, built on ChannelIfAttached with the channel
// generation guard (`ch.Client() == client`) at the call site. This accessor
// has no non-test caller; it is retained for tests and as a convenience for a
// caller that does not need the generation guard. appwire.Client keeps its
// Features privately, so component 05's capability probe reads
// ProtocolVersion/SourceID/Features through the channel value directly;
// HubVersion stays preflight-owned (the handshake's ServerInfo.Version is the
// hub's package constant, not its build identity). Like ClientIfAttached it
// takes the manager-wide mutex, not the per-host gate, and never dials.
func (m *Manager) HandshakeIfAttached(name string) (appwire.InitializeResponse, bool) {
	ch, ok := m.attachedChannel(name)
	if !ok {
		return appwire.InitializeResponse{}, false
	}
	return ch.Handshake(), true
}

// attachedChannel resolves name to its installed live channel, normalizing the
// name through the registry exactly as ClientIfAttached does. It is the shared
// body of the attached-only lookups so their liveness predicate cannot drift.
func (m *Manager) attachedChannel(name string) (*Channel, bool) {
	if m.reg == nil {
		return nil, false
	}
	host, ok := m.reg.Get(name)
	if !ok {
		return nil, false
	}
	return m.installedLiveChannel(host.Name)
}

// lostAfterPublish retires the channel Ensure just published when the link died
// between the publish and the caller taking it, and classifies the outcome. It
// runs with name's host lock held, before the caller closes the published
// channel.
//
// publishChannel has already taken the map slot from stale, whose supervisor is
// blocked on this lock: clearing the map without giving the slot back would leave
// that supervisor owning nothing and orphan the host until the next Ensure, so
// the slot returns to stale exactly as the pre-publish check preserves it. When
// Close landed between the checks that guard this path, the manager — not the
// host — ended the attempt: the drop is then terminal, and reporting the
// retryable one would send the caller around a loop that cannot succeed.
//
// Close owns the events for the channels it tears down, but its snapshot holds
// the replacement this call already published (or nothing) and never stale, so it
// cannot pair stale's outstanding Attached: the Detached is emitted here, exactly
// as the sibling pre-announce path at the isClosed/isClosed check does.
func (m *Manager) lostAfterPublish(name string, stale *Channel) error {
	m.clearChannel(name)
	if stale != nil {
		if !m.restoreChannel(name, stale) {
			// Close won the race between the pre-publish check and here: its
			// snapshot sees the replacement, so stale's Attached would otherwise
			// leak. Pair it now that the manager is terminal.
			m.detachEvent(name, StateDisconnected)
			return ErrManagerClosed
		}
	} else if m.isClosed() {
		return ErrManagerClosed
	} else {
		m.stateEvent(name, StateDisconnected)
	}
	return errChannelDropped(name)
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
//
// Concurrent callers all wait for the same teardown. A second Close blocks until
// the first has finished closing channels and joining ensureWG/supervisorsWG, so
// each caller can treat its own return as the point after which no lifecycle
// event will be delivered; every caller receives the same teardown error.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		<-done
		return m.closeErr
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
		// No channel may remain mapped once Close returns: a consumer that treats
		// Close as terminal must not find an entry that looks live afterwards.
		m.clearChannel(e.name)
		lock.Unlock()
		m.releaseHostLock(e.name)
		if err != nil && first == nil {
			first = err
		}
	}
	// Wait out any in-flight Ensure caller. An initial Ensure for a host with no
	// mapped channel is a caller goroutine, not a supervisor: it is absent from the
	// channel snapshot and from supervisorsWG, so only this accounting keeps its
	// terminal/error emissions from surfacing after Close returns.
	m.ensureWG.Wait()
	// Wait for the supervisors to quiesce. baseCtx is already canceled, so each
	// one ends after at most one state transition; letting Close return first
	// would surface lifecycle events after Close, which consumers treat as
	// terminal. OnEvent runs synchronously, so any event a supervisor still emits
	// is delivered before this returns.
	m.supervisorsWG.Wait()
	// Publish the outcome and release every concurrent caller at once. The write
	// happens-before the close, and each waiter's receive happens-after it, so the
	// closeErr a waiter reads is ordered without the mutex.
	m.closeErr = first
	close(m.closeDone)
	return first
}

// DetachHost removes name's host cleanly: it stops the supervisor first, then
// drops the mapped channel, so a removed-then-readded host attaches fresh. The
// name is normalized through the registry exactly like Ensure and
// ClientIfAttached, and an unknown name (or a nil registry) is a no-op
// returning nil: removal of the registry entry is what makes re-add clean, so
// there is nothing to detach for a name the registry no longer reports.
//
// The host lock serializes with an in-flight Ensure — both hold it while they
// inspect or publish the host's channel, so a channel cannot be announced as
// usable after its teardown has begun — and the supervisor is stopped before
// the channel is cleared so it cannot reconnect behind this call. The Detached
// is paired through claimAnnounced exactly like Close, so consumers see it
// only when the channel was announced. The channel close runs after the locks
// are released: Channel.Close blocks on the ssh child's exit and must never be
// held across m.mu, but the host lock spans the map mutation so no Ensure can
// interleave between the clear and the reap. This never dials, preflights, or
// attaches, and a second call for the same name is a no-op returning nil.
func (m *Manager) DetachHost(name string) error {
	if m.reg == nil {
		return nil
	}
	// Trimmed for the lock key exactly as AddHost trims and as the registry's
	// Get and Remove trim what they are given, so a padded spelling takes the
	// same host gate as its canonical name.
	name = strings.TrimSpace(name)
	host, ok := m.reg.Get(name)
	if !ok {
		return nil
	}
	// The registry is the authority on the name's spelling, and every map below
	// is keyed off host.Name: normalizing here keeps a padded spelling from
	// missing the channel Ensure published, exactly as Ensure does.
	name = host.Name
	// Serialize with Ensure and this host's supervisor, as Close does: neither
	// can inspect or publish the channel while it is being torn down.
	lock := m.hostLock(name)
	lock.Lock()
	// A remove/re-add that swapped the entry while this call waited for the
	// gate leaves the re-added host's own channel mapped under the name; the
	// teardown is scoped to the identity resolved above, so that channel — and
	// its supervisor — stay up.
	ch := m.teardownHostChannel(name, host, true)
	lock.Unlock()
	m.releaseHostLock(name)
	// Reaping the ssh child can block on its exit, which needs no lock; the map
	// no longer references this channel, so a concurrent Ensure attaching fresh
	// — the re-add path — cannot interleave with it.
	if ch != nil {
		_ = ch.Close()
	}
	return nil
}

// teardownHostChannel performs the channel half of a host teardown while the
// caller holds the host lock: it stops the supervisor — a loop parked in
// backoff would otherwise re-attach behind the teardown and a removed host
// would come back on its own — then drops the mapped channel together with
// the per-host caches, so a detached host's caches do not survive the detach
// and a re-add starts clean rather than inheriting this identity's deploy
// marker, resolved executable, or pending restart. claimAnnounced pairs the
// Attached a consumer saw with a Detached under the same lock that ordered
// them, and makes the pairing a no-op for a channel whose Detached was
// already emitted — so a Close racing the teardown cannot pair it twice. The
// returned channel is the one that was mapped, for the caller to reap after
// releasing the host lock: Channel.Close blocks on the ssh child's exit and
// must never run under the host lock, while the lock spans the map mutation
// so no Ensure can interleave between the clear and the reap. Callers hold
// the host lock.
//
// captured is the identity the caller resolved before the gate, and
// hadCaptured says whether it resolved one at all. It scopes the teardown to
// that identity: a remove/re-add that swapped the name's entry while the
// caller waited for the gate leaves the re-added host's own attach mapped
// under the name, and a channel built from a strictly newer registration is
// that host's live channel — with its supervisor and its caches — which
// nothing here was asked to touch. The caller's own identity still comes
// down, and a name with no captured identity keeps the sweep of an older
// attach's channel.
// The teardown is scoped to the identity the caller captured. A name whose
// channel or reconnect loop belongs to a strictly newer registration is the
// re-added host's own live state: reaping that channel, cancelling the loop
// reconnecting it, or clearing the caches it is using would leave a host whose
// add answered success silently offline. The captured identity's own resources
// still come down, and a name with no captured identity keeps the sweep of an
// older attach's channel.
func (m *Manager) teardownHostChannel(name string, captured hostreg.Host, hadCaptured bool) *Channel {
	newer := func(generation uint64) bool { return hadCaptured && generation > captured.Generation }
	// The loops first: one reconnecting a newer registration is that host's own
	// live state, and cancelling it would leave nobody to bring the host back.
	reconnectingNewer := m.stopSupervisorsBefore(name, newer)
	m.mu.Lock()
	ch := m.chans[name]
	if ch != nil && newer(ch.host.Generation) {
		// The re-added host's own live channel: it stays mapped, and its
		// Attached stays unpaired.
		m.mu.Unlock()
		return nil
	}
	delete(m.chans, name)
	m.mu.Unlock()
	if !reconnectingNewer {
		m.clearHostCaches(name)
	}
	if ch != nil && m.claimAnnounced(name, ch) {
		m.detachEvent(name, StateDisconnected)
	}
	return ch
}

// stopSupervisorsBefore ends every reconnect loop name still has except the
// loops reconnecting a registration newer than the caller captured, and
// reports whether any such loop was left running. Ending all of the others, not
// just the most recent, is what reaches a predecessor parked in backoff after a
// replacement attach registered over it; skipping the newer ones is what keeps
// a stale teardown from cancelling the re-added host's own reconnect.
func (m *Manager) stopSupervisorsBefore(name string, newer func(uint64) bool) bool {
	m.mu.Lock()
	loops := m.supervisors[name]
	if len(loops) == 0 {
		m.mu.Unlock()
		return false
	}
	kept := make(map[*supervisorLoop]struct{}, len(loops))
	var ended []*supervisorLoop
	for loop := range loops {
		if newer(loop.host.Generation) {
			kept[loop] = struct{}{}
			continue
		}
		ended = append(ended, loop)
	}
	if len(kept) == 0 {
		delete(m.supervisors, name)
	} else {
		m.supervisors[name] = kept
	}
	m.mu.Unlock()
	for _, loop := range ended {
		loop.cancel()
	}
	return len(kept) > 0
}

// RemoveHost deregisters name and tears down its channel as one atomic step:
// under the host lock it drops the registry entry, stops the supervisor, and
// clears the mapped channel together with the per-host caches, so a concurrent
// Ensure cannot publish a fresh channel for a host that is deregistered — its
// registry recheck under the same lock sees the entry gone and refuses.
//
// DetachHost alone cannot give the caller that guarantee: it drops the channel
// under the lock but leaves the registry entry to the caller, and the gap
// between the two is exactly where a racing Ensure dials and publishes for a
// host the caller is about to remove. The registry is the manager's own
// (New takes it), so this method owns the whole lifecycle the hub's host
// manager drives: persist first at the caller, then RemoveHost once, and
// nothing is resurrected.
//
// Like DetachHost this never dials, an unknown name is a no-op returning nil,
// and a second call for the same name is a no-op. The Detached pairing and
// the post-lock reap mirror DetachHost exactly — both run through the same
// teardownHostChannel. A nil registry is an error rather than a silent no-op:
// a removal that committed nothing must not report success — a Remove that
// answered Removed:true while the host stayed in the live registry would
// leave it listed as removable-but-never-removed, mislabeled and unremovable
// — mirroring AddHost's loud-refusal posture.
//
// The removal targets the identity the call resolved before the gate, not
// whatever holds the name when the gate is taken: a remove/re-add that swaps
// the entry while this call waits leaves the re-added host — its registry
// entry and its own channel — untouched.
func (m *Manager) RemoveHost(name string) error {
	if m.reg == nil {
		return errors.New("sshconn: RemoveHost with no registry")
	}
	// Trimmed for the lock key exactly as AddHost trims and as the registry's
	// Get and Remove trim what they are given: a padded spelling must take the
	// same host gate as its canonical name rather than mint a second gate for
	// one host that a concurrent teardown of the canonical name races.
	name = strings.TrimSpace(name)
	host, ok := m.reg.Get(name)
	if ok {
		// The registry is the authority on the name's spelling, and every map
		// below is keyed off host.Name, exactly as Ensure and DetachHost do.
		name = host.Name
	}
	lock := m.hostLock(name)
	defer m.releaseHostLock(name)
	lock.Lock()
	// Under the gate the name can hold a registration this call never captured
	// — an add that landed the name while this call waited, the mirror of the
	// remove/re-add below — so the unknown-name half is re-checked here rather
	// than trusted from the lookup above: a host whose add is about to answer
	// success is not this call's to remove, and sweeping its channel would kill
	// the attach that add's caller is about to use. A name that still holds
	// nothing keeps the no-op the doc promises.
	if !ok {
		if _, exists := m.reg.Get(name); exists {
			lock.Unlock()
			return nil
		}
	}
	// A remove/re-add can swap the name's entry in the window this call spent
	// waiting for the gate, which leaves a different registration here. The
	// identity this call was asked to remove is gone by then — the swap's own
	// removal tore it down — so its registry entry is not this call's to drop:
	// taking the replacement's would deregister the host its caller re-added,
	// behind that add's own success. The channel half below still runs, scoped
	// by the captured identity.
	if !ok || m.reg.SameRegistration(name, host) {
		// Registry entry first, under the same lock the rechecks in Ensure and
		// reconnectOnce consult: once it is gone no attach path can publish. An
		// unknown name is the no-op the doc promises — the entry is already gone,
		// but a channel an older attach published for it still comes down.
		if err := m.reg.Remove(name); err != nil && !errors.Is(err, hostreg.ErrUnknownHost) {
			lock.Unlock()
			return err
		}
	}
	ch := m.teardownHostChannel(name, host, ok)
	lock.Unlock()
	// The reap runs after the lock is released, exactly as DetachHost's does:
	// Channel.Close blocks on the ssh child's exit, and nothing references the
	// channel once the map entry is gone.
	if ch != nil {
		_ = ch.Close()
	}
	return nil
}

// AddHost registers entry in the manager's own registry under the same
// per-host gate RemoveHost and the attach paths coordinate on. The hub's host
// manager routes its re-adds through here so a remove/re-add of a name can
// never interleave with an attach for it: once an Ensure holds the gate, the
// entry it captured cannot be swapped before its pre-publish recheck, and a
// parked Ensure that takes the gate after the swap sees the new identity at
// its recheck and refuses the stale attach instead of publishing it.
//
// Validation and normalization are the registry's own (Registry.Add), so this
// inserts exactly what hostreg would; only the locking discipline is added. A
// nil registry is an error rather than a silent no-op — an add that committed
// nothing must not report success.
func (m *Manager) AddHost(entry hostreg.Host) error {
	if m.reg == nil {
		return errors.New("sshconn: AddHost with no registry")
	}
	// Trimmed for the lock key exactly as Get and Remove trim what they are
	// given, so a padded spelling takes the same gate as its canonical name —
	// and held in a name so the release below pairs this exact acquisition.
	name := strings.TrimSpace(entry.Name)
	lock := m.hostLock(name)
	defer m.releaseHostLock(name)
	lock.Lock()
	defer lock.Unlock()
	return m.reg.Add(entry)
}

// UpdateHost replaces name's registry entry with entry and tears the host's
// channel down as one atomic step, under the per-host gate. Inside the one
// hold the order is fixed: refuse an absent name, validate, then tear the
// retired channel down, then swap the registry, then release.
//
// The teardown comes BEFORE the swap because a gate-free reader must never pair
// the new entry with the retired channel. The row build behind host/list and
// host/status takes no host gate: it snapshots the registry and then resolves
// the channel by name alone (ClientIfAttached/ChannelIfAttached compare no
// registrations). If the swap landed first, such a reader could take the new
// entry inside that window and pair it with the still-mapped channel of the
// identity this call is retiring, record the retired host's handshake and facts
// under the new generation — which is above the generation fence the swap
// itself advances — and render the retired host's server facts for the new
// identity until the next successful attach. Unmapping the channel before the
// new generation becomes visible makes that pairing impossible. The reverse
// window, the old entry observed with no channel, is harmless: it can only make
// a row render offline, and the retire leaves that clean.
//
// An absent name is refused and the entry validated before the teardown because
// the caller's contract is that an error from this method means nothing live
// changed. The absence check runs first: Registry.Update is the authority on
// ErrUnknownHost and rechecks it under its own lock, but by the time it runs
// below the channel is already retired, so a name a directly driven registry
// dropped would otherwise be disconnected while still live. hostreg.ValidateEntry
// is the side-effect-free half of the same Normalize+validateEntry pass
// Registry.Update runs, so checking it here lets the teardown stay
// unconditional — a shape refusal returns before the channel is touched, and
// the swap below cannot refuse for a shape reason. Only a directly driven
// registry can still refuse the swap (the name dropped inside the gate hold
// after the capture); that path returns the error with the retired channel
// already gone, and the host reattaches on the next Ensure.
//
// Every update tears the channel down, whether or not the edit changed a field
// the dial reads. The reason is the fence, not the dial: a supervisor captures
// the entry it supervises when it starts, and reconnectOnce refuses to publish a
// replacement once SameRegistration no longer matches that capture — so a
// channel kept across an update that advanced the generation could never be
// reconnected by the supervisor that owns it, and the host would go dark on the
// next link drop with nothing to bring it back. Retiring the channel with the
// identity keeps one rule instead of a rebind path.
//
// It never dials, deploys, or attaches. An unknown name is hostreg's own
// ErrUnknownHost, and a nil registry is an error rather than a silent no-op,
// mirroring AddHost and RemoveHost: an update that committed nothing must not
// report success.
//
// onRetire, when non-nil, runs while the per-host gate is still held, in the
// same hold as the swap: after the registry already holds the new entry and the
// retired identity's teardown (its Detached included) has been emitted, and
// immediately before the gate is released. That placement is the contract, not
// an implementation detail. Every lifecycle event is delivered synchronously
// with the gate held, so a concurrent attach can only start for the new
// generation once the gate is free; a caller's per-identity state retired from
// this hook therefore cannot have a new-generation event interleave between the
// swap and the retirement and be erased by it. The hook runs only on a swap
// this call actually made: never on a refusal, and the pre-swap capture it is
// handed is guaranteed present, because an absent name is refused before
// anything live moves.
//
// The hook receives the entry the swap actually retired — the pre-swap capture,
// its Generation included — so a caller can retire per-identity state by
// generation rather than by timing: it can mark every row built from that
// identity (or any earlier one) as retired, which is what makes the retirement
// immune to a late write from a row that captured the old entry before the
// swap but only reaches its state write after the hook. The hook must not call
// back into Manager — the gate is non-reentrant — and must not take a lock
// another goroutine may hold while parked on this gate.
func (m *Manager) UpdateHost(entry hostreg.Host, onRetire func(retired hostreg.Host)) error {
	if m.reg == nil {
		return errors.New("sshconn: UpdateHost with no registry")
	}
	// Trimmed for the lock key exactly as AddHost trims, so a padded spelling
	// takes the same host gate as its canonical name.
	name := strings.TrimSpace(entry.Name)
	lock := m.hostLock(name)
	defer m.releaseHostLock(name)
	lock.Lock()
	// The identity this call retires is resolved under the gate, and so is the
	// swap: a remove/re-add that took the name while this call waited is not
	// this call's to tear down, and the teardown below is scoped to the entry
	// the update actually replaced. The capture is guaranteed present on every
	// path that reaches the teardown — an absent name is refused immediately
	// below — so hadCaptured reads as "there is an identity here to retire" for
	// teardownHostChannel's own contract.
	before, hadCaptured := m.reg.Get(name)
	// Refuse an absent name before anything live moves, in the same gate hold as
	// the swap it guards. Registry.Update is the authority on this refusal and
	// rechecks it under its own lock, but by the time it runs below the teardown
	// has already retired the channel: an update whose name a directly driven
	// registry dropped (one bypassing this manager) would disconnect a host that
	// is still live and then answer an error — exactly the "an error means
	// nothing live changed" contract the validate-first step exists to keep.
	// The message is Registry.Update's own — fmt.Errorf("%w: %q",
	// ErrUnknownHost, name) — so callers' errors.Is keeps working across the
	// moved check.
	if !hadCaptured {
		lock.Unlock()
		return fmt.Errorf("%w: %q", hostreg.ErrUnknownHost, name)
	}
	// Validate before anything live moves. The caller's contract is that an
	// error from this method means nothing live changed, so a refused update
	// must not have torn the host's channel down on its way out. ValidateEntry
	// is the side-effect-free half of the same Normalize+validateEntry pass
	// Registry.Update runs below; doing it here, ahead of the teardown, is what
	// lets the teardown stay unconditional — the swap below can then refuse only
	// for a reason independent of the entry's shape.
	if err := hostreg.ValidateEntry(entry); err != nil {
		lock.Unlock()
		return err
	}
	// The teardown targets the captured entry's own spelling. The capture is
	// guaranteed present — an absent name was refused above — so before.Name is
	// always a real host and there is no fallback to the gate key: a fallback
	// keyed to name could never run, and the empty name it once guarded against
	// (a directly driven registry inserting the name inside the capture/swap
	// window) is now refused before the teardown instead of swapped past it.
	// Tear the retired channel down BEFORE the swap becomes visible. A gate-free
	// row build resolves a name through the registry and then looks the channel
	// up by name alone, so a swap that landed first would let it pair the new
	// entry with this retired channel; unmapping first closes that window. See
	// the method comment.
	ch := m.teardownHostChannel(before.Name, before, hadCaptured)
	if m.opts.beforeUpdateHostSwap != nil {
		m.opts.beforeUpdateHostSwap(name)
	}
	if err := m.reg.Update(entry); err != nil {
		// Validation above already passed, so this refusal is not a shape
		// refusal: only a directly driven registry — one whose name was dropped
		// from under the gate — can reach it. The entry is unchanged and the
		// retired channel is already gone; the host reattaches on the next
		// Ensure, which finds no channel under the name and dials fresh. Reap
		// the channel this call did retire — the close blocks on the ssh child's
		// exit, so it happens after the gate is released, as everywhere else —
		// and report the refusal.
		lock.Unlock()
		if ch != nil {
			_ = ch.Close()
		}
		return err
	}
	// The caller's retirement runs under the gate, in the same hold as the swap,
	// so no new-identity lifecycle event can interleave before it: an attach
	// cannot acquire the gate until it is released below. It receives the entry
	// the swap replaced — the capture above, guaranteed present because an
	// absent name was refused before the teardown — so the caller retires by
	// identity rather than by timing.
	if onRetire != nil {
		onRetire(before)
	}
	// The post-swap seam runs last in the hold, so the window it exposes is
	// exactly "the swap and the caller's retirement are done, the gate is still
	// ours": a directly driven registry change made here lands after the committed
	// swap and the retirement, inside the same gate hold, and before any gate-free
	// reread the caller makes. It runs only down this path — a refusal returned
	// above — so a test can key it to a swap that actually happened.
	if m.opts.AfterUpdateHostSwap != nil {
		m.opts.AfterUpdateHostSwap(name)
	}
	lock.Unlock()
	// The reap runs after the lock is released, exactly as DetachHost's and
	// RemoveHost's do: Channel.Close blocks on the ssh child's exit.
	if ch != nil {
		_ = ch.Close()
	}
	return nil
}

// clearHostCaches drops every per-host record that must not survive a detach or
// removal: the at-most-once dev-deploy marker, the resolved executable path,
// and any pending restart. A re-added host starts clean instead of inheriting
// the previous identity's dial and deploy decisions.
func (m *Manager) clearHostCaches(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.devDeployed, name)
	delete(m.resolvedTargets, name)
	delete(m.pendingRestarts, name)
}

// ensureOnce runs one full preflight-then-decide-then-attach sequence with the
// state transitions around it. Every branch is chosen by the one decision table
// in ensureDecision; this function owns only the ordering and the recovery.
//
// The ladder guarantees three things the ad-hoc gates did not:
//   - one probe pass, with every fact recorded as known or unknown honestly;
//   - the deploy path is offered to every host before any terminal refusal, so a
//     protocol-broken or flag-less host can still be upgraded over ssh;
//   - a deploy or restart that leaves the host without a listener is retried by
//     the next Ensure, matching ErrRestart's stated contract;
//   - a deploy on a host with no hub present is a fresh install, not a restart:
//     it falls through to the explicit-attach bootstrap (below) and a reconnect
//     never turns it into a hub start.
//
// explicit marks an explicit attach request (component 06's first host action),
// which is the only place the first-attach bootstrap start may run; a reconnect
// passes false so it never starts a hub.
func (m *Manager) ensureOnce(ctx context.Context, host hostreg.Host, explicit bool) (*Channel, error) {
	// Address the executable this Manager already resolved for the host when the
	// registry has no evener_path: a deploy target from an earlier attempt (or a
	// discovered install) is the binary the host actually runs, and probing the
	// bare `evener` instead made version auto-match fail and re-deploy on every
	// reconnect.
	m.applyResolvedTarget(&host)
	// Refuse an unusable hub address before any ssh command runs: the probe and
	// restart paths below would otherwise address (and possibly kill) whatever
	// holds the default port, or poll a port the bridge never dials.
	if err := m.checkHostAddr(host); err != nil {
		return nil, err
	}
	m.stateEvent(host.Name, StatePreflighting)
	// Bound only the preflight here: deploy/restart below get deployLimit, and
	// attach bounds its own handshake (initTimeout).
	preflightCtx, cancelPreflight := context.WithTimeout(ctx, m.opts.attemptLimit())
	facts, err := m.preflight(preflightCtx, host)
	cancelPreflight()
	if err != nil {
		return nil, err
	}
	// Preflight may have discovered the executable during THIS attempt (the
	// installer default ~/.local/bin/evener, off the non-interactive PATH) and
	// recorded it. Re-apply it before anything addresses the binary again: the
	// deploy/restart phases and, crucially, attach → channelArgv otherwise build
	// the bridge from an empty evener_path, and evenerCommand falls back to the
	// bare word `evener`, which the host cannot resolve. The first attach to an
	// already-healthy host at that location would then fail with
	// command-not-found and only recover on a later attempt.
	m.applyResolvedTarget(&host)

	expected := m.opts.controllerVersion()
	// One probe of the hub that is actually RUNNING. ok is false when nothing
	// answered or the body was not a hub health response: an unknown fact, never
	// a hub with an empty version.
	probeCtx, cancelProbe := context.WithTimeout(ctx, m.opts.attemptLimit())
	running, runningKnown := m.probeRunningHub(probeCtx, host)
	cancelProbe()
	// Settlement judges the build the host will actually serve, exactly as
	// recoverRestart and the restart waits do: a host that keeps its own build
	// (hostBuildDiffers, no deploy configured) answers with its own version, not
	// the controller's. Comparing against the controller's version here would
	// leave a recorded start on such a host unsettled forever.
	servedVersion, _ := expectedServedBuild(facts, expected)
	if pending := m.pendingRestart(host.Name); runningKnown && running.version == servedVersion &&
		(pending.start || running.differentProcessFrom(pending.replaced)) {
		// A hub already serving exactly the expected build resolves whatever an
		// earlier restart or start left outstanding; do not launch a second hub
		// over it. A recorded START has no predecessor to exclude — bootstrapHub
		// only ever starts a hub where nothing was serving — so the expected build
		// alone settles it, exactly as recoverRestart judges a recovered start (a
		// start's zero `replaced` can never satisfy differentProcessFrom, so the
		// version match is the only thing that can clear it). A recorded
		// REPLACEMENT must additionally be provably not the process it meant to
		// replace: version equality alone is not that proof, because two unstamped
		// builds both report "dev", so a restart that never took would otherwise be
		// cleared on the old process's answer and the next Ensure would attach to
		// it. A start time that is missing on either side is not proof either: an
		// old hub whose health body carries no started_at would otherwise clear the
		// pending marker on the same answer, so the identity must be known on BOTH
		// sides and must differ before the marker is cleared (differentProcessFrom).
		m.clearPendingRestart(host.Name)
	}

	// A deploy replaces a hub only when one is present. The health probe is the
	// cheap half of that question; when nothing answered but a deploy is on the
	// table, ask whether a supervisor unit or a listener owns the hub port before
	// deciding. This probe runs only for a deploy with no answering hub, so the
	// common attach path pays nothing extra.
	hubPresent := runningKnown
	if !hubPresent && m.deployRequired(host.Name, facts, expected) {
		// Bound the hub-presence probe like every other phase: hubIsPresent issues
		// real ssh commands (systemctl list-units / launchctl list, then the port
		// probe — lsof, ss, or the host's TCP tables) and otherwise inherits the
		// attempt context, which for a supervisor reconnect is only
		// WithCancel(m.baseCtx) with no deadline. A remote command that hangs
		// (systemctl blocked on a stuck dbus, a probe stuck on NFS) would then hold
		// the per-host lock forever and stall every later Ensure and reconnect for
		// the host — exactly what attemptLimit exists to prevent.
		presentCtx, cancelPresent := context.WithTimeout(ctx, m.opts.attemptLimit())
		present, err := m.hubIsPresent(presentCtx, host, facts)
		cancelPresent()
		if err != nil {
			return nil, err
		}
		hubPresent = present
	}

	deploy, restart := m.ensureDecision(host.Name, facts, expected, running, runningKnown, hubPresent)
	if deploy {
		m.stateEvent(host.Name, StateDeploying)
		deployCtx, cancelDeploy := context.WithTimeout(ctx, m.opts.deployLimit())
		resolvedTarget, err := m.deploy(deployCtx, host, facts)
		cancelDeploy()
		if err != nil {
			return nil, err
		}
		if resolvedTarget != "" {
			// The installer fallback with an empty evener_path installed to its own
			// default location. Use that path for the restart, health re-probe, and
			// attach instead of assuming a separate `command -v evener` result.
			host.EvenerPath = resolvedTarget
			// Persist it too, so the next attempt (including the supervisor's
			// reconnect, which starts from the original registry host) addresses the
			// installed binary rather than re-resolving an empty evener_path and
			// re-deploying.
			m.setResolvedTarget(host.Name, resolvedTarget)
		}
		// The controller's own build is installed now; the dev-identity question
		// is settled for this Manager's lifetime.
		m.markDevDeployed(host.Name)
		// Judge the build just installed on the fresh on-disk facts BEFORE the
		// restart path. A wrong operator-supplied artifact is a permanent cause:
		// the restart onto it fails retryably in waitHealthy (the process never
		// reports the expected version), which would mask the terminal refusal and
		// leave the supervisor pushing and restarting forever without converging.
		// Judging first makes the permanent cause the one that surfaces.
		facts, err = m.reReadLaunchContract(ctx, host, facts)
		if err != nil {
			return nil, err
		}
		if err := m.deployedBuildNotStamped(host.Name, facts, expected); err != nil {
			return nil, err
		}
	}
	if restart {
		m.stateEvent(host.Name, StateRestarting)
		restartCtx, cancelRestart := context.WithTimeout(ctx, m.opts.deployLimit())
		var restartErr error
		if pending := m.pendingRestart(host.Name); pending.command != "" && !runningKnown {
			// A previous restart killed the old hub and left no listener. There is
			// no hub to kill now, so complete the recorded restart instead.
			restartErr = m.recoverRestart(restartCtx, host, facts, expected, pending)
		} else {
			restartErr = m.restartHub(restartCtx, host, facts, running)
		}
		cancelRestart()
		if restartErr != nil {
			return nil, restartErr
		}
	}
	if restart && !deploy {
		// A restart-only attempt (replacing a stale process, or completing a
		// recorded restart) still needs the post-phase re-read: the serving process
		// was replaced, so the launch contract read before it is stale. A deploy
		// already re-read the on-disk contract above — before the restart path,
		// where the terminal judgement must win — and the restart serves that same
		// on-disk binary, so there is nothing new to read here.
		facts, err = m.reReadLaunchContract(ctx, host, facts)
		if err != nil {
			return nil, err
		}
	}
	// The terminal protocol refusal fires only now, after the deploy has had its
	// chance and a restart has brought up the build that will actually serve. A
	// host that still speaks another appwire protocol cannot be attached, and when
	// no deploy was possible that is terminal — the outcome the old "always deploy"
	// path deferred by failing as a retryable ErrDeploy instead.
	if facts.LaunchCheckKnown && facts.Protocol != appwire.ProtocolVersion {
		return nil, fmt.Errorf("%w: host %q protocol %q, want %q", ErrProtocolIncompatible, host.Name, facts.Protocol, appwire.ProtocolVersion)
	}
	// The terminal launch-contract refusal fires only now: the deploy path has
	// had its chance, and this judges the build that will actually serve.
	if !slices.Contains(facts.LaunchFlags, requiredLaunchFlag) {
		return nil, fmt.Errorf("%w: host %q launch_flags %v missing %q", ErrLaunchContract, host.Name, facts.LaunchFlags, requiredLaunchFlag)
	}
	// A differing build VERSION is not a gate on the attach. A host that answered
	// the launch-check speaks this controller's appwire protocol — launch-check
	// refuses any protocol but its own (launchcheck.go:72-74), so a known contract
	// is itself the compatibility proof — and the wire layer enforces the same rule
	// again at Initialize (appwire.ProtocolVersionMismatchError). Gating on the
	// version label instead refused working hosts: an unstamped "dev" host against
	// a stamped controller, or a host built from another checkout, were told to
	// configure a deploy path they did not need, for a difference the protocol
	// already tolerates.
	//
	// Version auto-match still converges a host that can be converged: with a deploy
	// path configured, deployRequired above installs this controller's build and the
	// judgement right after the write (above) proves the result, while the installer
	// path keeps its own terminal ErrVersionMismatch for a moved channel tag. What
	// is left here attaches on the host's own build, and reports the difference
	// where it accepts it — at the attach, so a passed-over bootstrap cannot leave a
	// notice for an attach that never happened.
	// First attach to a stopped host must be able to start the hub. The probe
	// above only restarts a hub that is already answering, and the bridge is a
	// client that must not start one, so a configured host whose hub is not
	// running could never become usable. Bootstrap only for an explicit attach
	// request (never a reconnect or the background snapshot walk, which is
	// attached-only) and only when this attempt would otherwise attach to an
	// acceptable build: the terminal refusals above have already fired for a host
	// that cannot be upgraded, so there is nothing worth starting.
	if explicit && !runningKnown && !restart && m.pendingRestart(host.Name).command == "" {
		m.stateEvent(host.Name, StateRestarting)
		startCtx, cancelStart := context.WithTimeout(ctx, m.opts.deployLimit())
		bootErr := m.bootstrapHub(startCtx, host, facts, expected)
		cancelStart()
		if bootErr != nil {
			return nil, bootErr
		}
	}
	// The host keeps the build it has: nothing installed this controller's, and the
	// protocol — not the build label — decides compatibility. Say so, so accepting
	// the difference is never silent.
	if hostBuildDiffers(facts, expected) {
		m.logf("sshconn: host %s runs build %q, controller is %q; attaching anyway (a protocol-compatible host need not run this controller's build, and no deploy path replaced it)", host.Name, facts.Version, expected)
	}
	m.stateEvent(host.Name, StateAttaching)
	return m.attach(ctx, host, facts)
}

// bootstrapHub starts a host hub that is not running, the first-attach repair.
// It refuses to start anything when a process already owns the configured
// address (a second hub would race hub.lock), starts the identified supervisor's
// unit when one is named unambiguously (systemd `start` / launchd `kickstart
// -k`), otherwise the ops doc's detached ad hoc launch of the resolved
// evener_path / `command -v evener`, and then waits for the expected build
// exactly as the restart path does. A host with no resolvable executable and no
// supervisor is refused with ErrRestart, so the first host action surfaces the
// failure instead of attaching nothing.
func (m *Manager) bootstrapHub(ctx context.Context, host hostreg.Host, facts Preflight, expected string) error {
	// The build this start must see is the one the host will actually run, not
	// necessarily this controller's: expectedServedBuild.
	startVersion, startPin := expectedServedBuild(facts, expected)
	port := hubPort(m.hostAddr(host))
	lp, err := m.probeListeners(ctx, host, port)
	if err != nil {
		return err
	}
	if len(lp.pids) > 0 || lp.present {
		// Something already owns the address, even if it did not answer the health
		// probe. Starting a second hub would race hub.lock; leave it to the attach
		// attempt. An unnamed listener counts too: the port is held even when the
		// host's probes cannot say by which process.
		return nil
	}

	set, err := m.detectSupervisor(ctx, host, facts)
	if err != nil {
		return err
	}
	sup := set.live
	if sup.kind == supervisorNone {
		sup = set.dormant
	}
	if sup.kind != supervisorNone {
		if remote, ok := sup.startRemote(facts.UID); ok {
			// kickstart -k / start both bring the unit up; the recorded pending
			// restart keeps the start recoverable if this attempt is interrupted.
			m.setPendingStart(host.Name, remote)
			out, runErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, remote), nil)
			if runErr != nil && !sup.restartStatusIsAdvisory() {
				return fmt.Errorf("%w: host %q %s: %w: %s", ErrRestart, host.Name, remote, runErr, tail(out))
			}
			if err := m.waitStartedHealthy(ctx, host, startVersion, startPin); err != nil {
				return err
			}
			m.clearPendingRestart(host.Name)
			return nil
		}
	}

	// Ad hoc: launch the resolved executable detached, discarding output (there is
	// no recovered log for a hub that was not running).
	target, err := m.expectedHubExecutable(ctx, host)
	if err != nil {
		return err
	}
	relaunch := relaunchCommand(hubBootstrapArgv(m.opts, host, target), "")
	m.setPendingStart(host.Name, relaunch)
	out, runErr := m.runner.Run(ctx, rawCommandArgv(m.opts, host, relaunch), nil)
	if runErr != nil {
		return fmt.Errorf("%w: host %q bootstrap launch: %w: %s", ErrRestart, host.Name, runErr, tail(out))
	}
	if err := m.waitStartedHealthy(ctx, host, startVersion, startPin); err != nil {
		return err
	}
	m.clearPendingRestart(host.Name)
	return nil
}

// deployRequired reports whether the on-disk build must be replaced before the
// host can attach: its launch-check version differs from the controller's, its
// appwire protocol is incompatible, its launch flags are missing the required
// one, or its contract could not be read at all. ensureOnce calls it before the
// hub-presence probe, which is why it is a separate function from ensureDecision
// rather than a private detail of it.
func (m *Manager) deployRequired(name string, facts Preflight, expected string) bool {
	protocolOK := facts.LaunchCheckKnown && facts.Protocol == appwire.ProtocolVersion
	flagsOK := slices.Contains(facts.LaunchFlags, requiredLaunchFlag)
	versionDiffers := hostBuildDiffers(facts, expected)
	deployNeeded := versionDiffers || !protocolOK || !flagsOK

	// "dev" is not an identity: an unstamped controller and an unstamped host
	// both report it, so equality cannot prove they are the same code. The same
	// is true of a dirty version, which names a commit plus an uncommitted-changes
	// marker rather than the code that was compiled: a different dirty checkout at
	// the controller's commit reports the identical "<sha>-dirty" version. When a
	// deploy is configured the controller installs its own build once per Manager
	// and then trusts the host for this process's lifetime; with no deploy
	// configured there is nothing to install and the literal comparison stands.
	// A DIRTY controller with a deploy configured cannot install anything (deploy
	// refuses it terminally: see errControllerDirty), so the force below is what
	// keeps it from attaching to a host whose code equality cannot prove; the
	// refusal is terminal rather than a retryable ErrDeploy, so the same forced
	// deploy cannot become an endless cross-compile.
	deployPossible := m.canDeploy()
	devUnverified := isUnverifiableVersion(expected) && deployPossible && !m.isDevDeployed(name)

	// A deploy is only a decision when there is something to deploy. With no
	// BuildSource/BuildBinary configured, attempting one fails at
	// verifyBuildSource with ErrDeploy, which is non-terminal: the supervisor
	// retried it forever and the cause was discarded. With nothing to deploy a
	// version difference is left to the attach path, which accepts the host's own
	// build; ensureOnce refuses terminally only the cases no deploy could fix — a
	// protocol or launch-contract failure.
	return (deployNeeded && deployPossible) || devUnverified
}

// ensureDecision is the one decision table for ensureOnce. Everything it needs
// comes from a single preflight, a single /api/health probe, and the one
// hub-presence probe ensureOnce runs when a deploy is on the table:
//
//   - deploy when the on-disk binary is not the build this controller requires:
//     its launch-check version differs, its appwire protocol is incompatible,
//     its launch flags are missing the required one, or its contract could not
//     be read at all. The deploy runs over ssh, not appwire, so even a
//     protocol-broken host is upgradable.
//   - restart when a deploy replaces a hub that is actually present, a running
//     hub answers with a build other than the expected one, or a previous
//     restart left the host without a listener (a pending relaunch). A deploy
//     that finds no hub present (hubPresent false) is a fresh install, not a
//     restart: it must fall through to the explicit-attach bootstrap, and a
//     reconnect must never turn it into a hub start.
//   - attach otherwise.
//
// No terminal refusal is decided here: the protocol and launch-contract gates
// are judged by ensureOnce only after a deploy/restart has run.
func (m *Manager) ensureDecision(name string, facts Preflight, expected string, running hubIdentity, runningKnown, hubPresent bool) (deploy, restart bool) {
	deploy = m.deployRequired(name, facts, expected)
	versionDiffers := hostBuildDiffers(facts, expected)
	// Replace the RUNNING hub only when the binary a restart would launch (the
	// on-disk one) already matches the controller. A restart cannot give the
	// on-disk binary a version it does not carry, so an on-disk mismatch needs the
	// deploy above and never a restart; requiring this is what stops a host with no
	// build source from retrying a restart that can never succeed. Replacing a
	// stale *process* whose on-disk binary already matches is exactly the case this
	// covers. A deploy restarts only a hub that is present; with nothing present
	// there is nothing to replace.
	runningStale := runningKnown && running.version != expected && !versionDiffers
	restart = (deploy && hubPresent) || runningStale || m.pendingRestart(name).command != ""
	return deploy, restart
}

// hubIsPresent reports whether a hub is present on host in a form a restart can
// replace: a live supervisor unit that owns one, or a process holding the hub
// port. A hub that answered /api/health is already known, so the caller passes
// runningKnown in and this runs only when the health probe found nothing. It
// separates a deploy that must replace a running hub from a fresh install on a
// stopped host, and it is why a reconnect never starts a hub: with nothing
// present ensureDecision chooses no restart at all, and the first-attach
// bootstrap stays gated to an explicit request.
func (m *Manager) hubIsPresent(ctx context.Context, host hostreg.Host, facts Preflight) (bool, error) {
	set, err := m.detectSupervisor(ctx, host, facts)
	if err != nil {
		return false, err
	}
	if set.live.kind != supervisorNone {
		return true, nil
	}
	cleared, err := m.portCleared(ctx, host, hubPort(m.hostAddr(host)))
	if err != nil {
		return false, err
	}
	return !cleared, nil
}

// refreshLaunchContract re-reads the on-disk launch contract after a deploy or
// restart and refuses a still-incompatible protocol. The pre-restart facts
// describe the build the restart replaced, so judging the launch contract on
// them would let a host predating a required flag, or speaking an older
// protocol, never be accepted even after a successful upgrade.
func (m *Manager) refreshLaunchContract(ctx context.Context, host hostreg.Host, facts Preflight) (Preflight, error) {
	refreshed, err := m.probeLaunchCheck(ctx, host)
	if err != nil {
		return facts, err
	}
	if refreshed.Protocol != appwire.ProtocolVersion {
		return facts, fmt.Errorf("%w: host %q protocol %q, want %q after deploy/restart", ErrProtocolIncompatible, host.Name, refreshed.Protocol, appwire.ProtocolVersion)
	}
	facts.LaunchCheckKnown = true
	facts.Protocol = refreshed.Protocol
	facts.LaunchFlags = refreshed.LaunchFlags
	// Report the version the re-probe actually read. The restart verified the
	// *running* hub against expected, but this launch-check describes the on-disk
	// binary, which can still differ (a replaced or partially installed file);
	// overwriting it with expected would make the channel claim a match it did
	// not observe. A difference here is what ensureOnce's post-deploy gate
	// refuses on: a deploy that leaves it cannot be retried into converging.
	facts.Version = refreshed.Version
	return facts, nil
}

// reReadLaunchContract re-reads the on-disk launch contract under the deploy
// limit. It is refreshLaunchContract plus the phase timeout, so the two call
// sites that judge a deploy or a restart bound the remote probe identically
// instead of each repeating the WithTimeout.
func (m *Manager) reReadLaunchContract(ctx context.Context, host hostreg.Host, facts Preflight) (Preflight, error) {
	refreshCtx, cancel := context.WithTimeout(ctx, m.opts.deployLimit())
	defer cancel()
	return m.refreshLaunchContract(refreshCtx, host, facts)
}

// deployedBuildNotStamped is the terminal refusal a deploy that left the host on a
// build other than the controller's must produce, or nil when the fresh facts pin
// the controller's build (or could not be read at all). It judges the launch
// contract re-read after the phase, the only evidence available for an
// operator-supplied artifact built from another tree: it targets the right
// platform and carries a foreign identity, so the pre-push check passes and this
// disagreement is what remains. Attaching anyway would serve a host on a build the
// controller did not stamp, the version auto-match guarantee this component exists
// for; retrying re-pushes the same artifact, so the refusal is terminal rather
// than the retryable ErrDeploy (the same reason errControllerDirty is).
//
// It is judged only where a deploy ran. A pass that merely started or restarted the
// hub launched the build already on disk, and the attach rule allows that build to
// be the host's own, so judging the on-disk version after a restart-only pass would
// refuse exactly the host the rule exists to attach to. What a restart must prove —
// that the process answering is the one the phase produced — belongs to the waits,
// which already do it; what this adds is the on-disk judgement, and only a deploy
// changes what is on disk.
//
// The remedy clause comes through Options.DeployHelp (unstampedRemedy), so a hub
// names the flags to set exactly as its sibling refusals do while an embedder with
// no flags keeps the library's own sentence.
func (m *Manager) deployedBuildNotStamped(hostName string, facts Preflight, expected string) error {
	if !hostBuildDiffers(facts, expected) {
		return nil
	}
	return fmt.Errorf("%w: host %q still reports version %q after this controller deployed its build, want %q; the build the host now holds was not built from this controller's tree, so the host cannot be pinned to the controller's build — %s",
		errDeployUnstamped, hostName, facts.Version, expected, m.unstampedRemedy())
}

// attach spawns the bridge, wraps its stdio in a StreamTransport, and
// initializes the AppWire client.
func (m *Manager) attach(ctx context.Context, host hostreg.Host, facts Preflight) (*Channel, error) {
	argv := channelArgv(m.opts, host)
	sink := newDiagSink(m.diagWriter)
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
	// deadline: neither caller imposes the attempt limit on this path. Ensure
	// hands ensureOnce plain context.WithCancel (so an in-flight attempt cannot
	// outlive the manager), and reconnectOnce passes the supervisor's own
	// cancellable context, which carries no deadline either — so a hung
	// Initialize stops at initTimeout (default 30s) rather than running to an
	// outer attemptLimit (default 70s) the old comment still described.
	// WithTimeout keeps whichever deadline is earlier.
	initCtx, cancel := context.WithTimeout(ctx, m.opts.initTimeout())
	defer cancel()

	handshake, err := client.Initialize(initCtx, appwire.InitializeParams{
		ProtocolVersion: appwire.ProtocolVersion,
		ClientInfo:      appwire.ClientInfo{Name: m.opts.clientName(), Version: m.opts.clientVersion()},
	})
	if err != nil {
		// Reap synchronously before reading the sink: os/exec copies the child's
		// stderr from its own goroutine and Wait returns only once that copy is
		// done, so a classification cannot read a partial diagnostic.
		_ = reapBridge(transport, stdio)
		if m.baseCtx.Err() != nil {
			// Close landed while the handshake was in flight. The canceled base
			// context, not a transport fault, is why this channel is unusable.
			return nil, ErrManagerClosed
		}
		if _, isMismatch := errors.AsType[appwire.ProtocolVersionMismatchError](err); isMismatch {
			return nil, fmt.Errorf("%w: host %q: %w", ErrProtocolIncompatible, host.Name, err)
		}
		// A started ssh ran a remote command, so neither its stderr (which the
		// remote command's own output shares) nor its exit status can prove the
		// refusal is ssh's own: ssh forwards the remote command's status unchanged,
		// including 255. Stay retryable, matching the one-shot preflight path
		// (isSSHAuthFailure); only a failure to spawn ssh is unambiguous, and the
		// Start-error branch above already classifies that one.
		return nil, fmt.Errorf("%w: host %q initialize: %w: %s", ErrSSHStart, host.Name, err, sink.tail())
	}
	// Publish the handshake before the channel is handed to any caller, so the
	// attached-only HandshakeIfAttached never races the attach it reports on.
	ch.handshake = handshake

	if m.baseCtx.Err() != nil {
		_ = reapBridge(transport, stdio)
		return nil, ErrManagerClosed
	}

	go func() {
		_ = stdio.Wait()
		// Retire the channel's liveness before announcing the reaping. A
		// consumer that has observed the child's exit (stopped) — or that
		// decides liveness without consulting stopped at all — must never find
		// the already-exited process still offered by ClientIfAttached or
		// installedLiveChannel, so lost is closed before stopped.
		ch.markLost()
		close(ch.stopped)
		if h := m.opts.afterChildExit; h != nil {
			h(host.Name, ch)
		}
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
//
// Close is a link-down edge too. The client closes the transport itself on a
// notification overflow (client.go's ErrNotificationOverflow path), which never
// surfaces as a Recv error, so without this the manager would keep offering a
// channel whose reader is gone and no supervisor would reconnect.
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

func (t *lossWatchingTransport) Close() error {
	t.onErr()
	return t.Transport.Close()
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

	if m.opts.beforeSuperviseGate != nil {
		m.opts.beforeSuperviseGate(host.Name, ch)
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

	// BackoffMax bounds every reconnect delay, the first one included: a caller
	// that configures a maximum below the base must not have its first retry wait
	// longer than the bound it asked for.
	delay := min(m.opts.backoffBase(), m.opts.backoffMax())
	for {
		if err := m.opts.waitSleep(ctx, m.jitterFor(delay)); err != nil {
			// A canceled loop no longer owns the host, so it announces nothing.
			// stopSupervisor stopped it because the host reached a terminal
			// outcome — announced by the caller that stopped it, and possibly
			// followed by a replacement's Attached — and Close canceled the base
			// context, whose terminal event is the Detached it emits while tearing
			// the channel down (a supervisor on a canceled manager "returns
			// without emitting one", as Close's pairing comment states). Emitting a
			// Disconnected of its own would contradict that announcement or arrive
			// after the replacement's Attached.
			return
		}
		if !m.reconnectOnce(ctx, host, lock) {
			return
		}
		delay = nextBackoff(delay, m.opts.backoffMax())
	}
}

// startSupervise launches the reconnect loop that owns host's channel and
// reports whether it started. The supervisor gets its own context, derived from
// baseCtx, so a terminal failure can end it even while it waits out a backoff.
//
// The registration happens under m.mu, together with the closed check: Go
// increments supervisorsWG before it returns, so once Close has set closed and
// released the lock no new supervisor can join the group, and every supervisor
// that did join is observed by Close's Wait.
//
// Every live loop stays registered as its own entry: a replacement attach adds a
// loop rather than overwriting the host's slot, so stopSupervisor can end a
// predecessor still parked in backoff instead of leaking it.
func (m *Manager) startSupervise(host hostreg.Host, ch *Channel, lock *sync.Mutex) bool {
	ctx, cancel := context.WithCancel(m.baseCtx)
	loop := &supervisorLoop{host: host, cancel: cancel}
	m.mu.Lock()
	if m.closed {
		// Close already ran. A supervisor started now would only race its Wait, and
		// the caller reaps the channel it could not supervise instead.
		m.mu.Unlock()
		cancel()
		return false
	}
	if m.supervisors[host.Name] == nil {
		m.supervisors[host.Name] = map[*supervisorLoop]struct{}{}
	}
	m.supervisors[host.Name][loop] = struct{}{}
	// The supervisor goroutine outlives this call and keeps gating on lock, so
	// it takes its own live-user reference here — under m.mu, before the
	// goroutine exists, and while the caller's reference still pins the entry
	// (an entry is deleted only at zero references, so the entry this lock
	// belongs to is the one being counted). Without it, the caller's release
	// with its return could drop the entry while the loop still holds this
	// mutex, and a fresh acquisition would build a second gate for the name.
	m.locks[host.Name].refs++
	// WaitGroup.Go adds, runs, and marks the loop done, so Close waiting on the
	// group observes the fully-finished loop.
	m.supervisorsWG.Go(func() {
		// Deregister and release this loop's context child when it ends. Without the
		// deregistration a replaced loop's handle would stay in the host's set for
		// good — a leak that grows by one with each reconnect — and its WithCancel
		// child would stay registered with baseCtx until Close.
		defer func() {
			m.mu.Lock()
			if set := m.supervisors[host.Name]; set != nil {
				delete(set, loop)
				if len(set) == 0 {
					delete(m.supervisors, host.Name)
				}
			}
			m.mu.Unlock()
			cancel()
			// The loop no longer touches the gate, so its reference goes with
			// it — the last one out drops the entry.
			m.releaseHostLock(host.Name)
			// Announce the exit only after the gate is released, so a test that
			// waits on it can assert on the supervisor's final state.
			if m.opts.superviseExited != nil {
				m.opts.superviseExited(host.Name)
			}
		}()
		m.supervise(ctx, host, ch, lock)
	})
	m.mu.Unlock()
	return true
}

// supervisorLoop identifies one live supervise goroutine. A cancel func is not a
// valid map key, so loops are keyed by this handle.
type supervisorLoop struct {
	// host is the registration this loop reconnects. The teardown fences read
	// it, so a stale removal cannot cancel the reconnect of a host that was
	// re-added under the same name after the removal looked.
	host   hostreg.Host
	cancel context.CancelFunc
}

// stopSupervisor ends every reconnect loop host still has, if any. Without it a
// terminal failure announced by Ensure would leave the supervisor that retired
// its own channel still looping, to repeat the same terminal outcome — a second
// EventFailed for a host consumers were already told was finished — or, for a
// terminal class the next attempt happens to pass, to announce an Attached after
// that Failed. Cancelling all of them, not just the most recent, is what reaches
// a predecessor parked in backoff after a replacement attach registered over it.
func (m *Manager) stopSupervisor(name string) {
	m.mu.Lock()
	loops := m.supervisors[name]
	delete(m.supervisors, name)
	m.mu.Unlock()
	for loop := range loops {
		loop.cancel()
	}
}

// reconnectOnce runs one re-attach attempt with the host lock held, and reports
// whether another attempt is worth making. Every outcome that ends the
// supervisor emits its own state event first.
func (m *Manager) reconnectOnce(ctx context.Context, host hostreg.Host, lock *sync.Mutex) bool {
	lock.Lock()
	defer lock.Unlock()

	if ctx.Err() != nil {
		// A canceled loop no longer owns the host; the canceller (a terminal
		// Ensure, a terminal reconnect, or Close's Detached) owns the terminal
		// announcement. Emit nothing, exactly as supervise's backoff path does.
		return false
	}
	if m.currentChannel(host.Name) != nil {
		// Any mapped channel means another supervisor owns this host: one whose link
		// has dropped is still owned, because its own supervisor is reconnecting.
		// Keying on liveness here let two supervisors attach for one host, and the
		// loser's channel was clobbered without ever being detached.
		return false
	}
	nch, err := m.ensureOnce(ctx, host, false)
	if err == nil {
		if nch.isLost() || nch.isClosed() {
			// The link died between the handshake and this publish. Announcing it
			// would install a dead channel and leak the old registration, so reap
			// it and take another turn through the backoff.
			_ = nch.Close()
			m.stateEvent(host.Name, StateReconnecting)
			return true
		}
		// Recheck the registry before publishing, as Ensure does — and on the
		// full entry and its generation, not just the name: a host deregistered
		// while this loop was reconnecting it (RemoveHost stops parked loops,
		// but this attempt was already in flight) must not gain a fresh
		// channel, and a remove/re-add that swapped the entry this loop
		// captured must not gain a channel built from the removed entry
		// either — byte-identical re-adds included, because the registry
		// assigns the re-add a new generation. Reap the replacement, report
		// the host honestly disconnected, and stand down.
		if !m.reg.SameRegistration(host.Name, host) {
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		if !m.publishChannel(host.Name, nch, host) {
			// Close landed mid-attempt; reap the channel it would have orphaned.
			_ = nch.Close()
			m.stateEvent(host.Name, StateDisconnected)
			return false
		}
		if m.opts.afterPublish != nil {
			m.opts.afterPublish(host.Name, nch)
		}
		if nch.isLost() || nch.isClosed() {
			// The link dropped between the pre-publish check and the announcement.
			// Announcing it would install an unusable channel and leak the source a
			// consumer builds from the Attached, so give the slot back, reap it, and
			// take another turn through the backoff. This supervisor owns the host,
			// so clearing the slot does not orphan it.
			m.clearChannel(host.Name)
			_ = nch.Close()
			m.stateEvent(host.Name, StateReconnecting)
			return true
		}
		m.attachEvent(host.Name, nch, StateAttached)
		if !m.startSupervise(host, nch, lock) {
			// Close set its flag before supervision began: pair the Attached and
			// reap the channel, which nothing will ever reconnect.
			m.detachEvent(host.Name, StateDisconnected)
			m.clearChannel(host.Name)
			_ = nch.Close()
			return false
		}
		return false
	}
	// Close canceling the attempt surfaces a preflight that was in flight as a
	// retryable transport failure. The canceled base context, not the host, is
	// why: map it to ErrManagerClosed before the terminal check, exactly as Ensure
	// does, so shutdown does not emit a spurious StateReconnecting and send the
	// supervisor around one more loop iteration.
	if m.baseCtx.Err() != nil && !isTerminal(err) {
		err = ErrManagerClosed
	}
	if isTerminal(err) {
		// Close canceling the attempt is not a host failure: during shutdown a
		// consumer must not receive an EventFailed for it. The mapping to
		// ErrManagerClosed above covers only the retryable transport shape, so a
		// terminal fault found concurrently with the shutdown — a protocol
		// mismatch, an unparseable preflight — must be suppressed directly here,
		// exactly as Ensure's own baseCtx.Err() path suppresses it.
		if m.baseCtx.Err() == nil {
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

// nextBackoff doubles delay, capped at limit. The doubling saturates rather
// than wrapping: a delay near the representable maximum (which a caller can
// seed through BackoffBase/BackoffMax) would otherwise overflow negative, and a
// negative delay fires the sleep timer immediately instead of backing off.
func nextBackoff(delay, limit time.Duration) time.Duration {
	if delay > maxDuration/2 {
		if limit > 0 {
			return limit
		}
		return maxDuration
	}
	delay *= 2
	if limit > 0 && delay > limit {
		return limit
	}
	return delay
}

// isTerminal reports whether err ends the reconnect loop instead of being
// retried. Every contract violation is terminal, matching spec 04's "Error
// handling". An authentication-shaped failure is deliberately not: ssh forwards
// a remote command's stderr and exit status, so the refusal cannot be attributed
// to ssh, and retrying is the safe side of that ambiguity (see ErrSSHAuth).
func isTerminal(err error) bool {
	switch {
	case errors.Is(err, ErrProtocolIncompatible),
		errors.Is(err, ErrUnsupportedHost),
		errors.Is(err, ErrLaunchContract),
		errors.Is(err, ErrVersionMismatch),
		errors.Is(err, ErrHostNotFound),
		errors.Is(err, ErrHostAddr),
		errors.Is(err, ErrPreflightDecode),
		errors.Is(err, errExecutableMissing),
		errors.Is(err, errControllerDirty),
		errors.Is(err, errRunTargetUnservable),
		errors.Is(err, errDeployArtifactUnusable),
		errors.Is(err, errDeployUnstamped),
		errors.Is(err, ErrManagerClosed):
		return true
	default:
		return false
	}
}

// ErrControllerDirty is the exported alias for the terminal dirty-controller
// deploy refusal (errControllerDirty, deploy.go): this controller was built
// from a dirty tree, so it has no reproducible identity to install on or match
// against a host, and the refusal is terminal rather than a retryable ErrDeploy.
// It is exported so a caller — the hub's attach handler — can match the refusal
// with errors.Is and surface it as a typed deploy failure
// (appwire.HubLaunchError) rather than a generic internal error.
var ErrControllerDirty = errControllerDirty

// ErrRunTargetUnservable is the exported alias for the terminal run-target
// deploy refusal (errRunTargetUnservable, deploy.go): the configured run target
// is not the `evener` binary a host hub can serve, so no deploy to it can ever
// attach and the refusal is terminal rather than a retryable ErrDeploy. It is
// exported so a caller — the hub's attach handler — can match the refusal with
// errors.Is and surface it as a typed deploy failure (appwire.HubLaunchError)
// rather than a generic internal error, the same reason ErrControllerDirty is.
var ErrRunTargetUnservable = errRunTargetUnservable

// ErrDeployArtifactUnusable is the exported alias for the terminal
// operator-artifact deploy refusal (errDeployArtifactUnusable, deploy.go): the
// -deploy-binary artifact an operator supplied cannot serve the host — it
// targets another platform, or it is a Go program that is not evener — so
// retrying re-reads the same file and can never succeed. It is exported so a
// caller — the hub's attach handler — can match the refusal with errors.Is and
// surface it as a typed deploy failure (appwire.HubLaunchError) rather than a
// generic internal error, the same reason ErrRunTargetUnservable is.
var ErrDeployArtifactUnusable = errDeployArtifactUnusable

// ErrDeployUnstamped is the exported alias for the terminal post-deploy identity
// refusal (errDeployUnstamped, deploy.go): this controller deployed its build but
// the host still reports a different one, so the artifact was not stamped by this
// controller and the host can never be pinned to its build. It is exported so a
// caller — the hub's attach handler — can match the refusal with errors.Is and
// surface it as a typed deploy failure (appwire.HubLaunchError) rather than a
// generic internal error, the same reason ErrControllerDirty is.
var ErrDeployUnstamped = errDeployUnstamped

// hostLockEntry is one per-host gate together with its live-user count.
// refs counts the hostLock acquisitions that have not been released yet —
// holders and parked waiters both — so the entry can be dropped exactly when
// nobody can still be using its mutex.
type hostLockEntry struct {
	mu   sync.Mutex
	refs int
}

// hostLock returns the per-host gate for name and registers the caller as a
// live user of it: every hostLock call must be paired with exactly one
// releaseHostLock call, made by the caller once neither it nor anyone it handed
// the gate to can touch the mutex again. The pairing keeps the map bounded
// (a permanent gate per name would leak one mutex entry for every host
// name ever attached, for the manager's lifetime) while making that
// cleanup safe: an entry is deleted only when its last user released it,
// so a gate is never dropped while a holder — or a goroutine parked
// waiting on it — still uses it, and two callers can never hold two
// different gates for one name.
func (m *Manager) hostLock(name string) *sync.Mutex {
	m.mu.Lock()
	entry := m.locks[name]
	if entry == nil {
		entry = &hostLockEntry{}
		m.locks[name] = entry
	}
	entry.refs++
	m.mu.Unlock()
	return &entry.mu
}

// releaseHostLock pairs one hostLock call for name, dropping the entry when
// the last live user released it: refs reached zero, so no goroutine holds
// the gate or is parked acquiring it, and a later acquisition building a
// fresh entry for the name cannot split one host's exclusion across two
// mutexes. Waiters are counted at acquisition, before they park, so they hold
// the entry open for as long as they wait; lockHostCtx's abandoned acquire
// parks on the mutex without a reference of its own, but its caller's
// reference covers the parking and the abandoned goroutine only hands the
// mutex straight back — it never runs a critical section — so a release that
// deletes the entry under it leaves it touching a mutex nothing else
// references.
func (m *Manager) releaseHostLock(name string) {
	m.mu.Lock()
	if entry := m.locks[name]; entry != nil {
		entry.refs--
		if entry.refs == 0 {
			delete(m.locks, name)
		}
	}
	m.mu.Unlock()
}

// lockHostCtx acquires the per-host gate, giving up when ctx is done. The host
// lock itself is a plain sync.Mutex: Close and the reconnect supervisors take it
// without a context and must not be made to fail, so a canceled waiter cannot be
// woken out of Lock directly. This helper parks a goroutine on Lock instead and,
// when the caller gives up first, has that goroutine hand the lock straight back
// once it acquires it — the gate is never left held by a caller that returned.
func lockHostCtx(ctx context.Context, mu *sync.Mutex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Fast path: an uncontended acquire needs neither a goroutine nor a select.
	if mu.TryLock() {
		return nil
	}
	acquired := make(chan struct{})
	abandon := make(chan struct{})
	go func() {
		mu.Lock()
		// The unbuffered send completes only if the caller is still waiting; once
		// it has given up, nothing will receive, so release the lock here.
		select {
		case acquired <- struct{}{}:
		case <-abandon:
			mu.Unlock()
		}
	}()
	select {
	case <-acquired:
		return nil
	case <-ctx.Done():
		close(abandon)
		return ctx.Err()
	}
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

// Attached reports whether host currently has a usable channel: one that has
// been established, has not been closed, and has not lost its link. A host that
// has never been Ensure'd is not attached; the hub's background refresh
// attaches lazily, so this converges within one refresh interval. A link-lost
// channel reports detached as soon as markLost closes lost, before the
// supervisor wakes to clear and replace it.
func (m *Manager) Attached(name string) bool {
	ch := m.currentChannel(name)
	return ch != nil && !ch.isClosed() && !ch.isLost()
}

// publishChannel records ch as name's channel unless Close already ran, in which
// case nothing would ever supervise it: the caller reaps ch and reports
// ErrManagerClosed instead.
//
// registration is the entry the channel was dialed for, which the caller
// revalidated with hostreg.Registry.SameRegistration under the host lock
// immediately before this call: the registry entry name resolved to before
// ensureOnce folded this Manager's resolved executable path into the copy of
// the host it dialed. Storing that value — rather than re-reading the registry
// here — is what keeps identity pairing honest. A re-read is a TOCTOU: an
// update landing between the caller's recheck and this function would stamp a
// channel built from the pre-update entry with the post-update one, which
// MatchesRegistration then accepts for a row rendering the new configuration.
// The caller's capture cannot go stale in that way: the registry never mutates
// an inserted entry, so a later update only makes SameRegistration refuse the
// capture, never adopt it.
func (m *Manager) publishChannel(name string, ch *Channel, registration hostreg.Host) bool {
	// Write the identity before the channel becomes visible through the map
	// below, so no reader can find it there with an empty registration. The
	// manager mutex is left unnested from the registry's: nothing here reads the
	// registry.
	ch.reg = registration
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

// beginEnsure registers an in-flight Ensure as a caller goroutine Close must
// wait for, unless Close already ran. The Add happens under the same mutex that
// sets closed — the ordering startSupervise uses — so Close's Wait cannot miss
// this call: once Close has released the lock no new Ensure can register, and
// every Ensure that registered is counted.
func (m *Manager) beginEnsure() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.ensureWG.Add(1)
	return true
}

func (m *Manager) clearChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.chans, name)
}

// isDevVersion reports whether v is an identity-less development build.
// buildinfo reports "dev" for any binary built without ldflags, so two dev
// builds cannot be told apart by it.
func isDevVersion(v string) bool {
	return v == "" || v == "dev"
}

// isDirtyVersion reports whether v carries buildinfo's dirty marker. The
// "-dirty" suffix names the commit a checkout started from, not the code that
// was compiled: two dirty checkouts at the same commit can carry different
// uncommitted changes and still report the same version, so equality on it
// proves nothing about the binaries matching.
func isDirtyVersion(v string) bool {
	return strings.HasSuffix(strings.TrimSpace(v), "-dirty")
}

// isUnverifiableVersion reports whether v cannot prove two builds are the same
// code. An empty version and "dev" are identity-less by construction; a dirty
// version is a commit plus a marker rather than a content identity, so a host
// reporting the controller's own "<sha>-dirty" may be running a different dirty
// checkout of that commit.
func isUnverifiableVersion(v string) bool {
	return isDevVersion(v) || isDirtyVersion(v)
}

// hostBuildDiffers reports whether the host's known on-disk build is a build other
// than the one this controller expects to find there. An unreadable contract
// carries no build at all, so it is not a difference to report: the launch-contract
// gates handle a host that could not answer.
//
// Every place that compares the host's build against the controller's asks this,
// so the rule has one expression: when the attach stopped being gated on the build
// version, four copies of it had to be found and reconsidered, and the copies that
// were missed were exactly the ones that kept refusing.
func hostBuildDiffers(facts Preflight, expected string) bool {
	return facts.LaunchCheckKnown && facts.Version != expected
}

// canBuild reports whether the primary cross-compile + push path is available: a
// build source (or an injected BuildBinary) is configured.
func (m *Manager) canBuild() bool {
	return m.opts.BuildBinary != nil || strings.TrimSpace(m.opts.BuildSource) != ""
}

// canDeploy reports whether the controller can install its own build on a host
// at all: either the primary push path is available, or the installer fallback
// can pin a published artifact for this controller's build channel. A dev/dirty
// controller with no build source has neither, so a version difference cannot be
// converged here — under the attach rule that is not a refusal: the host keeps its
// own build and attaches, and ensureOnce reports the difference. What such a
// controller still refuses terminally is a protocol or launch-contract failure,
// which no deploy could have fixed.
//
// This answers "is a deploy path configured", not "will the configured path
// accept this build": a dirty controller with a build source reaches deploy,
// which refuses it terminally (errControllerDirty) instead of returning false
// here — returning false would let the decision ladder attach to a host whose
// code equality the dirty version cannot prove.
func (m *Manager) canDeploy() bool {
	if m.canBuild() {
		return true
	}
	_, err := installerRefFor(buildinfo.BuildChannel(), buildinfo.ReleaseTag, buildinfo.GitDirty, m.installerRemedy())
	return err == nil
}

// deployRemedy is the remedy clause a deploy refusal appends: the embedder's
// text when it supplied any, else the library's own sentence for that refusal.
// See Options.DeployHelp.
func (m *Manager) deployRemedy(libraryDefault string) string {
	if help := strings.TrimSpace(m.opts.DeployHelp); help != "" {
		return help
	}
	return libraryDefault
}

// deployHelp is the remedy clause the refusals that have nothing to install
// append: the missing-executable preflight refusal, and the installer fallbacks
// through installerRemedy. The default is what an embedder with no flags to name
// gets, so an unconfigured hub that also supplies no help is refused exactly as it
// was before this field existed.
func (m *Manager) deployHelp() string {
	return m.deployRemedy("set Options.BuildSource")
}

// installerRemedy is the remedy clause the installer-path refusals append. Its
// default names the option an embedder sets directly, which is the right text
// when there are no CLI flags to name; a hub supplies Options.DeployHelp
// instead, so an operator is told which flags to set rather than an internal
// field they cannot act on.
func (m *Manager) installerRemedy() string {
	return m.deployRemedy("use the atomic push path or Options.BuildBinary")
}

// unstampedRemedy is the remedy clause the post-phase build-identity refusal
// appends (deployedBuildNotStamped). Its default names what an embedder can do
// directly — supply a matching artifact or a build source — which is the right
// text when there are no CLI flags to name; a hub supplies Options.DeployHelp
// instead, so an operator is told which flags to set rather than an internal
// field they cannot act on.
func (m *Manager) unstampedRemedy() string {
	return m.deployRemedy("supply an artifact built from this controller's tree, or a build source")
}

// isDevDeployed reports whether this Manager has installed its own dev build on
// name. See ensureDecision.
func (m *Manager) isDevDeployed(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.devDeployed[name]
}

func (m *Manager) markDevDeployed(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devDeployed[name] = true
}

// resolvedTarget returns the executable path this Manager resolved for name on
// an earlier attempt, or "" when none has been recorded.
func (m *Manager) resolvedTarget(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolvedTargets[name]
}

// applyResolvedTarget fills host.EvenerPath from the executable path this
// Manager already resolved for the host, when the registry configured none. A
// configured evener_path always wins. ensureOnce applies it twice for one
// reason: preflight can discover and record the installer-default binary during
// the very attempt that needs to address it (probeInstallerDefaultExecutable →
// setResolvedTarget), and a host.EvenerPath that stays empty there makes
// channelArgv build the bridge from the bare word `evener`, which a
// non-interactive PATH need not carry.
func (m *Manager) applyResolvedTarget(host *hostreg.Host) {
	if strings.TrimSpace(host.EvenerPath) != "" {
		return
	}
	if p := m.resolvedTarget(host.Name); p != "" {
		host.EvenerPath = p
	}
}

// setResolvedTarget records the executable path this Manager resolved for name.
// It carries a deploy target (or a discovered install) across attempts, so a
// later Ensure — or the supervisor's reconnect, which starts from the original
// registry host — addresses the installed binary instead of a bare `evener` the
// non-interactive PATH may not carry.
func (m *Manager) setResolvedTarget(name, target string) {
	if target == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolvedTargets == nil {
		m.resolvedTargets = map[string]string{}
	}
	m.resolvedTargets[name] = target
}

// pendingRestart returns the restart a failed or unverified restart recorded, or
// the zero value when there is none.
func (m *Manager) pendingRestart(name string) pendingRestartState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingRestarts[name]
}

func (m *Manager) setPendingRestart(name, remote string, replaced hubIdentity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingRestarts[name] = pendingRestartState{command: remote, replaced: replaced}
}

// setPendingStart records a command that starts a hub where nothing was
// serving. It is the bootstrap path's recording: there is no predecessor
// identity to settle, and a later recovery must not require one.
func (m *Manager) setPendingStart(name, remote string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingRestarts[name] = pendingRestartState{command: remote, start: true}
}

func (m *Manager) clearPendingRestart(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pendingRestarts, name)
}

// restoreChannel re-maps ch as name's channel unless Close already ran, giving
// the slot back to a channel whose supervisor is still blocked on the host lock.
// It reports whether the slot was restored.
func (m *Manager) restoreChannel(name string, ch *Channel) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.chans[name] = ch
	return true
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
	host hostreg.Host
	// reg is the registration this channel was published for: the registry
	// entry the caller validated under the host lock and passed to
	// publishChannel, captured before ensureOnce folded this Manager's
	// resolved executable path (applyResolvedTarget) into the copy of the host
	// it dialed. Identity pairing (MatchesRegistration) compares reg, not host:
	// a host that configured no evener_path never carries the resolved path in
	// the registry, so comparing host refused the common case. It is written
	// once, under the host lock at publish and before the channel is visible
	// through the map, and never mutated.
	reg   hostreg.Host
	facts Preflight
	// handshake is the InitializeResponse the attach's own initialize captured.
	// It is published once, before the channel is visible through the map, and
	// never mutated, so the attached-only lookups can read it without a lock.
	handshake appwire.InitializeResponse
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

// Handshake returns the InitializeResponse captured when this channel attached:
// ProtocolVersion, ServerInfo, SourceID, and the peer's advertised Features.
// appwire.Client keeps its own Features copy privately, so component 05's
// capability probe reads ProtocolVersion/SourceID/Features here (reached by
// host name through Manager.ChannelIfAttached and the
// remoteHostHandshakeForChannel closure, not through
// Manager.HandshakeIfAttached) rather than re-running the connection-scoped
// initialize. ServerInfo is carried but not consumed: its Version is the hub's
// package constant, not its build identity, so the probe's HubVersion stays
// preflight-owned.
func (c *Channel) Handshake() appwire.InitializeResponse { return c.handshake }

// Host returns the registry entry this channel was built from. The value owns
// its slice, mirroring Preflight and hostreg's own copies.
func (c *Channel) Host() hostreg.Host {
	h := c.host
	h.Roots = slices.Clone(c.host.Roots)
	return h
}

// MatchesRegistration reports whether entry is the registration this channel
// was published for: content and generation both, the same predicate
// hostreg.SameRegistration wraps. It compares the registration captured at
// publish (reg), not the host the bridge was dialed with (host): that copy
// carries the executable path this Manager resolved for a host that configured
// no evener_path, which the registry entry never does, so comparing it refused
// the common case. It compares in place, so a caller pairing a live channel
// with the entry it renders — and only comparing — need not clone the host and
// its roots.
func (c *Channel) MatchesRegistration(entry hostreg.Host) bool {
	return hostreg.SameRegistration(c.reg, entry)
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
	// failure worth reporting to Manager.Close's caller. os.File reports a repeat
	// close as os.ErrClosed; a pipe reports io.ErrClosedPipe.
	if errIn != nil && !errors.Is(errIn, os.ErrClosed) && !errors.Is(errIn, io.ErrClosedPipe) {
		return errIn
	}
	if errOut != nil && !errors.Is(errOut, os.ErrClosed) && !errors.Is(errOut, io.ErrClosedPipe) {
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

func (l *linkMonitor) Write(p []byte) (int, error) {
	n, err := l.rw.Write(p)
	if err != nil || n != len(p) {
		// The send-side peer of Read's EOF: a write that failed — or put only part
		// of a frame on the wire — desynchronizes the stream and leaves every later
		// request broken. Without this signal a Send failure left isLost false, and
		// liveChannel kept handing out a channel whose writes all fail.
		l.once.Do(l.onErr)
	}
	return n, err
}

func (l *linkMonitor) Close() error { return l.rw.Close() }
