package main

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

// kata yjsc: appwire.ValidateMutationParams gates all seven retry-safe turn
// mutations on identity + preconditions. The TUI sent none of them, so every
// send, queue, steer and interrupt was rejected by its own daemon.
//
// The guard only checks that the fields are PRESENT and non-blank, so these
// assert the values are meaningful: a fresh id per user action (the journal
// keys replay on it, so two actions sharing one id would collapse into one),
// and preconditions that actually reflect the session being acted on.

func TestSendHubInputSendsFreshClientMutationID(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var seen []string
	appserver.HandleTyped(app.Router(), appwire.MethodTurnStart, func(_ context.Context, params appwire.TurnStartParams) (appwire.TurnStartResponse, error) {
		seen = append(seen, params.ClientMutationID)
		return appwire.TurnStartResponse{Turn: appwire.Turn{ID: "turn_1"}}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	ref := appwire.Ref{SourceID: "local", ThreadID: "th_1"}
	for _, text := range []string{"first", "second"} {
		msg := sendHubInput(client, ref, text, text, nil)()
		if sendMsg, ok := msg.(hubSendMsg); !ok || sendMsg.err != nil {
			t.Fatalf("send %q: msg=%T err=%v", text, msg, sendMsg.err)
		}
	}

	if len(seen) != 2 {
		t.Fatalf("turn/start calls = %d, want 2", len(seen))
	}
	for i, id := range seen {
		if id == "" {
			t.Errorf("call %d: ClientMutationID is empty", i)
		}
	}
	if seen[0] == seen[1] {
		t.Errorf("both sends used ClientMutationID %q; each user action needs its own (the journal replays on it)", seen[0])
	}
}

func TestSendHubActionInterruptSendsIdentityAndExpectedTurn(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnInterruptParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnInterrupt, func(_ context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
		got = params
		return appwire.TurnInterruptResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubAction(client, appwire.Ref{SourceID: "local", ThreadID: "th_1"}, "interrupt", "turn_7")()
	if actionMsg, ok := msg.(hubActionMsg); !ok || actionMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, actionMsg.err)
	}
	if got.ClientMutationID == "" {
		t.Error("ClientMutationID is empty")
	}
	if got.ExpectedTurnID != "turn_7" {
		t.Errorf("ExpectedTurnID = %q, want turn_7", got.ExpectedTurnID)
	}
}

func TestSendHubQueueSendsIdentityAndExpectedTurn(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnQueueParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, params appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		got = params
		return appwire.EmptyResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubQueue(client, appwire.Ref{SourceID: "local", ThreadID: "th_q"}, "queue me", "queue me", nil, "turn_3")()
	if queueMsg, ok := msg.(hubQueueMsg); !ok || queueMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, queueMsg.err)
	}
	if got.ClientMutationID == "" {
		t.Error("ClientMutationID is empty")
	}
	if got.ExpectedTurnID != "turn_3" {
		t.Errorf("ExpectedTurnID = %q, want turn_3", got.ExpectedTurnID)
	}
}

// drainAsSteer additionally carries expectedQueueRevision, the CAS token that
// makes the drain reject rather than swallow a queue that changed underneath it.
// A hardcoded zero would satisfy the guard and defeat the check, so this pins
// the session's real revision.
func TestSendHubDrainAsSteerSendsIdentityTurnAndQueueRevision(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	var got appwire.TurnDrainAsSteerParams
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, params appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		got = params
		return appwire.TurnDrainAsSteerResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubDrainAsSteer(client, appwire.Ref{SourceID: "local", ThreadID: "th_d"}, "steer", "steer", nil, "turn_9", 42, 1)()
	if drainMsg, ok := msg.(hubDrainAsSteerMsg); !ok || drainMsg.err != nil {
		t.Fatalf("msg=%T err=%v", msg, drainMsg.err)
	}
	if got.ClientMutationID == "" {
		t.Error("ClientMutationID is empty")
	}
	if got.ExpectedTurnID != "turn_9" {
		t.Errorf("ExpectedTurnID = %q, want turn_9", got.ExpectedTurnID)
	}
	if got.ExpectedQueueRevision != 42 {
		t.Errorf("ExpectedQueueRevision = %d, want 42", got.ExpectedQueueRevision)
	}
}

// Found by running the TUI against a rate-limited daemon: after a turn failed,
// the composer was still in queue mode, so Enter sent turn/queue with an empty
// ExpectedTurnID and the daemon answered "expectedTurnId is required". The user
// saw a wire-level protocol error for an action the client could tell was
// impossible before sending.
//
// Same principle the web client already applies at its own submit
// (Spawn.tsx handleSpawn, kata xgk8): a mutation that CANNOT succeed must not be
// sent, whatever path reaches it.
func TestSendHubQueueRefusesWithoutAnActiveTurn(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	called := false
	appserver.HandleTyped(app.Router(), appwire.MethodTurnQueue, func(_ context.Context, _ appwire.TurnQueueParams) (appwire.EmptyResponse, error) {
		called = true
		return appwire.EmptyResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubQueue(client, appwire.Ref{SourceID: "local", ThreadID: "th_q"}, "queue me", "queue me", nil, "")
	queueMsg, ok := msg().(hubQueueMsg)
	if !ok {
		t.Fatalf("msg=%T", msg())
	}
	if called {
		t.Error("turn/queue was sent with no ExpectedTurnID; it cannot succeed")
	}
	if queueMsg.err == nil {
		t.Fatal("expected an error the composer can show")
	}
	if strings.Contains(queueMsg.err.Error(), "expectedTurnId is required") {
		t.Errorf("error is the raw wire rejection, not a client-side explanation: %v", queueMsg.err)
	}
	// The draft must survive so the user does not retype.
	if queueMsg.draft != "queue me" {
		t.Errorf("draft = %q, want it preserved", queueMsg.draft)
	}
}

func TestSendHubDrainAsSteerRefusesWithoutAnActiveTurn(t *testing.T) {
	app := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	called := false
	appserver.HandleTyped(app.Router(), appwire.MethodTurnDrainAsSteer, func(_ context.Context, _ appwire.TurnDrainAsSteerParams) (appwire.TurnDrainAsSteerResponse, error) {
		called = true
		return appwire.TurnDrainAsSteerResponse{}, nil
	})
	client, cleanup := newTUIAppWireClient(t, app)
	defer cleanup()

	msg := sendHubDrainAsSteer(client, appwire.Ref{SourceID: "local", ThreadID: "th_d"}, "steer", "steer", nil, "", 0)
	drainMsg, ok := msg().(hubDrainAsSteerMsg)
	if !ok {
		t.Fatalf("msg=%T", msg())
	}
	if called {
		t.Error("turn/drainAsSteer was sent with no ExpectedTurnID; it cannot succeed")
	}
	if drainMsg.err == nil {
		t.Fatal("expected an error the composer can show")
	}
}
