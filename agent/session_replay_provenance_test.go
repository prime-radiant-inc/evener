package agent

// Tests for Task 6: cross-model transcript replay provenance (spec N4). They
// drive expandHistory directly with a replayScope for each destination behavior
// tag and assert which provider/model-scoped content (thinking, web_search) from
// completed prior turns survives into the outgoing request, and that
// tool_call/tool_result ids and the stored transcript are never touched.

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
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

// tagResolver maps an instance id to a behavior tag for the web_search family
// check.
func tagResolver(m map[string]string) func(string) string {
	return func(id string) string {
		if t, ok := m[id]; ok {
			return t
		}
		return id
	}
}

// --- thinking, anthropic-family targets (exact-model, closes G12) ---

func TestExpandHistory_Anthropic_SameModel_ThinkingReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6-20260101")}
	out := expandHistory(turns, replayScope{
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-sonnet-4-5", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canon,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("canonical ResponseModel fallback should treat dated snapshot as same model")
	}

	mismatch := expandHistory(turns, replayScope{
		Provider: "anthropic", Model: "claude-sonnet-4-5", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canon,
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
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "google", Model: "gemini-3-pro", BehaviorTag: "google",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("google enforces same-provider only; same-provider thinking must replay across models")
	}
}

func TestExpandHistory_Google_DifferentProvider_ThinkingAbsent(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "google", Model: "gemini-3-pro", BehaviorTag: "google",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
	})
	if hasContentKind(out, llm.ContentThinking) {
		t.Fatal("cross-provider thinking replayed into google; want it stripped")
	}
}

// --- thinking, openai Responses + openai-compat targets (builder-guarded) ---

func TestExpandHistory_OpenAIResponses_ThinkingUnfiltered(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "openai", Model: "gpt-5.4", BehaviorTag: "openai",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("openai Responses keeps its own reasoning guard; expansion must not strip thinking")
	}
}

func TestExpandHistory_OpenAICompat_ThinkingUnfiltered(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantThinkingTurn("anthropic", "claude-opus-4-6", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "work", Model: "kimi-k2", BehaviorTag: "openai-compatible",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
	})
	if !hasContentKind(out, llm.ContentThinking) {
		t.Fatal("openai-compat keeps its reasoningReplayField guard; expansion must not strip thinking")
	}
}

// --- web_search family scoping (G13) ---

func TestExpandHistory_WebSearch_SameFamilyReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "anthropic-2", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom:  len(turns),
		behaviorTagOf: tagResolver(map[string]string{"anthropic": "anthropic", "anthropic-2": "anthropic"}),
	})
	if !hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("web_search within the anthropic family must replay verbatim")
	}
}

// TestExpandHistory_WebSearch_AnthropicSiblingTagReplays pins that distinct
// anthropic-wire behavior tags (anthropic and kimi-anthropic) share the
// anthropic family, so an anthropic-produced raw web_search block replays
// verbatim into a kimi-anthropic request — the raw block shape is identical.
func TestExpandHistory_WebSearch_AnthropicSiblingTagReplays(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "kimi", Model: "kimi-for-coding", BehaviorTag: "kimi-anthropic",
		InFlightFrom:  len(turns),
		behaviorTagOf: tagResolver(map[string]string{"anthropic": "anthropic", "kimi": "kimi-anthropic"}),
	})
	if !hasContentKind(out, llm.ContentWebSearch) {
		t.Fatal("anthropic→kimi-anthropic web_search must replay verbatim (same anthropic-wire family)")
	}
}

// TestExpandHistory_WebSearch_CrossFamilyDropped is the G13 case: an
// anthropic-produced raw web_search block must not land in an openai request.
func TestExpandHistory_WebSearch_CrossFamilyDropped(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{assistantWebSearchTurn("anthropic", "claude-opus-4-6")}
	out := expandHistory(turns, replayScope{
		Provider: "openai", Model: "gpt-5.4", BehaviorTag: "openai",
		InFlightFrom:  len(turns),
		behaviorTagOf: tagResolver(map[string]string{"anthropic": "anthropic", "openai": "openai"}),
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
		Provider: "openai", Model: "gpt-5.6", BehaviorTag: "openai",
		InFlightFrom:  len(turns),
		behaviorTagOf: tagResolver(map[string]string{"openai": "openai"}),
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
		Provider: "anthropic", Model: "claude-sonnet-4-5", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: 1, canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-sonnet-4-5", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
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
		Provider: "anthropic", Model: "claude-opus-4-6", BehaviorTag: "anthropic",
		InFlightFrom: len(turns), canonicalModel: canonicalModelID,
	})
	if !hasContentKind(back, llm.ContentThinking) {
		t.Fatal("switching back to the producing model must restore thinking replay")
	}
}
