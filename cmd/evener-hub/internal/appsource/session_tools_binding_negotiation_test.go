package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func TestSessionToolsBindingRequiresNegotiatedBind(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		sessionTools   bool
		bindReject     bool
		managerAllowed bool
		wantHubID      string
	}{
		{name: "feature unavailable", wantHubID: "hub-local"},
		{name: "bind rejected", sessionTools: true, bindReject: true},
		{name: "negotiated binding", sessionTools: true, managerAllowed: true, wantHubID: "hub-local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := rendezvous.Entry{
				Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
				ThreadID: "thread", SessionID: "instance", InstanceID: "instance", WorkspaceRef: "local:thread",
			}
			source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
				return []LocalDaemonEntry{{Entry: entry}}
			}, nil)
			wanted := source.threadFromEntry(LocalDaemonEntry{Entry: entry})
			if wanted.Evener.InstanceID != entry.SessionID {
				t.Fatalf("wanted instance = %q, binding instance = %q", wanted.Evener.InstanceID, entry.SessionID)
			}
			var bindingRequests atomic.Int32
			source.SetSessionTools(func(appwire.SessionBinding) (appwire.SessionHubClient, error) {
				return bindingTestOwner{}, nil
			}, func() (string, error) { return "hub-local", nil })
			source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
				transport := newScriptedAppwireTransport(nil)
				transport.send = func(_ context.Context, message appwire.Message) error {
					if message.Request == nil {
						return nil
					}
					var response any
					switch message.Request.Method {
					case appwire.MethodInitialize:
						response = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, Features: appwire.FeatureSet{SessionTools: tc.sessionTools, TurnStart: true}}
					case appwire.MethodRuntimeSessionBind:
						bindingRequests.Add(1)
						if tc.bindReject {
							transport.recv <- appwire.ErrorMessage(message.Request.ID, appwire.Unavailable("binding refused"))
							return nil
						}
						var binding appwire.SessionBinding
						if err := json.Unmarshal(message.Request.Params, &binding); err != nil {
							return err
						}
						response = binding
					case appwire.MethodThreadRead:
						response = appwire.ThreadReadResponse{Thread: appwire.Thread{
							ID: "instance", Source: "local",
							Evener: appwire.EvenerThread{Ref: "local:thread", InstanceID: "instance"},
						}}
					case appwire.MethodTurnStart:
						response = appwire.TurnStartResponse{}
					default:
						return errors.New("unexpected AppWire method")
					}
					transport.recv <- appwire.ResponseMessage(message.Request.ID, response)
					return nil
				}
				return transport, nil
			}

			ref, err := appwire.ParseRef("local:thread")
			if err != nil {
				t.Fatal(err)
			}
			lease, err := source.AcquireRelaySession(ref)
			if err != nil {
				t.Fatalf("acquire relay: %v", err)
			}
			t.Cleanup(lease.Close)
			read, err := lease.Read(t.Context(), appwire.ThreadReadParams{Ref: ref.String()})
			if err != nil {
				t.Fatalf("read through negotiated connection: %v", err)
			}
			if read.Handoff != nil {
				read.Handoff.Commit()
			}

			source.sessionToolsMu.Lock()
			published := source.sessionToolsConnections[ref.String()].binding
			source.sessionToolsMu.Unlock()

			managerProof := &appwire.SessionActorProof{CallerRef: "local:manager", Actor: appwire.SessionActor{Kind: "manager"}}
			mutation := appwire.TurnStartParams{
				Ref: ref.String(), ThreadID: "instance", ExpectedInstanceID: "instance",
				ClientMutationID: "mutation-id", Input: []appwire.InputItem{{Type: "text", Text: "sentinel"}},
				ManagerProof: managerProof,
			}
			if tc.managerAllowed {
				if _, err := source.StartTurnAtEntry(t.Context(), entry, mutation); err != nil {
					t.Fatalf("Manager mutation through negotiated binding: %v", err)
				}
			} else if _, err := source.StartTurnAtEntry(t.Context(), entry, mutation); err == nil {
				t.Fatal("Manager mutation accepted without a successful SessionTools bind")
			}
			if published.HubID != tc.wantHubID {
				t.Fatalf("published HubID = %q, want %q", published.HubID, tc.wantHubID)
			}
			if tc.managerAllowed {
				if published.BindingID == "" {
					t.Fatal("successful SessionTools bind did not publish binding ID")
				}
			} else if published.BindingID != "" {
				t.Fatal("unadmitted SessionTools connection published a binding ID")
			}

			// Human input has no ManagerProof and remains on the ordinary relay path.
			mutation.ManagerProof = nil
			if _, err := source.StartTurnAtEntry(t.Context(), entry, mutation); err != nil {
				t.Fatalf("human mutation without ManagerProof: %v", err)
			}
			if got := bindingRequests.Load(); got != boolCount(tc.sessionTools) {
				t.Fatalf("runtime-session-bind calls = %d, want %d", got, boolCount(tc.sessionTools))
			}
		})
	}
}

func boolCount(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

type bindingTestOwner struct{}

func (bindingTestOwner) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Manager: true}, nil
}
func (bindingTestOwner) Invoke(context.Context, appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	return appwire.SessionOperationResponse{}, nil
}
func (bindingTestOwner) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, nil
}
func TestSessionToolsRejectedBindRecoversThroughRunOwner(t *testing.T) {
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: "ws://daemon", SourceID: "local",
		ThreadID: "thread", SessionID: "instance", InstanceID: "instance", WorkspaceRef: "local:thread",
	}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{{Entry: entry}}
	}, nil)
	wanted := source.threadFromEntry(LocalDaemonEntry{Entry: entry})
	if wanted.Evener.InstanceID != entry.SessionID {
		t.Fatalf("wanted instance = %q, binding instance = %q", wanted.Evener.InstanceID, entry.SessionID)
	}
	var bindingRequests atomic.Int32
	firstBindAttempt := make(chan struct{}, 1)
	releaseFirstBind := make(chan struct{})
	secondBindAttempt := make(chan struct{}, 1)
	releaseSecondBind := make(chan struct{})
	recoveredRead := make(chan struct{}, 1)
	secondBindingID := make(chan string, 1)
	managerTurnBindingID := make(chan string, 1)
	source.SetSessionTools(func(appwire.SessionBinding) (appwire.SessionHubClient, error) {
		return bindingTestOwner{}, nil
	}, func() (string, error) { return "hub-local", nil })
	source.dial = func(context.Context, string, *http.Client, http.Header) (appwire.Transport, error) {
		transport := newScriptedAppwireTransport(nil)
		transport.send = func(ctx context.Context, message appwire.Message) error {
			if message.Request == nil {
				return nil
			}
			var response any
			switch message.Request.Method {
			case appwire.MethodInitialize:
				response = appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, Features: appwire.FeatureSet{SessionTools: true, TurnStart: true}}
			case appwire.MethodRuntimeSessionBind:
				attempt := bindingRequests.Add(1)
				if attempt == 1 {
					firstBindAttempt <- struct{}{}
					// TRIPWIRE: bound the test-controlled first refusal so a broken test cannot hang.
					select {
					case <-releaseFirstBind:
					case <-ctx.Done():
						return ctx.Err()
					}
					transport.recv <- appwire.ErrorMessage(message.Request.ID, appwire.Unavailable("transient bind refusal"))
					return nil
				}
				secondBindAttempt <- struct{}{}
				// TRIPWIRE: keep the recovered bind pending until authority checks finish.
				select {
				case <-releaseSecondBind:
				case <-ctx.Done():
					return ctx.Err()
				}
				var binding appwire.SessionBinding
				if err := json.Unmarshal(message.Request.Params, &binding); err != nil {
					return err
				}
				response = binding
				secondBindingID <- binding.BindingID
			case appwire.MethodThreadRead:
				response = appwire.ThreadReadResponse{Thread: appwire.Thread{
					ID: "instance", Source: "local",
					Evener: appwire.EvenerThread{Ref: "local:thread", InstanceID: "instance"},
				}}
				if bindingRequests.Load() >= 2 {
					recoveredRead <- struct{}{}
				}
			case appwire.MethodTurnStart:
				var params appwire.TurnStartParams
				if err := json.Unmarshal(message.Request.Params, &params); err != nil {
					return err
				}
				if params.ManagerProof != nil {
					if params.ManagerProof.BindingID == "" {
						return errors.New("recovered Manager mutation had no binding proof")
					}
					managerTurnBindingID <- params.ManagerProof.BindingID
				}
				response = appwire.TurnStartResponse{}
			default:
				return errors.New("unexpected AppWire method")
			}
			transport.recv <- appwire.ResponseMessage(message.Request.ID, response)
			return nil
		}
		return transport, nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		source.RunSessionTools(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		// TRIPWIRE: every exit cancels and joins the RunSessionTools owner.
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("RunSessionTools did not stop during cleanup")
		}
	})
	// TRIPWIRE: bound event waits; no sleeps or widened deadlines.
	select {
	case <-firstBindAttempt:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSessionTools did not attempt the initial binding")
	}
	close(releaseFirstBind)
	select {
	case <-secondBindAttempt:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSessionTools did not attempt recovery after the refusal")
	}
	ref, err := appwire.ParseRef("local:thread")
	if err != nil {
		t.Fatal(err)
	}
	managerMutation := appwire.TurnStartParams{
		Ref: ref.String(), ThreadID: "instance", ExpectedInstanceID: "instance",
		ClientMutationID: "manager-mutation", Input: []appwire.InputItem{{Type: "text", Text: "manager"}},
		ManagerProof: &appwire.SessionActorProof{CallerRef: "local:manager", Actor: appwire.SessionActor{Kind: "manager"}},
	}
	if _, err := source.StartTurnAtEntry(t.Context(), entry, managerMutation); err == nil {
		t.Fatal("Manager mutation accepted before a successful binding")
	}
	managerMutation.ManagerProof = nil
	managerMutation.ClientMutationID = "human-mutation"
	if _, err := source.StartTurnAtEntry(t.Context(), entry, managerMutation); err != nil {
		t.Fatalf("human mutation should remain available: %v", err)
	}
	close(releaseSecondBind)
	select {
	case <-recoveredRead:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSessionTools did not recover, publish, and read after the transient refusal")
	}
	if got := bindingRequests.Load(); got < 2 {
		t.Fatalf("binding attempts = %d, want recovery attempt", got)
	}
	managerMutation.ManagerProof = &appwire.SessionActorProof{CallerRef: "local:manager", Actor: appwire.SessionActor{Kind: "manager"}}
	managerMutation.ClientMutationID = "manager-after-recovery"
	if _, err := source.StartTurnAtEntry(t.Context(), entry, managerMutation); err != nil {
		t.Fatalf("Manager mutation after recovered binding: %v", err)
	}
	var publishedBindingID string
	select {
	case publishedBindingID = <-secondBindingID:
	case <-time.After(5 * time.Second):
		t.Fatal("recovered binding request was not observed")
	}
	select {
	case got := <-managerTurnBindingID:
		if got != publishedBindingID {
			t.Fatalf("Manager proof binding = %q, recovered binding = %q", got, publishedBindingID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovered Manager mutation was not observed")
	}
}
