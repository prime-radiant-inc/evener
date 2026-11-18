package agent

import (
	"context"
	"slices"
	"strings"
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
	const signed = `[{"type":"reasoning.text","signature":"sig-c13"}]`
	const encrypted = `[{"type":"reasoning.encrypted","id":"id-b27","data":"enc-b27"}]`
	for _, tc := range []struct {
		name string
		raw  bool
	}{{"replay-off", false}, {"raw-replay", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			primary := replayGateway("primary", registry.ProtocolOpenAIResponses)
			third := replayGateway("third", registry.ProtocolOpenAIChat)
			row := replayGateway("fallback", registry.ProtocolOpenAIChat).Resolved()
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
				Thinking: &llm.ThinkingData{Text: "signed-b49", EncryptedContent: `[{"type":"reasoning.text","text":"signed-b49","signature":"sig-b49"}]`},
			})
			fallbackAdapter := &fakeAdapter{name: row.Instance, steps: []func(llm.Request) llm.Response{
				fixedStep(answer), fixedStep(agenttest.FinalResponse("done")),
			}}
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
			type part struct{ text, details, query string }
			want := [][]part{
				{{query: "q-a08"}, {text: "c13", details: signed}, {query: "q-c13"}},
				{{query: "q-a08"}},
			}
			if tc.raw {
				want[1] = append(want[1], part{text: "b27"})
			}
			for i, req := range requests {
				var got []part
				for _, m := range req.Messages {
					for _, p := range m.Content {
						switch p.Kind {
						case llm.ContentThinking:
							got = append(got, part{text: p.Thinking.Text, details: p.Thinking.EncryptedContent})
						case llm.ContentWebSearch:
							got = append(got, part{query: p.WebSearch.Query})
						}
					}
				}
				if !slices.Equal(got, want[i]) {
					t.Errorf("fallback request %d reasoning and web search = %+v, want %+v", i, got, want[i])
				}
			}
			followup := requests[1]
			if wantTokens := llm.EstimateInputTokensForResolved(row, followup).Tokens; followup.FullHistoryInputTokensEstimate != wantTokens || followup.InputTokensEstimate != wantTokens {
				t.Errorf("fallback estimates = %d full, %d input; projected request costs %d", followup.FullHistoryInputTokensEstimate, followup.InputTokensEstimate, wantTokens)
			}
			body, err := chatcompletions.DefaultProtocol.BuildBody(followup, row)
			if err != nil {
				t.Fatal(err)
			}
			var scalars []string
			for _, message := range body["messages"].([]map[string]any) {
				if text, ok := message["reasoning"].(string); ok {
					scalars = append(scalars, text)
				}
			}
			if (tc.raw && !slices.Equal(scalars, []string{"native-b27"})) || (!tc.raw && len(scalars) != 0) {
				t.Errorf("fallback wire scalars = %q", scalars)
			}
		})
	}
}

func TestFallbacksWithoutRawReplayShareTheRoundsHistory(t *testing.T) {
	t.Parallel()
	producerProtocol := registry.ProtocolOpenAIResponses
	primary := NewOpenAIProfile("primary")
	adapter := &agenttest.ModelTrackingAdapter{Provider: primary.ID(), Respond: func(req llm.Request) (llm.Response, error) {
		switch req.Model {
		case "fallback-b":
			// The registry now routes the unstamped turn's issuer over chat.
			producerProtocol = registry.ProtocolOpenAIChat
		case "fallback-c":
			return agenttest.FinalResponse("done"), nil
		}
		return llm.Response{}, llm.ErrorFromHTTPStatus(req.Provider, 404, "", nil, nil)
	}}
	s := newSession(t, withProfile(primary), withAdapter(adapter), withConfig(SessionConfig{ModelFallbacks: []string{"fallback-b", "fallback-c"}}))
	s.setRoundFallbackSource(roundFallbackSource{
		turns: []schema.Turn{assistantWebSearchTurn("archive", "reasoner")},
		scope: replayScope{Protocol: primary.Protocol(), InFlightFrom: 1, protocolOf: func(string) string { return producerProtocol }},
	})
	req := llm.Request{Provider: primary.ID(), Model: primary.Model(), Messages: []llm.Message{llm.User("r08")}}
	if _, _, _, _, err := s.callModelWithFallback(tripwireCtx(t), primary, req, nil, "", 0); err != nil {
		t.Fatal(err)
	}
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("got %d model calls, want the primary and both fallbacks", len(requests))
	}
	for _, req := range requests[1:] {
		if !hasContentKind(req.Messages, llm.ContentWebSearch) {
			t.Errorf("%s lost the web search in this round's common history", req.Model)
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
