package interactiveartifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		for _, want := range []string{"bounded-pipe-frame", "next-private-request"} {
			message, err := server.Recv(ctx)
			if err != nil {
				serverDone <- err
				return
			}
			if message.Request == nil || message.Request.Method != want {
				serverDone <- errors.New("unexpected control frame")
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
	queuedCtx, queuedCancel := context.WithCancel(ctx)
	queuedCancel()
	queuedErr := controlRequest(queuedCtx, client, "never-sent", struct{}{}, nil)
	if _, ok := errors.AsType[appwire.RequestNotSentError](queuedErr); !ok || !errors.Is(queuedErr, context.Canceled) {
		t.Errorf("queued cancellation lost not-sent identity: %T %v", queuedErr, queuedErr)
	}
	cancel()
	// The real pipe is still mid-frame. Finish that shared frame using its
	// connection lifetime rather than close the stream on this call's behalf.
	close(paused.resume)
	err = <-callDone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call: %v", err)
	}
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); ok {
		t.Fatal("in-flight frame incorrectly marked unsent")
	}
	requireNoError(t, client.Request(ctx, "next-private-request", struct{}{}, nil))
	requireNoError(t, <-serverDone)
}

func TestControlCanceledBeforeDispatchPreservesAuthority(t *testing.T) {
	s := processSupervisor(t, filepath.Join(t.TempDir(), "private"), testPolicy)
	grant, err := s.Grant(t.Context(), testScope())
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = s.Revoke(ctx, grant.Token)
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); !ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-dispatch cancellation lost identity: %T %v", err, err)
	}
	client := sdkClient(t, grant.Readiness.Endpoint, grant.Token)
	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_list"})
	requireNoError(t, err)
	if result.IsError {
		t.Fatal("unsent revocation changed real service authority")
	}
}

func TestLifetimeTransportCanceledEntryWritesNoFrame(t *testing.T) {
	toChildR, toChildW, err := os.Pipe()
	requireNoError(t, err)
	toParentR, toParentW, err := os.Pipe()
	requireNoError(t, err)
	sender := &lifetimeTransport{Transport: appwire.NewStreamTransport(&pipeStream{reader: toParentR, writer: toChildW}), ctx: t.Context()}
	receiver := appwire.NewStreamTransport(&pipeStream{reader: toChildR, writer: toParentW})
	t.Cleanup(func() { _ = sender.Close(); _ = receiver.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = sender.Send(ctx, appwire.RequestMessage(appwire.NewIntID(1), "canceled", struct{}{}))
	if _, ok := errors.AsType[appwire.RequestNotSentError](err); !ok {
		t.Errorf("canceled entry=%T %v", err, err)
	}
	requireNoError(t, sender.Send(t.Context(), appwire.RequestMessage(appwire.NewIntID(2), "live", struct{}{})))
	message, err := receiver.Recv(t.Context())
	requireNoError(t, err)
	if message.Request == nil || message.Request.Method != "live" {
		t.Fatalf("canceled frame was written: %+v", message)
	}
}
