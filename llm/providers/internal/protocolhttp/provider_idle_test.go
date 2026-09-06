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

type idleRoundTripper func(*http.Request) (*http.Response, error)

func (f idleRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCompleteProviderIdleIsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registerTestSchemes()
		r, w := io.Pipe()
		defer r.Close()
		client := &http.Client{Transport: idleRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Body != nil {
				io.Copy(io.Discard, req.Body)
				req.Body.Close()
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r, Request: req}, nil
		})}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer w.Close()
			io.WriteString(w, `{"id":"r1"}`)
			time.Sleep(2 * time.Minute)
		}()
		res := testRes("https://example.invalid", registry.AuthBearer)
		sink := &captureSink{}
		ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("idle")), sink)
		call := &Call{Operation: "test.complete", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"input": "hi"}, Req: llm.Request{Model: "m", AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Minute}}, Res: res, Client: client}
		_, err := Complete(ctx, call, func(raw map[string]any) (llm.Response, error) { return llm.Response{ID: "r1"}, nil })
		if err == nil {
			t.Error("stalled response accepted as successful completion")
		}
		llm.WaitForPriorAPIAttempts(ctx)
		if len(sink.attempts) != 1 {
			t.Fatalf("attempts=%d", len(sink.attempts))
		}
		if sink.attempts[0].Outcome != apilog.AttemptProviderTimeout {
			t.Errorf("outcome=%s, want provider timeout", sink.attempts[0].Outcome)
		}
		<-done
	})
}

func TestStreamRejectedBodyPreservesIdleAndCallerCancellation(t *testing.T) {
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
			call := &Call{Operation: "test.stream", EndpointFamily: "test", Method: http.MethodPost, URL: URL(res, res.Transport.Endpoint), Body: map[string]any{"input": "hi"}, Req: llm.Request{Model: "m", AdapterTimeout: &llm.AdapterTimeout{StreamRead: time.Minute}}, Res: res, Client: client}
			_, err := Stream(ctx, call, nil)
			wantErr := llm.ErrResponseIdleTimeout
			wantOutcome := apilog.AttemptProviderTimeout
			if callerCancel {
				wantErr = context.Canceled
				wantOutcome = apilog.AttemptCallerCancel
			}
			if !errors.Is(err, wantErr) {
				t.Errorf("stream rejected-body error=%v, want %v", err, wantErr)
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
