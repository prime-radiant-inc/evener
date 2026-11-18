package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
}

func TestFallbackReplaysOnlyItsIssuersReasoning(t *testing.T) {
	t.Parallel()
	primary := replayGateway("work", registry.ProtocolOpenAIResponses)
	fallback := replayGateway("gw", registry.ProtocolOpenAIChat)
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
	fallbackAdapter := newFakeAdapter(fallback.ID(),
		fixedStep(withReasoningMarker(agenttest.ToolCallResponse(
			llm.ToolCallData{ID: "tool-b", Name: "missing_b", Arguments: []byte(`{}`)}), fallback.Protocol(), "fallback-73")),
		fixedStep(withReasoningMarker(agenttest.FinalResponse("done"), fallback.Protocol(), "fallback-81")),
		fixedStep(agenttest.FinalResponse("done")),
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
	}
	assertReasoningMarkers(t, fallbackRequests[0])
	assertReasoningMarkers(t, fallbackRequests[1], "fallback-73")
	assertReasoningMarkers(t, fallbackRequests[2], "fallback-73", "fallback-81")

	meta, env := s.Meta(), s.currentEnv()
	s.Close()
	for i, redirect := range []struct {
		wire, deployment, body, stream  string
		enrolled, wantPlain, wantCipher bool
	}{
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
			switch {
			case !redirect.wantPlain:
				assertReasoningMarkers(t, req)
			case req.Provider == primary.ID():
				assertReasoningMarkers(t, req, "primary-42")
			default:
				assertReasoningMarkers(t, req, "fallback-73", "fallback-81")
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
