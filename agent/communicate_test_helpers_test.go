package agent

import (
	"encoding/json"
	"strings"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output":   output,
	})
	return llm.Response{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "communicate_test_call",
				Name:      "communicate",
				Arguments: args,
				Type:      "function",
			},
		}},
	}}
}

func communicateWithDefaultOutput(message string) llm.Response {
	return communicateWithStructured(message, map[string]any{
		"message":   message,
		"data":      map[string]any{},
		"artifacts": []string{},
	})
}

func requestContainsText(req llm.Request, want string) bool {
	for _, message := range req.Messages {
		if strings.Contains(message.Text(), want) {
			return true
		}
	}
	return false
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
			Arguments: agenttest.CommunicateResponse(true, text).ToolCalls()[0].Arguments,
			Type:      "function",
		},
	})
	return wrapped
}
