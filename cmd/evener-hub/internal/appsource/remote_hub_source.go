package appsource

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
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

	subMu  sync.Mutex
	subs   map[string]*remoteHubSubscription // key: remote thread ID
	drains map[*appwire.Client]struct{}      // clients whose notification stream is being drained

	// probeMu guards probe (the last successful probe, cached against the
	// client it ran on) and facts (the preflight seam HostCapabilities reads
	// while probing). It is never held across a wire call.
	probeMu sync.Mutex
	probe   *remoteHubProbe
}

var _ Source = (*RemoteHubSource)(nil)

func NewRemoteHubSource(id string, roots []string, client RemoteHubClientFunc) *RemoteHubSource {
	return &RemoteHubSource{
		id:     id,
		roots:  roots,
		client: client,
		subs:   map[string]*remoteHubSubscription{},
		drains: map[*appwire.Client]struct{}{},
	}
}

func (s *RemoteHubSource) ID() string { return s.id }

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
	client, err := s.client(ctx, s.id)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
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
