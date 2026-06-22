package agent

import (
	"encoding/json"
	"strings"

	"primeradiant.com/serf/llm"
)

func communicateResponse(endTurn bool, message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": endTurn,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
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
	return communicateResponse(true, message)
}

func wrapCommunicateResponse(resp llm.Response) llm.Response {
	text := strings.TrimSpace(resp.Text())
	if text == "" || len(resp.ToolCalls()) > 0 {
		return resp
	}

	wrapped := resp
	wrapped.Message = resp.Message
	wrapped.Message.Content = append(append([]llm.ContentPart{}, resp.Message.Content...), llm.ContentPart{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        "communicate_test_call",
			Name:      "communicate",
			Arguments: communicateResponse(true, text).ToolCalls()[0].Arguments,
			Type:      "function",
		},
	})
	return wrapped
}
