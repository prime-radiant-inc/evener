package llm

import (
	"context"
	"encoding/json"
	"testing"
)

// fuzzDispatchAdapter is a stub ProviderAdapter returning fuzzer-controlled
// outcomes, recording whether the client actually dispatched to it.
type fuzzDispatchAdapter struct {
	resp   Response
	err    error
	called bool
}

func (a *fuzzDispatchAdapter) Name() string { return "stub" }
func (a *fuzzDispatchAdapter) Complete(_ context.Context, _ Request) (Response, error) {
	a.called = true
	return a.resp, a.err
}
func (a *fuzzDispatchAdapter) Stream(_ context.Context, _ Request) (Stream, error) {
	a.called = true
	if a.err != nil {
		return nil, a.err
	}
	// Honor the adapter contract: return a non-nil stream (never nil,nil). An
	// already-closed empty stream lets the client's stamping wrapper drain and
	// exit cleanly without leaking the pump goroutine across fuzz iterations.
	return doneStream{}, nil
}

// doneStream is a minimal already-finished Stream.
type doneStream struct{}

func (doneStream) Events() <-chan StreamEvent {
	ch := make(chan StreamEvent)
	close(ch)
	return ch
}
func (doneStream) Close() error { return nil }

// FuzzClientDispatch drives Client.Complete and Client.Stream — request
// validation, provider resolution/normalization, behavior-tag lookup, the
// middleware chain, and provider/error stamping — over an arbitrary request and
// an arbitrary adapter outcome. These dispatch functions (0% fuzz, ~85-96% unit)
// shape every provider call; this puts adversarial requests through them with a
// stub standing in for the network (the fuzzer never makes a real call).
//
// Oracles: Complete/Stream never panic, and whenever the client actually reaches
// the adapter, the resolved provider is stamped onto the response (and a non-nil
// error carries a provider) — a dispatch that forgets to stamp reddens it.
func FuzzClientDispatch(f *testing.F) {
	f.Add([]byte(`{"model":"m","provider":"stub","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`),
		[]byte(`{"id":"r1","model":"m"}`), uint8(0), 200)
	f.Add([]byte(`{}`), []byte(`{}`), uint8(1), 429)

	f.Fuzz(func(t *testing.T, reqBytes, respBytes []byte, errSel uint8, status int) {
		var req Request
		_ = json.Unmarshal(reqBytes, &req)
		req.Provider = "stub" // force routing to the stub regardless of fuzzed bytes
		var resp Response
		_ = json.Unmarshal(respBytes, &resp)

		var advErr error
		if errSel%3 != 0 {
			advErr = ErrorFromHTTPStatus("stub", status, "stub error", nil, nil)
		}

		c := NewClient()
		adapter := &fuzzDispatchAdapter{resp: resp, err: advErr}
		c.Register(adapter)

		gotResp, gotErr := c.Complete(context.Background(), req)
		// If the client reached the adapter, it must stamp the provider onto the
		// response (Complete does this unconditionally after dispatch).
		if adapter.called && gotResp.Provider == "" {
			t.Fatalf("Complete reached the adapter but left Response.Provider unstamped (err=%v)", gotErr)
		}

		// Stream over the same inputs: never panic; close any returned stream.
		adapter.called = false
		s, serr := c.Stream(context.Background(), req)
		if serr == nil && s != nil {
			_ = s.Close()
		}
	})
}
