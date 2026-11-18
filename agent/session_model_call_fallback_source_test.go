package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	"primeradiant.com/evener/llm/registry"
)

// TestRequestMessagesLaysOutTheSystemPromptAndHistory pins the layout a request
// dispatches and the fallback lays out: the system prompt leads as its own
// message, or, when the session folds it into the first user message, as that
// message's leading text. A history starting with another role takes the
// separate-message form either way.
func TestRequestMessagesLaysOutTheSystemPromptAndHistory(t *testing.T) {
	t.Parallel()
	const system = "the system prompt"
	histories := map[string][]llm.Message{
		"empty":            nil,
		"leading-user":     {llm.User("first"), llm.Assistant("answer"), llm.User("second")},
		"leading-non-user": {llm.Assistant("earlier answer"), llm.User("now")},
		"single-user":      {llm.User("only")},
	}
	for _, tc := range []struct {
		name         string
		promptAsUser bool
	}{
		{name: "system-message"},
		{name: "prompt-as-user", promptAsUser: true},
	} {
		s := newSession(t, withConfig(SessionConfig{SystemPromptAsUser: tc.promptAsUser, testOnly: testConfig{skipGitSnapshot: true}}))
		for name, history := range histories {
			laid := s.requestMessages(system, history)
			// A prompt-as-user session folds the prompt into a leading user
			// message; any other shape keeps it as its own message.
			folds := tc.promptAsUser && len(history) > 0 && history[0].Role == llm.RoleUser
			if folds {
				if got := len(laid); got != len(history) {
					t.Errorf("%s/%s: laid out %d messages, want %d: the folded prompt rides the first user message", tc.name, name, got, len(history))
					continue
				}
				if got := laid[0].Text(); !strings.HasPrefix(got, system) {
					t.Errorf("%s/%s: first message does not lead with the system prompt", tc.name, name)
				}
				if laid[0].Role != llm.RoleUser {
					t.Errorf("%s/%s: folded prompt landed on role %q, want user", tc.name, name, laid[0].Role)
				}
				continue
			}
			if got := len(laid); got != len(history)+1 {
				t.Errorf("%s/%s: laid out %d messages, want %d", tc.name, name, got, len(history)+1)
				continue
			}
			wantRole := llm.RoleSystem
			if tc.promptAsUser {
				wantRole = llm.RoleUser
			}
			if laid[0].Role != wantRole || laid[0].Text() != system {
				t.Errorf("%s/%s: leading message role %q text %q, want role %q carrying the system prompt", tc.name, name, laid[0].Role, laid[0].Text(), wantRole)
			}
			rest := laid[1:]
			if len(history) == 0 {
				rest = nil
			}
			if eq, _, _ := jsonEqual(t, rest, history); !eq {
				t.Errorf("%s/%s: the history does not follow the leading message unchanged", tc.name, name)
			}
		}
	}
}

func TestFallbackKeepsOpaqueAndWebSearchScopeAcrossProtocols(t *testing.T) {
	t.Run("Responses search behind a Chat primary", testResponsesFallbackWebSearch)
	const signed = `[{"type":"reasoning.text","signature":"sig-c13"}]`
	const encrypted = `[{"type":"reasoning.encrypted","id":"id-b27","data":"enc-b27"}]`
	const signedAnswer = `[{"type":"reasoning.text","text":"signed-b49","signature":"sig-b49"}]`
	for _, tc := range []struct {
		name string
		raw  bool
	}{{"replay-off", false}, {"raw-replay", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			primary := replayGateway("primary", registry.ProtocolOpenAIResponses)
			third := replayGateway("third", registry.ProtocolOpenAIChat)
			row := replayGateway("fallback", registry.ProtocolOpenAIChat).Resolved()
			row.Transport.BaseURL = "https://fallback.example.invalid"
			row.Caps.ReplayReasoningText = &tc.raw
			fallback := provider.FromResolved(row, nil)
			unavailable := func(req llm.Request) error { return llm.ErrorFromHTTPStatus(req.Provider, 404, "", nil, nil) }
			primaryAdapter := &agenttest.ScriptedAdapter{Provider: primary.ID(), FaultResponder: unavailable}
			sentTool := false
			thirdAdapter := &agenttest.ModelTrackingAdapter{
				Provider: third.ID(),
				Respond: func(req llm.Request) (llm.Response, error) {
					if sentTool {
						return llm.Response{}, unavailable(req)
					}
					sentTool = true
					resp := agenttest.ToolCallResponse(llm.ToolCallData{ID: "call-c13", Name: "missing_tool", Arguments: []byte(`{}`)})
					resp.Message.Content = append([]llm.ContentPart{
						{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "c13", EncryptedContent: signed}},
						{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q-c13"}},
					}, resp.Message.Content...)
					return resp, nil
				},
			}
			answer := withReasoningMarker(agenttest.FinalResponse("done"), row.Protocol, "b27")
			// Chat decodes the scalar text and encrypted details into one part.
			answer.Message.Content[0].Thinking.EncryptedContent = encrypted
			answer.Message.Content[0].Thinking.ChatScalar = &llm.ChatReasoningScalar{Field: "reasoning", Text: "native-b27"}
			answer.Message.Content = append(answer.Message.Content, llm.ContentPart{Kind: llm.ContentThinking,
				Thinking: &llm.ThinkingData{Text: "signed-b49", EncryptedContent: signedAnswer},
			})
			fallbackAdapter := newFakeAdapter(row.Instance, fixedStep(answer), fixedStep(agenttest.FinalResponse("done")))
			profiles := map[string]*provider.Profile{"third/reasoner": third, "fallback/reasoner": fallback}
			s := newSession(t, withProfile(primary), withAdapter(primaryAdapter), withAdapter(thirdAdapter), withAdapter(fallbackAdapter), withConfig(SessionConfig{
				ModelFallbacks: []string{"third/reasoner", "fallback/reasoner"},
				ResolveProfile: func(ref string) (*provider.Profile, error) { return profiles[ref], nil },
			}))
			prior := llm.Response{Provider: primary.ID(), Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q-a08", Raw: []byte(`{"type":"web_search_call","id":"ws-a08"}`)}},
			}}}
			if err := s.appendAssistantTurn(prior, ModelAttemptMetadata{Protocol: primary.Protocol(), RequestModel: primary.Model()}); err != nil {
				t.Fatal(err)
			}
			mirror, err := chatcompletions.ExtractRecordedResponse([]byte(`{"choices":[{"message":{"role":"assistant","content":"answer-b52","reasoning":"mirror-b52","reasoning_details":[{"type":"reasoning.text","signature":"sig-b52","text":"mirror-b52"}]}}]}`), row.WireID)
			if err != nil {
				t.Fatal(err)
			}
			mirror.Provider = row.Instance
			if err := s.appendAssistantTurn(mirror, ModelAttemptMetadata{Protocol: row.Protocol, RequestModel: row.ModelID}); err != nil {
				t.Fatal(err)
			}
			ctx := tripwireCtx(t)
			for _, input := range []string{"turn-one", "turn-two"} {
				if _, err := s.ProcessInput(ctx, input, nil); err != nil {
					t.Fatal(err)
				}
			}
			requests := fallbackAdapter.Requests()
			if len(requests) != 2 {
				t.Fatalf("fallback requests = %d, want the tool continuation and a follow-up", len(requests))
			}
			type part struct{ text, details string }
			var want [2][]part
			if tc.raw {
				want[0] = []part{{text: "mirror-b52"}}
				want[1] = append(want[1], part{text: "mirror-b52"}, part{text: "b27", details: encrypted}, part{text: "signed-b49", details: signedAnswer})
			} else {
				want[1] = append(want[1], part{details: encrypted}, part{details: signedAnswer})
			}
			var scalars []string
			for i, req := range requests {
				var got []part
				for _, m := range req.Messages {
					for _, p := range m.Content {
						if p.Kind == llm.ContentThinking {
							got = append(got, part{text: p.Thinking.Text, details: p.Thinking.EncryptedContent})
						}
					}
				}
				if !slices.Equal(got, want[i]) {
					t.Errorf("fallback request %d reasoning = %+v, want %+v", i, got, want[i])
				}
				body, err := chatcompletions.DefaultProtocol.BuildBody(req, row)
				if err != nil {
					t.Fatal(err)
				}
				// The Chat wire has no native search history field. This includes
				// the scripted query-only part from the other Chat instance.
				wire := mustJSON(t, body["messages"])
				if strings.Contains(wire, "ws-a08") || strings.Contains(wire, "q-c13") {
					t.Errorf("Chat request %d carried a native search or its query", i)
				}
				var otherSignature bool
				for _, message := range body["messages"].([]map[string]any) {
					details, _ := message["reasoning_details"].([]map[string]any)
					for _, d := range details {
						if d["signature"] == "sig-c13" && d["text"] == "c13" {
							otherSignature = true
						}
					}
					if text, ok := message["reasoning"].(string); ok && i == len(requests)-1 {
						scalars = append(scalars, text)
					}
				}
				if otherSignature {
					t.Errorf("fallback request %d replayed another instance's signature", i)
				}
			}
			followup := requests[1]
			if wantTokens := llm.EstimateInputTokensForResolved(row, followup).Tokens; followup.FullHistoryInputTokensEstimate != wantTokens || followup.InputTokensEstimate != wantTokens {
				t.Errorf("fallback estimates = %d full, %d input; projected request costs %d", followup.FullHistoryInputTokensEstimate, followup.InputTokensEstimate, wantTokens)
			}
			if (tc.raw && !slices.Equal(scalars, []string{"mirror-b52", "native-b27"})) || (!tc.raw && len(scalars) != 0) {
				t.Errorf("fallback wire scalars = %q", scalars)
			}
			t.Run("own tool continuation", func(t *testing.T) { testChatToolContinuation(t, chatContinuationCase{raw: tc.raw, viaFallback: true}) })
			t.Run("direct Chat tool continuation", func(t *testing.T) { testChatToolContinuation(t, chatContinuationCase{raw: tc.raw}) })
			t.Run("second Chat model takes the tool continuation", func(t *testing.T) {
				testChatToolContinuation(t, chatContinuationCase{raw: tc.raw, viaFallback: true, switchModel: true})
				t.Run("separate scalar as assistant text", func(t *testing.T) {
					testChatToolContinuation(t, chatContinuationCase{raw: tc.raw, viaFallback: true, switchModel: true, asText: true})
				})
			})
			t.Run("Chat display behind a Responses primary", func(t *testing.T) {
				testChatToolContinuation(t, chatContinuationCase{raw: tc.raw, asText: true, viaFallback: true})
			})
		})
	}
}

func testResponsesFallbackWebSearch(t *testing.T) {
	t.Parallel()
	const search = `{"type":"web_search_call","id":"ws-73","status":"completed","action":{"type":"search","query":"q-73"}}`
	const foreign = `{"type":"server_tool_use","id":"srv-29","name":"web_search","input":{"query":"q-29"}}`
	const legacy = `{"type":"web_search_call","id":"ws-11","action":{"type":"search","query":"q-11"}}`
	const ordinaryCall = "call-29"
	requests := make(chan json.RawMessage, 4)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		var req struct{ Input json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case requests <- req.Input:
		default:
			t.Error("unexpected extra Responses request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if calls.Add(1) == 1 {
			writeSSE(t, w, nil, "response.completed", map[string]any{
				"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{
					json.RawMessage(search), map[string]string{"type": "function_call", "call_id": "call-73", "name": "missing_tool", "arguments": "{}"},
				}},
			})
			return
		}
		report := agenttest.CommunicateCall("done", "answer")
		writeResponsesFunctionCall(t, w, nil, "final", report.ID, report.Name, string(report.Arguments))
	}))
	t.Cleanup(srv.Close)

	unavailable := func(req llm.Request) error { return llm.ErrorFromHTTPStatus(req.Provider, 404, "", nil, nil) }
	chat := &agenttest.ScriptedAdapter{Provider: "chat", FaultResponder: unavailable}
	searchPart := func(raw string) llm.ContentPart {
		return llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Raw: json.RawMessage(raw)}}
	}
	anthropic := &agenttest.ModelTrackingAdapter{Provider: "claude"}
	issued := false
	anthropic.Respond = func(req llm.Request) (llm.Response, error) {
		if issued {
			return llm.Response{}, unavailable(req)
		}
		issued = true
		reply := agenttest.ToolCallResponse(llm.ToolCallData{ID: ordinaryCall, Name: "missing_tool", Arguments: []byte("{}")})
		reply.Message.Content = append(reply.Message.Content, searchPart(foreign))
		return reply, nil
	}
	dir := t.TempDir()
	client := registryClientAt(t, dir, map[string]registry.Provider{
		"chat":   {Base: "openai-compatible", Surface: registry.SurfaceOpenAI, APIKey: "test"},
		"claude": {Base: "anthropic", Surface: registry.SurfaceOpenAI, APIKey: "test"},
		"gw":     openaiInstance(srv.URL),
	}, []string{"gw"}, chat, anthropic)
	profile := resolveClientProfile(t, client, "chat/reasoner")
	resolve := func(ref string) (*provider.Profile, error) { return provider.Resolve(client.Registry(), ref) }
	fallbacks := []string{"claude/reasoner", "gw/gpt-5.2"}
	s := newSession(t, withClient(client), withProfile(profile), withConfig(SessionConfig{
		StateDir: dir, ModelFallbacks: fallbacks, ResolveProfile: resolve, OpenAIResponsesContinuation: "off",
	}))
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{searchPart(legacy)}})
	// A recorded protocol identifies the format even without an instance name.
	prior := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		searchPart(`{"type":"web_search_tool_result","tool_use_id":"srv-old","content":[]}`),
	}}}
	if err := s.appendAssistantTurn(prior, ModelAttemptMetadata{Protocol: registry.ProtocolAnthropic}); err != nil {
		t.Fatal(err)
	}
	sendOneUserInput(t, s, "first")
	sendOneUserInput(t, s, "later")
	s.Close()
	resumed, err := RestoreSessionFromMetaWithConfig(client, profile, s.currentEnv(), loadMetaForTest(t, s), RestoreSessionConfig{
		StateDir: dir, ModelFallbacks: fallbacks, ResolveProfile: resolve, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resumed.Close)
	sendOneUserInput(t, resumed, "after restart")
	if n := len(requests); n != 4 {
		t.Fatalf("Responses fallback received %d requests, want two tool rounds, a later turn and a cold resume", n)
	}
	var previous json.RawMessage
	for i := range 4 {
		input := <-requests
		if previous != nil && !bytes.HasPrefix(input, append(bytes.Clone(bytes.TrimSuffix(previous, []byte("]"))), ',')) {
			t.Errorf("Responses fallback request %d rewrote its previous input prefix", i)
		}
		var searches []string
		var calledTool, returnedTool bool
		var wire []struct {
			Type, ID string
			CallID   string `json:"call_id"`
		}
		if err := json.Unmarshal(input, &wire); err != nil {
			t.Fatal(err)
		}
		for _, item := range wire {
			switch item.Type {
			case "web_search_call", "server_tool_use", "web_search_tool_result":
				searches = append(searches, item.ID)
			case "function_call":
				calledTool = calledTool || item.CallID == ordinaryCall
			case "function_call_output":
				returnedTool = returnedTool || item.CallID == ordinaryCall
			}
		}
		want := []string{"ws-11"}
		if i > 0 {
			want = append(want, "ws-73")
		}
		if !slices.Equal(searches, want) || !calledTool || !returnedTool {
			t.Errorf("Responses fallback request %d carried searches %q and ordinary tool call/result %t/%t, want %q and both tool items", i, searches, calledTool, returnedTool, want)
		}
		previous = input
	}
}

type chatContinuationCase struct {
	raw, viaFallback, asText, mirrorSigned, switchModel bool
}

func testChatToolContinuation(t *testing.T, tc chatContinuationCase) {
	scalar := " \tλ-83\n"
	const signed = " signed-17 "
	if tc.mirrorSigned {
		scalar = signed
	}
	const answer = "answer-417"
	details := []any{
		map[string]any{"type": "reasoning.text", "index": 7, "signature": "sig-17", "text": signed},
		map[string]any{"type": "reasoning.encrypted", "id": "r-23", "data": "cipher-29"},
	}
	const unsigned = "unsigned-detail-41"
	const nextScalar = "scalar-52"
	nextDetails := []any{map[string]any{"type": "reasoning.text", "text": "signed-52", "signature": "sig-52"}}
	call := map[string]any{"id": "call-9", "type": "function", "function": map[string]any{"name": "missing_tool", "arguments": " { } "}}
	type chatRequest struct {
		Model    string
		Messages []json.RawMessage
	}
	history := make(chan chatRequest, 10)
	var calls atomic.Int32
	var rejectIssuer atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if rejectIssuer.Load() && req.Model == "reasoner" {
			http.NotFound(w, r)
			return
		}
		select {
		case history <- req:
		case <-r.Context().Done():
			return
		}
		delta := map[string]any{}
		n := int(calls.Add(1))
		if n == 1 {
			if tc.switchModel {
				rejectIssuer.Store(true)
			}
			delta["content"] = answer
			delta["tool_calls"] = []any{call}
			delta["reasoning"] = scalar
			delta["reasoning_details"] = details
			if tc.asText {
				delta["reasoning_details"] = append(slices.Clone(details), map[string]any{"type": "reasoning.text", "index": 8, "text": unsigned})
			}
		} else {
			done := agenttest.CommunicateCall("done", "recovered")
			if tc.switchModel && n == 2 {
				done.ID = "other-done-52"
				delta["reasoning"], delta["reasoning_details"] = nextScalar, nextDetails
			}
			delta["tool_calls"] = []any{map[string]any{"id": done.ID, "type": "function", "function": map[string]any{"name": done.Name, "arguments": string(done.Arguments)}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(t, w, nil, "message", map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": "tool_calls"}}})
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	clientAt := func(base string) *llm.Client {
		model := registry.Model{Caps: registry.Caps{Reasoning: new(true), ReplayReasoningText: new(tc.raw), ThinkingAsText: new(tc.asText)}}
		redirected := model
		redirected.Transport = &registry.Transport{StreamEndpoint: "/redirected/chat/completions"}
		inst := registry.Provider{Base: "openai-compatible", Surface: registry.SurfaceOpenAI, APIKey: "test", Transport: registry.Transport{BaseURL: base},
			Models: map[string]registry.Model{"reasoner": model, "other": model, "redirected": redirected}}
		c := registryClientAt(t, dir, map[string]registry.Provider{"work": openaiInstance(srv.URL), "gw": inst}, []string{"work", "gw"})
		if tc.asText && tc.viaFallback {
			c.Use(llm.MiddlewareFunc{Stream: func(ctx context.Context, req llm.Request, next llm.StreamFunc) (llm.Stream, error) {
				if req.Provider == "gw" {
					row := resolveClientProfile(t, c, "gw/"+req.Model).Resolved()
					want := llm.EstimateInputTokensForResolved(row, req).Tokens
					if req.InputTokensEstimate != want || req.FullHistoryInputTokensEstimate != want {
						t.Errorf("Chat fallback estimates = %d input, %d full; projected request costs %d", req.InputTokensEstimate, req.FullHistoryInputTokensEstimate, want)
					}
				}
				return next(ctx, req)
			}})
		}
		return c
	}
	client := clientAt(srv.URL)
	target := func(model string) (*provider.Profile, []string) {
		if tc.viaFallback {
			fallbacks := []string{"gw/" + model}
			if tc.switchModel && model == "reasoner" {
				fallbacks = append(fallbacks, "gw/other")
			}
			if tc.asText && model == "reasoner" {
				fallbacks = append(fallbacks, "gw/redirected")
			}
			return resolveClientProfile(t, client, "work/gpt-5.2"), fallbacks
		}
		if model == "reasoner" {
			return resolveClientProfile(t, client, "gw/reasoner"), []string{"other"}
		}
		return resolveClientProfile(t, client, "gw/"+model), nil
	}
	profile, fallbacks := target("reasoner")
	resolve := func(ref string) (*provider.Profile, error) { return provider.Resolve(client.Registry(), ref) }
	s := newSession(t, withClient(client), withProfile(profile), withConfig(SessionConfig{
		StateDir: dir, ModelFallbacks: fallbacks, ResolveProfile: resolve, OpenAIResponsesContinuation: "off",
	}))
	for _, input := range []string{"begin", "another turn"} {
		sendOneUserInput(t, s, input)
	}
	rejectIssuer.Store(false)
	for _, dest := range []struct{ base, model string }{{srv.URL, "reasoner"}, {srv.URL, "other"}, {srv.URL + "/other", "reasoner"}, {srv.URL + "/other", "other"}, {srv.URL, "reasoner"}} {
		s.Close()
		client = clientAt(dest.base)
		profile, fallbacks = target(dest.model)
		var err error
		s, err = RestoreSessionFromMetaWithConfig(client, profile, s.currentEnv(), loadMetaForTest(t, s), RestoreSessionConfig{
			StateDir: dir, ModelFallbacks: fallbacks, ResolveProfile: resolve, testOnly: sharingHostProbe(testConfig{skipGitSnapshot: true}),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		sendOneUserInput(t, s, "after restart")
	}
	type destination struct{ sameRoute, sameModel bool }
	// Tool continuation, next turn, cold resume, another model, redirected
	// endpoint, and a return to the original endpoint.
	targets := []destination{{true, true}, {true, true}, {true, true}, {true, false}, {false, true}, {false, false}, {true, true}}
	if tc.switchModel {
		targets[0].sameModel, targets[1].sameModel = false, false
	}
	if tc.asText && tc.viaFallback && !tc.switchModel {
		rejectIssuer.Store(true)
		sendOneUserInput(t, s, "continue on the other endpoint")
		targets = append(targets, destination{false, false})
	} else if tc.asText && !tc.viaFallback {
		if err := s.SetModel("gw/redirected"); err != nil {
			t.Fatal(err)
		}
		sendOneUserInput(t, s, "switch to the other endpoint")
		targets = append(targets, destination{false, false})
		if err := s.SetModel("gw/reasoner"); err != nil {
			t.Fatal(err)
		}
	}
	if !tc.viaFallback {
		rejectIssuer.Store(true)
		sendOneUserInput(t, s, "use the alternate model")
		targets = append(targets, destination{true, false})
	}
	if n := int(calls.Load()); n != 1+len(targets) {
		t.Fatalf("got %d Chat requests, want %d", n, 1+len(targets))
	}
	<-history // the first request predates the tool call
	var warm, toolResult json.RawMessage
	for i, dest := range targets {
		n := i + 1
		var got, result, ownDetails json.RawMessage
		var ownScalar string
		req := <-history
		if dest.sameModel != (req.Model == "reasoner") {
			t.Fatalf("Chat request %d reached model %q, expecting original model %t", n+1, req.Model, dest.sameModel)
		}
		for _, item := range req.Messages {
			var msg struct {
				ToolCallID string                `json:"tool_call_id"`
				ToolCalls  []struct{ ID string } `json:"tool_calls"`
				Details    json.RawMessage       `json:"reasoning_details"`
				Scalar     string                `json:"reasoning"`
			}
			if err := json.Unmarshal(item, &msg); err != nil {
				t.Fatal(err)
			}
			if len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "call-9" {
				got = item
			}
			if len(msg.ToolCalls) > 0 && msg.ToolCalls[0].ID == "other-done-52" {
				ownDetails, ownScalar = msg.Details, msg.Scalar
			}
			if msg.ToolCallID == "call-9" {
				result = item
			}
		}
		want := map[string]any{"role": "assistant", "content": answer, "tool_calls": []any{call}}
		keepNative := dest.sameRoute && (dest.sameModel || !tc.viaFallback)
		if keepNative {
			want["reasoning_details"] = details
		}
		if tc.asText {
			pieces := []string{scalar}
			if keepNative && !tc.mirrorSigned {
				pieces = append(pieces, signed)
			}
			want["content"] = strings.Join(append(pieces, unsigned), "\n") + "\n\n" + answer
		} else if tc.raw && dest.sameRoute && dest.sameModel {
			want["reasoning"] = scalar
		}
		if string(got) != mustJSON(t, want) {
			t.Errorf("Chat request %d replayed %s, want %s", n+1, got, mustJSON(t, want))
		}
		if tc.switchModel && n > 1 && req.Model == "other" && dest.sameRoute {
			var wantScalar string
			if tc.raw && !tc.asText {
				wantScalar = nextScalar
			}
			if string(ownDetails) != mustJSON(t, nextDetails) || ownScalar != wantScalar {
				t.Errorf("Chat request %d lost the second model's own reasoning: details %s, scalar %q", n+1, ownDetails, ownScalar)
			}
		}
		if n == 1 {
			warm, toolResult = got, result
		} else if dest == targets[0] && !bytes.Equal(got, warm) {
			t.Errorf("Chat request %d rewrote the assistant message sent with its tool result", n+1)
		}
		if len(result) == 0 || !bytes.Equal(result, toolResult) {
			t.Errorf("Chat request %d lost or changed the ordinary tool result", n+1)
		}
	}
}

func TestFallbackHistoryReuseFollowsTheDestination(t *testing.T) {
	t.Parallel()
	for _, protocol := range []string{registry.ProtocolGoogle, registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol, func(t *testing.T) {
			row := registry.Resolved{
				Instance: "work", ModelID: "model", WireID: "model", Protocol: protocol,
				Transport: registry.Transport{BaseURL: "https://example.invalid", Endpoint: "/models/{model}"},
			}
			scope := func() replayScope {
				return replayScope{Instance: "primary", Protocol: protocol, destination: &row, reasoningRoute: row.ReasoningReplayRoute()}
			}
			first := scope().fallbackHistoryKey()
			row.ModelID = "alias"
			if scope().fallbackHistoryKey() != first {
				t.Fatal("the same wire model and instance lost its reusable projection")
			}
			row.Instance = "other"
			if scope().fallbackHistoryKey() == first {
				t.Error("distinct provider instances selected the same history projection")
			}
			if protocol == registry.ProtocolGoogle {
				turn := schema.Turn{Message: llm.Message{Role: llm.RoleAssistant}, ResponseProvider: row.Instance, ResponseProtocol: protocol}
				if !scope().thinkingReplayEligible(turn, false) {
					t.Error("the completed turn from the selected Gemini fallback lost its eligibility")
				}
				row.Instance = "work"
				row.Transport.Endpoint = "/models/model"
				if scope().fallbackHistoryKey() == first {
					t.Error("distinct Gemini operation templates selected the same history projection")
				}
			}
		})
	}
}

func TestFallbacksWithoutRawReplayShareTheRoundsHistory(t *testing.T) {
	t.Parallel()
	producerProtocol := registry.ProtocolOpenAIResponses
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(req llm.Request) (llm.Response, error) {
		switch req.Model {
		case "fallback-b":
			// The registry now routes the unstamped turn's issuer over chat.
			producerProtocol = registry.ProtocolOpenAIChat
		case "fallback-d":
			return agenttest.FinalResponse("done"), nil
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus(req.Provider, 404, "", nil, nil)
	}}
	instance := openaiInstance("https://gateway.example.invalid/v1")
	instance.Models = map[string]registry.Model{"fallback-d": {Transport: &registry.Transport{StreamEndpoint: "/alternate"}}}
	client := registryClient(t, map[string]registry.Provider{"openai": instance}, adapter)
	primary := resolveClientProfile(t, client, "openai/primary")
	s := newSession(t, withProfile(primary), withClient(client), withConfig(SessionConfig{ModelFallbacks: []string{"fallback-b", "fallback-c", "fallback-d"}}))
	const cipher = "sealed-on-the-original-deployment"
	sealed := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("ordinary answer"),
		ResponseProvider: primary.ID(), ResponseProtocol: primary.Protocol(), ResponseReasoningRoute: primary.ReasoningReplayRoute(),
	}
	sealed.Message.Content = append(sealed.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: cipher}})
	archived := assistantWebSearchTurn("archive", "reasoner")
	archived.Message.Content[0].WebSearch.Raw = json.RawMessage(`{"type":"web_search_call","id":"ws-archive"}`)
	s.setRoundFallbackSource(roundFallbackSource{
		turns: []schema.Turn{archived, sealed},
		scope: replayScope{Instance: primary.ID(), Protocol: primary.Protocol(), InFlightFrom: 2, protocolOf: func(string) string { return producerProtocol }},
	})
	req := llm.Request{Provider: primary.ID(), Model: primary.Model(), Messages: []llm.Message{llm.User("r08")}}
	if _, _, _, _, err := s.callModelWithFallback(tripwireCtx(t), primary, req, nil, "", 0); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 4 {
		t.Fatalf("got %d model calls, want the primary and all three fallbacks", len(requests))
	}
	for _, req := range requests[1:] {
		if !hasContentKind(req.Messages, llm.ContentWebSearch) {
			t.Errorf("%s lost the web search in this round's common history", req.Model)
		}
		hasCipher := slices.ContainsFunc(req.Messages, func(msg llm.Message) bool {
			return slices.ContainsFunc(msg.Content, func(p llm.ContentPart) bool { return p.Thinking != nil && p.Thinking.EncryptedContent == cipher })
		})
		if want := req.Model != "fallback-d"; hasCipher != want {
			t.Errorf("%s received the original deployment's ciphertext = %t, want %t", req.Model, hasCipher, want)
		}
	}
}

// TestCompletedRoundLeavesNoFallbackSource pins that a round which runs to
// completion hands its source to the model call and leaves nothing behind, so
// a later round or a later fallback never reads an earlier round's input.
func TestCompletedRoundLeavesNoFallbackSource(t *testing.T) {
	t.Parallel()
	sess := newSession(t,
		withAdapter(&agenttest.ScriptedAdapter{
			Provider:  "openai",
			Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") },
		}),
		withConfig(SessionConfig{testOnly: testConfig{skipGitSnapshot: true}}),
	)
	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "task", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	sess.mu.Lock()
	source, set := sess.roundFallbackSource, sess.roundFallbackSourceSet
	sess.mu.Unlock()
	if set {
		t.Fatalf("a completed round left its fallback source behind: %#v", source)
	}
}

// TestRoundWithoutFallbackRecordsNoSource pins the removed work: a round whose
// preparation completes records nothing once its model call has taken the
// source, so no later round can inherit this round's input and no eager
// message slice survives the round.
func TestRoundWithoutFallbackRecordsNoSource(t *testing.T) {
	t.Parallel()
	s, _ := newRequestBuildBenchSession(t)
	s.setRoundFallbackSource(roundFallbackSource{system: "system prompt", turns: []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("first"))}})
	if source, ok := s.takeRoundFallbackSource(); !ok || source.system != "system prompt" || len(source.turns) != 1 {
		t.Fatalf("round source did not hold the recorded input: %#v", source)
	}
	if source, ok := s.takeRoundFallbackSource(); ok {
		t.Fatalf("a taken source stayed behind: %#v", source)
	}
}
