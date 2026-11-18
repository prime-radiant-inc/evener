package llm

import (
	"net/http"
	"net/url"
	"reflect"
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
		{"https://two.example.invalid:443/complete", false, true},
		{"http://one.example.invalid/complete", false, false},
		{"https://one.example.invalid:9443/complete", false, false},
	} {
		got, changed := req.ForReasoningEndpoint(tc.target, registry.Resolved{Protocol: registry.ProtocolOpenAIResponses})
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
	same, changed := delta.ForReasoningEndpoint("https://one.example.invalid/responses", registry.Resolved{})
	if changed || !reflect.DeepEqual(same, delta) {
		t.Fatal("issuer lost its original delta")
	}
	other, changed := delta.ForReasoningEndpoint(second.ReasoningEndpoint, registry.Resolved{})
	if !changed || other.PreviousResponseID != "" || other.HistoryMode != HistoryModeFullHistoryFallback || len(other.Messages) != len(full) || other.Messages[1].Content[1].ToolCall.ThoughtSignature != "" || !reflect.DeepEqual(other.Messages[3], second) {
		t.Fatal("new origin needs full history projected for its own turns")
	}
	if full[1].Content[2].ToolCall.ThoughtSignature != "one" || delta.PreviousResponseID != "anchor" {
		t.Fatal("projection mutated the source")
	}
}
