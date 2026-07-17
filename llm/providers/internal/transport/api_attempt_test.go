package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
)

type observingReadCloser struct {
	reader io.Reader

	mu         sync.Mutex
	readCount  int
	closeCount int
}

func (b *observingReadCloser) Read(p []byte) (int, error) {
	b.mu.Lock()
	b.readCount++
	b.mu.Unlock()
	return b.reader.Read(p)
}

func (b *observingReadCloser) Close() error {
	b.mu.Lock()
	b.closeCount++
	b.mu.Unlock()
	return nil
}

func (b *observingReadCloser) counts() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readCount, b.closeCount
}

type bodyObservationReporter interface {
	observedBytes() []byte
	observedExactly() bool
}

type terminalReadCloser struct {
	data []byte
	err  error
	done bool
}

func (r *terminalReadCloser) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), r.err
}

func (*terminalReadCloser) Close() error { return nil }

func TestAPIAttemptCompleteDoesNotDrainOrCloseResponseBody(t *testing.T) {
	body := &observingReadCloser{reader: bytes.NewReader([]byte("observed-unconsumed"))}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       body,
		}, nil
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_observation_only_complete")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	buf := make([]byte, len("observed-"))
	if _, err := io.ReadFull(response.Body, buf); err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	decodeErr := errors.New("adapter stopped decoding")
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode, Err: decodeErr}, llm.APITimeoutNone, decodeErr, nil)

	reads, closes := body.counts()
	if reads != 1 {
		t.Fatalf("underlying reads after completion = %d, want the adapter's one read", reads)
	}
	if closes != 0 {
		t.Fatalf("underlying closes after completion = %d, want none", closes)
	}
	reporter := response.Body.(bodyObservationReporter)
	if reporter.observedExactly() {
		t.Fatal("partial response became exact after decode failure")
	}
	if got := string(reporter.observedBytes()); got != "observed-" {
		t.Fatalf("observed response = %q, want only adapter bytes", got)
	}
}

func TestAPIAttemptCompleteDoesNotWaitForUnconsumedRequestBody(t *testing.T) {
	requestBody := &observingReadCloser{reader: bytes.NewReader([]byte("unconsumed request"))}
	var observedRequestBody interface {
		io.Closer
		bodyObservationReporter
	}
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		var ok bool
		observedRequestBody, ok = request.Body.(interface {
			io.Closer
			bodyObservationReporter
		})
		if !ok {
			t.Fatalf("request body %T does not report observations", request.Body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_unconsumed_request")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/v1", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}

	completed := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		_ = observedRequestBody.Close()
		<-completed
		t.Fatal("attempt completion blocked waiting for an unconsumed request body")
	}
	reads, closes := requestBody.counts()
	if reads != 0 || closes != 0 {
		t.Fatalf("completion touched request body: reads=%d closes=%d", reads, closes)
	}
	if observedRequestBody.observedExactly() {
		t.Fatal("unconsumed request body was marked exact")
	}
	if got := observedRequestBody.observedBytes(); len(got) != 0 {
		t.Fatalf("unconsumed request recorded %d bytes", len(got))
	}
}

func TestAPIAttemptBodyObserversReportOnlyReturnedBytesAndEOFExactness(t *testing.T) {
	tests := []struct {
		name      string
		body      func() io.ReadCloser
		read      func(io.ReadCloser) error
		want      string
		wantExact bool
	}{
		{
			name: "EOF is exact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				_, err := io.ReadAll(reader)
				return err
			},
			want:      "provider-body",
			wantExact: true,
		},
		{
			name: "partial read is inexact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				_, err := io.ReadFull(reader, make([]byte, len("provider")))
				return err
			},
			want: "provider",
		},
		{
			name: "early close is inexact",
			body: func() io.ReadCloser {
				return io.NopCloser(bytes.NewReader([]byte("provider-body")))
			},
			read: func(reader io.ReadCloser) error {
				if _, err := io.ReadFull(reader, make([]byte, len("provider"))); err != nil {
					return err
				}
				return reader.Close()
			},
			want: "provider",
		},
		{
			name: "cancellation is inexact",
			body: func() io.ReadCloser {
				return &terminalReadCloser{data: []byte("canceled"), err: context.Canceled}
			},
			read: func(reader io.ReadCloser) error {
				buf := make([]byte, len("canceled"))
				n, err := reader.Read(buf)
				if n != len(buf) {
					return errors.New("cancellation read returned wrong byte count")
				}
				if !errors.Is(err, context.Canceled) {
					return errors.New("cancellation read lost context error")
				}
				return nil
			},
			want: "canceled",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       testCase.body(),
				}, nil
			})}
			sink := &responseAssociationSink{}
			ctx := llm.WithAPIAttemptSink(
				llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_observer_exactness")),
				sink,
			)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, _, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
				return llm.APIAttemptMeta{ProviderInstance: "test", RequestModel: "test-model"}
			})
			if err != nil {
				t.Fatalf("DoWithAPIAttempts: %v", err)
			}
			if err := testCase.read(response.Body); err != nil {
				t.Fatalf("adapter read: %v", err)
			}
			reporter, ok := response.Body.(bodyObservationReporter)
			if !ok {
				t.Fatalf("response body %T does not report observation exactness", response.Body)
			}
			if got := string(reporter.observedBytes()); got != testCase.want {
				t.Fatalf("observed bytes = %q, want %q", got, testCase.want)
			}
			if got := reporter.observedExactly(); got != testCase.wantExact {
				t.Fatalf("observed exactness = %v, want %v", got, testCase.wantExact)
			}
		})
	}
}
