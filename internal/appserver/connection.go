package appserver

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
)

type admittedRequest struct {
	ctx     context.Context
	message appwire.Message
}

type Connection struct {
	peerMu       sync.Mutex
	peerHubID    string
	sessionMu    sync.Mutex
	sessionTools *sessionToolsConnection
	// sessionToolsInstalling reserves the single runtime slot while its callback waits outside sessionMu.
	sessionToolsInstalling *sessionToolsConnection
	sessionNext            atomic.Uint64
	sessionLifetime        context.Context
	id                     string
	server                 *Server
	send                   chan appwire.Message
	sendMu                 sync.RWMutex
	sendClosed             bool
	// requests is the bounded inbound queue between the transport's receive
	// loop (the only producer) and the serial worker (the only consumer).
	// Neither side ever closes it — producer teardown is "stop sending",
	// consumer teardown is context cancellation — and ServeWebSocket purges
	// its buffered frames at teardown so an orphaned handler retains at most
	// the one frame it is executing.
	requests chan admittedRequest
	// queueSaturationAdvised makes the queue-saturation advisory one-shot per
	// connection. Only the receive-loop goroutine touches it.
	queueSaturationAdvised bool
	// queueHighWaterAdvised makes the high-water refusal advisory one-shot
	// per connection, separately from queueSaturationAdvised so a refusal
	// cannot consume the full-queue block advisory (and vice versa). Only
	// the receive-loop goroutine touches it.
	queueHighWaterAdvised bool
	// slowReadSlots is the buffered-channel semaphore capping in-flight slow
	// reads per connection (slowReadDispatchCap). The worker acquires a slot
	// before spawning a slow read; the slow-read goroutine releases it when
	// handleAndEnqueue returns.
	slowReadSlots chan struct{}
	// requestPools counts, by the pool ConcurrentRequest named, the admitted
	// requests in flight; each pool holds at most concurrentRequestCap, and
	// requestsInFlight, their sum, at most concurrentRequestTotalCap.
	// requestPoolsMu guards both: the worker takes slots and the requests'
	// goroutines free them.
	requestPoolsMu   sync.Mutex
	requestPools     map[string]int
	requestsInFlight int
	// requestPoolSaturationAdvised marks the pools whose fill has been
	// logged. requestPoolsMu guards it, and a pool's mark goes when the pool
	// drains, so it holds only pools in use however many names clients send:
	// a pool reports once each time it fills.
	requestPoolSaturationAdvised map[string]bool
	// capSaturationAdvised makes the cap-saturation advisory one-shot per
	// connection. Only the worker goroutine touches it.
	capSaturationAdvised bool
	// requestTotalSaturationAdvised does the same for the connection's total
	// of admitted requests. Only the worker goroutine touches it.
	requestTotalSaturationAdvised bool
	// slowReadMu guards slowReadInflight, the per-method tally of in-flight
	// slow reads the stall advisory names.
	slowReadMu       sync.Mutex
	slowReadInflight map[string]int
	// workerExited closes when the serial worker returns; tests assert
	// worker teardown against it instead of sleeping.
	workerExited chan struct{}
	// pendingAdmissions covers the wire-admitted-to-capture interval. Entries
	// are removed on claim or read completion, so cancellation leaves no
	// unbounded tombstones.
	admissionMu       sync.Mutex
	pendingAdmissions map[*subscriptionAdmission]struct{}
	mu                sync.RWMutex
	initialized       bool
	recoveryRunning   bool
	// recoveryClearID is the requestIDKey of the in-flight force-stop
	// response, from the moment its handler finishes (markRecoveryClear)
	// through the moment its transport.Send call actually returns
	// (afterSend). It is the single source of truth for "some force-stop
	// response still has to reach the wire": beforeSend does not clear it
	// (only recoveryRunning, so the client-visible guarantee — recoveryRunning
	// is already false before any byte of the response reaches the wire — is
	// unchanged), so admission below can check recoveryClearID != "" the same
	// way it checks recoveryRunning, covering enqueue and the transport write
	// with one field instead of a second bool that could drift from it.
	recoveryClearID string
	cancel          context.CancelFunc
	responseMu      sync.Mutex
	hydrationMu     sync.Mutex
	hydrations      map[string]*hydrationResponseFinalizer
	// afterWrite holds the AfterResponseWritten callbacks, keyed the same way
	// hydrations is (requestIDKey). afterWriteDrained records that
	// runPendingAfterWrite has already run, so a callback arriving after the
	// send loop stopped is refused instead of being retained forever. Both
	// are guarded by responseMu.
	afterWrite        map[string]func()
	afterWriteDrained bool
}

func (c *Connection) ID() string {
	return c.id
}

func (c *Connection) setCancel(cancel context.CancelFunc) {
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
}

func (c *Connection) cancelContext() {
	c.mu.RLock()
	cancel := c.cancel
	c.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Connection) enqueue(msg appwire.Message) bool {
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	if c.sendClosed {
		return false
	}
	select {
	case c.send <- msg:
		return true
	default:
		return false
	}
}

func (c *Connection) enqueueResponse(ctx context.Context, msg appwire.Message) error {
	responseID, responseSucceeded := responseHydrationOutcome(msg)
	c.hydrationMu.Lock()
	c.sendMu.RLock()
	if c.sendClosed {
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
			c.hydrationMu.Unlock()
			finalizer.abortAfterWithdrawal()
		} else {
			c.hydrationMu.Unlock()
		}
		return context.Canceled
	}
	select {
	case c.send <- msg:
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			if responseSucceeded || finalizer.releaseOnErrorResponse {
				if c.server.beforeHydrationCommit != nil {
					c.server.beforeHydrationCommit()
				}
				finalizer.commit(responseSucceeded)
				c.hydrationMu.Unlock()
				finalizer.commitAfterRelease()
			} else {
				finalizer.abort()
				c.hydrationMu.Unlock()
				finalizer.abortAfterWithdrawal()
			}
		} else {
			c.hydrationMu.Unlock()
		}
		return nil
	case <-ctx.Done():
		finalizer := c.takeHydration(responseID)
		c.sendMu.RUnlock()
		if finalizer != nil {
			finalizer.abort()
			c.hydrationMu.Unlock()
			finalizer.abortAfterWithdrawal()
		} else {
			c.hydrationMu.Unlock()
		}
		return ctx.Err()
	}
}

// beforeSend runs on the send loop immediately before the transport call for
// every outbound frame, so it stays cheap for the common case (no force stop
// in flight) via the RLock check below: when msg is the response
// markRecoveryClear tagged, it clears recoveryRunning here — strictly before
// Send, not after: Send can block or fail against a peer that stopped
// draining, so clearing only once it returns would (as an earlier version of
// this fix did) leave a gap between the client receiving the frame and the
// flag actually clearing. Clearing immediately before the call instead keeps
// a real ordering guarantee: this runs in the same goroutine as Send,
// strictly before it in program order, so the client cannot observe the
// response before recoveryRunning is already false.
//
// recoveryClearID is deliberately left set (not cleared here): admission
// checks it the same way it checks recoveryRunning, so a force stop is still
// refused for as long as Send is actually in flight — a blocked write, not
// just a full outbound buffer, is otherwise a second gap this same
// admission check needs to cover. afterSend clears it once Send returns.
func (c *Connection) beforeSend(msg appwire.Message) {
	c.mu.RLock()
	clearID := c.recoveryClearID
	c.mu.RUnlock()
	if clearID == "" {
		return
	}
	id, _ := responseHydrationOutcome(msg)
	if id != clearID {
		return
	}
	c.mu.Lock()
	if c.recoveryClearID == id {
		c.recoveryRunning = false
	}
	c.mu.Unlock()
}

// afterSend runs on the send loop immediately after a successful transport
// call, the send-loop half of beforeSend: it clears recoveryClearID once
// Send has actually returned for the response beforeSend marked, ending
// admission's refusal window. It is wired in beside responseWritten, which
// already runs at this exact point for an unrelated purpose (the
// AfterResponseWritten callback), rather than adding a third send-loop
// callback parameter for one more single-purpose hook.
func (c *Connection) afterSend(msg appwire.Message) {
	c.mu.RLock()
	clearID := c.recoveryClearID
	c.mu.RUnlock()
	if clearID == "" {
		return
	}
	id, _ := responseHydrationOutcome(msg)
	if id != clearID {
		return
	}
	c.mu.Lock()
	if c.recoveryClearID == id {
		c.recoveryClearID = ""
	}
	c.mu.Unlock()
}

func requestIDKey(id appwire.ID) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func (c *Connection) closeSend() {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.sendClosed {
		return
	}
	c.sendClosed = true
	close(c.send)
}

func (c *Connection) HandleMessage(ctx context.Context, msg appwire.Message) appwire.Message {
	if msg.Notification != nil {
		return c.handleNotification(*msg.Notification)
	}
	if msg.Request == nil {
		return appwire.ErrorMessage(appwire.NewIntID(0), appwire.InvalidRequest("request message required"))
	}
	req := *msg.Request
	if _, ok := ctx.Value(subscriptionAdmissionResolverPanicContextKey{}).(bool); ok {
		return appwire.ErrorMessage(req.ID, appwire.InternalError("internal error handling request"))
	}
	if reason, ok := ctx.Value(subscriptionAdmissionFailureContextKey{}).(string); ok {
		return appwire.ErrorMessage(req.ID, appwire.SessionUnavailable(reason))
	}
	// ping is the browser's app-level heartbeat (browsers cannot send WS ping
	// frames from JS). It bypasses the router and is answered here, before
	// the initialize gate; the receive loop answers it inline, bypassing the
	// request queue, so no handler can starve it.
	if req.Method == appwire.MethodPing {
		return appwire.ResponseMessage(req.ID, struct{}{})
	}
	if !c.isInitialized() && req.Method != appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.InvalidRequest("initialize required"))
	}
	if c.isInitialized() && req.Method == appwire.MethodInitialize {
		return appwire.ErrorMessage(req.ID, appwire.InvalidRequest("already initialized"))
	}
	if req.Method == appwire.MethodRuntimeSessionBind {
		return c.bindSessionTools(ctx, req)
	}
	if !c.server.cfg.AdapterNativeInitialize {
		if err := appwire.ValidateMutationParams(req.Method, req.Params); err != nil {
			return appwire.ErrorMessage(req.ID, appwire.InvalidParams(err.Error()))
		}
	}
	handlerCtx := context.WithValue(ctx, connectionContextKey{}, c)
	handlerCtx = context.WithValue(handlerCtx, requestIDContextKey{}, requestIDKey(req.ID))
	result, err := c.server.router.Dispatch(handlerCtx, req)
	if err != nil {
		return appwire.ErrorMessage(req.ID, WireError(err))
	}
	if req.Method == appwire.MethodInitialize {
		c.setInitialized()
	}
	return appwire.ResponseMessage(req.ID, result)
}

func (c *Connection) handleNotification(notification appwire.Notification) appwire.Message {
	switch notification.Method {
	case appwire.MethodInitialized:
		return appwire.Message{}
	default:
		return appwire.Message{}
	}
}

type connectionContextKey struct{}

type requestIDContextKey struct{}

func Notify(ctx context.Context, method string, params any) {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil || method == "" {
		return
	}
	conn.enqueue(appwire.NotificationMessage(method, params))
}

func (c *Connection) isInitialized() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

func (c *Connection) setInitialized() {
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
}
