package server

import (
	"context"
	"strings"

	"primeradiant.com/serf/appwire"
)

// installProjectedMutationCallbacksForTest keeps older projector-focused tests
// explicit about their fake mutation authority. Production AppWire handlers do
// not consult these server projections; the callback bundle below is the test
// double for the Session-owned compare-and-commit layer.
func installProjectedMutationCallbacksForTest(s *Server) {
	s.mu.RLock()
	steer := s.steerFunc
	steerImages := s.steerWithImagesFunc
	queue := s.queueFunc
	queueImages := s.queueWithImagesFunc
	drain := s.drainSteerFunc
	drainInput := s.drainSteerInputFunc
	cancel := s.cancelFunc
	s.mu.RUnlock()

	functions := RetrySafeTurnFunctions{
		Start: func(params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
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
		},
	}
	if steer != nil || steerImages != nil {
		functions.Steer = func(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			text, images := inputFromItems("", params.Input)
			s.mu.RLock()
			activeTurnID := s.appActiveTurnID
			reservedTurnID := s.appReservedTurnID
			processing := s.processing
			s.mu.RUnlock()
			if !processing && strings.TrimSpace(reservedTurnID) == "" || params.ExpectedTurnID != activeTurnID {
				return appwire.TurnSteerResponse{}, appwire.Conflict("turn is not active")
			}
			if len(images) > 0 && steerImages == nil {
				return appwire.TurnSteerResponse{}, appwire.Unavailable("steer with images not available")
			}
			if steerImages != nil {
				steerImages(text, images)
			} else {
				steer(text)
			}
			return appwire.TurnSteerResponse{}, nil
		}
	}
	if queue != nil || queueImages != nil {
		functions.Queue = func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			text, images := inputFromItems("", params.Input)
			s.mu.RLock()
			processing := s.processing
			reservedTurnID := s.appReservedTurnID
			closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
			s.mu.RUnlock()
			if closed {
				return appwire.TurnQueueResponse{}, appwire.Conflict("session is closed")
			}
			if !processing && strings.TrimSpace(reservedTurnID) == "" {
				return appwire.TurnQueueResponse{}, appwire.Conflict("no active turn to queue against")
			}
			if len(images) > 0 {
				if queueImages == nil {
					return appwire.TurnQueueResponse{}, appwire.Unavailable("image queue not available")
				}
				err := queueImages(text, images)
				// Stand in for the bridge: a real session emits QUEUE_CHANGED
				// here and the daemon re-samples the queue facet. A double that
				// mutated the queue without republishing would leave the
				// materialized depth at whatever it was before.
				s.RefreshThreadEnvelope()
				return appwire.TurnQueueResponse{}, err
			}
			if queue == nil {
				return appwire.TurnQueueResponse{}, appwire.Unavailable("queue not available")
			}
			return appwire.TurnQueueResponse{}, queue(text)
		}
	}
	if drain != nil {
		functions.Drain = func(params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			text, images := inputFromItems("", params.Input)
			hasInput := strings.TrimSpace(text) != "" || len(images) > 0
			s.mu.RLock()
			processing := s.processing
			closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
			s.mu.RUnlock()
			if closed {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("session is closed")
			}
			if !processing {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("no active turn to steer")
			}
			if hasInput {
				if drainInput == nil {
					return appwire.TurnDrainAsSteerResponse{}, appwire.Unavailable("drain-as-steer with input not available")
				}
				return appwire.TurnDrainAsSteerResponse{}, drainInput(text, images)
			}
			if s.materializedQueueDepth() == 0 {
				return appwire.TurnDrainAsSteerResponse{}, appwire.Conflict("queue is empty")
			}
			return appwire.TurnDrainAsSteerResponse{}, drain()
		}
	}
	if cancel != nil {
		functions.Interrupt = func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			s.mu.RLock()
			activeTurnID := s.appActiveTurnID
			s.mu.RUnlock()
			if params.ExpectedTurnID != activeTurnID {
				return appwire.TurnInterruptResponse{}, appwire.Conflict("turn is not active")
			}
			cancel()
			return appwire.TurnInterruptResponse{}, nil
		}
	}
	s.SetRetrySafeTurnFunctions(functions)
}
