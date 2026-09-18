package interactiveartifacts

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagedHTTPClientReleasesIdleSockets(t *testing.T) {
	idle := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			idle <- struct{}{}
		case http.StateClosed:
			closed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	client := NewHTTPClient("opaque")
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	requireNoError(t, err)
	response, err := client.Do(req)
	requireNoError(t, err)
	requireNoError(t, response.Body.Close())
	<-idle
	client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("managed HTTP client retained released idle socket")
	}
}
