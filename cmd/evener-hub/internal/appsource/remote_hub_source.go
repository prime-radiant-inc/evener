package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"primeradiant.com/evener/appwire"
)

// RemoteHubClientFunc returns an attached, initialized AppWire client for the
// named remote host, attaching on first use. Component 04 supplies it.
type RemoteHubClientFunc func(ctx context.Context, host string) (*appwire.Client, error)

// RemoteHubSource exposes a remote evener hub as one more appsource.Source on
// the controller hub. Its ID is the host name from the controller's [[hosts]]
// config; every call is forwarded to the remote hub's AppWire edge over a
// single long-lived client, and every ref is translated between the
// controller's "<host>:<thread>" namespace and the remote hub's
// "local:<thread>" namespace.
//
// This is components 05a-05d: the read path (ID, ListThreads, ReadThread,
// ListTurns, ListModels) plus registration, subscription fan-out, the
// turn/thread lifecycle mutations (including mutation-unknown mapping) that
// live in remote_hub_mutations.go, and the capability probe in
// remote_hub_probe.go.
//
// The client is never cached here: every request re-invokes the connector, so a
// component-04 reconnect that swaps the underlying client is picked up
// automatically.
type RemoteHubSource struct {
	id     string
	roots  []string
	client RemoteHubClientFunc

	// facts is the optional component-04 preflight seam for the facts AppWire
	// cannot report on an already-initialized connection (see HostFacts).
	facts HostFactsFunc

	// online is the optional availability signal: it reports whether the
	// remote host currently has a live channel. nil means online (the pre-06
	// default); the setter is called once at registration.
	online func() bool

	subMu  sync.Mutex
	subs   map[string]*remoteHubSubscription // key: remote thread ID
	drains map[*appwire.Client]struct{}      // clients whose notification stream is being drained
	// hostSubs are host-level (non-thread) notification consumers registered
	// through SubscribeHostNotifications, e.g. the component-07a remote-admin
	// fan-out. They are fed by the same drainLoop that routes thread
	// notifications, never by a second reader of Client.Notifications().
	hostSubs map[*remoteHubHostSubscription]struct{}
	// hostNotifyDropped counts host-level notifications dropped because a
	// consumer's buffer was full. Host fan-out is deliberately non-blocking so a
	// stalled consumer cannot stall thread routing; this counter makes the
	// tradeoff observable.
	hostNotifyDropped atomic.Int64

	// probeMu serializes HostCapabilities and guards probe, the last successful
	// probe cached against the client it ran on.
	probeMu sync.Mutex
	probe   *remoteHubProbe
}

var (
	_ Source       = (*RemoteHubSource)(nil)
	_ OnlineSource = (*RemoteHubSource)(nil)
)

func NewRemoteHubSource(id string, roots []string, client RemoteHubClientFunc) *RemoteHubSource {
	return &RemoteHubSource{
		id:       id,
		roots:    roots,
		client:   client,
		subs:     map[string]*remoteHubSubscription{},
		drains:   map[*appwire.Client]struct{}{},
		hostSubs: map[*remoteHubHostSubscription]struct{}{},
	}
}

func (s *RemoteHubSource) ID() string { return s.id }

// SetHostFacts installs the component-04 preflight facts seam used by the
// capability probe. It is optional and expected to be called once at
// registration before the source serves. With no facts seam a probe leaves
// ProtocolVersion, HubVersion, OS, Arch, and Features zero-valued.
func (s *RemoteHubSource) SetHostFacts(fn HostFactsFunc) { s.facts = fn }

// SetHostOnline installs the availability signal: it reports whether the
// source's remote host currently has a live channel. It is optional and
// expected to be called once at registration before the source serves. With
// no signal installed the source reports online (the pre-06 default).
func (s *RemoteHubSource) SetHostOnline(fn func() bool) { s.online = fn }

// Online reports whether this source can currently serve requests.
func (s *RemoteHubSource) Online() bool {
	if s.online == nil {
		return true
	}
	return s.online()
}

// call forwards one request over the current remote client and translates any
// refs in the response back into the controller namespace.
func (s *RemoteHubSource) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return s.mapCallError(err)
	}
	client, err := s.client(ctx, s.id)
	if err != nil {
		return s.mapCallError(err)
	}
	return s.callOn(ctx, client, method, params, out)
}

// callOn forwards one request on an already-resolved client and translates any
// refs in the response. The capability probe uses it so every wire call runs on
// the exact client its cache was keyed against, not on whatever client the
// connector returns between calls.
func (s *RemoteHubSource) callOn(ctx context.Context, client *appwire.Client, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return s.mapCallError(err)
	}
	if err := client.Request(ctx, method, params, out); err != nil {
		return s.mapCallError(err)
	}
	return s.translateOut(out)
}

// mapCallError mirrors LocalDaemonSource's error shapes for a remote hub: a
// transport-level failure (dial, EOF, reset, closed, timeout) becomes
// SessionUnavailable so the hub's auto-resume gate can fire, while an
// application-level WireError carrying a semantic code is preserved exactly.
func (s *RemoteHubSource) mapCallError(err error) error {
	if err == nil {
		return nil
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		return s.transportUnavailable(err)
	}
	if wire.Code != appwire.CodeInternalError {
		return err
	}
	if remoteHubTransportText(strings.ToLower(wire.Message)) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + wire.Message)
	}
	return err
}

// transportUnavailable maps a non-wire transport failure. Caller cancellation
// stays raw; every transport-shaped failure names the host so the fleet view
// and the auto-resume gate can attribute it.
func (s *RemoteHubSource) transportUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	if remoteHubTransportText(strings.ToLower(err.Error())) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	return err
}

func remoteHubTransportText(lower string) bool {
	switch {
	case strings.Contains(lower, "eof"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "use of closed network connection"),
		strings.Contains(lower, "i/o timeout"):
		return true
	default:
		return false
	}
}

func (s *RemoteHubSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	remote := params
	remote.SourceIDs = remapRemoteSourceIDs(s.id, params.SourceIDs)
	var out appwire.ThreadListResponse
	if err := s.call(ctx, appwire.MethodThreadList, remote, &out); err != nil {
		return appwire.ThreadListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.ThreadReadResponse
	if err := s.call(ctx, appwire.MethodThreadRead, remote, &out); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ListTurns(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	var out appwire.ThreadTurnsListResponse
	if err := s.call(ctx, appwire.MethodThreadTurnsList, remote, &out); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return out, nil
}

func (s *RemoteHubSource) ListModels(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
	var out appwire.ModelListResponse
	if err := s.call(ctx, appwire.MethodModelList, params, &out); err != nil {
		return appwire.ModelListResponse{}, err
	}
	return out, nil
}

// AdminCall forwards one hub-scoped admin RPC to this remote host's hub over
// the shared per-host client and returns that method's own result verbatim
// (component 07a). It translates nothing: the caller has already decided the
// method is one the proxy may forward, and the answer is the remote hub's
// answer, not a re-shaped one.
//
// The client is resolved per call, so a component-04 reconnect that swaps the
// underlying client is picked up automatically. Errors are mapped exactly as on
// the other forwarding paths: an application-level WireError keeps its semantic
// code and message (a launch credential-env refusal or an auth Codex/gcp-adc
// refusal reaches the browser unchanged), while a transport-level failure
// (dial, EOF, reset, timeout) becomes SessionUnavailable so the browser sees
// CodeUnavailable/auto-resume rather than an InternalError, and so it matches
// the pre-call offline refusal. A caller can still tell the two apart: a remote
// refusal keeps its own code, a dead channel is SessionUnavailable.
func (s *RemoteHubSource) AdminCall(ctx context.Context, method string, params json.RawMessage, out *json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return s.mapCallError(err)
	}
	client, err := s.client(ctx, s.id)
	if err != nil {
		return s.mapCallError(err)
	}
	if err := client.Request(ctx, method, params, out); err != nil {
		return s.mapCallError(err)
	}
	return nil
}

// remoteHubHostSubscription is one host-level notification consumer, e.g. the
// component-07a remote-admin fan-out.
//
// Two channels with one writer each, mirroring remoteHubSubscription. in is
// written only by the owning client's drainLoop and closed only by that same
// drainLoop on client teardown; because it is never closed while a send can run,
// a delivery can never panic on a closed channel, and a full buffer drops rather
// than blocking the shared drain. out is written and closed only by the
// subscription's pump goroutine, so it is out's sole owner: the pump closes it
// on context end or when in closes, which is what makes the returned channel's
// documented closure contract hold on every path. done is closed by the pump on
// exit so the drain can skip a subscription whose pump is already gone.
type remoteHubHostSubscription struct {
	client *appwire.Client
	in     chan appwire.Notification
	out    chan appwire.Notification
	done   chan struct{}
}

// SubscribeHostNotifications registers a consumer for this remote hub's
// host-level notifications: every notification the shared client delivers —
// including the config broadcasts (evener/auth/updated and friends) that carry
// no thread route and are therefore dropped by thread routing.
//
// Registration starts the client's drain, so an admin-only consumer with no
// thread subscribers still receives notifications. The returned channel is
// closed when ctx ends or when the client's notification stream ends (a
// reconnect or a dead channel), so the caller re-subscribes and binds whatever
// client the connector returns next. Each call runs one pump goroutine for
// exactly the subscription's lifetime; it exits on either termination path, so
// a reconnecting host never accumulates one goroutine per reconnect.
//
// This is deliberately not a second reader of Client.Notifications(): that
// channel already has exactly one consumer (drainLoop), and a second reader
// would race it and silently split the stream.
func (s *RemoteHubSource) SubscribeHostNotifications(ctx context.Context) (<-chan appwire.Notification, error) {
	client, err := s.client(ctx, s.id)
	if err != nil {
		return nil, s.mapCallError(err)
	}
	sub := &remoteHubHostSubscription{
		client: client,
		in:     make(chan appwire.Notification, remoteHubSubBuffer),
		out:    make(chan appwire.Notification, remoteHubSubBuffer),
		done:   make(chan struct{}),
	}
	s.subMu.Lock()
	s.hostSubs[sub] = struct{}{}
	s.ensureDrainLocked(client)
	s.subMu.Unlock()
	go s.pumpHostSubscription(ctx, sub)
	return sub.out, nil
}

// publishHostNotification delivers one remote notification to every registered
// host-level consumer. It runs on the drain goroutine only, so it is the sole
// writer of each subscription's in channel; in is never closed while a send can
// run, so no send can race a close.
//
// Delivery is non-blocking: a full consumer buffer drops the notification and
// counts it. Unlike thread routing — where a dropped turn/completed is the
// failure this component exists to prevent — a dropped config broadcast only
// leaves a settings pane stale until its next refresh, whereas blocking here
// would stall the shared drain and therefore every thread notification for the
// same client. A healthy consumer's pump keeps reading in, so drops are the
// exception rather than the rule.
func (s *RemoteHubSource) publishHostNotification(notification appwire.Notification) {
	s.subMu.Lock()
	subs := make([]*remoteHubHostSubscription, 0, len(s.hostSubs))
	for sub := range s.hostSubs {
		subs = append(subs, sub)
	}
	s.subMu.Unlock()
	for _, sub := range subs {
		select {
		case sub.in <- notification:
		case <-sub.done:
		default:
			s.hostNotifyDropped.Add(1)
		}
	}
}

// pumpHostSubscription owns sub.out and sub.done: it is out's only sender and
// only closer, so the returned channel closes on every termination path — its
// context ending or the drain closing in on client teardown.
func (s *RemoteHubSource) pumpHostSubscription(ctx context.Context, sub *remoteHubHostSubscription) {
	defer close(sub.out)
	defer close(sub.done)
	defer s.unregisterHostSubscription(sub)
	for {
		select {
		case <-ctx.Done():
			return
		case notification, ok := <-sub.in:
			if !ok {
				return
			}
			select {
			case sub.out <- notification:
			case <-ctx.Done():
				return
			}
		}
	}
}

// unregisterHostSubscription removes sub from the routing table. It is safe to
// call after drainLoop has already removed it on client teardown.
func (s *RemoteHubSource) unregisterHostSubscription(sub *remoteHubHostSubscription) {
	s.subMu.Lock()
	delete(s.hostSubs, sub)
	s.subMu.Unlock()
}
