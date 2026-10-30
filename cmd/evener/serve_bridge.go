package main

import (
	"context"
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// serveBridge owns what attaches the daemon to each session's event stream: the
// bridge drains that teardown waits for, and the mutation runner that an
// interrupt cancels and waits on. thread/clear, the input loop and shutdown
// all act on it, so the locks that keep a drain from being missed live here.
type serveBridge struct {
	srv  serveServer
	deps serveDeps
	// eventObserver runs ON the bridge goroutine, which is the daemon's
	// authoritative consumer, so it must never block: see verboseEventTee.
	// Both observer funcs are nil unless --verbose.
	eventObserver      func(events.SessionEvent)
	closeEventObserver func()

	// Every bridge drain this serve starts, so teardown can wait for them.
	// A session gets a new one on thread/clear, and each ends when its own session's
	// event channel closes.
	drainsMu             sync.Mutex
	bridgeStartMu        sync.Mutex
	bridgeDrains         []<-chan struct{}
	bridgeDrainBySession map[string]<-chan struct{}
	// teardownStarted says the snapshot in awaitDrains has already been taken, so no
	// later drain can ever appear in it. bridgeSession refuses to start one
	// past this point; see the refusal there for why that is the right answer.
	teardownStarted    bool
	shutdownExpiryOnce sync.Once
	shutdownExpiry     <-chan struct{}
	shutdownExpiryStop chan struct{}

	notifyCallback     func()
	subscriberCallback func() int

	mutationRunnerMu     sync.Mutex
	mutationRunnerCancel context.CancelFunc
	mutationRunnerDone   <-chan struct{}
}

func newServeBridge(srv serveServer, deps serveDeps, eventObserver func(events.SessionEvent), closeEventObserver func()) *serveBridge {
	return &serveBridge{
		srv:                  srv,
		deps:                 deps,
		eventObserver:        eventObserver,
		closeEventObserver:   closeEventObserver,
		bridgeDrainBySession: make(map[string]<-chan struct{}),
		shutdownExpiryStop:   make(chan struct{}),
	}
}

func (b *serveBridge) sharedShutdownExpiry() <-chan struct{} {
	b.shutdownExpiryOnce.Do(func() {
		source := b.deps.drainWaitExpiry()
		done := make(chan struct{})
		b.shutdownExpiry = done
		go func() {
			defer close(done)
			select {
			case <-source:
			case <-b.shutdownExpiryStop:
			}
		}()
	})
	return b.shutdownExpiry
}

// awaitDrains waits for every bridge drain this serve started, then closes the
// event observer. It is the serve loop's deferred teardown.
//
// The tee must OUTLIVE every drain. Session.Close() closes the event channel
// but does not wait for the buffered tail, so a drain is still calling the
// observer after the session is closed -- and observe on a closed tee panics
// on the drain's own goroutine, which kills the process and skips every
// defer registered above this one. Waiting and closing live in ONE defer so
// the order cannot be broken by inserting another defer between them.
//
// THE WAIT IS BUDGETED, AND EXPIRY SKIPS THE CLOSE. A drain wedged inside
// BridgeEvent -- on a lock, or on a subsystem that stopped answering --
// must not make the daemon ignore SIGTERM until something SIGKILLs it, so
// the wait gives up after shutdownDrainWaitBudget.
//
// Giving up means LEAVING THE TEE OPEN, not closing it anyway. Closing it
// under a live drain is the exact crash this defer exists to prevent, so an
// expiry that closed anyway would trade a hang for the bug rather than fix
// either. An abandoned tee costs a truncated NDJSON tail on a process that
// is exiting regardless, and the OS reclaims the goroutine and the buffer.
//
// The budget bounds the pathological case ONLY; it must never absorb real
// work, because a budget that can expire on a slow machine quietly converts
// a slowness bug into a truncated log. A healthy drain is the session's
// buffered tail -- at most its 256-deep channel, each event memory work plus
// at most one jobs.jsonl read -- which is milliseconds, and still under
// 100ms with the deliberately slowed observer serve_verbose_e2e_test uses.
// Thirty seconds is two orders of magnitude past that, so an expiry here
// means a genuine wedge, and it stays inside systemd's 90s default stop
// timeout so the daemon still gets to exit by its own decision.
//
// "Genuine wedge" is a claim about every drain that can reach this wait, and
// it holds because every session this daemon makes current is closed by
// someone: shutdown's one pass (closeForShutdown) over whatever was live
// when it ran, or the thread/clear that installed a replacement past that pass and
// therefore closes its own. Without that second half, a thread/clear landing in
// the window between the pass and this snapshot registered a drain on a
// session nothing would ever close, which no budget can end -- the expiry
// fired every time, on work that was neither wedged nor real.
func (b *serveBridge) awaitDrains() {
	defer close(b.shutdownExpiryStop)
	b.bridgeStartMu.Lock()
	b.drainsMu.Lock()
	pending := append([]<-chan struct{}(nil), b.bridgeDrains...)
	// The bridge-start lock prevents bridgeSession from slipping a drain
	// behind this snapshot while b.drainsMu protects the list itself.
	b.teardownStarted = true
	b.drainsMu.Unlock()
	b.bridgeStartMu.Unlock()
	// One budget for the whole teardown, not one per drain: what must be
	// bounded is how long SIGTERM goes unanswered.
	expiry := b.sharedShutdownExpiry()
	for _, drained := range pending {
		select {
		case <-drained:
		case <-expiry:
			return
		}
	}
	if b.closeEventObserver != nil {
		b.closeEventObserver()
	}
}

func (b *serveBridge) setMutationRunner(cancel context.CancelFunc, done <-chan struct{}) {
	b.mutationRunnerMu.Lock()
	b.mutationRunnerCancel = cancel
	b.mutationRunnerDone = done
	b.mutationRunnerMu.Unlock()
}

func (b *serveBridge) clearMutationRunner(done <-chan struct{}) {
	b.mutationRunnerMu.Lock()
	if b.mutationRunnerDone == done {
		b.mutationRunnerCancel = nil
		b.mutationRunnerDone = nil
	}
	b.mutationRunnerMu.Unlock()
}

func (b *serveBridge) cancelAndWaitMutationRunner() {
	b.mutationRunnerMu.Lock()
	cancelRunner := b.mutationRunnerCancel
	runnerDone := b.mutationRunnerDone
	b.mutationRunnerMu.Unlock()
	if cancelRunner != nil {
		cancelRunner()
	}
	if runnerDone != nil {
		<-runnerDone
	}
}

func (b *serveBridge) bridgeSession(s *agent.Session) {
	// The idle kick is set on the Session, so it must be re-established
	// whenever the session is replaced (e.g. on thread/clear). It feeds the
	// first continuation prompt back into the serve loop's input channel
	// as an EntryContinuation-kind message (the agent module must not
	// import server, so this is a callback into the server, spec §C4/§7).
	s.SetKickFunc(func(prompt string) { b.srv.SubmitContinuation(prompt) })
	// The notify wake is wired alongside the kick: when a child finishes
	// and the parent is idle, the durable notification queue is already
	// populated; this callback feeds a text-less EntryNotification kick
	// into the serve loop so the parent drains it on the next turn.
	b.notifyCallback = func() { b.srv.SubmitNotification() }
	s.SetNotifyFunc(b.notifyCallback)
	// Each execution the session starts publishes its TurnID, with the
	// thread active, before its first entry is recorded, so clients hold
	// the running turn before any of its history arrives.
	s.SetExecutionStartedFunc(b.srv.SetProcessingTurn)
	s.SetClientMutationStartWakeFunc(func() {
		b.srv.SubmitClientMutationStart(s.ID())
	})
	s.SetPendingUserInputWakeFunc(func() {
		b.srv.SubmitPendingUserInput(s.ID())
	})
	// Descendants do not have their own daemon or authoritative event
	// consumer. Forward their enriched events synchronously into this
	// daemon's per-thread AppWire projections instead.
	ownerThreadID := s.ID()
	s.SetDescendantEventFunc(func(event events.SessionEvent) {
		b.srv.RecordDescendantAppEvent(ownerThreadID, event)
	})
	// A descendant's transcript lives alongside its owner's, under the same
	// state dir convention every other session/transcript lookup in this
	// codebase uses. A descendant's history is projected from it, from
	// its first observation on (ledger #110/#111).
	stateDir := s.StateDir()
	b.srv.SetDescendantTranscriptPathFunc(func(threadID string) string {
		return schema.SessionTranscriptPath(stateDir, threadID)
	})
	// A descendant session's own watches live in that child's job manager and
	// appear on no row today, because only the root's status is projected. The
	// root's own row also carries watches the diagnostics facet only re-samples
	// on a few events and a turn boundary, so it can lag a watch armed in
	// between. The appwire thread LIST samples both on read through this seam,
	// resolving the root to its own rows and a descendant to that child's, for
	// every row of a page in one walk of the live tree. Wire it beside the
	// transcript resolver: both reach across the appwire-server/delegate-controller
	// boundary, and both must track the session that is current after a
	// thread/clear identity swap.
	b.srv.SetDescendantLiveWatchesFunc(func(threadIDs []string) map[string][]agent.WatchStatusInfo {
		return s.LiveWatchRowsForSessions(threadIDs)
	})
	// The root's row carries its whole tree's subagent tally (S3), read from
	// the delegate controller when the list is served. Set per session so it
	// follows the replacement a thread/clear installs.
	b.srv.SetSubagentTallyFunc(s.SubagentTally)
	// The M7 sandbox-escalation gate blocks a denied tool call only when a human
	// is actually watching this thread; the probe reads the live AppWire
	// subscriber count. Set per-session (like the kick/notify wakes) so it tracks
	// the current session's id across thread/clear.
	b.subscriberCallback = func() int { return b.deps.subscriberCount(b.srv, s.ID()) }
	s.SetSubscriberCountFunc(b.subscriberCallback)
	// Called synchronously, NOT with `go`: the bridge registers as the
	// session's authoritative consumer and then drains on its own
	// goroutine, so returning here means the registration has taken effect.
	// Spawning this would reopen the window in which the session is live
	// and its feed is still best-effort.
	//
	// The bridge start and drain registration are one critical section with
	// teardown, but the bridge call itself must stay outside b.drainsMu because
	// a closed session invokes onDrained synchronously.
	drained := make(chan struct{})
	b.bridgeStartMu.Lock()
	defer b.bridgeStartMu.Unlock()
	// Past the teardown's snapshot, a drain started here would be one the
	// wait cannot see, and the tee would be closed under it -- the same
	// crash the wait exists to prevent, reached from the other side. The
	// lock that makes the registration atomic makes this mutually exclusive
	// with the snapshot for free.
	//
	// Refusing leaves this session with no authoritative consumer, which
	// anywhere else on this branch is the corruption being prevented. Here
	// it costs nothing: the only caller that can reach this is thread/clear on a
	// handler goroutine that outlived httpSrv.Close(), and by the time the
	// snapshot has been taken the listener is closed, the rendezvous entry
	// has been removed, and the input loop has exited. The replacement
	// cannot take a turn and no client can read it, so there is no reader
	// for its projection to be permanently wrong for.
	//
	// It costs nothing on disk either, and that is a SEPARATE guarantee
	// rather than a consequence of this refusal: a thread/clear reaching here
	// installed its replacement past shutdown's closing pass, so it has
	// already closed it, which is what runs the env's Cleanup()
	// (agent/execenv/local.go) and disposes the session's owned scratch
	// directory. Left unclosed that directory is disk state -- unlike the
	// tee's buffered tail above, which the OS reclaims once this process
	// exits, it survives AFTER exit. Do not read this refusal as the thing
	// that prevents that: declining to bridge a session and closing one are
	// different acts, and only the second reaches the env.
	b.drainsMu.Lock()
	teardown := b.teardownStarted
	b.drainsMu.Unlock()
	if teardown {
		return
	}
	b.deps.bridge(b.srv, s, b.eventObserver, func() {
		close(drained)
		b.drainsMu.Lock()
		if b.bridgeDrainBySession[s.ID()] == drained {
			delete(b.bridgeDrainBySession, s.ID())
		}
		b.drainsMu.Unlock()
	})
	b.drainsMu.Lock()
	b.bridgeDrains = append(b.bridgeDrains, drained)
	b.bridgeDrainBySession[s.ID()] = drained
	completed := false
	select {
	case <-drained:
		completed = true
	default:
	}
	if completed && b.bridgeDrainBySession[s.ID()] == drained {
		delete(b.bridgeDrainBySession, s.ID())
	}
	b.drainsMu.Unlock()
}

func (b *serveBridge) waitForSessionBridgeDrain(sessionID string) bool {
	b.drainsMu.Lock()
	drained := b.bridgeDrainBySession[sessionID]
	b.drainsMu.Unlock()
	if drained == nil {
		return true
	}
	select {
	case <-drained:
		b.drainsMu.Lock()
		if b.bridgeDrainBySession[sessionID] == drained {
			delete(b.bridgeDrainBySession, sessionID)
		}
		b.drainsMu.Unlock()
		return true
	default:
	}
	select {
	case <-drained:
		b.drainsMu.Lock()
		if b.bridgeDrainBySession[sessionID] == drained {
			delete(b.bridgeDrainBySession, sessionID)
		}
		b.drainsMu.Unlock()
		return true
	case <-b.sharedShutdownExpiry():
		return false
	}
}
