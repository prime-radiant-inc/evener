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
	s.mu.RLock()
	fn := s.appSessionActivity.activityRead
	sourceID := sourceIDForProjection(s.appSourceID)
	s.mu.RUnlock()
	if err := validateSessionActivityRef(ctx, params.Ref, sourceID); err != nil {
		return appwire.SessionActivitySummary{}, err
	}
	if fn == nil {
		return appwire.SessionActivitySummary{}, appwire.SessionUnavailable("session activity is unavailable")
	}
	return fn(ctx, params)
}

// SetThreadDelegatesListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadDelegatesListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.delegatesList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadDelegatesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionDelegatesResponse, error) {
	s.mu.RLock()
	fn := s.appSessionActivity.delegatesList
	sourceID := sourceIDForProjection(s.appSourceID)
	s.mu.RUnlock()
	if err := validateSessionActivityRef(ctx, params.Ref, sourceID); err != nil {
		return appwire.SessionDelegatesResponse{}, err
	}
	if fn == nil {
		return appwire.SessionDelegatesResponse{}, appwire.SessionUnavailable("session activity is unavailable")
	}
	return fn(ctx, params)
}

// SetThreadJobsListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadJobsListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.jobsList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadJobsList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionJobsResponse, error) {
	s.mu.RLock()
	fn := s.appSessionActivity.jobsList
	sourceID := sourceIDForProjection(s.appSourceID)
	s.mu.RUnlock()
	if err := validateSessionActivityRef(ctx, params.Ref, sourceID); err != nil {
		return appwire.SessionJobsResponse{}, err
	}
	if fn == nil {
		return appwire.SessionJobsResponse{}, appwire.SessionUnavailable("session activity is unavailable")
	}
	return fn(ctx, params)
}

// SetThreadWatchesListFunc installs the context-bearing session activity read.
func (s *Server) SetThreadWatchesListFunc(fn func(context.Context, appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error)) {
	s.mu.Lock()
	s.appSessionActivity.watchesList = fn
	s.mu.Unlock()
}

func (s *Server) handleThreadWatchesList(ctx context.Context, params appwire.SessionActivityListParams) (appwire.SessionWatchesResponse, error) {
	s.mu.RLock()
	fn := s.appSessionActivity.watchesList
	sourceID := sourceIDForProjection(s.appSourceID)
	s.mu.RUnlock()
	if err := validateSessionActivityRef(ctx, params.Ref, sourceID); err != nil {
		return appwire.SessionWatchesResponse{}, err
	}
	if fn == nil {
		return appwire.SessionWatchesResponse{}, appwire.SessionUnavailable("session activity is unavailable")
	}
	return fn(ctx, params)
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
