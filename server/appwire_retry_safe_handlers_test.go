package server

// Characterization of the seven retry-safe turn handlers' shared contract:
// identity fencing, the thread/clear gate, trimmed mutation identity reaching
// the callback, error normalization and the unwired-hook answer. Each handler
// is a table row so a drift between them shows as one failing row.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

type retrySafeSeen struct {
	mu         sync.Mutex
	mutationID string
	instanceID string
	calls      int
}

func (r *retrySafeSeen) record(mutationID, instanceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutationID, r.instanceID = mutationID, instanceID
	r.calls++
}

type retrySafeHandlerCase struct {
	name string
	// wire installs only this handler's callback; it records the identity it
	// receives and answers cbErr.
	wire func(s *Server, seen *retrySafeSeen, cbErr error)
	// call invokes the handler with otherwise valid params.
	call func(s *Server, mutationID, instanceID string) error
}

var retrySafeTextInput = []appwire.InputItem{{Type: "text", Text: "hello"}}

func retrySafeHandlerCases() []retrySafeHandlerCase {
	const ref = "local:th_1"
	return []retrySafeHandlerCase{
		{"start",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: func(p appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnStartResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnStart(context.Background(), appwire.TurnStartParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i, Input: retrySafeTextInput})
				return err
			}},
		{"steer",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Steer: func(p appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnSteerResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i, Input: retrySafeTextInput})
				return err
			}},
		{"interrupt",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Interrupt: func(_ context.Context, p appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnInterruptResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnInterrupt(context.Background(), appwire.TurnInterruptParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i})
				return err
			}},
		{"queue",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Queue: func(p appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnQueueResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i, Input: retrySafeTextInput})
				return err
			}},
		{"drain",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Drain: func(p appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnDrainAsSteerResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnDrainAsSteer(context.Background(), appwire.TurnDrainAsSteerParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i})
				return err
			}},
		{"promote",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Promote: func(p appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnPromoteQueuedAsSteerResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnPromoteQueuedAsSteer(context.Background(), appwire.TurnPromoteQueuedAsSteerParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i, ExpectedEntryID: "q_1"})
				return err
			}},
		{"cancel",
			func(s *Server, seen *retrySafeSeen, cbErr error) {
				s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Cancel: func(p appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
					seen.record(p.ClientMutationID, p.ExpectedInstanceID)
					return appwire.TurnCancelQueuedResponse{}, cbErr
				}})
			},
			func(s *Server, m, i string) error {
				_, err := s.handleAppTurnCancelQueued(context.Background(), appwire.TurnCancelQueuedParams{Ref: ref, ClientMutationID: m, ExpectedInstanceID: i, ExpectedEntryID: "q_1"})
				return err
			}},
	}
}

func newRetrySafeTestServer() *Server {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")
	return srv
}

func wireErrorOf(t *testing.T, err error) appwire.WireError {
	t.Helper()
	werr, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("error %v is not a WireError", err)
	}
	return werr
}

func errorDataOf(t *testing.T, err error) appwire.ErrorData {
	t.Helper()
	data, ok := wireErrorOf(t, err).Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("error %v carries no ErrorData", err)
	}
	return data
}

func TestRetrySafeHandlersDeliverTrimmedIdentity(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			var seen retrySafeSeen
			tc.wire(srv, &seen, nil)
			if err := tc.call(srv, "  m1  ", "  th_1  "); err != nil {
				t.Fatalf("call: %v", err)
			}
			if seen.calls != 1 || seen.mutationID != "m1" || seen.instanceID != "th_1" {
				t.Fatalf("callback saw %+v, want one call with trimmed identity", &seen)
			}
		})
	}
}

func TestRetrySafeHandlersRequireMutationIdentity(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		for _, bad := range []struct{ name, mutationID, instanceID string }{
			{"blank mutation id", "   ", "th_1"},
			{"blank instance id", "m1", "   "},
		} {
			t.Run(tc.name+"/"+bad.name, func(t *testing.T) {
				srv := newRetrySafeTestServer()
				var seen retrySafeSeen
				tc.wire(srv, &seen, nil)
				err := tc.call(srv, bad.mutationID, bad.instanceID)
				if got := wireErrorOf(t, err).Code; got != appwire.CodeInvalidParams {
					t.Fatalf("code=%v, want InvalidParams", got)
				}
				if seen.calls != 0 {
					t.Fatal("callback ran for a request without a mutation identity")
				}
			})
		}
	}
}

func TestRetrySafeHandlersFenceStaleInstance(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			var seen retrySafeSeen
			tc.wire(srv, &seen, nil)
			data := errorDataOf(t, tc.call(srv, "m1", "th_old"))
			if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "m1" {
				t.Fatalf("data=%+v, want a not-accepted answer carrying the mutation id", data)
			}
			if seen.calls != 0 {
				t.Fatal("callback ran against a stale instance")
			}
		})
	}
}

func TestRetrySafeHandlersRefuseWhileThreadClearHoldsTheGate(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			var seen retrySafeSeen
			tc.wire(srv, &seen, nil)
			srv.appMutationGate.Lock()
			err := tc.call(srv, "m1", "th_1")
			srv.appMutationGate.Unlock()
			data := errorDataOf(t, err)
			if data.MutationOutcome != appwire.MutationOutcomeNotAccepted || data.ClientMutationID != "m1" {
				t.Fatalf("data=%+v, want a not-accepted answer carrying the mutation id", data)
			}
			if seen.calls != 0 {
				t.Fatal("callback ran while thread clear held the gate")
			}
		})
	}
}

func TestRetrySafeHandlersNormalizeCallbackFailure(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			var seen retrySafeSeen
			tc.wire(srv, &seen, errors.New("store unavailable"))
			data := errorDataOf(t, tc.call(srv, " m1 ", "th_1"))
			if data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
				data.ClientMutationID != "m1" ||
				data.MutationOutcome != appwire.MutationOutcomeUnknown {
				t.Fatalf("data=%+v, want outcome-unknown carrying the trimmed mutation id", data)
			}
		})
	}
}

func TestRetrySafeHandlersPassThroughWireErrors(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			var seen retrySafeSeen
			tc.wire(srv, &seen, appwire.Conflict("queue moved"))
			if got := wireErrorOf(t, tc.call(srv, "m1", "th_1")).Code; got != appwire.CodeConflict {
				t.Fatalf("code=%v, want the callback's Conflict untouched", got)
			}
		})
	}
}

func TestRetrySafeHandlersAnswerUnavailableWhenUnwired(t *testing.T) {
	for _, tc := range retrySafeHandlerCases() {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			if got := wireErrorOf(t, tc.call(srv, "m1", "th_1")).Code; got != appwire.CodeUnavailable {
				t.Fatalf("code=%v, want Unavailable", got)
			}
		})
	}
}

// Input-carrying handlers reject a request with no content. Drain is the
// exception: its input is optional because its job is to pop queued entries.
func TestRetrySafeInputGate(t *testing.T) {
	ctx := context.Background()
	noContent := []appwire.InputItem{{Type: "text", Text: "   "}}
	rejects := map[string]func(*Server) error{
		"start": func(s *Server) error {
			_, err := s.handleAppTurnStart(ctx, appwire.TurnStartParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1", Input: noContent})
			return err
		},
		"steer": func(s *Server) error {
			_, err := s.handleAppTurnSteer(ctx, appwire.TurnSteerParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1", Input: noContent})
			return err
		},
		"queue": func(s *Server) error {
			_, err := s.handleAppTurnQueue(ctx, appwire.TurnQueueParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1", Input: noContent})
			return err
		},
	}
	wired := RetrySafeTurnFunctions{
		Start: func(appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
			return appwire.TurnStartResponse{}, nil
		},
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			return appwire.TurnQueueResponse{}, nil
		},
		Drain: func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			return appwire.TurnDrainAsSteerResponse{}, nil
		},
	}
	for name, call := range rejects {
		t.Run(name, func(t *testing.T) {
			srv := newRetrySafeTestServer()
			srv.SetRetrySafeTurnFunctions(wired)
			if got := wireErrorOf(t, call(srv)).Code; got != appwire.CodeInvalidParams {
				t.Fatalf("code=%v, want InvalidParams", got)
			}
		})
	}
	t.Run("drain accepts no input", func(t *testing.T) {
		srv := newRetrySafeTestServer()
		srv.SetRetrySafeTurnFunctions(wired)
		if _, err := srv.handleAppTurnDrainAsSteer(ctx, appwire.TurnDrainAsSteerParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1"}); err != nil {
			t.Fatalf("drain without input: %v", err)
		}
	})
}

// The index-addressed handlers validate their static shape before dispatch.
func TestRetrySafeQueueEntryShapeGate(t *testing.T) {
	ctx := context.Background()
	srv := newRetrySafeTestServer()
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
		Cancel: func(appwire.TurnCancelQueuedParams) (appwire.TurnCancelQueuedResponse, error) {
			return appwire.TurnCancelQueuedResponse{}, nil
		},
	})
	for _, bad := range []struct {
		name  string
		index int
		entry string
	}{{"negative index", -1, "q_1"}, {"blank entry id", 0, "  "}} {
		t.Run("promote/"+bad.name, func(t *testing.T) {
			_, err := srv.handleAppTurnPromoteQueuedAsSteer(ctx, appwire.TurnPromoteQueuedAsSteerParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1", Index: bad.index, ExpectedEntryID: bad.entry})
			if got := wireErrorOf(t, err).Code; got != appwire.CodeInvalidParams {
				t.Fatalf("code=%v, want InvalidParams", got)
			}
		})
		t.Run("cancel/"+bad.name, func(t *testing.T) {
			_, err := srv.handleAppTurnCancelQueued(ctx, appwire.TurnCancelQueuedParams{Ref: "local:th_1", ClientMutationID: "m1", ExpectedInstanceID: "th_1", Index: bad.index, ExpectedEntryID: bad.entry})
			if got := wireErrorOf(t, err).Code; got != appwire.CodeInvalidParams {
				t.Fatalf("code=%v, want InvalidParams", got)
			}
		})
	}
}
