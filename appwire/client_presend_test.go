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
// second Request on the same client is provably queued on Client's write slot
// while the test decides what happens to its context. Later sends refuse a canceled
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
// eight's finding needs: Client serializes frame writes on one write slot, and a
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

// waitForPendingRequests blocks until the client has want requests registered,
// which is the point at which a request is committed to acquiring the write
// slot. It is how the queued-window test makes "queued behind the holder" a
// fact rather than a sleep's guess.
func waitForPendingRequests(t *testing.T, client *Client, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		client.pendingMu.Lock()
		got := len(client.pending)
		client.pendingMu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending requests = %d, want %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestClientRequestQueuedBehindAStuckWriteReturnsNotSentOnCancel pins round
// nine's write-slot finding: the wait for the slot must observe the request's
// context, not just the write itself. The holder parks inside the transport for
// the whole assertion window — a wedged transport write that never returns on
// its own — and the request queued behind it must still report
// RequestNotSentError promptly once its context ends, instead of parking behind
// the holder until the slot frees (which, for this holder, is never). The
// transport's send count proves the queued request itself never reached it, and
// the follow-up write after the holder is released proves the early return did
// not consume the slot.
func TestClientRequestQueuedBehindAStuckWriteReturnsNotSentOnCancel(t *testing.T) {
	transport := newGatedWriteTransport()
	client := NewClient(transport)

	// Take the write slot with a call that parks inside the transport and stays
	// parked: release is only closed after the request under test has returned.
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
	// Registration precedes the slot acquisition, so once the request is
	// pending it is committed to queueing behind the parked holder.
	waitForPendingRequests(t, client, 1)
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Request stayed queued behind a write that was still in progress after its context was canceled")
	}

	if _, notSent := errors.AsType[RequestNotSentError](err); !notSent {
		t.Fatalf("error = %T %v, want RequestNotSentError: the queued request was never transmitted", err, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to still match context.Canceled", err)
	}
	if got := transport.sendCount(); got != 1 {
		t.Fatalf("transport saw %d sends, want 1: the queued request must never reach the transport", got)
	}

	// Release the parked holder; the not-sent return must have left the slot
	// free for the next writer rather than consuming its token.
	close(transport.release)
	select {
	case herr := <-holder:
		if herr != nil {
			t.Fatalf("holder Notify: %v", herr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write-slot holder did not return after release")
	}
	if err := client.Notify(t.Context(), NotifyEvenerAuthUpdated, map[string]string{}); err != nil {
		t.Fatalf("Notify after the not-sent return: %v", err)
	}
	if got := transport.sendCount(); got != 2 {
		t.Fatalf("transport saw %d sends, want 2: the slot must still be usable after a not-sent return", got)
	}
}

// serializingWriteTransport records the peak number of Sends inside the
// transport at once: each Send holds an in-flight count across a short pause,
// so two writers that were not serialized would overlap and push the peak above
// one.
type serializingWriteTransport struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	sends       int
}

func (s *serializingWriteTransport) Send(context.Context, Message) error {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.sends++
	s.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return nil
}

func (s *serializingWriteTransport) Recv(ctx context.Context) (Message, error) {
	<-ctx.Done()
	return Message{}, ctx.Err()
}

func (s *serializingWriteTransport) Close() error { return nil }

func (s *serializingWriteTransport) peak() (maxInFlight, sends int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxInFlight, s.sends
}

// TestClientWritesStaySerializedOnTheSharedSlot pins the other half of round
// nine's write-slot change: making the wait cancellable must not let two frames
// reach the transport at once. Notify and Request callers all take the same
// one-token slot, so the transport never sees concurrent Sends and every writer
// still gets through.
func TestClientWritesStaySerializedOnTheSharedSlot(t *testing.T) {
	transport := &serializingWriteTransport{}
	client := NewClient(transport)

	const writers = 8
	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		if i%2 == 0 {
			go func() {
				defer wg.Done()
				if err := client.Notify(context.Background(), NotifyEvenerAuthUpdated, map[string]string{}); err != nil {
					t.Errorf("Notify: %v", err)
				}
			}()
			continue
		}
		// Every other writer is a Request: it takes the same slot through the
		// cancellable path, then parks waiting for a response this transport
		// never sends until reqCtx ends.
		go func() {
			defer wg.Done()
			if err := client.Request(reqCtx, MethodEvenerInstanceList, EmptyParams{}, nil); err == nil {
				t.Error("Request succeeded without a response")
			}
		}()
	}

	deadline := time.Now().Add(10 * time.Second)
	var sends int
	for {
		_, sends = transport.peak()
		if sends == writers {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transport saw %d sends, want %d", sends, writers)
		}
		time.Sleep(time.Millisecond)
	}
	cancelRequests()
	wg.Wait()

	peak, sends := transport.peak()
	if peak != 1 {
		t.Fatalf("transport saw %d concurrent Sends, want 1: writes must stay serialized on the client's single slot", peak)
	}
	if sends != writers {
		t.Fatalf("transport saw %d sends, want %d", sends, writers)
	}
}
