package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
)

// markRecoveryClear records resp's requestIDKey as the force-stop response
// whose transmission should clear recoveryRunning. It must be called before
// resp is enqueued, while recoveryRunning is still true (see the comment in
// receiveInbound): the flag has to survive the enqueue itself, and beforeSend
// clears it only once the send loop dequeues this exact response, right
// before it reaches the transport.
func (c *Connection) markRecoveryClear(resp appwire.Message) string {
	id, _ := responseHydrationOutcome(resp)
	if id == "" {
		return ""
	}
	c.mu.Lock()
	c.recoveryClearID = id
	c.mu.Unlock()
	return id
}

// clearRecoveryOnPanic runs fn, and if fn panics, rolls back the force-stop
// busy flag before letting the panic continue to fn's own caller. It exists
// for the one gap markRecoveryClear/beforeSend otherwise leaves: fn is the
// enqueue step, and beforeSend — the thing that normally clears the flag —
// only ever runs once the send loop dequeues the response fn was supposed to
// place on the send channel. If fn panics first, that response never gets
// there, beforeSend never runs for it, and without this rollback the
// connection would refuse every force stop from then on. The panic itself
// is deliberately not recovered here — handleRecovered's own barrier (fn
// runs from inside its onResponse continuation) still logs it and answers
// the request — this only repairs the connection-local state a panic mid
// enqueue would otherwise strand.
//
// The rollback only clears state still keyed to id (the response this call
// marked via markRecoveryClear), comparing exactly like beforeSend/afterSend
// do: fn can panic after the send loop already transmitted this response and
// admitted a newer force stop, and an unconditional clear would wipe that
// newer force stop's state instead of just this one's.
func (c *Connection) clearRecoveryOnPanic(id string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.mu.Lock()
			if c.recoveryClearID == id {
				c.recoveryClearID = ""
				c.recoveryRunning = false
			}
			c.mu.Unlock()
			panic(r)
		}
	}()
	fn()
}

// concurrentDispatchMethod reports whether a request method leaves the
// receive loop for its own goroutine. The set is exactly the known-slow read
// methods — the ones that scan a full transcript and can park for a while
// (thread/read is the handler the original ping-starvation bug named;
// thread/turns/list and evener/subagentPreview walk the same saved
// transcripts). Everything else — every mutation and every other read — is
// bounded work and runs inline, keeping the per-connection ordering those
// handlers were written against. A method added here must be safe to run
// out of order against every other request on the connection.
func concurrentDispatchMethod(method string) bool {
	switch method {
	case appwire.MethodThreadRead, appwire.MethodThreadTurnsList, appwire.MethodEvenerSubagentPreview,
		// evener/update/check is a read-only network comparison with no
		// shared-state writes; running it inline blocks unrelated RPCs
		// on the connection for up to two 10s GitHub timeouts. The
		// frontend already discards stale check responses by sequence.
		appwire.MethodEvenerUpdateCheck,
		// evener/update/apply is a multi-minute download, verify, and
		// install under the overall upgrade deadline; running it inline
		// holds the connection's serial worker that whole time. Safe
		// out of order: hubUpdateMu plus the cross-process install lock
		// fail a concurrent apply fast, and the restart waits on its own
		// response flush rather than connection order.
		appwire.MethodEvenerUpdateApply,
		// evener/upgrade is the install-only half of the same
		// operation: a multi-minute download, verify, and write under
		// the overall upgrade deadline. It takes the same hubUpdateMu
		// and install lock, so a concurrent install fails fast rather
		// than interleaving, and the phone's install action otherwise
		// holds its session sends for that whole time.
		appwire.MethodEvenerUpgrade,
		// evener/activity/read waits up to its per-host budget on each
		// attached host, and a phone polls it every ten seconds on the
		// connection its sends ride. It writes nothing, and each answer
		// replaces the last, so order does not matter to the poller.
		appwire.MethodEvenerActivityRead,
		// evener/jobs/list builds the whole activity tree: seconds and
		// megabytes for a delegate-heavy session, and the web UI refetches
		// it on every delegate update on the socket its navigation rides.
		// It writes nothing (the shared fold caches are mutex-guarded and
		// already serve every connection at once), and the client asks for
		// one tree at a time and discards a response that is stale or from
		// another revision, so answer order does not matter.
		appwire.MethodEvenerJobsList,
		// evener/plugin/checkUpdates waits on every installed plugin's git
		// remote (each under its own timeout, a few at a time) when a
		// plugins view opens. It changes nothing installed (the answers it
		// keeps are guarded by the plugins manager's own mutex), and each
		// answer replaces the last, so order does not matter.
		appwire.MethodEvenerPluginCheckUpdates,
		// Session activity reads may reconstruct retained journals. They are
		// read-only and carry request cancellation into bounded reconstruction.
		appwire.MethodEvenerThreadActivityRead,
		appwire.MethodEvenerThreadDelegatesList,
		appwire.MethodEvenerThreadJobsList,
		appwire.MethodEvenerThreadWatchesList:
		return true
	}
	return false
}

// receiveInbound applies the connection's inbound policy to one frame on
// behalf of the transport's receive loop, which holds only transport
// concerns: a ping request is answered inline through the shared
// handleAndEnqueue barrier, bypassing the request queue — one ping
// implementation, one panic barrier, no hand-built response beside the real
// path that could drift from it. Initialized force-stop requests have one
// independent recovery slot; every other frame is enqueued for the serial worker. False means the connection died while blocked on a full
// queue and the loop should return. A second transport inherits the whole
// policy by driving this same entry point.
//
// The inline ping response enqueues through enqueueResponse, which parks on
// a full outbound channel until the send loop drains it or the write-timeout
// cascade cancels the connection: ping liveness is guaranteed against
// handler behavior, not against a peer that stopped draining its own socket.
func (c *Connection) receiveInbound(ctx context.Context, msg appwire.Message) bool {
	if msg.Request != nil && c.server.cfg.RequestAdmissionContext != nil {
		admitted, ok := c.admitRequestContext(ctx, msg)
		if !ok {
			c.enqueueDispatched(ctx, appwire.ErrorMessage(msg.Request.ID, appwire.InternalError("internal error admitting request")))
			return true
		}
		ctx = admitted
	}
	if msg.Request != nil && msg.Request.Method == appwire.MethodPing {
		c.handleAndEnqueue(ctx, msg)
		return true
	}
	if msg.Request != nil && msg.Request.Method == appwire.MethodEvenerThreadForceStop && c.isInitialized() {
		c.mu.Lock()
		// recoveryClearID != "" covers the gap between beforeSend clearing
		// recoveryRunning and afterSend confirming the response actually
		// reached the transport: without it, a force stop whose write is
		// merely slow (not blocked behind a full buffer, which
		// recoveryRunning alone already covers) would be admitted while the
		// prior one is still being transmitted.
		//
		// This does trade back a sliver of the #2469 guarantee: between
		// transport.Send handing the frame to the socket and afterSend
		// running (a handful of instructions in the same goroutine, no I/O),
		// a client that received the response and immediately fires a
		// sequential force stop could still see a spurious "already
		// running" refusal. Closing that sliver would mean clearing
		// recoveryClearID in beforeSend too - which is exactly what would
		// reopen the (much larger, blocked-write-duration) admission window
		// this field exists to close. Deliberately kept as an over-refusal
		// this narrow rather than reintroduced as an under-refusal this
		// wide.
		busy := c.recoveryRunning || c.recoveryClearID != ""
		if !busy {
			c.recoveryRunning = true
		}
		c.mu.Unlock()
		if busy {
			// This enqueue runs directly in the receive loop's goroutine, with
			// no handler and so no handleRecovered call to barrier it — recoverPanic
			// is the barrier here, the same one handleRecovered uses internally,
			// so a panic enqueueing the refusal can't crash this goroutine (and,
			// since nothing above it recovers either, the whole process) any more
			// than a panic enqueueing an admitted force stop's real response can.
			c.recoverPanic(msg, func() {
				c.enqueueDispatched(ctx, appwire.ErrorMessage(msg.Request.ID, appwire.Unavailable("force stop is already running")))
			})
			return true
		}
		// Recovery must reach ownership cancellation even when the serial worker
		// is awaiting an unresponsive daemon. Other mutations retain FIFO order.
		// recoveryRunning stays true across this whole dispatch, including the
		// enqueue: enqueueResponse can block for a long time against a peer
		// that has stopped draining (a full outbound buffer parks it until the
		// send loop catches up or the connection ends), and clearing the flag
		// any earlier than that would let a flood of force stops pile up
		// dispatch goroutines behind it with no bound — the same unbounded
		// backlog the flag exists to prevent for every other mutation.
		//
		// markRecoveryClear instead defers the clear to beforeSend, which the
		// send loop runs immediately before handing this exact response to the
		// transport. That keeps the ordering guarantee the flag depends on: a
		// channel send happens before the corresponding receive completes, so
		// the send loop can never dequeue (and so never transmit) this
		// response before beforeSend has already cleared the flag — a client
		// that pipelines a second force stop right behind the first can never
		// observe the flag still set for a recovery that has already been
		// handed to the transport.
		//
		// clearRecoveryOnPanic covers the gap that leaves: if enqueueDispatched
		// panics before the response ever reaches the send channel, beforeSend
		// — the only other thing that clears the flag — never runs for it, and
		// the connection would refuse every force stop from then on. The panic
		// itself is still handleRecovered's to log and contain; this only
		// rolls the flag back first.
		go func() {
			c.handleRecovered(ctx, msg, func(resp appwire.Message) {
				id := c.markRecoveryClear(resp)
				c.clearRecoveryOnPanic(id, func() { c.enqueueDispatched(ctx, resp) })
			})
		}()
		return true
	}
	return c.enqueueRequest(ctx, msg)
}

// backpressureRefusal mints the in-band refusal for a retry-safe mutation
// arriving while the inbound queue is at or beyond half its capacity — the
// depth at which the queue is demonstrably backlogged, while the connection
// still carries writes. The receive loop, enqueueRequest's only caller and
// this method's only caller, is the queue's sole producer, so len is exact
// at the check with no lock. A refusal never enqueues, so it consumes no
// queue depth; reads and other non-mutation frames keep the FIFO pressure
// valve at every depth. The refusal precedes the worker's params validation:
// a malformed mutation refused under load surfaces its real invalid-params
// error on the retried attempt, and a request whose params carry no
// extractable clientMutationId is still refused — the frame is never queued
// either way.
func (c *Connection) backpressureRefusal(msg appwire.Message) (appwire.Message, bool) {
	request := msg.Request
	if request == nil {
		return appwire.Message{}, false
	}
	highWater := (cap(c.requests) + 1) / 2
	if len(c.requests) < highWater {
		return appwire.Message{}, false
	}
	if !appwire.RetrySafeMutationMethod(request.Method) {
		return appwire.Message{}, false
	}
	return appwire.ErrorMessage(request.ID, appwire.BackpressureRefusal(clientMutationIDOf(request.Params),
		"the hub's inbound queue is busy and did not accept this message")), true
}

// clientMutationIDOf extracts the refusal's clientMutationId from the
// request's raw params with a minimal decode of the one field every
// retry-safe mutation carries. The extracted id is never logged. Params that
// do not decode, or carry no id, give "".
func clientMutationIDOf(params json.RawMessage) string {
	var only struct {
		ClientMutationID string `json:"clientMutationId"`
	}
	if err := json.Unmarshal(params, &only); err != nil {
		return ""
	}
	return only.ClientMutationID
}

// enqueueRequest pushes one inbound frame onto the connection's bounded
// request queue on behalf of the transport's receive loop, which is the only
// producer. A full queue blocks until the worker frees a slot or the
// connection context ends; false means the connection died while blocked and
// the loop should return. Blocking is the pressure valve: the parked loop
// stops calling Recv, inbound frames accumulate in the kernel socket buffer,
// and TCP flow control eventually reaches the client — no wire error, no
// eviction. A client that pipelines deeper than the queue experiences
// exactly the ordering it asked for, applied at the transport instead of in
// server memory.
func (c *Connection) enqueueRequest(ctx context.Context, msg appwire.Message) bool {
	if refusal, ok := c.backpressureRefusal(msg); ok {
		// The first refusal on the connection gets its own advisory, worded
		// for this branch: the frame is answered in band, the receive loop
		// keeps reading, and only mutations are turned away. A separate
		// one-shot from the full-queue block advisory below, which must stay
		// available to name the wedge signature when a non-mutation frame
		// later parks the loop.
		if !c.queueHighWaterAdvised {
			c.queueHighWaterAdvised = true
			c.server.logf("appserver: connection %s inbound request queue at high-water depth (%d of %d frames); refusing mutation requests in band until the worker drains", c.id, len(c.requests), cap(c.requests))
		}
		// This enqueue runs directly in the receive loop's goroutine, with no
		// handler and so no handleRecovered call to barrier it — recoverPanic
		// is the barrier here, the same one the force-stop busy refusal in
		// receiveInbound uses. Delivery can park on a full outbound buffer
		// until the connection ends: the same acknowledged trade the inline
		// ping path makes.
		c.recoverPanic(msg, func() {
			c.enqueueDispatched(ctx, refusal)
		})
		return true
	}
	request := admittedRequest{ctx: ctx, message: msg}
	select {
	case c.requests <- request:
		return true
	default:
	}
	// Saturation is a healthy server applying flow control, but it is also
	// the one state where ping and dead-peer detection wait on the client's
	// own backlog, so its first occurrence per connection gets an advisory —
	// the same channel evictSlowConsumer uses, and the proportionate version
	// of a metric this package does not have.
	if !c.queueSaturationAdvised {
		c.queueSaturationAdvised = true
		c.server.logf("appserver: connection %s inbound request queue is full (%d frames); blocking the receive loop until the worker frees a slot", c.id, cap(c.requests))
	}
	if c.server.blockedEnqueue != nil {
		c.server.blockedEnqueue()
	}
	select {
	case c.requests <- request:
		return true
	case <-ctx.Done():
		return false
	}
}

// runWorker is the connection's serial worker: the only consumer of the
// request queue, started by the transport beside its send loop. It drains
// the queue strictly in arrival order and applies the dispatch policy in
// executeOrdered, so per-connection ordering is preserved for every queued
// frame while the receive loop stays parked in Recv — no handler can starve
// the transport's ping answering, close handling, or dead-peer detection.
//
// The ordering contract, for two requests A before B on one connection:
// earlier serial requests block all later work; nothing waits for a slow
// read; ping (answered in the receive loop, never queued) waits for no
// queued work. Slow reads are unordered among themselves, and a serial
// request issued after a slow read runs without waiting for it. Responses
// always pair by request id, and only the sequence-cut discipline governs
// how notifications interleave with a hydration response.
//
// Cancellation: the connection context ending is the only cancellation this
// transport has. No request begins executing after the worker has observed
// cancellation, and it observes at every dequeue — the post-dequeue re-check
// below, because select chooses randomly when both cases are ready. Requests
// still queued behind the observation point never execute; a request that
// never started has no side effects and no peer remains to answer.
func (c *Connection) runWorker(ctx context.Context) {
	defer close(c.workerExited)
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-c.requests:
			msg := request.message
			if c.server.afterWorkerDequeue != nil {
				c.server.afterWorkerDequeue(msg)
			}
			// select chooses randomly when both cases are ready, so a
			// canceled connection can still win a dequeue; re-check before
			// executing so no request starts after cancellation is
			// observable here.
			if ctx.Err() != nil {
				return
			}
			c.executeOrdered(request.ctx, msg)
		}
	}
}

// purgeRequestQueue discards every frame still buffered in the request queue
// at teardown. The transport calls it after its receive loop — the queue's
// only producer — has returned and the connection context is canceled; that
// ownership rule is what makes draining safe. The worker may race the purge
// by winning a dequeue, but its post-dequeue re-check discards the message
// just the same: both sides only ever discard. Without the purge, an
// orphaned handler that ignores its canceled context would retain the worker
// goroutine, through it the Connection, and through that up to a full
// queue's worth of decoded frames; after it, such a handler retains exactly
// the one frame it is executing.
//
// A non-empty purge reports one bounded advisory line: the count plus a
// per-method tally, catalog methods by name and everything else aggregated
// as unknown — never params, which can carry user content. This is an
// aggregate teardown advisory, not request-level attribution; "did my
// mutation run?" belongs to the ClientMutationID dedup on reconnect.
func (c *Connection) purgeRequestQueue() {
	discarded := 0
	tally := map[string]int{}
drain:
	for {
		select {
		case request := <-c.requests:
			msg := request.message
			discarded++
			// Tally only cataloged names verbatim: the method field is
			// client-controlled and the transport read limit admits very
			// large strings, so an uncataloged value could carry arbitrary
			// size or control characters into the log.
			name := methodOf(msg)
			if !appwire.KnownWireName(name) {
				name = "unknown"
			}
			tally[name]++
		default:
			break drain
		}
	}
	if discarded == 0 {
		return
	}
	c.server.logf("appserver: connection %s discarded %d undispatched queued frames at teardown (%s)", c.id, discarded, formatTally(tally))
}

// formatTally renders a per-method tally as a deterministic "name=count"
// list, shared by the teardown purge and the stall advisory.
func formatTally(tally map[string]int) string {
	names := make([]string, 0, len(tally))
	for name := range tally {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, tally[name]))
	}
	return strings.Join(parts, " ")
}

// executeOrdered applies the dispatch policy to one dequeued frame: the
// slow-read methods concurrentDispatchMethod names, and the requests the
// server's ConcurrentRequest admits, spawn onto their own goroutine — a
// full-transcript read or a remote host's answer cannot head-of-line block
// the connection — and everything else, initialize and notifications included,
// executes inline in the worker so handlers keep the per-connection ordering
// they were written against.
//
// Ordering constraints in detail:
//
//   - initialize must be the first request. Pre-initialize frames ride the
//     queue like everything else; the worker executes them in order, and
//     HandleMessage's gate answers non-initialize requests with "initialize
//     required" while the handshake itself completes — response enqueued —
//     before any later frame is dequeued. Later dispatch therefore cannot
//     observe a half-initialized connection; the isInitialized check below
//     is exact rather than racy because the worker is the goroutine that
//     sets it.
//   - responses enter the connection send queue through the same
//     enqueueResponse path on both dispatch modes, so hydration capture
//     commit/abort ordering is unchanged.
//
// Error responses from enqueueResponse are terminal for the connection, but
// a handler goroutine must not tear the worker down out from under it;
// canceling the shared context is enough — the worker exits at its next
// dequeue and the receive loop's next Recv fails into normal close handling.
func (c *Connection) executeOrdered(ctx context.Context, msg appwire.Message) {
	var resolverPanic bool
	var resolvedKey string
	var secondaryKey string
	var resolved bool
	intent := SubscriptionAdmissionNotSubscribe
	if resolve := c.server.cfg.SubscriptionAdmissionResolverV2; resolve != nil && msg.Request != nil && c.isInitialized() && (msg.Request.Method == appwire.MethodThreadRead || msg.Request.Method == appwire.MethodThreadUnsubscribe) {
		var result SubscriptionAdmissionResolution
		result, resolverPanic = safeResolveAdmissionV2(c.server, resolve, msg)
		resolvedKey, secondaryKey, resolved, intent = result.Key, result.SecondaryKey, result.Intent == SubscriptionAdmissionResolved, result.Intent
	} else if resolve := c.server.cfg.SubscriptionAdmissionResolver; resolve != nil && msg.Request != nil && c.isInitialized() && (msg.Request.Method == appwire.MethodThreadRead || msg.Request.Method == appwire.MethodThreadUnsubscribe) {
		resolvedKey, resolved, resolverPanic = safeResolveAdmission(c.server, resolve, msg)
		if msg.Request.Method == appwire.MethodThreadRead && threadReadAdmissionEligible(msg.Request.Params) {
			intent = SubscriptionAdmissionResolved
			if !resolved || resolvedKey == "" {
				intent = SubscriptionAdmissionUnresolved
			}
		}
	}
	if resolverPanic {
		ctx = context.WithValue(ctx, subscriptionAdmissionResolverPanicContextKey{}, true)
	} else if msg.Request != nil && msg.Request.Method == appwire.MethodThreadRead && threadReadAdmissionEligible(msg.Request.Params) && intent == SubscriptionAdmissionUnresolved {
		ctx = context.WithValue(ctx, subscriptionAdmissionFailureContextKey{}, "subscription admission is unavailable")
	}
	if msg.Request != nil && msg.Request.Method == appwire.MethodThreadUnsubscribe && c.isInitialized() {
		if (resolved || intent == SubscriptionAdmissionUnresolved) && resolvedKey != "" {
			c.cancelSubscriptionAdmissions(resolvedKey)
			ctx = context.WithValue(ctx, subscriptionLifecycleContextKey{}, resolvedKey)
			if secondaryKey != "" && secondaryKey != resolvedKey {
				c.cancelSubscriptionAdmissions(secondaryKey)
				if intent == SubscriptionAdmissionUnresolved {
					ctx = context.WithValue(ctx, subscriptionLifecycleContextKeysKey{}, []string{resolvedKey, secondaryKey})
					ctx = context.WithValue(ctx, subscriptionUnresolvedContextKey{}, true)
				}
			}
		}
	}
	if msg.Request != nil && concurrentDispatchMethod(msg.Request.Method) && c.isInitialized() {
		method := msg.Request.Method
		var admission *subscriptionAdmission
		if method == appwire.MethodThreadRead {
			if resolved && resolvedKey != "" {
				admission = c.beginSubscriptionAdmission(requestIDKey(msg.Request.ID), resolvedKey)
			}
		}
		if !c.acquireSlowReadSlot(ctx, method) {
			c.finishSubscriptionAdmission(admission)
			// Canceled while dequeued-awaiting-dispatch-capacity; the worker
			// loop observes the same cancellation at its next select and
			// exits without executing anything further.
			return
		}
		go func() {
			defer c.finishSubscriptionAdmission(admission)
			defer c.releaseSlowReadSlot(method)
			c.handleAndEnqueue(ctx, msg)
		}()
		return
	}
	if concurrent := c.server.cfg.ConcurrentRequest; msg.Request != nil && concurrent != nil && c.isInitialized() {
		if pool, ok := concurrent(msg.Request.Method, msg.Request.Params); ok {
			if refusal := c.tryRequestSlot(ctx, pool); refusal != "" {
				c.recoverPanic(msg, func() {
					c.enqueueDispatched(ctx, appwire.ErrorMessage(msg.Request.ID, appwire.Unavailable(refusal)))
				})
				return
			}
			go func() {
				defer c.releaseRequestSlot(pool)
				c.handleAndEnqueue(ctx, msg)
			}()
			return
		}
	}
	c.handleAndEnqueue(ctx, msg)
}

func safeResolveAdmission(server *Server, resolve func(appwire.Message) (string, bool), msg appwire.Message) (key string, ok, panicked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			server.panicLogf("appserver: panic resolving subscription admission for %s: %v\n%s", methodOf(msg), recovered, debug.Stack())
			key, ok, panicked = "", false, true
		}
	}()
	key, ok = resolve(msg)
	return key, ok, false
}

func safeResolveAdmissionV2(server *Server, resolve func(appwire.Message) SubscriptionAdmissionResolution, msg appwire.Message) (result SubscriptionAdmissionResolution, panicked bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			server.panicLogf("appserver: panic resolving subscription admission for %s: %v\n%s", methodOf(msg), recovered, debug.Stack())
			result, panicked = SubscriptionAdmissionResolution{}, true
		}
	}()
	return resolve(msg), false
}

func threadReadAdmissionEligible(raw json.RawMessage) bool {
	var params appwire.ThreadReadParams
	if json.Unmarshal(raw, &params) != nil || !params.Subscribe {
		return false
	}
	return appwire.ValidateThreadReadParams(params) == nil
}

// acquireSlowReadSlot takes one slowReadDispatchCap slot on behalf of the
// worker before it spawns a slow read — the request's third lifecycle state,
// dequeued awaiting dispatch capacity. A full cap parks the worker here, so
// the blocked slow read and every request queued behind it wait for one of
// the in-flight reads to finish: the design's second deliberate scheduling
// change (ping bypass is the first), blocking backpressure like the request
// queue and explicitly not the dead 128-slot limiter's wire error. The
// acquire gets the same cancellation discipline as the dequeue: it selects on
// ctx.Done so teardown is never held, and because select chooses randomly
// when a slot release and cancellation are simultaneously ready, it re-checks
// ctx.Err after acquiring — releasing the slot and reporting false on cancel,
// so no slow read starts after cancellation is observable here.
//
// Saturation is observable like queue saturation: the first blocked acquire
// per connection reports through Server.logf. And because the ping bypass
// keeps the connection's heartbeat answering while the serial lane is dead
// behind a saturated cap — a wedge PR #667 would have surfaced as a stalled
// heartbeat — a single acquire parked past the stall threshold reports again,
// naming the wait duration and the in-flight methods, so a wedged lane has an
// operational signature even though the connection looks healthy.
func (c *Connection) acquireSlowReadSlot(ctx context.Context, method string) bool {
	acquired := false
	select {
	case c.slowReadSlots <- struct{}{}:
		acquired = true
	default:
	}
	if !acquired {
		if !c.capSaturationAdvised {
			c.capSaturationAdvised = true
			c.server.logf("appserver: connection %s slow-read dispatch cap is full (%d in flight); holding the next slow read until one finishes", c.id, cap(c.slowReadSlots))
		}
		start := time.Now()
		stall := time.NewTimer(c.server.slowReadStallThreshold)
		defer stall.Stop()
		for !acquired {
			select {
			case c.slowReadSlots <- struct{}{}:
				acquired = true
			case <-stall.C:
				c.server.logf("appserver: connection %s slow-read acquire for %s has been parked %s behind in-flight reads (%s)", c.id, method, time.Since(start).Round(time.Second), c.inflightSlowReads())
			case <-ctx.Done():
				return false
			}
		}
	}
	if c.server.afterSlowReadAcquire != nil {
		c.server.afterSlowReadAcquire()
	}
	if ctx.Err() != nil {
		<-c.slowReadSlots
		return false
	}
	c.slowReadMu.Lock()
	c.slowReadInflight[method]++
	c.slowReadMu.Unlock()
	return true
}

// tryRequestSlot takes one of pool's slots for a request the server's
// ConcurrentRequest admitted, or at once returns the refusal to answer it
// with when that pool or the connection's total is full
// (concurrentRequestCap says why it never parks). A pool's first refusal each
// time it fills, and the connection total's first, report through
// Server.logf, as the slow-read cap's does.
func (c *Connection) tryRequestSlot(ctx context.Context, pool string) (refusal string) {
	// As the slow-read acquire does: a request dequeued after cancellation
	// starts nothing. The caller's refusal enqueue then fails on the same
	// cancellation, which tears the connection down anyway.
	if err := ctx.Err(); err != nil {
		return err.Error()
	}
	c.requestPoolsMu.Lock()
	poolFull := c.requestPools[pool] >= concurrentRequestCap
	totalFull := !poolFull && c.requestsInFlight >= concurrentRequestTotalCap
	if !poolFull && !totalFull {
		c.requestPools[pool]++
		c.requestsInFlight++
	}
	advisePool := poolFull && !c.requestPoolSaturationAdvised[pool]
	if advisePool {
		c.requestPoolSaturationAdvised[pool] = true
	}
	c.requestPoolsMu.Unlock()
	switch {
	case poolFull:
		refusal = fmt.Sprintf("this connection already has %d requests waiting on slow answers from %q; try again shortly", concurrentRequestCap, pool)
		if advisePool {
			c.server.logf("appserver: connection %s concurrent request pool is full for %q (%d in flight); refusing its further requests as Unavailable until one finishes", c.id, pool, concurrentRequestCap)
		}
	case totalFull:
		refusal = fmt.Sprintf("this connection already has %d requests waiting on slow answers; try again shortly", concurrentRequestTotalCap)
		if !c.requestTotalSaturationAdvised {
			c.requestTotalSaturationAdvised = true
			c.server.logf("appserver: connection %s concurrent request pools are full in total (%d in flight); refusing further admitted requests as Unavailable until one finishes", c.id, concurrentRequestTotalCap)
		}
	}
	return refusal
}

// releaseRequestSlot frees the pool slot tryRequestSlot took, dropping a pool
// with nothing left in flight, and its advisory mark, so the maps hold only
// pools in use.
func (c *Connection) releaseRequestSlot(pool string) {
	c.requestPoolsMu.Lock()
	defer c.requestPoolsMu.Unlock()
	c.requestsInFlight--
	c.requestPools[pool]--
	if c.requestPools[pool] <= 0 {
		delete(c.requestPools, pool)
		delete(c.requestPoolSaturationAdvised, pool)
	}
}

// releaseSlowReadSlot frees the cap slot when a slow read's handleAndEnqueue
// returns — completion, not response delivery, is what drains the cap.
func (c *Connection) releaseSlowReadSlot(method string) {
	c.slowReadMu.Lock()
	c.slowReadInflight[method]--
	if c.slowReadInflight[method] <= 0 {
		delete(c.slowReadInflight, method)
	}
	c.slowReadMu.Unlock()
	<-c.slowReadSlots
}

// inflightSlowReads renders the per-method tally the stall advisory names.
// Every key is a concurrentDispatchMethod member, so the strings are ours,
// not client-controlled.
func (c *Connection) inflightSlowReads() string {
	c.slowReadMu.Lock()
	defer c.slowReadMu.Unlock()
	return formatTally(c.slowReadInflight)
}

// handleAndEnqueue runs one request through HandleMessage and enqueues its
// response. It is the panic barrier for handler code: a panicking handler is
// logged with its stack and answered with an InternalError response, and the
// connection lives on. Handlers dispatched on their own goroutine have no
// other recover between them and the runtime (the net/http barrier only
// covers the receive loop's goroutine), and the inline path shares the
// barrier so both dispatch modes contain a panic identically.
func (c *Connection) handleAndEnqueue(ctx context.Context, msg appwire.Message) {
	c.handleRecovered(ctx, msg, func(resp appwire.Message) {
		c.enqueueDispatched(ctx, resp)
	})
}

// handleRecovered is handleAndEnqueue's panic barrier as a continuation:
// onResponse runs from inside the same defer/recover that covers
// HandleMessage, so whatever onResponse does with the response — enqueue it,
// or the force-stop dispatch's clear-then-enqueue — is covered by the
// barrier exactly like the handler itself. Every onResponse call, on either
// path below, goes through deliver, which recovers its own panics: a panic
// from onResponse is logged and swallowed there without a follow-up
// response (retrying it risks a duplicate frame, and a client that gets
// nothing back still has its own request timeout), and — because deliver
// never lets that panic reach this function's own defer — it can never
// re-enter (and escape) the outer barrier either, including when onResponse
// panics while delivering the recover branch's synthesized error.
//
// This continuation shape is why onResponse exists at all rather than a
// return value: an earlier version returned the response and left the caller
// to enqueue it outside this function, which quietly narrowed the barrier to
// HandleMessage alone — a panic during the enqueue itself (a real,
// independent failure mode: enqueueResponse touches hydration bookkeeping
// beyond the plain channel send) could then escape the dispatch goroutine
// uncaught.
func (c *Connection) handleRecovered(ctx context.Context, msg appwire.Message, onResponse func(appwire.Message)) {
	deliver := func(resp appwire.Message) {
		c.recoverPanic(msg, func() { onResponse(resp) })
	}
	defer func() {
		if r := recover(); r != nil {
			c.server.panicLogf("appserver: panic handling %s: %v\n%s", methodOf(msg), r, debug.Stack())
			if msg.Request != nil {
				deliver(appwire.ErrorMessage(msg.Request.ID, appwire.InternalError("internal error handling request")))
			}
		}
	}()
	deliver(c.HandleMessage(ctx, msg))
}

// recoverPanic runs fn, logging and swallowing any panic instead of letting
// it escape to fn's caller. It is the shared panic barrier every dispatch
// path in this file uses outside HandleMessage's own coverage in
// handleRecovered: handleRecovered's deliver uses it for onResponse, and the
// force-stop busy-refusal path (which never reaches handleRecovered — there
// is no handler response to deliver) uses it directly, so an enqueue panic
// on either path is contained identically.
func (c *Connection) recoverPanic(msg appwire.Message, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.server.panicLogf("appserver: panic handling %s: %v\n%s", methodOf(msg), r, debug.Stack())
		}
	}()
	fn()
}

// methodOf names a message's method for the panic log; a frame that is
// neither request nor notification cannot reach a handler, but the barrier
// covers it anyway.
func methodOf(msg appwire.Message) string {
	switch {
	case msg.Request != nil:
		return msg.Request.Method
	case msg.Notification != nil:
		return msg.Notification.Method
	}
	return "invalid frame"
}

// enqueueDispatched is the one enqueue body every dispatch path shares:
// enqueue a response, canceling the connection when it cannot be enqueued.
func (c *Connection) enqueueDispatched(ctx context.Context, resp appwire.Message) {
	if resp.Kind() == appwire.MessageInvalid {
		return
	}
	if err := c.enqueueResponse(ctx, resp); err != nil {
		c.cancelContext()
	}
}

func (c *Connection) admitRequestContext(ctx context.Context, msg appwire.Message) (admitted context.Context, ok bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.server.panicLogf("appserver: panic admitting %s: %v\n%s", methodOf(msg), recovered, debug.Stack())
			admitted, ok = nil, false
		}
	}()
	admitted = c.server.cfg.RequestAdmissionContext(ctx, msg)
	return admitted, admitted != nil
}
