package appwire

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedWriteTransport lets a test hold a client's single frame-write slot open:
// the first Send parks inside the transport until release is closed, so any
// second Request on the same client is provably queued on Client's sendMu while
// the test decides what happens to its context. Later sends refuse a canceled
// context the way StreamTransport.Send does — before a byte is written — and
// never park again. sendCount reports how many frames were offered to the
// transport at all, which is what makes "never transmitted" checkable.
type gatedWriteTransport struct {
	firstSendStarted chan struct{}
	release          chan struct{}

	mu    sync.Mutex
	sends int
}

func newGatedWriteTransport() *gatedWriteTransport {
	return &gatedWriteTransport{
		firstSendStarted: make(chan struct{}),
		release:          make(chan struct{}),
	}
}

func (g *gatedWriteTransport) Send(ctx context.Context, _ Message) error {
	g.mu.Lock()
	g.sends++
	first := g.sends == 1
	g.mu.Unlock()
	if first {
		close(g.firstSendStarted)
		<-g.release
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (g *gatedWriteTransport) Recv(ctx context.Context) (Message, error) {
	<-ctx.Done()
	return Message{}, ctx.Err()
}

func (g *gatedWriteTransport) Close() error { return nil }

func (g *gatedWriteTransport) sendCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sends
}

// recordingTransport accepts every frame and never answers, so a request that
// reached the wire parks until its context ends. It is the post-send
// counterpart to gatedWriteTransport.
type recordingTransport struct {
	mu     sync.Mutex
	sends  int
	onSend func()
}

func (r *recordingTransport) Send(_ context.Context, _ Message) error {
	r.mu.Lock()
	r.sends++
	r.mu.Unlock()
	if r.onSend != nil {
		r.onSend()
	}
	return nil
}

func (r *recordingTransport) Recv(ctx context.Context) (Message, error) {
	<-ctx.Done()
	return Message{}, ctx.Err()
}

func (r *recordingTransport) Close() error { return nil }

// TestClientRequestReportsAPreSendCancellationAsNotSent pins the seam round
// eight's finding needs: Client serializes frame writes on sendMu, and a
// context that ends while the request is queued there must be reported as
// provably unsent — the caller's cancellation still matches errors.Is, wrapped
// in RequestNotSentError so a caller deciding whether a non-idempotent remote
// mutation may have been applied can tell it apart from a lost response.
//
// The transport is the proof: only the queued holder's frame ever reaches it,
// so the request under test cannot have been written.
func TestClientRequestReportsAPreSendCancellationAsNotSent(t *testing.T) {
	transport := newGatedWriteTransport()
	client := NewClient(transport)

	// Take the write slot with a call that parks inside the transport.
	holderCtx := t.Context()
	holder := make(chan error, 1)
	go func() { holder <- client.Notify(holderCtx, NotifyEvenerAuthUpdated, map[string]string{}) }()
	select {
	case <-transport.firstSendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the write-slot holder never reached the transport")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Request(ctx, MethodEvenerInstanceList, EmptyParams{}, nil) }()
	// Let the request queue on the write slot, then end its context there.
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(transport.release)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Request did not return after its context was canceled while queued")
	}

	var notSent RequestNotSentError
	if !errors.As(err, &notSent) {
		t.Fatalf("error = %T %v, want RequestNotSentError: a request queued on the write slot was never transmitted", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to still match context.Canceled", err)
	}
	if notSent.Err == nil {
		t.Fatalf("RequestNotSentError must carry the context error that ended the call, got %+v", notSent)
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("error text = %q, want it to keep the cause's text", err.Error())
	}
	if got := transport.sendCount(); got != 1 {
		t.Fatalf("transport saw %d sends, want 1: the queued request must never reach the transport", got)
	}
}

// TestClientRequestKeepsAPostSendCancellationUnmarked pins the other side of the
// seam: once the frame has been handed to the transport, the client cannot know
// whether it arrived, so a context end there stays a bare context error and is
// never reported as not-sent. The distinction is only trustworthy in one
// direction, which is what makes it safe for a mutation's retry decision.
func TestClientRequestKeepsAPostSendCancellationUnmarked(t *testing.T) {
	sent := make(chan struct{})
	transport := &recordingTransport{onSend: func() {
		select {
		case <-sent:
		default:
			close(sent)
		}
	}}
	client := NewClient(transport)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Request(ctx, MethodEvenerPluginInstall, EmptyParams{}, nil) }()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the transport")
	}
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Request did not return after its context was canceled")
	}
	if err == nil {
		t.Fatal("Request succeeded after its context was canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if notSent, ok := errors.AsType[RequestNotSentError](err); ok {
		t.Fatalf("error = %v (%+v); a frame that was written must not be reported as not sent", err, notSent)
	}
}
