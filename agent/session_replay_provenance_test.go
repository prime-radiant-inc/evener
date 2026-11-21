package agent

// Tests for Task 6: cross-model transcript replay provenance (spec N4). They
// drive expandHistory directly with a replayScope for each destination wire
// protocol and assert which provider/model-scoped content (thinking,
// web_search) from completed prior turns survives into the outgoing request,
// and that tool_call/tool_result ids and the stored transcript are never
// touched.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

// assistantThinkingTurn builds a completed assistant turn carrying a signed
// thinking block plus answer text, stamped with the given provenance.
func assistantThinkingTurn(provider, reqModel, respModel string) schema.Turn {
	return schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning", Signature: "sig-abc"}},
			{Kind: llm.ContentText, Text: "answer"},
		}},
		ResponseProvider:     provider,
		ResponseRequestModel: reqModel,
		ResponseModel:        respModel,
	}
}

// assistantWebSearchTurn builds a completed assistant turn carrying a raw
// web_search block plus answer text.
func assistantWebSearchTurn(provider, reqModel string) schema.Turn {
	return schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{Query: "q", Raw: json.RawMessage(`{"type":"server_tool_use"}`)}},
			{Kind: llm.ContentText, Text: "answer"},
		}},
		ResponseProvider:     provider,
		ResponseRequestModel: reqModel,
	}
}

// hasThinking / hasWebSearch report whether any expanded message still carries
// the given content kind.
func hasContentKind(msgs []llm.Message, kind llm.ContentKind) bool {
	for _, m := range msgs {
		for _, p := range m.Content {
			if p.Kind == kind {
				return true
			}
		}
	}
	return false
}

// protocolResolver maps an instance id to the protocol it speaks today, the
// way the session resolves one through the registry. An id the map does not
// name is no longer configured and resolves to "", which makes turns it
// produced ineligible.
func protocolResolver(m map[string]string) func(string) string {
	return func(id string) string { return m[id] }
}

// replayProtocolOf resolves every instance id these tests produce turns from.
var replayProtocolOf = protocolResolver(map[string]string{
	"anthropic":   registry.ProtocolAnthropic,
	"anthropic-2": registry.ProtocolAnthropic,
	"kimi":        registry.ProtocolAnthropic,
	"google":      registry.ProtocolGoogle,
	"openai":      registry.ProtocolOpenAIResponses,
	"openai-a":    registry.ProtocolOpenAIResponses,
	"openai-b":    registry.ProtocolOpenAIResponses,
	"work":        registry.ProtocolOpenAIChat,
})

// TestChatSessionReplaysFullReasoningAcrossToolRounds: round one's chat reasoning rides its continuation.
func TestChatSessionReplaysFullReasoningAcrossToolRounds(t *testing.T) {
	t.Parallel()
	const thought, sig = "round one chain of thought", "sig-chat"
	check := func(t *testing.T, want, on bool) {
		round := agenttest.ToolCallResponse(llm.ToolCallData{ID: "call_unknown", Name: "no_such_tool", Arguments: []byte(`{}`)})
		round.Message.Content = append([]llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thought, Signature: sig}}}, round.Message.Content...)
		adapter := newFakeAdapter("gw", func(llm.Request) llm.Response { return round }, func(llm.Request) llm.Response { return agenttest.FinalResponse("done") })
		profile := provider.FromResolved(registry.Resolved{Instance: "gw", ModelID: "chat-row", WireID: "chat-row", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, Caps: registry.Caps{Reasoning: new(on)}}, nil)
		sess := newSession(t, withProfile(profile), withAdapter(adapter))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: only a genuine hang.
		defer cancel()
		_, err := sess.ProcessInput(ctx, "go", nil)
		if reqs := adapter.Requests(); err != nil || len(reqs) < 2 {
			t.Fatalf("chat requests = %d, want the tool round and its continuation", len(reqs))
		} else if got := hasSignedThinking(reqs[1].Messages, thought, sig); got != want {
			t.Fatalf("continuation replayed reasoning = %v, want %v", got, want)
		}
	}
	t.Run("on", func(t *testing.T) { t.Parallel(); check(t, true, true) })
	t.Run("off", func(t *testing.T) { t.Parallel(); check(t, false, false) })
}

// --- the session-side lookups the scope is wired to ---

// TestSessionInstanceProtocol resolves through the registry rather than the
// credentialed instance list (controller ruling R1): a curated implicit
// provider has no credential on a bare client, so Instance() would not list it
// and a legacy turn from it would silently lose its thinking replay.
func TestSessionInstanceProtocol(t *testing.T) {
	t.Parallel()
	sess := &Session{client: llm.NewClient()}
	cases := map[string]string{
		"openai":    registry.ProtocolOpenAIResponses,
		"anthropic": registry.ProtocolAnthropic,
		"nope":      "",
	}
	for name, want := range cases {
		if got := sess.instanceProtocol(name); got != want {
			t.Errorf("instanceProtocol(%q) = %q, want %q", name, got, want)
		}
	}
	if got := (&Session{}).instanceProtocol("openai"); got != "" {
		t.Errorf("instanceProtocol without a client = %q, want empty", got)
	}
}

// TestSessionCanonicalModelID pins the G12 provenance fallback's
// canonicalization: a "[1m]" ref is an alias row, so it folds onto its target;
// a dated id the catalog does not carry as its own row folds onto the base row
// the registry matched; and a dated snapshot that IS its own catalog row folds
// onto the undated row the same instance serves — after the alias fold, so
// every spelling of one deployment lands on the same id.
func TestSessionCanonicalModelID(t *testing.T) {
	t.Parallel()
	sess := &Session{client: llm.NewClient()}
	cases := []struct{ model, want string }{
		{"claude-sonnet-4-5[1m]", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5-20990101", "claude-sonnet-4-5"},
		{"  claude-sonnet-4-5[1m]  ", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5-20250929[1m]", "claude-sonnet-4-5"},
	}
	for _, tc := range cases {
		if got := sess.canonicalModelID("anthropic", tc.model); got != tc.want {
			t.Errorf("canonicalModelID(anthropic, %q) = %q, want %q", tc.model, got, tc.want)
		}
	}
	if got := sess.canonicalModelID("nope", "some-model"); got != "some-model" {
		t.Errorf("canonicalModelID on an unresolvable instance = %q, want the trimmed ref", got)
	}
	if got := (&Session{}).canonicalModelID("anthropic", " claude-sonnet-4-5[1m] "); got != "claude-sonnet-4-5[1m]" {
		t.Errorf("canonicalModelID without a client = %q, want the trimmed ref", got)
	}
}

// --- thinking, anthropic-family targets (exact-model, closes G12) ---

func TestExpandHistory_Anthropic_SameModel_ThinkingReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6-20260101")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("same-model thinking was stripped; want it replayed")
	}
}

// TestExpandHistory_Anthropic_DifferentModel_ThinkingAbsent is the G12 case:
// after an anthropic→anthropic model change, the prior model's signed thinking
// must not replay to the new model.
func TestExpandHistory_Anthropic_DifferentModel_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6-20260101")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-sonnet-4-5", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("G12: different-model thinking replayed; want it stripped from the outgoing request")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive thinking stripping")
	}
}

func TestExpandHistory_Anthropic_DifferentProvider_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("openai", "gpt-5.4", "gpt-5.4")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("cross-provider thinking replayed into anthropic; want it stripped")
	}
}

// TestExpandHistory_Anthropic_EmptyRequestModel_CanonicalFallback exercises the
// ResponseRequestModel-empty branch: comparison falls back to canonicalized
// ResponseModel, so a dated snapshot of the same requested alias still replays.
func TestExpandHistory_Anthropic_EmptyRequestModel_CanonicalFallback(t *testing.T) {
	t.Parallel()
	canon := func(m string) string {
		if m == "claude-opus-4-6" || m == "claude-opus-4-6-20260101" {
			return "claude-opus-4-6"
		}
		return m
	}
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "", "claude-opus-4-6-20260101")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf, canonicalModel: canon,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("canonical ResponseModel fallback should treat dated snapshot as same model")
	}

	mismatch := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-sonnet-4-5", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf, canonicalModel: canon,
	})
	if hasContentKind(mismatch, llm.ContentThinking) {
		t.Fatal("canonical fallback must still strip a genuinely different model")
	}
}

// TestExpandHistory_EmptyProvenance_ThinkingReplays covers legacy transcripts:
// a turn with no provenance replays everywhere unchanged.
func TestExpandHistory_EmptyProvenance_ThinkingReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("", "", "")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("empty-provenance (legacy) thinking must remain replay-eligible")
	}
}

// --- thinking, google target ((Instance, WireID) provenance) ---

func TestExpandHistory_Google_InstanceAndWireIDProvenance(t *testing.T) {
	t.Parallel()
	googleTurn := func(instance, wireModel string) schema.Turn {
		return schema.Turn{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
					GoogleInteraction: true,
					GoogleInstance:    instance,
					GoogleModel:       wireModel,
					Text:              "thought",
					Summary:           []string{"thought"},
					Signature:         "sig-g",
				}},
				{Kind: llm.ContentText, Text: "answer"},
			}},
			ResponseProvider: instance,
			ResponseProtocol: registry.ProtocolGoogle,
			ResponseModel:    wireModel,
		}
	}
	turns := []schema.Turn{googleTurn("google", "gemini-3.8-flash")}
	same := expandHistory(turns, replayScope{
		Instance: "google", Model: "alias-to-38", WireID: "gemini-3.8-flash", Protocol: registry.ProtocolGoogle,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(same, llm.ContentThinking) {
		t.Fatal("same (Instance, WireID) including alias must replay GoogleInteraction thinking")
	}
	diffWire := expandHistory(turns, replayScope{
		Instance: "google", Model: "gemini-4-neon", WireID: "gemini-4-neon", Protocol: registry.ProtocolGoogle,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(diffWire, llm.ContentThinking) {
		t.Fatal("different WireID on same Google instance must strip GoogleInteraction thinking")
	}
}

func TestExpandHistory_Google_DifferentProvider_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "google", Model: "gemini-3-pro", Protocol: registry.ProtocolGoogle,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("cross-provider thinking replayed into google; want it stripped")
	}
}

// --- thinking, openai Responses target (same-deployment only) ---
//
// openai Responses carries an opaque encrypted_content blob that only its
// issuing deployment can decrypt. A cross-deployment replay yields a 400
// "Encrypted content is not supported.", so thinking from a different provider
// is stripped; same-provider thinking (even across models) replays.

func TestExpandHistory_OpenAIResponses_SameProviderDifferentModel_ThinkingReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("openai", "gpt-5.4", "gpt-5.4")}
	out := expandHistory(turns, replayScope{
		Instance: "openai", Model: "gpt-5.6", Protocol: registry.ProtocolOpenAIResponses,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("openai same-provider thinking must replay across models")
	}
}

func TestExpandHistory_OpenAIResponses_DifferentProvider_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "openai", Model: "gpt-5.4", Protocol: registry.ProtocolOpenAIResponses,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("openai cross-provider thinking replayed; want it stripped (a bare Text projection never rides)")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive thinking stripping")
	}
}

// TestExpandHistory_OpenAIResponses_DifferentProvider_RedactedThinkingAbsent
// covers the redacted_thinking half of the filter loop: ContentRedThinking is
// stripped under the same !keepThinking guard as ContentThinking, but no prior
// test exercised it for the openai family.
func TestExpandHistory_OpenAIResponses_DifferentProvider_RedactedThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Text: "redacted"}},
			{Kind: llm.ContentText, Text: "answer"},
		}},
		ResponseProvider: "anthropic",
	}}
	out := expandHistory(turns, replayScope{
		Instance: "openai", Model: "gpt-5.4", Protocol: registry.ProtocolOpenAIResponses,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentRedThinking) {
		t.Fatal("openai cross-provider redacted_thinking replayed; want it stripped")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive redacted_thinking stripping")
	}
}

// A scope that carries no target policy (DropThinking false) leaves thinking
// alone, whatever the protocol: the chat drop is a property of the outgoing
// target, set by request assembly from the profile (#4563), not a provenance
// rule that a bare scope can infer.
func TestExpandHistory_OpenAIChat_ScopeWithoutDropKeepsThinking(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("a scope without the target's drop policy must not strip thinking")
	}
}

// TestExpandHistory_RecordedProtocolBeatsInstanceLookup pins that a turn
// carrying its own ResponseProtocol is judged by that, not by what its
// instance speaks today: an instance repointed at another protocol since the
// turn was written must not make its stored thinking replay-eligible.
func TestExpandHistory_RecordedProtocolBeatsInstanceLookup(t *testing.T) {
	t.Parallel()
	turn := assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")
	turn.ResponseProtocol = registry.ProtocolOpenAIChat
	turns := []schema.Turn{turn}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("a turn produced over another protocol replayed; the recorded protocol must decide")
	}
}

// TestExpandHistory_UnconfiguredInstance_ThinkingAbsent covers the spec §7.5
// rule for a pre-cut-over turn whose instance is gone: with nothing to resolve
// it to, the turn is not replay-eligible.
func TestExpandHistory_UnconfiguredInstance_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("retired-instance", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "retired-instance", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("thinking from an instance that is no longer configured replayed; want it stripped")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive thinking stripping")
	}
}

// --- web_search family scoping (G13) ---

func TestExpandHistory_WebSearch_SameFamilyReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic-2", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns),
		protocolOf:   protocolResolver(map[string]string{"anthropic": registry.ProtocolAnthropic, "anthropic-2": registry.ProtocolAnthropic}),
	})
	if !hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("web_search within the anthropic family must replay verbatim")
	}
}

// TestExpandHistory_WebSearch_AcrossAnthropicInstancesReplays pins that two
// different instances both speaking the anthropic protocol replay each other's
// raw web_search blocks verbatim: the block shape is the protocol's, so an
// anthropic-produced one is byte-compatible in a request to the kimi instance.
func TestExpandHistory_WebSearch_AcrossAnthropicInstancesReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "kimi", Model: "kimi-for-coding", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns),
		protocolOf:   protocolResolver(map[string]string{"anthropic": registry.ProtocolAnthropic, "kimi": registry.ProtocolAnthropic}),
	})
	if !hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("anthropic→kimi web_search must replay verbatim (both speak the anthropic protocol)")
	}
}

// TestExpandHistory_WebSearch_CrossFamilyDropped is the G13 case: an
// anthropic-produced raw web_search block must not land in an openai request.
func TestExpandHistory_WebSearch_CrossFamilyDropped(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "openai", Model: "gpt-5.4", Protocol: registry.ProtocolOpenAIResponses,
		InFlightFrom: len(turns),
		protocolOf:   protocolResolver(map[string]string{"anthropic": registry.ProtocolAnthropic, "openai": registry.ProtocolOpenAIResponses}),
	})
	if hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("G13: cross-family web_search replayed; want the raw block dropped")
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive web_search dropping")
	}
}

func TestExpandHistory_WebSearch_OpenAISameFamilyReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("openai", "gpt-5.4")}
	out := expandHistory(turns, replayScope{
		Instance: "openai", Model: "gpt-5.6", Protocol: registry.ProtocolOpenAIResponses,
		InFlightFrom: len(turns),
		protocolOf:   protocolResolver(map[string]string{"openai": registry.ProtocolOpenAIResponses}),
	})
	if !hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("openai↔openai web_search must replay verbatim across models")
	}
}

// --- tool_call / tool_result ids: never provenance-restricted ---

func TestExpandHistory_ToolIdsReplayVerbatim_AcrossModelChange(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{
		{
			Kind: schema.TurnAssistant,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "t", Signature: "sig"}},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-123", Name: "read"}},
			}},
			ResponseProvider: "anthropic", ResponseRequestModel: "claude-opus-4-6",
		},
		{
			Kind: schema.TurnToolResults,
			Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-123", Name: "read", Content: "ok"}},
			}},
			ResponseProvider: "anthropic", ResponseRequestModel: "claude-opus-4-6",
		},
	}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-sonnet-4-5", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("thinking should be stripped on the model change")
	}
	var sawCall, sawResult bool
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolCall && p.ToolCall.ID == "call-123" {
				sawCall = true
			}
			if p.Kind == llm.ContentToolResult && p.ToolResult.ToolCallID == "call-123" {
				sawResult = true
			}
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("tool ids must replay verbatim across a model change: call=%v result=%v", sawCall, sawResult)
	}
}

// --- in-flight-turn exemption (fallback rounds keep today's semantics) ---

func TestExpandHistory_InFlightTurn_NotFiltered(t *testing.T) {
	t.Parallel()
	// A prior completed turn (index 0) on model A, then the in-flight turn's
	// same-family fallback round (index 1) on model B. Target is model A: the
	// prior turn's thinking is stripped, the in-flight fallback round's is kept.
	turns := []schema.Turn{
		assistantThinkingTurn("anthropic", "claude-sonnet-4-5", "claude-sonnet-4-5"),
		assistantThinkingTurn("anthropic", "claude-haiku-4-5", "claude-haiku-4-5"),
	}
	out := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: 1, protocolOf: replayProtocolOf,
	})
	var thinkingCount int
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind == llm.ContentThinking {
				thinkingCount++
			}
		}
	}
	if thinkingCount != 1 {
		t.Fatalf("want exactly the in-flight round's thinking kept (1), got %d", thinkingCount)
	}
}

// TestExpandHistory_StoredTranscriptUntouched confirms the rules govern the
// outgoing projection only: the input turns are not mutated, and switching back
// to the producing model restores full replay.
func TestExpandHistory_StoredTranscriptUntouched(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}

	stripped := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-sonnet-4-5", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if hasContentKind(stripped, llm.ContentThinking) {
		t.Fatal("thinking should be stripped for the switched-to model")
	}
	// The stored turn's content is unchanged (still carries thinking).
	if len(turns[0].Message.Content) != 2 || turns[0].Message.Content[0].Kind != llm.ContentThinking {
		t.Fatalf("stored transcript turn was mutated by projection: %+v", turns[0].Message.Content)
	}
	// Switching back restores full replay.
	back := expandHistory(turns, replayScope{
		Instance: "anthropic", Model: "claude-opus-4-6", Protocol: registry.ProtocolAnthropic,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(back, llm.ContentThinking) {
		t.Fatal("switching back to the producing model must restore thinking replay")
	}
}

// wireNativePlain is the one reasoning item a fallback sends.
const wireNativePlain = `[{"content":[{"text":"plain","type":"reasoning_text"}],"summary":[],"type":"reasoning"}]`

func reasoningNative(text string) []llm.ResponsesReasoningPart {
	return []llm.ResponsesReasoningPart{{Type: "reasoning_text", Text: text}}
}

// responsesWire replays turns to the Responses instance and returns the reasoning
// items the endpoint receives, as JSON, with the projected messages, so a part
// the projection keeps and reasoningItem drops fails the test.
func responsesWire(t *testing.T, turns []schema.Turn, instance string, from int) (string, []llm.Message) {
	t.Helper()
	out := expandHistory(turns, replayScope{Instance: instance, Model: "gpt-5.4",
		Protocol: registry.ProtocolOpenAIResponses, InFlightFrom: from, protocolOf: replayProtocolOf})
	res := registry.Resolved{Instance: "openai", ModelID: "gpt-5.4", WireID: "gpt-5.4", Protocol: replayProtocolOf("openai")}
	msgs := append([]llm.Message{{Role: llm.RoleUser}}, out...)
	body, err := (&responses.Protocol{}).BuildBody(llm.ShapeRequest(llm.Request{Model: "gpt-5.4", Messages: msgs}, res), res)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	var items []any
	for _, it := range body["input"].([]any) {
		if m, ok := it.(map[string]any); ok && m["type"] == "reasoning" {
			items = append(items, m)
		}
	}
	b, _ := json.Marshal(items)
	return string(b), out
}

// TestAnthropicPrimaryToResponsesFallbackKeepsPlaintextWithoutSignature: on a
// fallback to Responses a foreign Responses turn's native plaintext rides the
// wire without its blob, while an Anthropic turn's bare text and signature drop.
func TestAnthropicPrimaryToResponsesFallbackKeepsPlaintextWithoutSignature(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, ResponseProvider: "openai-other", ResponseProtocol: registry.ProtocolOpenAIResponses,
			Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: "enc_other",
					ResponsesContentParts: reasoningNative("plain")}}, {Kind: llm.ContentText, Text: "answer"}}}},
		{Kind: schema.TurnAssistant, ResponseProvider: "anthropic", Message: llm.Message{Role: llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought", Signature: "sig_anth"}}, {Kind: llm.ContentText, Text: "answer"}}}},
	}
	wire, out := responsesWire(t, turns, "openai", len(turns))
	if wire != wireNativePlain {
		t.Fatalf("wire reasoning = %s, want %s: the native plaintext rides, the foreign bare text does not", wire, wireNativePlain)
	}
	if kept := hasContentKind(out, llm.ContentThinking); !kept {
		t.Fatal("the projection must keep the Responses turn's portable plaintext, matching the wire")
	}
}

// TestCrossInstanceResponsesStripsOpaqueBlobKeepsNativePlaintext pins #6891 and
// #6970: a Responses item replayed to another deployment keeps its native
// plaintext and no foreign blob or provider-issued item id, so it cannot 400
// with "Encrypted content is not supported" or name an item the target never
// issued, while the issuer still sends its own id.
func TestCrossInstanceResponsesStripsOpaqueBlobKeepsNativePlaintext(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{{Kind: schema.TurnAssistant, ResponseProvider: "openai-a", ResponseProtocol: registry.ProtocolOpenAIResponses,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{ID: "rs_foreign", EncryptedContent: "enc_A", ResponsesContentParts: reasoningNative("plain")}},
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: "enc_only"}}, {Kind: llm.ContentText, Text: "answer"}}}}}
	wire, out := responsesWire(t, turns, "openai-b", len(turns))
	if wire != wireNativePlain {
		t.Fatalf("wire reasoning = %s, want %s: the plaintext replays verbatim and no blob rides", wire, wireNativePlain)
	}
	if strings.Contains(wire, "rs_foreign") {
		t.Fatalf("the fallback wire names the source instance's reasoning item id: %s", wire)
	}
	if !hasContentKind(out, llm.ContentText) {
		t.Fatal("answer text must survive the strip")
	}
	// The issuer keeps its own blobs, so the strip is bound to provenance.
	issuer, _ := responsesWire(t, turns, "openai-a", len(turns))
	if issuer == wireNativePlain {
		t.Fatalf("the issuer must keep its own blobs, not the bare plaintext: %s", issuer)
	}
	if !strings.Contains(issuer, `"id":"rs_foreign"`) || !strings.Contains(issuer, `"encrypted_content":"enc_A"`) {
		t.Fatalf("the issuer must keep its own item id and blob: %s", issuer)
	}

	// Pressure prices that same wire: the foreign target bills the native
	// plaintext only, while the issuer pays for its own item id and blobs.
	for _, tc := range []struct {
		instance string
		tokens   int
	}{
		{"openai-b", (len("plain") + len("answer")) / 4},
		{"openai-a", (len("rs_foreign") + len("enc_A") + len("enc_only") + len("answer")) / 4},
	} {
		res := registry.Resolved{Instance: tc.instance, ModelID: "gpt-5.4", WireID: "gpt-5.4", Protocol: registry.ProtocolOpenAIResponses}
		cm := contextmgr.NewManager(provider.FromResolved(res, nil), nil, cheapmodel.New(nil))
		if got := cm.EstimateUsage(turns, 0).Used; got != tc.tokens {
			t.Fatalf("pressure for %s = %d tokens, want %d: price what the wire sends", tc.instance, got, tc.tokens)
		}
	}
}

// TestModelFallbacks_RebindsGoogleWireIDProvenance verifies that falling back
// from Google Model A to Google Model B rebinds scope.WireID so Model B keeps
// its own prior thoughts/signatures while dropping Model A's, and same-WireID
// aliases retain signatures.
func TestModelFallbacks_RebindsGoogleWireIDProvenance(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(k string) (string, bool) {
			if k == "GEMINI_API_KEY" {
				return "k-test", true
			}
			return "", false
		}),
		registry.WithInstances(map[string]registry.Provider{
			"google": {
				Models: map[string]registry.Model{
					"alias-b": {AliasOf: "gemini-3.5-flash", WireID: "gemini-3.5-flash"},
				},
			},
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}

	var captured []llm.Request
	adapter := newFakeErrAdapter("google", []func(llm.Request) (llm.Response, error){
		func(req llm.Request) (llm.Response, error) {
			captured = append(captured, req)
			return llm.Response{}, llm.ErrorFromHTTPStatus("google", 404, "primary model missing", nil, nil)
		},
		func(req llm.Request) (llm.Response, error) {
			captured = append(captured, req)
			return agenttest.FinalResponse("ok from fallback"), nil
		},
	})
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(adapter)

	primaryRes, err := r.Resolve("google/gemini-3.8-flash")
	if err != nil {
		t.Fatal(err)
	}
	sess := newSession(t,
		withClient(client),
		withProfile(provider.FromResolved(primaryRes, r)),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"alias-b"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		}),
	)
	// Seed prior turns from Model A (gemini-3.8-flash) and Model B (gemini-3.5-flash).
	sess.history = append(sess.history, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				GoogleInteraction: true, GoogleInstance: "google", GoogleModel: "gemini-3.8-flash",
				Text: "thought-A", Summary: []string{"thought-A"}, Signature: "sig-A",
			}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				GoogleInteraction: true, GoogleInstance: "google", GoogleModel: "gemini-3.8-flash",
				ID: "call_A", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*"}`), ThoughtSignature: "tc-sig-A",
			}},
		}},
		ResponseProvider: "google", ResponseProtocol: registry.ProtocolGoogle, ResponseModel: "gemini-3.8-flash",
	})
	sess.appendTurn(schema.TurnToolResults, llm.ToolResult("call_A", "ok", false))
	sess.history = append(sess.history, schema.Turn{
		Kind: schema.TurnAssistant,
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				GoogleInteraction: true, GoogleInstance: "google", GoogleModel: "gemini-3.5-flash",
				Text: "thought-B", Summary: []string{"thought-B"}, Signature: "sig-B",
			}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				GoogleInteraction: true, GoogleInstance: "google", GoogleModel: "gemini-3.5-flash",
				ID: "call_B", Name: "glob", Arguments: json.RawMessage(`{"pattern":"*"}`), ThoughtSignature: "tc-sig-B",
			}},
		}},
		ResponseProvider: "google", ResponseProtocol: registry.ProtocolGoogle, ResponseModel: "gemini-3.5-flash",
	})
	sess.appendTurn(schema.TurnToolResults, llm.ToolResult("call_B", "ok", false))

	if _, err := sess.ProcessInput(context.Background(), "go", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("captured = %d, want 2 (primary + fallback)", len(captured))
	}
	// Primary (gemini-3.8-flash) keeps thought-A and drops thought-B.
	if !hasSignedThinking(captured[0].Messages, "thought-A", "sig-A") || hasSignedThinking(captured[0].Messages, "thought-B", "sig-B") {
		t.Fatalf("primary messages did not isolate (google, gemini-3.8-flash): %+v", captured[0].Messages)
	}
	// Fallback (alias-b -> WireID gemini-3.5-flash) keeps thought-B and drops thought-A, and BuildBody emits only tc-sig-B.
	if hasSignedThinking(captured[1].Messages, "thought-A", "sig-A") || !hasSignedThinking(captured[1].Messages, "thought-B", "sig-B") {
		t.Fatalf("fallback messages did not rebind WireID to gemini-3.5-flash: %+v", captured[1].Messages)
	}
	fbRes, err := r.Resolve("google/alias-b")
	if err != nil {
		t.Fatal(err)
	}
	wireBody, err := (&google.Protocol{}).BuildBody(llm.ShapeRequest(captured[1], fbRes), fbRes)
	if err != nil {
		t.Fatalf("BuildBody(fallback): %v", err)
	}
	steps := wireBody["input"].([]any)
	var sawThoughtB, sawSigB, sawSigA bool
	for _, rawStep := range steps {
		s, _ := rawStep.(map[string]any)
		if s["type"] == "thought" && s["signature"] == "sig-B" {
			sawThoughtB = true
		}
		if s["type"] == "function_call" && s["signature"] == "tc-sig-B" {
			sawSigB = true
		}
		if s["type"] == "function_call" && s["signature"] == "tc-sig-A" {
			sawSigA = true
		}
	}
	if !sawThoughtB || !sawSigB || sawSigA {
		t.Fatalf("fallback wire steps provenance mismatch: thoughtB=%v sigB=%v sigA=%v", sawThoughtB, sawSigB, sawSigA)
	}
}

// TestVertexSupportsGeminiModel_SwitchAndStartupRejections verifies that
// resolveModelSwitchTarget, validateModelSwitchMembership, and
// resolveStartupModelProfile reject unsupported Vertex Gemini models locally
// before live discovery or committing.
func TestVertexSupportsGeminiModel_SwitchAndStartupRejections(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(k string) (string, bool) {
			switch k {
			case "GOOGLE_VERTEX_PROJECT":
				return "p", true
			case "GOOGLE_VERTEX_LOCATION":
				return "global", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	for _, unsupported := range []string{"gemini-2.5-flash", "gemini-3-flash-preview", "gemini-3-pro-preview"} {
		res, err := r.Resolve("google-vertex/" + unsupported)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", unsupported, err)
		}
		prof := provider.FromResolved(res, r)
		if _, _, _, err := resolveModelSwitchTarget(client, prof, "", llm.DefaultAdapterTimeout(), "", nil); err == nil {
			t.Errorf("resolveModelSwitchTarget(%s) = nil, want rejection", unsupported)
		}
		if err := validateModelSwitchMembership(client, prof, llm.ModelListing{Usable: false}); err == nil {
			t.Errorf("validateModelSwitchMembership(%s) = nil, want rejection", unsupported)
		}
		if _, _, err := resolveStartupModelProfile(client, prof, "", llm.DefaultAdapterTimeout(), nil); err == nil {
			t.Errorf("resolveStartupModelProfile(%s) = nil, want rejection", unsupported)
		}
	}
}

// TestRestoreSession_PrunesRetiredCheapModelAndModelFallbacks verifies that
// restoring a pre-cutover session prunes saved ModelFallbacks and saved
// CheapModel targeting retired google-vertex-express or unsupported
// google-vertex/gemini-2.5-* with a warning, while keeping explicit
// RestoreSessionConfig.ModelFallbacks strict.
func TestRestoreSession_PrunesRetiredCheapModelAndModelFallbacks(t *testing.T) {
	t.Parallel()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(k string) (string, bool) {
			switch k {
			case "GEMINI_API_KEY":
				return "k-gem", true
			case "GOOGLE_VERTEX_PROJECT":
				return "p", true
			case "GOOGLE_VERTEX_LOCATION":
				return "global", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(&fakeAdapter{name: "google"})

	gRes, err := r.Resolve("google/gemini-3.8-flash")
	if err != nil {
		t.Fatal(err)
	}
	prof := provider.FromResolved(gRes, r)

	stateDir := t.TempDir()
	meta := schema.SessionMeta{
		ID:         "0123456789ABCDEFGHJKMNPQRS",
		CreatedAt:  time.Now(),
		Model:      "gemini-3.8-flash",
		ProfileID:  "google",
		CheapModel: "google-vertex-express/gemini-2.5-flash-lite",
		Config: schema.ConfigSnapshot{
			ModelFallbacks: []string{"google-vertex-express/gemini-3.8-flash", "google-vertex/gemini-2.5-flash", "google-vertex/gemini-3.8-flash"},
		},
	}
	sess, err := RestoreSessionFromMetaWithConfig(client, prof, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir: stateDir,
		ResolveProfile: func(ref string) (*provider.Profile, error) {
			return provider.Resolve(r, ref)
		},
	})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}

	if got := sess.profile.ConfiguredCheapModel(); got != "" {
		t.Fatalf("saved retired CheapModel was not pruned: %q", got)
	}
	if got := sess.profile.CheapModel(); got != "gemini-3.5-flash-lite" {
		t.Fatalf("CheapModel fallback = %q, want gemini-3.5-flash-lite", got)
	}
	if len(sess.cfg.ModelFallbacks) != 1 || sess.cfg.ModelFallbacks[0] != "google-vertex/gemini-3.8-flash" {
		t.Fatalf("saved ModelFallbacks = %v, want [google-vertex/gemini-3.8-flash]", sess.cfg.ModelFallbacks)
	}
	sess.Close()
	var warnings []string
	for ev := range sess.Events() {
		if d, ok := ev.Data.(events.WarningData); ok {
			warnings = append(warnings, d.Message)
		}
	}
	if len(warnings) < 2 {
		t.Fatalf("expected warning diagnostics for pruned CheapModel and ModelFallbacks, got %v", warnings)
	}

	// Explicit CLI --model-fallback stays strict and fails restore.
	if _, err := RestoreSessionFromMetaWithConfig(client, prof, execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{
		StateDir:       t.TempDir(),
		ModelFallbacks: []string{"google-vertex/gemini-2.5-flash"},
		ResolveProfile: func(ref string) (*provider.Profile, error) {
			return provider.Resolve(r, ref)
		},
	}); err == nil {
		t.Fatal("expected explicit ModelFallbacks override targeting unsupported Vertex 2.5 model to fail restore")
	}

	t.Run("rejects unsupported Vertex primary model on restore without overwriting saved meta and allows supported override", func(t *testing.T) {
		dir := t.TempDir()
		savedMeta := schema.SessionMeta{
			ID:        "0123456789ABCDEFGHJKMNPQRW",
			CreatedAt: time.Now(),
			Model:     "gemini-3.8-flash",
			ProfileID: "google",
		}
		if err := schema.SaveSessionMeta(dir, savedMeta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		v25Res, err := r.Resolve("google-vertex/gemini-2.5-flash")
		if err != nil {
			t.Fatal(err)
		}
		profV25 := provider.FromResolved(v25Res, r)
		if _, err := RestoreSessionFromMetaWithConfig(client, profV25, execenv.NewLocalExecutionEnvironment(t.TempDir()), savedMeta, RestoreSessionConfig{
			StateDir: dir,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		}); err == nil {
			t.Fatal("expected RestoreSessionFromMetaWithConfig with unsupported google-vertex/gemini-2.5-flash to fail")
		}
		reloaded, err := schema.LoadSessionMeta(dir, savedMeta.ID)
		if err != nil {
			t.Fatalf("LoadSessionMeta after rejected restore: %v", err)
		}
		if reloaded.ProfileID != "google" || reloaded.Model != "gemini-3.8-flash" {
			t.Fatalf("rejected restore overwrote saved meta: got %s/%s, want google/gemini-3.8-flash", reloaded.ProfileID, reloaded.Model)
		}

		v38Res, err := r.Resolve("google-vertex/gemini-3.8-flash")
		if err != nil {
			t.Fatal(err)
		}
		profV38 := provider.FromResolved(v38Res, r)
		sessOK, err := RestoreSessionFromMetaWithConfig(client, profV38, execenv.NewLocalExecutionEnvironment(t.TempDir()), savedMeta, RestoreSessionConfig{
			StateDir: dir,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig with supported google-vertex/gemini-3.8-flash failed: %v", err)
		}
		sessOK.Close()
	})

	t.Run("registered adapter override under google-vertex bypasses Interactions model check across startup, switch, and fallbacks", func(t *testing.T) {
		cOverride := llm.NewClient(llm.WithRegistry(r))
		cOverride.Register(&fakeAdapter{name: "google"})
		cOverride.Register(&fakeAdapter{name: "google-vertex"})

		v25Res, err := r.Resolve("google-vertex/gemini-2.5-flash")
		if err != nil {
			t.Fatal(err)
		}
		profV25 := provider.FromResolved(v25Res, r)

		fresh, err := NewSession(cOverride, profV25, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
			StateDir:       t.TempDir(),
			ModelFallbacks: []string{"google-vertex/gemini-2.5-flash"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		})
		if err != nil {
			t.Fatalf("NewSession with registered google-vertex override failed: %v", err)
		}
		fresh.Close()

		sw, err := NewSession(cOverride, prof, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
			StateDir: t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		})
		if err != nil {
			t.Fatalf("NewSession for SetModel: %v", err)
		}
		if err := sw.SetModel("google-vertex/gemini-2.5-flash"); err != nil {
			t.Fatalf("SetModel to google-vertex/gemini-2.5-flash with registered override failed: %v", err)
		}
		sw.Close()

		metaOv := schema.SessionMeta{
			ID:         "0123456789ABCDEFGHJKMNPQRX",
			CreatedAt:  time.Now(),
			Model:      "gemini-2.5-flash",
			ProfileID:  "google-vertex",
			CheapModel: "google-vertex/gemini-2.5-flash-lite",
			Config: schema.ConfigSnapshot{
				ModelFallbacks: []string{"google-vertex/gemini-2.5-flash"},
			},
		}
		restoredOv, err := RestoreSessionFromMetaWithConfig(cOverride, profV25, execenv.NewLocalExecutionEnvironment(t.TempDir()), metaOv, RestoreSessionConfig{
			StateDir: t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig with registered override failed: %v", err)
		}
		if got := restoredOv.profile.CheapModelRefString(); got != "google-vertex/gemini-2.5-flash-lite" {
			t.Fatalf("overridden CheapModel = %q, want google-vertex/gemini-2.5-flash-lite", got)
		}
		if len(restoredOv.cfg.ModelFallbacks) != 1 || restoredOv.cfg.ModelFallbacks[0] != "google-vertex/gemini-2.5-flash" {
			t.Fatalf("overridden ModelFallbacks = %v, want [google-vertex/gemini-2.5-flash]", restoredOv.cfg.ModelFallbacks)
		}
		restoredOv.Close()
	})

	t.Run("preserves temporarily disabled saved CheapModel and ModelFallbacks while keeping explicit CLI strict", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "providers.toml")
		tomlCfg := `
[providers."google-vertex".models."gemini-3.5-flash-lite"]
disabled = true

[providers."google-vertex".models."gemini-3.8-flash"]
disabled = true
`
		if err := os.WriteFile(cfgPath, []byte(tomlCfg), 0o600); err != nil {
			t.Fatalf("WriteFile providers.toml: %v", err)
		}
		rDis, err := registry.Load(
			registry.WithConfigPath(cfgPath),
			registry.WithOffline(true), registry.WithoutCache(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(k string) (string, bool) {
				switch k {
				case "GEMINI_API_KEY":
					return "k-gem", true
				case "GOOGLE_VERTEX_PROJECT":
					return "p", true
				case "GOOGLE_VERTEX_LOCATION":
					return "global", true
				}
				return "", false
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cDis := llm.NewClient(llm.WithRegistry(rDis))
		cDis.Register(&fakeAdapter{name: "google"})

		gResDis, err := rDis.Resolve("google/gemini-3.8-flash")
		if err != nil {
			t.Fatal(err)
		}
		profDis := provider.FromResolved(gResDis, rDis)
		stateDirDis := t.TempDir()
		metaDis := schema.SessionMeta{
			ID:         "0123456789ABCDEFGHJKMNPQRT",
			CreatedAt:  time.Now(),
			Model:      "gemini-3.8-flash",
			ProfileID:  "google",
			CheapModel: "google-vertex/gemini-3.5-flash-lite",
			Config: schema.ConfigSnapshot{
				ModelFallbacks: []string{"google-vertex-express/gemini-3.8-flash", "google-vertex/gemini-2.5-flash", "google-vertex/gemini-3.8-flash"},
			},
		}
		if err := schema.SaveSessionMeta(stateDirDis, metaDis); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		sessDis, err := RestoreSessionFromMetaWithConfig(cDis, profDis, execenv.NewLocalExecutionEnvironment(t.TempDir()), metaDis, RestoreSessionConfig{
			StateDir: stateDirDis,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(rDis, ref)
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig with disabled saved entries: %v", err)
		}
		if got := sessDis.profile.CheapModelRefString(); got != "google-vertex/gemini-3.5-flash-lite" {
			t.Fatalf("disabled saved CheapModel dropped: got %q, want google-vertex/gemini-3.5-flash-lite", got)
		}
		if len(sessDis.cfg.ModelFallbacks) != 1 || sessDis.cfg.ModelFallbacks[0] != "google-vertex/gemini-3.8-flash" {
			t.Fatalf("disabled saved ModelFallbacks = %v, want [google-vertex/gemini-3.8-flash]", sessDis.cfg.ModelFallbacks)
		}
		if err := sessDis.saveMeta(); err != nil {
			t.Fatalf("saveMeta: %v", err)
		}
		reloaded, err := schema.LoadSessionMeta(stateDirDis, metaDis.ID)
		if err != nil {
			t.Fatalf("LoadSessionMeta: %v", err)
		}
		if reloaded.CheapModel != "google-vertex/gemini-3.5-flash-lite" {
			t.Fatalf("on-disk CheapModel = %q, want google-vertex/gemini-3.5-flash-lite", reloaded.CheapModel)
		}
		if len(reloaded.Config.ModelFallbacks) != 1 || reloaded.Config.ModelFallbacks[0] != "google-vertex/gemini-3.8-flash" {
			t.Fatalf("on-disk ModelFallbacks = %v, want [google-vertex/gemini-3.8-flash]", reloaded.Config.ModelFallbacks)
		}
		sessDis.Close()
		var disWarnings []string
		for ev := range sessDis.Events() {
			if d, ok := ev.Data.(events.WarningData); ok {
				disWarnings = append(disWarnings, d.Message)
			}
		}
		if len(disWarnings) != 2 {
			t.Fatalf("expected 2 pruning warnings (express + 2.5) and zero for disabled models, got %v", disWarnings)
		}

		// Explicit CLI --model-fallback stays strict on disabled models.
		if _, err := RestoreSessionFromMetaWithConfig(cDis, profDis, execenv.NewLocalExecutionEnvironment(t.TempDir()), metaDis, RestoreSessionConfig{
			StateDir:       t.TempDir(),
			ModelFallbacks: []string{"google-vertex/gemini-3.8-flash"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(rDis, ref)
			},
		}); !errors.Is(err, registry.ErrModelDisabled) {
			t.Fatalf("expected explicit ModelFallbacks targeting disabled model to fail with ErrModelDisabled, got %v", err)
		}
	})

	t.Run("preserves saved CheapModel and ModelFallbacks when custom google-vertex-express replacement instance is configured", func(t *testing.T) {
		dir := t.TempDir()
		credMarker := filepath.Join(dir, "cred-ran")
		cfgPath := filepath.Join(dir, "providers.toml")
		tomlCfg := fmt.Sprintf(
			"[providers.\"google-vertex-express\"]\nbase = \"google\"\napi_key = '''$(sh -c 'printf x >> %q; printf k-repl')'''\n",
			credMarker,
		)
		if err := os.WriteFile(cfgPath, []byte(tomlCfg), 0o600); err != nil {
			t.Fatalf("WriteFile providers.toml: %v", err)
		}
		rRepl, err := registry.Load(
			registry.WithConfigPath(cfgPath),
			registry.WithOffline(true), registry.WithoutCache(),
			registry.WithStateRoot(t.TempDir()),
			registry.WithEnv(func(k string) (string, bool) {
				if k == "GEMINI_API_KEY" {
					return "k-gem", true
				}
				return "", false
			}),
		)
		if err != nil {
			t.Fatalf("registry.Load: %v", err)
		}
		cRepl := llm.NewClient(llm.WithRegistry(rRepl))
		cRepl.Register(&fakeAdapter{name: "google"})
		cRepl.Register(&fakeAdapter{name: "google-vertex-express"})

		gResRepl, err := rRepl.Resolve("google/gemini-3.8-flash")
		if err != nil {
			t.Fatal(err)
		}
		profRepl := provider.FromResolved(gResRepl, rRepl)

		// Check CheapModel migration alone is credential-free via HasInstance + ResolveInstanceModelFacts.
		metaCheapOnly := schema.SessionMeta{
			ID:         "0123456789ABCDEFGHJKMNPQRU",
			CreatedAt:  time.Now(),
			Model:      "gemini-3.8-flash",
			ProfileID:  "google",
			CheapModel: "google-vertex-express/gemini-3.5-flash-lite",
		}
		sessCheap, err := RestoreSessionFromMetaWithConfig(cRepl, profRepl, execenv.NewLocalExecutionEnvironment(t.TempDir()), metaCheapOnly, RestoreSessionConfig{
			StateDir: t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(rRepl, ref)
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig (cheap only): %v", err)
		}
		if got := sessCheap.profile.CheapModelRefString(); got != "google-vertex-express/gemini-3.5-flash-lite" {
			t.Fatalf("configured replacement CheapModel pruned: got %q, want google-vertex-express/gemini-3.5-flash-lite", got)
		}
		if _, statErr := os.Stat(credMarker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("CheapModel migration check executed command credential unexpectedly: %v", statErr)
		}
		sessCheap.Close()

		// Also verify saved ModelFallbacks pointing to the configured replacement instance survive.
		metaBoth := schema.SessionMeta{
			ID:         "0123456789ABCDEFGHJKMNPQRV",
			CreatedAt:  time.Now(),
			Model:      "gemini-3.8-flash",
			ProfileID:  "google",
			CheapModel: "google-vertex-express/gemini-3.5-flash-lite",
			Config: schema.ConfigSnapshot{
				ModelFallbacks: []string{"google-vertex-express/gemini-3.8-flash"},
			},
		}
		sessBoth, err := RestoreSessionFromMetaWithConfig(cRepl, profRepl, execenv.NewLocalExecutionEnvironment(t.TempDir()), metaBoth, RestoreSessionConfig{
			StateDir: t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(rRepl, ref)
			},
		})
		if err != nil {
			t.Fatalf("RestoreSessionFromMetaWithConfig (both): %v", err)
		}
		if got := sessBoth.profile.CheapModelRefString(); got != "google-vertex-express/gemini-3.5-flash-lite" {
			t.Fatalf("configured replacement CheapModel = %q, want google-vertex-express/gemini-3.5-flash-lite", got)
		}
		if len(sessBoth.cfg.ModelFallbacks) != 1 || sessBoth.cfg.ModelFallbacks[0] != "google-vertex-express/gemini-3.8-flash" {
			t.Fatalf("configured replacement ModelFallbacks = %v, want [google-vertex-express/gemini-3.8-flash]", sessBoth.cfg.ModelFallbacks)
		}
		sessBoth.Close()
	})
}
