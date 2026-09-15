package appsource

import (
	"context"
	"errors"
	"fmt"
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
// This is component 05a: the read path (ID, ListThreads, ReadThread, ListTurns,
// ListModels) plus registration, and component 05b: SubscribeThread and the
// per-host notification fan-out that routes remote notifications to the
// controller relays watching each remote thread. Turn/thread lifecycle
// mutations and mutation-unknown mapping (05c) and the capability probe (05d)
// are staged; their interface methods exist and fail loudly until then.
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
	// remoteMu serializes the wire-level subscribe and unsubscribe a
	// subscription's lifecycle emits against the routing-table mutation that
	// decides them, so a replacement's subscribe can never land before its
	// predecessor's unsubscribe for the same remote thread. subMu is always
	// taken inside it, never the other way around.
	remoteMu sync.Mutex
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
func (s *RemoteHubSource) call(ctx context.Context, method string, params any, out any) error {
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
	// The remote client is shared by every controller relay for this host, so
	// controller-level replacement semantics must never reach it: the remote
	// hub's replaceSubscription read scopes the whole connection to this one
	// thread, silently dropping every other remote thread's subscription while
	// their local routing entries stayed live. Replacement is a controller-side
	// concept — app_relay.go applies it to the controller's own subscriptions —
	// and SubscribeThread clears the flag for the same reason.
	remote.ReplaceSubscription = false
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

// notImplemented is the staged-method error for interface methods 05b/05c/05d
// will fill in. It is deliberately loud and names the Go method so a wiring
// mistake surfaces instead of silently degrading.
func (s *RemoteHubSource) notImplemented(method string) error {
	return appwire.InternalError(fmt.Sprintf("remote hub source: %s is not implemented yet", method))
}

func (s *RemoteHubSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{}, s.notImplemented("StartThread")
}

func (s *RemoteHubSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{}, s.notImplemented("ResumeThread")
}

func (s *RemoteHubSource) ForkThread(context.Context, appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	return appwire.ThreadForkResponse{}, s.notImplemented("ForkThread")
}

func (s *RemoteHubSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	return appwire.TurnStartResponse{}, s.notImplemented("StartTurn")
}

func (s *RemoteHubSource) SteerTurn(context.Context, appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
	return appwire.TurnSteerResponse{}, s.notImplemented("SteerTurn")
}

func (s *RemoteHubSource) ResolveSandboxEscalation(context.Context, appwire.SandboxEscalationResolveParams) error {
	return s.notImplemented("ResolveSandboxEscalation")
}

func (s *RemoteHubSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, s.notImplemented("InterruptTurn")
}

func (s *RemoteHubSource) QueueTurn(context.Context, appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
	return appwire.TurnQueueResponse{}, s.notImplemented("QueueTurn")
}

func (s *RemoteHubSource) DrainAsSteer(context.Context, appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
	return appwire.TurnDrainAsSteerResponse{}, s.notImplemented("DrainAsSteer")
}

func (s *RemoteHubSource) PromoteQueuedAsSteer(context.Context, appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
	return appwire.TurnPromoteQueuedAsSteerResponse{}, s.notImplemented("PromoteQueuedAsSteer")
}

func (s *RemoteHubSource) CancelQueued(context.Context, appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
	return appwire.TurnCancelQueuedResponse{}, s.notImplemented("CancelQueued")
}

func (s *RemoteHubSource) CompactThread(context.Context, appwire.ThreadCompactStartParams) error {
	return s.notImplemented("CompactThread")
}

func (s *RemoteHubSource) ShutdownThread(context.Context, appwire.ThreadShutdownParams) error {
	return s.notImplemented("ShutdownThread")
}

func (s *RemoteHubSource) SetThreadModel(context.Context, appwire.ThreadModelSetParams) error {
	return s.notImplemented("SetThreadModel")
}

func (s *RemoteHubSource) SetThreadReasoningEffort(context.Context, appwire.ThreadReasoningEffortSetParams) error {
	return s.notImplemented("SetThreadReasoningEffort")
}

func (s *RemoteHubSource) SetThreadVisionModel(context.Context, appwire.ThreadVisionModelSetParams) error {
	return s.notImplemented("SetThreadVisionModel")
}

func (s *RemoteHubSource) SetThreadName(context.Context, appwire.ThreadNameSetParams) error {
	return s.notImplemented("SetThreadName")
}

func (s *RemoteHubSource) GoalSet(context.Context, appwire.GoalSetParams) (appwire.GoalSetResponse, error) {
	return appwire.GoalSetResponse{}, s.notImplemented("GoalSet")
}

func (s *RemoteHubSource) NotesHumanSet(context.Context, appwire.NotesHumanSetParams) (appwire.NotesHumanSetResponse, error) {
	return appwire.NotesHumanSetResponse{}, s.notImplemented("NotesHumanSet")
}

func (s *RemoteHubSource) UrlsRemove(context.Context, appwire.UrlsRemoveParams) (appwire.UrlsRemoveResponse, error) {
	return appwire.UrlsRemoveResponse{}, s.notImplemented("UrlsRemove")
}

func (s *RemoteHubSource) ClearThread(context.Context, appwire.ThreadClearParams) (appwire.ThreadClearResponse, error) {
	return appwire.ThreadClearResponse{}, s.notImplemented("ClearThread")
}

func (s *RemoteHubSource) ListTasks(context.Context, appwire.TaskListParams) (appwire.TaskListResponse, error) {
	return appwire.TaskListResponse{}, s.notImplemented("ListTasks")
}

func (s *RemoteHubSource) ListJobs(context.Context, appwire.JobsListParams) (appwire.JobsListResponse, error) {
	return appwire.JobsListResponse{}, s.notImplemented("ListJobs")
}

func (s *RemoteHubSource) JobOutput(context.Context, appwire.JobsOutputParams) (appwire.JobsOutputResponse, error) {
	return appwire.JobsOutputResponse{}, s.notImplemented("JobOutput")
}
