package server

import (
	"context"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

type sessionActivityHooks struct {
	activityRead  func(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error)
	delegatesList func(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error)
	jobsList      func(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error)
	watchesList   func(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error)
}

// SetThreadActivityReadFunc installs the context-bearing session activity read.
func (s *Server) SetThreadActivityReadFunc(fn func(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error)) {
	s.mu.Lock()
	s.appSessionActivity.activityRead = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadActivityRead(ctx context.Context, params appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
	return handleSessionActivity(ctx, s, params.Ref, params, func(h sessionActivityHooks) func(context.Context, appwire.SessionActivityReadParams) (appwire.SessionActivitySummary, error) {
		return h.activityRead
	})
}

// SetThreadDelegatesListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadDelegatesListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.delegatesList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadDelegatesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	return handleSessionActivity(ctx, s, params.Ref, params, func(h sessionActivityHooks) func(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
		return h.delegatesList
	})
}

// SetThreadJobsListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadJobsListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.jobsList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadJobsList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	return handleSessionActivity(ctx, s, params.Ref, params, func(h sessionActivityHooks) func(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
		return h.jobsList
	})
}

// SetThreadWatchesListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadWatchesListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.watchesList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadWatchesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	return handleSessionActivity(ctx, s, params.Ref, params, func(h sessionActivityHooks) func(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
		return h.watchesList
	})
}

// handleSessionActivity serves one session activity route: it refuses a ref
// this daemon does not serve or a route with no installed read, then runs the
// read fenced against a root replacement.
func handleSessionActivity[P, R any](ctx context.Context, s *Server, ref string, params P, pick func(sessionActivityHooks) func(context.Context, P) (R, error)) (R, error) {
	s.mu.RLock()
	fn := pick(s.appSessionActivity)
	sourceID := sourceIDForProjection(s.appSourceID)
	s.mu.RUnlock()
	var zero R
	if err := validateSessionActivityRef(ctx, ref, sourceID); err != nil {
		return zero, err
	}
	if fn == nil {
		return zero, appwire.SessionUnavailable("session activity is unavailable")
	}
	return readCurrentSessionActivity(s, ref, func() (R, error) { return fn(ctx, params) })
}

// readCurrentSessionActivity fences slow results across a root replacement.
// A stable workspace ref can survive clear, but its old session data cannot.
// Explicit descendant reads keep their own addressed source across root clear.
func readCurrentSessionActivity[R any](server *Server, ref string, read func() (R, error)) (R, error) {
	server.mu.RLock()
	rootID, rootRef := server.appRootIdentityLocked()
	rootAddressed := ref == rootRef || ref == (appwire.Ref{SourceID: sourceIDForProjection(server.appSourceID), ThreadID: rootID}).String()
	server.mu.RUnlock()
	result, err := read()
	if err != nil {
		return result, err
	}
	server.mu.RLock()
	currentID, currentRef := server.appRootIdentityLocked()
	server.mu.RUnlock()
	if rootAddressed && (rootID != currentID || rootRef != currentRef) {
		var zero R
		return zero, appwire.SessionActivityCursorStale()
	}
	return result, nil
}

func validateSessionActivityRef(ctx context.Context, rawRef, sourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, err := appwire.ParseRef(strings.TrimSpace(rawRef))
	if err != nil {
		return appwire.InvalidParams(err.Error())
	}
	if ref.SourceID != sourceID {
		return appwire.SessionUnavailable("source not found: " + ref.SourceID)
	}
	return nil
}

func (s *Server) registerSessionActivityHandlers() {
	appserver.HandleTyped(s.appServer.Router(), appwire.MethodEvenerThreadActivityRead, s.handleThreadActivityRead)
	appserver.HandleTyped(s.appServer.Router(), appwire.MethodEvenerThreadDelegatesList, s.handleThreadDelegatesList)
	appserver.HandleTyped(s.appServer.Router(), appwire.MethodEvenerThreadJobsList, s.handleThreadJobsList)
	appserver.HandleTyped(s.appServer.Router(), appwire.MethodEvenerThreadWatchesList, s.handleThreadWatchesList)
}

// activityChangeNotificationLocked addresses the controller-selected subscriber
// target while preserving the affected owner independently of the physical
// session whose event bridge carried the invalidation.
func (s *Server) activityChangeNotificationLocked(event events.SessionEvent) []pendingAppNotification {
	if event.Kind != events.EventSessionActivityChanged {
		return nil
	}
	change, ok := event.Data.(events.SessionActivityChangedData)
	if !ok || change.ThreadID == "" || change.SessionID == "" || len(change.Resources) == 0 {
		return nil
	}
	threadID := change.ThreadID
	ref := appwire.Ref{SourceID: sourceIDForProjection(s.appSourceID), ThreadID: threadID}.String()
	rootID, rootRef := s.appRootIdentityLocked()
	if threadID == rootID {
		ref = rootRef
	}
	params := appwire.SessionActivityChangedParams{
		ThreadID: threadID, Ref: ref, SessionID: change.SessionID,
		Resources: append([]appwire.SessionActivityResource(nil), change.Resources...),
	}
	return []pendingAppNotification{{threadID: threadID, ref: ref, method: appwire.NotifyEvenerThreadActivityChanged, params: params}}
}
