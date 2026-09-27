package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appitempaging"
)

// RemoteHubClientFunc returns an attached, initialized AppWire client for the
// named remote host, attaching on first use. Component 04 supplies it. A
// production RemoteHubSource resolves its calls through the attached-only
// lookup instead (SetHostClientIfAttached, backed by Manager.ClientIfAttached),
// so this dialing connector is the fallback for tests and one of the explicit
// attach triggers the hub itself drives (`hubcore.WebConfig.RemoteHostClient`).
type RemoteHubClientFunc func(ctx context.Context, host string) (*appwire.Client, error)

// RemoteHubSource exposes a remote evener hub as one more appsource.Source on
// the controller hub. Its ID is the host name from the controller's [[hosts]]
// config; every call is forwarded to the remote hub's AppWire edge over a
// single long-lived client, and every ref is translated between the
// controller's "<host>:<thread>" namespace and the remote hub's
// "local:<thread>" namespace.
//
// This is components 05a-05d: the read path (ID, ListThreads, ReadThread,
// ListTurns, ListModels, and the item-mode paging seam) plus registration,
// subscription fan-out, the turn/thread lifecycle mutations (including
// mutation-unknown mapping) that live in remote_hub_mutations.go, and the
// capability probe in remote_hub_probe.go.
//
// The client is never cached here: every request re-invokes the connector, so a
// component-04 reconnect that swaps the underlying client is picked up
// automatically.
type RemoteHubSource struct {
	id string
	// roots is the host's configured [[hosts]].roots scoping. It is retained
	// for the staged 05b/05c/05d lifecycle, mutation and capability work, which
	// is the "advisory inputs to components 04/05" contract in
	// hubcore.HostConfig; the 05a read path does not narrow by root.
	roots  []string
	client RemoteHubClientFunc
	// clientIfAttached is the non-dialing, attached-only client lookup
	// (SetHostClientIfAttached, backed by sshconn.Manager.ClientIfAttached). Every
	// call the source serves resolves through it, so a direct read, mutation,
	// subscription, or probe can never implicitly attach a dormant host or re-dial
	// one that dropped. It is nil only in tests, where the wired RemoteHubClientFunc
	// stays the fallback; production always installs it.
	clientIfAttached func(host string) (*appwire.Client, bool)

	// itemPaging retains the opaque remote item cursor behind the
	// controller-owned cursor minted for it, mirroring the bounded local-daemon
	// snapshot: an evicted continuation degrades to a typed stale-cursor error
	// instead of leaking the remote hub's cursor identity into the controller.
	itemPaging remoteItemPagingCache

	// itemPagingLocks serializes the peek → remote I/O → put read-modify-write
	// for one remote thread, mirroring LocalDaemonSource.itemPagingLocks. Without
	// it, concurrent requests for the same thread interleave: one request's
	// RebaseCursor can be applied to another request's retained remote cursor and
	// replay a stale boundary under a newer incarnation. The key is the
	// controller-side ref, the same key the paging cache uses.
	itemPagingLocks keyedMutexRegistry
	subMu           sync.Mutex
	subs            map[string]*remoteHubSubscription // key: remote thread ID
	drains          map[*appwire.Client]struct{}      // clients whose notification stream is being drained
	// remoteMu serializes the wire-level subscribe and unsubscribe a
	// subscription's lifecycle emits against the routing-table mutation that
	// decides them, so a replacement's subscribe can never land before its
	// predecessor's unsubscribe for the same remote thread. subMu is always
	// taken inside it, never the other way around.
	remoteMu sync.Mutex
	// facts is the optional component-04 preflight seam for the facts AppWire
	// cannot report on an already-initialized connection (see HostFacts).
	facts HostFactsFunc
	// handshake is the optional component-04 attach-handshake seam: the
	// InitializeResponse the live channel captured at attach, reached by host name
	// (SetHostHandshake, backed by the remoteHostHandshakeForChannel closure over
	// sshconn.Manager.ChannelIfAttached with the client-identity guard at the call
	// site). The probe reads ProtocolVersion/SourceID/Features from it; HubVersion
	// stays preflight-owned. nil leaves those to the facts seam (tests); it is
	// never dialed for.
	handshake HostHandshakeFunc

	// online is the optional availability signal: it reports whether the
	// remote host currently has a live channel. nil means online (the pre-06
	// default); the setter is called once at registration.
	online func() bool

	// hostSubs are host-level (non-thread) notification consumers registered
	// through SubscribeHostNotifications, e.g. the component-07a remote-admin
	// fan-out. They are fed by the same drainLoop that routes thread
	// notifications, never by a second reader of Client.Notifications().
	hostSubs map[*remoteHubHostSubscription]struct{}
	// hostFilter is the optional publish-time method filter for host-level
	// consumers: nil accepts every notification. It exists so a consumer can
	// exclude the noisy notification families it does not own before they reach
	// its bounded buffer. Guarded by subMu; installed once before serving.
	hostFilter func(method string) bool
	// hostNotifyDropped counts host-level notifications dropped because a
	// consumer's buffer was full. Host fan-out is deliberately non-blocking so a
	// stalled consumer cannot stall thread routing; this counter makes the
	// tradeoff observable.
	hostNotifyDropped atomic.Int64

	// probeMu guards probe (the last successful probe, cached against the
	// client it ran on) and facts (the preflight seam HostCapabilities reads
	// while probing). It is never held across a wire call.
	probeMu sync.Mutex
	probe   *remoteHubProbe
}

var (
	_ Source                  = (*RemoteHubSource)(nil)
	_ ItemCandidateSource     = (*RemoteHubSource)(nil)
	_ ItemReadCandidateSource = (*RemoteHubSource)(nil)
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

// 05a staged two relay overrides here that 05b deletes: RelayOnThreadRead and
// SupportsThreadRelay both reported false only because SubscribeThread was
// notImplemented, and the hub's defaults (true for both) are the truth now. A
// thread/read — plain or subscribe:true — starts a controller relay, which
// attaches through SubscribeThread and retires that attach with its own
// thread/unsubscribe. See stripRemoteSubscription for why a read's own Subscribe
// flag still never reaches the wire on its own.

// EnrichThreadFileBackedImages reports that this source's threads name files on
// the remote host, not this one. The hub's file-backed output-image pass reads
// the thread's CWD on the local filesystem, so running it on a remote thread
// would probe controller-local paths named by remote data; the remote hub has
// already enriched its own replies.
func (s *RemoteHubSource) EnrichThreadFileBackedImages() bool { return false }

// SetHostFacts installs the component-04 preflight facts seam used by the
// capability probe. It is optional and expected to be called once at
// registration before the source serves. With no facts seam a probe leaves
// ProtocolVersion, HubVersion, OS, Arch, and Features zero-valued.
//
// The write is guarded by probeMu, the same lock HostCapabilities reads the
// seam under, so a caller that installs facts while a probe is in flight does
// not race the read.
func (s *RemoteHubSource) SetHostFacts(fn HostFactsFunc) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	s.facts = fn
}

// SetHostHandshake installs the component-04 attach-handshake seam the capability
// probe reads (see HostHandshakeFunc). It is optional and expected to be called
// once at registration before the source serves; the write is guarded by probeMu,
// the same lock the probe reads the seam under. With no handshake seam installed
// the preflight-owned fields come from SetHostFacts instead.
func (s *RemoteHubSource) SetHostHandshake(fn HostHandshakeFunc) {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	s.handshake = fn
}

// SetHostClientIfAttached installs the non-dialing, attached-only client lookup
// (backed by sshconn.Manager.ClientIfAttached) that every call this source serves
// resolves through. It is expected to be called once at registration before the
// source serves; nil leaves the wired RemoteHubClientFunc as the fallback so
// tests that only inject a client function still work. With it installed a host
// that is not currently attached yields a typed SessionUnavailable error rather
// than a dial (component 05, §"Every other remote call is non-dialing").
func (s *RemoteHubSource) SetHostClientIfAttached(fn func(host string) (*appwire.Client, bool)) {
	s.clientIfAttached = fn
}

// SetHostOnline installs the availability signal: it reports whether the
// source's remote host currently has a live channel. It is optional and
// expected to be called once at registration before the source serves. With
// no signal installed the source reports online (the pre-06 default).
func (s *RemoteHubSource) SetHostOnline(fn func() bool) { s.online = fn }

// SetHostNotificationFilter installs the optional publish-time method filter
// for host-level notification consumers: only methods it accepts are published
// to them (nil accepts every notification). It is expected to be called once at
// registration before the source serves, like SetHostFacts and SetHostOnline.
func (s *RemoteHubSource) SetHostNotificationFilter(fn func(method string) bool) {
	s.subMu.Lock()
	s.hostFilter = fn
	s.subMu.Unlock()
}

// Online reports whether this source can currently serve requests.
func (s *RemoteHubSource) Online() bool {
	if s.online == nil {
		return true
	}
	return s.online()
}

// call forwards one request over the current remote client and translates any
// refs in the response back into the controller namespace.
//
// A caller cancellation or deadline stays raw at every step, exactly as
// mutationCall and LocalDaemonSource.withClientCallMapper leave ctx.Err(): the
// caller's own context ending is not host unavailability, so mapping it through
// would fire the auto-resume gate for a request the caller abandoned.
func (s *RemoteHubSource) call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.mapConnectError(err)
	}
	return s.callOn(ctx, client, method, params, out)
}

// resolveClient returns the client to serve one call on. With the attached-only
// lookup installed (production) it answers from the host's live channel and
// reports a typed SessionUnavailable — never a dial — for a host that is not
// attached, so a direct read, mutation, subscription, or probe cannot implicitly
// attach a dormant host or re-dial one that dropped between a check and the call
// (component 05, §"Every other remote call is non-dialing"). With no lookup
// installed (tests) it falls back to the wired RemoteHubClientFunc.
//
// It is also the shared remote-dispatch seam the host-routing origin guard
// enforces (component 07, §"Host-routing origin guard"): every call the source
// serves — read, mutation, subscription, admin forward, or probe — resolves its
// client here, so a remote-originated request is refused typed before it can
// dispatch to any host. A request this hub serves from its own cached state
// never reaches this seam and is unaffected.
func (s *RemoteHubSource) resolveClient(ctx context.Context) (*appwire.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := guardRemoteDispatch(ctx); err != nil {
		return nil, err
	}
	if s.clientIfAttached != nil {
		if client, ok := s.clientIfAttached(s.id); ok && client != nil {
			return client, nil
		}
		return nil, appwire.SessionUnavailable("remote hub unavailable: " + s.id)
	}
	return s.client(ctx, s.id)
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
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.mapCallError(err)
	}
	return s.translateOut(out)
}

// mapCallError mirrors LocalDaemonSource's error shapes for a remote hub: a
// transport-level failure (dial, EOF, reset, closed, timeout) becomes
// SessionUnavailable so the hub's auto-resume gate can fire, while an
// application-level WireError carrying a semantic code is preserved exactly.
// A caller cancellation or deadline is the caller's own context expiring, not
// host unavailability, so it stays raw exactly as localDaemonCallError leaves
// it.
func (s *RemoteHubSource) mapCallError(err error) error {
	if err == nil {
		return nil
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
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

// mapConnectError mirrors localDaemonDialError for the attach step: a timeout
// or reset while opening the SSH channel is host unavailability, not a slow
// request, so it is classified before the request-level mapping applies.
func (s *RemoteHubSource) mapConnectError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[appwire.WireError](err); ok {
		return s.mapCallError(err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return s.transportUnavailable(err)
}

// MapAttachError classifies one attach/connect failure exactly as this source's
// own call and resolveClient path does. A caller that had to attach the host
// before calling this source — the explicit thread/list fan-out, which dials
// through the Ensure-backed RemoteHostClient and then lets the source's
// attached-only resolver serve the list — reports the failure through this so
// sshconn's transient attach failures (ErrSSHStart, ErrRestart) and transport
// losses reach it as the typed SessionUnavailable the auto-resume/refusal gates
// match, rather than as a raw transport error. The caller's own context ending
// stays raw, exactly as call leaves it.
func (s *RemoteHubSource) MapAttachError(err error) error {
	if err == nil {
		return nil
	}
	return s.mapConnectError(err)
}

// transportUnavailable maps a non-wire transport failure. Caller cancellation
// stays raw; every transport-shaped failure names the host so the fleet view
// and the auto-resume gate can attribute it.
//
// A write-side connection failure counts as transport loss, which is what makes
// io.ErrClosedPipe, os.ErrClosed, net.ErrClosed, and appwire.ErrStreamClosed
// listed here (round eight). The send path reports them when the stream's write
// side is already gone — a partial write onto a connection the peer has torn
// down, or the first write after this end closed it — and they are deliberately
// not distinguishable from the read-side failures around it: like io.EOF or
// EPIPE they say the connection failed mid-call, not that the request was never
// sent. All four shapes are needed because each layer reports its own: a pipe
// gives io.ErrClosedPipe, the SSH stdio path's closed descriptor gives
// os.ErrClosed ("file already closed", often wrapped in an *fs.PathError), a
// closed network connection gives net.ErrClosed, and the stream transport's
// closed or poisoned state gives appwire.ErrStreamClosed, which is the shape a
// host channel's teardown produces on the next write. For a forwarded read that
// is SessionUnavailable either way, and for a forwarded mutation
// remoteHubAdminMutationCallError re-labels exactly this unavailability to
// outcome-unknown/blocked, so a closed write cannot escape as a raw error a
// caller might blind-retry.
func (s *RemoteHubSource) transportUnavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	// Component 04's attach step returns sshconn's typed start failure for a host
	// it could not reach or bring up (spawn, initialize, or preflight). That is
	// host unavailability, so it maps like any other transient transport failure.
	// sshconn.ErrSSHAuth, ErrProtocolIncompatible, ErrUnsupportedHost, and the
	// other terminal classes are deliberately not matched: they name a host that
	// will never attach and must stay raw so recovery is not retried forever.
	if errors.Is(err, sshconn.ErrSSHStart) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	// A failed hub restart is transient as well: sshconn stays disconnected and
	// retries the restart on the next Ensure (ErrRestart is not terminal), so it
	// names a host that is momentarily down, not one that can never attach. A
	// recovery/auto-resume gate that saw the raw error could not attribute the
	// outage, so it maps like the other transient attach failures.
	if errors.Is(err, sshconn.ErrRestart) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, appwire.ErrStreamClosed) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, net.ErrClosed) ||
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

// remoteHubTransportText recognizes transport-shaped error text. "eof" is
// matched as a standalone token only so an application message that merely
// contains those letters is not reclassified as host unavailability.
func remoteHubTransportText(lower string) bool {
	switch {
	case containsWord(lower, "eof"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "closed pipe"),
		// os.ErrClosed's text: the descriptor this end writes to is gone, the
		// same class of failure as net.ErrClosed's "use of closed network
		// connection" below. It matters on the WireError path, where the
		// failure arrives as the peer's message and errors.Is has nothing to
		// match against.
		strings.Contains(lower, "file already closed"),
		strings.Contains(lower, "use of closed network connection"),
		strings.Contains(lower, "i/o timeout"):
		return true
	default:
		return false
	}
}

// containsWord reports whether text contains word delimited by non-word bytes.
func containsWord(text, word string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], word)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(word)
		if (index == 0 || !isWordByte(text[index-1])) && (end == len(text) || !isWordByte(text[end])) {
			return true
		}
		offset = index + 1
	}
}

func isWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// remoteThreadListBudget is the deadline one controller-side ListThreads call
// runs under, in place of the local-daemon budget (app_threadlist.go).
//
// This source's list does NOT attach the host itself: ListThreads resolves its
// client through the attached-only lookup (clientIfAttached, backed by
// sshconn.Manager.ClientIfAttached) and never dials, so a direct read can never
// force a dormant host online or re-dial one that dropped. The attach that can
// precede the call is the explicit-SourceIDs trigger in the multi-source
// fan-out (app_threadlist.go), which dials through the Ensure-backed
// RemoteHostClient and then lets this source's attached-only resolver serve the
// list — both under the deadline the source reports. That attach is an ssh
// spawn, an AppWire initialize and a preflight (dial →
// hubcore.WebConfig.RemoteHostClient → sshconn.Manager.Ensure); the transport's
// own connect bound alone is 10s and the attach as a whole can run the
// restart/deploy ladder, so a three-second budget does not describe this call:
// it cuts a working attach off, and the timeout that results is
// indistinguishable from a broken host, because both are just "no threads". An
// unfiltered list then drops the whole host and reports success.
//
// This is what one list is worth waiting for: it covers the transport's 10s
// connect bound (sshconn's defaultConnectTimeout) plus the handshake with
// margin, so a host that cannot be reached fails on its own transport rather
// than on the list budget, and it stays far below the preflight and deploy
// bounds (sshconn's attemptLimit, 70s by default, and deployLimit, 10m) that no
// request may block on. An attach that outlives it is a sustained outage rather
// than a cold start, and that is the fleet's attachment state to report
// (component 06), not something a list may wait out.
const remoteThreadListBudget = 15 * time.Second

// ThreadListBudget reports the deadline ListThreads needs, over the fan-out's
// local-daemon default. See remoteThreadListBudget.
func (s *RemoteHubSource) ThreadListBudget() time.Duration { return remoteThreadListBudget }

func (s *RemoteHubSource) ListThreads(ctx context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	// The controller selected only other sources. remapRemoteSourceIDs would
	// drop every entry and forward a request with no filter at all, so an
	// explicit exclusion must never widen into an unfiltered list.
	if len(params.SourceIDs) > 0 && !slices.Contains(params.SourceIDs, s.id) {
		return appwire.ThreadListResponse{}, nil
	}
	remote := params
	// Only this hub's own namespace is representable in a controller ref, so an
	// unfiltered controller list is scoped to it: forwarding no filter lets a
	// nested remote hub return its own remote refs, which translateOut refuses
	// and which would otherwise abort the whole response.
	remote.SourceIDs = remapRemoteSourceIDs(s.id, params.SourceIDs)
	if len(remote.SourceIDs) == 0 {
		remote.SourceIDs = []string{remoteHubNamespace}
	}
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
	// Preserve the caller's ThreadID, empty included: the remote hub resolves a
	// bare non-empty ThreadID in preference to Ref, so sending the translated
	// ref's suffix here would address the thread by its stable identity and be
	// rejected as an unknown bare ID once an identity replacement has moved the
	// live thread ID. An empty ThreadID lets the remote resolve the ref, which
	// is stable-aware. The ref's suffix stays this hub's local routing key.
	remote.ThreadID = params.ThreadID
	// The remote client is shared by every controller relay for this host, so
	// controller-level replacement semantics must never reach it: the remote
	// hub's replaceSubscription read scopes the whole connection to this one
	// thread, silently dropping every other remote thread's subscription while
	// their local routing entries stayed live. Replacement is a controller-side
	// concept — app_relay.go applies it to the controller's own subscriptions —
	// and SubscribeThread clears the flag for the same reason.
	// The snapshot read must not subscribe either. This source's subscription is
	// established solely by SubscribeThread, which owns the one remote
	// subscription per thread and its thread/unsubscribe on teardown. A
	// subscribed read here would attach the shared connection to this thread
	// with no local routing entry tracking it, so nothing would drop it when the
	// relay never attaches (an empty thread ID, a deletion fence, a failed
	// subscribe) and the long-lived connection would keep forwarding that thread
	// — into a notification stream nothing is draining — for the rest of its
	// life. The relay path subscribes explicitly (app_relay.go startRelay), so
	// no subscription is lost.
	remote = stripRemoteSubscription(remote)
	var out appwire.ThreadReadResponse
	if err := s.call(ctx, appwire.MethodThreadRead, remote, &out); err != nil {
		return appwire.ThreadReadResponse{}, err
	}
	return out, nil
}

// stripRemoteSubscription removes the controller's subscription intent from a
// plain remote read. A remote subscription is created by SubscribeThread and by
// nothing else: it is the controller relay that owns the attach — it holds the
// returned channel open, re-attaches it through its recovery path, and retires
// it with thread/unsubscribe when the relay ends (remote_hub_subscription.go).
// A read that forwarded Subscribe would make the remote hub register a second,
// persistent subscription nothing on this side owns or can retire: an orphan
// that outlives the read. ReplaceSubscription is stripped for a sharper reason —
// it is connection-scoped on the shared per-host client, so forwarding it would
// drop every other thread's remote subscription while their local routing
// entries stayed live.
func stripRemoteSubscription(params appwire.ThreadReadParams) appwire.ThreadReadParams {
	params.Subscribe = false
	params.ReplaceSubscription = false
	return params
}

func (s *RemoteHubSource) ListTurns(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	remote := params
	remote.Ref = ref.String()
	// Preserve the caller's ThreadID for the same reason ReadThread does: a bare
	// threadId the remote cannot resolve is rejected, while an empty one lets
	// the remote resolve the (stable-aware) ref.
	remote.ThreadID = params.ThreadID
	var out appwire.ThreadTurnsListResponse
	if err := s.call(ctx, appwire.MethodThreadTurnsList, remote, &out); err != nil {
		return appwire.ThreadTurnsListResponse{}, err
	}
	return out, nil
}

// FetchSessionImage fetches one image out of the owning host's own local
// session state, for the controller routes that serve a remote session's
// host-qualified image URLs (multi-host component 05). It resolves the
// attached-only client like every other remote call — an unattached or unknown
// host is refused typed and never dialed, so this path can never fall back to a
// local read — and the response needs no translation: it carries bytes and a
// media type, not refs.
func (s *RemoteHubSource) FetchSessionImage(ctx context.Context, params appwire.SessionImageParams) (appwire.SessionImageResponse, error) {
	var out appwire.SessionImageResponse
	if err := s.call(ctx, appwire.MethodEvenerSessionImage, params, &out); err != nil {
		return appwire.SessionImageResponse{}, err
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

// remoteHubItemCursorProjectionVersion identifies cursor identities minted by
// RemoteHubSource. It is independent of the remote hub's own projection fence.
const remoteHubItemCursorProjectionVersion uint16 = 1

var remoteHubItemIncarnationSequence atomic.Uint64

// remoteItemPagingState retains the remote hub's opaque item cursor behind a
// controller-owned cursor, keyed by the controller ref that cursor names.
type remoteItemPagingState struct {
	identity appitempaging.CursorIdentity
	native   string
	// candidates is the chronological window observed under identity: every page
	// fetched for this continuation, merged. It is more than the page just
	// fetched because the remote hub returns only a tail page per request, so a
	// complete page (native == "") is the oldest page, not the whole transcript.
	// Retaining the merged window lets the hub's size packer mint a continuation
	// from identity AND lets any boundary observed under identity be replayed
	// locally, instead of failing ValidateCursorBoundary against the tail alone.
	candidates []appitempaging.TranscriptItemCandidate
	// spans records the runs of retained candidates the window observed without a
	// hole, oldest first and maximally merged. A forward page that resumes above
	// the retained window without provably abutting it leaves a run of its own:
	// the positions between the two may hold items this source never fetched (and
	// may equally hold none at all, because a logical group with no visible items
	// still consumes an entry ordinal — apptranscript.groupedAppTurnProjection).
	// The remote cursor stays the authority for whatever lives there, so the
	// window keeps its incarnation and a client's live cursors survive the append,
	// but no locally served answer may cross the stretch between two runs.
	spans []remoteItemSpan
	// complete marks a page the remote hub returned in full (it named no older
	// cursor).
	complete bool
	// head is the newest position observed for this thread at the last fresh
	// page, so a later fresh page whose newest position precedes it is
	// recognized as a divergent transcript and the incarnation rotates.
	head    appwire.ThreadItemPosition
	hasHead bool
}

// remoteItemSpan is one run of retained candidates this source observed as a
// provably contiguous stretch of the remote transcript: every position between
// lower and upper was observed, either by a single page or by pages shown to abut
// in that run. A page returned for a retained remote cursor continues the page
// that cursor was minted from (the remote hub's own sequence has nothing between
// them), so continuations extend the run they were fetched from — which is what
// lets a span a forward append left be filled piecewise.
type remoteItemSpan struct {
	lower appwire.ThreadItemPosition
	upper appwire.ThreadItemPosition
}

// remoteItemPagingCapacity bounds retained continuations. An evicted entry
// turns its outstanding cursor into a typed stale error, exactly like the
// bounded local-daemon item snapshot cache.
const remoteItemPagingCapacity = 64

// remoteItemPagingCandidateCapacity and remoteItemPagingByteCapacity bound one
// continuation's retained candidate window. Every page is merged into the
// retained window so a complete page's earlier boundaries stay answerable, so
// an unbounded window would grow to the whole transcript and to every tool
// output in it.
const (
	remoteItemPagingCandidateCapacity = 1024
	remoteItemPagingByteCapacity      = 8 << 20
)

type remoteItemPagingCache struct {
	mu      sync.Mutex
	entries map[string]remoteItemPagingState
	order   []string
}

func (c *remoteItemPagingCache) put(key string, state remoteItemPagingState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]remoteItemPagingState)
	}
	// Recency is stamped on commit, matching itemSnapshotStateCache's
	// MoveToFront-on-put: a re-put of an already retained key moves it to the
	// back so eviction reclaims the least recently committed continuation, not
	// the one an actively paging thread just advanced.
	if index := slices.Index(c.order, key); index >= 0 {
		c.order = append(c.order[:index], c.order[index+1:]...)
	}
	c.order = append(c.order, key)
	c.entries[key] = state
	for len(c.order) > remoteItemPagingCapacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *remoteItemPagingCache) peek(key string) (remoteItemPagingState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.entries[key]
	return state, ok
}

// remoteItemPagingKey is the controller-side ref that names one remote thread
// in the controller namespace; it identifies both the retained continuation
// and the identity fence minted into the controller's cursor.
func remoteItemPagingKey(sourceID, threadID string) string {
	return appwire.Ref{SourceID: sourceID, ThreadID: threadID}.String()
}

// ListItemCandidates serves the controller's item-mode turn page for a remote
// thread. The remote hub's opaque cursor never reaches the controller: each
// page mints (or continues) a controller-owned identity, and the remote cursor
// is retained behind it.
func (s *RemoteHubSource) ListItemCandidates(ctx context.Context, params appwire.ThreadTurnsListParams) (ItemCandidateResult, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	key := remoteItemPagingKey(s.id, ref.ThreadID)
	// Hold the per-thread paging lock from the retained-state peek through the
	// remote page and its put, exactly as LocalDaemonSource does: the retained
	// remote cursor and its controller identity are one read-modify-write unit.
	unlock := s.itemPagingLocks.lock(key)
	defer unlock()
	itemLimit, err := appwire.NormalizeTranscriptItemLimit(params.ItemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.ItemLimit = itemLimit

	if params.Cursor == "" {
		remote.Cursor = ""
		candidates, native, err := s.remoteItemPage(ctx, remote)
		if err != nil {
			return ItemCandidateResult{}, err
		}
		if err := validateRemotePageCursor(native, candidates); err != nil {
			return ItemCandidateResult{}, err
		}
		identity, head, hasHead := s.remoteItemPageIdentity(key, candidates)
		return s.recordRemoteItemPage(key, identity, candidates, native, head, hasHead, nil)
	}

	state, ok := s.itemPaging.peek(key)
	if !ok {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	if state.complete {
		return continueCompleteRemoteItemPage(params.Cursor, state, itemLimit)
	}
	before, err := appitempaging.DecodeCursor(params.Cursor, state.identity)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// The decoded boundary is a fence, not free-form input: it must still name an
	// item the retained window observed before it is rebased and forwarded,
	// otherwise a caller that keeps a valid identity but moves the boundary would
	// page the remote from a position this incarnation never proved.
	if err := appitempaging.ValidateCursorBoundary(state.candidates, before); err != nil {
		return ItemCandidateResult{}, err
	}
	// Packing can drop the oldest selected item after the cursor is minted, so
	// the caller's boundary may be newer than the retained page boundary; the
	// retained remote cursor is rebased onto the caller's boundary before it is
	// replayed against the remote hub.
	native, err := appitempaging.RebaseCursor(state.native, before)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote.Cursor = native
	candidates, next, err := s.remoteItemPage(ctx, remote)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// A remote hub is a separate process, possibly a different version: its
	// continuation page is held to the contract LocalDaemonSource holds the
	// daemon's to, so a stale or repeating answer cannot be cached and re-served
	// under a live controller cursor.
	candidates, err = validateRemoteContinuationPage(native, next, before, candidates)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// The requested boundary was validated under state.identity, so a page that
	// contradicts the retained window means the transcript was rewritten after
	// the cursor was minted. Answering under a rotated identity would splice old
	// and new history together, so the continuation fails closed instead.
	if _, compatible := remoteMergeCandidates(state.candidates, candidates); !compatible {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	return s.recordRemoteItemPage(key, state.identity, candidates, next, state.head, state.hasHead, &before)
}

// ReadItemCandidates materializes a remote item-mode thread read into the
// private candidate contract. It issues its own read and mints the same
// identity ItemCandidatesFromRead does.
func (s *RemoteHubSource) ReadItemCandidates(ctx context.Context, params appwire.ThreadReadParams) (ItemCandidateResult, error) {
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	remote := params
	remote.Ref = ref.String()
	remote.ThreadID = ref.ThreadID
	remote.IncludeTurns = true
	remote = stripRemoteSubscription(remote)
	var out appwire.ThreadReadResponse
	if err := s.call(ctx, appwire.MethodThreadRead, remote, &out); err != nil {
		return ItemCandidateResult{}, err
	}
	return s.ItemCandidatesFromRead(ctx, params, out)
}

// ItemCandidatesFromRead converts an already-materialized remote item read
// into the controller's candidate contract without issuing another read, so
// the cursor minted from thread/read stays continuable through
// thread/turns/list.
func (s *RemoteHubSource) ItemCandidatesFromRead(ctx context.Context, params appwire.ThreadReadParams, response appwire.ThreadReadResponse) (ItemCandidateResult, error) {
	if err := ctx.Err(); err != nil {
		return ItemCandidateResult{}, err
	}
	ref, err := s.toRemoteRef(params.Ref, params.ThreadID)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	key := remoteItemPagingKey(s.id, ref.ThreadID)
	// The read is already materialized, but minting its controller identity and
	// replacing the retained cursor is the same read-modify-write unit as a
	// paged continuation, so it takes the same per-thread lock.
	unlock := s.itemPagingLocks.lock(key)
	defer unlock()
	candidates, err := appitempaging.CandidatesFromTurns(response.Thread.Turns)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	// A remote hub is a separate process, possibly a different version: its
	// fragments must satisfy the same strictly-increasing, uniquely-keyed contract
	// the local daemon source validates — plus the per-entry adjacency a page's
	// observed run depends on — before anything is merged or cached, or a
	// misbehaving remote could poison the retained cursor identity for later
	// continuations.
	if err := validateRemotePagePositions(candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	// The remote hub's read cursor is retained behind the controller identity, so
	// it must canonically name the window it arrived with before it can be rebased
	// onto a controller boundary later.
	if err := validateRemotePageCursor(response.OlderCursor, candidates); err != nil {
		return ItemCandidateResult{}, err
	}
	identity, head, hasHead := s.remoteItemPageIdentity(key, candidates)
	return s.recordRemoteItemPage(key, identity, candidates, response.OlderCursor, head, hasHead, nil)
}

// remoteItemPage issues one remote item-mode turn page and returns its
// positioned candidates and the remote cursor for the next older page.
func (s *RemoteHubSource) remoteItemPage(ctx context.Context, remote appwire.ThreadTurnsListParams) ([]appitempaging.TranscriptItemCandidate, string, error) {
	var out appwire.ThreadTurnsListResponse
	if err := s.call(ctx, appwire.MethodThreadTurnsList, remote, &out); err != nil {
		return nil, "", err
	}
	candidates, err := appitempaging.CandidatesFromTurns(out.Data)
	if err != nil {
		return nil, "", err
	}
	if err := validateRemotePagePositions(candidates); err != nil {
		return nil, "", err
	}
	return candidates, out.NextCursor, nil
}

// validateRemotePagePositions applies to one remote item page the whole positional
// contract this source must prove before the page is merged or retained: the
// strictly-increasing, uniquely-keyed candidates appitempaging validates, and the
// per-entry adjacency the page's observed run depends on.
func validateRemotePagePositions(candidates []appitempaging.TranscriptItemCandidate) error {
	if err := appitempaging.ValidateCandidates(candidates); err != nil {
		return err
	}
	return validateRemotePageAdjacency(candidates)
}

// validateRemotePageAdjacency rejects a page whose observed positions skip an item
// index inside one entry. Item-mode positions are (entry, item) pairs and a turn's
// projected items are numbered densely from zero within their entry
// (apptranscript.ProjectTurn and server.positionAppItems), so (10,0) followed by
// (10,2) without (10,1) is an item the remote holds and did not return — never a
// contiguous window. Both consumers of an observed page trust the run between its
// endpoints: the retained span the page is recorded as, and the local continuation
// served from that span. Accepting the page would let them skip the withheld item
// silently, so it is refused before anything is merged or cached, exactly as an
// out-of-order page is. A jump to a later entry is not gated: entry ordinals advance
// for logical groups that project no visible item at all, which positions alone
// cannot rule out.
func validateRemotePageAdjacency(candidates []appitempaging.TranscriptItemCandidate) error {
	for index := 1; index < len(candidates); index++ {
		previous := candidates[index-1].Position
		current := candidates[index].Position
		if current.Entry == previous.Entry && current.Item != previous.Item+1 {
			return fmt.Errorf("remote item page skips item positions in entry %d: %d then %d", previous.Entry, previous.Item, current.Item)
		}
	}
	return nil
}

// validateRemotePageCursor validates the cursor a remote page carries before the
// source retains it as the remote cursor behind a controller identity. A cursor is
// retained only when it canonically names the page that carried it: both sides of
// this stack mint it at exactly the packed page's oldest item
// (cmd/evener-hub's packedOlderCursor for a live page,
// apptranscript.ItemWindowOptions for a past one), so a cursor naming any other
// position came from a different page or a different observation and cannot be
// rebased onto a controller boundary without paging a region this window never
// proved. A page that names a continuation but carries no item has no boundary to
// canonicalize against at all, and retaining its cursor would re-ask the remote
// for the same unnamed page forever.
func validateRemotePageCursor(next string, candidates []appitempaging.TranscriptItemCandidate) error {
	if next == "" {
		return nil
	}
	if len(candidates) == 0 {
		return appwire.TranscriptItemCursorStale()
	}
	canonical, err := appitempaging.RebaseCursor(next, candidates[0].Position)
	if err != nil {
		return err
	}
	if canonical != next {
		return appwire.TranscriptItemCursorStale()
	}
	return nil
}

// validateRemoteContinuationPage applies to a remote continuation page the checks
// LocalDaemonSource applies to the daemon's, and clips the page to the requested
// boundary the same way, before the caller retains either the returned cursor or
// the page:
//
//   - the returned cursor must carry the identity of the cursor that was sent and
//     canonically name this page (validateRemotePageCursor), so a remote answering
//     from another incarnation, or handing back a page it already served, cannot
//     rotate or re-cache the retained window under a live controller cursor;
//   - the page must make strict backward progress past the boundary the remote was
//     asked to continue from. Items the remote re-reports at or above that boundary
//     are dropped rather than re-served, exactly as localDaemonCandidatesBefore
//     drops them, and a page with nothing older is stale: a repeated page would
//     otherwise be cached and mint a controller cursor that never advances.
func validateRemoteContinuationPage(
	request, next string,
	before appwire.ThreadItemPosition,
	candidates []appitempaging.TranscriptItemCandidate,
) ([]appitempaging.TranscriptItemCandidate, error) {
	if next != "" && len(candidates) > 0 {
		boundary := candidates[0].Position
		requestCanonical, err := appitempaging.RebaseCursor(request, boundary)
		if err != nil {
			return nil, err
		}
		nextCanonical, err := appitempaging.RebaseCursor(next, boundary)
		if err != nil {
			return nil, err
		}
		if requestCanonical != nextCanonical {
			return nil, appwire.TranscriptItemCursorStale()
		}
	}
	if err := validateRemotePageCursor(next, candidates); err != nil {
		return nil, err
	}
	older := make([]appitempaging.TranscriptItemCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if remotePositionCompare(candidate.Position, before) >= 0 {
			continue
		}
		older = append(older, candidate)
	}
	if len(older) == 0 {
		return nil, appwire.TranscriptItemCursorStale()
	}
	return older, nil
}

// continueCompleteRemoteItemPage answers a continuation of a page the remote hub
// returned in full. No older remote page exists, so the retained candidate
// snapshot is re-served locally before the caller's boundary, exactly as the
// local daemon source does for a complete snapshot. The remote hub is not
// called; the same identity and snapshot keep further continuations answerable.
// Only the window's provably contiguous prefix is served: a boundary above an
// unobserved span would have to include items this source never fetched, and
// with the remote cursor exhausted the honest answer is a stale cursor rather
// than a page that silently omits the middle.
func continueCompleteRemoteItemPage(cursor string, state remoteItemPagingState, itemLimit int) (ItemCandidateResult, error) {
	before, err := appitempaging.DecodeCursor(cursor, state.identity)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	selected, hasOlder, err := appitempaging.SelectCandidates(remoteContiguousPrefix(state), &before, itemLimit)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	if len(selected) == 0 {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	window := appitempaging.TranscriptItemWindow{Candidates: selected}
	if hasOlder {
		window.OlderCursor, err = appitempaging.EncodeCursor(state.identity, selected[0].Position)
		if err != nil {
			return ItemCandidateResult{}, err
		}
	}
	return ItemCandidateResult{Candidates: window, Identity: state.identity, Exhausted: !hasOlder}, nil
}

// remoteContiguousPrefix returns the leading run of retained candidates the
// window observed without a hole: everything from the oldest retained candidate up
// to the upper edge of the observed run that holds it. It is the whole window
// while one run covers it, and a forward page the source could not prove abuts the
// retained window ends it at the last provably observed candidate below the
// unobserved stretch.
func remoteContiguousPrefix(state remoteItemPagingState) []appitempaging.TranscriptItemCandidate {
	if len(state.candidates) == 0 {
		return nil
	}
	index := remoteSpanContaining(state.spans, state.candidates[0].Position)
	if index < 0 {
		// No recorded run covers the oldest candidate, so the window holds no proof
		// of contiguity: serve that candidate alone rather than guess across it.
		return state.candidates[:1]
	}
	upper := state.spans[index].upper
	prefix := len(state.candidates)
	for position, candidate := range state.candidates {
		if remotePositionCompare(candidate.Position, upper) > 0 {
			prefix = position
			break
		}
	}
	return state.candidates[:prefix]
}

// remoteSpanContaining returns the index of the observed run covering position, or
// -1 when no recorded run does.
func remoteSpanContaining(spans []remoteItemSpan, position appwire.ThreadItemPosition) int {
	for index, span := range spans {
		if remotePositionCompare(span.lower, position) <= 0 && remotePositionCompare(position, span.upper) <= 0 {
			return index
		}
	}
	return -1
}

// remoteSpanContains reports whether an observed run covers position.
func remoteSpanContains(span remoteItemSpan, position appwire.ThreadItemPosition) bool {
	return remotePositionCompare(span.lower, position) <= 0 && remotePositionCompare(position, span.upper) <= 0
}

// remoteCandidateAt returns the window's candidate at position, if it holds one.
// The window is keyed by position, so at most one candidate can match.
func remoteCandidateAt(candidates []appitempaging.TranscriptItemCandidate, position appwire.ThreadItemPosition) (appitempaging.TranscriptItemCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.Position == position {
			return candidate, true
		}
	}
	return appitempaging.TranscriptItemCandidate{}, false
}

// remoteItemPageHead returns the newest position of an item page, which is the
// last candidate of the strictly chronological window.
func remoteItemPageHead(candidates []appitempaging.TranscriptItemCandidate) (appwire.ThreadItemPosition, bool) {
	if len(candidates) == 0 {
		return appwire.ThreadItemPosition{}, false
	}
	return candidates[len(candidates)-1].Position, true
}

// remotePositionCompare orders two positions the way the paging package does:
// negative when a is older, zero when equal, positive when newer.
func remotePositionCompare(a, b appwire.ThreadItemPosition) int {
	if a.Entry < b.Entry || (a.Entry == b.Entry && a.Item < b.Item) {
		return -1
	}
	if a == b {
		return 0
	}
	return 1
}

// remoteItemPageIdentity chooses the controller-owned identity for a fresh
// remote page. The incarnation is reused while the page's newest position is
// not older than the retained one AND the page does not contradict the retained
// window or provably skip items: an untouched or appended transcript keeps
// outstanding cursors valid, so a re-read between a page and a scroll no longer
// invalidates them. A forward page that may have skipped a span is accepted too
// — entry ordinals advance for logical groups with no visible items, so
// requiring the exact successor position would stale a live cursor on an
// ordinary append — and the skipped span is recorded so no local answer spans
// it. A page that starts before the retained head, or that re-reports an
// observed position with a different item, is a rewrite, so the incarnation
// rotates and outstanding cursors fail closed. The remote hub stays the
// authority for its own opaque cursor; this fence only stops the controller's
// cursor namespace from churning on every compatible read.
func (s *RemoteHubSource) remoteItemPageIdentity(key string, candidates []appitempaging.TranscriptItemCandidate) (appitempaging.CursorIdentity, appwire.ThreadItemPosition, bool) {
	head, hasHead := remoteItemPageHead(candidates)
	if !hasHead {
		return s.mintRemoteItemIdentity(key), appwire.ThreadItemPosition{}, false
	}
	if state, ok := s.itemPaging.peek(key); ok && state.hasHead && remotePositionCompare(head, state.head) >= 0 {
		if _, compatible := remoteMergeCandidates(state.candidates, candidates); compatible {
			return state.identity, head, true
		}
	}
	return s.mintRemoteItemIdentity(key), head, true
}

// remoteMergeCandidates folds a newly observed page into the retained window,
// keeping it chronological. It reports ok=false when a position observed before
// now carries a different fingerprint, i.e. the item at that position was
// replaced and the transcript rewritten under this incarnation, and when a
// forward page provably skips items: a fragment cut mid-turn leaves items
// between the two pages that were never returned. Items are keyed by position:
// an item-mode projection positions every item uniquely, and a replacement at an
// observed position must be recognized rather than silently appended at the same
// boundary.
//
// Whether a forward page that merely *may* have skipped items is contiguous is not
// decided here: the caller records the observed runs
// (remoteItemSpansAfterObserving), so a page that cannot be shown to abut the
// retained window starts a run of its own instead of rotating the incarnation.
// Contiguity is not provenance: the exact successor position of the retained newest
// item can be computed from positions alone, but a later entry is not evidence of a
// hole, because entry ordinals advance for logical groups that project no item at
// all. A backward page is not gated here: it is fetched through the retained remote
// cursor, which is the remote hub's own authority for the next older page.
func remoteMergeCandidates(retained, observed []appitempaging.TranscriptItemCandidate) ([]appitempaging.TranscriptItemCandidate, bool) {
	if len(observed) == 0 {
		return retained, true
	}
	if len(retained) == 0 {
		return append([]appitempaging.TranscriptItemCandidate(nil), observed...), true
	}
	byPosition := make(map[appwire.ThreadItemPosition]appitempaging.TranscriptItemCandidate, len(retained))
	for _, candidate := range retained {
		byPosition[candidate.Position] = candidate
	}
	merged := append([]appitempaging.TranscriptItemCandidate(nil), retained...)
	overlap := false
	for _, candidate := range observed {
		if previous, ok := byPosition[candidate.Position]; ok {
			if transcriptItemFingerprint(previous) != transcriptItemFingerprint(candidate) {
				return nil, false
			}
			overlap = true
			continue
		}
		byPosition[candidate.Position] = candidate
		merged = append(merged, candidate)
	}
	if !overlap {
		retainedNewest := retained[len(retained)-1]
		observedOldest := observed[0]
		if remotePositionCompare(retainedNewest.Position, observedOldest.Position) < 0 &&
			remoteForwardMergeCutsItems(retainedNewest, observedOldest) {
			// Either fragment is cut mid-turn: items between the two pages exist and
			// were never returned, so the union cannot be served soundly.
			return nil, false
		}
	}
	slices.SortFunc(merged, func(a, b appitempaging.TranscriptItemCandidate) int {
		return remotePositionCompare(a.Position, b.Position)
	})
	return merged, true
}

// remotePositionsAdjacent reports whether newer is the position immediately
// after older. Item-mode positions are (entry, item) pairs, and an entry can
// project several items, so the successor of an item is the next item of the
// same entry or the first item (item 0) of the next entry. This is the only
// successor relationship positions alone can prove: a page that resumes at a
// later entry may still be contiguous (entry ordinals skip for logical groups
// with no visible items), but that cannot be proven locally, so
// remoteItemSpansAfterObserving records it as the start of a new observed run.
func remotePositionsAdjacent(older, newer appwire.ThreadItemPosition) bool {
	if older.Entry == newer.Entry {
		return newer.Item == older.Item+1
	}
	return newer.Entry == older.Entry+1 && newer.Item == 0
}

// remoteForwardMergeCutsItems reports whether merging a forward page across the
// retained window provably skips items. Either fragment can be cut mid-turn: the
// retained newest item's HasLaterItems says its turn continues after the retained
// window, and the observed oldest item's position within its turn (or its
// HasEarlierItems flag) says the page starts inside a turn. The position check is
// not redundant with the flag: a mixed-version remote that omits the flag still
// reports the position, and a page that starts mid-turn is a hole either way. A
// cross-entry page that starts its turn is not cut, because the entries between
// the two visible turns may be logical groups that projected no item at all.
func remoteForwardMergeCutsItems(older, newer appitempaging.TranscriptItemCandidate) bool {
	return older.HasLaterItems || newer.HasEarlierItems || newer.Position.Item != 0
}

// remoteItemSpansAfterObserving folds one observed page into the window's
// recorded runs.
//
// A remote page is internally contiguous — the hub returns consecutive items, and
// validateRemotePagePositions has already refused a page that skips an item index
// inside one entry — so the page contributes the run between its oldest and its
// newest item. That run is merged with every recorded run it is provably connected
// to, and runs it connects are merged with each other, so the recorded runs stay
// maximal. Connected means one of:
//
//   - the runs overlap, or the new run is exactly the successor position of the
//     recorded run's upper edge and that fragment is complete
//     (remotePositionsAdjacent plus HasLaterItems, the same proof
//     remoteMergeCandidates used for an abutting forward page);
//   - the recorded run covers the boundary the page was fetched from, because a
//     page returned for a retained remote cursor is the next older page in the
//     remote's own sequence, and nothing lies between them whether or not the
//     positions are adjacent. This is the evidence a position-only rule cannot
//     see, and it is what lets a run a forward append left be filled piecewise by
//     the continuations that walk the remote cursor down to the retained window.
//
// Runs that connect to none of these stay separate: the stretch between them was
// never observed, so no locally served answer may cross it.
func remoteItemSpansAfterObserving(
	window []appitempaging.TranscriptItemCandidate,
	spans []remoteItemSpan,
	observed []appitempaging.TranscriptItemCandidate,
	from *appwire.ThreadItemPosition,
) []remoteItemSpan {
	if len(observed) == 0 {
		return spans
	}
	frontier := remoteItemSpan{lower: observed[0].Position, upper: observed[len(observed)-1].Position}
	remaining := append([]remoteItemSpan(nil), spans...)
	for joined := true; joined; {
		joined = false
		kept := remaining[:0]
		for _, span := range remaining {
			if !remoteItemSpanJoins(window, frontier, span, from) {
				kept = append(kept, span)
				continue
			}
			frontier = remoteItemSpanUnion(frontier, span)
			joined = true
		}
		remaining = kept
	}
	merged := make([]remoteItemSpan, 0, len(remaining)+1)
	inserted := false
	for _, span := range remaining {
		if !inserted && remotePositionCompare(frontier.upper, span.lower) < 0 {
			merged = append(merged, frontier)
			inserted = true
		}
		merged = append(merged, span)
	}
	if !inserted {
		merged = append(merged, frontier)
	}
	return merged
}

// remoteItemSpanJoins reports whether an observed run and a recorded run are
// provably one contiguous stretch of the remote transcript. The window supplies
// the fragment-completeness flags the position-adjacency proof needs.
func remoteItemSpanJoins(
	window []appitempaging.TranscriptItemCandidate,
	run, span remoteItemSpan,
	from *appwire.ThreadItemPosition,
) bool {
	if from != nil && remoteSpanContains(span, *from) {
		return true
	}
	older, newer := run, span
	if remotePositionCompare(span.upper, run.lower) < 0 {
		older, newer = span, run
	}
	if remotePositionCompare(older.upper, newer.lower) >= 0 {
		return true
	}
	if !remotePositionsAdjacent(older.upper, newer.lower) {
		return false
	}
	candidate, ok := remoteCandidateAt(window, older.upper)
	return ok && !candidate.HasLaterItems
}

// remoteItemSpanUnion returns the smallest run covering both.
func remoteItemSpanUnion(left, right remoteItemSpan) remoteItemSpan {
	lower, upper := left.lower, left.upper
	if remotePositionCompare(right.lower, lower) < 0 {
		lower = right.lower
	}
	if remotePositionCompare(right.upper, upper) > 0 {
		upper = right.upper
	}
	return remoteItemSpan{lower: lower, upper: upper}
}

// remoteItemSpansForCandidates returns the single run a window of candidates
// covers when that window is one page, or the trimmed suffix of one page: its
// positions are strictly increasing, they hold no gap inside an entry
// (validateRemotePagePositions), and the page carried every position between its
// oldest and newest item.
func remoteItemSpansForCandidates(candidates []appitempaging.TranscriptItemCandidate) []remoteItemSpan {
	if len(candidates) == 0 {
		return nil
	}
	return []remoteItemSpan{{lower: candidates[0].Position, upper: candidates[len(candidates)-1].Position}}
}

// remoteRetainedCandidatesExceedBounds reports whether a merged retained window
// exceeds either retention bound, so the caller rotates the incarnation instead
// of accumulating an unbounded window.
func remoteRetainedCandidatesExceedBounds(candidates []appitempaging.TranscriptItemCandidate) bool {
	return len(candidates) > remoteItemPagingCandidateCapacity || remoteItemCandidatesBytes(candidates) > remoteItemPagingByteCapacity
}

// remoteItemCandidatesBytes measures the retained window's transcript payload so
// its memory can be bounded by bytes as well as by item count.
//
// The measurement walks the retained value itself instead of listing the fields it
// expects: every string, byte slice and map entry reachable from a candidate
// contributes its own length, so a variable-length field added to ThreadItem,
// InputItem, OutputImage, or Turn is counted as soon as it exists and no list here
// can fall behind. The hand-written sum this replaces counted only the payload
// fields its author remembered — ThreadItem.ID, ToolName, CallID, Source,
// SteeringKind, ClientMutationID and every field of the candidate's turn were
// invisible to it — so a remote page could retain megabytes per item while the
// estimate saw almost nothing.
//
// The one field not counted is the turn's item slice (appwire.Turn.Items): a page's
// candidates share that slice, its elements' payload is already counted through each
// candidate's own Item, and counting it per candidate would multiply a page's payload
// by its item count and rotate windows that fit the bound.
func remoteItemCandidatesBytes(candidates []appitempaging.TranscriptItemCandidate) int {
	total := 0
	for _, candidate := range candidates {
		total += remoteItemCandidateBytes(candidate)
	}
	return total
}

// remoteItemCandidateBytes measures one candidate's retained payload, including its
// turn's own fields and nested image payloads and metadata.
func remoteItemCandidateBytes(candidate appitempaging.TranscriptItemCandidate) int {
	return remoteRetainedPayloadBytes(reflect.ValueOf(candidate))
}

// remoteTurnType is the turn whose item slice remoteRetainedPayloadBytes leaves out
// of the measurement (see remoteItemCandidatesBytes).
var remoteTurnType = reflect.TypeFor[appwire.Turn]()

// remoteRetainedPayloadBytes sums the variable-length payload held by one retained
// value and everything reachable from it. Fixed-size fields (counts, flags, pointers,
// struct headers) contribute nothing: the candidate-count bound already caps that
// part of a window, while the byte bound exists to cap the unbounded part.
func remoteRetainedPayloadBytes(value reflect.Value) int {
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			return 0
		}
		return remoteRetainedPayloadBytes(value.Elem())
	case reflect.String:
		return value.Len()
	case reflect.Slice:
		if value.IsNil() {
			return 0
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			// A byte slice (json.RawMessage included) holds its bytes directly.
			return value.Len()
		}
		total := 0
		for index := 0; index < value.Len(); index++ {
			total += remoteRetainedPayloadBytes(value.Index(index))
		}
		return total
	case reflect.Array:
		total := 0
		for index := 0; index < value.Len(); index++ {
			total += remoteRetainedPayloadBytes(value.Index(index))
		}
		return total
	case reflect.Map:
		total := 0
		for entry := value.MapRange(); entry.Next(); {
			total += remoteRetainedPayloadBytes(entry.Key()) + remoteRetainedPayloadBytes(entry.Value())
		}
		return total
	case reflect.Struct:
		total := 0
		turn := value.Type() == remoteTurnType
		for field, fieldValue := range value.Fields() {
			if turn && field.Name == "Items" {
				continue
			}
			total += remoteRetainedPayloadBytes(fieldValue)
		}
		return total
	default:
		return 0
	}
}

// remoteTrimCandidatesToBound returns the newest suffix of candidates that fits
// remoteItemPagingCandidateCapacity and remoteItemPagingByteCapacity. The newest
// candidate is always kept even when it alone exceeds the byte budget, mirroring
// the packer's rule of retaining the nearest item past its soft result limit, so
// an oversized item cannot empty the window. Without this, a single page that
// exceeds the bound would be retained wholesale and the documented window bound
// would not hold.
func remoteTrimCandidatesToBound(candidates []appitempaging.TranscriptItemCandidate) []appitempaging.TranscriptItemCandidate {
	if !remoteRetainedCandidatesExceedBounds(candidates) {
		return candidates
	}
	start := len(candidates) - 1
	bytes := 0
	for start >= 0 {
		size := remoteItemCandidateBytes(candidates[start])
		if start < len(candidates)-1 {
			if len(candidates)-start > remoteItemPagingCandidateCapacity || bytes+size > remoteItemPagingByteCapacity {
				break
			}
		}
		bytes += size
		start--
	}
	return candidates[start+1:]
}

// recordRemoteItemPage builds the controller window for one remote page and
// retains the remote cursor behind the controller identity. from is the boundary
// a continuation page was fetched from through the retained remote cursor, and nil
// for a page fetched on its own (a fresh page or a materialized read); it is the
// traversal evidence remoteItemSpansAfterObserving uses to continue an observed
// run.
func (s *RemoteHubSource) recordRemoteItemPage(
	key string,
	identity appitempaging.CursorIdentity,
	candidates []appitempaging.TranscriptItemCandidate,
	native string,
	head appwire.ThreadItemPosition,
	hasHead bool,
	from *appwire.ThreadItemPosition,
) (ItemCandidateResult, error) {
	retained := []appitempaging.TranscriptItemCandidate(nil)
	spans := []remoteItemSpan(nil)
	if previous, ok := s.itemPaging.peek(key); ok && previous.identity == identity {
		retained = previous.candidates
		spans = previous.spans
	}
	windowCandidates := candidates
	merged, compatible := remoteMergeCandidates(retained, candidates)
	if !compatible || remoteRetainedCandidatesExceedBounds(merged) {
		// The fresh page contradicts the retained window, or the union would
		// exceed the retained bound, so the accumulated history cannot be served
		// soundly: rotate the incarnation and retain only this page, trimmed to
		// the newest candidates that fit the bound when the page alone exceeds it.
		// The cursor minted below stays live while its boundary is retained;
		// boundaries older than the bounded window fail closed as stale rather
		// than being answered from a truncated history.
		identity = s.mintRemoteItemIdentity(key)
		merged = append([]appitempaging.TranscriptItemCandidate(nil), remoteTrimCandidatesToBound(candidates)...)
		head, hasHead = remoteItemPageHead(merged)
		// The retained state is the trimmed suffix, so the window handed back and
		// the cursor minted from it must both name positions that suffix holds.
		// Returning the untrimmed page with a cursor at its oldest item would mint
		// a continuation the retained window cannot validate, so the first
		// continuation would fail ValidateCursorBoundary as stale.
		windowCandidates = merged
		// The retained window is one page, or the suffix of one, so it is a single
		// observed run.
		spans = remoteItemSpansForCandidates(merged)
	} else {
		spans = remoteItemSpansAfterObserving(merged, spans, candidates, from)
	}
	window := appitempaging.TranscriptItemWindow{Candidates: windowCandidates}
	state := remoteItemPagingState{identity: identity, native: native, candidates: merged, spans: spans, complete: native == "", head: head, hasHead: hasHead}
	if native == "" {
		// The identity is returned even when the page is complete: packing can
		// still drop the oldest item for size and needs an identity to mint a
		// continuation cursor from, exactly as the local-daemon source does. The
		// merged window is retained so that continuation, and every boundary
		// observed under this identity below any unobserved span, can be served
		// locally.
		s.itemPaging.put(key, state)
		return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: true}, nil
	}
	if len(windowCandidates) == 0 {
		return ItemCandidateResult{}, appwire.TranscriptItemCursorStale()
	}
	cursor, err := appitempaging.EncodeCursor(identity, windowCandidates[0].Position)
	if err != nil {
		return ItemCandidateResult{}, err
	}
	window.OlderCursor = cursor
	s.itemPaging.put(key, state)
	return ItemCandidateResult{Candidates: window, Identity: identity, Exhausted: false}, nil
}

func (s *RemoteHubSource) mintRemoteItemIdentity(key string) appitempaging.CursorIdentity {
	return appitempaging.CursorIdentity{
		ThreadRef:         key,
		Incarnation:       fmt.Sprintf("remote-hub-incarnation-%d", remoteHubItemIncarnationSequence.Add(1)),
		ProjectionVersion: remoteHubItemCursorProjectionVersion,
	}
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
//
// This mapping is right for the read-only methods on the proxy's allow-list.
// A forwarded method that mutates the host uses AdminMutationCall instead,
// whose lost-response mapping reports the mutation outcome as unknown.
func (s *RemoteHubSource) AdminCall(ctx context.Context, method string, params json.RawMessage, out *json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return s.mapCallError(err)
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		// Resolving the client is non-dialing (the attached-only lookup), so a
		// failure here means either the host has no live channel — reported as a
		// SessionUnavailable the auto-resume gate can act on — or the caller's own
		// context ended. This is the connect mapper, matching call and
		// mutationCall.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.mapConnectError(err)
	}
	if err := client.Request(ctx, method, params, out); err != nil {
		return s.mapCallError(err)
	}
	return nil
}

// AdminMutationCall forwards one allow-listed hub-scoped admin RPC that is a
// non-idempotent mutation (component 07a). It is AdminCall's mutating twin: the
// request/response path is identical, and the remote's own result or semantic
// refusal is returned verbatim, but a lost response is mapped differently.
//
// A transport failure on a forwarded mutation cannot be told apart from one
// where the remote applied the change and only the answer was lost. Reporting
// that as SessionUnavailable — AdminCall's mapping, correct for the read-only
// methods — would invite exactly the blind retry that is unsafe for an
// operation with no idempotency key: a retried instance/create makes a second
// instance and a retried plugin/install a second install. The loss therefore
// becomes ErrorMutationOutcomeUnknown with RetryDispositionBlocked, so the
// caller is told the outcome is unknown and is not told to retry automatically.
//
// It mirrors mutationCall's remoteHubMutationCallError except for that
// disposition. A forwarded thread mutation carries a clientMutationId the
// remote dedups on, so the appwire retry-safe-mutation model can call its
// transport loss "automatic"; an admin forward carries no such id
// (HostRequestParams has none) and no admin method dedups, so the same loss is
// "blocked". A semantic WireError keeps its code and message, exactly as on
// AdminCall.
//
// Only a failure of client.Request is a possible lost response. A failure to
// acquire the client — the host is offline, the attach is refused, the dial
// fails — happens before any request frame is sent, so the mutation provably
// did not reach the host and the outcome is not unknown. That failure maps
// through mapCallError exactly as on AdminCall (SessionUnavailable for a dead
// channel), which is a safe retry; reporting it as outcome-unknown/blocked
// would discourage a retry that cannot double-apply anything.
//
// The caller's own context ending while the call is in flight is reported the
// same blocked way (round seven). The browser disconnecting cancels the RPC
// handler's context, and appwire.Client reports that identically whether it
// stopped the frame write (StreamTransport.Send's own ctx check,
// appwire/stream_transport.go) or the response wait after the frame went out
// (Client.request's select on ctx.Done(), appwire/client.go). Nothing in the
// client's API distinguishes those two once the frame has been dispatched, so
// the post-send reading — the response was lost, the host may have applied the
// change — is the only safe one: the raw cancellation this used to return reads
// as "nothing happened, retry", which duplicates a forwarded instance/create or
// plugin/install whenever the frame did reach the host. The pre-call ctx check
// inside the call stays, because a context already canceled before the request
// is handed to the client cannot have sent anything.
//
// The client does distinguish one earlier window (round eight): a context that
// has already ended when the request reaches the point where its frame would be
// written — typically queued behind another writer on the client's single write
// slot, since a controller browser can have two admin RPCs in flight on one
// host at once — comes back as appwire.RequestNotSentError, which proves nothing
// was transmitted, and since round nine it does so as soon as the context ends
// rather than waiting for the writer ahead of it. That case maps exactly as the
// pre-call check above does: a raw caller context error (cancellation or
// expired deadline — both stay raw, as they do on every other path), and never
// outcome-unknown, because a mutation that never reached the host cannot have
// been applied and blocking its retry is simply wrong. Only from dispatch onward
// does the ambiguous in-flight reading apply.
func (s *RemoteHubSource) AdminMutationCall(ctx context.Context, method string, params json.RawMessage, out *json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		// Provably not sent: this runs before the request is handed to the
		// client, so the caller's context ending maps exactly as AdminCall maps
		// it (a raw cancellation or expired deadline), which is a safe retry.
		return s.mapCallError(err)
	}
	client, err := s.resolveClient(ctx)
	if err != nil {
		// Resolving the client never dials, so a failure here never reached the
		// wire: it keeps the same safe-retry mapping as the pre-call check (a raw
		// caller cancellation or deadline), and an unattached host classifies
		// through the connect mapper as SessionUnavailable, exactly as call and
		// mutationCall do.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return s.mapConnectError(err)
	}
	if err := client.Request(ctx, method, params, out); err != nil {
		return s.remoteHubAdminMutationCallError(err)
	}
	return nil
}

// remoteHubAdminMutationCallError turns a transport-level loss on a forwarded
// admin mutation into an explicit outcome-unknown error.
//
// Four shapes reach it. A pre-send failure the client proved never reached the
// transport (appwire.RequestNotSentError — the caller's context ended while the
// request was queued on the client's write slot) is not a lost response at all,
// so it maps exactly as the pre-call context check does: a raw cancellation or
// expired deadline, both safe retries. A context end
// the in-flight call observed (the caller's cancellation or deadline) is the
// ambiguous case described on AdminMutationCall: it becomes
// outcome-unknown/blocked directly, so the classification no longer depends on
// mapCallError turning a deadline into SessionUnavailable and the
// unavailability re-label below catching it. A semantic wire refusal keeps its
// own code and message, exactly as on AdminCall. Everything else is mapped by
// mapCallError, and only an unavailability it produced is re-labelled: that
// mapping stays deliberately narrow because mapCallError's other outcomes are
// not lost responses.
func (s *RemoteHubSource) remoteHubAdminMutationCallError(err error) error {
	if _, notSent := errors.AsType[appwire.RequestNotSentError](err); notSent {
		// Provably not transmitted, so the mutation provably did not happen:
		// report it the way the pre-call context check reports an unsent call
		// rather than as an unknown outcome that blocks a safe retry.
		return s.mapCallError(err)
	}
	var refused appwire.WireError
	if !errors.As(err, &refused) && callerContextEnded(err) {
		return s.hubAdminMutationOutcomeUnknown(
			"mutation outcome is unknown after the caller's context ended while the remote hub call was in flight")
	}
	mapped := s.mapCallError(err)
	var wire appwire.WireError
	if !errors.As(mapped, &wire) {
		return mapped
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if wire.Code != appwire.CodeUnavailable || !ok || data.EvenerErrorInfo != appwire.ErrorSessionUnavailable {
		return mapped
	}
	return s.hubAdminMutationOutcomeUnknown("mutation outcome is unknown after remote hub response loss")
}

// callerContextEnded reports whether err is the caller's own context ending
// while the call was in flight, as appwire.Client reports it: a bare
// context.Canceled or context.DeadlineExceeded. A remote's own refusal is a
// WireError and never reaches this test — remoteHubAdminMutationCallError
// checks that first — so a semantic error keeps its code and message.
func callerContextEnded(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// hubAdminMutationOutcomeUnknown builds the blocked-retry error every lost
// forwarded admin mutation is reported as. The message names the host (the id
// is the source's identity in the controller's registry) and the reason, so an
// operator reading a hub log can tell a lost response from a caller-side
// context end.
func (s *RemoteHubSource) hubAdminMutationOutcomeUnknown(reason string) appwire.WireError {
	return appwire.WireError{
		Code:    appwire.CodeInternalError,
		Message: reason + ": " + s.id,
		Data: appwire.ErrorData{
			EvenerErrorInfo:  appwire.ErrorMutationOutcomeUnknown,
			MutationOutcome:  appwire.MutationOutcomeUnknown,
			RetryDisposition: appwire.RetryDispositionBlocked,
		},
	}
}

// remoteHubHostSubscription is one host-level notification consumer, e.g. the
// component-07a remote-admin fan-out.
//
// Two channels with one writer each, mirroring remoteHubSubscription. in is
// written only by the owning client's drainLoop and is NEVER closed, so a
// delivery can never panic on a closed channel, and a full buffer drops rather
// than blocking the shared drain. out is written and closed only by the
// subscription's pump goroutine, so it is out's sole owner: the pump closes it
// on context end or on client teardown, which is what makes the returned
// channel's documented closure contract hold on every path. done is closed by
// the pump on exit so a publisher can skip a subscription whose pump is already
// gone. clientDone is closed by the owning client's drainLoop when that client
// is torn down, which unblocks a pump parked on a full out and ends the
// subscription even though in is never closed.
type remoteHubHostSubscription struct {
	client     *appwire.Client
	in         chan appwire.Notification
	out        chan appwire.Notification
	done       chan struct{}
	clientDone chan struct{}
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
	client, err := s.resolveClient(ctx)
	if err != nil {
		return nil, s.mapCallError(err)
	}
	sub := &remoteHubHostSubscription{
		client:     client,
		in:         make(chan appwire.Notification, remoteHubSubBuffer),
		out:        make(chan appwire.Notification, remoteHubSubBuffer),
		done:       make(chan struct{}),
		clientDone: make(chan struct{}),
	}
	s.subMu.Lock()
	s.hostSubs[sub] = struct{}{}
	s.ensureDrainLocked(client)
	s.subMu.Unlock()
	go s.pumpHostSubscription(ctx, sub)
	return sub.out, nil
}

// HostNotificationSubscribers reports how many host-level notification
// consumers are attached to this source right now. A subscription is counted
// from registration until its pump exits — on its own context ending or on the
// owning client's teardown — so the count is the observable form of "a consumer
// like the component-07a fan-out is still subscribed". A server-lifecycle test
// uses it to prove a shut-down hub released its fan-out's subscription, the
// same way SubscriberCount exposes thread subscriptions on the RPC server.
func (s *RemoteHubSource) HostNotificationSubscribers() int {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	return len(s.hostSubs)
}

// publishHostNotification delivers one remote notification to every host-level
// consumer owned by client. It runs on that client's drain goroutine only, so it
// is the sole writer of each of its subscriptions' in channels; in is never
// closed, so no send can race a close, and no notification from one client
// connection can reach a subscription owned by another.
//
// Delivery is non-blocking: a full consumer buffer drops the notification and
// counts it. Unlike thread routing — where a dropped turn/completed is the
// failure this component exists to prevent — a dropped config broadcast only
// leaves a settings pane stale until its next refresh, whereas blocking here
// would stall the shared drain and therefore every thread notification for the
// same client. A healthy consumer's pump keeps reading in, so drops are the
// exception rather than the rule.
//
// The filter is applied before the matching subscriptions are snapshotted, so a
// notification the filter rejects — the high-frequency thread/streaming
// families that are the overwhelming majority of traffic on this goroutine —
// costs a lock and a method comparison and builds no slice. Only a notification
// the filter accepts pays for the snapshot.
func (s *RemoteHubSource) publishHostNotification(client *appwire.Client, notification appwire.Notification) {
	s.subMu.Lock()
	filter := s.hostFilter
	if filter != nil && !filter(notification.Method) {
		s.subMu.Unlock()
		return
	}
	subs := make([]*remoteHubHostSubscription, 0, len(s.hostSubs))
	for sub := range s.hostSubs {
		if sub.client == client {
			subs = append(subs, sub)
		}
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
// context ending or the owning client being torn down. The clientDone case in
// the out send is what makes the second path reachable even when a full out
// would otherwise park the pump.
func (s *RemoteHubSource) pumpHostSubscription(ctx context.Context, sub *remoteHubHostSubscription) {
	defer close(sub.out)
	defer close(sub.done)
	defer s.unregisterHostSubscription(sub)
	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.clientDone:
			return
		case notification := <-sub.in:
			select {
			case sub.out <- notification:
			case <-ctx.Done():
				return
			case <-sub.clientDone:
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
