package agent

import (
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

func communicateResponse(kind, message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"kind":    kind,
		"message": message,
	})
	return llm.Response{
		Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "communicate_test_call",
						Name:      "communicate",
						Arguments: args,
						Type:      "function",
					},
				},
			},
		},
	}
}

func finalResponse(message string) llm.Response {
	return communicateResponse(communicateKindFinal, message)
}

func messageResponse(message string) llm.Response {
	return communicateResponse(communicateKindMessage, message)
}

func askResponse(message string) llm.Response {
	return communicateResponse(communicateKindAsk, message)
}

func wrapCommunicateResponse(resp llm.Response) llm.Response {
	text := strings.TrimSpace(resp.Text())
	if text == "" || len(resp.ToolCalls()) > 0 {
		return resp
	}

	kind := communicateKindFinal
	if looksLikeQuestion(text) {
		kind = communicateKindAsk
	}

	wrapped := communicateResponse(kind, text)
	wrapped = resp
	wrapped.Message = resp.Message
	wrapped.Message.Content = append(append([]llm.ContentPart{}, resp.Message.Content...), llm.ContentPart{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "communicate_test_call",
			Name:      "communicate",
			Arguments: communicateResponse(kind, text).ToolCalls()[0].Arguments,
			Type:      "function",
		},
	})
	return wrapped
}
