package server

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

func TestAppWireMutationResponseLossRetriesOnce(t *testing.T) {
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&blockingServerAdapter{
		name:    "openai",
		started: make(chan struct{}),
		done:    make(chan error, 1),
	})
	sess, err := agent.NewSession(
		client,
		provider.NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		agent.SessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	start, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "start-active-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "keep the turn active"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	params := appwire.TurnQueueParams{
		Ref:              "local:" + sess.ID(),
		ClientMutationID: "queue-after-response-loss",
		ExpectedTurnID:   start.Turn.ID,
		Input:            []appwire.InputItem{{Type: "text", Text: "queued exactly once"}},
	}

	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: sess.AcceptClientMutationQueue,
	})

	firstConn := srv.AppServer().NewConnection("first")
	initializeMutationRecoveryConnection(t, firstConn)
	_ = firstConn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, params),
	) // The response is lost after the daemon handles the request.

	retryConn := srv.AppServer().NewConnection("retry")
	initializeMutationRecoveryConnection(t, retryConn)
	retry := retryConn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, params),
	)
	if retry.Kind() != appwire.MessageResponse {
		t.Fatalf("retry response kind = %v, error = %#v", retry.Kind(), retry.Error)
	}
	response, ok := retry.Response.Result.(appwire.TurnQueueResponse)
	if !ok {
		t.Fatalf("retry response result = %T, want appwire.TurnQueueResponse", retry.Response.Result)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("retry disposition = %q, want %q", response.Receipt.Disposition, appwire.MutationDispositionReplayed)
	}
	if got := sess.QueueTexts(); len(got) != 1 || got[0] != "queued exactly once" {
		t.Fatalf("queue texts after response loss retry = %#v, want one queued effect", got)
	}
}

func TestAppWireMutationReplayTable(t *testing.T) {
	tests := []struct {
		name   string
		method string
		setup  func(*testing.T, *agent.Session, *int) any
		assert func(*testing.T, *agent.Session, *int, appwire.Message)
	}{
		{
			name:   "start",
			method: appwire.MethodTurnStart,
			setup: func(_ *testing.T, sess *agent.Session, _ *int) any {
				return appwire.TurnStartParams{
					ClientMutationID: "replay-start",
					Ref:              "local:" + sess.ID(),
					Input:            []appwire.InputItem{{Type: "text", Text: "start once"}},
				}
			},
			assert: func(t *testing.T, _ *agent.Session, _ *int, response appwire.Message) {
				result := response.Response.Result.(appwire.TurnStartResponse)
				if result.Turn.ID == "" {
					t.Fatal("replayed start returned no stable turn")
				}
			},
		},
		{
			name:   "steer",
			method: appwire.MethodTurnSteer,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				turnID := acceptMutationReplayActiveTurn(t, sess)
				return appwire.TurnSteerParams{
					ClientMutationID: "replay-steer",
					Ref:              "local:" + sess.ID(),
					ExpectedTurnID:   turnID,
					Input:            []appwire.InputItem{{Type: "text", Text: "steer once"}},
				}
			},
			assert: func(t *testing.T, sess *agent.Session, _ *int, _ appwire.Message) {
				steering := sess.SteeringQueueSnapshot()
				if len(steering) != 1 || steering[0].Text != "steer once" {
					t.Fatalf("steering effects = %+v, want one", steering)
				}
			},
		},
		{
			name:   "queue",
			method: appwire.MethodTurnQueue,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				turnID := acceptMutationReplayActiveTurn(t, sess)
				return appwire.TurnQueueParams{
					ClientMutationID: "replay-queue",
					Ref:              "local:" + sess.ID(),
					ExpectedTurnID:   turnID,
					Input:            []appwire.InputItem{{Type: "text", Text: "queue once"}},
				}
			},
			assert: func(t *testing.T, sess *agent.Session, _ *int, _ appwire.Message) {
				if texts := sess.QueueTexts(); len(texts) != 1 || texts[0] != "queue once" {
					t.Fatalf("queue effects = %#v, want one", texts)
				}
			},
		},
		{
			name:   "drain",
			method: appwire.MethodTurnDrainAsSteer,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				turnID := acceptMutationReplayActiveTurn(t, sess)
				if err := sess.Enqueue(context.Background(), "drain once"); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
				return appwire.TurnDrainAsSteerParams{
					ClientMutationID:      "replay-drain",
					Ref:                   "local:" + sess.ID(),
					ExpectedTurnID:        turnID,
					ExpectedQueueRevision: 1,
				}
			},
			assert: func(t *testing.T, sess *agent.Session, _ *int, _ appwire.Message) {
				steering := sess.SteeringQueueSnapshot()
				if len(sess.QueueTexts()) != 0 || len(steering) != 1 || steering[0].Text != "drain once" {
					t.Fatalf("drain effects: queue=%#v steering=%+v", sess.QueueTexts(), steering)
				}
			},
		},
		{
			name:   "promote",
			method: appwire.MethodTurnPromoteQueuedAsSteer,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				turnID := acceptMutationReplayActiveTurn(t, sess)
				if err := sess.Enqueue(context.Background(), "promote once"); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
				return appwire.TurnPromoteQueuedAsSteerParams{
					ClientMutationID: "replay-promote",
					Ref:              "local:" + sess.ID(),
					ExpectedTurnID:   turnID,
					Index:            0,
					ExpectedEntryID:  sess.QueueIDs()[0],
				}
			},
			assert: func(t *testing.T, sess *agent.Session, _ *int, _ appwire.Message) {
				steering := sess.SteeringQueueSnapshot()
				if len(sess.QueueTexts()) != 0 || len(steering) != 1 || steering[0].Text != "promote once" {
					t.Fatalf("promote effects: queue=%#v steering=%+v", sess.QueueTexts(), steering)
				}
			},
		},
		{
			name:   "cancel",
			method: appwire.MethodTurnCancelQueued,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				if err := sess.Enqueue(context.Background(), "cancel once"); err != nil {
					t.Fatalf("Enqueue: %v", err)
				}
				return appwire.TurnCancelQueuedParams{
					ClientMutationID: "replay-cancel",
					Ref:              "local:" + sess.ID(),
					Index:            0,
					ExpectedEntryID:  sess.QueueIDs()[0],
				}
			},
			assert: func(t *testing.T, sess *agent.Session, _ *int, response appwire.Message) {
				result := response.Response.Result.(appwire.TurnCancelQueuedResponse)
				if result.RemovedText != "cancel once" || len(sess.QueueTexts()) != 0 {
					t.Fatalf("cancel result=%+v queue=%#v", result, sess.QueueTexts())
				}
			},
		},
		{
			name:   "interrupt",
			method: appwire.MethodTurnInterrupt,
			setup: func(t *testing.T, sess *agent.Session, _ *int) any {
				return appwire.TurnInterruptParams{
					ClientMutationID: "replay-interrupt",
					Ref:              "local:" + sess.ID(),
					ExpectedTurnID:   acceptMutationReplayActiveTurn(t, sess),
				}
			},
			assert: func(t *testing.T, _ *agent.Session, interrupts *int, _ appwire.Message) {
				if *interrupts != 1 {
					t.Fatalf("interrupt callback count = %d, want 1", *interrupts)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := newMutationReplaySession(t)
			interrupts := 0
			params := tt.setup(t, sess, &interrupts)
			srv := NewServer(ServerConfig{})
			srv.SetAppIdentity("local", sess.ID())
			srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
				Start:   sess.AcceptClientMutationStart,
				Steer:   sess.AcceptClientMutationSteer,
				Queue:   sess.AcceptClientMutationQueue,
				Drain:   sess.AcceptClientMutationDrainAsSteer,
				Promote: sess.AcceptClientMutationPromoteQueuedAsSteer,
				Cancel:  sess.AcceptClientMutationCancelQueued,
				Interrupt: func(ctx context.Context, params appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
					return sess.InterruptClientMutation(ctx, params, func() { interrupts++ })
				},
			})

			first := srv.AppServer().NewConnection("first")
			initializeMutationRecoveryConnection(t, first)
			applied := first.HandleMessage(
				context.Background(),
				appwire.RequestMessage(appwire.NewIntID(2), tt.method, params),
			)
			if applied.Kind() != appwire.MessageResponse {
				t.Fatalf("first response kind = %v, error = %#v", applied.Kind(), applied.Error)
			}

			retryConnection := srv.AppServer().NewConnection("retry")
			initializeMutationRecoveryConnection(t, retryConnection)
			replayed := retryConnection.HandleMessage(
				context.Background(),
				appwire.RequestMessage(appwire.NewIntID(2), tt.method, params),
			)
			if replayed.Kind() != appwire.MessageResponse {
				t.Fatalf("retry response kind = %v, error = %#v", replayed.Kind(), replayed.Error)
			}
			if receipt := mutationReplayReceipt(t, replayed.Response.Result); receipt.Disposition != appwire.MutationDispositionReplayed {
				t.Fatalf("retry receipt = %+v, want replayed", receipt)
			}
			tt.assert(t, sess, &interrupts, replayed)
		})
	}
}

func TestAppWireMutationPayloadMismatchIsInvalidRequest(t *testing.T) {
	sess := newMutationReplaySession(t)
	turnID := acceptMutationReplayActiveTurn(t, sess)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: sess.AcceptClientMutationQueue,
	})
	conn := srv.AppServer().NewConnection("mismatch")
	initializeMutationRecoveryConnection(t, conn)
	params := appwire.TurnQueueParams{
		ClientMutationID: "reused-mutation-id",
		Ref:              "local:" + sess.ID(),
		ExpectedTurnID:   turnID,
		Input:            []appwire.InputItem{{Type: "text", Text: "first payload"}},
	}
	first := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, params),
	)
	if first.Kind() != appwire.MessageResponse {
		t.Fatalf("first response kind = %v, error = %#v", first.Kind(), first.Error)
	}
	params.Input[0].Text = "different payload"
	mismatch := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnQueue, params),
	)
	if mismatch.Kind() != appwire.MessageError || mismatch.Error.Error.Code != appwire.CodeInvalidRequest {
		t.Fatalf("mismatch response = %#v, want InvalidRequest", mismatch)
	}
	if texts := sess.QueueTexts(); len(texts) != 1 || texts[0] != "first payload" {
		t.Fatalf("queue effects after mismatch = %#v, want original only", texts)
	}
}

func TestAppWireMutationPersistenceFailureCanRecoverInProcess(t *testing.T) {
	sess := newMutationReplaySession(t)
	turnID := acceptMutationReplayActiveTurn(t, sess)
	attempts := 0
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: func(params appwire.TurnQueueParams) (appwire.TurnQueueResponse, error) {
			attempts++
			if attempts == 1 {
				return appwire.TurnQueueResponse{}, errors.New("journal write failed")
			}
			return sess.AcceptClientMutationQueue(params)
		},
	})
	conn := srv.AppServer().NewConnection("persistence-recovery")
	initializeMutationRecoveryConnection(t, conn)
	params := appwire.TurnQueueParams{
		ClientMutationID: "recover-after-persistence",
		Ref:              "local:" + sess.ID(),
		ExpectedTurnID:   turnID,
		Input:            []appwire.InputItem{{Type: "text", Text: "apply after recovery"}},
	}
	failed := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(2), appwire.MethodTurnQueue, params),
	)
	if failed.Kind() != appwire.MessageError {
		t.Fatalf("persistence response kind = %v, want error", failed.Kind())
	}
	data, ok := failed.Error.Error.Data.(appwire.ErrorData)
	if !ok ||
		data.SerfErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != params.ClientMutationID ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked ||
		data.Cause != "persistenceUnavailable" {
		t.Fatalf("persistence error = %#v", failed.Error.Error)
	}

	recovered := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(appwire.NewIntID(3), appwire.MethodTurnQueue, params),
	)
	if recovered.Kind() != appwire.MessageResponse {
		t.Fatalf("recovery response kind = %v, error = %#v", recovered.Kind(), recovered.Error)
	}
	if receipt := recovered.Response.Result.(appwire.TurnQueueResponse).Receipt; receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("recovery receipt = %+v, want applied", receipt)
	}
	if texts := sess.QueueTexts(); len(texts) != 1 || texts[0] != "apply after recovery" {
		t.Fatalf("recovery effects = %#v, want one", texts)
	}
}

func TestAppWireMutationTerminalRejectionReplays(t *testing.T) {
	sess := newMutationReplaySession(t)
	turnID := acceptMutationReplayActiveTurn(t, sess)
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", sess.ID())
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{
		Queue: sess.AcceptClientMutationQueue,
	})
	conn := srv.AppServer().NewConnection("terminal-rejection")
	initializeMutationRecoveryConnection(t, conn)
	params := appwire.TurnQueueParams{
		ClientMutationID: "terminal-rejection",
		Ref:              "local:" + sess.ID(),
		ExpectedTurnID:   turnID + "-stale",
		Input:            []appwire.InputItem{{Type: "text", Text: "must not queue"}},
	}
	for requestID := int64(2); requestID <= 3; requestID++ {
		rejected := conn.HandleMessage(
			context.Background(),
			appwire.RequestMessage(appwire.NewIntID(requestID), appwire.MethodTurnQueue, params),
		)
		if rejected.Kind() != appwire.MessageError || rejected.Error.Error.Code != appwire.CodeConflict {
			t.Fatalf("rejection %d = %#v, want Conflict", requestID, rejected)
		}
		data, ok := rejected.Error.Error.Data.(appwire.ErrorData)
		if !ok ||
			data.ClientMutationID != params.ClientMutationID ||
			data.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
			data.RetryDisposition != appwire.RetryDispositionNone {
			t.Fatalf("rejection %d data = %#v", requestID, rejected.Error.Error.Data)
		}
	}
	if texts := sess.QueueTexts(); len(texts) != 0 {
		t.Fatalf("terminal rejection produced effects: %#v", texts)
	}
}

func newMutationReplaySession(t *testing.T) *agent.Session {
	t.Helper()
	stateDir := t.TempDir()
	client := llm.NewClient()
	client.Register(&blockingServerAdapter{
		name:    "openai",
		started: make(chan struct{}),
		done:    make(chan error, 1),
	})
	sess, err := agent.NewSession(
		client,
		provider.NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(stateDir),
		agent.SessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	sess.SetClientMutationStartWakeFunc(func() {})
	t.Cleanup(func() { sess.Close() })
	return sess
}

func acceptMutationReplayActiveTurn(t *testing.T, sess *agent.Session) string {
	t.Helper()
	response, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "active-" + t.Name(),
		Input:            []appwire.InputItem{{Type: "text", Text: "active turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	return response.Turn.ID
}

func mutationReplayReceipt(t *testing.T, result any) appwire.MutationReceipt {
	t.Helper()
	switch response := result.(type) {
	case appwire.TurnStartResponse:
		return response.Receipt
	case appwire.TurnSteerResponse:
		return response.Receipt
	case appwire.TurnQueueResponse:
		return response.Receipt
	case appwire.TurnDrainAsSteerResponse:
		return response.Receipt
	case appwire.TurnPromoteQueuedAsSteerResponse:
		return response.Receipt
	case appwire.TurnCancelQueuedResponse:
		return response.Receipt
	case appwire.TurnInterruptResponse:
		return response.Receipt
	default:
		t.Fatalf("unexpected mutation response type %T", result)
		return appwire.MutationReceipt{}
	}
}

type mutationRecoveryConnection interface {
	HandleMessage(context.Context, appwire.Message) appwire.Message
}

func initializeMutationRecoveryConnection(t *testing.T, conn mutationRecoveryConnection) {
	t.Helper()
	response := conn.HandleMessage(
		context.Background(),
		appwire.RequestMessage(
			appwire.NewIntID(1),
			appwire.MethodInitialize,
			appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion},
		),
	)
	if response.Kind() != appwire.MessageResponse {
		t.Fatalf("initialize response kind = %v, error = %#v", response.Kind(), response.Error)
	}
}
