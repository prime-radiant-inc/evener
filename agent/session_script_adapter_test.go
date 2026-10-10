package agent

import (
	"context"
	"sync"

	"primeradiant.com/evener/llm"
)

// sessionStep answers one model call. It runs with the adapter unlocked and
// sees the call's context, so it can block until the call is cancelled.
type sessionStep func(ctx context.Context, req llm.Request) (llm.Response, error)

// sessionScriptAdapter scripts each session's model calls on its own, keyed
// by the request's session id, so a child blocked in its call never holds up
// its parent's next one. fakeAdapter keeps one script shared by every session
// and holds its lock while a step runs; use this adapter when sessions must
// move independently. A session with no script left (or none at all) is
// answered by otherwise, or a plain "done" when that is nil.
type sessionScriptAdapter struct {
	name      string
	otherwise sessionStep

	mu      sync.Mutex
	scripts map[string][]sessionStep
}

func newSessionScriptAdapter(name string) *sessionScriptAdapter {
	return &sessionScriptAdapter{name: name, scripts: map[string][]sessionStep{}}
}

// script appends steps to sessionID's script.
func (a *sessionScriptAdapter) script(sessionID string, steps ...sessionStep) *sessionScriptAdapter {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scripts[sessionID] = append(a.scripts[sessionID], steps...)
	return a
}

func (a *sessionScriptAdapter) Name() string { return a.name }

func (a *sessionScriptAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *sessionScriptAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	step := a.otherwise
	if steps := a.scripts[req.SessionID]; len(steps) > 0 {
		step, a.scripts[req.SessionID] = steps[0], steps[1:]
	}
	a.mu.Unlock()
	resp := llm.Response{Message: llm.Assistant("done")}
	if step != nil {
		var err error
		if resp, err = step(ctx, req); err != nil {
			return llm.Response{}, err
		}
	}
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

// answerStep answers with resp, the way a fakeAdapter step does.
func answerStep(resp llm.Response) sessionStep {
	return func(context.Context, llm.Request) (llm.Response, error) { return resp, nil }
}

// awaitCancelStep holds the call until its context ends, the way a model
// call in flight ends when its run is stopped.
func awaitCancelStep(ctx context.Context, _ llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}
