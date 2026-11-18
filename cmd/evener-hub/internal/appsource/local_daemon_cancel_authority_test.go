package appsource

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// withClientCallMapper used to replace every error its call returned with the
// child context's cancellation. That is wrong for a mutation: when Force Stop
// cancels a model-set call after thread/model/set was dispatched but before its
// reply arrives, the client has already classified the call as an
// outcome-unknown mutation carrying the client mutation ID and a blocked retry
// disposition. The hub's still-connected caller needs that verdict to reconcile
// the pick; a bare context.Canceled erases it. A cancellation may not erase
// what the mutation already did or may already have done.
func TestLocalDaemonSetThreadModelPreservesOutcomeUnknownAfterForceStop(t *testing.T) {
	entry := rendezvous.Entry{
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://daemon",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
		PID:       4242,
		StartedAt: time.Now(),
		StateDir:  t.TempDir(),
	}
	source := NewLocalDaemonSource("local", func() []rendezvous.Entry {
		return []rendezvous.Entry{entry}
	}, nil)
	dispatched := false
	var release func()
	var transport *scriptedAppwireTransport
	transport = newScriptedAppwireTransport(func(_ context.Context, msg appwire.Message) error {
		if msg.Request == nil {
			return nil
		}
		switch msg.Request.Method {
		case appwire.MethodInitialize:
			transport.recv <- appwire.ResponseMessage(msg.Request.ID, appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion})
		case appwire.MethodThreadModelSet:
			// thread/model/set is on the wire now, so from here the daemon may
			// have applied it. Force Stop cancels the in-flight call before its
			// reply arrives.
			dispatched = true
			release = source.BeginRecovery(entry)
		}
		return nil
	})
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	source.dial = dialTransport(transport)

	_, err := source.SetThreadModel(context.Background(), appwire.ThreadModelSetParams{
		Ref:              "local:th_1",
		ModelProvider:    "anthropic",
		Model:            "claude-opus-5",
		ClientMutationID: "pick-force-stopped",
	})
	if !dispatched {
		t.Fatal("thread/model/set was never dispatched")
	}
	wire, ok := errors.AsType[appwire.WireError](err)
	if !ok {
		t.Fatalf("error = %T=%v, want the classified outcome-unknown WireError, not the child context's cancellation", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok ||
		wire.Code != appwire.CodeInternalError ||
		data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "pick-force-stopped" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("wire = %+v data = %+v, want mutationOutcomeUnknown/unknown/blocked for pick-force-stopped", wire, wire.Data)
	}
}

// Preserving a verdict must not widen into "keep whatever fn returned": a call
// that provably never reached the wire has nothing to reconcile, and a plain
// transport failure only says the connection died. Both keep the child
// context's cancellation instead of becoming an in-doubt mutation the hub would
// treat as possibly applied. A delivered peer refusal, by contrast, is a
// verdict like the outcome-unknown and survives the cancellation.
func TestLocalDaemonMutationCancelKeepsUnsentAndTransportFailuresCanceled(t *testing.T) {
	for _, test := range []struct {
		name    string
		call    func(ctx context.Context, client *appwire.Client) error
		wantErr error
		want    appwire.WireError // zero when the cancellation should win
	}{
		{
			name: "provably unsent",
			call: func(ctx context.Context, client *appwire.Client) error {
				_, err := client.ThreadModelSet(ctx, appwire.ThreadModelSetParams{
					Ref:              "local:th_1",
					ModelProvider:    "anthropic",
					Model:            "claude-opus-5",
					ClientMutationID: "pick-unsent",
				})
				return err
			},
			wantErr: context.Canceled,
		},
		{
			name: "plain transport failure",
			call: func(context.Context, *appwire.Client) error {
				return io.EOF
			},
			wantErr: context.Canceled,
		},
		{
			name: "delivered peer refusal",
			call: func(context.Context, *appwire.Client) error {
				return appwire.InvalidParams("model not offered by this daemon")
			},
			want: appwire.InvalidParams("model not offered by this daemon"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := NewLocalDaemonSource("local", nil, nil)
			source.dial = dialTransport(newScriptedReplyTransport(false, func(request *appwire.Request) (any, error) {
				if request.Method != appwire.MethodInitialize {
					t.Errorf("call reached the wire: %s", request.Method)
				}
				return appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion}, nil
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := source.withMutationClientDisposition(ctx, rendezvousEntry("ws://daemon"), "pick-canceled", appwire.RetryDispositionBlocked, func(callCtx context.Context, client *appwire.Client) error {
				// The child context ends here: for a request still waiting on the
				// write slot that is the one failure appwire.Client can prove was
				// never transmitted.
				cancel()
				return test.call(callCtx, client)
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %T=%v, want %v", err, err, test.wantErr)
				}
				if wire, ok := errors.AsType[appwire.WireError](err); ok {
					t.Fatalf("call without a verdict was classified as %#v", wire)
				}
				// The child context's cancellation is the answer, not the
				// client's not-sent classification that unwraps to it.
				if notSent, ok := errors.AsType[appwire.RequestNotSentError](err); ok {
					t.Fatalf("provably unsent call was reported as %+v instead of the cancellation", notSent)
				}
				return
			}
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != test.want.Code || wire.Message != test.want.Message {
				t.Fatalf("error = %T=%+v, want the delivered refusal %+v preserved", err, err, test.want)
			}
		})
	}
}
