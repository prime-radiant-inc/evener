// Package fakellm serves a scriptable OpenAI chat-completions-compatible
// provider so a live stack — a real serf-hub, a real serf daemon child, a
// real session loop — can be driven one model round at a time with no
// provider credential, no network, and no wall-clock guessing about when a
// turn is "still busy". It is the deterministic half of the boundary
// AGENTS.md draws: serf plumbing (appwire RPC, daemon queues, session loops)
// gets a scripted provider; only model behaviour itself stays live.
//
// A test holds a turn open simply by not answering. Example (in
// example_test.go) is that sequence end to end; it is a compiled example
// rather than a snippet in this comment so it cannot drift away from the
// signatures it calls.
//
// Requests carrying no tools — the background session namer's structured
// GenerateObject call — are answered automatically and never surface on
// Next, so a test only ever sees the session's own loop.
package fakellm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ModelID is the model the fake advertises on /models. Point a providers.toml
// instance at BaseURL and spawn against "<instance>/" + ModelID.
const ModelID = "fake-test-model"

// autoNameJSON answers the session namer's structured-output call. The namer
// runs on a detached goroutine the moment a turn starts, so every live-stack
// test would otherwise have to know about it.
const autoNameJSON = `{"name":"Fake Session"}`

// Server is a running fake provider. The zero value is not usable; call New.
type Server struct {
	listener net.Listener
	http     *http.Server
	calls    chan *Call

	closeOnce sync.Once
	closed    chan struct{}
}

// Call is one model request from the session loop, paused until the test
// answers it. While a Call is outstanding the session's turn is in flight,
// which is exactly the window steer, queue, and interrupt act on.
type Call struct {
	// Body is the decoded chat-completions request.
	Body map[string]any

	once  sync.Once
	reply chan reply
}

type reply struct {
	text     string
	toolName string
	toolArgs map[string]any
}

// New starts a fake provider on a kernel-assigned loopback port.
func New() (*Server, error) { return NewOn("127.0.0.1:0") }

// NewOn starts a fake provider on an explicit host:port. Pass "127.0.0.1:0"
// to let the kernel assign the port and read it back from Addr.
func NewOn(addr string) (*Server, error) {
	var config net.ListenConfig
	listener, err := config.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fakellm: listen on %s: %w", addr, err)
	}
	s := &Server{
		listener: listener,
		calls:    make(chan *Call),
		closed:   make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChat)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fakellm: unexpected path "+r.URL.Path, http.StatusNotFound)
	})
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 30 * time.Second}
	go func() {
		// A held round keeps its HTTP handler parked, so Serve returns only
		// once Close shuts the listener down.
		_ = s.http.Serve(listener)
	}()
	return s, nil
}

// Addr is the fake's real listen address, host:port.
func (s *Server) Addr() string { return s.listener.Addr().String() }

// BaseURL is the value to put in a providers.toml instance's base_url.
func (s *Server) BaseURL() string { return "http://" + s.Addr() + "/v1" }

// Close releases every paused call and shuts the listener down.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.http.Close()
	})
}

// Next returns the next model request from the session loop, blocking until
// one arrives. done cancels the wait; a closed Server reports an error rather
// than blocking forever.
func (s *Server) Next(done <-chan struct{}) (*Call, error) {
	select {
	case call := <-s.calls:
		return call, nil
	case <-s.closed:
		return nil, errors.New("fakellm: server closed while waiting for a model request")
	case <-done:
		return nil, errors.New("fakellm: timed out waiting for a model request")
	}
}

// RespondText finishes the round with an assistant message and finish_reason
// "stop", which ends the turn.
func (c *Call) RespondText(text string) {
	c.respond(reply{text: text})
}

// RespondToolCall finishes the round with a single tool call, so the session
// runs the tool and comes back for another round — the seam where queued
// steering is injected (agent/session_tool_round.go).
func (c *Call) RespondToolCall(name string, args map[string]any) {
	c.respond(reply{toolName: name, toolArgs: args})
}

func (c *Call) respond(r reply) {
	c.once.Do(func() {
		c.reply <- r
		close(c.reply)
	})
}

// Texts returns every message's text content in wire order, as
// "<role>: <text>" lines. Tests assert on it to prove a steered or queued
// message actually reached the model request.
func (c *Call) Texts() []string {
	raw, _ := c.Body["messages"].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		out = append(out, role+": "+contentText(msg["content"]))
	}
	return out
}

// Contains reports whether any message in the request carries the substring.
func (c *Call) Contains(substr string) bool {
	for _, line := range c.Texts() {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

// contentText flattens both content shapes the adapter emits: a bare string,
// or an array of {"type":"text","text":...} parts.
func contentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			piece, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := piece["text"].(string); ok {
				b.WriteString(text)
			}
		}
		return b.String()
	default:
		return ""
	}
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":%q,"object":"model"}]}`, ModelID)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "fakellm: decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	stream, _ := body["stream"].(bool)

	// No tools means this is not the session loop: it is the background
	// namer's GenerateObject call. Answer it inline so it never steals a
	// round from the test's script.
	if tools, ok := body["tools"].([]any); !ok || len(tools) == 0 {
		writeReply(w, stream, reply{text: autoNameJSON})
		return
	}

	call := &Call{Body: body, reply: make(chan reply, 1)}
	select {
	case s.calls <- call:
	case <-r.Context().Done():
		return
	case <-s.closed:
		http.Error(w, "fakellm: server closed", http.StatusServiceUnavailable)
		return
	}

	select {
	case rep := <-call.reply:
		writeReply(w, stream, rep)
	case <-r.Context().Done():
		// The daemon gave up (interrupt cancels the in-flight model call).
	case <-s.closed:
	}
}

func writeReply(w http.ResponseWriter, stream bool, rep reply) {
	if stream {
		writeSSE(w, rep)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(nonStreamBody(rep))
}

func nonStreamBody(rep reply) []byte {
	message := map[string]any{"role": "assistant"}
	finish := "stop"
	if rep.toolName != "" {
		finish = "tool_calls"
		message["tool_calls"] = []any{toolCallJSON(rep)}
	} else {
		message["content"] = rep.text
	}
	payload := map[string]any{
		"id":      "fakellm-1",
		"model":   ModelID,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
	out, err := json.Marshal(payload)
	if err != nil {
		// The payload is built from plain maps of strings; marshalling it
		// cannot fail, but a silent empty body would be a mystery to debug.
		return []byte(`{"error":{"message":"fakellm: marshal response"}}`)
	}
	return out
}

func writeSSE(w http.ResponseWriter, rep reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	var delta map[string]any
	finish := "stop"
	if rep.toolName != "" {
		finish = "tool_calls"
		delta = map[string]any{"tool_calls": []any{toolCallJSON(rep)}}
	} else {
		delta = map[string]any{"role": "assistant", "content": rep.text}
	}
	chunks := []map[string]any{
		{"id": "fakellm-1", "model": ModelID, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}},
		{
			"id":      "fakellm-1",
			"model":   ModelID,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		},
	}
	for _, chunk := range chunks {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
		flush()
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flush()
}

func toolCallJSON(rep reply) map[string]any {
	args, err := json.Marshal(rep.toolArgs)
	if err != nil {
		args = []byte("{}")
	}
	return map[string]any{
		"index":    0,
		"id":       "call_fakellm_1",
		"type":     "function",
		"function": map[string]any{"name": rep.toolName, "arguments": string(args)},
	}
}
