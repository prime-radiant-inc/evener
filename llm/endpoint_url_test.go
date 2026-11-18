package llm

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func TestFinalResponseEndpointURLPrefersSanitizedFinalRequest(t *testing.T) {
	t.Parallel()
	finalURL, err := url.Parse("https://redirect-user:redirect-password@final.example.test/v2/messages?token=secret#fragment")
	if err != nil {
		t.Fatalf("parse final URL: %v", err)
	}
	resp := &http.Response{Request: &http.Request{URL: finalURL}}

	got := FinalResponseEndpointURL(resp, "https://original.example.test/v1/messages?key=original-secret")
	if want := "https://final.example.test/v2/messages"; got != want {
		t.Fatalf("FinalResponseEndpointURL() = %q, want %q", got, want)
	}
}

func TestRequestProjectsReasoningForEachServingOrigin(t *testing.T) {
	t.Parallel()
	turn := func(source, label string) Message {
		m := Assistant(label)
		m.ReasoningEndpoint = source
		m.Content = append(m.Content, ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: label}},
			ContentPart{Kind: ContentToolCall, ToolCall: &ToolCallData{ID: label, Name: "read", ThoughtSignature: label}})
		return m
	}
	first := turn("https://One.Example.invalid:443/v1/stream", "one")
	row := registry.Resolved{Protocol: registry.ProtocolOpenAIResponses, Transport: registry.Transport{
		BaseURL: "https://one.example.invalid", Endpoint: "/complete", StreamEndpoint: "/v1/stream?alt=sse", CountTokensEndpoint: "/count",
	}}
	// A transcript can hold only the original HTTPS origin digest for two.example.invalid.
	second := turn("sha256:7c4d5aac60e263bd15762dfaf711cea1651d025ae55888c2df3811ba80a2d260", "two")
	full := []Message{System("context"), first, ToolResult("one", "ok", false), second, User("next")}
	req := Request{Messages: full}
	if !req.HasReasoningEndpoints() {
		t.Fatal("request lost its per-turn serving origins")
	}
	for _, tc := range []struct {
		target                string
		firstOwns, secondOwns bool
	}{
		{"https://one.example.invalid/complete", true, false},
		{"https://one.example.invalid/count", true, false},
		{"https://one.example.invalid/v1/stream?alt=sse", true, false},
		{"https://one.example.invalid/another/deployment", false, false},
		{"https://one.example.invalid/v1/stream?tenant=29", false, false},
		{"https://two.example.invalid:443/complete", false, true},
		{"http://one.example.invalid/complete", false, false},
		{"https://one.example.invalid:9443/complete", false, false},
	} {
		got, changed := req.ForReasoningEndpoint(tc.target, row)
		if !changed || len(got.Messages) != len(full) {
			t.Fatalf("projection for %s lost conversation", tc.target)
		}
		for _, entry := range []struct {
			index int
			owns  bool
		}{{1, tc.firstOwns}, {3, tc.secondOwns}} {
			m := got.Messages[entry.index]
			if entry.owns {
				if !reflect.DeepEqual(m, full[entry.index]) {
					t.Errorf("issuer lost original parts at %d", entry.index)
				}
			} else if len(m.Content) != 2 || m.Text() != full[entry.index].Text() || m.Content[1].ToolCall.ID != m.Text() || m.Content[1].ToolCall.ThoughtSignature != "" {
				t.Errorf("conversation projection for %s at %d: %+v", tc.target, entry.index, m.Content)
			}
		}
		if !reflect.DeepEqual(got.Messages[2], full[2]) {
			t.Fatal("tool result changed")
		}
	}
	chat := registry.Resolved{Protocol: registry.ProtocolOpenAIChat, Caps: registry.Caps{ThinkingAsText: new(true)}}
	got, _ := req.ForReasoningEndpoint("https://two.example.invalid/", chat)
	if len(got.Messages[1].Content) != 3 || !reflect.DeepEqual(got.Messages[1].Content[1].Thinking, &ThinkingData{Text: "one"}) {
		t.Fatal("Chat lost the unsigned display text")
	}
	delta := Request{PreviousResponseID: "anchor", HistoryMode: HistoryModeResponsesDelta, Messages: full[4:], Continuation: &ContinuationMetadata{AnchorEndpoint: first.ReasoningEndpoint, FullHistory: full}}
	if !delta.HasReasoningEndpoints() {
		t.Fatal("delta lost the issuing origin")
	}
	same, changed := delta.ForReasoningEndpoint(row.RequestURL(row.Transport.Endpoint), row)
	if changed || !reflect.DeepEqual(same, delta) {
		t.Fatal("issuer lost its original delta")
	}
	sibling, changed := delta.ForReasoningEndpoint(row.Transport.BaseURL+"/another/deployment", row)
	if !changed || sibling.PreviousResponseID != "" || len(sibling.Messages) != len(full) || sibling.Messages[1].Content[1].ToolCall.ThoughtSignature != "" {
		t.Fatal("another deployment on the same host needs the ordinary conversation")
	}
	other, changed := delta.ForReasoningEndpoint(second.ReasoningEndpoint, registry.Resolved{})
	if !changed || other.PreviousResponseID != "" || other.HistoryMode != HistoryModeFullHistoryFallback || len(other.Messages) != len(full) || other.Messages[1].Content[1].ToolCall.ThoughtSignature != "" || !reflect.DeepEqual(other.Messages[3], second) {
		t.Fatal("new origin needs full history projected for its own turns")
	}
	if full[1].Content[2].ToolCall.ThoughtSignature != "one" || delta.PreviousResponseID != "anchor" {
		t.Fatal("projection mutated the source")
	}
	for _, protocol := range []string{registry.ProtocolOpenAIChat, registry.ProtocolOpenAIResponses} {
		t.Run(protocol+" serving proof belongs to its configured instance", func(t *testing.T) {
			issuer := registry.Resolved{Instance: "first", Protocol: protocol, Transport: row.Transport}
			issuer.Credential.Value = "fixture-first"
			other := issuer
			other.Instance = "second"
			other.Credential.Value = "fixture-second"
			rotated := issuer
			rotated.Credential.Value = "fixture-rotated"
			visible := turn("", "answer")
			visible.Content = append(visible.Content, ContentPart{Kind: ContentRedThinking, Thinking: &ThinkingData{EncryptedContent: "sealed"}})
			result := ToolResult("answer", "ok", false)
			for _, tc := range []struct{ name, served, replay string }{
				{"configured operations", issuer.RequestURL(issuer.Transport.StreamEndpoint), issuer.RequestURL(issuer.Transport.Endpoint)},
				{"redirected recipient", "https://redirect.example.invalid/route", "https://redirect.example.invalid/route"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					u, err := url.Parse(tc.served)
					if err != nil {
						t.Fatal(err)
					}
					response := Response{Message: visible}
					StampHTTPResponseEndpoint(&response, &http.Response{Request: &http.Request{URL: u}}, "", APILogCredentialMaterial{}, issuer)
					req := Request{Messages: []Message{response.Message, result}}
					for _, owner := range []registry.Resolved{issuer, rotated} {
						got, changed := req.ForReasoningEndpoint(tc.replay, owner)
						if changed || !reflect.DeepEqual(got, req) {
							t.Fatal("the issuing instance lost native state")
						}
					}
					for _, target := range []string{tc.replay, response.ServingOrigin} {
						got, changed := req.ForReasoningEndpoint(target, other)
						if !changed || len(got.Messages) != 2 || got.Messages[0].HasNativeReasoning() {
							t.Errorf("another instance at the same URL received native state for %s", tc.name)
							continue
						}
						if got.Messages[0].Text() != "answer" || len(got.Messages[0].Content) != 2 || got.Messages[0].Content[1].ToolCall.ID != "answer" || !reflect.DeepEqual(got.Messages[1], result) {
							t.Fatal("another instance lost visible history or the tool exchange")
						}
					}
					if protocol == registry.ProtocolOpenAIResponses {
						delta := Request{
							Messages: req.Messages[1:], HistoryMode: HistoryModeResponsesDelta,
							PreviousResponseID: "resp_17", ConversationID: "conv_17",
							Continuation: &ContinuationMetadata{AnchorEndpoint: response.ReasoningEndpoint(), FullHistory: req.Messages},
						}
						if got, changed := delta.ForReasoningEndpoint(tc.replay, rotated); changed || !reflect.DeepEqual(got, delta) {
							t.Error("the issuing instance lost its original delta")
						}
						got, changed := delta.ForReasoningEndpoint(tc.replay, other)
						if !changed || got.PreviousResponseID != "" || got.ConversationID != "" || got.Continuation != nil || got.HistoryMode != HistoryModeFullHistoryFallback {
							t.Error("another instance at the same URL received the opaque continuation")
						} else if len(got.Messages) != 2 || !reflect.DeepEqual(got.Messages[0], visible.WithoutNativeReasoning(false)) || !reflect.DeepEqual(got.Messages[1], result) {
							t.Error("continuation fallback lost the visible tool exchange")
						}
					}
				})
			}
		})
	}
	t.Run("admission prices the largest single recipient and retains the source history", func(t *testing.T) {
		row.Caps.ContextWindow = new(3_100)
		origin := parseServingURL(row.Transport.BaseURL).origin
		msgs := []Message{User("next")}
		for _, issuer := range []struct {
			proof string
			chars int
		}{
			{row.RequestURL(row.Transport.Endpoint), 4_000},
			{ConfiguredServingRecipient(row), 2_000},
			{row.Transport.BaseURL + "/sibling", 5_000},
			{origin, 2_000},
			{"https://two.example.invalid/deployment", 7_200},
			{UnknownServingOrigin, 9_000},
		} {
			msg := Assistant("seen")
			msg.Content = append(msg.Content, ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{EncryptedContent: strings.Repeat("x", issuer.chars)}})
			msgs = append(msgs, msg.BindReasoningEndpoint(issuer.proof))
		}
		req := Request{Messages: msgs}
		admitted, budget, err := ApplyTokenBudget(req, row)
		if err != nil || budget.InputTokens-budget.SafetyTokens != 2_007 {
			t.Errorf("budget = %+v, err = %v; want 2,007 input tokens before reserve", budget, err)
		}
		for _, tc := range []struct {
			endpoint string
			tokens   int
		}{{row.RequestURL(row.Transport.StreamEndpoint), 2_007}, {row.Transport.BaseURL + "/sibling", 1_757}, {"https://two.example.invalid/deployment", 1_807}, {"https://elsewhere.example.invalid/", 7}} {
			view, _ := admitted.ForReasoningEndpoint(tc.endpoint, row)
			if got := EstimateInputTokensForResolved(row, view).Tokens; got != tc.tokens {
				t.Errorf("admitted history for %s = %d tokens, want %d", tc.endpoint, got, tc.tokens)
			}
		}
		row.Caps.MaxInputTokens = new(3_030)
		if _, _, err := ApplyTokenBudget(req, row); Kind(err) != KindContextLength {
			t.Errorf("largest eligible recipient exceeds 3,030 with reserve, got %v", err)
		}
	})
	t.Run("Chat display remains shared while signed details belong to their issuer", func(t *testing.T) {
		chat.Transport = row.Transport
		req := Request{}
		for _, source := range []string{chat.RequestURL(chat.Transport.Endpoint), chat.Transport.BaseURL + "/sibling", UnknownServingOrigin} {
			msg := Assistant("seen")
			msg.Content = append(msg.Content,
				ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: strings.Repeat("d", 400)}},
				ContentPart{Kind: ContentThinking, Thinking: &ThinkingData{Text: "signed", EncryptedContent: `[{"type":"reasoning.text","signature":"sig","text":"signed"}]`}},
			)
			req.Messages = append(req.Messages, msg.BindReasoningEndpoint(source))
		}
		estimator := TokenPartEstimatorFor(chat)
		var largest int
		for _, source := range []string{chat.RequestURL(chat.Transport.Endpoint), chat.Transport.BaseURL + "/sibling", "https://other.example.invalid/"} {
			view, _ := req.ForReasoningEndpoint(source, chat)
			var cost TokenParts
			for _, msg := range view.Messages {
				cost = cost.Add(estimator.Parts(msg))
			}
			if source == "https://other.example.invalid/" && cost.Total() != 303 {
				t.Fatalf("unrelated deployment sees %d tokens, want all three visible answers and displays", cost.Total())
			}
			largest = max(largest, cost.Total())
		}
		if got := EstimateInputTokensForResolved(chat, req).Tokens; got != largest {
			t.Errorf("Chat estimate = %d tokens, largest eligible projection = %d", got, largest)
		}
	})
}
