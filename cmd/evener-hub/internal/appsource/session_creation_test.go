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
