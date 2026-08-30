package agent

// Tests for Task 6: cross-model transcript replay provenance (spec N4). They
// drive expandHistory directly with a replayScope for each destination wire
// protocol and assert which provider/model-scoped content (thinking,
// web_search) from completed prior turns survives into the outgoing request,
// and that tool_call/tool_result ids and the stored transcript are never
// touched.

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
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
	"work":        registry.ProtocolOpenAIChat,
})

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
// canonicalization: a "[1m]" ref is an alias row, so it folds onto its target,
// and a dated id the catalog does not carry as its own row folds onto the base
// row the registry matched.
//
// Known narrowing: a dated snapshot the catalog DOES carry as its own row
// (anthropic's claude-sonnet-4-5-20250929) canonicalizes to itself, so it no
// longer compares equal to the undated alias the way the deleted
// EmbeddedModelCatalog canonicalizer made it. Legacy transcripts only, and the
// conservative direction — thinking is stripped, never wrongly replayed.
func TestSessionCanonicalModelID(t *testing.T) {
	t.Parallel()
	sess := &Session{client: llm.NewClient()}
	cases := []struct{ model, want string }{
		{"claude-sonnet-4-5[1m]", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5-20990101", "claude-sonnet-4-5"},
		{"  claude-sonnet-4-5[1m]  ", "claude-sonnet-4-5"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"},
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

// --- thinking, google target (same-provider only) ---

func TestExpandHistory_Google_SameProviderDifferentModel_ThinkingReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("google", "gemini-2.5-pro", "gemini-2.5-pro")}
	out := expandHistory(turns, replayScope{
		Instance: "google", Model: "gemini-3-pro", Protocol: registry.ProtocolGoogle,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("google enforces same-provider only; same-provider thinking must replay across models")
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
		t.Fatal("openai cross-provider thinking replayed; want it stripped (encrypted_content is deployment-scoped)")
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

func TestExpandHistory_OpenAICompat_ThinkingUnfiltered(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Instance: "work", Model: "kimi-k2", Protocol: registry.ProtocolOpenAIChat,
		InFlightFrom: len(turns), protocolOf: replayProtocolOf,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("openai-compat keeps its reasoningReplayField guard; expansion must not strip thinking")
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
