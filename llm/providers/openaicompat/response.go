package openaicompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/providers/internal/openaichat"
)

type chatCompletionResponse struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []chatChoice   `json:"choices"`
	Usage   map[string]any `json:"usage"`
}

type chatChoice struct {
	Index        int            `json:"index"`
	Message      chatMessage    `json:"message"`
	FinishReason string         `json:"finish_reason"`
	Usage        map[string]any `json:"usage,omitempty"`
}

type chatMessage struct {
	Role             string                `json:"role"`
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ReasoningText    string                `json:"reasoning_text,omitempty"`
	ReasoningDetails []reasoningDetailItem `json:"reasoning_details,omitempty"`
	ToolCalls        []chatToolCall        `json:"tool_calls,omitempty"`
}

// reasoningDetailItem represents an element in the reasoning_details array
// used by OpenRouter for models like MiniMax M2.7. MiniMax's actual format
// is {type: "reasoning.text", text: "...", format: "...", index: N}.
// Raw keeps the item's verbatim JSON so encrypted items replay unchanged —
// providers may require metadata beyond the fields decoded here.
type reasoningDetailItem struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Index  int    `json:"index,omitempty"`
	// Thinking is kept for backward compatibility with older OpenRouter format
	// variants that used {type: "thinking", thinking: "..."}.
	Thinking string `json:"thinking,omitempty"`
	// Raw is the verbatim wire JSON of this item, captured by UnmarshalJSON.
	Raw json.RawMessage `json:"-"`
	// ID and Data carry the opaque {type: "reasoning.encrypted", id, data}
	// items OpenRouter emits for Gemini/o-series: the reasoning chain is
	// encrypted server-side and must be round-tripped verbatim.
	ID   string `json:"id,omitempty"`
	Data string `json:"data,omitempty"`
}

// UnmarshalJSON decodes the known fields and keeps the item's verbatim JSON
// in Raw for lossless replay of encrypted items.
func (d *reasoningDetailItem) UnmarshalJSON(b []byte) error {
	type plain reasoningDetailItem
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*d = reasoningDetailItem(p)
	d.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// isEncrypted reports whether the item is an opaque encrypted reasoning block
// that must be replayed verbatim to preserve the reasoning chain.
func (d reasoningDetailItem) isEncrypted() bool {
	return d.Type == "reasoning.encrypted" && d.ID != "" && d.Data != ""
}

// encodeEncryptedDetails serializes the encrypted items of a reasoning_details
// array into the JSON string carried on ThinkingData.EncryptedContent, in
// arrival order. Returns "" when there are none.
func encodeEncryptedDetails(items []reasoningDetailItem) string {
	enc := collectEncryptedDetails(items)
	if len(enc) == 0 {
		return ""
	}
	b, err := json.Marshal(enc)
	if err != nil {
		return ""
	}
	return string(b)
}

// collectEncryptedDetails returns the encrypted items as wire maps, preserving
// order AND every field the provider sent (decoded from the item's verbatim
// Raw JSON — replay must be lossless or continuation can break). Used both to
// build EncryptedContent and to accumulate streamed items.
func collectEncryptedDetails(items []reasoningDetailItem) []map[string]any {
	var enc []map[string]any
	for _, d := range items {
		if !d.isEncrypted() {
			continue
		}
		var verbatim map[string]any
		if len(d.Raw) > 0 && json.Unmarshal(d.Raw, &verbatim) == nil {
			enc = append(enc, verbatim)
			continue
		}
		// Raw missing (item built programmatically): fall back to known fields.
		enc = append(enc, map[string]any{"type": d.Type, "id": d.ID, "data": d.Data})
	}
	return enc
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   map[string]any    `json:"usage"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatDelta      `json:"delta"`
	FinishReason string         `json:"finish_reason"`
	Usage        map[string]any `json:"usage,omitempty"`
}

type chatDelta struct {
	Role             string                `json:"role"`
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ReasoningText    string                `json:"reasoning_text,omitempty"`
	ReasoningDetails []reasoningDetailItem `json:"reasoning_details,omitempty"`
	ToolCalls        []chatChunkToolCall   `json:"tool_calls,omitempty"`
}

// reasoningFieldNames are the wire fields OpenAI-compatible providers use for
// reasoning deltas, in llm's canonical parse order. Providers pick one
// (llama.cpp: reasoning_content; OpenRouter/chutes: reasoning; some gateways:
// reasoning_text); chutes.ai duplicates identical content across two, so
// exactly one is read per chunk.
var reasoningFieldNames = llm.OpenAICompatReasoningFields()

// reasoningFromDelta returns the chunk's reasoning text and the field it
// arrived on — the first non-empty of reasoning_content/reasoning/
// reasoning_text, falling back to reasoning_details text items (which replay
// through the reasoning_details path, so they report no field).
func reasoningFromDelta(d chatDelta) (text, field string) {
	for i, v := range [...]string{d.ReasoningContent, d.Reasoning, d.ReasoningText} {
		if v != "" {
			return v, reasoningFieldNames[i]
		}
	}
	if len(d.ReasoningDetails) > 0 {
		var b strings.Builder
		for _, item := range d.ReasoningDetails {
			piece := item.Text
			if piece == "" {
				piece = item.Thinking
			}
			b.WriteString(piece)
		}
		return b.String(), ""
	}
	return "", ""
}

// isReasoningFieldName reports whether a thinking signature names one of the
// known reasoning wire fields (as opposed to a cryptographic signature from
// another provider's transcript).
func isReasoningFieldName(sig string) bool {
	return llm.IsOpenAICompatReasoningField(sig)
}

type chatChunkToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

// extractReasoning returns the reasoning text from a chat message and the wire
// field it arrived on, checking reasoning_details (OpenRouter MiniMax format)
// first, then the reasoning_content/reasoning/reasoning_text field variants.
// MiniMax uses {type: "reasoning.text", text: "..."}; older/alternate formats
// use {type: "thinking", thinking: "..."}. Details report no field — they
// replay through the reasoning_details path.
func extractReasoning(msg chatMessage) (text, field string) {
	if len(msg.ReasoningDetails) > 0 {
		var b strings.Builder
		for _, d := range msg.ReasoningDetails {
			var piece string
			if d.Text != "" {
				piece = d.Text
			} else if d.Thinking != "" {
				piece = d.Thinking
			}
			if piece != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(piece)
			}
		}
		if b.Len() > 0 {
			return b.String(), ""
		}
	}
	for i, v := range [...]string{msg.ReasoningContent, msg.Reasoning, msg.ReasoningText} {
		if v != "" {
			return v, reasoningFieldNames[i]
		}
	}
	return "", ""
}

func fromChatCompletionResponse(raw map[string]any, quirks ProviderQuirks) (llm.Response, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return llm.Response{}, err
	}
	var parsed chatCompletionResponse
	if err := json.Unmarshal(b, &parsed); err != nil {
		return llm.Response{}, fmt.Errorf("failed to parse chat completion response: %w", err)
	}

	if len(parsed.Choices) == 0 {
		return llm.Response{}, errors.New("no choices in response")
	}
	choice := parsed.Choices[0]

	// Build message.
	parts := []llm.ContentPart{}
	reasoning, field := extractReasoning(choice.Message)
	encrypted := encodeEncryptedDetails(choice.Message.ReasoningDetails)
	if reasoning != "" || encrypted != "" {
		parts = append(parts, llm.ContentPart{
			Kind:     llm.ContentThinking,
			Thinking: &llm.ThinkingData{Text: reasoning, Signature: field, EncryptedContent: encrypted},
		})
	}
	if choice.Message.Content != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		args := rescueClaudeXMLArgs(tc.Function.Arguments)
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(args),
				Type:      "function",
			},
		})
	}

	msg := llm.Message{Role: llm.RoleAssistant, Content: parts}

	rawFinish := choice.FinishReason
	mappedFinish := quirks.mapFinishReason(rawFinish)
	var finish llm.FinishReason
	if mappedFinish == rawFinish {
		finish = llm.NormalizeFinishReason("", rawFinish)
	} else {
		finish = llm.FinishReason{Reason: mappedFinish, Raw: rawFinish}
	}

	resp := llm.Response{
		Provider: "openai-compatible",
		Model:    parsed.Model,
		ID:       parsed.ID,
		Message:  msg,
		Finish:   finish,
		Raw:      raw,
	}

	// Usage is normally top-level, but Moonshot/Kimi sometimes report it on
	// choices[0].usage instead. Top-level wins when both are present.
	if parsed.Usage != nil {
		resp.Usage = openaichat.ParseChatUsage(parsed.Usage)
	} else if choice.Usage != nil {
		resp.Usage = openaichat.ParseChatUsage(choice.Usage)
	}

	if resp.Usage.ReasoningTokens == nil && resp.Usage.ReasoningTokensEstimated == nil {
		chars := 0
		for _, p := range parts {
			if p.Kind == llm.ContentThinking && p.Thinking != nil {
				chars += len(p.Thinking.Text)
			}
		}
		if est := estimateThinkingFromBuf(chars); est > 0 {
			e := est
			resp.Usage.ReasoningTokensEstimated = &e
		}
	}

	return resp, nil
}

// estimateThinkingFromBuf returns a char/4 rough estimate from a
// thinking-content character count. Used only for the Usage metadata
// field ReasoningTokensEstimated — never for billing.
func estimateThinkingFromBuf(chars int) int {
	if chars == 0 {
		return 0
	}
	est := chars / 4
	if est < 1 {
		est = 1
	}
	return est
}
