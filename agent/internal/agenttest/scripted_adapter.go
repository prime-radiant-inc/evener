package agenttest

import (
	"context"
	"sync"

	"primeradiant.com/serf/llm"
)

// ScriptedAdapter is a reusable llm.ProviderAdapter whose every Complete call is
// answered by Responder. Unlike FakeAdapter (a fixed Steps slice), the Responder
// can react to the request and — in the fuzz harness — draw its reply from the
// rapid machine, so the response vocabulary (text, tool calls, communicate,
// empty, pause) is fuzzer-driven. It depends only on llm (no rapid, no agent),
// keeping agenttest dependency-light and reusable.
//
// Responder is called synchronously from the turn loop. The harness guarantees a
// single concurrent Complete caller per adapter (the namer is disabled and each
// subagent run gets its own adapter), so drawing inside Responder is race-free.
type ScriptedAdapter struct {
	Provider  string
	Responder func(req llm.Request) llm.Response

	mu       sync.Mutex
	requests []llm.Request
}

// Name reports the provider name the adapter answers to.
func (a *ScriptedAdapter) Name() string { return a.Provider }

// Complete records the request and returns Responder's reply, stamping the
// provider/model fields the core expects.
func (a *ScriptedAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.requests = append(a.requests, req)
	a.mu.Unlock()
	resp := a.Responder(req)
	resp.Provider = a.Provider
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

// Stream is unsupported: ScriptedAdapter exercises the non-streaming path.
func (a *ScriptedAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// Requests returns a copy of every request the adapter has seen.
func (a *ScriptedAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}
