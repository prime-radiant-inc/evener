package appwire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var ErrNotificationOverflow = errors.New("appwire notification buffer overflow")

// NotificationBufferCap sizes the Notifications() channel. Overflow is a
// deliberate loud failure (the connection is torn down rather than silently
// dropping or buffering without bound), so the capacity must hold any single
// legitimate burst even while the consumer waits for a scheduling slice: a
// large initial-turn replay can be ~160 messages, and request paths that never
// consume notifications (short-lived withClient calls) ride entirely on this
// buffer.
const NotificationBufferCap = 4096

type Client struct {
	transport     Transport
	nextID        atomic.Int64
	sendMu        sync.Mutex
	pendingMu     sync.Mutex
	pending       map[string]pendingRequest
	notifications chan Notification
	orderedFrames func(Message, error)
	pendingCoord  PendingCoordinator
	featuresMu    sync.RWMutex
	features      FeatureSet
	// logf sinks connection-lifecycle events; nil discards them. See SetLogf.
	logf func(format string, args ...any)
	// closed latches the read loop's exit. failPending only fails the entries
	// registered at that instant; a request that registers afterwards would
	// otherwise wait forever for a response no goroutine can deliver (a first
	// Send after a TCP half-close succeeds, so the send path does not save it).
	// request selects on this alongside its reply channel.
	closedMu sync.Mutex
	closeErr error
	closed   chan struct{}
}

type pendingRequest struct {
	id ID
	ch chan Message
}

func NewClient(transport Transport) *Client {
	c := &Client{
		transport:     transport,
		pending:       map[string]pendingRequest{},
		notifications: make(chan Notification, NotificationBufferCap),
		closed:        make(chan struct{}),
	}
	c.nextID.Store(1)
	return c
}

// Pinger is an optional transport capability: a transport that can send a
// keepalive ping and block until the peer answers or ctx is done. The WebSocket
// transport implements it; in-memory test transports do not, so they opt out of
// keepalive.
type Pinger interface {
	Ping(context.Context) error
}

const (
	// keepalivePingInterval / keepalivePongTimeout mirror the server side
	// (internal/appserver). They bound how long a silently-dropped daemon
	// connection blocks the hub's read loop before it errors out and the
	// subscription closes.
	keepalivePingInterval = 15 * time.Second
	keepalivePongTimeout  = 10 * time.Second
)

func (c *Client) Start(ctx context.Context) {
	c.startWithKeepalive(ctx, keepalivePingInterval, keepalivePongTimeout)
}

func (c *Client) startWithKeepalive(ctx context.Context, pingInterval, pongTimeout time.Duration) {
	if pinger, ok := c.transport.(Pinger); ok {
		go runClientKeepalive(ctx, pinger, c.transport.Close, pingInterval, pongTimeout, c.logf)
	}
	go func() {
		for {
			msg, err := c.transport.Recv(ctx)
			if err != nil {
				if c.orderedFrames != nil {
					c.orderedFrames(msg, err)
				}
				// Latch before failPending: an entry registered after the latch
				// is caught by request's own select even when it misses the
				// drain below; the reverse order would leave a window where it
				// misses both.
				c.markClosed(err)
				c.failPending(err)
				close(c.notifications)
				return
			}
			if msg.Notification != nil {
				if c.orderedFrames != nil {
					c.orderedFrames(msg, nil)
					continue
				}
				if !c.enqueueNotification(*msg.Notification) {
					c.markClosed(ErrNotificationOverflow)
					c.failPending(ErrNotificationOverflow)
					_ = c.transport.Close()
					close(c.notifications)
					return
				}
				continue
			}
			id := msg.IDString()
			c.pendingMu.Lock()
			pending := c.pending[id]
			delete(c.pending, id)
			c.pendingMu.Unlock()
			if pending.ch != nil {
				if c.orderedFrames != nil {
					c.orderedFrames(msg, nil)
				}
				pending.ch <- msg
			}
		}
	}()
}

// SetOrderedFrameHandler transfers notification delivery to a synchronous
// receive-loop observer while retaining normal response correlation. It exists
// for clients whose correctness depends on the response frame being an exact
// cut marker in the same ordered feed. Set it before Start.
func (c *Client) SetOrderedFrameHandler(handler func(Message, error)) {
	c.orderedFrames = handler
}

// SetLogf installs a sink for connection-lifecycle events a peer cannot
// observe from its own side of the socket (today: keepalive teardown, see
// runClientKeepalive). Until it is called the sink is nil and those events
// are discarded: this client runs inside interactive TUI sessions where the
// standard log package's default stderr destination would scroll into
// bubbletea's live grid in -debug mode (no alternate screen) and corrupt the
// render permanently (issue #783), so silence is the only default safe
// everywhere. Callers that want these events (hub, TUI) provide their own
// sink. Set it before Start, which reads the sink once to hand to the
// keepalive goroutine.
func (c *Client) SetLogf(logf func(format string, args ...any)) {
	c.logf = logf
}

type requestIDObserverKey struct{}

// WithRequestIDObserver returns a context that reports the id appwire mints for
// each request issued with it, before that request frame is sent. An
// ordered-frame handler sees every response the connection carries, so on a
// connection with concurrent requests the id is what tells a caller's own cut
// apart from the rest of them.
func WithRequestIDObserver(ctx context.Context, observe func(ID)) context.Context {
	if observe == nil {
		return ctx
	}
	return context.WithValue(ctx, requestIDObserverKey{}, observe)
}

func requestIDObserverFrom(ctx context.Context) func(ID) {
	observe, _ := ctx.Value(requestIDObserverKey{}).(func(ID))
	return observe
}

// runClientKeepalive pings the peer every interval and closes the transport if
// a ping goes unanswered within timeout. Closing unblocks the read loop's Recv,
// which fails pending requests and closes the notifications channel — so a
// silently-dead daemon surfaces as a normal subscription end. The teardown
// logs one line first: the closed connection then fails every later write
// with an ordinary transport error, so without the log a pong-timeout
// teardown under load — e.g. the hub subprocess starved past
// interval+timeout by concurrent CI gates (#154) — is indistinguishable
// from a dead hub. The line goes through logf (installed via SetLogf, nil
// discards it) rather than the standard log package, so it can never land on
// a live TUI session's terminal (issue #783).
func runClientKeepalive(ctx context.Context, pinger Pinger, closeFn func() error, interval, timeout time.Duration, logf func(format string, args ...any)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, timeout)
			err := pinger.Ping(pingCtx)
			cancel()
			if err != nil {
				if logf != nil {
					logf("appwire: keepalive ping failed (ping interval %s, pong timeout %s): %v; closing connection", interval, timeout, err)
				}
				_ = closeFn()
				return
			}
		}
	}
}

func (c *Client) enqueueNotification(notification Notification) bool {
	select {
	case c.notifications <- notification:
		return true
	default:
		return false
	}
}

func (c *Client) Notifications() <-chan Notification {
	return c.notifications
}

func (c *Client) Close() error {
	return c.transport.Close()
}

// SetPendingCoordinator installs an optimistic-rendering coordinator
// that observes the four conversation-affecting Turn* methods. Pass
// nil to disable. Safe to call before Start.
func (c *Client) SetPendingCoordinator(pc PendingCoordinator) {
	c.pendingCoord = pc
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	id := NewIntID(c.nextID.Add(1) - 1)
	if observe := requestIDObserverFrom(ctx); observe != nil {
		observe(id)
	}
	ch := make(chan Message, 1)

	c.pendingMu.Lock()
	c.pending[id.String()] = pendingRequest{id: id, ch: ch}
	c.pendingMu.Unlock()

	c.sendMu.Lock()
	if err := c.transport.Send(ctx, RequestMessage(id, method, params)); err != nil {
		c.sendMu.Unlock()
		c.removePending(id)
		return err
	}
	c.sendMu.Unlock()

	var msg Message
	select {
	case msg = <-ch:
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.closed:
		// The read loop is gone. A response or failPending error that raced in
		// before the exit still wins; otherwise synthesize the same error frame
		// failPending would have delivered, so callers see one failure shape.
		select {
		case msg = <-ch:
		default:
			c.removePending(id)
			msg = ErrorMessage(id, InternalError(c.closedError().Error()))
		}
	}

	if msg.Error != nil {
		wire := msg.Error.Error
		wire.Message = fmt.Sprintf("appwire %s: %s", method, wire.Message)
		return wire
	}
	if msg.Response == nil {
		return fmt.Errorf("appwire %s: expected response", method)
	}
	if out == nil {
		return nil
	}
	data, err := json.Marshal(msg.Response.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func (c *Client) Request(ctx context.Context, method string, params any, out any) error {
	return c.request(ctx, method, params, out)
}

func (c *Client) Notify(ctx context.Context, method string, params any) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.transport.Send(ctx, NotificationMessage(method, params))
}

func (c *Client) markClosed(err error) {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	select {
	case <-c.closed:
		return
	default:
	}
	c.closeErr = err
	close(c.closed)
}

func (c *Client) closedError() error {
	c.closedMu.Lock()
	defer c.closedMu.Unlock()
	if c.closeErr != nil {
		return c.closeErr
	}
	return errors.New("appwire client closed")
}

func (c *Client) removePending(id ID) {
	c.pendingMu.Lock()
	delete(c.pending, id.String())
	c.pendingMu.Unlock()
}

func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, pending := range c.pending {
		delete(c.pending, id)
		pending.ch <- ErrorMessage(pending.id, InternalError(err.Error()))
	}
}

// ProtocolVersionMismatchError is returned by Client.Initialize when a hub
// answers the handshake with a protocol version other than the one this
// client build speaks. Callers match it with errors.As instead of parsing
// the message, so an incompatible hub can be distinguished from any other
// Initialize failure (kata zedg).
type ProtocolVersionMismatchError struct {
	Got  string // protocol version the hub reported
	Want string // protocol version this client requires
}

func (e ProtocolVersionMismatchError) Error() string {
	return fmt.Sprintf("appwire initialize: server protocol version %q, want %q", e.Got, e.Want)
}

func (c *Client) Initialize(ctx context.Context, params InitializeParams) (InitializeResponse, error) {
	var out InitializeResponse
	if params.ProtocolVersion == "" {
		params.ProtocolVersion = ProtocolVersion
	}
	if params.ProtocolVersion != ProtocolVersion {
		return out, fmt.Errorf("appwire initialize: protocol version %q, want %q", params.ProtocolVersion, ProtocolVersion)
	}
	if err := c.request(ctx, MethodInitialize, params, &out); err != nil {
		return out, err
	}
	if out.ProtocolVersion != ProtocolVersion {
		return InitializeResponse{}, ProtocolVersionMismatchError{Got: out.ProtocolVersion, Want: ProtocolVersion}
	}
	if err := c.Notify(ctx, MethodInitialized, EmptyParams{}); err != nil {
		return InitializeResponse{}, err
	}
	c.featuresMu.Lock()
	c.features = out.Features
	c.featuresMu.Unlock()
	return out, nil
}

func (c *Client) ThreadList(ctx context.Context, params ThreadListParams) (ThreadListResponse, error) {
	var out ThreadListResponse
	err := c.request(ctx, MethodThreadList, params, &out)
	return out, err
}

func (c *Client) ThreadRead(ctx context.Context, params ThreadReadParams) (ThreadReadResponse, error) {
	var out ThreadReadResponse
	err := c.request(ctx, MethodThreadRead, params, &out)
	return out, err
}

func (c *Client) ThreadUnsubscribe(ctx context.Context, params ThreadUnsubscribeParams) (EmptyResponse, error) {
	var out EmptyResponse
	err := c.request(ctx, MethodThreadUnsubscribe, params, &out)
	return out, err
}

func (c *Client) ThreadTurnsList(ctx context.Context, params ThreadTurnsListParams) (ThreadTurnsListResponse, error) {
	var out ThreadTurnsListResponse
	err := c.request(ctx, MethodThreadTurnsList, params, &out)
	return out, err
}

func (c *Client) ThreadTurnItemsList(ctx context.Context, params ThreadTurnItemsListParams) (ThreadTurnItemsListResponse, error) {
	var out ThreadTurnItemsListResponse
	err := c.request(ctx, MethodThreadTurnItemsList, params, &out)
	return out, err
}

func (c *Client) ThreadTranscriptList(ctx context.Context, params ThreadTranscriptListParams) (ThreadTranscriptListResponse, error) {
	var out ThreadTranscriptListResponse
	err := c.request(ctx, MethodEvenerThreadTranscriptsList, params, &out)
	return out, err
}

func (c *Client) ThreadStart(ctx context.Context, params ThreadStartParams) (ThreadStartResponse, error) {
	var out ThreadStartResponse
	err := c.request(ctx, MethodThreadStart, params, &out)
	return out, err
}

func (c *Client) ThreadResume(ctx context.Context, params ThreadResumeParams) (ThreadResumeResponse, error) {
	var out ThreadResumeResponse
	err := c.request(ctx, MethodThreadResume, params, &out)
	return out, err
}

func (c *Client) ThreadFork(ctx context.Context, params ThreadForkParams) (ThreadForkResponse, error) {
	var out ThreadForkResponse
	err := c.request(ctx, MethodThreadFork, params, &out)
	return out, err
}

func (c *Client) ThreadClear(ctx context.Context, params ThreadClearParams) (ThreadClearResponse, error) {
	var out ThreadClearResponse
	err := c.request(ctx, MethodThreadClear, params, &out)
	return out, err
}

func (c *Client) ThreadModelSet(ctx context.Context, params ThreadModelSetParams) error {
	return c.request(ctx, MethodThreadModelSet, params, nil)
}

func (c *Client) ThreadNameSet(ctx context.Context, params ThreadNameSetParams) error {
	return c.request(ctx, MethodEvenerThreadNameSet, params, nil)
}

func (c *Client) ThreadReasoningEffortSet(ctx context.Context, params ThreadReasoningEffortSetParams) error {
	return c.request(ctx, MethodThreadReasoningEffortSet, params, nil)
}

func (c *Client) ThreadVisionModelSet(ctx context.Context, params ThreadVisionModelSetParams) error {
	return c.request(ctx, MethodThreadVisionModelSet, params, nil)
}

func (c *Client) ThreadCompactStart(ctx context.Context, params ThreadCompactStartParams) error {
	return c.request(ctx, MethodThreadCompactStart, params, nil)
}

func (c *Client) ThreadShutdown(ctx context.Context, params ThreadShutdownParams) error {
	return c.request(ctx, MethodThreadShutdown, params, nil)
}

func (c *Client) TurnStart(ctx context.Context, params TurnStartParams) (TurnStartResponse, error) {
	var out TurnStartResponse
	err := c.request(ctx, MethodTurnStart, params, &out)
	return out, err
}

func (c *Client) TurnSteer(ctx context.Context, params TurnSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnSteer, inputText(params.Input), pendingTargetRef(params.Ref, params.ThreadID))
	}
	err := c.request(ctx, MethodTurnSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}

func inputText(input []InputItem) string {
	for _, item := range input {
		if strings.TrimSpace(item.Text) != "" {
			return item.Text
		}
	}
	return ""
}

func (c *Client) TurnInterrupt(ctx context.Context, params TurnInterruptParams) error {
	return c.request(ctx, MethodTurnInterrupt, params, nil)
}

// TurnQueue calls turn/queue (kata 111a) to enqueue a user message during a
// running turn. The daemon returns immediately once the text has been
// recorded; the queued message is processed as a fresh user turn after the
// active turn completes.
func (c *Client) TurnQueue(ctx context.Context, params TurnQueueParams) error {
	return c.request(ctx, MethodTurnQueue, params, nil)
}

// GoalSet calls goal/set to set (or, with an empty objective, clear) the
// session's /goal. The daemon returns immediately; GoalSetResponse.Started
// reports whether the goal loop began right away or after the current turn.
func (c *Client) GoalSet(ctx context.Context, params GoalSetParams) (GoalSetResponse, error) {
	var out GoalSetResponse
	err := c.request(ctx, MethodGoalSet, params, &out)
	return out, err
}

// TurnDrainAsSteer calls turn/drainAsSteer (kata 0bq1) to drain every queued
// message into a single STEERING message for the in-flight turn.
func (c *Client) TurnDrainAsSteer(ctx context.Context, params TurnDrainAsSteerParams) error {
	var handle PendingHandle
	if c.pendingCoord != nil {
		handle = c.pendingCoord.Register(MethodTurnDrainAsSteer, "", params.Ref)
	}
	err := c.request(ctx, MethodTurnDrainAsSteer, params, nil)
	if err != nil && handle != nil {
		handle.Fail(err.Error())
	}
	return err
}

// TurnPromoteQueuedAsSteer calls turn/promoteQueuedAsSteer (issue #22) to
// remove the queued message at params.Index and inject it as a user-sourced
// STEERING message into the in-flight turn, leaving the rest of the queue
// in place. The daemon returns Conflict when no turn is active or the index
// no longer resolves against the live queue.
func (c *Client) TurnPromoteQueuedAsSteer(ctx context.Context, params TurnPromoteQueuedAsSteerParams) error {
	return c.request(ctx, MethodTurnPromoteQueuedAsSteer, params, nil)
}

// TurnCancelQueued calls turn/cancelQueued (issue #23) to remove the queued
// message at params.Index so it is never consumed. The response echoes the
// removed entry's full text and image count. The daemon returns Conflict
// when the index no longer resolves against the live queue or the expected
// entry id mismatches (the queue shifted under the client's snapshot,
// review F1) — unlike promote, no active turn is required.
func (c *Client) TurnCancelQueued(ctx context.Context, params TurnCancelQueuedParams) (TurnCancelQueuedResponse, error) {
	var resp TurnCancelQueuedResponse
	if err := c.request(ctx, MethodTurnCancelQueued, params, &resp); err != nil {
		return TurnCancelQueuedResponse{}, err
	}
	return resp, nil
}

func pendingTargetRef(ref, threadID string) string {
	if strings.TrimSpace(ref) != "" {
		return ref
	}
	return threadID
}

func (c *Client) TasksList(ctx context.Context, params TaskListParams) (TaskListResponse, error) {
	var out TaskListResponse
	err := c.request(ctx, MethodEvenerTasksList, params, &out)
	return out, err
}

func (c *Client) JobsList(ctx context.Context, params JobsListParams) (JobsListResponse, error) {
	var out JobsListResponse
	err := c.request(ctx, MethodEvenerJobsList, params, &out)
	return out, err
}

func (c *Client) JobOutput(ctx context.Context, params JobsOutputParams) (JobsOutputResponse, error) {
	var out JobsOutputResponse
	err := c.request(ctx, MethodEvenerJobsOutput, params, &out)
	return out, err
}

func (c *Client) PathsComplete(ctx context.Context, params PathsCompleteParams) (PathsCompleteResponse, error) {
	var out PathsCompleteResponse
	err := c.request(ctx, MethodEvenerPathsComplete, params, &out)
	return out, err
}

func (c *Client) DirsCreate(ctx context.Context, params DirsCreateParams) (DirsCreateResponse, error) {
	var out DirsCreateResponse
	err := c.request(ctx, MethodEvenerDirsCreate, params, &out)
	return out, err
}

func (c *Client) ProjectsRecent(ctx context.Context, params ProjectsRecentParams) (ProjectsRecentResponse, error) {
	var out ProjectsRecentResponse
	err := c.request(ctx, MethodEvenerProjectsRecent, params, &out)
	return out, err
}

func (c *Client) GitHead(ctx context.Context, params GitHeadParams) (GitHeadResponse, error) {
	var out GitHeadResponse
	err := c.request(ctx, MethodEvenerGitHead, params, &out)
	return out, err
}

func (c *Client) MobilePairing(ctx context.Context, params MobilePairingParams) (MobilePairingResponse, error) {
	var out MobilePairingResponse
	err := c.request(ctx, MethodEvenerMobilePairing, params, &out)
	return out, err
}

func (c *Client) NavigationRead(ctx context.Context, params NavigationReadParams) (NavigationReadResponse, error) {
	var out NavigationReadResponse
	err := c.request(ctx, MethodEvenerNavigationRead, params, &out)
	return out, err
}

func (c *Client) FavoriteSet(ctx context.Context, params FavoriteSetParams) (FavoriteSetResponse, error) {
	var out FavoriteSetResponse
	err := c.request(ctx, MethodEvenerFavoriteSet, params, &out)
	return out, err
}

func (c *Client) ArchiveSet(ctx context.Context, params ArchiveParams) (ArchiveResponse, error) {
	var out ArchiveResponse
	err := c.request(ctx, MethodEvenerArchiveSet, params, &out)
	return out, err
}

func (c *Client) ProjectDelete(ctx context.Context, params ProjectDeleteParams) (ProjectDeleteResponse, error) {
	var out ProjectDeleteResponse
	err := c.request(ctx, MethodEvenerProjectDelete, params, &out)
	return out, err
}

func (c *Client) SessionDelete(ctx context.Context, params SessionDeleteParams) (SessionDeleteResponse, error) {
	var out SessionDeleteResponse
	err := c.request(ctx, MethodEvenerSessionDelete, params, &out)
	return out, err
}

func (c *Client) Search(ctx context.Context, params SearchParams) (SearchResponse, error) {
	var out SearchResponse
	err := c.request(ctx, MethodEvenerSearch, params, &out)
	return out, err
}

func (c *Client) HarnessList(ctx context.Context, params HarnessListParams) (HarnessListResponse, error) {
	var out HarnessListResponse
	err := c.request(ctx, MethodEvenerHarnessesList, params, &out)
	return out, err
}

func (c *Client) Upgrade(ctx context.Context, params UpgradeParams) (UpgradeResponse, error) {
	var out UpgradeResponse
	err := c.request(ctx, MethodEvenerUpgrade, params, &out)
	return out, err
}

func (c *Client) AuthStatus(ctx context.Context, params AuthStatusParams) (AuthStatusResponse, error) {
	var out AuthStatusResponse
	err := c.request(ctx, MethodEvenerAuthStatus, params, &out)
	return out, err
}

func (c *Client) AuthLoginStart(ctx context.Context, params AuthLoginStartParams) (AuthLoginStartResponse, error) {
	var out AuthLoginStartResponse
	err := c.request(ctx, MethodEvenerAuthLoginStart, params, &out)
	return out, err
}

func (c *Client) AuthLoginComplete(ctx context.Context, params AuthLoginCompleteParams) (AuthLoginCompleteResponse, error) {
	var out AuthLoginCompleteResponse
	err := c.request(ctx, MethodEvenerAuthLoginComplete, params, &out)
	return out, err
}

func (c *Client) AuthLogout(ctx context.Context, params AuthLogoutParams) (AuthLogoutResponse, error) {
	var out AuthLogoutResponse
	err := c.request(ctx, MethodEvenerAuthLogout, params, &out)
	return out, err
}

func (c *Client) ModelList(ctx context.Context, params ModelListParams) (ModelListResponse, error) {
	var out ModelListResponse
	err := c.request(ctx, MethodModelList, params, &out)
	return out, err
}

func (c *Client) CommandList(ctx context.Context) (CommandListResponse, error) {
	var out CommandListResponse
	err := c.request(ctx, MethodEvenerCommandList, EmptyParams{}, &out)
	return out, err
}

func (c *Client) MarketplaceList(ctx context.Context) (MarketplaceListResponse, error) {
	var out MarketplaceListResponse
	err := c.request(ctx, MethodEvenerMarketplaceList, EmptyParams{}, &out)
	return out, err
}

func (c *Client) MarketplaceAdd(ctx context.Context, params MarketplaceAddParams) (MarketplaceListResponse, error) {
	var out MarketplaceListResponse
	err := c.request(ctx, MethodEvenerMarketplaceAdd, params, &out)
	return out, err
}

func (c *Client) MarketplaceRemove(ctx context.Context, params MarketplaceNameParams) (MarketplaceListResponse, error) {
	var out MarketplaceListResponse
	err := c.request(ctx, MethodEvenerMarketplaceRemove, params, &out)
	return out, err
}

func (c *Client) MarketplaceRefresh(ctx context.Context, params MarketplaceNameParams) (MarketplaceListResponse, error) {
	var out MarketplaceListResponse
	err := c.request(ctx, MethodEvenerMarketplaceRefresh, params, &out)
	return out, err
}

func (c *Client) MarketplaceBrowse(ctx context.Context, params MarketplaceBrowseParams) (MarketplaceBrowseResponse, error) {
	var out MarketplaceBrowseResponse
	err := c.request(ctx, MethodEvenerMarketplaceBrowse, params, &out)
	return out, err
}

func (c *Client) PluginList(ctx context.Context) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginList, EmptyParams{}, &out)
	return out, err
}

func (c *Client) PluginPreview(ctx context.Context, params PluginPreviewParams) (PluginPreviewResponse, error) {
	var out PluginPreviewResponse
	err := c.request(ctx, MethodEvenerPluginPreview, params, &out)
	return out, err
}

func (c *Client) PluginInstall(ctx context.Context, params PluginRefParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginInstall, params, &out)
	return out, err
}

func (c *Client) PluginUpgrade(ctx context.Context, params PluginRefParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginUpgrade, params, &out)
	return out, err
}

func (c *Client) PluginRemove(ctx context.Context, params PluginRefParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginRemove, params, &out)
	return out, err
}

func (c *Client) PluginEnable(ctx context.Context, params PluginRefParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginEnable, params, &out)
	return out, err
}

func (c *Client) PluginDisable(ctx context.Context, params PluginRefParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginDisable, params, &out)
	return out, err
}

func (c *Client) PluginSetAutoUpgrade(ctx context.Context, params PluginSetAutoUpgradeParams) (PluginListResponse, error) {
	var out PluginListResponse
	err := c.request(ctx, MethodEvenerPluginSetAutoUpgrade, params, &out)
	return out, err
}
