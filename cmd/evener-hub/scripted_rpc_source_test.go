package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

type exactRPCSource struct {
	*scriptedAppSource
	mu                              sync.Mutex
	readErr, turnsErr, subscribeErr error
	notifications                   chan appwire.Notification
	startThread                     appwire.Thread
	resumeThread                    appwire.Thread
	started                         chan struct{}
	release                         chan struct{}
	startTurnErr                    error
	startTurnErrors                 []error
	canceled                        chan struct{}
}

func (s *exactRPCSource) ReadThread(ctx context.Context, p appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if s.readErr != nil {
		return appwire.ThreadReadResponse{}, s.readErr
	}
	return s.scriptedAppSource.ReadThread(ctx, p)
}

func (s *exactRPCSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	if s.turnsErr != nil {
		return appwire.ThreadTurnsListResponse{}, s.turnsErr
	}
	return appwire.ThreadTurnsListResponse{Data: s.thread.Turns}, nil
}

func (s *exactRPCSource) SubscribeThread(ctx context.Context, _ appwire.ThreadReadParams) (<-chan appwire.Notification, error) {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.subscribeErr != nil {
		return nil, s.subscribeErr
	}
	if s.notifications != nil {
		return s.notifications, nil
	}
	ch := make(chan appwire.Notification)
	go func() {
		<-ctx.Done()
		if s.canceled != nil {
			close(s.canceled)
		}
		close(ch)
	}()
	return ch, nil
}

func (s *exactRPCSource) StartThread(context.Context, appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	return appwire.ThreadStartResponse{Thread: s.startThread}, nil
}

func (s *exactRPCSource) ResumeThread(context.Context, appwire.ThreadResumeParams) (appwire.ThreadResumeResponse, error) {
	return appwire.ThreadResumeResponse{Thread: s.resumeThread}, nil
}

func (s *exactRPCSource) StartTurn(context.Context, appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
	s.mu.Lock()
	if len(s.startTurnErrors) > 0 {
		err := s.startTurnErrors[0]
		s.startTurnErrors = s.startTurnErrors[1:]
		s.mu.Unlock()
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_exact"}}, err
	}
	s.mu.Unlock()
	if s.startTurnErr != nil {
		return appwire.TurnStartResponse{}, s.startTurnErr
	}
	return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_exact"}}, nil
}

func exactDispatch(ctx context.Context, t *testing.T, server *appserver.Server, method string, params any) (any, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return server.Router().Dispatch(ctx, appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: raw})
}
