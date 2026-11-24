package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// The remote owner's error, not a successful response, carries the identity.
// The actual router drops the response when the handler returns an error.
func TestManagerCreatePostSpawnFailureRemoteRouter(t *testing.T) {
	owner := appserver.NewServer(appserver.ServerConfig{ServerName: "owner", SourceID: "local"})
	appserver.HandleTyped(owner.Router(), appwire.MethodThreadStart, func(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
		if params.Source != "" || params.Harness != "evener" {
			t.Errorf("forwarded routing fields: %+v", params)
		}
		return appwire.ThreadStartResponse{Thread: appwire.Thread{Evener: appwire.EvenerThread{Ref: "local:should-not-be-used"}}}, appwire.WireError{
			Code: appwire.CodeUnavailable, Message: "owner read failed", Data: map[string]any{
				"evenerErrorInfo": appwire.ErrorSessionUnavailable, "cause": "ownerRead", "createdRef": "local:created", "createdInstanceId": "instance-1",
			},
		}
	})
	server := httptest.NewServer(http.HandlerFunc(owner.ServeWebSocket))
	defer server.Close()
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	defer client.Close()
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	_, err = source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("start error %T: %v", err, err)
	}
	data, marshalErr := json.Marshal(wire.Data)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	var partial struct {
		appwire.ErrorData
		CreatedRef        string `json:"createdRef"`
		CreatedInstanceID string `json:"createdInstanceId"`
	}
	if err := json.Unmarshal(data, &partial); err != nil {
		t.Fatal(err)
	}
	if wire.Code != appwire.CodeUnavailable || partial.EvenerErrorInfo != appwire.ErrorSessionUnavailable || partial.Cause != "ownerRead" || partial.CreatedRef != "host:created" || partial.CreatedInstanceID != "instance-1" || partial.RetryDisposition == appwire.RetryDispositionAutomatic {
		t.Fatalf("remote partial error lost original classification or translated identity: %+v, data=%s", wire, data)
	}
}

func TestRemoteThreadStartErrorPreservesIdempotencyFields(t *testing.T) {
	for _, tc := range []struct {
		name             string
		mutationOutcome  appwire.MutationOutcome
		retryDisposition appwire.RetryDisposition
	}{
		{name: "unknown blocked", mutationOutcome: appwire.MutationOutcomeUnknown, retryDisposition: appwire.RetryDispositionBlocked},
		{name: "target deleted none", mutationOutcome: appwire.MutationOutcomeTargetDeleted, retryDisposition: appwire.RetryDispositionNone},
		{name: "retryable partial empty", mutationOutcome: "", retryDisposition: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := appserver.NewServer(appserver.ServerConfig{ServerName: "owner", SourceID: "local"})
			appserver.HandleTyped(owner.Router(), appwire.MethodThreadStart, func(_ context.Context, _ appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
				return appwire.ThreadStartResponse{}, appwire.WireError{
					Code:    appwire.CodeUnavailable,
					Message: "post-spawn error",
					Data: appwire.ThreadStartPartialErrorData{
						ErrorData: appwire.ErrorData{
							EvenerErrorInfo:  appwire.ErrorSessionUnavailable,
							ClientMutationID: "mut-remote-1",
							MutationOutcome:  tc.mutationOutcome,
							RetryDisposition: tc.retryDisposition,
						},
						CreatedRef:        "local:created",
						CreatedInstanceID: "instance-1",
					},
				}
			})
			server := httptest.NewServer(http.HandlerFunc(owner.ServeWebSocket))
			defer server.Close()
			transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			client := appwire.NewClient(transport)
			client.Start(t.Context())
			defer client.Close()
			if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
				t.Fatal(err)
			}
			source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
			_, err = source.StartThread(t.Context(), appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-remote-1"})
			var wire appwire.WireError
			if !errors.As(err, &wire) {
				t.Fatalf("start error %T: %v", err, err)
			}
			raw, _ := json.Marshal(wire.Data)
			var got appwire.ThreadStartPartialErrorData
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.CreatedRef != "host:created" || got.CreatedInstanceID != "instance-1" ||
				got.ClientMutationID != "mut-remote-1" || got.MutationOutcome != tc.mutationOutcome || got.RetryDisposition != tc.retryDisposition {
				t.Fatalf("translated partial error = %+v, want ref=host:created id=mut-remote-1 outcome=%q retry=%q", got, tc.mutationOutcome, tc.retryDisposition)
			}
		})
	}
}

func TestRemoteThreadStartPreservesRequestNotSentError(t *testing.T) {
	transport := &gatedSendTransport{firstSendEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
	client := appwire.NewClient(transport)
	go func() { _ = client.Notify(t.Context(), appwire.NotifyEvenerAuthUpdated, map[string]string{}) }()
	<-transport.firstSendEntered

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) { return client, nil })
	ctx, cancel := context.WithCancel(t.Context())
	ctx = appwire.WithRequestIDObserver(ctx, func(appwire.ID) { cancel() })
	_, err := source.StartThread(ctx, appwire.ThreadStartParams{Source: "host", ClientMutationID: "mut-notsent"})
	close(transport.releaseFirst)
	if got := transport.sendCount(); got != 1 {
		t.Fatalf("transport sends = %d, want 1", got)
	}
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); !ok {
		t.Fatalf("StartThread err = %T (%v), want appwire.RequestNotSentError", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartThread err = %v, want context.Canceled in chain", err)
	}
}
