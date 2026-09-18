package interactiveartifacts

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

type pausedControlRead struct {
	pipeStream
	once    sync.Once
	entered chan struct{}
	resume  chan struct{}
}

func (p *pausedControlRead) Read(data []byte) (int, error) {
	first := false
	p.once.Do(func() { first = true })
	if first {
		n, err := p.reader.Read(data[:1])
		close(p.entered)
		return n, err
	}
	<-p.resume
	return p.reader.Read(data)
}
func TestControlCancellationDuringRealPipeWritePreservesOtherRequests(t *testing.T) {
	toChildR, toChildW, err := os.Pipe()
	requireNoError(t, err)
	toParentR, toParentW, err := os.Pipe()
	requireNoError(t, err)
	ctx := t.Context()
	raw := appwire.NewStreamTransport(&pipeStream{reader: toParentR, writer: toChildW})
	client := appwire.NewClient(&lifetimeTransport{Transport: raw, ctx: ctx})
	client.Start(ctx)
	t.Cleanup(func() { _ = client.Close() })
	paused := &pausedControlRead{reader: toChildR, writer: toParentW, entered: make(chan struct{}), resume: make(chan struct{})}
	server := appwire.NewStreamTransport(paused)
	t.Cleanup(func() { _ = server.Close() })
	serverDone := make(chan error, 1)
	go func() {
		for range 2 {
			message, err := server.Recv(ctx)
			if err != nil {
				serverDone <- err
				return
			}
			if err := server.Send(ctx, appwire.Message{Response: &appwire.Response{ID: message.Request.ID, Result: struct{}{}}}); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	callCtx, cancel := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		callDone <- client.Request(callCtx, "bounded-pipe-frame", struct {
			Data string `json:"data"`
		}{Data: strings.Repeat("x", 1<<20)}, nil)
	}()
	<-paused.entered
	cancel()
	// The real pipe is still mid-frame. Finish that shared frame using its
	// connection lifetime rather than close the stream on this call's behalf.
	close(paused.resume)
	if err := <-callDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call: %v", err)
	}
	requireNoError(t, client.Request(ctx, "next-private-request", struct{}{}, nil))
	requireNoError(t, <-serverDone)
}
