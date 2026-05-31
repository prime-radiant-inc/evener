package openaicompat

import (
	"encoding/json"
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
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role             string                `json:"role"`
	Content          string                `json:"content"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	ReasoningDetails []reasoningDetailItem `json:"reasoning_details,omitempty"`
	ToolCalls        []chatToolCall        `json:"tool_calls,omitempty"`
}

// reasoningDetailItem represents an element in the reasoning_details array
// used by OpenRouter for models like MiniMax M2.7. MiniMax's actual format
// is {type: "reasoning.text", text: "...", format: "...", index: N}.
// We preserve unknown fields via the Extra map so round-tripping the message
// back to the model keeps the reasoning chain intact.
type reasoningDetailItem struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Format string `json:"format,omitempty"`
	Index  int    `json:"index,omitempty"`
	// Thinking is kept for backward compatibility with older OpenRouter format
	// variants that used {type: "thinking", thinking: "..."}.
	Thinking string `json:"thinking,omitempty"`
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
	Index        int       `json:"index"`
	Delta        chatDelta `json:"delta"`
	FinishReason string    `json:"finish_reason"`
}

type chatDelta struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCalls        []chatChunkToolCall `json:"tool_calls,omitempty"`
}

type chatChunkToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id,omitempty"`
	Type     string           `json:"type,omitempty"`
	Function chatFunctionCall `json:"function"`
}

// extractReasoning returns the reasoning text from a chat message, checking
// reasoning_details (OpenRouter MiniMax format) first, then reasoning_content.
// MiniMax uses {type: "reasoning.text", text: "..."}; older/alternate formats
// use {type: "thinking", thinking: "..."}.
func extractReasoning(msg chatMessage) string {
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
			return b.String()
		}
	}
	return msg.ReasoningContent
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
		return llm.Response{}, fmt.Errorf("no choices in response")
	}
	choice := parsed.Choices[0]

	// Build message.
	parts := []llm.ContentPart{}
	if reasoning := extractReasoning(choice.Message); reasoning != "" {
		parts = append(parts, llm.ContentPart{
			Kind:     llm.ContentThinking,
			Thinking: &llm.ThinkingData{Text: reasoning},
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

	if parsed.Usage != nil {
		resp.Usage = openaichat.ParseChatUsage(parsed.Usage)
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
