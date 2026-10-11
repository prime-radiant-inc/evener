package server

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestCommandInputWiringAndCapability(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_318")
	input := []appwire.InputItem{{Type: "command", Name: "probe"}, {Type: "skill", Name: "probe"}}
	var received []appwire.InputItem
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			received = params.Input
			return appwire.TurnQueueResponse{}, nil
		},
	})
	if srv.appCapabilities("idle", false).CommandInput {
		t.Fatal("partial wiring advertises command consumption on all routes")
	}
	if _, err := srv.handleAppTurnQueue(context.Background(), appwire.TurnQueueParams{Ref: "local:th_318", ClientMutationID: "queue-318", ExpectedInstanceID: "th_318", Input: input}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, input) {
		t.Fatalf("command/skill identities changed at seam: %+v", received)
	}
	_, err := srv.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{Ref: "local:th_318", ClientMutationID: "steer-318", ExpectedInstanceID: "th_318", Input: input[:1]})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("unwired command seam accepted selection: %v", err)
	}
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
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
	})
	if !srv.appCapabilities("idle", false).CommandInput {
		t.Fatal("fully wired runtime does not advertise command input")
	}
}
