package llm_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// wallTimeoutStreamBody is a scripted provider body that stays open until the
// request context is canceled or the adapter closes the response body. The
// provider adapter and llm.Client remain real; only the provider's timing is
// scripted at the HTTP boundary.
type wallTimeoutStreamBody struct {
	ctx         context.Context
	readStarted chan struct{}
	bodyClosed  chan struct{}
	readOnce    sync.Once
	closeOnce   sync.Once
}

func newWallTimeoutStreamBody(ctx context.Context) *wallTimeoutStreamBody {
	return &wallTimeoutStreamBody{
		ctx:         ctx,
		readStarted: make(chan struct{}),
		bodyClosed:  make(chan struct{}),
	}
}

func (b *wallTimeoutStreamBody) Read([]byte) (int, error) {
	b.readOnce.Do(func() { close(b.readStarted) })
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-b.bodyClosed:
		return 0, io.ErrClosedPipe
	}
}

func (b *wallTimeoutStreamBody) Close() error {
	b.closeOnce.Do(func() { close(b.bodyClosed) })
	return nil
}

func TestClientStream_WallCeilingCancelsBodyAndClassifiesRetryableTimeout(t *testing.T) {
	for _, provider := range wireProviders() {
		t.Run(provider.name, func(t *testing.T) {
			var body *wallTimeoutStreamBody
			client := provider.wireClient(t, "https://provider.test", &http.Client{
				Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
					body = newWallTimeoutStreamBody(request.Context())
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       body,
						Request:    request,
					}, nil
				}),
			}, nil)

			sink := &lockedAttemptSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_wall_ceiling_"+provider.name)),
				sink,
			)
			req := providerRequest(provider.name, "test-model")
			req.AdapterTimeout = &llm.AdapterTimeout{Request: 25 * time.Millisecond, StreamRead: time.Hour}
			stream, err := client.Stream(ctx, req)
			if err != nil {
				t.Fatalf("Client.Stream: %v", err)
			}
			t.Cleanup(func() { _ = stream.Close() })

			select {
			case <-body.readStarted:
			case <-time.After(time.Second):
				t.Fatal("scripted provider body was never read")
			}

			var streamErr error
			events := stream.Events()
			eventDeadline := time.NewTimer(time.Second)
			defer eventDeadline.Stop()
			for {
				select {
				case event, ok := <-events:
					if !ok {
						if streamErr == nil {
							t.Fatal("stream ended without a terminal error")
						}
						goto streamSettled
					}
					if event.Type == llm.StreamEventError {
						streamErr = event.Err
					}
				case <-eventDeadline.C:
					t.Fatal("stream did not emit a wall-ceiling error")
				}
			}

		streamSettled:
			if streamErr == nil {
				t.Fatal("stream ended without a terminal error")
			}
			var llmErr llm.Error
			if !errors.As(streamErr, &llmErr) {
				t.Fatalf("stream error = %T (%v), want llm.Error", streamErr, streamErr)
			}
			if got := llm.Kind(streamErr); got != llm.KindTimeout {
				t.Fatalf("stream error kind = %v, want timeout", got)
			}
			if !llmErr.Retryable() {
				t.Fatal("wall-ceiling timeout must remain retryable")
			}
			if !errors.Is(streamErr, context.DeadlineExceeded) {
				t.Fatalf("stream error = %v, want context deadline cause", streamErr)
			}

			select {
			case <-body.bodyClosed:
			case <-time.After(time.Second):
				t.Fatal("response body was not closed after wall-ceiling cancellation")
			}

			attempts := sink.snapshot()
			if len(attempts) != 1 {
				t.Fatalf("canonical attempts = %d, want one", len(attempts))
			}
			if got := attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
				t.Fatalf("canonical outcome = %q, want provider_timeout", got)
			}
		})
	}
}

func TestStreamGenerate_WallCeilingResetsForNextHTTPAttempt(t *testing.T) {
	provider := wireProviders()[0]
	var calls atomic.Int32
	var firstBody *wallTimeoutStreamBody
	client := provider.wireClient(t, "https://provider.test", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				firstBody = newWallTimeoutStreamBody(request.Context())
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       firstBody,
					Request:    request,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(provider.streamBody)),
				Request:    request,
			}, nil
		}),
	}, nil)

	prompt := "hello"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := llm.StreamGenerate(ctx, llm.GenerateOptions{
		Client:         client,
		Model:          "test-model",
		Provider:       provider.name,
		Prompt:         &prompt,
		AdapterTimeout: &llm.AdapterTimeout{Request: 25 * time.Millisecond, StreamRead: time.Hour},
		RetryPolicy: &llm.RetryPolicy{
			MaxRetries: 1,
			BaseDelay:  0,
			MaxDelay:   0,
			Jitter:     false,
		},
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("StreamGenerate: %v", err)
	}
	defer func() { _ = result.Close() }()
	for range result.Events() {
	}
	response, err := result.Response()
	if err != nil {
		t.Fatalf("StreamGenerate response: %v", err)
	}
	if response == nil || response.Text() != "hello" {
		t.Fatalf("response = %+v, want scripted success", response)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("HTTP attempts = %d, want one timed-out attempt plus one retry", got)
	}
	select {
	case <-firstBody.bodyClosed:
	case <-time.After(time.Second):
		t.Fatal("timed-out attempt body was not closed before retry completed")
	}
}
