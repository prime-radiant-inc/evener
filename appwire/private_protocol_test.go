package appwire

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

func TestPrivateArtifactCatalogsAreExactAndIsolated(t *testing.T) {
	bootstrapScope := MethodScope("daemon-bootstrap")
	brokerScope := MethodScope("daemon-broker")
	if !bootstrapScope.Routed() || !brokerScope.Routed() {
		t.Fatal("private daemon scopes must participate in router catalog checks")
	}

	bootstrap := CatalogMethodNames(bootstrapScope)
	broker := CatalogMethodNames(brokerScope)
	slices.Sort(bootstrap)
	slices.Sort(broker)
	wantBootstrap := []string{
		"evener/artifacts/broker/finalizeOwnership",
		"evener/artifacts/broker/install",
		"evener/artifacts/broker/launchHello",
		"evener/artifacts/broker/launchInstall",
	}
	wantBroker := []string{"evener/artifacts/broker/authenticate"}
	if !reflect.DeepEqual(bootstrap, wantBootstrap) {
		t.Fatalf("bootstrap catalog = %v, want %v", bootstrap, wantBootstrap)
	}
	if !reflect.DeepEqual(broker, wantBroker) {
		t.Fatalf("broker catalog = %v, want %v", broker, wantBroker)
	}

	for _, scope := range []MethodScope{ScopeHub, ScopeDaemon} {
		for _, method := range CatalogMethodNames(scope) {
			if slices.Contains(wantBootstrap, method) || slices.Contains(wantBroker, method) {
				t.Fatalf("ordinary %s catalog includes private method %q", scope, method)
			}
		}
	}
	for _, method := range append(bootstrap, broker...) {
		if method == MethodThreadList || method == MethodPing || method == MethodInitialize {
			t.Fatalf("private catalog includes ordinary method %q", method)
		}
	}
}

func TestPrivateWSTransportBoundsFramesWithoutRecording(t *testing.T) {
	recordingPath := filepath.Join(t.TempDir(), "frames.jsonl")
	recorder, err := NewFrameRecorder(recordingPath)
	if err != nil {
		t.Fatal(err)
	}
	previousRecorder := appwireFrameRecorder
	appwireFrameRecorder = recorder
	t.Cleanup(func() { appwireFrameRecorder = previousRecorder })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			t.Errorf("accept: %v", acceptErr)
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		if _, _, readErr := conn.Read(r.Context()); readErr != nil {
			t.Errorf("read valid private frame: %v", readErr)
			return
		}
		response := ResponseMessage(NewIntID(1), BrokerAuthenticateResponse{DaemonEpoch: "daemon-epoch"})
		encoded, marshalErr := marshalWSMessage(response)
		if marshalErr != nil {
			t.Errorf("marshal response: %v", marshalErr)
			return
		}
		if writeErr := conn.Write(r.Context(), websocket.MessageText, encoded); writeErr != nil {
			t.Errorf("write response: %v", writeErr)
		}
	}))
	defer server.Close()

	conn, _, err := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := NewPrivateWSTransport(conn)
	defer transport.Close() //nolint:errcheck
	if err := transport.Send(t.Context(), RequestMessage(NewIntID(1), MethodBrokerAuthenticate, BrokerAuthenticateParams{DaemonEpoch: "daemon-epoch", Capability: "secret-capability"})); err != nil {
		t.Fatalf("send valid private frame: %v", err)
	}
	if _, err := transport.Recv(t.Context()); err != nil {
		t.Fatalf("receive valid private frame: %v", err)
	}
	oversize := RequestMessage(NewIntID(2), MethodBrokerAuthenticate, BrokerAuthenticateParams{Capability: strings.Repeat("x", PrivateBrokerFrameLimit)})
	if err := transport.Send(context.Background(), oversize); !errors.Is(err, ErrPrivateFrameTooLarge) {
		t.Fatalf("oversize send error = %v, want ErrPrivateFrameTooLarge", err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	recorded, err := os.ReadFile(recordingPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 0 {
		t.Fatalf("private frames reached global recorder: %s", recorded)
	}
}
