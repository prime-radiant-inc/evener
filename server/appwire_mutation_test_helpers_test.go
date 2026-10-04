package server

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"primeradiant.com/evener/appwire"
)

// wireRetrySafeCapabilities installs the retry-safe steer, queue and interrupt
// seams the AppWire handlers actually dispatch through (handleAppTurnSteer,
// handleAppTurnQueue, handleAppTurnInterrupt). Capabilities follow those
// registrations; compatibility setters cannot enable an RPC handler.
func wireRetrySafeCapabilities(s *Server) {
	s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
		Interrupt: func(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			return appwire.TurnInterruptResponse{}, nil
		},
	})
}

// installProjectedMutationCallbacksForTest keeps older projector-focused tests
// explicit about their fake mutation authority. Production AppWire handlers do
// not consult these server projections; the callback bundle below is the test
// double for the Session-owned compare-and-commit layer.
func installProjectedMutationCallbacksForTest(s *Server, functions RetrySafeTurnFunctions) {
	s.mu.RLock()
	cancel := s.cancelFunc
	s.mu.RUnlock()

	if functions.Start == nil {
		functions.Start = func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			text, images := inputFromItems("", params.Input)
			turnID, err := s.reserveAppTurnIDForStart()
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			select {
			case s.inputCh <- InputMessage{Text: text, Images: images}:
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress}}, nil
			default:
				s.releaseAppTurnID(turnID)
				return appwire.TurnStartResponse{}, appwire.Conflict("input buffer full")
			}
		}
	}
	if steer := functions.Steer; steer != nil {
		functions.Steer = func(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			s.mu.RLock()
			reservedTurnID := s.appReservedTurnID
			processing := s.processing
			s.mu.RUnlock()
			if !processing && strings.TrimSpace(reservedTurnID) == "" {
				return appwire.TurnSteerResponse{}, appwire.Conflict("turn is not active")
			}
			return steer(params)
		}
	}
	if queue := functions.Queue; queue != nil {
		functions.Queue = func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			s.mu.RLock()
			processing := s.processing
			reservedTurnID := s.appReservedTurnID
			closed := appStatus(s.status.State, processing, strings.TrimSpace(s.appReservedTurnID) != "") == appwire.ThreadStatusClosed
			s.mu.RUnlock()
			if closed {
				return appwire.TurnQueueResponse{}, appwire.Conflict("session is closed")
			}
			if !processing && strings.TrimSpace(reservedTurnID) == "" {
				return appwire.TurnQueueResponse{}, appwire.Conflict("no active turn to queue against")
			}
			return queue(params)
		}
	}
	if drain := functions.Drain; drain != nil {
		functions.Drain = func(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			text, images := inputFromItems("", params.Input)
			hasInput := strings.TrimSpace(text) != "" || len(images) > 0
			s.mu.RLock()
			processing := s.processing
			closed := appStatus(s.status.State, processing, strings.TrimSpace(s.appReservedTurnID) != "") == appwire.ThreadStatusClosed
			s.mu.RUnlock()
			if closed {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("session is closed")
			}
			if !processing {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("no active turn to steer")
			}
			if !hasInput && s.materializedQueueDepth() == 0 {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("queue is empty")
			}
			return drain(params)
		}
	}
	if cancel != nil {
		functions.Interrupt = func(_ context.Context, _ appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			// An interrupt names no turn, so the precondition is whether
			// anything is running -- the same signal the steer fake uses, since
			// this harness marks a started turn by reserving its id rather than
			// by flipping processing.
			s.mu.RLock()
			reservedTurnID := s.appReservedTurnID
			processing := s.processing
			s.mu.RUnlock()
			if !processing && strings.TrimSpace(reservedTurnID) == "" {
				return appwire.TurnInterruptResponse{}, appwire.Conflict("session is not processing")
			}
			cancel()
			return appwire.TurnInterruptResponse{}, nil
		}
	}
	s.SetRetrySafeTurnFunctions(functions)
}

// reservedTestTurns numbers the turn ids reserveAppTurnIDForStart mints.
var reservedTestTurns atomic.Uint64

// reserveAppTurnIDForStart is the projected-mutation fake's turn/start
// reservation: it refuses a closed or busy thread and marks the thread's turn
// reserved, which its status and capabilities report as active.
func (s *Server) reserveAppTurnIDForStart() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	processing := s.processing
	reserved := strings.TrimSpace(s.appReservedTurnID) != ""
	if appStatus(s.status.State, processing, reserved) == appwire.ThreadStatusClosed {
		return "", appwire.Conflict("session is closed")
	}
	if processing || reserved {
		return "", appwire.Conflict("session is processing")
	}
	turnID := fmt.Sprintf("turn_reserved_%d", reservedTestTurns.Add(1))
	s.appActiveTurnID = turnID
	s.appReservedTurnID = turnID
	return turnID, nil
}

// releaseAppTurnID undoes reserveAppTurnIDForStart's reservation of turnID.
func (s *Server) releaseAppTurnID(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appReservedTurnID == turnID {
		s.appReservedTurnID = ""
		s.appActiveTurnID = ""
	}
}
