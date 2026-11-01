package protocolhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// failingApply and failingPrepare are authenticators registered under their
// own scheme names so Prepare's error arms can be driven without a wire.
type failingApply struct{ err error }

func (failingApply) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return nil
}
func (failingApply) RequiresStreamingComplete() bool { return false }
func (f failingApply) Apply(context.Context, *http.Request, registry.Resolved) error {
	return f.err
}

type failingPrepare struct{}

func (failingPrepare) Apply(context.Context, *http.Request, registry.Resolved) error { return nil }
func (failingPrepare) PrepareRequest(context.Context, *http.Request, map[string]any, llm.Request, registry.Resolved) error {
	return errPrepareRefused
}
func (failingPrepare) RequiresStreamingComplete() bool { return false }

var errPrepareRefused = errors.New("preparer refused the request")

var (
	failingSchemeOnce  sync.Once
	failingApplyName   = "test-apply-fails"
	failingPrepareName = "test-prepare-fails"
)

func registerFailingSchemes() {
	failingSchemeOnce.Do(func() {
		llm.RegisterAuthenticator(failingApplyName, failingApply{err: errApplyRefused})
		llm.RegisterAuthenticator(failingPrepareName, failingPrepare{})
	})
}

var errApplyRefused = errors.New("authenticator refused the request")

// Prepare surfaces each assembly failure as its own error: an unparseable
// URL, an authenticator that refuses the row, a preparer that refuses the
// body, and a body that cannot be marshaled.
func TestPrepareSurfacesEachAssemblyFailure(t *testing.T) {
	registerFailingSchemes()
	for _, tc := range []struct {
		name    string
		call    *Call
		wantErr error
	}{
		{
			name:    "unparseable URL",
			call:    &Call{Method: http.MethodGet, URL: "http://example.test:bad-port/", Res: testRes("https://example.test", registry.AuthBearer)},
			wantErr: nil, // the url package's own error; asserted non-nil below
		},
		{
			name:    "authenticator refuses",
			call:    &Call{Method: http.MethodPost, URL: "https://example.test/x", Res: testRes("https://example.test", failingApplyName)},
			wantErr: errApplyRefused,
		},
		{
			name:    "preparer refuses the body",
			call:    &Call{Method: http.MethodPost, URL: "https://example.test/x", Body: map[string]any{"input": "hi"}, Res: testRes("https://example.test", failingPrepareName)},
			wantErr: errPrepareRefused,
		},
		{
			// The bearer scheme has no request preparer, so assembly reaches
			// the marshal with the unmarshalable value still in the body.
			name:    "body cannot be marshaled",
			call:    &Call{Method: http.MethodPost, URL: "https://example.test/x", Body: map[string]any{"bad": make(chan int)}, Res: testRes("https://example.test", registry.AuthBearer)},
			wantErr: nil, // the encoding/json error; asserted non-nil below
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Prepare(context.Background(), tc.call)
			if err == nil {
				t.Fatal("Prepare succeeded despite the scripted failure")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A 2xx exchange with no finisher is a legitimate bare call: Do must accept
// it without decoding, the shape listings rely on when they hand the body to
// the caller directly.
func TestDoWithoutFinishAcceptsA2xxBody(t *testing.T) {
	registerTestSchemes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"r1"}`))
	}))
	t.Cleanup(srv.Close)
	res := testRes(srv.URL, registry.AuthBearer)
	call := &Call{Operation: "things.get", Method: http.MethodGet, URL: srv.URL + "/things", Res: res, Client: srv.Client()}
	if err := Do(context.Background(), call, nil); err != nil {
		t.Fatalf("Do without a finisher = %v, want the bare exchange accepted", err)
	}
}

func streamOf(events ...llm.StreamEvent) func(context.Context) (llm.Stream, error) {
	return func(context.Context) (llm.Stream, error) {
		s := llm.NewChanStream(func() {})
		go func() {
			for _, ev := range events {
				s.Send(ev)
			}
			s.CloseSend()
		}()
		return s, nil
	}
}

// CompleteViaStream surfaces every way a stream can end badly: an in-band
// error event carries its own error verbatim, an error event with no error
// object still fails naming the transport, and a stream that closes without a
// final response never passes as a completion.
func TestCompleteViaStreamSurfacesEveryEnding(t *testing.T) {
	inBand := errors.New("decoded in-band failure")
	if _, err := CompleteViaStream(context.Background(), "inst", streamOf(llm.StreamEvent{Type: llm.StreamEventError, Err: inBand})); !errors.Is(err, inBand) {
		t.Fatalf("err = %v, want the in-band error verbatim", err)
	}
	if _, err := CompleteViaStream(context.Background(), "inst", streamOf(llm.StreamEvent{Type: llm.StreamEventError})); err == nil {
		t.Fatal("an error event with no error object must still fail the stream")
	}
	if _, err := CompleteViaStream(context.Background(), "inst", streamOf(llm.StreamEvent{Type: llm.StreamEventStreamStart})); err == nil {
		t.Fatal("a stream that ends without a final response must fail")
	}
}
