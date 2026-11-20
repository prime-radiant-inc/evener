package contextmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/registry"
)

func projectionParityBody(t *testing.T, proto llm.Protocol, res registry.Resolved, msg llm.Message) []byte {
	t.Helper()
	body, err := proto.BuildBody(llm.Request{Messages: []llm.Message{msg}}, res)
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return raw
}

// projectionParityMessage is request assembly's projection of one turn: the
// estimator's trim plus the cross-provider strip a Responses target applies.
func projectionParityMessage(res registry.Resolved, turn schema.Turn) llm.Message {
	msg := llm.TrimThinkingForReplayMessage(turn.Message, !llm.ReplaysAssistantThinking(res))
	if !foreignReasoningTurn(res, turn) {
		return msg
	}
	kept := make([]llm.ContentPart, 0, len(msg.Content))
	for _, p := range msg.Content {
		if np, drop := llm.StripOpaqueThinkingForCrossProvider(p); !drop {
			kept = append(kept, np)
		}
	}
	msg.Content = kept
	return msg
}

// projectionParityPrices checks the estimator's token delta between two turns
// against the builder's byte delta at four bytes per character, then returns the
// large body. Both turns carry the same ASCII shape, so fixed keys cancel.
func projectionParityPrices(t *testing.T, name string, res registry.Resolved, proto llm.Protocol, large, small schema.Turn, deltaChars int) []byte {
	t.Helper()
	largeBody := projectionParityBody(t, proto, res, projectionParityMessage(res, large))
	smallBody := projectionParityBody(t, proto, res, projectionParityMessage(res, small))
	estDelta := estimateTokenPartsFor(res, []schema.Turn{large}).Total() - estimateTokenPartsFor(res, []schema.Turn{small}).Total()
	if bodyDelta := len(largeBody) - len(smallBody); estDelta != deltaChars/4 || bodyDelta != deltaChars {
		t.Fatalf("%s: estimate delta = %d tokens, body delta = %d bytes, want %d and %d",
			name, estDelta, bodyDelta, deltaChars/4, deltaChars)
	}
	return largeBody
}

func projectionParityResolved(protocol, instance string) registry.Resolved {
	reasoning := true
	return registry.Resolved{Instance: instance, ModelID: "gpt-5.4", WireID: "gpt-5.4",
		Protocol: protocol, Caps: registry.Caps{Reasoning: &reasoning}}
}

// projectionParityTurn is an assistant turn whose only content is one thinking part.
func projectionParityTurn(provider string, thinking llm.ThinkingData) schema.Turn {
	return schema.Turn{Kind: schema.TurnAssistant, ResponseProvider: provider, Message: llm.Message{
		Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &thinking}}}}
}

func projectionParityDetails(text string) []llm.ResponsesReasoningPart {
	return []llm.ResponsesReasoningPart{{Type: "reasoning_text", Text: text}}
}

// TestEstimateTokenPartsMatchesProjectedPayload pins the invariant: history
// pressure must price what the selected builder serializes, retaining one
// reasoning representation per turn rather than summing incompatible ones.
func TestEstimateTokenPartsMatchesProjectedPayload(t *testing.T) {
	t.Parallel()
	const largeChars, smallChars = 4800, 800
	const deltaChars = largeChars - smallChars
	big, little := strings.Repeat("t", largeChars), strings.Repeat("t", smallChars)

	t.Run("chat parity", func(t *testing.T) {
		res := projectionParityResolved(registry.ProtocolOpenAIChat, "openai")
		projectionParityPrices(t, "chat", res, &chatcompletions.Protocol{},
			projectionParityTurn("", llm.ThinkingData{Text: big}),
			projectionParityTurn("", llm.ThinkingData{Text: little}), deltaChars)
	})

	t.Run("responses parity", func(t *testing.T) {
		res := projectionParityResolved(registry.ProtocolOpenAIResponses, "openai")
		projectionParityPrices(t, "responses", res, &responses.Protocol{},
			projectionParityTurn(res.Instance, llm.ThinkingData{ResponsesContentParts: projectionParityDetails(big)}),
			projectionParityTurn(res.Instance, llm.ThinkingData{ResponsesContentParts: projectionParityDetails(little)}), deltaChars)
	})

	t.Run("independent scalar and details", func(t *testing.T) {
		res := projectionParityResolved(registry.ProtocolOpenAIResponses, "openai")
		makeTurn := func(scalar string) schema.Turn {
			return projectionParityTurn(res.Instance, llm.ThinkingData{
				EncryptedContent: scalar, ResponsesContentParts: projectionParityDetails(big)})
		}
		large, small := makeTurn(strings.Repeat("e", largeChars)), makeTurn(strings.Repeat("e", smallChars))
		raw := projectionParityPrices(t, "scalar and details", res, &responses.Protocol{}, large, small, deltaChars)
		if got := estimateTokenPartsFor(res, []schema.Turn{large}).Total(); got != largeChars/4 {
			t.Fatalf("estimate = %d, want the scalar alone %d, not the %d-token sum", got, largeChars/4, 2*largeChars/4)
		}
		if !strings.Contains(string(raw), strings.Repeat("e", largeChars)) || strings.Contains(string(raw), big) {
			t.Fatalf("body should carry the scalar and not the details: %s", raw)
		}
	})

	foreignParity := func(t *testing.T, instance, provider, producerProtocol string) {
		t.Helper()
		res := projectionParityResolved(registry.ProtocolOpenAIResponses, instance)
		blob := strings.Repeat("z", largeChars)
		makeTurn := func(plain string) schema.Turn {
			turn := projectionParityTurn(provider, llm.ThinkingData{
				ID: "rs_reasoning_item", EncryptedContent: blob, ResponsesContentParts: projectionParityDetails(plain)})
			turn.ResponseProtocol = producerProtocol
			return turn
		}
		large, small := makeTurn(big), makeTurn(little)
		raw := projectionParityPrices(t, "foreign", res, &responses.Protocol{}, large, small, deltaChars)
		if got := estimateTokenPartsFor(res, []schema.Turn{large}).Total(); got != largeChars/4 {
			t.Fatalf("opaque blob billed: estimate = %d, want %d plaintext tokens", got, largeChars/4)
		}
		if strings.Contains(string(raw), blob) || !strings.Contains(string(raw), big) {
			t.Fatalf("foreign body must carry the plaintext and not the blob: %s", raw)
		}
		req := llm.Request{Messages: []llm.Message{projectionParityMessage(res, large)}}
		if _, budget, err := llm.ApplyTokenBudgetWithToolBytes(req, res, 0); err != nil || budget.InputTokens != largeChars/4+budget.SafetyTokens {
			t.Fatalf("admission = %+v, %v, want %d plaintext tokens plus the reserve", budget, err, largeChars/4)
		}
	}
	t.Run("foreign turn", func(t *testing.T) { foreignParity(t, "openai", "openai-b", "") })
	t.Run("unresolved recipient", func(t *testing.T) { foreignParity(t, "", "openai-b", "") })
	t.Run("cross-protocol same instance", func(t *testing.T) { foreignParity(t, "openai", "openai", "openai-chat") })

	t.Run("fallback profile", func(t *testing.T) {
		large := projectionParityTurn("", llm.ThinkingData{Text: big})
		small := projectionParityTurn("", llm.ThinkingData{Text: little})
		unset := projectionParityResolved(registry.ProtocolOpenAIChat, "openai")
		unset.Caps.Reasoning = nil
		raw := projectionParityPrices(t, "reasoning unset", unset, &chatcompletions.Protocol{}, large, small, deltaChars)
		if !strings.Contains(string(raw), big) {
			t.Fatalf("nil-reasoning chat body does not replay the text: %s", raw)
		}
		disabled := projectionParityResolved(registry.ProtocolOpenAIChat, "openai")
		disabled.Caps.Reasoning = new(bool)
		raw = projectionParityPrices(t, "reasoning false", disabled, &chatcompletions.Protocol{}, large, small, 0)
		if strings.Contains(string(raw), big) {
			t.Fatalf("reasoning=false chat body carries reasoning: %s", raw)
		}
	})

	t.Run("incremental and full agree", func(t *testing.T) {
		res := projectionParityResolved(registry.ProtocolOpenAIResponses, "openai")
		turns := []schema.Turn{
			projectionParityTurn(res.Instance, llm.ThinkingData{ResponsesContentParts: projectionParityDetails(big)}),
			projectionParityTurn("openai-b", llm.ThinkingData{
				EncryptedContent: strings.Repeat("z", smallChars), ResponsesContentParts: projectionParityDetails(little)}),
			projectionParityTurn(res.Instance, llm.ThinkingData{ResponsesContentParts: projectionParityDetails(little)}),
		}
		mgr := NewManager(testProfile("openai", "gpt-5.2", 1_000_000).WithResolved(res), nil, cheapmodel.New(nil))
		estimator := llm.TokenPartEstimatorFor(res)
		var acc llm.TokenParts
		for i, turn := range turns {
			msg := projectionParityMessage(res, turn)
			if foreignReasoningTurn(res, turn) {
				acc = acc.Add(estimator.PartsForeignReasoning(msg))
			} else {
				acc = acc.Add(estimator.Parts(msg))
			}
			if got, want := acc.Total(), mgr.estimateTokens(turns[:i+1]); got != want {
				t.Fatalf("prefix %d: incremental parts = %d tokens, full estimate = %d", i+1, got, want)
			}
		}
	})
}
