package appwiretest

import (
	"context"
	"testing"

	"primeradiant.com/serf/appwire"
)

func FuzzScriptedTransportLifecycle(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3}, "session.event")
	f.Add([]byte{0, 0, 4}, "")

	f.Fuzz(func(t *testing.T, operations []byte, method string) {
		if len(operations) > 64 {
			operations = operations[:64]
		}
		if len(method) > 256 {
			method = method[:256]
		}

		transport := NewScriptedTransport()
		closed := false
		for _, op := range operations {
			switch op % 5 {
			case 0:
				err := transport.Send(context.Background(), appwire.Message{
					Notification: &appwire.Notification{Method: method},
				})
				if closed {
					if err == nil {
						t.Fatal("Send after Close returned nil error")
					}
					continue
				}
				if err != nil {
					t.Fatalf("Send: %v", err)
				}
				if sent := <-transport.Sent(); sent.Kind() != appwire.MessageNotification {
					t.Fatalf("sent kind = %v, want notification", sent.Kind())
				}
			case 1:
				if closed {
					continue
				}
				transport.DeliverNotification(appwire.Notification{Method: method})
				got, err := transport.Recv(context.Background())
				if err != nil {
					t.Fatalf("Recv notification: %v", err)
				}
				if got.Notification == nil || got.Notification.Method != method {
					t.Fatalf("notification = %+v, want method %q", got.Notification, method)
				}
			case 2:
				if closed {
					continue
				}
				id := appwire.NewIntID(int64(op))
				transport.DeliverResponse(id, method)
				got, err := transport.Recv(context.Background())
				if err != nil {
					t.Fatalf("Recv response: %v", err)
				}
				if got.Response == nil || got.Response.ID.Int64() != int64(op) {
					t.Fatalf("response = %+v, want id %d", got.Response, op)
				}
			case 3, 4:
				if err := transport.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
				closed = true
			}
		}

		if err := transport.Close(); err != nil {
			t.Fatalf("final Close: %v", err)
		}
		if _, err := transport.Recv(context.Background()); err == nil {
			t.Fatal("Recv after Close returned nil error")
		}
	})
}

func FuzzScriptedTransportEdges(f *testing.F) {
	for scenario := range 4 {
		f.Add(uint8(scenario), "message")
	}
	f.Fuzz(func(t *testing.T, scenario uint8, message string) {
		transport := NewScriptedTransport()
		switch scenario % 4 {
		case 0:
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			if err := transport.Send(context.Background(), appwire.Message{}); err == nil {
				t.Fatal("send on closed transport succeeded")
			}
		case 1:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := transport.Recv(ctx); err == nil {
				t.Fatal("receive with canceled context succeeded")
			}
		case 2:
			id := appwire.NewIntID(42)
			transport.DeliverError(id, -1, message)
			got, err := transport.Recv(context.Background())
			if err != nil || got.Error == nil || got.Error.Error.Message != message {
				t.Fatalf("Recv()=(%+v, %v)", got, err)
			}
		case 3:
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
			if err := transport.Close(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
