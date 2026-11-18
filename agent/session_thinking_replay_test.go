package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// TestExpandHistory_OpenAIChat_DropThinkingStripsPriorAndInFlight covers both
// halves of the replay: a completed prior turn and the in-flight turn's own
// earlier round. The in-flight exemption belongs to the N4 provenance rules;
// the chat drop is a target policy and applies to every replayed message.
func TestExpandHistory_OpenAIChat_DropThinkingStripsPriorAndInFlight(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{
		assistantThinkingTurn("work", "kimi-k2", "kimi-k2"),
		assistantThinkingTurn("work", "kimi-k2", "kimi-k2"),
	}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: 1, DropThinking: true, protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("chat target replayed assistant thinking; the wire carries no continuation state in it")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive the thinking drop")
	}
	if got := len(out); got != len(turns) {
		t.Fatalf("expanded messages = %d, want %d: the drop must not remove whole turns", got, len(turns))
	}
}

// A chat row can replay reasoning as a wire field or as ordinary content.
// Without either capability, it keeps only its opaque continuation state.
func TestDropsReplayedThinking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile *provider.Profile
		want    bool
	}{
		{"openai-chat", newOpenAICompatProfile("unknown-gateway", "model-a", 0), true},
		{"openai-chat raw reasoning", replayGateway("gw", registry.ProtocolOpenAIChat), false},
		{"openai-chat thinking_as_text", reasoningProfile("glm-5.3", registry.Caps{ThinkingAsText: new(true)}), false},
		{"anthropic", newAnthropicProfile("claude-opus-4-6"), false},
		{"openai-responses", NewOpenAIProfile("gpt-5.2"), false},
		{"google", newGeminiProfile("gemini-3-pro"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := dropsReplayedThinking(tc.profile); got != tc.want {
			t.Errorf("dropsReplayedThinking(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSession_ChatTargetRequestCarriesNoReplayedThinking drives one real turn
// and asserts the recorded provider request, not the expansion alone: a chat
// target's outgoing messages carry no ContentThinking, while the answer text
// stays.
func TestSession_ChatTargetRequestCarriesNoReplayedThinking(t *testing.T) {
	t.Parallel()
	row := replayGateway("gw", registry.ProtocolOpenAIChat).Resolved()
	row.Caps.ReplayReasoningText = new(false)
	adapter := newFakeAdapter(row.Instance, fixedStep(agenttest.ToolCallResponse(communicateCall("c1", "ok"))))
	sess := newSession(t, withProfile(provider.FromResolved(row, nil)), withAdapter(adapter))
	prior := llm.Response{Provider: row.Instance, Model: row.WireID, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "old chain of thought"}},
		{Kind: llm.ContentText, Text: "old answer"},
	}}}
	if err := sess.appendAssistantTurn(prior, ModelAttemptMetadata{Protocol: row.Protocol, RequestModel: row.ModelID}); err != nil {
		t.Fatal(err)
	}

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	req := reqs[len(reqs)-1]
	if hasContentKind(req.Messages, llm.ContentThinking) {
		t.Fatal("chat provider request carried replayed thinking")
	}
	if !hasContentKind(req.Messages, llm.ContentText) {
		t.Fatal("chat provider request lost the answer text")
	}
}

func replayGateway(instance, protocol string) *provider.Profile {
	return provider.FromResolved(registry.Resolved{
		Instance: instance, ModelID: "reasoner", WireID: "served-reasoner",
		Protocol: protocol, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{Reasoning: new(true), ReplayReasoningText: new(true)},
	}, nil)
}

func withReasoningMarker(resp llm.Response, protocol, marker string) llm.Response {
	thinking := &llm.ThinkingData{Text: marker}
	if protocol == registry.ProtocolOpenAIResponses {
		thinking.ID = "rs_" + marker
	} else {
		thinking.Signature = "reasoning_content"
	}
	resp.Message.Content = append([]llm.ContentPart{{
		Kind: llm.ContentThinking, Thinking: thinking,
	}}, resp.Message.Content...)
	return resp
}

func assertReasoningMarkers(t *testing.T, req llm.Request, want ...string) {
	t.Helper()
	var got []string
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentThinking && p.Thinking != nil && p.Thinking.Text != "" {
				got = append(got, p.Thinking.Text)
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s/%s reasoning = %q, want %q", req.Provider, req.Model, got, want)
	}
}

func TestSessionReplaysReasoningAcrossToolRoundsAndTurns(t *testing.T) {
	for _, profile := range []*provider.Profile{
		replayGateway("gw", registry.ProtocolOpenAIChat),
		replayGateway("work", registry.ProtocolOpenAIResponses),
	} {
		t.Run(profile.Protocol(), func(t *testing.T) {
			t.Parallel()
			adapter := newFakeAdapter(profile.ID(),
				fixedStep(withReasoningMarker(agenttest.ToolCallResponse(
					llm.ToolCallData{ID: "tool-a", Name: "missing_a", Arguments: []byte(`{}`)}), profile.Protocol(), "r17a")),
				fixedStep(withReasoningMarker(agenttest.ToolCallResponse(
					llm.ToolCallData{ID: "tool-b", Name: "missing_b", Arguments: []byte(`{}`)}), profile.Protocol(), "r28b")),
				fixedStep(withReasoningMarker(agenttest.FinalResponse("done"), profile.Protocol(), "r39c")),
				fixedStep(agenttest.FinalResponse("done")),
			)
			s := newSession(t, withProfile(profile), withAdapter(adapter))
			ctx := tripwireCtx(t)
			for _, input := range []string{"first", "follow-up"} {
				if _, err := s.ProcessInput(ctx, input, nil); err != nil {
					t.Fatal(err)
				}
			}
			requests := adapter.Requests()
			if len(requests) != 4 {
				t.Fatalf("got %d requests, want two tools, their continuation and a follow-up", len(requests))
			}
			assertReasoningMarkers(t, requests[1], "r17a")
			assertReasoningMarkers(t, requests[2], "r17a", "r28b")
			assertReasoningMarkers(t, requests[3], "r17a", "r28b", "r39c")
		})
	}
	t.Run("empty Responses answers", func(t *testing.T) {
		t.Run("enrolled", func(t *testing.T) { testResponsesEmptyAnswerReplay(t, true) })
		t.Run("strict", func(t *testing.T) { testResponsesEmptyAnswerReplay(t, false) })
	})
	t.Run("empty Chat answers", func(t *testing.T) {
		for _, mode := range []string{"scalar", "array", "strict", "disabled", "assistant text"} {
			t.Run(mode, func(t *testing.T) { testChatEmptyAnswerReplay(t, mode) })
		}
		t.Run("no Chat source", func(t *testing.T) {
			t.Parallel()
			profile := replayGateway("gw", registry.ProtocolOpenAIChat)
			reply := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "display-53"}},
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_a", Text: "display-16", EncryptedContent: "cipher-62"}},
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "t-24", Signature: "sig-43"}},
			}}}
			adapter := newFakeAdapter(profile.ID(), fixedStep(reply), fixedStep(agenttest.FinalResponse("recovered")))
			s := newSession(t, withProfile(profile), withAdapter(adapter))
			if answer, err := s.ProcessInput(tripwireCtx(t), "start", nil); err != nil || answer != "recovered" {
				t.Fatalf("retry returned %q with %v", answer, err)
			}
			requests := adapter.Requests()
			if len(requests) != 2 || slices.ContainsFunc(requests[1].Messages, func(m llm.Message) bool { return m.Role == llm.RoleAssistant }) {
				t.Fatal("an assistant turn without a Chat continuation entered the retry history")
			}
		})
	})
}

func testChatEmptyAnswerReplay(t *testing.T, mode string) {
	t.Parallel()
	const plain = " \tλ<&>19\r\n"
	field, value := "reasoning_text", any(plain)
	if mode == "array" {
		field, value = "reasoning_details", []any{
			map[string]any{"type": "reasoning.text", "id": "r-9", "index": 7, "format": "f9", "text": plain},
			map[string]any{"type": "reasoning.text", "index": 2, "text": " second-41 "},
		}
	}
	raw := map[string]any{field: value}
	sealed := []any{
		map[string]any{"type": "reasoning.encrypted", "id": "s-3", "data": "opaque-23"},
		map[string]any{"type": "reasoning.text", "index": 4, "signature": "signed-43"},
	}
	outputs := []map[string]any{raw, {"reasoning_details": sealed}, {}, raw}
	history := make(chan json.RawMessage, 8)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model    string
			Messages json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if req.Model == "unavailable" {
			http.NotFound(w, r)
			return
		}
		select {
		case history <- req.Messages:
		case <-r.Context().Done():
			return
		}
		n := int(calls.Add(1)) - 1
		delta, finish := map[string]any{}, "stop"
		if n < len(outputs) {
			delta = outputs[n]
		} else {
			final := agenttest.CommunicateCall("c-done", "recovered")
			delta["tool_calls"] = []any{map[string]any{"index": 0, "id": final.ID, "type": "function", "function": map[string]any{"name": final.Name, "arguments": string(final.Arguments)}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, nil, "message", map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	enrolled := mode == "scalar" || mode == "array"
	caps := registry.Caps{Reasoning: new(mode != "disabled"), ReplayReasoningText: new(enrolled), ThinkingAsText: new(mode == "assistant text")}
	if mode == "array" {
		caps.ReasoningField = new(field)
	}
	instance := registry.Provider{Base: "openai-compatible", APIKey: "test", Transport: registry.Transport{BaseURL: srv.URL}, Models: map[string]registry.Model{
		"answering":   {Caps: caps},
		"unavailable": {Caps: registry.Caps{ReplayReasoningText: new(!enrolled)}},
		"other":       {Caps: registry.Caps{ReplayReasoningText: new(true)}},
	}}
	client := registryClientAt(t, dir, map[string]registry.Provider{"gw": instance}, []string{"gw"})
	profile := resolveClientProfile(t, client, "gw/unavailable")
	fallbacks := []string{"answering"}
	tc := testConfig{minimalSystemPrompt: true, skipGitSnapshot: true}
	s := newSession(t, withClient(client), withProfile(profile), withConfig(SessionConfig{StateDir: dir, ModelFallbacks: fallbacks, testOnly: tc}))
	if answer, err := s.ProcessInput(tripwireCtx(t), "start", nil); answer != "" || !errors.Is(err, errEmptyResponseExhausted) || int(calls.Load()) != len(outputs) {
		t.Fatalf("empty answer recovery returned %q after %d attempts with %v", answer, calls.Load(), err)
	}
	s.Close()
	cold, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(s.currentEnv().WorkingDirectory()), loadMetaForTest(t, s), RestoreSessionConfig{
		StateDir: dir, ModelFallbacks: fallbacks, testOnly: sharingHostProbe(tc),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cold.Close)
	if answer, err := cold.ProcessInput(tripwireCtx(t), "resume", nil); err != nil || answer != "recovered" || int(calls.Load()) != len(outputs)+1 {
		t.Fatalf("cold retry returned %q after %d attempts with %v", answer, calls.Load(), err)
	}
	var prior json.RawMessage
	var want []string
	for n := 0; n <= len(outputs); n++ {
		wire := <-history
		if n > 0 && !bytes.HasPrefix(wire, append(bytes.Clone(bytes.TrimSuffix(prior, []byte("]"))), ',')) {
			t.Errorf("request %d rewrote the previously sent Chat messages", n+1)
		}
		prior = wire
		var items []json.RawMessage
		if err := json.Unmarshal(wire, &items); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range items {
			var m struct{ Role string }
			if err := json.Unmarshal(item, &m); err != nil {
				t.Fatal(err)
			}
			if m.Role == "assistant" {
				got = append(got, string(item))
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("request %d assistant messages = %v, want %v", n+1, got, want)
		}
		msg := map[string]any{"role": "assistant", "content": ""}
		switch {
		case n == 1 && mode != "disabled":
			msg["reasoning_details"] = sealed
		case (n == 0 || n == 3) && enrolled:
			msg[field] = value
		case (n == 0 || n == 3) && mode == "assistant text":
			msg["content"] = plain
		default:
			continue
		}
		want = append(want, mustJSON(t, msg))
	}
	if mode == "array" {
		if err := cold.SetModel("gw/other"); err != nil {
			t.Fatal(err)
		}
		if _, err := cold.ProcessInput(tripwireCtx(t), "different issuer", nil); err != nil || int(calls.Load()) != len(outputs)+2 {
			t.Fatalf("switched model ended after %d total requests with %v", calls.Load(), err)
		}
		if wire := <-history; bytes.Contains(wire, []byte("second-41")) || !bytes.Contains(wire, []byte("opaque-23")) {
			t.Error("the other Chat model received foreign plaintext or lost the Chat continuation")
		}
	}
}

func testResponsesEmptyAnswerReplay(t *testing.T, enrolled bool) {
	t.Helper()
	t.Parallel()
	part := func(kind, text string) map[string]any { return map[string]any{"type": kind, "text": text} }
	outputs := []struct {
		item   map[string]any
		replay bool
	}{
		{map[string]any{"type": "reasoning", "id": "rs_plain", "summary": []any{}, "content": []any{part("reasoning_text", "λ-425"), part("reasoning_text", "")}}, enrolled},
		{map[string]any{"type": "message", "role": "assistant", "content": []any{part("output_text", "")}}, enrolled},
		{map[string]any{"type": "reasoning", "id": "rs_opaque", "summary": []any{}, "encrypted_content": "sealed-419"}, true},
		{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "refusal", "refusal": ""}}}, true},
		{map[string]any{"type": "reasoning", "id": "rs_empty", "summary": []any{}, "content": []any{part("reasoning_text", "")}}, enrolled},
		{map[string]any{"type": "reasoning", "id": "rs_display", "content": []any{part("text", "display-34")}}, false},
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string
			Input []map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Model == "unavailable" {
			http.NotFound(w, r)
			return
		}
		attempt := int(calls.Add(1)) - 1
		var got, want []map[string]any
		for _, item := range req.Input {
			if item["type"] == "reasoning" || item["role"] == "assistant" {
				got = append(got, item)
			}
		}
		for _, prior := range outputs[:min(attempt, len(outputs))] {
			if prior.replay {
				want = append(want, prior.item)
			}
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, want); gotJSON != wantJSON {
			t.Errorf("request %d replayed %s, want %s", attempt+1, gotJSON, wantJSON)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush, _ := w.(http.Flusher)
		if attempt < len(outputs) {
			writeSSE(t, w, flush, "response.completed", map[string]any{
				"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{outputs[attempt].item}},
			})
			return
		}
		final := agenttest.CommunicateCall("call-done", "recovered")
		writeResponsesFunctionCall(t, w, flush, "resp-done", final.ID, final.Name, string(final.Arguments))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	inst := openaiInstance(srv.URL)
	// The answering row has the opposite replay policy to the primary.
	inst.Models = map[string]registry.Model{
		"unavailable": {Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{ReplayReasoningText: new(!enrolled)}},
		"reasoner":    {Protocol: registry.ProtocolOpenAIResponses, Caps: registry.Caps{ReplayReasoningText: new(enrolled)}},
	}
	client := registryClientAt(t, dir, map[string]registry.Provider{"work": inst}, []string{"work"})
	profile := resolveClientProfile(t, client, "work/unavailable")
	fallbacks := []string{"reasoner"}
	s := newSession(t, withClient(client), withProfile(profile), withConfig(SessionConfig{StateDir: dir, OpenAIResponsesContinuation: "off", ModelFallbacks: fallbacks}))
	const beforeResume = 4 // initial answer and the three empty-answer retries
	if _, err := s.ProcessInput(tripwireCtx(t), "begin", nil); !errors.Is(err, errEmptyResponseExhausted) || int(calls.Load()) != beforeResume {
		t.Fatalf("empty-answer recovery ended after %d attempts with %v", calls.Load(), err)
	}
	s.Close()
	resumed, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(s.currentEnv().WorkingDirectory()), loadMetaForTest(t, s), RestoreSessionConfig{
		StateDir: dir, ModelFallbacks: fallbacks, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	checkStored := func(replies, finalAnswers int) {
		t.Helper()
		want := finalAnswers
		for _, output := range outputs[:replies] {
			if output.replay {
				want++
			}
		}
		got := 0
		for _, turn := range currentHistory(t, resumed) {
			if turn.Kind == schema.TurnAssistant {
				got++
			}
		}
		if got != want {
			t.Errorf("after %d empty replies and %d answers the session retained %d assistant turns, want %d", replies, finalAnswers, got, want)
		}
	}
	checkStored(beforeResume, 0)
	if got, err := resumed.ProcessInput(tripwireCtx(t), "resume", nil); err != nil || got != "recovered" || int(calls.Load()) != len(outputs)+1 {
		t.Fatalf("resumed input returned %q after %d attempts with %v", got, calls.Load(), err)
	}
	checkStored(len(outputs), 1)
}

func TestFallbackReplaysOnlyItsIssuersReasoning(t *testing.T) {
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) { testFallbackReasoningIssuer(t, protocol) })
	}
}

func testFallbackReasoningIssuer(t *testing.T, protocol string) {
	t.Parallel()
	primary := replayGateway("work", registry.ProtocolOpenAIResponses)
	fallback := replayGateway("gw", protocol)
	const ciphertext = "ciphertext-485"
	primaryAnswered := false
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider: primary.ID(),
		FaultResponder: func(req llm.Request) error {
			if primaryAnswered {
				return llm.ErrorFromHTTPStatus(req.Provider, 404, "missing", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			primaryAnswered = true
			resp := withReasoningMarker(agenttest.ToolCallResponse(
				llm.ToolCallData{ID: "tool-a", Name: "missing_a", Arguments: []byte(`{}`)}), primary.Protocol(), "primary-42")
			resp.Message.Content = append(resp.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_sealed", EncryptedContent: ciphertext}})
			return resp
		},
	}
	fallbackMarker := "fallback-73"
	firstFallback := agenttest.ToolCallResponse(llm.ToolCallData{ID: "tool-b", Name: "missing_b", Arguments: []byte(`{}`)})
	const mixedID, emptyID, sealed = "rs_mixed", "rs_empty", "ciphertext-f73"
	const legacyID, legacyCipher, legacyDisplay = "rs_legacy", "ciphertext-f91", "display-f91"
	if protocol == registry.ProtocolOpenAIResponses {
		fallbackMarker = " \tλ<&>\r\n"
		issued, err := responses.ExtractRecordedResponse([]byte(`{"output":[
			{"type":"reasoning","id":"`+mixedID+`","encrypted_content":"`+sealed+`","summary":[{"type":"summary_text","text":"s73"}],"content":[{"type":"reasoning_text","text":`+mustJSON(t, fallbackMarker)+`}]},
			{"type":"reasoning","id":"`+emptyID+`","content":[{"type":"reasoning_text","text":""}]}
		]}`), fallback.Model())
		if err != nil {
			t.Fatal(err)
		}
		issued.Message.Content = append(issued.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
			ID: legacyID, EncryptedContent: legacyCipher, Text: legacyDisplay,
		}})
		firstFallback.Message.Content = append(issued.Message.Content, firstFallback.Message.Content...)
	} else {
		firstFallback = withReasoningMarker(firstFallback, protocol, fallbackMarker)
	}
	fallbackAdapter := newFakeAdapter(fallback.ID(),
		fixedStep(firstFallback),
		fixedStep(withReasoningMarker(agenttest.FinalResponse("done"), fallback.Protocol(), "fallback-81")),
		fixedStep(agenttest.FinalResponse("done")),
		fixedStep(agenttest.FinalResponse("cold reply")),
		fixedStep(agenttest.FinalResponse("wire-model reply")),
		fixedStep(agenttest.FinalResponse("deployment reply")),
		fixedStep(agenttest.FinalResponse("body-model reply")),
		fixedStep(agenttest.FinalResponse("stream reply")),
		fixedStep(agenttest.FinalResponse("strict route reply")),
		fixedStep(agenttest.FinalResponse("same-model reply")),
	)
	// The profile used to assemble each request resolves on the same registry
	// as the client dispatching it. A restart can redirect the selected model
	// through its wire ID, request URL, or a body.model constant.
	clientFor := func(wireSuffix, deployment, bodySuffix, stream string, replay bool) *llm.Client {
		instances := testRegistryInstances()
		for _, profile := range []*provider.Profile{primary, fallback} {
			inst := instances[profile.ID()]
			inst.Surface = registry.SurfaceOpenAI
			transport := &registry.Transport{
				BaseURL:        "https://" + profile.ID() + ".example.test/v1",
				Endpoint:       "/generate?deployment=" + deployment + "&token=token-75c",
				StreamEndpoint: "/stream?deployment=" + stream,
			}
			if bodySuffix != "" {
				transport.Body = map[string]any{"model": profile.ID() + bodySuffix}
			}
			caps := profile.Resolved().Caps
			caps.ReplayReasoningText = new(replay)
			inst.Models = map[string]registry.Model{"reasoner": {
				WireID: profile.ID() + wireSuffix, Protocol: profile.Protocol(), Caps: caps, Transport: transport,
			}}
			instances[profile.ID()] = inst
		}
		return registryClient(t, instances, primaryAdapter, fallbackAdapter)
	}
	client := clientFor("-declared", "deployment-1", "-wire-1", "s1", true)
	checkIssuedItems := func(req llm.Request, plain, encrypted bool) []byte {
		t.Helper()
		if protocol != registry.ProtocolOpenAIResponses {
			return nil
		}
		row := resolveClientProfile(t, client, req.Provider+"/"+req.Model).Resolved()
		body, err := responses.DefaultProtocol.BuildBody(llm.ShapeRequest(req, row), row)
		if err != nil {
			t.Fatal(err)
		}
		var got, want []any
		for _, value := range body["input"].([]any) {
			item := value.(map[string]any)
			if item["id"] == mixedID || item["id"] == emptyID || item["id"] == legacyID {
				got = append(got, item)
			}
		}
		if encrypted {
			want = append(want, map[string]any{"type": "reasoning", "id": mixedID, "summary": []any{map[string]any{"type": "summary_text", "text": "s73"}}, "encrypted_content": sealed})
		}
		if plain {
			want = append(want, map[string]any{"type": "reasoning", "id": emptyID, "summary": []any{}, "content": []any{map[string]any{"type": "reasoning_text", "text": ""}}})
		}
		if encrypted {
			want = append(want, map[string]any{"type": "reasoning", "id": legacyID, "summary": []any{}, "encrypted_content": legacyCipher})
		}
		if eq, a, b := jsonEqual(t, got, want); !eq {
			t.Errorf("%s native reasoning items = %s, want %s", req.Provider, a, b)
		}
		if req.Provider == fallback.ID() {
			if estimate := llm.EstimateInputTokensForResolved(row, req).Tokens; req.FullHistoryInputTokensEstimate != estimate {
				t.Errorf("fallback estimate = %d, projected request costs %d", req.FullHistoryInputTokensEstimate, estimate)
			}
		}
		input, err := json.Marshal(body["input"])
		if err != nil {
			t.Fatal(err)
		}
		return input
	}
	assertPrefix := func(before, after []byte) {
		t.Helper()
		if len(before) > 0 && !bytes.HasPrefix(after, append(bytes.Clone(bytes.TrimSuffix(before, []byte("]"))), ',')) {
			t.Error("the next Responses fallback request rewrote its previously sent input")
		}
	}
	resolve := func(ref string) (*provider.Profile, error) { return provider.Resolve(client.Registry(), ref) }
	stateDir := t.TempDir()
	s := newSession(t, withProfile(resolveClientProfile(t, client, "work/reasoner")), withClient(client), withStateDir(stateDir),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gw/reasoner"},
			ResolveProfile: resolve,
		}))
	ctx := tripwireCtx(t)
	for _, input := range []string{"first", "follow-up"} {
		if _, err := s.ProcessInput(ctx, input, nil); err != nil {
			t.Fatal(err)
		}
	}
	primaryRequests, fallbackRequests := primaryAdapter.Requests(), fallbackAdapter.Requests()
	if len(primaryRequests) != 4 || len(fallbackRequests) != 3 {
		t.Fatalf("requests = %d primary, %d fallback; want 4 and 3", len(primaryRequests), len(fallbackRequests))
	}
	for _, req := range primaryRequests[1:] {
		assertReasoningMarkers(t, req, "primary-42")
		checkIssuedItems(req, false, false)
	}
	assertReasoningMarkers(t, fallbackRequests[0])
	inFlight := []string{fallbackMarker}
	if protocol == registry.ProtocolOpenAIResponses {
		inFlight = append(inFlight, legacyDisplay)
	}
	assertReasoningMarkers(t, fallbackRequests[1], inFlight...)
	toolInput := checkIssuedItems(fallbackRequests[1], true, true)
	if protocol == registry.ProtocolOpenAIChat {
		assertReasoningMarkers(t, fallbackRequests[2], fallbackMarker, "fallback-81")
	}
	followupInput := checkIssuedItems(fallbackRequests[2], true, true)
	assertPrefix(toolInput, followupInput)

	meta, env := s.Meta(), s.currentEnv()
	s.Close()
	for i, redirect := range []struct {
		wire, deployment, body, stream  string
		enrolled, wantPlain, wantCipher bool
	}{
		{"-declared", "deployment-1", "-wire-1", "s1", true, true, true},
		{"-wire-2", "deployment-1", "", "s1", true, false, true},
		{"-declared", "deployment-2", "-wire-1", "s1", true, false, false},
		{"-declared", "deployment-1", "-wire-3", "s1", true, false, true},
		{"-declared", "deployment-1", "-wire-1", "s2", true, false, false},
		{"-declared", "deployment-1", "-wire-1", "s2", false, false, false},
		{"-wire-1", "deployment-1", "", "s1", true, true, true},
	} {
		client = clientFor(redirect.wire, redirect.deployment, redirect.body, redirect.stream, redirect.enrolled)
		resumed, err := RestoreSessionFromMetaWithConfig(client, resolveClientProfile(t, client, "work/reasoner"), env, meta, RestoreSessionConfig{
			StateDir: stateDir, ResolveProfile: resolve, ModelFallbacks: []string{"gw/reasoner"},
			testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := resumed.ProcessInput(ctx, "after-redirect", nil); err != nil {
			t.Fatal(err)
		}
		meta = resumed.Meta()
		resumed.Close()
		primaryRequests, fallbackRequests = primaryAdapter.Requests(), fallbackAdapter.Requests()
		if len(primaryRequests) != 5+i || len(fallbackRequests) != 4+i {
			t.Fatalf("redirect %+v made %d primary and %d fallback requests", redirect, len(primaryRequests), len(fallbackRequests))
		}
		for _, req := range []llm.Request{primaryRequests[4+i], fallbackRequests[3+i]} {
			isFallback := req.Provider == fallback.ID()
			input := checkIssuedItems(req, isFallback && redirect.wantPlain, isFallback && redirect.wantCipher)
			if i == 0 && isFallback {
				assertPrefix(followupInput, input)
			}
			switch {
			case isFallback && protocol == registry.ProtocolOpenAIResponses:
				// The input encoder selects ciphertext over its accompanying display.
			case !redirect.wantPlain:
				assertReasoningMarkers(t, req)
			case req.Provider == primary.ID():
				assertReasoningMarkers(t, req, "primary-42")
			default:
				assertReasoningMarkers(t, req, fallbackMarker, "fallback-81")
			}
			if !requestCarriesText(req, "follow-up") {
				t.Errorf("%s lost conversation history across the redirect", req.Provider)
			}
			if req.Provider == primary.ID() {
				row := resolveClientProfile(t, client, req.Provider+"/"+req.Model).Resolved()
				body, err := responses.DefaultProtocol.BuildBody(llm.ShapeRequest(req, row), row)
				if err != nil {
					t.Fatal(err)
				}
				items := body["input"].([]any)
				hasCipher := slices.ContainsFunc(items, func(item any) bool { return item.(map[string]any)["encrypted_content"] == ciphertext })
				if hasCipher != redirect.wantCipher {
					t.Errorf("redirect %+v sent ciphertext = %t, want %t", redirect, hasCipher, redirect.wantCipher)
				}
			}
		}
	}
}

func TestSessionScopesReasoningTextToTheIssuingModel(t *testing.T) {
	for _, profile := range []*provider.Profile{
		replayGateway("gw", registry.ProtocolOpenAIChat),
		replayGateway("gw", registry.ProtocolOpenAIResponses),
	} {
		t.Run(profile.Protocol(), func(t *testing.T) {
			t.Parallel()
			adapter := &agenttest.ScriptedAdapter{
				Provider:  profile.ID(),
				Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
			}
			// The instance speaks chat by default. Earlier records can omit the
			// protocol stamp for a model that overrides it to Responses.
			instance := testRegistryInstances()["gw"]
			instance.Models = map[string]registry.Model{
				"reasoner":  {Protocol: profile.Protocol(), WireID: "served-reasoner", Caps: profile.Resolved().Caps},
				"different": {Protocol: profile.Protocol(), Caps: profile.Resolved().Caps},
			}
			client := registryClient(t, map[string]registry.Provider{"gw": instance}, adapter)
			s := newSession(t, withProfile(resolveClientProfile(t, client, "gw/reasoner")), withClient(client))
			for _, turn := range []struct {
				provider, requested, reported, protocol, marker string
			}{
				{profile.ID(), "reasoner", "snapshot", profile.Protocol(), "same-3a"},
				{profile.ID(), "reasoner", "snapshot", "", "same-4b"},
				{profile.ID(), "", "served-reasoner", profile.Protocol(), "same-5c"},
				{profile.ID(), "different", "served-reasoner", profile.Protocol(), "model-6d"},
				{profile.ID(), "reasoner", "snapshot", registry.ProtocolAnthropic, "protocol-7e"},
				{"different", "reasoner", "snapshot", profile.Protocol(), "instance-8f"},
				{"", "", "", "", "unknown-9a"},
			} {
				resp := withReasoningMarker(llm.Response{Message: llm.Assistant("answer")}, profile.Protocol(), turn.marker)
				resp.Provider, resp.Model = turn.provider, turn.reported
				if err := s.appendAssistantTurn(resp, ModelAttemptMetadata{
					Protocol: turn.protocol, RequestModel: turn.requested,
				}); err != nil {
					t.Fatal(err)
				}
			}
			otherProtocol := registry.ProtocolOpenAIChat
			if profile.Protocol() == registry.ProtocolOpenAIChat {
				otherProtocol = registry.ProtocolOpenAIResponses
			}
			foreign := withReasoningMarker(llm.Response{Provider: profile.ID(), Model: "served-reasoner", Message: llm.Assistant("answer")}, otherProtocol, "old-route-19")
			if err := s.appendAssistantTurn(foreign, ModelAttemptMetadata{RequestModel: "reasoner"}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProcessInput(tripwireCtx(t), "continue", nil); err != nil {
				t.Fatal(err)
			}
			requests := adapter.Requests()
			if len(requests) != 1 {
				t.Fatalf("got %d requests, want one follow-up", len(requests))
			}
			assertReasoningMarkers(t, requests[0], "same-3a", "same-4b", "same-5c")
			if err := s.SetModel("different"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProcessInput(tripwireCtx(t), "follow-up", nil); err != nil {
				t.Fatal(err)
			}
			requests = adapter.Requests()
			if len(requests) != 2 || requests[1].Model != "different" {
				t.Fatalf("expected a follow-up on the selected model, got %d requests", len(requests))
			}
			assertReasoningMarkers(t, requests[1], "model-6d")
		})
	}
}

func TestChatThinkingAsTextAcrossModels(t *testing.T) {
	t.Parallel()
	t.Run("strict Chat rows", func(t *testing.T) { testChatToolContinuation(t, chatContinuationCase{asText: true}) })
	t.Run("enrolled Chat rows", func(t *testing.T) { testChatToolContinuation(t, chatContinuationCase{raw: true, asText: true}) })
	t.Run("scalar mirrors a signed detail", func(t *testing.T) {
		testChatToolContinuation(t, chatContinuationCase{raw: true, asText: true, mirrorSigned: true})
	})
	t.Run("fallbacks on one endpoint select their own presentation", func(t *testing.T) {
		adapter := &agenttest.ModelTrackingAdapter{Provider: "gw", Respond: func(req llm.Request) (llm.Response, error) {
			if req.Model == "last" {
				return agenttest.FinalResponse("done"), nil
			}
			return llm.Response{}, llm.ErrorFromHTTPStatus(req.Provider, 404, "", nil, nil)
		}}
		instance := registry.Provider{Base: "openai-compatible", Surface: registry.SurfaceOpenAI, Transport: registry.Transport{BaseURL: "https://chat.example.invalid"},
			Models: map[string]registry.Model{
				"source":  {Caps: registry.Caps{ThinkingAsText: new(true)}, Transport: &registry.Transport{StreamEndpoint: "/original"}},
				"primary": {}, "strict": {}, "as-text": {Caps: registry.Caps{ThinkingAsText: new(true)}}, "last": {},
			},
		}
		client := registryClient(t, map[string]registry.Provider{"gw": instance}, adapter)
		source := resolveClientProfile(t, client, "gw/source")
		primary := resolveClientProfile(t, client, "gw/primary")
		s := newSession(t, withClient(client), withProfile(primary), withConfig(SessionConfig{ModelFallbacks: []string{"strict", "as-text", "last"}}))
		reply := []byte(`{"choices":[{"message":{"content":"answer-64","reasoning":"scalar-29","reasoning_details":[{"type":"reasoning.encrypted","id":"r-71","data":"cipher-61","text":"opaque-extension-84"},{"type":"reasoning.text","text":"display-53"}]}}]}`)
		prior, err := chatcompletions.ExtractRecordedResponse(reply, source.Model())
		if err != nil {
			t.Fatal(err)
		}
		prior.Provider = source.ID()
		if err := s.appendAssistantTurn(prior, ModelAttemptMetadata{Protocol: source.Protocol(), RequestModel: source.Model(), ReasoningRoute: source.ReasoningReplayRoute()}); err != nil {
			t.Fatal(err)
		}
		sendOneUserInput(t, s, "followup")
		if len(adapter.Requests()) != 4 {
			t.Fatalf("got %d requests, want the primary and all three fallbacks", len(adapter.Requests()))
		}
		for _, req := range adapter.Requests() {
			row := resolveClientProfile(t, client, "gw/"+req.Model).Resolved()
			body, err := chatcompletions.DefaultProtocol.BuildBody(req, row)
			if err != nil {
				t.Fatal(err)
			}
			want := "answer-64"
			if req.Model == "as-text" {
				want = "display-53\n\n" + want
			}
			messages := body["messages"].([]map[string]any)
			i := slices.IndexFunc(messages, func(m map[string]any) bool { return m["role"] == "assistant" })
			if i == -1 || messages[i]["content"] != want || messages[i]["reasoning_content"] != nil || messages[i]["reasoning_details"] != nil {
				t.Errorf("%s omitted or changed the assistant's selected representation", req.Model)
			}
		}
	})
	row := replayGateway("gw", registry.ProtocolOpenAIChat).Resolved()
	row.Caps.ThinkingAsText = new(true)
	profile := provider.FromResolved(row, nil)
	adapter := newFakeAdapter(profile.ID(), fixedStep(agenttest.FinalResponse("done")))
	s := newSession(t, withProfile(profile), withAdapter(adapter))
	prior := withReasoningMarker(llm.Response{Provider: row.Instance, Model: "other", Message: llm.Assistant("answer-4e")}, row.Protocol, "trace-7b")
	if err := s.appendAssistantTurn(prior, ModelAttemptMetadata{Protocol: row.Protocol, RequestModel: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(tripwireCtx(t), "continue", nil); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want one", len(requests))
	}
	body, err := chatcompletions.DefaultProtocol.BuildBody(requests[0], row)
	if err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]map[string]any)
	for _, m := range messages {
		if m["role"] == "assistant" && m["content"] == "trace-7b\n\nanswer-4e" && m["reasoning_content"] == nil {
			return
		}
	}
	t.Error("prior model's thinking and answer did not travel together as ordinary assistant text")
}

// TestExpandHistory_OpenAIChat_DropThinkingKeepsEncryptedDetails pins the
// continuation-state exception: an OpenRouter-style reasoning_details array is
// not the display text #4563 measured. The chat adapter must round-trip it, so
// the part survives with only its text cleared. (A signature-bearing item that
// needs the text is the next test's case.)
func TestExpandHistory_OpenAIChat_DropThinkingKeepsEncryptedDetails(t *testing.T) {
	t.Parallel()
	const compat = `[{"type":"reasoning.encrypted","data":"abc"}]`
	turns := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "display reasoning", EncryptedContent: compat}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), DropThinking: true, protocolOf: replayProtocolOf,
	})
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind != llm.ContentThinking {
				continue
			}
			if p.Thinking == nil || p.Thinking.EncryptedContent != compat {
				t.Fatalf("encrypted reasoning_details did not round-trip: %+v", p.Thinking)
			}
			if p.Thinking.Text != "" {
				t.Fatalf("display reasoning rode with the encrypted detail: %q", p.Thinking.Text)
			}
			return
		}
	}
	t.Fatal("the encrypted reasoning_details part was dropped; the chat adapter needs it for continuation")
}

// TestExpandHistory_OpenAIChat_SignatureItemKeepsItsText covers the signed
// variant: when the reasoning_details array carries a reasoning.text item with
// an empty text field, ThinkingData.Text is the text the builder merges into
// that signed item, so the trim must keep it.
func TestExpandHistory_OpenAIChat_SignatureItemKeepsItsText(t *testing.T) {
	t.Parallel()
	const compat = `[{"type":"reasoning.text","text":"","signature":"sig"}]`
	turns := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "signed reasoning", EncryptedContent: compat}},
		{Kind: llm.ContentText, Text: "answer"},
	}}}}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), DropThinking: true, protocolOf: replayProtocolOf,
	})
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind != llm.ContentThinking {
				continue
			}
			if p.Thinking == nil || p.Thinking.Text != "signed reasoning" || p.Thinking.EncryptedContent != compat {
				t.Fatalf("signature item lost the text it needs: %+v", p.Thinking)
			}
			return
		}
	}
	t.Fatal("the signature-bearing part was dropped")
}

// TestFallbackContinuationKeepsThinkingForTheFallbackPolicy pins the per-target
// projection: a chat primary strips its own thinking, but the untrimmed source
// must still feed an Anthropic fallback, whose signed thinking has to survive
// into the fallback's next request after a tool round (#4563 review).
func TestFallbackContinuationKeepsThinkingForTheFallbackPolicy(t *testing.T) {
	t.Parallel()
	primary := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "chat-primary", WireID: "chat-primary",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{Reasoning: new(true)},
	}, nil)
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "claude-fallback", WireID: "claude-fallback",
		Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{InputModalities: []string{"text", "image"}},
	}, nil)
	permErr := llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider:       "gw",
		FaultResponder: func(llm.Request) error { return permErr },
		Responder:      func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	const text, signature = "signed fallback thought", "sig-abc"
	tool := llm.ToolCallData{ID: "call_unknown", Name: "no_such_tool", Arguments: []byte(`{}`)}
	var fallbackCalls int
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider: "gwb",
		Responder: func(llm.Request) llm.Response {
			fallbackCalls++
			if fallbackCalls == 1 {
				resp := agenttest.ToolCallResponse(tool)
				resp.Message.Content = append([]llm.ContentPart{{
					Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: text, Signature: signature},
				}}, resp.Message.Content...)
				return resp
			}
			return agenttest.FinalResponse("done")
		},
	}
	sess := newSession(t,
		withProfile(primary),
		withAdapter(primaryAdapter),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/claude-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/claude-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) < 2 {
		t.Fatalf("fallback requests = %d, want a tool round and its continuation", len(fallbackReqs))
	}
	if !hasSignedThinking(fallbackReqs[len(fallbackReqs)-1].Messages, text, signature) {
		t.Fatal("the fallback continuation lost the signed thinking the chat primary does not carry")
	}
	primaryReqs := primaryAdapter.Requests()
	if len(primaryReqs) >= 2 && hasSignedThinking(primaryReqs[len(primaryReqs)-1].Messages, text, signature) {
		t.Fatal("the chat primary received thinking its policy drops")
	}
}

func hasSignedThinking(messages []llm.Message, text, signature string) bool {
	for _, m := range messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentThinking && p.Thinking != nil &&
				p.Thinking.Text == text && p.Thinking.Signature == signature {
				return true
			}
		}
	}
	return false
}

// TestFallbackReceivesTurnsAppendedDuringSkillReload pins the fallback snapshot
// refresh: a compacted-skill reload appends instructions while the request is
// prepared, and a permanent primary failure must hand the fallback the rebuilt
// history rather than the snapshot taken before the reload (#4563 review).
func TestFallbackReceivesTurnsAppendedDuringSkillReload(t *testing.T) {
	t.Parallel()
	root := skillFixtureRoot(t)
	markGitRoot(t, root)
	const reloadedMarker = "RELOAD_BODY_a71c"
	newBody := strings.Repeat(reloadedMarker+" reloaded instruction\n", 64)
	oldBody := strings.Repeat("RELOAD_OLD_5e10 stale instruction\n", 64)
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\n"+oldBody)
	source := filepath.Join(root, "skills", "opaque", "SKILL.md")
	var mainCalls atomic.Int32
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "claude-fallback", WireID: "claude-fallback",
		Protocol: registry.ProtocolAnthropic, Surface: registry.SurfaceAnthropic,
		Caps: registry.Caps{InputModalities: []string{"text", "image"}},
	}, nil)
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider:  "gwb",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("fallback ok") },
	}
	s, main, _ := newReloadSession(t, root, 0,
		func(llm.Request) llm.Response {
			if mainCalls.Add(1) == 1 {
				return agenttest.ToolCallResponse(useSkillCall("skill-1", "opaque"))
			}
			return agenttest.ToolCallResponse(communicateCall("done-1", "ok"))
		},
		reloadSummaryResponder("SUMMARY_reload_fb", nil),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1, StateDir: t.TempDir(),
			ModelFallbacks: []string{"gwb/claude-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/claude-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
		}),
	)
	var mainAttempts atomic.Int32
	main.FaultResponder = func(llm.Request) error {
		if mainAttempts.Add(1) == 3 {
			return llm.ErrorFromHTTPStatus("anthropic", 404, "model not found", nil, nil)
		}
		return nil
	}
	seedNumberedSessionHistory(t, s, 12)
	s.contextMgr.PreserveRecentTurns = 2
	if _, err := s.ProcessInput(context.Background(), "REQUEST_93d2", nil); err != nil {
		t.Fatalf("activation: %v", err)
	}
	if lifecycleInventory(s)["opaque"].Ordinary == nil {
		t.Fatal("test premise: the activation recorded no ordinary skill inventory")
	}
	foldWithReloadSelection(t, s, schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}})
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) == 0 {
		t.Fatal("test premise: the fold published no reload handoff")
	}
	if err := os.WriteFile(source, []byte("---\nname: opaque\ndescription: fixture\n---\n"+newBody), 0o644); err != nil {
		t.Fatalf("rewrite source: %v", err)
	}
	if _, err := s.ProcessInput(context.Background(), "REQUEST_again", nil); err != nil {
		t.Fatalf("reload round: %v", err)
	}
	if mainReqs := main.Requests(); len(mainReqs) < 3 || !requestCarriesText(mainReqs[2], reloadedMarker) {
		t.Fatalf("test premise: the reload round did not carry the reloaded instructions (%d requests)", len(mainReqs))
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if !requestCarriesText(fallbackReqs[len(fallbackReqs)-1], reloadedMarker) {
		t.Fatal("the fallback request lost the instructions appended during the skill reload")
	}
}

func requestCarriesText(req llm.Request, want string) bool {
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentText && strings.Contains(p.Text, want) {
				return true
			}
		}
	}
	return false
}

// TestFallbackThinkingProjectionRepricesTheBudget pins the estimate refresh:
// the continuation builder prices the untrimmed messages, but a chat fallback
// drops their thinking before dispatch. Keeping the stale estimate makes the
// budget reject a request that fits, so the fallback never runs (#4563 review).
func TestFallbackThinkingProjectionRepricesTheBudget(t *testing.T) {
	t.Parallel()
	primary := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "chat-primary", WireID: "chat-primary",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{Reasoning: new(true)},
	}, nil)
	fallback := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "chat-fallback", WireID: "chat-fallback",
		Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
	}, nil)
	fallback = withContextWindow(fallback, 50000)
	permErr := llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider:       "gw",
		FaultResponder: func(llm.Request) error { return permErr },
		Responder:      func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider:  "gwb",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ok") },
	}
	sess := newSession(t,
		withProfile(primary),
		withAdapter(primaryAdapter),
		withAdapter(fallbackAdapter),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/chat-fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/chat-fallback" {
					return fallback, nil
				}
				return nil, nil
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("THINK_b7d1 padded reasoning\n", 80000)}},
		{Kind: llm.ContentText, Text: "prior answer"},
	}})

	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if requestCarriesText(fallbackReqs[len(fallbackReqs)-1], "THINK_b7d1") {
		t.Fatal("the chat fallback carried thinking its policy drops")
	}
}
