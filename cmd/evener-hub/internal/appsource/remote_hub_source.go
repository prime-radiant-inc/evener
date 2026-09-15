package appsource

import (
	"context"
	"errors"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"syscall"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
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
// This is components 05a-05c: the read path (ID, ListThreads, ReadThread,
// ListTurns, ListModels) plus registration, subscription fan-out, and the
// turn/thread lifecycle mutations (including mutation-unknown mapping) that
// live in remote_hub_mutations.go. The capability probe (05d) is still staged.
//
// The client is never cached here: every request re-invokes the connector, so a
// component-04 reconnect that swaps the underlying client is picked up
// automatically.
type RemoteHubSource struct {
	id     string
	roots  []string
	client RemoteHubClientFunc

	subMu  sync.Mutex
	subs   map[string]*remoteHubSubscription // key: remote thread ID
	drains map[*appwire.Client]struct{}      // clients whose notification stream is being drained
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
	// The production connector (sshManager.Ensure) reports a bridge that could
	// not be spawned or handshaken as sshconn.ErrSSHStart, wrapping an
	// *exec.ExitError or plain stderr text that carries none of the
	// transport-shaped strings below. It is retryable — the remote host is
	// merely unreachable right now — so it must become SessionUnavailable for
	// the auto-resume gate. The terminal sshconn classes (ErrSSHAuth,
	// ErrHostNotFound, ErrProtocolIncompatible, …) fall through to `return err`
	// untouched: retrying them cannot succeed.
	if errors.Is(err, sshconn.ErrSSHStart) {
		return appwire.SessionUnavailable("remote hub unavailable: " + s.id + ": " + err.Error())
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
	// A non-empty filter that does not name this source excludes it: answer
	// empty without forwarding. remapRemoteSourceIDs on its own maps such a
	// filter to an empty slice, and SourceIDs is `omitempty` on the wire, so the
	// omitted slice would ask the remote hub for ALL of its sources — the
	// opposite of what the caller requested. The hub's thread/list fan-out
	// (sourceAllowedForList) already skips this source in that case; the source
	// method must not depend on its caller for its own filter contract.
	if len(params.SourceIDs) > 0 && !slices.Contains(params.SourceIDs, s.id) {
		return appwire.ThreadListResponse{}, nil
	}
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
