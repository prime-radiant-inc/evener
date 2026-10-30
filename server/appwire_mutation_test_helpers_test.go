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
// registrations.
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
			turnID, err := s.startTestTurn()
			if err != nil {
				return appwire.TurnStartResponse{}, err
			}
			select {
			case s.inputCh <- InputMessage{Text: text, Images: images}:
				return appwire.TurnStartResponse{Turn: appwire.Turn{ID: turnID, Status: appwire.TurnStatusInProgress}}, nil
			default:
				s.releaseTestTurn(turnID)
				return appwire.TurnStartResponse{}, appwire.Conflict("input buffer full")
			}
		}
	}
	if steer := functions.Steer; steer != nil {
		functions.Steer = func(params appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			s.mu.RLock()
			processing := s.processing
			s.mu.RUnlock()
			if !processing {
				return appwire.TurnSteerResponse{}, appwire.Conflict("turn is not active")
			}
			return steer(params)
		}
	}
	if queue := functions.Queue; queue != nil {
		functions.Queue = func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			s.mu.RLock()
			processing := s.processing
			closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
			s.mu.RUnlock()
			if closed {
				return appwire.TurnQueueResponse{}, appwire.Conflict("session is closed")
			}
			if !processing {
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
			closed := appStatus(s.status.State, processing) == appwire.ThreadStatusClosed
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
			// anything is running.
			s.mu.RLock()
			processing := s.processing
			s.mu.RUnlock()
			if !processing {
				return appwire.TurnInterruptResponse{}, appwire.Conflict("session is not processing")
			}
			cancel()
			return appwire.TurnInterruptResponse{}, nil
		}
	}
	s.SetRetrySafeTurnFunctions(functions)
}

// startedTestTurns numbers the turn ids startTestTurn mints.
var startedTestTurns atomic.Uint64

// startTestTurn is the projected-mutation fake's turn/start: it refuses a
// closed or busy thread and marks a turn running under a fresh id.
func (s *Server) startTestTurn() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if appStatus(s.status.State, s.processing) == appwire.ThreadStatusClosed {
		return "", appwire.Conflict("session is closed")
	}
	if s.processing {
		return "", appwire.Conflict("session is processing")
	}
	turnID := fmt.Sprintf("turn_started_%d", startedTestTurns.Add(1))
	s.processing = true
	s.appActiveTurnID = turnID
	return turnID, nil
}

// releaseTestTurn undoes startTestTurn when its input could not be delivered.
func (s *Server) releaseTestTurn(turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appActiveTurnID == turnID {
		s.endProcessingLocked()
	}
}

func inputFromItems(prompt string, items []appwire.InputItem) (string, []ImageAttachment) {
	parts := []string{}
	if strings.TrimSpace(prompt) != "" {
		parts = append(parts, prompt)
	}
	images := make([]ImageAttachment, 0)
	for _, item := range items {
		switch item.Type {
		case "text", "input_text", "":
			if strings.TrimSpace(item.Text) != "" {
				parts = append(parts, item.Text)
			}
		case "image", "input_image":
			images = append(images, ImageAttachment{
				MediaType: item.MediaType,
				Data:      item.Data,
				Name:      item.Name,
			})
		}
	}
	return strings.Join(parts, "\n"), images
}
