package server

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestServerAppWireControlNeedsNoTurnID walks the whole daemon request path —
// the flag-day validator on the router, then each handler's own preconditions —
// for every control mutation. Control is session-scoped (Jesse, 2026-08-16): it
// applies to whatever the session is running, so naming a turn is not something
// a client has to do, and refusing one that cannot is how Stop and Steer used to
// fail in the windows they mattered most.
//
// The preconditions that name a real object stay required and are asserted here
// too, because dropping the turn id must not drop them: drainAsSteer still needs
// the queue revision it is compare-and-swapping, and promoteQueuedAsSteer still
// needs the entry it is promoting.
func TestServerAppWireControlNeedsNoTurnID(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "th_1")

	var steers, queues, drains, promotes int
	var interrupts []appwire.TurnInterruptParams
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Steer: func(appwire.TurnSteerParams) (appwire.TurnSteerResponse, error) {
			steers++
			return appwire.TurnSteerResponse{}, nil
		},
		Queue: func(appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			queues++
			return appwire.TurnQueueResponse{}, nil
		},
		Drain: func(appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
			drains++
			return appwire.TurnDrainAsSteerResponse{}, nil
		},
		Promote: func(appwire.TurnPromoteQueuedAsSteerParams) (appwire.TurnPromoteQueuedAsSteerResponse, error) {
			promotes++
			return appwire.TurnPromoteQueuedAsSteerResponse{}, nil
		},
		Interrupt: func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
			interrupts = append(interrupts, params)
			return appwire.TurnInterruptResponse{}, nil
		},
	})

	conn := srv.AppServer().NewConnection("test")
	conn.HandleMessage(context.Background(), appwire.RequestMessage(
		appwire.NewIntID(1), appwire.MethodInitialize,
		appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion},
	))

	input := []appwire.InputItem{{Type: "text", Text: "apply this now"}}
	revision := uint64(7)
	for _, tc := range []struct {
		name   string
		id     int64
		method string
		params any
		landed func() int
	}{
		{"steer", 2, appwire.MethodTurnSteer, appwire.TurnSteerParams{
			ClientMutationID: "cm-steer", Ref: "local:th_1", Input: input,
		}, func() int { return steers }},
		{"queue", 3, appwire.MethodTurnQueue, appwire.TurnQueueParams{
			ClientMutationID: "cm-queue", Ref: "local:th_1", Input: input,
		}, func() int { return queues }},
		{"interrupt", 4, appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
			ClientMutationID: "cm-interrupt", Ref: "local:th_1",
		}, func() int { return len(interrupts) }},
		{"drainAsSteer", 5, appwire.MethodTurnDrainAsSteer, appwire.TurnDrainAsSteerParams{
			ClientMutationID: "cm-drain", Ref: "local:th_1", Input: input,
			ExpectedQueueRevision: revision,
		}, func() int { return drains }},
		{"promoteQueuedAsSteer", 6, appwire.MethodTurnPromoteQueuedAsSteer, appwire.TurnPromoteQueuedAsSteerParams{
			ClientMutationID: "cm-promote", Ref: "local:th_1", ExpectedEntryID: "entry_1",
		}, func() int { return promotes }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := conn.HandleMessage(context.Background(), appwire.RequestMessage(
				appwire.NewIntID(tc.id), tc.method, tc.params,
			))
			if response.Kind() != appwire.MessageResponse {
				t.Fatalf("%s with no expectedTurnId = %+v, want a response", tc.method, response.Error.Error)
			}
			if tc.landed() != 1 {
				t.Fatalf("%s with no expectedTurnId reached the session %d times, want 1", tc.method, tc.landed())
			}
		})
	}

	// The router's flag-day validator is skipped for adapter-native servers
	// (internal/appserver/server.go), so each handler's own preconditions are
	// the only ones on that path and have to be checked without it.
	//
	// drainAsSteer's revision is not checked here: it is a uint64, so an absent
	// one and a deliberate zero are the same value to a handler, and only the
	// router's validator (which sees the raw JSON) can tell them apart --
	// TestControlMutationsRequireNoTurnID covers that half.
	t.Run("handlers refuse the preconditions that still name a real object", func(t *testing.T) {
		if _, err := srv.handleAppTurnPromoteQueuedAsSteer(context.Background(), appwire.TurnPromoteQueuedAsSteerParams{
			ClientMutationID: "cm-promote-direct", Ref: "local:th_1",
		}); err == nil {
			t.Fatal("promoteQueuedAsSteer with no expectedEntryId was accepted; the entry is what it promotes")
		}
	})
}
