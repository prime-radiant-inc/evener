package appserver

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
)

func TestServeWebSocketRecoveryBypassesSerialWorkerAndRefusesOverlap(t *testing.T) {
	server, httpServer, serialStarted, releaseSerial := dispatchSlowSerialServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	HandleTyped(server.Router(), appwire.MethodEvenerThreadForceStop, func(ctx context.Context, _ appwire.ThreadForceStopParams) (appwire.EmptyResponse, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return appwire.EmptyResponse{}, nil
	})
	client := dialAppWireClient(t, httpServer)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { _, _ = client.ThreadList(ctx, appwire.ThreadListParams{}) }()
	waitFor(t, "serial handler", serialStarted)
	first := make(chan error, 1)
	go func() {
		first <- client.Request(ctx, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}, nil)
	}()
	waitFor(t, "recovery handler", started)
	if err := client.Request(ctx, appwire.MethodEvenerThreadForceStop, appwire.ThreadForceStopParams{Ref: "local:owner"}, nil); err == nil || ctx.Err() != nil {
		t.Fatalf("overlapping recovery error=%v context=%v", err, ctx.Err())
	}
	if err := client.Request(ctx, appwire.MethodPing, appwire.EmptyParams{}, nil); err != nil {
		t.Fatal(err)
	}
	releaseSerial()
}
