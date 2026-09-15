package appsource

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestRemoteHubSourceAdminCallReturnsRemoteResultVerbatim(t *testing.T) {
	source, calls := newScriptedRemote(t, "host", func(method string, params json.RawMessage) scriptedReply {
		if method != appwire.MethodEvenerInstanceList {
			t.Errorf("method = %q, want %q", method, appwire.MethodEvenerInstanceList)
		}
		return scriptedReply{result: appwire.InstanceListResponse{Instances: []appwire.InstanceEntry{{Name: "openai"}}}}
	})

	var out json.RawMessage
	if err := source.AdminCall(context.Background(), appwire.MethodEvenerInstanceList, json.RawMessage(`{"limit":1}`), &out); err != nil {
		t.Fatalf("AdminCall: %v", err)
	}
	if got := string(lastMethodCall(t, calls(), appwire.MethodEvenerInstanceList)); got != `{"limit":1}` {
		t.Fatalf("forwarded params = %s, want the caller's params unchanged", got)
	}
	var decoded appwire.InstanceListResponse
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode result %s: %v", out, err)
	}
	if len(decoded.Instances) != 1 || decoded.Instances[0].Name != "openai" {
		t.Fatalf("result = +%v, want the remote's list", decoded.Instances)
	}
}

func TestRemoteHubSourceAdminCallPreservesSemanticWireError(t *testing.T) {
	refusal := appwire.InvalidParams("refused by the host")
	source, _ := newScriptedRemote(t, "host", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{wireErr: &refusal}
	})

	var out json.RawMessage
	err := source.AdminCall(context.Background(), appwire.MethodEvenerLaunchSetLayer, nil, &out)
	if err == nil {
		t.Fatal("AdminCall succeeded despite the remote's refusal")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T %v, want appwire.WireError", err, err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
}

// TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters covers
// the component-07a broker seam: registration starts the drain and delivers
// host-level notifications that carry no thread route, and a cancelled context
// removes the consumer so a departed reader cannot wedge the shared drain.
func TestRemoteHubSourceSubscribeHostNotificationsDeliversAndUnregisters(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	server := appwire.NewStreamTransport(serverConn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := server.Recv(ctx)
			if err != nil {
				return
			}
			if msg.Request == nil || msg.Request.Method != appwire.MethodInitialize {
				continue
			}
			data, _ := json.Marshal(appwire.InitializeResponse{ProtocolVersion: appwire.ProtocolVersion, SourceID: "local"})
			if err := server.Send(ctx, appwire.ResponseMessage(msg.Request.ID, json.RawMessage(data))); err != nil {
				return
			}
		}
	}()

	client := appwire.NewClient(appwire.NewStreamTransport(clientConn))
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{}); err != nil {
		t.Fatalf("initialize scripted remote: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = client.Close()
		<-done
	})

	source := NewRemoteHubSource("host", nil, func(context.Context, string) (*appwire.Client, error) {
		return client, nil
	})

	subCtx, stopSub := context.WithCancel(context.Background())
	notifications, err := source.SubscribeHostNotifications(subCtx)
	if err != nil {
		t.Fatalf("SubscribeHostNotifications: %v", err)
	}

	if err := server.Send(ctx, appwire.NotificationMessage(appwire.NotifyEvenerAuthUpdated, map[string]string{"provider": "openai"})); err != nil {
		t.Fatalf("send notification: %v", err)
	}
	select {
	case notification := <-notifications:
		if notification.Method != appwire.NotifyEvenerAuthUpdated {
			t.Fatalf("notification method = %q, want %q", notification.Method, appwire.NotifyEvenerAuthUpdated)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the host-level notification")
	}

	stopSub()
	deadline := time.Now().Add(5 * time.Second)
	for {
		source.subMu.Lock()
		remaining := len(source.hostSubs)
		source.subMu.Unlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hostSubs = %d after cancel, want 0", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
