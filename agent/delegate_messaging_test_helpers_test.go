package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// messagingAdapter scripts a root session and the delegates under it, each
// from its own step list, so sessions running at the same time never race
// for one script. The root is recognized by its session id. A delegate is
// recognized by the first marker one of its user messages carries (its brief
// names it) and keeps that script for the rest of its life. A step may block
// on a channel the test controls: steps run outside the adapter's lock. An
// unscripted request is answered with a plain final report.
type messagingAdapter struct {
	mu       sync.Mutex
	rootID   string
	markers  []string
	scripts  map[string][]func(llm.Request) llm.Response
	sessions map[string]string
	requests map[string][]llm.Request
	arrived  chan struct{}
}

func newMessagingAdapter(markers ...string) *messagingAdapter {
	return &messagingAdapter{
		markers:  markers,
		scripts:  make(map[string][]func(llm.Request) llm.Response),
		sessions: make(map[string]string),
		requests: make(map[string][]llm.Request),
		arrived:  make(chan struct{}, 1),
	}
}

func (*messagingAdapter) Name() string { return "openai" }

func (*messagingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

// script appends steps to a script: "root", or one of the markers.
func (a *messagingAdapter) script(key string, steps ...func(llm.Request) llm.Response) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scripts[key] = append(a.scripts[key], steps...)
}

func (a *messagingAdapter) requestsFor(key string) []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.requests[key])
}

// waitForRequests waits until key's script has answered at least n requests.
func (a *messagingAdapter) waitForRequests(t *testing.T, key string, n int) []llm.Request {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	deadline := time.After(30 * time.Second)
	for {
		if requests := a.requestsFor(key); len(requests) >= n {
			return requests
		}
		select {
		case <-a.arrived:
		case <-deadline:
			t.Fatalf("%s made %d requests, want %d", key, len(a.requestsFor(key)), n)
		}
	}
}

func (a *messagingAdapter) keyLocked(req llm.Request) string {
	if req.SessionID == a.rootID {
		return "root"
	}
	if key, ok := a.sessions[req.SessionID]; ok {
		return key
	}
	for _, msg := range req.Messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		for _, marker := range a.markers {
			if strings.Contains(msg.Text(), marker) {
				a.sessions[req.SessionID] = marker
				return marker
			}
		}
	}
	return ""
}

func (a *messagingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	key := a.keyLocked(req)
	a.requests[key] = append(a.requests[key], req)
	var step func(llm.Request) llm.Response
	if steps := a.scripts[key]; len(steps) > 0 {
		step, a.scripts[key] = steps[0], steps[1:]
	}
	a.mu.Unlock()
	select {
	case a.arrived <- struct{}{}:
	default:
	}
	response := finalResponse("done")
	if step != nil {
		response = step(req)
	}
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}

// newMessagingRoot starts a root session answered by adapter.
func newMessagingRoot(t *testing.T, adapter *messagingAdapter) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	adapter.mu.Lock()
	adapter.rootID = sess.ID()
	adapter.mu.Unlock()
	return sess
}

// delegateToolResponse is a model turn that starts one delegate with prompt.
func delegateToolResponse(id, prompt string) llm.Response {
	args, _ := json.Marshal(map[string]any{"prompt": prompt})
	return toolCallResponse(llm.ToolCallData{ID: id, Name: "delegate", Arguments: args, Type: "function"})
}

// writeToolDescriptions renders each tool's description and its parameters'
// descriptions, parameters sorted by name, for a prompt golden.
func writeToolDescriptions(golden *strings.Builder, defs ...llm.ToolDefinition) {
	for _, def := range defs {
		fmt.Fprintf(golden, "\n## %s\n\n%s\n\n", def.Name, def.Description)
		props, _ := def.Parameters["properties"].(map[string]any)
		for _, param := range slices.Sorted(maps.Keys(props)) {
			description, _ := props[param].(map[string]any)["description"].(string)
			fmt.Fprintf(golden, "- `%s`: %s\n", param, description)
		}
	}
}
