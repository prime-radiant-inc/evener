package protocolhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/registry"
)

type truncatedListingBody struct{}

func (truncatedListingBody) Read(p []byte) (int, error) {
	return copy(p, `{"error":`), io.ErrUnexpectedEOF
}
func (truncatedListingBody) Close() error { return nil }

func TestModelsListTruncatedRejectedBodyPreservesStatus(t *testing.T) {
	registerTestSchemes()
	for _, tc := range []struct {
		name   string
		status int
		kind   llm.ErrorKind
	}{
		{"authentication", http.StatusUnauthorized, llm.KindAuthentication},
		{"rate_limit", http.StatusTooManyRequests, llm.KindRateLimit},
		{"server", http.StatusServiceUnavailable, llm.KindServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := testRes("https://example.invalid", registry.AuthBearer)
			client := &http.Client{Transport: idleRoundTripper(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: truncatedListingBody{}, Request: req}, nil
			})}
			call := &Call{Operation: "models.list", Method: http.MethodGet, URL: "https://example.invalid/models", Res: res, Client: client}
			err := Do(context.Background(), call, func(*Result) (*llm.Response, error) {
				t.Fatal("decoder called for rejected response")
				return nil, nil
			})
			var le llm.Error
			if !errors.As(err, &le) {
				t.Fatalf("error = %v, want typed HTTP error", err)
			}
			if le.StatusCode() != tc.status || le.Provider() != res.Instance || llm.Kind(err) != tc.kind {
				t.Fatalf("status=%d provider=%q kind=%v; want status=%d provider=%q kind=%v", le.StatusCode(), le.Provider(), llm.Kind(err), tc.status, res.Instance, tc.kind)
			}
		})
	}
}

func TestModelsListRejectedBodyPreservesIdleAndCallerCancellation(t *testing.T) {
	for _, callerCancel := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			registerTestSchemes()
			r, w := io.Pipe()
			defer r.Close()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &http.Client{Transport: idleRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.Body != nil {
					io.Copy(io.Discard, req.Body)
					req.Body.Close()
				}
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: r, Request: req}, nil
			})}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer w.Close()
				io.WriteString(w, `{"error":"busy"}`)
				time.Sleep(2 * time.Minute)
			}()
			if callerCancel {
				time.AfterFunc(30*time.Second, cancel)
			}
			res := testRes("https://example.invalid", registry.AuthBearer)
			sink := &captureSink{}
			ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(parent, llm.NewAPIAttemptGroup("idle-reject")), sink)
			call := &Call{Operation: "models.list", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"input": "hi"}, Req: llm.Request{Model: "m", AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Minute}}, Res: res, Client: client}
			err := Do(ctx, call, nil)
			wantErr := llm.ErrResponseIdleTimeout
			wantOutcome := apilog.AttemptProviderTimeout
			if callerCancel {
				wantErr = context.Canceled
				wantOutcome = apilog.AttemptCallerCancel
			}
			if !errors.Is(err, wantErr) {
				t.Errorf("listing rejected-body error=%v, want %v", err, wantErr)
			}
			llm.WaitForPriorAPIAttempts(ctx)
			if len(sink.attempts) != 1 {
				t.Fatalf("attempts=%d", len(sink.attempts))
			}
			if sink.attempts[0].Outcome != wantOutcome {
				t.Errorf("outcome=%s, want %s", sink.attempts[0].Outcome, wantOutcome)
			}
			<-done
		})
	}
}
