package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Not parallel: the read-cut hook is package-wide. The provider is scripted,
// but admission, request construction, transcript writes and reads are real.
func TestThreadReadProvenanceCoversPostCutReply(t *testing.T) {
	const alpha, beta = "read-alpha-719", "read-beta-831"
	requests := 0
	model := &readFrontierProvider{complete: func(req llm.Request) llm.Response {
		requests++
		var input strings.Builder
		for _, message := range req.Messages {
			for _, part := range message.Content {
				if part.Kind == llm.ContentText {
					input.WriteString(part.Text)
				}
			}
		}
		if !strings.Contains(input.String(), alpha) || strings.Contains(input.String(), beta) != (requests == 2) {
			t.Fatal("actual provider request has incorrect Alpha/Beta coverage")
		}
		text := alpha
		if requests == 2 {
			text = beta
		}
		args, err := json.Marshal(map[string]any{"message": text, "end_turn": true, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
		if err != nil {
			t.Fatal(err)
		}
		call := llm.ToolCallData{ID: text, Name: "communicate", Arguments: args, Type: "function"}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
	}}
	client := llm.NewClient()
	client.Register(model)
	dir := t.TempDir()
	sess, err := agent.NewSession(client, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), agent.SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	if err := sess.Rename("read-frontier-fixture"); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(ServerConfig{})
	t.Cleanup(srv.Close)
	wireTranscriptHistory(t, sess, srv)
	srv.SetThreadEnvelopeSource(&readFrontierSource{sess: sess})
	process := func(text string) {
		t.Helper()
		if _, err := sess.ProcessInput(t.Context(), text, nil); err != nil {
			t.Fatal(err)
		}
		srv.RefreshThreadEnvelope()
	}
	process(alpha)
	cutReached := false
	threadReadAfterCutHook = func() {
		cutReached = true
		process(beta)
	}
	t.Cleanup(func() { threadReadAfterCutHook = nil })
	read, err := srv.handleAppThreadRead(t.Context(), appwire.ThreadReadParams{Ref: "local:" + sess.ID(), IncludeTurns: true, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if !cutReached || requests != 2 {
		t.Fatalf("post-cut request not exercised: cut=%t requests=%d", cutReached, requests)
	}
	var output *appwire.ConversationOutputProvenance
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			if item.Type == "agentMessage" && item.Text == beta {
				output = item.ConversationProvenance
			}
		}
	}
	if output == nil {
		t.Fatal("post-cut Beta canonical reply missing")
	}
	t.Logf("Beta consumed by actual request and committed after read cut: request=%s frontier=%s", output.RequestID, output.FrontierID)
	state := read.Thread.Evener.ConversationProvenance
	if state == nil || !state.Authoritative {
		t.Fatal("read lost authoritative provenance")
	}
	nodes := make(map[string]appwire.ConversationFrontier)
	for _, node := range state.Frontiers {
		nodes[node.ID] = node
	}
	seen := make(map[string]bool)
	for id := output.FrontierID; id != ""; {
		node, ok := nodes[id]
		if !ok || seen[id] {
			t.Fatalf("returned Beta reply has unresolvable actual frontier %q", id)
		}
		seen[id] = true
		id = node.BaseID
	}
}

type readFrontierProvider struct {
	complete func(llm.Request) llm.Response
}

func (*readFrontierProvider) Name() string { return "openai" }
func (p *readFrontierProvider) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	return p.complete(req), nil
}
func (*readFrontierProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

type readFrontierSource struct {
	stubThreadEnvelopeSource
	sess *agent.Session
}

func (s *readFrontierSource) ConversationProvenance() *appwire.ConversationProvenanceState {
	return s.sess.ConversationProvenance()
}
