package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

const seedSubscriptionMethod = "test/seedSubscription"

type seedSubscriptionParams struct {
	ThreadID string `json:"threadId"`
}

// seedSubscription connects a client to server's websocket edge and subscribes
// it to threadID through the same admission path thread/read uses, so the
// registry holds a live, registered connection rather than a hand-built one.
func seedSubscription(t *testing.T, server *appserver.Server, threadID string) {
	t.Helper()
	appserver.HandleTyped(server.Router(), seedSubscriptionMethod, func(ctx context.Context, p seedSubscriptionParams) (struct{}, error) {
		return struct{}{}, appserver.SubscribeWithAdmission(ctx, p.ThreadID, false)
	})
	edge := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(edge.Close)
	transport, err := appwire.DialWebSocket(context.Background(), "ws"+edge.URL[len("http"):], edge.Client())
	if err != nil {
		t.Fatalf("dial subscriber: %v", err)
	}
	client := appwire.NewClient(transport)
	client.Start(context.Background())
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("initialize subscriber: %v", err)
	}
	if err := client.Request(context.Background(), seedSubscriptionMethod, seedSubscriptionParams{ThreadID: threadID}, nil); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}
